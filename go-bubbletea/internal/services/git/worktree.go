package git

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// WorktreeManager manages git worktrees for Claude Code sessions.
type WorktreeManager struct {
	runner  CommandRunner
	logger  *slog.Logger
	repoDir string // Main repository directory (absolute path)
}

// Worktree represents a git worktree associated with a bead.
type Worktree struct {
	Path   string // Absolute path to the worktree
	Branch string // Branch name (e.g., "az/bead-123")
	BeadID string // Associated bead ID
}

// NewWorktreeManager creates a new WorktreeManager.
func NewWorktreeManager(runner CommandRunner, repoDir string, logger *slog.Logger) *WorktreeManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &WorktreeManager{
		runner:  runner,
		logger:  logger,
		repoDir: repoDir,
	}
}

// Create creates a new worktree for the given bead ID.
// It creates the worktree at ../RepoName-beadID/ with branch az/beadID.
func (w *WorktreeManager) Create(ctx context.Context, beadID string, baseBranch string) (*Worktree, error) {
	// Get repository name from repoDir
	repoName := filepath.Base(w.repoDir)

	// Calculate worktree path: ../RepoName-beadID/
	worktreePath := filepath.Join(filepath.Dir(w.repoDir), fmt.Sprintf("%s-%s", repoName, beadID))

	// Branch name: az/beadID
	branchName := fmt.Sprintf("az/%s", beadID)

	w.logger.Info("creating worktree",
		"beadID", beadID,
		"path", worktreePath,
		"branch", branchName,
		"baseBranch", baseBranch,
	)

	// Check if worktree already exists
	exists, err := w.Exists(ctx, beadID)
	if err != nil {
		return nil, fmt.Errorf("failed to check if worktree exists: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("worktree for bead %s already exists", beadID)
	}

	// Create worktree with new branch from baseBranch
	// git worktree add -b az/beadID ../RepoName-beadID baseBranch
	_, err = w.runner.Run(ctx, "worktree", "add", "-b", branchName, worktreePath, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w", err)
	}

	w.logger.Info("worktree created successfully", "beadID", beadID, "path", worktreePath)

	return &Worktree{
		Path:   worktreePath,
		Branch: branchName,
		BeadID: beadID,
	}, nil
}

// Delete removes the worktree and branch for the given bead ID.
func (w *WorktreeManager) Delete(ctx context.Context, beadID string) error {
	w.logger.Info("deleting worktree", "beadID", beadID)

	// Get worktree info to find the path
	worktree, err := w.Get(ctx, beadID)
	if err != nil {
		return fmt.Errorf("failed to get worktree info: %w", err)
	}

	// Remove worktree
	// git worktree remove <path>
	_, err = w.runner.Run(ctx, "worktree", "remove", worktree.Path)
	if err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	// Delete branch
	// git branch -D az/beadID
	_, err = w.runner.Run(ctx, "branch", "-D", worktree.Branch)
	if err != nil {
		// Log warning but don't fail - branch might already be deleted
		w.logger.Warn("failed to delete branch", "branch", worktree.Branch, "error", err)
	}

	w.logger.Info("worktree deleted successfully", "beadID", beadID)

	return nil
}

// Get returns information about the worktree for the given bead ID.
func (w *WorktreeManager) Get(ctx context.Context, beadID string) (*Worktree, error) {
	worktrees, err := w.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	for _, wt := range worktrees {
		if wt.BeadID == beadID {
			return &wt, nil
		}
	}

	return nil, fmt.Errorf("worktree for bead %s not found", beadID)
}

// List returns all worktrees managed by this WorktreeManager.
// It filters for worktrees that match the az/beadID pattern.
func (w *WorktreeManager) List(ctx context.Context) ([]Worktree, error) {
	// git worktree list --porcelain
	output, err := w.runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return w.parseWorktreeList(output), nil
}

// Exists checks if a worktree exists for the given bead ID.
func (w *WorktreeManager) Exists(ctx context.Context, beadID string) (bool, error) {
	_, err := w.Get(ctx, beadID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// parseWorktreeList parses the output of 'git worktree list --porcelain'.
// Example output:
//   worktree /home/user/repo
//   HEAD abc123
//   branch refs/heads/main
//
//   worktree /home/user/repo-bead-123
//   HEAD def456
//   branch refs/heads/az/bead-123
func (w *WorktreeManager) parseWorktreeList(output string) []Worktree {
	var worktrees []Worktree

	lines := strings.Split(output, "\n")
	var currentPath string
	var currentBranch string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			branchRef := strings.TrimPrefix(line, "branch ")
			currentBranch = strings.TrimPrefix(branchRef, "refs/heads/")
		} else if line == "" && currentPath != "" && currentBranch != "" {
			// End of worktree entry
			// Only include worktrees with az/ branches
			if strings.HasPrefix(currentBranch, "az/") {
				beadID := strings.TrimPrefix(currentBranch, "az/")
				worktrees = append(worktrees, Worktree{
					Path:   currentPath,
					Branch: currentBranch,
					BeadID: beadID,
				})
			}

			// Reset for next entry
			currentPath = ""
			currentBranch = ""
		}
	}

	// Handle last entry if output doesn't end with blank line
	if currentPath != "" && currentBranch != "" && strings.HasPrefix(currentBranch, "az/") {
		beadID := strings.TrimPrefix(currentBranch, "az/")
		worktrees = append(worktrees, Worktree{
			Path:   currentPath,
			Branch: currentBranch,
			BeadID: beadID,
		})
	}

	return worktrees
}
