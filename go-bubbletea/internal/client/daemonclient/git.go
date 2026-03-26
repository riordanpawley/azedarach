package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/services/git"
)

const (
	CommandGitFetch      = "git.fetch"
	CommandGitMerge      = "git.merge"
	CommandGitCheckout   = "git.checkout"
	CommandGitAbortMerge = "git.abort_merge"
	CommandGitDiffStat   = "git.diff_stat"
	CommandGitStatus     = "git.status"
)

// GitCommandRequest captures the daemon request body for git workflow commands.
type GitCommandRequest struct {
	Worktree string `json:"worktree"`
	Remote   string `json:"remote,omitempty"`
	Branch   string `json:"branch,omitempty"`
}

// GitCommandResponse captures the daemon response body for git workflow commands.
type GitCommandResponse struct {
	Worktree string `json:"worktree"`
	Remote   string `json:"remote,omitempty"`
	Branch   string `json:"branch,omitempty"`
}

// GitMergeCommandResponse captures the daemon response body for merge commands.
type GitMergeCommandResponse struct {
	Worktree string          `json:"worktree"`
	Branch   string          `json:"branch"`
	Result   git.MergeResult `json:"result"`
}

type gitOutputBody struct {
	Output string `json:"output"`
}

type gitStatusBody struct {
	Status git.GitStatus `json:"status"`
}

// GitFetch asks the daemon to fetch updates for a worktree from the requested remote.
func (c *Client) GitFetch(ctx context.Context, worktree, remote string) (GitCommandResponse, error) {
	var resp GitCommandResponse
	if err := c.commandJSON(ctx, CommandGitFetch, GitCommandRequest{
		Worktree: worktree,
		Remote:   remote,
	}, &resp); err != nil {
		return GitCommandResponse{}, err
	}
	return resp, nil
}

// GitMerge asks the daemon to merge a branch into the requested worktree.
func (c *Client) GitMerge(ctx context.Context, worktree, branch string) (GitMergeCommandResponse, error) {
	var resp GitMergeCommandResponse
	if err := c.commandJSON(ctx, CommandGitMerge, GitCommandRequest{
		Worktree: worktree,
		Branch:   branch,
	}, &resp); err != nil {
		return GitMergeCommandResponse{}, err
	}
	return resp, nil
}

// GitCheckout asks the daemon to checkout a branch in the requested worktree.
func (c *Client) GitCheckout(ctx context.Context, worktree, branch string) (GitCommandResponse, error) {
	var resp GitCommandResponse
	if err := c.commandJSON(ctx, CommandGitCheckout, GitCommandRequest{
		Worktree: worktree,
		Branch:   branch,
	}, &resp); err != nil {
		return GitCommandResponse{}, err
	}
	return resp, nil
}

// GitAbortMerge asks the daemon to abort an ongoing merge in the requested worktree.
func (c *Client) GitAbortMerge(ctx context.Context, worktree string) (GitCommandResponse, error) {
	var resp GitCommandResponse
	if err := c.commandJSON(ctx, CommandGitAbortMerge, GitCommandRequest{
		Worktree: worktree,
	}, &resp); err != nil {
		return GitCommandResponse{}, err
	}
	return resp, nil
}

// GitDiffStat asks the daemon to get the diff stat output for the requested worktree.
func (c *Client) GitDiffStat(ctx context.Context, worktree string) (string, error) {
	var resp gitOutputBody
	if err := c.commandJSON(ctx, CommandGitDiffStat, GitCommandRequest{
		Worktree: worktree,
	}, &resp); err != nil {
		return "", err
	}
	return resp.Output, nil
}

// GitStatus asks the daemon to get git status for the requested worktree.
func (c *Client) GitStatus(ctx context.Context, worktree string) (git.GitStatus, error) {
	var resp gitStatusBody
	if err := c.commandJSON(ctx, CommandGitStatus, GitCommandRequest{
		Worktree: worktree,
	}, &resp); err != nil {
		return git.GitStatus{}, err
	}
	return resp.Status, nil
}
