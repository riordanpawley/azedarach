package userstore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/dbpathguard"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/sqlitemigration"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

//go:embed migrations/*
var migrationFiles embed.FS

var migrationArtifacts = []sqlitemigration.Artifact{
	{ID: "user_0001_cross_project_projection", Path: "migrations/user_0001_cross_project_projection.manifest.sql", Checksum: "a313332cc21b8c02be4125bfddc9a05299d41b4dc76414abe51163ae88f97d41"},
	{ID: "user_0002_normalized_projection", Path: "migrations/user_0002_normalized_projection.manifest.sql", Checksum: "15a9ef67dd84425a0d29ab62f7107755134799567b6671b477782467496c5434"},
	{ID: "user_0003_canonical_issue_state_repair", Path: "migrations/user_0003_canonical_issue_state_repair.manifest.sql", Checksum: "981bf427d53fe031296d27659293494c48c63f8865333a08340a0fe542c4883f"},
	{ID: "user_0004_canonical_archive_state_repair", Path: "migrations/user_0004_canonical_archive_state_repair.manifest.sql", Checksum: "302f16948cea6ddef8ea11e3a6fac09f9234817e9d3e29d9b9b516f707e83941"},
	{ID: "user_0005_project_delta_consumer", Path: "migrations/user_0005_project_delta_consumer.manifest.sql", Checksum: "3462d998e1abfb9ef02f22b964934bbcd7b6e2c9e25e233a2d9d89002f6cc863"},
}

var migrationRegistrations = []sqlitemigration.Artifact{
	{ID: "user_0001_cross_project_projection", Path: "migrations/user_0001_cross_project_projection.manifest.sql"},
	{ID: "user_0002_normalized_projection", Path: "migrations/user_0002_normalized_projection.manifest.sql"},
	{ID: "user_0003_canonical_issue_state_repair", Path: "migrations/user_0003_canonical_issue_state_repair.manifest.sql"},
	{ID: "user_0004_canonical_archive_state_repair", Path: "migrations/user_0004_canonical_archive_state_repair.manifest.sql"},
	{ID: "user_0005_project_delta_consumer", Path: "migrations/user_0005_project_delta_consumer.manifest.sql"},
}

const projectionVersion = 2
const DefaultProjectionMaxAge = 2 * time.Minute
const migrationArtifactAuthority sqlitemigration.Authority = "user.projection"

type Store struct {
	db                    *sql.DB
	dbPath                string
	maxProjectionAge      time.Duration
	now                   func() time.Time
	migrationBeforeCommit func() error
	seedBeforeCommit      func() error
	snapshotAfterProjects func()
}
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type migrationDB interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
type Option func(*Store)

func WithMaxProjectionAge(age time.Duration) Option {
	return func(s *Store) { s.maxProjectionAge = age }
}
func withClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }
func withMigrationBeforeCommit(hook func() error) Option {
	return func(s *Store) { s.migrationBeforeCommit = hook }
}
func withSeedBeforeCommit(hook func() error) Option {
	return func(s *Store) { s.seedBeforeCommit = hook }
}
func withSnapshotAfterProjects(hook func()) Option {
	return func(s *Store) { s.snapshotAfterProjects = hook }
}

func DefaultPath() string {
	path, _ := config.UserDBPath()
	return path
}

