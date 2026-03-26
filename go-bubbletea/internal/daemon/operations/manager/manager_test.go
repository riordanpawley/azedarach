package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
)

type memoryStore struct {
	mu      sync.Mutex
	records map[string]daemonops.Record
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
	s.records[record.ID] = cloneRecord(record)
	return cloneRecord(record), nil
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

	select {
	case <-secondStarted:
		t.Fatal("second operation started before conflicting resource was released")
	case <-time.After(50 * time.Millisecond):
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
