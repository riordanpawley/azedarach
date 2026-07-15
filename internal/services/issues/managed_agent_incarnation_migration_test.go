package issues

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedAgentIncarnationMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedAgentIdentitySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO daemon_managed_agent_incarnations(project_id,session_id,logical_pane_id,tmux_pane_id,pane_pid,agent_incarnation,observed_at,updated_at) VALUES('p','s','agent','12',100,'a','2026-07-15T00:00:00Z','2026-07-15T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	reopenedDB, err := reopened.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.CloseDB()
	var rows, markers int
	if err := reopenedDB.QueryRow(`SELECT COUNT(*) FROM daemon_managed_agent_incarnations WHERE agent_incarnation='a'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("preserved rows=%d err=%v", rows, err)
	}
	if err := reopenedDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='0049_managed_agent_incarnations'`).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("migration markers=%d err=%v", markers, err)
	}
}

func TestManagedAgentIncarnationMigrationRejectsIndexDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, slog.Default())
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_daemon_managed_agent_physical_incarnation`); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "managed agent identity schema drifted") {
		t.Fatalf("drift error=%v", err)
	}
	_ = reopened.CloseDB()
}
