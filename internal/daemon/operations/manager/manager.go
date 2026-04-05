package manager

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
)

type Config struct {
	Now         func() time.Time
	NewID       func() string
	BaseContext context.Context
}

type Manager struct {
	store daemonops.Store
	now   func() time.Time
	newID func() string
	base  context.Context

	mu           sync.Mutex
	intakeClosed bool
	pending      []*managedOp
	running      map[string]*managedOp
	resourceBusy map[string]string
	activeDedupe map[string]string
	recentDedupe map[string]recentEntry
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
	return &Manager{
		store:        store,
		now:          cfg.Now,
		newID:        cfg.NewID,
		base:         cfg.BaseContext,
		running:      make(map[string]*managedOp),
		resourceBusy: make(map[string]string),
		activeDedupe: make(map[string]string),
		recentDedupe: make(map[string]recentEntry),
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
	created, err := m.store.Create(ctx, record)
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
	m.startReadyLocked()
	return daemonops.SubmitResult{Record: created}, nil
}

func (m *Manager) Get(ctx context.Context, operationID string) (daemonops.Record, error) {
	return m.store.Get(ctx, operationID)
}

func (m *Manager) List(ctx context.Context, query daemonops.Query) ([]daemonops.Record, error) {
	return m.store.List(ctx, query)
}

func (m *Manager) Cancel(ctx context.Context, operationID, reason string) (daemonops.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if idx := m.indexPendingLocked(operationID); idx >= 0 {
		op := m.pending[idx]
		m.pending = append(m.pending[:idx], m.pending[idx+1:]...)
		finished := m.now().UTC()
		msg := reason
		if msg == "" {
			msg = "cancelled"
		}
		updated, err := m.store.Update(ctx, daemonops.UpdateParams{
			ID:           operationID,
			ToState:      daemonops.StateCancelled,
			FinishedAt:   &finished,
			ErrorMessage: &msg,
		})
		if err != nil {
			return daemonops.Record{}, err
		}
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
	return op.record, nil
}

func (m *Manager) StopIntake() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.intakeClosed = true
	return nil
}

func (m *Manager) CancelQueued(ctx context.Context, reason string) error {
	m.mu.Lock()
	pending := append([]*managedOp(nil), m.pending...)
	m.pending = nil
	m.mu.Unlock()

	var firstErr error
	for _, op := range pending {
		finished := m.now().UTC()
		msg := reason
		if msg == "" {
			msg = "cancelled"
		}
		updated, err := m.store.Update(ctx, daemonops.UpdateParams{
			ID:           op.record.ID,
			ToState:      daemonops.StateCancelled,
			FinishedAt:   &finished,
			ErrorMessage: &msg,
		})
		if err != nil && firstErr == nil {
			firstErr = err
			continue
		}
		op.record = updated
		m.mu.Lock()
		m.clearDedupeLocked(op)
		m.mu.Unlock()
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
		return nil
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
		if !m.canRunLocked(op.record.ResourceKeys) {
			i++
			continue
		}
		m.pending = append(m.pending[:i], m.pending[i+1:]...)
		m.startLocked(op)
	}
}

func (m *Manager) canRunLocked(resourceKeys []string) bool {
	for _, key := range resourceKeys {
		if _, busy := m.resourceBusy[key]; busy {
			return false
		}
	}
	return true
}

func (m *Manager) startLocked(op *managedOp) {
	ctx, cancel := context.WithCancel(m.base)
	op.cancel = cancel
	m.running[op.record.ID] = op
	for _, key := range op.record.ResourceKeys {
		m.resourceBusy[key] = op.record.ID
	}
	m.wg.Add(1)
	go m.execute(ctx, op)
}

func (m *Manager) execute(ctx context.Context, op *managedOp) {
	defer m.wg.Done()

	started := m.now().UTC()
	updated, err := m.store.Update(context.Background(), daemonops.UpdateParams{
		ID:        op.record.ID,
		ToState:   daemonops.StateRunning,
		StartedAt: &started,
	})
	if err != nil {
		msg := err.Error()
		finished := m.now().UTC()
		updated, _ = m.store.Update(context.Background(), daemonops.UpdateParams{
			ID:           op.record.ID,
			ToState:      daemonops.StateFailed,
			FinishedAt:   &finished,
			ErrorMessage: &msg,
		})
		op.record = updated
		m.finish(op)
		return
	}
	op.record = updated

	var (
		payload []byte
		runErr  error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("operation %s panicked: %v\n%s", op.record.ID, r, debug.Stack())
			}
		}()
		payload, runErr = op.runner(ctx)
	}()
	finished := m.now().UTC()
	params := daemonops.UpdateParams{
		ID:            op.record.ID,
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

	updated, err = m.store.Update(context.Background(), params)
	if err == nil {
		op.record = updated
	}
	m.finish(op)
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
	if op.record.DedupeKey != "" && op.recentWindow > 0 {
		m.recentDedupe[dedupeMapKey(op.record.ProjectID, op.record.DedupeKey)] = recentEntry{
			operationID: op.record.ID,
			expiresAt:   m.now().UTC().Add(op.recentWindow),
		}
	}
	m.startReadyLocked()
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
