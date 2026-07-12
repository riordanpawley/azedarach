package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

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
