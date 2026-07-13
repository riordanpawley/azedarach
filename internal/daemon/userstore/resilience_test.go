package userstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestMigrationFailureRollsBackSchemaDataAndMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`CREATE TABLE preferences(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO preferences VALUES('theme','dark')`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected migration interruption")
	if store, openErr := Open(path, withMigrationBeforeCommit(func() error { return injected })); openErr == nil {
		_ = store.Close()
		t.Fatal("migration unexpectedly succeeded")
	}
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var schemaTables int
	if err = raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&schemaTables); err != nil {
		t.Fatal(err)
	}
	if schemaTables != 0 {
		t.Fatalf("failed migration left schema marker table behind: %d", schemaTables)
	}
	var preference string
	if err = raw.QueryRow(`SELECT value FROM preferences WHERE key='theme'`).Scan(&preference); err != nil || preference != "dark" {
		t.Fatalf("unrelated data after rollback=%q err=%v", preference, err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after rolled-back migration: %v", err)
	}
	defer store.Close()
	var markers int
	if err = store.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id IN ('user_0001_cross_project_projection','user_0002_normalized_projection','user_0003_canonical_issue_state_repair','user_0004_canonical_archive_state_repair')`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 4 {
		t.Fatalf("migration markers after retry=%d want=4", markers)
	}
}

func TestCanonicalIssueRepairMigrationInvalidatesRootUserProjections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO projects(project_id,name,path,db_path,projection_version,freshness) VALUES('p','P','/p','/p/.azedarach/azedarach.db',2,'ready'); DELETE FROM schema_migrations WHERE id='user_0003_canonical_issue_state_repair'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var freshness string
	if err := reopened.db.QueryRow(`SELECT freshness FROM projects WHERE project_id='p'`).Scan(&freshness); err != nil {
		t.Fatal(err)
	}
	if freshness != "stale" {
		t.Fatalf("freshness=%q want stale", freshness)
	}
	var marker int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='user_0003_canonical_issue_state_repair'`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("repair marker=%d err=%v", marker, err)
	}
}

func TestCanonicalArchiveRepairReinvalidatesRootUserProjections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO projects(project_id,name,path,db_path,projection_version,freshness) VALUES('p','P','/p','/p/.azedarach/azedarach.db',2,'ready'); DELETE FROM schema_migrations WHERE id='user_0004_canonical_archive_state_repair'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var freshness string
	if err := reopened.db.QueryRow(`SELECT freshness FROM projects WHERE project_id='p'`).Scan(&freshness); err != nil {
		t.Fatal(err)
	}
	if freshness != "stale" {
		t.Fatalf("freshness=%q want stale", freshness)
	}
}

