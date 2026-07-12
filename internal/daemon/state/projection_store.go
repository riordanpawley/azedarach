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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/observability/tracesqlite"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

const (
	sessionStateTable               = "daemon_session_projections"
	sessionObservationTable         = "daemon_session_observations"
	sessionActivityTable            = "daemon_session_activity_evidence"
	physicalSessionObservationTable = "daemon_physical_session_observations"
	worktreeStateTable              = "daemon_worktree_projections"
	orchestratorLeaseTable          = "daemon_orchestrator_scope_leases"
	advisorSessionTable             = "daemon_advisor_sessions"
	orchestratorLoopTable           = "daemon_orchestrator_loop_checkpoints"
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

// SessionActivityEvidence records durable activity evidence before it is
// materialized into the canonical session projection.
type SessionActivityEvidence struct {
	ProjectID       string
	SessionID       string
	IssueID         string
	Activity        string
	ActivitySource  string
	SourceSessionID string
	Agent           string
	Hook            string
	Event           string
	ObservedAt      time.Time
	UpdatedAt       time.Time
}

// PhysicalSessionObservation records tmux/hook facts independently from any
// desired logical worker, advisor, or orchestrator intent.
type PhysicalSessionObservation struct {
	ProjectID      string
	SessionID      string
	ObservedState  SessionState
	Activity       string
	ActivitySource string
	UpdatedAt      time.Time
}

// ApplyPhysicalSessionObservation atomically records a monotonic physical
// runtime fact and fans its observed fields into every existing logical intent
// linked by physical session ID. It never creates desired intent authority and
// never changes an intent's desired state or typed identity. Returned rows are
// safe to publish because they are produced only after the transaction commits.
func (s *RuntimeStateStore) ApplyPhysicalSessionObservation(ctx context.Context, observation PhysicalSessionObservation) ([]Session, bool, error) {
	var changed []Session
	var applied bool
	err := sqliteutil.WithWriteLock(s.dbPath, func() error {
		db, err := s.dbHandle()
		if err != nil {
			return err
		}
		observation, err = normalizePhysicalSessionObservation(observation)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin physical session observation fan-out: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		result, err := tx.ExecContext(ctx, `INSERT INTO `+physicalSessionObservationTable+`
			(project_id,session_id,observed_state,activity,activity_source,updated_at,observed_version)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(project_id,session_id) DO UPDATE SET
				observed_state=excluded.observed_state,
				activity=excluded.activity,
				activity_source=excluded.activity_source,
				updated_at=excluded.updated_at,
				observed_version=excluded.observed_version
			WHERE excluded.observed_version > `+physicalSessionObservationTable+`.observed_version`,
			observation.ProjectID, observation.SessionID, observation.ObservedState,
			observation.Activity, observation.ActivitySource,
			observation.UpdatedAt.Format(time.RFC3339Nano), observation.UpdatedAt.UnixNano())
		if err != nil {
			return fmt.Errorf("persist physical session observation: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect physical session observation write: %w", err)
		}
		if affected == 0 {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit stale physical session observation no-op: %w", err)
			}
			return nil
		}

		linked, err := listSessionIntentsByPhysicalIDTx(ctx, tx, observation.ProjectID, observation.SessionID)
		if err != nil {
			return err
		}
		for _, intent := range linked {
			freshness := intent.UpdatedAt
			if observation.UpdatedAt.After(freshness) {
				freshness = observation.UpdatedAt
			}
			if _, err := tx.ExecContext(ctx, `UPDATE `+sessionStateTable+`
				SET observed_state=?, activity=?, activity_source=?, updated_at=?
				WHERE project_id=? AND logical_id=?`, observation.ObservedState,
				observation.Activity, observation.ActivitySource, freshness.Format(time.RFC3339Nano),
				observation.ProjectID, logicalSessionIntentID(intent)); err != nil {
				return fmt.Errorf("fan physical session observation into logical intent %s: %w", logicalSessionIntentID(intent), err)
			}
		}
		changed, err = listSessionIntentsByPhysicalIDTx(ctx, tx, observation.ProjectID, observation.SessionID)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit physical session observation fan-out: %w", err)
		}
		applied = true
		return nil
	})
	return changed, applied, err
}

func normalizePhysicalSessionObservation(observation PhysicalSessionObservation) (PhysicalSessionObservation, error) {
	observation.ProjectID = normalizedProjectID(observation.ProjectID)
	observation.SessionID = strings.TrimSpace(observation.SessionID)
	observation.ObservedState = NormalizeSessionState(observation.ObservedState)
	observation.Activity = strings.TrimSpace(observation.Activity)
	observation.ActivitySource = strings.TrimSpace(observation.ActivitySource)
	if observation.SessionID == "" {
		return observation, fmt.Errorf("physical session observation: missing session id")
	}
	if !validSessionProductState(observation.ObservedState, false) {
		return observation, fmt.Errorf("physical session observation: invalid observed state %q", observation.ObservedState)
	}
	if observation.ObservedState == SessionStateStopped && (observation.Activity != "" || observation.ActivitySource != "") {
		return observation, fmt.Errorf("physical session observation: stopped runtime cannot retain activity")
	}
	if observation.UpdatedAt.IsZero() {
		observation.UpdatedAt = time.Now().UTC()
	} else {
		observation.UpdatedAt = observation.UpdatedAt.UTC()
	}
	return observation, nil
}

func listSessionIntentsByPhysicalIDTx(ctx context.Context, tx *sql.Tx, projectID, sessionID string) ([]Session, error) {
	rows, err := tx.QueryContext(ctx, `SELECT session_id,issue_id,role,scope_kind,scope_id,state,
		COALESCE(observed_state,state),COALESCE(activity,''),COALESCE(activity_source,''),
		COALESCE(tmux_attached_count,0),COALESCE(started_at,''),updated_at
		FROM `+sessionStateTable+` WHERE project_id=? AND session_id=?
		ORDER BY logical_id`, projectID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list physical session logical intents: %w", err)
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		var session Session
		var role, scopeKind, state, observed, startedAt, updatedAt string
		if err := rows.Scan(&session.ID, &session.IssueID, &role, &scopeKind, &session.ScopeID,
			&state, &observed, &session.Activity, &session.ActivitySource,
			&session.TmuxAttachedCount, &startedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan physical session logical intent: %w", err)
		}
		session.Role, session.ScopeKind = SessionRole(role), SessionScopeKind(scopeKind)
		session.State, session.ObservedState = NormalizeSessionState(SessionState(state)), NormalizeSessionState(SessionState(observed))
		if session.StartedAt, err = parseOptionalRuntimeStateTime(startedAt); err != nil {
			return nil, err
		}
		if session.UpdatedAt, err = parseRuntimeStateTime(updatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate physical session logical intents: %w", err)
	}
	return sessions, nil
}

func (s *RuntimeStateStore) GetPhysicalSessionObservation(ctx context.Context, projectID, sessionID string) (PhysicalSessionObservation, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return PhysicalSessionObservation{}, false, err
	}
	var out PhysicalSessionObservation
	var state, updated string
	err = db.QueryRowContext(ctx, `SELECT project_id,session_id,observed_state,activity,activity_source,updated_at
		FROM `+physicalSessionObservationTable+` WHERE project_id=? AND session_id=?`,
		normalizedProjectID(projectID), strings.TrimSpace(sessionID)).Scan(
		&out.ProjectID, &out.SessionID, &state, &out.Activity, &out.ActivitySource, &updated)
	if err == sql.ErrNoRows {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	out.ObservedState = NormalizeSessionState(SessionState(state))
	out.UpdatedAt, err = parseRuntimeStateTime(updated)
	return out, true, err
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
	ListSessionIntentStates(context.Context, string) ([]Session, error)
	GetSessionState(context.Context, string, string) (Session, bool, error)
	GetSessionIntent(context.Context, string, SessionRole, SessionScopeKind, string) (Session, bool, error)
	GetSessionStateByIssueID(context.Context, string, string) (Session, bool, error)
	GetWorkerSessionStateByIssueID(context.Context, string, string, string) (Session, bool, error)
}

// SessionStateWriter mutates persisted session state rows for a project.
type SessionStateWriter interface {
	UpsertSessionState(context.Context, string, Session) error
	DeleteSessionState(context.Context, string, string) error
	DeleteSessionIntentState(context.Context, string, Session) error
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
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		return s.upsertSessionStateLocked(ctx, projectID, session)
	})
}

