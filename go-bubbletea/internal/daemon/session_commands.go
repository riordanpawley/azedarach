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
)

type sessionCommandBody struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type sessionRecoveryResult struct {
	RecreatedTmuxSessions int `json:"recreated_tmux_sessions"`
	AlignedDaemonSessions int `json:"aligned_daemon_sessions"`
}

func (d *Daemon) decodeSessionRequest(req protocol.RequestEnvelope) (sessionCommandBody, protocol.ResponseEnvelope, bool) {
	var cmd sessionCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return sessionCommandBody{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), false
	}
	if cmd.ProjectID == "" {
		cmd.ProjectID = req.Meta.ProjectID
	}
	if cmd.ProjectID == "" {
		cmd.ProjectID = "default"
	}
	if cmd.SessionID == "" {
		return sessionCommandBody{}, d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: project_id/session_id"), false
	}
	return cmd, protocol.ResponseEnvelope{}, true
}

func (d *Daemon) handleSessionStart(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if exists {
		return d.errorResponse(req, protocol.ErrorCodeConflict, fmt.Sprintf("session already exists: %s (use 'az attach %s' to connect)", cmd.SessionID, cmd.SessionID)), nil
	}
	tasks, err := d.issues.Search(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if len(tasks) == 0 {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("issue not found: %s", cmd.SessionID)), nil
	}
	baseBranch := cmd.BaseBranch
	if baseBranch == "" {
		baseBranch = d.cfg.BaseBranch
	}
	worktree, err := d.worktree.Create(ctx, cmd.SessionID, baseBranch)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if err := d.tmux.NewSession(ctx, cmd.SessionID, worktree.Path); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if err := d.tmux.SendKeys(ctx, cmd.SessionID, d.cfg.CLITool); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	_ = d.issues.Update(ctx, cmd.SessionID, domain.StatusInProgress)
	if err := d.applySessionLifecycleTransition(
		ctx,
		req,
		cmd.ProjectID,
		cmd.SessionID,
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
		fmt.Sprintf("  To attach: az attach %s", cmd.SessionID),
		fmt.Sprintf("  Or run:    tmux attach-session -t %s", cmd.SessionID),
		"",
	}, "\n")
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionAttach(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	exists, err := d.tmux.HasSession(ctx, cmd.SessionID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !exists {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("session not found: %s (use 'az start %s' to create)", cmd.SessionID, cmd.SessionID)), nil
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
		daemonhandlers.CommandSessionAttach,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session attach transition: %v", err)), nil
	}
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionStop(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req)
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
		daemonhandlers.CommandSessionStop,
	); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("record session stop transition: %v", err)), nil
	}
	output := strings.Join([]string{
		fmt.Sprintf("Killing session: %s", cmd.SessionID),
		fmt.Sprintf("✓ Session killed: %s", cmd.SessionID),
		"  Note: Worktree is preserved. Use 'git worktree remove' to clean up.",
		"",
	}, "\n")
	return d.commandOutput(req, output), nil
}

func (d *Daemon) handleSessionStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	cmd, _, ok := d.decodeSessionRequest(req)
	if !ok {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid session request"), nil
	}
	if _, err := d.reconcileTmuxAndDaemonSessions(ctx, cmd.ProjectID, cmd.SessionID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("session reconciliation during session.status failed", "project_id", cmd.ProjectID, "session_id", cmd.SessionID, "error", err)
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
		taskMap[task.ID] = task
	}
	if cmd.SessionID != "" {
		found := false
		for _, name := range tmuxSessions {
			if name == cmd.SessionID {
				found = true
				break
			}
		}
		if !found {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("no active session found for issue: %s", cmd.SessionID)), nil
		}
		tmuxSessions = []string{cmd.SessionID}
	}
	if len(tmuxSessions) == 0 {
		return d.commandOutput(req, "No active sessions\n"), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Active Sessions (%d):\n\n", len(tmuxSessions))
	b.WriteString("ISSUE ID\tSTATUS\tTITLE\n")
	b.WriteString("-------\t------\t-----\n")
	for _, name := range tmuxSessions {
		task, ok := taskMap[name]
		status := "unknown"
		title := "(not in issues)"
		if ok {
			status = string(task.Status)
			title = task.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", name, status, title)
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

	result, err := d.reconcileTmuxAndDaemonSessions(ctx, cmd.ProjectID, cmd.SessionID)
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
	for _, name := range tmuxSessions {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if sessionID != "" && name != sessionID {
			continue
		}
		tmuxSet[name] = struct{}{}
	}

	snapshot := d.sessionStore.ReadSnapshot(projectID)
	for id, session := range snapshot.Sessions {
		if sessionID != "" && id != sessionID {
			continue
		}
		if session.State == daemonstate.SessionStateStopped {
			continue
		}
		if _, ok := tmuxSet[id]; ok {
			continue
		}
		issueID := session.IssueID
		if issueID == "" {
			issueID = id
		}
		wt, getErr := d.worktree.Get(ctx, issueID)
		if getErr != nil {
			continue
		}
		if newErr := d.tmux.NewSession(ctx, id, wt.Path); newErr != nil {
			continue
		}
		_ = d.tmux.SendKeys(ctx, id, d.cfg.CLITool)
		tmuxSet[id] = struct{}{}
		result.RecreatedTmuxSessions++
	}

	for id := range tmuxSet {
		session, ok := snapshot.Sessions[id]
		issueID := id
		if ok && session.IssueID != "" {
			issueID = session.IssueID
		}
		if !ok {
			if _, err := d.sessionStore.UpsertSession(projectID, id, issueID, daemonstate.SessionStateStarting); err == nil {
				if _, err := d.sessionStore.UpsertSession(projectID, id, issueID, daemonstate.SessionStateAttached); err == nil {
					result.AlignedDaemonSessions++
				}
			}
			continue
		}

		switch session.State {
		case daemonstate.SessionStateStopped:
			if _, err := d.sessionStore.UpsertSession(projectID, id, issueID, daemonstate.SessionStateStarting); err == nil {
				if _, err := d.sessionStore.UpsertSession(projectID, id, issueID, daemonstate.SessionStateAttached); err == nil {
					result.AlignedDaemonSessions++
				}
			}
		case daemonstate.SessionStateStarting:
			if _, err := d.sessionStore.UpsertSession(projectID, id, issueID, daemonstate.SessionStateAttached); err == nil {
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
		if strings.TrimSpace(name) == "" {
			continue
		}
		tmuxSet[name] = struct{}{}
	}
	snapshot := d.sessionStore.ReadSnapshot(projectID)

	for i := range tasks {
		taskID := tasks[i].ID
		if _, ok := tmuxSet[taskID]; !ok {
			continue
		}

		state := domain.SessionBusy
		if session, ok := snapshot.Sessions[taskID]; ok {
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
