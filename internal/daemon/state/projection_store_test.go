package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/testisolation"

	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/observability"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	_ "modernc.org/sqlite"
)

type codedSQLiteTestError struct {
	code int
}

func (e codedSQLiteTestError) Error() string { return "sqlite contention" }
func (e codedSQLiteTestError) Code() int     { return e.code }

func TestRetrySQLiteWriteRetriesBusyAndLocked(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{name: "busy snapshot", code: 517},
		{name: "locked", code: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			waits := 0
			err := retrySQLiteWrite(
				context.Background(),
				time.Second,
				time.Millisecond,
				func(context.Context, time.Duration) error {
					waits++
					return nil
				},
				func(context.Context) error {
					attempts++
					if attempts == 1 {
						return fmt.Errorf("wrapped: %w", codedSQLiteTestError{code: tt.code})
					}
					return nil
				},
			)
			if err != nil {
				t.Fatalf("retrySQLiteWrite: %v", err)
			}
			if attempts != 2 || waits != 1 {
				t.Fatalf("attempts=%d waits=%d, want attempts=2 waits=1", attempts, waits)
			}
		})
	}
}

func TestRuntimeStateStoreManagedAgentIdentityRejectsStaleAcrossStores(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	first := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	second := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	old := ManagedAgentIdentity{ProjectID: "p", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old", ObservedAt: time.Unix(100, 0)}
	if err := first.UpsertManagedAgentIdentity(ctx, old); err != nil {
		t.Fatalf("upsert old identity: %v", err)
	}
	current := old
	current.PanePID = 200
	current.AgentIncarnation = "new"
	current.ObservedAt = time.Unix(200, 0)
	if err := second.UpsertManagedAgentIdentity(ctx, current); err != nil {
		t.Fatalf("upsert current identity: %v", err)
	}
	matched, err := first.MatchManagedAgentIdentity(ctx, old)
	if err != nil {
		t.Fatalf("match stale identity: %v", err)
	}
	if matched {
		t.Fatal("stale daemon matched superseded process incarnation")
	}
	matched, err = first.MatchManagedAgentIdentity(ctx, current)
	if err != nil || !matched {
		t.Fatalf("current identity match = %v, err=%v", matched, err)
	}
	listed, err := second.ListManagedAgentIdentities(ctx, "p", "az-1")
	if err != nil || len(listed) != 1 || listed[0].LogicalPaneID != "agent" {
		t.Fatalf("listed identities = %+v err=%v", listed, err)
	}
	old.ObservedAt = time.Unix(150, 0)
	if err := first.UpsertManagedAgentIdentity(ctx, old); !errors.Is(err, ErrStaleManagedAgentIdentity) {
		t.Fatalf("replay stale identity error = %v, want ErrStaleManagedAgentIdentity", err)
	}
	got, found, err := second.GetManagedAgentIdentity(ctx, "p", "az-1", "agent")
	if err != nil || !found || got.PanePID != 200 || got.AgentIncarnation != "new" {
		t.Fatalf("identity after stale replay = %+v found=%v err=%v", got, found, err)
	}
}

func TestRuntimeStateStoreSessionIntentRejectsStaleDaemonReplay(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	first := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	second := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })

	base := time.Date(2026, time.July, 17, 3, 0, 0, 0, time.UTC)
	staleRunning := Session{
		ID: "az-ticket", IssueID: "ticket", Role: SessionRoleWorker,
		ScopeKind: SessionScopeIssue, ScopeID: "ticket",
		State: SessionStateRunning, UpdatedAt: base,
	}
	if err := first.UpsertSessionState(ctx, "p", staleRunning); err != nil {
		t.Fatalf("seed running intent: %v", err)
	}
	stopped := staleRunning
	stopped.State = SessionStateStopped
	stopped.UpdatedAt = base.Add(time.Second)
	if err := second.UpsertSessionState(ctx, "p", stopped); err != nil {
		t.Fatalf("write stopped intent: %v", err)
	}
	if err := first.UpsertSessionState(ctx, "p", staleRunning); err != nil {
		t.Fatalf("replay stale running intent: %v", err)
	}

	got, found, err := second.GetSessionState(ctx, "p", staleRunning.ID)
	if err != nil || !found {
		t.Fatalf("load intent after stale replay: found=%v err=%v", found, err)
	}
	if got.State != SessionStateStopped || !got.UpdatedAt.Equal(stopped.UpdatedAt) {
		t.Fatalf("intent after stale replay = %+v, want stopped at %s", got, stopped.UpdatedAt)
	}

	// Equal timestamps cannot let a stale non-terminal writer reverse a stop.
	equalRunning := staleRunning
	equalRunning.UpdatedAt = stopped.UpdatedAt
	if err := first.UpsertSessionState(ctx, "p", equalRunning); err != nil {
		t.Fatalf("replay equal-time running intent: %v", err)
	}
	got, found, err = second.GetSessionState(ctx, "p", staleRunning.ID)
	if err != nil || !found || got.State != SessionStateStopped {
		t.Fatalf("intent after equal-time replay = %+v found=%v err=%v, want stopped", got, found, err)
	}

	// A genuinely newer start remains authoritative after issue reactivation.
	restarted := staleRunning
	restarted.UpdatedAt = stopped.UpdatedAt.Add(time.Second)
	if err := first.UpsertSessionState(ctx, "p", restarted); err != nil {
		t.Fatalf("write newer running intent: %v", err)
	}
	got, found, err = second.GetSessionState(ctx, "p", staleRunning.ID)
	if err != nil || !found || got.State != SessionStateRunning || !got.UpdatedAt.Equal(restarted.UpdatedAt) {
		t.Fatalf("intent after newer start = %+v found=%v err=%v", got, found, err)
	}
}

