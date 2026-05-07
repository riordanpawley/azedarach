package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type sessionCommandBody struct {
	ProjectID  string   `json:"project_id"`
	SessionID  string   `json:"session_id"`
	BaseBranch string   `json:"base_branch,omitempty"`
	Yolo       bool     `json:"yolo,omitempty"`
	StartWork  *bool    `json:"start_work,omitempty"`
	ImagePaths []string `json:"image_paths,omitempty"`
	Prompt     string   `json:"initial_prompt,omitempty"`
}

type resolvedSessionTarget struct {
	ProjectID  string
	IssueID    string
	SessionID  string
	BaseBranch string
	Yolo       bool
	StartWork  bool
	ImagePaths []string
	Prompt     string
}

type sessionRecoveryResult struct {
	RecreatedTmuxSessions int `json:"recreated_tmux_sessions"`
	AlignedDaemonSessions int `json:"aligned_daemon_sessions"`
}

const (
	sessionInvariantSessionStartConflict daemonInvariantID = daemonInvariantSessionStartConflict
	sessionInvariantSessionAttachTarget  daemonInvariantID = daemonInvariantSessionAttachTarget
	sessionInvariantSessionStopTargets   daemonInvariantID = daemonInvariantSessionStopTargets
	sessionInvariantSessionReconcile     daemonInvariantID = daemonInvariantSessionReconcile

	sessionConflictWindowName = "resolve-conflict"
)

type SessionLongRunningExecutor interface {
	Execute(ctx context.Context, req protocol.RequestEnvelope, command string, exec func(context.Context) (protocol.ResponseEnvelope, error)) (protocol.ResponseEnvelope, error)
}

func sessionKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sessionProjectionIssueID(session daemonstate.Session, namingScope string) string {
	issue, issueErr := naming.ParseIssueID(strings.TrimSpace(session.IssueID))
	if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(session.ID, namingScope); ok {
		_, parsedErr := naming.ParseIssueID(parsedIssueID)
		if parsedErr == nil && issueErr != nil {
			return parsedIssueID
		}
		sessionLikePrefix := naming.ProjectSessionPrefix(namingScope) + "-"
		if parsedErr == nil && issueErr == nil && (naming.IssueIDsEqual(issue.String(), session.ID) || strings.HasPrefix(strings.ToLower(issue.String()), strings.ToLower(sessionLikePrefix))) {
			return parsedIssueID
		}
	}
	if issueErr == nil {
		return issue.String()
	}

	fallback, fallbackErr := naming.ParseIssueID(strings.TrimSpace(session.ID))
	if fallbackErr != nil {
		return ""
	}
	return fallback.String()
}

func sessionProjectionStateRank(state daemonstate.SessionState) int {
	switch state {
	case daemonstate.SessionStateAttached:
		return 4
	case daemonstate.SessionStateStarting:
		return 3
	case daemonstate.SessionStatePaused:
		return 2
	case daemonstate.SessionStateStopped:
		return 1
	default:
		return 0
	}
}

func shouldReplaceSessionProjection(existing, candidate daemonstate.Session) bool {
	if candidate.UpdatedAt.After(existing.UpdatedAt) {
		return true
	}
	if existing.UpdatedAt.After(candidate.UpdatedAt) {
		return false
	}
	existingRank := sessionProjectionStateRank(existing.State)
	candidateRank := sessionProjectionStateRank(candidate.State)
	if candidateRank != existingRank {
		return candidateRank > existingRank
	}
	return strings.TrimSpace(candidate.ID) < strings.TrimSpace(existing.ID)
}

func sessionProjectionLatestByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	byIssueKey := make(map[string]daemonstate.Session, len(sessions))
	for _, session := range sessions {
		key := sessionKey(sessionProjectionIssueID(session, namingScope))
		if key == "" {
			continue
		}
		existing, exists := byIssueKey[key]
		if !exists || shouldReplaceSessionProjection(existing, session) {
			byIssueKey[key] = session
		}
	}
	return byIssueKey
}

func sessionProjectionForReconcileByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	return sessionProjectionLatestByIssueKey(sessions, namingScope)
}

func sessionProjectionForTmuxHydrationByIssueKey(sessions []daemonstate.Session, namingScope string) map[string]daemonstate.Session {
	return sessionProjectionLatestByIssueKey(sessions, namingScope)
}

func sessionProjectionForTaskDisplayByIssueKey(snapshotByIssueKey, projectionByIssueKey map[string]daemonstate.Session) map[string]daemonstate.Session {
	byIssueKey := make(map[string]daemonstate.Session, len(snapshotByIssueKey)+len(projectionByIssueKey))
	for key, session := range snapshotByIssueKey {
		byIssueKey[key] = session
	}
	for key, session := range projectionByIssueKey {
		existing, exists := byIssueKey[key]
		if !exists || shouldReplaceSessionProjection(existing, session) {
			byIssueKey[key] = session
		}
	}
	return byIssueKey
}

