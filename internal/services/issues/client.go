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

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type dependencyRemovalConfirmationKey struct{}

// ErrDependencyRemovalConfirmationRequired is returned when a removal that can
// unblock or retarget workflow is attempted without explicit confirmation.
var ErrDependencyRemovalConfirmationRequired = errors.New("explicit confirmation required")
var ErrDeleteBlockedByRuntimeAttachments = errors.New("delete blocked: task has worktree or active session")

// WithDependencyRemovalConfirmation marks a context as explicitly confirming a
// dependency removal that can unblock or retarget workflow.
func WithDependencyRemovalConfirmation(ctx context.Context) context.Context {
	return context.WithValue(ctx, dependencyRemovalConfirmationKey{}, true)
}

func hasDependencyRemovalConfirmation(ctx context.Context) bool {
	confirmed, _ := ctx.Value(dependencyRemovalConfirmationKey{}).(bool)
	return confirmed
}

const (
	nextAlphaIssueIndexMetaKey = "issue:id_next_alpha_index"
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

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_txlock=immediate", filepath.ToSlash(c.dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, c.wrapError("open-db", "", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
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
	if err := c.ensureRuntimeProjectionSchema(db); err != nil {
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
			started_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
			ON daemon_session_projections (project_id, issue_id)`,
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
	return nil
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
	_, err := db.Exec(`
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
	return nil
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

// Create inserts a new issue and returns its generated id.
func (c *Client) Create(ctx context.Context, params CreateTaskParams) (string, error) {
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
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
			VALUES (?, ?, ?, NULL)
			ON CONFLICT(issue_id, depends_on_id, dependency_type)
			DO UPDATE SET tombstoned_at = NULL
		`, issueID, parentID, string(domain.DependencyParentChild)); err != nil {
			return "", c.wrapError("create", issueID, err)
		}
		if err := c.reopenClosedParentForActiveChild(ctx, tx, issueID, parentID); err != nil {
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

// AddDependency creates or restores a dependency edge between two issues.
func (c *Client) AddDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
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

	if _, err := db.ExecContext(ctx, `
		INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
		VALUES (?, ?, ?, NULL)
		ON CONFLICT(issue_id, depends_on_id, dependency_type)
		DO UPDATE SET tombstoned_at = NULL
	`, issueID, dependsOnID, canonicalType); err != nil {
		return c.wrapError("add-dependency", issueID, err)
	}
	if canonicalType == string(domain.DependencyParentChild) {
		if err := c.reopenClosedParentForActiveChild(ctx, db, issueID, dependsOnID); err != nil {
			return c.wrapError("add-dependency", issueID, err)
		}
	}

	return nil
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

func (c *Client) issueExists(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `
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

// RemoveDependency tombstones a dependency edge between two issues.
func (c *Client) RemoveDependency(ctx context.Context, issueID, dependsOnID, dependencyType string) error {
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

	res, err := db.ExecContext(ctx, `
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

	return nil
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
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
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
	if err := tx.QueryRowContext(ctx, `
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
					FROM daemon_session_projections
					WHERE issue_id = ? AND LOWER(TRIM(COALESCE(state, ''))) <> 'stopped'
				)
				THEN 1 ELSE 0
			END
		)
	`, id, id).Scan(&runtimeAttachmentCount); err != nil {
		return c.wrapError("delete", id, err)
	}
	if runtimeAttachmentCount > 0 {
		return c.wrapError("delete", id, ErrDeleteBlockedByRuntimeAttachments)
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
	if err := tx.Commit(); err != nil {
		return c.wrapError("delete", id, err)
	}
	tx = nil
	return nil
}

// Archive soft-deletes an issue.
func (c *Client) Archive(ctx context.Context, id string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := db.ExecContext(ctx, `
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
	return nil
}

type UpdateTaskParams struct {
	Title           string
	Description     string
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

func (c *Client) queryTasks(ctx context.Context, db *sql.DB, query string, args ...any) ([]domain.Task, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, 32)
	taskIDs := make([]string, 0, 32)
	taskIndexByID := map[string]int{}

	for rows.Next() {
		task := domain.Task{}
		var createdRaw string
		var updatedRaw string
		var statusRaw string
		var typeRaw string
		var priorityRaw int
		var implementationsRaw string
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Description,
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

func (c *Client) queryTasksWithRuntime(ctx context.Context, db *sql.DB, projectID string) ([]domain.Task, error) {
	rows, err := db.QueryContext(ctx, `
		WITH ranked_session AS (
			SELECT
				issue_id,
				state,
				COALESCE(started_at, '') AS started_at,
				updated_at,
				session_id,
				ROW_NUMBER() OVER (
					PARTITION BY issue_id
					ORDER BY
						CASE state
							WHEN 'attached' THEN 0
							WHEN 'paused' THEN 1
							WHEN 'starting' THEN 2
							WHEN 'stopped' THEN 3
							ELSE 4
						END,
						updated_at DESC,
						session_id DESC
				) AS rn
			FROM daemon_session_projections
			WHERE project_id = ?
		),
		session_pick AS (
			SELECT issue_id, state, started_at, updated_at
			FROM ranked_session
			WHERE rn = 1
		)
		SELECT
			i.id,
			i.title,
			COALESCE(i.description, ''),
			i.status,
			i.priority,
			i.issue_type,
			COALESCE(i.implementations_json, '[]'),
			i.created_at,
			i.updated_at,
			COALESCE(sp.state, ''),
			COALESCE(sp.started_at, ''),
			COALESCE(sp.updated_at, ''),
			COALESCE(w.path, ''),
			COALESCE(w.git_status_json, '')
		FROM issues i
		LEFT JOIN session_pick sp ON sp.issue_id = i.id
		LEFT JOIN daemon_worktree_projections w
			ON w.project_id = ? AND w.issue_id = i.id
		WHERE i.deleted_at IS NULL
		ORDER BY i.updated_at DESC
	`, projectID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, 32)
	taskIDs := make([]string, 0, 32)
	taskIndexByID := map[string]int{}

	for rows.Next() {
		task := domain.Task{}
		var (
			createdRaw         string
			updatedRaw         string
			statusRaw          string
			typeRaw            string
			priorityRaw        int
			implementationsRaw string
			sessionStateRaw    string
			sessionStartedRaw  string
			sessionUpdatedRaw  string
			worktreePath       string
			gitStatusRaw       string
		)
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Description,
			&statusRaw,
			&priorityRaw,
			&typeRaw,
			&implementationsRaw,
			&createdRaw,
			&updatedRaw,
			&sessionStateRaw,
			&sessionStartedRaw,
			&sessionUpdatedRaw,
			&worktreePath,
			&gitStatusRaw,
		); err != nil {
			return nil, err
		}

		task.Status = domain.Status(statusRaw)
		task.Priority = domain.Priority(priorityRaw)
		task.Type = domain.TaskType(typeRaw)
		task.CreatedAt = parseTimestamp(createdRaw)
		task.UpdatedAt = parseTimestamp(updatedRaw)
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
				issueID, err := naming.ParseIssueID(task.ID)
				if err != nil {
					continue
				}
				task.Session = &domain.Session{
					IssueID:   issueID,
					State:     mapRuntimeSessionState(sessionStateRaw),
					StartedAt: startedAt,
					Worktree:  worktreePath,
				}
			task.HasTmuxSession = true
		}

		if strings.TrimSpace(gitStatusRaw) != "" {
			var status git.GitStatus
			if err := json.Unmarshal([]byte(gitStatusRaw), &status); err == nil {
				task.HasUncommittedChanges = status.HasChanges
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
		}

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

func decodeImplementationsJSON(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var impls []string
	if err := json.Unmarshal([]byte(raw), &impls); err != nil {
		return nil
	}
	if len(impls) == 0 {
		return nil
	}
	return impls
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

func mapRuntimeSessionState(value string) domain.SessionState {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "paused":
		return domain.SessionPaused
	case "attached", "starting":
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
	taskIDs []string,
	taskIndexByID map[string]int,
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
			depArgs = append(depArgs, id)
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
			taskIndex, ok := taskIndexByID[issueID]
			if !ok {
				continue
			}
			task := &tasks[taskIndex]
			if normalizeDependencyType(dependencyType) == string(domain.DependencyParentChild) {
				parentID := dependsOnID
				task.ParentID = &parentID
				continue
			}
			task.Dependencies = append(task.Dependencies, domain.Dependency{
				ID:   dependsOnID,
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
