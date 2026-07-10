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
		row := db.QueryRowContext(ctx, `SELECT issue_id, session_id, created_at, updated_at FROM `+advisorSessionTable+` WHERE project_id = ? AND request_id = ?`, projectID, requestID)
		var createdRaw, updatedRaw string
		if scanErr := row.Scan(&session.IssueID, &session.SessionID, &createdRaw, &updatedRaw); scanErr == nil {
			session.ProjectID, session.RequestID = projectID, requestID
			if session.IssueID != issueID {
				return fmt.Errorf("advisor session request %s belongs to issue %s, not %s", requestID, session.IssueID, issueID)
			}
			if session.CreatedAt, scanErr = time.Parse(time.RFC3339Nano, createdRaw); scanErr != nil {
				return fmt.Errorf("decode advisor created_at: %w", scanErr)
			}
			if session.UpdatedAt, scanErr = time.Parse(time.RFC3339Nano, updatedRaw); scanErr != nil {
				return fmt.Errorf("decode advisor updated_at: %w", scanErr)
			}
			attached = true
			return nil
		} else if scanErr != sql.ErrNoRows {
			return fmt.Errorf("get advisor session: %w", scanErr)
		}
		now := time.Now().UTC()
		_, writeErr := db.ExecContext(ctx, `INSERT INTO `+advisorSessionTable+` (project_id, request_id, issue_id, session_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, projectID, requestID, issueID, candidateSessionID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if writeErr != nil {
			return fmt.Errorf("insert advisor session: %w", writeErr)
		}
		session = AdvisorSession{ProjectID: projectID, RequestID: requestID, IssueID: issueID, SessionID: candidateSessionID, CreatedAt: now, UpdatedAt: now}
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
