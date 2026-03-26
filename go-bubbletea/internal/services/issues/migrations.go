package issues

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	id          string
	path        string
	shouldApply func(context.Context, *sql.DB) (bool, error)
}

var orderedMigrations = []migration{
	{id: "0001_bootstrap_tables", path: "migrations/0001_bootstrap_tables.sql"},
	{id: "0002_dependency_foreign_keys", path: "migrations/0002_dependency_foreign_keys.sql", shouldApply: shouldApplyDependencyFKMigration},
	{id: "0003_issue_indexes", path: "migrations/0003_issue_indexes.sql"},
}

func (c *Client) runMigrations(ctx context.Context, db *sql.DB) error {
	if err := ensureMigrationTable(ctx, db); err != nil {
		return err
	}

	for _, m := range orderedMigrations {
		applied, err := isMigrationApplied(ctx, db, m.id)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", m.id, err)
		}
		if applied {
			continue
		}

		shouldApply := true
		if m.shouldApply != nil {
			shouldApply, err = m.shouldApply(ctx, db)
			if err != nil {
				return fmt.Errorf("evaluate migration %s precondition: %w", m.id, err)
			}
		}

		if shouldApply {
			sqlText, err := loadMigrationSQL(m.path)
			if err != nil {
				return fmt.Errorf("load migration %s: %w", m.id, err)
			}
			if err := c.applyMigration(ctx, db, m.id, sqlText); err != nil {
				return err
			}
			continue
		}

		if err := recordAppliedMigration(ctx, db, m.id); err != nil {
			return fmt.Errorf("record skipped migration %s: %w", m.id, err)
		}
	}

	return nil
}

func ensureMigrationTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
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

func recordAppliedMigration(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO schema_migrations (id, applied_at)
		VALUES (?, ?)
	`, id, time.Now().UTC().Format(time.RFC3339Nano))
	return err
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

func (c *Client) applyMigration(ctx context.Context, db *sql.DB, id, sqlText string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	beforeCount, countErr := dependencyCount(tx)
	if countErr != nil {
		return fmt.Errorf("count dependencies before migration %s: %w", id, countErr)
	}

	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration %s: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (id, applied_at)
		VALUES (?, ?)
	`, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	afterCount, countErr := dependencyCount(tx)
	if countErr != nil {
		return fmt.Errorf("count dependencies after migration %s: %w", id, countErr)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil

	if id == "0002_dependency_foreign_keys" {
		if dropped := beforeCount - afterCount; dropped > 0 {
			c.logger.Warn("dropped orphaned dependency edges during sqlite fk migration", "dropped", dropped)
		}
	}

	return nil
}

func dependencyCount(tx *sql.Tx) (int, error) {
	exists, err := tableExists(tx, "issue_dependencies")
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM issue_dependencies`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func tableExists(queryer interface {
	QueryRow(string, ...any) *sql.Row
}, tableName string) (bool, error) {
	var exists bool
	err := queryer.QueryRow(`
		SELECT EXISTS(
			SELECT 1
			FROM sqlite_master
			WHERE type = 'table' AND name = ?
		)
	`, tableName).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func shouldApplyDependencyFKMigration(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list('issue_dependencies')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	hasIssueFK := false
	hasDependsOnFK := false
	for rows.Next() {
		var (
			id       int
			seq      int
			table    string
			from     string
			to       string
			onUpdate string
			onDelete string
			match    string
		)
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, err
		}
		if table != "issues" || to != "id" {
			continue
		}
		if from == "issue_id" {
			hasIssueFK = true
		}
		if from == "depends_on_id" {
			hasDependsOnFK = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	return !(hasIssueFK && hasDependsOnFK), nil
}