func Open(path string, options ...Option) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("user database path is empty")
	}
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("canonicalize user database path: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(filepath.Dir(path)); resolveErr == nil {
		path = filepath.Join(resolved, filepath.Base(path))
	}
	if err := dbpathguard.Check(path); err != nil {
		return nil, fmt.Errorf("refuse user database: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create user database directory: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_txlock=immediate", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, dbPath: path, maxProjectionAge: DefaultProjectionMaxAge, now: func() time.Time { return time.Now().UTC() }}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	if err := sqliteutil.WithWriteLock(path, func() error { return s.migrate(context.Background()) }); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if err := sqlitemigration.Validate(migrationFiles, migrationArtifacts); err != nil {
		return fmt.Errorf("validate user migration registry: %w", err)
	}
	if err := sqlitemigration.ValidateRegistrations(migrationArtifacts, migrationRegistrations); err != nil {
		return fmt.Errorf("validate user migration artifact coverage: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return fmt.Errorf("enable user database WAL: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin user database migration: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (id TEXT PRIMARY KEY, applied_at TEXT NOT NULL, artifact_checksum TEXT);
CREATE TABLE IF NOT EXISTS projects (
 project_id TEXT PRIMARY KEY, name TEXT NOT NULL, path TEXT NOT NULL, db_path TEXT NOT NULL,
 schema_version INTEGER NOT NULL DEFAULT 0, projection_version INTEGER NOT NULL,
 schema_fingerprint TEXT NOT NULL DEFAULT '',
 checkpoint INTEGER NOT NULL DEFAULT 0, refresh_generation INTEGER NOT NULL DEFAULT 0,
 delta_cursor INTEGER NOT NULL DEFAULT 0, delta_hash TEXT NOT NULL DEFAULT '', delta_source_vector_json BLOB NOT NULL DEFAULT '[]',
 delta_projector_id TEXT NOT NULL DEFAULT '', delta_projector_schema INTEGER NOT NULL DEFAULT 0, delta_projector_build TEXT NOT NULL DEFAULT '', delta_projector_checksum TEXT NOT NULL DEFAULT '',
 freshness TEXT NOT NULL, refreshed_at TEXT, last_attempt_at TEXT, last_error TEXT NOT NULL DEFAULT '', registered INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS project_issue_projection (
 project_id TEXT NOT NULL, issue_id TEXT NOT NULL, title TEXT NOT NULL,
 description TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '', design TEXT NOT NULL DEFAULT '', acceptance TEXT NOT NULL DEFAULT '',
 assignee TEXT NOT NULL DEFAULT '', labels_json BLOB NOT NULL DEFAULT '[]', estimate INTEGER, implementations_json BLOB NOT NULL DEFAULT '[]',
 status TEXT NOT NULL, lifecycle TEXT NOT NULL, review_state TEXT NOT NULL DEFAULT 'none', display_phase TEXT NOT NULL,
 closed_outcome TEXT NOT NULL, archive_state TEXT NOT NULL DEFAULT 'live', deletion_state TEXT NOT NULL DEFAULT 'present',
 review_ready INTEGER NOT NULL DEFAULT 0, waiting_human INTEGER NOT NULL DEFAULT 0, waiting_human_source TEXT NOT NULL DEFAULT '', waiting_human_reason TEXT NOT NULL DEFAULT '',
 waiting_ai INTEGER NOT NULL DEFAULT 0, human_attention_rank INTEGER NOT NULL DEFAULT 0,
 priority INTEGER NOT NULL, issue_type TEXT NOT NULL, parent_issue_id TEXT NOT NULL DEFAULT '',
 has_tmux_session INTEGER NOT NULL DEFAULT 0, origin TEXT NOT NULL DEFAULT '',
 pr_number INTEGER NOT NULL DEFAULT 0, pr_remote_key TEXT NOT NULL DEFAULT '', pr_display_key TEXT NOT NULL DEFAULT '', pr_url TEXT NOT NULL DEFAULT '',
 pr_state TEXT NOT NULL DEFAULT '', pr_draft INTEGER NOT NULL DEFAULT 0, pr_checks_status TEXT NOT NULL DEFAULT '',
 ownership_owner_id TEXT NOT NULL DEFAULT '', ownership_owner_kind TEXT NOT NULL DEFAULT '', ownership_claimed_at TEXT, ownership_expires_at TEXT,
 coordination_leases_json BLOB NOT NULL DEFAULT '[]', operation_blockers_json BLOB NOT NULL DEFAULT '[]', fact_reasons_json BLOB NOT NULL DEFAULT '[]',
 git_diff_total INTEGER NOT NULL DEFAULT 0, session_rank INTEGER NOT NULL DEFAULT 0, runtime_updated_at TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(project_id, issue_id)
);
CREATE INDEX IF NOT EXISTS idx_project_issue_projection_search ON project_issue_projection(title, project_id, issue_id);
CREATE INDEX IF NOT EXISTS idx_project_issue_projection_review ON project_issue_projection(review_ready, project_id, updated_at DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS project_issue_search_projection USING fts5(project_id UNINDEXED,issue_id UNINDEXED,content,tokenize='unicode61');
CREATE TABLE IF NOT EXISTS project_session_projection (
 project_id TEXT NOT NULL, session_id TEXT NOT NULL, issue_id TEXT NOT NULL,
 role TEXT NOT NULL DEFAULT '', scope_kind TEXT NOT NULL DEFAULT '', scope_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL, activity TEXT NOT NULL, activity_source TEXT NOT NULL DEFAULT '',
 total_count INTEGER NOT NULL DEFAULT 0, active_count INTEGER NOT NULL DEFAULT 0, paused_count INTEGER NOT NULL DEFAULT 0,
 tmux_attached INTEGER NOT NULL DEFAULT 0, tmux_attached_count INTEGER NOT NULL DEFAULT 0, started_at TEXT,
 worktree TEXT NOT NULL, devserver_port INTEGER NOT NULL DEFAULT 0, devserver_command TEXT NOT NULL DEFAULT '', devserver_running INTEGER NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id, session_id)
);
CREATE TABLE IF NOT EXISTS project_worktree_projection (
 project_id TEXT NOT NULL, issue_id TEXT NOT NULL, path TEXT NOT NULL, branch TEXT NOT NULL DEFAULT '',
 ahead_count INTEGER NOT NULL DEFAULT 0, behind_count INTEGER NOT NULL DEFAULT 0,
 additions INTEGER NOT NULL DEFAULT 0, deletions INTEGER NOT NULL DEFAULT 0,
 has_uncommitted_changes INTEGER NOT NULL DEFAULT 0, has_conflicts INTEGER NOT NULL DEFAULT 0,
 conflict_files_json BLOB NOT NULL DEFAULT '[]',
 updated_at TEXT NOT NULL, PRIMARY KEY(project_id,issue_id)
);
CREATE TABLE IF NOT EXISTS project_issue_dependency_projection (
 project_id TEXT NOT NULL, issue_id TEXT NOT NULL, depends_on_issue_id TEXT NOT NULL,
 dependency_type TEXT NOT NULL, PRIMARY KEY(project_id,issue_id,depends_on_issue_id,dependency_type)
);
CREATE TABLE IF NOT EXISTS user_views (
 view_id TEXT PRIMARY KEY, title TEXT NOT NULL, definition_json BLOB NOT NULL,
 project_scope TEXT NOT NULL DEFAULT 'all_projects', project_ids_json BLOB NOT NULL DEFAULT '[]',
 built_in INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS user_view_selections (
 consumer TEXT PRIMARY KEY, view_id TEXT NOT NULL, updated_at TEXT NOT NULL,
 FOREIGN KEY(view_id) REFERENCES user_views(view_id)
);`)
	if err != nil {
		return fmt.Errorf("migrate user cross-project projection: %w", err)
	}
	if err := sqlitemigration.EnsureLedgerChecksumsInTransaction(ctx, tx, migrationArtifactAuthority, migrationArtifacts); err != nil {
		return err
	}
	if err := recordAppliedUserMigration(ctx, tx, "user_0001_cross_project_projection"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, tx, "projects", "refresh_generation", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, tx, "projects", "last_attempt_at", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, tx, "projects", "schema_fingerprint", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	for _, column := range []struct{ name, declaration string }{
		{"delta_cursor", "INTEGER NOT NULL DEFAULT 0"},
		{"delta_hash", "TEXT NOT NULL DEFAULT ''"},
		{"delta_source_vector_json", "BLOB NOT NULL DEFAULT '[]'"},
		{"delta_projector_id", "TEXT NOT NULL DEFAULT ''"},
		{"delta_projector_schema", "INTEGER NOT NULL DEFAULT 0"},
		{"delta_projector_build", "TEXT NOT NULL DEFAULT ''"},
		{"delta_projector_checksum", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, tx, "projects", column.name, column.declaration); err != nil {
			return fmt.Errorf("migrate project delta consumer column %s: %w", column.name, err)
		}
	}
	if err := validateProjectDeltaConsumerSchema(ctx, tx); err != nil {
		return err
	}
	if err := s.migrateLegacyTaskJSON(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET freshness='stale',last_error=''
		WHERE NOT EXISTS(SELECT 1 FROM schema_migrations WHERE id='user_0003_canonical_issue_state_repair');
		UPDATE projects SET freshness='stale',last_error=''
		WHERE NOT EXISTS(SELECT 1 FROM schema_migrations WHERE id='user_0004_canonical_archive_state_repair')`); err != nil {
		return fmt.Errorf("schedule project refresh after canonical issue state repair: %w", err)
	}
	for _, id := range []string{"user_0003_canonical_issue_state_repair", "user_0004_canonical_archive_state_repair"} {
		if err := recordAppliedUserMigration(ctx, tx, id); err != nil {
			return err
		}
	}
	if err := recordAppliedUserMigration(ctx, tx, "user_0005_project_delta_consumer"); err != nil {
		return err
	}
	if s.migrationBeforeCommit != nil {
		if err := s.migrationBeforeCommit(); err != nil {
			return fmt.Errorf("before user database migration commit: %w", err)
		}
	}
	if err := sqlitemigration.EnsureLedgerChecksumsInTransaction(ctx, tx, migrationArtifactAuthority, migrationArtifacts); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user database migrations: %w", err)
	}
	return s.seedViews(ctx)
}

func recordAppliedUserMigration(ctx context.Context, db sqlitemigration.LedgerWriter, id string) error {
	return sqlitemigration.RecordAppliedIfMissing(ctx, db, migrationArtifacts, id, time.Now().UTC().Format(time.RFC3339Nano))
}

func (s *Store) ensureColumn(ctx context.Context, db migrationDB, table, column, declaration string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+declaration)
	return err
}

func validateProjectDeltaConsumerSchema(ctx context.Context, db queryer) error {
	type columnContract struct {
		typeName     string
		notNull      int
		defaultValue string
	}
	want := map[string]columnContract{
		"delta_cursor":             {"INTEGER", 1, "0"},
		"delta_hash":               {"TEXT", 1, "''"},
		"delta_source_vector_json": {"BLOB", 1, "'[]'"},
		"delta_projector_id":       {"TEXT", 1, "''"},
		"delta_projector_schema":   {"INTEGER", 1, "0"},
		"delta_projector_build":    {"TEXT", 1, "''"},
		"delta_projector_checksum": {"TEXT", 1, "''"},
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(projects)`)
	if err != nil {
		return fmt.Errorf("inspect project delta consumer schema: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typeName string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan project delta consumer schema: %w", err)
		}
		contract, ok := want[name]
		if !ok {
			continue
		}
		seen[name] = true
		if strings.ToUpper(typeName) != contract.typeName || notNull != contract.notNull || !defaultValue.Valid || defaultValue.String != contract.defaultValue {
			return fmt.Errorf("project delta consumer column %s drift: type=%s not_null=%d default=%q", name, typeName, notNull, defaultValue.String)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate project delta consumer schema: %w", err)
	}
	for name := range want {
		if !seen[name] {
			return fmt.Errorf("project delta consumer column %s is missing", name)
		}
	}
	return nil
}

func (s *Store) migrateLegacyTaskJSON(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(project_issue_projection)`)
	if err != nil {
		return err
	}
	hasTaskJSON := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "task_json" {
			hasTaskJSON = true
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if !hasTaskJSON {
		return recordAppliedUserMigration(ctx, tx, "user_0002_normalized_projection")
	}
	legacyRows, err := tx.QueryContext(ctx, `SELECT project_id,task_json FROM project_issue_projection ORDER BY project_id,issue_id`)
	if err != nil {
		return err
	}
	type projectedTask struct {
		projectID string
		task      domain.Task
	}
	var tasks []projectedTask
	for legacyRows.Next() {
		var projectID string
		var raw []byte
		if err = legacyRows.Scan(&projectID, &raw); err != nil {
			legacyRows.Close()
			return err
		}
		var task domain.Task
		if err = json.Unmarshal(raw, &task); err != nil {
			legacyRows.Close()
			return fmt.Errorf("decode legacy projected task: %w", err)
		}
		tasks = append(tasks, projectedTask{projectID, task})
	}
	if err = legacyRows.Close(); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE project_issue_projection; DROP TABLE project_session_projection; DROP TABLE project_worktree_projection; DROP TABLE project_issue_dependency_projection;
CREATE TABLE project_issue_projection (
 project_id TEXT NOT NULL, issue_id TEXT NOT NULL, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '', design TEXT NOT NULL DEFAULT '', acceptance TEXT NOT NULL DEFAULT '', assignee TEXT NOT NULL DEFAULT '', labels_json BLOB NOT NULL DEFAULT '[]', estimate INTEGER, implementations_json BLOB NOT NULL DEFAULT '[]', status TEXT NOT NULL, lifecycle TEXT NOT NULL, review_state TEXT NOT NULL DEFAULT 'none', display_phase TEXT NOT NULL, closed_outcome TEXT NOT NULL, archive_state TEXT NOT NULL DEFAULT 'live', deletion_state TEXT NOT NULL DEFAULT 'present', review_ready INTEGER NOT NULL DEFAULT 0, waiting_human INTEGER NOT NULL DEFAULT 0, waiting_human_source TEXT NOT NULL DEFAULT '', waiting_human_reason TEXT NOT NULL DEFAULT '', waiting_ai INTEGER NOT NULL DEFAULT 0, human_attention_rank INTEGER NOT NULL DEFAULT 0, priority INTEGER NOT NULL, issue_type TEXT NOT NULL, parent_issue_id TEXT NOT NULL DEFAULT '', has_tmux_session INTEGER NOT NULL DEFAULT 0, origin TEXT NOT NULL DEFAULT '', pr_number INTEGER NOT NULL DEFAULT 0, pr_remote_key TEXT NOT NULL DEFAULT '', pr_display_key TEXT NOT NULL DEFAULT '', pr_url TEXT NOT NULL DEFAULT '', pr_state TEXT NOT NULL DEFAULT '', pr_draft INTEGER NOT NULL DEFAULT 0, pr_checks_status TEXT NOT NULL DEFAULT '', ownership_owner_id TEXT NOT NULL DEFAULT '', ownership_owner_kind TEXT NOT NULL DEFAULT '', ownership_claimed_at TEXT, ownership_expires_at TEXT, coordination_leases_json BLOB NOT NULL DEFAULT '[]', operation_blockers_json BLOB NOT NULL DEFAULT '[]', fact_reasons_json BLOB NOT NULL DEFAULT '[]', git_diff_total INTEGER NOT NULL DEFAULT 0, session_rank INTEGER NOT NULL DEFAULT 0, runtime_updated_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(project_id, issue_id));
CREATE INDEX idx_project_issue_projection_search ON project_issue_projection(title, project_id, issue_id); CREATE INDEX idx_project_issue_projection_review ON project_issue_projection(review_ready, project_id, updated_at DESC); DELETE FROM project_issue_search_projection;
CREATE TABLE project_session_projection (project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,role TEXT NOT NULL DEFAULT '',scope_kind TEXT NOT NULL DEFAULT '',scope_id TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,activity TEXT NOT NULL,activity_source TEXT NOT NULL DEFAULT '',total_count INTEGER NOT NULL DEFAULT 0,active_count INTEGER NOT NULL DEFAULT 0,paused_count INTEGER NOT NULL DEFAULT 0,tmux_attached INTEGER NOT NULL DEFAULT 0,tmux_attached_count INTEGER NOT NULL DEFAULT 0,started_at TEXT,worktree TEXT NOT NULL,devserver_port INTEGER NOT NULL DEFAULT 0,devserver_command TEXT NOT NULL DEFAULT '',devserver_running INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL,PRIMARY KEY(project_id,session_id));
CREATE TABLE project_worktree_projection (project_id TEXT NOT NULL,issue_id TEXT NOT NULL,path TEXT NOT NULL,branch TEXT NOT NULL DEFAULT '',ahead_count INTEGER NOT NULL DEFAULT 0,behind_count INTEGER NOT NULL DEFAULT 0,additions INTEGER NOT NULL DEFAULT 0,deletions INTEGER NOT NULL DEFAULT 0,has_uncommitted_changes INTEGER NOT NULL DEFAULT 0,has_conflicts INTEGER NOT NULL DEFAULT 0,conflict_files_json BLOB NOT NULL DEFAULT '[]',updated_at TEXT NOT NULL,PRIMARY KEY(project_id,issue_id));
CREATE TABLE project_issue_dependency_projection (project_id TEXT NOT NULL,issue_id TEXT NOT NULL,depends_on_issue_id TEXT NOT NULL,dependency_type TEXT NOT NULL,PRIMARY KEY(project_id,issue_id,depends_on_issue_id,dependency_type));`); err != nil {
		return fmt.Errorf("rebuild normalized projection tables: %w", err)
	}
	for _, item := range tasks {
		if err = insertTask(ctx, tx, item.projectID, item.task); err != nil {
			return fmt.Errorf("migrate normalized projected task: %w", err)
		}
	}
	if err = recordAppliedUserMigration(ctx, tx, "user_0002_normalized_projection"); err != nil {
		return err
	}
	return nil
}

func (s *Store) seedViews(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin user view seed: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, view := range []domain.BoardView{domain.DefaultBoardView(), domain.PlanningBoardView(), domain.OrchestrationBoardView(), domain.CloseoutBoardView(), domain.GridBoardView(), domain.TreeBoardView()} {
		if err := preserveUserViewIDConflict(ctx, tx, view.ID); err != nil {
			return err
		}
		raw, err := domain.EncodeBoardViewDefinitionJSON(view)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO user_views(view_id,title,definition_json,built_in,created_at,updated_at) VALUES(?,?,?,1,?,?) ON CONFLICT(view_id) DO UPDATE SET title=excluded.title,definition_json=excluded.definition_json,built_in=1,updated_at=excluded.updated_at,deleted_at=NULL WHERE user_views.built_in=1 AND (user_views.title<>excluded.title OR user_views.definition_json<>excluded.definition_json OR user_views.deleted_at IS NOT NULL)`, view.ID, view.Title, raw, now, now); err != nil {
			return err
		}
	}
	for consumer, viewID := range map[string]string{"global_board": string(domain.BoardViewDefaultID), "tmux_selector": string(domain.BoardViewOrchestrationID), "search": string(domain.BoardViewDefaultID), "review": string(domain.BoardViewCloseoutID)} {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_view_selections(consumer,view_id,updated_at) VALUES(?,?,?)`, consumer, viewID, now); err != nil {
			return err
		}
	}
	if s.seedBeforeCommit != nil {
		if err := s.seedBeforeCommit(); err != nil {
			return fmt.Errorf("before user view seed commit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit user view seed: %w", err)
	}
	return nil
}

func preserveUserViewIDConflict(ctx context.Context, tx *sql.Tx, builtInID domain.BoardViewID) error {
	var raw, projectIDs []byte
	var title, scope, createdAt, updatedAt string
	var deletedAt sql.NullString
	var builtIn int
	err := tx.QueryRowContext(ctx, `SELECT title,definition_json,project_scope,project_ids_json,built_in,created_at,updated_at,deleted_at FROM user_views WHERE view_id=?`, builtInID).Scan(&title, &raw, &scope, &projectIDs, &builtIn, &createdAt, &updatedAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) || builtIn != 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect user view id conflict %q: %w", builtInID, err)
	}
	view, err := domain.DecodeBoardViewDefinitionJSON(raw)
	if err != nil {
		return fmt.Errorf("preserve custom user view conflicting with built-in %q: %w", builtInID, err)
	}
	base := string(builtInID) + "-custom"
	candidate := base
	for suffix := 2; ; suffix++ {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_views WHERE view_id=?)`, candidate).Scan(&exists); err != nil {
			return fmt.Errorf("find conflict-free user view id for %q: %w", builtInID, err)
		}
		if !exists {
			break
		}
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
	view.ID = domain.BoardViewID(candidate)
	encoded, err := domain.EncodeBoardViewDefinitionJSON(view)
	if err != nil {
		return fmt.Errorf("encode preserved custom user view %q: %w", builtInID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_views(view_id,title,definition_json,project_scope,project_ids_json,built_in,created_at,updated_at,deleted_at) VALUES(?,?,?,?,?,0,?,?,?)`, candidate, title, encoded, scope, projectIDs, createdAt, updatedAt, deletedAt); err != nil {
		return fmt.Errorf("preserve custom user view %q as %q: %w", builtInID, candidate, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_view_selections SET view_id=? WHERE view_id=?`, candidate, builtInID); err != nil {
		return fmt.Errorf("preserve custom user view selections for %q: %w", builtInID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_views WHERE view_id=? AND built_in=0`, builtInID); err != nil {
		return fmt.Errorf("remove conflicting custom user view %q: %w", builtInID, err)
	}
	return nil
}

func (s *Store) ResolveView(ctx context.Context, viewID, consumer string) (domain.BoardView, error) {
	record, err := s.ResolveGlobalView(ctx, viewID, consumer)
	return record.View, err
}

func (s *Store) ResolveGlobalView(ctx context.Context, viewID, consumer string) (protocol.GlobalViewRecord, error) {
	viewID = strings.TrimSpace(viewID)
	if viewID == "" && strings.TrimSpace(consumer) != "" {
		_ = s.db.QueryRowContext(ctx, `SELECT view_id FROM user_view_selections WHERE consumer=?`, strings.TrimSpace(consumer)).Scan(&viewID)
	}
	if viewID == "" {
		viewID = string(domain.BoardViewDefaultID)
	}
	var raw, projectIDsRaw []byte
	var scopeKind protocol.GlobalViewScopeKind
	if err := s.db.QueryRowContext(ctx, `SELECT definition_json,project_scope,project_ids_json FROM user_views WHERE view_id=? AND deleted_at IS NULL`, viewID).Scan(&raw, &scopeKind, &projectIDsRaw); err != nil {
		return protocol.GlobalViewRecord{}, err
	}
	view, err := domain.DecodeBoardViewDefinitionJSON(raw)
	if err != nil {
		return protocol.GlobalViewRecord{}, err
	}
	scope, err := decodeViewScope(scopeKind, projectIDsRaw)
	if err != nil {
		return protocol.GlobalViewRecord{}, err
	}
	return protocol.GlobalViewRecord{View: view, Scope: scope}, nil
}

func (s *Store) ListViews(ctx context.Context) ([]domain.BoardViewRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT definition_json,built_in,created_at,updated_at FROM user_views WHERE deleted_at IS NULL ORDER BY built_in DESC,title,view_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BoardViewRecord
	for rows.Next() {
		var raw []byte
		var built bool
		var created, updated string
		if err = rows.Scan(&raw, &built, &created, &updated); err != nil {
			return nil, err
		}
		view, e := domain.DecodeBoardViewDefinitionJSON(raw)
		if e != nil {
			return nil, e
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, created)
		updatedAt, _ := time.Parse(time.RFC3339Nano, updated)
		out = append(out, domain.BoardViewRecord{ProjectID: "global", View: view, BuiltIn: built, CreatedAt: createdAt, UpdatedAt: updatedAt})
	}
	return out, rows.Err()
}

func (s *Store) ListGlobalViews(ctx context.Context) ([]protocol.GlobalViewRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT definition_json,project_scope,project_ids_json FROM user_views WHERE deleted_at IS NULL ORDER BY built_in DESC,title,view_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.GlobalViewRecord
	for rows.Next() {
		var raw, projectIDsRaw []byte
		var scopeKind protocol.GlobalViewScopeKind
		if err = rows.Scan(&raw, &scopeKind, &projectIDsRaw); err != nil {
			return nil, err
		}
		view, e := domain.DecodeBoardViewDefinitionJSON(raw)
		if e != nil {
			return nil, e
		}
		scope, e := decodeViewScope(scopeKind, projectIDsRaw)
		if e != nil {
			return nil, e
		}
		out = append(out, protocol.GlobalViewRecord{View: view, Scope: scope})
	}
	return out, rows.Err()
}

func (s *Store) ListViewSelections(ctx context.Context) (map[protocol.GlobalViewConsumer]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT consumer,view_id FROM user_view_selections ORDER BY consumer`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[protocol.GlobalViewConsumer]string)
	for rows.Next() {
		var consumer protocol.GlobalViewConsumer
		var viewID string
		if err := rows.Scan(&consumer, &viewID); err != nil {
			return nil, err
		}
		if consumer.Valid() {
			out[consumer] = viewID
		}
	}
	return out, rows.Err()
}

func (s *Store) SaveView(ctx context.Context, view domain.BoardView) (domain.BoardViewRecord, error) {
	var record domain.BoardViewRecord
	err := sqliteutil.WithWriteLock(s.dbPath, func() error { var err error; record, err = s.saveView(ctx, view); return err })
	return record, err
}

func (s *Store) SaveGlobalView(ctx context.Context, record protocol.GlobalViewRecord) (protocol.GlobalViewRecord, error) {
	if err := record.Scope.Validate(); err != nil {
		return protocol.GlobalViewRecord{}, err
	}
	var saved domain.BoardViewRecord
	err := sqliteutil.WithWriteLock(s.dbPath, func() error {
		var err error
		saved, err = s.saveViewWithScope(ctx, record.View, record.Scope)
		return err
	})
	if err != nil {
		return protocol.GlobalViewRecord{}, err
	}
	record.View = saved.View
	record.Scope.Kind = normalizedScopeKind(record.Scope)
	return record, nil
}

func (s *Store) saveView(ctx context.Context, view domain.BoardView) (domain.BoardViewRecord, error) {
	return s.saveViewWithScope(ctx, view, protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeAllProjects})
}
func (s *Store) saveViewWithScope(ctx context.Context, view domain.BoardView, scope protocol.GlobalViewScope) (domain.BoardViewRecord, error) {
	view = view.Normalized()
	if err := view.Validate(); err != nil {
		return domain.BoardViewRecord{}, err
	}
	raw, err := domain.EncodeBoardViewDefinitionJSON(view)
	if err != nil {
		return domain.BoardViewRecord{}, err
	}
	now := s.now().UTC()
	projectIDsRaw, err := encodeViewScope(scope)
	if err != nil {
		return domain.BoardViewRecord{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO user_views(view_id,title,definition_json,project_scope,project_ids_json,built_in,created_at,updated_at) VALUES(?,?,?,?,?,0,?,?) ON CONFLICT(view_id) DO UPDATE SET title=excluded.title,definition_json=excluded.definition_json,project_scope=excluded.project_scope,project_ids_json=excluded.project_ids_json,updated_at=excluded.updated_at,deleted_at=NULL WHERE user_views.built_in=0`, view.ID, view.Title, raw, normalizedScopeKind(scope), projectIDsRaw, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return domain.BoardViewRecord{}, err
	}
	return domain.BoardViewRecord{ProjectID: "global", View: view, CreatedAt: now, UpdatedAt: now}, nil
}

func normalizedScopeKind(scope protocol.GlobalViewScope) protocol.GlobalViewScopeKind {
	if scope.Kind == "" {
		return protocol.GlobalViewScopeAllProjects
	}
	return scope.Kind
}
func encodeViewScope(scope protocol.GlobalViewScope) ([]byte, error) {
	ids := append([]naming.ProjectID(nil), scope.ProjectIDs...)
	if normalizedScopeKind(scope) == protocol.GlobalViewScopeCurrentProject && scope.CurrentProjectID != "" {
		ids = []naming.ProjectID{scope.CurrentProjectID}
	}
	return json.Marshal(ids)
}
func decodeViewScope(kind protocol.GlobalViewScopeKind, raw []byte) (protocol.GlobalViewScope, error) {
	if kind == "" {
		kind = protocol.GlobalViewScopeAllProjects
	}
	var ids []naming.ProjectID
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &ids); err != nil {
			return protocol.GlobalViewScope{}, fmt.Errorf("decode global view scope: %w", err)
		}
	}
	scope := protocol.GlobalViewScope{Kind: kind}
	switch kind {
	case protocol.GlobalViewScopeSelectedProjects:
		scope.ProjectIDs = ids
	case protocol.GlobalViewScopeCurrentProject:
		if len(ids) > 0 {
			scope.CurrentProjectID = ids[0]
		}
	}
	if err := scope.Validate(); err != nil {
		return protocol.GlobalViewScope{}, err
	}
	return scope, nil
}

func (s *Store) DeleteView(ctx context.Context, viewID string) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error { return s.deleteView(ctx, viewID) })
}
func (s *Store) deleteView(ctx context.Context, viewID string) error {
	viewID = domain.NormalizeBoardViewID(viewID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE user_views SET deleted_at=?,updated_at=? WHERE view_id=? AND built_in=0 AND deleted_at IS NULL`, now, now, viewID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("global view %q not found or built-in", viewID)
	}
	defaults := map[protocol.GlobalViewConsumer]string{protocol.GlobalViewConsumerBoard: string(domain.BoardViewDefaultID), protocol.GlobalViewConsumerTmuxSelector: string(domain.BoardViewOrchestrationID), protocol.GlobalViewConsumerSearch: string(domain.BoardViewDefaultID), protocol.GlobalViewConsumerReview: string(domain.BoardViewCloseoutID)}
	for consumer, fallback := range defaults {
		if _, err := tx.ExecContext(ctx, `UPDATE user_view_selections SET view_id=?,updated_at=? WHERE consumer=? AND view_id=?`, fallback, now, consumer, viewID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SelectView(ctx context.Context, consumer, viewID string) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error { return s.selectView(ctx, consumer, viewID) })
}
func (s *Store) selectView(ctx context.Context, consumer, viewID string) error {
	if _, err := s.ResolveView(ctx, viewID, ""); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_view_selections(consumer,view_id,updated_at) VALUES(?,?,?) ON CONFLICT(consumer) DO UPDATE SET view_id=excluded.view_id,updated_at=excluded.updated_at`, consumer, domain.NormalizeBoardViewID(viewID), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type ProjectInput struct {
	ProjectID, Name, Path, DBPath string
	SchemaVersion                 int
	SchemaFingerprint             string
	Checkpoint                    uint64
	RefreshGeneration             uint64
	Tasks                         []domain.Task
	Delta                         *ProjectDeltaState
}

// BeginProjectRefresh reserves the next durable publication generation before
// a daemon reads the authoritative project store. Only that generation may
// subsequently publish success or failure.
func (s *Store) BeginProjectRefresh(ctx context.Context, project CatalogProject) (uint64, error) {
	var generation uint64
	project.ProjectID = strings.TrimSpace(project.ProjectID)
	if project.ProjectID == "" {
		return 0, fmt.Errorf("catalog project ID is empty")
	}
	err := sqliteutil.WithWriteLock(s.dbPath, func() error {
		now := s.now().UTC().Format(time.RFC3339Nano)
		return s.db.QueryRowContext(ctx, `INSERT INTO projects(project_id,name,path,db_path,projection_version,refresh_generation,freshness,last_attempt_at,last_error,registered) VALUES(?,?,?,?,?,1,'stale',?,'refresh in progress',1)
ON CONFLICT(project_id) DO UPDATE SET name=excluded.name,path=excluded.path,db_path=excluded.db_path,projection_version=excluded.projection_version,refresh_generation=projects.refresh_generation+1,freshness='stale',last_attempt_at=excluded.last_attempt_at,last_error='refresh in progress',registered=1
RETURNING refresh_generation`, project.ProjectID, project.Name, cleanCatalogPath(project.Path), cleanCatalogPath(project.DBPath), projectionVersion, now).Scan(&generation)
	})
	return generation, err
}

func (s *Store) ReplaceProject(ctx context.Context, in ProjectInput) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error { return s.replaceProject(ctx, in) })
}

func (s *Store) replaceProject(ctx context.Context, in ProjectInput) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current uint64
	var currentGeneration uint64
	err = tx.QueryRowContext(ctx, `SELECT checkpoint,refresh_generation FROM projects WHERE project_id=?`, in.ProjectID).Scan(&current, &currentGeneration)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	// Refresh generations order daemon publications. Composite source
	// checkpoints include independent issue/runtime clocks and are therefore
	// freshness tokens, not a globally monotonic sequence.
	if err == nil && in.RefreshGeneration == 0 && in.Checkpoint < current {
		return nil
	}
	if err == nil && in.RefreshGeneration != 0 && in.RefreshGeneration != currentGeneration {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects(project_id,name,path,db_path,schema_version,schema_fingerprint,projection_version,checkpoint,freshness,refreshed_at,last_attempt_at,last_error,registered)
VALUES(?,?,?,?,?,?,?,?,'fresh',?,?,'',1) ON CONFLICT(project_id) DO UPDATE SET name=excluded.name,path=excluded.path,db_path=excluded.db_path,schema_version=excluded.schema_version,schema_fingerprint=excluded.schema_fingerprint,projection_version=excluded.projection_version,checkpoint=excluded.checkpoint,freshness='fresh',refreshed_at=excluded.refreshed_at,last_attempt_at=excluded.last_attempt_at,last_error='',registered=1`, in.ProjectID, in.Name, in.Path, in.DBPath, in.SchemaVersion, in.SchemaFingerprint, projectionVersion, in.Checkpoint, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if in.Delta != nil {
		sourceVector, marshalErr := json.Marshal(in.Delta.SourceVector)
		if marshalErr != nil {
			return fmt.Errorf("encode project delta source vector: %w", marshalErr)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE projects SET delta_cursor=?,delta_hash=?,delta_source_vector_json=?,delta_projector_id=?,delta_projector_schema=?,delta_projector_build=?,delta_projector_checksum=? WHERE project_id=?`,
			in.Delta.Cursor, in.Delta.Hash, sourceVector, in.Delta.Projector.ID, in.Delta.Projector.SchemaVersion, in.Delta.Projector.Build, in.Delta.Projector.Checksum, in.ProjectID); err != nil {
			return fmt.Errorf("publish project delta bootstrap state: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_issue_projection WHERE project_id=?`, in.ProjectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_issue_search_projection WHERE project_id=?`, in.ProjectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_session_projection WHERE project_id=?`, in.ProjectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_worktree_projection WHERE project_id=?`, in.ProjectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_issue_dependency_projection WHERE project_id=?`, in.ProjectID); err != nil {
		return err
	}
	for _, task := range in.Tasks {
		if e := insertTask(ctx, tx, in.ProjectID, task); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func insertTask(ctx context.Context, tx *sql.Tx, projectID string, task domain.Task) error {
	if task.Status == "" {
		task.Status = domain.StatusOpen
	}
	if task.Type == "" {
		task.Type = domain.TypeTask
	}
	state, err := task.IssueState()
	if err != nil {
		return fmt.Errorf("normalize issue %s state: %w", task.ID, err)
	}
	facts := task.IssueFacts()
	parentID := ""
	if task.ParentID != nil {
		parentID = task.ParentID.String()
	}
	waitingAI, _ := domain.BoardColumnPredicate{Kind: domain.BoardPredicateWaitingAIDelegated}.MatchTask(task)
	labels, err := json.Marshal(task.Labels)
	if err != nil {
		return err
	}
	impls, err := json.Marshal(task.Implementations)
	if err != nil {
		return err
	}
	leases, err := json.Marshal(task.CoordinationLeases)
	if err != nil {
		return err
	}
	blockers, err := json.Marshal(facts.OperationBlockers)
	if err != nil {
		return err
	}
	reasons, err := json.Marshal(facts.Reasons)
	if err != nil {
		return err
	}
	var estimate any
	if task.Estimate != nil {
		estimate = *task.Estimate
	}
	var runtimeUpdated, ownershipClaimed, ownershipExpires any
	if !task.RuntimeUpdatedAt.IsZero() {
		runtimeUpdated = formatTime(task.RuntimeUpdatedAt)
	}
	ownerID, ownerKind := "", ""
	if task.Ownership != nil {
		ownerID, ownerKind, ownershipClaimed = task.Ownership.OwnerID, task.Ownership.OwnerKind, formatTime(task.Ownership.ClaimedAt)
		if task.Ownership.ExpiresAt != nil {
			ownershipExpires = formatTime(*task.Ownership.ExpiresAt)
		}
	}
	var pr domain.PullRequest
	if task.PullRequest != nil {
		pr = *task.PullRequest
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_issue_projection(
project_id,issue_id,title,description,notes,design,acceptance,assignee,labels_json,estimate,implementations_json,
status,lifecycle,review_state,display_phase,closed_outcome,archive_state,review_ready,waiting_human,waiting_human_source,waiting_human_reason,waiting_ai,human_attention_rank,
priority,issue_type,parent_issue_id,has_tmux_session,origin,pr_number,pr_remote_key,pr_display_key,pr_url,pr_state,pr_draft,pr_checks_status,
ownership_owner_id,ownership_owner_kind,ownership_claimed_at,ownership_expires_at,coordination_leases_json,operation_blockers_json,fact_reasons_json,
git_diff_total,session_rank,runtime_updated_at,created_at,updated_at) VALUES(`+strings.TrimSuffix(strings.Repeat("?,", 47), ",")+`)`,
		projectID, task.ID.String(), task.Title, task.Description, task.Notes, task.Design, task.Acceptance, task.Assignee, labels, estimate, impls,
		string(task.Status), string(state.Workflow()), string(state.Review()), string(facts.DisplayPhase), string(state.CloseOutcome()), string(state.Archive()), boolInt(domain.TaskReviewReady(task)), boolInt(facts.WaitingHuman), string(facts.WaitingHumanSource), facts.WaitingHumanReason, boolInt(waitingAI), int(domain.HumanAttentionRank(task)),
		int(task.Priority), string(task.Type), parentID, boolInt(task.HasTmuxSession), task.Origin, pr.Number, pr.RemoteKey, pr.DisplayKey, pr.URL, pr.State, boolInt(pr.Draft), pr.ChecksStatus,
		ownerID, ownerKind, ownershipClaimed, ownershipExpires, leases, blockers, reasons, task.GitAdditions+task.GitDeletions, projectionSessionRank(task), runtimeUpdated, formatTime(task.CreatedAt), formatTime(task.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert normalized issue %s: %w", task.ID, err)
	}
	searchFields := []string{task.ID.String(), task.Title, task.Description, task.Notes, task.Design, task.Acceptance, task.Assignee, string(task.Status), task.IssueDisplayStatusText(), task.Priority.String(), string(task.Type)}
	searchFields = append(searchFields, task.Labels...)
	searchFields = append(searchFields, task.Implementations...)
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_issue_search_projection(project_id,issue_id,content) VALUES(?,?,?)`, projectID, task.ID.String(), strings.Join(searchFields, "\n")); err != nil {
		return fmt.Errorf("index normalized issue %s: %w", task.ID, err)
	}
	updated := formatTime(task.UpdatedAt)
	if task.Session != nil {
		session := task.Session
		var started any
		if session.StartedAt != nil {
			started = formatTime(*session.StartedAt)
		}
		devPort, devCommand, devRunning := 0, "", false
		if session.DevServer != nil {
			devPort, devCommand, devRunning = session.DevServer.Port, session.DevServer.Command, session.DevServer.Running
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO project_session_projection(project_id,session_id,issue_id,role,scope_kind,scope_id,state,activity,activity_source,total_count,active_count,paused_count,tmux_attached,tmux_attached_count,started_at,worktree,devserver_port,devserver_command,devserver_running,updated_at) VALUES(`+strings.TrimSuffix(strings.Repeat("?,", 20), ",")+`)`, projectID, naming.CanonicalSessionID(projectID, task.ID.String()), task.ID.String(), session.Role, session.ScopeKind, session.ScopeID, string(session.State), session.Activity, session.ActivitySource, session.TotalCount, session.ActiveCount, session.PausedCount, boolInt(session.TmuxAttached), session.TmuxAttachedCount, started, session.Worktree, devPort, devCommand, boolInt(devRunning), formatTime(session.UpdatedAt))
		if err != nil {
			return err
		}
	}
	if task.HasWorktree || task.Session != nil && strings.TrimSpace(task.Session.Worktree) != "" {
		worktree := ""
		if task.Session != nil {
			worktree = task.Session.Worktree
		}
		conflicts, marshalErr := json.Marshal(task.ConflictFiles)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO project_worktree_projection(project_id,issue_id,path,ahead_count,behind_count,additions,deletions,has_uncommitted_changes,has_conflicts,conflict_files_json,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, projectID, task.ID.String(), worktree, task.GitAheadCount, task.GitBehindCount, task.GitAdditions, task.GitDeletions, boolInt(task.HasUncommittedChanges), boolInt(task.HasConflicts), conflicts, updated)
		if err != nil {
			return err
		}
	}
	for _, dependency := range task.Dependencies {
		if _, err = tx.ExecContext(ctx, `INSERT INTO project_issue_dependency_projection(project_id,issue_id,depends_on_issue_id,dependency_type) VALUES(?,?,?,?)`, projectID, task.ID.String(), dependency.ID.String(), string(dependency.Type)); err != nil {
			return err
		}
	}
	return nil
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return time.Time{}.UTC().Format(time.RFC3339Nano)
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func projectionSessionRank(task domain.Task) int {
	if task.Session == nil {
		if task.HasWorktree {
			return 1
		}
		return 0
	}
	switch task.Session.State {
	case domain.SessionWaiting:
		return 7
	case domain.SessionBusy:
		return 6
	case domain.SessionPaused:
		return 5
	case domain.SessionError:
		return 4
	case domain.SessionDone:
		return 3
	case domain.SessionIdle:
		return 2
	default:
		return 1
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) MarkUnavailable(ctx context.Context, projectID, name, path, dbPath string, cause error) error {
	return s.MarkUnavailableGeneration(ctx, projectID, name, path, dbPath, 0, cause)
}
func (s *Store) MarkUnavailableGeneration(ctx context.Context, projectID, name, path, dbPath string, generation uint64, cause error) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error { return s.markUnavailable(ctx, projectID, name, path, dbPath, generation, cause) })
}
func (s *Store) markUnavailable(ctx context.Context, projectID, name, path, dbPath string, generation uint64, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if generation != 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE projects SET name=?,path=?,db_path=?,projection_version=?,freshness='unavailable',last_attempt_at=?,last_error=?,registered=1 WHERE project_id=? AND refresh_generation=?`, name, path, dbPath, projectionVersion, s.now().UTC().Format(time.RFC3339Nano), msg, projectID, generation)
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(project_id,name,path,db_path,projection_version,freshness,last_attempt_at,last_error,registered) VALUES(?,?,?,?,?,'unavailable',?,?,1) ON CONFLICT(project_id) DO UPDATE SET name=excluded.name,path=excluded.path,db_path=excluded.db_path,projection_version=excluded.projection_version,freshness='unavailable',last_attempt_at=excluded.last_attempt_at,last_error=excluded.last_error,registered=1`, projectID, name, path, dbPath, projectionVersion, now, msg)
	return err
}

func (s *Store) ReconcileCatalog(ctx context.Context, projectIDs []string) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error { return s.reconcileCatalog(ctx, projectIDs) })
}
func (s *Store) reconcileCatalog(ctx context.Context, projectIDs []string) error {
	if len(projectIDs) == 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE projects SET registered=0,freshness='unavailable',last_error='project is no longer registered'`)
		return err
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(projectIDs)), ",")
	args := make([]any, len(projectIDs))
	for i, v := range projectIDs {
		args[i] = v
	}
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET registered=0,freshness='unavailable',last_error='project is no longer registered' WHERE project_id NOT IN (`+marks+`)`, args...)
	return err
}

// CatalogProject is the canonical registry metadata copied into the user DB.
// Reconciliation never reads project databases and never removes last-good rows.
type CatalogProject struct {
	ProjectID string
	Name      string
	Path      string
	DBPath    string
}

func (s *Store) ReconcileProjects(ctx context.Context, projects []CatalogProject) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		ids := make([]string, 0, len(projects))
		for _, project := range projects {
			project.ProjectID = strings.TrimSpace(project.ProjectID)
			if project.ProjectID == "" {
				return fmt.Errorf("catalog project ID is empty")
			}
			ids = append(ids, project.ProjectID)
			_, err = tx.ExecContext(ctx, `INSERT INTO projects(project_id,name,path,db_path,projection_version,freshness,last_error,registered) VALUES(?,?,?,?,?,'stale','project has not been projected',1)
ON CONFLICT(project_id) DO UPDATE SET name=excluded.name,path=excluded.path,db_path=excluded.db_path,freshness=CASE WHEN projects.registered=0 THEN 'stale' ELSE projects.freshness END,last_error=CASE WHEN projects.registered=0 THEN 'project re-registered; refresh required' ELSE projects.last_error END,registered=1`, project.ProjectID, project.Name, cleanCatalogPath(project.Path), cleanCatalogPath(project.DBPath), projectionVersion)
			if err != nil {
				return err
			}
		}
		if len(ids) == 0 {
			_, err = tx.ExecContext(ctx, `UPDATE projects SET registered=0,freshness='unavailable',last_error='project is no longer registered'`)
		} else {
			marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
			args := make([]any, len(ids))
			for i, id := range ids {
				args[i] = id
			}
			_, err = tx.ExecContext(ctx, `UPDATE projects SET registered=0,freshness='unavailable',last_error='project is no longer registered' WHERE project_id NOT IN (`+marks+`)`, args...)
		}
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

func cleanCatalogPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func (s *Store) Snapshot(ctx context.Context, query string) (protocol.GlobalSnapshotResponseBody, error) {
	return s.SnapshotForView(ctx, query, nil)
}

func (s *Store) SnapshotForView(ctx context.Context, query string, view *domain.BoardView) (protocol.GlobalSnapshotResponseBody, error) {
	return s.SnapshotForScopedView(ctx, query, view, protocol.GlobalViewScope{})
}

// SnapshotForScopedView pushes project scope into SQLite so excluded projects
// are never hydrated into memory.
func (s *Store) SnapshotForScopedView(ctx context.Context, query string, view *domain.BoardView, scope protocol.GlobalViewScope) (protocol.GlobalSnapshotResponseBody, error) {
	return s.SnapshotForScopedViewWithTasks(ctx, query, view, scope, nil)
}

// SnapshotForScopedViewWithTasks returns view candidates plus explicitly scoped
// task metadata. Explicit tasks do not affect the view projection; callers use
// them to hydrate live runtime rows that remain visible independently of a view.
func (s *Store) SnapshotForScopedViewWithTasks(ctx context.Context, query string, view *domain.BoardView, scope protocol.GlobalViewScope, hydrate []protocol.ScopedIssueID) (protocol.GlobalSnapshotResponseBody, error) {
	now := s.now().UTC()
	out := protocol.GlobalSnapshotResponseBody{SchemaVersion: projectionVersion, GeneratedAt: now}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, fmt.Errorf("begin global snapshot: %w", err)
	}
	defer tx.Rollback()
	q := `SELECT project_id,name,path,db_path,schema_version,schema_fingerprint,projection_version,checkpoint,delta_cursor,delta_hash,delta_source_vector_json,delta_projector_id,delta_projector_schema,delta_projector_build,delta_projector_checksum,freshness,refreshed_at,last_attempt_at,last_error,registered FROM projects`
	args := []any{}
	ids := scopeProjectIDs(scope)
	if len(ids) > 0 {
		ids = appendUniqueStrings(ids, hydrationProjectIDs(hydrate)...)
	}
	if len(ids) > 0 {
		q += ` WHERE project_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	q += ` ORDER BY name,project_id`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return out, err
	}
	var projects []protocol.GlobalProjectSnapshot
	for rows.Next() {
		var p protocol.GlobalProjectSnapshot
		var refreshed, attempted sql.NullString
		var deltaSources []byte
		if err = rows.Scan(&p.ProjectID, &p.Name, &p.Path, &p.DBPath, &p.SchemaVersion, &p.SchemaFingerprint, &p.ProjectionVersion, &p.Checkpoint,
			&p.DeltaCursor, &p.DeltaHash, &deltaSources, &p.DeltaProjector.ID, &p.DeltaProjector.SchemaVersion, &p.DeltaProjector.Build, &p.DeltaProjector.Checksum,
			&p.Freshness, &refreshed, &attempted, &p.LastError, &p.Registered); err != nil {
			return out, err
		}
		if err = decodeJSON(deltaSources, &p.DeltaSourceVector); err != nil {
			return out, err
		}
		if attempted.Valid {
			if t, e := time.Parse(time.RFC3339Nano, attempted.String); e == nil {
				p.LastAttemptAt = &t
			}
		}
		if refreshed.Valid {
			if t, e := time.Parse(time.RFC3339Nano, refreshed.String); e == nil {
				p.LastRefreshedAt = &t
			}
		}
		if p.Registered && p.DeltaProjector.ID == "" && p.Freshness == protocol.GlobalProjectionFreshnessFresh && p.LastRefreshedAt != nil && s.maxProjectionAge > 0 && now.Sub(*p.LastRefreshedAt) > s.maxProjectionAge {
			p.Freshness = protocol.GlobalProjectionFreshnessStale
		}
		if p.Freshness != protocol.GlobalProjectionFreshnessFresh {
			out.Partial = true
		}
		projects = append(projects, p)
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if s.snapshotAfterProjects != nil {
		s.snapshotAfterProjects()
	}
	for i := range projects {
		if projects[i].Registered {
			projects[i].Tasks, err = s.tasks(ctx, tx, projects[i].ProjectID, query, view, hydratedIssueIDs(hydrate, projects[i].ProjectID), nil)
			if err != nil {
				return out, err
			}
		}
	}
	out.Projects = projects
	if err = tx.Commit(); err != nil {
		return out, fmt.Errorf("commit global snapshot read: %w", err)
	}
	return out, nil
}

func hydrationProjectIDs(ids []protocol.ScopedIssueID) []string {
	out := make([]string, 0, len(ids))
	for _, scoped := range ids {
		if projectID := protocol.NormalizeProjectID(scoped.ProjectID.String()); projectID != "" {
			out = appendUniqueStrings(out, projectID)
		}
	}
	return out
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	for _, value := range dst {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		dst = append(dst, value)
	}
	return dst
}

func scopeProjectIDs(scope protocol.GlobalViewScope) []string {
	if scope.Kind == protocol.GlobalViewScopeCurrentProject {
		return []string{protocol.NormalizeProjectID(scope.CurrentProjectID.String())}
	}
	if scope.Kind != protocol.GlobalViewScopeSelectedProjects {
		return nil
	}
	out := make([]string, 0, len(scope.ProjectIDs))
	seen := map[string]bool{}
	for _, raw := range scope.ProjectIDs {
		id := protocol.NormalizeProjectID(raw.String())
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func hydratedIssueIDs(ids []protocol.ScopedIssueID, projectID string) []string {
	projectID = protocol.NormalizeProjectID(projectID)
	seen := make(map[string]struct{})
	out := make([]string, 0, len(ids))
	for _, scoped := range ids {
		if protocol.NormalizeProjectID(scoped.ProjectID.String()) != projectID {
			continue
		}
		issueID := strings.TrimSpace(scoped.IssueID.String())
		if issueID == "" {
			continue
		}
		if _, ok := seen[issueID]; ok {
			continue
		}
		seen[issueID] = struct{}{}
		out = append(out, issueID)
	}
	return out
}

func (s *Store) tasks(ctx context.Context, db queryer, projectID, query string, view *domain.BoardView, hydrate, only []string) ([]domain.Task, error) {
	q := `SELECT i.issue_id,i.title,i.description,i.notes,i.design,i.acceptance,i.assignee,i.labels_json,i.estimate,i.implementations_json,
i.status,i.lifecycle,i.review_state,i.closed_outcome,i.archive_state,i.waiting_human,i.waiting_human_source,i.waiting_human_reason,i.waiting_ai,
i.priority,i.issue_type,i.parent_issue_id,i.has_tmux_session,i.origin,i.pr_number,i.pr_remote_key,i.pr_display_key,i.pr_url,i.pr_state,i.pr_draft,i.pr_checks_status,
i.ownership_owner_id,i.ownership_owner_kind,i.ownership_claimed_at,i.ownership_expires_at,i.coordination_leases_json,i.operation_blockers_json,i.fact_reasons_json,i.runtime_updated_at,i.created_at,i.updated_at,
s.issue_id,s.role,s.scope_kind,s.scope_id,s.state,s.activity,s.activity_source,s.total_count,s.active_count,s.paused_count,s.tmux_attached,s.tmux_attached_count,s.started_at,s.worktree,s.devserver_port,s.devserver_command,s.devserver_running,s.updated_at,
w.issue_id,w.path,w.ahead_count,w.behind_count,w.additions,w.deletions,w.has_uncommitted_changes,w.has_conflicts,w.conflict_files_json
FROM project_issue_projection i
LEFT JOIN project_session_projection s ON s.project_id=i.project_id AND s.issue_id=i.issue_id
LEFT JOIN project_worktree_projection w ON w.project_id=i.project_id AND w.issue_id=i.issue_id
WHERE i.project_id=?`
	args := []any{projectID}
	if len(only) > 0 {
		q += ` AND i.issue_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(only)), ",") + `)`
		for _, issueID := range only {
			args = append(args, issueID)
		}
	}
	q += ` AND (`
	candidateClauses := make([]string, 0, 2)
	if strings.TrimSpace(query) != "" {
		if expression := domain.ContentQueryFTSExpression(query); expression != "" {
			candidateClauses = append(candidateClauses, `i.issue_id IN (SELECT issue_id FROM project_issue_search_projection WHERE project_id=? AND project_issue_search_projection MATCH ?)`)
			args = append(args, projectID, expression)
		}
	}
	if view != nil {
		clause, viewArgs := viewCandidateSQL(*view)
		if clause != "" {
			candidateClauses = append(candidateClauses, "("+clause+")")
			args = append(args, viewArgs...)
		}
	}
	if len(candidateClauses) == 0 {
		candidateClauses = append(candidateClauses, `1=1`)
	}
	q += strings.Join(candidateClauses, ` AND `) + `)`
	if len(hydrate) > 0 {
		q += ` OR (i.project_id=? AND i.issue_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(hydrate)), ",") + `))`
		args = append(args, projectID)
		for _, issueID := range hydrate {
			args = append(args, issueID)
		}
	}
	q += ` ORDER BY i.updated_at DESC,i.issue_id`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		var t domain.Task
		var labels, impls, leases, blockers, reasons, conflicts []byte
		var estimate sql.NullInt64
		var lifecycle, review, outcome, archive string
		var parent string
		var hasTmux, prDraft bool
		var pr domain.PullRequest
		var ownerID, ownerKind string
		var ownerClaimed, ownerExpires, runtimeUpdated sql.NullString
		var created, updated string
		var sessionIssue sql.NullString
		var sess domain.Session
		var sessState, sessUpdated sql.NullString
		var sessTotal, sessActive, sessPaused, sessAttachedCount, devPort sql.NullInt64
		var sessAttached, devRunning sql.NullBool
		var sessStarted sql.NullString
		var sessRole, sessScopeKind, sessScopeID, sessActivity, sessActivitySource, sessWorktree, devCommand sql.NullString
		var worktreeIssue, worktreePath sql.NullString
		var ahead, behind, additions, deletions sql.NullInt64
		var uncommitted, hasConflicts sql.NullBool
		if err = rows.Scan(&t.ID, &t.Title, &t.Description, &t.Notes, &t.Design, &t.Acceptance, &t.Assignee, &labels, &estimate, &impls,
			&t.Status, &lifecycle, &review, &outcome, &archive, &t.Facts.WaitingHuman, &t.Facts.WaitingHumanSource, &t.Facts.WaitingHumanReason, &t.Facts.WaitingAI,
			&t.Priority, &t.Type, &parent, &hasTmux, &t.Origin, &pr.Number, &pr.RemoteKey, &pr.DisplayKey, &pr.URL, &pr.State, &prDraft, &pr.ChecksStatus,
			&ownerID, &ownerKind, &ownerClaimed, &ownerExpires, &leases, &blockers, &reasons, &runtimeUpdated, &created, &updated,
			&sessionIssue, &sessRole, &sessScopeKind, &sessScopeID, &sessState, &sessActivity, &sessActivitySource, &sessTotal, &sessActive, &sessPaused, &sessAttached, &sessAttachedCount, &sessStarted, &sessWorktree, &devPort, &devCommand, &devRunning, &sessUpdated,
			&worktreeIssue, &worktreePath, &ahead, &behind, &additions, &deletions, &uncommitted, &hasConflicts, &conflicts); err != nil {
			return nil, err
		}
		t.State, err = domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflow(lifecycle), Review: domain.IssueReviewState(review), CloseOutcome: domain.IssueCloseOutcome(outcome), Archive: domain.IssueArchiveState(archive)})
		if err != nil {
			return nil, fmt.Errorf("hydrate projected issue %s state: %w", t.ID, err)
		}
		t.Facts.LifecycleState, t.Facts.ReviewState, t.Facts.ClosedOutcome, t.Facts.ArchiveState = t.State.Workflow(), t.State.Review(), t.State.CloseOutcome(), t.State.Archive()
		t.Facts.BoardPhase, t.Facts.DisplayPhase, t.Facts.DisplayStatus = t.State.BoardPhase(), t.State.DisplayPhase(), t.State.DisplayPhase().FilterStatus()
		t.HasTmuxSession = hasTmux
		pr.Draft = prDraft
		if pr.Number != 0 || pr.URL != "" || pr.RemoteKey != "" {
			t.PullRequest = &pr
		}
		if estimate.Valid {
			value := int(estimate.Int64)
			t.Estimate = &value
		}
		if parent != "" {
			id := naming.IssueID(parent)
			t.ParentID = &id
		}
		if err = decodeJSON(labels, &t.Labels); err != nil {
			return nil, err
		}
		if err = decodeJSON(impls, &t.Implementations); err != nil {
			return nil, err
		}
		if err = decodeJSON(leases, &t.CoordinationLeases); err != nil {
			return nil, err
		}
		if err = decodeJSON(blockers, &t.Facts.OperationBlockers); err != nil {
			return nil, err
		}
		if err = decodeJSON(reasons, &t.Facts.Reasons); err != nil {
			return nil, err
		}
		t.Facts.DelegatedOperation = len(t.Facts.OperationBlockers) > 0
		if ownerID != "" {
			claimed, parseErr := parseTime(ownerClaimed.String)
			if parseErr != nil {
				return nil, parseErr
			}
			ownership := domain.IssueOwnership{OwnerID: ownerID, OwnerKind: ownerKind, ClaimedAt: claimed}
			if ownerExpires.Valid {
				expiry, e := parseTime(ownerExpires.String)
				if e != nil {
					return nil, e
				}
				ownership.ExpiresAt = &expiry
			}
			t.Ownership = &ownership
		}
		if runtimeUpdated.Valid {
			t.RuntimeUpdatedAt, err = parseTime(runtimeUpdated.String)
			if err != nil {
				return nil, err
			}
		}
		t.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		t.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, err
		}
		if sessionIssue.Valid {
			sess.IssueID = t.ID
			sess.Role = sessRole.String
			sess.ScopeKind = sessScopeKind.String
			sess.ScopeID = sessScopeID.String
			sess.State = domain.SessionState(sessState.String)
			sess.Activity = sessActivity.String
			sess.ActivitySource = sessActivitySource.String
			sess.TotalCount = int(sessTotal.Int64)
			sess.ActiveCount = int(sessActive.Int64)
			sess.PausedCount = int(sessPaused.Int64)
			sess.TmuxAttached = sessAttached.Bool
			sess.TmuxAttachedCount = int(sessAttachedCount.Int64)
			sess.Worktree = sessWorktree.String
			if sessStarted.Valid {
				value, e := parseTime(sessStarted.String)
				if e != nil {
					return nil, e
				}
				sess.StartedAt = &value
			}
			if sessUpdated.Valid {
				sess.UpdatedAt, err = parseTime(sessUpdated.String)
				if err != nil {
					return nil, err
				}
			}
			if devPort.Int64 != 0 || devCommand.String != "" {
				sess.DevServer = &domain.DevServer{Port: int(devPort.Int64), Command: devCommand.String, Running: devRunning.Bool}
			}
			t.Session = &sess
		}
		if worktreeIssue.Valid {
			t.HasWorktree = true
			t.GitAheadCount = int(ahead.Int64)
			t.GitBehindCount = int(behind.Int64)
			t.GitAdditions = int(additions.Int64)
			t.GitDeletions = int(deletions.Int64)
			t.HasUncommittedChanges = uncommitted.Bool
			t.HasConflicts = hasConflicts.Bool
			if err = decodeJSON(conflicts, &t.ConflictFiles); err != nil {
				return nil, err
			}
			if t.Session == nil && worktreePath.String != "" { /* path is represented by HasWorktree without inventing a session */
			}
		}
		// Session-derived facts are reconstructed from normalized rows while persisted
		// human-authority and operation facts remain authoritative materialized facts.
		var investigationAcceptance *domain.InvestigationAcceptance
		if t.Facts.WaitingHuman && t.Facts.WaitingHumanSource == domain.WaitingHumanSourceInvestigationAcceptance {
			investigationAcceptance = &domain.InvestigationAcceptance{Disposition: domain.InvestigationDispositionHumanFindings, Reason: t.Facts.WaitingHumanReason}
		}
		derived := domain.DeriveIssueFacts(domain.IssueFactsInput{Status: t.Status, Priority: t.Priority, Type: t.Type, State: t.State, Session: t.Session, HasTmuxSession: t.HasTmuxSession, OperationBlockers: t.Facts.OperationBlockers, DecisionWaiting: t.Facts.WaitingHuman && t.Facts.WaitingHumanSource == domain.WaitingHumanSourceInteractionRequest, DecisionWaitReason: t.Facts.WaitingHumanReason, InvestigationAcceptance: investigationAcceptance})
		derived.Reasons = t.Facts.Reasons
		t.Facts = derived
		out = append(out, t)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = s.attachDependencies(ctx, db, projectID, out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) != "" {
		out = domain.FilterTasksByContentQuery(out, query)
	}
	return out, nil
}

func decodeJSON(raw []byte, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode normalized projection JSON: %w", err)
	}
	return nil
}
func parseTime(raw string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse projection timestamp %q: %w", raw, err)
	}
	return value, nil
}

