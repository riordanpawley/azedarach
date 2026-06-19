package git

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	diffStatInsertionsPattern = regexp.MustCompile(`(\d+)\s+insertion(?:s)?\(\+\)`)
	diffStatDeletionsPattern  = regexp.MustCompile(`(\d+)\s+deletion(?:s)?\(-\)`)
)

const mergeCleanupTimeout = 15 * time.Second

// Client provides high-level git operations.
type Client struct {
	runner          CommandRunner
	logger          *slog.Logger
	worktreeLocksMu sync.Mutex
	worktreeLocks   map[string]*worktreeLock
}

// GitStatus represents the status of a git repository.
type GitStatus struct {
	Modified       []string `json:"modified"`
	Added          []string `json:"added"`
	Deleted        []string `json:"deleted"`
	Untracked      []string `json:"untracked"`
	Staged         []string `json:"staged"`
	Conflicted     []string `json:"conflicted,omitempty"`
	HasChanges     bool     `json:"has_changes"`
	HasConflicts   bool     `json:"has_conflicts,omitempty"`
	GitAdditions   int      `json:"git_additions,omitempty"`
	GitDeletions   int      `json:"git_deletions,omitempty"`
	GitAheadCount  int      `json:"git_ahead_count,omitempty"`
	GitBehindCount int      `json:"git_behind_count,omitempty"`
}

// MergeResult represents the result of a git merge operation.
type MergeResult struct {
	Success       bool
	HasConflicts  bool
	ConflictFiles []string
	Message       string
}

// DiffFileStatus represents a changed file status from git diff --name-status.
type DiffFileStatus string

const (
	DiffFileModified DiffFileStatus = "modified"
	DiffFileAdded    DiffFileStatus = "added"
	DiffFileDeleted  DiffFileStatus = "deleted"
	DiffFileRenamed  DiffFileStatus = "renamed"
)

// ChangedFile represents a changed file from git diff --name-status.
type ChangedFile struct {
	Path    string
	OldPath string
	Status  DiffFileStatus
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

	output, err := c.runInWorktree(ctx, worktree, "status", "--porcelain")
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
		"conflicted", len(status.Conflicted),
	)

	return status, nil
}

// RuntimeStatus returns porcelain status plus base-relative diff and branch counts.
// Metric failures are treated as best-effort so callers still receive dirty/clean state.
func (c *Client) RuntimeStatus(ctx context.Context, worktree, baseBranch string) (*GitStatus, error) {
	status, err := c.Status(ctx, worktree)
	if err != nil {
		return nil, err
	}

	if additions, deletions, err := c.DiffStatTotals(ctx, worktree, baseBranch); err == nil {
		status.GitAdditions = additions
		status.GitDeletions = deletions
	}

	if ahead, behind, err := c.BranchAheadBehind(ctx, worktree, baseBranch); err == nil {
		status.GitAheadCount = ahead
		status.GitBehindCount = behind
	}

	return status, nil
}

// Fetch fetches updates from the remote repository.
func (c *Client) Fetch(ctx context.Context, worktree, remote string) error {
	c.logger.Info("fetching from remote", "worktree", worktree, "remote", remote)

	_, err := c.runInWorktree(ctx, worktree, "fetch", remote)
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

	output, err := c.runInWorktree(ctx, worktree, "merge", "--no-edit", branch)

	result := &MergeResult{
		Success:      err == nil,
		HasConflicts: false,
		Message:      mergeResultMessage(output, err),
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
			if abortErr := c.AbortMerge(ctx, worktree); abortErr != nil {
				return nil, fmt.Errorf("failed to abort conflicted merge: %w", abortErr)
			}
		} else if c.mergeInProgress(ctx, worktree) {
			c.logger.Warn("merge commit failed; aborting incomplete merge",
				"branch", branch,
				"error", err,
			)
			if abortErr := c.AbortMerge(ctx, worktree); abortErr != nil {
				return nil, fmt.Errorf("failed to abort incomplete merge: %w", abortErr)
			}
		} else {
			c.logger.Error("merge failed", "branch", branch, "error", err)
			return nil, fmt.Errorf("failed to merge branch: %w", err)
		}
	} else {
		c.logger.Info("merge completed successfully", "branch", branch)
	}

	return result, nil
}

