package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/config"
)

const (
	projectionSessionTable  = "daemon_session_projections"
	projectionWorktreeTable = "daemon_worktree_projections"
)

// WorktreeProjection captures cached daemon worktree projection state.
type WorktreeProjection struct {
	ProjectID        string
	IssueID          string
	Path             string
	Branch           string
	UpdatedAt        time.Time
	GitStatusRaw     json.RawMessage
	GitStatusUpdated *time.Time
}

// ProjectionStore persists daemon runtime projections in sqlite.
type ProjectionStore struct {
	dbPath string
	logger *slog.Logger

	mu sync.Mutex
	db *sql.DB
}

// NewProjectionStore returns a sqlite-backed projection store rooted at the repo db path.
func NewProjectionStore(repoDir string, logger *slog.Logger) *ProjectionStore {
	if logger == nil {
		logger = slog.Default()
	}
	dbPath, err := resolveProjectionDBPath(repoDir)
	if err != nil {
		logger.Warn("failed to resolve projection db path", "repo_dir", repoDir, "error", err)
		dbPath = filepath.Join(repoDir, ".azedarach", "azedarach.db")
	}
	return &ProjectionStore{dbPath: dbPath, logger: logger}
}

// NewProjectionStoreAtPath returns a sqlite-backed projection store at dbPath.
func NewProjectionStoreAtPath(dbPath string, logger *slog.Logger) *ProjectionStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProjectionStore{dbPath: dbPath, logger: logger}
}

func resolveProjectionDBPath(repoDir string) (string, error) {
	if override := strings.TrimSpace(os.Getenv("AZEDARACH_DB_PATH")); override != "" {
		return override, nil
	}
	baseRoot, err := config.ResolveProjectRoot(repoDir)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(baseRoot) == "" {
		baseRoot = "."
	}
	return filepath.Join(baseRoot, ".azedarach", "azedarach.db"), nil
}

func (s *ProjectionStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *ProjectionStore) UpsertSession(ctx context.Context, projectID string, session Session) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizedProjectID(projectID)
	if strings.TrimSpace(session.ID) == "" {
		return fmt.Errorf("upsert session projection: missing session id")
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO `+projectionSessionTable+` (
			project_id,
			session_id,
			issue_id,
			state,
			updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, session_id) DO UPDATE SET
			issue_id = excluded.issue_id,
			state = excluded.state,
			updated_at = excluded.updated_at
	`,
		projectID,
		session.ID,
		session.IssueID,
		string(session.State),
		session.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert session projection %s/%s: %w", projectID, session.ID, err)
	}
	return nil
}

func (s *ProjectionStore) DeleteSession(ctx context.Context, projectID, sessionID string) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizedProjectID(projectID)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM `+projectionSessionTable+`
		WHERE project_id = ? AND session_id = ?
	`, projectID, sessionID); err != nil {
		return fmt.Errorf("delete session projection %s/%s: %w", projectID, sessionID, err)
	}
	return nil
}

func (s *ProjectionStore) ListSessions(ctx context.Context, projectID string) ([]Session, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = normalizedProjectID(projectID)
	rows, err := db.QueryContext(ctx, `
		SELECT
			session_id,
			issue_id,
			state,
			updated_at
		FROM `+projectionSessionTable+`
		WHERE project_id = ?
		ORDER BY updated_at DESC, session_id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list session projections %s: %w", projectID, err)
	}
	defer rows.Close()

	sessions := make([]Session, 0)
	for rows.Next() {
		var (
			sessionID string
			issueID   string
			stateRaw  string
			updatedAt string
		)
		if err := rows.Scan(&sessionID, &issueID, &stateRaw, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan session projection row: %w", err)
		}
		parsedUpdatedAt, err := parseProjectionTime(updatedAt)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, Session{
			ID:        sessionID,
			IssueID:   issueID,
			State:     SessionState(stateRaw),
			UpdatedAt: parsedUpdatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session projections: %w", err)
	}
	return sessions, nil
}

func (s *ProjectionStore) UpsertWorktree(ctx context.Context, projection WorktreeProjection) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projection.ProjectID = normalizedProjectID(projection.ProjectID)
	if strings.TrimSpace(projection.IssueID) == "" {
		return fmt.Errorf("upsert worktree projection: missing issue id")
	}
	if projection.UpdatedAt.IsZero() {
		projection.UpdatedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO `+projectionWorktreeTable+` (
			project_id,
			issue_id,
			path,
			branch,
			updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, issue_id) DO UPDATE SET
			path = excluded.path,
			branch = excluded.branch,
			updated_at = excluded.updated_at
	`,
		projection.ProjectID,
		projection.IssueID,
		projection.Path,
		projection.Branch,
		projection.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert worktree projection %s/%s: %w", projection.ProjectID, projection.IssueID, err)
	}
	return nil
}