func (s *RuntimeStateStore) upsertSessionStateLocked(ctx context.Context, projectID string, session Session) error {
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
	metadataProvided := session.Role != "" || session.ScopeKind != "" || strings.TrimSpace(session.ScopeID) != ""
	session = NormalizeSessionMetadata(session)
	session.State = NormalizeSessionState(session.State)
	session.ObservedState = NormalizeSessionState(session.ObservedState)
	if session.State == SessionStateStopped {
		session.ObservedState = SessionStateStopped
	}
	session.Activity = strings.ToLower(strings.TrimSpace(session.Activity))
	session.ActivitySource = strings.ToLower(strings.TrimSpace(session.ActivitySource))
	var existing Session
	var found bool
	if !metadataProvided && sessionStorageTableForID(session.ID) == sessionStateTable {
		rows, listErr := s.ListSessionIntentStates(ctx, projectID)
		err = listErr
		if err == nil {
			for _, candidate := range rows {
				if strings.TrimSpace(candidate.ID) != strings.TrimSpace(session.ID) {
					continue
				}
				if found {
					return fmt.Errorf("upsert session state %s/%s: logical intent identity required for shared physical runtime", projectID, session.ID)
				}
				existing, found = candidate, true
			}
		}
	} else if !metadataProvided {
		existing, found, err = s.GetSessionState(ctx, projectID, session.ID)
	} else if sessionStorageTableForID(session.ID) == sessionStateTable {
		rows, listErr := s.ListSessionIntentStates(ctx, projectID)
		err = listErr
		if err == nil {
			logicalID := logicalSessionIntentID(session)
			for _, candidate := range rows {
				if logicalSessionIntentID(candidate) == logicalID {
					existing, found = candidate, true
					break
				}
			}
		}
	} else {
		existing, found, err = s.GetSessionState(ctx, projectID, session.ID)
	}
	if err == nil && found {
		if !metadataProvided {
			session.Role, session.ScopeKind, session.ScopeID = existing.Role, existing.ScopeKind, existing.ScopeID
		}
		if strings.TrimSpace(string(session.ObservedState)) == "" && strings.TrimSpace(string(existing.ObservedState)) != "" {
			session.ObservedState = existing.ObservedState
		}
		if session.Activity == "" && session.State != SessionStateStopped && session.ObservedState != SessionStateStopped && strings.TrimSpace(existing.Activity) != "" {
			session.Activity = strings.TrimSpace(existing.Activity)
			session.ActivitySource = strings.TrimSpace(existing.ActivitySource)
		}
		if session.StartedAt == nil || session.StartedAt.IsZero() {
			if existing.StartedAt != nil && !existing.StartedAt.IsZero() {
				started := existing.StartedAt.UTC()
				session.StartedAt = &started
			} else if session.State != SessionStateStopped {
				started := session.UpdatedAt.UTC()
				session.StartedAt = &started
			}
		}
	}
	if strings.TrimSpace(string(session.ObservedState)) == "" {
		session.ObservedState = session.State
	}
	normalizeSessionProjectionActivity(&session)
	if err := ValidateSessionProduct(session); err != nil {
		return fmt.Errorf("upsert session state %s/%s: %w", projectID, session.ID, err)
	}
	if session.StartedAt == nil || session.StartedAt.IsZero() {
		if session.State != SessionStateStopped {
			started := session.UpdatedAt.UTC()
			session.StartedAt = &started
		}
	}
	targetTable := sessionStorageTableForID(session.ID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert session state %s/%s: %w", projectID, session.ID, err)
	}
	defer func() { _ = tx.Rollback() }()
	if targetTable == sessionStateTable {
		var observedState, activity, activitySource, observedAt string
		err := tx.QueryRowContext(ctx, `SELECT observed_state,activity,activity_source,updated_at
			FROM `+physicalSessionObservationTable+` WHERE project_id=? AND session_id=?`, projectID, session.ID).
			Scan(&observedState, &activity, &activitySource, &observedAt)
		if err == nil {
			session.ObservedState = NormalizeSessionState(SessionState(observedState))
			session.Activity, session.ActivitySource = activity, activitySource
			physicalUpdatedAt, parseErr := parseRuntimeStateTime(observedAt)
			if parseErr != nil {
				return fmt.Errorf("parse physical observation freshness for %s/%s: %w", projectID, session.ID, parseErr)
			}
			if physicalUpdatedAt.After(session.UpdatedAt) {
				session.UpdatedAt = physicalUpdatedAt
			}
		} else if err != sql.ErrNoRows {
			return fmt.Errorf("load physical observation before intent upsert %s/%s: %w", projectID, session.ID, err)
		}
		if err := ValidateSessionProduct(session); err != nil {
			return fmt.Errorf("hydrate session state %s/%s from physical observation: %w", projectID, session.ID, err)
		}
	}
	startedAt := nullableRuntimeStateTime(session.StartedAt)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO `+targetTable+` (
			project_id,
			session_id,
			issue_id,
			role,
			scope_kind,
			scope_id,
			state,
			observed_state,
			activity,
			activity_source,
			tmux_attached_count,
			started_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, logical_id) DO UPDATE SET
			session_id = excluded.session_id,
			issue_id = excluded.issue_id,
			role = excluded.role,
			scope_kind = excluded.scope_kind,
			scope_id = excluded.scope_id,
			state = excluded.state,
			observed_state = excluded.observed_state,
			activity = excluded.activity,
			activity_source = excluded.activity_source,
			tmux_attached_count = excluded.tmux_attached_count,
			started_at = excluded.started_at,
			updated_at = excluded.updated_at
	`,
		projectID,
		session.ID,
		session.IssueID,
		string(session.Role),
		string(session.ScopeKind),
		session.ScopeID,
		string(session.State),
		string(session.ObservedState),
		session.Activity,
		session.ActivitySource,
		session.TmuxAttachedCount,
		startedAt,
		session.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert session state %s/%s: %w", projectID, session.ID, err)
	}
	if targetTable == sessionObservationTable {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+sessionStateTable+` WHERE project_id = ? AND session_id = ?`, projectID, session.ID); err != nil {
			return fmt.Errorf("delete moved session intent %s/%s: %w", projectID, session.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upsert session state %s/%s: %w", projectID, session.ID, err)
	}
	return nil
}

func (s *RuntimeStateStore) DeleteSessionState(ctx context.Context, projectID, sessionID string) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		return s.deleteSessionStateLocked(ctx, projectID, sessionID)
	})
}

func (s *RuntimeStateStore) DeleteSessionIntentState(ctx context.Context, projectID string, session Session) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		db, err := s.dbHandle()
		if err != nil {
			return err
		}
		projectID = normalizedProjectID(projectID)
		session = NormalizeSessionMetadata(session)
		logicalID := logicalSessionIntentID(session)
		if strings.TrimSpace(logicalID) == "" {
			return nil
		}
		for _, table := range []string{sessionStateTable, sessionObservationTable} {
			if _, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id=? AND logical_id=?`, projectID, logicalID); err != nil {
				return fmt.Errorf("delete logical session intent %s/%s: %w", projectID, logicalID, err)
			}
		}
		return nil
	})
}

func (s *RuntimeStateStore) deleteSessionStateLocked(ctx context.Context, projectID, sessionID string) error {
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
	if _, err := db.ExecContext(ctx, `
		DELETE FROM `+sessionObservationTable+`
		WHERE project_id = ? AND session_id = ?
	`, projectID, sessionID); err != nil {
		return fmt.Errorf("delete session observation %s/%s: %w", projectID, sessionID, err)
	}
	return nil
}

func (s *RuntimeStateStore) UpsertSessionActivityEvidence(ctx context.Context, evidence SessionActivityEvidence) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		return s.upsertSessionActivityEvidenceLocked(ctx, evidence)
	})
}

