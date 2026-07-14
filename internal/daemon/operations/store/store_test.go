package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/testisolation"
)

func TestRealProjectOperationsDatabaseMigrationClones(t *testing.T) {
	rawPaths := strings.TrimSpace(os.Getenv("AZEDARACH_PROJECT_DB_CLONES"))
	if rawPaths == "" {
		t.Skip("AZEDARACH_PROJECT_DB_CLONES is not set")
	}
	for _, path := range filepath.SplitList(rawPaths) {
		path := path
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path))), func(t *testing.T) {
			if err := testisolation.CheckDatabaseClone(path, "."); err != nil {
				t.Fatalf("refuse unsafe project database clone before SQLite open: %v", err)
			}
			beforeDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			var beforeOperations, hasOperations int
			if err = beforeDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='daemon_operations'`).Scan(&hasOperations); err != nil {
				_ = beforeDB.Close()
				t.Fatal(err)
			}
			if hasOperations == 1 {
				if err = beforeDB.QueryRow(`SELECT COUNT(*) FROM daemon_operations`).Scan(&beforeOperations); err != nil {
					_ = beforeDB.Close()
					t.Fatal(err)
				}
			}
			if err = beforeDB.Close(); err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			store := NewAtPath(path, slog.Default())
			if _, err = store.ValidationSnapshot(ctx, "migration-review", time.Now().UTC(), time.Minute); err != nil {
				t.Fatal(err)
			}
			if _, err = store.List(ctx, Query{Limit: 1}); err != nil {
				t.Fatal(err)
			}
			var checksum string
			if err = store.db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, "daemon_operations_0003_validation_leases").Scan(&checksum); err != nil {
				t.Fatal(err)
			}
			if checksum != "317c9ea680d378637b417005ccfcf0d989e4c025cbbacabdd581f00dafac19df" {
				t.Fatalf("validation migration checksum = %q", checksum)
			}
			var objects, afterOperations int
			if err = store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN (
				'daemon_validation_requests','daemon_validation_state',
				'idx_daemon_validation_one_active_aggregate','idx_daemon_validation_project_queue','idx_daemon_validation_expiry',
				'daemon_validation_requests_insert_revision','daemon_validation_requests_update_revision'
			)`).Scan(&objects); err != nil {
				t.Fatal(err)
			}
			if objects != 7 {
				t.Fatalf("validation migration objects = %d, want 7", objects)
			}
			if err = store.db.QueryRow(`SELECT COUNT(*) FROM daemon_operations`).Scan(&afterOperations); err != nil {
				t.Fatal(err)
			}
			if afterOperations != beforeOperations {
				t.Fatalf("operation row preservation = %d/%d", beforeOperations, afterOperations)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened := NewAtPath(path, slog.Default())
			if _, err = reopened.ValidationSnapshot(ctx, "migration-review", time.Now().UTC(), time.Minute); err != nil {
				t.Fatal(err)
			}
			var ledgerRows int
			if err = reopened.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND artifact_checksum=?`, "daemon_operations_0003_validation_leases", checksum).Scan(&ledgerRows); err != nil {
				t.Fatal(err)
			}
			if ledgerRows != 1 {
				t.Fatalf("validation migration ledger rows after reopen = %d, want 1", ledgerRows)
			}
			if err = reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSQLiteStoreMigrationBackfillsLegacyOperationsAfterOtherAuthorityConversion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range orderedMigrations {
		sqlText, err := loadMigrationSQL(migration.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(sqlText); err != nil {
			t.Fatalf("apply legacy fixture %s: %v", migration.id, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(id,applied_at) VALUES(?, 'legacy')`, migration.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repo := NewAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = repo.Close() })
	if _, err := repo.List(context.Background(), Query{Limit: 1}); err != nil {
		t.Fatalf("active operation store open: %v", err)
	}
	for _, artifact := range migrationArtifacts {
		var checksum string
		if err := repo.db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, artifact.ID).Scan(&checksum); err != nil {
			t.Fatal(err)
		}
		if checksum != artifact.Checksum {
			t.Fatalf("migration %s checksum = %q, want %q", artifact.ID, checksum, artifact.Checksum)
		}
	}
}

