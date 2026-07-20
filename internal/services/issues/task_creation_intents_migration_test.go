package issues

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskCreationIntentsMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTaskCreationIntentsSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(ctx, CreateTaskParams{Title: "history"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_task_creation_intents_issue; DROP TABLE task_creation_intents; DELETE FROM schema_migrations WHERE id=?`, taskCreationIntentsMigrationID); err != nil {
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
	if err := validateTaskCreationIntentsSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var history, marker int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE title='history'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history=%d err=%v", history, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=? AND artifact_checksum=?`, taskCreationIntentsMigrationID, taskCreationIntentsMigrationChecksum).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("marker=%d err=%v", marker, err)
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

func TestTaskCreationIntentsMigrationRollsBackAndRejectsDrift(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_task_creation_intents_issue; DROP TABLE task_creation_intents; DELETE FROM schema_migrations WHERE id=?`, taskCreationIntentsMigrationID); err != nil {
		t.Fatal(err)
	}
	sqlText, err := loadMigrationSQL("migrations/0059_task_creation_intents.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.applyMigration(ctx, db, taskCreationIntentsMigrationID, sqlText+`; INSERT INTO missing_task_creation_failure VALUES(1)`); err == nil {
		t.Fatal("injected migration unexpectedly succeeded")
	}
	var tables, markers int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='task_creation_intents'`).Scan(&tables)
	_ = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, taskCreationIntentsMigrationID).Scan(&markers)
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
	if _, err := db.Exec(`DROP INDEX idx_task_creation_intents_issue; CREATE INDEX idx_task_creation_intents_issue ON task_creation_intents(issue_id)`); err != nil {
		t.Fatal(err)
	}
	if err := retried.CloseDB(); err != nil {
		t.Fatal(err)
	}
	drifted := NewClientAtPath(path, slog.Default())
	if _, err := drifted.dbHandle(); err == nil || !strings.Contains(err.Error(), "task creation intents schema drifted") {
		t.Fatalf("drift error=%v", err)
	}
	_ = drifted.CloseDB()
}
