package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	defaultRuntimeReconcileInterval = 30 * time.Second
	defaultRuntimeReconcileTimeout  = 5 * time.Second
	scopedRuntimeReconcileTimeout   = 20 * time.Second
)

type runtimeReconciler interface {
	Reconcile(context.Context, string) (protocol.RuntimeReconcileResponseBody, error)
}

type runtimeReconcileService struct {
	daemon *Daemon
}

type runtimeReconcileCommandBody struct {
	ProjectID string `json:"project_id"`
}

type runtimeReconcileRequestContextKey struct{}

type runtimeReconcileRequestContext struct {
	Priority reconcileQueuePriority
	Reason   string
}

type runtimeReconcileSweepMetrics struct {
	Processed int
	Skipped   int
	Deferred  int
	Failed    int
}

func newRuntimeReconcileService(d *Daemon) *runtimeReconcileService {
	return &runtimeReconcileService{daemon: d}
}

func (s *runtimeReconcileService) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	result := protocol.RuntimeReconcileResponseBody{
		ProjectID:        protocol.NormalizeProjectID(projectID),
		InvariantSources: invariantSourceDebugMap(),
	}
	d := s.daemon
	if d == nil {
		return result, nil
	}
	if d.tmux == nil || d.sessionStore == nil || d.sessionRuntimeStateStoreIfConfigured(result.ProjectID) == nil {
		return result, nil
	}

	var errs []error
	if d.worktreeRuntimeStateStoreIfConfigured(result.ProjectID) != nil && d.worktreeManagerForProject(result.ProjectID) != nil {
		if worktreeCount, err := d.refreshWorktreeRuntimeState(ctx, result.ProjectID); err != nil {
			errs = append(errs, fmt.Errorf("refresh worktree runtime state: %w", err))
		} else {
			result.WorktreesRefreshed = worktreeCount
		}
		if sessionResult, err := d.reconcileTmuxAndDaemonSessions(ctx, result.ProjectID, ""); err != nil {
			errs = append(errs, fmt.Errorf("reconcile sessions: %w", err))
		} else {
			result.RecreatedTmuxSessions = sessionResult.RecreatedTmuxSessions
			result.AlignedDaemonSessions = sessionResult.AlignedDaemonSessions
		}
	}

	if err := d.refreshSessionRuntimeState(ctx, result.ProjectID); err != nil {
		errs = append(errs, fmt.Errorf("refresh session runtime state: %w", err))
	}

	return result, errors.Join(errs...)
}

func (d *Daemon) runtimeReconcileInterval() time.Duration {
	if d.cfg.RuntimeReconcileInterval > 0 {
		return d.cfg.RuntimeReconcileInterval
	}
	return defaultRuntimeReconcileInterval
}

func (d *Daemon) runtimeReconcileTimeout() time.Duration {
	if d.cfg.RuntimeReconcileTimeout > 0 {
		return d.cfg.RuntimeReconcileTimeout
	}
	if isScopedDaemonModeEnv(os.Getenv("AZEDARACH_DAEMON_SCOPE"), os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE")) {
		return scopedRuntimeReconcileTimeout
	}
	return defaultRuntimeReconcileTimeout
}

func (d *Daemon) runtimeReconcileProjectID(req protocol.RequestEnvelope) string {
	projectID := strings.TrimSpace(d.projectID(req.Meta))
	if projectID == "" {
		return protocol.DefaultProjectID
	}
	return projectID
}

func (d *Daemon) ensureRuntimeReconciler() runtimeReconciler {
	if d.runtimeReconciler == nil {
		d.runtimeReconciler = newRuntimeReconcileService(d)
	}
	return d.runtimeReconciler
}

