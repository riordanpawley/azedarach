package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

type dependencyRemovalConfirmationKey struct{}
type parentChildOrphanConfirmationKey struct{}
type issueMutationLockKey struct{}

const runtimeSessionProjectionUnionSQL = `
	SELECT
		project_id,
		session_id,
		issue_id,
		state,
		observed_state,
		activity,
		activity_source,
		tmux_attached_count,
		started_at,
		updated_at
	FROM daemon_session_projections
	UNION ALL
	SELECT
		project_id,
		session_id,
		issue_id,
		state,
		observed_state,
		activity,
		activity_source,
		tmux_attached_count,
		started_at,
		updated_at
	FROM daemon_session_observations
`

// ErrDependencyRemovalConfirmationRequired is returned when a removal that can
// unblock or retarget workflow is attempted without explicit confirmation.
var ErrDependencyRemovalConfirmationRequired = errors.New("explicit confirmation required")
var ErrParentChildOrphanConfirmationRequired = errors.New("parent-child removal would orphan active child; keep the parent-child hierarchy and use blocks/open status to pause or supersede child work, or pass explicit parent-orphan confirmation")
var ErrDeleteBlockedByRuntimeAttachments = errors.New("delete blocked: task has worktree or active session")
var ErrIssueHasLiveChildren = errors.New("issue has undeleted descendants")

type ParentChangeRequiredError struct {
	IssueID         string
	CurrentParent   string
	RequestedParent string
}

func (e ParentChangeRequiredError) Error() string {
	return fmt.Sprintf("refusing to change parent for %s: current parent %s, requested parent %s", e.IssueID, e.CurrentParent, e.RequestedParent)
}

type LiveChildrenMutationError struct {
	Operation       string
	IssueID         string
	DescendantCount int
}

func (e LiveChildrenMutationError) Error() string {
	descendantLabel := "descendants"
	if e.DescendantCount == 1 {
		descendantLabel = "descendant"
	}
	return fmt.Sprintf(
		"cannot %s issue %s: %d undeleted %s remain through active parent-child edges; use an explicit recursive cleanup or supersede workflow that handles every child edge and descendant issue",
		e.Operation,
		e.IssueID,
		e.DescendantCount,
		descendantLabel,
	)
}

func (e LiveChildrenMutationError) Is(target error) bool {
	return target == ErrIssueHasLiveChildren
}

// WithDependencyRemovalConfirmation marks a context as explicitly confirming a
// dependency removal that can unblock or retarget workflow.
func WithDependencyRemovalConfirmation(ctx context.Context) context.Context {
	return context.WithValue(ctx, dependencyRemovalConfirmationKey{}, true)
}

// WithParentChildOrphanConfirmation marks a context as explicitly confirming a
// parent-child removal that can move active child work to the root board.
func WithParentChildOrphanConfirmation(ctx context.Context) context.Context {
	return context.WithValue(ctx, parentChildOrphanConfirmationKey{}, true)
}

func hasDependencyRemovalConfirmation(ctx context.Context) bool {
	confirmed, _ := ctx.Value(dependencyRemovalConfirmationKey{}).(bool)
	return confirmed
}

func hasParentChildOrphanConfirmation(ctx context.Context) bool {
	confirmed, _ := ctx.Value(parentChildOrphanConfirmationKey{}).(bool)
	return confirmed
}

const (
	nextAlphaIssueIndexMetaKey = "issue:id_next_alpha_index"
	sqliteBusyPrimaryCode      = 5
	sqliteBusyRetryDelay       = 100 * time.Millisecond
	// Keep at least one foreground reader available while Linear sync owns a write connection.
	sqliteMaxOpenConns         = 4
	issueGraphClosureProjectID = "default"
)

// Client wraps local SQLite task store operations.
type Client struct {
	dbPath string
	logger *slog.Logger

	mu sync.Mutex
	db *sql.DB
}

type sqlIssueExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type sqlIssueQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type sqlIssueDBTX interface {
	sqlIssueExecer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var issueMutationLocks sync.Map

// WithMutationLock serializes issue-store writes that must not interleave with
// multi-step daemon side effects for the same SQLite database.
func (c *Client) WithMutationLock(ctx context.Context, fn func(context.Context) error) error {
	return c.withMutationLock(ctx, fn)
}

func (c *Client) withMutationLock(ctx context.Context, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if locked, _ := ctx.Value(issueMutationLockKey{}).(bool); locked {
		return fn(ctx)
	}
	lock := issueMutationLockForPath(c.dbPath)
	lock.Lock()
	defer lock.Unlock()
	return fn(context.WithValue(ctx, issueMutationLockKey{}, true))
}

func issueMutationLockForPath(dbPath string) *sync.Mutex {
	key := strings.TrimSpace(dbPath)
	if abs, err := filepath.Abs(key); err == nil {
		key = filepath.Clean(abs)
	}
	if key == "" {
		key = "."
	}
	value, _ := issueMutationLocks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// NewClient creates a SQLite-backed issue store client rooted at the repository.
func NewClient(repoDir string, logger *slog.Logger) *Client {
	dbPath, err := resolveDBPath(repoDir)
	if err != nil {
		// Keep daemon bootstrap non-fatal and surface DB errors on first operation.
		if logger != nil {
			logger.Warn("failed to resolve azedarach issue database path", "repoDir", repoDir, "error", err)
		}
		fallbackRoot := strings.TrimSpace(repoDir)
		if normalizedRoot, normalizeErr := config.ResolveProjectRoot(repoDir); normalizeErr == nil {
			fallbackRoot = normalizedRoot
		}
		if strings.TrimSpace(fallbackRoot) == "" {
			fallbackRoot = "."
		}
		dbPath = filepath.Join(fallbackRoot, ".azedarach", "azedarach.db")
	}
	return NewClientAtPath(dbPath, logger)
}

// NewClientAtPath creates a SQLite-backed issue store client for tests and explicit wiring.
func NewClientAtPath(dbPath string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		dbPath: dbPath,
		logger: logger,
	}
}

func (c *Client) dbHandle() (*sql.DB, error) {
	initStartedAt := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.db != nil {
		return c.db, nil
	}

	dbDir := filepath.Dir(c.dbPath)
	if dbDir != "" && dbDir != "." {
		if err := os.MkdirAll(dbDir, 0o755); err != nil {
			return nil, c.wrapError("open-db", "", fmt.Errorf("create db directory: %w", err))
		}
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		filepath.ToSlash(c.dbPath),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, c.wrapError("open-db", "", err)
	}

	db.SetMaxOpenConns(sqliteMaxOpenConns)
	db.SetMaxIdleConns(sqliteMaxOpenConns)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	pingDoneAt := time.Now()
	if err := c.configureSQLite(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	configDoneAt := time.Now()
	if err := c.runMigrations(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	migrationsDoneAt := time.Now()
	if err := c.normalizeDependencyEnumRows(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	normalizeDoneAt := time.Now()
	if err := c.ensureSpecSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	specSchemaDoneAt := time.Now()
	if err := c.ensureSpecAuditSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := c.ensureDecisionSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := c.ensureDecisionAuditSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := c.ensureRuntimeProjectionSchema(db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	if err := c.normalizeProviderDisplayKeyIssueIDs(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, c.wrapError("open-db", "", err)
	}
	specAuditDoneAt := time.Now()

	c.db = db
	if c.logger != nil {
		c.logger.Info(
			"issue store init timings",
			"db_path", c.dbPath,
			"total_ms", specAuditDoneAt.Sub(initStartedAt).Milliseconds(),
			"ping_ms", pingDoneAt.Sub(initStartedAt).Milliseconds(),
			"configure_sqlite_ms", configDoneAt.Sub(pingDoneAt).Milliseconds(),
			"migrations_ms", migrationsDoneAt.Sub(configDoneAt).Milliseconds(),
			"normalize_dependency_rows_ms", normalizeDoneAt.Sub(migrationsDoneAt).Milliseconds(),
			"ensure_spec_schema_ms", specSchemaDoneAt.Sub(normalizeDoneAt).Milliseconds(),
			"ensure_spec_audit_schema_ms", specAuditDoneAt.Sub(specSchemaDoneAt).Milliseconds(),
		)
	}
	return c.db, nil
}

func (c *Client) ensureRuntimeProjectionSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS daemon_session_projections (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			activity TEXT,
			activity_source TEXT,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
			ON daemon_session_projections (project_id, issue_id)`,
		`CREATE TABLE IF NOT EXISTS daemon_session_observations (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			observed_state TEXT,
			activity TEXT,
			activity_source TEXT,
			tmux_attached_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_observations_project_issue
			ON daemon_session_observations (project_id, issue_id)`,
		`CREATE TABLE IF NOT EXISTS daemon_worktree_projections (
			project_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			path TEXT NOT NULL,
			branch TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			git_status_json TEXT,
			git_status_updated_at TEXT,
			PRIMARY KEY (project_id, issue_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_worktree_projections_project_path
			ON daemon_worktree_projections (project_id, path)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure runtime projection schema: %w", err)
		}
	}
	if err := ensureSQLiteColumn(db, "daemon_session_projections", "tmux_attached_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	if err := ensureSQLiteColumn(db, "daemon_session_projections", "observed_state", "TEXT"); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	if err := ensureSQLiteColumn(db, "daemon_session_projections", "activity", "TEXT"); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	if err := ensureSQLiteColumn(db, "daemon_session_projections", "activity_source", "TEXT"); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{"started_at", "TEXT"},
		{"observed_state", "TEXT"},
		{"activity", "TEXT"},
		{"activity_source", "TEXT"},
		{"tmux_attached_count", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureSQLiteColumn(db, "daemon_session_observations", column.name, column.ddl); err != nil {
			return fmt.Errorf("ensure runtime projection schema: %w", err)
		}
	}
	if err := migrateRuntimeSessionObservations(db); err != nil {
		return fmt.Errorf("ensure runtime projection schema: %w", err)
	}
	return nil
}

func migrateRuntimeSessionObservations(db *sql.DB) error {
	if _, err := db.Exec(`
		INSERT INTO daemon_session_observations (
			project_id,
			session_id,
			issue_id,
			state,
			observed_state,
			activity,
			activity_source,
			tmux_attached_count,
			started_at,
			updated_at
		)
		SELECT
			project_id,
			session_id,
			issue_id,
			state,
			observed_state,
			activity,
			activity_source,
			COALESCE(tmux_attached_count, 0),
			started_at,
			updated_at
		FROM daemon_session_projections
		WHERE instr(session_id, '.pane-') > 0
		ON CONFLICT(project_id, session_id) DO UPDATE SET
			issue_id = excluded.issue_id,
			state = excluded.state,
			observed_state = excluded.observed_state,
			activity = excluded.activity,
			activity_source = excluded.activity_source,
			tmux_attached_count = excluded.tmux_attached_count,
			started_at = excluded.started_at,
			updated_at = excluded.updated_at
	`); err != nil {
		return fmt.Errorf("migrate session observations: copy pane rows: %w", err)
	}
	if _, err := db.Exec(`
		DELETE FROM daemon_session_projections
		WHERE instr(session_id, '.pane-') > 0
	`); err != nil {
		return fmt.Errorf("migrate session observations: delete pane rows from intent table: %w", err)
	}
	return nil
}

func ensureSQLiteColumn(db *sql.DB, tableName, columnName, columnDDL string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, columnName) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, columnDDL))
	return err
}

func (c *Client) CloseDB() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	if err != nil {
		return c.wrapError("close-db", "", err)
	}
	return nil
}

func (c *Client) DBStats() (sql.DBStats, error) {
	db, err := c.dbHandle()
	if err != nil {
		return sql.DBStats{}, err
	}
	return db.Stats(), nil
}

func (c *Client) configureSQLite(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("apply %s: %w", pragma, err)
		}
	}
	return nil
}

func (c *Client) normalizeDependencyEnumRows(db *sql.DB) error {
	res, err := db.Exec(`
		UPDATE issue_dependencies
		SET dependency_type = CASE
			WHEN dependency_type = 'parent_child' THEN 'parent-child'
			WHEN dependency_type = 'blocked_by' THEN 'blocked-by'
			WHEN dependency_type = 'related_to' THEN 'related'
			WHEN dependency_type = 'discovered_from' THEN 'discovered-from'
			ELSE dependency_type
		END
		WHERE dependency_type = 'parent_child'
			OR dependency_type = 'blocked_by'
			OR dependency_type = 'related_to'
			OR dependency_type = 'discovered_from'
	`)
	if err != nil {
		return fmt.Errorf("normalize dependency enum rows: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected > 0 {
		if err := c.rebuildIssueGraphClosure(context.Background(), db); err != nil {
			return fmt.Errorf("rebuild graph closure after dependency enum normalization: %w", err)
		}
	}
	return nil
}

type issueIDMigration struct {
	OldID         string
	NewID         string
	Provider      string
	ProviderScope string
	RemoteKey     string
	DisplayKey    string
}

func (c *Client) normalizeProviderDisplayKeyIssueIDs(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT id
		FROM issues
		WHERE deleted_at IS NULL
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("list issues for provider display-key normalization: %w", err)
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	legacyIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan issue id for provider display-key normalization: %w", err)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		existing[id] = struct{}{}
		if isLinearDisplayKeyIssueID(id) {
			legacyIDs = append(legacyIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate issue ids for provider display-key normalization: %w", err)
	}
	if len(legacyIDs) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider display-key normalization: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	nextIndex := 0
	if raw, err := c.getMetaValue(ctx, tx, nextAlphaIssueIndexMetaKey); err == nil {
		nextIndex = parseNextAlphaIssueIndex(raw)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read next alpha id for provider display-key normalization: %w", err)
	}

	migrations := make([]issueIDMigration, 0, len(legacyIDs))
	for _, oldID := range legacyIDs {
		newID, nextReserved := allocateNextAlphaIssueID(nextIndex, existing)
		nextIndex = nextReserved
		existing[newID] = struct{}{}
		prefix := strings.TrimSpace(strings.SplitN(oldID, "-", 2)[0])
		migrations = append(migrations, issueIDMigration{
			OldID:         oldID,
			NewID:         newID,
			Provider:      "linear",
			ProviderScope: "team:" + prefix,
			RemoteKey:     oldID,
			DisplayKey:    oldID,
		})
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, migration := range migrations {
		if err := c.migrateProviderDisplayKeyIssueID(ctx, tx, migration, now); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, nextAlphaIssueIndexMetaKey, strconv.Itoa(nextIndex)); err != nil {
		return fmt.Errorf("persist next alpha id after provider display-key normalization: %w", err)
	}
	if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
		return fmt.Errorf("rebuild graph closure after provider display-key normalization: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider display-key normalization: %w", err)
	}
	tx = nil
	if c.logger != nil {
		c.logger.Info("normalized provider display-key issue ids", "count", len(migrations))
	}
	return nil
}

func (c *Client) migrateProviderDisplayKeyIssueID(ctx context.Context, tx *sql.Tx, migration issueIDMigration, now string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issues (
			id,
			title,
			description,
			status,
			priority,
			issue_type,
			created_at,
			updated_at,
			closed_at,
			assignee,
			labels_json,
			implementations_json,
			design,
			notes,
			acceptance,
			estimate,
			deleted_at
		)
		SELECT
			?,
			title,
			description,
			status,
			priority,
			issue_type,
			created_at,
			?,
			closed_at,
			assignee,
			labels_json,
			implementations_json,
			design,
			notes,
			acceptance,
			estimate,
			deleted_at
		FROM issues
		WHERE id = ?
	`, migration.NewID, now, migration.OldID); err != nil {
		return fmt.Errorf("copy provider display-key issue %s to %s: %w", migration.OldID, migration.NewID, err)
	}

	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE issue_dependencies SET issue_id = ? WHERE issue_id = ?`, []any{migration.NewID, migration.OldID}},
		{`UPDATE issue_dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, []any{migration.NewID, migration.OldID}},
		{`UPDATE spec_requirements SET issue_id = ? WHERE issue_id = ?`, []any{migration.NewID, migration.OldID}},
		{`UPDATE spec_links SET issue_id = ? WHERE issue_id = ?`, []any{migration.NewID, migration.OldID}},
		{`UPDATE issue_external_refs SET issue_id = ?, updated_at = ? WHERE issue_id = ?`, []any{migration.NewID, now, migration.OldID}},
		{`UPDATE daemon_session_projections SET issue_id = ?, updated_at = ? WHERE issue_id = ?`, []any{migration.NewID, now, migration.OldID}},
		{`UPDATE daemon_session_observations SET issue_id = ?, updated_at = ? WHERE issue_id = ?`, []any{migration.NewID, now, migration.OldID}},
		{`UPDATE daemon_worktree_projections SET issue_id = ?, updated_at = ? WHERE issue_id = ?`, []any{migration.NewID, now, migration.OldID}},
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			return fmt.Errorf("rewrite references from provider display-key issue %s to %s: %w", migration.OldID, migration.NewID, err)
		}
	}

	metadataJSON, err := marshalStringMap(map[string]string{
		"migrated_from_issue_id": migration.OldID,
		"migration":              "provider-display-key-issue-id",
	})
	if err != nil {
		return fmt.Errorf("marshal provider display-key migration metadata %s: %w", migration.OldID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_external_refs (
			issue_id,
			provider,
			provider_scope,
			remote_key,
			display_key,
			url,
			metadata_json,
			created_at,
			updated_at
		)
		SELECT ?, ?, ?, ?, ?, NULL, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1
			FROM issue_external_refs
			WHERE provider = ?
				AND provider_scope = ?
				AND remote_key = ?
				AND deleted_at IS NULL
		)
	`, migration.NewID, migration.Provider, migration.ProviderScope, migration.RemoteKey, migration.DisplayKey, metadataJSON, now, now, migration.Provider, migration.ProviderScope, migration.RemoteKey); err != nil {
		return fmt.Errorf("record external ref for provider display-key issue %s: %w", migration.OldID, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, migration.OldID); err != nil {
		return fmt.Errorf("delete provider display-key issue %s after migration to %s: %w", migration.OldID, migration.NewID, err)
	}
	return nil
}

func isLinearDisplayKeyIssueID(id string) bool {
	id = strings.TrimSpace(id)
	prefix, numeric, ok := strings.Cut(id, "-")
	if !ok || prefix == "" || numeric == "" {
		return false
	}
	for _, r := range prefix {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	for _, r := range numeric {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// List fetches all active issues from local SQLite store.
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	tasks, err := c.queryTasks(ctx, db, `
		SELECT
			id,
			title,
			COALESCE(description, ''),
			COALESCE(notes, ''),
			COALESCE(design, ''),
			COALESCE(acceptance, ''),
			COALESCE(assignee, ''),
			COALESCE(labels_json, '[]'),
			estimate,
			status,
			priority,
			issue_type,
			COALESCE(implementations_json, '[]'),
			created_at,
			updated_at
		FROM issues
		WHERE deleted_at IS NULL
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, c.wrapError("list", "", err)
	}
	return tasks, nil
}

// ListWithRuntime fetches active issues and runtime projection fields using a single joined SQLite query.
func (c *Client) ListWithRuntime(ctx context.Context, projectID string) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	tasks, err := c.queryTasksWithRuntime(ctx, db, projectID)
	if err != nil {
		return nil, c.wrapError("list-with-runtime", projectID, err)
	}
	return tasks, nil
}

// SearchWithRuntime fetches active issues matching query through the issue
// content FTS index, then hydrates only the matching runtime rows.
func (c *Client) SearchWithRuntime(ctx context.Context, projectID, query string) ([]domain.Task, error) {
	startedAt := time.Now()
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	expr := domain.ContentQueryFTSExpression(query)
	if expr == "" {
		return []domain.Task{}, nil
	}
	rows, err := db.QueryContext(ctx, issueSearchIDsQuery(), expr)
	if err != nil {
		c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, 0, err)
		return nil, c.wrapError("search-with-runtime", projectID, err)
	}

	ids := make([]string, 0, 32)
	seen := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, len(ids), err)
			return nil, c.wrapError("search-with-runtime", projectID, err)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, len(ids), err)
		return nil, c.wrapError("search-with-runtime", projectID, err)
	}
	if err := rows.Close(); err != nil {
		c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, len(ids), err)
		return nil, c.wrapError("search-with-runtime", projectID, err)
	}
	c.logSQLiteRead(ctx, "issue.search_ids_fts", startedAt, len(ids), nil)
	if len(ids) == 0 {
		return []domain.Task{}, nil
	}

	tasks, err := c.queryTasksWithRuntime(ctx, db, projectID, ids...)
	if err != nil {
		return nil, c.wrapError("search-with-runtime", projectID, err)
	}
	return domain.FilterTasksByContentQuery(tasks, query), nil
}

func issueSearchIDsQuery() string {
	return `
		SELECT i.id
		FROM issue_search_fts
		JOIN issues i ON i.rowid = issue_search_fts.rowid
		WHERE issue_search_fts MATCH ?
			AND i.deleted_at IS NULL
		ORDER BY i.updated_at DESC, i.id
	`
}

// ListSummariesWithRuntime fetches active issues with runtime projection fields
// but without long-form detail text. It is intended for board/list snapshots
// where fetching full issue bodies for every task dominates load time.
func (c *Client) ListSummariesWithRuntime(ctx context.Context, projectID string) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	tasks, err := c.queryTaskSummariesWithRuntime(ctx, db, projectID)
	if err != nil {
		return nil, c.wrapError("list-summaries-with-runtime", projectID, err)
	}
	return tasks, nil
}

// ListGraphReadinessWithRuntime fetches the root graph plus direct dependency
// context needed by daemon graph-readiness checks. The read scales with the
// requested root graph instead of all active issues in the project.
func (c *Client) ListGraphReadinessWithRuntime(ctx context.Context, projectID, rootID string) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return []domain.Task{}, nil
	}
	issueIDs, err := c.graphReadinessContextIDs(ctx, db, rootID)
	if err != nil {
		return nil, c.wrapError("list-graph-readiness-with-runtime", rootID, err)
	}
	if len(issueIDs) == 0 {
		return []domain.Task{}, nil
	}
	tasks, err := c.queryTasksWithRuntimeProjection(ctx, db, projectID, false, issueIDs...)
	if err != nil {
		return nil, c.wrapError("list-graph-readiness-with-runtime", rootID, err)
	}
	return tasks, nil
}

// GetWithRuntime fetches one active issue with runtime projection fields.
func (c *Client) GetWithRuntime(ctx context.Context, projectID, id string) (domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.Task{}, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.Task{}, c.wrapError("get-with-runtime", id, domain.ErrNotFound)
	}
	tasks, err := c.queryTasksWithRuntime(ctx, db, projectID, id)
	if err != nil {
		return domain.Task{}, c.wrapError("get-with-runtime", id, err)
	}
	if len(tasks) == 0 {
		return domain.Task{}, c.wrapError("get-with-runtime", id, domain.ErrNotFound)
	}
	return tasks[0], nil
}

// GetManyWithRuntime fetches active issues by ID with runtime projection fields.
func (c *Client) GetManyWithRuntime(ctx context.Context, projectID string, ids []string) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	issueIDs := uniqueIssueIDStrings(ids)
	if len(issueIDs) == 0 {
		return []domain.Task{}, nil
	}
	tasks, err := c.queryTasksWithRuntime(ctx, db, projectID, issueIDs...)
	if err != nil {
		return nil, c.wrapError("get-many-with-runtime", strings.Join(issueIDs, ","), err)
	}
	return tasks, nil
}

// GetWithDependencyContextRuntime fetches one issue plus direct dependencies and dependents.
func (c *Client) GetWithDependencyContextRuntime(ctx context.Context, projectID, id string) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, c.wrapError("get-with-dependency-context-runtime", id, domain.ErrNotFound)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT id
		FROM (
			SELECT ? AS id
			UNION ALL
			SELECT depends_on_id AS id
			FROM issue_dependencies
			WHERE issue_id = ? AND tombstoned_at IS NULL
			UNION ALL
			SELECT issue_id AS id
			FROM issue_dependencies
			WHERE depends_on_id = ? AND tombstoned_at IS NULL
		)
	`, id, id, id)
	if err != nil {
		return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
	}

	issueIDs := make([]string, 0, 8)
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			_ = rows.Close()
			return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
		}
		if strings.TrimSpace(issueID) != "" {
			issueIDs = append(issueIDs, issueID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
	}
	if err := rows.Close(); err != nil {
		return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
	}

	tasks, err := c.queryTasksWithRuntime(ctx, db, projectID, issueIDs...)
	if err != nil {
		return nil, c.wrapError("get-with-dependency-context-runtime", id, err)
	}
	for _, task := range tasks {
		if task.ID.String() == id {
			return tasks, nil
		}
	}
	return nil, c.wrapError("get-with-dependency-context-runtime", id, domain.ErrNotFound)
}

type dependencyContextOptions struct {
	includeAncestors  bool
	includeDependents bool
}

// DependencyContextOption configures task dependency context reads.
type DependencyContextOption func(*dependencyContextOptions)

// WithAncestorContext includes the full parent-child ancestor chain for each requested issue.
func WithAncestorContext() DependencyContextOption {
	return func(opts *dependencyContextOptions) {
		opts.includeAncestors = true
	}
}

// WithoutDependentContext omits direct dependents from dependency context reads.
func WithoutDependentContext() DependencyContextOption {
	return func(opts *dependencyContextOptions) {
		opts.includeDependents = false
	}
}

// GetManyWithDependencyContextRuntime fetches issues plus direct dependencies and dependents.
func (c *Client) GetManyWithDependencyContextRuntime(ctx context.Context, projectID string, ids []string, options ...DependencyContextOption) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	opts := dependencyContextOptions{includeDependents: true}
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}

	seen := map[string]struct{}{}
	issueIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		issueIDs = append(issueIDs, id)
	}
	if len(issueIDs) == 0 {
		return nil, c.wrapError("get-many-with-dependency-context-runtime", "", domain.ErrNotFound)
	}

	query, args := dependencyContextIDsQuery(issueIDs, opts)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
	}

	contextIDs := make([]string, 0, len(issueIDs)*2)
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			_ = rows.Close()
			return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
		}
		if strings.TrimSpace(issueID) != "" {
			contextIDs = append(contextIDs, issueID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
	}
	if err := rows.Close(); err != nil {
		return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
	}
	if opts.includeAncestors {
		ancestorIDs, err := c.parentAncestorIDs(ctx, db, issueIDs)
		if err != nil {
			return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
		}
		contextIDs = append(contextIDs, ancestorIDs...)
	}
	if len(contextIDs) == 0 {
		return []domain.Task{}, nil
	}

	tasks, err := c.queryTasksWithRuntime(ctx, db, projectID, contextIDs...)
	if err != nil {
		return nil, c.wrapError("get-many-with-dependency-context-runtime", strings.Join(issueIDs, ","), err)
	}
	return tasks, nil
}

func dependencyContextIDsQuery(ids []string, opts dependencyContextOptions) (string, []any) {
	issueIDs := uniqueIssueIDStrings(ids)
	if len(issueIDs) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")
	dependentQuery := ""
	if opts.includeDependents {
		dependentQuery = fmt.Sprintf(`
			UNION ALL
			SELECT issue_id AS id
			FROM issue_dependencies
			WHERE depends_on_id IN (%s) AND tombstoned_at IS NULL
		`, placeholders)
	}
	query := fmt.Sprintf(`
		SELECT DISTINCT id
		FROM (
			SELECT id
			FROM issues
			WHERE deleted_at IS NULL AND id IN (%s)
			UNION ALL
			SELECT depends_on_id AS id
			FROM issue_dependencies
			WHERE issue_id IN (%s) AND tombstoned_at IS NULL
			%s
		)
	`, placeholders, placeholders, dependentQuery)
	args := make([]any, 0, len(issueIDs)*3)
	for _, issueID := range issueIDs {
		args = append(args, issueID)
	}
	for _, issueID := range issueIDs {
		args = append(args, issueID)
	}
	if opts.includeDependents {
		for _, issueID := range issueIDs {
			args = append(args, issueID)
		}
	}
	return query, args
}

func (c *Client) graphReadinessContextIDs(ctx context.Context, db *sql.DB, rootID string) ([]string, error) {
	startedAt := time.Now()
	query, args := graphReadinessContextIDsQuery(rootID)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		c.logSQLiteRead(ctx, "issue.graph_readiness_context_ids", startedAt, 0, err)
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			c.logSQLiteRead(ctx, "issue.graph_readiness_context_ids", startedAt, len(ids), err)
			return nil, err
		}
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		c.logSQLiteRead(ctx, "issue.graph_readiness_context_ids", startedAt, len(ids), err)
		return nil, err
	}
	c.logSQLiteRead(ctx, "issue.graph_readiness_context_ids", startedAt, len(ids), nil)
	return ids, nil
}