func (s *RuntimeStateStore) upsertSessionActivityEvidenceLocked(ctx context.Context, evidence SessionActivityEvidence) error {
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID := normalizedProjectID(evidence.ProjectID)
	sessionID := strings.TrimSpace(evidence.SessionID)
	issueID := strings.TrimSpace(evidence.IssueID)
	activity := strings.ToLower(strings.TrimSpace(evidence.Activity))
	if projectID == "" || sessionID == "" || issueID == "" || activity == "" {
		return fmt.Errorf("upsert session activity evidence: missing project, session, issue, or activity")
	}
	activitySource := strings.ToLower(strings.TrimSpace(evidence.ActivitySource))
	if activitySource == "" {
		activitySource = "hooks"
	}
	observedAt := evidence.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	updatedAt := evidence.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = observedAt
	}
	sourceSessionID := strings.TrimSpace(evidence.SourceSessionID)
	if sourceSessionID == "" {
		sourceSessionID = sessionID
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO `+sessionActivityTable+` (
			project_id,
			session_id,
			issue_id,
			activity,
			activity_source,
			source_session_id,
			agent,
			hook,
			event,
			observed_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, session_id, source_session_id) DO UPDATE SET
			issue_id = excluded.issue_id,
			activity = excluded.activity,
			activity_source = excluded.activity_source,
			agent = excluded.agent,
			hook = excluded.hook,
			event = excluded.event,
			observed_at = excluded.observed_at,
			updated_at = excluded.updated_at
		WHERE excluded.observed_at >= `+sessionActivityTable+`.observed_at
	`,
		projectID,
		sessionID,
		issueID,
		activity,
		activitySource,
		sourceSessionID,
		strings.TrimSpace(evidence.Agent),
		strings.TrimSpace(evidence.Hook),
		strings.TrimSpace(evidence.Event),
		observedAt.UTC().Format(time.RFC3339Nano),
		updatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert session activity evidence %s/%s: %w", projectID, sessionID, err)
	}
	return nil
}

func (s *RuntimeStateStore) GetSessionActivityEvidence(ctx context.Context, projectID, sessionID string) (SessionActivityEvidence, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return SessionActivityEvidence{}, false, err
	}
	projectID = normalizedProjectID(projectID)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionActivityEvidence{}, false, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT
			project_id,
			session_id,
			issue_id,
			activity,
			activity_source,
			source_session_id,
			agent,
			hook,
			event,
			observed_at,
			updated_at
		FROM `+sessionActivityTable+`
		WHERE project_id = ? AND session_id = ?
		ORDER BY observed_at DESC, source_session_id ASC
	`, projectID, sessionID)
	if err != nil {
		return SessionActivityEvidence{}, false, err
	}
	defer rows.Close()
	evidenceRows := make([]SessionActivityEvidence, 0)
	for rows.Next() {
		evidence, err := scanSessionActivityEvidence(rows)
		if err != nil {
			return SessionActivityEvidence{}, false, err
		}
		evidenceRows = append(evidenceRows, evidence)
	}
	if err := rows.Err(); err != nil {
		return SessionActivityEvidence{}, false, err
	}
	evidenceRows = AggregateSessionActivityEvidence(evidenceRows)
	if len(evidenceRows) == 0 {
		return SessionActivityEvidence{}, false, nil
	}
	return evidenceRows[0], true, nil
}

func AggregateSessionActivityEvidence(evidenceRows []SessionActivityEvidence) []SessionActivityEvidence {
	type aggregate struct {
		latest         SessionActivityEvidence
		latestDecisive SessionActivityEvidence
		rank           int
	}
	bySession := make(map[string]aggregate, len(evidenceRows))
	for _, evidence := range evidenceRows {
		projectID := normalizedProjectID(evidence.ProjectID)
		sessionID := strings.TrimSpace(evidence.SessionID)
		if projectID == "" || sessionID == "" {
			continue
		}
		evidence.ProjectID = projectID
		evidence.SessionID = sessionID
		evidence.Activity = strings.ToLower(strings.TrimSpace(evidence.Activity))
		if evidence.Activity == "" {
			continue
		}
		key := projectID + "\x00" + sessionID
		next := bySession[key]
		if next.latest.SessionID == "" || evidence.ObservedAt.After(next.latest.ObservedAt) || (evidence.ObservedAt.Equal(next.latest.ObservedAt) && strings.TrimSpace(evidence.SourceSessionID) < strings.TrimSpace(next.latest.SourceSessionID)) {
			next.latest = evidence
		}
		rank := sessionActivityEvidenceRank(evidence.Activity)
		if rank > next.rank || (rank == next.rank && (next.latestDecisive.SessionID == "" || evidence.ObservedAt.After(next.latestDecisive.ObservedAt))) {
			next.rank = rank
			next.latestDecisive = evidence
		}
		bySession[key] = next
	}
	out := make([]SessionActivityEvidence, 0, len(bySession))
	for _, item := range bySession {
		evidence := item.latest
		if item.latestDecisive.SessionID != "" {
			evidence.Activity = item.latestDecisive.Activity
			evidence.ActivitySource = item.latestDecisive.ActivitySource
			evidence.SourceSessionID = item.latestDecisive.SourceSessionID
			evidence.Agent = item.latestDecisive.Agent
			evidence.Hook = item.latestDecisive.Hook
			evidence.Event = item.latestDecisive.Event
		}
		out = append(out, evidence)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].ObservedAt.After(out[j].ObservedAt)
		}
		if out[i].SessionID != out[j].SessionID {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].SourceSessionID < out[j].SourceSessionID
	})
	return out
}

func sessionActivityEvidenceRank(activity string) int {
	switch strings.ToLower(strings.TrimSpace(activity)) {
	case "busy", "starting", "working":
		return 3
	case "waiting":
		return 2
	case "idle", "paused":
		return 1
	default:
		return 0
	}
}