func (s *Store) attachDependencies(ctx context.Context, db queryer, projectID string, tasks []domain.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	byID := make(map[string]*domain.Task, len(tasks))
	for i := range tasks {
		byID[tasks[i].ID.String()] = &tasks[i]
	}
	rows, err := db.QueryContext(ctx, `SELECT issue_id,depends_on_issue_id,dependency_type FROM project_issue_dependency_projection WHERE project_id=? ORDER BY issue_id,depends_on_issue_id,dependency_type`, projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, dependencyID, dependencyType string
		if err = rows.Scan(&issueID, &dependencyID, &dependencyType); err != nil {
			return err
		}
		if task := byID[issueID]; task != nil {
			task.Dependencies = append(task.Dependencies, domain.Dependency{ID: naming.IssueID(dependencyID), Type: domain.DependencyType(dependencyType)})
		}
	}
	return rows.Err()
}

func viewCandidateSQL(view domain.BoardView) (string, []any) {
	view = view.Normalized()
	columnClauses := make([]string, 0, len(view.Columns))
	var args []any
	for _, column := range view.Columns {
		parts := make([]string, 0, len(column.Predicates))
		for _, predicate := range column.Predicates {
			clause, values := predicateSQL(predicate)
			if clause == "" {
				continue
			}
			parts = append(parts, clause)
			args = append(args, values...)
		}
		if len(parts) > 0 {
			columnClauses = append(columnClauses, "("+strings.Join(parts, " AND ")+")")
		}
	}
	columnSQL := strings.Join(columnClauses, " OR ")
	filterParts := make([]string, 0, len(view.Filters))
	filterArgs := []any{}
	for _, filter := range view.Filters {
		clause, values := predicateSQL(filter)
		if clause != "" {
			filterParts = append(filterParts, clause)
			filterArgs = append(filterArgs, values...)
		}
	}
	if len(filterParts) > 0 {
		filterSQL := "(" + strings.Join(filterParts, " AND ") + ")"
		if columnSQL != "" {
			return filterSQL + " AND (" + columnSQL + ")", append(filterArgs, args...)
		}
		return filterSQL, filterArgs
	}
	return columnSQL, args
}

func predicateSQL(predicate domain.BoardColumnPredicate) (string, []any) {
	predicate = predicate.Normalized()
	switch predicate.Kind {
	case domain.BoardPredicateLifecycle:
		return inSQL("lifecycle", stringValues(predicate.Lifecycle))
	case domain.BoardPredicateDisplayPhase:
		return inSQL("display_phase", stringValues(predicate.DisplayPhases))
	case domain.BoardPredicateClosedOutcome:
		return inSQL("closed_outcome", stringValues(predicate.ClosedOutcomes))
	case domain.BoardPredicateReviewReady:
		return "review_ready=1", nil
	case domain.BoardPredicateWaitingHuman:
		return "waiting_human=1", nil
	case domain.BoardPredicateWaitingAIDelegated:
		return "waiting_ai=1", nil
	default:
		return "", nil
	}
}

func stringValues[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
func inSQL(column string, values []string) (string, []any) {
	if len(values) == 0 {
		return "0", nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return column + " IN (" + marks + ")", args
}
