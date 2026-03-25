package issues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type dependencyRemovalConfirmationKey struct{}

// ErrDependencyRemovalConfirmationRequired is returned when a removal that can
// unblock or retarget workflow is attempted without explicit confirmation.
var ErrDependencyRemovalConfirmationRequired = errors.New("explicit confirmation required")

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

	openOnce sync.Once
	db       *sql.DB
	openErr  error
}

// NewClient creates a SQLite-backed issue store client rooted at the repository.
func NewClient(repoDir string, logger *slog.Logger) *Client {
	dbPath, err := resolveDBPath(repoDir)
	if err != nil {
		// Keep daemon bootstrap non-fatal and surface DB errors on first operation.
		if logger != nil {
			logger.Warn("failed to resolve azedarach issue database path", "repoDir", repoDir, "error", err)
		}
		dbPath = filepath.Join(repoDir, ".azedarach", "azedarach.db")
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
	c.openOnce.Do(func() {
		dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", filepath.ToSlash(c.dbPath))
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			c.openErr = err
			return
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := db.Ping(); err != nil {
			_ = db.Close()
			c.openErr = err
			return
		}
		if err := c.normalizeDependencyEnumRows(db); err != nil {
			_ = db.Close()
			c.openErr = err
			return
		}
		c.db = db
	})
	if c.openErr != nil {
		return nil, c.wrapError("open-db", "", c.openErr)
	}
	return c.db, nil
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

// CreateTaskParams contains parameters for creating a new issue.
type CreateTaskParams struct {
	Title       string
	Description string
	Type        domain.TaskType
	Priority    domain.Priority
	ParentID    *string
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)
	`, issueID, params.Title, nullableString(params.Description), string(domain.StatusOpen), int(params.Priority), string(issueType), now, now); err != nil {
		return "", c.wrapError("create", issueID, err)
	}

	if params.ParentID != nil && strings.TrimSpace(*params.ParentID) != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
			VALUES (?, ?, ?, NULL)
			ON CONFLICT(issue_id, depends_on_id, dependency_type)
			DO UPDATE SET tombstoned_at = NULL
		`, issueID, strings.TrimSpace(*params.ParentID), string(domain.DependencyParentChild)); err != nil {
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
	Title       string
	Description string
	Type        domain.TaskType
	Priority    domain.Priority
}

// UpdateDetails updates non-status issue metadata.
func (c *Client) UpdateDetails(ctx context.Context, id string, params UpdateTaskParams) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Description,
			&statusRaw,
			&priorityRaw,
			&typeRaw,
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

	dependencyRows, err := db.QueryContext(ctx, `
		SELECT issue_id, depends_on_id, dependency_type
		FROM issue_dependencies
		WHERE tombstoned_at IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer dependencyRows.Close()

	for dependencyRows.Next() {
		var issueID string
		var dependsOnID string
		var dependencyType string
		if err := dependencyRows.Scan(&issueID, &dependsOnID, &dependencyType); err != nil {
			return nil, err
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
	if err := dependencyRows.Err(); err != nil {
		return nil, err
	}

	return tasks, nil
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

	baseRoot, err := resolveBaseGitRoot(absStart)
	if err == nil {
		return filepath.Join(baseRoot, ".azedarach", "azedarach.db"), nil
	}

	// Fallback path search keeps existing repositories usable even when git
	// common-dir discovery is unavailable in constrained runtime environments.
	if fallbackPath, fallbackErr := resolveDBPathBySearch(absStart); fallbackErr == nil {
		return fallbackPath, nil
	}
	return "", fmt.Errorf("resolve base git root: %w", err)
}

func resolveBaseGitRoot(startDir string) (string, error) {
	if root, err := resolveBaseGitRootWithGitExec(startDir); err == nil {
		return root, nil
	}
	if root, err := resolveBaseGitRootFromGitMarker(startDir); err == nil {
		return root, nil
	}
	return "", fmt.Errorf("unable to resolve git root from %s", startDir)
}

func resolveBaseGitRootWithGitExec(startDir string) (string, error) {
	out, err := exec.Command("git", "-C", startDir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("resolve git common dir: %w", err)
	}
	return baseGitRootFromCommonDir(startDir, strings.TrimSpace(string(out)))
}

func resolveBaseGitRootFromGitMarker(startDir string) (string, error) {
	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for dir := absStart; ; dir = filepath.Dir(dir) {
		marker := filepath.Join(dir, ".git")
		info, statErr := os.Stat(marker)
		if statErr == nil {
			if info.IsDir() {
				return dir, nil
			}

			content, readErr := os.ReadFile(marker)
			if readErr != nil {
				return "", fmt.Errorf("read git marker %s: %w", marker, readErr)
			}
			gitDir, parseErr := parseGitDirPointer(string(content))
			if parseErr != nil {
				return "", fmt.Errorf("parse git marker %s: %w", marker, parseErr)
			}
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Clean(filepath.Join(dir, gitDir))
			}
			return baseGitRootFromCommonDir(dir, gitDir)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	return "", fmt.Errorf("no .git marker found for %s", absStart)
}

func parseGitDirPointer(content string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "gitdir:") {
			continue
		}
		target := strings.TrimSpace(strings.TrimPrefix(trimmed, "gitdir:"))
		if target == "" {
			return "", fmt.Errorf("empty gitdir target")
		}
		return target, nil
	}
	return "", fmt.Errorf("missing gitdir pointer")
}

func resolveDBPathFromGitCommonDir(startDir, gitCommonDir string) (string, error) {
	baseRoot, err := baseGitRootFromCommonDir(startDir, gitCommonDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(baseRoot, ".azedarach", "azedarach.db"), nil
}

func baseGitRootFromCommonDir(startDir, commonDir string) (string, error) {
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return "", fmt.Errorf("resolve git common dir: empty output")
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Clean(filepath.Join(startDir, commonDir))
	}
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir), nil
	}

	// Worktree forms may return nested paths like:
	//   /path/to/repo/.git/worktrees/<name>
	// Normalize by locating the `.git` segment and deriving repo root from it.
	segments := strings.Split(filepath.ToSlash(commonDir), "/")
	for i, seg := range segments {
		if seg != ".git" {
			continue
		}
		gitDir := filepath.FromSlash(strings.Join(segments[:i+1], "/"))
		return filepath.Dir(gitDir), nil
	}

	return "", fmt.Errorf("resolve git common dir: expected .git path, got %s", commonDir)
}

func resolveDBPathBySearch(startDir string) (string, error) {
	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for dir := absStart; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, ".azedarach", "azedarach.db")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		candidate := filepath.Join(homeDir, ".azedarach", "azedarach.db")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("unable to locate .azedarach/azedarach.db starting at %s", absStart)
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