func TestRetrySQLiteWriteDoesNotRetryPermanentFailure(t *testing.T) {
	want := errors.New("constraint failed")
	attempts := 0
	err := retrySQLiteWrite(
		context.Background(),
		time.Second,
		time.Millisecond,
		func(context.Context, time.Duration) error {
			t.Fatal("wait called for permanent failure")
			return nil
		},
		func(context.Context) error {
			attempts++
			return want
		},
	)
	if !errors.Is(err, want) {
		t.Fatalf("retrySQLiteWrite error = %v, want %v", err, want)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRetrySQLiteWriteReturnsCurrentWaitFailureAfterContention(t *testing.T) {
	attempts := 0
	err := retrySQLiteWrite(
		context.Background(),
		time.Second,
		time.Millisecond,
		func(context.Context, time.Duration) error {
			return context.DeadlineExceeded
		},
		func(context.Context) error {
			attempts++
			return codedSQLiteTestError{code: 517}
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retrySQLiteWrite error = %v, want current wait failure", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 before injected exhaustion", attempts)
	}
}

func TestRetrySQLiteWritePreservesExternalCancellationAfterContention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := retrySQLiteWrite(
		ctx,
		time.Second,
		time.Millisecond,
		waitSQLiteWriteRetry,
		func(context.Context) error {
			attempts++
			if attempts == 1 {
				return codedSQLiteTestError{code: 517}
			}
			cancel()
			return context.Canceled
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retrySQLiteWrite error = %v, want external cancellation", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRetrySQLiteWriteBoundsEachAttempt(t *testing.T) {
	startedAt := time.Now()
	err := retrySQLiteWrite(
		context.Background(),
		10*time.Millisecond,
		time.Millisecond,
		waitSQLiteWriteRetry,
		func(attemptCtx context.Context) error {
			<-attemptCtx.Done()
			return attemptCtx.Err()
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retrySQLiteWrite error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("attempt ignored retry budget: %s", elapsed)
	}
}

func TestRetrySQLiteWritePreservesCurrentErrorAfterContention(t *testing.T) {
	permanent := errors.New("projection invariant failed")
	tests := []struct {
		name    string
		budget  time.Duration
		current func(context.Context) error
		want    error
	}{
		{
			name:   "returned cancellation",
			budget: time.Second,
			current: func(context.Context) error {
				return context.Canceled
			},
			want: context.Canceled,
		},
		{
			name:   "retry deadline",
			budget: 20 * time.Millisecond,
			current: func(attemptCtx context.Context) error {
				<-attemptCtx.Done()
				return attemptCtx.Err()
			},
			want: context.DeadlineExceeded,
		},
		{
			name:   "permanent failure",
			budget: time.Second,
			current: func(context.Context) error {
				return permanent
			},
			want: permanent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			err := retrySQLiteWrite(
				context.Background(),
				tt.budget,
				time.Millisecond,
				waitSQLiteWriteRetry,
				func(attemptCtx context.Context) error {
					attempts++
					if attempts == 1 {
						return codedSQLiteTestError{code: 517}
					}
					return tt.current(attemptCtx)
				},
			)
			if !errors.Is(err, tt.want) {
				t.Fatalf("retrySQLiteWrite error = %v, want current error %v", err, tt.want)
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d, want 2", attempts)
			}
		})
	}
}

func TestRuntimeStateStoreTerminalProjectionWritesRestartAfterBusySnapshot(t *testing.T) {
	operations := []string{
		"upsert_session_state",
		"delete_worktree_state",
		"apply_physical_session_observation",
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "azedarach.db")
			store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.dbHandle(); err != nil {
				t.Fatal(err)
			}

			reader, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(100)&_pragma=journal_mode(WAL)")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reader.Close() })
			writer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(100)&_pragma=journal_mode(WAL)")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.Close() })
			if _, err := writer.Exec(`CREATE TABLE projection_conflict_test (id INTEGER PRIMARY KEY, source TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}

			attempts := 0
			err = store.withRetryingWriteLock(context.Background(), operation, func(context.Context) error {
				attempts++
				if attempts > 2 {
					_, err := writer.Exec(`INSERT INTO projection_conflict_test(source) VALUES('terminal')`)
					return err
				}
				tx, err := reader.Begin()
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback() }()
				var count int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM projection_conflict_test`).Scan(&count); err != nil {
					return err
				}
				if _, err := writer.Exec(`INSERT INTO projection_conflict_test(source) VALUES('snapshot-writer')`); err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO projection_conflict_test(source) VALUES('stale-reader')`); err == nil {
					return errors.New("stale snapshot write unexpectedly succeeded")
				} else if !isSQLiteWriteContention(err) {
					return fmt.Errorf("stale snapshot write returned non-contention error: %w", err)
				} else {
					return fmt.Errorf("%s projection conflict: %w", operation, err)
				}
			})
			if err != nil {
				t.Fatalf("terminal projection write: %v", err)
			}
			if attempts != 3 {
				t.Fatalf("attempts = %d, want 3", attempts)
			}
			var terminalRows int
			if err := writer.QueryRow(`SELECT COUNT(*) FROM projection_conflict_test WHERE source='terminal'`).Scan(&terminalRows); err != nil {
				t.Fatal(err)
			}
			if terminalRows != 1 {
				t.Fatalf("terminal rows = %d, want exactly one", terminalRows)
			}
		})
	}
}

func TestRuntimeStateStoreMethodsRecoverFromInjectedBusySnapshot(t *testing.T) {
	newStore := func(t *testing.T) *RuntimeStateStore {
		t.Helper()
		store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	retryingContext := func(operation string, attempts *int) context.Context {
		return WithSQLiteWriteAttemptHookForTest(context.Background(), func(gotOperation string, attempt int) error {
			if gotOperation != operation {
				return fmt.Errorf("unexpected projection operation %s, want %s", gotOperation, operation)
			}
			*attempts = attempt
			if attempt <= 2 {
				return codedSQLiteTestError{code: 517}
			}
			return nil
		})
	}

	t.Run("UpsertSessionState", func(t *testing.T) {
		store := newStore(t)
		attempts := 0
		ctx := retryingContext("upsert_session_state", &attempts)
		want := Session{
			ID: "az-issue", IssueID: "issue", Role: SessionRoleWorker,
			ScopeKind: SessionScopeIssue, ScopeID: "issue",
			State: SessionStateStopped, ObservedState: SessionStateStopped,
			UpdatedAt: time.Now().UTC(),
		}
		if err := store.UpsertSessionState(ctx, "p", want); err != nil {
			t.Fatal(err)
		}
		got, found, err := store.GetSessionState(context.Background(), "p", want.ID)
		if err != nil || !found || got.State != SessionStateStopped {
			t.Fatalf("session=%+v found=%v err=%v", got, found, err)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})

	t.Run("DeleteWorktreeState", func(t *testing.T) {
		store := newStore(t)
		if err := store.UpsertWorktreeState(context.Background(), WorktreeState{
			ProjectID: "p", IssueID: "issue", Path: "/tmp/issue", Branch: "issue",
		}); err != nil {
			t.Fatal(err)
		}
		attempts := 0
		ctx := retryingContext("delete_worktree_state", &attempts)
		if err := store.DeleteWorktreeState(ctx, "p", "issue"); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.GetWorktreeStateByIssueID(context.Background(), "p", "issue"); err != nil || found {
			t.Fatalf("deleted worktree found=%v err=%v", found, err)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})

	t.Run("ApplyPhysicalSessionObservation", func(t *testing.T) {
		store := newStore(t)
		seed := Session{
			ID: "az-issue", IssueID: "issue", Role: SessionRoleWorker,
			ScopeKind: SessionScopeIssue, ScopeID: "issue",
			State: SessionStateStopped, ObservedState: SessionStateStopped,
			UpdatedAt: time.Now().UTC().Add(-time.Second),
		}
		if err := store.UpsertSessionState(context.Background(), "p", seed); err != nil {
			t.Fatal(err)
		}
		attempts := 0
		ctx := retryingContext("apply_physical_session_observation", &attempts)
		changed, applied, err := store.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{
			ProjectID: "p", SessionID: seed.ID, ObservedState: SessionStateRunning,
			Activity: "busy", ActivitySource: "hooks", UpdatedAt: time.Now().UTC(),
		})
		if err != nil || !applied || len(changed) != 1 {
			t.Fatalf("changed=%+v applied=%v err=%v", changed, applied, err)
		}
		got, found, err := store.GetPhysicalSessionObservation(context.Background(), "p", seed.ID)
		if err != nil || !found || got.ObservedState != SessionStateRunning {
			t.Fatalf("observation=%+v found=%v err=%v", got, found, err)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})
}

func TestRuntimeStateStoreTerminalRetryKeepsWriterSlot(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.dbHandle(); err != nil {
		t.Fatal(err)
	}
	firstAttempt := make(chan struct{})
	backgroundDone := make(chan error, 1)
	terminalDone := make(chan error, 1)
	attempts := 0
	go func() {
		terminalDone <- store.withRetryingWriteLock(context.Background(), "delete_worktree_state", func(context.Context) error {
			attempts++
			if attempts == 1 {
				close(firstAttempt)
				return codedSQLiteTestError{code: 517}
			}
			return nil
		})
	}()
	select {
	case <-firstAttempt:
	case err := <-terminalDone:
		t.Fatalf("terminal write failed before first attempt: %v", err)
	case <-time.After(time.Second):
		t.Fatal("terminal write did not start")
	}
	go func() {
		backgroundDone <- store.UpsertWorktreeState(context.Background(), WorktreeState{
			ProjectID: "p", IssueID: "background", Path: "/tmp/background", Branch: "background",
		})
	}()
	select {
	case err := <-backgroundDone:
		t.Fatalf("background writer bypassed terminal retry slot: %v", err)
	case err := <-terminalDone:
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-backgroundDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("background writer did not resume after terminal retry")
	}
}

func TestRuntimeStateStoreContentionDiagnosticNamesOperation(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), logger)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.dbHandle(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := store.withRetryingWriteLock(ctx, "delete_worktree_state", func(context.Context) error {
		cancel()
		return codedSQLiteTestError{code: 517}
	})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "runtime projection write delete_worktree_state failed on attempt 1") {
		t.Fatalf("diagnostic error = %v", err)
	}
	if got := logs.String(); !strings.Contains(got, `"operation":"delete_worktree_state"`) || !strings.Contains(got, `"attempt":1`) {
		t.Fatalf("contention log missing operation diagnostics: %s", got)
	}
}

func TestRuntimeStateStoreSessionRoundTrip(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	updatedAt := time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC)
	if err := store.UpsertSessionState(context.Background(), "proj-a", Session{
		ID:             "sess-1",
		IssueID:        "bja",
		State:          SessionStateAttached,
		Activity:       "NO-AGENT",
		ActivitySource: "SESSION",
		UpdatedAt:      updatedAt,
	}); err != nil {
		t.Fatalf("UpsertSessionState: %v", err)
	}

	sessions, err := store.ListSessionStates(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListSessionStates: %v", err)
	}
	if got, want := len(sessions), 1; got != want {
		t.Fatalf("sessions count = %d, want %d", got, want)
	}
	if sessions[0].ID != "sess-1" || sessions[0].IssueID != "bja" {
		t.Fatalf("session row = %+v", sessions[0])
	}
	if sessions[0].State != SessionStateAttached {
		t.Fatalf("session state = %s, want %s", sessions[0].State, SessionStateAttached)
	}
	if sessions[0].Activity != "" || sessions[0].ActivitySource != "" {
		t.Fatalf("desired-only session activity = %s/%s, want empty without physical observation", sessions[0].Activity, sessions[0].ActivitySource)
	}

	if err := store.DeleteSessionState(context.Background(), "proj-a", "sess-1"); err != nil {
		t.Fatalf("DeleteSessionState: %v", err)
	}
	sessions, err = store.ListSessionStates(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListSessionStates after delete: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions after delete = %d, want 0", len(sessions))
	}
}

func TestRuntimeStateStoreBoundsConnectionPool(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	db, err := store.dbHandle()
	if err != nil {
		t.Fatalf("dbHandle: %v", err)
	}
	if got := db.Stats().MaxOpenConnections; got != runtimeStateMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, runtimeStateMaxOpenConns)
	}
}

func TestRuntimeStateStoreClearsSessionActivityForStoppedRows(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()
	now := time.Date(2026, time.April, 1, 8, 5, 0, 0, time.UTC)
	if err := store.UpsertSessionState(ctx, "proj-a", Session{
		ID:             "sess-1",
		IssueID:        "bja",
		State:          SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "runtime",
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("UpsertSessionState running: %v", err)
	}
	if err := store.UpsertSessionState(ctx, "proj-a", Session{
		ID:        "sess-1",
		IssueID:   "bja",
		State:     SessionStateStopped,
		UpdatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("UpsertSessionState stopped: %v", err)
	}

	session, found, err := store.GetSessionState(ctx, "proj-a", "sess-1")
	if err != nil {
		t.Fatalf("GetSessionState: %v", err)
	}
	if !found {
		t.Fatal("expected stopped session row")
	}
	if session.Activity != "" || session.ActivitySource != "" {
		t.Fatalf("session activity = %s/%s, want empty activity for stopped row", session.Activity, session.ActivitySource)
	}
	if session.ObservedState != "" {
		t.Fatalf("session observed state = %s, desired stop must not fabricate runtime observation", session.ObservedState)
	}

	if err := store.ReplaceSessionStates(ctx, "proj-a", []Session{
		{ID: "sess-2", IssueID: "bjb", State: SessionStateStopped, ObservedState: SessionStateStopped, Activity: "no-agent", ActivitySource: "session", UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceSessionStates stopped: %v", err)
	}
	session, found, err = store.GetSessionState(ctx, "proj-a", "sess-2")
	if err != nil {
		t.Fatalf("GetSessionState replaced: %v", err)
	}
	if !found {
		t.Fatal("expected replaced stopped session row")
	}
	if session.Activity != "" || session.ActivitySource != "" {
		t.Fatalf("replaced session activity = %s/%s, want empty activity for stopped row", session.Activity, session.ActivitySource)
	}
	if session.ObservedState != "" {
		t.Fatalf("replaced session observed state = %s, desired snapshot must not fabricate runtime observation", session.ObservedState)
	}
}

func TestRuntimeStateStoreSessionReplaceAndList(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Date(2026, time.April, 1, 8, 15, 0, 0, time.UTC)
	if err := store.ReplaceSessionStates(context.Background(), "proj-a", []Session{
		{ID: "sess-1", IssueID: "bja", State: SessionStateAttached, UpdatedAt: now},
		{ID: "sess-2", IssueID: "bjb", State: SessionStatePaused, UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceSessionStates: %v", err)
	}

	sessions, err := store.ListSessionStates(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListSessionStates: %v", err)
	}
	if got, want := len(sessions), 2; got != want {
		t.Fatalf("sessions count = %d, want %d", got, want)
	}

	if err := store.ReplaceSessionStates(context.Background(), "proj-a", []Session{
		{ID: "sess-2", IssueID: "bjb", State: SessionStateAttached, UpdatedAt: now.Add(1 * time.Minute)},
	}); err != nil {
		t.Fatalf("ReplaceSessionStates second pass: %v", err)
	}

	sessions, err = store.ListSessionStates(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListSessionStates after replace: %v", err)
	}
	if got, want := len(sessions), 1; got != want {
		t.Fatalf("sessions after replace = %d, want %d", got, want)
	}
	if sessions[0].ID != "sess-2" || sessions[0].IssueID != "bjb" {
		t.Fatalf("session row after replace = %+v", sessions[0])
	}
	if sessions[0].State != SessionStateAttached {
		t.Fatalf("session state after replace = %s, want %s", sessions[0].State, SessionStateAttached)
	}
}

func TestReplaceSessionStatesPrunesSharedRuntimeByLogicalIntent(t *testing.T) {
	ctx := context.Background()
	sharedID, issueID := "az-root", "root"
	worker := Session{ID: sharedID, IssueID: issueID, Role: SessionRoleWorker, ScopeKind: SessionScopeIssue, ScopeID: issueID, State: SessionStateRunning}
	rooted := Session{ID: sharedID, IssueID: issueID, Role: SessionRoleOrchestrator, ScopeKind: SessionScopeOrchestration, ScopeID: issueID, State: SessionStateRunning}
	for _, tc := range []struct {
		name    string
		keep    Session
		removed SessionRole
	}{
		{name: "worker only", keep: worker, removed: SessionRoleOrchestrator},
		{name: "rooted only", keep: rooted, removed: SessionRoleWorker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
			t.Cleanup(func() { _ = store.Close() })
			if err := store.ReplaceSessionStates(ctx, "p", []Session{worker, rooted}); err != nil {
				t.Fatal(err)
			}
			if err := store.ReplaceSessionStates(ctx, "p", []Session{tc.keep}); err != nil {
				t.Fatal(err)
			}
			rows, err := store.ListSessionIntentStates(ctx, "p")
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Role != tc.keep.Role || rows[0].ID != sharedID {
				t.Fatalf("remaining intents=%+v", rows)
			}
			if rows[0].Role == tc.removed {
				t.Fatalf("stale %s intent retained", tc.removed)
			}
		})
	}
}

func TestApplySessionCompensationPreservesRootedOrchestratorIntent(t *testing.T) {
	ctx := context.Background()
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, time.July, 16, 7, 30, 0, 0, time.UTC)
	rooted := Session{
		ID: "az-root", IssueID: "root", Role: SessionRoleOrchestrator,
		ScopeKind: SessionScopeOrchestration, ScopeID: "root",
		State: SessionStateStarting, UpdatedAt: now,
	}
	if err := store.ReplaceSessionStates(ctx, "project", []Session{rooted}); err != nil {
		t.Fatal(err)
	}
	changed, winner, err := store.ApplySessionCompensation(ctx, "project", rooted, SessionStateStopped, SessionStateStopped, "", "", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if winner != SessionStateStopped || len(changed) != 1 || changed[0].Role != SessionRoleOrchestrator || changed[0].State != SessionStateStopped || changed[0].ObservedState != SessionStateStopped {
		t.Fatalf("typed compensation changed=%+v winner=%s", changed, winner)
	}
	if worker, found, err := store.GetWorkerSessionStateByIssueID(ctx, "project", "root", "az-root"); err != nil || found {
		t.Fatalf("typed compensation worker=%+v found=%t err=%v", worker, found, err)
	}
}

func TestRuntimeStateStoreSessionGetters(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Date(2026, time.April, 1, 8, 20, 0, 0, time.UTC)
	rows := []Session{
		{ID: "sess-1", IssueID: "bja", State: SessionStateAttached, Activity: "busy", ActivitySource: "runtime", UpdatedAt: now},
		{ID: "sess-1.pane-2", IssueID: "bja", State: SessionStatePaused, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now.Add(1 * time.Minute)},
	}
	if err := store.ReplaceSessionStates(context.Background(), "proj-a", rows); err != nil {
		t.Fatalf("ReplaceSessionStates: %v", err)
	}

	session, found, err := store.GetSessionState(context.Background(), "proj-a", "sess-1")
	if err != nil {
		t.Fatalf("GetSessionState: %v", err)
	}
	if !found {
		t.Fatal("expected session state by session id")
	}
	if session.ID != "sess-1" || session.IssueID != "bja" {
		t.Fatalf("session by id = %+v", session)
	}

	session, found, err = store.GetSessionStateByIssueID(context.Background(), "proj-a", "bja")
	if err != nil {
		t.Fatalf("GetSessionStateByIssueID: %v", err)
	}
	if !found {
		t.Fatal("expected session state by issue id")
	}
	if session.ID != "sess-1.pane-2" || session.State != SessionStatePaused {
		t.Fatalf("session by issue = %+v", session)
	}
	if session.Activity != "idle" || session.ActivitySource != "hooks" {
		t.Fatalf("session activity by issue = %s/%s, want idle/hooks", session.Activity, session.ActivitySource)
	}
}

func TestRuntimeStateStoreSeparatesSessionIntentAndObservations(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()
	now := time.Date(2026, time.April, 1, 8, 25, 0, 0, time.UTC)
	parent := Session{ID: "az-bja", IssueID: "bja", State: SessionStateRunning, UpdatedAt: now}
	pane := Session{ID: "az-bja.pane-535", IssueID: "bja", State: SessionStateRunning, Activity: "busy", ActivitySource: "runtime", UpdatedAt: now.Add(time.Second)}
	if err := store.UpsertSessionState(ctx, "proj-a", parent); err != nil {
		t.Fatalf("UpsertSessionState parent: %v", err)
	}
	if err := store.UpsertSessionState(ctx, "proj-a", pane); err != nil {
		t.Fatalf("UpsertSessionState pane: %v", err)
	}

	allRows, err := store.ListSessionStates(ctx, "proj-a")
	if err != nil {
		t.Fatalf("ListSessionStates: %v", err)
	}
	if got, want := len(allRows), 2; got != want {
		t.Fatalf("all session rows = %d, want %d: %+v", got, want, allRows)
	}
	intentRows, err := store.ListSessionIntentStates(ctx, "proj-a")
	if err != nil {
		t.Fatalf("ListSessionIntentStates: %v", err)
	}
	if got, want := len(intentRows), 1; got != want {
		t.Fatalf("intent session rows = %d, want %d: %+v", got, want, intentRows)
	}
	if intentRows[0].ID != parent.ID {
		t.Fatalf("intent row = %+v, want parent %s", intentRows[0], parent.ID)
	}

	db, err := sql.Open("sqlite", store.dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var parentCount, observationCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_session_projections WHERE session_id = ?`, parent.ID).Scan(&parentCount); err != nil {
		t.Fatalf("count parent rows: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_session_observations WHERE session_id = ?`, pane.ID).Scan(&observationCount); err != nil {
		t.Fatalf("count observation rows: %v", err)
	}
	if parentCount != 1 || observationCount != 1 {
		t.Fatalf("physical rows parent=%d observation=%d, want 1/1", parentCount, observationCount)
	}
}

func TestRuntimeStateStoreListsSessionStatesByIssueIDs(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	now := time.Date(2026, time.July, 13, 5, 0, 0, 0, time.UTC)
	for _, session := range []Session{
		{ID: "target-intent", IssueID: "det", State: SessionStateRunning, UpdatedAt: now},
		{ID: "target-intent.pane-1", IssueID: "det", State: SessionStateRunning, UpdatedAt: now.Add(time.Second)},
		{ID: "historical", IssueID: "old", State: SessionStateStopped, UpdatedAt: now.Add(-time.Hour)},
	} {
		if err := store.UpsertSessionState(ctx, "project", session); err != nil {
			t.Fatalf("seed %s: %v", session.ID, err)
		}
	}

	sessions, err := store.ListSessionStatesByIssueIDs(ctx, "project", []string{"det"})
	if err != nil {
		t.Fatalf("ListSessionStatesByIssueIDs: %v", err)
	}
	if got, want := len(sessions), 2; got != want {
		t.Fatalf("scoped sessions = %d, want %d: %+v", got, want, sessions)
	}
	for _, session := range sessions {
		if session.IssueID != "det" {
			t.Fatalf("scoped query returned unrelated session: %+v", session)
		}
	}

	db, err := sql.Open("sqlite", store.dbPath)
	if err != nil {
		t.Fatalf("open query-plan db: %v", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `
		EXPLAIN QUERY PLAN
		SELECT session_id
		FROM (`+sessionProjectionUnionSQL()+`)
		WHERE project_id = ? AND issue_id IN (?)
	`, "project", "det")
	if err != nil {
		t.Fatalf("explain scoped session query: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		tablePlan := "SEARCH " + table + " USING "
		if !strings.Contains(plan.String(), tablePlan+"INDEX") &&
			!strings.Contains(plan.String(), tablePlan+"COVERING INDEX") {
			t.Fatalf("scoped query plan does not use issue index for %s:\n%s", table, plan.String())
		}
	}
}

func TestRuntimeStateStoreKeepsHookObservationSeparateFromCanonicalSession(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()
	now := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	started := now
	if err := store.UpsertSessionState(ctx, "proj-a", Session{
		ID:             "az-bja",
		IssueID:        "bja",
		State:          SessionStateRunning,
		ObservedState:  SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		StartedAt:      &started,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("UpsertSessionState parent: %v", err)
	}
	if err := store.UpsertSessionState(ctx, "proj-a", Session{
		ID:             "az-bja.pane-535",
		IssueID:        "bja",
		State:          SessionStatePaused,
		ObservedState:  SessionStatePaused,
		Activity:       "idle",
		ActivitySource: "hooks",
		StartedAt:      &started,
		UpdatedAt:      now.Add(time.Second),
	}); err != nil {
		t.Fatalf("UpsertSessionState pane: %v", err)
	}

	session, found, err := store.GetSessionState(ctx, "proj-a", "az-bja")
	if err != nil {
		t.Fatalf("GetSessionState parent: %v", err)
	}
	if !found {
		t.Fatal("expected canonical parent session")
	}
	if session.State != SessionStateRunning {
		t.Fatalf("parent state = %s, want existing lifecycle state preserved", session.State)
	}
	if session.Activity != "" || session.ActivitySource != "" {
		t.Fatalf("parent activity = %s/%s, want desired-only parent unaffected by pane observation", session.Activity, session.ActivitySource)
	}
}

func TestRuntimeStateStoreSessionActivityEvidenceRoundTrip(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()
	older := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	if err := store.UpsertSessionActivityEvidence(ctx, SessionActivityEvidence{
		ProjectID:       "proj-a",
		SessionID:       "az-bja",
		IssueID:         "bja",
		Activity:        "idle",
		ActivitySource:  "hooks",
		SourceSessionID: "az-bja.pane-535",
		Agent:           "codex",
		Hook:            "permission_request",
		Event:           "permission_request",
		ObservedAt:      newer,
		UpdatedAt:       newer,
	}); err != nil {
		t.Fatalf("UpsertSessionActivityEvidence newer: %v", err)
	}
	if err := store.UpsertSessionActivityEvidence(ctx, SessionActivityEvidence{
		ProjectID:       "proj-a",
		SessionID:       "az-bja",
		IssueID:         "bja",
		Activity:        "busy",
		ActivitySource:  "hooks",
		SourceSessionID: "az-bja.pane-122",
		ObservedAt:      older,
		UpdatedAt:       older,
	}); err != nil {
		t.Fatalf("UpsertSessionActivityEvidence older: %v", err)
	}
	if err := store.UpsertSessionActivityEvidence(ctx, SessionActivityEvidence{
		ProjectID:       "proj-a",
		SessionID:       "az-bja",
		IssueID:         "bja",
		Activity:        "idle",
		ActivitySource:  "hooks",
		SourceSessionID: "az-bja.pane-122",
		ObservedAt:      older.Add(-time.Minute),
		UpdatedAt:       older.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("UpsertSessionActivityEvidence same-source older: %v", err)
	}

	evidence, found, err := store.GetSessionActivityEvidence(ctx, "proj-a", "az-bja")
	if err != nil {
		t.Fatalf("GetSessionActivityEvidence: %v", err)
	}
	if !found {
		t.Fatal("expected session activity evidence")
	}
	if evidence.Activity != "busy" ||
		evidence.ActivitySource != "hooks" ||
		evidence.SourceSessionID != "az-bja.pane-122" ||
		!evidence.ObservedAt.Equal(newer) {
		t.Fatalf("evidence = %+v, want busy aggregate with newest observed timestamp", evidence)
	}

	listed, err := store.ListSessionActivityEvidence(ctx, "proj-a", []string{"bja"})
	if err != nil {
		t.Fatalf("ListSessionActivityEvidence: %v", err)
	}
	if got, want := len(listed), 2; got != want {
		t.Fatalf("listed evidence = %d, want %d: %+v", got, want, listed)
	}
}

func TestRuntimeStateStoreMigratesLegacyHookPaneObservationsToActivityEvidence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE daemon_session_projections (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			observed_state TEXT,
			activity TEXT,
			activity_source TEXT,
			tmux_attached_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		);
		CREATE TABLE daemon_session_observations (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			observed_state TEXT,
			activity TEXT,
			activity_source TEXT,
			tmux_attached_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		);
		INSERT INTO daemon_session_observations (
			project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at
		) VALUES
			('proj-a', 'az-bja.pane-111', 'bja', 'running', 'running', 'busy', 'hooks', 0, '2026-04-01T08:00:00Z', '2026-04-01T08:00:00Z'),
			('proj-a', 'az-bja.pane-535', 'bja', 'paused', 'paused', 'idle', 'hooks', 0, '2026-04-01T08:00:01Z', '2026-04-01T08:00:01Z'),
			('proj-a', 'az-bjc.pane-222', 'bjc', 'paused', 'paused', '', 'hooks', 0, '2026-04-01T08:00:02Z', '2026-04-01T08:00:02Z'),
			('proj-a', 'az-bjd.pane-333', 'bjd', 'paused', 'paused', 'idle', 'runtime', 0, '2026-04-01T08:00:03Z', '2026-04-01T08:00:03Z');
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy observations: %v", err)
	}
	_ = db.Close()

	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})
	ctx := context.Background()
	evidence, found, err := store.GetSessionActivityEvidence(ctx, "proj-a", "az-bja")
	if err != nil {
		t.Fatalf("GetSessionActivityEvidence bja: %v", err)
	}
	if !found {
		t.Fatal("expected migrated bja evidence")
	}
	if evidence.Activity != "busy" ||
		evidence.SourceSessionID != "az-bja.pane-111" ||
		evidence.ObservedAt.Format(time.RFC3339) != "2026-04-01T08:00:01Z" {
		t.Fatalf("bja evidence = %+v, want busy aggregate from migrated pane evidence", evidence)
	}
	listed, err := store.ListSessionActivityEvidence(ctx, "proj-a", []string{"bja"})
	if err != nil {
		t.Fatalf("ListSessionActivityEvidence bja: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("bja migrated evidence rows = %d, want 2: %+v", len(listed), listed)
	}
	evidence, found, err = store.GetSessionActivityEvidence(ctx, "proj-a", "az-bjc")
	if err != nil {
		t.Fatalf("GetSessionActivityEvidence bjc: %v", err)
	}
	if !found || evidence.Activity != "idle" {
		t.Fatalf("bjc evidence = %+v, found=%v, want idle fallback from paused state", evidence, found)
	}
	if _, found, err = store.GetSessionActivityEvidence(ctx, "proj-a", "az-bjd"); err != nil {
		t.Fatalf("GetSessionActivityEvidence bjd: %v", err)
	} else if found {
		t.Fatal("did not expect runtime-sourced pane observation to migrate as hook evidence")
	}
}

func TestRuntimeStateStoreLogicalIdentityRebuildRestoresOrderedSessionIndexes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	legacyTable := func(table string) string {
		return `CREATE TABLE ` + table + ` (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		);`
	}
	if _, err := db.Exec(
		legacyTable(sessionStateTable) +
			legacyTable(sessionObservationTable) + `
			CREATE INDEX idx_daemon_session_projections_project_issue_updated
				ON daemon_session_projections(project_id, issue_id, updated_at DESC, session_id DESC);
			CREATE INDEX idx_daemon_session_observations_project_issue_updated
				ON daemon_session_observations(project_id, issue_id, updated_at DESC, session_id DESC);
		`); err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy runtime schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy runtime schema: %v", err)
	}

	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ListSessionStates(context.Background(), "proj-a"); err != nil {
		t.Fatalf("open runtime store: %v", err)
	}
	handle, err := store.dbHandle()
	if err != nil {
		t.Fatalf("runtime db handle: %v", err)
	}
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		index := "idx_" + table + "_project_issue_updated"
		query := `SELECT session_id FROM ` + table + ` INDEXED BY ` + index + ` WHERE project_id=? AND issue_id=? ORDER BY updated_at DESC,session_id DESC`
		rows, err := handle.QueryContext(context.Background(), query, "proj-a", "issue-a")
		if err != nil {
			t.Fatalf("ordered query for %s after logical identity rebuild: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close ordered query for %s: %v", table, err)
		}
	}
}

func TestRuntimeStateStoreRealProjectDatabaseMigrationClones(t *testing.T) {
	rawPaths := strings.TrimSpace(os.Getenv("AZEDARACH_PROJECT_DB_CLONES"))
	if rawPaths == "" {
		t.Skip("AZEDARACH_PROJECT_DB_CLONES is not set")
	}
	for _, path := range filepath.SplitList(rawPaths) {
		path := path
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			if err := testisolation.CheckDatabaseClone(path, "."); err != nil {
				t.Fatalf("refuse unsafe project database clone before SQLite open: %v", err)
			}
			before, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
			if err != nil {
				t.Fatalf("open project database clone read-only: %v", err)
			}
			rowCounts := make(map[string]int)
			for _, table := range []string{sessionStateTable, sessionObservationTable} {
				var count int
				if err := before.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
					_ = before.Close()
					t.Fatalf("count %s before runtime-store open: %v", table, err)
				}
				rowCounts[table] = count
			}
			if err := before.Close(); err != nil {
				t.Fatalf("close project database clone read-only: %v", err)
			}

			assertOpen := func() {
				store := NewRuntimeStateStoreAtPath(path, slog.Default())
				handle, err := store.dbHandle()
				if err != nil {
					t.Fatalf("open runtime store against project database clone: %v", err)
				}
				for _, table := range []string{sessionStateTable, sessionObservationTable} {
					index := "idx_" + table + "_project_issue_updated"
					query := `SELECT session_id FROM ` + table + ` INDEXED BY ` + index + ` ORDER BY project_id,issue_id,updated_at DESC,session_id DESC LIMIT 1`
					rows, err := handle.QueryContext(context.Background(), query)
					if err != nil {
						_ = store.Close()
						t.Fatalf("ordered runtime read for %s: %v", table, err)
					}
					if err := rows.Close(); err != nil {
						_ = store.Close()
						t.Fatalf("close ordered runtime read for %s: %v", table, err)
					}
					var count int
					if err := handle.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
						_ = store.Close()
						t.Fatalf("count %s after runtime-store open: %v", table, err)
					}
					if count != rowCounts[table] {
						_ = store.Close()
						t.Fatalf("%s rows = %d after runtime-store open, want %d", table, count, rowCounts[table])
					}
				}
				if err := store.Close(); err != nil {
					t.Fatalf("close runtime store: %v", err)
				}
			}
			assertOpen()
			assertOpen()
		})
	}
}

