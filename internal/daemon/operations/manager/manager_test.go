package manager

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
)

type memoryStore struct {
	mu      sync.Mutex
	records map[string]daemonops.Record
}

type toggleUpdateErrorStore struct {
	*memoryStore
	mu        sync.Mutex
	updateErr error
}

type blockingProgressStore struct {
	*memoryStore
	deadlineObserved chan struct{}
}

type controlledTerminalStore struct {
	*memoryStore
	attempted chan struct{}
	allow     chan struct{}
}

func (s *controlledTerminalStore) Update(ctx context.Context, params daemonops.UpdateParams) (daemonops.Record, error) {
	if params.FinishedAt != nil {
		select {
		case s.attempted <- struct{}{}:
		default:
		}
		select {
		case <-s.allow:
			return s.memoryStore.Update(ctx, params)
		case <-ctx.Done():
			return daemonops.Record{}, ctx.Err()
		}
	}
	return s.memoryStore.Update(ctx, params)
}

type blockingStartStore struct {
	*memoryStore
	deadlineObserved chan struct{}
}

func (s *blockingStartStore) Update(ctx context.Context, params daemonops.UpdateParams) (daemonops.Record, error) {
	if params.StartedAt != nil {
		<-ctx.Done()
		close(s.deadlineObserved)
		return daemonops.Record{}, ctx.Err()
	}
	return s.memoryStore.Update(ctx, params)
}

func (s *blockingProgressStore) Update(ctx context.Context, params daemonops.UpdateParams) (daemonops.Record, error) {
	if params.Progress != nil {
		<-ctx.Done()
		close(s.deadlineObserved)
		return daemonops.Record{}, ctx.Err()
	}
	return s.memoryStore.Update(ctx, params)
}

func (s *toggleUpdateErrorStore) setUpdateError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateErr = err
}

func (s *toggleUpdateErrorStore) Update(ctx context.Context, params daemonops.UpdateParams) (daemonops.Record, error) {
	s.mu.Lock()
	err := s.updateErr
	s.mu.Unlock()
	if err != nil {
		return daemonops.Record{}, err
	}
	return s.memoryStore.Update(ctx, params)
}

func newMemoryStore() *memoryStore {
	return &memoryStore{records: make(map[string]daemonops.Record)}
}

func (s *memoryStore) Create(_ context.Context, record daemonops.Record) (daemonops.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.ID] = cloneRecord(record)
	return cloneRecord(record), nil
}

func (s *memoryStore) Get(_ context.Context, id string) (daemonops.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return daemonops.Record{}, daemonops.ErrNotFound
	}
	return cloneRecord(record), nil
}

func (s *memoryStore) List(_ context.Context, query daemonops.Query) ([]daemonops.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]daemonops.Record, 0, len(s.records))
	for _, record := range s.records {
		if query.ProjectID != "" && record.ProjectID != query.ProjectID {
			continue
		}
		if query.IssueID != "" && record.IssueID != query.IssueID {
			continue
		}
		if query.Kind != "" && record.Kind != query.Kind {
			continue
		}
		if len(query.States) > 0 && !containsState(query.States, record.State) {
			continue
		}
		out = append(out, cloneRecord(record))
	}
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (s *memoryStore) Update(_ context.Context, params daemonops.UpdateParams) (daemonops.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[params.ID]
	if !ok {
		return daemonops.Record{}, daemonops.ErrNotFound
	}
	record.State = params.ToState
	record.UpdatedAt = time.Now().UTC()
	if params.StartedAt != nil {
		started := *params.StartedAt
		record.StartedAt = &started
	}
	if params.FinishedAt != nil {
		finished := *params.FinishedAt
		record.FinishedAt = &finished
	}
	if params.ErrorMessage != nil {
		record.ErrorMessage = *params.ErrorMessage
	}
	if params.ResultPayload != nil {
		record.ResultPayload = append([]byte(nil), params.ResultPayload...)
	}
	if params.Progress != nil {
		progress := *params.Progress
		record.Progress = &progress
	}
	s.records[record.ID] = cloneRecord(record)
	return cloneRecord(record), nil
}

