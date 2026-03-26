package daemonclient

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

const (
	CommandSessionStart    = "session.start"
	CommandSessionAttach   = "session.attach"
	CommandSessionStop     = "session.stop"
	CommandSessionStatus   = "session.status"
	CommandDevServerStart  = "devserver.start"
	CommandDevServerStop   = "devserver.stop"
	CommandDevServerStatus = "devserver.status"
	CommandWorktreeList    = "worktree.list"
	CommandWorktreeRemove  = "worktree.remove"
)

type sessionCommandBody struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type commandOutputBody struct {
	Output string `json:"output"`
}

type devServerCommandBody struct {
	IssueID string `json:"issue_id"`
}

type devServerResultBody struct {
	IssueID string           `json:"issue_id"`
	Server  devserver.Server `json:"server"`
}

type worktreeListBody struct {
	ProjectID string            `json:"project_id"`
	Worktrees []worktreePayload `json:"worktrees"`
}

type worktreePayload struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	IssueID string `json:"issue_id"`
}

type worktreeCommandBody struct {
	ProjectID string `json:"project_id"`
	IssueID   string `json:"issue_id,omitempty"`
}

func (c *Client) commandOutput(ctx context.Context, command string, body any) (string, error) {
	resp, err := c.commandJSONResponse(ctx, command, body)
	if err != nil {
		return "", err
	}
	if len(resp.Body) == 0 {
		return "", nil
	}
	var out commandOutputBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return "", err
	}
	return out.Output, nil
}

func (c *Client) projectRoute() string {
	if c.projectID != "" {
		return c.projectID
	}
	return "default"
}

// StartSession asks the daemon to start one session for issue/task id.
func (c *Client) StartSession(ctx context.Context, issueID string, baseBranch string) (string, error) {
	return c.commandOutput(ctx, CommandSessionStart, sessionCommandBody{
		ProjectID:  c.projectID,
		SessionID:  issueID,
		BaseBranch: baseBranch,
	})
}

// StopSession asks the daemon to stop one session for issue/task id.
func (c *Client) StopSession(ctx context.Context, issueID string) (string, error) {
	return c.commandOutput(ctx, CommandSessionStop, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: issueID,
	})
}

// AttachSession asks the daemon to attach to one session for issue/task id.
func (c *Client) AttachSession(ctx context.Context, issueID string) (string, error) {
	return c.commandOutput(ctx, CommandSessionAttach, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: issueID,
	})
}

// SessionStatus asks the daemon for the current session status view.
func (c *Client) SessionStatus(ctx context.Context, issueID string) (string, error) {
	return c.commandOutput(ctx, CommandSessionStatus, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: issueID,
	})
}

// DevServerStatus returns daemon-owned devserver status for one issue.
func (c *Client) DevServerStatus(ctx context.Context, issueID string) (devserver.Server, error) {
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStatus, devServerCommandBody{IssueID: issueID}, &out); err != nil {
		return devserver.Server{}, err
	}
	return out.Server, nil
}

// StartDevServer asks daemon to start one devserver.
func (c *Client) StartDevServer(ctx context.Context, issueID string) (devserver.Server, error) {
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStart, devServerCommandBody{IssueID: issueID}, &out); err != nil {
		return devserver.Server{}, err
	}
	return out.Server, nil
}

// StopDevServer asks daemon to stop one devserver.
func (c *Client) StopDevServer(ctx context.Context, issueID string) (devserver.Server, error) {
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStop, devServerCommandBody{IssueID: issueID}, &out); err != nil {
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
		ProjectID string `json:"project_id"`
	}{ProjectID: c.projectRoute()}, &out); err != nil {
		return nil, err
	}

	worktrees := make([]git.Worktree, 0, len(out.Worktrees))
	for _, wt := range out.Worktrees {
		worktrees = append(worktrees, git.Worktree{
			Path:    wt.Path,
			Branch:  wt.Branch,
			IssueID: wt.IssueID,
		})
	}
	return worktrees, nil
}

// RemoveWorktree asks the daemon to remove one worktree for an issue in the current project route.
func (c *Client) RemoveWorktree(ctx context.Context, issueID string) error {
	return c.commandJSON(ctx, CommandWorktreeRemove, worktreeCommandBody{
		ProjectID: c.projectRoute(),
		IssueID:   issueID,
	}, nil)
}

// CleanupOrphanedWorktrees asks the daemon to remove orphaned worktrees for the current project route.
func (c *Client) CleanupOrphanedWorktrees(ctx context.Context) (int, error) {
	var out protocol.CleanupOrphanedResponseBody
	if err := c.commandJSON(ctx, protocol.CommandWorktreeCleanupOrphaned, protocol.CleanupOrphanedRequestBody{
		ProjectID: c.projectRoute(),
	}, &out); err != nil {
		return 0, err
	}
	return out.WorktreesRemoved, nil
}