func sessionProjectionStartedAtForTaskDisplay(snapshotSession daemonstate.Session, snapshotOK bool, projectionSession daemonstate.Session, projectionOK bool) *time.Time {
	var startedSource *time.Time

	if snapshotOK && snapshotSession.StartedAt != nil && !snapshotSession.StartedAt.IsZero() {
		startedSource = snapshotSession.StartedAt
	}
	if projectionOK && projectionSession.StartedAt != nil && !projectionSession.StartedAt.IsZero() {
		if startedSource == nil || startedSource.IsZero() || projectionSession.StartedAt.Before(*startedSource) {
			startedSource = projectionSession.StartedAt
		}
	}
	if startedSource == nil || startedSource.IsZero() {
		switch {
		case snapshotOK && !snapshotSession.UpdatedAt.IsZero():
			startedSource = &snapshotSession.UpdatedAt
		case projectionOK && !projectionSession.UpdatedAt.IsZero():
			startedSource = &projectionSession.UpdatedAt
		}
	}
	if startedSource == nil || startedSource.IsZero() {
		return nil
	}
	started := startedSource.UTC()
	return &started
}

func (d *Daemon) sourceForSessionInvariant(invariant daemonInvariantID) daemonInvariantSource {
	return sourceForInvariant(invariant)
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
	projectID = d.canonicalProjectID(projectID)
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
	projectID = d.canonicalProjectID(projectID)
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
	if repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(trimmed)); repoDir != "" {
		return repoDir
	}
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
		cmd.ProjectID = req.Meta.ProjectID.String()
	}
	cmd.ProjectID = d.canonicalProjectID(cmd.ProjectID)
	if requireSession && cmd.SessionID == "" {
		return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/session_id"), false
	}

	issueID := ""
	namingScope := d.sessionNamingScope(cmd.ProjectID)
	sessionInput := strings.TrimSpace(cmd.SessionID)
	if sessionInput != "" {
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(sessionInput, namingScope); ok {
			sessionInput = parsedIssueID
		}
		validIssueID, issueErr := naming.ParseIssueID(sessionInput)
		if issueErr != nil {
			return resolvedSessionTarget{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid session/issue id: %v", issueErr)), false
		}
		issueID = validIssueID.String()
	}
	sessionID := ""
	if issueID != "" {
		typedIssueID, _ := naming.ParseIssueID(issueID)
		sessionID = naming.CanonicalSessionIDForIssue(namingScope, typedIssueID).String()
	}
	startWork := true
	if cmd.StartWork != nil {
		startWork = *cmd.StartWork
	}
	return resolvedSessionTarget{
		ProjectID:  cmd.ProjectID,
		IssueID:    issueID,
		SessionID:  sessionID,
		BaseBranch: cmd.BaseBranch,
		Yolo:       cmd.Yolo,
		StartWork:  startWork,
		ImagePaths: cmd.ImagePaths,
		Prompt:     cmd.Prompt,
	}, protocol.ResponseEnvelope{}, true
}

