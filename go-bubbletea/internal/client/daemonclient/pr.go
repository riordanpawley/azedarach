package daemonclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/riordanpawley/azedarach/internal/services/pr"
)

const (
	CommandPRCreate        = "pr.create"
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
