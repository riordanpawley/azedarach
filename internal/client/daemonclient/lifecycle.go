package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

const (
	CommandSessionStart          = "session.start"
	CommandSessionAttach         = "session.attach"
	CommandSessionPause          = "session.pause"
	CommandSessionResume         = "session.resume"
	CommandSessionStop           = "session.stop"
	CommandSessionStatus         = "session.status"
	CommandRuntimeReconcile      = "runtime.reconcile"
	CommandRuntimeReconcileIssue = "runtime.reconcile_issue"
	CommandDevServerStart        = "devserver.start"
	CommandDevServerStop         = "devserver.stop"
	CommandDevServerStatus       = "devserver.status"
	CommandDevServerList         = "devserver.list"
	CommandWorktreeList          = "worktree.list"
	CommandWorktreeRemove        = "worktree.remove"
)

type sessionCommandBody struct {
	ProjectID  naming.ProjectID `json:"project_id"`
	SessionID  naming.SessionID `json:"session_id,omitempty"`
	BaseBranch string           `json:"base_branch,omitempty"`
	Yolo       bool             `json:"yolo,omitempty"`
	StartWork  *bool            `json:"start_work,omitempty"`
	ImagePaths []string         `json:"image_paths,omitempty"`
}

// StartSessionParams captures lifecycle start options in a single payload
// to avoid brittle positional argument expansion at callsites.
type StartSessionParams struct {
	IssueID    string
	BaseBranch string
	Yolo       bool
	StartWork  *bool
	ImagePaths []string
}

type commandOutputBody struct {
	Output string `json:"output"`
}

type devServerCommandBody struct {
	IssueID naming.IssueID `json:"issue_id"`
}

type devServerResultBody struct {
	IssueID naming.IssueID   `json:"issue_id"`
	Server  devserver.Server `json:"server"`
}

type devServerListBody struct {
	Servers []devserver.Server `json:"servers"`
}

type worktreeListBody struct {
	ProjectID naming.ProjectID  `json:"project_id"`
	Worktrees []worktreePayload `json:"worktrees"`
}

type worktreePayload struct {
	Path    string         `json:"path"`
	Branch  string         `json:"branch"`
	IssueID naming.IssueID `json:"issue_id"`
}

type worktreeCommandBody struct {
	ProjectID naming.ProjectID `json:"project_id"`
	IssueID   naming.IssueID   `json:"issue_id,omitempty"`
	Force     bool             `json:"force,omitempty"`
}

// RuntimeReconcileResult captures the runtime repair summary returned by the daemon.
type RuntimeReconcileResult struct {
	ProjectID             naming.ProjectID `json:"project_id"`
	WorktreesRefreshed    int              `json:"worktrees_refreshed"`
	RecreatedTmuxSessions int              `json:"recreated_tmux_sessions"`
	AlignedDaemonSessions int              `json:"aligned_daemon_sessions"`
}