func (d *Daemon) sessionProjectionSnapshot(ctx context.Context, projectID string) ([]daemonstate.Session, error) {
	if d == nil || d.sessionStore == nil {
		return nil, nil
	}
	projectID = d.canonicalProjectID(projectID)
	if err := d.refreshSessionInvariantCacheIfConfigured(ctx, projectID); err != nil {
		return nil, err
	}
	snapshot := d.sessionStore.ReadSnapshot(projectID)
	sessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (d *Daemon) tmuxSessionNamesForIssue(ctx context.Context, projectID, issueID, canonicalSessionID string, source daemonInvariantSource) ([]string, error) {
	typedIssueID, issueErr := naming.ParseIssueID(strings.TrimSpace(issueID))
	if issueErr != nil {
		return nil, nil
	}
	projectID = d.canonicalProjectID(projectID)
	namingScope := d.sessionNamingScope(projectID)
	canonicalSessionID = strings.TrimSpace(canonicalSessionID)
	names := map[string]struct{}{}

	if usesProjectionSource(source) {
		snapshotSessions, err := d.sessionProjectionSnapshot(ctx, projectID)
		if err != nil {
			return nil, err
		}
		for _, session := range snapshotSessions {
			if session.State == daemonstate.SessionStateStopped {
				continue
			}
			sessionID := strings.TrimSpace(session.ID)
			if sessionID == "" {
				continue
			}
			if canonicalSessionID != "" && strings.EqualFold(sessionID, canonicalSessionID) {
				names[sessionID] = struct{}{}
				continue
			}
			projectedIssueID := sessionProjectionIssueID(session, namingScope)
			if naming.IssueIDsEqual(projectedIssueID, typedIssueID.String()) {
				names[sessionID] = struct{}{}
			}
		}
	}

	if usesTmuxSource(source) && d.tmux != nil {
		sessions, err := d.tmux.ListSessions(ctx)
		if err != nil {
			return nil, err
		}
		for _, sessionName := range sessions {
			name := strings.TrimSpace(sessionName)
			if name == "" {
				continue
			}
			if canonicalSessionID != "" && strings.EqualFold(name, canonicalSessionID) {
				names[name] = struct{}{}
				continue
			}
			projectedIssueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
			if ok && naming.IssueIDsEqual(projectedIssueID, typedIssueID.String()) {
				names[name] = struct{}{}
			}
		}
	}

	resolved := make([]string, 0, len(names))
	for name := range names {
		resolved = append(resolved, name)
	}
	return resolved, nil
}

func (d *Daemon) sessionExistsForInvariant(ctx context.Context, projectID, issueID, canonicalSessionID string, source daemonInvariantSource) (bool, error) {
	names, err := d.tmuxSessionNamesForIssue(ctx, projectID, issueID, canonicalSessionID, source)
	if err != nil {
		return false, err
	}
	if len(names) == 0 {
		return false, nil
	}
	target := strings.TrimSpace(canonicalSessionID)
	if target == "" {
		return true, nil
	}
	for _, name := range names {
		if strings.TrimSpace(name) == target {
			return true, nil
		}
	}
	return false, nil
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
			"start_work", cmd.StartWork,
			"image_count", len(cmd.ImagePaths),
		)
	}
	if err := d.ensureFreshRuntimeForIssueMutation(ctx, cmd.ProjectID, cmd.IssueID, daemonhandlers.CommandSessionStart); err != nil {
		if d.cfg.Logger != nil {
			if errors.Is(err, context.Canceled) {
				d.cfg.Logger.Warn("daemon session start aborted after freshness cancellation",
					"project_id", cmd.ProjectID,
					"issue_id", cmd.IssueID,
					"session_id", cmd.SessionID,
					"error", err,
				)
			} else if isRuntimeMutationFreshnessTimeout(err) {
				d.cfg.Logger.Warn("daemon session start continuing after freshness timeout",
					"project_id", cmd.ProjectID,
					"issue_id", cmd.IssueID,
					"session_id", cmd.SessionID,
					"error", err,
				)
			}
		}
		if errors.Is(err, context.Canceled) {
			return d.mutationFreshnessErrorResponse(req, err), nil
		}
		if !isRuntimeMutationFreshnessTimeout(err) {
			return d.mutationFreshnessErrorResponse(req, err), nil
		}
	}
	startConflictSource := d.sourceForSessionInvariant(sessionInvariantSessionStartConflict)
	exists, err := d.sessionExistsForInvariant(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, startConflictSource)
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
		baseBranch = d.baseBranchForProject(cmd.ProjectID)
	}
	worktreeManager := d.worktreeManagerForProject(cmd.ProjectID)
	if worktreeManager == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "worktree manager unavailable"), nil
	}
	worktree, err := worktreeManager.CreateWithTitle(ctx, cmd.IssueID, task.Title, baseBranch)
	reusedWorktree := false
	if err != nil {
		// Recovery path: git worktree add can return non-zero after materializing
		// a usable worktree (for example, hooks that fail post-checkout).
		// If we can load the worktree for the issue, continue by reusing it.
		if recoveredWorktree, recoverErr := worktreeManager.Get(ctx, cmd.IssueID); recoverErr == nil {
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
		if err := d.runWorktreeInitCommands(ctx, cmd.ProjectID, worktree.Path); err != nil {
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
	if cmd.StartWork {
		initialPrompt := strings.TrimSpace(cmd.Prompt)
		if initialPrompt == "" {
			initialPrompt = buildStartWorkPrompt(cmd.IssueID, task.Type.String(), task.Title)
		}
		launchCommand := d.buildSessionLaunchCommand(cmd.ProjectID, cmd.IssueID, cmd.SessionID, cmd.Yolo, cmd.ImagePaths, initialPrompt)
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
	if updateErr := issueClient.Update(ctx, cmd.IssueID, domain.StatusInProgress); updateErr != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("failed to update issue status to in_progress after session start",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"error", updateErr,
		)
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
		func() string {
			if cmd.StartWork {
				return "Launching AI session in tmux"
			}
			return "Skipping AI launch (tmux session only)"
		}(),
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
			if sessionKey(task.ID.String()) == requestedKey {
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
	attachTargetSource := d.sourceForSessionInvariant(sessionInvariantSessionAttachTarget)
	exists, err := d.sessionExistsForInvariant(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, attachTargetSource)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s (use 'az start %s' to create)", cmd.IssueID, cmd.IssueID)), nil
	}
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
	output := strings.Join([]string{
		fmt.Sprintf("Attaching to session: %s", cmd.SessionID),
		"(Press Ctrl+B then D to detach)",
		"",
	}, "\n")
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
	if !d.sessionLifecycleTransitionNeeded(cmd.ProjectID, cmd.SessionID, cmd.IssueID, daemonstate.SessionStatePaused) {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("daemon session pause unchanged",
				"project_id", cmd.ProjectID,
				"issue_id", cmd.IssueID,
				"session_id", cmd.SessionID,
			)
		}
		return d.commandOutput(req, fmt.Sprintf("Paused session: %s\n", cmd.IssueID)), nil
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
	if err := d.ensureFreshRuntimeForIssueMutation(ctx, cmd.ProjectID, cmd.IssueID, daemonhandlers.CommandSessionPause); err != nil {
		return d.mutationFreshnessErrorResponse(req, err), nil
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
	if !d.sessionLifecycleTransitionNeeded(cmd.ProjectID, cmd.SessionID, cmd.IssueID, daemonstate.SessionStateAttached) {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("daemon session resume unchanged",
				"project_id", cmd.ProjectID,
				"issue_id", cmd.IssueID,
				"session_id", cmd.SessionID,
			)
		}
		return d.commandOutput(req, fmt.Sprintf("Resumed session: %s\n", cmd.IssueID)), nil
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
	if err := d.ensureFreshRuntimeForIssueMutation(ctx, cmd.ProjectID, cmd.IssueID, daemonhandlers.CommandSessionResume); err != nil {
		return d.mutationFreshnessErrorResponse(req, err), nil
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
	stopTargetsSource := d.sourceForSessionInvariant(sessionInvariantSessionStopTargets)
	// Write-through stopped projection immediately so cache-first task/session
	// reads do not resurrect a just-stopped session while tmux/process stop
	// work is still in flight.
	clearStopPending := d.markSessionStopPending(cmd.ProjectID, cmd.IssueID)
	defer clearStopPending()
	if err := d.writeSessionStopProjection(cmd.ProjectID, cmd.SessionID, cmd.IssueID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session stop intent: %v", err)), nil
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
	if err := ctx.Err(); err != nil {
		return d.mutationFreshnessErrorResponse(req, err), nil
	}
	sessionNamesToKill, err := d.tmuxSessionNamesForIssue(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID, stopTargetsSource)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	exists := len(sessionNamesToKill) > 0
	if exists {
		for _, sessionName := range sessionNamesToKill {
			if err := d.tmux.KillSession(ctx, sessionName); err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
		}
	}
	if err := d.refreshStoppedSessionRuntimeState(ctx, cmd.ProjectID, cmd.IssueID, cmd.SessionID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("daemon session stop post-kill issue refresh failed",
			"project_id", cmd.ProjectID,
			"issue_id", cmd.IssueID,
			"session_id", cmd.SessionID,
			"error", err,
		)
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

func (d *Daemon) handleSessionResolveConflict(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if d.sessionLongRunning != nil {
		return d.sessionLongRunning.Execute(ctx, req, req.Command, func(execCtx context.Context) (protocol.ResponseEnvelope, error) {
			return d.handleSessionResolveConflictDirect(execCtx, req)
		})
	}
	return d.handleSessionResolveConflictDirect(ctx, req)
}

func (d *Daemon) handleSessionResolveConflictDirect(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.SessionResolveConflictRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	projectID := protocol.TrimProjectID(body.ProjectID.String())
	if projectID == "" {
		projectID = req.Meta.ProjectID.String()
	}
	projectID = d.canonicalProjectID(projectID)
	issueID, err := naming.ParseIssueID(strings.TrimSpace(body.IssueID.String()))
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/issue_id"), nil
	}
	issueIDString := issueID.String()
	conflictFiles := normalizeConflictFiles(body.ConflictFiles)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session conflict resolution requested",
			"project_id", projectID,
			"issue_id", issueIDString,
			"worktree", strings.TrimSpace(body.Worktree),
			"conflict_file_count", len(conflictFiles),
		)
	}

	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.Search(ctx, issueIDString)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	task, ok := resolveSessionIssue(tasks, issueIDString)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("issue not found: %s", issueIDString)), nil
	}

	worktreePath, worktreeBranch, reusedWorktree, err := d.ensureConflictWorktree(ctx, projectID, issueIDString, task.Title, body.Worktree)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, issueIDString, worktreePath, worktreeBranch)

	canonicalSessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope(projectID), issueID).String()
	sessionName, reusedSession, err := d.ensureConflictSession(ctx, projectID, issueIDString, canonicalSessionID, worktreePath)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if err := d.recordConflictSessionAttached(ctx, req, projectID, canonicalSessionID, issueIDString, reusedSession); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record conflict session projection: %v", err)), nil
	}

	reusedWindow, err := d.tmux.EnsureWindow(ctx, sessionName, sessionConflictWindowName, worktreePath)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		prompt = buildConflictResolutionPrompt(issueIDString, conflictFiles)
	}
	launchCommand := d.buildSessionLaunchCommand(projectID, issueIDString, canonicalSessionID, body.Yolo, body.ImagePaths, prompt)
	if err := d.tmux.SendKeys(ctx, sessionName+":"+sessionConflictWindowName, launchCommand); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}

	resp := d.successResponse(req)
	out := protocol.SessionResolveConflictResponseBody{
		ProjectID:     naming.ProjectID(projectID),
		IssueID:       issueID,
		SessionID:     naming.SessionID(canonicalSessionID),
		Worktree:      worktreePath,
		WindowName:    sessionConflictWindowName,
		ConflictFiles: conflictFiles,
		ReusedSession: reusedSession,
		ReusedWindow:  reusedWindow,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal response body: %v", err)), nil
	}
	resp.Body = encoded
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session conflict resolution launched",
			"project_id", projectID,
			"issue_id", issueIDString,
			"session_id", canonicalSessionID,
			"tmux_session", sessionName,
			"window", sessionConflictWindowName,
			"worktree", worktreePath,
			"reused_session", reusedSession,
			"reused_window", reusedWindow,
			"reused_worktree", reusedWorktree,
		)
	}
	return resp, nil
}

