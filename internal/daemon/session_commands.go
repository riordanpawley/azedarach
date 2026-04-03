package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type sessionCommandBody struct {
	ProjectID  string   `json:"project_id"`
	SessionID  string   `json:"session_id"`
	BaseBranch string   `json:"base_branch,omitempty"`
	Yolo       bool     `json:"yolo,omitempty"`
	ImagePaths []string `json:"image_paths,omitempty"`
	Prompt     string   `json:"initial_prompt,omitempty"`
}

type resolvedSessionTarget struct {
	ProjectID  string
	IssueID    string
	SessionID  string
	BaseBranch string
	Yolo       bool
	ImagePaths []string
	Prompt     string
}

type sessionRecoveryResult struct {
	RecreatedTmuxSessions int `json:"recreated_tmux_sessions"`
	AlignedDaemonSessions int `json:"aligned_daemon_sessions"`
}

type SessionLongRunningExecutor interface {
	Execute(ctx context.Context, req protocol.RequestEnvelope, command string, exec func(context.Context) (protocol.ResponseEnvelope, error)) (protocol.ResponseEnvelope, error)
}

func sessionKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sessionProjectionIssueID(session daemonstate.Session, namingScope string) string {
	issueID := strings.TrimSpace(session.IssueID)
	if issueID != "" {
		return issueID
	}
	if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(session.ID, namingScope); ok {
		return parsedIssueID
	}
	return strings.TrimSpace(session.ID)
}

func sessionProjectionByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	byIssueKey := make(map[string]daemonstate.Session, len(sessions))
	for _, session := range sessions {
		key := sessionKey(sessionProjectionIssueID(session, namingScope))
		if key == "" {
			continue
		}
		byIssueKey[key] = session
	}
	return byIssueKey
}

func sessionStopPendingKey(projectID, issueID string) string {
	project := protocol.NormalizeProjectID(projectID)
	issueKey := sessionKey(issueID)
	if issueKey == "" {
		return ""
	}
	return strings.ToLower(project) + ":" + issueKey
}

func (d *Daemon) markSessionStopPending(projectID, issueID string) func() {
	key := sessionStopPendingKey(projectID, issueID)
	if key == "" {
		return func() {}
	}

	d.sessionStopMu.Lock()
	if d.sessionStopPending == nil {
		d.sessionStopPending = map[string]int{}
	}
	d.sessionStopPending[key]++
	d.sessionStopMu.Unlock()

	return func() {
		d.sessionStopMu.Lock()
		defer d.sessionStopMu.Unlock()
		if d.sessionStopPending == nil {
			return
		}
		next := d.sessionStopPending[key] - 1
		if next <= 0 {
			delete(d.sessionStopPending, key)
			return
		}
		d.sessionStopPending[key] = next
	}
}

func (d *Daemon) isSessionStopPending(projectID, issueID string) bool {
	key := sessionStopPendingKey(projectID, issueID)
	if key == "" {
		return false
	}

	d.sessionStopMu.Lock()
	defer d.sessionStopMu.Unlock()
	return d.sessionStopPending[key] > 0
}

func (d *Daemon) sessionNamingScope(projectID string) string {
	trimmed := protocol.TrimProjectID(projectID)
	if trimmed == "" || trimmed == protocol.DefaultProjectID {
		return d.cfg.RepoDir
	}
	return trimmed
}

func (d *Daemon) decodeSessionRequest(req protocol.RequestEnvelope, requireSession bool) (resolvedSessionTarget, protocol.ResponseEnvelope, bool) {
	var cmd sessionCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), false
	}
	cmd.ProjectID = protocol.TrimProjectID(cmd.ProjectID)
	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID
	}
	cmd.ProjectID = protocol.NormalizeProjectID(cmd.ProjectID)
	if requireSession && cmd.SessionID == "" {
		return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/session_id"), false
	}

	issueID := strings.TrimSpace(cmd.SessionID)
	namingScope := d.sessionNamingScope(cmd.ProjectID)
	if issueID != "" {
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(issueID, namingScope); ok {
			issueID = parsedIssueID
		}
	}
	sessionID := ""
	if issueID != "" {
		sessionID = naming.CanonicalSessionID(namingScope, issueID)
	}
	return resolvedSessionTarget{
		ProjectID:  cmd.ProjectID,
		IssueID:    issueID,
		SessionID:  sessionID,
		BaseBranch: cmd.BaseBranch,
		Yolo:       cmd.Yolo,
		ImagePaths: cmd.ImagePaths,
		Prompt:     cmd.Prompt,
	}, protocol.ResponseEnvelope{}, true
}

