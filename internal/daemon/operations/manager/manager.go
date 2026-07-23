package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
)

type Config struct {
	Now                    func() time.Time
	NewID                  func() string
	BaseContext            context.Context
	Logger                 *slog.Logger
	LifecycleWriteTimeout  time.Duration
	LifecycleRetryInterval time.Duration
	LifecycleRetryWait     func(context.Context) error
}

const terminalPersistenceMaxAttempts = 2

type Manager struct {
	store                 daemonops.Store
	now                   func() time.Time
	newID                 func() string
	base                  context.Context
	log                   *slog.Logger
	lifecycleWriteTimeout time.Duration
	lifecycleRetryWait    func(context.Context) error

	mu           sync.Mutex
	intakeClosed bool
	pending      []*managedOp
	running      map[string]*managedOp
	resourceBusy map[string]string
	activeDedupe map[string]string
	recentDedupe map[string]recentEntry
	lifecycleErr error
	wg           sync.WaitGroup
}

type managedOp struct {
	record       daemonops.Record
	runner       daemonops.Runner
	cancel       context.CancelFunc
	cancelMu     sync.Mutex
	cancelReason string
	recentWindow time.Duration
}

type recentEntry struct {
	operationID string
	expiresAt   time.Time
}

func New(store daemonops.Store, cfg Config) *Manager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = func() string { return cfg.Now().UTC().Format("20060102150405.000000000") }
	}
	if cfg.BaseContext == nil {
		cfg.BaseContext = context.Background()
	}
	if cfg.LifecycleWriteTimeout <= 0 {
		cfg.LifecycleWriteTimeout = 2 * time.Second
	}
	if cfg.LifecycleRetryInterval <= 0 {
		cfg.LifecycleRetryInterval = 100 * time.Millisecond
	}
	if cfg.LifecycleRetryWait == nil {
		retryInterval := cfg.LifecycleRetryInterval
		cfg.LifecycleRetryWait = func(ctx context.Context) error {
			timer := time.NewTimer(retryInterval)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	return &Manager{
		store:                 store,
		now:                   cfg.Now,
		newID:                 cfg.NewID,
		base:                  cfg.BaseContext,
		log:                   cfg.Logger,
		lifecycleWriteTimeout: cfg.LifecycleWriteTimeout,
		lifecycleRetryWait:    cfg.LifecycleRetryWait,
		running:               make(map[string]*managedOp),
		resourceBusy:          make(map[string]string),
		activeDedupe:          make(map[string]string),
		recentDedupe:          make(map[string]recentEntry),
	}
}

func (m *Manager) Submit(ctx context.Context, req daemonops.SubmitRequest, runner daemonops.Runner) (daemonops.SubmitResult, error) {
	if runner == nil {
		return daemonops.SubmitResult{}, daemonops.ErrInvalidOperation
	}
	normalized, err := m.normalizeRequest(req)
	if err != nil {
		return daemonops.SubmitResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.intakeClosed {
		return daemonops.SubmitResult{}, daemonops.ErrIntakeClosed
	}

	if existing, ok, err := m.lookupDedupeLocked(ctx, normalized); err != nil {
		return daemonops.SubmitResult{}, err
	} else if ok {
		m.logOperationDedupedLocked(existing)
		return daemonops.SubmitResult{Record: existing, Deduped: true}, nil
	}

	now := m.now().UTC()
	record := daemonops.Record{
		ID:           normalized.ID,
		ProjectID:    normalized.ProjectID,
		IssueID:      normalized.IssueID,
		Kind:         normalized.Kind,
		DedupeKey:    normalized.DedupeKey,
		ResourceKeys: append([]string(nil), normalized.ResourceKeys...),
		State:        daemonops.StateQueued,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	created, err := m.create(ctx, record)
	if err != nil {
		return daemonops.SubmitResult{}, err
	}

	op := &managedOp{
		record:       created,
		runner:       runner,
		recentWindow: normalized.RecentDedupeWindow,
	}
	m.pending = append(m.pending, op)
	if created.DedupeKey != "" {
		m.activeDedupe[dedupeMapKey(created.ProjectID, created.DedupeKey)] = created.ID
	}
	m.logOperationQueuedLocked(op)
	m.startReadyLocked()
	return daemonops.SubmitResult{Record: created}, nil
}

func (m *Manager) Get(ctx context.Context, operationID string) (daemonops.Record, error) {
	return m.store.Get(ctx, operationID)
}

func (m *Manager) List(ctx context.Context, query daemonops.Query) ([]daemonops.Record, error) {
	return m.store.List(ctx, query)
}

func (m *Manager) Queue(query daemonops.Query) daemonops.QueueSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot := daemonops.QueueSnapshot{ProjectID: query.ProjectID}
	remaining := query.Limit
	appendIfAllowed := func(entry daemonops.QueueEntry, target *[]daemonops.QueueEntry) {
		if query.Limit > 0 && remaining == 0 {
			return
		}
		if !matchesQueueQuery(entry.Record, query) {
			return
		}
		*target = append(*target, entry)
		if query.Limit > 0 {
			remaining--
		}
	}

	running := make([]*managedOp, 0, len(m.running))
	for _, op := range m.running {
		running = append(running, op)
	}
	sort.SliceStable(running, func(i, j int) bool {
		left := running[i].record
		right := running[j].record
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.ID < right.ID
	})
	for _, op := range running {
		record := cloneOperationRecord(op.record)
		record.State = daemonops.StateRunning
		appendIfAllowed(daemonops.QueueEntry{Record: record}, &snapshot.Running)
	}

	for idx, op := range m.pending {
		blocked := m.blockedResourcesLocked(op.record.ResourceKeys)
		record := cloneOperationRecord(op.record)
		record.State = daemonops.StateQueued
		appendIfAllowed(daemonops.QueueEntry{
			Record:               record,
			QueueIndex:           idx + 1,
			BlockingOperationIDs: sortedMapValues(blocked),
			BlockedResourceKeys:  sortedMapKeys(blocked),
		}, &snapshot.Queued)
	}
	return snapshot
}

func (m *Manager) Cancel(ctx context.Context, operationID, reason string) (daemonops.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if idx := m.indexPendingLocked(operationID); idx >= 0 {
		op := m.pending[idx]
		finished := m.now().UTC()
		msg := reason
		if msg == "" {
			msg = "cancelled"
		}
		updated, err := m.update(ctx, daemonops.UpdateParams{
			ID:           operationID,
			ToState:      daemonops.StateCancelled,
			FinishedAt:   &finished,
			ErrorMessage: &msg,
		})
		if err != nil {
			return daemonops.Record{}, err
		}
		m.pending = append(m.pending[:idx], m.pending[idx+1:]...)
		op.record = updated
		m.clearDedupeLocked(op)
		return updated, nil
	}

	op, ok := m.running[operationID]
	if !ok {
		return daemonops.Record{}, daemonops.ErrNotFound
	}
	op.cancelMu.Lock()
	op.cancelReason = reason
	cancel := op.cancel
	op.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return cloneOperationRecord(op.record), nil
}

func (m *Manager) StopIntake() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.intakeClosed = true
	return nil
}

func (m *Manager) CancelQueued(ctx context.Context, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := append([]*managedOp(nil), m.pending...)

	var firstErr error
	for _, op := range pending {
		record := cloneOperationRecord(op.record)
		finished := m.now().UTC()
		msg := reason
		if msg == "" {
			msg = "cancelled"
		}
		updated, err := m.update(ctx, daemonops.UpdateParams{
			ID:           record.ID,
			ToState:      daemonops.StateCancelled,
			FinishedAt:   &finished,
			ErrorMessage: &msg,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		op.record = cloneOperationRecord(updated)
		if idx := m.indexPendingLocked(record.ID); idx >= 0 {
			m.pending = append(m.pending[:idx], m.pending[idx+1:]...)
		}
		m.clearDedupeLocked(op)
	}
	return firstErr
}

func (m *Manager) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.wg.Wait()
	}()
	select {
	case <-done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.lifecycleErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) normalizeRequest(req daemonops.SubmitRequest) (daemonops.SubmitRequest, error) {
	if req.Kind == "" {
		return daemonops.SubmitRequest{}, daemonops.ErrInvalidOperation
	}
	if req.ID == "" {
		req.ID = m.newID()
	}
	req.ResourceKeys = normalizeResourceKeys(req.ResourceKeys)
	return req, nil
}

func normalizeResourceKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	uniq := make(map[string]struct{}, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := uniq[key]; ok {
			continue
		}
		uniq[key] = struct{}{}
		normalized = append(normalized, key)
	}
	sort.Strings(normalized)
	return normalized
}

func cloneOperationRecord(record daemonops.Record) daemonops.Record {
	record.ResourceKeys = append([]string(nil), record.ResourceKeys...)
	record.ResultPayload = append([]byte(nil), record.ResultPayload...)
	if record.Progress != nil {
		progress := *record.Progress
		record.Progress = &progress
	}
	return record
}

func (m *Manager) recordForOp(op *managedOp) daemonops.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneOperationRecord(op.record)
}