func (d *Daemon) ensureConflictWorktree(ctx context.Context, projectID, issueID, issueTitle, requestedWorktree string) (path, branch string, reused bool, err error) {
	if worktree := strings.TrimSpace(requestedWorktree); worktree != "" {
		return filepath.Clean(worktree), "", true, nil
	}
	worktreeManager := d.worktreeManagerForProject(projectID)
	if worktreeManager == nil {
		return "", "", false, errors.New("worktree manager unavailable")
	}
	if worktree, getErr := worktreeManager.Get(ctx, issueID); getErr == nil {
		return worktree.Path, worktree.Branch, true, nil
	}

	baseBranch := d.baseBranchForProject(projectID)
	worktree, createErr := worktreeManager.CreateWithTitle(ctx, issueID, issueTitle, baseBranch)
	if createErr != nil {
		if recoveredWorktree, recoverErr := worktreeManager.Get(ctx, issueID); recoverErr == nil {
			return recoveredWorktree.Path, recoveredWorktree.Branch, true, nil
		}
		return "", "", false, createErr
	}
	if err := d.runWorktreeInitCommands(ctx, projectID, worktree.Path); err != nil {
		return "", "", false, fmt.Errorf("worktree init failed: %w", err)
	}
	return worktree.Path, worktree.Branch, false, nil
}