func (d *Daemon) handleSessionStart(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if d.sessionLongRunning != nil {
		return d.sessionLongRunning.Execute(ctx, req, req.Command, func(execCtx context.Context) (protocol.ResponseEnvelope, error) {
			return d.handleSessionStartDirect(execCtx, req)
		})
	}
	return d.handleSessionStartDirect(ctx, req)
}

func (d *Daemon) handleSessionStartDirect(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session start requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"base_branch", cmd.BaseBranch,
			"yolo", cmd.Yolo,
			"image_count", len(cmd.ImagePaths),
		)
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if exists {
		return d.errorResponse(req, protocol.ErrorCodeConflict, fmt.Sprintf("session already exists: %s (use 'az attach %s' to connect)", cmd.IssueID, cmd.IssueID)), nil
	}
	issueClient := d.issueClientForProject(cmd.ProjectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.Search(ctx, cmd.IssueID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	task, ok := resolveSessionIssue(tasks, cmd.IssueID)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("issue not found: %s", cmd.IssueID)), nil
	}
	baseBranch := cmd.BaseBranch
	if baseBranch == "" {
		baseBranch = d.cfg.BaseBranch
	}
	worktree, err := d.worktree.CreateWithTitle(ctx, cmd.IssueID, task.Title, baseBranch)
	reusedWorktree := false
	if err != nil {
		// Recovery path: git worktree add can return non-zero after materializing
		// a usable worktree (for example, hooks that fail post-checkout).
		// If we can load the worktree for the issue, continue by reusing it.
		if recoveredWorktree, recoverErr := d.worktree.Get(ctx, cmd.IssueID); recoverErr == nil {
			worktree = recoveredWorktree
			reusedWorktree = true
		} else {
			if !errors.Is(err, git.ErrWorktreeAlreadyExists) {
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("worktree already exists but could not be loaded: %v", recoverErr)), nil
		}
	}
	if !reusedWorktree {
		if err := d.runWorktreeInitCommands(ctx, worktree.Path); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("worktree init failed: %v", err)), nil
		}
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session start worktree prepared",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"worktree", worktree.Path,
			"branch", worktree.Branch,
			"reused_worktree", reusedWorktree,
		)
	}
	d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, cmd.ProjectID, cmd.IssueID, worktree.Path, worktree.Branch)
	if err := d.tmux.NewSession(ctx, cmd.SessionID, worktree.Path); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	initialPrompt := strings.TrimSpace(cmd.Prompt)
	if initialPrompt == "" {
		initialPrompt = buildStartWorkPrompt(cmd.IssueID, task.Type.String(), task.Title)
	}
	launchCommand := d.buildSessionLaunchCommand(cmd.IssueID, cmd.SessionID, cmd.Yolo, cmd.ImagePaths, initialPrompt)
	if err := d.tmux.SendKeys(ctx, cmd.SessionID, launchCommand); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session start launch command sent",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"prompt_bytes", len(initialPrompt),
		)
	}
	if updateErr := issueClient.Update(ctx, cmd.IssueID, domain.StatusInProgress); updateErr != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("failed to update issue status to in_progress after session start",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"error", updateErr,
		)
	}
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionStart,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session start transition: %v", err)), nil
	}

	worktreeLine := fmt.Sprintf("Worktree created: %s", worktree.Path)
	if reusedWorktree {
		worktreeLine = fmt.Sprintf("Worktree reused: %s", worktree.Path)
	}
	output := strings.Join([]string{
		fmt.Sprintf("Starting session for: %s - %s", task.ID, task.Title),
		fmt.Sprintf("Creating worktree from branch: %s", baseBranch),
		worktreeLine,
		fmt.Sprintf("Creating tmux session: %s", cmd.SessionID),
		"",
		"✓ Session started successfully",
		fmt.Sprintf("  To attach: az attach %s", cmd.IssueID),
		fmt.Sprintf("  Or run:    tmux attach-session -t %s", cmd.SessionID),
		"",
	}, "\n")
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session start completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"worktree", worktree.Path,
			"reused_worktree", reusedWorktree,
		)
	}
	return d.commandOutput(req, output), nil
}

