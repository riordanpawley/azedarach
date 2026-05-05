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
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
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
	baseBranchForProject        func(string) string

	refreshMu      sync.Mutex
	refreshRunning map[string]bool
	pollers        map[string]context.CancelFunc

	runtimeSignalsMu    sync.Mutex
	runtimeSignalsCache map[string]runtimeSignalProjection
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
	_ daemonhandlers.GitService               = (*gitServiceAdapter)(nil)
	_ daemonhandlers.GitMergePreflightService = (*gitServiceAdapter)(nil)
	_ daemonhandlers.GitDiscardChangesService = (*gitServiceAdapter)(nil)
	_ daemonhandlers.GitCheckpointService     = (*gitServiceAdapter)(nil)
)

func (a *gitServiceAdapter) Fetch(ctx context.Context, projectID, worktree, remote string) error {
	if err := a.client.Fetch(ctx, worktree, remote); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return nil
}

func (a *gitServiceAdapter) Merge(ctx context.Context, projectID, worktree, branch string) (*git.MergeResult, error) {
	result, err := a.client.Merge(ctx, worktree, branch)
	if err != nil {
		return nil, err
	}
	// Merge completion should always trigger an update notification so clients
	// refresh runtime git signals even when porcelain status stays clean.
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, true)
	return result, nil
}

func (a *gitServiceAdapter) Checkout(ctx context.Context, projectID, worktree, branch string) error {
	if err := a.client.Checkout(ctx, worktree, branch); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return nil
}

func (a *gitServiceAdapter) AbortMerge(ctx context.Context, projectID, worktree string) error {
	if err := a.client.AbortMerge(ctx, worktree); err != nil {
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

	if hasMergeBlockingGitStatusChanges(result.SourceStatus) {
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
	if err := a.client.DiscardChanges(ctx, worktree); err != nil {
		return nil, err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true, false)
	return &daemonhandlers.GitDiscardChangesResult{Worktree: worktree}, nil
}

func (a *gitServiceAdapter) Checkpoint(ctx context.Context, projectID string, req daemonhandlers.GitCheckpointRequest) (*daemonhandlers.GitCheckpointResult, error) {
	if err := a.client.CreateCheckpoint(ctx, req.Worktree, req.Message); err != nil {
		return nil, err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, req.Worktree, true, false)
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = git.DefaultCheckpointMessage
	}
	return &daemonhandlers.GitCheckpointResult{Worktree: req.Worktree, Message: message}, nil
}

func (a *gitServiceAdapter) DiffStat(ctx context.Context, _ string, worktree, baseBranch string) (string, error) {
	return a.client.DiffStat(ctx, worktree, baseBranch)
}

func (a *gitServiceAdapter) RuntimeSignals(ctx context.Context, projectID string, targets []daemonhandlers.GitRuntimeSignalsTarget, baseBranch string, compareRemote bool, remote string) ([]daemonhandlers.GitRuntimeSignalsResult, int, error) {
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
		cacheKey := runtimeSignalCacheKey(projectID, issueID, worktree, baseBranch, compareRemote, remote)
		if cached, ok := a.cachedRuntimeSignal(cacheKey, now); ok {
			results = append(results, cached)
			continue
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
			status, refreshErr := a.refreshGitStatusWriteThroughResult(refreshCtx, projectID, worktree, true, false)
			outcome := throttle.Record(key, gitStatusSignature(status), refreshErr)
			if a.logger != nil {
				counters := throttle.snapshotCounters()
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

func (a *gitServiceAdapter) refreshGitStatusWriteThroughResult(ctx context.Context, projectID, worktree string, publishOnChange, forcePublish bool) (*git.GitStatus, error) {
	projectID = normalizeProjectID(projectID)
	baseBranch := a.resolvedBaseBranch(projectID)

	var (
		status *git.GitStatus
		err    error
	)
	if baseBranch != "" {
		status, err = a.client.RuntimeStatus(ctx, worktree, baseBranch)
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
			if a.logger != nil {
				a.logger.Debug("git status refresh after mutation failed", "project_id", projectID, "worktree", worktree, "error", err)
			}
			return nil, err
		}
	}
	if a.runtimeProjectionWriter != nil {
		_ = a.runtimeProjectionWriter.PersistGitStatusProjectionAndPublish(ctx, projectID, "", worktree, status, publishOnChange, forcePublish)
		a.invalidateRuntimeSignalCache(projectID, worktree)
		return status, nil
	}
	changed, issueID := a.persistStatusSnapshot(ctx, projectID, worktree, status)
	a.invalidateRuntimeSignalCache(projectID, worktree)
	if (forcePublish || (publishOnChange && changed)) && a.onStatusUpdate != nil && strings.TrimSpace(issueID) != "" {
		a.onStatusUpdate(ctx, projectID, issueID, worktree, status)
	}
	return status, nil
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
		return false, ""
	}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		return false, ""
	}
	changed := string(rawStatus) != string(projection.GitStatusRaw)
	if err := runtimeStore.UpsertWorktreeStateGitStatus(ctx, projectID, projection.IssueID, rawStatus, time.Now().UTC()); err != nil && a.logger != nil {
		a.logger.Debug("persist git status projection failed", "project_id", projectID, "issue_id", projection.IssueID, "worktree", worktree, "error", err)
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