func (d *Daemon) ensureRuntimeReconcileThrottle() *reconcileThrottle {
	if d == nil {
		return newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "runtime_reconcile",
			Budget:               defaultRuntimeReconcileBudget,
			Cadence:              defaultRuntimeReconcileInterval,
			UnchangedBackoffBase: defaultRuntimeReconcileUnchangedBackoff,
			UnchangedBackoffMax:  maxRuntimeReconcileUnchangedBackoff,
			FailureBackoffBase:   defaultRuntimeReconcileFailureBackoff,
			FailureBackoffMax:    maxRuntimeReconcileFailureBackoff,
		})
	}

	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	if d.runtimeReconcileThrottle == nil {
		d.runtimeReconcileThrottle = newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "runtime_reconcile",
			Budget:               defaultRuntimeReconcileBudget,
			Cadence:              d.runtimeReconcileInterval(),
			UnchangedBackoffBase: defaultRuntimeReconcileUnchangedBackoff,
			UnchangedBackoffMax:  maxRuntimeReconcileUnchangedBackoff,
			FailureBackoffBase:   defaultRuntimeReconcileFailureBackoff,
			FailureBackoffMax:    maxRuntimeReconcileFailureBackoff,
			Logger:               d.cfg.Logger,
		})
	}
	return d.runtimeReconcileThrottle
}

func (d *Daemon) ensureRuntimeReconcileQueue() *reconcileQueue[protocol.RuntimeReconcileResponseBody] {
	if d == nil {
		return newReconcileQueue[protocol.RuntimeReconcileResponseBody](reconcileQueueConfig{
			Name:    "runtime_reconcile",
			Workers: defaultRuntimeReconcileQueueWorkers,
		})
	}

	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	if d.runtimeReconcileQueue == nil {
		d.runtimeReconcileQueue = newReconcileQueue[protocol.RuntimeReconcileResponseBody](reconcileQueueConfig{
			Name:    "runtime_reconcile",
			Workers: defaultRuntimeReconcileQueueWorkers,
			Logger:  d.cfg.Logger,
		})
	}
	return d.runtimeReconcileQueue
}

func (d *Daemon) queueRuntimeReconcile(ctx context.Context, projectID string, priority reconcileQueuePriority, reason string) (reconcileQueueSubmission[protocol.RuntimeReconcileResponseBody], error) {
	projectID = protocol.NormalizeProjectID(projectID)
	return d.ensureRuntimeReconcileQueue().Enqueue(reconcileQueueRequest[protocol.RuntimeReconcileResponseBody]{
		Key:         projectID,
		Priority:    priority,
		Reason:      reason,
		ExecContext: ctx,
		Work: func(workCtx context.Context) (protocol.RuntimeReconcileResponseBody, error) {
			workCtx = context.WithValue(workCtx, runtimeReconcileRequestContextKey{}, runtimeReconcileRequestContext{
				Priority: priority,
				Reason:   reason,
			})
			result, err := d.ensureRuntimeReconciler().Reconcile(workCtx, projectID)
			d.ensureRuntimeReconcileThrottle().Record(projectID, runtimeReconcileResultSignature(result), err)
			return result, err
		},
	})
}

func (d *Daemon) ensureFreshRuntimeForMutation(ctx context.Context, projectID string, reason string) error {
	if d == nil {
		return nil
	}
	projectID = protocol.NormalizeProjectID(projectID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "mutation"
	}

	timeout := d.runtimeReconcileTimeout()
	if timeout <= 0 {
		timeout = defaultRuntimeReconcileTimeout
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	submission, err := d.queueRuntimeReconcile(reconcileCtx, projectID, reconcilePriorityManual, "mutation:"+reason)
	if err != nil {
		return fmt.Errorf("queue runtime reconcile: %w", err)
	}
	outcome, err := submission.Wait(reconcileCtx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			if fallbackErr := d.directRuntimeReconcileForMutation(ctx, projectID, reason, timeout); fallbackErr == nil {
				if d.cfg.Logger != nil {
					d.cfg.Logger.Warn("runtime reconcile queue wait timed out; direct mutation reconcile succeeded",
						"project_id", projectID,
						"reason", reason,
					)
				}
				return nil
			} else {
				return fmt.Errorf("wait runtime reconcile: %w; direct runtime reconcile fallback failed: %w", err, fallbackErr)
			}
		}
		return fmt.Errorf("wait runtime reconcile: %w", err)
	}
	if outcome.Err != nil {
		return fmt.Errorf("runtime reconcile failed: %w", outcome.Err)
	}
	return nil
}

func (d *Daemon) directRuntimeReconcileForMutation(ctx context.Context, projectID string, reason string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultRuntimeReconcileTimeout
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reconcileCtx = context.WithValue(reconcileCtx, runtimeReconcileRequestContextKey{}, runtimeReconcileRequestContext{
		Priority: reconcilePriorityManual,
		Reason:   "mutation-direct:" + strings.TrimSpace(reason),
	})
	result, err := d.ensureRuntimeReconciler().Reconcile(reconcileCtx, projectID)
	d.ensureRuntimeReconcileThrottle().Record(projectID, runtimeReconcileResultSignature(result), err)
	if err != nil {
		return err
	}
	return nil
}

