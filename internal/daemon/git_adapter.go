package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

var (
	daemonDiffStatInsertionsPattern = regexp.MustCompile(`(\d+)\s+insertion(?:s)?\(\+\)`)
	daemonDiffStatDeletionsPattern  = regexp.MustCompile(`(\d+)\s+deletion(?:s)?\(-\)`)
)

const runtimeSignalProjectionTTL = 15 * time.Second

type gitServiceAdapter struct {
	client                      *git.Client
	runtimeStateStore           *daemonstate.RuntimeStateStore
	runtimeStateStoreForProject func(string) *daemonstate.RuntimeStateStore
	runtimeProjectionWriter     runtimeProjectionWriter
	statusRefreshQueue          *reconcileQueue[*git.GitStatus]
	statusRefreshThrottle       *reconcileThrottle
	logger                      *slog.Logger
	pollInterval                time.Duration
	onStatusUpdate              func(ctx context.Context, projectID, issueID, worktree string, status *git.GitStatus)
	baseBranch                  string
	workflowMode                string
	baseBranchForProject        func(string) string
	workflowModeForProject      func(string) string
	baseBranchForWorktree       func(context.Context, string, string) string
	heavySessionStartActive     func(context.Context, string) bool

	refreshMu      sync.Mutex
	refreshRunning map[string]bool
	pollers        map[string]context.CancelFunc

	runtimeSignalsMu    sync.Mutex
	runtimeSignalsCache map[string]runtimeSignalProjection

	hookRefreshContinuationWG sync.WaitGroup
	hookRefreshRetryDelay     func(context.Context, int) error
	hookRefreshLifecycleMu    sync.Mutex
	hookRefreshContext        context.Context
	hookRefreshCancel         context.CancelFunc
}

func (a *gitServiceAdapter) runtimeStore(projectID string) *daemonstate.RuntimeStateStore {
	if a == nil {
		return nil
	}
	if a.runtimeStateStoreForProject != nil {
		if store := a.runtimeStateStoreForProject(projectID); store != nil {
			return store
		}
	}
	return a.runtimeStateStore
}

type runtimeSignalProjection struct {
	signal      daemonhandlers.GitRuntimeSignalsResult
	refreshedAt time.Time
}

var (
	_ daemonhandlers.GitService                  = (*gitServiceAdapter)(nil)
	_ daemonhandlers.GitMergePreflightService    = (*gitServiceAdapter)(nil)
	_ daemonhandlers.GitStatusHookRefreshService = (*gitServiceAdapter)(nil)
	_ daemonhandlers.GitDiscardChangesService    = (*gitServiceAdapter)(nil)
	_ daemonhandlers.GitCheckpointService        = (*gitServiceAdapter)(nil)
)

func (a *gitServiceAdapter) Fetch(ctx context.Context, projectID, worktree, remote string) error {
	if err := a.client.Fetch(ctx, worktree, remote); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return nil
}

func (a *gitServiceAdapter) PullBase(ctx context.Context, projectID, worktree, remote, baseBranch string) error {
	err := a.client.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		currentBranch, err := a.client.CurrentBranch(ctx, worktree)
		if err != nil {
			return err
		}
		if currentBranch == baseBranch {
			return a.client.Pull(ctx, worktree, remote, baseBranch)
		}
		return a.client.FetchRef(ctx, worktree, remote, baseBranch+":"+baseBranch)
	})
	if err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, true)
	return nil
}

func (a *gitServiceAdapter) Push(ctx context.Context, projectID, worktree, remote, branch string) error {
	if err := a.client.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		return a.client.Push(ctx, worktree, remote, branch)
	}); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, true)
	return nil
}

func (a *gitServiceAdapter) Merge(ctx context.Context, projectID, worktree, branch string) (*git.MergeResult, error) {
	ctx = a.withCandidateValidationProgress(ctx, projectID, worktree)
	result, err := a.client.MergeCleanlyTransactional(ctx, worktree, branch)
	if err != nil {
		return nil, err
	}
	// Merge completion should always trigger an update notification so clients
	// refresh runtime git signals even when porcelain status stays clean.
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, true)
	return result, nil
}

func (a *gitServiceAdapter) withCandidateValidationProgress(ctx context.Context, projectID, worktree string) context.Context {
	return git.WithCandidateValidationObserver(ctx, func(attempt git.CandidateValidationAttempt) {
		message := fmt.Sprintf("candidate_head=%s status=%s canonical=%t", attempt.CandidateHead, attempt.Status, attempt.Canonical)
		if detail := strings.TrimSpace(attempt.Message); detail != "" {
			message += " detail=" + detail
		}
		_ = daemonops.ReportProgress(ctx, daemonops.Progress{
			Phase:   "candidate_validation." + string(attempt.Status),
			Message: message,
			Current: 65,
			Total:   100,
			Unit:    "percent",
			Percent: 65,
		})
		if a.logger != nil {
			a.logger.InfoContext(ctx, "integration candidate validation disposition",
				"project_id", projectID,
				"worktree", worktree,
				"candidate_head", attempt.CandidateHead,
				"validation_status", attempt.Status,
				"canonical", attempt.Canonical,
			)
		}
	})
}

func (a *gitServiceAdapter) Checkout(ctx context.Context, projectID, worktree, branch string) error {
	if err := a.client.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		return a.client.Checkout(ctx, worktree, branch)
	}); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return nil
}

func (a *gitServiceAdapter) WorktreePathForBranch(ctx context.Context, _ string, branch string) (string, bool, error) {
	return a.client.WorktreePathForBranch(ctx, branch)
}

func (a *gitServiceAdapter) AbortMerge(ctx context.Context, projectID, worktree string) error {
	if err := a.client.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		return a.client.AbortMerge(ctx, worktree)
	}); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return nil
}

