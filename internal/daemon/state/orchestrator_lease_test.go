package state

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestOrchestratorScopeLeaseExactScopeIdentity(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	project := mustOrchestratorIdentity(t, "proj-a", domain.ProjectOrchestrationScope())
	rootA := mustRootedOrchestratorIdentity(t, "proj-a", "root-a")
	rootB := mustRootedOrchestratorIdentity(t, "proj-a", "root-b")
	probe := func(context.Context, string) (bool, error) { return true, nil }

	for _, tc := range []struct {
		identity  domain.OrchestratorIdentity
		sessionID string
	}{
		{project, "project-session"},
		{rootA, "root-a-session"},
		{rootB, "root-b-session"},
	} {
		result, err := store.AcquireOrchestratorScopeLease(ctx, tc.identity, tc.sessionID, probe)
		if err != nil {
			t.Fatalf("AcquireOrchestratorScopeLease(%s): %v", tc.sessionID, err)
		}
		if result.Disposition != OrchestratorLeaseAcquired {
			t.Fatalf("disposition = %q, want acquired", result.Disposition)
		}
	}

	leases, err := store.ListOrchestratorScopeLeases(ctx, "proj-a")
	if err != nil {
		t.Fatalf("ListOrchestratorScopeLeases: %v", err)
	}
	if len(leases) != 3 {
		t.Fatalf("leases = %d, want 3", len(leases))
	}
	attached, err := store.AcquireOrchestratorScopeLease(ctx, rootA, "root-a-session", probe)
	if err != nil {
		t.Fatalf("attach equivalent lease: %v", err)
	}
	if attached.Disposition != OrchestratorLeaseAttached || attached.Lease.SessionID != "root-a-session" {
		t.Fatalf("attach result = %+v", attached)
	}
	_, err = store.AcquireOrchestratorScopeLease(ctx, rootA, "duplicate", probe)
	if !errors.Is(err, ErrOrchestratorLeaseConflict) {
		t.Fatalf("duplicate acquire error = %v, want conflict", err)
	}
	var conflict *OrchestratorLeaseConflictError
	if !errors.As(err, &conflict) || conflict.Lease.SessionID != "root-a-session" {
		t.Fatalf("typed conflict = %+v, %v", conflict, err)
	}
}

func TestOrchestratorScopeLeaseStaleRecoveryAndLifecyclePersistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	ctx := context.Background()
	identity := mustRootedOrchestratorIdentity(t, "proj-a", "root-a")
	storeA := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	result, err := storeA.AcquireOrchestratorScopeLease(ctx, identity, "stale-session", func(context.Context, string) (bool, error) { return false, nil })
	if err != nil || result.Disposition != OrchestratorLeaseAcquired {
		t.Fatalf("initial acquire = %+v, %v", result, err)
	}
	if _, err := storeA.AdvanceOrchestratorScopeCursor(ctx, identity, 41); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}
	if err := storeA.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	storeB := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeB.Close() })
	recovered, err := storeB.AcquireOrchestratorScopeLease(ctx, identity, "replacement", func(_ context.Context, sessionID string) (bool, error) {
		if sessionID != "stale-session" {
			t.Fatalf("probed session = %q", sessionID)
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("recover stale lease: %v", err)
	}
	if recovered.Disposition != OrchestratorLeaseRecoveredStale || recovered.Lease.SessionID != "replacement" || recovered.Lease.Cursor != 41 {
		t.Fatalf("recovered = %+v", recovered)
	}
	paused, err := storeB.SetOrchestratorScopeLeaseLifecycle(ctx, identity, "replacement", domain.OrchestratorPaused)
	if err != nil || paused.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("pause lease = %+v, %v", paused, err)
	}
	probeCalled := false
	_, err = storeB.AcquireOrchestratorScopeLease(ctx, identity, "other-session", func(context.Context, string) (bool, error) {
		probeCalled = true
		return false, nil
	})
	if !errors.Is(err, ErrOrchestratorLeaseConflict) || probeCalled {
		t.Fatalf("paused replacement error/probe = %v/%t, want conflict/false", err, probeCalled)
	}
	woken, err := storeB.SetOrchestratorScopeLeaseLifecycle(ctx, identity, "replacement", domain.OrchestratorWorking)
	if err != nil || woken.Lifecycle != domain.OrchestratorWorking {
		t.Fatalf("wake lease = %+v, %v", woken, err)
	}
	if err := storeB.ReleaseOrchestratorScopeLease(ctx, identity, "other-session"); !errors.Is(err, ErrOrchestratorLeaseConflict) {
		t.Fatalf("foreign release error = %v, want conflict", err)
	}
}

