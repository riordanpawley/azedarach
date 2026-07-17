package issues

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRootedSessionRoleExclusivityMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := seed.Create(ctx, CreateTaskParams{Title: "preserved root", Type: domain.TypeEpic})
	if err != nil {
		t.Fatal(err)
	}
	if err := seedRootedWorkerDual(ctx, db, rootID, "az-"+rootID); err != nil {
		t.Fatal(err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	upgraded := NewClientAtPath(path, slog.Default())
	db, err = upgraded.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRootedSessionRoleExclusivitySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.GetWithRuntime(ctx, "migration-test", rootID); err != nil {
		t.Fatalf("historical issue not preserved: %v", err)
	}
	assertRootedIntentOnly(t, db, rootID, "az-"+rootID)
	var checksum, appliedAt string
	if err := db.QueryRow(`SELECT artifact_checksum,applied_at FROM schema_migrations WHERE id=?`, rootedSessionRoleExclusivityMigrationID).Scan(&checksum, &appliedAt); err != nil {
		t.Fatal(err)
	}
	if checksum != rootedSessionRoleExclusivityChecksum {
		t.Fatalf("checksum=%q", checksum)
	}
	if err := upgraded.CloseDB(); err != nil {
		t.Fatal(err)
	}

	reopened := NewClientAtPath(path, slog.Default())
	reopenedDB, err := reopened.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.CloseDB() //nolint:errcheck
	var rows int
	var reopenedAppliedAt string
	if err := reopenedDB.QueryRow(`SELECT COUNT(*),MAX(applied_at) FROM schema_migrations WHERE id=?`, rootedSessionRoleExclusivityMigrationID).Scan(&rows, &reopenedAppliedAt); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || reopenedAppliedAt != appliedAt {
		t.Fatalf("idempotent reopen rows=%d applied_at=%q want=%q", rows, reopenedAppliedAt, appliedAt)
	}
	assertRootedIntentOnly(t, reopenedDB, rootID, "az-"+rootID)
}

func TestRootedSessionRoleExclusivityMigrationRollbackPreservesHistoricalRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := seed.Create(ctx, CreateTaskParams{Title: "rollback root", Type: domain.TypeEpic})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "az-" + rootID
	if err := seedRootedWorkerDual(ctx, db, rootID, sessionID); err != nil {
		t.Fatal(err)
	}
	sqlText, err := loadMigrationSQL("migrations/0053_rooted_session_role_exclusivity.sql")
	if err != nil {
		t.Fatal(err)
	}
	err = seed.applyMigration(ctx, db, rootedSessionRoleExclusivityMigrationID, sqlText+`; INSERT INTO definitely_missing_table VALUES (1)`)
	if err == nil {
		t.Fatal("injected migration failure unexpectedly succeeded")
	}
	var intents, indexCount, ledgerRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM daemon_session_projections WHERE session_id=?`, sessionID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_daemon_session_projections_physical_session_unique'`).Scan(&indexCount)
	_ = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, rootedSessionRoleExclusivityMigrationID).Scan(&ledgerRows)
	if intents != 2 || indexCount != 0 || ledgerRows != 0 {
		t.Fatalf("rollback intents=%d index=%d ledger=%d", intents, indexCount, ledgerRows)
	}
	_ = seed.CloseDB()
}