func TestProgressReporterPreservesCallerDeadlineForDurableWrite(t *testing.T) {
	store := &blockingProgressStore{memoryStore: newMemoryStore(), deadlineObserved: make(chan struct{})}
	mgr := New(store, Config{NewID: func() string { return "op-progress-timeout" }})
	result, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{Kind: "bounded-progress"}, func(ctx context.Context) ([]byte, error) {
		progressCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		defer cancel()
		return nil, daemonops.ReportProgress(progressCtx, daemonops.Progress{Phase: "persist"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.deadlineObserved:
	default:
		t.Fatal("durable progress store did not observe caller deadline")
	}
	record, err := store.Get(context.Background(), result.Record.ID)
	if err != nil || record.State != daemonops.StateFailed || !strings.Contains(record.ErrorMessage, context.DeadlineExceeded.Error()) {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestLifecycleStartWriteIsBounded(t *testing.T) {
	store := &blockingStartStore{memoryStore: newMemoryStore(), deadlineObserved: make(chan struct{})}
	mgr := New(store, Config{NewID: func() string { return "op-start-timeout" }, LifecycleWriteTimeout: 25 * time.Millisecond})
	result, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{Kind: "bounded-start"}, func(context.Context) ([]byte, error) {
		t.Fatal("runner started without durable running state")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.deadlineObserved:
	default:
		t.Fatal("start store did not observe lifecycle deadline")
	}
	record, err := store.Get(context.Background(), result.Record.ID)
	if err != nil || record.State != daemonops.StateFailed || !strings.Contains(record.ErrorMessage, context.DeadlineExceeded.Error()) {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestTerminalPersistenceFailureRetainsAuthorityUntilRetrySucceeds(t *testing.T) {
	store := &controlledTerminalStore{memoryStore: newMemoryStore(), attempted: make(chan struct{}, 1), allow: make(chan struct{})}
	ids := []string{"op-first", "op-second"}
	var idMu sync.Mutex
	mgr := New(store, Config{
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			id := ids[0]
			ids = ids[1:]
			return id
		},
		LifecycleWriteTimeout:  25 * time.Millisecond,
		LifecycleRetryInterval: 10 * time.Millisecond,
	})
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{Kind: "first", ResourceKeys: []string{"session:one"}}, func(context.Context) ([]byte, error) {
		return []byte("first"), nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.attempted:
	case <-time.After(time.Second):
		t.Fatal("terminal persistence was not attempted")
	}
	secondStarted := make(chan struct{})
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{Kind: "second", ResourceKeys: []string{"session:one"}}, func(context.Context) ([]byte, error) {
		close(secondStarted)
		return []byte("second"), nil
	}); err != nil {
		t.Fatal(err)
	}
	queue := mgr.Queue(daemonops.Query{})
	if len(queue.Running) != 1 || queue.Running[0].Record.ID != "op-first" || len(queue.Queued) != 1 || queue.Queued[0].Record.ID != "op-second" || !containsString(queue.Queued[0].BlockingOperationIDs, "op-first") {
		t.Fatalf("terminal retry authority snapshot=%+v, want op-first blocking op-second", queue)
	}
	select {
	case <-secondStarted:
		t.Fatal("resource authority released while first operation remained durably running")
	default:
	}
	close(store.allow)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("queued operation did not start after terminal persistence retry succeeded")
	}
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalPersistenceFailurePropagatesAndRetainsAuthorityOnShutdown(t *testing.T) {
	base, stop := context.WithCancel(context.Background())
	store := &controlledTerminalStore{memoryStore: newMemoryStore(), attempted: make(chan struct{}, 1), allow: make(chan struct{})}
	mgr := New(store, Config{BaseContext: base, NewID: func() string { return "op-terminal-failure" }, LifecycleWriteTimeout: 25 * time.Millisecond, LifecycleRetryInterval: 10 * time.Millisecond})
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{Kind: "terminal-failure", ResourceKeys: []string{"session:held"}, DedupeKey: "held"}, func(context.Context) ([]byte, error) {
		return []byte("result"), nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.attempted:
	case <-time.After(time.Second):
		t.Fatal("terminal persistence was not attempted")
	}
	stop()
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mgr.Drain(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain error=%v, want propagated terminal deadline", err)
	}
	queue := mgr.Queue(daemonops.Query{})
	if len(queue.Running) != 1 || queue.Running[0].Record.ID != "op-terminal-failure" {
		t.Fatalf("running authority=%+v, want failed terminal operation retained", queue.Running)
	}
}

func cloneRecord(record daemonops.Record) daemonops.Record {
	record.ResourceKeys = append([]string(nil), record.ResourceKeys...)
	record.ResultPayload = append([]byte(nil), record.ResultPayload...)
	return record
}

func containsState(states []daemonops.State, state daemonops.State) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

type capturedLog struct {
	message string
	attrs   map[string]any
}

type captureHandler struct {
	mu      sync.Mutex
	records []capturedLog
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := make(map[string]any)
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, capturedLog{message: record.Message, attrs: attrs})
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) find(message string) (capturedLog, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range h.records {
		if record.message == message {
			return record, true
		}
	}
	return capturedLog{}, false
}

func TestSubmitDedupesActiveOperation(t *testing.T) {
	store := newMemoryStore()
	mgr := New(store, Config{NewID: func() string { return "op-1" }})

	started := make(chan struct{})
	release := make(chan struct{})
	runnerCalls := 0
	runner := func(ctx context.Context) ([]byte, error) {
		runnerCalls++
		close(started)
		<-release
		return []byte("ok"), nil
	}

	first, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:          "p1",
		Kind:               "session.start",
		DedupeKey:          "same",
		ResourceKeys:       []string{"issue:1"},
		RecentDedupeWindow: time.Second,
	}, runner)
	if err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	<-started

	second, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    "p1",
		Kind:         "session.start",
		DedupeKey:    "same",
		ResourceKeys: []string{"issue:1"},
	}, func(context.Context) ([]byte, error) {
		t.Fatal("deduped runner should not execute")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("second submit error: %v", err)
	}
	if !second.Deduped {
		t.Fatal("expected deduped submit result")
	}
	if second.Record.ID != first.Record.ID {
		t.Fatalf("deduped record id = %q, want %q", second.Record.ID, first.Record.ID)
	}
	if runnerCalls != 1 {
		t.Fatalf("runner calls = %d, want 1", runnerCalls)
	}

	close(release)
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestManagerLogsBacklogDiagnosticsForBlockedOperation(t *testing.T) {
	store := newMemoryStore()
	logs := &captureHandler{}
	mgr := New(store, Config{
		NewID:  func() string { return time.Now().UTC().Format("150405.000000000") },
		Logger: slog.New(logs),
	})

	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ID:           "op-running",
		ProjectID:    "p1",
		IssueID:      "az-1",
		Kind:         "worktree.remove",
		ResourceKeys: []string{"issue:p1:az-1"},
	}, func(context.Context) ([]byte, error) {
		close(started)
		<-release
		return nil, nil
	}); err != nil {
		t.Fatalf("submit running operation: %v", err)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first operation did not start")
	}

	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ID:           "op-blocked",
		ProjectID:    "p1",
		IssueID:      "az-1",
		Kind:         "worktree.remove",
		ResourceKeys: []string{"issue:p1:az-1"},
	}, func(context.Context) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("submit blocked operation: %v", err)
	}

	record, ok := logs.find("daemon operation waiting for busy resources")
	if !ok {
		t.Fatal("expected busy-resource diagnostic log")
	}
	if record.attrs["operation_id"] != "op-blocked" {
		t.Fatalf("operation_id = %v, want op-blocked", record.attrs["operation_id"])
	}
	if got := record.attrs["blocked_resources"]; !containsStringAttr(got, "issue:p1:az-1") {
		t.Fatalf("blocked_resources = %v, want issue:p1:az-1", got)
	}
	if got := record.attrs["blocking_operations"]; !containsStringAttr(got, "op-running") {
		t.Fatalf("blocking_operations = %v, want op-running", got)
	}
	if _, ok := record.attrs["queue_wait_ms"]; !ok {
		t.Fatalf("queue_wait_ms missing from attrs: %+v", record.attrs)
	}

	close(release)
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func containsStringAttr(value any, want string) bool {
	values, ok := value.([]string)
	if !ok {
		return false
	}
	return containsString(values, want)
}

