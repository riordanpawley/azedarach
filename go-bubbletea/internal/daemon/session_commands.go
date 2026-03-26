package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type sessionCommandBody struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type resolvedSessionTarget struct {
	ProjectID  string
	IssueID    string
	SessionID  string
	BaseBranch string
}

type sessionRecoveryResult struct {
	RecreatedTmuxSessions int `json:"recreated_tmux_sessions"`
	AlignedDaemonSessions int `json:"aligned_daemon_sessions"`
}

func sessionKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (d *Daemon) decodeSessionRequest(req protocol.RequestEnvelope, requireSession bool) (resolvedSessionTarget, protocol.ResponseEnvelope, bool) {
	var cmd sessionCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), false
	}
	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID
	}
	if cmd.ProjectID == "" {
		cmd.ProjectID = "default"
	}
	if requireSession && cmd.SessionID == "" {
		return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/session_id"), false
	}

	issueID := strings.TrimSpace(cmd.SessionID)
	if issueID != "" {
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(issueID, d.cfg.RepoDir); ok {
			issueID = parsedIssueID
		}
	}
	sessionID := ""
	if issueID != "" {
		sessionID = naming.CanonicalSessionID(d.cfg.RepoDir, issueID)
	}
	return resolvedSessionTarget{
		ProjectID:  cmd.ProjectID,
		IssueID:    issueID,
		SessionID:  sessionID,
		BaseBranch: cmd.BaseBranch,
	}, protocol.ResponseEnvelope{}, true
}