func TestRuntimeStateStoreMigratesActivityEvidencePrimaryKeyToSourceSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE daemon_session_activity_evidence (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			activity TEXT NOT NULL,
			activity_source TEXT NOT NULL,
			source_session_id TEXT,
			agent TEXT,
			hook TEXT,
			event TEXT,
			observed_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		);
		INSERT INTO daemon_session_activity_evidence (
			project_id, session_id, issue_id, activity, activity_source, source_session_id, agent, hook, event, observed_at, updated_at
		) VALUES (
			'proj-a', 'az-bja', 'bja', 'idle', 'hooks', 'az-bja.pane-535', 'codex', 'permission_request', 'permission_request', '2026-04-01T08:00:01Z', '2026-04-01T08:00:01Z'
		);
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed old activity evidence schema: %v", err)
	}
	_ = db.Close()

	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})
	ctx := context.Background()
	if err := store.UpsertSessionActivityEvidence(ctx, SessionActivityEvidence{
		ProjectID:       "proj-a",
		SessionID:       "az-bja",
		IssueID:         "bja",
		Activity:        "busy",
		ActivitySource:  "hooks",
		SourceSessionID: "az-bja.pane-122",
		ObservedAt:      time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("upsert second source activity evidence: %v", err)
	}

	listed, err := store.ListSessionActivityEvidence(ctx, "proj-a", []string{"bja"})
	if err != nil {
		t.Fatalf("ListSessionActivityEvidence: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("activity evidence rows = %d, want 2: %+v", len(listed), listed)
	}
	evidence, found, err := store.GetSessionActivityEvidence(ctx, "proj-a", "az-bja")
	if err != nil {
		t.Fatalf("GetSessionActivityEvidence: %v", err)
	}
	if !found || evidence.Activity != "busy" || evidence.SourceSessionID != "az-bja.pane-122" {
		t.Fatalf("aggregate evidence = %+v, found=%v, want busy from second source", evidence, found)
	}
}

func TestRuntimeStateStoreMigratesLegacyPaneRowsToObservations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE daemon_session_projections (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			observed_state TEXT,
			activity TEXT,
			activity_source TEXT,
			tmux_attached_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		);
		INSERT INTO daemon_session_projections (
			project_id, session_id, issue_id, state, observed_state, activity, activity_source, tmux_attached_count, started_at, updated_at
		) VALUES
			('proj-a', 'az-bja', 'bja', 'running', 'running', '', '', 0, '2026-04-01T08:00:00Z', '2026-04-01T08:00:00Z'),
			('proj-a', 'az-bja.pane-535', 'bja', 'running', 'stopped', 'busy', 'runtime', 0, '2026-04-01T08:00:01Z', '2026-04-01T08:00:01Z');
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy schema: %v", err)
	}
	_ = db.Close()

	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})
	ctx := context.Background()
	allRows, err := store.ListSessionStates(ctx, "proj-a")
	if err != nil {
		t.Fatalf("ListSessionStates: %v", err)
	}
	if got, want := len(allRows), 2; got != want {
		t.Fatalf("all session rows = %d, want %d: %+v", got, want, allRows)
	}
	intentRows, err := store.ListSessionIntentStates(ctx, "proj-a")
	if err != nil {
		t.Fatalf("ListSessionIntentStates: %v", err)
	}
	if got, want := len(intentRows), 1; got != want {
		t.Fatalf("intent session rows = %d, want %d: %+v", got, want, intentRows)
	}
	if intentRows[0].ID != "az-bja" {
		t.Fatalf("intent row = %+v, want parent", intentRows[0])
	}
	verifyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open migrated sqlite: %v", err)
	}
	defer verifyDB.Close()
	var legacyPaneCount, observationPaneCount int
	if err := verifyDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_session_projections WHERE session_id = 'az-bja.pane-535'`).Scan(&legacyPaneCount); err != nil {
		t.Fatalf("count legacy pane rows: %v", err)
	}
	if err := verifyDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_session_observations WHERE session_id = 'az-bja.pane-535'`).Scan(&observationPaneCount); err != nil {
		t.Fatalf("count migrated pane rows: %v", err)
	}
	if legacyPaneCount != 0 || observationPaneCount != 1 {
		t.Fatalf("pane physical rows legacy=%d observation=%d, want 0/1", legacyPaneCount, observationPaneCount)
	}
}

