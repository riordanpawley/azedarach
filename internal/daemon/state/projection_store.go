package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/config"
)

const (
	sessionStateTable  = "daemon_session_projections"
	worktreeStateTable = "daemon_worktree_projections"
)

// WorktreeState captures durable daemon worktree state stored in sqlite.
type WorktreeState struct {
	ProjectID        string
	IssueID          string
	Path             string
	Branch           string
	UpdatedAt        time.Time
	GitStatusRaw     json.RawMessage
	GitStatusUpdated *time.Time
}

// RuntimeStateStore persists daemon-owned session/worktree state in sqlite.
type RuntimeStateStore struct {
	dbPath string
	logger *slog.Logger

	mu sync.Mutex
	db *sql.DB
}

// SessionStateReader loads persisted session state rows for a project.
type SessionStateReader interface {
	ListSessionStates(context.Context, string) ([]Session, error)
	GetSessionState(context.Context, string, string) (Session, bool, error)
	GetSessionStateByIssueID(context.Context, string, string) (Session, bool, error)
}

// SessionStateWriter mutates persisted session state rows for a project.
type SessionStateWriter interface {
	UpsertSessionState(context.Context, string, Session) error
	DeleteSessionState(context.Context, string, string) error
	ReplaceSessionStates(context.Context, string, []Session) error
}

// SessionStateStore groups the durable session-state read/write contract.
type SessionStateStore interface {
	SessionStateReader
	SessionStateWriter
}

// WorktreeStateReader loads persisted worktree state rows for a project.
type WorktreeStateReader interface {
	ListWorktreeStates(context.Context, string) ([]WorktreeState, error)
	GetWorktreeStateByPath(context.Context, string, string) (WorktreeState, bool, error)
	GetWorktreeStateByIssueID(context.Context, string, string) (WorktreeState, bool, error)
}

// WorktreeStateWriter mutates persisted worktree state rows for a project.
type WorktreeStateWriter interface {
	UpsertWorktreeState(context.Context, WorktreeState) error
	DeleteWorktreeState(context.Context, string, string) error
	ReplaceWorktreeStates(context.Context, string, []WorktreeState) error
	UpsertWorktreeStateGitStatus(context.Context, string, string, json.RawMessage, time.Time) error
}

// WorktreeStateStore groups the durable worktree-state read/write contract.
type WorktreeStateStore interface {
	WorktreeStateReader
	WorktreeStateWriter
}

var (
	_ SessionStateStore  = (*RuntimeStateStore)(nil)
	_ WorktreeStateStore = (*RuntimeStateStore)(nil)
)

// NewRuntimeStateStore returns a sqlite-backed daemon runtime-state store rooted at the repo db path.
func NewRuntimeStateStore(repoDir string, logger *slog.Logger) *RuntimeStateStore {
	if logger == nil {
		logger = slog.Default()
	}
	dbPath, err := resolveRuntimeStateDBPath(repoDir)
	if err != nil {
		logger.Warn("failed to resolve runtime state db path", "repo_dir", repoDir, "error", err)
		dbPath = filepath.Join(repoDir, ".azedarach", "azedarach.db")
	}
	return &RuntimeStateStore{dbPath: dbPath, logger: logger}
}

// NewRuntimeStateStoreAtPath returns a sqlite-backed daemon runtime-state store at dbPath.
func NewRuntimeStateStoreAtPath(dbPath string, logger *slog.Logger) *RuntimeStateStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &RuntimeStateStore{dbPath: dbPath, logger: logger}
}

