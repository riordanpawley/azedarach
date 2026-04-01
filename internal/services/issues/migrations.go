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
	{id: "0004_spec_tables", path: "migrations/0004_spec_tables.sql"},
	{id: "0005_spec_audit_log", path: "migrations/0005_spec_audit_log.sql"},
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

type sqliteColumnSpec struct {
	name string
	ddl  string
}

func (c *Client) ensureSpecSchema(db *sql.DB) error {
	requirementsDDL := `
		CREATE TABLE IF NOT EXISTS spec_requirements (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			local_id TEXT NOT NULL,
			external_code TEXT,
			title TEXT NOT NULL,
			description TEXT,
			issue_id TEXT,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE SET NULL
		)
	`
	if _, err := db.Exec(requirementsDDL); err != nil {
		return fmt.Errorf("ensure spec_requirements table: %w", err)
	}
	if err := ensureTableColumns(db, "spec_requirements", []sqliteColumnSpec{
		{name: "local_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "external_code", ddl: "TEXT"},
		{name: "title", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "description", ddl: "TEXT"},
		{name: "issue_id", ddl: "TEXT"},
		{name: "status", ddl: "TEXT NOT NULL DEFAULT 'open'"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return fmt.Errorf("ensure spec_requirements columns: %w", err)
	}

	linksDDL := `
		CREATE TABLE IF NOT EXISTS spec_links (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			requirement_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			note TEXT,
			implementations_json TEXT,
			fulfillment_status TEXT,
			fulfilled_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE,
			FOREIGN KEY (requirement_id) REFERENCES spec_requirements(id) ON DELETE CASCADE
		)
	`
	if _, err := db.Exec(linksDDL); err != nil {
		return fmt.Errorf("ensure spec_links table: %w", err)
	}
	if err := ensureTableColumns(db, "spec_links", []sqliteColumnSpec{
		{name: "issue_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "requirement_id", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "role", ddl: "TEXT NOT NULL DEFAULT 'implements'"},
		{name: "note", ddl: "TEXT"},
		{name: "implementations_json", ddl: "TEXT"},
		{name: "fulfillment_status", ddl: "TEXT"},
		{name: "fulfilled_at", ddl: "TEXT"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return fmt.Errorf("ensure spec_links columns: %w", err)
	}

	indexes := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_spec_requirements_active_local_id ON spec_requirements(local_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_spec_requirements_active_external_code ON spec_requirements(external_code) WHERE deleted_at IS NULL AND external_code IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spec_requirements_issue_status_updated ON spec_requirements(issue_id, status, updated_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spec_requirements_updated ON spec_requirements(updated_at DESC, local_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_spec_links_active_issue_requirement ON spec_links(issue_id, requirement_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spec_links_issue_role_updated ON spec_links(issue_id, role, updated_at DESC) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spec_links_requirement_role_updated ON spec_links(requirement_id, role, updated_at DESC) WHERE deleted_at IS NULL`,
	}
	for _, stmt := range indexes {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure spec schema index: %w", err)
		}
	}

	return nil
}

func (c *Client) ensureSpecAuditSchema(db *sql.DB) error {
	auditDDL := `
		CREATE TABLE IF NOT EXISTS spec_audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			actor_source TEXT NOT NULL,
			before_json TEXT NOT NULL,
			after_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		)
	`
	if _, err := db.Exec(auditDDL); err != nil {
		return fmt.Errorf("ensure spec_audit_log table: %w", err)
	}
	if err := ensureTableColumns(db, "spec_audit_log", []sqliteColumnSpec{
		{name: "entity_type", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "entity_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "operation", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "actor_source", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "before_json", ddl: "TEXT NOT NULL DEFAULT 'null'"},
		{name: "after_json", ddl: "TEXT NOT NULL DEFAULT 'null'"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
	}); err != nil {
		return fmt.Errorf("ensure spec_audit_log columns: %w", err)
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_spec_audit_entity_created_at ON spec_audit_log(entity_type, entity_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_spec_audit_created_at ON spec_audit_log(created_at, id)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure spec audit index: %w", err)
		}
	}

	return nil
}

func ensureTableColumns(db *sql.DB, tableName string, columns []sqliteColumnSpec) error {
	existing, err := tableColumns(db, tableName)
	if err != nil {
		return err
	}

	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, column.name, column.ddl)
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("add column %s.%s: %w", tableName, column.name, err)
		}
	}

	return nil
}

func tableColumns(db *sql.DB, tableName string) (map[string]struct{}, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info('%s')", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string]struct{})
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}