func resolveSessionIssue(tasks []domain.Task, requestedIssueID string) (domain.Task, bool) {
	if len(tasks) == 0 {
		return domain.Task{}, false
	}
	requestedKey := sessionKey(requestedIssueID)
	if requestedKey != "" {
		for _, task := range tasks {
			if sessionKey(task.ID) == requestedKey {
				return task, true
			}
		}
	}
	return tasks[0], true
}

func (d *Daemon) handleSessionAttach(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session attach requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s (use 'az start %s' to create)", cmd.IssueID, cmd.IssueID)), nil
	}
	output := strings.Join([]string{
		fmt.Sprintf("Attaching to session: %s", cmd.SessionID),
		"(Press Ctrl+B then D to detach)",
		"",
	}, "\n")
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionAttach,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session attach transition: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session attach completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionPause(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session pause requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionPause,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session pause transition: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session pause completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	return d.commandOutput(req, fmt.Sprintf("Paused session: %s\n", cmd.IssueID)), nil
}

func (d *Daemon) handleSessionResume(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session resume requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionResume,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session resume transition: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session resume completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	return d.commandOutput(req, fmt.Sprintf("Resumed session: %s\n", cmd.IssueID)), nil
}

func (d *Daemon) handleSessionStop(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if d.sessionLongRunning != nil {
		return d.sessionLongRunning.Execute(ctx, req, req.Command, func(execCtx context.Context) (protocol.ResponseEnvelope, error) {
			return d.handleSessionStopDirect(execCtx, req)
		})
	}
	return d.handleSessionStopDirect(ctx, req)
}

func (d *Daemon) handleSessionStopDirect(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session stop requested",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
		)
	}
	// Write-through stopped projection immediately so cache-first task/session
	// reads do not resurrect a just-stopped session while tmux/process stop
	// work is still in flight.
	d.writeSessionStopProjection(cmd.ProjectID, cmd.SessionID, cmd.IssueID)
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if exists {
		clearStopPending := d.markSessionStopPending(cmd.ProjectID, cmd.IssueID)
		defer clearStopPending()
		if err := d.tmux.KillSession(ctx, cmd.SessionID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
	}
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
		cmd.IssueID,
		daemonhandlers.CommandSessionStop,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session stop transition: %v", err)), nil
	}
	outputLines := []string{
		fmt.Sprintf("Killing session: %s", cmd.IssueID),
		fmt.Sprintf("✓ Session killed: %s", cmd.IssueID),
	}
	if !exists {
		outputLines[0] = fmt.Sprintf("Session not found in tmux: %s", cmd.IssueID)
		outputLines[1] = fmt.Sprintf("✓ Session marked stopped: %s", cmd.IssueID)
	}
	output := strings.Join(append(outputLines,
		"  Note: Worktree is preserved. Use 'git worktree remove' to clean up.",
		"",
	), "\n")
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session stop completed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"tmux_session_existed", exists,
		)
	}
	return d.commandOutput(req, output), nil
}

func (d *Daemon) writeSessionStopProjection(projectID, sessionID, issueID string) {
	if d.sessionRuntimeStateStore(projectID) == nil {
		return
	}

	projectID = protocol.NormalizeProjectID(projectID)
	sessionID = strings.TrimSpace(sessionID)
	issueID = strings.TrimSpace(issueID)
	if sessionID == "" {
		return
	}
	if issueID == "" {
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(sessionID, d.sessionNamingScope(projectID)); ok {
			issueID = parsedIssueID
		}
	}
	if issueID == "" {
		issueID = sessionID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session := daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateStopped,
		UpdatedAt: time.Now().UTC(),
	}
	if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, session); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("write-through stop session runtime state failed",
			"project_id", projectID,
			"session_id", sessionID,
			"issue_id", issueID,
			"error", err,
		)
	}
}