func containsString(values []string, want string) bool {
	for _, got := range values {
		if got == want {
			return true
		}
	}
	return false
}

func TestSubmitDoesNotDedupeAcrossProjects(t *testing.T) {
	store := newMemoryStore()
	id := 0
	mgr := New(store, Config{
		NewID: func() string {
			id++
			return time.Now().Format("150405") + string(rune('a'+id))
		},
	})

	release := make(chan struct{})
	started := make(chan string, 2)
	runner := func(name string) daemonops.Runner {
		return func(context.Context) ([]byte, error) {
			started <- name
			<-release
			return nil, nil
		}
	}

	first, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    "p1",
		Kind:         "session.start",
		DedupeKey:    "same",
		ResourceKeys: []string{"issue:1"},
	}, runner("first"))
	if err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	second, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    "p2",
		Kind:         "session.start",
		DedupeKey:    "same",
		ResourceKeys: []string{"issue:1"},
	}, runner("second"))
	if err != nil {
		t.Fatalf("second submit error: %v", err)
	}
	if first.Deduped || second.Deduped {
		t.Fatal("cross-project submits should not dedupe")
	}
	if first.Record.ID == second.Record.ID {
		t.Fatalf("record ids should differ across projects, both were %q", first.Record.ID)
	}

	close(release)
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestSubmitSerializesConflictingResources(t *testing.T) {
	store := newMemoryStore()
	nextID := 0
	mgr := New(store, Config{NewID: func() string { nextID++; return time.Now().Format("150405") + string(rune('a'+nextID)) }})

	firstRunning := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{}, 1)

	_, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    "p1",
		Kind:         "git.merge",
		ResourceKeys: []string{"worktree:/tmp/az-1"},
	}, func(context.Context) ([]byte, error) {
		close(firstRunning)
		<-firstRelease
		return nil, nil
	})
	if err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	<-firstRunning

	_, err = mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    "p1",
		Kind:         "worktree.cleanup",
		ResourceKeys: []string{"worktree:/tmp/az-1"},
	}, func(context.Context) ([]byte, error) {
		secondStarted <- struct{}{}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("second submit error: %v", err)
	}

	queue := mgr.Queue(daemonops.Query{})
	if len(queue.Running) != 1 || len(queue.Queued) != 1 || queue.Queued[0].Record.Kind != "worktree.cleanup" || !containsString(queue.Queued[0].BlockingOperationIDs, queue.Running[0].Record.ID) {
		t.Fatalf("conflicting resource snapshot=%+v, want queued cleanup blocked by running merge", queue)
	}
	select {
	case <-secondStarted:
		t.Fatal("second operation started before conflicting resource was released")
	default:
	}

	close(firstRelease)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second operation did not start after conflict cleared")
	}

	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestQueueSnapshotShowsBlockedOperationDependencies(t *testing.T) {
	store := newMemoryStore()
	mgr := New(store, Config{})

	firstRunning := make(chan struct{})
	firstRelease := make(chan struct{})
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ID:           "op-running",
		ProjectID:    "p1",
		IssueID:      "az-1",
		Kind:         "git.merge",
		ResourceKeys: []string{"worktree:/tmp/az-1"},
	}, func(context.Context) ([]byte, error) {
		close(firstRunning)
		<-firstRelease
		return nil, nil
	}); err != nil {
		t.Fatalf("submit running operation: %v", err)
	}
	<-firstRunning

	queuedStarted := make(chan struct{}, 1)
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ID:           "op-queued",
		ProjectID:    "p1",
		IssueID:      "az-2",
		Kind:         "worktree.cleanup",
		ResourceKeys: []string{"worktree:/tmp/az-1"},
	}, func(context.Context) ([]byte, error) {
		queuedStarted <- struct{}{}
		return nil, nil
	}); err != nil {
		t.Fatalf("submit queued operation: %v", err)
	}
	select {
	case <-queuedStarted:
		t.Fatal("blocked operation started before snapshot")
	default:
	}

	snapshot := mgr.Queue(daemonops.Query{ProjectID: "p1"})
	if len(snapshot.Running) != 1 || snapshot.Running[0].Record.ID != "op-running" {
		t.Fatalf("running snapshot = %+v, want op-running", snapshot.Running)
	}
	if len(snapshot.Queued) != 1 {
		t.Fatalf("queued snapshot len = %d, want 1", len(snapshot.Queued))
	}
	queued := snapshot.Queued[0]
	if queued.Record.ID != "op-queued" || queued.QueueIndex != 1 {
		t.Fatalf("queued entry = %+v, want op-queued index 1", queued)
	}
	if !containsString(queued.BlockingOperationIDs, "op-running") {
		t.Fatalf("blocking operation ids = %+v, want op-running", queued.BlockingOperationIDs)
	}
	if !containsString(queued.BlockedResourceKeys, "worktree:/tmp/az-1") {
		t.Fatalf("blocked resources = %+v, want worktree", queued.BlockedResourceKeys)
	}
	select {
	case <-queuedStarted:
		t.Fatal("blocked operation started before running operation released")
	default:
	}

	close(firstRelease)
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestSubmitRunsNonConflictingResourcesConcurrently(t *testing.T) {
	store := newMemoryStore()
	id := 0
	mgr := New(store, Config{NewID: func() string { id++; return time.Now().Format("150405") + string(rune('a'+id)) }})

	startGate := make(chan struct{})
	started := make(chan string, 2)
	runner := func(name string) daemonops.Runner {
		return func(context.Context) ([]byte, error) {
			started <- name
			<-startGate
			return nil, nil
		}
	}

	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{ProjectID: "p1", Kind: "git.merge", ResourceKeys: []string{"worktree:a"}}, runner("first")); err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{ProjectID: "p1", Kind: "git.merge", ResourceKeys: []string{"worktree:b"}}, runner("second")); err != nil {
		t.Fatalf("second submit error: %v", err)
	}

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatalf("expected both operations to start concurrently, saw %v", seen)
		}
	}

	close(startGate)
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestCancelQueuedMarksCancelled(t *testing.T) {
	store := newMemoryStore()
	mgr := New(store, Config{NewID: func() string { return "op-queued" }})

	runningStarted := make(chan struct{})
	runningRelease := make(chan struct{})
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{ProjectID: "p1", Kind: "git.merge", ResourceKeys: []string{"worktree:a"}}, func(context.Context) ([]byte, error) {
		close(runningStarted)
		<-runningRelease
		return nil, nil
	}); err != nil {
		t.Fatalf("first submit error: %v", err)
	}
	<-runningStarted

	queued, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{ID: "op-queued", ProjectID: "p1", Kind: "worktree.cleanup", ResourceKeys: []string{"worktree:a"}}, func(context.Context) ([]byte, error) {
		t.Fatal("queued runner should not run after cancel")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("queued submit error: %v", err)
	}

	cancelled, err := mgr.Cancel(context.Background(), queued.Record.ID, "user cancelled")
	if err != nil {
		t.Fatalf("cancel error: %v", err)
	}
	if cancelled.State != daemonops.StateCancelled {
		t.Fatalf("cancelled state = %q, want %q", cancelled.State, daemonops.StateCancelled)
	}
	if cancelled.ErrorMessage != "user cancelled" {
		t.Fatalf("cancelled message = %q, want user cancelled", cancelled.ErrorMessage)
	}

	close(runningRelease)
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestCancelQueuedKeepsOperationPendingWhenStoreUpdateFails(t *testing.T) {
	store := &toggleUpdateErrorStore{memoryStore: newMemoryStore()}
	mgr := New(store, Config{})

	runningStarted := make(chan struct{})
	runningRelease := make(chan struct{})
	if _, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{ID: "op-running", ProjectID: "p1", Kind: "blocker", ResourceKeys: []string{"issue:a"}}, func(context.Context) ([]byte, error) {
		close(runningStarted)
		<-runningRelease
		return nil, nil
	}); err != nil {
		t.Fatalf("submit blocker: %v", err)
	}
	<-runningStarted

	queued, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{ID: "op-queued", ProjectID: "p1", Kind: "worktree.cleanup", ResourceKeys: []string{"issue:a"}}, func(context.Context) ([]byte, error) {
		t.Fatal("queued cleanup must remain blocked")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("submit queued cleanup: %v", err)
	}
	injectedErr := errors.New("injected cancel persistence failure")
	store.setUpdateError(injectedErr)
	if _, err := mgr.Cancel(context.Background(), queued.Record.ID, "reopen"); !errors.Is(err, injectedErr) {
		t.Fatalf("cancel error = %v, want injected persistence failure", err)
	}
	queue := mgr.Queue(daemonops.Query{ProjectID: "p1", IssueID: "", Kind: "worktree.cleanup"})
	if len(queue.Queued) != 1 || queue.Queued[0].Record.ID != queued.Record.ID {
		t.Fatalf("queued cleanup after failed cancel = %+v, want %s", queue.Queued, queued.Record.ID)
	}

	store.setUpdateError(nil)
	if _, err := mgr.Cancel(context.Background(), queued.Record.ID, "retry reopen"); err != nil {
		t.Fatalf("cancel retry: %v", err)
	}
	close(runningRelease)
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