func (s *RuntimeStateStore) ListSessionActivityEvidence(ctx context.Context, projectID string, issueIDs []string) ([]SessionActivityEvidence, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = normalizedProjectID(projectID)
	rows, err := db.QueryContext(ctx, `
		SELECT
			project_id,
			session_id,
			issue_id,
			activity,
			activity_source,
			source_session_id,
			agent,
			hook,
			event,
			observed_at,
			updated_at
		FROM `+sessionActivityTable+`
		WHERE project_id = ?
		ORDER BY observed_at DESC, session_id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list session activity evidence %s: %w", projectID, err)
	}
	defer rows.Close()

	allowed := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		issueID = strings.ToLower(strings.TrimSpace(issueID))
		if issueID != "" {
			allowed[issueID] = struct{}{}
		}
	}
	evidenceRows := make([]SessionActivityEvidence, 0)
	for rows.Next() {
		evidence, err := scanSessionActivityEvidence(rows)
		if err != nil {
			return nil, err
		}
		if len(allowed) > 0 {
			if _, ok := allowed[strings.ToLower(strings.TrimSpace(evidence.IssueID))]; !ok {
				continue
			}
		}
		evidenceRows = append(evidenceRows, evidence)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list session activity evidence rows %s: %w", projectID, err)
	}
	return evidenceRows, nil
}

type sessionActivityEvidenceScanner interface {
	Scan(dest ...any) error
}

func scanSessionActivityEvidence(scanner sessionActivityEvidenceScanner) (SessionActivityEvidence, error) {
	var (
		evidence      SessionActivityEvidence
		observedAtRaw string
		updatedAtRaw  string
	)
	if err := scanner.Scan(
		&evidence.ProjectID,
		&evidence.SessionID,
		&evidence.IssueID,
		&evidence.Activity,
		&evidence.ActivitySource,
		&evidence.SourceSessionID,
		&evidence.Agent,
		&evidence.Hook,
		&evidence.Event,
		&observedAtRaw,
		&updatedAtRaw,
	); err != nil {
		return SessionActivityEvidence{}, err
	}
	observedAt, err := parseRuntimeStateTime(observedAtRaw)
	if err != nil {
		return SessionActivityEvidence{}, fmt.Errorf("parse session activity observed_at: %w", err)
	}
	updatedAt, err := parseRuntimeStateTime(updatedAtRaw)
	if err != nil {
		return SessionActivityEvidence{}, fmt.Errorf("parse session activity updated_at: %w", err)
	}
	evidence.ProjectID = normalizedProjectID(evidence.ProjectID)
	evidence.SessionID = strings.TrimSpace(evidence.SessionID)
	evidence.IssueID = strings.TrimSpace(evidence.IssueID)
	evidence.Activity = strings.ToLower(strings.TrimSpace(evidence.Activity))
	evidence.ActivitySource = strings.ToLower(strings.TrimSpace(evidence.ActivitySource))
	evidence.SourceSessionID = strings.TrimSpace(evidence.SourceSessionID)
	evidence.Agent = strings.TrimSpace(evidence.Agent)
	evidence.Hook = strings.TrimSpace(evidence.Hook)
	evidence.Event = strings.TrimSpace(evidence.Event)
	evidence.ObservedAt = observedAt
	evidence.UpdatedAt = updatedAt
	return evidence, nil
}

func (s *RuntimeStateStore) ReplaceSessionStates(ctx context.Context, projectID string, sessions []Session) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		return s.replaceSessionStatesLocked(ctx, projectID, sessions)
	})
}

func (s *RuntimeStateStore) replaceSessionStatesLocked(ctx context.Context, projectID string, sessions []Session) error {
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

	activeIntentSessions := make(map[string]struct{}, len(sessions))
	activeObservationSessions := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		metadataProvided := session.Role != "" || session.ScopeKind != "" || strings.TrimSpace(session.ScopeID) != ""
		if !metadataProvided {
			var roleRaw, scopeKindRaw, scopeID string
			scanErr := tx.QueryRowContext(ctx, `SELECT COALESCE(role, 'worker'), COALESCE(scope_kind, 'issue'), COALESCE(scope_id, issue_id) FROM `+sessionStorageTableForID(session.ID)+` WHERE project_id = ? AND session_id = ? ORDER BY CASE WHEN role='worker' THEN 0 ELSE 1 END, updated_at DESC LIMIT 1`, projectID, strings.TrimSpace(session.ID)).Scan(&roleRaw, &scopeKindRaw, &scopeID)
			if scanErr == nil {
				session.Role, session.ScopeKind, session.ScopeID = SessionRole(roleRaw), SessionScopeKind(scopeKindRaw), scopeID
			} else if scanErr != sql.ErrNoRows {
				return fmt.Errorf("load session metadata before replace: %w", scanErr)
			}
		}
		session = NormalizeSessionMetadata(session)
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" {
			continue
		}
		session.State = NormalizeSessionState(session.State)
		session.ObservedState = NormalizeSessionState(session.ObservedState)
		if session.State == SessionStateStopped {
			session.ObservedState = SessionStateStopped
		}
		targetTable := sessionStorageTableForID(sessionID)
		logicalID := logicalSessionIntentID(session)
		if targetTable == sessionStateTable {
			activeIntentSessions[logicalID] = struct{}{}
		} else {
			activeObservationSessions[logicalID] = struct{}{}
		}
		updatedAt := session.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if strings.TrimSpace(string(session.ObservedState)) == "" {
			session.ObservedState = session.State
		}
		normalizeSessionProjectionActivity(&session)
		if err := ValidateSessionProduct(session); err != nil {
			return fmt.Errorf("replace session state %s/%s: %w", projectID, sessionID, err)
		}
		startedAt := nullableRuntimeStateTime(session.StartedAt)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+targetTable+` (
				project_id,
				session_id,
				issue_id,
				role,
				scope_kind,
				scope_id,
				state,
				observed_state,
				activity,
				activity_source,
				tmux_attached_count,
				started_at,
				updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, logical_id) DO UPDATE SET
				session_id = excluded.session_id,
				issue_id = excluded.issue_id,
				role = excluded.role,
				scope_kind = excluded.scope_kind,
				scope_id = excluded.scope_id,
				state = excluded.state,
				observed_state = excluded.observed_state,
				activity = excluded.activity,
				activity_source = excluded.activity_source,
				tmux_attached_count = excluded.tmux_attached_count,
				started_at = excluded.started_at,
				updated_at = excluded.updated_at
		`,
			projectID,
			sessionID,
			session.IssueID,
			string(session.Role),
			string(session.ScopeKind),
			session.ScopeID,
			string(session.State),
			string(session.ObservedState),
			session.Activity,
			session.ActivitySource,
			session.TmuxAttachedCount,
			startedAt,
			updatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("upsert session state %s/%s: %w", projectID, sessionID, err)
		}
		if targetTable == sessionObservationTable {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+sessionStateTable+` WHERE project_id = ? AND session_id = ?`, projectID, sessionID); err != nil {
				return fmt.Errorf("delete moved session state %s/%s: %w", projectID, sessionID, err)
			}
		}
	}

	if err := deleteStaleSessionRows(ctx, tx, sessionStateTable, projectID, activeIntentSessions); err != nil {
		return err
	}
	if err := deleteStaleSessionRows(ctx, tx, sessionObservationTable, projectID, activeObservationSessions); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace session state rows %s: %w", projectID, err)
	}
	tx = nil
	return nil
}

func deleteStaleSessionRows(ctx context.Context, tx *sql.Tx, tableName, projectID string, activeSessions map[string]struct{}) error {
	if len(activeSessions) == 0 {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM `+tableName+`
			WHERE project_id = ?
		`, projectID); err != nil {
			return fmt.Errorf("clear session state rows %s: %w", projectID, err)
		}
		return nil
	}
	args := make([]any, 0, len(activeSessions)+1)
	args = append(args, projectID)
	placeholders := make([]string, 0, len(activeSessions))
	for logicalID := range activeSessions {
		placeholders = append(placeholders, "?")
		args = append(args, logicalID)
	}
	query := `DELETE FROM ` + tableName + ` WHERE project_id = ? AND logical_id NOT IN (` + strings.Join(placeholders, ",") + `)`
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("delete stale session state rows %s: %w", projectID, err)
	}
	return nil
}

func normalizeSessionProjectionActivity(session *Session) {
	if session == nil {
		return
	}
	session.Activity = strings.ToLower(strings.TrimSpace(session.Activity))
	session.ActivitySource = strings.ToLower(strings.TrimSpace(session.ActivitySource))
	if NormalizeSessionState(session.State) == SessionStateStopped || NormalizeSessionState(session.ObservedState) == SessionStateStopped {
		session.Activity = ""
		session.ActivitySource = ""
		return
	}
	if session.Activity == "" {
		session.ActivitySource = ""
	} else if session.ActivitySource == "" {
		session.ActivitySource = "session"
	}
}

func sessionStorageTableForID(sessionID string) string {
	if isSessionObservationID(sessionID) {
		return sessionObservationTable
	}
	return sessionStateTable
}

func logicalSessionIntentID(session Session) string {
	base := string(session.Role) + ":" + string(session.ScopeKind) + ":" + strings.TrimSpace(session.ScopeID)
	if isSessionObservationID(session.ID) {
		return base + ":pane:" + strings.TrimSpace(session.ID)
	}
	return base
}

func isSessionObservationID(sessionID string) bool {
	return strings.Contains(strings.TrimSpace(sessionID), ".pane-")
}

func sessionProjectionUnionSQL() string {
	return `
		SELECT
			project_id,
			session_id,
			issue_id,
			role,
			scope_kind,
			scope_id,
			state,
			observed_state,
			activity,
			activity_source,
			tmux_attached_count,
			started_at,
			updated_at
		FROM ` + sessionStateTable + `
		UNION ALL
		SELECT
			project_id,
			session_id,
			issue_id,
			role,
			scope_kind,
			scope_id,
			state,
			observed_state,
			activity,
			activity_source,
			tmux_attached_count,
			started_at,
			updated_at
		FROM ` + sessionObservationTable
}

func (s *RuntimeStateStore) ListSessionStates(ctx context.Context, projectID string) ([]Session, error) {
	return s.listSessionStatesFromQuery(ctx, projectID, sessionProjectionUnionSQL())
}

func (s *RuntimeStateStore) ListSessionIntentStates(ctx context.Context, projectID string) ([]Session, error) {
	return s.listSessionStatesFromQuery(ctx, projectID, `
		SELECT
			project_id,
			session_id,
			issue_id,
			role,
			scope_kind,
			scope_id,
			state,
			observed_state,
			activity,
			activity_source,
			tmux_attached_count,
			started_at,
			updated_at
		FROM `+sessionStateTable)
}

