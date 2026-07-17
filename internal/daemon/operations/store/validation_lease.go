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
	"modernc.org/sqlite"
)

func (s *SQLiteStore) AcquireValidation(ctx context.Context, request domain.ValidationAcquire, now time.Time) (domain.ValidationRequest, error) {
	// Preserve source compatibility for internal callers while all transport
	// callers are required to provide the explicit authority dimensions.
	if request.Scope == "" {
		request.Scope = domain.ValidationScopeTicket
	}
	if request.Purpose == "" {
		request.Purpose = domain.ValidationPurposeCapacity
		if request.ReviewerID != "" || request.ReviewEpochEventID != 0 {
			request.Purpose = domain.ValidationPurposeReviewEvidence
		}
	}
	if request.IsolationMode == "" {
		request.IsolationMode = "legacy"
	}
	if request.EnvironmentFingerprint == "" {
		request.EnvironmentFingerprint = "legacy"
	}
	if request.Override == "" {
		request.Override = domain.ValidationOverrideNone
	}
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
	if err := reconcileJoinedValidationTx(ctx, tx, request.ProjectID); err != nil {
		return domain.ValidationRequest{}, err
	}
	compatibilityKey := request.CompatibilityKey()
	execution := domain.ValidationExecutionExecuted
	authoritativeRequestID := request.RequestID
	state := domain.ValidationRequestQueued
	var source *domain.ValidationRequest
	if request.Override != domain.ValidationOverrideForceRerun {
		if request.Override != domain.ValidationOverrideNoReuse {
			source, err = compatibleValidationTx(ctx, tx, request, compatibilityKey, true)
			if err != nil {
				return domain.ValidationRequest{}, err
			}
			if source != nil {
				execution, authoritativeRequestID, state = domain.ValidationExecutionReused, source.RequestID, source.State
			}
		}
		if source == nil && request.Purpose == domain.ValidationPurposeCapacity {
			source, err = compatibleValidationTx(ctx, tx, request, compatibilityKey, false)
			if err != nil {
				return domain.ValidationRequest{}, err
			}
			if source != nil {
				execution, authoritativeRequestID = domain.ValidationExecutionJoined, source.RequestID
			}
		}
	}
	if request.Override == domain.ValidationOverrideEmergency {
		execution, authoritativeRequestID, state = domain.ValidationExecutionSkipped, request.RequestID, domain.ValidationRequestCancelled
		source = nil
	}
	var finishedAt any
	var outcome string
	evidenceJSON := "{}"
	if source != nil && execution == domain.ValidationExecutionReused {
		finishedAt = now.Format(time.RFC3339Nano)
		outcome = "reused " + source.RequestID
		reusedEvidence := source.Evidence
		reusedEvidence.RequestID = request.RequestID
		reusedEvidence.Execution = domain.ValidationExecutionReused
		reusedEvidence.AuthoritativeRequestID = source.RequestID
		encoded, marshalErr := json.Marshal(reusedEvidence)
		if marshalErr != nil {
			return domain.ValidationRequest{}, fmt.Errorf("encode reused validation evidence: %w", marshalErr)
		}
		evidenceJSON = string(encoded)
	} else if execution == domain.ValidationExecutionSkipped {
		finishedAt = now.Format(time.RFC3339Nano)
		outcome = "emergency skip: " + request.OverrideReason
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO daemon_validation_requests
		(request_id,lease_token_hash,project_id,issue_id,class,scope,purpose,execution,authoritative_request_id,compatibility_key,isolation_mode,environment_fingerprint,override_kind,override_actor,override_reason,profile,command,source_revision,reviewer_id,review_epoch_event_id,state,queued_at,heartbeat_at,expires_at,finished_at,outcome,evidence_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.RequestID, validationTokenHash(request.LeaseToken), request.ProjectID, request.IssueID, request.Class, request.Scope, request.Purpose,
		execution, authoritativeRequestID, compatibilityKey, request.IsolationMode, request.EnvironmentFingerprint, request.Override, request.OverrideActor, request.OverrideReason,
		request.Profile, request.Command, request.SourceRevision, request.ReviewerID, request.ReviewEpochEventID, state, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), nullableValidationExpiry(execution, now, request.TTL), finishedAt, outcome, evidenceJSON)
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
	if current.ProjectID != request.ProjectID || current.IssueID != request.IssueID || current.Class != request.Class || current.Scope != request.Scope || current.Purpose != request.Purpose || current.Profile != request.Profile || current.Command != request.Command || current.SourceRevision != request.SourceRevision || current.ReviewerID != request.ReviewerID || current.ReviewEpochEventID != request.ReviewEpochEventID || current.IsolationMode != request.IsolationMode || current.EnvironmentFingerprint != request.EnvironmentFingerprint || current.Override != request.Override || current.OverrideActor != request.OverrideActor || current.OverrideReason != request.OverrideReason {
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
	if err := reconcileJoinedValidationTx(ctx, tx, request.ProjectID); err != nil {
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
	if strings.TrimSpace(authorization.RequestID) == "" || strings.TrimSpace(authorization.LeaseToken) == "" || !authorization.Class.Valid() {
		return domain.ValidationRequest{}, fmt.Errorf("nested validation authorization requires request id, lease token, and supported class")
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
	if authorization.Scope == "" {
		authorization.Scope = current.Scope
	}
	if authorization.Purpose == "" {
		authorization.Purpose = current.Purpose
	}
	if err := authorization.Validate(); err != nil {
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
	if current.Scope != authorization.Scope || current.Purpose != authorization.Purpose {
		return domain.ValidationRequest{}, fmt.Errorf("nested validation cannot change active %s/%s identity to %s/%s", current.Scope, current.Purpose, authorization.Scope, authorization.Purpose)
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
		if validationEvidence.Present && validationEvidence.Scope == "" {
			validationEvidence.Scope = current.Scope
		}
		if validationEvidence.Present && validationEvidence.Purpose == "" {
			validationEvidence.Purpose = current.Purpose
		}
		if validationEvidence.Present && validationEvidence.Execution != "" && validationEvidence.Execution != domain.ValidationExecutionExecuted {
			return domain.ValidationRequest{}, fmt.Errorf("executed validation cannot submit %s evidence", validationEvidence.Execution)
		}
		if validationEvidence.Present && validationEvidence.AuthoritativeRequestID != "" && validationEvidence.AuthoritativeRequestID != current.RequestID {
			return domain.ValidationRequest{}, fmt.Errorf("validation evidence authoritative request does not match held request %s", current.RequestID)
		}
		if validationEvidence.Present && validationEvidence.Execution == "" {
			validationEvidence.Execution = domain.ValidationExecutionExecuted
		}
		if validationEvidence.Present && validationEvidence.AuthoritativeRequestID == "" {
			validationEvidence.AuthoritativeRequestID = current.RequestID
		}
		if validationEvidence.Present && (validationEvidence.RequestID != current.RequestID || validationEvidence.Class != current.Class || validationEvidence.Scope != current.Scope || validationEvidence.Purpose != current.Purpose || validationEvidence.Profile != current.Profile || validationEvidence.SourceRevision != current.SourceRevision || !validationEvidence.Held) {
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
		if err := reconcileJoinedValidationTx(ctx, tx, current.ProjectID); err != nil {
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
	now = now.UTC()
	snapshot, err := s.readValidationSnapshot(ctx, projectID, now)
	if err != nil {
		if isValidationSQLiteBusy(err) {
			return unavailableValidationSnapshot(now, err), nil
		}
		return domain.ValidationSnapshot{}, err
	}
	if snapshot.Freshness == domain.ValidationSnapshotFresh {
		return snapshot, nil
	}
	staleSnapshot := snapshot

	db, err := s.dbHandle()
	if err != nil {
		return domain.ValidationSnapshot{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		if isValidationSQLiteBusy(err) {
			return snapshot, nil
		}
		return domain.ValidationSnapshot{}, fmt.Errorf("begin validation snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := expireAndReconcileValidationTx(ctx, tx, projectID, now, ttl); err != nil {
		if isValidationSQLiteBusy(err) {
			return snapshot, nil
		}
		return domain.ValidationSnapshot{}, err
	}
	snapshot, err = queryValidationSnapshot(ctx, tx, projectID, now)
	if err != nil {
		if isValidationSQLiteBusy(err) {
			return staleSnapshot, nil
		}
		return domain.ValidationSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		if isValidationSQLiteBusy(err) {
			return staleSnapshot, nil
		}
		return domain.ValidationSnapshot{}, fmt.Errorf("commit validation snapshot reconciliation: %w", err)
	}
	snapshot.Freshness = domain.ValidationSnapshotFresh
	snapshot.DegradedReason = ""
	return snapshot, nil
}

type validationSnapshotQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *SQLiteStore) readValidationSnapshot(ctx context.Context, projectID string, now time.Time) (domain.ValidationSnapshot, error) {
	db, err := s.validationReadDBHandle()
	if err != nil {
		return domain.ValidationSnapshot{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("begin validation projection read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := queryValidationSnapshot(ctx, tx, projectID, now)
	if err != nil {
		return domain.ValidationSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("commit validation projection read: %w", err)
	}
	return snapshot, nil
}

func queryValidationSnapshot(ctx context.Context, queryer validationSnapshotQueryer, projectID string, now time.Time) (domain.ValidationSnapshot, error) {
	rows, err := queryer.QueryContext(ctx, validationSelect+` WHERE project_id=? AND (state IN ('active','queued') OR sequence IN (SELECT sequence FROM daemon_validation_requests WHERE project_id=? AND state NOT IN ('active','queued') ORDER BY sequence DESC LIMIT 20)) ORDER BY sequence`, projectID, projectID)
	if err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("list validation requests: %w", err)
	}
	defer rows.Close()
	snapshot := domain.ValidationSnapshot{
		Schema: "azedarach.validation_lease_status.v2", Active: []domain.ValidationRequest{}, Queued: []domain.ValidationRequest{}, Recent: []domain.ValidationRequest{},
		Freshness: domain.ValidationSnapshotFresh, ObservedAt: now,
	}
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
		if (request.State == domain.ValidationRequestActive || request.State == domain.ValidationRequestQueued) && request.ExpiresAt != nil && !request.ExpiresAt.After(now) {
			snapshot.Freshness = domain.ValidationSnapshotStale
			snapshot.DegradedReason = "validation capacity has expired leases pending reconciliation"
		}
	}
	if err := rows.Err(); err != nil {
		return domain.ValidationSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("close validation snapshot rows: %w", err)
	}
	if err := queryer.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM daemon_validation_state WHERE project_id=?),0)`, projectID).Scan(&snapshot.Revision); err != nil {
		return domain.ValidationSnapshot{}, fmt.Errorf("read validation snapshot revision: %w", err)
	}
	return snapshot, nil
}

func unavailableValidationSnapshot(now time.Time, err error) domain.ValidationSnapshot {
	return domain.ValidationSnapshot{
		Schema: "azedarach.validation_lease_status.v2", Active: []domain.ValidationRequest{}, Queued: []domain.ValidationRequest{}, Recent: []domain.ValidationRequest{},
		Freshness: domain.ValidationSnapshotUnavailable, ObservedAt: now, DegradedReason: "validation capacity projection unavailable: " + err.Error(),
	}
}

func isValidationSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 5
}

func (s *SQLiteStore) LatestReviewValidation(ctx context.Context, projectID, issueID string, now time.Time, ttl time.Duration) (*domain.ValidationRequest, error) {
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
	request, err := scanValidationRequest(tx.QueryRowContext(ctx, validationSelect+` WHERE project_id=? AND issue_id=? AND class='aggregate' AND scope='ticket' AND purpose='review_evidence' ORDER BY sequence DESC LIMIT 1`, strings.TrimSpace(projectID), strings.TrimSpace(issueID)))
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

// LatestAggregateValidation remains as a compatibility name for callers that
// have not yet adopted the authority-oriented method name. It intentionally
// returns only explicit review evidence; legacy aggregate rows are excluded.
func (s *SQLiteStore) LatestAggregateValidation(ctx context.Context, projectID, issueID string, now time.Time, ttl time.Duration) (*domain.ValidationRequest, error) {
	return s.LatestReviewValidation(ctx, projectID, issueID, now, ttl)
}

func compatibleValidationTx(ctx context.Context, tx *sql.Tx, target domain.ValidationAcquire, key string, completed bool) (*domain.ValidationRequest, error) {
	condition := `state IN ('active','queued') AND execution='executed'`
	if completed {
		condition = `state='completed' AND execution='executed' AND json_extract(evidence_json,'$.present')=1`
	}
	rows, err := tx.QueryContext(ctx, validationSelect+` WHERE project_id=? AND compatibility_key=? AND `+condition+` ORDER BY sequence DESC`, target.ProjectID, key)
	if err != nil {
		return nil, fmt.Errorf("find compatible validation: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		request, scanErr := scanValidationRequest(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if domain.ValidationRequestCanSatisfy(request, target) {
			if target.Purpose == domain.ValidationPurposeCapacity && request.Evidence.OverlapDetected {
				continue
			}
			return &request, nil
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

func reconcileJoinedValidationTx(ctx context.Context, tx *sql.Tx, projectID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE daemon_validation_requests AS follower
		SET state=(SELECT source.state FROM daemon_validation_requests source WHERE source.request_id=follower.authoritative_request_id),
			finished_at=(SELECT source.finished_at FROM daemon_validation_requests source WHERE source.request_id=follower.authoritative_request_id),
			outcome='joined ' || follower.authoritative_request_id || ': ' || (SELECT source.outcome FROM daemon_validation_requests source WHERE source.request_id=follower.authoritative_request_id),
			evidence_json=json_set((SELECT source.evidence_json FROM daemon_validation_requests source WHERE source.request_id=follower.authoritative_request_id), '$.request_id', follower.request_id, '$.execution', 'joined', '$.authoritative_request_id', follower.authoritative_request_id),
			expires_at=NULL
		WHERE follower.project_id=? AND follower.execution='joined' AND follower.state='queued'
		AND (SELECT source.state FROM daemon_validation_requests source WHERE source.request_id=follower.authoritative_request_id) IN ('completed','failed','cancelled','expired')`, projectID)
	if err != nil {
		return fmt.Errorf("reconcile joined validation requests: %w", err)
	}
	return nil
}

func nullableValidationExpiry(execution domain.ValidationExecution, now time.Time, ttl time.Duration) any {
	if execution == domain.ValidationExecutionReused || execution == domain.ValidationExecutionSkipped {
		return nil
	}
	return now.Add(ttl).Format(time.RFC3339Nano)
}

func expireAndReconcileValidationTx(ctx context.Context, tx *sql.Tx, projectID string, now time.Time, ttl time.Duration) error {
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='expired',finished_at=?,outcome='heartbeat expired',expires_at=NULL WHERE project_id=? AND state IN ('active','queued') AND expires_at IS NOT NULL AND julianday(expires_at)<=julianday(?)`, now.Format(time.RFC3339Nano), projectID, now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("expire stale validation requests: %w", err)
	}
	return reconcileValidationQueueTx(ctx, tx, projectID, now, ttl)
}

func reconcileValidationQueueTx(ctx context.Context, tx *sql.Tx, projectID string, now time.Time, ttl time.Duration) error {
	finished := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='cancelled',finished_at=?,expires_at=NULL,outcome='development validation no longer uses daemon admission' WHERE project_id=? AND state IN ('active','queued') AND purpose='development'`, finished, projectID); err != nil {
		return fmt.Errorf("cancel legacy development validation requests: %w", err)
	}
	// Publication authority is never capacity-admitted. A push or exact-review
	// gate starts immediately even while controlled CI timing owns capacity.
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE project_id=? AND state='queued' AND execution='executed' AND purpose IN ('push_gate','review_evidence')`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), projectID); err != nil {
		return fmt.Errorf("activate publication validation requests: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE project_id=? AND state='queued' AND execution='executed' AND purpose='capacity' AND class='safe'`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), projectID); err != nil {
		return fmt.Errorf("activate safe validation requests: %w", err)
	}
	var activeAggregate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_validation_requests WHERE project_id=? AND state='active' AND purpose='capacity' AND class='aggregate'`, projectID).Scan(&activeAggregate); err != nil {
		return err
	}
	if activeAggregate > 0 {
		return nil
	}
	var firstSequence int64
	var firstClass domain.ValidationClass
	err := tx.QueryRowContext(ctx, `SELECT sequence,class FROM daemon_validation_requests WHERE project_id=? AND state='queued' AND execution='executed' AND purpose='capacity' AND class!='safe' ORDER BY sequence LIMIT 1`, projectID).Scan(&firstSequence, &firstClass)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if firstClass == domain.ValidationClassAggregate {
		var activeShared int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_validation_requests WHERE project_id=? AND state='active' AND purpose='capacity' AND class='shared'`, projectID).Scan(&activeShared); err != nil {
			return err
		}
		if activeShared == 0 {
			_, err = tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE sequence=? AND state='queued'`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), firstSequence)
			return err
		}
		var bypassedShared int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_validation_requests WHERE project_id=? AND purpose='capacity' AND class='shared' AND sequence>? AND started_at IS NOT NULL`, projectID, firstSequence).Scan(&bypassedShared); err != nil {
			return err
		}
		if bypassedShared >= validationAggregateSharedBypassLimit {
			return nil
		}
		// A queued aggregate expresses future exclusivity, not current
		// ownership. Let one later focused request join the current shared
		// generation, then drain shared owners for the aggregate. Counting
		// durable started requests makes the bound survive daemon replacement.
		_, err = tx.ExecContext(ctx, `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE sequence=(SELECT sequence FROM daemon_validation_requests WHERE project_id=? AND state='queued' AND execution='executed' AND purpose='capacity' AND class='shared' AND sequence>? ORDER BY sequence LIMIT 1) AND state='queued'`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), projectID, firstSequence)
		return err
	}
	var nextAggregate sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(sequence) FROM daemon_validation_requests WHERE project_id=? AND state='queued' AND execution='executed' AND purpose='capacity' AND class='aggregate'`, projectID).Scan(&nextAggregate); err != nil {
		return err
	}
	query := `UPDATE daemon_validation_requests SET state='active',started_at=?,heartbeat_at=?,expires_at=? WHERE project_id=? AND state='queued' AND execution='executed' AND purpose='capacity' AND class='shared'`
	args := []any{now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano), projectID}
	if nextAggregate.Valid {
		query += ` AND sequence < ?`
		args = append(args, nextAggregate.Int64)
	}
	_, err = tx.ExecContext(ctx, query, args...)
	return err
}

const validationAggregateSharedBypassLimit = 1

const validationSelect = `SELECT sequence,request_id,project_id,issue_id,class,scope,purpose,execution,authoritative_request_id,compatibility_key,isolation_mode,environment_fingerprint,override_kind,override_actor,override_reason,profile,command,source_revision,reviewer_id,review_epoch_event_id,state,queued_at,started_at,heartbeat_at,expires_at,finished_at,outcome,evidence_json FROM daemon_validation_requests`

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
	if err := scanner.Scan(&request.Sequence, &request.RequestID, &request.ProjectID, &request.IssueID, &request.Class, &request.Scope, &request.Purpose, &request.Execution, &request.AuthoritativeRequestID, &request.CompatibilityKey, &request.IsolationMode, &request.EnvironmentFingerprint, &request.Override, &request.OverrideActor, &request.OverrideReason, &request.Profile, &request.Command, &request.SourceRevision, &request.ReviewerID, &request.ReviewEpochEventID, &request.State, &queued, &started, &heartbeat, &expires, &finished, &request.Outcome, &evidenceJSON); err != nil {
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