func (a *gitServiceAdapter) MergePreflight(ctx context.Context, _ string, req daemonhandlers.GitMergePreflightRequest) (*daemonhandlers.GitMergePreflightResult, error) {
	result, err := a.client.MergePreflight(ctx, req.SourceWorktree, req.TargetWorktree, req.TargetRef, req.SourceBranch)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("merge preflight returned no result")
	}

	resp := &daemonhandlers.GitMergePreflightResult{
		SourceID:       req.SourceID,
		SourceWorktree: req.SourceWorktree,
		TargetID:       req.TargetID,
		TargetWorktree: req.TargetWorktree,
		Clean:          true,
		ConflictFiles:  append([]string(nil), result.ConflictFiles...),
	}

	if !req.IgnoreSourceDirty && hasMergeBlockingGitStatusChanges(result.SourceStatus) {
		resp.Clean = false
		resp.SourceFiles = mergeBlockingGitStatusFiles(result.SourceStatus)
		resp.Reasons = append(resp.Reasons, "Source worktree has uncommitted changes")
	}
	if hasMergeBlockingGitStatusChanges(result.TargetStatus) {
		resp.Clean = false
		resp.TargetFiles = mergeBlockingGitStatusFiles(result.TargetStatus)
		resp.Reasons = append(resp.Reasons, "Target worktree has uncommitted changes")
	}
	if result.HasConflicts {
		resp.Clean = false
		if len(result.ConflictFiles) > 0 {
			resp.Reasons = append(resp.Reasons, fmt.Sprintf("Merge would conflict in %d files: %s", len(result.ConflictFiles), strings.Join(result.ConflictFiles, ", ")))
		} else {
			resp.Reasons = append(resp.Reasons, "Merge would conflict; merge and resolve base branch into the source branch first")
		}
	}

	return resp, nil
}

func (a *gitServiceAdapter) DiscardChanges(ctx context.Context, projectID, worktree string) (*daemonhandlers.GitDiscardChangesResult, error) {
	if err := a.client.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		return a.client.DiscardChanges(ctx, worktree)
	}); err != nil {
		return nil, err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return &daemonhandlers.GitDiscardChangesResult{Worktree: worktree}, nil
}

func (a *gitServiceAdapter) Checkpoint(ctx context.Context, projectID string, req daemonhandlers.GitCheckpointRequest) (*daemonhandlers.GitCheckpointResult, error) {
	if err := a.client.WithWorktreeLock(ctx, req.Worktree, func(ctx context.Context) error {
		return a.client.CreateCheckpoint(ctx, req.Worktree, req.Message)
	}); err != nil {
		return nil, err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, req.Worktree, true, false)
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = git.DefaultCheckpointMessage
	}
	return &daemonhandlers.GitCheckpointResult{Worktree: req.Worktree, Message: message}, nil
}

func (a *gitServiceAdapter) DiffStat(ctx context.Context, projectID string, worktree, baseBranch string) (string, error) {
	requestedBase := strings.TrimSpace(baseBranch)
	defaultBase := strings.TrimSpace(a.resolvedBaseBranch(projectID))
	preferRemote := a.preferRemoteRuntimeBase(projectID)
	if requestedBase == "" || requestedBase == defaultBase {
		if resolved := a.worktreeSpecificBaseBranch(ctx, projectID, worktree); resolved != "" {
			requestedBase = resolved
			preferRemote = false
		} else if requestedBase == "" {
			requestedBase = defaultBase
		}
	}
	return a.client.DiffStatWithBasePreference(ctx, worktree, requestedBase, preferRemote)
}

func (a *gitServiceAdapter) RuntimeSignals(ctx context.Context, projectID string, targets []daemonhandlers.GitRuntimeSignalsTarget, baseBranch string, compareRemote bool, remote string, refresh bool) ([]daemonhandlers.GitRuntimeSignalsResult, int, error) {
	projectID = normalizeProjectID(projectID)
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		if a.baseBranchForProject != nil {
			baseBranch = strings.TrimSpace(a.baseBranchForProject(projectID))
		}
	}
	if baseBranch == "" {
		baseBranch = strings.TrimSpace(a.baseBranch)
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = "origin"
	}

	results := make([]daemonhandlers.GitRuntimeSignalsResult, 0, len(targets))
	partialFailures := 0
	now := time.Now()
	for _, target := range targets {
		issueID := strings.TrimSpace(target.IssueID)
		worktree := strings.TrimSpace(target.Worktree)
		if issueID == "" || worktree == "" {
			continue
		}
		if refresh {
			if _, err := a.refreshGitStatusWriteThroughResult(ctx, projectID, worktree, true, false); err != nil {
				partialFailures++
			}
		}
		cacheKey := runtimeSignalCacheKey(projectID, issueID, worktree, baseBranch, compareRemote, remote)
		if !refresh {
			if cached, ok := a.cachedRuntimeSignal(cacheKey, now); ok {
				results = append(results, cached)
				continue
			}
		}

		signal, found, err := a.computeRuntimeSignalFromProjection(ctx, projectID, issueID, worktree)
		if err != nil {
			partialFailures++
			if cached, ok := a.cachedRuntimeSignalAnyBase(projectID, issueID, worktree, now); ok {
				results = append(results, cached)
				continue
			}
			results = append(results, daemonhandlers.GitRuntimeSignalsResult{
				IssueID:  issueID,
				Worktree: worktree,
			})
			continue
		}
		if !found {
			results = append(results, daemonhandlers.GitRuntimeSignalsResult{
				IssueID:  issueID,
				Worktree: worktree,
			})
			continue
		}
		a.storeRuntimeSignal(cacheKey, signal, now)
		results = append(results, signal)
	}

	return results, partialFailures, nil
}