func (d *Daemon) handleSessionStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, false)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	tmuxSessions, err := d.listTmuxSessionsCacheFirst(ctx, cmd.ProjectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	issueClient := d.issueClientForProject(cmd.ProjectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	taskMap := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		taskMap[sessionKey(task.ID)] = task
	}
	if cmd.IssueID != "" {
		matching := make([]string, 0, 1)
		namingScope := d.sessionNamingScope(cmd.ProjectID)
		for _, name := range tmuxSessions {
			if issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope); ok && naming.IssueIDsEqual(issueID, cmd.IssueID) {
				matching = append(matching, name)
			}
		}
		if len(matching) == 0 {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("no active session found for issue: %s", cmd.IssueID)), nil
		}
		tmuxSessions = matching
	}
	if len(tmuxSessions) == 0 {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("daemon session status snapshot", "project_id", cmd.ProjectID, "issue_id", cmd.IssueID, "active_sessions", 0)
		}
		return d.commandOutput(req, "No active sessions\n"), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Active Sessions (%d):\n\n", len(tmuxSessions))
	b.WriteString("ISSUE ID\tSTATUS\tTITLE\n")
	b.WriteString("-------\t------\t-----\n")
	for _, name := range tmuxSessions {
		issueID := name
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(name, d.sessionNamingScope(cmd.ProjectID)); ok {
			issueID = parsedIssueID
		}
		task, ok := taskMap[sessionKey(issueID)]
		status := "unknown"
		title := "(not in issues)"
		if ok {
			status = string(task.Status)
			title = task.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", issueID, status, title)
	}
	b.WriteString("\nUse 'az attach <issue-id>' to attach to a session\n")
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session status snapshot", "project_id", cmd.ProjectID, "issue_id", cmd.IssueID, "active_sessions", len(tmuxSessions))
	}
	return d.commandOutput(req, b.String()), nil
}

