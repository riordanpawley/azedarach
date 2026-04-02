package state

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectionStoreSessionRoundTrip(t *testing.T) {
	store := NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	updatedAt := time.Date(2026, time.April, 1, 8, 0, 0, 0, time.UTC)
	if err := store.UpsertSession(context.Background(), "proj-a", Session{
		ID:        "sess-1",
		IssueID:   "bja",
		State:     SessionStateAttached,
		UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	sessions, err := store.ListSessions(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
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

	if err := store.DeleteSession(context.Background(), "proj-a", "sess-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	sessions, err = store.ListSessions(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListSessions after delete: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions after delete = %d, want 0", len(sessions))
	}
}

func TestProjectionStoreSessionReplaceAndList(t *testing.T) {
	store := NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Date(2026, time.April, 1, 8, 15, 0, 0, time.UTC)
	if err := store.ReplaceSessions(context.Background(), "proj-a", []Session{
		{ID: "sess-1", IssueID: "bja", State: SessionStateAttached, UpdatedAt: now},
		{ID: "sess-2", IssueID: "bjb", State: SessionStatePaused, UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceSessions: %v", err)
	}

	sessions, err := store.ListSessions(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if got, want := len(sessions), 2; got != want {
		t.Fatalf("sessions count = %d, want %d", got, want)
	}

	if err := store.ReplaceSessions(context.Background(), "proj-a", []Session{
		{ID: "sess-2", IssueID: "bjb", State: SessionStateAttached, UpdatedAt: now.Add(1 * time.Minute)},
	}); err != nil {
		t.Fatalf("ReplaceSessions second pass: %v", err)
	}

	sessions, err = store.ListSessions(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListSessions after replace: %v", err)
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

func TestProjectionStoreWorktreeReplaceAndList(t *testing.T) {
	store := NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	now := time.Date(2026, time.April, 1, 8, 30, 0, 0, time.UTC)
	if err := store.ReplaceWorktrees(context.Background(), "proj-a", []WorktreeProjection{
		{ProjectID: "proj-a", IssueID: "bja", Path: "/tmp/repo-bja", Branch: "riordan/bja/task", UpdatedAt: now},
		{ProjectID: "proj-a", IssueID: "bjb", Path: "/tmp/repo-bjb", Branch: "riordan/bjb/task", UpdatedAt: now},
	}); err != nil {
		t.Fatalf("ReplaceWorktrees: %v", err)
	}

	worktrees, err := store.ListWorktrees(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if got, want := len(worktrees), 2; got != want {
		t.Fatalf("worktrees count = %d, want %d", got, want)
	}

	if err := store.UpsertWorktree(context.Background(), WorktreeProjection{
		ProjectID: "proj-a",
		IssueID:   "bja",
		Path:      "/tmp/repo-bja-updated",
		Branch:    "riordan/bja/updated",
		UpdatedAt: now.Add(1 * time.Minute),
	}); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}
	worktrees, err = store.ListWorktrees(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListWorktrees after upsert: %v", err)
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

	if err := store.DeleteWorktree(context.Background(), "proj-a", "bja"); err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	worktrees, err = store.ListWorktrees(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("ListWorktrees after delete: %v", err)
	}
	if got, want := len(worktrees), 1; got != want {
		t.Fatalf("worktrees after delete = %d, want %d", got, want)
	}
}

func TestProjectionStoreWorktreeGitStatusUpdateGuardrail(t *testing.T) {
	store := NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = store.Close()
	})

	createdAt := time.Date(2026, time.April, 1, 9, 5, 0, 0, time.UTC)
	if err := store.UpsertWorktree(context.Background(), WorktreeProjection{
		ProjectID: "proj-a",
		IssueID:   "bja",
		Path:      "/tmp/repo-bja",
		Branch:    "riordan/bja/task",
		UpdatedAt: createdAt,
	}); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	statusAt := time.Date(2026, time.April, 1, 9, 10, 0, 0, time.UTC)
	rawStatus := json.RawMessage(`{"clean":false,"modified":["README.md"]}`)
	if err := store.UpsertWorktreeGitStatus(context.Background(), "proj-a", "bja", rawStatus, statusAt); err != nil {
		t.Fatalf("UpsertWorktreeGitStatus existing row: %v", err)
	}

	projection, found, err := store.GetWorktreeByIssueID(context.Background(), "proj-a", "bja")
	if err != nil {
		t.Fatalf("GetWorktreeByIssueID: %v", err)
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

	err = store.UpsertWorktreeGitStatus(context.Background(), "proj-a", "missing", json.RawMessage(`{"clean":true}`), statusAt)
	if err == nil {
		t.Fatal("UpsertWorktreeGitStatus missing row: expected error")
	}
	if got := err.Error(); !strings.Contains(got, "expected 1 affected row(s), got 0") {
		t.Fatalf("UpsertWorktreeGitStatus missing row error = %q, want affected-row guardrail", got)
	}
}

func TestProjectionStoreGitStatusRoundTrip(t *testing.T) {
	store := NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
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

	if err := store.UpsertWorktree(context.Background(), WorktreeProjection{
		ProjectID: "proj-a",
		IssueID:   "bja",
		Path:      "/tmp/repo-bja",
		Branch:    "riordan/bja/task",
		UpdatedAt: time.Date(2026, time.April, 1, 8, 55, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	if err := store.UpsertWorktreeGitStatus(
		context.Background(),
		"proj-a",
		"bja",
		rawStatus,
		time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("UpsertWorktreeGitStatus: %v", err)
	}

	projection, found, err := store.GetWorktreeByPath(context.Background(), "proj-a", "/tmp/repo-bja")
	if err != nil {
		t.Fatalf("GetWorktreeByPath: %v", err)
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