func (m *Manager) setRecordForOp(op *managedOp, record daemonops.Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op.record = cloneOperationRecord(record)
}

func matchesQueueQuery(record daemonops.Record, query daemonops.Query) bool {
	if query.ProjectID != "" && record.ProjectID != query.ProjectID {
		return false
	}
	if query.IssueID != "" && record.IssueID != query.IssueID {
		return false
	}
	if query.Kind != "" && record.Kind != query.Kind {
		return false
	}
	if len(query.States) > 0 {
		for _, state := range query.States {
			if record.State == state {
				return true
			}
		}
		return false
	}
	return true
}

func (m *Manager) lookupDedupeLocked(ctx context.Context, req daemonops.SubmitRequest) (daemonops.Record, bool, error) {
	if req.DedupeKey == "" {
		return daemonops.Record{}, false, nil
	}
	mapKey := dedupeMapKey(req.ProjectID, req.DedupeKey)
	if operationID, ok := m.activeDedupe[mapKey]; ok {
		record, err := m.store.Get(ctx, operationID)
		return record, err == nil, err
	}
	if recent, ok := m.recentDedupe[mapKey]; ok {
		if m.now().UTC().After(recent.expiresAt) {
			delete(m.recentDedupe, mapKey)
			return daemonops.Record{}, false, nil
		}
		record, err := m.store.Get(ctx, recent.operationID)
		return record, err == nil, err
	}
	return daemonops.Record{}, false, nil
}

