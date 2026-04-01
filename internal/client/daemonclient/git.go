package daemonclient

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/services/git"
)

const (
	CommandGitFetch          = "git.fetch"
	CommandGitMerge          = "git.merge"
	CommandGitCheckout       = "git.checkout"
	CommandGitAbortMerge     = "git.abort_merge"
	CommandGitDiffStat       = "git.diff_stat"
	CommandGitStatus         = "git.status"
	CommandGitRuntimeSignals = "git.runtime_signals"
	CommandGitPreflight      = "git.preflight"
	CommandGitDiscard        = "git.discard"
	CommandGitCheckpoint     = "git.checkpoint"
)

// GitCommandRequest captures the daemon request body for git workflow commands.
type GitCommandRequest struct {
	Worktree      string                    `json:"worktree"`
	Remote        string                    `json:"remote,omitempty"`
	Branch        string                    `json:"branch,omitempty"`
	BaseBranch    string                    `json:"base_branch,omitempty"`
	Targets       []GitRuntimeSignalsTarget `json:"targets,omitempty"`
	CompareRemote bool                      `json:"compare_remote,omitempty"`
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

type GitRuntimeSignalsTarget struct {
	IssueID  string `json:"issue_id"`
	Worktree string `json:"worktree"`
}

type GitRuntimeSignalsResult struct {
	IssueID               string `json:"issue_id"`
	Worktree              string `json:"worktree"`
	HasUncommittedChanges bool   `json:"has_uncommitted_changes"`
	GitAdditions          int    `json:"git_additions"`
	GitDeletions          int    `json:"git_deletions"`
	GitAheadCount         int    `json:"git_ahead_count"`
	GitBehindCount        int    `json:"git_behind_count"`
}

type gitRuntimeSignalsBody struct {
	Signals         []GitRuntimeSignalsResult `json:"signals"`
	PartialFailures int                       `json:"partial_failures"`
}

// GitMergePreflightRequest captures the daemon request body for merge preflight prediction.
type GitMergePreflightRequest struct {
	Worktree   string `json:"worktree"`
	BaseBranch string `json:"base_branch,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

// GitMergePreflightResponse captures the daemon response body for merge preflight prediction.
type GitMergePreflightResponse struct {
	Worktree   string          `json:"worktree"`
	BaseBranch string          `json:"base_branch,omitempty"`
	Branch     string          `json:"branch,omitempty"`
	Result     git.MergeResult `json:"result"`
}

// GitDiscardRequest captures the daemon request body for discarding worktree changes.
type GitDiscardRequest struct {
	Worktree string `json:"worktree"`
}

// GitDiscardResponse captures the daemon response body for discarding worktree changes.
type GitDiscardResponse struct {
	Worktree string `json:"worktree"`
}

// GitCheckpointRequest captures the daemon request body for checkpoint commits.
type GitCheckpointRequest struct {
	Worktree string `json:"worktree"`
	Message  string `json:"message,omitempty"`
}

// GitCheckpointResponse captures the daemon response body for checkpoint commits.
type GitCheckpointResponse struct {
	Worktree string `json:"worktree"`
}

// GitFetch asks the daemon to fetch updates for a worktree from the requested remote.
func (c *Client) GitFetch(ctx context.Context, worktree, remote string) (GitCommandResponse, error) {
	raw, err := c.commandJSONResponse(ctx, CommandGitFetch, GitCommandRequest{
		Worktree: worktree,
		Remote:   remote,
	})
	if err != nil {
		return GitCommandResponse{}, err
	}
	var resp GitCommandResponse
	if err := decodeLongRunningJSON(CommandGitFetch, raw.Body, &resp); err != nil {
		return GitCommandResponse{}, err
	}
	return resp, nil
}

// GitMerge asks the daemon to merge a branch into the requested worktree.
func (c *Client) GitMerge(ctx context.Context, worktree, branch string) (GitMergeCommandResponse, error) {
	raw, err := c.commandJSONResponse(ctx, CommandGitMerge, GitCommandRequest{
		Worktree: worktree,
		Branch:   branch,
	})
	if err != nil {
		return GitMergeCommandResponse{}, err
	}
	var resp GitMergeCommandResponse
	if err := decodeLongRunningJSON(CommandGitMerge, raw.Body, &resp); err != nil {
		return GitMergeCommandResponse{}, err
	}
	return resp, nil
}

// GitCheckout asks the daemon to checkout a branch in the requested worktree.
func (c *Client) GitCheckout(ctx context.Context, worktree, branch string) (GitCommandResponse, error) {
	raw, err := c.commandJSONResponse(ctx, CommandGitCheckout, GitCommandRequest{
		Worktree: worktree,
		Branch:   branch,
	})
	if err != nil {
		return GitCommandResponse{}, err
	}
	var resp GitCommandResponse
	if err := decodeLongRunningJSON(CommandGitCheckout, raw.Body, &resp); err != nil {
		return GitCommandResponse{}, err
	}
	return resp, nil
}

// GitAbortMerge asks the daemon to abort an ongoing merge in the requested worktree.
func (c *Client) GitAbortMerge(ctx context.Context, worktree string) (GitCommandResponse, error) {
	raw, err := c.commandJSONResponse(ctx, CommandGitAbortMerge, GitCommandRequest{
		Worktree: worktree,
	})
	if err != nil {
		return GitCommandResponse{}, err
	}
	var resp GitCommandResponse
	if err := decodeLongRunningJSON(CommandGitAbortMerge, raw.Body, &resp); err != nil {
		return GitCommandResponse{}, err
	}
	return resp, nil
}

// GitDiffStat asks the daemon to get the diff stat output for the requested worktree.
func (c *Client) GitDiffStat(ctx context.Context, worktree, baseBranch string) (string, error) {
	var resp gitOutputBody
	if err := c.commandJSON(ctx, CommandGitDiffStat, GitCommandRequest{
		Worktree:   worktree,
		BaseBranch: baseBranch,
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

// GitRuntimeSignals asks the daemon to compute runtime git signals for issue worktrees.
func (c *Client) GitRuntimeSignals(ctx context.Context, targets []GitRuntimeSignalsTarget, baseBranch string, compareRemote bool, remote string) ([]GitRuntimeSignalsResult, int, error) {
	var resp gitRuntimeSignalsBody
	if err := c.commandJSON(ctx, CommandGitRuntimeSignals, GitCommandRequest{
		Targets:       targets,
		BaseBranch:    baseBranch,
		CompareRemote: compareRemote,
		Remote:        remote,
	}, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Signals, resp.PartialFailures, nil
}

// GitMergePreflight asks the daemon to predict whether the requested merge would conflict.
func (c *Client) GitMergePreflight(ctx context.Context, worktree, baseBranch, branch string) (GitMergePreflightResponse, error) {
	raw, err := c.commandJSONResponse(ctx, CommandGitPreflight, GitMergePreflightRequest{
		Worktree:   worktree,
		BaseBranch: baseBranch,
		Branch:     branch,
	})
	if err != nil {
		return GitMergePreflightResponse{}, err
	}
	var resp GitMergePreflightResponse
	if err := decodeLongRunningJSON(CommandGitPreflight, raw.Body, &resp); err != nil {
		return GitMergePreflightResponse{}, err
	}
	return resp, nil
}

// GitDiscardChanges asks the daemon to discard staged and unstaged changes in a worktree.
func (c *Client) GitDiscardChanges(ctx context.Context, worktree string) (GitDiscardResponse, error) {
	raw, err := c.commandJSONResponse(ctx, CommandGitDiscard, GitDiscardRequest{
		Worktree: worktree,
	})
	if err != nil {
		return GitDiscardResponse{}, err
	}
	var resp GitDiscardResponse
	if err := decodeLongRunningJSON(CommandGitDiscard, raw.Body, &resp); err != nil {
		return GitDiscardResponse{}, err
	}
	return resp, nil
}

// GitCheckpointCommit asks the daemon to create a checkpoint commit in a worktree.
func (c *Client) GitCheckpointCommit(ctx context.Context, worktree, message string) (GitCheckpointResponse, error) {
	raw, err := c.commandJSONResponse(ctx, CommandGitCheckpoint, GitCheckpointRequest{
		Worktree: worktree,
		Message:  message,
	})
	if err != nil {
		return GitCheckpointResponse{}, err
	}
	var resp GitCheckpointResponse
	if err := decodeLongRunningJSON(CommandGitCheckpoint, raw.Body, &resp); err != nil {
		return GitCheckpointResponse{}, err
	}
	return resp, nil
}