func (a *gitServiceAdapter) BranchBehind(ctx context.Context, projectID, worktree, _baseBranch, _remote string) (int, int, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return 0, 0, nil
	}
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil {
		return 0, 0, nil
	}

	projection, found, err := runtimeStore.GetWorktreeStateByPath(ctx, projectID, worktree)
	if err != nil {
		return 0, 0, err
	}
	if !found || len(projection.GitStatusRaw) == 0 {
		return 0, 0, nil
	}

	var status git.GitStatus
	if err := json.Unmarshal(projection.GitStatusRaw, &status); err != nil {
		if a.logger != nil {
			a.logger.Debug("unmarshal cached git status projection failed for branch-behind",
				"project_id", projectID,
				"worktree", worktree,
				"error", err,
			)
		}
		return 0, 0, nil
	}
	return status.GitAheadCount, status.GitBehindCount, nil
}

func (a *gitServiceAdapter) Status(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return &git.GitStatus{}, nil
	}
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil {
		return &git.GitStatus{}, nil
	}

	projection, found, err := runtimeStore.GetWorktreeStateByPath(ctx, projectID, worktree)
	if err != nil {
		return nil, err
	}
	if !found || len(projection.GitStatusRaw) == 0 {
		a.refreshGitStatusVisible(projectID, worktree)
		a.ensureStatusPoller(projectID, worktree)
		return &git.GitStatus{}, nil
	}

	var cached git.GitStatus
	unmarshalErr := json.Unmarshal(projection.GitStatusRaw, &cached)
	if unmarshalErr == nil {
		a.refreshGitStatusVisible(projectID, worktree)
		a.ensureStatusPoller(projectID, worktree)
		return &cached, nil
	}
	if a.logger != nil {
		a.logger.Debug("unmarshal cached git status projection failed",
			"project_id", projectID,
			"worktree", worktree,
			"error", unmarshalErr,
		)
	}
	a.refreshGitStatusVisible(projectID, worktree)
	a.ensureStatusPoller(projectID, worktree)
	return &git.GitStatus{}, nil
}

func (a *gitServiceAdapter) RefreshStatus(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return &git.GitStatus{}, nil
	}
	status, err := a.refreshGitStatusManual(ctx, projectID, worktree)
	if err != nil {
		return nil, err
	}
	a.ensureStatusPoller(projectID, worktree)
	if status == nil {
		return &git.GitStatus{}, nil
	}
	return status, nil
}

func (a *gitServiceAdapter) RefreshStatusForHook(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return &git.GitStatus{}, nil
	}

	if _, err := a.queueDurableGitHookRefresh(ctx, projectID, worktree); err != nil {
		if a.logger != nil {
			a.logger.Warn("daemon hook git status refresh enqueue failed",
				"project_id", projectID,
				"worktree", worktree,
				"error", err,
			)
		}
		return nil, err
	}
	if a.logger != nil {
		a.logger.Info("daemon hook git status refresh enqueued",
			"project_id", projectID,
			"worktree", worktree,
		)
	}
	return a.cachedGitStatus(ctx, projectID, worktree)
}

func (a *gitServiceAdapter) cachedGitStatus(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil {
		return &git.GitStatus{}, nil
	}
	projection, found, err := runtimeStore.GetWorktreeStateByPath(ctx, projectID, worktree)
	if err != nil {
		return nil, err
	}
	if !found || len(projection.GitStatusRaw) == 0 {
		return &git.GitStatus{}, nil
	}
	var cached git.GitStatus
	if err := json.Unmarshal(projection.GitStatusRaw, &cached); err != nil {
		if a.logger != nil {
			a.logger.Debug("unmarshal cached git status projection failed for hook",
				"project_id", projectID,
				"worktree", worktree,
				"error", err,
			)
		}
		return &git.GitStatus{}, nil
	}
	return &cached, nil
}

func (a *gitServiceAdapter) ensureStatusRefreshQueue() *reconcileQueue[*git.GitStatus] {
	if a == nil {
		return newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
			Name:    "git_status_refresh",
			Workers: defaultGitStatusRefreshQueueWorkers,
		})
	}

	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	if a.statusRefreshQueue == nil {
		a.statusRefreshQueue = newReconcileQueue[*git.GitStatus](reconcileQueueConfig{
			Name:    "git_status_refresh",
			Workers: defaultGitStatusRefreshQueueWorkers,
			Logger:  a.logger,
		})
	}
	return a.statusRefreshQueue
}

