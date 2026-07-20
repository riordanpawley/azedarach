package git

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
)

var (
	diffStatInsertionsPattern = regexp.MustCompile(`(\d+)\s+insertion(?:s)?\(\+\)`)
	diffStatDeletionsPattern  = regexp.MustCompile(`(\d+)\s+deletion(?:s)?\(-\)`)
)

var mergeHookNames = map[string]struct{}{
	"pre-merge-commit":   {},
	"prepare-commit-msg": {},
	"commit-msg":         {},
	"post-merge":         {},
}

const (
	mergeCleanupTimeout           = 15 * time.Second
	diffStatFallbackTimeout       = 1500 * time.Millisecond
	diffStatFailureBackoff        = 5 * time.Minute
	maxDiffStatBackoffReasonRunes = 180
	maxRefGraphSnapshotCommits    = 5000
)

// Client provides high-level git operations.
type Client struct {
	runner                 CommandRunner
	logger                 *slog.Logger
	worktreeLocksMu        sync.Mutex
	worktreeLocks          map[string]*worktreeLock
	diffStatMu             sync.Mutex
	diffStatBackoff        map[string]diffStatBackoffState
	now                    func() time.Time
	removeJournal          func(string) error
	syncJournalDir         func(string) error
	artifactFailureTempDir string
	artifactCopyChunk      func(string, int)
}

type diffStatBackoffState struct {
	Until  time.Time
	Reason string
}

type diffStatBackoffError struct {
	Operation  string
	BaseBranch string
	Until      time.Time
	Reason     string
}

func (e diffStatBackoffError) Error() string {
	operation := strings.TrimSpace(e.Operation)
	if operation == "" {
		operation = "git diff stat"
	}
	baseBranch := strings.TrimSpace(e.BaseBranch)
	if baseBranch == "" {
		baseBranch = "working tree"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "previous failure"
	}
	return fmt.Sprintf("%s backoff active for %s until %s: %s", operation, baseBranch, e.Until.UTC().Format(time.RFC3339), reason)
}

// GitStatus represents the status of a git repository.
type GitStatus struct {
	Modified          []string `json:"modified"`
	Added             []string `json:"added"`
	Deleted           []string `json:"deleted"`
	Untracked         []string `json:"untracked"`
	Staged            []string `json:"staged"`
	Conflicted        []string `json:"conflicted,omitempty"`
	HasChanges        bool     `json:"has_changes"`
	HasConflicts      bool     `json:"has_conflicts,omitempty"`
	GitAdditions      int      `json:"git_additions,omitempty"`
	GitDeletions      int      `json:"git_deletions,omitempty"`
	GitAheadCount     int      `json:"git_ahead_count,omitempty"`
	GitBehindCount    int      `json:"git_behind_count,omitempty"`
	hasTrackedChanges bool
}

// MarshalJSON preserves the complete status envelope consumed by runtime
// projections. Required list fields are always JSON arrays, including for a
// clean repository.
func (s GitStatus) MarshalJSON() ([]byte, error) {
	type gitStatusJSON GitStatus
	if s.Modified == nil {
		s.Modified = []string{}
	}
	if s.Added == nil {
		s.Added = []string{}
	}
	if s.Deleted == nil {
		s.Deleted = []string{}
	}
	if s.Untracked == nil {
		s.Untracked = []string{}
	}
	if s.Staged == nil {
		s.Staged = []string{}
	}
	return json.Marshal(gitStatusJSON(s))
}

// EvidenceCommit is an issue-scoped commit found on a branch.
type EvidenceCommit struct {
	Hash    string
	Subject string
}

// RefGraphSnapshot is one immutable view of several refs captured by a single
// git-log process. It supports snapshot-wide containment analysis without
// issuing subprocesses per issue, commit, or branch.
type RefGraphSnapshot struct {
	Tips      map[string]string
	Commits   map[string]RefGraphCommit
	Order     []string
	Truncated bool
	reachable map[string]map[string]struct{}
	complete  map[string]bool
}

type RefGraphCommit struct {
	Hash         string
	Parents      []string
	Subject      string
	ChangedFiles []string
}

// MergeResult represents the result of a git merge operation.
type MergeResult struct {
	Success            bool
	HasConflicts       bool
	ConflictFiles      []string
	Message            string
	HookDiagnostics    []GitHookDiagnostic          `json:"hook_diagnostics,omitempty"`
	ValidationAttempts []CandidateValidationAttempt `json:"validation_attempts,omitempty"`
}

type CandidateValidationStatus = domain.IntegrationCandidateValidationStatus

// CandidateValidationAttempt identifies the exact reconciled commit evaluated
// by a repository integration gate. Canonical is true only after that same OID
// has been applied to the target worktree successfully.
type CandidateValidationAttempt = domain.IntegrationCandidateValidationAttempt

const (
	CandidateValidationRunning    = domain.IntegrationCandidateValidationRunning
	CandidateValidationPassed     = domain.IntegrationCandidateValidationPassed
	CandidateValidationFailed     = domain.IntegrationCandidateValidationFailed
	CandidateValidationCancelled  = domain.IntegrationCandidateValidationCancelled
	CandidateValidationSuperseded = domain.IntegrationCandidateValidationSuperseded
)

type candidateValidationObserverKey struct{}
type candidateValidationCommandKey struct{}
type candidateValidationAdmissionKey struct{}
type candidateValidationTicketKey struct{}
type candidateValidationReviewAuthorityKey struct{}

