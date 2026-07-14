package issues

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestDecisionPropagationOutboxMigrationFreshHistoricalAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	issueID, err := seed.Create(ctx, CreateTaskParams{Title: "preserved", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDecisionPropagationOutboxSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE decision_propagation_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE id=?`, decisionPropagationOutboxMigrationID); err != nil {
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
	if err := validateDecisionPropagationOutboxSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.GetWithRuntime(ctx, "migration-test", issueID); err != nil {
		t.Fatalf("historical row not preserved: %v", err)
	}
	var checksum string
	if err := db.QueryRow(`SELECT artifact_checksum FROM schema_migrations WHERE id=?`, decisionPropagationOutboxMigrationID).Scan(&checksum); err != nil || checksum != "a12c44ba35156d71fbcd88a9d78e4cdb234e75e7e4aef5f896c8b1182ada858d" {
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
	var markerCount int
	if err := reopenedDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, decisionPropagationOutboxMigrationID).Scan(&markerCount); err != nil || markerCount != 1 {
		t.Fatalf("idempotent marker count=%d err=%v", markerCount, err)
	}
}

func TestDecisionPropagationOutboxMigrationRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	issueID, err := seed.Create(ctx, CreateTaskParams{Title: "rollback sentinel", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE decision_propagation_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE id=?`, decisionPropagationOutboxMigrationID); err != nil {
		t.Fatal(err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}

	failed := NewClientAtPath(path, slog.Default())
	failed.decisionOutboxMigrationFailureHook = func(stage string) error {
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
	var tableCount, markerCount, issueCount int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='decision_propagation_outbox'`).Scan(&tableCount)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, decisionPropagationOutboxMigrationID).Scan(&markerCount)
	_ = raw.QueryRow(`SELECT COUNT(*) FROM issues WHERE id=?`, issueID).Scan(&issueCount)
	_ = raw.Close()
	if tableCount != 0 || markerCount != 0 || issueCount != 1 {
		t.Fatalf("rollback table=%d marker=%d issue=%d", tableCount, markerCount, issueCount)
	}
	retried := NewClientAtPath(path, slog.Default())
	retriedDB, err := retried.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	defer retried.CloseDB()
	if err := validateDecisionPropagationOutboxSchema(ctx, retriedDB); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionPropagationOutboxMigrationRejectsAppliedSchemaDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, slog.Default())
	db, err := seed.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_decision_propagation_outbox_active`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_decision_propagation_outbox_active ON decision_propagation_outbox(retired_at)`); err != nil {
		t.Fatal(err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}
	reopened := NewClientAtPath(path, slog.Default())
	if _, err := reopened.dbHandle(); err == nil || !strings.Contains(err.Error(), "idx_decision_propagation_outbox_active") || !strings.Contains(err.Error(), "drifted") {
		t.Fatalf("drift error=%v, want wrong-definition outbox index rejection", err)
	}
	_ = reopened.CloseDB()
}