func graphReadinessContextIDsQuery(rootID string) (string, []any) {
	query := `
		WITH graph(id) AS (
			SELECT id
			FROM issues
			WHERE id = ? AND deleted_at IS NULL

			UNION

			SELECT closure.descendant_id
			FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
			INNER JOIN issues child
				ON child.id = closure.descendant_id
				AND child.deleted_at IS NULL
			WHERE closure.project_id = ?
				AND closure.dependency_type = ?
				AND closure.ancestor_id = ?
		),
		context(id) AS (
			SELECT id FROM graph

			UNION

			SELECT dep.depends_on_id
			FROM graph graph_issue
			CROSS JOIN issue_dependencies dep INDEXED BY idx_dependencies_issue_active_type
			CROSS JOIN issues dep_issue
			WHERE dep.issue_id = graph_issue.id
				AND dep_issue.id = dep.depends_on_id
				AND dep_issue.deleted_at IS NULL
				AND dep.tombstoned_at IS NULL
		)
		SELECT id
		FROM context
	`
	return query, []any{
		strings.TrimSpace(rootID),
		issueGraphClosureProjectID,
		string(domain.DependencyParentChild),
		strings.TrimSpace(rootID),
	}
}

// GetManyMetadataWithRuntime fetches lightweight issue metadata plus stored runtime projection fields.
func (c *Client) GetManyMetadataWithRuntime(ctx context.Context, projectID string, ids []string) ([]domain.Task, error) {
	return c.getManyMetadataWithRuntime(ctx, projectID, ids, false)
}

// GetManyMetadataWithAncestorContextRuntime fetches lightweight issue metadata plus parent ancestor context.
func (c *Client) GetManyMetadataWithAncestorContextRuntime(ctx context.Context, projectID string, ids []string) ([]domain.Task, error) {
	return c.getManyMetadataWithRuntime(ctx, projectID, ids, true)
}

func (c *Client) getManyMetadataWithRuntime(ctx context.Context, projectID string, ids []string, includeAncestors bool) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = "default"
	}
	issueIDs := uniqueIssueIDStrings(ids)
	if len(issueIDs) == 0 {
		return nil, c.wrapError("get-many-metadata-runtime", "", domain.ErrNotFound)
	}
	contextIDs := append([]string(nil), issueIDs...)
	if includeAncestors {
		ancestorIDs, err := c.parentAncestorIDs(ctx, db, issueIDs)
		if err != nil {
			return nil, c.wrapError("get-many-metadata-runtime", strings.Join(issueIDs, ","), err)
		}
		contextIDs = uniqueIssueIDStrings(append(contextIDs, ancestorIDs...))
	}
	tasks, err := c.queryTaskMetadataWithRuntime(ctx, db, projectID, contextIDs...)
	if err != nil {
		return nil, c.wrapError("get-many-metadata-runtime", strings.Join(issueIDs, ","), err)
	}
	return tasks, nil
}