func (s *ProjectionStore) DeleteWorktree(ctx context.Context, projectID, issueID string) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizedProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM `+projectionWorktreeTable+`
		WHERE project_id = ? AND issue_id = ?
	`, projectID, issueID); err != nil {
		return fmt.Errorf("delete worktree projection %s/%s: %w", projectID, issueID, err)
	}
	return nil
}

func (s *ProjectionStore) ReplaceWorktrees(ctx context.Context, projectID string, projections []WorktreeProjection) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizedProjectID(projectID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace worktree projections: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	activeIssues := make(map[string]struct{}, len(projections))
	for _, projection := range projections {
		if strings.TrimSpace(projection.IssueID) == "" {
			continue
		}
		activeIssues[projection.IssueID] = struct{}{}
		updatedAt := projection.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+projectionWorktreeTable+` (
				project_id,
				issue_id,
				path,
				branch,
				updated_at
			) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(project_id, issue_id) DO UPDATE SET
				path = excluded.path,
				branch = excluded.branch,
				updated_at = excluded.updated_at
		`,
			projectID,
			projection.IssueID,
			projection.Path,
			projection.Branch,
			updatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("upsert worktree projection %s/%s: %w", projectID, projection.IssueID, err)
		}
	}

	if len(activeIssues) == 0 {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM `+projectionWorktreeTable+`
			WHERE project_id = ?
		`, projectID); err != nil {
			return fmt.Errorf("clear worktree projections %s: %w", projectID, err)
		}
	} else {
		args := make([]any, 0, len(activeIssues)+1)
		args = append(args, projectID)
		placeholders := make([]string, 0, len(activeIssues))
		for issueID := range activeIssues {
			placeholders = append(placeholders, "?")
			args = append(args, issueID)
		}
		query := `DELETE FROM ` + projectionWorktreeTable + ` WHERE project_id = ? AND issue_id NOT IN (` + strings.Join(placeholders, ",") + `)`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("delete stale worktree projections %s: %w", projectID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace worktree projections %s: %w", projectID, err)
	}
	tx = nil
	return nil
}