func (d *Daemon) ensureConflictSession(ctx context.Context, projectID, issueID, canonicalSessionID, worktreePath string) (sessionName string, reused bool, err error) {
	names, err := d.tmuxSessionNamesForIssue(ctx, projectID, issueID, canonicalSessionID, daemonInvariantSourceTmux)
	if err != nil {
		return "", false, err
	}
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), canonicalSessionID) {
			return strings.TrimSpace(name), true, nil
		}
	}
	if len(names) > 0 {
		return strings.TrimSpace(names[0]), true, nil
	}
	if d.tmux == nil {
		return "", false, errors.New("tmux service unavailable")
	}
	if err := d.tmux.NewSession(ctx, canonicalSessionID, worktreePath); err != nil {
		return "", false, err
	}
	return canonicalSessionID, false, nil
}

func (d *Daemon) recordConflictSessionAttached(ctx context.Context, req protocol.RequestEnvelope, projectID, sessionID, issueID string, reusedSession bool) error {
	if !reusedSession {
		return d.applySessionLifecycleTransition(ctx, req, projectID, sessionID, issueID, daemonhandlers.CommandSessionStart)
	}
	err := d.applySessionLifecycleTransition(ctx, req, projectID, sessionID, issueID, daemonhandlers.CommandSessionAttach)
	if err == nil || !errors.Is(err, daemonstate.ErrInvalidTransition) {
		return err
	}
	if d.sessionStore == nil {
		return errors.New("session store unavailable")
	}
	event, forceErr := d.sessionStore.ForceUpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached)
	if forceErr != nil {
		return forceErr
	}
	d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(ctx, projectID, req.Meta, event.Session)
	return nil
}

func normalizeConflictFiles(files []string) []string {
	seen := make(map[string]struct{}, len(files))
	out := make([]string, 0, len(files))
	for _, file := range files {
		trimmed := strings.TrimSpace(file)
		if trimmed == "" {
			continue
		}
		cleaned := filepath.Clean(trimmed)
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func (d *Daemon) mutationFreshnessErrorResponse(req protocol.RequestEnvelope, err error) protocol.ResponseEnvelope {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return d.errorResponse(req, protocol.ErrorCodeTimeout, fmt.Sprintf("refresh runtime state before mutation: %v", err))
	}
	return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("refresh runtime state before mutation: %v", err))
}

func isRuntimeMutationFreshnessTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "wait runtime reconcile") &&
		strings.Contains(message, "deadline exceeded")
}

func (d *Daemon) writeSessionStopProjection(projectID, sessionID, issueID string) error {
	if d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return nil
	}

	projectID = d.canonicalProjectID(projectID)
	sessionID = strings.TrimSpace(sessionID)
	issueID = strings.TrimSpace(issueID)
	if sessionID == "" {
		return errors.New("missing session_id for stop projection")
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
	if err := d.sessionRuntimeStateStoreIfConfigured(projectID).UpsertSessionState(ctx, projectID, session); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("write-through stop session runtime state failed",
				"project_id", projectID,
				"session_id", sessionID,
				"issue_id", issueID,
				"error", err,
			)
		}
		return err
	}
	return nil
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
	taskMap := make(map[naming.IssueID]domain.Task, len(tasks))
	for _, task := range tasks {
		taskMap[task.ID] = task
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
		issueIDRaw := name
		if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(name, d.sessionNamingScope(cmd.ProjectID)); ok {
			issueIDRaw = parsedIssueID
		}
		issueID, parseErr := naming.ParseIssueID(issueIDRaw)
		task, ok := taskMap[issueID]
		if parseErr != nil {
			ok = false
		}
		status := "unknown"
		title := "(not in issues)"
		if ok {
			status = string(task.Status)
			title = task.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", issueIDRaw, status, title)
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
		cmd.ProjectID = req.Meta.ProjectID.String()
	}
	cmd.ProjectID = d.canonicalProjectID(cmd.ProjectID)
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
	d.runtimeProjectionStateWriter().PersistSessionProjectionAndPublish(context.Background(), projectID, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, event.Session)
	return nil
}