func (c *Client) parentAncestorIDs(ctx context.Context, db *sql.DB, issueIDs []string) ([]string, error) {
	seen := map[string]struct{}{}
	seeds := make([]string, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		if _, ok := seen[issueID]; ok {
			continue
		}
		seen[issueID] = struct{}{}
		seeds = append(seeds, issueID)
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	query, args := parentAncestorIDsQuery(seeds)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ancestors := make([]string, 0, len(seeds))
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(issueID) != "" {
			ancestors = append(ancestors, issueID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ancestors, nil
}

func parentAncestorIDsQuery(issueIDs []string) (string, []any) {
	seeds := uniqueIssueIDStrings(issueIDs)
	if len(seeds) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(seeds)), ",")
	query := fmt.Sprintf(`
		SELECT DISTINCT closure.ancestor_id
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_descendant
		JOIN issues i ON i.id = closure.ancestor_id
		WHERE i.deleted_at IS NULL
			AND closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.descendant_id IN (%s)
	`, placeholders)
	args := make([]any, 0, len(seeds)+2)
	args = append(args, issueGraphClosureProjectID, string(domain.DependencyParentChild))
	for _, seed := range seeds {
		args = append(args, seed)
	}
	return query, args
}

// ListGraphDescendantIDs returns active descendant issue IDs for an ancestor in
// the materialized issue graph closure projection.
func (c *Client) ListGraphDescendantIDs(ctx context.Context, ancestorID, dependencyType string) ([]string, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	canonicalType, err := canonicalGraphClosureDependencyType(dependencyType)
	if err != nil {
		return nil, c.wrapError("list-graph-descendants", ancestorID, err)
	}
	ids, err := c.listGraphDescendantIDs(ctx, db, strings.TrimSpace(ancestorID), canonicalType)
	if err != nil {
		return nil, c.wrapError("list-graph-descendants", ancestorID, err)
	}
	return ids, nil
}

// ListGraphAncestorIDs returns active ancestor issue IDs for a descendant in
// the materialized issue graph closure projection.
func (c *Client) ListGraphAncestorIDs(ctx context.Context, descendantID, dependencyType string) ([]string, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	canonicalType, err := canonicalGraphClosureDependencyType(dependencyType)
	if err != nil {
		return nil, c.wrapError("list-graph-ancestors", descendantID, err)
	}
	ids, err := c.listGraphAncestorIDs(ctx, db, strings.TrimSpace(descendantID), canonicalType)
	if err != nil {
		return nil, c.wrapError("list-graph-ancestors", descendantID, err)
	}
	return ids, nil
}

func canonicalGraphClosureDependencyType(value string) (string, error) {
	canonicalType, err := canonicalDependencyType(value)
	if err != nil {
		return "", err
	}
	if canonicalType != string(domain.DependencyParentChild) {
		return "", fmt.Errorf("unsupported graph closure dependency type %q", strings.TrimSpace(value))
	}
	return canonicalType, nil
}

func (c *Client) listGraphDescendantIDs(ctx context.Context, queryer sqlIssueDBTX, ancestorID, dependencyType string) ([]string, error) {
	if strings.TrimSpace(ancestorID) == "" {
		return []string{}, nil
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT closure.descendant_id
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
		INNER JOIN issues descendant
			ON descendant.id = closure.descendant_id
			AND descendant.deleted_at IS NULL
		WHERE closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.ancestor_id = ?
		ORDER BY closure.depth, closure.descendant_id
	`, issueGraphClosureProjectID, dependencyType, ancestorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssueIDRows(rows)
}

func (c *Client) listGraphAncestorIDs(ctx context.Context, queryer sqlIssueDBTX, descendantID, dependencyType string) ([]string, error) {
	if strings.TrimSpace(descendantID) == "" {
		return []string{}, nil
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT closure.ancestor_id
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_descendant
		INNER JOIN issues ancestor
			ON ancestor.id = closure.ancestor_id
			AND ancestor.deleted_at IS NULL
		WHERE closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.descendant_id = ?
		ORDER BY closure.depth, closure.ancestor_id
	`, issueGraphClosureProjectID, dependencyType, descendantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssueIDRows(rows)
}

func scanIssueIDRows(rows *sql.Rows) ([]string, error) {
	ids := []string{}
	for rows.Next() {
		var issueID string
		if err := rows.Scan(&issueID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(issueID) != "" {
			ids = append(ids, issueID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func uniqueIssueIDStrings(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// Search queries issues by id/title/description.
func (c *Client) Search(ctx context.Context, query string) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return c.List(ctx)
	}

	like := "%" + q + "%"
	tasks, err := c.queryTasks(ctx, db, `
		SELECT
			id,
			title,
			COALESCE(description, ''),
			COALESCE(notes, ''),
			COALESCE(design, ''),
			COALESCE(acceptance, ''),
			COALESCE(assignee, ''),
			COALESCE(labels_json, '[]'),
			estimate,
			status,
			priority,
			issue_type,
			COALESCE(implementations_json, '[]'),
			created_at,
			updated_at
		FROM issues
		WHERE
			deleted_at IS NULL
			AND (id LIKE ? OR title LIKE ? OR description LIKE ?)
		ORDER BY updated_at DESC
		LIMIT 200
	`, like, like, like)
	if err != nil {
		return nil, c.wrapError("search", query, err)
	}
	return tasks, nil
}

// Ready fetches open tasks that do not have unresolved blockers.
func (c *Client) Ready(ctx context.Context) ([]domain.Task, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	tasks, err := c.queryTasks(ctx, db, `
		SELECT
			i.id,
			i.title,
			COALESCE(i.description, ''),
			COALESCE(i.notes, ''),
			COALESCE(i.design, ''),
			COALESCE(i.acceptance, ''),
			COALESCE(i.assignee, ''),
			COALESCE(i.labels_json, '[]'),
			i.estimate,
			i.status,
			i.priority,
			i.issue_type,
			COALESCE(i.implementations_json, '[]'),
			i.created_at,
			i.updated_at
		FROM issues i
		WHERE
			i.deleted_at IS NULL
			AND i.status = 'open'
			AND NOT EXISTS (
				SELECT 1
				FROM issue_dependencies d
				JOIN issues dep ON dep.id = d.depends_on_id
				WHERE
					d.issue_id = i.id
					AND d.tombstoned_at IS NULL
					AND d.dependency_type = 'blocks'
					AND dep.deleted_at IS NULL
					AND dep.status != 'closed'
			)
		ORDER BY i.priority ASC, i.updated_at DESC
	`)
	if err != nil {
		return nil, c.wrapError("ready", "", err)
	}
	return tasks, nil
}

// Update changes an issue status.
func (c *Client) Update(ctx context.Context, id string, status domain.Status) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	if status == domain.StatusDone {
		openChildCount, err := c.countOpenChildren(ctx, db, id)
		if err != nil {
			return c.wrapError("update", id, err)
		}
		if openChildCount > 0 {
			return c.wrapError("update", id, fmt.Errorf("%w: cannot close parent issue with %d open child issue(s)", domain.ErrConflict, openChildCount))
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var closedAt *string
	if status == domain.StatusDone {
		closedAt = &now
	}
	res, err := db.ExecContext(ctx, `
		UPDATE issues
		SET
			status = ?,
			updated_at = ?,
			closed_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, string(status), now, closedAt, id)
	if err != nil {
		return c.wrapError("update", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("update", id, domain.ErrNotFound)
	}
	return nil
}

// UpdateWithRuntime changes an issue status and returns the changed issue.
func (c *Client) UpdateWithRuntime(ctx context.Context, projectID, id string, status domain.Status) (domain.Task, error) {
	if err := c.Update(ctx, id, status); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

func (c *Client) countOpenChildren(ctx context.Context, db *sql.DB, parentID string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM issue_dependencies d
		JOIN issues child ON child.id = d.issue_id
		WHERE
			d.depends_on_id = ?
			AND d.tombstoned_at IS NULL
			AND d.dependency_type IN ('parent-child', 'parent_child')
			AND child.deleted_at IS NULL
			AND child.status != 'closed'
	`, parentID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CreateTaskParams contains parameters for creating a new issue.
type CreateTaskParams struct {
	Title           string
	Description     string
	Type            domain.TaskType
	Priority        domain.Priority
	Status          domain.Status
	Assignee        string
	Labels          []string
	Implementations []string
	Design          string
	Notes           string
	Acceptance      string
	Estimate        *int
	ParentID        *string
}

type UpsertExternalIssueRefParams struct {
	IssueID       string
	Provider      string
	ProviderScope string
	RemoteKey     string
	DisplayKey    string
	URL           string
	Metadata      map[string]string
}

// Create inserts a new issue and returns its generated id.
func (c *Client) Create(ctx context.Context, params CreateTaskParams) (string, error) {
	var issueID string
	err := retrySQLiteBusy(ctx, func() error {
		var err error
		issueID, err = c.createOnce(ctx, params)
		return err
	})
	if err != nil {
		return "", err
	}
	return issueID, nil
}

func (c *Client) createOnce(ctx context.Context, params CreateTaskParams) (string, error) {
	var issueID string
	err := c.withMutationLock(ctx, func(ctx context.Context) error {
		return sqliteutil.WithWriteLock(c.dbPath, func() error {
			var err error
			issueID, err = c.createOnceLocked(ctx, params)
			return err
		})
	})
	return issueID, err
}

func (c *Client) createOnceLocked(ctx context.Context, params CreateTaskParams) (string, error) {
	db, err := c.dbHandle()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(params.Title) == "" {
		return "", c.wrapError("create", "", errors.New("title is required"))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", c.wrapError("create", "", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	nextIndex := 0
	if raw, err := c.getMetaValue(ctx, tx, nextAlphaIssueIndexMetaKey); err == nil {
		nextIndex = parseNextAlphaIssueIndex(raw)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", c.wrapError("create", "", err)
	}

	existingRows, err := tx.QueryContext(ctx, `SELECT id FROM issues`)
	if err != nil {
		return "", c.wrapError("create", "", err)
	}
	defer existingRows.Close()
	existing := map[string]struct{}{}
	for existingRows.Next() {
		var id string
		if err := existingRows.Scan(&id); err != nil {
			return "", c.wrapError("create", "", err)
		}
		existing[id] = struct{}{}
	}
	if err := existingRows.Err(); err != nil {
		return "", c.wrapError("create", "", err)
	}

	issueID, nextReserved := allocateNextAlphaIssueID(nextIndex, existing)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	issueType := params.Type
	if issueType == "" {
		issueType = domain.TypeTask
	}
	status := params.Status
	if status == "" {
		status = domain.StatusOpen
	}
	labelsJSON, err := marshalOptionalStringSlice(params.Labels)
	if err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	implementationsJSON, err := marshalOptionalStringSlice(params.Implementations)
	if err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	var closedAt any
	if status == domain.StatusDone {
		closedAt = now
	}
	var estimate any
	if params.Estimate != nil {
		estimate = *params.Estimate
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issues (
			id,
			title,
			description,
			status,
			priority,
			issue_type,
			created_at,
			updated_at,
			closed_at,
			assignee,
			labels_json,
			implementations_json,
			design,
			notes,
			acceptance,
			estimate,
			deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, issueID, params.Title, nullableString(params.Description), string(status), int(params.Priority), string(issueType), now, now, closedAt, nullableString(params.Assignee), labelsJSON, implementationsJSON, nullableString(params.Design), nullableString(params.Notes), nullableString(params.Acceptance), estimate); err != nil {
		return "", c.wrapError("create", issueID, err)
	}

	if params.ParentID != nil && strings.TrimSpace(*params.ParentID) != "" {
		parentID := strings.TrimSpace(*params.ParentID)
		parentExists, err := c.issueExists(ctx, tx, parentID)
		if err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if !parentExists {
			return "", c.wrapError("create", issueID, domain.ErrNotFound)
		}
		if err := c.reopenClosedParentForActiveChild(ctx, tx, issueID, parentID); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
			VALUES (?, ?, ?, NULL)
			ON CONFLICT(issue_id, depends_on_id, dependency_type)
			DO UPDATE SET tombstoned_at = NULL
		`, issueID, parentID, string(domain.DependencyParentChild)); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, nextAlphaIssueIndexMetaKey, strconv.Itoa(nextReserved)); err != nil {
		return "", c.wrapError("create", issueID, err)
	}

	if err := tx.Commit(); err != nil {
		return "", c.wrapError("create", issueID, err)
	}
	tx = nil
	return issueID, nil
}

// CreateWithRuntime inserts a new issue and returns the created task with runtime projection fields.
func (c *Client) CreateWithRuntime(ctx context.Context, projectID string, params CreateTaskParams) (domain.Task, error) {
	id, err := c.Create(ctx, params)
	if err != nil {
		return domain.Task{}, err
	}
	var task domain.Task
	err = retrySQLiteBusy(ctx, func() error {
		var err error
		task, err = c.GetWithRuntime(ctx, projectID, id)
		return err
	})
	if err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

func retrySQLiteBusy(ctx context.Context, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if !IsSQLiteBusy(err) {
			return err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(sqliteBusyRetryDelay):
		}
	}
}

// IsSQLiteBusy reports whether err wraps a temporary SQLite busy/locked result.
func IsSQLiteBusy(err error) bool {
	for err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqliteBusyPrimaryCode {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func (c *Client) UpsertExternalIssueRef(ctx context.Context, params UpsertExternalIssueRefParams) (domain.ExternalIssueRef, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.ExternalIssueRef{}, err
	}
	normalized, err := normalizeExternalIssueRefParams(params)
	if err != nil {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", params.IssueID, err)
	}
	exists, err := c.issueExists(ctx, db, normalized.IssueID)
	if err != nil {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, err)
	}
	if !exists {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, fmt.Errorf("issue not found: %s", normalized.IssueID))
	}

	metadataJSON, err := marshalStringMap(normalized.Metadata)
	if err != nil {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO issue_external_refs (
			issue_id,
			provider,
			provider_scope,
			remote_key,
			display_key,
			url,
			metadata_json,
			created_at,
			updated_at,
			deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(issue_id, provider, provider_scope, remote_key)
		DO UPDATE SET
			display_key = excluded.display_key,
			url = excluded.url,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`, normalized.IssueID, normalized.Provider, normalized.ProviderScope, normalized.RemoteKey, nullableString(normalized.DisplayKey), nullableString(normalized.URL), nullableString(metadataJSON), now, now); err != nil {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, err)
	}
	ref, found, err := c.GetExternalIssueRef(ctx, normalized.Provider, normalized.ProviderScope, normalized.RemoteKey)
	if err != nil {
		return domain.ExternalIssueRef{}, err
	}
	if !found {
		return domain.ExternalIssueRef{}, c.wrapError("upsert-external-ref", normalized.IssueID, errors.New("external ref not found after upsert"))
	}
	return ref, nil
}

func (c *Client) GetExternalIssueRef(ctx context.Context, provider, providerScope, remoteKey string) (domain.ExternalIssueRef, bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.ExternalIssueRef{}, false, err
	}
	provider = strings.TrimSpace(provider)
	providerScope = strings.TrimSpace(providerScope)
	remoteKey = strings.TrimSpace(remoteKey)
	if provider == "" || remoteKey == "" {
		return domain.ExternalIssueRef{}, false, c.wrapError("get-external-ref", "", errors.New("provider and remote key are required"))
	}
	row := db.QueryRowContext(ctx, `
		SELECT issue_id, provider, provider_scope, remote_key, COALESCE(display_key, ''), COALESCE(url, ''), COALESCE(metadata_json, ''), created_at, updated_at
		FROM issue_external_refs
		WHERE provider = ? AND provider_scope = ? AND remote_key = ? AND deleted_at IS NULL
	`, provider, providerScope, remoteKey)
	ref, err := scanExternalIssueRef(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExternalIssueRef{}, false, nil
	}
	if err != nil {
		return domain.ExternalIssueRef{}, false, c.wrapError("get-external-ref", "", err)
	}
	return ref, true, nil
}

func (c *Client) GetExternalIssueRefByDisplayKey(ctx context.Context, provider, providerScope, displayKey string) (domain.ExternalIssueRef, bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.ExternalIssueRef{}, false, err
	}
	provider = strings.TrimSpace(provider)
	providerScope = strings.TrimSpace(providerScope)
	displayKey = strings.TrimSpace(displayKey)
	if provider == "" || displayKey == "" {
		return domain.ExternalIssueRef{}, false, nil
	}
	ref, err := scanExternalIssueRef(db.QueryRowContext(ctx, `
		SELECT
			issue_id,
			provider,
			provider_scope,
			remote_key,
			COALESCE(display_key, ''),
			COALESCE(url, ''),
			COALESCE(metadata_json, ''),
			created_at,
			updated_at
		FROM issue_external_refs
		WHERE provider = ?
			AND provider_scope = ?
			AND display_key = ?
			AND deleted_at IS NULL
		ORDER BY updated_at DESC, issue_id ASC
		LIMIT 1
	`, provider, providerScope, displayKey))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ExternalIssueRef{}, false, nil
		}
		return domain.ExternalIssueRef{}, false, c.wrapError("get-external-ref-by-display-key", displayKey, err)
	}
	return ref, true, nil
}

func (c *Client) ListExternalIssueRefs(ctx context.Context, issueID string) ([]domain.ExternalIssueRef, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, c.wrapError("list-external-refs", "", errors.New("issue id is required"))
	}
	rows, err := db.QueryContext(ctx, `
		SELECT issue_id, provider, provider_scope, remote_key, COALESCE(display_key, ''), COALESCE(url, ''), COALESCE(metadata_json, ''), created_at, updated_at
		FROM issue_external_refs
		WHERE issue_id = ? AND deleted_at IS NULL
		ORDER BY provider, provider_scope, remote_key
	`, issueID)
	if err != nil {
		return nil, c.wrapError("list-external-refs", issueID, err)
	}
	defer rows.Close()

	var refs []domain.ExternalIssueRef
	for rows.Next() {
		ref, err := scanExternalIssueRef(rows)
		if err != nil {
			return nil, c.wrapError("list-external-refs", issueID, err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-external-refs", issueID, err)
	}
	return refs, nil
}

// AddDependency creates or restores a dependency edge between two issues.
func (c *Client) AddDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.addDependency(ctx, issueID, dependsOnID, dependencyType, false)
	})
}

// AddDependencyWithParentChange creates or restores a dependency edge, allowing
// an existing parent-child edge to be replaced when forceParentChange is true.
func (c *Client) AddDependencyWithParentChange(ctx context.Context, issueID, dependsOnID, dependencyType string, forceParentChange bool) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.addDependency(ctx, issueID, dependsOnID, dependencyType, forceParentChange)
	})
}

func (c *Client) addDependency(ctx context.Context, issueID, dependsOnID, dependencyType string, forceParentChange bool) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}

	issueID = strings.TrimSpace(issueID)
	dependsOnID = strings.TrimSpace(dependsOnID)
	dependencyType = strings.TrimSpace(dependencyType)
	if issueID == "" || dependsOnID == "" || dependencyType == "" {
		return c.wrapError("add-dependency", issueID, errors.New("issue id, dependency id, and dependency type are required"))
	}

	canonicalType, err := canonicalDependencyType(dependencyType)
	if err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}

	if dependencyIsAcyclic(canonicalType) {
		if issueID == dependsOnID {
			return c.wrapError("add-dependency", issueID, domain.ErrConflict)
		}

		cycle, err := c.wouldCreateDependencyCycle(ctx, db, issueID, dependsOnID)
		if err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		if cycle {
			return c.wrapError("add-dependency", issueID, domain.ErrConflict)
		}
	}

	sourceExists, err := c.issueExists(ctx, db, issueID)
	if err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	if !sourceExists {
		return c.wrapError("add-dependency", issueID, domain.ErrNotFound)
	}

	targetExists, err := c.issueExists(ctx, db, dependsOnID)
	if err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	if !targetExists {
		return c.wrapError("add-dependency", issueID, domain.ErrNotFound)
	}

	tombstoneOldParent := ""
	if canonicalType == string(domain.DependencyParentChild) {
		currentParents, err := c.activeParents(ctx, db, issueID)
		if err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		for _, currentParent := range currentParents {
			if currentParent == dependsOnID {
				continue
			}
			if !forceParentChange {
				return c.wrapError("add-dependency", issueID, ParentChangeRequiredError{
					IssueID:         issueID,
					CurrentParent:   currentParent,
					RequestedParent: dependsOnID,
				})
			}
			tombstoneOldParent = currentParent
			break
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if tombstoneOldParent != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE issue_dependencies
			SET tombstoned_at = ?
			WHERE issue_id = ? AND depends_on_id != ? AND dependency_type IN (?, 'parent_child') AND tombstoned_at IS NULL
		`, time.Now().UTC().Format(time.RFC3339Nano), issueID, dependsOnID, canonicalType); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
	}

	if canonicalType == string(domain.DependencyParentChild) {
		sourceExists, err := c.issueExists(ctx, tx, issueID)
		if err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		if !sourceExists {
			return c.wrapError("add-dependency", issueID, domain.ErrNotFound)
		}
		targetExists, err := c.issueExists(ctx, tx, dependsOnID)
		if err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
		if !targetExists {
			return c.wrapError("add-dependency", issueID, domain.ErrNotFound)
		}
		if err := c.reopenClosedParentForActiveChild(ctx, tx, issueID, dependsOnID); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, ?, NULL)
		ON CONFLICT(issue_id, depends_on_id, dependency_type)
		DO UPDATE SET tombstoned_at = NULL
	`, issueID, dependsOnID, canonicalType); err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	if canonicalType == string(domain.DependencyParentChild) {
		if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	committed = true
	return nil
}

// AddDependencyWithRuntime creates or restores a dependency edge and returns the changed issue.
func (c *Client) AddDependencyWithRuntime(ctx context.Context, projectID, issueID, dependsOnID, dependencyType string) (domain.Task, error) {
	return c.AddDependencyWithRuntimeAndParentChange(ctx, projectID, issueID, dependsOnID, dependencyType, false)
}

// AddDependencyWithRuntimeAndParentChange creates or restores a dependency edge
// and returns the changed issue.
func (c *Client) AddDependencyWithRuntimeAndParentChange(ctx context.Context, projectID, issueID, dependsOnID, dependencyType string, forceParentChange bool) (domain.Task, error) {
	if err := c.AddDependencyWithParentChange(ctx, issueID, dependsOnID, dependencyType, forceParentChange); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, issueID)
}