type CandidateValidationReviewAuthority struct {
	ReviewerID, ReviewerKind, PublicationOperationID, AcceptedPublicationOperationID string
	ReviewEpochEventID, AcceptedReviewEventID                                        int64
}

type CandidateValidationObserver func(CandidateValidationAttempt)

// CandidateValidationAdmission lets the daemon bind exact synthetic-candidate
// execution to its durable validation authority. A reused admission skips the
// command; otherwise finish must be called with the terminal attempt.
type CandidateValidationAdmission func(context.Context, string) (reused bool, finish func(CandidateValidationAttempt) error, err error)

func WithCandidateValidationObserver(ctx context.Context, observer CandidateValidationObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, candidateValidationObserverKey{}, observer)
}

// WithCandidateValidationCommand binds the project-owned configured command
// used to validate an exact synthetic merge candidate. The command executes in
// the detached candidate worktree with AZEDARACH_CANDIDATE_HEAD set.
func WithCandidateValidationCommand(ctx context.Context, command string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, candidateValidationCommandKey{}, strings.TrimSpace(command))
}

// WithCandidateValidationTicket binds the durable ticket attribution for a
// ticket-owned synthetic candidate. Repository-owned candidates omit this
// binding and execute without ticket identity, regardless of caller shell
// state.
func WithCandidateValidationTicket(ctx context.Context, ticketID naming.TicketID) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, candidateValidationTicketKey{}, ticketID)
}

func WithCandidateValidationReviewAuthority(ctx context.Context, authority CandidateValidationReviewAuthority) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, candidateValidationReviewAuthorityKey{}, authority)
}

func WithCandidateValidationAdmission(ctx context.Context, admission CandidateValidationAdmission) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if admission == nil {
		return ctx
	}
	return context.WithValue(ctx, candidateValidationAdmissionKey{}, admission)
}

func notifyCandidateValidation(ctx context.Context, attempt CandidateValidationAttempt) {
	if ctx == nil {
		return
	}
	observer, _ := ctx.Value(candidateValidationObserverKey{}).(CandidateValidationObserver)
	if observer != nil {
		observer(attempt)
	}
}

// ReportCandidateValidation delivers candidate disposition changes to an
// observer attached by the daemon integration boundary.
func ReportCandidateValidation(ctx context.Context, attempt CandidateValidationAttempt) {
	notifyCandidateValidation(ctx, attempt)
}

// GitHookDiagnostic describes synchronous git hook time observed while a git
// command was blocking its caller.
type GitHookDiagnostic struct {
	Hook       string `json:"hook,omitempty"`
	Command    string `json:"command,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms"`
	ExitStatus int    `json:"exit_status"`
	Blocking   bool   `json:"blocking"`
	TimedOut   bool   `json:"timed_out,omitempty"`
}

// IsTransactionalMergeStaleTarget reports whether a transactional scratch
// merge was validated against a target HEAD that changed before final apply.
func IsTransactionalMergeStaleTarget(result *MergeResult) bool {
	if result == nil || result.Success || result.HasConflicts {
		return false
	}
	message := strings.TrimSpace(result.Message)
	return strings.Contains(message, "target HEAD moved from ") &&
		strings.Contains(message, "after scratch validation; retry integration with a fresh scratch merge")
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
		runner:         runner,
		logger:         logger,
		syncJournalDir: syncDirectory,
	}
}

// HeadRevision returns the exact commit currently checked out in worktree.
func (c *Client) HeadRevision(ctx context.Context, worktree string) (string, error) {
	output, err := c.runInWorktree(ctx, worktree, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD revision: %w", err)
	}
	if revision := strings.TrimSpace(output); revision != "" {
		return revision, nil
	}
	return "", errors.New("resolve HEAD revision: empty output")
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
	return c.RuntimeStatusWithBasePreference(ctx, worktree, baseBranch, false)
}

// RuntimeStatusWithBasePreference returns runtime status, optionally preferring
// remote base refs before local base refs for origin-oriented workflows.
func (c *Client) RuntimeStatusWithBasePreference(ctx context.Context, worktree, baseBranch string, preferRemote bool) (*GitStatus, error) {
	status, err := c.Status(ctx, worktree)
	if err != nil {
		return nil, err
	}

	if additions, deletions, err := c.DiffStatTotalsWithBasePreference(ctx, worktree, baseBranch, preferRemote); err == nil {
		status.GitAdditions = additions
		status.GitDeletions = deletions
	}

	if ahead, behind, err := c.BranchAheadBehindWithBasePreference(ctx, worktree, baseBranch, preferRemote); err == nil {
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
	return c.mergeWithEnv(ctx, worktree, branch, nil, false)
}

func (c *Client) mergeWithEnv(ctx context.Context, worktree, branch string, extraEnv []string, forceNoFF bool) (*MergeResult, error) {
	c.logger.Info("merging branch", "worktree", worktree, "branch", branch)

	runCtx, cancel := mergeCommandContext(ctx)
	defer cancel()

	output, err, hookDiagnostics := c.runMergeWithHookDiagnostics(runCtx, worktree, branch, extraEnv, forceNoFF)

	result := &MergeResult{
		Success:      err == nil,
		HasConflicts: false,
		Message:      mergeResultMessage(output, err),
	}
	result.HookDiagnostics = hookDiagnostics

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

// Merge hooks run inside git's synchronous merge process. Azedarach bounds that
// process with the caller's deadline or a conservative default timeout, then
// uses Git Trace2 to observe per-hook timing without logging hook paths, argv,
// branch names, commit messages, or prompt/body text.
func mergeCommandContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, domain.IntegrationMergeTimeout)
}

