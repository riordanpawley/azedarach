package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionIdempotencyMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDecisionIdempotencySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO decisions(local_id,title,rationale,created_at,updated_at) VALUES('dec-9','legacy','preserved','2026-07-16T00:00:00Z','2026-07-16T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	resetDecisionIdempotencyMigration(t, db)
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	upgraded := NewClientAtPath(path, slog.Default())
	db, err = upgraded.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDecisionIdempotencySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var title, checksum string
	if err := db.QueryRow(`SELECT title FROM decisions WHERE local_id='dec-9'`).Scan(&title); err != nil || title != "legacy" {
		t.Fatalf("historical decision title=%q err=%v", title, err)
	}
	if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, decisionIdempotencyMigrationID).Scan(&checksum); err != nil || checksum != "86d5400fe33bbc19e7e848bc232335809f76d85e4d45a6e45f6bc7ff77547f47" {
		t.Fatalf("checksum=%q err=%v", checksum, err)
	}
	if err := upgraded.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	reopenedDB, err := reopened.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.CloseDB()
	var markers int
	if err := reopenedDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, decisionIdempotencyMigrationID).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("idempotent marker count=%d err=%v", markers, err)
	}
}

func TestDecisionIdempotencyMigrationRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO decisions(local_id,title,rationale,created_at,updated_at) VALUES('dec-9','legacy','preserved','2026-07-16T00:00:00Z','2026-07-16T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	resetDecisionIdempotencyMigration(t, db)
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	failed := NewClientAtPath(path, slog.Default())
	failed.decisionIdempotencyFailureHook = func(stage string) error {
		if stage == "after_schema" {
			return errors.New("injected interruption")
		}
		return nil
	}
	if _, err := failed.dbHandle(); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("migration error=%v, want rollback", err)
	}
	_ = failed.CloseDB()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var columns, indexes, markers, decisions int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('decisions') WHERE name='idempotency_key'`).Scan(&columns)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_decisions_idempotency_key'`).Scan(&indexes)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, decisionIdempotencyMigrationID).Scan(&markers)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM decisions WHERE local_id='dec-9'`).Scan(&decisions)
	_ = raw.Close()
	if columns != 0 || indexes != 0 || markers != 0 || decisions != 1 {
		t.Fatalf("rollback columns=%d indexes=%d markers=%d decisions=%d", columns, indexes, markers, decisions)
	}

	retried := NewClientAtPath(path, slog.Default())
	retriedDB, err := retried.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer retried.CloseDB()
	if err := validateDecisionIdempotencySchema(ctx, retriedDB); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionIdempotencyMigrationRejectsIndexDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_decisions_idempotency_key`); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "decision idempotency schema drifted") {
		t.Fatalf("drift error=%v", err)
	}
	_ = reopened.CloseDB()
}

func resetDecisionIdempotencyMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP INDEX idx_decisions_idempotency_key`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE decisions DROP COLUMN idempotency_key`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE id=?`, decisionIdempotencyMigrationID); err != nil {
		t.Fatal(err)
	}
}