func TestCancelRunningTransitionsToCancelled(t *testing.T) {
	store := newMemoryStore()
	mgr := New(store, Config{NewID: func() string { return "op-running" }})

	started := make(chan struct{})
	result := make(chan error, 1)
	submitted, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    "p1",
		Kind:         "session.start",
		ResourceKeys: []string{"issue:1"},
	}, func(ctx context.Context) ([]byte, error) {
		close(started)
		<-ctx.Done()
		result <- ctx.Err()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatalf("submit error: %v", err)
	}
	<-started

	if _, err := mgr.Cancel(context.Background(), submitted.Record.ID, "shutdown"); err != nil {
		t.Fatalf("cancel running error: %v", err)
	}
	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
	if got := <-result; !errors.Is(got, context.Canceled) {
		t.Fatalf("runner context err = %v, want canceled", got)
	}

	record, err := mgr.Get(context.Background(), submitted.Record.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if record.State != daemonops.StateCancelled {
		t.Fatalf("final state = %q, want %q", record.State, daemonops.StateCancelled)
	}
	if record.ErrorMessage != "shutdown" {
		t.Fatalf("cancel reason = %q, want shutdown", record.ErrorMessage)
	}
}

func TestStopIntakeRejectsNewOperations(t *testing.T) {
	mgr := New(newMemoryStore(), Config{})
	if err := mgr.StopIntake(); err != nil {
		t.Fatalf("stop intake error: %v", err)
	}
	_, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{ProjectID: "p1", Kind: "git.merge"}, func(context.Context) ([]byte, error) {
		return nil, nil
	})
	if !errors.Is(err, daemonops.ErrIntakeClosed) {
		t.Fatalf("submit error = %v, want ErrIntakeClosed", err)
	}
}

