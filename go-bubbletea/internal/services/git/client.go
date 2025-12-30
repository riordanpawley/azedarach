package git

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Client provides high-level git operations.
type Client struct {
	runner CommandRunner
	logger *slog.Logger
}

// GitStatus represents the status of a git repository.
type GitStatus struct {
	Modified   []string
	Added      []string
	Deleted    []string
	Untracked  []string
	Staged     []string
	HasChanges bool
}

// MergeResult represents the result of a git merge operation.
type MergeResult struct {
	Success       bool
	HasConflicts  bool
	ConflictFiles []string
	Message       string
}

// NewClient creates a new git client.
func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		runner: runner,
		logger: logger,
	}
}

// Status returns the git status of the repository.
// It parses the output of 'git status --porcelain' to provide structured information.
func (c *Client) Status(ctx context.Context, worktree string) (*GitStatus, error) {
	c.logger.Debug("getting git status", "worktree", worktree)

	output, err := c.runner.Run(ctx, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to get git status: %w", err)
	}

	status := parseGitStatus(output)
	c.logger.Debug("git status parsed",
		"hasChanges", status.HasChanges,
		"modified", len(status.Modified),
		"added", len(status.Added),
		"deleted", len(status.Deleted),
		"untracked", len(status.Untracked),
		"staged", len(status.Staged),
	)

	return status, nil
}

// Fetch fetches updates from the remote repository.
func (c *Client) Fetch(ctx context.Context, worktree, remote string) error {
	c.logger.Info("fetching from remote", "worktree", worktree, "remote", remote)

	_, err := c.runner.Run(ctx, "fetch", remote)
	if err != nil {
		return fmt.Errorf("failed to fetch from remote: %w", err)
	}

	c.logger.Info("fetch completed successfully", "remote", remote)
	return nil
}

// Merge merges the specified branch into the current branch.
// It detects merge conflicts and returns detailed information.
func (c *Client) Merge(ctx context.Context, worktree, branch string) (*MergeResult, error) {
	c.logger.Info("merging branch", "worktree", worktree, "branch", branch)

	output, err := c.runner.Run(ctx, "merge", branch)

	result := &MergeResult{
		Success:      err == nil,
		HasConflicts: false,
		Message:      output,
	}

	if err != nil {
		// Check if it's a merge conflict
		if strings.Contains(err.Error(), "CONFLICT") || strings.Contains(output, "CONFLICT") {
			result.HasConflicts = true
			result.ConflictFiles = parseConflicts(output)
			c.logger.Warn("merge has conflicts",
				"branch", branch,
				"conflicts", result.ConflictFiles,
			)
		} else {
			c.logger.Error("merge failed", "branch", branch, "error", err)
			return nil, fmt.Errorf("failed to merge branch: %w", err)
		}
	} else {
		c.logger.Info("merge completed successfully", "branch", branch)
	}

	return result, nil
}

// AbortMerge aborts an ongoing merge operation.
func (c *Client) AbortMerge(ctx context.Context, worktree string) error {
	c.logger.Info("aborting merge", "worktree", worktree)

	_, err := c.runner.Run(ctx, "merge", "--abort")
	if err != nil {
		return fmt.Errorf("failed to abort merge: %w", err)
	}

	c.logger.Info("merge aborted successfully")
	return nil
}

// Diff returns the diff output for the working directory.
func (c *Client) Diff(ctx context.Context, worktree string) (string, error) {
	c.logger.Debug("getting diff", "worktree", worktree)

	output, err := c.runner.Run(ctx, "diff")
	if err != nil {
		return "", fmt.Errorf("failed to get diff: %w", err)
	}

	return output, nil
}

// DiffStat returns the diff stat output (summary of changes).
func (c *Client) DiffStat(ctx context.Context, worktree string) (string, error) {
	c.logger.Debug("getting diff stat", "worktree", worktree)

	output, err := c.runner.Run(ctx, "diff", "--stat")
	if err != nil {
		return "", fmt.Errorf("failed to get diff stat: %w", err)
	}

	return output, nil
}

