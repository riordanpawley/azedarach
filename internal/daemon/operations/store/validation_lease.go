package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func (s *SQLiteStore) AcquireValidation(ctx context.Context, request domain.ValidationAcquire, now time.Time) (domain.ValidationRequest, error) {
	if err := request.Validate(); err != nil {
		return domain.ValidationRequest{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	now = now.UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ValidationRequest{}, fmt.Errorf("begin validation acquire: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := expireAndReconcileValidationTx(ctx, tx, request.ProjectID, now, request.TTL); err != nil {
		return domain.ValidationRequest{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO daemon_validation_requests
		(request_id,lease_token_hash,project_id,issue_id,class,profile,command,source_revision,state,queued_at,heartbeat_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,'queued',?,?,?)`, request.RequestID, validationTokenHash(request.LeaseToken), request.ProjectID, request.IssueID, request.Class,
		request.Profile, request.Command, request.SourceRevision, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(request.TTL).Format(time.RFC3339Nano))
	if err != nil {
		return domain.ValidationRequest{}, fmt.Errorf("queue validation request: %w", err)
	}
	current, err := getValidationRequestTx(ctx, tx, request.RequestID)
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	if err := authenticateValidationRequestTx(ctx, tx, request.RequestID, request.LeaseToken); err != nil {
		return domain.ValidationRequest{}, err
	}
	if current.ProjectID != request.ProjectID || current.IssueID != request.IssueID || current.Class != request.Class || current.Profile != request.Profile || current.Command != request.Command || current.SourceRevision != request.SourceRevision {
		return domain.ValidationRequest{}, fmt.Errorf("validation request id %s already exists with different identity", request.RequestID)
	}
	if current.State == domain.ValidationRequestQueued {
		if _, err := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET heartbeat_at=?,expires_at=? WHERE request_id=? AND state='queued'`, now.Format(time.RFC3339Nano), now.Add(request.TTL).Format(time.RFC3339Nano), request.RequestID); err != nil {
			return domain.ValidationRequest{}, fmt.Errorf("heartbeat queued validation request: %w", err)
		}
	}
	if err := reconcileValidationQueueTx(ctx, tx, request.ProjectID, now, request.TTL); err != nil {
		return domain.ValidationRequest{}, err
	}
	current, err = getValidationRequestTx(ctx, tx, request.RequestID)
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ValidationRequest{}, fmt.Errorf("commit validation acquire: %w", err)
	}
	return current, nil
}

func (s *SQLiteStore) HeartbeatValidation(ctx context.Context, requestID, leaseToken string, now time.Time, ttl time.Duration) (domain.ValidationRequest, error) {
	return s.transitionValidation(ctx, requestID, leaseToken, now, ttl, "heartbeat", "")
}

func (s *SQLiteStore) AuthorizeNestedValidation(ctx context.Context, authorization domain.ValidationNestedAuthorization, now time.Time, ttl time.Duration) (domain.ValidationRequest, error) {
	if err := authorization.Validate(); err != nil {
		return domain.ValidationRequest{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ValidationRequest{}, fmt.Errorf("begin nested validation authorization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getValidationRequestTx(ctx, tx, authorization.RequestID)
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	if err := authenticateValidationRequestTx(ctx, tx, authorization.RequestID, authorization.LeaseToken); err != nil {
		return domain.ValidationRequest{}, err
	}
	if err := expireAndReconcileValidationTx(ctx, tx, current.ProjectID, now.UTC(), ttl); err != nil {
		return domain.ValidationRequest{}, err
	}
	current, err = getValidationRequestTx(ctx, tx, authorization.RequestID)
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	if current.State != domain.ValidationRequestActive {
		return domain.ValidationRequest{}, fmt.Errorf("validation request %s is not active", authorization.RequestID)
	}
	if current.Class != domain.ValidationClassAggregate && current.Class != authorization.Class {
		return domain.ValidationRequest{}, fmt.Errorf("nested %s validation cannot join active %s request", authorization.Class, current.Class)
	}
	if err := tx.Commit(); err != nil {
		return domain.ValidationRequest{}, fmt.Errorf("commit nested validation authorization: %w", err)
	}
	return current, nil
}

func (s *SQLiteStore) FinishValidation(ctx context.Context, requestID, leaseToken string, state domain.ValidationRequestState, outcome string, evidence domain.ValidationEvidence, now time.Time, ttl time.Duration) (domain.ValidationRequest, error) {
	if state != domain.ValidationRequestCompleted && state != domain.ValidationRequestCancelled && state != domain.ValidationRequestFailed {
		return domain.ValidationRequest{}, fmt.Errorf("unsupported terminal validation state %q", state)
	}
	return s.transitionValidation(ctx, requestID, leaseToken, now, ttl, string(state), strings.TrimSpace(outcome), evidence)
}

func (s *SQLiteStore) transitionValidation(ctx context.Context, requestID, leaseToken string, now time.Time, ttl time.Duration, action, outcome string, evidence ...domain.ValidationEvidence) (domain.ValidationRequest, error) {
	db, err := s.dbHandle()
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	now = now.UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ValidationRequest{}, fmt.Errorf("begin validation %s: %w", action, err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getValidationRequestTx(ctx, tx, requestID)
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	if err := authenticateValidationRequestTx(ctx, tx, requestID, leaseToken); err != nil {
		return domain.ValidationRequest{}, err
	}
	if err := expireAndReconcileValidationTx(ctx, tx, current.ProjectID, now, ttl); err != nil {
		return domain.ValidationRequest{}, err
	}
	switch action {
	case "heartbeat":
		expires := now.Add(ttl).Format(time.RFC3339Nano)
		result, execErr := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET heartbeat_at=?,expires_at=? WHERE request_id=? AND state='active'`, now.Format(time.RFC3339Nano), expires, requestID)
		if execErr != nil {
			return domain.ValidationRequest{}, fmt.Errorf("heartbeat validation request: %w", execErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return domain.ValidationRequest{}, fmt.Errorf("validation request %s is not active", requestID)
		}
	default:
		validationEvidence := domain.ValidationEvidence{}
		if len(evidence) > 0 {
			validationEvidence = evidence[0]
		}
		if validationEvidence.Present && (validationEvidence.RequestID != current.RequestID || validationEvidence.Class != current.Class || validationEvidence.Profile != current.Profile || validationEvidence.SourceRevision != current.SourceRevision || !validationEvidence.Held) {
			return domain.ValidationRequest{}, fmt.Errorf("validation evidence identity does not match held request %s", current.RequestID)
		}
		evidenceJSON, marshalErr := json.Marshal(validationEvidence)
		if marshalErr != nil {
			return domain.ValidationRequest{}, fmt.Errorf("encode validation evidence: %w", marshalErr)
		}
		result, execErr := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state=?,finished_at=?,outcome=?,evidence_json=?,expires_at=NULL WHERE request_id=? AND state IN ('queued','active')`, action, now.Format(time.RFC3339Nano), outcome, string(evidenceJSON), requestID)
		if execErr != nil {
			return domain.ValidationRequest{}, fmt.Errorf("finish validation request: %w", execErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return domain.ValidationRequest{}, fmt.Errorf("validation request %s is already terminal", requestID)
		}
		if err := reconcileValidationQueueTx(ctx, tx, current.ProjectID, now, ttl); err != nil {
			return domain.ValidationRequest{}, err
		}
	}
	current, err = getValidationRequestTx(ctx, tx, requestID)
	if err != nil {
		return domain.ValidationRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ValidationRequest{}, fmt.Errorf("commit validation %s: %w", action, err)
	}
	return current, nil
}

func (s *SQLiteStore) ValidationSnapshot(ctx context.Context, projectID string, now time.Time, ttl time.Duration) (domain.ValidationSnapshot, error) {
	db, err := s.dbHandle()
	if err != nil {
		return domain.ValidationSnapshot{}, err
	}
	now = now.UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("begin validation snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := expireAndReconcileValidationTx(ctx, tx, projectID, now, ttl); err != nil {
		return domain.ValidationSnapshot{}, err
	}
	rows, err := tx.QueryContext(ctx, validationSelect+` WHERE project_id=? AND (state IN ('active','queued') OR sequence IN (SELECT sequence FROM daemon_validation_requests WHERE project_id=? AND state NOT IN ('active','queued') ORDER BY sequence DESC LIMIT 20)) ORDER BY sequence`, projectID, projectID)
	if err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("list validation requests: %w", err)
	}
	defer rows.Close()
	snapshot := domain.ValidationSnapshot{Schema: "azedarach.validation_lease_status.v1", Active: []domain.ValidationRequest{}, Queued: []domain.ValidationRequest{}, Recent: []domain.ValidationRequest{}}
	for rows.Next() {
		request, scanErr := scanValidationRequest(rows)
		if scanErr != nil {
			return domain.ValidationSnapshot{}, scanErr
		}
		switch request.State {
		case domain.ValidationRequestActive:
			snapshot.Active = append(snapshot.Active, request)
		case domain.ValidationRequestQueued:
			snapshot.Queued = append(snapshot.Queued, request)
		default:
			snapshot.Recent = append(snapshot.Recent, request)
		}
	}
	if err := rows.Err(); err != nil {
		return domain.ValidationSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("close validation snapshot rows: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM daemon_validation_state WHERE project_id=?),0)`, projectID).Scan(&snapshot.Revision); err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("read validation snapshot revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("commit validation snapshot reconciliation: %w", err)
	}
	return snapshot, nil
}

func (s *SQLiteStore) LatestAggregateValidation(ctx context.Context, projectID, issueID string, now time.Time, ttl time.Duration) (*domain.ValidationRequest, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin latest aggregate validation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := expireAndReconcileValidationTx(ctx, tx, strings.TrimSpace(projectID), now.UTC(), ttl); err != nil {
		return nil, err
	}
	request, err := scanValidationRequest(tx.QueryRowContext(ctx, validationSelect+` WHERE project_id=? AND issue_id=? AND class='aggregate' ORDER BY sequence DESC LIMIT 1`, strings.TrimSpace(projectID), strings.TrimSpace(issueID)))
	if errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, fmt.Errorf("commit latest aggregate validation reconciliation: %w", commitErr)
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit latest aggregate validation reconciliation: %w", err)
	}
	return &request, nil
}

func expireAndReconcileValidationTx(ctx context.Context, tx *sql.Tx, projectID string, now time.Time, ttl time.Duration) error {
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='expired',finished_at=?,outcome='heartbeat expired',expires_at=NULL WHERE project_id=? AND state IN ('active','queued') AND expires_at IS NOT NULL AND julianday(expires_at)<=julianday(?)`, now.Format(time.RFC3339Nano), projectID, now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("expire stale validation requests: %w", err)
	}
	return reconcileValidationQueueTx(ctx, tx, projectID, now, ttl)
}

func reconcileValidationQueueTx(ctx context.Context, tx *sql.Tx, projectID string, now time.Time, ttl time.Duration) error {
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE project_id=? AND state='queued' AND class='safe'`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), projectID); err != nil {
		return fmt.Errorf("activate safe validation requests: %w", err)
	}
	var activeAggregate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_validation_requests WHERE project_id=? AND state='active' AND class='aggregate'`, projectID).Scan(&activeAggregate); err != nil {
		return err
	}
	if activeAggregate > 0 {
		return nil
	}
	var firstSequence int64
	var firstClass domain.ValidationClass
	err := tx.QueryRowContext(ctx, `SELECT sequence,class FROM daemon_validation_requests WHERE project_id=? AND state='queued' AND class!='safe' ORDER BY sequence LIMIT 1`, projectID).Scan(&firstSequence, &firstClass)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if firstClass == domain.ValidationClassAggregate {
		var activeShared int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_validation_requests WHERE project_id=? AND state='active' AND class='shared'`, projectID).Scan(&activeShared); err != nil {
			return err
		}
		if activeShared == 0 {
			_, err = tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE sequence=? AND state='queued'`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), firstSequence)
			return err
		}
		var bypassedShared int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_validation_requests WHERE project_id=? AND class='shared' AND sequence>? AND started_at IS NOT NULL`, projectID, firstSequence).Scan(&bypassedShared); err != nil {
			return err
		}
		if bypassedShared >= validationAggregateSharedBypassLimit {
			return nil
		}
		// A queued aggregate expresses future exclusivity, not current
		// ownership. Let one later focused request join the current shared
		// generation, then drain shared owners for the aggregate. Counting
		// durable started requests makes the bound survive daemon replacement.
		_, err = tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE sequence=(SELECT sequence FROM daemon_validation_requests WHERE project_id=? AND state='queued' AND class='shared' AND sequence>? ORDER BY sequence LIMIT 1) AND state='queued'`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), projectID, firstSequence)
		return err
	}
	var nextAggregate sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(sequence) FROM daemon_validation_requests WHERE project_id=? AND state='queued' AND class='aggregate'`, projectID).Scan(&nextAggregate); err != nil {
		return err
	}
	query := `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE project_id=? AND state='queued' AND class='shared'`
	args := []any{now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), projectID}
	if nextAggregate.Valid {
		query += ` AND sequence < ?`
		args = append(args, nextAggregate.Int64)
	}
	_, err = tx.ExecContext(ctx, query, args...)
	return err
}