func (d *Daemon) reconcileTmuxAndDaemonSessions(ctx context.Context, projectID, sessionID string) (sessionRecoveryResult, error) {
	result := sessionRecoveryResult{}
	if d == nil || d.sessionStore == nil || d.tmux == nil {
		return result, nil
	}
	projectID = d.canonicalProjectID(projectID)
	worktreeManager := d.worktreeManagerForProject(projectID)
	if worktreeManager == nil {
		return result, nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon session reconcile started", "project_id", projectID, "target_issue_id", sessionID)
	}
	validIssueKeys, issueValidationEnabled, err := d.reconcileIssueKeyIndex(ctx, projectID)
	if err != nil {
		return result, err
	}
	isValidIssueKey := func(issueKey string) bool {
		if !issueValidationEnabled {
			return true
		}
		_, ok := validIssueKeys[issueKey]
		return ok
	}
	pruneInvalidSessionProjection := func(session daemonstate.Session, issueID string) {
		runtimeStore := d.sessionRuntimeStateStoreIfConfigured(projectID)
		if runtimeStore == nil {
			return
		}
		sessionIDToDelete := strings.TrimSpace(session.ID)
		if sessionIDToDelete == "" {
			sessionIDToDelete = naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
		}
		if sessionIDToDelete == "" {
			return
		}
		if err := runtimeStore.DeleteSessionState(ctx, projectID, sessionIDToDelete); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session reconciliation failed to prune invalid desired session",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionIDToDelete,
				"error", err,
			)
			return
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("session reconciliation pruned invalid desired session",
				"project_id", projectID,
				"issue_id", issueID,
				"session_id", sessionIDToDelete,
			)
		}
	}

	source := d.sourceForSessionInvariant(sessionInvariantSessionReconcile)
	tmuxSessions := []string{}
	if usesTmuxSource(source) {
		tmuxSessions, err = d.tmux.ListSessions(ctx)
		if err != nil {
			return result, err
		}
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

	snapshotSessions := []daemonstate.Session{}
	if usesProjectionSource(source) {
		snapshotSessions, err = d.sessionSnapshotForReconcile(ctx, projectID)
		if err != nil {
			return result, err
		}
	}
	for _, session := range snapshotSessions {
		issueID := sessionProjectionIssueID(session, namingScope)
		issueKey := sessionKey(issueID)
		if targetIssueKey != "" && issueKey != targetIssueKey {
			continue
		}
		if issueKey == "" || !isValidIssueKey(issueKey) {
			pruneInvalidSessionProjection(session, issueID)
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
		wt, getErr := worktreeManager.Get(ctx, issueID)
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
		if sendErr := d.tmux.SendKeys(ctx, canonicalSessionID, d.buildSessionLaunchCommand(projectID, issueID, canonicalSessionID, false, nil, "")); sendErr != nil && d.cfg.Logger != nil {
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

	snapshotByIssueKey := sessionProjectionForReconcileByIssueKey(snapshotSessions, namingScope)

	for issueKey := range tmuxSet {
		sessionIDInTmux := tmuxNameByIssueKey[issueKey]
		session, ok := snapshotByIssueKey[issueKey]
		if !isValidIssueKey(issueKey) {
			continue
		}
		issueID, parsed := naming.ParseIssueIDFromSessionName(sessionIDInTmux, namingScope)
		if !parsed {
			issueID = sessionIDInTmux
		}
		if d.isSessionStopPending(projectID, issueID) {
			continue
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

		if session.State == daemonstate.SessionStateStopped {
			if err := d.tmux.KillSession(ctx, sessionIDInTmux); err != nil {
				if d.cfg.Logger != nil {
					d.cfg.Logger.Warn("session reconciliation failed to kill stopped session",
						"project_id", projectID,
						"issue_id", issueID,
						"session_id", sessionIDInTmux,
						"error", err,
					)
				}
				continue
			}
			stopped := session
			stopped.ObservedState = daemonstate.SessionStateStopped
			stopped.UpdatedAt = time.Now().UTC()
			if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, stopped); err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Debug("persist stopped session observation failed",
					"project_id", projectID,
					"issue_id", issueID,
					"session_id", sessionIDInTmux,
					"error", err,
				)
			}
			continue
		}

		d.ensureSessionWorktreeProjection(ctx, projectID, issueID)

		switch session.State {
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

func (d *Daemon) reconcileIssueKeyIndex(ctx context.Context, projectID string) (map[string]struct{}, bool, error) {
	if d == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID)) == "" {
		return nil, false, nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, false, nil
	}
	tasks, err := issueClient.List(ctx)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("session reconciliation issue validation unavailable",
				"project_id", projectID,
				"error", err,
			)
		}
		return nil, false, nil
	}
	index := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		key := sessionKey(task.ID.String())
		if key == "" {
			continue
		}
		index[key] = struct{}{}
	}
	return index, true, nil
}

func (d *Daemon) sessionSnapshotForReconcile(ctx context.Context, projectID string) ([]daemonstate.Session, error) {
	if d == nil {
		return nil, nil
	}
	projectID = d.canonicalProjectID(projectID)
	if d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return []daemonstate.Session{}, nil
	}
	return d.sessionProjectionSnapshot(ctx, projectID)
}