func TestOrchestratorScopeLeaseDuplicateAcquireAcrossStoresHasSingleWinner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	storeA := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	storeB := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeA.Close(); _ = storeB.Close() })
	identity := mustOrchestratorIdentity(t, "proj-a", domain.ProjectOrchestrationScope())
	probe := func(context.Context, string) (bool, error) { return true, nil }

	start := make(chan struct{})
	type outcome struct {
		result OrchestratorLeaseAcquireResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i, store := range []*RuntimeStateStore{storeA, storeB} {
		wg.Add(1)
		go func(sessionID string, store *RuntimeStateStore) {
			defer wg.Done()
			<-start
			result, err := store.AcquireOrchestratorScopeLease(context.Background(), identity, sessionID, probe)
			outcomes <- outcome{result: result, err: err}
		}([]string{"session-a", "session-b"}[i], store)
	}
	close(start)
	wg.Wait()
	close(outcomes)

	winners, conflicts := 0, 0
	for outcome := range outcomes {
		switch {
		case outcome.err == nil && outcome.result.Disposition == OrchestratorLeaseAcquired:
			winners++
		case errors.Is(outcome.err, ErrOrchestratorLeaseConflict):
			conflicts++
		default:
			t.Fatalf("unexpected outcome: %+v", outcome)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners/conflicts = %d/%d, want 1/1", winners, conflicts)
	}
}

func TestOrchestratorScopeLeaseAcquireRetriesConcurrentRuntimeWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	identity := mustRootedOrchestratorIdentity(t, "proj-a", "root-a")
	if _, err := store.AcquireOrchestratorScopeLease(ctx, identity, "stale-session", func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	storeDB, err := store.dbHandle()
	if err != nil {
		t.Fatalf("open runtime store: %v", err)
	}
	storeDB.SetMaxOpenConns(1)
	storeDB.SetMaxIdleConns(1)
	if _, err := storeDB.ExecContext(ctx, `PRAGMA busy_timeout=1`); err != nil {
		t.Fatalf("shorten runtime store busy timeout: %v", err)
	}

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var probeOnce sync.Once
	leaseResult := make(chan error, 1)
	go func() {
		_, acquireErr := store.AcquireOrchestratorScopeLease(ctx, identity, "replacement-session", func(probeCtx context.Context, _ string) (bool, error) {
			probeOnce.Do(func() {
				close(probeStarted)
				select {
				case <-releaseProbe:
				case <-probeCtx.Done():
				}
			})
			return false, nil
		})
		leaseResult <- acquireErr
	}()

	select {
	case <-probeStarted:
	case <-ctx.Done():
		t.Fatal("lease acquisition did not reach runtime probe")
	}
	concurrentDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatalf("open concurrent runtime writer: %v", err)
	}
	t.Cleanup(func() { _ = concurrentDB.Close() })
	if _, err := concurrentDB.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("begin concurrent runtime write: %v", err)
	}
	t.Cleanup(func() { _, _ = concurrentDB.ExecContext(context.Background(), `ROLLBACK`) })

	worktreeResult := make(chan error, 1)
	go func() {
		worktreeResult <- store.UpsertWorktreeState(ctx, WorktreeState{
			ProjectID: "proj-a",
			IssueID:   "root-a",
			Path:      "/tmp/root-a",
			Branch:    "test/root-a",
			UpdatedAt: time.Now().UTC(),
		})
	}()
	close(releaseProbe)
	time.Sleep(25 * time.Millisecond)
	if _, err := concurrentDB.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatalf("commit concurrent runtime write: %v", err)
	}

	if err := <-leaseResult; err != nil {
		t.Fatalf("acquire lease after transient contention: %v", err)
	}
	if err := <-worktreeResult; err != nil {
		t.Fatalf("refresh worktree after transient contention: %v", err)
	}
	lease, found, err := store.GetOrchestratorScopeLease(ctx, identity)
	if err != nil || !found || lease.SessionID != "replacement-session" {
		t.Fatalf("persisted lease = %+v, found=%t, err=%v", lease, found, err)
	}
	worktree, found, err := store.GetWorktreeStateByIssueID(ctx, "proj-a", "root-a")
	if err != nil || !found || worktree.Path != "/tmp/root-a" {
		t.Fatalf("persisted worktree = %+v, found=%t, err=%v", worktree, found, err)
	}
}

func TestOrchestratorLeaseAuthorityRefreshesStaleCacheBeforeAcquire(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	storeA := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	storeB := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeA.Close(); _ = storeB.Close() })
	authorityA := NewOrchestratorLeaseAuthority(storeA)
	authorityB := NewOrchestratorLeaseAuthority(storeB)
	identity := mustOrchestratorIdentity(t, "proj-a", domain.ProjectOrchestrationScope())
	ctx := context.Background()
	if err := authorityB.Refresh(ctx, "proj-a"); err != nil {
		t.Fatalf("seed empty cache: %v", err)
	}
	if _, err := authorityA.Acquire(ctx, identity, "session-a", func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("authority A acquire: %v", err)
	}
	_, err := authorityB.Acquire(ctx, identity, "session-b", func(_ context.Context, sessionID string) (bool, error) {
		if sessionID != "session-a" {
			t.Fatalf("runtime probe session = %q, want session-a", sessionID)
		}
		return true, nil
	})
	if !errors.Is(err, ErrOrchestratorLeaseConflict) {
		t.Fatalf("authority B acquire error = %v, want conflict", err)
	}
	lease, found, err := authorityB.Get(ctx, identity)
	if err != nil || !found || lease.SessionID != "session-a" {
		t.Fatalf("authority B refreshed lease = %+v, found=%t, err=%v", lease, found, err)
	}
}