// Push pushes the specified branch to the remote repository.
func (c *Client) Push(ctx context.Context, worktree, remote, branch string) error {
	c.logger.Info("pushing branch", "worktree", worktree, "remote", remote, "branch", branch)

	_, err := c.runner.Run(ctx, "push", remote, branch)
	if err != nil {
		return fmt.Errorf("failed to push branch: %w", err)
	}

	c.logger.Info("push completed successfully", "remote", remote, "branch", branch)
	return nil
}

// CurrentBranch returns the name of the current branch.
func (c *Client) CurrentBranch(ctx context.Context, worktree string) (string, error) {
	c.logger.Debug("getting current branch", "worktree", worktree)

	output, err := c.runner.Run(ctx, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}

	branch := strings.TrimSpace(output)
	c.logger.Debug("current branch", "branch", branch)

	return branch, nil
}

// Checkout checks out the specified branch.
func (c *Client) Checkout(ctx context.Context, worktree, branch string) error {
	c.logger.Info("checking out branch", "worktree", worktree, "branch", branch)

	_, err := c.runner.Run(ctx, "checkout", branch)
	if err != nil {
		return fmt.Errorf("failed to checkout branch: %w", err)
	}

	c.logger.Info("checkout completed successfully", "branch", branch)
	return nil
}

// parseGitStatus parses the output of 'git status --porcelain'.
// The format is: XY PATH
// Where X is the status of the index and Y is the status of the working tree.
//
// Examples:
//   M  file.txt  - modified in index (staged)
//    M file.txt  - modified in working tree (unstaged)
//   A  file.txt  - added to index (staged)
//   D  file.txt  - deleted from index (staged)
//   ?? file.txt  - untracked file
//   MM file.txt  - modified in both index and working tree
func parseGitStatus(output string) *GitStatus {
	status := &GitStatus{
		Modified:  make([]string, 0),
		Added:     make([]string, 0),
		Deleted:   make([]string, 0),
		Untracked: make([]string, 0),
		Staged:    make([]string, 0),
	}

	if output == "" {
		return status
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}

		indexStatus := line[0]
		workTreeStatus := line[1]
		path := strings.TrimSpace(line[2:])

		// Check if file is staged (index status is not space or ?)
		if indexStatus != ' ' && indexStatus != '?' {
			status.Staged = append(status.Staged, path)
		}

		// Parse status codes
		switch {
		case line[:2] == "??":
			status.Untracked = append(status.Untracked, path)
		case indexStatus == 'A' || workTreeStatus == 'A':
			status.Added = append(status.Added, path)
		case indexStatus == 'D' || workTreeStatus == 'D':
			status.Deleted = append(status.Deleted, path)
		case indexStatus == 'M' || workTreeStatus == 'M':
			status.Modified = append(status.Modified, path)
		}
	}

	status.HasChanges = len(status.Modified) > 0 ||
		len(status.Added) > 0 ||
		len(status.Deleted) > 0 ||
		len(status.Untracked) > 0 ||
		len(status.Staged) > 0

	return status
}

// parseConflicts extracts conflict file paths from git merge output.
// Handles multiple conflict formats:
//   - "CONFLICT (content): Merge conflict in <file>"
//   - "CONFLICT (modify/delete): <file> deleted in HEAD and modified in ..."
func parseConflicts(output string) []string {
	conflicts := make([]string, 0)

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if !strings.Contains(line, "CONFLICT") {
			continue
		}

		// Try to extract filename based on conflict type
		// Format 1: "CONFLICT (content): Merge conflict in <file>"
		if strings.Contains(line, "Merge conflict in ") {
			parts := strings.Split(line, "Merge conflict in ")
			if len(parts) >= 2 {
				file := strings.TrimSpace(parts[1])
				conflicts = append(conflicts, file)
			}
			continue
		}

		// Format 2: "CONFLICT (modify/delete): <file> deleted in ..." or "... modified in ..."
		// Find the text between ": " and " deleted in " or " modified in "
		if idx := strings.Index(line, "): "); idx != -1 {
			rest := line[idx+3:]
			// Look for " deleted in " or " modified in "
			var file string
			if idx2 := strings.Index(rest, " deleted in "); idx2 != -1 {
				file = strings.TrimSpace(rest[:idx2])
			} else if idx2 := strings.Index(rest, " modified in "); idx2 != -1 {
				file = strings.TrimSpace(rest[:idx2])
			}
			if file != "" {
				conflicts = append(conflicts, file)
			}
		}
	}

	return conflicts
}