func (m *Manager) startReadyLocked() {
	for i := 0; i < len(m.pending); {
		op := m.pending[i]
		if blocked := m.blockedResourcesLocked(op.record.ResourceKeys); len(blocked) > 0 {
			m.logOperationBlockedLocked(op, blocked)
			i++
			continue
		}
		m.pending = append(m.pending[:i], m.pending[i+1:]...)
		m.startLocked(op)
	}
}

func (m *Manager) canRunLocked(resourceKeys []string) bool {
	return len(m.blockedResourcesLocked(resourceKeys)) == 0
}

func (m *Manager) blockedResourcesLocked(resourceKeys []string) map[string]string {
	blocked := make(map[string]string)
	for _, key := range resourceKeys {
		if owner, busy := m.resourceBusy[key]; busy {
			blocked[key] = owner
		}
	}
	return blocked
}

func (m *Manager) startLocked(op *managedOp) {
	ctx, cancel := context.WithCancel(m.base)
	op.cancel = cancel
	m.running[op.record.ID] = op
	for _, key := range op.record.ResourceKeys {
		m.resourceBusy[key] = op.record.ID
	}
	m.logOperationStartLocked(op)
	m.wg.Add(1)
	go m.execute(ctx, op)
}

func (m *Manager) execute(ctx context.Context, op *managedOp) {
	defer m.wg.Done()

	operationID := m.recordForOp(op).ID
	started := m.now().UTC()
	updated, err := m.update(ctx, daemonops.UpdateParams{
		ID:        operationID,
		ToState:   daemonops.StateRunning,
		StartedAt: &started,
	})
	if err != nil {
		msg := err.Error()
		finished := m.now().UTC()
		updated, terminalErr := m.persistTerminal(daemonops.UpdateParams{
			ID:           operationID,
			ToState:      daemonops.StateFailed,
			FinishedAt:   &finished,
			ErrorMessage: &msg,
		})
		if terminalErr != nil {
			m.logLifecyclePersistenceFailure(operationID, daemonops.StateFailed, terminalErr)
			m.recordLifecyclePersistenceFailure(terminalErr)
			return
		}
		if updated.ID != "" {
			m.setRecordForOp(op, updated)
		}
		m.finish(op)
		return
	}
	m.setRecordForOp(op, updated)
	ctx = daemonops.WithProgressReporter(ctx, func(progressCtx context.Context, progress daemonops.Progress) error {
		progressCopy := progress
		record := m.recordForOp(op)
		updated, err := m.update(progressCtx, daemonops.UpdateParams{
			ID:       record.ID,
			ToState:  record.State,
			Progress: &progressCopy,
		})
		if err == nil {
			m.setRecordForOp(op, updated)
		}
		return err
	})

	var (
		payload []byte
		runErr  error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("operation %s panicked: %v\n%s", operationID, r, debug.Stack())
			}
		}()
		payload, runErr = op.runner(ctx)
	}()
	finished := m.now().UTC()
	params := daemonops.UpdateParams{
		ID:            operationID,
		FinishedAt:    &finished,
		ResultPayload: payload,
	}

	switch {
	case runErr == nil:
		params.ToState = daemonops.StateDone
	case errors.Is(runErr, context.Canceled):
		params.ToState = daemonops.StateCancelled
		op.cancelMu.Lock()
		reason := op.cancelReason
		op.cancelMu.Unlock()
		if reason == "" {
			reason = runErr.Error()
		}
		params.ErrorMessage = &reason
	default:
		params.ToState = daemonops.StateFailed
		msg := runErr.Error()
		params.ErrorMessage = &msg
	}

	updated, err = m.persistTerminal(params)
	if err != nil {
		m.logLifecyclePersistenceFailure(operationID, params.ToState, err)
		m.recordLifecyclePersistenceFailure(err)
		return
	}
	m.setRecordForOp(op, updated)
	m.logOperationFinished(m.recordForOp(op), finished.Sub(started))
	m.finish(op)
}