func TestRootedSessionRoleExclusivityMigrationConvergesRequiredStatePairs(t *testing.T) {
	for _, tc := range []struct {
		name, workerState, rootedState string
	}{
		{name: "running-running", workerState: "running", rootedState: "running"},
		{name: "stopped-stopped", workerState: "stopped", rootedState: "stopped"},
		{name: "stopped-running", workerState: "stopped", rootedState: "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "issues.db")
			seed := NewClientAtPath(path, slog.Default())
			db, err := seed.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			rootID, err := seed.Create(ctx, CreateTaskParams{Title: tc.name, Type: domain.TypeEpic})
			if err != nil {
				t.Fatal(err)
			}
			sessionID := "az-" + rootID
			if err := downgradeRootedSessionRoleExclusivity(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := insertRootedWorkerDual(ctx, db, rootID, sessionID, tc.workerState, tc.rootedState); err != nil {
				t.Fatal(err)
			}
			_ = seed.CloseDB()
			upgraded := NewClientAtPath(path, slog.Default())
			db, err = upgraded.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.CloseDB() //nolint:errcheck
			assertRootedIntentOnly(t, db, rootID, sessionID)
			var state string
			if err := db.QueryRow(`SELECT state FROM daemon_session_projections WHERE session_id=?`, sessionID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != tc.rootedState {
				t.Fatalf("rooted state=%q want=%q", state, tc.rootedState)
			}
		})
	}
}

func TestRootedSessionRoleExclusivityMigrationRejectsDriftAndAmbiguousDuplicates(t *testing.T) {
	t.Run("applied index drift", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "issues.db")
		seed := NewClientAtPath(path, slog.Default())
		db, err := seed.dbHandle()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP INDEX idx_daemon_session_projections_physical_session_unique`); err != nil {
			t.Fatal(err)
		}
		_ = seed.CloseDB()
		reopened := NewClientAtPath(path, slog.Default())
		_, err = reopened.dbHandle()
		if err == nil || !strings.Contains(err.Error(), "missing idx_daemon_session_projections_physical_session_unique") {
			t.Fatalf("drift error=%v", err)
		}
		_ = reopened.CloseDB()
	})

	t.Run("ambiguous duplicate", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "issues.db")
		seed := NewClientAtPath(path, slog.Default())
		db, err := seed.dbHandle()
		if err != nil {
			t.Fatal(err)
		}
		rootID, err := seed.Create(ctx, CreateTaskParams{Title: "ambiguous root", Type: domain.TypeEpic})
		if err != nil {
			t.Fatal(err)
		}
		if err := downgradeRootedSessionRoleExclusivity(ctx, db); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES
			('p','shared',?,'worker','issue',?,'running',?),
			('p','shared','','orchestrator','orchestration','project','running',?)`, rootID, rootID, now, now); err != nil {
			t.Fatal(err)
		}
		_ = seed.CloseDB()
		reopened := NewClientAtPath(path, slog.Default())
		_, err = reopened.dbHandle()
		if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Fatalf("ambiguous migration error=%v", err)
		}
		_ = reopened.CloseDB()
	})
}

func seedRootedWorkerDual(ctx context.Context, db *sql.DB, rootID, sessionID string) error {
	if err := downgradeRootedSessionRoleExclusivity(ctx, db); err != nil {
		return err
	}
	return insertRootedWorkerDual(ctx, db, rootID, sessionID, "stopped", "stopped")
}

func insertRootedWorkerDual(ctx context.Context, db *sql.DB, rootID, sessionID, workerState, rootedState string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, `INSERT INTO daemon_session_projections(project_id,session_id,issue_id,role,scope_kind,scope_id,state,updated_at) VALUES
		('p',?,?, 'worker','issue',?,?,?),
		('p',?,?, 'orchestrator','orchestration',?,?,?)`, sessionID, rootID, rootID, workerState, now, sessionID, rootID, rootID, rootedState, now)
	return err
}

func downgradeRootedSessionRoleExclusivity(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_daemon_session_projections_physical_session_unique;
		DELETE FROM schema_migrations WHERE id='0053_rooted_session_role_exclusivity'`)
	return err
}

func assertRootedIntentOnly(t *testing.T, db *sql.DB, rootID, sessionID string) {
	t.Helper()
	var rows, rooted int
	if err := db.QueryRow(`SELECT COUNT(*),SUM(CASE WHEN role='orchestrator' AND scope_kind='orchestration' AND scope_id=? THEN 1 ELSE 0 END) FROM daemon_session_projections WHERE session_id=?`, rootID, sessionID).Scan(&rows, &rooted); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || rooted != 1 {
		t.Fatalf("session intents rows=%d rooted=%d", rows, rooted)
	}
}