func TestSubmitRunnerPanicMarksOperationFailedAndDoesNotCrashManager(t *testing.T) {
	store := newMemoryStore()
	nextID := 0
	mgr := New(store, Config{NewID: func() string {
		nextID++
		if nextID == 1 {
			return "op-panic"
		}
		return "op-after"
	}})

	first, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    "p1",
		Kind:         "panic.case",
		ResourceKeys: []string{"res:1"},
	}, func(context.Context) ([]byte, error) {
		panic("boom")
	})
	if err != nil {
		t.Fatalf("submit panic operation: %v", err)
	}

	second, err := mgr.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    "p1",
		Kind:         "after.case",
		ResourceKeys: []string{"res:2"},
	}, func(context.Context) ([]byte, error) {
		return []byte("ok"), nil
	})
	if err != nil {
		t.Fatalf("submit second operation: %v", err)
	}

	if err := mgr.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}

	firstRecord, err := mgr.Get(context.Background(), first.Record.ID)
	if err != nil {
		t.Fatalf("get first record: %v", err)
	}
	if firstRecord.State != daemonops.StateFailed {
		t.Fatalf("first state = %q, want %q", firstRecord.State, daemonops.StateFailed)
	}
	if !strings.Contains(firstRecord.ErrorMessage, "panicked") {
		t.Fatalf("first error message missing panic marker: %q", firstRecord.ErrorMessage)
	}

	secondRecord, err := mgr.Get(context.Background(), second.Record.ID)
	if err != nil {
		t.Fatalf("get second record: %v", err)
	}
	if secondRecord.State != daemonops.StateDone {
		t.Fatalf("second state = %q, want %q", secondRecord.State, daemonops.StateDone)
	}
	if string(secondRecord.ResultPayload) != "ok" {
		t.Fatalf("second payload = %q, want ok", string(secondRecord.ResultPayload))
	}
}
