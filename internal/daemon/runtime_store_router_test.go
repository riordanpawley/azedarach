package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
)

func TestMigrateLegacyRuntimeStateCopiesRowsToProjectScopedStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	baseRepo := newRuntimeRouterTestRepo(t, "base")
	otherRepo := newRuntimeRouterTestRepo(t, "other")
	registry := &appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{
			{Name: "other", Path: otherRepo},
		},
	}
	if err := appconfig.SaveProjectsRegistry(registry); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	source := daemonstate.NewRuntimeStateStore(baseRepo, logger)
	t.Cleanup(func() { _ = source.Close() })

	projectID := "other"
	now := time.Date(2026, time.April, 3, 7, 10, 0, 0, time.UTC)
	if err := source.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        "sess-other",
		IssueID:   "az-1",
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed source session: %v", err)
	}
	if err := source.UpsertWorktreeState(context.Background(), daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   "az-1",
		Path:      filepath.Join(otherRepo, "-az-1"),
		Branch:    "riordan/az-1/task",
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed source worktree: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: baseRepo,
			Logger:  logger,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			"default": source,
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			baseRepo: source,
		},
	}

	if err := d.migrateLegacyRuntimeState(context.Background()); err != nil {
		t.Fatalf("migrate legacy runtime state: %v", err)
	}

	target := d.runtimeStateStoreForProject(projectID)
	if target == nil {
		t.Fatal("target runtime store nil")
	}
	if target == source {
		t.Fatal("target runtime store should be project-scoped, got source store")
	}
	t.Cleanup(func() { _ = target.Close() })

	sessions, err := target.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list migrated sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "sess-other" {
		t.Fatalf("migrated sessions = %+v, want sess-other", sessions)
	}
	worktrees, err := target.ListWorktreeStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list migrated worktrees: %v", err)
	}
	if len(worktrees) != 1 || worktrees[0].IssueID != "az-1" {
		t.Fatalf("migrated worktrees = %+v, want az-1 row", worktrees)
	}
}

func newRuntimeRouterTestRepo(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return root
}