func (d *Daemon) handleRuntimeReconcile(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body runtimeReconcileCommandBody
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
		}
	}
	projectID := strings.TrimSpace(body.ProjectID)
	if projectID == "" {
		projectID = d.runtimeReconcileProjectID(req)
	} else {
		projectID = protocol.NormalizeProjectID(projectID)
	}

	submission, err := d.queueRuntimeReconcile(ctx, projectID, reconcilePriorityManual, "manual")
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	outcome, err := submission.Wait(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if outcome.Err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, outcome.Err.Error()), nil
	}
	result := outcome.Value
	if len(result.InvariantSources) == 0 {
		result.InvariantSources = invariantSourceDebugMap()
	}

	resp := d.successResponse(req)
	resp.Revision = d.currentRevision(projectID)
	bodyBytes, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal response body: %v", err)), nil
	}
	resp.Body = bodyBytes
	return resp, nil
}

func (d *Daemon) runStartupRuntimeReconcile(ctx context.Context) (protocol.RuntimeReconcileResponseBody, error) {
	if d == nil {
		return protocol.RuntimeReconcileResponseBody{}, nil
	}
	timeout := d.runtimeReconcileTimeout()
	if timeout <= 0 {
		timeout = defaultRuntimeReconcileTimeout
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results, _, err := d.runRuntimeReconcileSweepWithPriority(reconcileCtx, reconcilePriorityBackground, "startup")
	return summarizeRuntimeReconcileSweep(results), err
}

func (d *Daemon) startRuntimeReconcileWorker(ctx context.Context) {
	if d == nil {
		return
	}
	interval := d.runtimeReconcileInterval()
	if interval <= 0 {
		interval = defaultRuntimeReconcileInterval
	}
	go func() {
		defer func() {
			if r := recover(); r != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Error("daemon runtime reconcile worker panicked", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.runRuntimeReconcileCycle(ctx)
			}
		}
	}()
}

func (d *Daemon) runRuntimeReconcileCycle(ctx context.Context) {
	if d == nil {
		return
	}
	timeout := d.runtimeReconcileTimeout()
	if timeout <= 0 {
		timeout = defaultRuntimeReconcileTimeout
	}
	reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	results, metrics, err := d.runRuntimeReconcileSweepWithPriority(reconcileCtx, reconcilePriorityBackground, "periodic")
	result := summarizeRuntimeReconcileSweep(results)
	projectCount := len(results)
	if projectCount == 0 {
		projectCount = 1
	}
	queueCounters := d.ensureRuntimeReconcileQueue().snapshotCounters()
	throttleCounters := d.ensureRuntimeReconcileThrottle().snapshotCounters()
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("daemon runtime reconcile cycle failed",
				"project_id", result.ProjectID,
				"project_count", projectCount,
				"processed_tasks", metrics.Processed,
				"skipped_tasks", metrics.Skipped,
				"deferred_tasks", metrics.Deferred,
				"failed_tasks", metrics.Failed,
				"queue_enqueued", queueCounters.Enqueued,
				"queue_dequeued", queueCounters.Dequeued,
				"queue_deduped", queueCounters.Deduped,
				"queue_reprioritized", queueCounters.Reprioritized,
				"throttle_processed", throttleCounters.Processed,
				"throttle_skipped", throttleCounters.Skipped,
				"throttle_deferred", throttleCounters.Deferred,
				"error", err,
			)
		}
		return
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("daemon runtime reconcile cycle completed",
			"project_id", result.ProjectID,
			"project_count", projectCount,
			"worktrees_refreshed", result.WorktreesRefreshed,
			"recreated_tmux_sessions", result.RecreatedTmuxSessions,
			"aligned_daemon_sessions", result.AlignedDaemonSessions,
			"processed_tasks", metrics.Processed,
			"skipped_tasks", metrics.Skipped,
			"deferred_tasks", metrics.Deferred,
			"failed_tasks", metrics.Failed,
			"queue_enqueued", queueCounters.Enqueued,
			"queue_dequeued", queueCounters.Dequeued,
			"queue_deduped", queueCounters.Deduped,
			"queue_reprioritized", queueCounters.Reprioritized,
			"throttle_processed", throttleCounters.Processed,
			"throttle_skipped", throttleCounters.Skipped,
			"throttle_deferred", throttleCounters.Deferred,
		)
	}
}