func (m *Manager) create(ctx context.Context, record daemonops.Record) (daemonops.Record, error) {
	writeCtx, cancel := context.WithTimeout(ctx, m.lifecycleWriteTimeout)
	defer cancel()
	return m.store.Create(writeCtx, record)
}

func (m *Manager) update(ctx context.Context, params daemonops.UpdateParams) (daemonops.Record, error) {
	writeCtx, cancel := context.WithTimeout(ctx, m.lifecycleWriteTimeout)
	defer cancel()
	return m.store.Update(writeCtx, params)
}

func (m *Manager) persistTerminal(params daemonops.UpdateParams) (daemonops.Record, error) {
	var (
		lastErr  error
		attempts int
	)
	for attempt := 1; attempt <= terminalPersistenceMaxAttempts; attempt++ {
		attempts = attempt
		updated, err := m.update(context.WithoutCancel(m.base), params)
		if err == nil {
			return updated, nil
		}
		lastErr = err
		if attempt == terminalPersistenceMaxAttempts {
			break
		}
		// Waiting only spaces the bounded attempts. Terminal persistence uses a
		// detached write context so manager shutdown or a wait-hook failure
		// cannot suppress the final recovery attempt.
		_ = m.lifecycleRetryWait(m.base)
	}
	return daemonops.Record{}, &terminalPersistenceError{
		operationID: params.ID,
		state:       params.ToState,
		attempts:    attempts,
		err:         lastErr,
	}
}

type terminalPersistenceError struct {
	operationID string
	state       daemonops.State
	attempts    int
	err         error
}

func (e *terminalPersistenceError) Error() string {
	return fmt.Sprintf("persist terminal operation %s as %s after %d attempts: %v", e.operationID, e.state, e.attempts, e.err)
}

func (e *terminalPersistenceError) Unwrap() error {
	return e.err
}

func (m *Manager) logLifecyclePersistenceFailure(operationID string, state daemonops.State, err error) {
	if m.log != nil {
		attempts := terminalPersistenceMaxAttempts
		var persistenceErr *terminalPersistenceError
		if errors.As(err, &persistenceErr) {
			attempts = persistenceErr.attempts
		}
		m.log.Error("daemon operation lifecycle persistence failed; authority retained",
			"operation_id", operationID,
			"state", state,
			"attempts", attempts,
			"retry_disposition", "retained_for_interrupted_recovery",
			"error", err,
		)
	}
}

func (m *Manager) recordLifecyclePersistenceFailure(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lifecycleErr = errors.Join(m.lifecycleErr, err)
}