func (d *Daemon) handleSessionStart(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if exists {
		return d.errorResponse(req, protocol.ErrorCodeConflict, fmt.Sprintf("session already exists: %s (use 'az attach %s' to connect)", cmd.IssueID, cmd.IssueID)), nil
	}
	tasks, err := d.issues.Search(ctx, cmd.IssueID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if len(tasks) == 0 {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("issue not found: %s", cmd.IssueID)), nil
	}
	baseBranch := cmd.BaseBranch
	if baseBranch == "" {
		baseBranch = d.cfg.BaseBranch
	}
	worktree, err := d.worktree.CreateWithTitle(ctx, cmd.IssueID, tasks[0].Title, baseBranch)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if err := d.tmux.NewSession(ctx, cmd.SessionID, worktree.Path); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if err := d.tmux.SendKeys(ctx, cmd.SessionID, d.cfg.CLITool); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	_ = d.issues.Update(ctx, cmd.IssueID, domain.StatusInProgress)
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

	output := strings.Join([]string{
		fmt.Sprintf("Starting session for: %s - %s", tasks[0].ID, tasks[0].Title),
		fmt.Sprintf("Creating worktree from branch: %s", baseBranch),
		fmt.Sprintf("Worktree created: %s", worktree.Path),
		fmt.Sprintf("Creating tmux session: %s", cmd.SessionID),
		"",
		"✓ Session started successfully",
		fmt.Sprintf("  To attach: az attach %s", cmd.IssueID),
		fmt.Sprintf("  Or run:    tmux attach-session -t %s", cmd.SessionID),
		"",
	}, "\n")
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionAttach(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
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
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionStop(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, true)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s", cmd.SessionID)), nil
	}
	if err := d.tmux.KillSession(ctx, cmd.SessionID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
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
	output := strings.Join([]string{
		fmt.Sprintf("Killing session: %s", cmd.IssueID),
		fmt.Sprintf("✓ Session killed: %s", cmd.IssueID),
		"  Note: Worktree is preserved. Use 'git worktree remove' to clean up.",
		"",
	}, "\n")
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req, false)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if _, err := d.reconcileTmuxAndDaemonSessions(ctx, cmd.ProjectID, cmd.IssueID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("session reconciliation during session.status failed", "project_id", cmd.ProjectID, "issue_id", cmd.IssueID, "error", err)
	}
	tmuxSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	tasks, err := d.issues.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	taskMap := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		taskMap[sessionKey(task.ID)] = task
	}
	if cmd.IssueID != "" {
		matching := make([]string, 0, 1)
		for _, name := range tmuxSessions {
			if issueID, ok := naming.ParseIssueIDFromSessionName(name, d.cfg.RepoDir); ok && naming.IssueIDsEqual(issueID, cmd.IssueID) {
				matching = append(matching, name)
			}
		}
		if len(matching) == 0 {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("no active session found for issue: %s", cmd.IssueID)), nil
		}
		tmuxSessions = matching
	}
	if len(tmuxSessions) == 0 {
		return d.commandOutput(req, "No active sessions\n"), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Active Sessions (%d):\n\n", len(tmuxSessions))
	b.WriteString("ISSUE ID\tSTATUS\tTITLE\n")
	b.WriteString("-------\t------\t-----\n")
	for _, name := range tmuxSessions {
		issueID := name
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(name, d.cfg.RepoDir); ok {
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
	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID
	}
	if cmd.ProjectID == "" {
		cmd.ProjectID = "default"
	}
	targetIssueID := strings.TrimSpace(cmd.SessionID)
	if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(targetIssueID, d.cfg.RepoDir); ok {
		targetIssueID = parsedIssueID
	}

	result, err := d.reconcileTmuxAndDaemonSessions(ctx, cmd.ProjectID, targetIssueID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
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

func (d *Daemon) reconcileTmuxAndDaemonSessions(ctx context.Context, projectID, sessionID string) (sessionRecoveryResult, error) {
	result := sessionRecoveryResult{}
	if d.sessionStore == nil {
		return result, nil
	}

	tmuxSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return result, err
	}
	tmuxSet := make(map[string]struct{}, len(tmuxSessions))
	tmuxNameByIssueKey := make(map[string]string, len(tmuxSessions))
	targetIssueKey := sessionKey(sessionID)
	for _, name := range tmuxSessions {
		issueID, ok := naming.ParseIssueIDFromSessionName(name, d.cfg.RepoDir)
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
		if session.State == daemonstate.SessionStateStopped {
			continue
		}
		if _, ok := tmuxSet[issueKey]; ok {
			continue
		}
		wt, getErr := d.worktree.Get(ctx, issueID)
		if getErr != nil {
			continue
		}
		canonicalSessionID := naming.CanonicalSessionID(d.cfg.RepoDir, issueID)
		if newErr := d.tmux.NewSession(ctx, canonicalSessionID, wt.Path); newErr != nil {
			continue
		}
		_ = d.tmux.SendKeys(ctx, canonicalSessionID, d.cfg.CLITool)
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
		issueID, parsed := naming.ParseIssueIDFromSessionName(sessionIDInTmux, d.cfg.RepoDir)
		if !parsed {
			issueID = sessionIDInTmux
		}
		if !ok {
			if _, err := d.sessionStore.UpsertSession(projectID, sessionIDInTmux, issueID, daemonstate.SessionStateStarting); err == nil {
				if _, err := d.sessionStore.UpsertSession(projectID, sessionIDInTmux, issueID, daemonstate.SessionStateAttached); err == nil {
					result.AlignedDaemonSessions++
				}
			}
			continue
		}

		canonicalSessionID := session.ID
		if canonicalSessionID == "" {
			canonicalSessionID = naming.CanonicalSessionID(d.cfg.RepoDir, issueID)
		}

		switch session.State {
		case daemonstate.SessionStateStopped:
			if _, err := d.sessionStore.UpsertSession(projectID, canonicalSessionID, issueID, daemonstate.SessionStateStarting); err == nil {
				if _, err := d.sessionStore.UpsertSession(projectID, canonicalSessionID, issueID, daemonstate.SessionStateAttached); err == nil {
					result.AlignedDaemonSessions++
				}
			}
		case daemonstate.SessionStateStarting:
			if _, err := d.sessionStore.UpsertSession(projectID, canonicalSessionID, issueID, daemonstate.SessionStateAttached); err == nil {
				result.AlignedDaemonSessions++
			}
		}
	}

	return result, nil
}

func (d *Daemon) enrichTasksWithSessionState(ctx context.Context, projectID string, tasks []domain.Task) []domain.Task {
	if len(tasks) == 0 || d.sessionStore == nil {
		return tasks
	}

	tmuxSessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return tasks
	}
	tmuxSet := make(map[string]struct{}, len(tmuxSessions))
	for _, name := range tmuxSessions {
		if issueID, ok := naming.ParseIssueIDFromSessionName(name, d.cfg.RepoDir); ok {
			key := sessionKey(issueID)
			if key == "" {
				continue
			}
			tmuxSet[key] = struct{}{}
		}
	}
	snapshot := d.sessionStore.ReadSnapshot(projectID)
	snapshotByKey := make(map[string]daemonstate.Session, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		issueID := strings.TrimSpace(session.IssueID)
		if issueID == "" {
			if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(session.ID, d.cfg.RepoDir); ok {
				issueID = parsedIssueID
			} else {
				issueID = session.ID
			}
		}
		snapshotByKey[sessionKey(issueID)] = session
	}

	for i := range tasks {
		taskID := tasks[i].ID
		taskKey := sessionKey(taskID)
		if _, ok := tmuxSet[taskKey]; !ok {
			continue
		}

		state := domain.SessionBusy
		if session, ok := snapshotByKey[taskKey]; ok {
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
			IssueID: taskID,
			State:   state,
		}
	}

	return tasks
}