func (d *Daemon) runRuntimeReconcileSweep(ctx context.Context) ([]protocol.RuntimeReconcileResponseBody, error) {
	results, _, err := d.runRuntimeReconcileSweepWithPriority(ctx, reconcilePriorityBackground, "background")
	return results, err
}

func (d *Daemon) runRuntimeReconcileSweepWithPriority(ctx context.Context, priority reconcileQueuePriority, reason string) ([]protocol.RuntimeReconcileResponseBody, runtimeReconcileSweepMetrics, error) {
	projectIDs, err := d.runtimeReconcileKnownProjectIDs(ctx)
	if err != nil {
		return nil, runtimeReconcileSweepMetrics{}, err
	}
	type queuedProject struct {
		projectID  string
		submission reconcileQueueSubmission[protocol.RuntimeReconcileResponseBody]
	}
	metrics := runtimeReconcileSweepMetrics{}
	queued := make([]queuedProject, 0, len(projectIDs))
	var submitErrs []error
	queue := d.ensureRuntimeReconcileQueue()
	throttle := d.ensureRuntimeReconcileThrottle()
	force := priority >= reconcilePriorityManual
	for _, projectID := range projectIDs {
		admission := reconcileThrottleDecision{Action: reconcileThrottleProcess}
		if !force && !queue.HasJob(projectID) {
			admission = throttle.Admit(projectID, false)
			switch admission.Action {
			case reconcileThrottleSkip:
				metrics.Skipped++
				continue
			case reconcileThrottleDefer:
				metrics.Deferred++
				continue
			}
		}
		submission, submitErr := d.queueRuntimeReconcile(ctx, projectID, priority, reason)
		if submitErr != nil {
			throttle.Refund(admission)
			submitErrs = append(submitErrs, fmt.Errorf("%s: %w", projectID, submitErr))
			metrics.Failed++
			continue
		}
		if submission.Deduped {
			throttle.Refund(admission)
		}
		if !submission.Deduped {
			metrics.Processed++
		}
		queued = append(queued, queuedProject{
			projectID:  projectID,
			submission: submission,
		})
	}

	results := make([]protocol.RuntimeReconcileResponseBody, 0, len(queued))
	var errs []error
	errs = append(errs, submitErrs...)
	for _, item := range queued {
		outcome, waitErr := item.submission.Wait(ctx)
		if waitErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", item.projectID, waitErr))
			metrics.Failed++
			continue
		}
		if outcome.Skipped || outcome.Deferred {
			continue
		}
		results = append(results, outcome.Value)
		if outcome.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", item.projectID, outcome.Err))
			metrics.Failed++
		}
	}
	return results, metrics, errors.Join(errs...)
}

