package issues

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHookRefreshIntentsMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	fresh := NewClientAtPath(path, slog.Default())
	db, err := fresh.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitHookRefreshIntentsSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Create(ctx, CreateTaskParams{Title: "history"}); err != nil {
		t.Fatal(err)
	}
	resetGitHookRefreshIntentsMigration(t, db)
	if err := fresh.CloseDB(); err != nil {
		t.Fatal(err)
	}

	upgraded := NewClientAtPath(path, slog.Default())
	db, err = upgraded.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitHookRefreshIntentsSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var issuesCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE title='history'`).Scan(&issuesCount); err != nil || issuesCount != 1 {
		t.Fatalf("historical issue count=%d err=%v", issuesCount, err)
	}
	var checksum string
	if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, gitHookRefreshIntentsMigrationID).Scan(&checksum); err != nil || checksum != "7eecd212c9b9a5907c425870ee861571d7654929d77067a1fc50c2e857c3335c" {
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
	if err := reopenedDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, gitHookRefreshIntentsMigrationID).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("idempotent marker count=%d err=%v", markers, err)
	}
}

func TestGitHookRefreshIntentsMigrationRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	resetGitHookRefreshIntentsMigration(t, db)
	sqlText, err := loadMigrationSQL("migrations/0053_git_hook_refresh_intents.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.applyMigration(ctx, db, gitHookRefreshIntentsMigrationID, sqlText+`; INSERT INTO missing_git_hook_refresh_failure VALUES(1)`); err == nil {
		t.Fatal("injected migration unexpectedly succeeded")
	}
	var tables, markers int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='daemon_git_hook_refresh_intents'`).Scan(&tables)
	_ = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, gitHookRefreshIntentsMigrationID).Scan(&markers)
	if tables != 0 || markers != 0 {
		t.Fatalf("rollback tables=%d markers=%d, want zero", tables, markers)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}

	retried := NewClientAtPath(path, slog.Default())
	retriedDB, err := retried.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer retried.CloseDB()
	if err := validateGitHookRefreshIntentsSchema(ctx, retriedDB); err != nil {
		t.Fatal(err)
	}
}

func TestGitHookRefreshIntentsMigrationRejectsIndexDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_daemon_git_hook_refresh_intents_pending`); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "git hook refresh intents schema drifted") {
		t.Fatalf("drift error=%v", err)
	}
	_ = reopened.CloseDB()
}

func TestGitHookRefreshIntentsMigrationRejectsColumnConstraintAndIndexDrift(t *testing.T) {
	canonical, err := loadMigrationSQL("migrations/0053_git_hook_refresh_intents.sql")
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		name string
		old  string
		new  string
	}{
		{name: "primary key", old: "PRIMARY KEY (project_id, worktree)", new: "UNIQUE (project_id, worktree)"},
		{name: "project id type", old: "project_id TEXT NOT NULL", new: "project_id BLOB NOT NULL"},
		{name: "requested generation nullable", old: "requested_generation INTEGER NOT NULL", new: "requested_generation INTEGER"},
		{name: "completed generation type", old: "completed_generation INTEGER NOT NULL", new: "completed_generation TEXT NOT NULL"},
		{name: "completed generation nullable", old: "completed_generation INTEGER NOT NULL", new: "completed_generation INTEGER"},
		{name: "completed generation missing default", old: " DEFAULT 0 CHECK (completed_generation >= 0)", new: " CHECK (completed_generation >= 0)"},
		{name: "completed generation wrong default", old: "DEFAULT 0 CHECK (completed_generation >= 0)", new: "DEFAULT 1 CHECK (completed_generation >= 0)"},
		{name: "requested at nullable", old: "requested_at TEXT NOT NULL", new: "requested_at TEXT"},
		{name: "completed at not null", old: "completed_at TEXT,", new: "completed_at TEXT NOT NULL,"},
		{name: "project id check", old: "CHECK (trim(project_id) <> '')", new: "CHECK (1)"},
		{name: "worktree check", old: "CHECK (trim(worktree) <> '')", new: "CHECK (1)"},
		{name: "requested generation check", old: "CHECK (requested_generation > 0)", new: "CHECK (1)"},
		{name: "completed generation check", old: "CHECK (completed_generation >= 0)", new: "CHECK (1)"},
		{name: "generation ordering check", old: "CHECK (completed_generation <= requested_generation)", new: "CHECK (1)"},
		{name: "pending index order", old: "(project_id, requested_generation, completed_generation)", new: "(project_id, completed_generation, requested_generation)"},
		{name: "pending index extra column", old: "(project_id, requested_generation, completed_generation)", new: "(project_id, requested_generation, completed_generation, worktree)"},
		{name: "pending index descending", old: "requested_generation, completed_generation", new: "requested_generation DESC, completed_generation"},
		{name: "pending index collation", old: "(project_id, requested_generation", new: "(project_id COLLATE NOCASE, requested_generation"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.db")
			client := NewClientAtPath(path, slog.Default())
			db, err := client.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TABLE daemon_git_hook_refresh_intents`); err != nil {
				t.Fatal(err)
			}
			malformed := strings.Replace(canonical, fixture.old, fixture.new, 1)
			if malformed == canonical {
				t.Fatalf("fixture did not alter canonical schema: %s", fixture.name)
			}
			if _, err := db.Exec(malformed); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseDB(); err != nil {
				t.Fatal(err)
			}
			reopened := NewClientAtPath(path, slog.Default())
			if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "git hook refresh intents schema drifted") {
				t.Fatalf("drift error=%v", err)
			}
			_ = reopened.CloseDB()
		})
	}
}

func resetGitHookRefreshIntentsMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_daemon_git_hook_refresh_intents_pending; DROP TABLE IF EXISTS daemon_git_hook_refresh_intents; DELETE FROM schema_migrations WHERE id=?`, gitHookRefreshIntentsMigrationID); err != nil {
		t.Fatal(err)
	}
}