func (c *Client) activeParents(ctx context.Context, db *sql.DB, issueID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT depends_on_id
		FROM issue_dependencies
		WHERE issue_id = ? AND dependency_type IN (?, 'parent_child') AND tombstoned_at IS NULL
		ORDER BY depends_on_id
	`, issueID, string(domain.DependencyParentChild))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var parentIDs []string
	for rows.Next() {
		var parentID string
		if err := rows.Scan(&parentID); err != nil {
			return nil, err
		}
		parentIDs = append(parentIDs, parentID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return parentIDs, nil
}

func (c *Client) reopenClosedParentForActiveChild(ctx context.Context, execer sqlIssueExecer, childID, parentID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := execer.ExecContext(ctx, `
		UPDATE issues
		SET
			status = ?,
			updated_at = ?,
			closed_at = NULL
		WHERE
			id = ?
			AND deleted_at IS NULL
			AND status = ?
			AND EXISTS (
				SELECT 1
				FROM issues child
				WHERE
					child.id = ?
					AND child.deleted_at IS NULL
					AND child.status != ?
			)
	`, string(domain.StatusInProgress), now, parentID, string(domain.StatusDone), childID, string(domain.StatusDone)); err != nil {
		return err
	}
	return nil
}

func (c *Client) issueExists(ctx context.Context, queryer sqlIssueQueryer, id string) (bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM issues
			WHERE id = ? AND deleted_at IS NULL
		)
	`, id).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (c *Client) rebuildIssueGraphClosure(ctx context.Context, execer sqlIssueExecer) error {
	if _, err := execer.ExecContext(ctx, `DELETE FROM issue_graph_closure`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := execer.ExecContext(ctx, `
		INSERT INTO issue_graph_closure (
			project_id,
			ancestor_id,
			descendant_id,
			dependency_type,
			depth,
			updated_at
		)
		WITH RECURSIVE parent_edges(ancestor_id, descendant_id) AS (
			SELECT d.depends_on_id, d.issue_id
			FROM issue_dependencies d
			INNER JOIN issues ancestor
				ON ancestor.id = d.depends_on_id
				AND ancestor.deleted_at IS NULL
			INNER JOIN issues descendant
				ON descendant.id = d.issue_id
				AND descendant.deleted_at IS NULL
			WHERE d.tombstoned_at IS NULL
				AND d.dependency_type IN (?, 'parent_child')
		),
		closure(ancestor_id, descendant_id, depth, path) AS (
			SELECT ancestor_id, descendant_id, 1, ',' || ancestor_id || ',' || descendant_id || ','
			FROM parent_edges
			UNION ALL
			SELECT c.ancestor_id, e.descendant_id, c.depth + 1, c.path || e.descendant_id || ','
			FROM closure c
			INNER JOIN parent_edges e
				ON e.ancestor_id = c.descendant_id
			WHERE instr(c.path, ',' || e.descendant_id || ',') = 0
		)
		SELECT
			?,
			ancestor_id,
			descendant_id,
			?,
			MIN(depth),
			?
		FROM closure
		WHERE ancestor_id <> descendant_id
		GROUP BY ancestor_id, descendant_id
	`, string(domain.DependencyParentChild), issueGraphClosureProjectID, string(domain.DependencyParentChild), now); err != nil {
		return err
	}
	return nil
}

// RemoveDependency tombstones a dependency edge between two issues.
func (c *Client) RemoveDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.removeDependency(ctx, issueID, dependsOnID, dependencyType)
	})
}

func (c *Client) removeDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}

	issueID = strings.TrimSpace(issueID)
	dependsOnID = strings.TrimSpace(dependsOnID)
	dependencyType = strings.TrimSpace(dependencyType)
	if issueID == "" || dependsOnID == "" || dependencyType == "" {
		return c.wrapError("remove-dependency", issueID, errors.New("issue id, dependency id, and dependency type are required"))
	}

	canonicalType, err := canonicalDependencyType(dependencyType)
	if err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}

	if dependencyTypeRequiresConfirmation(canonicalType) && !hasDependencyRemovalConfirmation(ctx) {
		return c.wrapError("remove-dependency", issueID, ErrDependencyRemovalConfirmationRequired)
	}
	if canonicalType == string(domain.DependencyParentChild) && !hasParentChildOrphanConfirmation(ctx) {
		active, err := c.parentChildRemovalWouldOrphanActiveChild(ctx, db, issueID, dependsOnID)
		if err != nil {
			return c.wrapError("remove-dependency", issueID, err)
		}
		if active {
			return c.wrapError("remove-dependency", issueID, ErrParentChildOrphanConfirmationRequired)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `
		UPDATE issue_dependencies
		SET tombstoned_at = ?
		WHERE issue_id = ? AND depends_on_id = ? AND dependency_type = ? AND tombstoned_at IS NULL
	`, time.Now().UTC().Format(time.RFC3339Nano), issueID, dependsOnID, canonicalType)
	if err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("remove-dependency", issueID, domain.ErrNotFound)
	}
	if canonicalType == string(domain.DependencyParentChild) {
		if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
			return c.wrapError("remove-dependency", issueID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return c.wrapError("remove-dependency", issueID, err)
	}
	committed = true
	return nil
}

// RemoveDependencyWithRuntime tombstones a dependency edge and returns the changed issue.
func (c *Client) RemoveDependencyWithRuntime(ctx context.Context, projectID, issueID, dependsOnID, dependencyType string) (domain.Task, error) {
	canonicalType, canonicalErr := canonicalDependencyType(dependencyType)
	if canonicalErr == nil && canonicalType == string(domain.DependencyParentChild) && !hasParentChildOrphanConfirmation(ctx) {
		exists, err := c.dependencyEdgeExists(ctx, issueID, dependsOnID, canonicalType)
		if err != nil {
			return domain.Task{}, c.wrapError("remove-dependency", issueID, err)
		}
		if !exists {
			if err := c.RemoveDependency(ctx, issueID, dependsOnID, dependencyType); err != nil {
				return domain.Task{}, err
			}
			return c.GetWithRuntime(ctx, projectID, issueID)
		}
		task, err := c.GetWithRuntime(ctx, projectID, issueID)
		if err != nil {
			return domain.Task{}, err
		}
		if parentChildRemovalWouldOrphanRuntimeChild(task) {
			return domain.Task{}, c.wrapError("remove-dependency", issueID, ErrParentChildOrphanConfirmationRequired)
		}
	}
	if err := c.RemoveDependency(ctx, issueID, dependsOnID, dependencyType); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, issueID)
}

