package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// WorktreeManager manages git worktrees for Claude Code sessions.
type WorktreeManager struct {
	runner  CommandRunner
	logger  *slog.Logger
	repoDir string // Main repository directory (absolute path)
}

// Worktree represents a git worktree associated with a issue.
type Worktree struct {
	Path    string // Absolute path to the worktree
	Branch  string // Branch name (e.g., "author/issue-id/title-slug")
	IssueID string // Associated issue ID
}

var (
	ErrWorktreeNotFound      = errors.New("worktree not found")
	ErrWorktreeAlreadyExists = errors.New("worktree already exists")
)

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

// Create creates a new worktree for the given issue ID.
func (w *WorktreeManager) Create(ctx context.Context, issueID string, baseBranch string) (*Worktree, error) {
	return w.CreateWithTitle(ctx, issueID, "", baseBranch)
}

// CreateWithTitle creates a new worktree and derives a deterministic branch name
// from git user, issue ID, and issue title.
func (w *WorktreeManager) CreateWithTitle(ctx context.Context, issueID, issueTitle, baseBranch string) (*Worktree, error) {
	// Get repository name from repoDir
	repoName := filepath.Base(w.repoDir)

	// Calculate worktree path: ../RepoName-issueID/
	worktreePath := filepath.Join(filepath.Dir(w.repoDir), fmt.Sprintf("%s-%s", repoName, issueID))

	branchAuthor := w.resolveBranchAuthor(ctx)
	branchTitle := strings.TrimSpace(issueTitle)
	if branchTitle == "" {
		branchTitle = issueID
	}
	branchName := naming.ComposeIssueBranchName(branchAuthor, issueID, branchTitle, 24)

	w.logger.Info("creating worktree",
		"issueID", issueID,
		"path", worktreePath,
		"branch", branchName,
		"baseBranch", baseBranch,
	)

	// Check if worktree already exists
	exists, err := w.Exists(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to check if worktree exists: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("%w for issue %s", ErrWorktreeAlreadyExists, issueID)
	}

	// Create worktree with new branch from baseBranch.
	_, err = w.runner.Run(ctx, "worktree", "add", "-b", branchName, worktreePath, baseBranch)
	if err != nil {
		// A previous partial attempt may have created the branch already.
		// In that case, attach a new worktree to the existing branch.
		if isBranchAlreadyExistsError(err, branchName) {
			_, retryErr := w.runner.Run(ctx, "worktree", "add", worktreePath, branchName)
			if retryErr != nil {
				return nil, fmt.Errorf("failed to create worktree: %w", retryErr)
			}
		} else {
			return nil, fmt.Errorf("failed to create worktree: %w", err)
		}
	}

	w.logger.Info("worktree created successfully", "issueID", issueID, "path", worktreePath)

	return &Worktree{
		Path:    worktreePath,
		Branch:  branchName,
		IssueID: issueID,
	}, nil
}

// Delete removes the worktree and branch for the given issue ID.
func (w *WorktreeManager) Delete(ctx context.Context, issueID string) error {
	return w.DeleteWithOptions(ctx, issueID, false)
}

// DeleteWithOptions removes the worktree and branch for the given issue ID.
func (w *WorktreeManager) DeleteWithOptions(ctx context.Context, issueID string, force bool) error {
	w.logger.Info("deleting worktree", "issueID", issueID)

	// Get worktree info to find the path
	worktree, err := w.Get(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to get worktree info: %w", err)
	}

	// Remove worktree
	// git worktree remove [--force] <path>
	removeArgs := []string{"worktree", "remove"}
	if force {
		removeArgs = append(removeArgs, "--force")
	}
	removeArgs = append(removeArgs, worktree.Path)
	_, err = w.runner.Run(ctx, removeArgs...)
	if err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	// Delete branch
	// git branch -D az/issueID
	_, err = w.runner.Run(ctx, "branch", "-D", worktree.Branch)
	if err != nil {
		// Log warning but don't fail - branch might already be deleted
		w.logger.Warn("failed to delete branch", "branch", worktree.Branch, "error", err)
	}

	w.logger.Info("worktree deleted successfully", "issueID", issueID)

	return nil
}

// Get returns information about the worktree for the given issue ID.
func (w *WorktreeManager) Get(ctx context.Context, issueID string) (*Worktree, error) {
	worktrees, err := w.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	for _, wt := range worktrees {
		if naming.IssueIDsEqual(wt.IssueID, issueID) {
			return &wt, nil
		}
	}

	return nil, fmt.Errorf("%w for issue %s", ErrWorktreeNotFound, issueID)
}

// List returns all worktrees managed by this WorktreeManager.
func (w *WorktreeManager) List(ctx context.Context) ([]Worktree, error) {
	// git worktree list --porcelain
	output, err := w.runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return w.parseWorktreeList(output), nil
}

// Exists checks if a worktree exists for the given issue ID.
func (w *WorktreeManager) Exists(ctx context.Context, issueID string) (bool, error) {
	_, err := w.Get(ctx, issueID)
	if err != nil {
		if errors.Is(err, ErrWorktreeNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// parseWorktreeList parses the output of 'git worktree list --porcelain'.
func (w *WorktreeManager) parseWorktreeList(output string) []Worktree {
	worktrees := make([]Worktree, 0)

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
			issueID, ok := naming.ExtractIssueIDFromBranchName(currentBranch)
			if ok {
				worktrees = append(worktrees, Worktree{
					Path:    currentPath,
					Branch:  currentBranch,
					IssueID: issueID,
				})
			}
			currentPath = ""
			currentBranch = ""
		}
	}

	// Handle last entry if output doesn't end with blank line.
	if currentPath != "" && currentBranch != "" {
		issueID, ok := naming.ExtractIssueIDFromBranchName(currentBranch)
		if !ok {
			return worktrees
		}
		worktrees = append(worktrees, Worktree{
			Path:    currentPath,
			Branch:  currentBranch,
			IssueID: issueID,
		})
	}

	return worktrees
}

func (w *WorktreeManager) resolveBranchAuthor(ctx context.Context) string {
	if configuredName, err := w.runner.Run(ctx, "config", "user.name"); err == nil {
		if author := naming.SanitizeBranchAuthor(configuredName); author != "" {
			return author
		}
	}
	if envUser := naming.SanitizeBranchAuthor(os.Getenv("USER")); envUser != "" {
		return envUser
	}
	return "author"
}

func isBranchAlreadyExistsError(err error, branchName string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") && strings.Contains(msg, strings.ToLower(branchName))
}