func (d *Daemon) handleSessionRecover(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd struct {
		ProjectID string `json:"project_id"`
		SessionID string `json:"session_id,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	cmd.ProjectID = protocol.TrimProjectID(cmd.ProjectID)
	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID
	}
	cmd.ProjectID = protocol.NormalizeProjectID(cmd.ProjectID)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session recover requested", "project_id", cmd.ProjectID, "target_issue_id", cmd.SessionID)
	}
	targetIssueID := strings.TrimSpace(cmd.SessionID)
	if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(targetIssueID, d.sessionNamingScope(cmd.ProjectID)); ok {
		targetIssueID = parsedIssueID
	}

	result, err := d.reconcileTmuxAndDaemonSessions(ctx, cmd.ProjectID, targetIssueID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session recover completed",
			"project_id", cmd.ProjectID,
			"target_issue_id", targetIssueID,
			"recreated_tmux_sessions", result.RecreatedTmuxSessions,
			"aligned_daemon_sessions", result.AlignedDaemonSessions,
		)
	}
	resp := d.successResponse(req)
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal response body: %v", err)), nil
	}
	resp.Body = body
	resp.Revision = d.currentRevision(cmd.ProjectID)
	return resp, nil
}

func (d *Daemon) upsertSessionAndPublish(projectID, sessionID, issueID string, state daemonstate.SessionState) error {
	if d.sessionStore == nil {
		return errors.New("session store unavailable")
	}
	event, err := d.sessionStore.UpsertSession(projectID, sessionID, issueID, state)
	if err != nil {
		return err
	}
	d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(context.Background(), projectID, protocol.Metadata{ProjectID: projectID}, event.Session)
	return nil
}

func (d *Daemon) reconcileTmuxAndDaemonSessions(ctx context.Context, projectID, sessionID string) (sessionRecoveryResult, error) {
	result := sessionRecoveryResult{}
	if d == nil || d.sessionStore == nil || d.tmux == nil || d.worktree == nil {
		return result, nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session reconcile started", "project_id", projectID, "target_issue_id", sessionID)
	}

	tmuxSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return result, err
	}
	tmuxSet := make(map[string]struct{}, len(tmuxSessions))
	tmuxNameByIssueKey := make(map[string]string, len(tmuxSessions))
	targetIssueKey := sessionKey(sessionID)
	namingScope := d.sessionNamingScope(projectID)
	for _, name := range tmuxSessions {
		issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
		if !ok {
			continue
		}
		key := sessionKey(issueID)
		if key == "" {
			continue
		}
		if targetIssueKey != "" && key != targetIssueKey {
			continue
		}
		tmuxSet[key] = struct{}{}
		tmuxNameByIssueKey[key] = name
	}

	snapshot := d.sessionStore.ReadSnapshot(projectID)
	for _, session := range snapshot.Sessions {
		issueID := strings.TrimSpace(session.IssueID)
		if issueID == "" {
			issueID = session.ID
		}
		issueKey := sessionKey(issueID)
		if targetIssueKey != "" && issueKey != targetIssueKey {
			continue
		}
		if d.isSessionStopPending(projectID, issueID) {
			continue
		}
		if session.State == daemonstate.SessionStateStopped {
			continue
		}
		if _, ok := tmuxSet[issueKey]; ok {
			continue
		}
		wt, getErr := d.worktree.Get(ctx, issueID)
		if getErr != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation skipped worktree restore",
					"project_id", projectID,
					"issue_id", issueID,
					"error", getErr,
				)
			}
			continue
		}
		canonicalSessionID := naming.CanonicalSessionID(namingScope, issueID)
		if newErr := d.tmux.NewSession(ctx, canonicalSessionID, wt.Path); newErr != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to recreate tmux session",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", canonicalSessionID,
					"worktree", wt.Path,
					"error", newErr,
				)
			}
			continue
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("daemon session reconcile recreated tmux session",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", canonicalSessionID,
				"worktree", wt.Path,
			)
		}
		if sendErr := d.tmux.SendKeys(ctx, canonicalSessionID, d.buildSessionLaunchCommand(issueID, canonicalSessionID, false, nil, "")); sendErr != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session reconciliation failed to seed launch command",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", canonicalSessionID,
				"error", sendErr,
			)
		}
		tmuxSet[issueKey] = struct{}{}
		tmuxNameByIssueKey[issueKey] = canonicalSessionID
		result.RecreatedTmuxSessions++
	}

	snapshotByIssueKey := make(map[string]daemonstate.Session, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		issueID := strings.TrimSpace(session.IssueID)
		if issueID == "" {
			issueID = session.ID
		}
		issueKey := sessionKey(issueID)
		if issueKey == "" {
			continue
		}
		snapshotByIssueKey[issueKey] = session
	}

	for issueKey := range tmuxSet {
		sessionIDInTmux := tmuxNameByIssueKey[issueKey]
		session, ok := snapshotByIssueKey[issueKey]
		issueID, parsed := naming.ParseIssueIDFromSessionName(sessionIDInTmux, namingScope)
		if !parsed {
			issueID = sessionIDInTmux
		}
		if !ok {
			if err := d.upsertSessionAndPublish(projectID, sessionIDInTmux, issueID, daemonstate.SessionStateStarting); err == nil {
				if err := d.upsertSessionAndPublish(projectID, sessionIDInTmux, issueID, daemonstate.SessionStateAttached); err == nil {
					result.AlignedDaemonSessions++
					if d.cfg.Logger != nil {
						d.cfg.Logger.Info("daemon session reconcile aligned daemon state",
							"project_id", projectID,
							"issue_id", issueID,
							"session_id", sessionIDInTmux,
							"state", string(daemonstate.SessionStateAttached),
						)
					}
				} else if d.cfg.Logger != nil {
					d.cfg.Logger.Warn("session reconciliation failed to mark session attached",
						"project_id", projectID,
						"issue_id", issueID,
						"session_id", sessionIDInTmux,
						"error", err,
					)
				}
			} else if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to mark session starting",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", sessionIDInTmux,
					"error", err,
				)
			}
			continue
		}

		canonicalSessionID := session.ID
		if canonicalSessionID == "" {
			canonicalSessionID = naming.CanonicalSessionID(namingScope, issueID)
		}

		switch session.State {
		case daemonstate.SessionStateStopped:
			if err := d.upsertSessionAndPublish(projectID, canonicalSessionID, issueID, daemonstate.SessionStateStarting); err == nil {
				if err := d.upsertSessionAndPublish(projectID, canonicalSessionID, issueID, daemonstate.SessionStateAttached); err == nil {
					result.AlignedDaemonSessions++
					if d.cfg.Logger != nil {
						d.cfg.Logger.Info("daemon session reconcile aligned daemon state",
							"project_id", projectID,
							"issue_id", issueID,
							"session_id", canonicalSessionID,
							"state", string(daemonstate.SessionStateAttached),
						)
					}
				} else if d.cfg.Logger != nil {
					d.cfg.Logger.Warn("session reconciliation failed to transition stopped session to attached",
						"project_id", projectID,
						"issue_id", issueID,
						"session_id", canonicalSessionID,
						"error", err,
					)
				}
			} else if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to transition stopped session to starting",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", canonicalSessionID,
					"error", err,
				)
			}
		case daemonstate.SessionStateStarting:
			if err := d.upsertSessionAndPublish(projectID, canonicalSessionID, issueID, daemonstate.SessionStateAttached); err == nil {
				result.AlignedDaemonSessions++
				if d.cfg.Logger != nil {
					d.cfg.Logger.Info("daemon session reconcile aligned daemon state",
						"project_id", projectID,
						"issue_id", issueID,
						"session_id", canonicalSessionID,
						"state", string(daemonstate.SessionStateAttached),
					)
				}
			} else if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("session reconciliation failed to transition session to attached",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", canonicalSessionID,
					"error", err,
				)
			}
		}
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session reconcile completed",
			"project_id", projectID,
			"target_issue_id", sessionID,
			"recreated_tmux_sessions", result.RecreatedTmuxSessions,
			"aligned_daemon_sessions", result.AlignedDaemonSessions,
		)
	}

	return result, nil
}

func (d *Daemon) enrichTasksWithSessionState(ctx context.Context, projectID string, tasks []domain.Task) []domain.Task {
	if len(tasks) == 0 || d.sessionStore == nil {
		return tasks
	}

	tmuxSessions, err := d.listTmuxSessionsCacheFirst(ctx, projectID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("failed to list tmux sessions while enriching task session state",
				"project_id", projectID,
				"error", err,
			)
		}
		return tasks
	}
	tmuxSet := make(map[string]struct{}, len(tmuxSessions))
	namingScope := d.sessionNamingScope(projectID)
	for _, name := range tmuxSessions {
		if issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope); ok {
			key := sessionKey(issueID)
			if key == "" {
				continue
			}
			tmuxSet[key] = struct{}{}
		}
	}
	snapshot := d.sessionStore.ReadSnapshot(projectID)
	snapshotSessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		snapshotSessions = append(snapshotSessions, session)
	}
	snapshotByKey := sessionProjectionByIssueKey(snapshotSessions, namingScope)

	projectionByKey := map[string]daemonstate.Session{}
	if d.sessionRuntimeStateStore(projectID) != nil {
		cachedSessions, err := d.sessionRuntimeStateStore(projectID).ListSessionStates(ctx, projectID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("failed to load cached session runtime state while enriching tasks", "project_id", projectID, "error", err)
			}
		} else {
			projectionByKey = sessionProjectionByIssueKey(cachedSessions, namingScope)
		}
	}

	for i := range tasks {
		taskID := tasks[i].ID
		taskKey := sessionKey(taskID)
		if _, ok := tmuxSet[taskKey]; !ok {
			continue
		}

		state := domain.SessionBusy
		var startedAt *time.Time
		session, ok := snapshotByKey[taskKey]
		if !ok {
			session, ok = projectionByKey[taskKey]
		}
		if ok {
			if !session.UpdatedAt.IsZero() {
				started := session.UpdatedAt.UTC()
				startedAt = &started
			}
			switch session.State {
			case daemonstate.SessionStatePaused:
				state = domain.SessionPaused
			case daemonstate.SessionStateStopped:
				continue
			default:
				state = domain.SessionBusy
			}
		}
		tasks[i].Session = &domain.Session{
			IssueID:   taskID,
			State:     state,
			StartedAt: startedAt,
		}
	}

	return tasks
}

func (d *Daemon) listTmuxSessionsCacheFirst(ctx context.Context, projectID string) ([]string, error) {
	projectID = protocol.NormalizeProjectID(projectID)

	if d.sessionRuntimeStateStore(projectID) != nil {
		cachedSessions, err := d.sessionRuntimeStateStore(projectID).ListSessionStates(ctx, projectID)
		if err == nil && len(cachedSessions) > 0 {
			cachedActive := make([]string, 0, len(cachedSessions))
			for _, session := range cachedSessions {
				if session.State == daemonstate.SessionStateStopped {
					continue
				}
				if d.isSessionStopPending(projectID, session.IssueID) {
					continue
				}
				if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(session.ID, d.sessionNamingScope(projectID)); ok {
					if d.isSessionStopPending(projectID, parsedIssueID) {
						continue
					}
				}
				if strings.TrimSpace(session.ID) == "" {
					continue
				}
				cachedActive = append(cachedActive, session.ID)
			}
			if len(cachedActive) > 0 {
				return cachedActive, nil
			}
		}
	}

	tmuxSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	return tmuxSessions, nil
}

func (d *Daemon) refreshSessionRuntimeState(ctx context.Context, projectID string) error {
	if d == nil || d.tmux == nil || d.sessionRuntimeStateStore(projectID) == nil || d.sessionStore == nil {
		return nil
	}
	tmuxSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return err
	}
	return d.persistTmuxSessionRuntimeState(ctx, projectID, tmuxSessions)
}

func (d *Daemon) persistTmuxSessionRuntimeState(ctx context.Context, projectID string, tmuxSessions []string) error {
	if d.sessionRuntimeStateStore(projectID) == nil || d.sessionStore == nil {
		return nil
	}
	projectID = protocol.NormalizeProjectID(projectID)

	namingScope := d.sessionNamingScope(projectID)
	snapshot := d.sessionStore.ReadSnapshot(projectID)
	snapshotSessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		snapshotSessions = append(snapshotSessions, session)
	}
	byIssueKey := sessionProjectionByIssueKey(snapshotSessions, namingScope)

	cachedByIssueKey := map[string]daemonstate.Session{}
	cachedSessions, err := d.sessionRuntimeStateStore(projectID).ListSessionStates(ctx, projectID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load cached session runtime-state snapshot failed", "project_id", projectID, "error", err)
		}
	} else {
		cachedByIssueKey = sessionProjectionByIssueKey(cachedSessions, namingScope)
	}

	rows := make([]daemonstate.Session, 0, len(tmuxSessions))
	for _, name := range tmuxSessions {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
		if !ok {
			issueID = name
		}
		issueKey := sessionKey(issueID)
		row := daemonstate.Session{
			ID:        name,
			IssueID:   issueID,
			State:     daemonstate.SessionStateAttached,
			UpdatedAt: time.Now().UTC(),
		}
		if existing, exists := byIssueKey[issueKey]; exists {
			row.State = existing.State
			if row.State == daemonstate.SessionStateStopped {
				row.State = daemonstate.SessionStateAttached
			}
			if !existing.UpdatedAt.IsZero() {
				row.UpdatedAt = existing.UpdatedAt
			}
		} else if cached, exists := cachedByIssueKey[issueKey]; exists {
			row.State = cached.State
			if row.State == daemonstate.SessionStateStopped {
				row.State = daemonstate.SessionStateAttached
			}
			if !cached.UpdatedAt.IsZero() {
				row.UpdatedAt = cached.UpdatedAt
			}
		}
		rows = append(rows, row)
	}
	return d.runtimeProjectionStateWriter().ReplaceSessionProjectionSnapshot(ctx, projectID, rows)
}

func (d *Daemon) buildSessionLaunchCommand(issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt string) string {
	toolCommand := d.buildCLIToolCommand(issueID, sessionID, yolo, imagePaths, initialPrompt)
	commands := make([]string, 0, len(d.cfg.SessionInitCommands)+2)
	for _, initCmd := range d.cfg.SessionInitCommands {
		trimmed := strings.TrimSpace(initCmd)
		if trimmed != "" {
			commands = append(commands, trimmed)
		}
	}
	commands = append(commands, toolCommand)

	shell := strings.TrimSpace(d.cfg.SessionShell)
	if shell == "" {
		shell = appconfig.DefaultSessionShell()
	}
	inner := strings.Join(commands, "; ")
	inner = inner + "; exec " + shell
	return fmt.Sprintf("%s -i -c %s", shell, singleQuoteForShell(inner))
}

func (d *Daemon) runWorktreeInitCommands(ctx context.Context, worktreePath string) error {
	commands := d.cfg.WorktreeInitCommands
	if len(commands) == 0 {
		return nil
	}

	shell := strings.TrimSpace(d.cfg.SessionShell)
	if shell == "" {
		shell = appconfig.DefaultSessionShell()
	}

	for _, initCmd := range commands {
		trimmed := strings.TrimSpace(initCmd)
		if trimmed == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, shell, "-lc", trimmed)
		cmd.Dir = worktreePath
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w (%s)", trimmed, err, strings.TrimSpace(string(output)))
		}
	}

	return nil
}

func (d *Daemon) buildCLIToolCommand(issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt string) string {
	tool := strings.TrimSpace(d.cfg.CLITool)
	if tool == "" {
		tool = "claude"
	}

	parts := []string{
		fmt.Sprintf(`AZEDARACH_ISSUE_ID="%s"`, escapeForShellDoubleQuotes(issueID)),
		tool,
	}

	if strings.EqualFold(tool, "codex") {
		sessionStartCommand := "az notify --json session_start"
		userPromptSubmitCommand := "az notify --json user_prompt_submit"
		preToolUseCommand := "az notify --json pre_tool_use"
		postToolUseCommand := "az notify --json post_tool_use"
		stopCommand := "az notify --json stop"
		parts = append(parts,
			buildCodexConfigOverrideArg("hooks.SessionStart", sessionStartCommand),
			buildCodexConfigOverrideArg("hooks.UserPromptSubmit", userPromptSubmitCommand),
			buildCodexConfigOverrideArg("hooks.PreToolUse", preToolUseCommand),
			buildCodexConfigOverrideArg("hooks.PostToolUse", postToolUseCommand),
			buildCodexConfigOverrideArg("hooks.Stop", stopCommand),
		)
		for _, imagePath := range imagePaths {
			trimmedPath := strings.TrimSpace(imagePath)
			if trimmedPath == "" {
				continue
			}
			parts = append(parts, fmt.Sprintf(`--image "%s"`, escapeForShellDoubleQuotes(trimmedPath)))
		}
	}
	if yolo {
		parts = append(parts, "--dangerously-skip-permissions")
	}
	if initialPrompt != "" {
		escapedPrompt := escapeForShellDoubleQuotes(initialPrompt)
		switch strings.ToLower(tool) {
		case "opencode":
			parts = append(parts, fmt.Sprintf(`--prompt "%s"`, escapedPrompt))
		case "codex":
			parts = append(parts, "--", fmt.Sprintf(`"%s"`, escapedPrompt))
		default:
			parts = append(parts, fmt.Sprintf(`"%s"`, escapedPrompt))
		}
	}

	return strings.Join(parts, " ")
}

func buildCodexConfigOverrideArg(key, command string) string {
	tomlCommand := strings.ReplaceAll(strings.ReplaceAll(command, `\`, `\\`), `"`, `\"`)
	override := fmt.Sprintf(`%s=[{hooks=[{command="%s"}]}]`, key, tomlCommand)
	return fmt.Sprintf(`-c "%s"`, escapeForShellDoubleQuotes(override))
}

func escapeForShellDoubleQuotes(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(escaped, `"`, `\"`)
}

func singleQuoteForShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func buildStartWorkPrompt(issueID, issueType, title string) string {
	safeIssueType := sanitizePromptInline(issueType, 0)
	if safeIssueType == "" {
		safeIssueType = "task"
	}
	safeTitle := sanitizePromptInline(title, 160)
	if safeTitle == "" {
		safeTitle = issueID
	}
	return fmt.Sprintf(
		"work on issue %s (%s): %s\n\nStart by running `az prime`. Then continue the task using the context it prints without waiting for further instruction.",
		issueID,
		safeIssueType,
		safeTitle,
	)
}

func sanitizePromptInline(value string, maxLength int) string {
	mapped := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	normalized := strings.Join(strings.Fields(mapped), " ")
	safe := strings.NewReplacer("<", "[", ">", "]").Replace(normalized)
	if maxLength <= 0 {
		return safe
	}
	runes := []rune(safe)
	if len(runes) <= maxLength {
		return safe
	}
	trimmed := strings.TrimSpace(string(runes[:maxLength-3]))
	return trimmed + "..."
}