func (c *Client) dependencyEdgeExists(ctx context.Context, issueID, dependsOnID, dependencyType string) (bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return false, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM issue_dependencies
			WHERE issue_id = ? AND depends_on_id = ? AND dependency_type = ? AND tombstoned_at IS NULL
		)
	`, issueID, dependsOnID, dependencyType).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (c *Client) parentChildRemovalWouldOrphanActiveChild(ctx context.Context, db *sql.DB, issueID, dependsOnID string) (bool, error) {
	var status string
	if err := db.QueryRowContext(ctx, `
		SELECT i.status
		FROM issues i
		INNER JOIN issue_dependencies d
			ON d.issue_id = i.id
			AND d.depends_on_id = ?
			AND d.dependency_type = ?
			AND d.tombstoned_at IS NULL
		WHERE i.id = ? AND i.deleted_at IS NULL
	`, dependsOnID, string(domain.DependencyParentChild), issueID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return isActiveParentChildRemovalStatus(domain.Status(strings.TrimSpace(status))), nil
}

func parentChildRemovalWouldOrphanRuntimeChild(task domain.Task) bool {
	return isActiveParentChildRemovalStatus(task.Status) || task.HasWorktree || task.HasTmuxSession || task.Session != nil
}

func isActiveParentChildRemovalStatus(status domain.Status) bool {
	switch status {
	case domain.StatusOpen, domain.StatusInProgress, domain.StatusInReview:
		return true
	default:
		return false
	}
}

func dependencyTypeRequiresConfirmation(dependencyType string) bool {
	switch dependencyType {
	case string(domain.DependencyBlocks), string(domain.DependencyParentChild):
		return true
	default:
		return false
	}
}

// Close marks an issue as closed.
func (c *Client) Close(ctx context.Context, id string, _ string) error {
	return c.Update(ctx, id, domain.StatusDone)
}

// Delete permanently removes an issue and its dependency rows.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.deleteLocked(ctx, id)
	})
}

func (c *Client) deleteLocked(ctx context.Context, id string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("delete", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	var runtimeAttachmentCount int
	runtimeAttachmentQuery := `
		SELECT (
			CASE
				WHEN EXISTS (
					SELECT 1
					FROM daemon_worktree_projections
					WHERE issue_id = ? AND TRIM(COALESCE(path, '')) <> ''
				)
				THEN 1 ELSE 0
			END
		) + (
			CASE
				WHEN EXISTS (
					SELECT 1
					FROM (` + runtimeSessionProjectionUnionSQL + `)
					WHERE issue_id = ? AND LOWER(TRIM(COALESCE(state, ''))) <> 'stopped'
				)
				THEN 1 ELSE 0
			END
		)
	`
	if err := tx.QueryRowContext(ctx, runtimeAttachmentQuery, id, id).Scan(&runtimeAttachmentCount); err != nil {
		return c.wrapError("delete", id, err)
	}
	if runtimeAttachmentCount > 0 {
		return c.wrapError("delete", id, ErrDeleteBlockedByRuntimeAttachments)
	}
	if err := c.guardNoUndeletedParentChildDescendants(ctx, tx, "delete", id); err != nil {
		return c.wrapError("delete", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_dependencies WHERE issue_id = ? OR depends_on_id = ?`, id, id); err != nil {
		return c.wrapError("delete", id, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, id)
	if err != nil {
		return c.wrapError("delete", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("delete", id, domain.ErrNotFound)
	}
	if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
		return c.wrapError("delete", id, err)
	}
	if err := tx.Commit(); err != nil {
		return c.wrapError("delete", id, err)
	}
	tx = nil
	return nil
}

// Archive soft-deletes an issue.
func (c *Client) Archive(ctx context.Context, id string) error {
	return c.withMutationLock(ctx, func(ctx context.Context) error {
		return c.archiveLocked(ctx, id)
	})
}

func (c *Client) archiveLocked(ctx context.Context, id string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("archive", id, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if err := c.guardNoUndeletedParentChildDescendants(ctx, tx, "archive", id); err != nil {
		return c.wrapError("archive", id, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET
			deleted_at = ?,
			updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, now, now, id)
	if err != nil {
		return c.wrapError("archive", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("archive", id, domain.ErrNotFound)
	}
	if err := c.rebuildIssueGraphClosure(ctx, tx); err != nil {
		return c.wrapError("archive", id, err)
	}
	if err := tx.Commit(); err != nil {
		return c.wrapError("archive", id, err)
	}
	tx = nil
	return nil
}

func (c *Client) EnsureNoUndeletedParentChildDescendants(ctx context.Context, operation, issueID string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	issueID = strings.TrimSpace(issueID)
	if err := c.guardNoUndeletedParentChildDescendants(ctx, db, operation, issueID); err != nil {
		return c.wrapError(operation, issueID, err)
	}
	return nil
}

func (c *Client) guardNoUndeletedParentChildDescendants(ctx context.Context, queryer sqlIssueQueryer, operation, issueID string) error {
	count, err := c.countUndeletedParentChildDescendants(ctx, queryer, issueID)
	if err != nil {
		return err
	}
	if count > 0 {
		return LiveChildrenMutationError{
			Operation:       operation,
			IssueID:         issueID,
			DescendantCount: count,
		}
	}
	return nil
}

func (c *Client) countUndeletedParentChildDescendants(ctx context.Context, queryer sqlIssueQueryer, issueID string) (int, error) {
	var count int
	if err := queryer.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT closure.descendant_id)
		FROM issue_graph_closure closure INDEXED BY idx_issue_graph_closure_ancestor
		INNER JOIN issues descendant
			ON descendant.id = closure.descendant_id
			AND descendant.deleted_at IS NULL
		WHERE closure.project_id = ?
			AND closure.dependency_type = ?
			AND closure.ancestor_id = ?
	`, issueGraphClosureProjectID, string(domain.DependencyParentChild), issueID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

type UpdateTaskParams struct {
	Title           string
	Description     string
	Notes           *string
	Type            domain.TaskType
	Priority        domain.Priority
	Implementations []string
}

// AppendNotes appends a single line to task notes.
func (c *Client) AppendNotes(ctx context.Context, id, line string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	noteLine := strings.TrimSpace(line)
	if noteLine == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := db.ExecContext(ctx, `
		UPDATE issues
		SET
			notes = CASE
				WHEN notes IS NULL OR TRIM(notes) = '' THEN ?
				ELSE notes || CHAR(10) || ?
			END,
			updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, noteLine, noteLine, now, id)
	if err != nil {
		return c.wrapError("append-notes", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("append-notes", id, domain.ErrNotFound)
	}
	return nil
}

// AppendNotesWithRuntime appends notes and returns the changed issue.
func (c *Client) AppendNotesWithRuntime(ctx context.Context, projectID, id, line string) (domain.Task, error) {
	if err := c.AppendNotes(ctx, id, line); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

// UpdateDetails updates non-status issue metadata.
func (c *Client) UpdateDetails(ctx context.Context, id string, params UpdateTaskParams) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if params.Implementations != nil {
		implsJSON, err := json.Marshal(params.Implementations)
		if err != nil {
			return c.wrapError("update-details", id, err)
		}
		if params.Notes != nil {
			res, err := db.ExecContext(ctx, `
		UPDATE issues
		SET
			title = ?,
			description = ?,
			notes = ?,
			issue_type = ?,
			priority = ?,
			implementations_json = ?,
			updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, params.Title, nullableString(params.Description), nullableString(*params.Notes), string(params.Type), int(params.Priority), string(implsJSON), now, id)
			if err != nil {
				return c.wrapError("update-details", id, err)
			}
			affected, _ := res.RowsAffected()
			if affected == 0 {
				return c.wrapError("update-details", id, domain.ErrNotFound)
			}
			return nil
		}
		res, err := db.ExecContext(ctx, `
		UPDATE issues
		SET
			title = ?,
			description = ?,
			issue_type = ?,
			priority = ?,
			implementations_json = ?,
			updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, params.Title, nullableString(params.Description), string(params.Type), int(params.Priority), string(implsJSON), now, id)
		if err != nil {
			return c.wrapError("update-details", id, err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return c.wrapError("update-details", id, domain.ErrNotFound)
		}
		return nil
	}
	if params.Notes != nil {
		res, err := db.ExecContext(ctx, `
		UPDATE issues
		SET
			title = ?,
			description = ?,
			notes = ?,
			issue_type = ?,
			priority = ?,
			updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, params.Title, nullableString(params.Description), nullableString(*params.Notes), string(params.Type), int(params.Priority), now, id)
		if err != nil {
			return c.wrapError("update-details", id, err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return c.wrapError("update-details", id, domain.ErrNotFound)
		}
		return nil
	}
	res, err := db.ExecContext(ctx, `
		UPDATE issues
		SET
			title = ?,
			description = ?,
			issue_type = ?,
			priority = ?,
			updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, params.Title, nullableString(params.Description), string(params.Type), int(params.Priority), now, id)
	if err != nil {
		return c.wrapError("update-details", id, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return c.wrapError("update-details", id, domain.ErrNotFound)
	}
	return nil
}

// UpdateDetailsWithRuntime updates issue metadata and returns the changed issue.
func (c *Client) UpdateDetailsWithRuntime(ctx context.Context, projectID, id string, params UpdateTaskParams) (domain.Task, error) {
	if err := c.UpdateDetails(ctx, id, params); err != nil {
		return domain.Task{}, err
	}
	return c.GetWithRuntime(ctx, projectID, id)
}

func (c *Client) queryTasks(ctx context.Context, db *sql.DB, query string, args ...any) ([]domain.Task, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, 32)
	taskIDs := make([]naming.IssueID, 0, 32)
	taskIndexByID := map[naming.IssueID]int{}

	for rows.Next() {
		task := domain.Task{}
		var createdRaw string
		var updatedRaw string
		var statusRaw string
		var typeRaw string
		var priorityRaw int
		var assigneeRaw string
		var labelsRaw string
		var estimateRaw sql.NullInt64
		var implementationsRaw string
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Description,
			&task.Notes,
			&task.Design,
			&task.Acceptance,
			&assigneeRaw,
			&labelsRaw,
			&estimateRaw,
			&statusRaw,
			&priorityRaw,
			&typeRaw,
			&implementationsRaw,
			&createdRaw,
			&updatedRaw,
		); err != nil {
			return nil, err
		}
		task.Status = domain.Status(statusRaw)
		task.Priority = domain.Priority(priorityRaw)
		task.Type = domain.TaskType(typeRaw)
		task.CreatedAt = parseTimestamp(createdRaw)
		task.UpdatedAt = parseTimestamp(updatedRaw)
		task.Assignee = strings.TrimSpace(assigneeRaw)
		task.Labels = decodeStringSliceJSON(labelsRaw)
		if estimateRaw.Valid {
			estimateValue := int(estimateRaw.Int64)
			task.Estimate = &estimateValue
		}
		task.Implementations = decodeImplementationsJSON(implementationsRaw)

		tasks = append(tasks, task)
		taskIDs = append(taskIDs, task.ID)
		taskIndexByID[task.ID] = len(tasks) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return tasks, nil
	}

	if err := c.loadDependenciesForTasks(ctx, db, taskIDs, taskIndexByID, tasks); err != nil {
		return nil, err
	}

	return tasks, nil
}

func (c *Client) queryTaskSummariesWithRuntime(ctx context.Context, db *sql.DB, projectID string, issueIDs ...string) ([]domain.Task, error) {
	return c.queryTasksWithRuntimeProjection(ctx, db, projectID, false, issueIDs...)
}

func (c *Client) queryTasksWithRuntime(ctx context.Context, db *sql.DB, projectID string, issueIDs ...string) ([]domain.Task, error) {
	return c.queryTasksWithRuntimeProjection(ctx, db, projectID, true, issueIDs...)
}

func (c *Client) queryTaskMetadataWithRuntime(ctx context.Context, db *sql.DB, projectID string, issueIDs ...string) ([]domain.Task, error) {
	ids := uniqueIssueIDStrings(issueIDs)
	if len(ids) == 0 {
		return []domain.Task{}, nil
	}
	startedAt := time.Now()
	query, args := taskMetadataRuntimeProjectionQuery(projectID, ids...)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, 0, err, "issue_count", len(ids))
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, len(ids))
	seen := map[naming.IssueID]int{}
	for rows.Next() {
		task := domain.Task{}
		var (
			statusRaw          string
			typeRaw            string
			priorityRaw        int
			createdRaw         string
			updatedRaw         string
			sessionStateRaw    string
			sessionStartedRaw  string
			sessionUpdatedRaw  string
			sessionActivityRaw string
			sessionSourceRaw   string
			tmuxAttachedCount  int
			worktreePath       string
			gitStatusRaw       string
			worktreeUpdatedRaw string
			gitUpdatedRaw      string
			parentIDRaw        string
		)
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&statusRaw,
			&priorityRaw,
			&typeRaw,
			&createdRaw,
			&updatedRaw,
			&sessionStateRaw,
			&sessionStartedRaw,
			&sessionUpdatedRaw,
			&sessionActivityRaw,
			&sessionSourceRaw,
			&tmuxAttachedCount,
			&worktreePath,
			&gitStatusRaw,
			&worktreeUpdatedRaw,
			&gitUpdatedRaw,
			&parentIDRaw,
		); err != nil {
			c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), err, "issue_count", len(ids))
			return nil, err
		}
		if idx, ok := seen[task.ID]; ok {
			if tasks[idx].ParentID == nil {
				if parentID, err := naming.ParseIssueID(parentIDRaw); err == nil {
					tasks[idx].ParentID = &parentID
				}
			}
			continue
		}
		task.Status = domain.Status(statusRaw)
		task.Priority = domain.Priority(priorityRaw)
		task.Type = domain.TaskType(typeRaw)
		task.CreatedAt = parseTimestamp(createdRaw)
		task.UpdatedAt = parseTimestamp(updatedRaw)
		task.RuntimeUpdatedAt = newestParsedTimestamp(task.UpdatedAt, gitUpdatedRaw)
		task.Origin = "local"
		if parentID, err := naming.ParseIssueID(parentIDRaw); err == nil {
			task.ParentID = &parentID
		}
		worktreePath = strings.TrimSpace(worktreePath)
		if worktreePath != "" {
			task.HasWorktree = true
		}
		applyGitStatusProjection(&task, gitStatusRaw)
		sessionStateRaw = strings.TrimSpace(sessionStateRaw)
		if sessionStateRaw != "" && sessionStateRaw != "stopped" {
			startedAt := parseOptionalTimestamp(sessionStartedRaw)
			if startedAt == nil {
				startedAt = parseOptionalTimestamp(sessionUpdatedRaw)
			}
			task.Session = &domain.Session{
				IssueID:           task.ID,
				State:             mapRuntimeSessionState(sessionStateRaw),
				Activity:          strings.ToLower(strings.TrimSpace(sessionActivityRaw)),
				ActivitySource:    strings.ToLower(strings.TrimSpace(sessionSourceRaw)),
				TmuxAttached:      tmuxAttachedCount > 0,
				TmuxAttachedCount: tmuxAttachedCount,
				StartedAt:         startedAt,
				UpdatedAt:         parseTimestamp(sessionUpdatedRaw),
				Worktree:          worktreePath,
			}
			task.HasTmuxSession = true
		}
		seen[task.ID] = len(tasks)
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), err, "issue_count", len(ids))
		return nil, err
	}
	c.logSQLiteRead(ctx, "issue.metadata_runtime_projection", startedAt, len(tasks), nil, "issue_count", len(ids))
	return tasks, nil
}