// MergeCleanly merges a branch and verifies the target worktree is clean after
// a merge attempt. If hooks or an interrupted merge leave new dirty files after
// starting from a clean target, those side effects are discarded and the merge
// result is reported as unsuccessful so higher-level integration can halt
// without leaving the target branch dirty.
func (c *Client) MergeCleanly(ctx context.Context, worktree, branch string) (*MergeResult, error) {
	preStatus, preStatusErr := c.Status(ctx, worktree)
	targetWasClean := preStatusErr == nil && !gitStatusDirty(preStatus)
	if preStatusErr != nil {
		c.logger.Debug("pre-merge target status check failed; post-merge cleanup disabled",
			"worktree", worktree,
			"branch", branch,
			"error", preStatusErr,
		)
	}

	result, err := c.Merge(ctx, worktree, branch)
	if err != nil {
		return c.cleanFailedMergeSideEffects(ctx, worktree, branch, targetWasClean, preStatusErr, err)
	}
	if result == nil {
		return result, err
	}
	if !result.Success {
		return c.cleanUnsuccessfulMergeSideEffects(ctx, worktree, branch, targetWasClean, preStatusErr, result)
	}

	postStatus, err := c.Status(ctx, worktree)
	if err != nil {
		return nil, fmt.Errorf("inspect target status after merge: %w", err)
	}
	if !gitStatusDirty(postStatus) {
		return result, nil
	}

	dirtySummary := gitStatusSummary(postStatus)
	if !targetWasClean {
		result.Success = false
		result.Message = appendMergeResultDetail(result.Message,
			fmt.Sprintf("merge completed but target worktree is dirty after merge; pre-merge status was not clean, leaving dirty files untouched: %s", dirtySummary))
		return result, nil
	}

	if err := c.DiscardChanges(ctx, worktree); err != nil {
		return nil, fmt.Errorf("merge completed but target worktree is dirty after merge (%s); failed to discard post-merge changes: %w", dirtySummary, err)
	}
	c.logger.Warn("merge left target dirty; discarded post-merge changes",
		"worktree", worktree,
		"branch", branch,
		"status", dirtySummary,
	)
	result.Success = false
	result.Message = appendMergeResultDetail(result.Message,
		fmt.Sprintf("merge completed but target worktree was dirty after post-merge hooks; discarded post-merge changes: %s", dirtySummary))
	return result, nil
}