func (a *gitServiceAdapter) queueGitStatusRefresh(projectID, worktree string, priority reconcileQueuePriority, reason string) (reconcileQueueSubmission[*git.GitStatus], error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return reconcileQueueSubmission[*git.GitStatus]{}, fmt.Errorf("missing worktree")
	}
	key := gitStatusRefreshQueueKey(projectID, worktree)
	if a != nil && priority <= reconcilePriorityBackground && a.heavySessionStartActive != nil && a.gitStatusHeavySessionStartActive(projectID) {
		until := time.Now().UTC().Add(defaultGitStatusRefreshCadence)
		if a.logger != nil {
			a.logger.Debug("git status refresh deferred during heavy session start",
				"project_id", projectID,
				"worktree", worktree,
				"reason", reason,
				"until", until,
			)
		}
		return immediateReconcileSubmission(reconcileQueueResult[*git.GitStatus]{
			Key:      key,
			Deferred: true,
			Reason:   heavySessionStartBackgroundDeferReason,
			Until:    until,
		}), nil
	}
	queue := a.ensureStatusRefreshQueue()
	force := priority >= reconcilePriorityManual
	throttle := a.ensureStatusRefreshThrottle()
	admission := reconcileThrottleDecision{Action: reconcileThrottleProcess}
	if !force && !queue.HasJob(key) {
		admission = throttle.Admit(key, false)
		if !admission.Allowed() {
			if a.logger != nil {
				counters := throttle.snapshotCounters()
				a.logger.Debug("git status refresh suppressed",
					"project_id", projectID,
					"worktree", worktree,
					"reason", reason,
					"action", string(admission.Action),
					"until", admission.Until,
					"throttle_processed", counters.Processed,
					"throttle_skipped", counters.Skipped,
					"throttle_deferred", counters.Deferred,
				)
			}
			return immediateReconcileSubmission(reconcileQueueResult[*git.GitStatus]{
				Key:      key,
				Skipped:  admission.Action == reconcileThrottleSkip,
				Deferred: admission.Action == reconcileThrottleDefer,
				Reason:   admission.Reason,
				Until:    admission.Until,
			}), nil
		}
	}

	submission, err := queue.Enqueue(reconcileQueueRequest[*git.GitStatus]{
		Key:      key,
		Priority: priority,
		Reason:   reason,
		Work: func(ctx context.Context) (*git.GitStatus, error) {
			refreshCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			hookRefresh := strings.EqualFold(strings.TrimSpace(reason), "hook")
			if hookRefresh {
				// A hook job drains durable generations rather than trusting queue
				// residency. Generations accepted while this keyed job is already
				// running therefore cannot be lost to queue deduplication.
				for {
					intent, pending, loadErr := a.pendingGitHookRefresh(refreshCtx, projectID, worktree)
					if loadErr != nil {
						return nil, loadErr
					}
					if !pending {
						break
					}
					if _, _, refreshErr := a.refreshGitHookStatusWriteThroughResult(refreshCtx, projectID, worktree, intent.RequestedGeneration); refreshErr != nil {
						return nil, refreshErr
					}
				}
			}
			status, refreshErr := a.refreshGitStatusWriteThroughResult(refreshCtx, projectID, worktree, true, false)
			outcome := throttle.Record(key, gitStatusSignature(status), refreshErr)
			if a.logger != nil {
				counters := throttle.snapshotCounters()
				if refreshErr != nil {
					if staleReason, ok := staleWorktreeGitRefreshErrorReason(refreshErr); ok {
						a.logger.Info("daemon git status refresh suppressed stale worktree",
							"project_id", projectID,
							"worktree", worktree,
							"reason", reason,
							"stale_reason", staleReason,
							"error", refreshErr,
						)
					} else {
						a.logger.Warn("daemon git status refresh failed",
							"project_id", projectID,
							"worktree", worktree,
							"reason", reason,
							"error", refreshErr,
						)
					}
				}
				a.logger.Debug("git status refresh processed",
					"project_id", projectID,
					"worktree", worktree,
					"reason", reason,
					"outcome", string(outcome),
					"throttle_processed", counters.Processed,
					"throttle_skipped", counters.Skipped,
					"throttle_deferred", counters.Deferred,
				)
			}
			return status, refreshErr
		},
	})
	if err != nil {
		return reconcileQueueSubmission[*git.GitStatus]{}, err
	}
	if submission.Deduped {
		throttle.Refund(admission)
	}
	return submission, nil
}

func (a *gitServiceAdapter) queueDurableGitHookRefresh(ctx context.Context, projectID, worktree string) (reconcileQueueSubmission[*git.GitStatus], error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	store := a.runtimeStore(projectID)
	if store == nil {
		return reconcileQueueSubmission[*git.GitStatus]{}, fmt.Errorf("runtime state store unavailable for durable git hook refresh")
	}
	if _, err := store.AcceptGitHookRefresh(ctx, projectID, worktree, time.Now().UTC()); err != nil {
		return reconcileQueueSubmission[*git.GitStatus]{}, err
	}
	submission, err := a.queuePersistedGitHookRefresh(projectID, worktree)
	if err != nil {
		return reconcileQueueSubmission[*git.GitStatus]{}, err
	}
	a.continuePendingGitHookRefreshAfter(submission, projectID, worktree)
	return submission, nil
}

func (a *gitServiceAdapter) queuePersistedGitHookRefresh(projectID, worktree string) (reconcileQueueSubmission[*git.GitStatus], error) {
	return a.queueGitStatusRefresh(projectID, worktree, reconcilePriorityManual, "hook")
}

func (a *gitServiceAdapter) continuePendingGitHookRefreshAfter(submission reconcileQueueSubmission[*git.GitStatus], projectID, worktree string) {
	reconcileCtx := a.gitHookRefreshReconcileContext()
	a.hookRefreshContinuationWG.Add(1)
	go func() {
		defer a.hookRefreshContinuationWG.Done()
		attempt := 0
		current := submission
		for {
			result, waitErr := current.Wait(reconcileCtx)
			if reconcileCtx.Err() != nil {
				return
			}
			checkCtx, cancel := context.WithTimeout(reconcileCtx, 2*time.Second)
			_, pending, loadErr := a.pendingGitHookRefresh(checkCtx, projectID, worktree)
			cancel()
			if loadErr == nil && !pending {
				return
			}
			if waitErr != nil || result.Err != nil || loadErr != nil {
				attempt++
				if err := a.waitGitHookRefreshRetry(reconcileCtx, attempt); err != nil {
					return
				}
			} else {
				attempt = 0
			}
			next, enqueueErr := a.queuePersistedGitHookRefresh(projectID, worktree)
			if enqueueErr != nil {
				attempt++
				if err := a.waitGitHookRefreshRetry(reconcileCtx, attempt); err != nil {
					return
				}
				continue
			}
			current = next
		}
	}()
}

func (a *gitServiceAdapter) gitHookRefreshReconcileContext() context.Context {
	a.hookRefreshLifecycleMu.Lock()
	defer a.hookRefreshLifecycleMu.Unlock()
	if a.hookRefreshContext == nil {
		a.hookRefreshContext, a.hookRefreshCancel = context.WithCancel(context.Background())
	}
	return a.hookRefreshContext
}

func (a *gitServiceAdapter) setGitHookRefreshReconcileContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.hookRefreshLifecycleMu.Lock()
	if a.hookRefreshCancel != nil {
		a.hookRefreshCancel()
	}
	a.hookRefreshContext = ctx
	a.hookRefreshCancel = nil
	a.hookRefreshLifecycleMu.Unlock()
}