func taskMetadataRuntimeProjectionQuery(projectID string, issueIDs ...string) (string, []any) {
	ids := uniqueIssueIDStrings(issueIDs)
	if len(ids) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := fmt.Sprintf(`
		WITH ranked_session AS (
			SELECT
				issue_id,
				COALESCE(NULLIF(TRIM(observed_state), ''), state) AS state,
				COALESCE(started_at, '') AS started_at,
				updated_at,
				session_id,
				COALESCE(activity, '') AS activity,
				COALESCE(activity_source, '') AS activity_source,
				COALESCE(tmux_attached_count, 0) AS tmux_attached_count,
				ROW_NUMBER() OVER (
					PARTITION BY issue_id
					ORDER BY
						CASE COALESCE(NULLIF(TRIM(observed_state), ''), state)
							WHEN 'running' THEN 0
							WHEN 'attached' THEN 0
							WHEN 'paused' THEN 1
							WHEN 'starting' THEN 2
							WHEN 'stopped' THEN 3
							ELSE 4
						END,
						updated_at DESC,
						session_id DESC
				) AS rn
			FROM (%s)
			WHERE project_id = ? AND issue_id IN (%s)
		),
		session_pick AS (
			SELECT issue_id, state, started_at, updated_at, activity, activity_source, tmux_attached_count
			FROM ranked_session
			WHERE rn = 1
		)
		SELECT
			i.id,
			i.title,
			i.status,
			i.priority,
			i.issue_type,
			i.created_at,
			i.updated_at,
			COALESCE(sp.state, ''),
			COALESCE(sp.started_at, ''),
			COALESCE(sp.updated_at, ''),
			COALESCE(sp.activity, ''),
			COALESCE(sp.activity_source, ''),
			COALESCE(sp.tmux_attached_count, 0),
			COALESCE(w.path, ''),
			COALESCE(w.git_status_json, ''),
			COALESCE(w.updated_at, ''),
			COALESCE(w.git_status_updated_at, ''),
			COALESCE(parent.depends_on_id, '')
		FROM issues i
		LEFT JOIN session_pick sp ON sp.issue_id = i.id
		LEFT JOIN daemon_worktree_projections w
			ON w.project_id = ? AND w.issue_id = i.id
		LEFT JOIN issue_dependencies parent
			ON parent.issue_id = i.id
			AND parent.tombstoned_at IS NULL
			AND parent.dependency_type IN (?, ?)
		WHERE i.deleted_at IS NULL AND i.id IN (%s)
		ORDER BY i.updated_at DESC
	`, runtimeSessionProjectionUnionSQL, placeholders, placeholders)
	args := make([]any, 0, len(ids)*2+4)
	args = append(args, projectID)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, projectID, string(domain.DependencyParentChild), "parent_child")
	for _, id := range ids {
		args = append(args, id)
	}
	return query, args
}

func (c *Client) queryTasksWithRuntimeProjection(ctx context.Context, db *sql.DB, projectID string, includeDetails bool, issueIDs ...string) ([]domain.Task, error) {
	startedAt := time.Now()
	issueCount := len(uniqueIssueIDStrings(issueIDs))
	query, args := taskRuntimeProjectionQuery(projectID, includeDetails, issueIDs...)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, 0, err, "include_details", includeDetails, "issue_count", issueCount)
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, 32)
	taskIDs := make([]naming.IssueID, 0, 32)
	taskIndexByID := map[naming.IssueID]int{}

	for rows.Next() {
		task := domain.Task{}
		var (
			createdRaw         string
			updatedRaw         string
			statusRaw          string
			typeRaw            string
			priorityRaw        int
			assigneeRaw        string
			labelsRaw          string
			estimateRaw        sql.NullInt64
			implementationsRaw string
			sessionStateRaw    string
			sessionStartedRaw  string
			sessionUpdatedRaw  string
			sessionActivityRaw string
			sessionSourceRaw   string
			tmuxAttachedCount  int
			worktreePath       string
			gitStatusRaw       string
			worktreeUpdatedRaw string
			gitUpdatedRaw      string
			originProvider     string
		)
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Description,
			&task.Notes,
			&task.Design,
			&task.Acceptance,
			&assigneeRaw,
			&labelsRaw,
			&estimateRaw,
			&statusRaw,
			&priorityRaw,
			&typeRaw,
			&implementationsRaw,
			&createdRaw,
			&updatedRaw,
			&sessionStateRaw,
			&sessionStartedRaw,
			&sessionUpdatedRaw,
			&sessionActivityRaw,
			&sessionSourceRaw,
			&tmuxAttachedCount,
			&worktreePath,
			&gitStatusRaw,
			&worktreeUpdatedRaw,
			&gitUpdatedRaw,
			&originProvider,
		); err != nil {
			c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
			return nil, err
		}
		if origin := strings.TrimSpace(strings.ToLower(originProvider)); origin != "" {
			task.Origin = origin
		} else {
			task.Origin = "local"
		}

		task.Status = domain.Status(statusRaw)
		task.Priority = domain.Priority(priorityRaw)
		task.Type = domain.TaskType(typeRaw)
		task.CreatedAt = parseTimestamp(createdRaw)
		task.UpdatedAt = parseTimestamp(updatedRaw)
		task.RuntimeUpdatedAt = newestParsedTimestamp(task.UpdatedAt, gitUpdatedRaw)
		task.Assignee = strings.TrimSpace(assigneeRaw)
		task.Labels = decodeStringSliceJSON(labelsRaw)
		if estimateRaw.Valid {
			estimateValue := int(estimateRaw.Int64)
			task.Estimate = &estimateValue
		}
		task.Implementations = decodeImplementationsJSON(implementationsRaw)

		worktreePath = strings.TrimSpace(worktreePath)
		if worktreePath != "" {
			task.HasWorktree = true
		}
		sessionStateRaw = strings.TrimSpace(sessionStateRaw)
		if sessionStateRaw != "" && sessionStateRaw != "stopped" {
			startedAt := parseOptionalTimestamp(sessionStartedRaw)
			if startedAt == nil {
				startedAt = parseOptionalTimestamp(sessionUpdatedRaw)
			}
			task.Session = &domain.Session{
				IssueID:           task.ID,
				State:             mapRuntimeSessionState(sessionStateRaw),
				Activity:          strings.ToLower(strings.TrimSpace(sessionActivityRaw)),
				ActivitySource:    strings.ToLower(strings.TrimSpace(sessionSourceRaw)),
				TmuxAttached:      tmuxAttachedCount > 0,
				TmuxAttachedCount: tmuxAttachedCount,
				StartedAt:         startedAt,
				UpdatedAt:         parseTimestamp(sessionUpdatedRaw),
				Worktree:          worktreePath,
			}
			task.HasTmuxSession = true
		}

		applyGitStatusProjection(&task, gitStatusRaw)

		tasks = append(tasks, task)
		taskIDs = append(taskIDs, task.ID)
		taskIndexByID[task.ID] = len(tasks) - 1
	}
	if err := rows.Err(); err != nil {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
		return nil, err
	}
	if len(tasks) == 0 {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), nil, "include_details", includeDetails, "issue_count", issueCount)
		return tasks, nil
	}
	if err := c.loadDependenciesForTasks(ctx, db, taskIDs, taskIndexByID, tasks); err != nil {
		c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), err, "include_details", includeDetails, "issue_count", issueCount)
		return nil, err
	}
	c.logSQLiteRead(ctx, "issue.runtime_projection", startedAt, len(tasks), nil, "include_details", includeDetails, "issue_count", issueCount)
	return tasks, nil
}

func (c *Client) logSQLiteRead(ctx context.Context, operation string, startedAt time.Time, rowCount int, err error, attrs ...any) {
	if c == nil || c.logger == nil || startedAt.IsZero() {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	base := []any{
		"event", "sqlite.query.completed",
		"service", "azedarach.issue_store",
		"dependency.name", "sqlite",
		"dependency.operation", operation,
		"dependency.duration_ms", time.Since(startedAt).Milliseconds(),
		"outcome", outcome,
		"row_count", rowCount,
	}
	base = append(base, attrs...)
	if err != nil {
		base = append(base, "error_class", "sqlite_query")
		c.logger.WarnContext(ctx, "sqlite query completed", base...)
		return
	}
	c.logger.DebugContext(ctx, "sqlite query completed", base...)
}

func applyGitStatusProjection(task *domain.Task, raw string) {
	if task == nil || strings.TrimSpace(raw) == "" {
		return
	}
	var status git.GitStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return
	}
	task.HasUncommittedChanges = status.HasChanges
	task.HasConflicts = status.HasConflicts
	task.ConflictFiles = append([]string(nil), status.Conflicted...)
	task.GitAdditions = status.GitAdditions
	task.GitDeletions = status.GitDeletions
	task.GitAheadCount = status.GitAheadCount
	task.GitBehindCount = status.GitBehindCount
	if task.GitAdditions == 0 {
		task.GitAdditions = len(status.Added) + len(status.Modified) + len(status.Staged)
	}
	if task.GitDeletions == 0 {
		task.GitDeletions = len(status.Deleted)
	}
}

