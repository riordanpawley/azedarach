package pr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// PRWorkflow manages GitHub PR operations via gh CLI
type PRWorkflow struct {
	runner CommandRunner
	logger *slog.Logger
}

// PRInfo contains information about a pull request
type PRInfo struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	State    string `json:"state"`    // open, closed, merged
	Draft    bool   `json:"isDraft"`
	Branch   string `json:"headRefName"`
	BaseRef  string `json:"baseRefName"`
}

// CreatePRParams contains parameters for creating a pull request
type CreatePRParams struct {
	Title      string
	Body       string
	Branch     string
	BaseBranch string
	Draft      bool
	BeadID     string
}

// NewPRWorkflow creates a new PR workflow service
func NewPRWorkflow(runner CommandRunner, logger *slog.Logger) *PRWorkflow {
	return &PRWorkflow{
		runner: runner,
		logger: logger,
	}
}

// Create creates a new pull request via gh pr create
func (w *PRWorkflow) Create(ctx context.Context, params CreatePRParams) (*PRInfo, error) {
	w.logger.Debug("creating PR",
		"title", params.Title,
		"branch", params.Branch,
		"base", params.BaseBranch,
		"draft", params.Draft,
		"bead_id", params.BeadID,
	)

	args := []string{
		"pr", "create",
		"--title", params.Title,
		"--body", params.Body,
		"--head", params.Branch,
		"--base", params.BaseBranch,
	}

	if params.Draft {
		args = append(args, "--draft")
	}

	// gh pr create doesn't return JSON by default, so we need to get the PR after creation
	out, err := w.runner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create PR: %w (output: %s)", err, string(out))
	}

	// Extract PR URL from output (last line usually contains the URL)
	outputStr := strings.TrimSpace(string(out))
	lines := strings.Split(outputStr, "\n")
	prURL := lines[len(lines)-1]

	w.logger.Info("PR created", "url", prURL)

	// Fetch the PR info using the branch
	return w.Get(ctx, params.Branch)
}

// Get retrieves PR information for a given branch
func (w *PRWorkflow) Get(ctx context.Context, branch string) (*PRInfo, error) {
	w.logger.Debug("fetching PR info", "branch", branch)

	args := []string{
		"pr", "view", branch,
		"--json", "number,title,url,state,isDraft,headRefName,baseRefName",
	}

	out, err := w.runner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR info for branch %s: %w", branch, err)
	}

	var info PRInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("failed to parse PR JSON: %w", err)
	}

	w.logger.Debug("fetched PR info", "number", info.Number, "state", info.State)
	return &info, nil
}

// List retrieves all open pull requests
func (w *PRWorkflow) List(ctx context.Context) ([]PRInfo, error) {
	w.logger.Debug("listing open PRs")

	args := []string{
		"pr", "list",
		"--json", "number,title,url,state,isDraft,headRefName,baseRefName",
	}

	out, err := w.runner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list PRs: %w", err)
	}

	var prs []PRInfo
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("failed to parse PR list JSON: %w", err)
	}

	w.logger.Debug("listed PRs", "count", len(prs))
	return prs, nil
}

// Merge merges a pull request using the specified strategy
func (w *PRWorkflow) Merge(ctx context.Context, prNumber int, strategy string) error {
	w.logger.Debug("merging PR", "number", prNumber, "strategy", strategy)

	args := []string{
		"pr", "merge", fmt.Sprintf("%d", prNumber),
	}

	// Add merge strategy flag
	switch strategy {
	case "squash":
		args = append(args, "--squash")
	case "rebase":
		args = append(args, "--rebase")
	case "merge":
		args = append(args, "--merge")
	default:
		return fmt.Errorf("invalid merge strategy: %s (must be squash, rebase, or merge)", strategy)
	}

	// Auto-confirm the merge
	args = append(args, "--auto")

	out, err := w.runner.Run(ctx, "gh", args...)
	if err != nil {
		return fmt.Errorf("failed to merge PR %d: %w (output: %s)", prNumber, err, string(out))
	}

	w.logger.Info("PR merged", "number", prNumber, "strategy", strategy)
	return nil
}

// Close closes a pull request without merging
func (w *PRWorkflow) Close(ctx context.Context, prNumber int) error {
	w.logger.Debug("closing PR", "number", prNumber)

	args := []string{
		"pr", "close", fmt.Sprintf("%d", prNumber),
	}

	out, err := w.runner.Run(ctx, "gh", args...)
	if err != nil {
		return fmt.Errorf("failed to close PR %d: %w (output: %s)", prNumber, err, string(out))
	}

	w.logger.Info("PR closed", "number", prNumber)
	return nil
}

// MarkReady marks a draft PR as ready for review
func (w *PRWorkflow) MarkReady(ctx context.Context, prNumber int) error {
	w.logger.Debug("marking PR ready for review", "number", prNumber)

	args := []string{
		"pr", "ready", fmt.Sprintf("%d", prNumber),
	}

	out, err := w.runner.Run(ctx, "gh", args...)
	if err != nil {
		return fmt.Errorf("failed to mark PR %d ready: %w (output: %s)", prNumber, err, string(out))
	}

	w.logger.Info("PR marked ready", "number", prNumber)
	return nil
}
