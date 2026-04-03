package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type reconcileQueuePriority int

const (
	reconcilePriorityBackground reconcileQueuePriority = 1
	reconcilePriorityVisible    reconcileQueuePriority = 2
	reconcilePriorityManual     reconcileQueuePriority = 3
)

func (p reconcileQueuePriority) String() string {
	switch p {
	case reconcilePriorityBackground:
		return "background"
	case reconcilePriorityVisible:
		return "visible"
	case reconcilePriorityManual:
		return "manual"
	default:
		return fmt.Sprintf("priority(%d)", p)
	}
}

type reconcileQueueConfig struct {
	Name    string
	Workers int
	Logger  *slog.Logger
	Now     func() time.Time
}

type reconcileQueueRequest[T any] struct {
	Key         string
	Priority    reconcileQueuePriority
	Reason      string
	ExecContext context.Context
	Work        func(context.Context) (T, error)
}

type reconcileQueueResult[T any] struct {
	Key        string
	Value      T
	Err        error
	StartedAt  time.Time
	FinishedAt time.Time
	Skipped    bool
	Deferred   bool
	Reason     string
	Until      time.Time
}

type reconcileQueueSubmission[T any] struct {
	Key           string
	Deduped       bool
	Reprioritized bool
	done          <-chan reconcileQueueResult[T]
}

func immediateReconcileSubmission[T any](result reconcileQueueResult[T]) reconcileQueueSubmission[T] {
	done := make(chan reconcileQueueResult[T], 1)
	done <- result
	close(done)
	return reconcileQueueSubmission[T]{
		Key:  result.Key,
		done: done,
	}
}

func (s reconcileQueueSubmission[T]) Wait(ctx context.Context) (reconcileQueueResult[T], error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result, ok := <-s.done:
		if !ok {
			return reconcileQueueResult[T]{}, context.Canceled
		}
		return result, nil
	case <-ctx.Done():
		return reconcileQueueResult[T]{}, ctx.Err()
	}
}

type reconcileQueueCounterSnapshot struct {
	Enqueued      uint64
	Dequeued      uint64
	Deduped       uint64
	Reprioritized uint64
}

type reconcileQueueSnapshot struct {
	Pending  []string
	Running  []string
	Counters reconcileQueueCounterSnapshot
}

type reconcileQueueCounters struct {
	enqueued      atomic.Uint64
	dequeued      atomic.Uint64
	deduped       atomic.Uint64
	reprioritized atomic.Uint64
}

type reconcileQueueJobState string

const (
	reconcileQueueJobPending reconcileQueueJobState = "pending"
	reconcileQueueJobRunning reconcileQueueJobState = "running"
)

type reconcileQueueJob[T any] struct {
	key         string
	priority    reconcileQueuePriority
	reason      string
	execContext context.Context
	work        func(context.Context) (T, error)
	waiters     []chan reconcileQueueResult[T]
	state       reconcileQueueJobState
	sequence    uint64
}

type reconcileQueue[T any] struct {
	name    string
	logger  *slog.Logger
	now     func() time.Time
	workers int

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cond    *sync.Cond
	closed  bool
	pending []*reconcileQueueJob[T]
	jobs    map[string]*reconcileQueueJob[T]
	nextSeq uint64

	counters reconcileQueueCounters
	wg       sync.WaitGroup
}

func newReconcileQueue[T any](cfg reconcileQueueConfig) *reconcileQueue[T] {
	workers := cfg.Workers
	if workers <= 0 {
		workers = 1
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = "reconcile"
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &reconcileQueue[T]{
		name:    name,
		logger:  cfg.Logger,
		now:     now,
		workers: workers,
		ctx:     ctx,
		cancel:  cancel,
		jobs:    make(map[string]*reconcileQueueJob[T]),
	}
	q.cond = sync.NewCond(&q.mu)
	q.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go q.worker(i + 1)
	}
	return q
}

func (q *reconcileQueue[T]) Close() error {
	if q == nil {
		return nil
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		q.wg.Wait()
		return nil
	}
	q.closed = true
	pending := append([]*reconcileQueueJob[T](nil), q.pending...)
	q.pending = nil
	for _, job := range pending {
		if current, ok := q.jobs[job.key]; ok && current == job {
			delete(q.jobs, job.key)
		}
	}
	q.cancel()
	q.cond.Broadcast()
	q.mu.Unlock()

	now := q.now().UTC()
	for _, job := range pending {
		q.finish(job, reconcileQueueResult[T]{
			Key:        job.key,
			Err:        context.Canceled,
			StartedAt:  now,
			FinishedAt: now,
		})
	}

	q.wg.Wait()
	return nil
}