func TestRuntimeStateStoreWorktreeReplaceAndList(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	if err := store.ReplaceWorktreeStates(context.Background(), "proj-a", []WorktreeState{
		{ProjectID: "proj-a", IssueID: "bja", Path: "/tmp/repo-bja", Branch: "riordan/bja/task", UpdatedAt: now},
		{ProjectID: "proj-a", IssueID: "bjb", Path: "/tmp/repo-bjb", Branch: "riordan/bjb/task", UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceWorktreeStates: %v", err)
	}

	worktrees, err := store.ListWorktreeStates(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListWorktreeStates: %v", err)
	}
	if got, want := len(worktrees), 2; got != want {
		t.Fatalf("worktrees count = %d, want %d", got, want)
	}

	if err := store.UpsertWorktreeState(context.Background(), WorktreeState{
		ProjectID: "proj-a",
		IssueID:   "bja",
		Path:      "/tmp/repo-bja-updated",
		Branch:    "riordan/bja/updated",
		UpdatedAt: now.Add(1 * time.Minute),
	}); err != nil {
		t.Fatalf("UpsertWorktreeState: %v", err)
	}
	worktrees, err = store.ListWorktreeStates(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListWorktreeStates after upsert: %v", err)
	}
	found := false
	for _, wt := range worktrees {
		if wt.IssueID != "bja" {
			continue
		}
		found = true
		if got, want := wt.Path, "/tmp/repo-bja-updated"; got != want {
			t.Fatalf("bja path = %q, want %q", got, want)
		}
	}
	if !found {
		t.Fatal("expected bja worktree projection")
	}

	if err := store.DeleteWorktreeState(context.Background(), "proj-a", "bja"); err != nil {
		t.Fatalf("DeleteWorktreeState: %v", err)
	}
	worktrees, err = store.ListWorktreeStates(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListWorktreeStates after delete: %v", err)
	}
	if got, want := len(worktrees), 1; got != want {
		t.Fatalf("worktrees after delete = %d, want %d", got, want)
	}
}

func TestRuntimeStateStoreRejectsInvalidSessionProducts(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	cases := []Session{
		{ID: "negative", IssueID: "a", State: SessionStateRunning, TmuxAttachedCount: -1},
		{ID: "stopped-attached", IssueID: "a", State: SessionStateStopped, TmuxAttachedCount: 1},
		{ID: "project-with-issue", IssueID: "a", Role: SessionRoleOrchestrator, ScopeKind: SessionScopeOrchestration, ScopeID: "project", State: SessionStateRunning},
		{ID: "root-mismatch", IssueID: "a", Role: SessionRoleOrchestrator, ScopeKind: SessionScopeOrchestration, ScopeID: "b", State: SessionStateRunning},
	}
	for _, session := range cases {
		if err := store.UpsertSessionState(ctx, "project", session); err == nil {
			t.Fatalf("UpsertSessionState(%s) accepted invalid product", session.ID)
		}
	}
	valid := Session{ID: "project", Role: SessionRoleOrchestrator, ScopeKind: SessionScopeOrchestration, ScopeID: "project", State: SessionStateRunning}
	if err := store.UpsertSessionState(ctx, "project", valid); err != nil {
		t.Fatalf("valid project orchestrator: %v", err)
	}
	valid.ID = "project-duplicate"
	if err := store.UpsertSessionState(ctx, "project", valid); err != nil {
		t.Fatalf("logical project orchestrator runtime reassociation: %v", err)
	}
	rows, err := store.ListSessionIntentStates(ctx, "project")
	if err != nil || len(rows) != 1 || rows[0].ID != "project-duplicate" {
		t.Fatalf("logical project orchestrator rows=%+v err=%v", rows, err)
	}
	db, err := store.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE daemon_session_projections SET tmux_attached_count=-1 WHERE session_id='project-duplicate'`); err == nil {
		t.Fatal("direct SQL bypassed authoritative session product trigger")
	}
}

func TestRuntimeStateStoreUpgradeFailsClosedOnInvalidHistoricalSessionProduct(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE daemon_session_projections(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id)); INSERT INTO daemon_session_projections VALUES('p','bad','','orchestrator','orchestration','root','running','running',0,'2026-07-13T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ListSessionStates(context.Background(), "p"); err == nil || !strings.Contains(err.Error(), "invalid historical runtime authority") {
		t.Fatalf("upgrade error=%v", err)
	}
}

func TestRuntimeStateStoreUpgradeCanonicalizesDuplicateLogicalSessions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(schema(sessionStateTable) + schema(sessionObservationTable) + `
		INSERT INTO daemon_session_projections VALUES
		('p','pr-worker','worker','worker','issue','worker','running','running','idle','hooks',0,'2026-07-13T00:00:00Z','2026-07-13T00:00:00Z'),
		('p','worker-new','worker','worker','issue','worker','paused','paused','busy','hooks',0,'2026-07-13T00:01:00Z','2026-07-13T00:02:00Z'),
		('p','pr-orchestrator-project','','orchestrator','orchestration','project','running','running','idle','',0,'2026-07-13T00:00:00Z','2026-07-13T00:00:00Z'),
		('p','project-new','','orchestrator','orchestration','project','paused','paused','busy','hooks',0,'2026-07-13T00:01:00Z','2026-07-13T00:02:00Z'),
		('p','pr-root','root','orchestrator','orchestration','root','running','running','idle','',0,'2026-07-13T00:00:00Z','2026-07-13T00:00:00Z'),
		('p','root-new','root','orchestrator','orchestration','root','paused','paused','busy','hooks',0,'2026-07-13T00:01:00Z','2026-07-13T00:02:00Z');
		INSERT INTO daemon_session_observations VALUES
		('p','observation-old','worker','worker','issue','worker','running','running','idle','',0,'2026-07-13T00:00:00Z','2026-07-13T00:00:00Z'),
		('p','observation-new','worker','worker','issue','worker','paused','paused','busy','hooks',0,'2026-07-13T00:01:00Z','2026-07-13T00:02:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	rows, err := store.ListSessionIntentStates(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("canonical rows=%d want 3: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.State != SessionStatePaused || row.Activity != "busy" || !row.StartedAt.Equal(time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("merged row=%+v", row)
		}
	}
	all, err := store.ListSessionStates(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("projection plus canonical observation rows=%d want 4", len(all))
	}
	for _, row := range all {
		if row.Role == SessionRoleWorker && row.State == SessionStatePaused && row.ID != "pr-worker" {
			t.Fatalf("desired/observed runtime association diverged: %+v", all)
		}
	}
}

func TestRuntimeStateStoreEnforcesRelationalSessionIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY); CREATE TABLE interaction_requests(id TEXT PRIMARY KEY,issue_id TEXT NOT NULL); INSERT INTO issues VALUES('a'),('b'); INSERT INTO interaction_requests VALUES('request-a','a')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.UpsertSessionState(ctx, "p", Session{ID: "worker-missing", IssueID: "missing", State: SessionStateRunning}); err == nil {
		t.Fatal("worker orphan accepted")
	}
	if err := store.UpsertSessionState(ctx, "p", Session{ID: "advisor-mismatch", IssueID: "b", Role: SessionRoleAdvisor, ScopeKind: SessionScopeInteraction, ScopeID: "request-a", State: SessionStateRunning}); err == nil {
		t.Fatal("advisor interaction/issue mismatch accepted")
	}
	if err := store.UpsertSessionState(ctx, "p", Session{ID: "advisor-valid", IssueID: "a", Role: SessionRoleAdvisor, ScopeKind: SessionScopeInteraction, ScopeID: "request-a", State: SessionStateRunning}); err != nil {
		t.Fatalf("valid advisor: %v", err)
	}
	handle, err := store.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx, `UPDATE daemon_session_projections SET issue_id='b' WHERE session_id='advisor-valid'`); err == nil {
		t.Fatal("direct advisor retarget bypassed relational guard")
	}
}