func (c *Client) cleanUnsuccessfulMergeSideEffects(ctx context.Context, worktree, branch string, targetWasClean bool, preStatusErr error, result *MergeResult) (*MergeResult, error) {
	if preStatusErr != nil {
		return result, nil
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
	defer cancel()

	postStatus, statusErr := c.Status(cleanupCtx, worktree)
	if statusErr != nil {
		return nil, fmt.Errorf("inspect target status after unsuccessful merge: %w", statusErr)
	}
	if !gitStatusDirty(postStatus) {
		return result, nil
	}

	dirtySummary := gitStatusSummary(postStatus)
	if !targetWasClean {
		result.Message = appendMergeResultDetail(result.Message,
			fmt.Sprintf("merge was unsuccessful and target worktree is dirty; pre-merge status was not clean, leaving dirty files untouched: %s", dirtySummary))
		return result, nil
	}

	if err := c.DiscardChanges(cleanupCtx, worktree); err != nil {
		return nil, fmt.Errorf("merge was unsuccessful and target worktree is dirty (%s); failed to discard partial merge changes: %w", dirtySummary, err)
	}
	c.logger.Warn("unsuccessful merge left target dirty; discarded partial merge changes",
		"worktree", worktree,
		"branch", branch,
		"status", dirtySummary,
		"has_conflicts", result.HasConflicts,
	)
	result.Message = appendMergeResultDetail(result.Message,
		fmt.Sprintf("merge was unsuccessful after dirtying target worktree; discarded partial merge changes: %s", dirtySummary))
	return result, nil
}

func (c *Client) cleanFailedMergeSideEffects(ctx context.Context, worktree, branch string, targetWasClean bool, preStatusErr error, mergeErr error) (*MergeResult, error) {
	if preStatusErr != nil {
		return nil, mergeErr
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeCleanupTimeout)
	defer cancel()

	abortedIncompleteMerge := false
	if c.mergeInProgress(cleanupCtx, worktree) {
		if abortErr := c.AbortMerge(cleanupCtx, worktree); abortErr != nil {
			return nil, fmt.Errorf("merge failed (%v); failed to abort incomplete merge during cleanup: %w", mergeErr, abortErr)
		}
		abortedIncompleteMerge = true
	}

	postStatus, statusErr := c.Status(cleanupCtx, worktree)
	if statusErr != nil {
		return nil, fmt.Errorf("merge failed (%v); inspect target status after failed merge: %w", mergeErr, statusErr)
	}
	if !gitStatusDirty(postStatus) {
		if abortedIncompleteMerge {
			return &MergeResult{
				Success:      false,
				HasConflicts: false,
				Message: appendMergeResultDetail(
					strings.TrimSpace(mergeErr.Error()),
					"merge command failed with an incomplete merge in progress; aborted incomplete merge during cleanup",
				),
			}, nil
		}
		return nil, mergeErr
	}

	dirtySummary := gitStatusSummary(postStatus)
	result := &MergeResult{
		Success:      false,
		HasConflicts: false,
		Message:      strings.TrimSpace(mergeErr.Error()),
	}
	if !targetWasClean {
		result.Message = appendMergeResultDetail(result.Message,
			fmt.Sprintf("merge command failed and target worktree is dirty; pre-merge status was not clean, leaving dirty files untouched: %s", dirtySummary))
		return result, nil
	}

	if err := c.DiscardChanges(cleanupCtx, worktree); err != nil {
		return nil, fmt.Errorf("merge failed and target worktree is dirty (%s); failed to discard partial merge changes: %w", dirtySummary, err)
	}
	c.logger.Warn("failed merge left target dirty; discarded partial merge changes",
		"worktree", worktree,
		"branch", branch,
		"status", dirtySummary,
		"merge_error", mergeErr,
		"aborted_incomplete_merge", abortedIncompleteMerge,
	)
	detail := "merge command failed after dirtying target worktree; discarded partial merge changes"
	if abortedIncompleteMerge {
		detail = "merge command failed with an incomplete merge in progress; aborted incomplete merge and discarded partial merge changes"
	}
	result.Message = appendMergeResultDetail(result.Message,
		fmt.Sprintf("%s: %s", detail, dirtySummary))
	return result, nil
}

func mergeResultMessage(output string, err error) string {
	output = strings.TrimSpace(output)
	if err == nil {
		return output
	}
	if output == "" {
		return err.Error()
	}
	return output + "\n" + err.Error()
}

func appendMergeResultDetail(message, detail string) string {
	message = strings.TrimSpace(message)
	detail = strings.TrimSpace(detail)
	switch {
	case message == "":
		return detail
	case detail == "":
		return message
	default:
		return message + "\n" + detail
	}
}

func (c *Client) mergeInProgress(ctx context.Context, worktree string) bool {
	output, err := c.runInWorktree(ctx, worktree, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	return err == nil && strings.TrimSpace(output) != ""
}

// AbortMerge aborts an ongoing merge operation.
func (c *Client) AbortMerge(ctx context.Context, worktree string) error {
	c.logger.Info("aborting merge", "worktree", worktree)

	_, err := c.runInWorktree(ctx, worktree, "merge", "--abort")
	if err != nil {
		return fmt.Errorf("failed to abort merge: %w", err)
	}

	c.logger.Info("merge aborted successfully")
	return nil
}

// MergeTreeWriteTree runs git merge-tree --write-tree to predict merge conflicts.
func (c *Client) MergeTreeWriteTree(ctx context.Context, worktree, targetRef, sourceBranch string) (string, error) {
	output, err := c.runInWorktree(ctx, worktree, "merge-tree", "--write-tree", targetRef, sourceBranch)
	if err != nil {
		return output, fmt.Errorf("failed to run merge-tree: %w", err)
	}
	return output, nil
}

// RestoreAll restores tracked changes in both index and worktree.
func (c *Client) RestoreAll(ctx context.Context, worktree string) error {
	if _, err := c.runInWorktree(ctx, worktree, "restore", "--staged", "--worktree", "."); err != nil {
		return fmt.Errorf("failed to restore changes: %w", err)
	}
	return nil
}

// CleanForce removes untracked files and directories.
func (c *Client) CleanForce(ctx context.Context, worktree string) error {
	if _, err := c.runInWorktree(ctx, worktree, "clean", "-fd"); err != nil {
		return fmt.Errorf("failed to clean changes: %w", err)
	}
	return nil
}

// AddAll stages all changes in the worktree.
func (c *Client) AddAll(ctx context.Context, worktree string) error {
	if _, err := c.runInWorktree(ctx, worktree, "add", "-A"); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}
	return nil
}

// Commit creates a commit in the worktree.
func (c *Client) Commit(ctx context.Context, worktree, message string) error {
	if _, err := c.runInWorktree(ctx, worktree, "commit", "-m", message); err != nil {
		return fmt.Errorf("failed to commit changes: %w", err)
	}
	return nil
}

// Diff returns the diff output for the working directory.
func (c *Client) Diff(ctx context.Context, worktree string) (string, error) {
	c.logger.Debug("getting diff", "worktree", worktree)

	output, err := c.runInWorktree(ctx, worktree, "diff")
	if err != nil {
		return "", fmt.Errorf("failed to get diff: %w", err)
	}

	return output, nil
}

// DiffStat returns the diff stat output (summary of changes).
func (c *Client) DiffStat(ctx context.Context, worktree, baseBranch string) (string, error) {
	c.logger.Debug("getting diff stat", "worktree", worktree)

	mergeBase, err := c.MergeBase(ctx, worktree, baseBranch)
	if err == nil {
		output, diffErr := c.runInWorktree(ctx, worktree, "diff", "--shortstat", mergeBase, "HEAD", "--", ":^.azedarach")
		if diffErr == nil {
			return strings.TrimSpace(output), nil
		}
		c.logger.Warn("base diff stat failed; falling back to local staged/unstaged aggregation",
			"baseBranch", strings.TrimSpace(baseBranch),
			"error", diffErr,
		)
	} else if strings.TrimSpace(baseBranch) != "" {
		c.logger.Warn("base diff stat failed; falling back to local staged/unstaged aggregation",
			"baseBranch", strings.TrimSpace(baseBranch),
			"error", err,
		)
	}

	unstagedOutput, err := c.runInWorktree(ctx, worktree, "diff", "--shortstat")
	if err != nil {
		return "", fmt.Errorf("failed to get unstaged diff stat: %w", err)
	}

	stagedOutput, err := c.runInWorktree(ctx, worktree, "diff", "--cached", "--shortstat")
	if err != nil {
		return "", fmt.Errorf("failed to get staged diff stat: %w", err)
	}

	unstagedOutput = strings.TrimSpace(unstagedOutput)
	stagedOutput = strings.TrimSpace(stagedOutput)
	switch {
	case unstagedOutput != "" && stagedOutput != "":
		return unstagedOutput + "\n" + stagedOutput, nil
	case unstagedOutput != "":
		return unstagedOutput, nil
	default:
		return stagedOutput, nil
	}
}

// DiffStatTotals parses additions and deletions from DiffStat output.
func (c *Client) DiffStatTotals(ctx context.Context, worktree, baseBranch string) (int, int, error) {
	diffStat, err := c.DiffStat(ctx, worktree, baseBranch)
	if err != nil {
		return 0, 0, err
	}
	additions, deletions := parseDiffStatTotals(diffStat)
	return additions, deletions, nil
}

// MergeBase resolves the merge base between base branch and HEAD.
func (c *Client) MergeBase(ctx context.Context, worktree, baseBranch string) (string, error) {
	baseBranch = strings.TrimSpace(baseBranch)
	candidates := c.baseRefCandidates(ctx, worktree, baseBranch)
	if len(candidates) == 0 {
		return "", fmt.Errorf("base branch is empty")
	}

	var lastErr error
	attempted := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		attempted = append(attempted, candidate)
		mergeBaseOutput, err := c.runInWorktree(ctx, worktree, "merge-base", candidate, "HEAD")
		if err != nil {
			lastErr = err
			continue
		}
		mergeBase := strings.TrimSpace(mergeBaseOutput)
		if mergeBase == "" {
			mergeBase = candidate
		}
		return mergeBase, nil
	}

	if lastErr != nil {
		return "", fmt.Errorf("failed to resolve merge-base for %s after trying %s: %w", baseBranch, strings.Join(attempted, ", "), lastErr)
	}
	return "", fmt.Errorf("failed to resolve merge-base for %s", baseBranch)
}