func (c *Client) runMergeWithHookDiagnostics(ctx context.Context, worktree, branch string, extraEnv []string, forceNoFF bool) (string, error, []GitHookDiagnostic) {
	args := []string{"merge", "--no-edit"}
	if forceNoFF {
		args = append(args, "--no-ff")
	}
	args = append(args, branch)
	traceFile, err := os.CreateTemp("", "azedarach-git-trace2-*.json")
	if err != nil {
		output, runErr := c.runInWorktreeWithEnv(ctx, worktree, extraEnv, args...)
		return output, runErr, nil
	}
	tracePath := traceFile.Name()
	_ = traceFile.Close()
	defer os.Remove(tracePath)

	startedAt := time.Now()
	mergeEnv := append([]string(nil), extraEnv...)
	mergeEnv = append(mergeEnv, "GIT_TRACE2_EVENT="+tracePath)
	output, runErr := c.runInWorktreeWithEnv(ctx, worktree, mergeEnv, args...)
	elapsed := time.Since(startedAt)
	diagnostics := parseGitMergeHookDiagnostics(tracePath, mergeCommandShape(forceNoFF), startedAt.Add(elapsed), errors.Is(ctx.Err(), context.DeadlineExceeded))
	return output, runErr, diagnostics
}

func mergeCommandShape(forceNoFF bool) string {
	args := []string{"merge", "--no-edit"}
	if forceNoFF {
		args = append(args, "--no-ff")
	}
	return "git " + latencytrace.CommandShape(append(args, "<branch>"))
}

// MergeCleanly merges a branch and verifies the target worktree is clean after
// a merge attempt. If hooks or an interrupted merge leave new dirty files after
// starting from a clean target, those side effects are discarded and the merge
// result is reported as unsuccessful so higher-level integration can halt
// without leaving the target branch dirty.
func (c *Client) MergeCleanly(ctx context.Context, worktree, branch string) (*MergeResult, error) {
	return c.mergeCleanlyWithEnv(ctx, worktree, branch, nil, false)
}

