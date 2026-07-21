package issues

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootedBootstrapAcknowledgementMigrationFreshHistoricalReopenAndSchemaDrift(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, rootedBootstrapAcknowledgementMigrationID).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != "b54bdf5ec3f6af17c91e1625582ac58e66e47948cea68ee73db88d4e8df6f161" {
		t.Fatalf("migration checksum = %q", checksum)
	}
	if !rootedBootstrapTableExists(t, db) {
		t.Fatal("fresh migration missing rooted bootstrap acknowledgement table")
	}
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "preserve historical issue"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE daemon_rooted_bootstrap_acknowledgements; DELETE FROM schema_migrations WHERE id IN (?,?)`, rootedBootstrapAcknowledgementMigrationID, managedAgentThreadIdentityMigrationID); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}

	historical := NewClientAtPath(path, slog.Default())
	historicalDB, err := historical.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if !rootedBootstrapTableExists(t, historicalDB) {
		t.Fatal("historical upgrade missing rooted bootstrap acknowledgement table")
	}
	if _, err := historical.GetWithRuntime(ctx, "migration-test", issueID); err != nil {
		t.Fatalf("historical issue not preserved: %v", err)
	}
	if err := historical.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	reopenedDB, err := reopened.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var migrationRows int
	if err := reopenedDB.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id=?`, rootedBootstrapAcknowledgementMigrationID).Scan(&migrationRows); err != nil || migrationRows != 1 {
		t.Fatalf("idempotent reopen migration rows=%d err=%v", migrationRows, err)
	}
	if _, err := reopenedDB.Exec(`DROP TABLE daemon_rooted_bootstrap_acknowledgements`); err != nil {
		t.Fatal(err)
	}
	if err := reopened.CloseDB(); err != nil {
		t.Fatal(err)
	}
	driftRepair := NewClientAtPath(path, slog.Default())
	if _, err := driftRepair.dbHandle(); err == nil || !strings.Contains(err.Error(), "managed thread identity column daemon_rooted_bootstrap_acknowledgements.tmux_pane_id is missing") {
		t.Fatalf("applied migration drift error = %v, want fail-closed missing pinned column", err)
	}
}

func TestRootedBootstrapAcknowledgementMigrationRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseDB() //nolint:errcheck
	if _, err := db.Exec(`DROP TABLE daemon_rooted_bootstrap_acknowledgements; DELETE FROM schema_migrations WHERE id=?`, rootedBootstrapAcknowledgementMigrationID); err != nil {
		t.Fatal(err)
	}
	sqlText, err := loadMigrationSQL("migrations/0049_rooted_bootstrap_acknowledgements.sql")
	if err != nil {
		t.Fatal(err)
	}
	err = client.applyMigration(context.Background(), db, rootedBootstrapAcknowledgementMigrationID, sqlText+`; INSERT INTO definitely_missing_table VALUES (1)`)
	if err == nil {
		t.Fatal("injected migration failure unexpectedly succeeded")
	}
	if rootedBootstrapTableExists(t, db) {
		t.Fatal("failed migration left candidate table behind")
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id=?`, rootedBootstrapAcknowledgementMigrationID).Scan(&rows); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("failed migration recorded ledger rows=%d", rows)
	}
}

func rootedBootstrapTableExists(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='daemon_rooted_bootstrap_acknowledgements'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}