const validationAggregateSharedBypassLimit = 1

const validationSelect = `SELECT sequence,request_id,project_id,issue_id,class,profile,command,source_revision,state,queued_at,started_at,heartbeat_at,expires_at,finished_at,outcome,evidence_json FROM daemon_validation_requests`

type validationScanner interface{ Scan(...any) error }

func getValidationRequestTx(ctx context.Context, tx *sql.Tx, requestID string) (domain.ValidationRequest, error) {
	request, err := scanValidationRequest(tx.QueryRowContext(ctx, validationSelect+` WHERE request_id=?`, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ValidationRequest{}, fmt.Errorf("validation request %s not found", requestID)
	}
	return request, err
}

func authenticateValidationRequestTx(ctx context.Context, tx *sql.Tx, requestID, leaseToken string) error {
	var storedHash string
	if err := tx.QueryRowContext(ctx, `SELECT lease_token_hash FROM daemon_validation_requests WHERE request_id=?`, requestID).Scan(&storedHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("validation request %s not found", requestID)
		}
		return err
	}
	want := validationTokenHash(leaseToken)
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(want)) != 1 {
		return fmt.Errorf("validation request %s lease token rejected", requestID)
	}
	return nil
}

func validationTokenHash(token string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}

func scanValidationRequest(scanner validationScanner) (domain.ValidationRequest, error) {
	var request domain.ValidationRequest
	var queued string
	var started, heartbeat, expires, finished sql.NullString
	var evidenceJSON string
	if err := scanner.Scan(&request.Sequence, &request.RequestID, &request.ProjectID, &request.IssueID, &request.Class, &request.Profile, &request.Command, &request.SourceRevision, &request.State, &queued, &started, &heartbeat, &expires, &finished, &request.Outcome, &evidenceJSON); err != nil {
		return request, err
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &request.Evidence); err != nil {
		return request, fmt.Errorf("decode validation evidence: %w", err)
	}
	var err error
	if request.QueuedAt, err = time.Parse(time.RFC3339Nano, queued); err != nil {
		return request, err
	}
	if request.StartedAt, err = parseOptionalValidationTime(started); err != nil {
		return request, err
	}
	if request.HeartbeatAt, err = parseOptionalValidationTime(heartbeat); err != nil {
		return request, err
	}
	if request.ExpiresAt, err = parseOptionalValidationTime(expires); err != nil {
		return request, err
	}
	if request.FinishedAt, err = parseOptionalValidationTime(finished); err != nil {
		return request, err
	}
	return request, nil
}

func parseOptionalValidationTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