func (d *Daemon) ensureSessionWorktreeProjection(ctx context.Context, projectID, issueID string) {
	if d == nil || d.worktreeRuntimeStateStore(projectID) == nil {
		return
	}
	projectID = protocol.NormalizeProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return
	}
	if _, found, err := d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByIssueID(ctx, projectID, issueID); err == nil && found {
		return
	}

	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectLocked(projectID))
	if repoDir == "" {
		return
	}
	worktreePath := filepath.Join(filepath.Dir(repoDir), fmt.Sprintf("%s-%s", filepath.Base(repoDir), issueID))
	info, err := os.Stat(worktreePath)
	if err != nil || !info.IsDir() {
		return
	}

	branch := ""
	if d.git != nil {
		if currentBranch, branchErr := d.git.CurrentBranch(ctx, worktreePath); branchErr == nil {
			branch = strings.TrimSpace(currentBranch)
		}
	}
	rev := d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, issueID, worktreePath, branch)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info(
			"backfilled session worktree projection",
			"project_id", projectID,
			"issue_id", issueID,
			"worktree", worktreePath,
			"branch", branch,
			"revision", rev,
		)
	}
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
	snapshotByKey := sessionProjectionLatestByIssueKey(snapshotSessions, namingScope)
	var projectionSessions []daemonstate.Session
	projectionByKey := map[string]daemonstate.Session{}
	if d.sessionRuntimeStateStoreIfConfigured(projectID) != nil {
		cachedSessions, err := d.sessionRuntimeStateStoreIfConfigured(projectID).ListSessionStates(ctx, projectID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("failed to load cached session runtime state while enriching tasks", "project_id", projectID, "error", err)
			}
		} else {
			projectionSessions = cachedSessions
			projectionByKey = sessionProjectionLatestByIssueKey(projectionSessions, namingScope)
		}
	}
	sessionByKey := sessionProjectionForTaskDisplayByIssueKey(snapshotByKey, projectionByKey)

	for i := range tasks {
		taskID := tasks[i].ID
		taskKey := sessionKey(taskID.String())
		if _, ok := tmuxSet[taskKey]; !ok {
			continue
		}

		state := domain.SessionBusy
		var startedAt *time.Time
		snapshotSession, snapshotOK := snapshotByKey[taskKey]
		projectionSession, projectionOK := projectionByKey[taskKey]
		session, ok := sessionByKey[taskKey]
		if ok {
			startedAt = sessionProjectionStartedAtForTaskDisplay(snapshotSession, snapshotOK, projectionSession, projectionOK)
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
			IssueID:   naming.IssueID(taskID),
			State:     state,
			StartedAt: startedAt,
		}
	}

	return tasks
}

func (d *Daemon) listTmuxSessionsCacheFirst(ctx context.Context, projectID string) ([]string, error) {
	projectID = d.canonicalProjectID(projectID)

	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return []string{}, nil
	}
	cachedSessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	cachedActive := d.activeSessionIDsFromProjection(projectID, cachedSessions)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("using projection-backed session runtime state",
			"project_id", projectID,
			"cached_sessions", len(cachedActive),
		)
	}
	return cachedActive, nil
}

func (d *Daemon) activeSessionIDsFromProjection(projectID string, sessions []daemonstate.Session) []string {
	active := make([]string, 0, len(sessions))
	for _, session := range sessions {
		observed := session.ObservedState
		if strings.TrimSpace(string(observed)) == "" {
			observed = session.State
		}
		if observed == daemonstate.SessionStateStopped {
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
		active = append(active, session.ID)
	}
	return active
}

func (d *Daemon) listProjectionSessionsOnly(ctx context.Context, projectID string) ([]string, error) {
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return []string{}, nil
	}
	cachedSessions, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return d.activeSessionIDsFromProjection(projectID, cachedSessions), nil
}

func (d *Daemon) refreshSessionRuntimeState(ctx context.Context, projectID string) error {
	if d == nil || d.tmux == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil || d.sessionStore == nil {
		return nil
	}
	tmuxSessions, err := d.tmux.ListSessionInfos(ctx)
	if err != nil {
		return err
	}
	return d.persistTmuxSessionRuntimeState(ctx, projectID, tmuxSessions)
}

func (d *Daemon) refreshStoppedSessionRuntimeState(ctx context.Context, projectID, issueID, sessionID string) error {
	if d == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil || d.sessionStore == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	sessionID = strings.TrimSpace(sessionID)
	if issueID == "" && sessionID == "" {
		return nil
	}

	var session daemonstate.Session
	if issueID != "" {
		if existing, found, err := d.sessionRuntimeStateStoreIfConfigured(projectID).GetSessionStateByIssueID(ctx, projectID, issueID); err != nil {
			return err
		} else if found {
			session = existing
		}
	}
	if strings.TrimSpace(session.ID) == "" {
		session = daemonstate.Session{
			ID:      sessionID,
			IssueID: issueID,
		}
	}
	if strings.TrimSpace(session.ID) == "" {
		session.ID = sessionID
	}
	if strings.TrimSpace(session.IssueID) == "" {
		session.IssueID = issueID
	}
	session.State = daemonstate.SessionStateStopped
	session.ObservedState = daemonstate.SessionStateStopped
	session.UpdatedAt = time.Now().UTC()
	return d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, session)
}