func (c *Client) mergeCleanlyWithEnv(ctx context.Context, worktree, branch string, extraEnv []string, forceNoFF bool) (*MergeResult, error) {
	preStatus, preStatusErr := c.Status(ctx, worktree)
	targetWasClean := preStatusErr == nil && !gitStatusDirty(preStatus)
	if preStatusErr != nil {
		c.logger.Debug("pre-merge target status check failed; post-merge cleanup disabled",
			"worktree", worktree,
			"branch", branch,
			"error", preStatusErr,
		)
	}

	result, err := c.mergeWithEnv(ctx, worktree, branch, extraEnv, forceNoFF)
	if err != nil {
		preservedArtifacts := c.preserveIntegrationFailureArtifacts(ctx, worktree, nil, err)
		cleanedResult, cleanErr := c.cleanFailedMergeSideEffects(ctx, worktree, branch, targetWasClean, preStatusErr, err)
		if preservedArtifacts != "" {
			if cleanedResult != nil {
				cleanedResult.Message = appendMergeResultDetail(cleanedResult.Message, "preserved integration failure artifacts at "+preservedArtifacts)
			}
			if cleanErr != nil {
				cleanErr = fmt.Errorf("%w; preserved integration failure artifacts at %s", cleanErr, preservedArtifacts)
			}
		}
		return cleanedResult, cleanErr
	}
	if result == nil {
		return result, err
	}
	if !result.Success {
		preservedArtifacts := c.preserveIntegrationFailureArtifacts(ctx, worktree, result, nil)
		cleanedResult, cleanErr := c.cleanUnsuccessfulMergeSideEffects(ctx, worktree, branch, targetWasClean, preStatusErr, result)
		if preservedArtifacts != "" && cleanedResult != nil {
			cleanedResult.Message = appendMergeResultDetail(cleanedResult.Message, "preserved integration failure artifacts at "+preservedArtifacts)
		}
		return cleanedResult, cleanErr
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

type gitTrace2HookStart struct {
	hookName string
	started  time.Time
}

type gitTrace2Event struct {
	Event      string   `json:"event"`
	Time       string   `json:"time"`
	ChildID    int      `json:"child_id"`
	ChildClass string   `json:"child_class"`
	HookName   string   `json:"hook_name"`
	Argv       []string `json:"argv"`
	Code       *int     `json:"code"`
	TRel       float64  `json:"t_rel"`
}

func parseGitMergeHookDiagnostics(tracePath, commandShape string, observedEnd time.Time, timedOut bool) []GitHookDiagnostic {
	file, err := os.Open(tracePath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var out []GitHookDiagnostic
	starts := map[int]gitTrace2HookStart{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		var evt gitTrace2Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			continue
		}
		switch evt.Event {
		case "child_start":
			hookName := normalizedMergeHookName(evt.HookName, evt.Argv)
			if evt.ChildClass != "hook" || hookName == "" {
				continue
			}
			starts[evt.ChildID] = gitTrace2HookStart{
				hookName: hookName,
				started:  parseGitTrace2Time(evt.Time),
			}
		case "child_exit":
			start, ok := starts[evt.ChildID]
			if !ok {
				continue
			}
			delete(starts, evt.ChildID)
			exitStatus := -1
			if evt.Code != nil {
				exitStatus = *evt.Code
			}
			out = append(out, GitHookDiagnostic{
				Hook:       start.hookName,
				Command:    commandShape,
				ElapsedMS:  gitTrace2ElapsedMS(start.started, evt.Time, evt.TRel, observedEnd),
				ExitStatus: exitStatus,
				Blocking:   true,
			})
		}
	}

	for _, start := range starts {
		elapsed := observedEnd.Sub(start.started).Milliseconds()
		if start.started.IsZero() || elapsed < 0 {
			elapsed = 0
		}
		out = append(out, GitHookDiagnostic{
			Hook:       start.hookName,
			Command:    commandShape,
			ElapsedMS:  elapsed,
			ExitStatus: -1,
			Blocking:   true,
			TimedOut:   timedOut,
		})
	}
	return out
}

func normalizedMergeHookName(name string, argv []string) string {
	name = strings.TrimSpace(name)
	if name == "" && len(argv) > 0 {
		name = filepath.Base(strings.TrimSpace(argv[0]))
	}
	if _, ok := mergeHookNames[name]; !ok {
		return ""
	}
	return name
}

func parseGitTrace2Time(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func gitTrace2ElapsedMS(started time.Time, ended string, tRel float64, observedEnd time.Time) int64 {
	if started.IsZero() {
		if tRel > 0 {
			return int64(tRel * 1000)
		}
		return 0
	}
	endTime := parseGitTrace2Time(ended)
	if endTime.IsZero() {
		endTime = observedEnd
	}
	elapsed := endTime.Sub(started).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
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
	return c.DiffStatWithBasePreference(ctx, worktree, baseBranch, false)
}

// DiffStatWithBasePreference returns diff stat output using a configurable
// base-ref preference for local or origin-oriented workflows.
func (c *Client) DiffStatWithBasePreference(ctx context.Context, worktree, baseBranch string, preferRemote bool) (string, error) {
	c.logger.Debug("getting diff stat", "worktree", worktree)
	baseBranch = strings.TrimSpace(baseBranch)

	if baseBranch != "" {
		mergeBase, err := c.mergeBaseForDiffStat(ctx, worktree, baseBranch, preferRemote)
		if err == nil {
			output, diffErr := c.baseDiffStat(ctx, worktree, baseBranch, mergeBase)
			if diffErr == nil {
				return strings.TrimSpace(output), nil
			}
			if _, ok := diffStatBackoffErr(diffErr); ok {
				c.logBaseDiffStatBackoff(baseBranch, diffErr)
				return "", diffErr
			}
			c.logger.Warn("base diff stat failed; falling back to local staged/unstaged aggregation",
				"baseBranch", baseBranch,
				"error", diffErr,
			)
		} else {
			c.logBaseDiffStatResolutionFailure(baseBranch, err)
			if _, ok := diffStatBackoffErr(err); ok {
				return "", err
			}
		}
	}

	return c.localDiffStat(ctx, worktree, baseBranch, baseBranch != "")
}

func (c *Client) mergeBaseForDiffStat(ctx context.Context, worktree, baseBranch string, preferRemote bool) (string, error) {
	key := c.diffStatBackoffKey("merge-base", worktree, baseBranch, boolKey(preferRemote))
	if state, ok := c.diffStatBackoffActive(key); ok {
		return "", diffStatBackoffError{
			Operation:  "merge-base resolution",
			BaseBranch: baseBranch,
			Until:      state.Until,
			Reason:     state.Reason,
		}
	}
	mergeBase, err := c.mergeBase(ctx, worktree, baseBranch, preferRemote)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr == nil {
			c.recordDiffStatBackoff(key, err)
		}
		return "", err
	}
	c.clearDiffStatBackoff(key)
	return mergeBase, nil
}

func (c *Client) baseDiffStat(ctx context.Context, worktree, baseBranch, mergeBase string) (string, error) {
	key := c.diffStatBackoffKey("base-shortstat", worktree, baseBranch, mergeBase)
	if state, ok := c.diffStatBackoffActive(key); ok {
		return "", diffStatBackoffError{
			Operation:  "base diff shortstat",
			BaseBranch: baseBranch,
			Until:      state.Until,
			Reason:     state.Reason,
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, diffStatFallbackTimeout)
	defer cancel()
	output, err := c.runInWorktree(runCtx, worktree, "diff", "--shortstat", mergeBase, "HEAD", "--", ":^.azedarach")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr == nil {
			c.recordDiffStatBackoff(key, err)
		}
		return "", err
	}
	c.clearDiffStatBackoff(key)
	return output, nil
}

func (c *Client) localDiffStat(ctx context.Context, worktree, baseBranch string, budgeted bool) (string, error) {
	key := c.diffStatBackoffKey("local-shortstat", worktree, baseBranch)
	if budgeted {
		if state, ok := c.diffStatBackoffActive(key); ok {
			return "", diffStatBackoffError{
				Operation:  "fallback diff shortstat",
				BaseBranch: baseBranch,
				Until:      state.Until,
				Reason:     state.Reason,
			}
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, diffStatFallbackTimeout)
		defer cancel()
	}

	unstagedOutput, err := c.runInWorktree(ctx, worktree, "diff", "--shortstat")
	if err != nil {
		wrapped := fmt.Errorf("fallback diff stat state=failed operation=unstaged: %w", err)
		if budgeted {
			c.recordDiffStatBackoff(key, wrapped)
		}
		return "", wrapped
	}

	stagedOutput, err := c.runInWorktree(ctx, worktree, "diff", "--cached", "--shortstat")
	if err != nil {
		wrapped := fmt.Errorf("fallback diff stat state=failed operation=staged: %w", err)
		if budgeted {
			c.recordDiffStatBackoff(key, wrapped)
		}
		return "", wrapped
	}
	if budgeted {
		c.clearDiffStatBackoff(key)
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

func (c *Client) logBaseDiffStatResolutionFailure(baseBranch string, err error) {
	if backoffErr, ok := diffStatBackoffErr(err); ok {
		c.logger.Debug("base diff stat skipped; unresolved base branch backoff active",
			"baseBranch", baseBranch,
			"state", "backoff_active",
			"until", backoffErr.Until,
			"reason", backoffErr.Reason,
		)
		return
	}
	c.logger.Warn("base diff stat failed; falling back to local staged/unstaged aggregation",
		"baseBranch", baseBranch,
		"error", err,
	)
}

func (c *Client) logBaseDiffStatBackoff(baseBranch string, err error) {
	backoffErr, ok := diffStatBackoffErr(err)
	if !ok {
		return
	}
	c.logger.Debug("base diff stat skipped; shortstat backoff active",
		"baseBranch", baseBranch,
		"state", "backoff_active",
		"until", backoffErr.Until,
		"reason", backoffErr.Reason,
	)
}

func diffStatBackoffErr(err error) (diffStatBackoffError, bool) {
	var backoffErr diffStatBackoffError
	if errors.As(err, &backoffErr) {
		return backoffErr, true
	}
	return diffStatBackoffError{}, false
}

// DiffStatTotals parses additions and deletions from DiffStat output.
func (c *Client) DiffStatTotals(ctx context.Context, worktree, baseBranch string) (int, int, error) {
	return c.DiffStatTotalsWithBasePreference(ctx, worktree, baseBranch, false)
}

// DiffStatTotalsWithBasePreference parses additions and deletions from
// DiffStatWithBasePreference output.
func (c *Client) DiffStatTotalsWithBasePreference(ctx context.Context, worktree, baseBranch string, preferRemote bool) (int, int, error) {
	diffStat, err := c.DiffStatWithBasePreference(ctx, worktree, baseBranch, preferRemote)
	if err != nil {
		return 0, 0, err
	}
	additions, deletions := parseDiffStatTotals(diffStat)
	return additions, deletions, nil
}

// MergeBase resolves the merge base between base branch and HEAD.
func (c *Client) MergeBase(ctx context.Context, worktree, baseBranch string) (string, error) {
	return c.mergeBaseForRevision(ctx, worktree, baseBranch, "HEAD", false)
}

func (c *Client) mergeBase(ctx context.Context, worktree, baseBranch string, preferRemote bool) (string, error) {
	return c.mergeBaseForRevision(ctx, worktree, baseBranch, "HEAD", preferRemote)
}

// MergeBaseForRevision resolves the merge base against one immutable candidate
// revision rather than symbolic HEAD. Callers that publish an exact review
// range should resolve HEAD once, then pass that revision here.
func (c *Client) MergeBaseForRevision(ctx context.Context, worktree, baseBranch, headRevision string) (string, error) {
	return c.mergeBaseForRevision(ctx, worktree, baseBranch, headRevision, false)
}

func (c *Client) mergeBaseForRevision(ctx context.Context, worktree, baseBranch, headRevision string, preferRemote bool) (string, error) {
	baseBranch = strings.TrimSpace(baseBranch)
	headRevision = strings.TrimSpace(headRevision)
	if headRevision == "" {
		return "", fmt.Errorf("head revision is empty")
	}
	candidates := c.baseRefCandidates(ctx, worktree, baseBranch, preferRemote)
	if len(candidates) == 0 {
		return "", fmt.Errorf("base branch is empty")
	}

	var lastErr error
	attempted := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		attempted = append(attempted, candidate)
		mergeBaseOutput, err := c.runInWorktree(ctx, worktree, "merge-base", candidate, headRevision)
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

// MergeBaseBetween resolves the common ancestor of two explicit immutable
// revisions without consulting HEAD or remote-tracking policy.
func (c *Client) MergeBaseBetween(ctx context.Context, worktree, leftRevision, rightRevision string) (string, error) {
	leftRevision = strings.TrimSpace(leftRevision)
	rightRevision = strings.TrimSpace(rightRevision)
	if leftRevision == "" || rightRevision == "" {
		return "", fmt.Errorf("merge-base requires two explicit revisions")
	}
	output, err := c.runInWorktree(ctx, worktree, "merge-base", leftRevision, rightRevision)
	if err != nil {
		return "", fmt.Errorf("resolve merge-base between %s and %s: %w", leftRevision, rightRevision, err)
	}
	resolved := strings.TrimSpace(output)
	if resolved == "" {
		return "", fmt.Errorf("merge-base between %s and %s was empty", leftRevision, rightRevision)
	}
	return resolved, nil
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

// ResolveCommit resolves ref to an exact commit object ID.
func (c *Client) ResolveCommit(ctx context.Context, worktree, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	oid, err := c.revParseVerify(ctx, worktree, ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve commit %s: %w", ref, err)
	}
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return "", fmt.Errorf("resolve commit %s returned an empty object ID", ref)
	}
	return oid, nil
}

// ResolveTree resolves a commit-ish to its exact tree object ID.
func (c *Client) ResolveTree(ctx context.Context, worktree, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	oid, err := c.revParseVerify(ctx, worktree, ref+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("resolve tree %s: %w", ref, err)
	}
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return "", fmt.Errorf("resolve tree %s returned an empty object ID", ref)
	}
	return oid, nil
}

// IssueEvidenceCommits returns commits on ref whose subject starts with
// "<issueID>:". Az close/integration commits use that convention as durable
// code evidence for closed child issues.
func (c *Client) IssueEvidenceCommits(ctx context.Context, worktree, ref, issueID string) ([]EvidenceCommit, error) {
	ref = strings.TrimSpace(ref)
	issueID = strings.TrimSpace(issueID)
	if ref == "" {
		return nil, fmt.Errorf("ref is required")
	}
	if issueID == "" {
		return nil, fmt.Errorf("issue id is required")
	}
	pattern := "^" + regexp.QuoteMeta(issueID) + ":"
	output, err := c.runInWorktree(ctx, worktree, "log", "--format=%H%x00%s", "--regexp-ignore-case", "--grep="+pattern, ref, "--")
	if err != nil {
		return nil, fmt.Errorf("list issue evidence commits for %s on %s: %w", issueID, ref, err)
	}
	lines := splitNonEmptyLines(output)
	out := make([]EvidenceCommit, 0, len(lines))
	for _, line := range lines {
		hash, subject, ok := strings.Cut(line, "\x00")
		hash = strings.TrimSpace(hash)
		subject = strings.TrimSpace(subject)
		if !ok || hash == "" {
			continue
		}
		out = append(out, EvidenceCommit{Hash: hash, Subject: subject})
	}
	return out, nil
}

// SnapshotRefGraph captures commit ancestry, subjects, and changed files for
// all requested refs with one git subprocess. Requested refs must name local
// or remote refs so --decorate=full can identify their tips.
func (c *Client) SnapshotRefGraph(ctx context.Context, worktree string, refs []string) (RefGraphSnapshot, error) {
	refs = uniqueTrimmedStrings(refs)
	if len(refs) == 0 {
		return RefGraphSnapshot{Tips: map[string]string{}, Commits: map[string]RefGraphCommit{}}, nil
	}
	args := []string{"log", "--ignore-missing", fmt.Sprintf("--max-count=%d", maxRefGraphSnapshotCommits), "--decorate=full", "--format=%x1e%H%x00%P%x00%D%x00%s", "--name-only"}
	args = append(args, refs...)
	args = append(args, "--")
	output, err := c.runInWorktree(ctx, worktree, args...)
	if err != nil {
		return RefGraphSnapshot{}, fmt.Errorf("capture ref graph: %w", err)
	}
	snapshot := RefGraphSnapshot{Tips: make(map[string]string, len(refs)), Commits: make(map[string]RefGraphCommit)}
	for _, record := range strings.Split(output, "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		lines := strings.Split(record, "\n")
		fields := strings.SplitN(lines[0], "\x00", 4)
		if len(fields) != 4 {
			continue
		}
		hash := strings.TrimSpace(fields[0])
		if hash == "" {
			continue
		}
		commit := RefGraphCommit{Hash: hash, Parents: strings.Fields(fields[1]), Subject: strings.TrimSpace(fields[3])}
		commit.ChangedFiles = uniqueTrimmedStrings(lines[1:])
		snapshot.Commits[hash] = commit
		snapshot.Order = append(snapshot.Order, hash)
		for _, decoration := range strings.Split(fields[2], ",") {
			decoration = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(decoration), "tag: "))
			if _, target, ok := strings.Cut(decoration, " -> "); ok {
				decoration = strings.TrimSpace(target)
			}
			for _, ref := range refs {
				if refDecorationMatches(ref, decoration) {
					snapshot.Tips[ref] = hash
				}
			}
		}
	}
	snapshot.reachable = make(map[string]map[string]struct{}, len(refs))
	snapshot.complete = make(map[string]bool, len(refs))
	for _, ref := range refs {
		snapshot.reachable[ref] = snapshot.reachableFrom(snapshot.Tips[ref])
		snapshot.complete[ref] = snapshot.Tips[ref] != ""
		for hash := range snapshot.reachable[ref] {
			if _, captured := snapshot.Commits[hash]; !captured {
				snapshot.complete[ref] = false
				break
			}
		}
	}
	snapshot.Truncated = len(snapshot.Order) >= maxRefGraphSnapshotCommits
	return snapshot, nil
}

// RefComplete reports whether the captured graph contains the ref tip and all
// of its reachable parents. False means containment negatives are unknown.
func (s RefGraphSnapshot) RefComplete(ref string) bool {
	return s.complete[strings.TrimSpace(ref)]
}

func (s RefGraphSnapshot) Contains(ref, hash string) bool {
	ref = strings.TrimSpace(ref)
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return false
	}
	_, ok := s.reachable[ref][hash]
	return ok
}

func (s RefGraphSnapshot) reachableFrom(tip string) map[string]struct{} {
	seen := make(map[string]struct{})
	stack := []string{tip}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current == "" {
			continue
		}
		if _, exists := seen[current]; exists {
			continue
		}
		seen[current] = struct{}{}
		stack = append(stack, s.Commits[current].Parents...)
	}
	return seen
}

