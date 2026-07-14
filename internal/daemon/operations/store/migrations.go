package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/sqlitemigration"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	id   string
	path string
}

var orderedMigrations = []migration{
	{id: "daemon_operations_0001", path: "migrations/0001_daemon_operations.sql"},
	{id: "daemon_operations_0002_progress", path: "migrations/0002_daemon_operation_progress.sql"},
	{id: "daemon_operations_0003_validation_leases", path: "migrations/0003_validation_leases.sql"},
}

var migrationArtifacts = []sqlitemigration.Artifact{
	{ID: "daemon_operations_0001", Path: "migrations/0001_daemon_operations.sql", Checksum: "573eaaeacb02741d90c12f4d0c747120dd7422dad46d65b0ca2e10fa77408321"},
	{ID: "daemon_operations_0002_progress", Path: "migrations/0002_daemon_operation_progress.sql", Checksum: "57fde6bd6348ea3925a7c027facbf23185bff81ebe659dfb6f5de1f2bb0c009a"},
	{ID: "daemon_operations_0003_validation_leases", Path: "migrations/0003_validation_leases.sql", Checksum: "317c9ea680d378637b417005ccfcf0d989e4c025cbbacabdd581f00dafac19df"},
}

const migrationArtifactAuthority sqlitemigration.Authority = "project.daemon_operations"

func runMigrations(ctx context.Context, db *sql.DB) error {
	if err := sqlitemigration.Validate(migrationFiles, migrationArtifacts); err != nil {
		return fmt.Errorf("validate migration registry: %w", err)
	}
	registrations := make([]sqlitemigration.Artifact, 0, len(orderedMigrations))
	for _, migration := range orderedMigrations {
		registrations = append(registrations, sqlitemigration.Artifact{ID: migration.id, Path: migration.path})
	}
	if err := sqlitemigration.ValidateRegistrations(migrationArtifacts, registrations); err != nil {
		return fmt.Errorf("validate migration artifact coverage: %w", err)
	}
	if err := ensureMigrationTable(ctx, db); err != nil {
		return err
	}
	if err := sqlitemigration.EnsureLedgerChecksumsAtomic(ctx, db, migrationArtifactAuthority, migrationArtifacts); err != nil {
		return err
	}
	for _, migration := range orderedMigrations {
		applied, err := isMigrationApplied(ctx, db, migration.id)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", migration.id, err)
		}
		if applied {
			continue
		}
		sqlText, err := loadMigrationSQL(migration.path)
		if err != nil {
			return fmt.Errorf("load migration %s: %w", migration.id, err)
		}
		if err := applyMigration(ctx, db, migration.id, sqlText); err != nil {
			return err
		}
	}
	if err := sqlitemigration.EnsureLedgerChecksumsAtomic(ctx, db, migrationArtifactAuthority, migrationArtifacts); err != nil {
		return err
	}
	return validateValidationLeaseSchema(ctx, db)
}

func validateValidationLeaseSchema(ctx context.Context, db *sql.DB) error {
	required := []struct {
		typeName  string
		name      string
		fragments []string
	}{
		{typeName: "table", name: "daemon_validation_requests", fragments: []string{
			"create table daemon_validation_requests",
			"sequence integer primary key autoincrement",
			"request_id text not null unique",
			"lease_token_hash text not null check (length(lease_token_hash) = 64)",
			"project_id text not null",
			"issue_id text not null",
			"class text not null check (class in ('aggregate','shared','safe'))",
			"profile text not null",
			"command text not null",
			"source_revision text not null",
			"state text not null check (state in ('queued','active','completed','cancelled','expired','failed'))",
			"queued_at text not null",
			"started_at text",
			"heartbeat_at text",
			"expires_at text",
			"finished_at text",
			"outcome text not null default ''",
			"evidence_json text not null default '{}'",
		}},
		{typeName: "table", name: "daemon_validation_state", fragments: []string{
			"create table daemon_validation_state",
			"project_id text primary key",
			"revision integer not null check (revision > 0)",
		}},
		{typeName: "index", name: "idx_daemon_validation_one_active_aggregate", fragments: []string{
			"create unique index idx_daemon_validation_one_active_aggregate",
			"on daemon_validation_requests(project_id)",
			"where state = 'active' and class = 'aggregate'",
		}},
		{typeName: "index", name: "idx_daemon_validation_project_queue", fragments: []string{
			"create index idx_daemon_validation_project_queue",
			"on daemon_validation_requests(project_id, state, sequence)",
		}},
		{typeName: "index", name: "idx_daemon_validation_expiry", fragments: []string{
			"create index idx_daemon_validation_expiry",
			"on daemon_validation_requests(state, expires_at)",
		}},
		{typeName: "trigger", name: "daemon_validation_requests_insert_revision", fragments: []string{
			"create trigger daemon_validation_requests_insert_revision",
			"after insert on daemon_validation_requests",
			"insert into daemon_validation_state(project_id, revision)",
			"values(new.project_id, 1)",
			"on conflict(project_id) do update set revision = revision + 1",
		}},
		{typeName: "trigger", name: "daemon_validation_requests_update_revision", fragments: []string{
			"create trigger daemon_validation_requests_update_revision",
			"after update on daemon_validation_requests",
			"insert into daemon_validation_state(project_id, revision)",
			"values(new.project_id, 1)",
			"on conflict(project_id) do update set revision = revision + 1",
		}},
	}
	for _, object := range required {
		var definition string
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.typeName, object.name).Scan(&definition); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("validation lease schema drift: missing %s %s", object.typeName, object.name)
			}
			return fmt.Errorf("validate validation lease %s %s: %w", object.typeName, object.name, err)
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(definition), " "))
		for _, fragment := range object.fragments {
			if !strings.Contains(normalized, fragment) {
				return fmt.Errorf("validation lease schema drift: %s %s is missing %q", object.typeName, object.name, fragment)
			}
		}
	}
	return nil
}

func ensureMigrationTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS schema_migrations (
            id TEXT PRIMARY KEY,
            applied_at TEXT NOT NULL,
            artifact_checksum TEXT
        )
    `)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}
	return nil
}

func isMigrationApplied(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM schema_migrations WHERE id = ?
        )
    `, id).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func loadMigrationSQL(path string) (string, error) {
	content, err := fs.ReadFile(migrationFiles, path)
	if err != nil {
		return "", err
	}
	sqlText := strings.TrimSpace(string(content))
	if sqlText == "" {
		return "", fmt.Errorf("empty migration sql")
	}
	return sqlText, nil
}

func applyMigration(ctx context.Context, db *sql.DB, id, sqlText string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}
	if err := sqlitemigration.RecordApplied(ctx, tx, migrationArtifacts, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}
