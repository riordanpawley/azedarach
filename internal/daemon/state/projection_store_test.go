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
	if sessions[0].Activity != "no-agent" || sessions[0].ActivitySource != "session" {
		t.Fatalf("session activity = %s/%s, want no-agent/session", sessions[0].Activity, sessions[0].ActivitySource)
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
	if session.ObservedState != SessionStateStopped {
		t.Fatalf("session observed state = %s, want %s", session.ObservedState, SessionStateStopped)
	}

	if err := store.ReplaceSessionStates(ctx, "proj-a", []Session{
		{ID: "sess-2", IssueID: "bjb", State: SessionStateStopped, Activity: "no-agent", ActivitySource: "session", UpdatedAt: now},
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
	if session.ObservedState != SessionStateStopped {
		t.Fatalf("replaced session observed state = %s, want %s", session.ObservedState, SessionStateStopped)
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

func TestRuntimeStateStoreSessionGetters(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Date(2026, time.April, 1, 8, 20, 0, 0, time.UTC)
	rows := []Session{
		{ID: "sess-1", IssueID: "bja", State: SessionStateAttached, Activity: "busy", ActivitySource: "runtime", UpdatedAt: now},
		{ID: "sess-2", IssueID: "bja", State: SessionStatePaused, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now.Add(1 * time.Minute)},
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
	if session.ID != "sess-2" || session.State != SessionStatePaused {
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

	got, err := store.ListProjectIDs(ctx)
	if err != nil {
		t.Fatalf("ListProjectIDs: %v", err)
	}
	want := []string{"proj-a", "proj-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListProjectIDs() = %v, want %v", got, want)
	}
}