func TestOrchestratorLifecycleGracePersistsAcrossRestartAndResets(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "lifecycle.db")
	storeA := NewRuntimeStateStoreAtPath(dbPath, nil)
	identity := mustOrchestratorIdentity(t, "proj-a", domain.ProjectOrchestrationScope())
	if _, err := storeA.AcquireOrchestratorScopeLease(ctx, identity, "orch", func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	policy := domain.OrchestratorLifecyclePolicy{CompleteGrace: 5 * time.Minute, WakeDebounce: time.Second}
	lease, err := storeA.EvaluateOrchestratorScopeLease(ctx, identity, "orch", start, domain.OrchestratorLifecycleFacts{}, policy)
	if err != nil || lease.Lifecycle != domain.OrchestratorCompleteGrace || lease.CompleteSince == nil || !lease.CompleteSince.Equal(start) {
		t.Fatalf("initial grace = %+v, %v", lease, err)
	}
	if err := storeA.Close(); err != nil {
		t.Fatal(err)
	}
	storeB := NewRuntimeStateStoreAtPath(dbPath, nil)
	lease, err = storeB.EvaluateOrchestratorScopeLease(ctx, identity, "orch", start.Add(6*time.Minute), domain.OrchestratorLifecycleFacts{}, policy)
	if err != nil || lease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("persisted grace = %+v, %v", lease, err)
	}
	lease, err = storeB.EvaluateOrchestratorScopeLease(ctx, identity, "orch", start.Add(7*time.Minute), domain.OrchestratorLifecycleFacts{OpenIssues: 1}, policy)
	if err != nil || lease.Lifecycle != domain.OrchestratorWorking || lease.CompleteSince != nil {
		t.Fatalf("reset grace = %+v, %v", lease, err)
	}
}

func TestOrchestratorWakeIsDurablyDebouncedAcrossStores(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wake.db")
	storeA := NewRuntimeStateStoreAtPath(dbPath, nil)
	storeB := NewRuntimeStateStoreAtPath(dbPath, nil)
	identity := mustOrchestratorIdentity(t, "proj-a", domain.ProjectOrchestrationScope())
	if _, err := storeA.AcquireOrchestratorScopeLease(ctx, identity, "orch", func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 10, 2, 0, 0, 0, time.UTC)
	policy := domain.OrchestratorLifecyclePolicy{WakeDebounce: 2 * time.Second}
	first, changed, err := storeA.WakeOrchestratorScopeLease(ctx, identity, now, domain.OrchestratorWakeOpenWork, policy)
	if err != nil || !changed || first.LastWakeAt == nil {
		t.Fatalf("first wake = %+v, %t, %v", first, changed, err)
	}
	duplicate, changed, err := storeB.WakeOrchestratorScopeLease(ctx, identity, now.Add(time.Second), domain.OrchestratorWakeReviewRequest, policy)
	if err != nil || changed || duplicate.LastWakeReason != domain.OrchestratorWakeOpenWork {
		t.Fatalf("duplicate wake = %+v, %t, %v", duplicate, changed, err)
	}
	accepted, changed, err := storeB.WakeOrchestratorScopeLease(ctx, identity, now.Add(2*time.Second), domain.OrchestratorWakeHumanAnswer, policy)
	if err != nil || !changed || accepted.LastWakeReason != domain.OrchestratorWakeHumanAnswer {
		t.Fatalf("accepted wake = %+v, %t, %v", accepted, changed, err)
	}
	if _, _, err := storeB.WakeOrchestratorScopeLease(ctx, identity, now.Add(3*time.Second), "", policy); err == nil {
		t.Fatal("invalid wake reason unexpectedly accepted")
	}
}

func mustRootedOrchestratorIdentity(t *testing.T, projectID, rootID string) domain.OrchestratorIdentity {
	t.Helper()
	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatalf("RootedOrchestrationScope: %v", err)
	}
	return mustOrchestratorIdentity(t, projectID, scope)
}

func mustOrchestratorIdentity(t *testing.T, projectID string, scope domain.OrchestrationScope) domain.OrchestratorIdentity {
	t.Helper()
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatalf("NewOrchestratorIdentity: %v", err)
	}
	return identity
}