func TestValidationLeaseMigrationUpgradesHistoricalOperationsStoreAndReopens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range orderedMigrations[:2] {
		sqlText, err := loadMigrationSQL(migration.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(sqlText); err != nil {
			t.Fatalf("apply historical fixture %s: %v", migration.id, err)
		}
		checksum := ""
		for _, artifact := range migrationArtifacts {
			if artifact.ID == migration.id {
				checksum = artifact.Checksum
				break
			}
		}
		if checksum == "" {
			t.Fatalf("missing artifact %s", migration.id)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(id,applied_at,artifact_checksum) VALUES(?, 'historical', ?)`, migration.id, checksum); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		store := NewAtPath(dbPath, slog.Default())
		if _, err := store.ValidationSnapshot(context.Background(), "project", time.Now().UTC(), time.Minute); err != nil {
			t.Fatalf("open %d validation projection: %v", i+1, err)
		}
		if i == 0 {
			var objects int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('daemon_validation_requests','daemon_validation_state','daemon_validation_requests_insert_revision','daemon_validation_requests_update_revision')`).Scan(&objects); err != nil {
				t.Fatal(err)
			}
			if objects != 4 {
				t.Fatalf("validation migration objects = %d, want 4", objects)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidationLeaseMigrationRollsBackWhenLedgerWriteFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range orderedMigrations[:2] {
		sqlText, loadErr := loadMigrationSQL(migration.path)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if _, err = db.Exec(sqlText); err != nil {
			t.Fatal(err)
		}
		var checksum string
		for _, artifact := range migrationArtifacts {
			if artifact.ID == migration.id {
				checksum = artifact.Checksum
			}
		}
		if _, err = db.Exec(`INSERT INTO schema_migrations(id,applied_at,artifact_checksum) VALUES(?, 'historical', ?)`, migration.id, checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`CREATE TRIGGER reject_validation_ledger BEFORE INSERT ON schema_migrations WHEN NEW.id='daemon_operations_0003_validation_leases' BEGIN SELECT RAISE(ABORT, 'injected validation ledger failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	failed := NewAtPath(dbPath, slog.Default())
	if _, err = failed.ValidationSnapshot(context.Background(), "project", time.Now().UTC(), time.Minute); err == nil || !strings.Contains(err.Error(), "injected validation ledger failure") {
		t.Fatalf("migration error = %v, want injected ledger failure", err)
	}
	_ = failed.Close()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var objects, ledgerRows int
	if err = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'daemon_validation_%' OR name LIKE 'idx_daemon_validation_%'`).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='daemon_operations_0003_validation_leases'`).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if objects != 0 || ledgerRows != 0 {
		t.Fatalf("failed migration residue objects=%d ledger=%d", objects, ledgerRows)
	}
	if _, err = raw.Exec(`DROP TRIGGER reject_validation_ledger`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}

	retried := NewAtPath(dbPath, slog.Default())
	defer retried.Close()
	if _, err = retried.ValidationSnapshot(context.Background(), "project", time.Now().UTC(), time.Minute); err != nil {
		t.Fatalf("retry validation migration: %v", err)
	}
}

func TestValidationLeaseMigrationRejectsAppliedSchemaDrift(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	seed := NewAtPath(dbPath, slog.Default())
	if _, err := seed.ValidationSnapshot(context.Background(), "project", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.db.Exec(`DROP TRIGGER daemon_validation_requests_update_revision`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := NewAtPath(dbPath, slog.Default())
	if _, err := reopened.ValidationSnapshot(context.Background(), "project", time.Now().UTC(), time.Minute); err == nil || !strings.Contains(err.Error(), "missing trigger daemon_validation_requests_update_revision") {
		t.Fatalf("schema drift error = %v", err)
	}
	_ = reopened.Close()
}

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
