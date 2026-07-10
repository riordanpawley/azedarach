package state

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestOrchestratorLoopCheckpointSurvivesRestartAndUsesCursorCAS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	identity := mustOrchestratorIdentity(t, "project", domain.ProjectOrchestrationScope())
	ctx := context.Background()
	storeA := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	advanced, err := storeA.AdvanceOrchestratorLoopCheckpoint(ctx, OrchestratorLoopCheckpoint{Identity: identity, WatchCursor: 41, LastActionKey: "wave-41", LastActionKind: "start", LastActionStatus: "pending", UpdatedAt: time.Now()}, 0)
	if err != nil || !advanced {
		t.Fatalf("initial advance = %t, %v", advanced, err)
	}
	if err := storeA.Close(); err != nil {
		t.Fatal(err)
	}

	storeB := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeB.Close() })
	checkpoint, found, err := storeB.GetOrchestratorLoopCheckpoint(ctx, identity)
	if err != nil || !found || checkpoint.WatchCursor != 41 || checkpoint.LastActionKey != "wave-41" {
		t.Fatalf("recovered checkpoint = %+v found=%t err=%v", checkpoint, found, err)
	}
	advanced, err = storeB.AdvanceOrchestratorLoopCheckpoint(ctx, OrchestratorLoopCheckpoint{Identity: identity, WatchCursor: 42, LastActionKey: "stale"}, 0)
	if err != nil || advanced {
		t.Fatalf("stale CAS = %t, %v", advanced, err)
	}
}

func TestOrchestratorLoopCheckpointMultiDaemonSingleCursorWinner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	storeA := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	storeB := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeA.Close(); _ = storeB.Close() })
	identity := mustOrchestratorIdentity(t, "project", domain.ProjectOrchestrationScope())
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*RuntimeStateStore{storeA, storeB} {
		wg.Add(1)
		go func(candidate *RuntimeStateStore) {
			defer wg.Done()
			<-start
			advanced, err := candidate.AdvanceOrchestratorLoopCheckpoint(ctx, OrchestratorLoopCheckpoint{Identity: identity, WatchCursor: 9, LastActionKey: "same-deterministic-action", UpdatedAt: time.Now()}, 0)
			results <- advanced
			errs <- err
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for advanced := range results {
		if advanced {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("cursor winners = %d, want 1", winners)
	}
}

func TestOrchestratorLoopActionMultiDaemonSingleCompletionWinner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	storeA := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	storeB := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeA.Close(); _ = storeB.Close() })
	identity := mustOrchestratorIdentity(t, "project", domain.ProjectOrchestrationScope())
	ctx := context.Background()
	claimed, err := storeA.AdvanceOrchestratorLoopCheckpoint(ctx, OrchestratorLoopCheckpoint{Identity: identity, WatchCursor: 12, LastActionKey: "action", LastActionKind: "start", LastActionStatus: "applying", UpdatedAt: time.Now()}, 0)
	if err != nil || !claimed {
		t.Fatalf("claim action = %t, %v", claimed, err)
	}
	winners := 0
	for _, store := range []*RuntimeStateStore{storeA, storeB} {
		completed, err := store.CompleteOrchestratorLoopAction(ctx, OrchestratorLoopCheckpoint{Identity: identity, WatchCursor: 12, LastActionKey: "action", LastActionKind: "start", LastActionStatus: "applied", UpdatedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		if completed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("completion winners = %d, want 1", winners)
	}
}
