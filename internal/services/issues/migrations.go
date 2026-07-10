package issues

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	id          string
	path        string
	shouldApply func(context.Context, *sql.DB) (bool, error)
	apply       func(context.Context, *sql.DB, string) error
}

var orderedMigrations = []migration{
	{id: "0001_bootstrap_tables", path: "migrations/0001_bootstrap_tables.sql"},
	{id: "0002_dependency_foreign_keys", path: "migrations/0002_dependency_foreign_keys.sql", shouldApply: shouldApplyDependencyFKMigration},
	{id: "0003_issue_indexes", path: "migrations/0003_issue_indexes.sql"},
	{id: "0004_spec_tables", path: "migrations/0004_spec_tables.sql"},
	{id: "0005_spec_audit_log", path: "migrations/0005_spec_audit_log.sql"},
	{id: "0006_external_issue_sync", path: "migrations/0006_external_issue_sync.sql"},
	{id: "0006_issue_external_refs", path: "migrations/0006_issue_external_refs.sql"},
	{id: "0007_external_issue_sync_payload", path: "migrations/0007_external_issue_sync_payload.sql"},
	{id: "0008_decision_tables", path: "migrations/0008_decision_tables.sql"},
	{id: "0009_decision_audit_log", path: "migrations/0009_decision_audit_log.sql"},
	{id: "0010_decisions_refresh", path: "migrations/0010_decisions_refresh.sql"},
	{id: "0011_decisions_consequences", path: "migrations/0011_decisions_consequences.sql"},
	{id: "0012_blocked_status_to_open", path: "migrations/0012_blocked_status_to_open.sql"},
	{id: "0013_closed_runtime_invariants", path: "migrations/0013_closed_runtime_invariants.sql"},
	{id: "0014_linear_sync_external_refs_backfill", path: "migrations/0014_linear_sync_external_refs_backfill.sql"},
	{id: "0015_issue_attachments", path: "migrations/0015_issue_attachments.sql"},
	{id: "0016_issue_search_fts", path: "migrations/0016_issue_search_fts.sql"},
	{id: "0017_spec_requirement_search_fts", path: "migrations/0017_spec_requirement_search_fts.sql"},
	{id: "0018_issue_graph_closure", path: "migrations/0018_issue_graph_closure.sql"},
	{id: "0019_issue_observation_events", path: "migrations/0019_issue_observation_events.sql"},
	{id: "0019_agent_learnings", path: "migrations/0019_agent_learnings.sql"},
	{id: "0020_agent_learning_lifecycle", path: "migrations/0020_agent_learning_lifecycle.sql"},
	{id: "0021_agent_learning_metadata", path: "migrations/0021_agent_learning_metadata.sql"},
	{id: "0021_agent_learning_relations", path: "migrations/0021_agent_learning_relations.sql"},
	{id: "0021_agent_learning_target_state", path: "migrations/0021_agent_learning_target_state.sql"},
	{id: "0025_agent_learning_privacy", path: "migrations/0025_agent_learning_privacy.sql", apply: applyAgentLearningPrivacyMigration},
	{id: "0026_issue_ownership", path: "migrations/0026_issue_ownership.sql", apply: applyIssueOwnershipMigration},
	{id: "0026_decision_search_fts", path: "migrations/0026_decision_search_fts.sql", apply: applyDecisionSearchFTSMigration},
	{id: "0027_issue_id_allocations", path: "migrations/0027_issue_id_allocations.sql"},
	{id: "0028_runtime_projection_order_indexes", path: "migrations/0028_runtime_projection_order_indexes.sql"},
	{id: "0029_issue_state_model_v2"},
	{id: "0030_issue_closed_runtime_v2_triggers", apply: applyIssueClosedRuntimeV2TriggersMigration},
	{id: "0031_board_views", path: "migrations/0031_board_views.sql"},
	{id: "0032_coordination_leases", path: "migrations/0032_coordination_leases.sql"},
	{id: "0033_orchestrator_scope_leases", path: "migrations/0033_orchestrator_scope_leases.sql"},
	{id: "0034_orchestrator_lifecycle_clock", apply: applyOrchestratorLifecycleClockMigration},
	{id: "0032_interaction_requests", path: "migrations/0032_interaction_requests.sql"},
}

const (
	issueStateModelV2MigrationID      = "0029_issue_state_model_v2"
	issueStateModelVersionMetaKey     = "issue:state_model_version"
	issueStateModelV2CutoverMarkerKey = "issue:state_model_v2_cutover"
	issueStateModelV2Version          = "2"
	boardViewsMigrationID             = "0031_board_views"
)