func (q *reconcileQueue[T]) Enqueue(req reconcileQueueRequest[T]) (reconcileQueueSubmission[T], error) {
	if q == nil {
		return reconcileQueueSubmission[T]{}, fmt.Errorf("reconcile queue unavailable")
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return reconcileQueueSubmission[T]{}, fmt.Errorf("reconcile queue %s: missing key", q.name)
	}
	if req.Work == nil {
		return reconcileQueueSubmission[T]{}, fmt.Errorf("reconcile queue %s: missing work for %s", q.name, key)
	}
	priority := req.Priority
	if priority <= 0 {
		priority = reconcilePriorityBackground
	}
	if req.ExecContext == nil {
		req.ExecContext = context.Background()
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = priority.String()
	}

	done := make(chan reconcileQueueResult[T], 1)

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return reconcileQueueSubmission[T]{}, fmt.Errorf("reconcile queue %s closed", q.name)
	}

	if existing, ok := q.jobs[key]; ok {
		existing.waiters = append(existing.waiters, done)
		q.counters.deduped.Add(1)

		reprioritized := false
		if existing.state == reconcileQueueJobPending && priority > existing.priority {
			existing.priority = priority
			existing.reason = reason
			existing.execContext = req.ExecContext
			q.sortPendingLocked()
			q.counters.reprioritized.Add(1)
			reprioritized = true
			q.cond.Signal()
		}
		pendingLen := len(q.pending)
		state := existing.state
		currentPriority := existing.priority
		q.mu.Unlock()

		q.log("reconcile queue deduped",
			"queue", q.name,
			"key", key,
			"state", state,
			"priority", currentPriority.String(),
			"reason", reason,
			"pending", pendingLen,
		)
		if reprioritized {
			q.log("reconcile queue reprioritized",
				"queue", q.name,
				"key", key,
				"priority", currentPriority.String(),
				"reason", reason,
				"pending", pendingLen,
			)
		}

		return reconcileQueueSubmission[T]{
			Key:           key,
			Deduped:       true,
			Reprioritized: reprioritized,
			done:          done,
		}, nil
	}

	q.nextSeq++
	job := &reconcileQueueJob[T]{
		key:         key,
		priority:    priority,
		reason:      reason,
		execContext: req.ExecContext,
		work:        req.Work,
		waiters:     []chan reconcileQueueResult[T]{done},
		state:       reconcileQueueJobPending,
		sequence:    q.nextSeq,
	}
	q.pending = append(q.pending, job)
	q.jobs[key] = job
	q.sortPendingLocked()
	pendingLen := len(q.pending)
	q.counters.enqueued.Add(1)
	q.cond.Signal()
	q.mu.Unlock()

	q.log("reconcile queue enqueued",
		"queue", q.name,
		"key", key,
		"priority", priority.String(),
		"reason", reason,
		"pending", pendingLen,
	)

	return reconcileQueueSubmission[T]{
		Key:  key,
		done: done,
	}, nil
}

func (q *reconcileQueue[T]) snapshotCounters() reconcileQueueCounterSnapshot {
	if q == nil {
		return reconcileQueueCounterSnapshot{}
	}
	return reconcileQueueCounterSnapshot{
		Enqueued:      q.counters.enqueued.Load(),
		Dequeued:      q.counters.dequeued.Load(),
		Deduped:       q.counters.deduped.Load(),
		Reprioritized: q.counters.reprioritized.Load(),
	}
}