func TestSnapshotUsesSingleReadGenerationDuringConcurrentReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	metadataRead := make(chan struct{})
	continueSnapshot := make(chan struct{})
	var hookCalls int
	store, err := Open(path, withSnapshotAfterProjects(func() {
		hookCalls++
		if hookCalls == 1 {
			close(metadataRead)
			<-continueSnapshot
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err = store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "old", Path: "/p", DBPath: "/p/db", Checkpoint: 1, Tasks: []domain.Task{projectionTestTask("old", "old")}}); err != nil {
		t.Fatal(err)
	}
	type snapshotResult struct {
		snapshot protocol.GlobalSnapshotResponseBody
		err      error
	}
	snapshotDone := make(chan snapshotResult, 1)
	go func() {
		snapshot, snapshotErr := store.Snapshot(ctx, "")
		snapshotDone <- snapshotResult{snapshot: snapshot, err: snapshotErr}
	}()
	<-metadataRead
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- store.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "new", Path: "/p", DBPath: "/p/db", Checkpoint: 2, Tasks: []domain.Task{projectionTestTask("new", "new")}})
	}()
	close(continueSnapshot)
	result := <-snapshotDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.snapshot.Projects) != 1 || result.snapshot.Projects[0].Name != "old" || result.snapshot.Projects[0].Checkpoint != 1 || len(result.snapshot.Projects[0].Tasks) != 1 || result.snapshot.Projects[0].Tasks[0].ID != "old" {
		t.Fatalf("snapshot mixed refresh generations: %#v", result.snapshot.Projects)
	}
	if err = <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestReplaceProjectFailurePreservesLastGoodProjectionAndCheckpoint(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	oldTask := projectionTestTask("old", "last good")
	if err := store.ReplaceProject(ctx, ProjectInput{
		ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Checkpoint: 7,
		Tasks: []domain.Task{oldTask},
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate an interrupted rebuild after the transaction has removed the old
	// rows but before it can install the replacement snapshot.
	if _, err := store.db.Exec(`CREATE TRIGGER interrupt_projection_rebuild
		BEFORE INSERT ON project_issue_projection
		WHEN NEW.issue_id = 'interrupt'
		BEGIN SELECT RAISE(ABORT, 'injected rebuild interruption'); END`); err != nil {
		t.Fatal(err)
	}
	err := store.ReplaceProject(ctx, ProjectInput{
		ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Checkpoint: 8,
		Tasks: []domain.Task{projectionTestTask("interrupt", "partial replacement")},
	})
	if err == nil {
		t.Fatal("ReplaceProject succeeded despite injected interruption")
	}

	project := projectSnapshot(t, store, "p")
	if project.Checkpoint != 7 || len(project.Tasks) != 1 || project.Tasks[0].ID != oldTask.ID {
		t.Fatalf("failed rebuild changed last-good projection: %#v", project)
	}
}

func TestRestartRepairsStaleProjectionAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	ctx := context.Background()
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.ReplaceProject(ctx, ProjectInput{
		ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Checkpoint: 3,
		Tasks: []domain.Task{projectionTestTask("before", "before restart")},
	}); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	repair := ProjectInput{
		ProjectID: "p", Name: "P renamed", Path: "/renamed", DBPath: "/renamed/db", Checkpoint: 4,
		Tasks: []domain.Task{projectionTestTask("after", "after restart")},
	}
	if err = restarted.ReplaceProject(ctx, repair); err != nil {
		t.Fatal(err)
	}
	if err = restarted.ReplaceProject(ctx, repair); err != nil {
		t.Fatalf("idempotent repair: %v", err)
	}

	project := projectSnapshot(t, restarted, "p")
	if project.Checkpoint != 4 || project.Name != "P renamed" || project.Path != "/renamed" || len(project.Tasks) != 1 || project.Tasks[0].ID != "after" {
		t.Fatalf("repaired projection: %#v", project)
	}
}

func TestCatalogIdentitySurvivesRenameAndPathChangeThenRemovalAndReAdd(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.ReplaceProject(ctx, ProjectInput{
		ProjectID: "stable-id", Name: "Old", Path: "/old", DBPath: "/old/db", Checkpoint: 2,
		Tasks: []domain.Task{projectionTestTask("kept", "kept")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileProjects(ctx, []CatalogProject{{ProjectID: "stable-id", Name: "New", Path: "/new", DBPath: "/new/db"}}); err != nil {
		t.Fatal(err)
	}
	project := projectSnapshot(t, store, "stable-id")
	if project.Name != "New" || project.Path != "/new" || project.DBPath != "/new/db" || !project.Registered || len(project.Tasks) != 1 {
		t.Fatalf("renamed catalog project: %#v", project)
	}

	if err := store.ReconcileProjects(ctx, nil); err != nil {
		t.Fatal(err)
	}
	project = projectSnapshot(t, store, "stable-id")
	if project.Registered || project.Freshness != protocol.GlobalProjectionFreshnessUnavailable || len(project.Tasks) != 0 {
		// Unregistered rows intentionally retain their last-good data in SQLite,
		// while snapshots suppress it until the project is registered again.
		t.Fatalf("removed catalog project: %#v", project)
	}
	if err := store.ReconcileProjects(ctx, []CatalogProject{{ProjectID: "stable-id", Name: "Again", Path: "/again", DBPath: "/again/db"}}); err != nil {
		t.Fatal(err)
	}
	project = projectSnapshot(t, store, "stable-id")
	if !project.Registered || project.Name != "Again" || len(project.Tasks) != 1 || project.Tasks[0].ID != "kept" {
		t.Fatalf("re-added catalog project lost stable identity: %#v", project)
	}
}

func TestSchemaDriftHealthPreservesLastGoodRowsAndSourceVersion(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.ReplaceProject(ctx, ProjectInput{
		ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", SchemaVersion: 31, Checkpoint: 12,
		Tasks: []domain.Task{projectionTestTask("kept", "last compatible snapshot")},
	}); err != nil {
		t.Fatal(err)
	}
	drift := errors.New("project schema version 32 is newer than supported version 31")
	if err := store.MarkUnavailable(ctx, "p", "P", "/p", "/p/db", drift); err != nil {
		t.Fatal(err)
	}

	project := projectSnapshot(t, store, "p")
	if project.Freshness != protocol.GlobalProjectionFreshnessUnavailable || project.SchemaVersion != 31 || project.Checkpoint != 12 || project.LastError != drift.Error() || len(project.Tasks) != 1 {
		t.Fatalf("schema-drift health: %#v", project)
	}
}

func TestConcurrentStoresRejectStaleWriterAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "azedarach.db")
	ctx := context.Background()
	stale, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	fresh, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()

	if err = fresh.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Checkpoint: 101, Tasks: []domain.Task{projectionTestTask("new", "new")}}); err != nil {
		t.Fatal(err)
	}
	if err = stale.ReplaceProject(ctx, ProjectInput{ProjectID: "p", Name: "stale metadata", Path: "/stale", DBPath: "/stale/db", Checkpoint: 100, Tasks: []domain.Task{projectionTestTask("old", "old")}}); err != nil {
		t.Fatal(err)
	}
	project := projectSnapshot(t, stale, "p")
	if project.Checkpoint != 101 || project.Name != "P" || len(project.Tasks) != 1 || project.Tasks[0].ID != "new" {
		t.Fatalf("stale writer won: %#v", project)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func projectionTestTask(id, title string) domain.Task {
	now := time.Now().UTC()
	return domain.Task{ID: naming.IssueID(id), Title: title, Status: domain.StatusOpen, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}
}

func projectSnapshot(t *testing.T, store *Store, projectID string) protocol.GlobalProjectSnapshot {
	t.Helper()
	snapshot, err := store.Snapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range snapshot.Projects {
		if project.ProjectID == projectID {
			return project
		}
	}
	t.Fatalf("project %q missing from snapshot %#v", projectID, snapshot.Projects)
	return protocol.GlobalProjectSnapshot{}
}