func (d *Daemon) runtimeReconcileKnownProjectIDs(ctx context.Context) ([]string, error) {
	source := sourceForInvariant(daemonInvariantRuntimeKnownProjectIDs)
	seen := map[string]struct{}{}
	projectIDs := make([]string, 0, 8)
	add := func(projectID string) {
		normalized := d.canonicalProjectID(projectID)
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		projectIDs = append(projectIDs, normalized)
	}

	if d == nil {
		return []string{protocol.DefaultProjectID}, nil
	}
	repoDir := strings.TrimSpace(d.cfg.RepoDir)
	scopedMode := isScopedDaemonModeEnv(os.Getenv("AZEDARACH_DAEMON_SCOPE"), os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE"))
	repoProjectID := ""
	repoNameProjectID := ""
	if repoDir != "" {
		repoNameProjectID = d.canonicalProjectID(filepath.Base(repoDir))
		projectID, err := appconfig.ProjectIDForRoot(repoDir)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("resolve runtime reconcile seed project id failed", "repo_dir", repoDir, "error", err)
			}
		} else {
			repoProjectID = d.canonicalProjectID(projectID)
			add(projectID)
		}
	}

	if d.sessionStore != nil {
		for _, projectID := range d.sessionStore.ProjectIDs() {
			add(projectID)
		}
	}

	addRuntimeStateProjects := func(store interface {
		ListProjectIDs(context.Context) ([]string, error)
	}) error {
		known, err := store.ListProjectIDs(ctx)
		if err != nil {
			return fmt.Errorf("list runtime-state project ids: %w", err)
		}
		for _, projectID := range known {
			add(projectID)
		}
		return nil
	}
	if usesProjectionSource(source) {
		if sessionStore := d.sessionRuntimeStateStoreIfConfigured(protocol.DefaultProjectID); sessionStore != nil {
			if err := addRuntimeStateProjects(sessionStore); err != nil {
				return nil, err
			}
		}
		if worktreeStore := d.worktreeRuntimeStateStoreIfConfigured(protocol.DefaultProjectID); worktreeStore != nil && worktreeStore != d.sessionRuntimeStateStoreIfConfigured(protocol.DefaultProjectID) {
			if err := addRuntimeStateProjects(worktreeStore); err != nil {
				return nil, err
			}
		}
	}

	d.revMu.Lock()
	for projectID := range d.revision {
		add(projectID)
	}
	d.revMu.Unlock()

	if len(projectIDs) == 0 {
		add(protocol.DefaultProjectID)
	}
	if scopedMode {
		projectIDs = prioritizeProjectIDs(projectIDs, []string{repoNameProjectID, repoProjectID})
	} else {
		slices.Sort(projectIDs)
	}
	return projectIDs, nil
}

func isScopedDaemonModeEnv(mode, source string) bool {
	mode = strings.TrimSpace(strings.ToLower(mode))
	source = strings.TrimSpace(strings.ToLower(source))
	switch mode {
	case "worktree", "scoped", "local":
		return source == "just-run"
	default:
		return false
	}
}

func prioritizeProjectIDs(projectIDs []string, preferred []string) []string {
	if len(projectIDs) == 0 {
		return projectIDs
	}
	ordered := append([]string(nil), projectIDs...)
	rest := make([]string, 0, len(ordered))
	seenPreferred := make(map[string]struct{}, len(preferred))
	prioritized := make([]string, 0, len(preferred))
	for _, id := range preferred {
		norm := protocol.NormalizeProjectID(id)
		if norm == "" {
			continue
		}
		if _, exists := seenPreferred[norm]; exists {
			continue
		}
		seenPreferred[norm] = struct{}{}
		prioritized = append(prioritized, norm)
	}
	found := make(map[string]struct{}, len(prioritized))
	for _, projectID := range ordered {
		if _, preferred := seenPreferred[projectID]; preferred {
			found[projectID] = struct{}{}
		} else {
			rest = append(rest, projectID)
		}
	}
	slices.Sort(rest)
	result := make([]string, 0, len(ordered))
	for _, projectID := range prioritized {
		if _, ok := found[projectID]; ok {
			result = append(result, projectID)
		}
	}
	return append(result, rest...)
}

func summarizeRuntimeReconcileSweep(results []protocol.RuntimeReconcileResponseBody) protocol.RuntimeReconcileResponseBody {
	if len(results) == 0 {
		return protocol.RuntimeReconcileResponseBody{
			ProjectID:        protocol.DefaultProjectID,
			InvariantSources: invariantSourceDebugMap(),
		}
	}
	summary := protocol.RuntimeReconcileResponseBody{
		ProjectID:        results[0].ProjectID,
		InvariantSources: invariantSourceDebugMap(),
	}
	if len(results) > 1 {
		summary.ProjectID = "multi"
	}
	for _, result := range results {
		summary.WorktreesRefreshed += result.WorktreesRefreshed
		summary.RecreatedTmuxSessions += result.RecreatedTmuxSessions
		summary.AlignedDaemonSessions += result.AlignedDaemonSessions
	}
	return summary
}

func runtimeReconcileResultSignature(result protocol.RuntimeReconcileResponseBody) string {
	raw, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(raw)
}

func runtimeReconcileRequestFromContext(ctx context.Context) runtimeReconcileRequestContext {
	if ctx == nil {
		return runtimeReconcileRequestContext{}
	}
	value, ok := ctx.Value(runtimeReconcileRequestContextKey{}).(runtimeReconcileRequestContext)
	if !ok {
		return runtimeReconcileRequestContext{}
	}
	return value
}