func (s RefGraphSnapshot) IssueEvidence(ref, issueID string) []EvidenceCommit {
	return s.IssueEvidenceByIssue(ref, []string{issueID})[strings.TrimSpace(issueID)]
}

func (s RefGraphSnapshot) IssueEvidenceByIssue(ref string, issueIDs []string) map[string][]EvidenceCommit {
	requested := make(map[string]string, len(issueIDs))
	out := make(map[string][]EvidenceCommit, len(issueIDs))
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID != "" {
			requested[strings.ToLower(issueID)] = issueID
		}
	}
	for _, hash := range s.Order {
		commit := s.Commits[hash]
		if !s.Contains(ref, hash) {
			continue
		}
		prefix, _, ok := strings.Cut(strings.TrimSpace(commit.Subject), ":")
		issueID, requestedIssue := requested[strings.ToLower(strings.TrimSpace(prefix))]
		if ok && requestedIssue {
			out[issueID] = append(out[issueID], EvidenceCommit{Hash: hash, Subject: commit.Subject})
		}
	}
	return out
}

// ChangedFilesExclusive returns a conservative touched-file union for commits
// reachable only from headRef. Files changed and later reverted remain present
// so overlap diagnostics prefer false positives over hidden collision risk.
func (s RefGraphSnapshot) ChangedFilesExclusive(baseRef, headRef string) []string {
	out := make([]string, 0)
	for _, hash := range s.Order {
		if s.Contains(headRef, hash) && !s.Contains(baseRef, hash) {
			out = append(out, s.Commits[hash].ChangedFiles...)
		}
	}
	return uniqueTrimmedStrings(out)
}

