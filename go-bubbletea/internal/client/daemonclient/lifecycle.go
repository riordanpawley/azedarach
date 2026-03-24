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
	CommandSessionStop     = "session.stop"
	CommandDevServerStart  = "devserver.start"
	CommandDevServerStop   = "devserver.stop"
	CommandDevServerStatus = "devserver.status"
	CommandWorktreeList    = "worktree.list"
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
	BeadID string `json:"bead_id"`
}

type devServerResultBody struct {
	BeadID string           `json:"bead_id"`
	Server devserver.Server `json:"server"`
}

type worktreeListBody struct {
	ProjectID string            `json:"project_id"`
	Worktrees []worktreePayload `json:"worktrees"`
}

type worktreePayload struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	BeadID string `json:"bead_id"`
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

// StartSession asks the daemon to start one session for bead/task id.
func (c *Client) StartSession(ctx context.Context, beadID string, baseBranch string) (string, error) {
	return c.commandOutput(ctx, CommandSessionStart, sessionCommandBody{
		ProjectID:  c.projectID,
		SessionID:  beadID,
		BaseBranch: baseBranch,
	})
}

// StopSession asks the daemon to stop one session for bead/task id.
func (c *Client) StopSession(ctx context.Context, beadID string) (string, error) {
	return c.commandOutput(ctx, CommandSessionStop, sessionCommandBody{
		ProjectID: c.projectID,
		SessionID: beadID,
	})
}

// DevServerStatus returns daemon-owned devserver status for one bead.
func (c *Client) DevServerStatus(ctx context.Context, beadID string) (devserver.Server, error) {
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStatus, devServerCommandBody{BeadID: beadID}, &out); err != nil {
		return devserver.Server{}, err
	}
	return out.Server, nil
}

// StartDevServer asks daemon to start one devserver.
func (c *Client) StartDevServer(ctx context.Context, beadID string) (devserver.Server, error) {
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStart, devServerCommandBody{BeadID: beadID}, &out); err != nil {
		return devserver.Server{}, err
	}
	return out.Server, nil
}

// StopDevServer asks daemon to stop one devserver.
func (c *Client) StopDevServer(ctx context.Context, beadID string) (devserver.Server, error) {
	var out devServerResultBody
	if err := c.commandJSON(ctx, CommandDevServerStop, devServerCommandBody{BeadID: beadID}, &out); err != nil {
		return devserver.Server{}, err
	}
	return out.Server, nil
}

// ToggleDevServer toggles running/stopped state through daemon command authority.
func (c *Client) ToggleDevServer(ctx context.Context, beadID string) (devserver.Server, error) {
	srv, err := c.DevServerStatus(ctx, beadID)
	if err != nil {
		var cmdErr *CommandError
		if errors.As(err, &cmdErr) && cmdErr.Code == protocol.ErrorCodeInvalidRequest {
			return c.StartDevServer(ctx, beadID)
		}
		return devserver.Server{}, err
	}
	if srv.Status == "running" {
		return c.StopDevServer(ctx, beadID)
	}
	return c.StartDevServer(ctx, beadID)
}

// RestartDevServer restarts one devserver through daemon command authority.
func (c *Client) RestartDevServer(ctx context.Context, beadID string) (devserver.Server, error) {
	_, err := c.StopDevServer(ctx, beadID)
	if err != nil {
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) || (cmdErr.Code != protocol.ErrorCodeInvalidRequest && cmdErr.Code != protocol.ErrorCodeConflict) {
			return devserver.Server{}, err
		}
	}
	return c.StartDevServer(ctx, beadID)
}

// ListWorktrees returns daemon-owned worktrees for the current project route.
func (c *Client) ListWorktrees(ctx context.Context) ([]git.Worktree, error) {
	projectID := c.projectID
	if projectID == "" {
		projectID = "default"
	}

	var out worktreeListBody
	if err := c.commandJSON(ctx, CommandWorktreeList, struct {
		ProjectID string `json:"project_id"`
	}{ProjectID: projectID}, &out); err != nil {
		return nil, err
	}

	worktrees := make([]git.Worktree, 0, len(out.Worktrees))
	for _, wt := range out.Worktrees {
		worktrees = append(worktrees, git.Worktree{
			Path:   wt.Path,
			Branch: wt.Branch,
			BeadID: wt.BeadID,
		})
	}
	return worktrees, nil
}