func (s *ProjectionStore) ListWorktrees(ctx context.Context, projectID string) ([]WorktreeProjection, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = normalizedProjectID(projectID)
	rows, err := db.QueryContext(ctx, `
		SELECT
			issue_id,
			path,
			branch,
			updated_at,
			COALESCE(git_status_json, ''),
			COALESCE(git_status_updated_at, '')
		FROM `+projectionWorktreeTable+`
		WHERE project_id = ?
		ORDER BY updated_at DESC, issue_id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list worktree projections %s: %w", projectID, err)
	}
	defer rows.Close()

	projections := make([]WorktreeProjection, 0)
	for rows.Next() {
		var (
			issueID   string
			path      string
			branch    string
			updatedAt string
			statusRaw string
			statusAt  string
		)
		if err := rows.Scan(&issueID, &path, &branch, &updatedAt, &statusRaw, &statusAt); err != nil {
			return nil, fmt.Errorf("scan worktree projection row: %w", err)
		}
		parsedUpdatedAt, err := parseProjectionTime(updatedAt)
		if err != nil {
			return nil, err
		}
		var parsedStatusUpdated *time.Time
		if strings.TrimSpace(statusAt) != "" {
			parsed, err := parseProjectionTime(statusAt)
			if err != nil {
				return nil, err
			}
			parsedStatusUpdated = &parsed
		}
		projections = append(projections, WorktreeProjection{
			ProjectID:        projectID,
			IssueID:          issueID,
			Path:             path,
			Branch:           branch,
			UpdatedAt:        parsedUpdatedAt,
			GitStatusRaw:     json.RawMessage(statusRaw),
			GitStatusUpdated: parsedStatusUpdated,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate worktree projections: %w", err)
	}
	return projections, nil
}

func (s *ProjectionStore) GetWorktreeByPath(ctx context.Context, projectID, worktreePath string) (WorktreeProjection, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return WorktreeProjection{}, false, err
	}
	projectID = normalizedProjectID(projectID)
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" {
		return WorktreeProjection{}, false, nil
	}

	row := db.QueryRowContext(ctx, `
		SELECT
			issue_id,
			path,
			branch,
			updated_at,
			COALESCE(git_status_json, ''),
			COALESCE(git_status_updated_at, '')
		FROM `+projectionWorktreeTable+`
		WHERE project_id = ? AND path = ?
	`, projectID, worktreePath)
	var (
		issueID   string
		path      string
		branch    string
		updatedAt string
		statusRaw string
		statusAt  string
	)
	if err := row.Scan(&issueID, &path, &branch, &updatedAt, &statusRaw, &statusAt); err != nil {
		if err == sql.ErrNoRows {
			return WorktreeProjection{}, false, nil
		}
		return WorktreeProjection{}, false, fmt.Errorf("get worktree projection by path %s/%s: %w", projectID, worktreePath, err)
	}
	parsedUpdatedAt, err := parseProjectionTime(updatedAt)
	if err != nil {
		return WorktreeProjection{}, false, err
	}
	var parsedStatusUpdated *time.Time
	if strings.TrimSpace(statusAt) != "" {
		parsed, err := parseProjectionTime(statusAt)
		if err != nil {
			return WorktreeProjection{}, false, err
		}
		parsedStatusUpdated = &parsed
	}
	return WorktreeProjection{
		ProjectID:        projectID,
		IssueID:          issueID,
		Path:             path,
		Branch:           branch,
		UpdatedAt:        parsedUpdatedAt,
		GitStatusRaw:     json.RawMessage(statusRaw),
		GitStatusUpdated: parsedStatusUpdated,
	}, true, nil
}

func (s *ProjectionStore) UpsertWorktreeGitStatus(ctx context.Context, projectID, issueID string, statusRaw json.RawMessage, updatedAt time.Time) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizedProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return fmt.Errorf("upsert worktree git status: missing issue id")
	}
	if len(statusRaw) == 0 {
		return fmt.Errorf("upsert worktree git status: missing payload")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `
		UPDATE `+projectionWorktreeTable+`
		SET
			git_status_json = ?,
			git_status_updated_at = ?
		WHERE project_id = ? AND issue_id = ?
	`, string(statusRaw), updatedAt.UTC().Format(time.RFC3339Nano), projectID, issueID)
	if err != nil {
		return fmt.Errorf("upsert worktree git status %s/%s: %w", projectID, issueID, err)
	}
	return nil
}

func parseProjectionTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse projection timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

func normalizedProjectID(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "default"
	}
	return projectID
}

func (s *ProjectionStore) dbHandle() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("ensure db directory: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_txlock=immediate", filepath.ToSlash(s.dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	if err := ensureProjectionSchema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.db = db
	return s.db, nil
}

func ensureProjectionSchema(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + projectionSessionTable + ` (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
			ON ` + projectionSessionTable + ` (project_id, issue_id)`,
		`CREATE TABLE IF NOT EXISTS ` + projectionWorktreeTable + ` (
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
			ON ` + projectionWorktreeTable + ` (project_id, path)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure projection schema: %w", err)
		}
	}
	if err := ensureColumn(ctx, db, projectionWorktreeTable, "git_status_json", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, projectionWorktreeTable, "git_status_updated_at", "TEXT"); err != nil {
		return err
	}
	return nil
}

func ensureColumn(ctx context.Context, db *sql.DB, tableName, columnName, columnDDL string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+tableName+`)`)
	if err != nil {
		return fmt.Errorf("read table info for %s: %w", tableName, err)
	}
	defer rows.Close()

	exists := false
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan table info for %s: %w", tableName, err)
		}
		if name == columnName {
			exists = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table info for %s: %w", tableName, err)
	}
	if exists {
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE `+tableName+` ADD COLUMN `+columnName+` `+columnDDL); err != nil {
		return fmt.Errorf("add column %s.%s: %w", tableName, columnName, err)
	}
	return nil
}