func refDecorationMatches(ref, decoration string) bool {
	ref, decoration = strings.TrimSpace(ref), strings.TrimSpace(decoration)
	if ref == "" || decoration == "" {
		return false
	}
	if ref == decoration || "refs/heads/"+ref == decoration || "refs/tags/"+ref == decoration {
		return true
	}
	return strings.HasPrefix(ref, "origin/") && "refs/remotes/"+ref == decoration
}

func uniqueTrimmedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// CommitContainedInRef reports whether commit is reachable from ref.
func (c *Client) CommitContainedInRef(ctx context.Context, worktree, commit, ref string) (bool, error) {
	commit = strings.TrimSpace(commit)
	ref = strings.TrimSpace(ref)
	if commit == "" {
		return false, fmt.Errorf("commit is required")
	}
	if ref == "" {
		return false, fmt.Errorf("ref is required")
	}
	if _, err := c.runInWorktree(ctx, worktree, "merge-base", "--is-ancestor", commit, ref); err != nil {
		if strings.Contains(err.Error(), "exit status 1") {
			return false, nil
		}
		return false, fmt.Errorf("check whether %s contains %s: %w", ref, commit, err)
	}
	return true, nil
}

// CommitChangedFiles returns files touched by commit.
func (c *Client) CommitChangedFiles(ctx context.Context, worktree, commit string) ([]string, error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return nil, fmt.Errorf("commit is required")
	}
	output, err := c.runInWorktreeRaw(ctx, worktree, "diff-tree", "--no-commit-id", "--name-only", "-z", "-r", commit)
	if err != nil {
		return nil, fmt.Errorf("list files changed by commit %s: %w", commit, err)
	}
	return parseNULTerminatedGitPaths(output)
}