// MergeBaseLocal resolves the merge base between a local base branch and HEAD.
func (c *Client) MergeBaseLocal(ctx context.Context, worktree, baseBranch string) (string, error) {
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		return "", fmt.Errorf("base branch is empty")
	}

	mergeBaseOutput, err := c.runInWorktree(ctx, worktree, "merge-base", baseBranch, "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to resolve merge-base for local branch %s: %w", baseBranch, err)
	}
	mergeBase := strings.TrimSpace(mergeBaseOutput)
	if mergeBase == "" {
		return baseBranch, nil
	}
	return mergeBase, nil
}

// ChangedFiles returns changed files from merge-base..HEAD.
func (c *Client) ChangedFiles(ctx context.Context, worktree, baseBranch string) ([]ChangedFile, error) {
	mergeBase, err := c.MergeBase(ctx, worktree, baseBranch)
	if err != nil {
		return nil, err
	}

	output, err := c.runInWorktree(ctx, worktree, "diff", "--name-status", mergeBase, "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}
	return parseChangedFilesOutput(output), nil
}

// ChangedFilesLocalBase returns changed files from the local base branch merge-base..HEAD.
func (c *Client) ChangedFilesLocalBase(ctx context.Context, worktree, baseBranch string) ([]ChangedFile, error) {
	mergeBase, err := c.MergeBaseLocal(ctx, worktree, baseBranch)
	if err != nil {
		return nil, err
	}

	output, err := c.runInWorktree(ctx, worktree, "diff", "--name-status", mergeBase, "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}
	return parseChangedFilesOutput(output), nil
}

// Push pushes the specified branch to the remote repository.
func (c *Client) Push(ctx context.Context, worktree, remote, branch string) error {
	c.logger.Info("pushing branch", "worktree", worktree, "remote", remote, "branch", branch)

	_, err := c.runInWorktree(ctx, worktree, "push", remote, branch)
	if err != nil {
		return fmt.Errorf("failed to push branch: %w", err)
	}

	c.logger.Info("push completed successfully", "remote", remote, "branch", branch)
	return nil
}

// CurrentBranch returns the name of the current branch.
func (c *Client) CurrentBranch(ctx context.Context, worktree string) (string, error) {
	c.logger.Debug("getting current branch", "worktree", worktree)

	output, err := c.runInWorktree(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}

	branch := strings.TrimSpace(output)
	c.logger.Debug("current branch", "branch", branch)

	return branch, nil
}

// WorktreePathForBranch returns the path of the git worktree currently attached to branch.
func (c *Client) WorktreePathForBranch(ctx context.Context, branch string) (string, bool, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", false, fmt.Errorf("branch is empty")
	}

	output, err := c.runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, fmt.Errorf("failed to list worktrees: %w", err)
	}

	lines := strings.Split(output, "\n")
	var currentPath string
	var currentBranch string
	flush := func() (string, bool) {
		if currentPath == "" || currentBranch == "" {
			return "", false
		}
		if currentBranch == branch {
			return currentPath, true
		}
		return "", false
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			if path, ok := flush(); ok {
				return path, true, nil
			}
			currentPath = strings.TrimPrefix(line, "worktree ")
			currentBranch = ""
		case strings.HasPrefix(line, "branch "):
			branchRef := strings.TrimPrefix(line, "branch ")
			currentBranch = strings.TrimPrefix(branchRef, "refs/heads/")
		case line == "":
			if path, ok := flush(); ok {
				return path, true, nil
			}
			currentPath = ""
			currentBranch = ""
		}
	}
	if path, ok := flush(); ok {
		return path, true, nil
	}

	return "", false, nil
}