func (q *reconcileQueue[T]) snapshot() reconcileQueueSnapshot {
	if q == nil {
		return reconcileQueueSnapshot{}
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	snap := reconcileQueueSnapshot{
		Pending:  make([]string, 0, len(q.pending)),
		Running:  make([]string, 0, len(q.jobs)),
		Counters: q.snapshotCounters(),
	}
	for _, job := range q.pending {
		snap.Pending = append(snap.Pending, job.key)
	}
	for key, job := range q.jobs {
		if job.state == reconcileQueueJobRunning {
			snap.Running = append(snap.Running, key)
		}
	}
	sort.Strings(snap.Running)
	return snap
}

func (q *reconcileQueue[T]) HasJob(key string) bool {
	if q == nil {
		return false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.jobs[key]
	return ok
}

func (q *reconcileQueue[T]) worker(workerID int) {
	defer q.wg.Done()
	for {
		job, pendingLen, ok := q.dequeue()
		if !ok {
			return
		}
		q.log("reconcile queue dequeued",
			"queue", q.name,
			"worker", workerID,
			"key", job.key,
			"priority", job.priority.String(),
			"reason", job.reason,
			"pending", pendingLen,
		)

		execCtx := job.execContext
		if execCtx == nil {
			execCtx = context.Background()
		}
		ctx, cancel := context.WithCancel(execCtx)
		stop := context.AfterFunc(q.ctx, cancel)
		startedAt := q.now().UTC()
		value, err := job.work(ctx)
		finishedAt := q.now().UTC()
		stop()
		cancel()

		q.finish(job, reconcileQueueResult[T]{
			Key:        job.key,
			Value:      value,
			Err:        err,
			StartedAt:  startedAt,
			FinishedAt: finishedAt,
		})
	}
}

func (q *reconcileQueue[T]) dequeue() (*reconcileQueueJob[T], int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for !q.closed && len(q.pending) == 0 {
		q.cond.Wait()
	}
	if q.closed {
		return nil, 0, false
	}

	job := q.pending[0]
	q.pending = q.pending[1:]
	job.state = reconcileQueueJobRunning
	q.counters.dequeued.Add(1)
	return job, len(q.pending), true
}

func (q *reconcileQueue[T]) finish(job *reconcileQueueJob[T], result reconcileQueueResult[T]) {
	q.mu.Lock()
	if current, ok := q.jobs[job.key]; ok && current == job {
		delete(q.jobs, job.key)
	}
	waiters := append([]chan reconcileQueueResult[T](nil), job.waiters...)
	q.mu.Unlock()

	for _, waiter := range waiters {
		waiter <- result
		close(waiter)
	}
}

func (q *reconcileQueue[T]) sortPendingLocked() {
	sort.SliceStable(q.pending, func(i, j int) bool {
		if q.pending[i].priority != q.pending[j].priority {
			return q.pending[i].priority > q.pending[j].priority
		}
		return q.pending[i].sequence < q.pending[j].sequence
	})
}

func (q *reconcileQueue[T]) log(msg string, attrs ...any) {
	if q == nil || q.logger == nil {
		return
	}
	q.logger.Debug(msg, attrs...)
}

type reconcileThrottleAction string

const (
	reconcileThrottleProcess reconcileThrottleAction = "process"
	reconcileThrottleSkip    reconcileThrottleAction = "skip"
	reconcileThrottleDefer   reconcileThrottleAction = "defer"
)

type reconcileThrottleDecision struct {
	Action         reconcileThrottleAction
	Reason         string
	Until          time.Time
	ConsumedBudget bool
	WindowStarted  time.Time
}

func (d reconcileThrottleDecision) Allowed() bool {
	return d.Action == reconcileThrottleProcess
}

type reconcileThrottleOutcome string

const (
	reconcileThrottleOutcomeChanged   reconcileThrottleOutcome = "changed"
	reconcileThrottleOutcomeUnchanged reconcileThrottleOutcome = "unchanged"
	reconcileThrottleOutcomeFailed    reconcileThrottleOutcome = "failed"
)

type reconcileThrottleCounterSnapshot struct {
	Processed uint64
	Skipped   uint64
	Deferred  uint64
}

type reconcileThrottleCounters struct {
	processed atomic.Uint64
	skipped   atomic.Uint64
	deferred  atomic.Uint64
}

type reconcileThrottleConfig struct {
	Name                 string
	Budget               int
	Cadence              time.Duration
	UnchangedBackoffBase time.Duration
	UnchangedBackoffMax  time.Duration
	FailureBackoffBase   time.Duration
	FailureBackoffMax    time.Duration
	Logger               *slog.Logger
	Now                  func() time.Time
}

type reconcileThrottleState struct {
	nextAllowedAt   time.Time
	lastSignature   string
	unchangedStreak int
	failureStreak   int
}

type reconcileThrottle struct {
	name                 string
	logger               *slog.Logger
	now                  func() time.Time
	budget               int
	cadence              time.Duration
	unchangedBackoffBase time.Duration
	unchangedBackoffMax  time.Duration
	failureBackoffBase   time.Duration
	failureBackoffMax    time.Duration

	mu            sync.Mutex
	windowStarted time.Time
	windowUsed    int
	targets       map[string]reconcileThrottleState
	counters      reconcileThrottleCounters
}

func newReconcileThrottle(cfg reconcileThrottleConfig) *reconcileThrottle {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = "reconcile"
	}
	cadence := cfg.Cadence
	if cadence < 0 {
		cadence = 0
	}
	return &reconcileThrottle{
		name:                 name,
		logger:               cfg.Logger,
		now:                  now,
		budget:               cfg.Budget,
		cadence:              cadence,
		unchangedBackoffBase: cfg.UnchangedBackoffBase,
		unchangedBackoffMax:  cfg.UnchangedBackoffMax,
		failureBackoffBase:   cfg.FailureBackoffBase,
		failureBackoffMax:    cfg.FailureBackoffMax,
		targets:              make(map[string]reconcileThrottleState),
	}
}

func (t *reconcileThrottle) Admit(key string, force bool) reconcileThrottleDecision {
	if t == nil {
		return reconcileThrottleDecision{Action: reconcileThrottleProcess}
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return reconcileThrottleDecision{Action: reconcileThrottleProcess}
	}

	now := t.now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.targets[key]
	if !force && !state.nextAllowedAt.IsZero() && now.Before(state.nextAllowedAt) {
		t.counters.skipped.Add(1)
		decision := reconcileThrottleDecision{
			Action: reconcileThrottleSkip,
			Reason: "backoff_active",
			Until:  state.nextAllowedAt,
		}
		t.log("reconcile throttle skipped",
			"throttle", t.name,
			"key", key,
			"reason", decision.Reason,
			"until", decision.Until,
		)
		return decision
	}

	if !force && t.budget > 0 {
		if t.windowStarted.IsZero() || (t.cadence > 0 && now.Sub(t.windowStarted) >= t.cadence) {
			t.windowStarted = now
			t.windowUsed = 0
		}
		if t.windowUsed >= t.budget {
			until := t.windowStarted
			if t.cadence > 0 {
				until = t.windowStarted.Add(t.cadence)
			}
			if state.nextAllowedAt.Before(until) {
				state.nextAllowedAt = until
				t.targets[key] = state
			}
			t.counters.deferred.Add(1)
			decision := reconcileThrottleDecision{
				Action:        reconcileThrottleDefer,
				Reason:        "budget_exhausted",
				Until:         until,
				WindowStarted: t.windowStarted,
			}
			t.log("reconcile throttle deferred",
				"throttle", t.name,
				"key", key,
				"reason", decision.Reason,
				"until", decision.Until,
				"budget", t.budget,
			)
			return decision
		}
		t.windowUsed++
		return reconcileThrottleDecision{
			Action:         reconcileThrottleProcess,
			ConsumedBudget: true,
			WindowStarted:  t.windowStarted,
		}
	}

	return reconcileThrottleDecision{Action: reconcileThrottleProcess}
}

func (t *reconcileThrottle) Refund(decision reconcileThrottleDecision) {
	if t == nil || !decision.ConsumedBudget {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.windowStarted.IsZero() && t.windowStarted.Equal(decision.WindowStarted) && t.windowUsed > 0 {
		t.windowUsed--
	}
}

func (t *reconcileThrottle) Record(key, signature string, err error) reconcileThrottleOutcome {
	if t == nil {
		if err != nil {
			return reconcileThrottleOutcomeFailed
		}
		return reconcileThrottleOutcomeChanged
	}
	key = strings.TrimSpace(key)
	if key == "" {
		if err != nil {
			return reconcileThrottleOutcomeFailed
		}
		return reconcileThrottleOutcomeChanged
	}

	now := t.now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.targets[key]
	waitFor := t.cadence
	outcome := reconcileThrottleOutcomeChanged

	switch {
	case err != nil:
		outcome = reconcileThrottleOutcomeFailed
		state.failureStreak++
		state.unchangedStreak = 0
		waitFor = maxDuration(waitFor, exponentialBackoff(t.failureBackoffBase, t.failureBackoffMax, state.failureStreak))
	case signature != "" && signature == state.lastSignature:
		outcome = reconcileThrottleOutcomeUnchanged
		state.unchangedStreak++
		state.failureStreak = 0
		waitFor = maxDuration(waitFor, exponentialBackoff(t.unchangedBackoffBase, t.unchangedBackoffMax, state.unchangedStreak))
	default:
		state.unchangedStreak = 0
		state.failureStreak = 0
	}

	if err == nil {
		state.lastSignature = signature
	}
	if waitFor > 0 {
		state.nextAllowedAt = now.Add(waitFor)
	} else {
		state.nextAllowedAt = time.Time{}
	}
	t.targets[key] = state
	t.counters.processed.Add(1)

	t.log("reconcile throttle recorded",
		"throttle", t.name,
		"key", key,
		"outcome", string(outcome),
		"next_allowed_at", state.nextAllowedAt,
		"unchanged_streak", state.unchangedStreak,
		"failure_streak", state.failureStreak,
	)

	return outcome
}

func (t *reconcileThrottle) snapshotCounters() reconcileThrottleCounterSnapshot {
	if t == nil {
		return reconcileThrottleCounterSnapshot{}
	}
	return reconcileThrottleCounterSnapshot{
		Processed: t.counters.processed.Load(),
		Skipped:   t.counters.skipped.Load(),
		Deferred:  t.counters.deferred.Load(),
	}
}

func (t *reconcileThrottle) log(msg string, attrs ...any) {
	if t == nil || t.logger == nil {
		return
	}
	t.logger.Debug(msg, attrs...)
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func exponentialBackoff(base, max time.Duration, streak int) time.Duration {
	if base <= 0 || streak <= 0 {
		return 0
	}
	delay := base
	for i := 1; i < streak; i++ {
		if max > 0 && delay >= max {
			return max
		}
		delay *= 2
		if max > 0 && delay >= max {
			return max
		}
	}
	return delay
}