func (a *gitServiceAdapter) stopGitHookRefreshReconciler() {
	if a == nil {
		return
	}
	a.hookRefreshLifecycleMu.Lock()
	if a.hookRefreshCancel != nil {
		a.hookRefreshCancel()
	}
	a.hookRefreshContext = nil
	a.hookRefreshCancel = nil
	a.hookRefreshLifecycleMu.Unlock()
}

func (a *gitServiceAdapter) waitGitHookRefreshRetry(ctx context.Context, attempt int) error {
	if a.hookRefreshRetryDelay != nil {
		return a.hookRefreshRetryDelay(ctx, attempt)
	}
	delay := 100 * time.Millisecond
	for i := 1; i < attempt && delay < 5*time.Second; i++ {
		delay *= 2
	}
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *gitServiceAdapter) waitForGitHookRefreshContinuations() {
	if a != nil {
		a.hookRefreshContinuationWG.Wait()
	}
}

func (a *gitServiceAdapter) pendingGitHookRefresh(ctx context.Context, projectID, worktree string) (daemonstate.GitHookRefreshIntent, bool, error) {
	store := a.runtimeStore(projectID)
	if store == nil {
		return daemonstate.GitHookRefreshIntent{}, false, fmt.Errorf("runtime state store unavailable for durable git hook refresh")
	}
	return store.GetPendingGitHookRefresh(ctx, projectID, worktree)
}

func (a *gitServiceAdapter) gitStatusHeavySessionStartActive(projectID string) bool {
	checkCtx, cancel := heavySessionStartSignalCheckContext(context.Background())
	defer cancel()
	return a.heavySessionStartActive(checkCtx, projectID)
}

func (a *gitServiceAdapter) refreshGitStatusVisible(projectID, worktree string) {
	if _, err := a.queueGitStatusRefresh(projectID, worktree, reconcilePriorityVisible, "visible"); err != nil && a.logger != nil {
		a.logger.Debug("git status visible refresh enqueue failed", "project_id", projectID, "worktree", worktree, "error", err)
	}
}

func (a *gitServiceAdapter) refreshGitStatusManual(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	submission, err := a.queueGitStatusRefresh(projectID, worktree, reconcilePriorityManual, "manual")
	if err != nil {
		return nil, err
	}
	result, err := submission.Wait(ctx)
	if err != nil {
		return nil, err
	}
	return result.Value, result.Err
}

func (a *gitServiceAdapter) refreshGitStatusWriteThrough(ctx context.Context, projectID, worktree string, publishOnChange, forcePublish bool) {
	_, _ = a.refreshGitStatusWriteThroughResult(ctx, projectID, worktree, publishOnChange, forcePublish)
}

func (a *gitServiceAdapter) refreshGitStatusPorcelainWriteThroughResult(ctx context.Context, projectID, worktree string, publishOnChange, forcePublish bool) (*git.GitStatus, uint64, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return &git.GitStatus{}, 0, nil
	}
	status, err := a.client.Status(ctx, worktree)
	if err != nil {
		persistCtx, persistCancel := gitStatusProjectionPersistContext(ctx)
		if a.suppressStaleWorktreeGitRefresh(persistCtx, projectID, worktree, err) {
			persistCancel()
			return nil, 0, newStaleWorktreeGitRefreshError(staleGitWorktreeRefreshReasonForError(worktree, err), worktree, err)
		}
		persistCancel()
		if a.logger != nil {
			a.logger.Warn("runtime signal porcelain git status refresh failed",
				"project_id", projectID,
				"worktree", worktree,
				"error", err,
			)
		}
		return nil, 0, err
	}
	statusPersistCtx, statusPersistCancel := gitStatusProjectionPersistContext(ctx)
	defer statusPersistCancel()
	issueID, branch := a.resolveWorktreeProjectionIdentity(statusPersistCtx, projectID, worktree)
	var rev uint64
	if a.runtimeProjectionWriter != nil {
		if issueID != "" {
			_ = a.runtimeProjectionWriter.PersistWorktreeProjection(statusPersistCtx, projectID, issueID, worktree, branch)
		}
		var publishErr error
		rev, publishErr = a.runtimeProjectionWriter.PersistGitStatusProjectionAndPublish(statusPersistCtx, projectID, issueID, worktree, status, publishOnChange, forcePublish)
		a.invalidateRuntimeSignalCache(projectID, worktree)
		if publishErr != nil {
			return status, 0, publishErr
		}
		return status, rev, nil
	}
	changed, issueID := a.persistStatusSnapshot(statusPersistCtx, projectID, worktree, status)
	a.invalidateRuntimeSignalCache(projectID, worktree)
	if forcePublish || (publishOnChange && changed) {
		if a.onStatusUpdate == nil || strings.TrimSpace(issueID) == "" {
			if forcePublish {
				return status, 0, fmt.Errorf("forced git status projection publication unavailable")
			}
		} else {
			a.onStatusUpdate(statusPersistCtx, projectID, issueID, worktree, status)
		}
	}
	return status, rev, nil
}

func (a *gitServiceAdapter) refreshGitHookStatusWriteThroughResult(ctx context.Context, projectID, worktree string, generation int64) (*git.GitStatus, uint64, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" || generation <= 0 {
		return nil, 0, fmt.Errorf("git hook refresh requires worktree and generation")
	}
	status, err := a.client.Status(ctx, worktree)
	if err != nil {
		return nil, 0, err
	}
	persistCtx, cancel := gitStatusProjectionPersistContext(ctx)
	defer cancel()
	issueID, branch := a.resolveWorktreeProjectionIdentity(persistCtx, projectID, worktree)
	if issueID == "" {
		return status, 0, fmt.Errorf("durable git hook projection target not found")
	}
	if a.runtimeProjectionWriter == nil {
		rawStatus, marshalErr := json.Marshal(status)
		if marshalErr != nil {
			return status, 0, marshalErr
		}
		store := a.runtimeStore(projectID)
		if store == nil {
			return status, 0, fmt.Errorf("durable git hook runtime state store unavailable")
		}
		published, persistErr := store.PersistGitHookRefreshPublication(persistCtx, projectID, issueID, worktree, generation, rawStatus, time.Now().UTC())
		if persistErr != nil {
			return status, 0, persistErr
		}
		if published && a.onStatusUpdate != nil {
			a.onStatusUpdate(persistCtx, projectID, issueID, worktree, status)
		}
		a.invalidateRuntimeSignalCache(projectID, worktree)
		return status, 0, nil
	}
	if err := a.runtimeProjectionWriter.PersistWorktreeProjection(persistCtx, projectID, issueID, worktree, branch); err != nil {
		return status, 0, err
	}
	rev, err := a.runtimeProjectionWriter.PersistGitHookStatusProjectionAndPublishResult(persistCtx, projectID, issueID, worktree, generation, status)
	a.invalidateRuntimeSignalCache(projectID, worktree)
	return status, rev, err
}