// Checkout checks out the specified branch.
func (c *Client) Checkout(ctx context.Context, worktree, branch string) error {
	c.logger.Info("checking out branch", "worktree", worktree, "branch", branch)

	_, err := c.runInWorktree(ctx, worktree, "checkout", branch)
	if err != nil {
		return fmt.Errorf("failed to checkout branch: %w", err)
	}

	c.logger.Info("checkout completed successfully", "branch", branch)
	return nil
}

// RevListCount returns the number of commits between two references.
// This is used by the GitSyncService to determine how far behind origin the local branch is.
func (c *Client) RevListCount(ctx context.Context, worktree, revRange string) (int, error) {
	c.logger.Debug("getting rev-list count", "worktree", worktree, "range", revRange)

	output, err := c.runInWorktree(ctx, worktree, "rev-list", "--count", revRange)
	if err != nil {
		return 0, fmt.Errorf("failed to get rev-list count: %w", err)
	}

	var count int
	_, err = fmt.Sscanf(strings.TrimSpace(output), "%d", &count)
	if err != nil {
		return 0, fmt.Errorf("failed to parse rev-list count: %w", err)
	}

	return count, nil
}

// BranchAheadBehind reports commit deltas for HEAD relative to the base branch.
// It tries the local base branch first, then falls back to origin/<base>.
func (c *Client) BranchAheadBehind(ctx context.Context, worktree, baseBranch string) (int, int, error) {
	baseBranch = strings.TrimSpace(baseBranch)
	candidates := c.baseRefCandidates(ctx, worktree, baseBranch)
	if len(candidates) == 0 {
		return 0, 0, fmt.Errorf("base branch is empty")
	}

	var lastErr error
	attempted := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		attempted = append(attempted, candidate)
		behind, err := c.RevListCount(ctx, worktree, "HEAD.."+candidate)
		if err != nil {
			lastErr = err
			continue
		}
		ahead, err := c.RevListCount(ctx, worktree, candidate+"..HEAD")
		if err != nil {
			lastErr = err
			continue
		}
		return ahead, behind, nil
	}

	if lastErr != nil {
		return 0, 0, fmt.Errorf("failed to resolve branch delta for %s after trying %s: %w", baseBranch, strings.Join(attempted, ", "), lastErr)
	}
	return 0, 0, fmt.Errorf("failed to resolve branch delta for %s", baseBranch)
}

