package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

type AdvisorSession struct {
	ProjectID string
	RequestID string
	IssueID   string
	SessionID string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type AdvisorRuntimeProbe func(context.Context, string) (bool, error)
type AdvisorRuntimeLaunch func(context.Context, AdvisorSession) error

// AcquireAdvisorSession atomically reserves the singleton session identity for
// an interaction request. Existing reservations attach regardless of the
// candidate identity, which makes concurrent discussion opens deterministic.
func (s *RuntimeStateStore) AcquireAdvisorSession(ctx context.Context, projectID, requestID, issueID, candidateSessionID string) (session AdvisorSession, attached bool, err error) {
	projectID = normalizedProjectID(projectID)
	requestID = strings.TrimSpace(requestID)
	issueID = strings.TrimSpace(issueID)
	candidateSessionID = strings.TrimSpace(candidateSessionID)
	if requestID == "" || issueID == "" || candidateSessionID == "" {
		return session, false, fmt.Errorf("acquire advisor session: project, request, issue, and session ids are required")
	}
	err = sqliteutil.WithWriteLock(s.dbPath, func() error {
		db, openErr := s.dbHandle()
		if openErr != nil {
			return openErr
		}
		var acquireErr error
		session, attached, acquireErr = s.acquireAdvisorSessionLocked(ctx, db, projectID, requestID, issueID, candidateSessionID)
		return acquireErr
	})
	return session, attached, err
}

func (s *RuntimeStateStore) acquireAdvisorSessionLocked(ctx context.Context, db *sql.DB, projectID, requestID, issueID, candidateSessionID string) (session AdvisorSession, attached bool, err error) {
	row := db.QueryRowContext(ctx, `SELECT issue_id, session_id, created_at, updated_at FROM `+advisorSessionTable+` WHERE project_id = ? AND request_id = ?`, projectID, requestID)
	var createdRaw, updatedRaw string
	if scanErr := row.Scan(&session.IssueID, &session.SessionID, &createdRaw, &updatedRaw); scanErr == nil {
		session.ProjectID, session.RequestID = projectID, requestID
		if session.IssueID != issueID {
			return session, false, fmt.Errorf("advisor session request %s belongs to issue %s, not %s", requestID, session.IssueID, issueID)
		}
		if session.CreatedAt, scanErr = time.Parse(time.RFC3339Nano, createdRaw); scanErr != nil {
			return session, false, fmt.Errorf("decode advisor created_at: %w", scanErr)
		}
		if session.UpdatedAt, scanErr = time.Parse(time.RFC3339Nano, updatedRaw); scanErr != nil {
			return session, false, fmt.Errorf("decode advisor updated_at: %w", scanErr)
		}
		attached = true
		return session, true, nil
	} else if scanErr != sql.ErrNoRows {
		return session, false, fmt.Errorf("get advisor session: %w", scanErr)
	}
	now := time.Now().UTC()
	_, writeErr := db.ExecContext(ctx, `INSERT INTO `+advisorSessionTable+` (project_id, request_id, issue_id, session_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, projectID, requestID, issueID, candidateSessionID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if writeErr != nil {
		return session, false, fmt.Errorf("insert advisor session: %w", writeErr)
	}
	session = AdvisorSession{ProjectID: projectID, RequestID: requestID, IssueID: issueID, SessionID: candidateSessionID, CreatedAt: now, UpdatedAt: now}
	return session, false, nil
}

// EnsureAdvisorSession serializes durable reservation, live-runtime probing,
// and launch so concurrent daemons cannot create duplicate advisor runtimes.
func (s *RuntimeStateStore) EnsureAdvisorSession(ctx context.Context, projectID, requestID, issueID, candidateSessionID string, probe AdvisorRuntimeProbe, launch AdvisorRuntimeLaunch) (session AdvisorSession, attached bool, err error) {
	if probe == nil || launch == nil {
		return session, false, fmt.Errorf("ensure advisor session: runtime probe and launch are required")
	}
	projectID, requestID, issueID, candidateSessionID = normalizedProjectID(projectID), strings.TrimSpace(requestID), strings.TrimSpace(issueID), strings.TrimSpace(candidateSessionID)
	if requestID == "" || issueID == "" || candidateSessionID == "" {
		return session, false, fmt.Errorf("ensure advisor session: project, request, issue, and session ids are required")
	}
	err = sqliteutil.WithWriteLock(s.dbPath, func() error {
		db, openErr := s.dbHandle()
		if openErr != nil {
			return openErr
		}
		reserved, _, acquireErr := s.acquireAdvisorSessionLocked(ctx, db, projectID, requestID, issueID, candidateSessionID)
		if acquireErr != nil {
			return acquireErr
		}
		session = reserved
		live, probeErr := probe(ctx, reserved.SessionID)
		if probeErr != nil {
			return fmt.Errorf("probe advisor runtime: %w", probeErr)
		}
		if live {
			attached = true
			return nil
		}
		if launchErr := launch(ctx, reserved); launchErr != nil {
			return fmt.Errorf("launch advisor runtime: %w", launchErr)
		}
		return nil
	})
	return session, attached, err
}

func (s *RuntimeStateStore) GetAdvisorSession(ctx context.Context, projectID, requestID string) (AdvisorSession, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return AdvisorSession{}, false, err
	}
	projectID, requestID = normalizedProjectID(projectID), strings.TrimSpace(requestID)
	var out AdvisorSession
	var createdRaw, updatedRaw string
	err = db.QueryRowContext(ctx, `SELECT issue_id, session_id, created_at, updated_at FROM `+advisorSessionTable+` WHERE project_id = ? AND request_id = ?`, projectID, requestID).Scan(&out.IssueID, &out.SessionID, &createdRaw, &updatedRaw)
	if err == sql.ErrNoRows {
		return AdvisorSession{}, false, nil
	}
	if err != nil {
		return AdvisorSession{}, false, fmt.Errorf("get advisor session: %w", err)
	}
	out.ProjectID, out.RequestID = projectID, requestID
	out.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return AdvisorSession{}, false, fmt.Errorf("decode advisor created_at: %w", err)
	}
	out.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return AdvisorSession{}, false, fmt.Errorf("decode advisor updated_at: %w", err)
	}
	return out, true, nil
}

func (s *RuntimeStateStore) ListAdvisorSessions(ctx context.Context, projectID string) ([]AdvisorSession, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID = normalizedProjectID(projectID)
	rows, err := db.QueryContext(ctx, `SELECT request_id, issue_id, session_id, created_at, updated_at FROM `+advisorSessionTable+` WHERE project_id = ? ORDER BY request_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list advisor sessions: %w", err)
	}
	defer rows.Close()

	out := make([]AdvisorSession, 0)
	for rows.Next() {
		var session AdvisorSession
		var createdRaw, updatedRaw string
		if err := rows.Scan(&session.RequestID, &session.IssueID, &session.SessionID, &createdRaw, &updatedRaw); err != nil {
			return nil, fmt.Errorf("scan advisor session: %w", err)
		}
		session.ProjectID = projectID
		session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return nil, fmt.Errorf("decode advisor created_at: %w", err)
		}
		session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
		if err != nil {
			return nil, fmt.Errorf("decode advisor updated_at: %w", err)
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate advisor sessions: %w", err)
	}
	return out, nil
}

func (s *RuntimeStateStore) DeleteAdvisorSession(ctx context.Context, projectID, requestID string) error {
	projectID, requestID = normalizedProjectID(projectID), strings.TrimSpace(requestID)
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		db, err := s.dbHandle()
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `DELETE FROM `+advisorSessionTable+` WHERE project_id = ? AND request_id = ?`, projectID, requestID)
		if err != nil {
			return fmt.Errorf("delete advisor session: %w", err)
		}
		return nil
	})
}