// ChangedFilesBetweenRefs returns files changed between baseRef and headRef.
func (c *Client) ChangedFilesBetweenRefs(ctx context.Context, worktree, baseRef, headRef string) ([]string, error) {
	baseRef = strings.TrimSpace(baseRef)
	headRef = strings.TrimSpace(headRef)
	if baseRef == "" {
		return nil, fmt.Errorf("base ref is required")
	}
	if headRef == "" {
		return nil, fmt.Errorf("head ref is required")
	}
	output, err := c.runInWorktreeRaw(ctx, worktree, "diff", "--name-only", "-z", baseRef+"..."+headRef, "--")
	if err != nil {
		return nil, fmt.Errorf("list files changed between %s and %s: %w", baseRef, headRef, err)
	}
	return parseNULTerminatedGitPaths(output)
}

// ChangedFilesBetweenRefTrees returns files whose final tree differs between
// baseRef and headRef without using merge-base ancestry.
func (c *Client) ChangedFilesBetweenRefTrees(ctx context.Context, worktree, baseRef, headRef string) ([]string, error) {
	baseRef = strings.TrimSpace(baseRef)
	headRef = strings.TrimSpace(headRef)
	if baseRef == "" {
		return nil, fmt.Errorf("base ref is required")
	}
	if headRef == "" {
		return nil, fmt.Errorf("head ref is required")
	}
	output, err := c.runInWorktreeRaw(ctx, worktree, "diff", "--name-only", "-z", baseRef, headRef, "--")
	if err != nil {
		return nil, fmt.Errorf("list files with different trees between %s and %s: %w", baseRef, headRef, err)
	}
	return parseNULTerminatedGitPaths(output)
}

func parseNULTerminatedGitPaths(output string) ([]string, error) {
	if output == "" {
		return nil, nil
	}
	if !strings.HasSuffix(output, "\x00") {
		return nil, fmt.Errorf("Git path output is not NUL terminated")
	}
	values := strings.Split(output[:len(output)-1], "\x00")
	for _, value := range values {
		if value == "" {
			return nil, fmt.Errorf("Git path output contains an empty path")
		}
	}
	return values, nil
}

// PatchDigest returns a stable SHA-256 identity for the exact tree delta
// between two verified revisions. The digest is independent of later movement
// on the configured integration branch because callers retain the original
// base revision as part of evidence provenance.
func (c *Client) PatchDigest(ctx context.Context, worktree, baseRef, headRef string) (string, error) {
	baseRef = strings.TrimSpace(baseRef)
	headRef = strings.TrimSpace(headRef)
	if baseRef == "" || headRef == "" {
		return "", fmt.Errorf("patch digest requires base and head revisions")
	}
	output, err := c.runInWorktreeRaw(ctx, worktree, "diff", "--binary", "--no-ext-diff", baseRef, headRef, "--")
	if err != nil {
		return "", fmt.Errorf("derive patch digest for %s..%s: %w", baseRef, headRef, err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(output))), nil
}