func (m *Manager) finish(op *managedOp) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.running, op.record.ID)
	for _, key := range op.record.ResourceKeys {
		if owner, ok := m.resourceBusy[key]; ok && owner == op.record.ID {
			delete(m.resourceBusy, key)
		}
	}
	m.clearDedupeLocked(op)
	if op.record.State == daemonops.StateDone && op.record.DedupeKey != "" && op.recentWindow > 0 {
		m.recentDedupe[dedupeMapKey(op.record.ProjectID, op.record.DedupeKey)] = recentEntry{
			operationID: op.record.ID,
			expiresAt:   m.now().UTC().Add(op.recentWindow),
		}
	}
	m.startReadyLocked()
}

func (m *Manager) logOperationQueuedLocked(op *managedOp) {
	if m.log == nil {
		return
	}
	blocked := m.blockedResourcesLocked(op.record.ResourceKeys)
	attrs := m.operationAttrsLocked(op,
		"pending_count", len(m.pending),
		"running_count", len(m.running),
		"blocked", len(blocked) > 0,
	)
	if len(blocked) > 0 {
		attrs = append(attrs, "blocked_resources", sortedMapKeys(blocked), "blocking_operations", sortedMapValues(blocked))
	}
	m.log.Info("daemon operation queued", attrs...)
}

func (m *Manager) logOperationDedupedLocked(record daemonops.Record) {
	if m.log == nil {
		return
	}
	m.log.Info("daemon operation deduped",
		"operation_id", record.ID,
		"project_id", record.ProjectID,
		"issue_id", record.IssueID,
		"kind", record.Kind,
		"state", record.State,
		"dedupe_key", record.DedupeKey,
		"resource_keys", append([]string(nil), record.ResourceKeys...),
		"pending_count", len(m.pending),
		"running_count", len(m.running),
	)
}

func (m *Manager) logOperationBlockedLocked(op *managedOp, blocked map[string]string) {
	if m.log == nil || len(blocked) == 0 {
		return
	}
	m.log.Info("daemon operation waiting for busy resources",
		append(m.operationAttrsLocked(op,
			"pending_count", len(m.pending),
			"running_count", len(m.running),
			"queue_wait_ms", durationMillis(m.now().UTC().Sub(op.record.CreatedAt)),
		), "blocked_resources", sortedMapKeys(blocked), "blocking_operations", sortedMapValues(blocked))...,
	)
}

func (m *Manager) logOperationStartLocked(op *managedOp) {
	if m.log == nil {
		return
	}
	m.log.Info("daemon operation started",
		m.operationAttrsLocked(op,
			"pending_count", len(m.pending),
			"running_count", len(m.running),
			"queue_wait_ms", durationMillis(m.now().UTC().Sub(op.record.CreatedAt)),
		)...,
	)
}

func (m *Manager) logOperationFinished(record daemonops.Record, runDuration time.Duration) {
	if m.log == nil {
		return
	}
	m.log.Info("daemon operation finished",
		"operation_id", record.ID,
		"project_id", record.ProjectID,
		"issue_id", record.IssueID,
		"kind", record.Kind,
		"state", record.State,
		"resource_keys", append([]string(nil), record.ResourceKeys...),
		"run_ms", durationMillis(runDuration),
		"total_ms", durationMillis(m.now().UTC().Sub(record.CreatedAt)),
	)
}

func (m *Manager) operationAttrsLocked(op *managedOp, attrs ...any) []any {
	base := []any{
		"operation_id", op.record.ID,
		"project_id", op.record.ProjectID,
		"issue_id", op.record.IssueID,
		"kind", op.record.Kind,
		"state", op.record.State,
		"resource_keys", append([]string(nil), op.record.ResourceKeys...),
	}
	return append(base, attrs...)
}

func durationMillis(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapValues(values map[string]string) []string {
	keys := sortedMapKeys(values)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

func (m *Manager) clearDedupeLocked(op *managedOp) {
	if op.record.DedupeKey == "" {
		return
	}
	mapKey := dedupeMapKey(op.record.ProjectID, op.record.DedupeKey)
	if operationID, ok := m.activeDedupe[mapKey]; ok && operationID == op.record.ID {
		delete(m.activeDedupe, mapKey)
	}
}

func (m *Manager) indexPendingLocked(operationID string) int {
	for i, op := range m.pending {
		if op.record.ID == operationID {
			return i
		}
	}
	return -1
}

func dedupeMapKey(projectID, dedupeKey string) string {
	return projectID + "::" + dedupeKey
}