func (c *Client) baseRefCandidates(ctx context.Context, worktree, baseBranch string) []string {
	ordered := make([]string, 0, 10)
	seen := map[string]struct{}{}
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		if _, exists := seen[ref]; exists {
			return
		}
		seen[ref] = struct{}{}
		ordered = append(ordered, ref)
	}

	normalizedBase := strings.ToLower(strings.TrimSpace(baseBranch))
	genericBase := normalizedBase == "" || normalizedBase == "main" || normalizedBase == "master"

	// Prefer remote default branch when configured base is generic (main/master).
	// This avoids massive, misleading deltas in repos whose canonical base branch differs.
	if headRef, err := c.runInWorktree(ctx, worktree, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		headRef = strings.TrimSpace(headRef) // e.g. origin/main or origin/trunk
		headLocal := strings.TrimPrefix(headRef, "origin/")
		if genericBase {
			if !strings.EqualFold(headRef, "origin/"+normalizedBase) && !strings.EqualFold(headLocal, normalizedBase) {
				add(headRef)
				add(headLocal)
			}
		}
		add(baseBranch)
		if baseBranch != "" && !strings.Contains(baseBranch, "/") {
			add("origin/" + baseBranch)
		}
		if genericBase {
			add(headRef)
			add(headLocal)
		}
	} else {
		add(baseBranch)
		if baseBranch != "" && !strings.Contains(baseBranch, "/") {
			add("origin/" + baseBranch)
		}
	}

	// Conservative well-known fallback refs.
	if genericBase {
		add("main")
		add("origin/main")
		add("master")
		add("origin/master")
	}

	return ordered
}

func (c *Client) runInWorktree(ctx context.Context, worktree string, args ...string) (string, error) {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return c.runner.Run(ctx, args...)
	}
	prefixed := make([]string, 0, len(args)+2)
	prefixed = append(prefixed, "-C", worktree)
	prefixed = append(prefixed, args...)
	return c.runner.Run(ctx, prefixed...)
}

// Pull pulls updates from the remote repository.
// This is used for updating the local base branch when origin mode is enabled.
func (c *Client) Pull(ctx context.Context, worktree, remote, branch string) error {
	c.logger.Info("pulling from remote", "worktree", worktree, "remote", remote, "branch", branch)

	_, err := c.runInWorktree(ctx, worktree, "pull", remote, branch)
	if err != nil {
		return fmt.Errorf("failed to pull from remote: %w", err)
	}

	c.logger.Info("pull completed successfully", "remote", remote, "branch", branch)
	return nil
}