func resolveRuntimeStateDBPath(repoDir string) (string, error) {
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

func (s *RuntimeStateStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *RuntimeStateStore) UpsertSessionState(ctx context.Context, projectID string, session Session) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizedProjectID(projectID)
	if strings.TrimSpace(session.ID) == "" {
		return fmt.Errorf("upsert session state: missing session id")
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO `+sessionStateTable+` (
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
		return fmt.Errorf("upsert session state %s/%s: %w", projectID, session.ID, err)
	}
	return nil
}

func (s *RuntimeStateStore) DeleteSessionState(ctx context.Context, projectID, sessionID string) error {
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
		DELETE FROM `+sessionStateTable+`
		WHERE project_id = ? AND session_id = ?
	`, projectID, sessionID); err != nil {
		return fmt.Errorf("delete session state %s/%s: %w", projectID, sessionID, err)
	}
	return nil
}

func (s *RuntimeStateStore) ReplaceSessionStates(ctx context.Context, projectID string, sessions []Session) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizedProjectID(projectID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace session state rows: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	activeSessions := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		activeSessions[sessionID] = struct{}{}
		updatedAt := session.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+sessionStateTable+` (
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
			sessionID,
			session.IssueID,
			string(session.State),
			updatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("upsert session state %s/%s: %w", projectID, sessionID, err)
		}
	}

	if len(activeSessions) == 0 {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM `+sessionStateTable+`
			WHERE project_id = ?
		`, projectID); err != nil {
			return fmt.Errorf("clear session state rows %s: %w", projectID, err)
		}
	} else {
		args := make([]any, 0, len(activeSessions)+1)
		args = append(args, projectID)
		placeholders := make([]string, 0, len(activeSessions))
		for sessionID := range activeSessions {
			placeholders = append(placeholders, "?")
			args = append(args, sessionID)
		}
		query := `DELETE FROM ` + sessionStateTable + ` WHERE project_id = ? AND session_id NOT IN (` + strings.Join(placeholders, ",") + `)`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("delete stale session state rows %s: %w", projectID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace session state rows %s: %w", projectID, err)
	}
	tx = nil
	return nil
}

func (s *RuntimeStateStore) ListSessionStates(ctx context.Context, projectID string) ([]Session, error) {
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
		FROM `+sessionStateTable+`
		WHERE project_id = ?
		ORDER BY updated_at DESC, session_id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list session state rows %s: %w", projectID, err)
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
			return nil, fmt.Errorf("scan session state row: %w", err)
		}
		parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
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
		return nil, fmt.Errorf("iterate session state rows: %w", err)
	}
	return sessions, nil
}

func (s *RuntimeStateStore) GetSessionState(ctx context.Context, projectID, sessionID string) (Session, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Session{}, false, err
	}
	projectID = normalizedProjectID(projectID)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Session{}, false, nil
	}

	row := db.QueryRowContext(ctx, `
		SELECT
			session_id,
			issue_id,
			state,
			updated_at
		FROM `+sessionStateTable+`
		WHERE project_id = ? AND session_id = ?
	`, projectID, sessionID)
	var (
		rowSessionID string
		issueID      string
		stateRaw     string
		updatedAt    string
	)
	if err := row.Scan(&rowSessionID, &issueID, &stateRaw, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return Session{}, false, nil
		}
		return Session{}, false, fmt.Errorf("get session state %s/%s: %w", projectID, sessionID, err)
	}
	parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
	if err != nil {
		return Session{}, false, err
	}
	return Session{
		ID:        rowSessionID,
		IssueID:   issueID,
		State:     SessionState(stateRaw),
		UpdatedAt: parsedUpdatedAt,
	}, true, nil
}

func (s *RuntimeStateStore) GetSessionStateByIssueID(ctx context.Context, projectID, issueID string) (Session, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Session{}, false, err
	}
	projectID = normalizedProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return Session{}, false, nil
	}

	row := db.QueryRowContext(ctx, `
		SELECT
			session_id,
			issue_id,
			state,
			updated_at
		FROM `+sessionStateTable+`
		WHERE project_id = ? AND issue_id = ?
		ORDER BY updated_at DESC, session_id ASC
		LIMIT 1
	`, projectID, issueID)
	var (
		sessionID string
		rowIssue  string
		stateRaw  string
		updatedAt string
	)
	if err := row.Scan(&sessionID, &rowIssue, &stateRaw, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return Session{}, false, nil
		}
		return Session{}, false, fmt.Errorf("get session state by issue %s/%s: %w", projectID, issueID, err)
	}
	parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
	if err != nil {
		return Session{}, false, err
	}
	return Session{
		ID:        sessionID,
		IssueID:   rowIssue,
		State:     SessionState(stateRaw),
		UpdatedAt: parsedUpdatedAt,
	}, true, nil
}

// ListProjectIDs returns all distinct project IDs referenced by persisted runtime-state rows.
func (s *RuntimeStateStore) ListProjectIDs(ctx context.Context) ([]string, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT project_id FROM `+sessionStateTable+`
		UNION
		SELECT project_id FROM `+worktreeStateTable+`
	`)
	if err != nil {
		return nil, fmt.Errorf("list runtime-state project ids: %w", err)
	}
	defer rows.Close()

	seen := map[string]struct{}{}
	projectIDs := make([]string, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan runtime-state project id: %w", err)
		}
		projectID := normalizedProjectID(raw)
		if _, exists := seen[projectID]; exists {
			continue
		}
		seen[projectID] = struct{}{}
		projectIDs = append(projectIDs, projectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runtime-state project ids: %w", err)
	}
	slices.Sort(projectIDs)
	return projectIDs, nil
}

