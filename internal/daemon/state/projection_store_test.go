package state

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRuntimeStateStoreSessionRoundTrip(t *testing.T) {
	store := NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	updatedAt := time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC)
	if err := store.UpsertSessionState(context.Background(), "proj-a", Session{
		ID:        "sess-1",
		IssueID:   "bja",
		State:     SessionStateAttached,
		UpdatedAt: updatedAt,
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
		{ID: "sess-1", IssueID: "bja", State: SessionStateAttached, UpdatedAt: now},
		{ID: "sess-2", IssueID: "bja", State: SessionStatePaused, UpdatedAt: now.Add(1 * time.Minute)},
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