func (a *gitServiceAdapter) refreshGitStatusWriteThroughResult(ctx context.Context, projectID, worktree string, publishOnChange, forcePublish bool) (*git.GitStatus, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return &git.GitStatus{}, nil
	}
	baseBranch, worktreeSpecificBase := a.resolvedBaseBranchForWorktree(ctx, projectID, worktree)
	preferRemoteBase := a.preferRemoteRuntimeBase(projectID) && !worktreeSpecificBase
	if err := a.client.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		return a.client.RecoverIntegrationJournal(ctx, worktree)
	}); err != nil {
		persistCtx, persistCancel := gitStatusProjectionPersistContext(ctx)
		if staleReason, ok := staleGitWorktreeRefreshReason(worktree, err); ok && staleReason != "missing_worktree_path" && a.suppressStaleWorktreeGitRefresh(persistCtx, projectID, worktree, err) {
			persistCancel()
			return nil, newStaleWorktreeGitRefreshError(staleReason, worktree, err)
		}
		persistCancel()
		if a.logger != nil {
			a.logger.Warn("failed to recover interrupted transactional integration before status refresh",
				"project_id", projectID,
				"worktree", worktree,
				"error", err,
			)
		}
	}

	var (
		status *git.GitStatus
		err    error
	)
	if baseBranch != "" {
		status, err = a.client.RuntimeStatusWithBasePreference(ctx, worktree, baseBranch, preferRemoteBase)
		if err != nil && a.logger != nil {
			a.logger.Debug("runtime git status refresh failed; falling back to porcelain status",
				"project_id", projectID,
				"worktree", worktree,
				"base_branch", baseBranch,
				"error", err,
			)
		}
	}
	if status == nil {
		status, err = a.client.Status(ctx, worktree)
		if err != nil {
			persistCtx, persistCancel := gitStatusProjectionPersistContext(ctx)
			if a.suppressStaleWorktreeGitRefresh(persistCtx, projectID, worktree, err) {
				persistCancel()
				return nil, newStaleWorktreeGitRefreshError(staleGitWorktreeRefreshReasonForError(worktree, err), worktree, err)
			}
			persistCancel()
			if a.logger != nil {
				a.logger.Debug("git status refresh after mutation failed", "project_id", projectID, "worktree", worktree, "error", err)
			}
			return nil, err
		}
	}
	statusPersistCtx, statusPersistCancel := gitStatusProjectionPersistContext(ctx)
	defer statusPersistCancel()
	if a.runtimeProjectionWriter != nil {
		if _, persistErr := a.runtimeProjectionWriter.PersistGitStatusProjectionAndPublish(statusPersistCtx, projectID, "", worktree, status, publishOnChange, forcePublish); persistErr != nil {
			return status, persistErr
		}
		a.invalidateRuntimeSignalCache(projectID, worktree)
		return status, nil
	}
	changed, issueID := a.persistStatusSnapshot(statusPersistCtx, projectID, worktree, status)
	a.invalidateRuntimeSignalCache(projectID, worktree)
	if (forcePublish || (publishOnChange && changed)) && a.onStatusUpdate != nil && strings.TrimSpace(issueID) != "" {
		a.onStatusUpdate(statusPersistCtx, projectID, issueID, worktree, status)
	}
	return status, nil
}

func (a *gitServiceAdapter) resolveWorktreeProjectionIdentity(ctx context.Context, projectID, worktree string) (string, string) {
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore != nil {
		projection, found, err := runtimeStore.GetWorktreeStateByPath(ctx, projectID, worktree)
		if err == nil && found && strings.TrimSpace(projection.IssueID) != "" {
			return strings.TrimSpace(projection.IssueID), strings.TrimSpace(projection.Branch)
		}
		if err != nil && a.logger != nil {
			a.logger.Debug("runtime signal worktree projection lookup failed", "project_id", projectID, "worktree", worktree, "error", err)
		}
	}
	branch, err := a.client.CurrentBranch(ctx, worktree)
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("runtime signal current branch lookup failed", "project_id", projectID, "worktree", worktree, "error", err)
		}
		return "", ""
	}
	issueID, ok := naming.ExtractIssueIDFromBranchName(branch)
	if !ok {
		return "", strings.TrimSpace(branch)
	}
	return strings.TrimSpace(issueID), strings.TrimSpace(branch)
}

func gitStatusProjectionPersistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), 2*time.Second)
	}
	if ctx.Err() == nil {
		return context.WithTimeout(ctx, 2*time.Second)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
}

func (a *gitServiceAdapter) resolvedBaseBranchForWorktree(ctx context.Context, projectID, worktree string) (string, bool) {
	if baseBranch := a.worktreeSpecificBaseBranch(ctx, projectID, worktree); baseBranch != "" {
		return baseBranch, true
	}
	return a.resolvedBaseBranch(projectID), false
}

func (a *gitServiceAdapter) worktreeSpecificBaseBranch(ctx context.Context, projectID, worktree string) string {
	if a != nil && a.baseBranchForWorktree != nil {
		if baseBranch := strings.TrimSpace(a.baseBranchForWorktree(ctx, projectID, worktree)); baseBranch != "" {
			return baseBranch
		}
	}
	return ""
}

