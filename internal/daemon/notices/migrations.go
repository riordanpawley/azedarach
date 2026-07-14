package notices

import (
	"context"
	"database/sql"
	"embed"
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
	{id: "daemon_notices_0001", path: "migrations/0001_daemon_notices.sql"},
}

var migrationArtifacts = []sqlitemigration.Artifact{
	{ID: "daemon_notices_0001", Path: "migrations/0001_daemon_notices.sql", Checksum: "05a47c2b8c1e16e79062f0d19762bce056c7fd56c05bc33d06ccf5b2ea7e008a"},
}

const migrationArtifactAuthority sqlitemigration.Authority = "project.daemon_notices"

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
	return sqlitemigration.EnsureLedgerChecksumsAtomic(ctx, db, migrationArtifactAuthority, migrationArtifacts)
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