type longRunningResultEnvelope struct {
	OperationID *string         `json:"operation_id,omitempty"`
	State       *string         `json:"state,omitempty"`
	Output      *string         `json:"output,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

type OperationPendingError struct {
	Command     string
	OperationID string
	State       protocol.OperationState
}

func (e *OperationPendingError) Error() string {
	if e == nil {
		return ""
	}
	if e.OperationID != "" {
		return fmt.Sprintf("%s pending: operation %s is %s", e.Command, e.OperationID, e.State)
	}
	return fmt.Sprintf("%s pending: operation is %s", e.Command, e.State)
}

func (c *Client) commandOutput(ctx context.Context, command string, body any) (string, error) {
	resp, err := c.commandJSONResponse(ctx, command, body)
	if err != nil {
		return "", err
	}
	return decodeCommandOutput(resp.Body)
}

func (c *Client) projectRoute() string {
	return c.projectID.String()
}

// StartSession asks the daemon to start one session for issue/task id.
func (c *Client) StartSession(ctx context.Context, params StartSessionParams) (string, error) {
	issueID, err := parseIssueID(params.IssueID)
	if err != nil {
		return "", err
	}
	return c.commandOutput(ctx, CommandSessionStart, sessionCommandBody{
		ProjectID:  c.projectID,
		SessionID:  naming.SessionID(issueID),
		BaseBranch: params.BaseBranch,
		Yolo:       params.Yolo,
		StartWork:  params.StartWork,
		ImagePaths: params.ImagePaths,
	})
}

// StopSession asks the daemon to stop one session for issue/task id.
func (c *Client) StopSession(ctx context.Context, issueID string) (string, error) {
	parsedIssueID, err := parseIssueID(issueID)
	if err != nil {
		return "", err
	}
	return c.commandOutput(ctx, CommandSessionStop, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: naming.SessionID(parsedIssueID),
	})
}

// AttachSession asks the daemon to attach to one session for issue/task id.
func (c *Client) AttachSession(ctx context.Context, issueID string) (string, error) {
	parsedIssueID, err := parseIssueID(issueID)
	if err != nil {
		return "", err
	}
	return c.commandOutput(ctx, CommandSessionAttach, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: naming.SessionID(parsedIssueID),
	})
}

// PauseSession marks one issue/session as paused in daemon lifecycle state.
func (c *Client) PauseSession(ctx context.Context, issueID string) (string, error) {
	parsedIssueID, err := parseIssueID(issueID)
	if err != nil {
		return "", err
	}
	return c.commandOutput(ctx, CommandSessionPause, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: naming.SessionID(parsedIssueID),
	})
}

// ResumeSession marks one issue/session as attached (active) in daemon lifecycle state.
func (c *Client) ResumeSession(ctx context.Context, issueID string) (string, error) {
	parsedIssueID, err := parseIssueID(issueID)
	if err != nil {
		return "", err
	}
	return c.commandOutput(ctx, CommandSessionResume, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: naming.SessionID(parsedIssueID),
	})
}

// SessionStatus asks the daemon for the current session status view.
func (c *Client) SessionStatus(ctx context.Context, issueID string) (string, error) {
	var sessionID naming.SessionID
	trimmedIssueID := strings.TrimSpace(issueID)
	if trimmedIssueID != "" {
		parsedIssueID, err := parseIssueID(trimmedIssueID)
		if err != nil {
			return "", err
		}
		sessionID = naming.SessionID(parsedIssueID)
	}
	return c.commandOutput(ctx, CommandSessionStatus, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: sessionID,
	})
}

// ReconcileRuntime asks the daemon to repair runtime, session, and worktree consistency for the current project route.
func (c *Client) ReconcileRuntime(ctx context.Context) (RuntimeReconcileResult, error) {
	var out RuntimeReconcileResult
	if err := c.commandJSON(ctx, CommandRuntimeReconcile, protocol.RuntimeReconcileRequestBody{
		ProjectID: c.projectID,
	}, &out); err != nil {
		return RuntimeReconcileResult{}, err
	}
	return out, nil
}

// ReconcileRuntimeIssues asks the daemon to repair runtime state for specific issues in the current project route.
func (c *Client) ReconcileRuntimeIssues(ctx context.Context, issueIDs []string) (RuntimeReconcileResult, error) {
	parsed := make([]naming.IssueID, 0, len(issueIDs))
	seen := make(map[string]struct{}, len(issueIDs))
	for _, raw := range issueIDs {
		issueID, err := parseIssueID(raw)
		if err != nil {
			return RuntimeReconcileResult{}, err
		}
		key := issueID.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		parsed = append(parsed, issueID)
	}
	if len(parsed) == 0 {
		return RuntimeReconcileResult{}, fmt.Errorf("at least one issue id is required")
	}

	var out RuntimeReconcileResult
	if err := c.commandJSON(ctx, CommandRuntimeReconcileIssue, protocol.RuntimeReconcileIssueRequestBody{
		ProjectID: c.projectID,
		IssueIDs:  parsed,
	}, &out); err != nil {
		return RuntimeReconcileResult{}, err
	}
	return out, nil
}

// DevServerStatus returns daemon-owned devserver status for one issue.
func (c *Client) DevServerStatus(ctx context.Context, issueID string) (devserver.Server, error) {
	parsedIssueID, err := parseIssueID(issueID)
	if err != nil {
		return devserver.Server{}, err
	}
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStatus, devServerCommandBody{IssueID: parsedIssueID}, &out); err != nil {
		return devserver.Server{}, err
	}
	return out.Server, nil
}

// ListDevServers returns the daemon-owned devserver inventory for the current project route.
func (c *Client) ListDevServers(ctx context.Context) ([]devserver.Server, error) {
	var out devServerListBody
	if err := c.commandJSON(ctx, CommandDevServerList, struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.Servers, nil
}

// StartDevServer asks daemon to start one devserver.
func (c *Client) StartDevServer(ctx context.Context, issueID string) (devserver.Server, error) {
	parsedIssueID, err := parseIssueID(issueID)
	if err != nil {
		return devserver.Server{}, err
	}
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStart, devServerCommandBody{IssueID: parsedIssueID}, &out); err != nil {
		return devserver.Server{}, err
	}
	return out.Server, nil
}

// StopDevServer asks daemon to stop one devserver.
func (c *Client) StopDevServer(ctx context.Context, issueID string) (devserver.Server, error) {
	parsedIssueID, err := parseIssueID(issueID)
	if err != nil {
		return devserver.Server{}, err
	}
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStop, devServerCommandBody{IssueID: parsedIssueID}, &out); err != nil {
		return devserver.Server{}, err
	}
	return out.Server, nil
}

// ToggleDevServer toggles running/stopped state through daemon command authority.
func (c *Client) ToggleDevServer(ctx context.Context, issueID string) (devserver.Server, error) {
	srv, err := c.DevServerStatus(ctx, issueID)
	if err != nil {
		var cmdErr *CommandError
		if errors.As(err, &cmdErr) && cmdErr.Code == protocol.ErrorCodeInvalidRequest {
			return c.StartDevServer(ctx, issueID)
		}
		return devserver.Server{}, err
	}
	if srv.Status == "running" {
		return c.StopDevServer(ctx, issueID)
	}
	return c.StartDevServer(ctx, issueID)
}

// RestartDevServer restarts one devserver through daemon command authority.
func (c *Client) RestartDevServer(ctx context.Context, issueID string) (devserver.Server, error) {
	_, err := c.StopDevServer(ctx, issueID)
	if err != nil {
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) || (cmdErr.Code != protocol.ErrorCodeInvalidRequest && cmdErr.Code != protocol.ErrorCodeConflict) {
			return devserver.Server{}, err
		}
	}
	return c.StartDevServer(ctx, issueID)
}

// ListWorktrees returns daemon-owned worktrees for the current project route.
func (c *Client) ListWorktrees(ctx context.Context) ([]git.Worktree, error) {
	var out worktreeListBody
	if err := c.commandJSON(ctx, CommandWorktreeList, struct {
		ProjectID naming.ProjectID `json:"project_id"`
	}{ProjectID: c.projectID}, &out); err != nil {
		return nil, err
	}

	worktrees := make([]git.Worktree, 0, len(out.Worktrees))
	for _, wt := range out.Worktrees {
		worktrees = append(worktrees, git.Worktree{
			Path:    wt.Path,
			Branch:  wt.Branch,
			IssueID: wt.IssueID.String(),
		})
	}
	return worktrees, nil
}

// RemoveWorktree asks the daemon to remove one worktree for an issue in the current project route.
func (c *Client) RemoveWorktree(ctx context.Context, issueID string) error {
	return c.RemoveWorktreeWithOptions(ctx, issueID, false)
}

// RemoveWorktreeWithOptions asks the daemon to remove one worktree for an issue in the current project route.
func (c *Client) RemoveWorktreeWithOptions(ctx context.Context, issueID string, force bool) error {
	parsedIssueID, err := parseIssueID(issueID)
	if err != nil {
		return err
	}
	return c.commandJSON(ctx, CommandWorktreeRemove, worktreeCommandBody{
		ProjectID: c.projectID,
		IssueID:   parsedIssueID,
		Force:     force,
	}, nil)
}

// CleanupOrphanedWorktrees asks the daemon to remove orphaned worktrees for the current project route.
func (c *Client) CleanupOrphanedWorktrees(ctx context.Context) (int, error) {
	resp, err := c.commandJSONResponse(ctx, protocol.CommandWorktreeCleanupOrphaned, protocol.CleanupOrphanedRequestBody{
		ProjectID: c.projectID,
	})
	if err != nil {
		return 0, err
	}
	var out protocol.CleanupOrphanedResponseBody
	if err := decodeLongRunningJSON(protocol.CommandWorktreeCleanupOrphaned, resp.Body, &out); err != nil {
		return 0, err
	}
	return out.WorktreesRemoved, nil
}

func decodeCommandOutput(body []byte) (string, error) {
	if len(body) == 0 {
		return "", nil
	}

	var envelope longRunningResultEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil {
		if envelope.Output != nil {
			return *envelope.Output, nil
		}
		if (envelope.OperationID != nil || envelope.State != nil) && len(envelope.Result) > 0 {
			var out commandOutputBody
			if err := json.Unmarshal(envelope.Result, &out); err != nil {
				return "", err
			}
			return out.Output, nil
		}
		if pending := pendingOperationError("", envelope); pending != nil {
			return "", pending
		}
	}

	var out commandOutputBody
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	return out.Output, nil
}

func decodeLongRunningJSON(command string, body []byte, out any) error {
	if out == nil || len(body) == 0 {
		return nil
	}

	var envelope longRunningResultEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && (envelope.OperationID != nil || envelope.State != nil) && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("decode %s result: %w", command, err)
		}
		return nil
	}
	if pending := pendingOperationError(command, envelope); pending != nil {
		return pending
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", command, err)
	}
	return nil
}

func pendingOperationError(command string, envelope longRunningResultEnvelope) error {
	state := operationStateFromEnvelope(envelope)
	if state == "" || isTerminalOperationState(state) {
		return nil
	}
	return &OperationPendingError{
		Command:     command,
		OperationID: stringValue(envelope.OperationID),
		State:       state,
	}
}

func operationStateFromEnvelope(envelope longRunningResultEnvelope) protocol.OperationState {
	if envelope.State == nil {
		return ""
	}
	state := protocol.OperationState(*envelope.State)
	if !state.Valid() {
		return ""
	}
	return state
}

func isTerminalOperationState(state protocol.OperationState) bool {
	switch state {
	case protocol.OperationStateDone,
		protocol.OperationStateFailed,
		protocol.OperationStateCancelled:
		return true
	default:
		return false
	}
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func parseIssueID(raw string) (naming.IssueID, error) {
	issueID, err := naming.ParseIssueID(raw)
	if err != nil {
		return "", fmt.Errorf("invalid issue id: %w", err)
	}
	return issueID, nil
}