type issueStateModelV2CutoverMarker struct {
	State       string `json:"state"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	BackupPath  string `json:"backup_path,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (c *Client) runMigrations(ctx context.Context, db *sql.DB) error {
	if err := ensureMigrationTable(ctx, db); err != nil {
		return err
	}
	if err := refusePartialIssueStateModelV2Cutover(ctx, db); err != nil {
		return err
	}
	if err := repairIssueBaseSchema(db); err != nil {
		return fmt.Errorf("repair issue base schema: %w", err)
	}
	if err := repairIssueDependencySchema(db); err != nil {
		return fmt.Errorf("repair issue dependency schema: %w", err)
	}
	if err := repairMetaSchema(db); err != nil {
		return fmt.Errorf("repair meta schema: %w", err)
	}
	externalRefsMigrationApplied, err := isMigrationApplied(ctx, db, "0006_issue_external_refs")
	if err != nil {
		return fmt.Errorf("check migration 0006_issue_external_refs before repair: %w", err)
	}
	if externalRefsMigrationApplied {
		if err := repairIssueExternalRefsSchema(db); err != nil {
			return fmt.Errorf("repair issue external refs schema: %w", err)
		}
	}
	if err := c.ensureSpecSchema(db); err != nil {
		return fmt.Errorf("repair spec schema: %w", err)
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
			if m.id == issueStateModelV2MigrationID {
				if err := c.applyIssueStateModelV2Migration(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
			if m.id == boardViewsMigrationID {
				sqlText, err := loadMigrationSQL(m.path)
				if err != nil {
					return fmt.Errorf("load migration %s: %w", m.id, err)
				}
				if err := c.applyBoardViewsMigration(ctx, db, m.id, sqlText); err != nil {
					return err
				}
				continue
			}
			if m.apply != nil {
				if err := m.apply(ctx, db, m.id); err != nil {
					return err
				}
				continue
			}
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

	if err := c.reconcileIssueStateModelV2Drift(ctx, db); err != nil {
		return err
	}

	if err := repairAgentLearningBaseSchema(ctx, db); err != nil {
		return fmt.Errorf("repair agent learning base schema: %w", err)
	}
	if err := repairIssueIDAllocationSchema(ctx, db); err != nil {
		return fmt.Errorf("repair issue id allocation schema: %w", err)
	}
	if err := c.seedBuiltInBoardViews(ctx, db, "default"); err != nil {
		return fmt.Errorf("seed built-in board views: %w", err)
	}

	return nil
}

func repairIssueIDAllocationSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issue_id_allocations (
			id TEXT PRIMARY KEY,
			allocated_at TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		return fmt.Errorf("ensure issue_id_allocations table: %w", err)
	}
	if err := ensureTableColumns(db, "issue_id_allocations", []sqliteColumnSpec{
		{name: "allocated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "source", ddl: "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return fmt.Errorf("ensure issue_id_allocations columns: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	sources := []struct {
		table  string
		column string
		source string
	}{
		{table: "issues", column: "id", source: "issues"},
		{table: "issue_dependencies", column: "issue_id", source: "issue_dependencies.issue_id"},
		{table: "issue_dependencies", column: "depends_on_id", source: "issue_dependencies.depends_on_id"},
		{table: "issue_external_refs", column: "issue_id", source: "issue_external_refs"},
		{table: "azedarach_external_issue_refs", column: "issue_id", source: "azedarach_external_issue_refs"},
		{table: "spec_requirements", column: "issue_id", source: "spec_requirements"},
		{table: "spec_links", column: "issue_id", source: "spec_links"},
		{table: "issue_attachments", column: "issue_id", source: "issue_attachments"},
		{table: "issue_observation_events", column: "issue_id", source: "issue_observation_events"},
		{table: "daemon_session_projections", column: "issue_id", source: "daemon_session_projections"},
		{table: "daemon_session_observations", column: "issue_id", source: "daemon_session_observations"},
		{table: "daemon_worktree_projections", column: "issue_id", source: "daemon_worktree_projections"},
		{table: "agent_learnings", column: "issue_id", source: "agent_learnings"},
		{table: "agent_learning_relations", column: "scope_issue_id", source: "agent_learning_relations"},
	}
	for _, source := range sources {
		if err := seedIssueIDAllocationsFromColumn(ctx, db, source.table, source.column, source.source, now); err != nil {
			return err
		}
	}
	return nil
}

func seedIssueIDAllocationsFromColumn(ctx context.Context, db *sql.DB, tableName, columnName, source, allocatedAt string) error {
	exists, err := tableExists(db, tableName)
	if err != nil {
		return fmt.Errorf("inspect %s for issue id allocation seed: %w", tableName, err)
	}
	if !exists {
		return nil
	}
	columns, err := tableColumns(db, tableName)
	if err != nil {
		return fmt.Errorf("inspect %s columns for issue id allocation seed: %w", tableName, err)
	}
	if _, ok := columns[columnName]; !ok {
		return nil
	}
	stmt := fmt.Sprintf(`
		INSERT INTO issue_id_allocations (id, allocated_at, source)
		SELECT DISTINCT TRIM(%[1]s), ?, ?
		FROM %[2]s
		WHERE TRIM(COALESCE(%[1]s, '')) <> ''
		ON CONFLICT(id) DO NOTHING
	`, columnName, tableName)
	if _, err := db.ExecContext(ctx, stmt, allocatedAt, source); err != nil {
		return fmt.Errorf("seed issue id allocations from %s.%s: %w", tableName, columnName, err)
	}
	return nil
}

func repairIssueBaseSchema(db *sql.DB) error {
	exists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect issues table: %w", err)
	}
	if !exists {
		return nil
	}

	return ensureTableColumns(db, "issues", []sqliteColumnSpec{
		{name: "description", ddl: "TEXT"},
		{name: "status", ddl: "TEXT NOT NULL DEFAULT 'open'"},
		{name: "priority", ddl: "INTEGER NOT NULL DEFAULT 2"},
		{name: "issue_type", ddl: "TEXT NOT NULL DEFAULT 'task'"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "closed_at", ddl: "TEXT"},
		{name: "assignee", ddl: "TEXT"},
		{name: "labels_json", ddl: "TEXT"},
		{name: "implementations_json", ddl: "TEXT"},
		{name: "design", ddl: "TEXT"},
		{name: "notes", ddl: "TEXT"},
		{name: "acceptance", ddl: "TEXT"},
		{name: "estimate", ddl: "INTEGER"},
		{name: "deleted_at", ddl: "TEXT"},
	})
}

func repairIssueDependencySchema(db *sql.DB) error {
	issuesExists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect issues table: %w", err)
	}
	if !issuesExists {
		return nil
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		)
	`); err != nil {
		return fmt.Errorf("ensure issue_dependencies table: %w", err)
	}
	if err := ensureTableColumns(db, "issue_dependencies", []sqliteColumnSpec{
		{name: "issue_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "depends_on_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "dependency_type", ddl: "TEXT NOT NULL DEFAULT 'blocks'"},
		{name: "tombstoned_at", ddl: "TEXT"},
	}); err != nil {
		return err
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_dependencies_issue_active_type ON issue_dependencies(issue_id, tombstoned_at, dependency_type)`,
		`CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on_active_type ON issue_dependencies(depends_on_id, tombstoned_at, dependency_type)`,
		`CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on ON issue_dependencies(depends_on_id, dependency_type, tombstoned_at)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure issue dependency index: %w", err)
		}
	}

	return nil
}

func repairMetaSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("ensure meta table: %w", err)
	}
	return nil
}

func repairIssueExternalRefsSchema(db *sql.DB) error {
	issuesExists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect issues table: %w", err)
	}
	if !issuesExists {
		return nil
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issue_external_refs (
			issue_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			provider_scope TEXT NOT NULL DEFAULT '',
			remote_key TEXT NOT NULL,
			display_key TEXT,
			url TEXT,
			metadata_json TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			PRIMARY KEY (issue_id, provider, provider_scope, remote_key),
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("ensure issue_external_refs table: %w", err)
	}
	if err := ensureTableColumns(db, "issue_external_refs", []sqliteColumnSpec{
		{name: "issue_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "provider", ddl: "TEXT NOT NULL DEFAULT 'linear'"},
		{name: "provider_scope", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "remote_key", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "display_key", ddl: "TEXT"},
		{name: "url", ddl: "TEXT"},
		{name: "metadata_json", ddl: "TEXT"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return err
	}

	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_external_refs_active_remote
			ON issue_external_refs(provider, provider_scope, remote_key)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_issue_external_refs_issue_active
			ON issue_external_refs(issue_id, provider, provider_scope, updated_at DESC)
			WHERE deleted_at IS NULL`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure issue external refs index: %w", err)
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

func applyOrchestratorLifecycleClockMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	columns := []struct{ name, definition string }{
		{"complete_since", "TEXT"},
		{"last_wake_at", "TEXT"},
		{"last_wake_reason", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		exists, err := txColumnExists(ctx, tx, "daemon_orchestrator_scope_leases", column.name)
		if err != nil {
			return fmt.Errorf("inspect migration %s column %s: %w", id, column.name, err)
		}
		if exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE daemon_orchestrator_scope_leases ADD COLUMN "+column.name+" "+column.definition); err != nil {
			return fmt.Errorf("apply migration %s column %s: %w", id, column.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	return nil
}

func (c *Client) applyBoardViewsMigration(ctx context.Context, db *sql.DB, id, sqlText string) error {
	if err := refusePartialBoardViewsSchema(db); err != nil {
		return fmt.Errorf("migration %s: %w", id, err)
	}
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
	if err := c.runBoardViewsMigrationFailureHook("after_schema"); err != nil {
		return fmt.Errorf("migration %s rolled back: %w", id, err)
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

	if id == boardViewsMigrationID && beforeCount != afterCount {
		c.logger.Warn("unexpected dependency count change during board view migration", "before", beforeCount, "after", afterCount)
	}
	return nil
}

func (c *Client) runBoardViewsMigrationFailureHook(stage string) error {
	if c.boardViewsMigrationFailureHook == nil {
		return nil
	}
	if err := c.boardViewsMigrationFailureHook(stage); err != nil {
		return fmt.Errorf("injected board views migration failure at %s: %w", stage, err)
	}
	return nil
}

func refusePartialBoardViewsSchema(db *sql.DB) error {
	exists, err := tableExists(db, "board_views")
	if err != nil {
		return fmt.Errorf("inspect board_views table: %w", err)
	}
	if !exists {
		return nil
	}
	columns, err := tableColumns(db, "board_views")
	if err != nil {
		return fmt.Errorf("inspect board_views columns: %w", err)
	}
	missing := missingBoardViewsColumns(columns)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("refusing startup with partial board_views schema: missing columns %s; restore the database from backup before retrying", strings.Join(missing, ", "))
}

func missingBoardViewsColumns(columns map[string]struct{}) []string {
	required := []string{
		"project_id",
		"id",
		"name",
		"definition_json",
		"built_in",
		"created_at",
		"updated_at",
		"deleted_at",
	}
	missing := make([]string, 0)
	for _, column := range required {
		if _, ok := columns[column]; !ok {
			missing = append(missing, column)
		}
	}
	sort.Strings(missing)
	return missing
}

func refusePartialIssueStateModelV2Cutover(ctx context.Context, db *sql.DB) error {
	marker, ok, err := readIssueStateModelV2CutoverMarker(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect issue state-model v2 cutover marker: %w", err)
	}
	if !ok {
		return nil
	}
	switch marker.State {
	case "complete", "":
		return nil
	default:
		return issueStateModelV2CutoverError("refusing startup after partial issue state-model v2 cutover", marker.BackupPath, marker.Error)
	}
}

func (c *Client) applyIssueStateModelV2Migration(ctx context.Context, db *sql.DB, id string) error {
	issuesExists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect migration %s issues table: %w", id, err)
	}
	if !issuesExists {
		return recordAppliedMigration(ctx, db, id)
	}

	columns, err := tableColumns(db, "issues")
	if err != nil {
		return fmt.Errorf("inspect migration %s issues columns: %w", id, err)
	}
	targetColumns := []string{"lifecycle_state", "closed_outcome", "review_state", "archived_at"}
	existingTargetColumns := 0
	for _, column := range targetColumns {
		if _, ok := columns[column]; ok {
			existingTargetColumns++
		}
	}
	if existingTargetColumns > 0 && existingTargetColumns != len(targetColumns) {
		return issueStateModelV2CutoverError("refusing startup after partial issue state-model v2 schema cutover", "", "")
	}
	if existingTargetColumns == len(targetColumns) {
		if err := validateIssueStateModelV2Rows(ctx, db); err != nil {
			return fmt.Errorf("validate existing issue state-model v2 rows: %w", err)
		}
		if err := setIssueStateModelV2CompleteMarker(ctx, db, ""); err != nil {
			return fmt.Errorf("mark existing issue state-model v2 migration complete: %w", err)
		}
		return recordAppliedMigration(ctx, db, id)
	}

	backupPath, err := c.backupIssueDBForStateModelV2(ctx, db)
	if err != nil {
		return fmt.Errorf("backup issue DB before migration %s: %w", id, err)
	}
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeIssueStateModelV2CutoverMarker(ctx, db, issueStateModelV2CutoverMarker{
		State:      "in_progress",
		StartedAt:  startedAt,
		BackupPath: backupPath,
	}); err != nil {
		return fmt.Errorf("mark migration %s in progress: %w", id, err)
	}

	if err := c.runIssueStateModelV2CutoverTransaction(ctx, db, id, backupPath, startedAt); err != nil {
		marker := issueStateModelV2CutoverMarker{
			State:      "failed",
			StartedAt:  startedAt,
			BackupPath: backupPath,
			Error:      err.Error(),
		}
		if markerErr := writeIssueStateModelV2CutoverMarker(context.Background(), db, marker); markerErr != nil {
			return issueStateModelV2CutoverError(
				fmt.Sprintf("migration %s failed and failed to record rollback details: %v", id, markerErr),
				backupPath,
				err.Error(),
			)
		}
		return issueStateModelV2CutoverError(fmt.Sprintf("migration %s rolled back", id), backupPath, err.Error())
	}

	return nil
}

func applyIssueClosedRuntimeV2TriggersMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, issueClosedRuntimeProjectionTablesSQL); err != nil {
		return fmt.Errorf("ensure migration %s runtime projection tables: %w", id, err)
	}

	for _, triggerName := range []string{
		"issue_closed_runtime_guard_insert",
		"issue_closed_runtime_guard_update",
		"daemon_worktree_closed_issue_guard_insert",
		"daemon_worktree_closed_issue_guard_update",
		"daemon_session_closed_issue_guard_insert",
		"daemon_session_closed_issue_guard_update",
		"issue_dependency_closed_runtime_guard_insert",
		"issue_dependency_closed_runtime_guard_update",
		"issue_descendant_closed_ancestor_guard_update",
	} {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+triggerName); err != nil {
			return fmt.Errorf("drop trigger %s: %w", triggerName, err)
		}
	}

	if _, err := tx.ExecContext(ctx, issueClosedRuntimeV2TriggersSQL); err != nil {
		return fmt.Errorf("apply migration %s triggers: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (id, applied_at)
		VALUES (?, ?)
	`, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}

const issueClosedRuntimeProjectionTablesSQL = `
CREATE TABLE IF NOT EXISTS daemon_session_projections (
	project_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	state TEXT NOT NULL,
	started_at TEXT,
	updated_at TEXT NOT NULL,
	tmux_attached_count INTEGER NOT NULL DEFAULT 0,
	observed_state TEXT,
	activity TEXT,
	activity_source TEXT,
	PRIMARY KEY (project_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
	ON daemon_session_projections (project_id, issue_id);

CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue_updated
	ON daemon_session_projections (project_id, issue_id, updated_at DESC, session_id DESC);

CREATE TABLE IF NOT EXISTS daemon_worktree_projections (
	project_id TEXT NOT NULL,
	issue_id TEXT NOT NULL,
	path TEXT NOT NULL,
	branch TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	git_status_json TEXT,
	git_status_updated_at TEXT,
	PRIMARY KEY (project_id, issue_id)
);

CREATE INDEX IF NOT EXISTS idx_daemon_worktree_projections_project_path
	ON daemon_worktree_projections (project_id, path);
`

const issueClosedRuntimeV2TriggersSQL = `
CREATE TRIGGER issue_closed_runtime_guard_insert
BEFORE INSERT ON issues
WHEN NEW.lifecycle_state = 'closed'
BEGIN
	SELECT RAISE(ABORT, 'closed issue cannot have active runtime attachments')
	WHERE
		EXISTS (
			SELECT 1
			FROM daemon_worktree_projections w
			WHERE
				w.issue_id = NEW.id
				AND TRIM(COALESCE(w.path, '')) <> ''
		)
		OR EXISTS (
			SELECT 1
			FROM daemon_session_projections s
			WHERE
				s.issue_id = NEW.id
				AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
		)
		OR EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT d.issue_id
				FROM issue_dependencies d
				WHERE
					d.depends_on_id = NEW.id
					AND d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			JOIN issues child ON child.id = descendants.issue_id
			WHERE
				child.archived_at IS NULL
				AND (
					child.lifecycle_state <> 'closed'
					OR
					EXISTS (
						SELECT 1
						FROM daemon_worktree_projections w
						WHERE
							w.issue_id = child.id
							AND TRIM(COALESCE(w.path, '')) <> ''
					)
					OR EXISTS (
						SELECT 1
						FROM daemon_session_projections s
						WHERE
							s.issue_id = child.id
							AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
					)
				)
		);
END;

CREATE TRIGGER issue_closed_runtime_guard_update
BEFORE UPDATE OF lifecycle_state ON issues
WHEN NEW.lifecycle_state = 'closed'
BEGIN
	SELECT RAISE(ABORT, 'closed issue cannot have active runtime attachments')
	WHERE
		EXISTS (
			SELECT 1
			FROM daemon_worktree_projections w
			WHERE
				w.issue_id = NEW.id
				AND TRIM(COALESCE(w.path, '')) <> ''
		)
		OR EXISTS (
			SELECT 1
			FROM daemon_session_projections s
			WHERE
				s.issue_id = NEW.id
				AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
		)
		OR EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT d.issue_id
				FROM issue_dependencies d
				WHERE
					d.depends_on_id = NEW.id
					AND d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			JOIN issues child ON child.id = descendants.issue_id
			WHERE
				child.archived_at IS NULL
				AND (
					child.lifecycle_state <> 'closed'
					OR
					EXISTS (
						SELECT 1
						FROM daemon_worktree_projections w
						WHERE
							w.issue_id = child.id
							AND TRIM(COALESCE(w.path, '')) <> ''
					)
					OR EXISTS (
						SELECT 1
						FROM daemon_session_projections s
						WHERE
							s.issue_id = child.id
							AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
					)
				)
		);
END;

CREATE TRIGGER daemon_worktree_closed_issue_guard_insert
BEFORE INSERT ON daemon_worktree_projections
WHEN TRIM(COALESCE(NEW.path, '')) <> ''
BEGIN
	SELECT RAISE(ABORT, 'cannot attach worktree to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;

CREATE TRIGGER daemon_worktree_closed_issue_guard_update
BEFORE UPDATE OF issue_id, path ON daemon_worktree_projections
WHEN TRIM(COALESCE(NEW.path, '')) <> ''
BEGIN
	SELECT RAISE(ABORT, 'cannot attach worktree to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;

CREATE TRIGGER daemon_session_closed_issue_guard_insert
BEFORE INSERT ON daemon_session_projections
WHEN LOWER(TRIM(COALESCE(NEW.state, ''))) <> 'stopped'
BEGIN
	SELECT RAISE(ABORT, 'cannot attach active session to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;

CREATE TRIGGER daemon_session_closed_issue_guard_update
BEFORE UPDATE OF issue_id, state ON daemon_session_projections
WHEN LOWER(TRIM(COALESCE(NEW.state, ''))) <> 'stopped'
BEGIN
	SELECT RAISE(ABORT, 'cannot attach active session to closed issue or closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT NEW.issue_id
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;

CREATE TRIGGER issue_dependency_closed_runtime_guard_insert
BEFORE INSERT ON issue_dependencies
WHEN
	NEW.tombstoned_at IS NULL
	AND NEW.dependency_type IN ('parent-child', 'parent_child')
BEGIN
	SELECT RAISE(ABORT, 'cannot place unresolved descendant under closed issue')
	WHERE
		EXISTS (
			WITH RECURSIVE ancestors(issue_id) AS (
				SELECT NEW.depends_on_id
				UNION
				SELECT d.depends_on_id
				FROM ancestors
				JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM ancestors
			JOIN issues i ON i.id = ancestors.issue_id
			WHERE
				i.lifecycle_state = 'closed'
				AND i.archived_at IS NULL
		)
		AND EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT NEW.issue_id
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			LEFT JOIN issues child ON child.id = descendants.issue_id
			WHERE
				(
					child.id IS NOT NULL
					AND child.archived_at IS NULL
					AND child.lifecycle_state <> 'closed'
				)
				OR
				EXISTS (
					SELECT 1
					FROM daemon_worktree_projections w
					WHERE
						w.issue_id = descendants.issue_id
						AND TRIM(COALESCE(w.path, '')) <> ''
				)
				OR EXISTS (
					SELECT 1
					FROM daemon_session_projections s
					WHERE
						s.issue_id = descendants.issue_id
						AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
				)
		);
END;

CREATE TRIGGER issue_dependency_closed_runtime_guard_update
BEFORE UPDATE OF issue_id, depends_on_id, dependency_type, tombstoned_at ON issue_dependencies
WHEN
	NEW.tombstoned_at IS NULL
	AND NEW.dependency_type IN ('parent-child', 'parent_child')
BEGIN
	SELECT RAISE(ABORT, 'cannot place unresolved descendant under closed issue')
	WHERE
		EXISTS (
			WITH RECURSIVE ancestors(issue_id) AS (
				SELECT NEW.depends_on_id
				UNION
				SELECT d.depends_on_id
				FROM ancestors
				JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM ancestors
			JOIN issues i ON i.id = ancestors.issue_id
			WHERE
				i.lifecycle_state = 'closed'
				AND i.archived_at IS NULL
		)
		AND EXISTS (
			WITH RECURSIVE descendants(issue_id) AS (
				SELECT NEW.issue_id
				UNION
				SELECT d.issue_id
				FROM descendants
				JOIN issue_dependencies d ON d.depends_on_id = descendants.issue_id
				WHERE
					d.tombstoned_at IS NULL
					AND d.dependency_type IN ('parent-child', 'parent_child')
			)
			SELECT 1
			FROM descendants
			LEFT JOIN issues child ON child.id = descendants.issue_id
			WHERE
				(
					child.id IS NOT NULL
					AND child.archived_at IS NULL
					AND child.lifecycle_state <> 'closed'
				)
				OR
				EXISTS (
					SELECT 1
					FROM daemon_worktree_projections w
					WHERE
						w.issue_id = descendants.issue_id
						AND TRIM(COALESCE(w.path, '')) <> ''
				)
				OR EXISTS (
					SELECT 1
					FROM daemon_session_projections s
					WHERE
						s.issue_id = descendants.issue_id
						AND LOWER(TRIM(COALESCE(s.state, ''))) <> 'stopped'
				)
		);
END;

CREATE TRIGGER issue_descendant_closed_ancestor_guard_update
BEFORE UPDATE OF lifecycle_state, archived_at ON issues
WHEN NEW.lifecycle_state <> 'closed' AND NEW.archived_at IS NULL
BEGIN
	SELECT RAISE(ABORT, 'cannot move descendant out of closed under closed ancestor')
	WHERE EXISTS (
		WITH RECURSIVE ancestors(issue_id) AS (
			SELECT d.depends_on_id
			FROM issue_dependencies d
			WHERE
				d.issue_id = NEW.id
				AND d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
			UNION
			SELECT d.depends_on_id
			FROM ancestors
			JOIN issue_dependencies d ON d.issue_id = ancestors.issue_id
			WHERE
				d.tombstoned_at IS NULL
				AND d.dependency_type IN ('parent-child', 'parent_child')
		)
		SELECT 1
		FROM ancestors
		JOIN issues i ON i.id = ancestors.issue_id
		WHERE
			i.lifecycle_state = 'closed'
			AND i.archived_at IS NULL
	);
END;
`

func (c *Client) runIssueStateModelV2CutoverTransaction(ctx context.Context, db *sql.DB, id, backupPath, startedAt string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if err := c.issueStateModelV2FailurePoint("after_begin"); err != nil {
		return err
	}

	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "lifecycle_state", ddl: "TEXT"},
		{name: "closed_outcome", ddl: "TEXT"},
		{name: "review_state", ddl: "TEXT"},
		{name: "archived_at", ddl: "TEXT"},
	} {
		exists, err := txColumnExists(ctx, tx, "issues", column.name)
		if err != nil {
			return fmt.Errorf("inspect migration %s column %s: %w", id, column.name, err)
		}
		if exists {
			return issueStateModelV2CutoverError("refusing startup after partial issue state-model v2 schema cutover", backupPath, "")
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE issues ADD COLUMN %s %s`, column.name, column.ddl)); err != nil {
			return fmt.Errorf("apply migration %s: add %s: %w", id, column.name, err)
		}
	}

	if err := c.issueStateModelV2FailurePoint("after_columns"); err != nil {
		return err
	}

	if err := normalizeResidualBlockedStatusesForStateModelV2(ctx, tx); err != nil {
		return fmt.Errorf("apply migration %s: normalize residual blocked statuses: %w", id, err)
	}
	if err := mapIssueStateModelV2Rows(ctx, tx); err != nil {
		return fmt.Errorf("apply migration %s: map v1 statuses: %w", id, err)
	}

	if err := c.issueStateModelV2FailurePoint("after_mapping"); err != nil {
		return err
	}
	if err := validateIssueStateModelV2Rows(ctx, tx); err != nil {
		return fmt.Errorf("validate migration %s: %w", id, err)
	}

	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeIssueStateModelV2CutoverMarkerTx(ctx, tx, issueStateModelV2CutoverMarker{
		State:       "complete",
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		BackupPath:  backupPath,
	}); err != nil {
		return fmt.Errorf("mark migration %s complete: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, issueStateModelVersionMetaKey, issueStateModelV2Version); err != nil {
		return fmt.Errorf("record migration %s state model version: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (id, applied_at)
		VALUES (?, ?)
	`, id, completedAt); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	if err := c.issueStateModelV2FailurePoint("before_commit"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}

func (c *Client) backupIssueDBForStateModelV2(ctx context.Context, db *sql.DB) (string, error) {
	return c.backupIssueDB(ctx, db, "state-model-v1")
}

func (c *Client) backupIssueDB(ctx context.Context, db *sql.DB, label string) (string, error) {
	if strings.TrimSpace(c.dbPath) == "" || c.dbPath == ":memory:" {
		return "", fmt.Errorf("cannot create SQLite backup for empty or in-memory issue DB path")
	}
	if err := c.issueStateModelV2FailurePoint("before_backup"); err != nil {
		return "", err
	}
	dbPath, err := filepath.Abs(c.dbPath)
	if err != nil {
		return "", fmt.Errorf("resolve DB path: %w", err)
	}
	backupPath := fmt.Sprintf("%s.%s.%s.bak", dbPath, label, time.Now().UTC().Format("20060102T150405.000000000Z"))
	for i := 1; ; i++ {
		if _, err := os.Stat(backupPath); err != nil {
			if os.IsNotExist(err) {
				break
			}
			return "", fmt.Errorf("inspect backup path: %w", err)
		}
		backupPath = fmt.Sprintf("%s.%s.%s.%d.bak", dbPath, label, time.Now().UTC().Format("20060102T150405.000000000Z"), i)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO `+quoteSQLiteStringLiteral(backupPath)); err != nil {
		return "", fmt.Errorf("vacuum into %s: %w", backupPath, err)
	}
	if err := c.issueStateModelV2FailurePoint("after_backup"); err != nil {
		return "", err
	}
	return backupPath, nil
}

type issueStateModelV2Reconciliation struct {
	id             string
	legacyStatus   string
	lifecycleState string
	closedOutcome  string
	reviewState    string
	archivedAt     sql.NullString
}

func (c *Client) reconcileIssueStateModelV2Drift(ctx context.Context, db *sql.DB) error {
	applied, err := isMigrationApplied(ctx, db, issueStateModelV2MigrationID)
	if err != nil {
		return fmt.Errorf("check issue state-model v2 migration before reconciliation: %w", err)
	}
	if !applied {
		return nil
	}
	updates, err := issueStateModelV2Reconciliations(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect issue state-model v2 compatibility mirror: %w", err)
	}
	if len(updates) == 0 {
		return validateIssueStateModelV2LegacyMirror(ctx, db)
	}

	backupPath, err := c.backupIssueDB(ctx, db, "state-model-v2-reconcile")
	if err != nil {
		return fmt.Errorf("backup issue DB before state-model v2 reconciliation: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin issue state-model v2 reconciliation: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	updates, err = issueStateModelV2Reconciliations(ctx, tx)
	if err != nil {
		return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, err.Error())
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET status = ?,
				lifecycle_state = ?,
				closed_outcome = ?,
				review_state = ?,
				archived_at = ?
			WHERE id = ?
		`, update.legacyStatus, update.lifecycleState, update.closedOutcome, update.reviewState, update.archivedAt, update.id); err != nil {
			return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, fmt.Sprintf("issue %s: %v", update.id, err))
		}
	}
	if err := c.issueStateModelV2FailurePoint("after_reconciliation"); err != nil {
		return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, err.Error())
	}
	if err := validateIssueStateModelV2Rows(ctx, tx); err != nil {
		return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, err.Error())
	}
	if err := validateIssueStateModelV2LegacyMirror(ctx, tx); err != nil {
		return issueStateModelV2CutoverError("issue state-model v2 reconciliation rolled back", backupPath, err.Error())
	}
	if err := tx.Commit(); err != nil {
		return issueStateModelV2CutoverError("commit issue state-model v2 reconciliation", backupPath, err.Error())
	}
	tx = nil
	return nil
}

func issueStateModelV2Reconciliations(ctx context.Context, db sqlIssueQueryer) ([]issueStateModelV2Reconciliation, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, status, priority, lifecycle_state, closed_outcome, review_state, archived_at, deleted_at
		FROM issues
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	updates := make([]issueStateModelV2Reconciliation, 0)
	for rows.Next() {
		var (
			id           string
			legacyStatus string
			priority     int
			lifecycleRaw sql.NullString
			outcomeRaw   sql.NullString
			reviewRaw    sql.NullString
			archivedAt   sql.NullString
			deletedAt    sql.NullString
		)
		if err := rows.Scan(&id, &legacyStatus, &priority, &lifecycleRaw, &outcomeRaw, &reviewRaw, &archivedAt, &deletedAt); err != nil {
			return nil, err
		}
		lifecycleState := lifecycleRaw.String
		closedOutcome := outcomeRaw.String
		reviewState := reviewRaw.String
		legacyState, legacyErr := domain.IssueStateFromLegacy(domain.LegacyIssueStateInput{
			Status:   domain.Status(legacyStatus),
			Priority: domain.Priority(priority),
			Archived: nonEmptyNullString(deletedAt),
		})
		var v2State domain.IssueState
		if strings.TrimSpace(lifecycleState) == "" {
			if legacyErr != nil {
				return nil, fmt.Errorf("issue %s legacy state: %w", id, legacyErr)
			}
			v2State = legacyState
		} else {
			var err error
			v2State, err = domain.NewIssueState(domain.IssueStateParts{
				Workflow:     domain.IssueWorkflow(lifecycleState),
				Review:       domain.IssueReviewState(reviewState),
				CloseOutcome: domain.IssueCloseOutcome(closedOutcome),
				Archive:      issueArchiveStateFromTimestamp(archivedAt),
				Deletion:     domain.IssueDeletionPresent,
			})
			if err != nil {
				return nil, fmt.Errorf("issue %s v2 state: %w", id, err)
			}
		}
		authoritativeState := v2State
		// Closing is monotonic across the cutover boundary. A legacy terminal
		// write therefore wins over a non-terminal v2 value; every other
		// disagreement is repaired from the v2 authority into the mirror.
		if legacyErr == nil && legacyState.IsClosed() && !v2State.IsClosed() {
			authoritativeState = legacyState
		}
		// Legacy writers only know deleted_at, while v2 writers update both
		// archive columns atomically. Any disagreement therefore means a legacy
		// archive or unarchive write happened after the last aligned state.
		// Preserve the v2 lifecycle authority selected above, but mirror the
		// legacy writer's latest archive intent into archived_at.
		authoritativeState, err = domain.NewIssueState(domain.IssueStateParts{
			Workflow:     authoritativeState.Workflow(),
			Review:       authoritativeState.Review(),
			CloseOutcome: authoritativeState.CloseOutcome(),
			Archive:      issueArchiveStateFromTimestamp(deletedAt),
			Deletion:     authoritativeState.Deletion(),
		})
		if err != nil {
			return nil, fmt.Errorf("issue %s reconciled state: %w", id, err)
		}
		expectedStatus := string(legacyStatusFromIssueState(authoritativeState))
		if legacyStatus == expectedStatus &&
			lifecycleState == string(authoritativeState.Workflow()) &&
			closedOutcome == string(authoritativeState.CloseOutcome()) &&
			reviewState == string(authoritativeState.Review()) &&
			archivedAt == deletedAt {
			continue
		}
		updates = append(updates, issueStateModelV2Reconciliation{
			id:             id,
			legacyStatus:   expectedStatus,
			lifecycleState: string(authoritativeState.Workflow()),
			closedOutcome:  string(authoritativeState.CloseOutcome()),
			reviewState:    string(authoritativeState.Review()),
			archivedAt:     deletedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}

func validateIssueStateModelV2LegacyMirror(ctx context.Context, db sqlIssueQueryer) error {
	updates, err := issueStateModelV2Reconciliations(ctx, db)
	if err != nil {
		return err
	}
	if len(updates) > 0 {
		return fmt.Errorf("legacy compatibility mirror drift: %d rows", len(updates))
	}
	return nil
}

func validateIssueStateModelV2Rows(ctx context.Context, db sqlIssueQueryer) error {
	checks := []struct {
		name  string
		query string
	}{
		{
			name: "missing lifecycle_state",
			query: `SELECT COUNT(*) FROM issues
				WHERE COALESCE(lifecycle_state, '') = ''`,
		},
		{
			name: "invalid lifecycle_state",
			query: `SELECT COUNT(*) FROM issues
				WHERE lifecycle_state NOT IN ('backlog', 'open', 'active', 'closed')`,
		},
		{
			name: "invalid closed_outcome for closed lifecycle",
			query: `SELECT COUNT(*) FROM issues
				WHERE lifecycle_state = 'closed' AND COALESCE(closed_outcome, '') NOT IN ('completed', 'cancelled')`,
		},
		{
			name: "invalid closed_outcome for non-closed lifecycle",
			query: `SELECT COUNT(*) FROM issues
				WHERE lifecycle_state <> 'closed' AND COALESCE(closed_outcome, '') <> 'none'`,
		},
		{
			name: "invalid closed_outcome",
			query: `SELECT COUNT(*) FROM issues
				WHERE closed_outcome NOT IN ('none', 'completed', 'cancelled')`,
		},
		{
			name: "missing review_state",
			query: `SELECT COUNT(*) FROM issues
				WHERE COALESCE(review_state, '') = ''`,
		},
		{
			name: "invalid review_state",
			query: `SELECT COUNT(*) FROM issues
				WHERE review_state NOT IN ('none', 'requested')`,
		},
		{
			name: "review requested for non-active lifecycle",
			query: `SELECT COUNT(*) FROM issues
				WHERE review_state = 'requested' AND lifecycle_state <> 'active'`,
		},
		{
			name: "archive timestamp drift",
			query: `SELECT COUNT(*) FROM issues
				WHERE COALESCE(archived_at, '') <> COALESCE(deleted_at, '')`,
		},
	}
	for _, check := range checks {
		var count int
		if err := db.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
		if count > 0 {
			return fmt.Errorf("%s: %d rows", check.name, count)
		}
	}
	return nil
}

func normalizeResidualBlockedStatusesForStateModelV2(ctx context.Context, tx *sql.Tx) error {
	script, err := fs.ReadFile(migrationFiles, "migrations/0012_blocked_status_to_open.sql")
	if err != nil {
		return fmt.Errorf("read blocked status migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, string(script)); err != nil {
		return fmt.Errorf("run blocked status migration: %w", err)
	}
	return nil
}

func mapIssueStateModelV2Rows(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, status, priority, deleted_at
		FROM issues
		ORDER BY id
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type update struct {
		id             string
		lifecycleState string
		closedOutcome  string
		reviewState    string
		archivedAt     sql.NullString
	}
	updates := []update{}
	for rows.Next() {
		var (
			id        string
			status    string
			priority  int
			deletedAt sql.NullString
		)
		if err := rows.Scan(&id, &status, &priority, &deletedAt); err != nil {
			return err
		}
		state, err := domain.IssueStateFromLegacy(domain.LegacyIssueStateInput{
			Status:   domain.Status(status),
			Priority: domain.Priority(priority),
			Archived: deletedAt.Valid && strings.TrimSpace(deletedAt.String) != "",
		})
		if err != nil {
			return fmt.Errorf("issue %s: %w", id, err)
		}
		updates = append(updates, update{
			id:             id,
			lifecycleState: string(state.Workflow()),
			closedOutcome:  string(state.CloseOutcome()),
			reviewState:    string(state.Review()),
			archivedAt:     deletedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issues
			SET lifecycle_state = ?,
				closed_outcome = ?,
				review_state = ?,
				archived_at = ?
			WHERE id = ?
		`, update.lifecycleState, update.closedOutcome, update.reviewState, update.archivedAt, update.id); err != nil {
			return fmt.Errorf("issue %s: %w", update.id, err)
		}
	}
	return nil
}

func readIssueStateModelV2CutoverMarker(ctx context.Context, db *sql.DB) (issueStateModelV2CutoverMarker, bool, error) {
	metaExists, err := tableExists(db, "meta")
	if err != nil {
		return issueStateModelV2CutoverMarker{}, false, err
	}
	if !metaExists {
		return issueStateModelV2CutoverMarker{}, false, nil
	}
	var raw string
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, issueStateModelV2CutoverMarkerKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return issueStateModelV2CutoverMarker{}, false, nil
	}
	if err != nil {
		return issueStateModelV2CutoverMarker{}, false, err
	}
	var marker issueStateModelV2CutoverMarker
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return issueStateModelV2CutoverMarker{}, true, fmt.Errorf("decode marker: %w", err)
	}
	return marker, true, nil
}

func writeIssueStateModelV2CutoverMarker(ctx context.Context, db *sql.DB, marker issueStateModelV2CutoverMarker) error {
	payload, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, issueStateModelV2CutoverMarkerKey, string(payload))
	return err
}

func writeIssueStateModelV2CutoverMarkerTx(ctx context.Context, tx *sql.Tx, marker issueStateModelV2CutoverMarker) error {
	payload, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, issueStateModelV2CutoverMarkerKey, string(payload))
	return err
}

func setIssueStateModelV2CompleteMarker(ctx context.Context, db *sql.DB, backupPath string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeIssueStateModelV2CutoverMarker(ctx, db, issueStateModelV2CutoverMarker{
		State:       "complete",
		StartedAt:   now,
		CompletedAt: now,
		BackupPath:  backupPath,
	}); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, issueStateModelVersionMetaKey, issueStateModelV2Version)
	return err
}

func issueStateModelV2CutoverError(message, backupPath, cause string) error {
	detail := strings.TrimSpace(message)
	if strings.TrimSpace(backupPath) != "" {
		detail += fmt.Sprintf("; backup=%s", backupPath)
	}
	if strings.TrimSpace(cause) != "" {
		detail += fmt.Sprintf("; restore the backup before retrying; cause=%s", cause)
	} else if strings.TrimSpace(backupPath) != "" {
		detail += "; restore the backup before retrying"
	}
	return fmt.Errorf("%s", detail)
}

func (c *Client) issueStateModelV2FailurePoint(stage string) error {
	if c.stateModelV2MigrationFailureHook == nil {
		return nil
	}
	if err := c.stateModelV2MigrationFailureHook(stage); err != nil {
		return fmt.Errorf("injected issue state-model v2 migration failure at %s: %w", stage, err)
	}
	return nil
}

func quoteSQLiteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func applyDecisionSearchFTSMigration(ctx context.Context, db *sql.DB, id string) error {
	var c Client
	if err := c.ensureDecisionSchema(db); err != nil {
		return fmt.Errorf("repair decision schema before migration %s: %w", id, err)
	}
	sqlText, err := loadMigrationSQL("migrations/0026_decision_search_fts.sql")
	if err != nil {
		return fmt.Errorf("load migration %s: %w", id, err)
	}
	return c.applyMigration(ctx, db, id, sqlText)
}

func applyAgentLearningPrivacyMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	hasEvidencePrivate, err := txColumnExists(ctx, tx, "agent_learnings", "evidence_private")
	if err != nil {
		return fmt.Errorf("inspect migration %s: %w", id, err)
	}
	if !hasEvidencePrivate {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_learnings ADD COLUMN evidence_private INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("apply migration %s: add evidence_private: %w", id, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_agent_learnings_active_privacy
			ON agent_learnings(project_id, status, evidence_private, updated_at DESC, local_id)
			WHERE deleted_at IS NULL
	`); err != nil {
		return fmt.Errorf("apply migration %s: create privacy index: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (id, applied_at)
		VALUES (?, ?)
	`, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}

func applyIssueOwnershipMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "owner_id", ddl: "TEXT"},
		{name: "owner_kind", ddl: "TEXT"},
		{name: "owner_claimed_at", ddl: "TEXT"},
		{name: "owner_expires_at", ddl: "TEXT"},
	} {
		exists, err := txColumnExists(ctx, tx, "issues", column.name)
		if err != nil {
			return fmt.Errorf("inspect migration %s column %s: %w", id, column.name, err)
		}
		if exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE issues ADD COLUMN %s %s`, column.name, column.ddl)); err != nil {
			return fmt.Errorf("apply migration %s: add %s: %w", id, column.name, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_issues_owner_active
			ON issues (owner_id, owner_expires_at)
			WHERE deleted_at IS NULL AND owner_id IS NOT NULL
	`); err != nil {
		return fmt.Errorf("apply migration %s: create ownership index: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (id, applied_at)
		VALUES (?, ?)
	`, id, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %s: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", id, err)
	}
	tx = nil
	return nil
}

func repairAgentLearningBaseSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	tableExists, err := txTableExists(ctx, tx, "agent_learnings")
	if err != nil {
		return fmt.Errorf("inspect table: %w", err)
	}
	if !tableExists {
		if err := tx.Commit(); err != nil {
			return err
		}
		tx = nil
		return nil
	}

	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "evidence_private", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "promotion_target", ddl: "TEXT"},
		{name: "promotion_target_id", ddl: "TEXT"},
		{name: "promotion_note", ddl: "TEXT"},
		{name: "promoted_at", ddl: "TEXT"},
		{name: "review_note", ddl: "TEXT"},
		{name: "reviewed_at", ddl: "TEXT"},
		{name: "expires_at", ddl: "TEXT"},
		{name: "stale_at", ddl: "TEXT"},
		{name: "last_recalled_at", ddl: "TEXT"},
		{name: "recall_count", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "superseded_at", ddl: "TEXT"},
		{name: "target_retired_at", ddl: "TEXT"},
		{name: "target_state", ddl: "TEXT"},
		{name: "target_hash", ddl: "TEXT"},
		{name: "target_metadata_json", ddl: "TEXT NOT NULL DEFAULT '{}'"},
		{name: "target_drifted_at", ddl: "TEXT"},
	} {
		exists, err := txColumnExists(ctx, tx, "agent_learnings", column.name)
		if err != nil {
			return fmt.Errorf("inspect column %s: %w", column.name, err)
		}
		if exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE agent_learnings ADD COLUMN %s %s`, column.name, column.ddl)); err != nil {
			return fmt.Errorf("add column %s: %w", column.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func txTableExists(ctx context.Context, tx *sql.Tx, tableName string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sqlite_master
			WHERE type = 'table' AND name = ?
		)
	`, tableName).Scan(&exists)
	return exists, err
}

func txColumnExists(ctx context.Context, tx *sql.Tx, tableName, columnName string) (bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info('%s')", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

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
			return false, err
		}
		if strings.EqualFold(name, columnName) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
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
	if err := migrateLegacySpecRequirementsSchema(db); err != nil {
		return fmt.Errorf("normalize legacy spec schema: %w", err)
	}

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

type specRequirementsLegacyProfile struct {
	hasBodyMD      bool
	hasKind        bool
	hasPriority    bool
	textPrimaryKey bool
}

func migrateLegacySpecRequirementsSchema(db *sql.DB) error {
	cols, err := tableColumnDetails(db, "spec_requirements")
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}

	profile := specRequirementsLegacyProfile{}
	for _, col := range cols {
		switch col.name {
		case "body_md":
			profile.hasBodyMD = true
		case "kind":
			profile.hasKind = true
		case "priority":
			profile.hasPriority = true
		case "id":
			typeName := strings.ToUpper(strings.TrimSpace(col.typ))
			profile.textPrimaryKey = col.primaryKey > 0 && !strings.Contains(typeName, "INT")
		}
	}
	hasColumn := func(name string) bool {
		for _, col := range cols {
			if col.name == name {
				return true
			}
		}
		return false
	}

	if !profile.hasBodyMD && !profile.hasKind && !profile.hasPriority && !profile.textPrimaryKey {
		return nil
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		ALTER TABLE spec_requirements RENAME TO spec_requirements_legacy
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		CREATE TABLE spec_requirements (
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
	`); err != nil {
		return err
	}

	localIDExpr := `CAST(id AS TEXT)`
	localIDExprLegacy := `CAST(legacy.id AS TEXT)`
	if hasColumn("local_id") {
		localIDExpr = `COALESCE(NULLIF(TRIM(local_id), ''), CAST(id AS TEXT))`
		localIDExprLegacy = `COALESCE(NULLIF(TRIM(legacy.local_id), ''), CAST(legacy.id AS TEXT))`
	}
	externalCodeExpr := `NULL`
	if hasColumn("external_code") {
		externalCodeExpr = `NULLIF(TRIM(external_code), '')`
	}
	descriptionExpr := `''`
	switch {
	case hasColumn("description") && profile.hasBodyMD:
		descriptionExpr = `COALESCE(NULLIF(description, ''), body_md, '')`
	case hasColumn("description"):
		descriptionExpr = `COALESCE(description, '')`
	case profile.hasBodyMD:
		descriptionExpr = `COALESCE(body_md, '')`
	}
	issueIDExpr := `NULL`
	if hasColumn("issue_id") {
		issueIDExpr = `NULLIF(TRIM(issue_id), '')`
	}
	statusExpr := `'open'`
	if hasColumn("status") {
		statusExpr = `CASE WHEN status IN ('open', 'accepted', 'superseded') THEN status ELSE 'open' END`
	}
	createdAtExpr := `'1970-01-01T00:00:00Z'`
	if hasColumn("created_at") {
		createdAtExpr = `created_at`
	}
	updatedAtExpr := `'1970-01-01T00:00:00Z'`
	if hasColumn("updated_at") {
		updatedAtExpr = `updated_at`
	}
	deletedAtExpr := `NULL`
	if hasColumn("deleted_at") {
		deletedAtExpr = `deleted_at`
	}

	copyRequirementsSQL := fmt.Sprintf(`
		INSERT INTO spec_requirements (
			local_id,
			external_code,
			title,
			description,
			issue_id,
			status,
			created_at,
			updated_at,
			deleted_at
		)
		SELECT
			%s,
			%s,
			title,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s
		FROM spec_requirements_legacy
	`, localIDExpr, externalCodeExpr, descriptionExpr, issueIDExpr, statusExpr, createdAtExpr, updatedAtExpr, deletedAtExpr)
	if _, err := tx.Exec(copyRequirementsSQL); err != nil {
		return err
	}

	if tableExistsInTx(tx, "spec_links") {
		if _, err := tx.Exec(`ALTER TABLE spec_links RENAME TO spec_links_legacy`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			CREATE TABLE spec_links (
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
		`); err != nil {
			return err
		}

		if _, err := tx.Exec(`
			CREATE TEMP TABLE spec_requirement_id_map (
				old_key TEXT PRIMARY KEY,
				new_id INTEGER NOT NULL
			)
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO spec_requirement_id_map(old_key, new_id)
			SELECT CAST(legacy.id AS TEXT), current.id
			FROM spec_requirements_legacy legacy
			JOIN spec_requirements current
			  ON current.local_id = ` + localIDExprLegacy + `
		`); err != nil {
			return err
		}
		if hasColumn("local_id") {
			if _, err := tx.Exec(`
				INSERT OR IGNORE INTO spec_requirement_id_map(old_key, new_id)
				SELECT legacy.local_id, current.id
				FROM spec_requirements_legacy legacy
				JOIN spec_requirements current
				  ON current.local_id = ` + localIDExprLegacy + `
				WHERE legacy.local_id IS NOT NULL AND TRIM(legacy.local_id) != ''
			`); err != nil {
				return err
			}
		}
		if hasColumn("external_code") {
			if _, err := tx.Exec(`
			INSERT OR IGNORE INTO spec_requirement_id_map(old_key, new_id)
			SELECT legacy.external_code, current.id
			FROM spec_requirements_legacy legacy
			JOIN spec_requirements current
			  ON current.local_id = ` + localIDExprLegacy + `
			WHERE legacy.external_code IS NOT NULL AND TRIM(legacy.external_code) != ''
		`); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(`
			INSERT INTO spec_links (
				issue_id,
				requirement_id,
				role,
				note,
				implementations_json,
				fulfillment_status,
				fulfilled_at,
				created_at,
				updated_at,
				deleted_at
			)
			SELECT
				l.issue_id,
				m.new_id,
				l.role,
				l.note,
				l.implementations_json,
				l.fulfillment_status,
				l.fulfilled_at,
				l.created_at,
				l.updated_at,
				l.deleted_at
			FROM spec_links_legacy l
			JOIN spec_requirement_id_map m
			  ON m.old_key = CAST(l.requirement_id AS TEXT)
		`); err != nil {
			return err
		}

		if _, err := tx.Exec(`DROP TABLE spec_links_legacy`); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`DROP TABLE spec_requirements_legacy`); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func tableExistsInTx(tx *sql.Tx, tableName string) bool {
	var exists int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = ?
	`, tableName).Scan(&exists); err != nil {
		return false
	}
	return exists > 0
}

func (c *Client) ensureDecisionSchema(db *sql.DB) error {
	decisionsDDL := `
		CREATE TABLE IF NOT EXISTS decisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			local_id TEXT NOT NULL,
			title TEXT NOT NULL,
			rationale TEXT,
			context TEXT,
			consequences TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		)
	`
	if _, err := db.Exec(decisionsDDL); err != nil {
		return fmt.Errorf("ensure decisions table: %w", err)
	}
	if err := ensureTableColumns(db, "decisions", []sqliteColumnSpec{
		{name: "local_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "title", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "rationale", ddl: "TEXT"},
		{name: "context", ddl: "TEXT"},
		{name: "consequences", ddl: "TEXT"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return fmt.Errorf("ensure decisions columns: %w", err)
	}

	linksDDL := `
		CREATE TABLE IF NOT EXISTS decision_links (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			decision_id INTEGER NOT NULL,
			target_kind TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relation TEXT NOT NULL,
			note TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			FOREIGN KEY (decision_id) REFERENCES decisions(id) ON DELETE CASCADE
		)
	`
	if _, err := db.Exec(linksDDL); err != nil {
		return fmt.Errorf("ensure decision_links table: %w", err)
	}
	if err := ensureTableColumns(db, "decision_links", []sqliteColumnSpec{
		{name: "decision_id", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "target_kind", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "target_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "relation", ddl: "TEXT NOT NULL DEFAULT 'relates'"},
		{name: "note", ddl: "TEXT"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "updated_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		{name: "deleted_at", ddl: "TEXT"},
	}); err != nil {
		return fmt.Errorf("ensure decision_links columns: %w", err)
	}

	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_decisions_active_local_id ON decisions(local_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_decisions_updated ON decisions(updated_at DESC, local_id) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_decision_links_active_unique ON decision_links(decision_id, target_kind, target_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_decision_links_target ON decision_links(target_kind, target_id, updated_at DESC) WHERE deleted_at IS NULL`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure decision schema index: %w", err)
		}
	}
	return nil
}

func (c *Client) ensureDecisionAuditSchema(db *sql.DB) error {
	auditDDL := `
		CREATE TABLE IF NOT EXISTS decision_audit_log (
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
		return fmt.Errorf("ensure decision_audit_log table: %w", err)
	}
	if err := ensureTableColumns(db, "decision_audit_log", []sqliteColumnSpec{
		{name: "entity_type", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "entity_id", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "operation", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "actor_source", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "before_json", ddl: "TEXT NOT NULL DEFAULT 'null'"},
		{name: "after_json", ddl: "TEXT NOT NULL DEFAULT 'null'"},
		{name: "created_at", ddl: "TEXT NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
	}); err != nil {
		return fmt.Errorf("ensure decision_audit_log columns: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_decision_audit_entity_created_at ON decision_audit_log(entity_type, entity_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_decision_audit_created_at ON decision_audit_log(created_at, id)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure decision audit index: %w", err)
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
	details, err := tableColumnDetails(db, tableName)
	if err != nil {
		return nil, err
	}
	columns := make(map[string]struct{})
	for _, detail := range details {
		columns[detail.name] = struct{}{}
	}
	return columns, nil
}

type tableColumnDetail struct {
	name       string
	typ        string
	primaryKey int
}

func tableColumnDetails(db *sql.DB, tableName string) ([]tableColumnDetail, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info('%s')", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	details := make([]tableColumnDetail, 0, 16)
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
		details = append(details, tableColumnDetail{
			name:       name,
			typ:        columnType,
			primaryKey: primaryKey,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return details, nil
}