func (d *Daemon) persistTmuxSessionRuntimeState(ctx context.Context, projectID string, tmuxSessions []tmux.SessionInfo) error {
	if d.sessionRuntimeStateStoreIfConfigured(projectID) == nil || d.sessionStore == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)

	namingScope := d.sessionNamingScope(projectID)
	existingSessions, err := d.sessionRuntimeStateStoreIfConfigured(projectID).ListSessionStates(ctx, projectID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load cached session runtime-state snapshot failed", "project_id", projectID, "error", err)
		}
	}
	existingByIssueKey := sessionProjectionForTmuxHydrationByIssueKey(existingSessions, namingScope)
	liveSessionIDs := make(map[string]struct{}, len(tmuxSessions))
	for _, info := range tmuxSessions {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		liveSessionIDs[name] = struct{}{}
		issueID, ok := naming.ParseIssueIDFromSessionName(name, namingScope)
		if !ok {
			issueID = name
		}
		issueKey := sessionKey(issueID)
		row := daemonstate.Session{
			ID:            name,
			IssueID:       issueID,
			State:         daemonstate.SessionStateAttached,
			ObservedState: daemonstate.SessionStateAttached,
			StartedAt:     info.CreatedAt,
			UpdatedAt:     time.Now().UTC(),
		}
		if existing, exists := existingByIssueKey[issueKey]; exists {
			row.State = existing.State
			row.ObservedState = daemonstate.SessionStateAttached
			if (row.StartedAt == nil || row.StartedAt.IsZero()) && existing.StartedAt != nil && !existing.StartedAt.IsZero() {
				started := existing.StartedAt.UTC()
				row.StartedAt = &started
			}
		}
		if row.StartedAt == nil || row.StartedAt.IsZero() {
			started := row.UpdatedAt.UTC()
			row.StartedAt = &started
		}
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, row); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist live tmux session runtime state failed",
				"project_id", projectID,
				"session_id", row.ID,
				"issue_id", row.IssueID,
				"error", err,
			)
		}
	}
	for _, session := range existingSessions {
		if _, live := liveSessionIDs[strings.TrimSpace(session.ID)]; live {
			continue
		}
		stopped := session
		stopped.ObservedState = daemonstate.SessionStateStopped
		stopped.UpdatedAt = time.Now().UTC()
		if err := d.runtimeProjectionStateWriter().PersistSessionProjection(ctx, projectID, stopped); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist missing tmux session as stopped failed",
				"project_id", projectID,
				"session_id", stopped.ID,
				"issue_id", stopped.IssueID,
				"error", err,
			)
		}
	}
	return nil
}

func (d *Daemon) buildSessionLaunchCommand(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt string) string {
	projectCfg := d.runtimeConfigForProject(projectID)
	toolCommand := d.buildCLIToolCommand(projectID, issueID, sessionID, yolo, imagePaths, initialPrompt)
	commands := make([]string, 0, len(projectCfg.SessionInitCommands)+2)
	for _, initCmd := range projectCfg.SessionInitCommands {
		trimmed := strings.TrimSpace(initCmd)
		if trimmed != "" {
			commands = append(commands, trimmed)
		}
	}
	commands = append(commands, toolCommand)

	shell := strings.TrimSpace(projectCfg.SessionShell)
	if shell == "" {
		shell = appconfig.DefaultSessionShell()
	}
	inner := strings.Join(commands, "; ")
	inner = inner + "; exec " + shell
	return fmt.Sprintf("%s -i -c %s", shell, singleQuoteForShell(inner))
}

func (d *Daemon) runWorktreeInitCommands(ctx context.Context, projectID string, worktreePath string) error {
	commands := d.runtimeConfigForProject(projectID).WorktreeInitCommands
	if len(commands) == 0 {
		return nil
	}

	shell := strings.TrimSpace(d.runtimeConfigForProject(projectID).SessionShell)
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

func (d *Daemon) buildCLIToolCommand(projectID, issueID, sessionID string, yolo bool, imagePaths []string, initialPrompt string) string {
	tool := strings.TrimSpace(d.runtimeConfigForProject(projectID).CLITool)
	if tool == "" {
		tool = "claude"
	}

	parts := []string{
		fmt.Sprintf(`AZEDARACH_ISSUE_ID="%s"`, escapeForShellDoubleQuotes(issueID)),
		tool,
	}

	if strings.EqualFold(tool, "codex") {
		sanitizedIssueID := strings.TrimSpace(issueID)
		if sanitizedIssueID == "" {
			sanitizedIssueID = "unknown-issue"
		}
		sessionStartCommand := fmt.Sprintf("az notify --json session_start %s", sanitizedIssueID)
		userPromptSubmitCommand := fmt.Sprintf("az notify --json user_prompt_submit %s", sanitizedIssueID)
		preToolUseCommand := fmt.Sprintf("az notify --json pre_tool_use %s", sanitizedIssueID)
		postToolUseCommand := fmt.Sprintf("az notify --json post_tool_use %s", sanitizedIssueID)
		stopCommand := fmt.Sprintf("az notify --json stop %s", sanitizedIssueID)
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
	override := fmt.Sprintf(`%s=[{hooks=[{type="command",command="%s"}]}]`, key, tomlCommand)
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

func buildConflictResolutionPrompt(issueID string, conflictFiles []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolve merge conflicts for issue %s.\n\n", issueID)
	b.WriteString("Start by running `az prime`. Inspect the conflicted files, resolve conflict markers, and run the focused checks needed to verify the merge.\n")
	if len(conflictFiles) > 0 {
		b.WriteString("\nConflicted files:\n")
		for _, file := range conflictFiles {
			fmt.Fprintf(&b, "- %s\n", sanitizePromptInline(file, 240))
		}
	}
	b.WriteString("\nDo not create a PR or push unless explicitly asked. Leave a concise summary of resolved files and validation results.")
	return b.String()
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