func TestCanonicalizeRuntimeLogicalSessionsPreservesArchivedRowsWithoutReinserting(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY,visibility TEXT NOT NULL);` + schema(sessionStateTable) + schema(sessionObservationTable) + `
		INSERT INTO issues VALUES('archived','archived'),('live','live');
		INSERT INTO daemon_session_projections VALUES
		('p','archived-old','archived','worker','issue','archived','running','running','', '',0,NULL,'2026-07-13T00:00:00Z'),
		('p','archived-new','archived','worker','issue','archived','stopped','stopped','', '',0,NULL,'2026-07-13T00:01:00Z'),
		('p','live-old','live','worker','issue','live','running','running','', '',0,NULL,'2026-07-13T00:00:00Z'),
		('p','live-new','live','worker','issue','live','paused','paused','', '',0,NULL,'2026-07-13T00:01:00Z');
		INSERT INTO daemon_session_observations VALUES
		('p','archived-observation','archived','worker','issue','archived','running','running','', '',0,NULL,'2026-07-13T00:02:00Z'),
		('p','archived-advisor','archived','advisor','interaction','request-archived','running','running','', '',0,NULL,'2026-07-13T00:03:00Z');
		CREATE TRIGGER reject_archived_session_insert BEFORE INSERT ON daemon_session_projections
		WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id AND visibility='archived')
		BEGIN SELECT RAISE(ABORT,'cannot attach session to archived issue'); END;
		CREATE TRIGGER reject_archived_observation_insert BEFORE INSERT ON daemon_session_observations
		WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id AND visibility='archived')
		BEGIN SELECT RAISE(ABORT,'cannot attach observation to archived issue'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := canonicalizeRuntimeLogicalSessions(context.Background(), db); err != nil {
		t.Fatalf("canonicalize with archived legacy rows: %v", err)
	}
	var archivedRows, archivedObservations, liveRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM daemon_session_projections WHERE issue_id='archived'`).Scan(&archivedRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM daemon_session_projections WHERE issue_id='live'`).Scan(&liveRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM daemon_session_observations WHERE issue_id='archived'`).Scan(&archivedObservations); err != nil {
		t.Fatal(err)
	}
	if archivedRows != 2 || archivedObservations != 2 || liveRows != 1 {
		t.Fatalf("rows after canonicalization archived projections/observations/live=%d/%d/%d want 2/2/1", archivedRows, archivedObservations, liveRows)
	}
	var liveSessionID string
	if err := db.QueryRow(`SELECT session_id FROM daemon_session_projections WHERE issue_id='live'`).Scan(&liveSessionID); err != nil {
		t.Fatal(err)
	}
	if liveSessionID != "live-new" {
		t.Fatalf("live winner=%q want live-new", liveSessionID)
	}
}

func TestCanonicalizeRuntimeLogicalSessionsSupportsRootDeletedAtArchiveAuthority(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY,deleted_at TEXT);` + schema(sessionStateTable) + schema(sessionObservationTable) + `
		INSERT INTO issues VALUES('archived','2026-07-13T00:00:00Z'),('live',NULL);
		INSERT INTO daemon_session_projections VALUES
		('p','archived','archived','worker','issue','archived','running','running','', '',0,NULL,'2026-07-13T00:00:00Z'),
		('p','live-old','live','worker','issue','live','running','running','', '',0,NULL,'2026-07-13T00:00:00Z'),
		('p','live-new','live','worker','issue','live','paused','paused','', '',0,NULL,'2026-07-13T00:01:00Z');
		CREATE TRIGGER reject_deleted_session_insert BEFORE INSERT ON daemon_session_projections
		WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id AND deleted_at IS NOT NULL)
		BEGIN SELECT RAISE(ABORT,'cannot attach session to archived issue'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := canonicalizeRuntimeLogicalSessions(context.Background(), db); err != nil {
		t.Fatalf("canonicalize root-shaped archive authority: %v", err)
	}
	var archivedRows, liveRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM daemon_session_projections WHERE issue_id='archived'`).Scan(&archivedRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM daemon_session_projections WHERE issue_id='live'`).Scan(&liveRows); err != nil {
		t.Fatal(err)
	}
	if archivedRows != 1 || liveRows != 1 {
		t.Fatalf("root-shaped rows archived/live=%d/%d want 1/1", archivedRows, liveRows)
	}
}

func TestRuntimeStateStoreOpensRootShapeWithDuplicateArchivedLogicalSessions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY,deleted_at TEXT);` + schema(sessionStateTable) + schema(sessionObservationTable) + `
		INSERT INTO issues VALUES('archived','2026-07-13T00:00:00Z'),('live',NULL);
		INSERT INTO daemon_session_projections VALUES
		('p','archived-old','archived','worker','issue','archived','running','running','', '',0,NULL,'2026-07-13T00:00:00Z'),
		('p','archived-new','archived','worker','issue','archived','stopped','stopped','', '',0,NULL,'2026-07-13T00:01:00Z'),
		('p','orphan','orphan','worker','issue','orphan','running','running','', '',0,NULL,'2026-07-13T00:01:00Z'),
		('p','live','live','worker','issue','live','running','running','', '',0,NULL,'2026-07-13T00:00:00Z');
		CREATE TRIGGER reject_deleted_session_insert BEFORE INSERT ON daemon_session_projections
		WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id AND deleted_at IS NOT NULL)
		BEGIN SELECT RAISE(ABORT,'cannot attach session to archived issue'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ListSessionStates(context.Background(), "p"); err != nil {
		t.Fatalf("open root-shaped runtime store: %v", err)
	}
	handle, err := store.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var archivedRows int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM daemon_session_projections WHERE issue_id='archived'`).Scan(&archivedRows); err != nil {
		t.Fatal(err)
	}
	if archivedRows != 1 {
		t.Fatalf("archived logical rows=%d want 1", archivedRows)
	}
	if _, err := handle.Exec(`INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','archived-third','archived','worker','issue','archived','running','2026-07-13T00:02:00Z')`); err == nil || !strings.Contains(err.Error(), "cannot attach session to archived issue") {
		t.Fatalf("restored archive guard error=%v", err)
	}
	if _, err := handle.Exec(`INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','orphan-new','missing','worker','issue','missing','running','2026-07-13T00:02:00Z')`); err == nil || !strings.Contains(err.Error(), "session issue does not exist") {
		t.Fatalf("new orphan guard error=%v", err)
	}
}

