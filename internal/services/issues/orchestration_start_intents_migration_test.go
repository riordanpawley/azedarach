package issues

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrchestrationStartIntentsMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOrchestrationStartIntentsSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(ctx, CreateTaskParams{Title: "history"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_orchestration_start_intents_recovery; DROP INDEX idx_orchestration_start_intents_dedupe; DROP TABLE orchestration_start_intents; DELETE FROM schema_migrations WHERE id=?`, orchestrationStartIntentsMigrationID); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}

	upgraded := NewClientAtPath(path, slog.Default())
	db, err = upgraded.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOrchestrationStartIntentsSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var history, markers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE title='history'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("historical rows=%d err=%v", history, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND artifact_checksum=?`, orchestrationStartIntentsMigrationID, "68b5ca7149782ade0701bd684e23379145b312805e022ad33e5f267c29cc3a00").Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("migration markers=%d err=%v", markers, err)
	}
	if err := upgraded.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	defer reopened.CloseDB()
	if _, err := reopened.dbHandle(); err != nil {
		t.Fatal(err)
	}
}

func TestOrchestrationStartIntentsMigrationRollsBackAndRejectsDrift(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_orchestration_start_intents_recovery; DROP INDEX idx_orchestration_start_intents_dedupe; DROP TABLE orchestration_start_intents; DELETE FROM schema_migrations WHERE id=?`, orchestrationStartIntentsMigrationID); err != nil {
		t.Fatal(err)
	}
	sqlText, err := loadMigrationSQL("migrations/0058_orchestration_start_intents.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.applyMigration(ctx, db, orchestrationStartIntentsMigrationID, sqlText+`; INSERT INTO missing_orchestration_start_failure VALUES(1)`); err == nil {
		t.Fatal("injected migration unexpectedly succeeded")
	}
	var tables, markers int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='orchestration_start_intents'`).Scan(&tables)
	_ = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, orchestrationStartIntentsMigrationID).Scan(&markers)
	if tables != 0 || markers != 0 {
		t.Fatalf("rollback tables=%d markers=%d", tables, markers)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}

	retried := NewClientAtPath(path, slog.Default())
	db, err = retried.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_orchestration_start_intents_recovery; CREATE INDEX idx_orchestration_start_intents_recovery ON orchestration_start_intents (project_id, issue_id)`); err != nil {
		t.Fatal(err)
	}
	if err := retried.CloseDB(); err != nil {
		t.Fatal(err)
	}
	drifted := NewClientAtPath(path, slog.Default())
	if _, err := drifted.dbHandle(); err == nil || !strings.Contains(err.Error(), "orchestration start intents schema drifted") {
		t.Fatalf("drift error=%v", err)
	}
	_ = drifted.CloseDB()
}