func (s *RuntimeStateStore) listSessionStatesFromQuery(ctx context.Context, projectID, sourceQuery string) ([]Session, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = normalizedProjectID(projectID)
	rows, err := db.QueryContext(ctx, `
		SELECT
			session_id,
			issue_id,
			COALESCE(role, 'worker'),
			COALESCE(scope_kind, 'issue'),
			COALESCE(scope_id, issue_id),
			state,
			COALESCE(observed_state, state),
			COALESCE(activity, ''),
			COALESCE(activity_source, ''),
			COALESCE(tmux_attached_count, 0),
			COALESCE(started_at, ''),
			updated_at
		FROM (`+sourceQuery+`)
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
			sessionID     string
			issueID       string
			roleRaw       string
			scopeKindRaw  string
			scopeID       string
			stateRaw      string
			observedRaw   string
			activityRaw   string
			sourceRaw     string
			attachedCount int
			startedAt     string
			updatedAt     string
		)
		if err := rows.Scan(&sessionID, &issueID, &roleRaw, &scopeKindRaw, &scopeID, &stateRaw, &observedRaw, &activityRaw, &sourceRaw, &attachedCount, &startedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan session state row: %w", err)
		}
		parsedStartedAt, err := parseOptionalRuntimeStateTime(startedAt)
		if err != nil {
			return nil, err
		}
		parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, Session{
			ID:                sessionID,
			IssueID:           issueID,
			Role:              SessionRole(roleRaw),
			ScopeKind:         SessionScopeKind(scopeKindRaw),
			ScopeID:           scopeID,
			State:             NormalizeSessionState(SessionState(stateRaw)),
			ObservedState:     NormalizeSessionState(SessionState(observedRaw)),
			Activity:          strings.ToLower(strings.TrimSpace(activityRaw)),
			ActivitySource:    strings.ToLower(strings.TrimSpace(sourceRaw)),
			TmuxAttachedCount: attachedCount,
			StartedAt:         parsedStartedAt,
			UpdatedAt:         parsedUpdatedAt,
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
			COALESCE(role, 'worker'),
			COALESCE(scope_kind, 'issue'),
			COALESCE(scope_id, issue_id),
			state,
			COALESCE(observed_state, state),
			COALESCE(activity, ''),
			COALESCE(activity_source, ''),
			COALESCE(tmux_attached_count, 0),
			COALESCE(started_at, ''),
			updated_at
		FROM (`+sessionProjectionUnionSQL()+`)
		WHERE project_id = ? AND session_id = ?
		ORDER BY CASE WHEN session_id LIKE '%.pane-%' THEN 1 ELSE 0 END
		LIMIT 1
	`, projectID, sessionID)
	var (
		rowSessionID  string
		issueID       string
		roleRaw       string
		scopeKindRaw  string
		scopeID       string
		stateRaw      string
		observedRaw   string
		activityRaw   string
		sourceRaw     string
		attachedCount int
		startedAt     string
		updatedAt     string
	)
	if err := row.Scan(&rowSessionID, &issueID, &roleRaw, &scopeKindRaw, &scopeID, &stateRaw, &observedRaw, &activityRaw, &sourceRaw, &attachedCount, &startedAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return Session{}, false, nil
		}
		return Session{}, false, fmt.Errorf("get session state %s/%s: %w", projectID, sessionID, err)
	}
	parsedStartedAt, err := parseOptionalRuntimeStateTime(startedAt)
	if err != nil {
		return Session{}, false, err
	}
	parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
	if err != nil {
		return Session{}, false, err
	}
	return Session{
		ID:                rowSessionID,
		IssueID:           issueID,
		Role:              SessionRole(roleRaw),
		ScopeKind:         SessionScopeKind(scopeKindRaw),
		ScopeID:           scopeID,
		State:             NormalizeSessionState(SessionState(stateRaw)),
		ObservedState:     NormalizeSessionState(SessionState(observedRaw)),
		Activity:          strings.ToLower(strings.TrimSpace(activityRaw)),
		ActivitySource:    strings.ToLower(strings.TrimSpace(sourceRaw)),
		TmuxAttachedCount: attachedCount,
		StartedAt:         parsedStartedAt,
		UpdatedAt:         parsedUpdatedAt,
	}, true, nil
}