func (a *gitServiceAdapter) refreshGitStatusAsync(projectID, worktree string) {
	if _, err := a.queueGitStatusRefresh(projectID, worktree, reconcilePriorityBackground, "background"); err != nil && a.logger != nil {
		a.logger.Debug("git status background refresh enqueue failed", "project_id", projectID, "worktree", worktree, "error", err)
	}
}

func (a *gitServiceAdapter) ensureStatusRefreshThrottle() *reconcileThrottle {
	if a == nil {
		return newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "git_status_refresh",
			Budget:               defaultGitStatusRefreshBudget,
			Cadence:              defaultGitStatusRefreshCadence,
			UnchangedBackoffBase: defaultGitStatusRefreshUnchangedBackoff,
			UnchangedBackoffMax:  maxGitStatusRefreshUnchangedBackoff,
			FailureBackoffBase:   defaultGitStatusRefreshFailureBackoff,
			FailureBackoffMax:    maxGitStatusRefreshFailureBackoff,
		})
	}

	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	if a.statusRefreshThrottle == nil {
		a.statusRefreshThrottle = newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "git_status_refresh",
			Budget:               defaultGitStatusRefreshBudget,
			Cadence:              defaultGitStatusRefreshCadence,
			UnchangedBackoffBase: defaultGitStatusRefreshUnchangedBackoff,
			UnchangedBackoffMax:  maxGitStatusRefreshUnchangedBackoff,
			FailureBackoffBase:   defaultGitStatusRefreshFailureBackoff,
			FailureBackoffMax:    maxGitStatusRefreshFailureBackoff,
			Logger:               a.logger,
		})
	}
	return a.statusRefreshThrottle
}

func (a *gitServiceAdapter) persistStatusSnapshot(ctx context.Context, projectID, worktree string, status *git.GitStatus) (bool, string) {
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil || status == nil {
		return false, ""
	}
	projection, found, err := runtimeStore.GetWorktreeStateByPath(ctx, projectID, worktree)
	if err != nil || !found || strings.TrimSpace(projection.IssueID) == "" {
		if err != nil && a.logger != nil {
			a.logger.Warn("persist git status projection lookup failed", "project_id", projectID, "worktree", worktree, "error", err)
		}
		return false, ""
	}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("marshal git status projection failed", "project_id", projectID, "issue_id", projection.IssueID, "worktree", worktree, "error", err)
		}
		return false, ""
	}
	changed := string(rawStatus) != string(projection.GitStatusRaw)
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, projection.IssueID, rawStatus, time.Now().UTC()); err != nil {
		if a.logger != nil {
			a.logger.Warn("persist git status projection failed", "project_id", projectID, "issue_id", projection.IssueID, "worktree", worktree, "error", err)
		}
		return false, ""
	}
	return changed, projection.IssueID
}

func (a *gitServiceAdapter) ensureStatusPoller(projectID, worktree string) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return
	}
	interval := a.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	key := projectID + "|" + worktree
	a.refreshMu.Lock()
	if a.pollers == nil {
		a.pollers = map[string]context.CancelFunc{}
	}
	if _, exists := a.pollers[key]; exists {
		a.refreshMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.pollers[key] = cancel
	a.refreshMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil && a.logger != nil {
				a.logger.Error("git status poller panicked", "project_id", projectID, "worktree", worktree, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refreshGitStatusAsync(projectID, worktree)
			}
		}
	}()
}

func (a *gitServiceAdapter) stopStatusPoller(projectID, worktree string) {
	if a == nil {
		return
	}
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return
	}
	key := projectID + "|" + worktree
	a.refreshMu.Lock()
	cancel := a.pollers[key]
	if cancel != nil {
		delete(a.pollers, key)
	}
	a.refreshMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *gitServiceAdapter) suppressStaleWorktreeGitRefresh(ctx context.Context, projectID, worktree string, cause error) bool {
	if a == nil {
		return false
	}
	runtimeStore := a.runtimeStore(projectID)
	return suppressStaleWorktreeGitRefreshProjection(ctx, projectID, worktree, cause, runtimeStore, a.runtimeProjectionWriter, a.logger, func() {
		a.stopStatusPoller(projectID, worktree)
	})
}

func staleGitWorktreeRefreshReasonForError(worktree string, err error) string {
	reason, _ := staleGitWorktreeRefreshReason(worktree, err)
	return reason
}

func gitStatusRefreshQueueKey(projectID, worktree string) string {
	return normalizeProjectID(projectID) + "|" + strings.TrimSpace(worktree)
}

func hasMergeBlockingGitStatusChanges(status git.GitStatus) bool {
	return len(status.Modified) > 0 ||
		len(status.Added) > 0 ||
		len(status.Deleted) > 0 ||
		len(status.Staged) > 0
}

func mergeBlockingGitStatusFiles(status git.GitStatus) []string {
	seen := make(map[string]struct{})
	files := make([]string, 0, len(status.Modified)+len(status.Added)+len(status.Deleted)+len(status.Staged))
	add := func(list []string) {
		for _, file := range list {
			file = strings.TrimSpace(file)
			if file == "" {
				continue
			}
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			files = append(files, file)
		}
	}
	add(status.Modified)
	add(status.Added)
	add(status.Deleted)
	add(status.Staged)
	return files
}

func normalizeProjectID(projectID string) string {
	return protocol.NormalizeProjectID(projectID)
}

func (a *gitServiceAdapter) resolvedBaseBranch(projectID string) string {
	projectID = normalizeProjectID(projectID)
	if a.baseBranchForProject != nil {
		if projectBase := strings.TrimSpace(a.baseBranchForProject(projectID)); projectBase != "" {
			return projectBase
		}
	}
	return strings.TrimSpace(a.baseBranch)
}