func TestRuntimeStateStoreReopensWithMissingIssueRuntimeProjection(t *testing.T) {
	recorder := newRuntimeRepairTraceRecorder(t)
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY,visibility TEXT NOT NULL);` + schema(sessionStateTable) + schema(sessionObservationTable) + `
		INSERT INTO daemon_session_projections VALUES
		('old-chefy','ch-dkx','dkx','worker','issue','dkx','stopped','stopped','','',0,NULL,'2026-07-09T03:22:36Z'),
		('chefy','ch-dkx','dkx','worker','issue','dkx','stopped','stopped','','',0,NULL,'2026-07-15T00:27:33Z');
		INSERT INTO daemon_session_observations VALUES
		('chefy','ch-dkx.pane-1700','dkx','worker','issue','dkx','stopped','stopped','','',0,NULL,'2026-07-09T03:22:36Z'),
		('chefy','ch-dkx.pane-2034','dkx','worker','issue','dkx','stopped','stopped','','',0,NULL,'2026-07-09T03:22:36Z');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	probeCount := 0
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default(), WithRuntimeLivenessProbe(func(context.Context, string) (bool, error) {
		probeCount++
		return false, nil
	}))
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ListSessionStates(context.Background(), "chefy"); err != nil {
		t.Fatalf("reopen with inactive missing-issue projection: %v", err)
	}
	if probeCount != 1 {
		t.Fatalf("liveness probes=%d want one consistent ch-dkx classification", probeCount)
	}
	assertRuntimeRepairSpans(t, recorder, "daemon.runtime_projection.orphan_classification", "daemon.runtime_projection.orphan_repair")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSessionStates(context.Background(), "chefy"); err != nil {
		t.Fatalf("second reopen after missing-issue repair: %v", err)
	}
	handle, err := store.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		var count int
		if err := handle.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE issue_id='dkx'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s orphan rows=%d want 0", table, count)
		}
	}
}

func newRuntimeRepairTraceRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	t.Setenv(latencytrace.EnvVar, "")
	t.Setenv(observability.EnvVar, "true")
	latencytrace.SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		latencytrace.SetConfigEnabled(false)
	})
	return recorder
}

func assertRuntimeRepairSpans(t *testing.T, recorder *tracetest.SpanRecorder, names ...string) {
	t.Helper()
	found := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range recorder.Ended() {
		found[span.Name()] = span
	}
	for _, name := range names {
		span := found[name]
		if span == nil {
			t.Fatalf("missing span %q; ended=%v", name, found)
		}
		attrs := map[string]bool{}
		for _, attr := range span.Attributes() {
			attrs[string(attr.Key)] = true
		}
		for _, key := range []string{"component", "phase", "outcome"} {
			if !attrs[key] {
				t.Fatalf("span %q missing attribute %q", name, key)
			}
		}
	}
}

func TestRuntimeStateStoreFailsClosedForUnverifiableMissingIssueRuntime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY,visibility TEXT NOT NULL);` + schema(sessionStateTable) + schema(sessionObservationTable) + `
		INSERT INTO daemon_session_projections VALUES
		('chefy','ch-live','live','worker','issue','live','running','running','busy','hooks',0,NULL,'2026-07-15T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ListSessionStates(context.Background(), "chefy"); err == nil || !strings.Contains(err.Error(), "ch-live: issue does not exist") {
		t.Fatalf("live missing-issue error=%v", err)
	}
}

func TestRuntimeStateStoreFailsClosedForLiveMissingIssueRuntime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY,visibility TEXT NOT NULL);` + schema(sessionStateTable) + schema(sessionObservationTable) + `
		INSERT INTO daemon_session_observations VALUES
		('chefy','ch-dkx.pane-1700','dkx','worker','issue','dkx','stopped','stopped','','',0,NULL,'2026-07-09T03:22:36Z');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var probed string
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default(), WithRuntimeLivenessProbe(func(_ context.Context, sessionID string) (bool, error) {
		probed = sessionID
		return true, nil
	}))
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ListSessionStates(context.Background(), "chefy"); err == nil || !strings.Contains(err.Error(), "runtime ch-dkx is live") {
		t.Fatalf("live terminal orphan error=%v", err)
	}
	if probed != "ch-dkx" {
		t.Fatalf("liveness probe session=%q want ch-dkx", probed)
	}
}

func TestRuntimeStateStoreMissingIssueRepairRollsBackMidBatchFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL,scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(`CREATE TABLE issues(id TEXT PRIMARY KEY,visibility TEXT NOT NULL);` + schema(sessionStateTable) + schema(sessionObservationTable) + `
		INSERT INTO daemon_session_projections VALUES ('chefy','ch-dkx','dkx','worker','issue','dkx','stopped','stopped','','',0,NULL,'2026-07-09T03:22:36Z');
		INSERT INTO daemon_session_observations VALUES ('chefy','ch-dkx.pane-1700','dkx','worker','issue','dkx','stopped','stopped','','',0,NULL,'2026-07-09T03:22:36Z');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default(),
		WithRuntimeLivenessProbe(func(context.Context, string) (bool, error) { return false, nil }),
		WithRuntimeRepairDeleteHookForTest(func(_, _ string, index int) error {
			if index == 1 {
				return errors.New("injected mid-batch failure")
			}
			return nil
		}),
	)
	if _, err := store.ListSessionStates(context.Background(), "chefy"); err == nil || !strings.Contains(err.Error(), "injected mid-batch failure") {
		t.Fatalf("repair error=%v", err)
	}
	verify, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		var count int
		if err := verify.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE issue_id='dkx'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s rows after rollback=%d want 1", table, count)
		}
	}
}

func TestRuntimeStateStoreCandidateCloneOpenAndReopen(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("AZEDARACH_RUNTIME_CLONE_DB"))
	if path == "" {
		t.Skip("AZEDARACH_RUNTIME_CLONE_DB not set")
	}
	store := NewRuntimeStateStoreAtPath(path, slog.Default(), WithRuntimeLivenessProbe(func(context.Context, string) (bool, error) { return false, nil }))
	if _, err := store.ListProjectIDs(context.Background()); err != nil {
		t.Fatalf("candidate clone open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProjectIDs(context.Background()); err != nil {
		t.Fatalf("candidate clone idempotent reopen: %v", err)
	}
	if issueID := strings.TrimSpace(os.Getenv("AZEDARACH_RUNTIME_CLONE_ABSENT_ISSUE")); issueID != "" {
		db, err := store.dbHandle()
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{sessionStateTable, sessionObservationTable} {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE issue_id=?`, issueID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("%s absent-issue rows=%d want 0", table, count)
			}
		}
	}
}