// FetchRef updates a local ref from a remote ref without switching branches.
// This allows updating the base branch (e.g., main) while the user is working on a feature branch.
func (c *Client) FetchRef(ctx context.Context, worktree, remote, refSpec string) error {
	c.logger.Info("fetching ref", "worktree", worktree, "remote", remote, "refSpec", refSpec)

	_, err := c.runInWorktree(ctx, worktree, "fetch", remote, refSpec)
	if err != nil {
		return fmt.Errorf("failed to fetch ref: %w", err)
	}

	c.logger.Info("fetch ref completed successfully", "remote", remote, "refSpec", refSpec)
	return nil
}

// parseGitStatus parses the output of 'git status --porcelain'.
// The format is: XY PATH
// Where X is the status of the index and Y is the status of the working tree.
//
// Examples:
//
//	M  file.txt  - modified in index (staged)
//	 M file.txt  - modified in working tree (unstaged)
//	A  file.txt  - added to index (staged)
//	D  file.txt  - deleted from index (staged)
//	?? file.txt  - untracked file
//	MM file.txt  - modified in both index and working tree
func parseGitStatus(output string) *GitStatus {
	status := &GitStatus{
		Modified:   make([]string, 0),
		Added:      make([]string, 0),
		Deleted:    make([]string, 0),
		Untracked:  make([]string, 0),
		Staged:     make([]string, 0),
		Conflicted: make([]string, 0),
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
		statusCode := line[:2]

		// Check if file is staged (index status is not space or ?)
		if indexStatus != ' ' && indexStatus != '?' {
			status.Staged = append(status.Staged, path)
		}

		// Parse status codes
		switch {
		case isUnmergedStatus(statusCode):
			status.Conflicted = append(status.Conflicted, path)
		case statusCode == "??":
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
		len(status.Staged) > 0 ||
		len(status.Conflicted) > 0
	status.HasConflicts = len(status.Conflicted) > 0

	return status
}

func gitStatusDirty(status *GitStatus) bool {
	if status == nil {
		return false
	}
	return status.HasChanges ||
		status.HasConflicts ||
		len(status.Modified) > 0 ||
		len(status.Added) > 0 ||
		len(status.Deleted) > 0 ||
		len(status.Untracked) > 0 ||
		len(status.Staged) > 0 ||
		len(status.Conflicted) > 0
}

func gitStatusSummary(status *GitStatus) string {
	if status == nil {
		return "unknown"
	}
	parts := make([]string, 0, 6)
	add := func(label string, values []string) {
		if len(values) == 0 {
			return
		}
		parts = append(parts, fmt.Sprintf("%d %s (%s)", len(values), label, strings.Join(values, ", ")))
	}
	add("modified", status.Modified)
	add("added", status.Added)
	add("deleted", status.Deleted)
	add("untracked", status.Untracked)
	add("staged", status.Staged)
	add("conflicted", status.Conflicted)
	if len(parts) == 0 {
		return "dirty"
	}
	return strings.Join(parts, ", ")
}

func isUnmergedStatus(statusCode string) bool {
	switch statusCode {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
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

func parseChangedFilesOutput(output string) []ChangedFile {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return []ChangedFile{}
	}

	lines := strings.Split(trimmed, "\n")
	changed := make([]ChangedFile, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		statusCode := strings.TrimSpace(parts[0])
		if statusCode == "" {
			continue
		}

		if strings.HasPrefix(statusCode, "R") && len(parts) >= 3 {
			changed = append(changed, ChangedFile{
				OldPath: parts[1],
				Path:    parts[2],
				Status:  DiffFileRenamed,
			})
			continue
		}

		path := parts[1]
		status := DiffFileModified
		switch statusCode {
		case "A":
			status = DiffFileAdded
		case "D":
			status = DiffFileDeleted
		case "M":
			status = DiffFileModified
		}
		changed = append(changed, ChangedFile{
			Path:   path,
			Status: status,
		})
	}

	return changed
}

func parseDiffStatTotals(diffStat string) (int, int) {
	insertions := 0
	for _, match := range diffStatInsertionsPattern.FindAllStringSubmatch(diffStat, -1) {
		if len(match) < 2 {
			continue
		}
		if value, err := strconv.Atoi(match[1]); err == nil {
			insertions += value
		}
	}

	deletions := 0
	for _, match := range diffStatDeletionsPattern.FindAllStringSubmatch(diffStat, -1) {
		if len(match) < 2 {
			continue
		}
		if value, err := strconv.Atoi(match[1]); err == nil {
			deletions += value
		}
	}

	return insertions, deletions
}
