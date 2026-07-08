package daemonclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/riordanpawley/azedarach/internal/services/pr"
)

const (
	CommandPRCreate        = "pr.create"
	CommandPRGet           = "pr.get"
	CommandPRChecks        = "pr.checks"
	CommandPROpen          = "pr.open"
	CommandPRMerge         = "pr.merge"
	CommandGitBranchBehind = "git.branch_behind"
)

// CreatePullRequestParams contains the payload used to create a pull request.
type CreatePullRequestParams struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
	Draft      bool   `json:"draft"`
	IssueID    string `json:"issue_id"`
}

// CreatePullRequestResult captures the deterministic daemon response for PR creation.
type CreatePullRequestResult struct {
	IssueID     string    `json:"issue_id"`
	PullRequest pr.PRInfo `json:"pull_request"`
}

// PullRequestBranchParams identifies a PR by head branch.
type PullRequestBranchParams struct {
	Branch string `json:"branch"`
}

// PullRequestGetResult captures PR metadata for a branch.
type PullRequestGetResult struct {
	PullRequest pr.PRInfo `json:"pull_request"`
}

// PullRequestChecksParams identifies a PR for checks by branch, number, or URL.
type PullRequestChecksParams struct {
	Ref string `json:"ref"`
}

// PullRequestChecksResult captures summarized and per-check CI status.
type PullRequestChecksResult struct {
	Ref          string         `json:"ref"`
	Checks       []pr.CheckInfo `json:"checks"`
	ChecksStatus string         `json:"checks_status"`
}

// PullRequestMergeParams identifies a PR to merge.
type PullRequestMergeParams struct {
	Branch   string `json:"branch"`
	Number   int    `json:"number"`
	Strategy string `json:"strategy"`
}

// PullRequestMergeResult captures a completed merge request.
type PullRequestMergeResult struct {
	Number   int    `json:"number"`
	Strategy string `json:"strategy"`
}

// BranchBehindCheckParams contains the payload used to check whether a branch is behind its base branch.
type BranchBehindCheckParams struct {
	Worktree   string `json:"worktree"`
	BaseBranch string `json:"base_branch"`
	Remote     string `json:"remote"`
}

// BranchBehindCheckResult captures the deterministic daemon response for branch-behind checks.
type BranchBehindCheckResult struct {
	Worktree      string `json:"worktree"`
	BaseBranch    string `json:"base_branch"`
	Remote        string `json:"remote"`
	RevRange      string `json:"rev_range"`
	AheadRevRange string `json:"ahead_rev_range"`
	CommitsAhead  int    `json:"commits_ahead"`
	Ahead         bool   `json:"ahead"`
	CommitsBehind int    `json:"commits_behind"`
	Behind        bool   `json:"behind"`
}

// CreatePullRequest asks the daemon to create a pull request and returns the created PR metadata.
func (c *Client) CreatePullRequest(ctx context.Context, params CreatePullRequestParams) (CreatePullRequestResult, error) {
	var out CreatePullRequestResult
	if err := c.commandJSON(ctx, CommandPRCreate, params, &out); err != nil {
		return CreatePullRequestResult{}, err
	}
	if out.IssueID == "" {
		return CreatePullRequestResult{}, fmt.Errorf("%s returned empty issue id", CommandPRCreate)
	}
	return out, nil
}

// GetPullRequest asks the daemon for PR metadata by branch.
func (c *Client) GetPullRequest(ctx context.Context, params PullRequestBranchParams) (PullRequestGetResult, error) {
	var out PullRequestGetResult
	if err := c.commandJSON(ctx, CommandPRGet, params, &out); err != nil {
		return PullRequestGetResult{}, err
	}
	if out.PullRequest.Number == 0 {
		return PullRequestGetResult{}, fmt.Errorf("%s returned empty pull request number", CommandPRGet)
	}
	return out, nil
}

// GetPullRequestChecks asks the daemon for PR checks by branch, number, or URL.
func (c *Client) GetPullRequestChecks(ctx context.Context, params PullRequestChecksParams) (PullRequestChecksResult, error) {
	var out PullRequestChecksResult
	if err := c.commandJSON(ctx, CommandPRChecks, params, &out); err != nil {
		return PullRequestChecksResult{}, err
	}
	return out, nil
}

// OpenPullRequest asks the daemon to open a PR in the browser.
func (c *Client) OpenPullRequest(ctx context.Context, params PullRequestBranchParams) error {
	var out struct {
		Branch string `json:"branch"`
	}
	if err := c.commandJSON(ctx, CommandPROpen, params, &out); err != nil {
		return err
	}
	return nil
}

// MergePullRequest asks the daemon to merge a PR.
func (c *Client) MergePullRequest(ctx context.Context, params PullRequestMergeParams) (PullRequestMergeResult, error) {
	var out PullRequestMergeResult
	if err := c.commandJSON(ctx, CommandPRMerge, params, &out); err != nil {
		return PullRequestMergeResult{}, err
	}
	if out.Number == 0 {
		return PullRequestMergeResult{}, fmt.Errorf("%s returned empty pull request number", CommandPRMerge)
	}
	return out, nil
}

// CheckBranchBehind asks the daemon whether a worktree branch is behind its base branch.
func (c *Client) CheckBranchBehind(ctx context.Context, params BranchBehindCheckParams) (BranchBehindCheckResult, error) {
	var out BranchBehindCheckResult
	if err := c.commandJSON(ctx, CommandGitBranchBehind, params, &out); err != nil {
		return BranchBehindCheckResult{}, err
	}
	return out, nil
}

func decodeBranchBehindResult(body []byte) (BranchBehindCheckResult, error) {
	var out BranchBehindCheckResult
	if err := json.Unmarshal(body, &out); err != nil {
		return BranchBehindCheckResult{}, err
	}
	return out, nil
}