func (s *RuntimeStateStore) GetSessionIntent(ctx context.Context, projectID string, role SessionRole, scopeKind SessionScopeKind, scopeID string) (Session, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Session{}, false, err
	}
	row := db.QueryRowContext(ctx, `SELECT session_id,issue_id,role,scope_kind,scope_id,state,COALESCE(observed_state,state),COALESCE(activity,''),COALESCE(activity_source,''),COALESCE(tmux_attached_count,0),COALESCE(started_at,''),updated_at FROM `+sessionStateTable+` WHERE project_id=? AND role=? AND scope_kind=? AND scope_id=? LIMIT 1`, normalizedProjectID(projectID), role, scopeKind, strings.TrimSpace(scopeID))
	var out Session
	var roleRaw, scopeRaw, stateRaw, observedRaw, startedRaw, updatedRaw string
	if err := row.Scan(&out.ID, &out.IssueID, &roleRaw, &scopeRaw, &out.ScopeID, &stateRaw, &observedRaw, &out.Activity, &out.ActivitySource, &out.TmuxAttachedCount, &startedRaw, &updatedRaw); err != nil {
		if err == sql.ErrNoRows {
			return Session{}, false, nil
		}
		return Session{}, false, err
	}
	out.Role, out.ScopeKind, out.State, out.ObservedState = SessionRole(roleRaw), SessionScopeKind(scopeRaw), NormalizeSessionState(SessionState(stateRaw)), NormalizeSessionState(SessionState(observedRaw))
	if out.StartedAt, err = parseOptionalRuntimeStateTime(startedRaw); err != nil {
		return Session{}, false, err
	}
	if out.UpdatedAt, err = parseRuntimeStateTime(updatedRaw); err != nil {
		return Session{}, false, err
	}
	return out, true, nil
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
			COALESCE(role, 'worker'),
			COALESCE(scope_kind, 'issue'),
			COALESCE(scope_id, issue_id),
			state,
			COALESCE(observed_state, state),
			COALESCE(activity, ''),
			COALESCE(activity_source, ''),
			COALESCE(tmux_attached_count, 0),
			COALESCE(started_at, ''),
			updated_at
		FROM (`+sessionProjectionUnionSQL()+`)
		WHERE project_id = ? AND issue_id = ?
		ORDER BY updated_at DESC, session_id ASC
		LIMIT 1
	`, projectID, issueID)
	var (
		sessionID     string
		rowIssue      string
		roleRaw       string
		scopeKindRaw  string
		scopeID       string
		stateRaw      string
		observedRaw   string
		activityRaw   string
		sourceRaw     string
		attachedCount int
		startedAt     string
		updatedAt     string
	)
	if err := row.Scan(&sessionID, &rowIssue, &roleRaw, &scopeKindRaw, &scopeID, &stateRaw, &observedRaw, &activityRaw, &sourceRaw, &attachedCount, &startedAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return Session{}, false, nil
		}
		return Session{}, false, fmt.Errorf("get session state by issue %s/%s: %w", projectID, issueID, err)
	}
	parsedStartedAt, err := parseOptionalRuntimeStateTime(startedAt)
	if err != nil {
		return Session{}, false, err
	}
	parsedUpdatedAt, err := parseRuntimeStateTime(updatedAt)
	if err != nil {
		return Session{}, false, err
	}
	return Session{
		ID:                sessionID,
		IssueID:           rowIssue,
		Role:              SessionRole(roleRaw),
		ScopeKind:         SessionScopeKind(scopeKindRaw),
		ScopeID:           scopeID,
		State:             NormalizeSessionState(SessionState(stateRaw)),
		ObservedState:     NormalizeSessionState(SessionState(observedRaw)),
		Activity:          strings.ToLower(strings.TrimSpace(activityRaw)),
		ActivitySource:    strings.ToLower(strings.TrimSpace(sourceRaw)),
		TmuxAttachedCount: attachedCount,
		StartedAt:         parsedStartedAt,
		UpdatedAt:         parsedUpdatedAt,
	}, true, nil
}

// GetWorkerSessionStateByIssueID returns the desired worker projection for an
// issue. Advisor and orchestrator rows can share an issue ID and must never be
// treated as worker lifecycle authority.
func (s *RuntimeStateStore) GetWorkerSessionStateByIssueID(ctx context.Context, projectID, issueID, canonicalSessionID string) (Session, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return Session{}, false, err
	}
	projectID = normalizedProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	canonicalSessionID = strings.TrimSpace(canonicalSessionID)
	if issueID == "" {
		return Session{}, false, nil
	}
	row := db.QueryRowContext(ctx, `
		SELECT session_id,issue_id,role,scope_kind,scope_id,state,
			COALESCE(observed_state,state),COALESCE(activity,''),COALESCE(activity_source,''),
			COALESCE(tmux_attached_count,0),COALESCE(started_at,''),updated_at
		FROM `+sessionStateTable+`
		WHERE project_id=? AND issue_id=? AND role='worker' AND scope_kind='issue' AND scope_id=issue_id
		ORDER BY CASE WHEN session_id=? THEN 0 ELSE 1 END, updated_at DESC, session_id ASC
		LIMIT 1`, projectID, issueID, canonicalSessionID)
	var session Session
	var role, scopeKind, state, observed, startedAt, updatedAt string
	if err := row.Scan(&session.ID, &session.IssueID, &role, &scopeKind, &session.ScopeID, &state,
		&observed, &session.Activity, &session.ActivitySource, &session.TmuxAttachedCount, &startedAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return Session{}, false, nil
		}
		return Session{}, false, fmt.Errorf("get worker session state by issue %s/%s: %w", projectID, issueID, err)
	}
	session.Role, session.ScopeKind = SessionRole(role), SessionScopeKind(scopeKind)
	session.State, session.ObservedState = NormalizeSessionState(SessionState(state)), NormalizeSessionState(SessionState(observed))
	if session.StartedAt, err = parseOptionalRuntimeStateTime(startedAt); err != nil {
		return Session{}, false, err
	}
	if session.UpdatedAt, err = parseRuntimeStateTime(updatedAt); err != nil {
		return Session{}, false, err
	}
	if err := ValidateSessionProduct(session); err != nil {
		return Session{}, false, fmt.Errorf("invalid worker session state by issue %s/%s: %w", projectID, issueID, err)
	}
	return session, true, nil
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
		SELECT project_id FROM `+sessionObservationTable+`
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
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		return s.upsertWorktreeStateLocked(ctx, worktreeState)
	})
}

func (s *RuntimeStateStore) upsertWorktreeStateLocked(ctx context.Context, worktreeState WorktreeState) error {
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
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		return s.deleteWorktreeStateLocked(ctx, projectID, issueID)
	})
}

func (s *RuntimeStateStore) deleteWorktreeStateLocked(ctx context.Context, projectID, issueID string) error {
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
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		return s.replaceWorktreeStatesLocked(ctx, projectID, worktreeStates)
	})
}

func (s *RuntimeStateStore) replaceWorktreeStatesLocked(ctx context.Context, projectID string, worktreeStates []WorktreeState) error {
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
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		return s.upsertWorktreeStateGitStatusLocked(ctx, projectID, issueID, statusRaw, updatedAt)
	})
}

func (s *RuntimeStateStore) upsertWorktreeStateGitStatusLocked(ctx context.Context, projectID, issueID string, statusRaw json.RawMessage, updatedAt time.Time) error {
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

func parseOptionalRuntimeStateTime(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := parseRuntimeStateTime(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullableRuntimeStateTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
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
	db, err := tracesqlite.Open(dsn)
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
		`CREATE TABLE IF NOT EXISTS ` + physicalSessionObservationTable + ` (
			project_id TEXT NOT NULL CHECK (trim(project_id) <> ''),
			session_id TEXT NOT NULL CHECK (trim(session_id) <> ''),
			observed_state TEXT NOT NULL CHECK (observed_state IN ('starting','running','stopping','paused','stopped')),
			activity TEXT NOT NULL DEFAULT '',
			activity_source TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			observed_version INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(project_id, session_id),
			CHECK (observed_state <> 'stopped' OR (activity = '' AND activity_source = ''))
		)`,
		`CREATE TABLE IF NOT EXISTS ` + sessionStateTable + ` (
			project_id TEXT NOT NULL,
			logical_id TEXT GENERATED ALWAYS AS (role||':'||scope_kind||':'||scope_id||CASE WHEN instr(session_id,'.pane-')>0 THEN ':pane:'||session_id ELSE '' END) STORED,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'worker',
			scope_kind TEXT NOT NULL DEFAULT 'issue',
			scope_id TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL,
			observed_state TEXT,
			activity TEXT,
			activity_source TEXT,
			tmux_attached_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			UNIQUE (project_id, logical_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_projections_project_issue
			ON ` + sessionStateTable + ` (project_id, issue_id)`,
		`CREATE TABLE IF NOT EXISTS ` + sessionObservationTable + ` (
			project_id TEXT NOT NULL,
			logical_id TEXT GENERATED ALWAYS AS (role||':'||scope_kind||':'||scope_id||CASE WHEN instr(session_id,'.pane-')>0 THEN ':pane:'||session_id ELSE '' END) STORED,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'worker',
			scope_kind TEXT NOT NULL DEFAULT 'issue',
			scope_id TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL,
			observed_state TEXT,
			activity TEXT,
			activity_source TEXT,
			tmux_attached_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,
			updated_at TEXT NOT NULL,
			UNIQUE (project_id, logical_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_observations_project_issue
			ON ` + sessionObservationTable + ` (project_id, issue_id)`,
		`CREATE TABLE IF NOT EXISTS ` + orchestratorLeaseTable + ` (
			project_id TEXT NOT NULL,
			scope_kind TEXT NOT NULL CHECK (scope_kind IN ('project', 'rooted')),
			root_issue_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL,
			lifecycle TEXT NOT NULL CHECK (lifecycle IN ('working', 'quiescent', 'complete-grace', 'paused')),
			acquired_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			complete_since TEXT,
			last_wake_at TEXT,
			last_wake_reason TEXT NOT NULL DEFAULT '',
			cursor INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (project_id, scope_kind, root_issue_id),
			UNIQUE (project_id, session_id),
			CHECK ((scope_kind = 'project' AND root_issue_id = '') OR (scope_kind = 'rooted' AND root_issue_id <> ''))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_orchestrator_scope_leases_project_updated
			ON ` + orchestratorLeaseTable + ` (project_id, updated_at DESC, scope_kind, root_issue_id)`,
		`CREATE TABLE IF NOT EXISTS ` + advisorSessionTable + ` (
			project_id TEXT NOT NULL,
			request_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, request_id),
			UNIQUE (project_id, session_id)
		)`,
		`CREATE TABLE IF NOT EXISTS ` + orchestratorLoopTable + ` (
			project_id TEXT NOT NULL,
			scope_kind TEXT NOT NULL CHECK (scope_kind IN ('project', 'rooted')),
			root_issue_id TEXT NOT NULL DEFAULT '',
			watch_cursor INTEGER NOT NULL DEFAULT 0,
			last_action_key TEXT NOT NULL DEFAULT '',
			last_action_kind TEXT NOT NULL DEFAULT '',
			last_action_status TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, scope_kind, root_issue_id),
			CHECK ((scope_kind = 'project' AND root_issue_id = '') OR (scope_kind = 'rooted' AND root_issue_id <> ''))
		)`,
		`CREATE TABLE IF NOT EXISTS ` + sessionActivityTable + ` (
			project_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			activity TEXT NOT NULL,
			activity_source TEXT NOT NULL,
			source_session_id TEXT NOT NULL,
			agent TEXT,
			hook TEXT,
			event TEXT,
			observed_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (project_id, session_id, source_session_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daemon_session_activity_evidence_project_issue
			ON ` + sessionActivityTable + ` (project_id, issue_id)`,
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
	if err := ensureColumn(ctx, db, physicalSessionObservationTable, "observed_version", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := backfillPhysicalObservationVersions(ctx, db); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionStateTable, "started_at", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, orchestratorLeaseTable, "complete_since", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, orchestratorLeaseTable, "last_wake_at", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, orchestratorLeaseTable, "last_wake_reason", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, orchestratorLeaseTable, "cursor", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionStateTable, "observed_state", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionStateTable, "activity", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionStateTable, "activity_source", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionStateTable, "tmux_attached_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		if err := ensureColumn(ctx, db, table, "role", "TEXT NOT NULL DEFAULT 'worker'"); err != nil {
			return err
		}
		if err := ensureColumn(ctx, db, table, "scope_kind", "TEXT NOT NULL DEFAULT 'issue'"); err != nil {
			return err
		}
		if err := ensureColumn(ctx, db, table, "scope_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if err := ensureColumn(ctx, db, sessionObservationTable, "started_at", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionObservationTable, "observed_state", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionObservationTable, "activity", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionObservationTable, "activity_source", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, sessionObservationTable, "tmux_attached_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := canonicalizeRuntimeLogicalSessions(ctx, db); err != nil {
		return err
	}
	if err := ensureRuntimeLogicalSessionIdentitySchema(ctx, db); err != nil {
		return err
	}
	if err := migrateSessionObservations(ctx, db); err != nil {
		return err
	}
	if err := ensureRuntimeSessionProductConstraints(ctx, db); err != nil {
		return err
	}
	if err := ensureSessionActivityEvidenceSourceKey(ctx, db); err != nil {
		return err
	}
	if err := migrateSessionActivityEvidence(ctx, db); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, worktreeStateTable, "git_status_json", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, worktreeStateTable, "git_status_updated_at", "TEXT"); err != nil {
		return err
	}
	return nil
}

func backfillPhysicalObservationVersions(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT project_id,session_id,updated_at FROM `+physicalSessionObservationTable+` WHERE observed_version=0`)
	if err != nil {
		return fmt.Errorf("list physical observations needing version backfill: %w", err)
	}
	type versionBackfill struct {
		projectID, sessionID string
		version              int64
	}
	var pending []versionBackfill
	for rows.Next() {
		var projectID, sessionID, updatedAt string
		if err := rows.Scan(&projectID, &sessionID, &updatedAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan physical observation version backfill: %w", err)
		}
		parsed, err := parseRuntimeStateTime(updatedAt)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("backfill physical observation version %s/%s: %w", projectID, sessionID, err)
		}
		pending = append(pending, versionBackfill{projectID: projectID, sessionID: sessionID, version: parsed.UnixNano()})
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close physical observation version backfill rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate physical observation version backfill rows: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin physical observation version backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range pending {
		if _, err := tx.ExecContext(ctx, `UPDATE `+physicalSessionObservationTable+` SET observed_version=? WHERE project_id=? AND session_id=? AND observed_version=0`, row.version, row.projectID, row.sessionID); err != nil {
			return fmt.Errorf("write physical observation version backfill %s/%s: %w", row.projectID, row.sessionID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit physical observation version backfill: %w", err)
	}
	return nil
}

func ensureSessionActivityEvidenceSourceKey(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+sessionActivityTable+`)`)
	if err != nil {
		return fmt.Errorf("inspect session activity evidence schema: %w", err)
	}
	defer rows.Close()

	type pkColumn struct {
		name string
		rank int
	}
	pkColumns := make([]pkColumn, 0, 3)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("scan session activity evidence schema: %w", err)
		}
		if pk > 0 {
			pkColumns = append(pkColumns, pkColumn{name: name, rank: pk})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read session activity evidence schema: %w", err)
	}
	sort.Slice(pkColumns, func(i, j int) bool { return pkColumns[i].rank < pkColumns[j].rank })
	if len(pkColumns) == 3 &&
		pkColumns[0].name == "project_id" &&
		pkColumns[1].name == "session_id" &&
		pkColumns[2].name == "source_session_id" {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session activity evidence migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	oldTable := fmt.Sprintf("%s_old_%d", sessionActivityTable, time.Now().UTC().UnixNano())
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+sessionActivityTable+` RENAME TO `+oldTable); err != nil {
		return fmt.Errorf("rename session activity evidence table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE `+sessionActivityTable+` (
		project_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		issue_id TEXT NOT NULL,
		activity TEXT NOT NULL,
		activity_source TEXT NOT NULL,
		source_session_id TEXT NOT NULL,
		agent TEXT,
		hook TEXT,
		event TEXT,
		observed_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (project_id, session_id, source_session_id)
	)`); err != nil {
		return fmt.Errorf("create session activity evidence source-key table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+sessionActivityTable+` (
		project_id,
		session_id,
		issue_id,
		activity,
		activity_source,
		source_session_id,
		agent,
		hook,
		event,
		observed_at,
		updated_at
	)
	SELECT
		project_id,
		session_id,
		issue_id,
		activity,
		activity_source,
		COALESCE(NULLIF(trim(source_session_id), ''), session_id),
		agent,
		hook,
		event,
		observed_at,
		updated_at
	FROM `+oldTable); err != nil {
		return fmt.Errorf("copy session activity evidence rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE `+oldTable); err != nil {
		return fmt.Errorf("drop migrated session activity evidence table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_daemon_session_activity_evidence_project_issue
		ON `+sessionActivityTable+` (project_id, issue_id)`); err != nil {
		return fmt.Errorf("create session activity evidence issue index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session activity evidence migration: %w", err)
	}
	committed = true
	return nil
}

func migrateSessionActivityEvidence(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		INSERT INTO `+sessionActivityTable+` (
			project_id,
			session_id,
			issue_id,
			activity,
			activity_source,
			source_session_id,
			agent,
			hook,
			event,
			observed_at,
			updated_at
		)
		SELECT
			project_id,
			parent_session_id,
			issue_id,
			activity,
			'hooks',
			session_id,
			'',
			'',
			'',
			updated_at,
			updated_at
		FROM (
			SELECT
				project_id,
				substr(session_id, 1, instr(session_id, '.pane-') - 1) AS parent_session_id,
				session_id,
				issue_id,
				CASE lower(trim(COALESCE(activity, '')))
					WHEN 'busy' THEN 'busy'
					WHEN 'waiting' THEN 'waiting'
					WHEN 'idle' THEN 'idle'
					ELSE CASE lower(trim(COALESCE(state, '')))
						WHEN 'running' THEN 'busy'
						WHEN 'paused' THEN 'idle'
						ELSE ''
					END
				END AS activity,
				updated_at
			FROM `+sessionObservationTable+`
			WHERE instr(session_id, '.pane-') > 0
				AND lower(trim(COALESCE(activity_source, ''))) = 'hooks'
				AND trim(COALESCE(issue_id, '')) <> ''
				AND trim(COALESCE(updated_at, '')) <> ''
		)
		WHERE parent_session_id <> ''
			AND activity <> ''
		ON CONFLICT(project_id, session_id, source_session_id) DO UPDATE SET
			issue_id = excluded.issue_id,
			activity = excluded.activity,
			activity_source = excluded.activity_source,
			agent = excluded.agent,
			hook = excluded.hook,
			event = excluded.event,
			observed_at = excluded.observed_at,
			updated_at = excluded.updated_at
		WHERE excluded.observed_at >= `+sessionActivityTable+`.observed_at
	`); err != nil {
		return fmt.Errorf("migrate session activity evidence: %w", err)
	}
	return nil
}

func migrateSessionObservations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM `+sessionObservationTable+` WHERE EXISTS(SELECT 1 FROM `+sessionStateTable+` p WHERE p.project_id=`+sessionObservationTable+`.project_id AND p.session_id=`+sessionObservationTable+`.session_id AND instr(p.session_id,'.pane-')>0)`); err != nil {
		return fmt.Errorf("migrate session observations: remove stale pane metadata: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO `+sessionObservationTable+` (
			project_id,
			session_id,
			issue_id,
			role,
			scope_kind,
			scope_id,
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
			role,
			scope_kind,
			COALESCE(NULLIF(trim(scope_id), ''), issue_id),
			state,
			observed_state,
			activity,
			activity_source,
			COALESCE(tmux_attached_count, 0),
			started_at,
			updated_at
		FROM `+sessionStateTable+`
		WHERE instr(session_id, '.pane-') > 0
		ON CONFLICT(project_id, logical_id) DO UPDATE SET
			session_id = excluded.session_id,
			issue_id = excluded.issue_id,
			role = excluded.role,
			scope_kind = excluded.scope_kind,
			scope_id = excluded.scope_id,
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
	if _, err := db.ExecContext(ctx, `
		DELETE FROM `+sessionStateTable+`
		WHERE instr(session_id, '.pane-') > 0
	`); err != nil {
		return fmt.Errorf("migrate session observations: delete pane rows from intent table: %w", err)
	}
	return nil
}

func ensureRuntimeLogicalSessionIdentitySchema(ctx context.Context, db *sql.DB) error {
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		var logicalColumns int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_xinfo('`+table+`') WHERE name='logical_id'`).Scan(&logicalColumns); err != nil {
			return err
		}
		logicalPrimary := logicalColumns > 0
		if logicalPrimary {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		newTable := table + "_logical_identity_v3"
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+newTable+`; CREATE TABLE `+newTable+`(
			project_id TEXT NOT NULL,session_id TEXT NOT NULL,issue_id TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'worker',scope_kind TEXT NOT NULL DEFAULT 'issue',scope_id TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,
			observed_state TEXT,activity TEXT,activity_source TEXT,tmux_attached_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,updated_at TEXT NOT NULL,
			logical_id TEXT GENERATED ALWAYS AS (role||':'||scope_kind||':'||scope_id||CASE WHEN instr(session_id,'.pane-')>0 THEN ':pane:'||session_id ELSE '' END) STORED,
			UNIQUE(project_id,logical_id));
			INSERT INTO `+newTable+`(project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at)
			SELECT project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at FROM `+table+`;
			DROP TABLE `+table+`; ALTER TABLE `+newTable+` RENAME TO `+table+`;`); err != nil {
			tx.Rollback()
			return fmt.Errorf("rebuild %s logical identity: %w", table, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_`+table+`_project_issue ON `+table+`(project_id,issue_id)`); err != nil {
			return err
		}
	}
	return nil
}

func ensureRuntimeSessionProductConstraints(ctx context.Context, db *sql.DB) error {
	var issuesTableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='issues'`).Scan(&issuesTableCount); err != nil {
		return fmt.Errorf("inspect relational issue authority: %w", err)
	}
	var interactionTableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='interaction_requests'`).Scan(&interactionTableCount); err != nil {
		return fmt.Errorf("inspect relational interaction authority: %w", err)
	}
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		if _, err := db.ExecContext(ctx, `UPDATE `+table+` SET state=CASE WHEN state='attached' THEN 'running' ELSE state END, observed_state=CASE WHEN observed_state='attached' THEN 'running' ELSE observed_state END WHERE state='attached' OR observed_state='attached'`); err != nil {
			return fmt.Errorf("normalize %s legacy attached state: %w", table, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE `+table+` SET scope_id=issue_id WHERE role='worker' AND scope_kind='issue' AND trim(scope_id)=''`); err != nil {
			return fmt.Errorf("backfill %s worker scope identity: %w", table, err)
		}
		rows, err := db.QueryContext(ctx, `SELECT session_id,issue_id,role,scope_kind,scope_id,state,COALESCE(observed_state,''),tmux_attached_count FROM `+table)
		if err != nil {
			return fmt.Errorf("validate %s session products: %w", table, err)
		}
		for rows.Next() {
			var session Session
			var role, scopeKind, state, observed string
			if err := rows.Scan(&session.ID, &session.IssueID, &role, &scopeKind, &session.ScopeID, &state, &observed, &session.TmuxAttachedCount); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan %s session product: %w", table, err)
			}
			session.Role, session.ScopeKind = SessionRole(role), SessionScopeKind(scopeKind)
			session.State, session.ObservedState = SessionState(state), SessionState(observed)
			if err := ValidateSessionProduct(session); err != nil {
				_ = rows.Close()
				return fmt.Errorf("invalid historical runtime authority %s/%s: %w", table, session.ID, err)
			}
			if issuesTableCount > 0 && (session.Role == SessionRoleWorker || (session.Role == SessionRoleOrchestrator && strings.TrimSpace(session.ScopeID) != "project")) {
				var count int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id=?`, strings.TrimSpace(session.IssueID)).Scan(&count); err != nil || count != 1 {
					_ = rows.Close()
					return fmt.Errorf("invalid historical runtime authority %s/%s: issue does not exist", table, session.ID)
				}
			}
			if interactionTableCount > 0 && session.Role == SessionRoleAdvisor {
				var count int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM interaction_requests WHERE id=? AND issue_id=?`, strings.TrimSpace(session.ScopeID), strings.TrimSpace(session.IssueID)).Scan(&count); err != nil || count != 1 {
					_ = rows.Close()
					return fmt.Errorf("invalid historical runtime authority %s/%s: advisor interaction scope does not match issue", table, session.ID)
				}
			}
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close %s session product validation: %w", table, err)
		}
		indexName := "idx_" + table + "_logical_scope_unique"
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS `+indexName+` ON `+table+`(project_id,role,scope_kind,scope_id) WHERE instr(session_id,'.pane-')=0`); err != nil {
			return fmt.Errorf("enforce %s logical session uniqueness: %w", table, err)
		}
		prefix := "runtime_" + table + "_product"
		for _, suffix := range []string{"insert", "update"} {
			if _, err := db.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+prefix+`_`+suffix); err != nil {
				return err
			}
			action := "INSERT"
			if suffix == "update" {
				action = "UPDATE"
			}
			relationalGuards := ""
			if issuesTableCount > 0 {
				relationalGuards += `SELECT CASE WHEN NEW.role IN ('worker','orchestrator') AND NOT (NEW.role='orchestrator' AND NEW.scope_id='project') AND NOT EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id) THEN RAISE(ABORT,'session issue does not exist') END;`
			}
			if interactionTableCount > 0 {
				relationalGuards += `SELECT CASE WHEN NEW.role='advisor' AND NOT EXISTS(SELECT 1 FROM interaction_requests WHERE id=NEW.scope_id AND issue_id=NEW.issue_id) THEN RAISE(ABORT,'advisor interaction scope does not match issue') END;`
			}
			trigger := `CREATE TRIGGER ` + prefix + `_` + suffix + ` BEFORE ` + action + ` ON ` + table + ` BEGIN
				SELECT CASE WHEN trim(NEW.project_id)='' OR trim(NEW.session_id)='' THEN RAISE(ABORT,'session projection identity must be nonempty') END;
				SELECT CASE WHEN NEW.role='worker' AND (NEW.scope_kind!='issue' OR trim(NEW.issue_id)='' OR trim(NEW.scope_id)='' OR NEW.scope_id!=NEW.issue_id) THEN RAISE(ABORT,'worker session requires matching issue scope') END;
				SELECT CASE WHEN NEW.role='advisor' AND (NEW.scope_kind!='interaction' OR trim(NEW.issue_id)='' OR trim(NEW.scope_id)='') THEN RAISE(ABORT,'advisor session requires interaction scope') END;
				SELECT CASE WHEN NEW.role='orchestrator' AND (NEW.scope_kind!='orchestration' OR trim(NEW.scope_id)='' OR (NEW.scope_id='project' AND trim(NEW.issue_id)!='') OR (NEW.scope_id!='project' AND NEW.issue_id!=NEW.scope_id)) THEN RAISE(ABORT,'invalid orchestrator session scope') END;
				` + relationalGuards + `
				SELECT CASE WHEN NEW.role NOT IN ('worker','advisor','orchestrator') OR NEW.scope_kind NOT IN ('issue','interaction','orchestration') THEN RAISE(ABORT,'invalid session role or scope') END;
				SELECT CASE WHEN NEW.state NOT IN ('starting','running','stopping','paused','stopped') OR COALESCE(NEW.observed_state,'') NOT IN ('','starting','running','stopping','paused','stopped') THEN RAISE(ABORT,'invalid session state') END;
				SELECT CASE WHEN NEW.tmux_attached_count<0 THEN RAISE(ABORT,'tmux attachment count cannot be negative') END;
				SELECT CASE WHEN (NEW.state='stopped' OR NEW.observed_state='stopped') AND NEW.tmux_attached_count!=0 THEN RAISE(ABORT,'stopped session cannot be attached') END;
			END`
			if _, err := db.ExecContext(ctx, trigger); err != nil {
				return fmt.Errorf("create %s session product trigger: %w", table, err)
			}
		}
	}
	return nil
}

func canonicalizeRuntimeLogicalSessions(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin logical session canonicalization: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE logical_runtime_winners AS
		SELECT DISTINCT project_id,role,scope_kind,scope_id,
		FIRST_VALUE(session_id) OVER (PARTITION BY project_id,role,scope_kind,scope_id ORDER BY
			CASE WHEN length(session_id)>3 AND substr(session_id,3,1)='-' AND substr(session_id,4)=CASE WHEN role='orchestrator' AND scope_id='project' THEN 'orchestrator-project' ELSE scope_id END THEN 0 ELSE 1 END,
			updated_at DESC,session_id ASC) AS winner_id
		FROM (SELECT project_id,session_id,role,scope_kind,scope_id,updated_at FROM `+sessionStateTable+` WHERE instr(session_id,'.pane-')=0
			UNION ALL SELECT project_id,session_id,role,scope_kind,scope_id,updated_at FROM `+sessionObservationTable+` WHERE instr(session_id,'.pane-')=0);`); err != nil {
		return fmt.Errorf("select shared logical runtime winners: %w", err)
	}
	for _, table := range []string{sessionStateTable, sessionObservationTable} {
		merged := "merged_" + table
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE `+merged+` AS
			SELECT DISTINCT t.project_id,w.winner_id AS session_id,
			FIRST_VALUE(t.issue_id) OVER newest AS issue_id,t.role,t.scope_kind,t.scope_id,
			FIRST_VALUE(t.state) OVER newest AS state,FIRST_VALUE(t.observed_state) OVER newest AS observed_state,
			FIRST_VALUE(t.activity) OVER newest AS activity,FIRST_VALUE(t.activity_source) OVER newest AS activity_source,
			FIRST_VALUE(t.tmux_attached_count) OVER newest AS tmux_attached_count,
			MIN(NULLIF(t.started_at,'')) OVER logical_scope AS started_at,MAX(t.updated_at) OVER logical_scope AS updated_at
			FROM `+table+` t JOIN logical_runtime_winners w USING(project_id,role,scope_kind,scope_id)
			WHERE instr(t.session_id,'.pane-')=0
			WINDOW logical_scope AS (PARTITION BY t.project_id,t.role,t.scope_kind,t.scope_id),
			newest AS (PARTITION BY t.project_id,t.role,t.scope_kind,t.scope_id ORDER BY t.updated_at DESC,t.session_id ASC);
			DELETE FROM `+table+` WHERE instr(session_id,'.pane-')=0;
			INSERT INTO `+table+`(project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at)
			SELECT project_id,session_id,issue_id,role,scope_kind,scope_id,state,observed_state,activity,activity_source,tmux_attached_count,started_at,updated_at FROM `+merged+`;
			DROP TABLE `+merged+`;`); err != nil {
			return fmt.Errorf("canonicalize %s shared logical sessions: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE logical_runtime_winners`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit logical session canonicalization: %w", err)
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