func TestRuntimeStateStorePaneMigrationReplacesStaleObservationMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := func(table string) string {
		return `CREATE TABLE ` + table + `(project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL DEFAULT 'worker',scope_kind TEXT NOT NULL DEFAULT 'issue',scope_id TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));`
	}
	if _, err := db.Exec(schema(sessionStateTable) + schema(sessionObservationTable) + `INSERT INTO daemon_session_observations(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','advisor.pane-1','a','worker','issue','a','running','2026-07-13T00:00:00Z'); INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES('p','advisor.pane-1','a','advisor','interaction','request-a','running','2026-07-13T00:01:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	session, found, err := store.GetSessionState(context.Background(), "p", "advisor.pane-1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if session.Role != SessionRoleAdvisor || session.ScopeKind != SessionScopeInteraction || session.ScopeID != "request-a" || session.IssueID != "a" {
		t.Fatalf("migrated observation=%+v", session)
	}
}

func TestRuntimeStateStoreWorktreeGetters(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Date(2026, time.April, 1, 8, 45, 0, 0, time.UTC)
	if err := store.ReplaceWorktreeStates(context.Background(), "proj-a", []WorktreeState{
		{ProjectID: "proj-a", IssueID: "bja", Path: "/tmp/repo-bja", Branch: "riordan/bja/task", UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceWorktreeStates: %v", err)
	}

	worktreeState, found, err := store.GetWorktreeStateByPath(context.Background(), "proj-a", "/tmp/repo-bja")
	if err != nil {
		t.Fatalf("GetWorktreeStateByPath: %v", err)
	}
	if !found {
		t.Fatal("expected worktree state by path")
	}
	if worktreeState.IssueID != "bja" {
		t.Fatalf("worktree state by path = %+v", worktreeState)
	}

	worktreeState, found, err = store.GetWorktreeStateByIssueID(context.Background(), "proj-a", "bja")
	if err != nil {
		t.Fatalf("GetWorktreeStateByIssueID: %v", err)
	}
	if !found {
		t.Fatal("expected worktree state by issue id")
	}
	if worktreeState.Path != "/tmp/repo-bja" {
		t.Fatalf("worktree state by issue = %+v", worktreeState)
	}
}

func TestRuntimeStateStoreWorktreeGitStatusUpdateGuardrail(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	createdAt := time.Date(2026, time.April, 1, 9, 5, 0, 0, time.UTC)
	if err := store.UpsertWorktreeState(context.Background(), WorktreeState{
		ProjectID: "proj-a",
		IssueID:   "bja",
		Path:      "/tmp/repo-bja",
		Branch:    "riordan/bja/task",
		UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("UpsertWorktreeState: %v", err)
	}

	statusAt := time.Date(2026, time.April, 1, 9, 10, 0, 0, time.UTC)
	rawStatus := json.RawMessage(`{"clean":false,"modified":["README.md"]}`)
	if err := store.UpsertWorktreeStateGitStatus(context.Background(), "proj-a", "bja", rawStatus, statusAt); err != nil {
		t.Fatalf("UpsertWorktreeStateGitStatus existing row: %v", err)
	}

	projection, found, err := store.GetWorktreeStateByIssueID(context.Background(), "proj-a", "bja")
	if err != nil {
		t.Fatalf("GetWorktreeStateByIssueID: %v", err)
	}
	if !found {
		t.Fatal("expected worktree projection")
	}
	if got, want := string(projection.GitStatusRaw), string(rawStatus); got != want {
		t.Fatalf("git status payload = %s, want %s", got, want)
	}
	if projection.GitStatusUpdated == nil || !projection.GitStatusUpdated.Equal(statusAt) {
		t.Fatalf("git status updated at = %v, want %v", projection.GitStatusUpdated, statusAt)
	}

	err = store.UpsertWorktreeStateGitStatus(context.Background(), "proj-a", "missing", json.RawMessage(`{"clean":true}`), statusAt)
	if err == nil {
		t.Fatal("UpsertWorktreeStateGitStatus missing row: expected error")
	}
	if got := err.Error(); !strings.Contains(got, "expected 1 affected row(s), got 0") {
		t.Fatalf("UpsertWorktreeStateGitStatus missing row error = %q, want affected-row guardrail", got)
	}
}

func TestRuntimeStateStoreGitHookPublicationBindsGenerationToExactWorktree(t *testing.T) {
	ctx := context.Background()
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	projectID := "proj-hook-binding"
	issueID := "az-hook-binding"
	projectedPath := "/repo/current-worktree"
	acceptedPath := "/repo/stale-worktree"
	if err := store.UpsertWorktreeState(ctx, WorktreeState{ProjectID: projectID, IssueID: issueID, Path: projectedPath, Branch: "az/hook-binding", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	intent, err := store.AcceptGitHookRefresh(ctx, projectID, acceptedPath, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if published, err := store.PersistGitHookRefreshPublication(ctx, projectID, issueID, acceptedPath, intent.RequestedGeneration, json.RawMessage(`{"has_changes":true}`), time.Now().UTC()); err == nil || published {
		t.Fatalf("stale-path publication published=%t err=%v, want transactional failure", published, err)
	}
	if pending, found, err := store.GetPendingGitHookRefresh(ctx, projectID, acceptedPath); err != nil || !found || pending.CompletedGeneration != 0 {
		t.Fatalf("pending intent after rolled-back stale path = %+v found=%t err=%v", pending, found, err)
	}
}

func TestRuntimeStateStoreGitStatusRoundTrip(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	rawStatus, err := json.Marshal(map[string]any{
		"has_changes": true,
		"modified":    []string{"README.md"},
	})
	if err != nil {
		t.Fatalf("json.Marshal status: %v", err)
	}

	if err := store.UpsertWorktreeState(context.Background(), WorktreeState{
		ProjectID: "proj-a",
		IssueID:   "bja",
		Path:      "/tmp/repo-bja",
		Branch:    "riordan/bja/task",
		UpdatedAt: time.Date(2026, time.April, 1, 8, 55, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertWorktreeState: %v", err)
	}

	if err := store.UpsertWorktreeStateGitStatus(
		context.Background(),
		"proj-a",
		"bja",
		rawStatus,
		time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("UpsertWorktreeStateGitStatus: %v", err)
	}

	projection, found, err := store.GetWorktreeStateByPath(context.Background(), "proj-a", "/tmp/repo-bja")
	if err != nil {
		t.Fatalf("GetWorktreeStateByPath: %v", err)
	}
	if !found {
		t.Fatal("expected worktree projection")
	}
	if projection.Path != "/tmp/repo-bja" {
		t.Fatalf("path = %q, want /tmp/repo-bja", projection.Path)
	}
	if len(projection.GitStatusRaw) == 0 {
		t.Fatal("status payload should not be empty")
	}
}

func TestRuntimeStateStoreListProjectIDs(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	ctx := context.Background()
	if err := store.UpsertSessionState(ctx, "proj-b", Session{
		ID:        "sess-b",
		IssueID:   "az-b",
		State:     SessionStateAttached,
		UpdatedAt: time.Date(2026, time.April, 2, 8, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertSessionState proj-b: %v", err)
	}
	if err := store.UpsertWorktreeState(ctx, WorktreeState{
		ProjectID: " proj-a ",
		IssueID:   "az-a",
		Path:      "/tmp/repo-az-a",
		Branch:    "riordan/az-a/task",
		UpdatedAt: time.Date(2026, time.April, 2, 8, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertWorktreeState proj-a: %v", err)
	}
	if _, _, err := store.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{
		ProjectID: "proj-c", SessionID: "orphan-runtime", ObservedState: SessionStateRunning,
		UpdatedAt: time.Date(2026, time.April, 2, 8, 2, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ApplyPhysicalSessionObservation proj-c: %v", err)
	}

	got, err := store.ListProjectIDs(ctx)
	if err != nil {
		t.Fatalf("ListProjectIDs: %v", err)
	}
	want := []string{"proj-a", "proj-b", "proj-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListProjectIDs() = %v, want %v", got, want)
	}
}

func TestRuntimeStateStorePhysicalObservationConstraints(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	want := PhysicalSessionObservation{
		ProjectID: "p", SessionID: "az-root", ObservedState: SessionStatePaused,
		Activity: "waiting", ActivitySource: "hooks", UpdatedAt: time.Now().UTC(),
	}
	if _, applied, err := store.ApplyPhysicalSessionObservation(ctx, want); err != nil || !applied {
		t.Fatal(err)
	}
	got, found, err := store.GetPhysicalSessionObservation(ctx, "p", "az-root")
	if err != nil || !found || got.ObservedState != want.ObservedState || got.Activity != want.Activity {
		t.Fatalf("observation=%+v found=%v err=%v", got, found, err)
	}
	db, err := store.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO daemon_physical_session_observations(project_id,session_id,observed_state,activity,activity_source,updated_at) VALUES('p','bad','stopped','busy','hooks',?)`, time.Now().UTC().Format(time.RFC3339Nano)); err == nil {
		t.Fatal("direct SQL accepted stopped physical observation with activity")
	}
}

func TestRuntimeStateStoreTmuxAttachmentObservationIsOptionalAndPreservedAcrossHooks(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	seed := Session{ID: "az-root", IssueID: "root", Role: SessionRoleWorker, ScopeKind: SessionScopeIssue, ScopeID: "root", State: SessionStateRunning, ObservedState: SessionStateRunning, UpdatedAt: now.Add(-time.Minute)}
	if err := store.UpsertSessionState(ctx, "p", seed); err != nil {
		t.Fatal(err)
	}
	attached := 2
	startedAt := now.Add(-time.Hour)
	if _, applied, err := store.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{ProjectID: "p", SessionID: seed.ID, ObservedState: SessionStateRunning, UpdatedAt: now, TmuxAttachedCount: &attached, StartedAt: &startedAt}); err != nil || !applied {
		t.Fatalf("apply tmux attachment observation: applied=%v err=%v", applied, err)
	}
	if _, applied, err := store.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{ProjectID: "p", SessionID: seed.ID, ObservedState: SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: now.Add(time.Second)}); err != nil || !applied {
		t.Fatalf("apply hook observation: applied=%v err=%v", applied, err)
	}
	got, found, err := store.GetSessionState(ctx, "p", seed.ID)
	if err != nil || !found || got.TmuxAttachedCount != attached || got.StartedAt == nil || !got.StartedAt.Equal(startedAt) || got.Activity != "busy" {
		t.Fatalf("session=%+v found=%v err=%v", got, found, err)
	}
}