func (a *gitServiceAdapter) preferRemoteRuntimeBase(projectID string) bool {
	if a == nil {
		return false
	}
	workflowMode := strings.TrimSpace(a.workflowMode)
	if a.workflowModeForProject != nil {
		if projectMode := strings.TrimSpace(a.workflowModeForProject(projectID)); projectMode != "" {
			workflowMode = projectMode
		}
	}
	return strings.EqualFold(workflowMode, "origin")
}

func gitStatusSignature(status *git.GitStatus) string {
	if status == nil {
		return ""
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (a *gitServiceAdapter) computeRuntimeSignalFromProjection(ctx context.Context, projectID, issueID, worktree string) (daemonhandlers.GitRuntimeSignalsResult, bool, error) {
	out := daemonhandlers.GitRuntimeSignalsResult{
		IssueID:  issueID,
		Worktree: worktree,
	}
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil {
		return out, false, nil
	}
	projection, found, err := runtimeStore.GetWorktreeStateByPath(ctx, projectID, worktree)
	if err != nil {
		return out, false, err
	}
	if !found || len(projection.GitStatusRaw) == 0 {
		return out, false, nil
	}
	var status git.GitStatus
	if err := json.Unmarshal(projection.GitStatusRaw, &status); err != nil {
		return out, false, err
	}
	out.HasUncommittedChanges = status.HasChanges
	out.HasConflicts = status.HasConflicts
	out.ConflictFiles = append([]string(nil), status.Conflicted...)
	out.GitAdditions = status.GitAdditions
	out.GitDeletions = status.GitDeletions
	out.GitAheadCount = status.GitAheadCount
	out.GitBehindCount = status.GitBehindCount

	return out, true, nil
}

func runtimeSignalCacheKey(projectID, issueID, worktree, baseBranch string, compareRemote bool, remote string) string {
	return strings.ToLower(strings.TrimSpace(projectID)) + "|" +
		strings.ToLower(strings.TrimSpace(issueID)) + "|" +
		strings.TrimSpace(worktree) + "|" +
		strings.TrimSpace(baseBranch) + "|" +
		strconv.FormatBool(compareRemote) + "|" +
		strings.TrimSpace(remote)
}

func (a *gitServiceAdapter) cachedRuntimeSignal(cacheKey string, now time.Time) (daemonhandlers.GitRuntimeSignalsResult, bool) {
	a.runtimeSignalsMu.Lock()
	defer a.runtimeSignalsMu.Unlock()
	if a.runtimeSignalsCache == nil {
		return daemonhandlers.GitRuntimeSignalsResult{}, false
	}
	entry, ok := a.runtimeSignalsCache[cacheKey]
	if !ok || entry.refreshedAt.IsZero() || now.Sub(entry.refreshedAt) > runtimeSignalProjectionTTL {
		return daemonhandlers.GitRuntimeSignalsResult{}, false
	}
	return entry.signal, true
}

func (a *gitServiceAdapter) cachedRuntimeSignalAnyBase(projectID, issueID, worktree string, now time.Time) (daemonhandlers.GitRuntimeSignalsResult, bool) {
	a.runtimeSignalsMu.Lock()
	defer a.runtimeSignalsMu.Unlock()
	if len(a.runtimeSignalsCache) == 0 {
		return daemonhandlers.GitRuntimeSignalsResult{}, false
	}
	prefix := strings.ToLower(strings.TrimSpace(projectID)) + "|" +
		strings.ToLower(strings.TrimSpace(issueID)) + "|" +
		strings.TrimSpace(worktree) + "|"
	var (
		found bool
		best  runtimeSignalProjection
	)
	for key, entry := range a.runtimeSignalsCache {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if entry.refreshedAt.IsZero() || now.Sub(entry.refreshedAt) > runtimeSignalProjectionTTL {
			continue
		}
		if !found || entry.refreshedAt.After(best.refreshedAt) {
			best = entry
			found = true
		}
	}
	if !found {
		return daemonhandlers.GitRuntimeSignalsResult{}, false
	}
	return best.signal, true
}

func (a *gitServiceAdapter) storeRuntimeSignal(cacheKey string, signal daemonhandlers.GitRuntimeSignalsResult, refreshedAt time.Time) {
	a.runtimeSignalsMu.Lock()
	defer a.runtimeSignalsMu.Unlock()
	if a.runtimeSignalsCache == nil {
		a.runtimeSignalsCache = make(map[string]runtimeSignalProjection)
	}
	a.runtimeSignalsCache[cacheKey] = runtimeSignalProjection{
		signal:      signal,
		refreshedAt: refreshedAt,
	}
}

func (a *gitServiceAdapter) invalidateRuntimeSignalCache(projectID, worktree string) {
	a.runtimeSignalsMu.Lock()
	defer a.runtimeSignalsMu.Unlock()
	if len(a.runtimeSignalsCache) == 0 {
		return
	}
	prefix := strings.ToLower(strings.TrimSpace(projectID)) + "|"
	worktreeNeedle := "|" + strings.TrimSpace(worktree) + "|"
	for key := range a.runtimeSignalsCache {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if strings.Contains(key, worktreeNeedle) {
			delete(a.runtimeSignalsCache, key)
		}
	}
}

func parseDiffStatTotalsDaemon(diffStat string) (int, int) {
	var additions, deletions int
	insertionMatches := daemonDiffStatInsertionsPattern.FindAllStringSubmatch(diffStat, -1)
	for _, insertionMatch := range insertionMatches {
		if len(insertionMatch) != 2 {
			continue
		}
		if parsed, err := strconv.Atoi(insertionMatch[1]); err == nil {
			additions += parsed
		}
	}
	deletionMatches := daemonDiffStatDeletionsPattern.FindAllStringSubmatch(diffStat, -1)
	for _, deletionMatch := range deletionMatches {
		if len(deletionMatch) != 2 {
			continue
		}
		if parsed, err := strconv.Atoi(deletionMatch[1]); err == nil {
			deletions += parsed
		}
	}
	return additions, deletions
}
