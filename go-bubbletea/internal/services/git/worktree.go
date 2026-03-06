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

// Worktree represents a git worktree associated with a issue.
type Worktree struct {
	Path    string // Absolute path to the worktree
	Branch  string // Branch name (e.g., "az/issue-123")
	IssueID string // Associated issue ID
}

// BranchOriginKind represents the source used to create a missing issue branch.
type BranchOriginKind string

const (
	BranchOriginBase     BranchOriginKind = "base"
	BranchOriginUpstream BranchOriginKind = "upstream"
)

// UpstreamBranchSource represents an eligible upstream-related issue branch.
type UpstreamBranchSource struct {
	IssueID string
	Branch  string
}

// BranchOriginOption is one selectable runtime origin candidate.
type BranchOriginOption struct {
	Kind          BranchOriginKind
	Branch        string
	SourceIssueID string
	Label         string
}

// BranchOriginSelection captures the explicit user choice for branch origin.
type BranchOriginSelection struct {
	Kind          BranchOriginKind
	SourceIssueID string
}

// BranchOriginChooser describes runtime branch-origin options for create/recreate flows.
type BranchOriginChooser struct {
	IssueID                           string
	TargetBranch                      string
	BaseBranch                        string
	Recreate                          bool
	Options                           []BranchOriginOption
	UpstreamUnavailableReason         string
	RequiresExplicitUpstreamSelection bool
}

func (c BranchOriginChooser) upstreamOptions() []BranchOriginOption {
	opts := make([]BranchOriginOption, 0, len(c.Options))
	for _, option := range c.Options {
		if option.Kind == BranchOriginUpstream {
			opts = append(opts, option)
		}
	}
	return opts
}

// Resolve returns the chosen source branch from a runtime origin selection.
func (c BranchOriginChooser) Resolve(selection BranchOriginSelection) (string, error) {
	switch selection.Kind {
	case BranchOriginBase:
		if strings.TrimSpace(c.BaseBranch) == "" {
			return "", fmt.Errorf("base branch is empty")
		}
		return c.BaseBranch, nil
	case BranchOriginUpstream:
		upstream := c.upstreamOptions()
		if len(upstream) == 0 {
			return "", fmt.Errorf("no eligible upstream source branches available")
		}
		if len(upstream) > 1 && strings.TrimSpace(selection.SourceIssueID) == "" {
			return "", fmt.Errorf("explicit source selection required when multiple upstream sources are available")
		}
		if strings.TrimSpace(selection.SourceIssueID) == "" {
			return upstream[0].Branch, nil
		}
		for _, option := range upstream {
			if option.SourceIssueID == selection.SourceIssueID {
				return option.Branch, nil
			}
		}
		return "", fmt.Errorf("upstream source issue %s is not eligible", selection.SourceIssueID)
	default:
		return "", fmt.Errorf("invalid branch origin kind: %q", selection.Kind)
	}
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

// BuildBranchOriginChooser builds runtime branch-origin options for missing/recreated issue branches.
func (w *WorktreeManager) BuildBranchOriginChooser(issueID string, baseBranch string, upstreamSources []UpstreamBranchSource, recreate bool) BranchOriginChooser {
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}

	chooser := BranchOriginChooser{
		IssueID:      issueID,
		TargetBranch: fmt.Sprintf("az/%s", issueID),
		BaseBranch:   baseBranch,
		Recreate:     recreate,
		Options: []BranchOriginOption{
			{
				Kind:   BranchOriginBase,
				Branch: baseBranch,
				Label:  fmt.Sprintf("Configured base branch (%s)", baseBranch),
			},
		},
	}

	for _, source := range upstreamSources {
		sourceIssueID := strings.TrimSpace(source.IssueID)
		sourceBranch := strings.TrimSpace(source.Branch)
		if sourceIssueID == "" || sourceBranch == "" {
			continue
		}

		chooser.Options = append(chooser.Options, BranchOriginOption{
			Kind:          BranchOriginUpstream,
			Branch:        sourceBranch,
			SourceIssueID: sourceIssueID,
			Label:         fmt.Sprintf("Upstream issue %s (%s)", sourceIssueID, sourceBranch),
		})
	}

	upstreamOptions := chooser.upstreamOptions()
	chooser.RequiresExplicitUpstreamSelection = len(upstreamOptions) > 1
	if len(upstreamOptions) == 0 {
		chooser.UpstreamUnavailableReason = "no eligible upstream issue branches available; create from configured base branch"
	}

	return chooser
}

// Create creates a new worktree for the given issue ID.
// It creates the worktree at ../RepoName-issueID/ with branch az/issueID.
func (w *WorktreeManager) Create(ctx context.Context, issueID string, baseBranch string) (*Worktree, error) {
	// Get repository name from repoDir
	repoName := filepath.Base(w.repoDir)

	// Calculate worktree path: ../RepoName-issueID/
	worktreePath := filepath.Join(filepath.Dir(w.repoDir), fmt.Sprintf("%s-%s", repoName, issueID))

	// Branch name: az/issueID
	branchName := fmt.Sprintf("az/%s", issueID)

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
		return nil, fmt.Errorf("worktree for issue %s already exists", issueID)
	}

	// Create worktree with new branch from baseBranch
	// git worktree add -b az/issueID ../RepoName-issueID baseBranch
	_, err = w.runner.Run(ctx, "worktree", "add", "-b", branchName, worktreePath, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w", err)
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
	w.logger.Info("deleting worktree", "issueID", issueID)

	// Get worktree info to find the path
	worktree, err := w.Get(ctx, issueID)
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
		if wt.IssueID == issueID {
			return &wt, nil
		}
	}

	return nil, fmt.Errorf("worktree for issue %s not found", issueID)
}

// List returns all worktrees managed by this WorktreeManager.
// It filters for worktrees that match the az/issueID pattern.
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
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// parseWorktreeList parses the output of 'git worktree list --porcelain'.
// Example output:
//
//	worktree /home/user/repo
//	HEAD abc123
//	branch refs/heads/main
//
//	worktree /home/user/repo-issue-123
//	HEAD def456
//	branch refs/heads/az/issue-123
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
			// End of worktree entry
			// Only include worktrees with az/ branches
			if strings.HasPrefix(currentBranch, "az/") {
				issueID := strings.TrimPrefix(currentBranch, "az/")
				worktrees = append(worktrees, Worktree{
					Path:    currentPath,
					Branch:  currentBranch,
					IssueID: issueID,
				})
			}

			// Reset for next entry
			currentPath = ""
			currentBranch = ""
		}
	}

	// Handle last entry if output doesn't end with blank line
	if currentPath != "" && currentBranch != "" && strings.HasPrefix(currentBranch, "az/") {
		issueID := strings.TrimPrefix(currentBranch, "az/")
		worktrees = append(worktrees, Worktree{
			Path:    currentPath,
			Branch:  currentBranch,
			IssueID: issueID,
		})
	}

	return worktrees
}
