package daemon

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestMigrateLegacyRuntimeStateCopiesRowsToProjectScopedStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	baseRepo := newRuntimeRouterTestRepo(t, "base")
	otherRepo := newRuntimeRouterTestRepo(t, "other")
	orphanRepo := newRuntimeRouterTestRepo(t, "orphan")
	registry := &appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{
			{Name: "other", Path: otherRepo},
			{Name: "orphan", Path: orphanRepo},
		},
	}
	if err := appconfig.SaveProjectsRegistry(registry); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	source := daemonstate.NewRuntimeStateStore(baseRepo, logger)
	t.Cleanup(func() { _ = source.Close() })

	projectID := "other"
	canonicalOtherProjectID, err := appconfig.ProjectIDForRoot(otherRepo)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(other): %v", err)
	}
	now := time.Date(2026, time.April, 3, 7, 10, 0, 0, time.UTC)
	if err := source.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        "sess-other",
		IssueID:   "az-1",
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed source session: %v", err)
	}
	physicalAt := now.Add(2 * time.Minute)
	const physicalVersion int64 = 900000000000000000
	if _, _, err := source.ApplyPhysicalSessionObservation(context.Background(), daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: "sess-other", ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt: physicalAt, ObservedVersion: physicalVersion,
	}); err != nil {
		t.Fatalf("seed source physical observation: %v", err)
	}
	// Simulate a legacy logical mirror whose observed fields drifted behind the
	// newer physical authority. Migration must ignore these mirror facts.
	rawDB, err := sql.Open("sqlite", filepath.Join(baseRepo, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatalf("open legacy runtime db: %v", err)
	}
	if _, err := rawDB.Exec(`UPDATE daemon_session_projections SET observed_state='running',activity='busy',activity_source='legacy' WHERE project_id=? AND session_id=?`, projectID, "sess-other"); err != nil {
		t.Fatalf("drift legacy logical observation: %v", err)
	}
	_ = rawDB.Close()
	const orphanProjectID = "orphan"
	const orphanVersion int64 = physicalVersion + 1
	if _, _, err := source.ApplyPhysicalSessionObservation(context.Background(), daemonstate.PhysicalSessionObservation{
		ProjectID: orphanProjectID, SessionID: "orphan-runtime", ObservedState: daemonstate.SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: physicalAt.Add(time.Minute), ObservedVersion: orphanVersion,
	}); err != nil {
		t.Fatalf("seed orphan-only physical observation: %v", err)
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

	sessions, err := target.ListSessionStates(context.Background(), canonicalOtherProjectID)
	if err != nil {
		t.Fatalf("list migrated sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "sess-other" {
		t.Fatalf("migrated sessions = %+v, want sess-other", sessions)
	}
	if sessions[0].ObservedState != daemonstate.SessionStateStopped || sessions[0].Activity != "" {
		t.Fatalf("migrated session hydrated from stale logical mirror: %+v", sessions[0])
	}
	physical, found, err := target.GetPhysicalSessionObservation(context.Background(), canonicalOtherProjectID, "sess-other")
	if err != nil || !found || physical.ObservedVersion != physicalVersion || !physical.UpdatedAt.Equal(physicalAt) {
		t.Fatalf("migrated physical observation = %+v found=%v err=%v", physical, found, err)
	}
	// A target produced by the older migration may already contain logical rows
	// while lacking physical authority. A subsequent migration must repair it.
	targetDB, err := sql.Open("sqlite", filepath.Join(otherRepo, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatalf("open routed runtime db: %v", err)
	}
	if _, err := targetDB.Exec(`DELETE FROM daemon_physical_session_observations WHERE project_id=?`, canonicalOtherProjectID); err != nil {
		t.Fatalf("remove routed physical authority: %v", err)
	}
	if _, err := targetDB.Exec(`UPDATE daemon_session_projections SET observed_state='running' WHERE project_id=?`, canonicalOtherProjectID); err != nil {
		t.Fatalf("drift routed logical mirror: %v", err)
	}
	_ = targetDB.Close()
	if err := d.migrateLegacyRuntimeState(context.Background()); err != nil {
		t.Fatalf("repair legacy physical migration: %v", err)
	}
	physical, found, err = target.GetPhysicalSessionObservation(context.Background(), canonicalOtherProjectID, "sess-other")
	if err != nil || !found || physical.ObservedVersion != physicalVersion {
		t.Fatalf("repaired physical observation = %+v found=%v err=%v", physical, found, err)
	}
	repaired, found, err := target.GetSessionState(context.Background(), canonicalOtherProjectID, "sess-other")
	if err != nil || !found || repaired.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("repaired logical hydration = %+v found=%v err=%v", repaired, found, err)
	}
	worktrees, err := target.ListWorktreeStates(context.Background(), canonicalOtherProjectID)
	if err != nil {
		t.Fatalf("list migrated worktrees: %v", err)
	}
	if len(worktrees) != 1 || worktrees[0].IssueID != "az-1" {
		t.Fatalf("migrated worktrees = %+v, want az-1 row", worktrees)
	}
	canonicalOrphanProjectID, err := appconfig.ProjectIDForRoot(orphanRepo)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(orphan): %v", err)
	}
	orphanTarget := d.runtimeStateStoreForProject(orphanProjectID)
	if orphanTarget == nil {
		t.Fatal("orphan target runtime store nil")
	}
	t.Cleanup(func() { _ = orphanTarget.Close() })
	orphanPhysical, found, err := orphanTarget.GetPhysicalSessionObservation(context.Background(), canonicalOrphanProjectID, "orphan-runtime")
	if err != nil || !found || orphanPhysical.ObservedVersion != orphanVersion {
		t.Fatalf("migrated orphan physical observation = %+v found=%v err=%v", orphanPhysical, found, err)
	}
	orphanSessions, err := orphanTarget.ListSessionStates(context.Background(), canonicalOrphanProjectID)
	if err != nil || len(orphanSessions) != 0 {
		t.Fatalf("orphan migration fabricated logical sessions = %+v err=%v", orphanSessions, err)
	}
}

func TestStoreRoutersReuseBaseRepoHandlesForLinkedWorktreeRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")

	baseRepo := newRuntimeRouterTestRepo(t, "base")
	worktree := newRuntimeRouterLinkedWorktree(t, baseRepo, "wt")
	registry := &appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{
			{Name: "linked-worktree", Path: worktree},
		},
	}
	if err := appconfig.SaveProjectsRegistry(registry); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	issueClient := issues.NewClient(baseRepo, logger)
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStore(baseRepo, logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })

	d := &Daemon{
		cfg: Config{
			RepoDir: baseRepo,
			Logger:  logger,
		},
		issues: issueClient,
		issueClientsByRoot: map[string]*issues.Client{
			daemonStoreRootKey(baseRepo): issueClient,
		},
		issueClientsByProject: map[string]*issues.Client{},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			daemonStoreRootKey(baseRepo): runtimeStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{},
	}

	if got := d.issueClientForProject("linked-worktree"); got != issueClient {
		t.Fatalf("issue client = %p, want base client %p", got, issueClient)
	}
	if got := d.runtimeStateStoreForProject("linked-worktree"); got != runtimeStore {
		t.Fatalf("runtime store = %p, want base store %p", got, runtimeStore)
	}
	if len(d.issueClientsByRoot) != 1 {
		t.Fatalf("issueClientsByRoot len = %d, want 1", len(d.issueClientsByRoot))
	}
	if len(d.runtimeStoresByRoot) != 1 {
		t.Fatalf("runtimeStoresByRoot len = %d, want 1", len(d.runtimeStoresByRoot))
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

func newRuntimeRouterLinkedWorktree(t *testing.T, baseRepo, name string) string {
	t.Helper()
	worktree := filepath.Join(filepath.Dir(baseRepo), name)
	if err := os.MkdirAll(filepath.Join(baseRepo, ".git", "worktrees", name), 0o755); err != nil {
		t.Fatalf("mkdir worktree git dir: %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	gitDir := filepath.Join(baseRepo, ".git", "worktrees", name)
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatalf("write worktree git pointer: %v", err)
	}
	return worktree
}