func (s *RuntimeStateStore) UpsertWorktreeState(ctx context.Context, worktreeState WorktreeState) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	worktreeState.ProjectID = normalizedProjectID(worktreeState.ProjectID)
	if strings.TrimSpace(worktreeState.IssueID) == "" {
		return fmt.Errorf("upsert worktree state: missing issue id")
	}
	if worktreeState.UpdatedAt.IsZero() {
		worktreeState.UpdatedAt = time.Now().UTC()
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO `+worktreeStateTable+` (
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
		worktreeState.ProjectID,
		worktreeState.IssueID,
		worktreeState.Path,
		worktreeState.Branch,
		worktreeState.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert worktree state %s/%s: %w", worktreeState.ProjectID, worktreeState.IssueID, err)
	}
	return nil
}

func (s *RuntimeStateStore) DeleteWorktreeState(ctx context.Context, projectID, issueID string) error {
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
		DELETE FROM `+worktreeStateTable+`
		WHERE project_id = ? AND issue_id = ?
	`, projectID, issueID); err != nil {
		return fmt.Errorf("delete worktree state %s/%s: %w", projectID, issueID, err)
	}
	return nil
}

func (s *RuntimeStateStore) ReplaceWorktreeStates(ctx context.Context, projectID string, worktreeStates []WorktreeState) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID = normalizedProjectID(projectID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace worktree state rows: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	activeIssues := make(map[string]struct{}, len(worktreeStates))
	for _, worktreeState := range worktreeStates {
		if strings.TrimSpace(worktreeState.IssueID) == "" {
			continue
		}
		activeIssues[worktreeState.IssueID] = struct{}{}
		updatedAt := worktreeState.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+worktreeStateTable+` (
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
			worktreeState.IssueID,
			worktreeState.Path,
			worktreeState.Branch,
			updatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("upsert worktree state %s/%s: %w", projectID, worktreeState.IssueID, err)
		}
	}

	if len(activeIssues) == 0 {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM `+worktreeStateTable+`
			WHERE project_id = ?
		`, projectID); err != nil {
			return fmt.Errorf("clear worktree state rows %s: %w", projectID, err)
		}
	} else {
		args := make([]any, 0, len(activeIssues)+1)
		args = append(args, projectID)
		placeholders := make([]string, 0, len(activeIssues))
		for issueID := range activeIssues {
			placeholders = append(placeholders, "?")
			args = append(args, issueID)
		}
		query := `DELETE FROM ` + worktreeStateTable + ` WHERE project_id = ? AND issue_id NOT IN (` + strings.Join(placeholders, ",") + `)`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("delete stale worktree state rows %s: %w", projectID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace worktree state rows %s: %w", projectID, err)
	}
	tx = nil
	return nil
}

func (s *RuntimeStateStore) ListWorktreeStates(ctx context.Context, projectID string) ([]WorktreeState, error) {
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
		FROM `+worktreeStateTable+`
		WHERE project_id = ?
		ORDER BY updated_at DESC, issue_id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list worktree state rows %s: %w", projectID, err)
	}
	defer rows.Close()

	worktreeStates := make([]WorktreeState, 0)
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
			return nil, fmt.Errorf("scan worktree state row: %w", err)
		}
		parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
		if err != nil {
			return nil, err
		}
		var parsedStatusUpdated *time.Time
		if strings.TrimSpace(statusAt) != "" {
			parsed, err := parseRuntimeStateTime(statusAt)
			if err != nil {
				return nil, err
			}
			parsedStatusUpdated = &parsed
		}
		worktreeStates = append(worktreeStates, WorktreeState{
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
		return nil, fmt.Errorf("iterate worktree state rows: %w", err)
	}
	return worktreeStates, nil
}

func (s *RuntimeStateStore) GetWorktreeStateByPath(ctx context.Context, projectID, worktreePath string) (WorktreeState, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return WorktreeState{}, false, err
	}
	projectID = normalizedProjectID(projectID)
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" {
		return WorktreeState{}, false, nil
	}

	row := db.QueryRowContext(ctx, `
		SELECT
			issue_id,
			path,
			branch,
			updated_at,
			COALESCE(git_status_json, ''),
			COALESCE(git_status_updated_at, '')
		FROM `+worktreeStateTable+`
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
			return WorktreeState{}, false, nil
		}
		return WorktreeState{}, false, fmt.Errorf("get worktree state by path %s/%s: %w", projectID, worktreePath, err)
	}
	parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
	if err != nil {
		return WorktreeState{}, false, err
	}
	var parsedStatusUpdated *time.Time
	if strings.TrimSpace(statusAt) != "" {
		parsed, err := parseRuntimeStateTime(statusAt)
		if err != nil {
			return WorktreeState{}, false, err
		}
		parsedStatusUpdated = &parsed
	}
	return WorktreeState{
		ProjectID:        projectID,
		IssueID:          issueID,
		Path:             path,
		Branch:           branch,
		UpdatedAt:        parsedUpdatedAt,
		GitStatusRaw:     json.RawMessage(statusRaw),
		GitStatusUpdated: parsedStatusUpdated,
	}, true, nil
}

