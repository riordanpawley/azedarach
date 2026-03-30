package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStoreCreateGetAndList(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	repo := NewAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = repo.Close() })

	submittedAt := time.Date(2026, 3, 26, 4, 0, 0, 0, time.UTC)
	created, err := repo.Create(context.Background(), CreateParams{
		OperationID:  "op-1",
		ProjectID:    "proj-1",
		IssueID:      "az-1",
		Kind:         "session.start",
		DedupeKey:    "proj-1::az-1::session.start",
		ResourceKeys: []string{"issue:az-1", "worktree:/tmp/az-1"},
		SubmittedAt:  submittedAt,
		ResultJSON:   json.RawMessage(`{"accepted":true}`),
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if created.State != StateQueued {
		t.Fatalf("created state = %q, want queued", created.State)
	}

	fetched, err := repo.Get(context.Background(), "op-1")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if fetched.OperationID != created.OperationID || fetched.ProjectID != "proj-1" || fetched.IssueID != "az-1" {
		t.Fatalf("fetched record = %+v", fetched)
	}
	if got, want := len(fetched.ResourceKeys), 2; got != want {
		t.Fatalf("resource keys len = %d, want %d", got, want)
	}
	if string(fetched.ResultJSON) != `{"accepted":true}` {
		t.Fatalf("result json = %s", string(fetched.ResultJSON))
	}

	listed, err := repo.List(context.Background(), Query{ProjectID: "proj-1", IssueID: "az-1", States: []State{StateQueued}})
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if got, want := len(listed), 1; got != want {
		t.Fatalf("list len = %d, want %d", got, want)
	}
}

func TestSQLiteStoreRestartVisibility(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	repo := NewAtPath(dbPath, slog.Default())

	created, err := repo.Create(context.Background(), CreateParams{
		OperationID:  "op-restart",
		ProjectID:    "proj-1",
		IssueID:      "az-2",
		Kind:         "git.merge",
		DedupeKey:    "proj-1::az-2::git.merge",
		ResourceKeys: []string{"issue:az-2", "worktree:/tmp/az-2"},
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if _, err := repo.Transition(context.Background(), TransitionParams{OperationID: created.OperationID, ToState: StateRunning}); err != nil {
		t.Fatalf("Transition running error: %v", err)
	}
	terminal, err := repo.Transition(context.Background(), TransitionParams{
		OperationID: created.OperationID,
		ToState:     StateFailed,
		ErrorJSON:   json.RawMessage(`{"message":"merge conflict"}`),
	})
	if err != nil {
		t.Fatalf("Transition failed error: %v", err)
	}
	if terminal.FinishedAt == nil {
		t.Fatal("finished_at was not set for terminal transition")
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	reopened := NewAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = reopened.Close() })

	fetched, err := reopened.Get(context.Background(), created.OperationID)
	if err != nil {
		t.Fatalf("Get after reopen error: %v", err)
	}
	if fetched.State != StateFailed {
		t.Fatalf("state after reopen = %q, want failed", fetched.State)
	}
	if fetched.StartedAt == nil {
		t.Fatal("started_at missing after reopen")
	}
	if fetched.FinishedAt == nil {
		t.Fatal("finished_at missing after reopen")
	}
	if string(fetched.ErrorJSON) != `{"message":"merge conflict"}` {
		t.Fatalf("error json = %s", string(fetched.ErrorJSON))
	}
}

func TestSQLiteStoreTransitionValidation(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	repo := NewAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = repo.Close() })

	_, err := repo.Create(context.Background(), CreateParams{
		OperationID:  "op-invalid",
		ProjectID:    "proj-1",
		IssueID:      "az-3",
		Kind:         "worktree.cleanup_orphaned",
		ResourceKeys: []string{"project:proj-1"},
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if _, err := repo.Transition(context.Background(), TransitionParams{OperationID: "op-invalid", ToState: StateDone}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition error = %v, want ErrInvalidTransition", err)
	}

	fetched, err := repo.Get(context.Background(), "op-invalid")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if fetched.State != StateQueued {
		t.Fatalf("state after invalid transition = %q, want queued", fetched.State)
	}
}

func TestSQLiteStoreListSupportsStateAndDedupeFilters(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	repo := NewAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = repo.Close() })

	fixtures := []CreateParams{
		{
			OperationID:  "op-a",
			ProjectID:    "proj-2",
			IssueID:      "az-10",
			Kind:         "session.start",
			DedupeKey:    "k-1",
			ResourceKeys: []string{"issue:az-10"},
		},
		{
			OperationID:  "op-b",
			ProjectID:    "proj-2",
			IssueID:      "az-10",
			Kind:         "session.start",
			DedupeKey:    "k-2",
			ResourceKeys: []string{"issue:az-10"},
		},
		{
			OperationID:  "op-c",
			ProjectID:    "proj-2",
			IssueID:      "az-11",
			Kind:         "git.merge",
			DedupeKey:    "k-3",
			ResourceKeys: []string{"issue:az-11"},
		},
	}
	for _, fixture := range fixtures {
		if _, err := repo.Create(context.Background(), fixture); err != nil {
			t.Fatalf("Create(%s) error: %v", fixture.OperationID, err)
		}
	}
	if _, err := repo.Transition(context.Background(), TransitionParams{OperationID: "op-b", ToState: StateRunning}); err != nil {
		t.Fatalf("Transition op-b error: %v", err)
	}
	if _, err := repo.Transition(context.Background(), TransitionParams{OperationID: "op-c", ToState: StateRunning}); err != nil {
		t.Fatalf("Transition op-c running error: %v", err)
	}
	if _, err := repo.Transition(context.Background(), TransitionParams{OperationID: "op-c", ToState: StateDone}); err != nil {
		t.Fatalf("Transition op-c done error: %v", err)
	}

	listed, err := repo.List(context.Background(), Query{
		ProjectID: "proj-2",
		IssueID:   "az-10",
		DedupeKey: "k-2",
		States:    []State{StateRunning},
	})
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if got, want := len(listed), 1; got != want {
		t.Fatalf("filtered list len = %d, want %d", got, want)
	}
	if listed[0].OperationID != "op-b" {
		t.Fatalf("filtered operation id = %q, want op-b", listed[0].OperationID)
	}
}

func TestResolveDBPathUsesBaseRepoForWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	got, err := resolveDBPath(start)
	if err != nil {
		t.Fatalf("resolveDBPath() error = %v", err)
	}
	want := filepath.Join(repo, ".azedarach", "azedarach.db")
	if got != want {
		t.Fatalf("resolveDBPath() = %q, want %q", got, want)
	}
}