func taskRuntimeProjectionQuery(projectID string, includeDetails bool, issueIDs ...string) (string, []any) {
	detailSelect := `
			COALESCE(i.description, ''),
			COALESCE(i.notes, ''),
			COALESCE(i.design, ''),
			COALESCE(i.acceptance, ''),`
	if !includeDetails {
		detailSelect = `
			'',
			'',
			'',
			'',`
	}

	seen := map[string]struct{}{}
	trimmedIDs := make([]string, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		if _, ok := seen[issueID]; ok {
			continue
		}
		seen[issueID] = struct{}{}
		trimmedIDs = append(trimmedIDs, issueID)
	}
	filtered := len(trimmedIDs) > 0
	idPlaceholders := ""
	if filtered {
		idPlaceholders = strings.TrimSuffix(strings.Repeat("?,", len(trimmedIDs)), ",")
	}
	sessionFilter := ""
	originFilter := ""
	whereFilter := ""
	if filtered {
		sessionFilter = fmt.Sprintf(" AND issue_id IN (%s)", idPlaceholders)
		originFilter = fmt.Sprintf(" AND issue_id IN (%s)", idPlaceholders)
		whereFilter = fmt.Sprintf(" AND i.id IN (%s)\n", idPlaceholders)
	}

	query := `
		WITH ranked_session AS (
			SELECT
				issue_id,
				COALESCE(NULLIF(TRIM(observed_state), ''), state) AS state,
				COALESCE(started_at, '') AS started_at,
				updated_at,
				session_id,
				COALESCE(activity, '') AS activity,
				COALESCE(activity_source, '') AS activity_source,
				COALESCE(tmux_attached_count, 0) AS tmux_attached_count,
				ROW_NUMBER() OVER (
					PARTITION BY issue_id
					ORDER BY
						CASE COALESCE(NULLIF(TRIM(observed_state), ''), state)
							WHEN 'running' THEN 0
							WHEN 'attached' THEN 0
							WHEN 'paused' THEN 1
							WHEN 'starting' THEN 2
							WHEN 'stopped' THEN 3
							ELSE 4
						END,
						updated_at DESC,
						session_id DESC
				) AS rn
			FROM (` + runtimeSessionProjectionUnionSQL + `)
			WHERE project_id = ?` + sessionFilter + `
		),
		session_pick AS (
			SELECT issue_id, state, started_at, updated_at, activity, activity_source, tmux_attached_count
			FROM ranked_session
			WHERE rn = 1
		),
		origin_pick AS (
			SELECT issue_id, MIN(provider) AS provider
			FROM issue_external_refs
			WHERE deleted_at IS NULL` + originFilter + `
			GROUP BY issue_id
		)
		SELECT
			i.id,
			i.title,
` + detailSelect + `
			COALESCE(i.assignee, ''),
			COALESCE(i.labels_json, '[]'),
			i.estimate,
			i.status,
			i.priority,
			i.issue_type,
			COALESCE(i.implementations_json, '[]'),
			i.created_at,
			i.updated_at,
			COALESCE(sp.state, ''),
			COALESCE(sp.started_at, ''),
			COALESCE(sp.updated_at, ''),
			COALESCE(sp.activity, ''),
			COALESCE(sp.activity_source, ''),
			COALESCE(sp.tmux_attached_count, 0),
			COALESCE(w.path, ''),
			COALESCE(w.git_status_json, ''),
			COALESCE(w.updated_at, ''),
			COALESCE(w.git_status_updated_at, ''),
			COALESCE(o.provider, '')
		FROM issues i
		LEFT JOIN session_pick sp ON sp.issue_id = i.id
		LEFT JOIN daemon_worktree_projections w
			ON w.project_id = ? AND w.issue_id = i.id
		LEFT JOIN origin_pick o ON o.issue_id = i.id
		WHERE i.deleted_at IS NULL
	` + whereFilter
	query += " ORDER BY i.updated_at DESC"

	args := []any{projectID}
	if filtered {
		for _, issueID := range trimmedIDs {
			args = append(args, issueID)
		}
		for _, issueID := range trimmedIDs {
			args = append(args, issueID)
		}
	}
	args = append(args, projectID)
	if filtered {
		for _, issueID := range trimmedIDs {
			args = append(args, issueID)
		}
	}
	return query, args
}

func decodeImplementationsJSON(raw string) []string {
	return decodeStringSliceJSON(raw)
}

func decodeStringSliceJSON(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func parseOptionalTimestamp(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed := parseTimestamp(raw)
	if parsed.IsZero() {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

func newestParsedTimestamp(base time.Time, rawValues ...string) time.Time {
	latest := base
	for _, raw := range rawValues {
		candidate := parseTimestamp(raw)
		if candidate.IsZero() {
			continue
		}
		if latest.IsZero() || candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

func mapRuntimeSessionState(value string) domain.SessionState {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "paused":
		return domain.SessionPaused
	case "running", "attached", "starting":
		return domain.SessionBusy
	case "done":
		return domain.SessionDone
	case "waiting":
		return domain.SessionWaiting
	case "error":
		return domain.SessionError
	default:
		return domain.SessionBusy
	}
}

func (c *Client) loadDependenciesForTasks(
	ctx context.Context,
	db *sql.DB,
	taskIDs []naming.IssueID,
	taskIndexByID map[naming.IssueID]int,
	tasks []domain.Task,
) error {
	const maxPlaceholders = 500
	for start := 0; start < len(taskIDs); start += maxPlaceholders {
		end := start + maxPlaceholders
		if end > len(taskIDs) {
			end = len(taskIDs)
		}
		chunk := taskIDs[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		depArgs := make([]any, 0, len(chunk))
		for _, id := range chunk {
			depArgs = append(depArgs, id.String())
		}

		rows, err := db.QueryContext(ctx, fmt.Sprintf(`
			SELECT issue_id, depends_on_id, dependency_type
			FROM issue_dependencies
			WHERE tombstoned_at IS NULL
				AND issue_id IN (%s)
		`, placeholders), depArgs...)
		if err != nil {
			return err
		}

		for rows.Next() {
			var issueID string
			var dependsOnID string
			var dependencyType string
			if err := rows.Scan(&issueID, &dependsOnID, &dependencyType); err != nil {
				_ = rows.Close()
				return err
			}
			issueIDTyped, err := naming.ParseIssueID(issueID)
			if err != nil {
				continue
			}
			taskIndex, ok := taskIndexByID[issueIDTyped]
			if !ok {
				continue
			}
			task := &tasks[taskIndex]
			if normalizeDependencyType(dependencyType) == string(domain.DependencyParentChild) {
				parentID, err := naming.ParseIssueID(dependsOnID)
				if err != nil {
					continue
				}
				task.ParentID = &parentID
				continue
			}
			dependencyID, err := naming.ParseIssueID(dependsOnID)
			if err != nil {
				continue
			}
			task.Dependencies = append(task.Dependencies, domain.Dependency{
				ID:   dependencyID,
				Type: domain.DependencyType(normalizeDependencyType(dependencyType)),
			})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func normalizeDependencyType(value string) string {
	switch strings.TrimSpace(value) {
	case "blocked_by", "blocked-by":
		return string(domain.DependencyBlockedBy)
	case "blocks":
		return string(domain.DependencyBlocks)
	case "related_to", "related-to", "related":
		return string(domain.DependencyRelatedTo)
	case "parent_child", "parent-child":
		return string(domain.DependencyParentChild)
	case "discovered_from", "discovered-from":
		return string(domain.DependencyDiscovered)
	case "created_by", "created-by", "created_from", "created-from", "created_in", "created-in":
		return string(domain.DependencyCreatedIn)
	default:
		return value
	}
}

func canonicalDependencyType(value string) (string, error) {
	switch normalizeDependencyType(strings.TrimSpace(value)) {
	case string(domain.DependencyBlocks), string(domain.DependencyBlockedBy):
		return string(domain.DependencyBlocks), nil
	case string(domain.DependencyParentChild):
		return string(domain.DependencyParentChild), nil
	case string(domain.DependencyRelatedTo):
		return string(domain.DependencyRelatedTo), nil
	case string(domain.DependencyDiscovered):
		return string(domain.DependencyDiscovered), nil
	case string(domain.DependencyCreatedIn):
		return string(domain.DependencyCreatedIn), nil
	default:
		return "", fmt.Errorf("unsupported dependency type %q", strings.TrimSpace(value))
	}
}

func dependencyIsAcyclic(dependencyType string) bool {
	switch dependencyType {
	case string(domain.DependencyBlocks), string(domain.DependencyParentChild):
		return true
	default:
		return false
	}
}

func (c *Client) wouldCreateDependencyCycle(ctx context.Context, db *sql.DB, issueID, dependsOnID string) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE reachable(id) AS (
			SELECT depends_on_id
			FROM issue_dependencies
			WHERE issue_id = ?
				AND tombstoned_at IS NULL
				AND dependency_type IN ('blocks', 'parent-child', 'parent_child')
			UNION
			SELECT d.depends_on_id
			FROM issue_dependencies d
			JOIN reachable r ON d.issue_id = r.id
			WHERE d.tombstoned_at IS NULL
				AND d.dependency_type IN ('blocks', 'parent-child', 'parent_child')
		)
		SELECT 1
		FROM reachable
		WHERE id = ?
		LIMIT 1
	`, dependsOnID, issueID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	return rows.Next(), rows.Err()
}

func parseTimestamp(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.000Z07:00",
	}
	for _, format := range formats {
		if parsed, err := time.Parse(format, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func marshalOptionalStringSlice(values []string) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return string(payload), nil
}

func marshalStringMap(values map[string]string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		normalized[key] = strings.TrimSpace(value)
	}
	if len(normalized) == 0 {
		return "", nil
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func normalizeExternalIssueRefParams(params UpsertExternalIssueRefParams) (UpsertExternalIssueRefParams, error) {
	params.IssueID = strings.TrimSpace(params.IssueID)
	params.Provider = strings.TrimSpace(params.Provider)
	params.ProviderScope = strings.TrimSpace(params.ProviderScope)
	params.RemoteKey = strings.TrimSpace(params.RemoteKey)
	params.DisplayKey = strings.TrimSpace(params.DisplayKey)
	params.URL = strings.TrimSpace(params.URL)
	if params.IssueID == "" {
		return UpsertExternalIssueRefParams{}, errors.New("issue id is required")
	}
	if params.Provider == "" {
		return UpsertExternalIssueRefParams{}, errors.New("provider is required")
	}
	if params.RemoteKey == "" {
		return UpsertExternalIssueRefParams{}, errors.New("remote key is required")
	}
	return params, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanExternalIssueRef(row scanner) (domain.ExternalIssueRef, error) {
	var ref domain.ExternalIssueRef
	var metadataRaw string
	var createdRaw string
	var updatedRaw string
	if err := row.Scan(&ref.IssueID, &ref.Provider, &ref.ProviderScope, &ref.RemoteKey, &ref.DisplayKey, &ref.URL, &metadataRaw, &createdRaw, &updatedRaw); err != nil {
		return domain.ExternalIssueRef{}, err
	}
	ref.CreatedAt = parseTimestamp(createdRaw)
	ref.UpdatedAt = parseTimestamp(updatedRaw)
	ref.Metadata = map[string]string{}
	if strings.TrimSpace(metadataRaw) != "" {
		if err := json.Unmarshal([]byte(metadataRaw), &ref.Metadata); err != nil {
			return domain.ExternalIssueRef{}, err
		}
	}
	if len(ref.Metadata) == 0 {
		ref.Metadata = nil
	}
	return ref, nil
}

func (c *Client) getMetaValue(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return "", err
	}
	return value, nil
}

func parseNextAlphaIssueIndex(raw string) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func allocateNextAlphaIssueID(startIndex int, existing map[string]struct{}) (string, int) {
	candidateIndex := startIndex
	for {
		candidateID := encodeAlphaIssueIndex(candidateIndex)
		if _, ok := existing[candidateID]; !ok {
			return candidateID, candidateIndex + 1
		}
		candidateIndex++
	}
}

func encodeAlphaIssueIndex(index int) string {
	if index < 0 {
		return "a"
	}
	remaining := index
	encoded := ""
	for remaining >= 0 {
		digit := remaining % 26
		encoded = string(rune('a'+digit)) + encoded
		remaining = (remaining / 26) - 1
	}
	return encoded
}

func resolveDBPath(repoDir string) (string, error) {
	if fromEnv := strings.TrimSpace(os.Getenv("AZEDARACH_DB_PATH")); fromEnv != "" {
		return fromEnv, nil
	}

	startDir := repoDir
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = cwd
	}
	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	baseRoot, err := config.ResolveProjectRoot(absStart)
	if err == nil {
		return filepath.Join(baseRoot, ".azedarach", "azedarach.db"), nil
	}
	return "", fmt.Errorf("resolve project root: %w", err)
}

func (c *Client) wrapError(op string, issueID string, err error) error {
	storeErr := &domain.TaskStoreError{
		Op:  op,
		Err: err,
	}
	if issueID != "" {
		storeErr.TaskID = issueID
	}
	return storeErr
}