func (s *RuntimeStateStore) GetWorktreeStateByIssueID(ctx context.Context, projectID, issueID string) (WorktreeState, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return WorktreeState{}, false, err
	}
	projectID = normalizedProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return WorktreeState{}, false, nil
	}

	row := db.QueryRowContext(ctx, `
		SELECT
			issue_id,
			path,
			branch,
			updated_at,
			COALESCE(git_status_json, ''),
			COALESCE(git_status_updated_at, '')
		FROM `+worktreeStateTable+`
		WHERE project_id = ? AND issue_id = ?
	`, projectID, issueID)
	var (
		rowIssueID string
		path       string
		branch     string
		updatedAt  string
		statusRaw  string
		statusAt   string
	)
	if err := row.Scan(&rowIssueID, &path, &branch, &updatedAt, &statusRaw, &statusAt); err != nil {
		if err == sql.ErrNoRows {
			return WorktreeState{}, false, nil
		}
		return WorktreeState{}, false, fmt.Errorf("get worktree state by issue %s/%s: %w", projectID, issueID, err)
	}
	parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
	if err != nil {
		return WorktreeState{}, false, err
	}
	var parsedStatusUpdated *time.Time
	if strings.TrimSpace(statusAt) != "" {
		parsed, err := parseRuntimeStateTime(statusAt)
		if err != nil {
			return WorktreeState{}, false, err
		}
		parsedStatusUpdated = &parsed
	}
	return WorktreeState{
		ProjectID:        projectID,
		IssueID:          rowIssueID,
		Path:             path,
		Branch:           branch,
		UpdatedAt:        parsedUpdatedAt,
		GitStatusRaw:     json.RawMessage(statusRaw),
		GitStatusUpdated: parsedStatusUpdated,
	}, true, nil
}

func (s *RuntimeStateStore) UpsertWorktreeStateGitStatus(ctx context.Context, projectID, issueID string, statusRaw json.RawMessage, updatedAt time.Time) error {
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
	result, err := db.ExecContext(ctx, `
		UPDATE `+worktreeStateTable+`
		SET
			git_status_json = ?,
			git_status_updated_at = ?
		WHERE project_id = ? AND issue_id = ?
	`, string(statusRaw), updatedAt.UTC().Format(time.RFC3339Nano), projectID, issueID)
	if err != nil {
		return fmt.Errorf("upsert worktree git status %s/%s: %w", projectID, issueID, err)
	}
	if err := requireAffectedRows(result, 1, "upsert worktree git status", projectID, issueID); err != nil {
		return err
	}
	return nil
}

func requireAffectedRows(result sql.Result, want int64, action, projectID, rowID string) error {
	if result == nil {
		return fmt.Errorf("%s %s/%s: missing exec result", action, projectID, rowID)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s %s/%s: read affected rows: %w", action, projectID, rowID, err)
	}
	if affected != want {
		return fmt.Errorf("%s %s/%s: expected %d affected row(s), got %d", action, projectID, rowID, want, affected)
	}
	return nil
}

func parseRuntimeStateTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse runtime-state timestamp %q: %w", value, err)
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

func (s *RuntimeStateStore) dbHandle() (*sql.DB, error) {
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
	if err := ensureRuntimeStateSchema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.db = db
	return s.db, nil
}

func ensureRuntimeStateSchema(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + sessionStateTable + ` (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			state TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
			ON ` + sessionStateTable + ` (project_id, issue_id)`,
		`CREATE TABLE IF NOT EXISTS ` + worktreeStateTable + ` (
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
			ON ` + worktreeStateTable + ` (project_id, path)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure runtime-state schema: %w", err)
		}
	}
	if err := ensureColumn(ctx, db, worktreeStateTable, "git_status_json", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, worktreeStateTable, "git_status_updated_at", "TEXT"); err != nil {
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