func TestRuntimeStateStoreUntypedSharedRuntimeMutationFailsClosed(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	for _, seed := range []Session{
		{ID: "az-root", IssueID: "root", Role: SessionRoleWorker, ScopeKind: SessionScopeIssue, ScopeID: "root", State: SessionStateRunning, UpdatedAt: now},
		{ID: "az-root", IssueID: "root", Role: SessionRoleOrchestrator, ScopeKind: SessionScopeOrchestration, ScopeID: "root", State: SessionStateRunning, UpdatedAt: now},
	} {
		if err := store.UpsertSessionState(ctx, "p", seed); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertSessionState(ctx, "p", Session{ID: "az-root", IssueID: "root", State: SessionStatePaused, UpdatedAt: now}); err == nil {
		t.Fatal("untyped mutation of shared physical runtime succeeded")
	}
}

func TestRuntimeStateStorePhysicalObservationFanoutIsMonotonicAcrossStores(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	stores := []*RuntimeStateStore{
		NewRuntimeStateStoreAtPath(dbPath, slog.Default()),
		NewRuntimeStateStoreAtPath(dbPath, slog.Default()),
	}
	for _, store := range stores {
		t.Cleanup(func() { _ = store.Close() })
	}
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	for _, seed := range []Session{
		{ID: "az-root", IssueID: "root", Role: SessionRoleWorker, ScopeKind: SessionScopeIssue, ScopeID: "root", State: SessionStateStopped, ObservedState: SessionStateStopped, UpdatedAt: base},
		{ID: "az-root", IssueID: "root", Role: SessionRoleOrchestrator, ScopeKind: SessionScopeOrchestration, ScopeID: "root", State: SessionStatePaused, ObservedState: SessionStatePaused, UpdatedAt: base},
	} {
		if err := stores[0].UpsertSessionState(ctx, "p", seed); err != nil {
			t.Fatal(err)
		}
	}
	newer := PhysicalSessionObservation{ProjectID: "p", SessionID: "az-root", ObservedState: SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: base.Add(2 * time.Second)}
	older := PhysicalSessionObservation{ProjectID: "p", SessionID: "az-root", ObservedState: SessionStatePaused, Activity: "waiting", ActivitySource: "hooks", UpdatedAt: base.Add(time.Second)}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i, observation := range []PhysicalSessionObservation{older, newer} {
		go func(store *RuntimeStateStore, observation PhysicalSessionObservation) {
			<-start
			_, _, err := store.ApplyPhysicalSessionObservation(ctx, observation)
			errs <- err
		}(stores[i], observation)
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	observation, found, err := stores[1].GetPhysicalSessionObservation(ctx, "p", "az-root")
	if err != nil || !found || observation.ObservedState != SessionStateRunning || observation.Activity != "busy" {
		t.Fatalf("physical observation=%+v found=%v err=%v", observation, found, err)
	}
	for _, role := range []SessionRole{SessionRoleWorker, SessionRoleOrchestrator} {
		scope := SessionScopeIssue
		if role == SessionRoleOrchestrator {
			scope = SessionScopeOrchestration
		}
		intent, found, err := stores[0].GetSessionIntent(ctx, "p", role, scope, "root")
		if err != nil || !found || intent.ObservedState != SessionStateRunning || intent.Activity != "busy" || !intent.UpdatedAt.Equal(newer.UpdatedAt) {
			t.Fatalf("%s intent=%+v found=%v err=%v", role, intent, found, err)
		}
		if role == SessionRoleWorker && intent.State != SessionStateStopped {
			t.Fatalf("worker desired state changed: %+v", intent)
		}
	}
	changed, applied, err := stores[0].ApplyPhysicalSessionObservation(ctx, older)
	if err != nil || applied || len(changed) != 0 {
		t.Fatalf("stale observation changed state: applied=%v changed=%+v err=%v", applied, changed, err)
	}
	observation, found, err = stores[1].GetPhysicalSessionObservation(ctx, "p", "az-root")
	if err != nil || !found || observation.ObservedState != SessionStateRunning || observation.Activity != "busy" {
		t.Fatalf("stale observation regressed physical fact: %+v found=%v err=%v", observation, found, err)
	}
}

func TestRuntimeStateStoreOrphanObservationHydratesLaterIntent(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	observedAt := time.Now().UTC().Add(time.Second)
	changed, applied, err := store.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{
		ProjectID: "p", SessionID: "az-root", ObservedState: SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: observedAt,
	})
	if err != nil || !applied || len(changed) != 0 {
		t.Fatalf("orphan observation changed=%+v applied=%v err=%v", changed, applied, err)
	}
	desiredAt := observedAt.Add(-time.Minute)
	if err := store.UpsertSessionState(ctx, "p", Session{
		ID: "az-root", IssueID: "root", Role: SessionRoleWorker,
		ScopeKind: SessionScopeIssue, ScopeID: "root", State: SessionStateStopped,
		ObservedState: SessionStateStopped, UpdatedAt: desiredAt,
	}); err != nil {
		t.Fatal(err)
	}
	intent, found, err := store.GetSessionIntent(ctx, "p", SessionRoleWorker, SessionScopeIssue, "root")
	if err != nil || !found {
		t.Fatalf("hydrated intent found=%v err=%v", found, err)
	}
	if intent.State != SessionStateStopped || intent.ObservedState != SessionStateRunning || intent.Activity != "busy" || intent.ActivitySource != "hooks" || !intent.UpdatedAt.Equal(observedAt) {
		t.Fatalf("hydrated intent = %+v", intent)
	}
}

func TestRuntimeStateStoreNewerRuntimeObservationSurvivesLaterDesiredWrite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	hookStore := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	tmuxStore := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = hookStore.Close(); _ = tmuxStore.Close() })
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-3 * time.Second)
	t2, t3, t4 := t1.Add(time.Second), t1.Add(2*time.Second), t1.Add(3*time.Second)
	if _, _, err := hookStore.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{
		ProjectID: "p", SessionID: "az-root", ObservedState: SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: t1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tmuxStore.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{
		ProjectID: "p", SessionID: "az-root", ObservedState: SessionStateStopped, UpdatedAt: t2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := hookStore.UpsertSessionState(ctx, "p", Session{
		ID: "az-root", IssueID: "root", Role: SessionRoleWorker, ScopeKind: SessionScopeIssue,
		ScopeID: "root", State: SessionStatePaused, ObservedState: SessionStateRunning,
		Activity: "stale", ActivitySource: "desired", UpdatedAt: t3,
	}); err != nil {
		t.Fatal(err)
	}
	intent, found, err := tmuxStore.GetSessionIntent(ctx, "p", SessionRoleWorker, SessionScopeIssue, "root")
	if err != nil || !found || intent.State != SessionStatePaused || intent.ObservedState != SessionStateStopped || intent.Activity != "" {
		t.Fatalf("later desired write overrode newer tmux fact: %+v found=%v err=%v", intent, found, err)
	}
	physical, found, err := hookStore.GetPhysicalSessionObservation(ctx, "p", "az-root")
	if err != nil || !found || physical.ObservedState != SessionStateStopped || !physical.UpdatedAt.Equal(t2) {
		t.Fatalf("physical tmux fact=%+v found=%v err=%v", physical, found, err)
	}
	if _, _, err := hookStore.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{
		ProjectID: "p", SessionID: "az-root", ObservedState: SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: t4,
	}); err != nil {
		t.Fatal(err)
	}
	intent, found, err = tmuxStore.GetSessionIntent(ctx, "p", SessionRoleWorker, SessionScopeIssue, "root")
	if err != nil || !found || intent.ObservedState != SessionStateRunning || intent.Activity != "busy" || !intent.UpdatedAt.Equal(t4) {
		t.Fatalf("newer hook did not win: %+v found=%v err=%v", intent, found, err)
	}
}

func TestRuntimeStateStoreUpgradeBackfillsPhysicalObservationVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	newerAt := time.Date(2026, time.July, 13, 8, 0, 0, 500000000, time.UTC)
	if _, err := db.Exec(`CREATE TABLE daemon_physical_session_observations(
		project_id TEXT NOT NULL, session_id TEXT NOT NULL, observed_state TEXT NOT NULL,
		activity TEXT NOT NULL DEFAULT '', activity_source TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL, PRIMARY KEY(project_id,session_id));
		INSERT INTO daemon_physical_session_observations VALUES('p','az-root','running','busy','hooks',?)`, newerAt.Format(time.RFC3339Nano)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, _, err := store.GetPhysicalSessionObservation(ctx, "p", "az-root"); err != nil {
		t.Fatal(err)
	}
	verifyDB, err := store.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := verifyDB.QueryRow(`SELECT observed_version FROM daemon_physical_session_observations WHERE project_id='p' AND session_id='az-root'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != newerAt.UnixNano() {
		t.Fatalf("observed_version=%d want %d", version, newerAt.UnixNano())
	}
	changed, applied, err := store.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{
		ProjectID: "p", SessionID: "az-root", ObservedState: SessionStatePaused,
		Activity: "waiting", ActivitySource: "hooks", UpdatedAt: newerAt.Add(-time.Second),
	})
	if err != nil || applied || len(changed) != 0 {
		t.Fatalf("older post-upgrade observation changed=%+v applied=%v err=%v", changed, applied, err)
	}
	got, found, err := store.GetPhysicalSessionObservation(ctx, "p", "az-root")
	if err != nil || !found || got.ObservedState != SessionStateRunning || got.Activity != "busy" {
		t.Fatalf("upgraded observation=%+v found=%v err=%v", got, found, err)
	}
}

func TestRuntimeStateStorePhysicalObservationFanoutRollsBackTogether(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	for _, seed := range []Session{
		{ID: "az-root", IssueID: "root", Role: SessionRoleWorker, ScopeKind: SessionScopeIssue, ScopeID: "root", State: SessionStateStopped, UpdatedAt: base},
		{ID: "az-root", IssueID: "root", Role: SessionRoleOrchestrator, ScopeKind: SessionScopeOrchestration, ScopeID: "root", State: SessionStatePaused, UpdatedAt: base},
	} {
		if err := store.UpsertSessionState(ctx, "p", seed); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER inject_physical_fanout_failure BEFORE UPDATE ON daemon_session_projections WHEN OLD.role='orchestrator' BEGIN SELECT RAISE(ABORT,'injected fanout failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.ApplyPhysicalSessionObservation(ctx, PhysicalSessionObservation{
		ProjectID: "p", SessionID: "az-root", ObservedState: SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: base.Add(time.Second),
	})
	if err == nil {
		t.Fatal("expected injected fanout failure")
	}
	if observation, found, err := store.GetPhysicalSessionObservation(ctx, "p", "az-root"); err != nil || found {
		t.Fatalf("physical observation escaped rollback: %+v found=%v err=%v", observation, found, err)
	}
	for _, role := range []SessionRole{SessionRoleWorker, SessionRoleOrchestrator} {
		scope := SessionScopeIssue
		wantObserved := SessionState("")
		if role == SessionRoleOrchestrator {
			scope = SessionScopeOrchestration
		}
		intent, found, err := store.GetSessionIntent(ctx, "p", role, scope, "root")
		if err != nil || !found || intent.ObservedState != wantObserved {
			t.Fatalf("%s intent escaped rollback: %+v found=%v err=%v", role, intent, found, err)
		}
	}
}