// BranchAheadBehind reports commit deltas for HEAD relative to the base branch.
// It tries the local base branch first, then falls back to origin/<base>.
func (c *Client) BranchAheadBehind(ctx context.Context, worktree, baseBranch string) (int, int, error) {
	return c.BranchAheadBehindWithBasePreference(ctx, worktree, baseBranch, false)
}

// BranchAheadBehindWithBasePreference reports commit deltas for HEAD relative
// to a base ref chosen by the configured local/remote workflow preference.
func (c *Client) BranchAheadBehindWithBasePreference(ctx context.Context, worktree, baseBranch string, preferRemote bool) (int, int, error) {
	baseBranch = strings.TrimSpace(baseBranch)
	candidates := c.baseRefCandidates(ctx, worktree, baseBranch, preferRemote)
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

func (c *Client) baseRefCandidates(ctx context.Context, worktree, baseBranch string, preferRemote bool) []string {
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

	var originHeadRef string
	var originHeadLocal string
	if headRef, err := c.runInWorktree(ctx, worktree, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && strings.TrimSpace(headRef) != "" {
		originHeadRef = strings.TrimSpace(headRef) // e.g. origin/main or origin/trunk
		originHeadLocal = strings.TrimPrefix(originHeadRef, "origin/")
	}

	addOriginHeadFallbacks := func() {
		if !genericBase || originHeadRef == "" {
			return
		}
		if !strings.EqualFold(originHeadRef, "origin/"+normalizedBase) {
			add(originHeadRef)
		}
		if !strings.EqualFold(originHeadLocal, normalizedBase) {
			add(originHeadLocal)
		}
	}

	if preferRemote {
		addOriginHeadFallbacks()
		if baseBranch != "" && !strings.Contains(baseBranch, "/") {
			add("origin/" + baseBranch)
		}
		add(baseBranch)
		if genericBase {
			if originHeadRef != "" {
				add(originHeadRef)
			}
			if originHeadLocal != "" {
				add(originHeadLocal)
			}
		}
	} else {
		add(baseBranch)
		if baseBranch != "" && !strings.Contains(baseBranch, "/") {
			add("origin/" + baseBranch)
		}
		addOriginHeadFallbacks()
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

func (c *Client) runInWorktreeRaw(ctx context.Context, worktree string, args ...string) (string, error) {
	runner, ok := c.runner.(rawCommandRunner)
	if !ok {
		return c.runInWorktree(ctx, worktree, args...)
	}
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return runner.RunRaw(ctx, args...)
	}
	prefixed := make([]string, 0, len(args)+2)
	prefixed = append(prefixed, "-C", worktree)
	prefixed = append(prefixed, args...)
	return runner.RunRaw(ctx, prefixed...)
}

func (c *Client) runInWorktreeWithEnv(ctx context.Context, worktree string, extraEnv []string, args ...string) (string, error) {
	runner, ok := c.runner.(envCommandRunner)
	if !ok {
		return c.runInWorktree(ctx, worktree, args...)
	}
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return runner.RunWithEnv(ctx, extraEnv, args...)
	}
	prefixed := make([]string, 0, len(args)+2)
	prefixed = append(prefixed, "-C", worktree)
	prefixed = append(prefixed, args...)
	return runner.RunWithEnv(ctx, extraEnv, prefixed...)
}

func (c *Client) diffStatBackoffKey(parts ...string) string {
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			part = "-"
		}
		normalized = append(normalized, part)
	}
	return strings.Join(normalized, "|")
}

func (c *Client) diffStatBackoffActive(key string) (diffStatBackoffState, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return diffStatBackoffState{}, false
	}
	now := c.currentTime().UTC()
	c.diffStatMu.Lock()
	defer c.diffStatMu.Unlock()
	state, ok := c.diffStatBackoff[key]
	if !ok {
		return diffStatBackoffState{}, false
	}
	if state.Until.IsZero() || !now.Before(state.Until) {
		delete(c.diffStatBackoff, key)
		return diffStatBackoffState{}, false
	}
	return state, true
}

func (c *Client) recordDiffStatBackoff(key string, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	until := c.currentTime().UTC().Add(diffStatFailureBackoff)
	state := diffStatBackoffState{
		Until:  until,
		Reason: compactDiffStatBackoffReason(err),
	}
	c.diffStatMu.Lock()
	if c.diffStatBackoff == nil {
		c.diffStatBackoff = make(map[string]diffStatBackoffState)
	}
	c.diffStatBackoff[key] = state
	c.diffStatMu.Unlock()
}

func (c *Client) clearDiffStatBackoff(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	c.diffStatMu.Lock()
	delete(c.diffStatBackoff, key)
	c.diffStatMu.Unlock()
}

func (c *Client) currentTime() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

func boolKey(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func compactDiffStatBackoffReason(err error) string {
	if err == nil {
		return "previous failure"
	}
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		return "previous failure"
	}
	reason = strings.Join(strings.Fields(reason), " ")
	runes := []rune(reason)
	if len(runes) <= maxDiffStatBackoffReasonRunes {
		return reason
	}
	return string(runes[:maxDiffStatBackoffReasonRunes]) + "..."
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
		if statusCode != "??" && statusCode != "!!" {
			status.hasTrackedChanges = true
		}

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

	status.HasChanges = status.hasTrackedChanges ||
		len(status.Modified) > 0 ||
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

func gitStatusHasTrackedChanges(status *GitStatus) bool {
	if status == nil {
		return false
	}
	return status.hasTrackedChanges ||
		status.HasConflicts ||
		len(status.Modified) > 0 ||
		len(status.Added) > 0 ||
		len(status.Deleted) > 0 ||
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
