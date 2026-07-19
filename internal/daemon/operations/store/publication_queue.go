package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const publicationOperationSelect = `SELECT
operation_id,project_id,issue_id,intent_key,request_fingerprint,actor_id,reviewer_kind,review_epoch_event_id,accepted_review_event_id,patch_evidence_id,target_id,target_branch,
source_revision,base_revision,candidate_revision,policy_version,environment_fingerprint,
validation_command,evidence_source,evidence_event_id,evidence_seq,evidence_digest,state,lease_owner,
claim_token,claim_expires_at,
validation_request_id,reused_evidence_id,failure_kind,failure_detail,failure_artifact,
created_at,updated_at,started_at,finished_at
FROM daemon_publication_operations`

type PublicationOperationUpdate struct {
	ExpectedStates      []domain.PublicationOperationState
	ExpectedClaimToken  string
	State               domain.PublicationOperationState
	ReleaseClaim        bool
	CandidateRevision   string
	ValidationRequestID string
	ReusedEvidenceID    string
	FailureKind         string
	FailureDetail       string
	FailureArtifact     string
	StartedAt           *time.Time
	FinishedAt          *time.Time
	UpdatedAt           time.Time
}

type PublicationOperationClaim struct {
	Owner string
	Token string
	Now   time.Time
	TTL   time.Duration
}

func (s *SQLiteStore) ClaimPublicationOperation(ctx context.Context, operationID string, claim PublicationOperationClaim) (domain.PublicationOperation, bool, error) {
	operationID = strings.TrimSpace(operationID)
	claim.Owner = strings.TrimSpace(claim.Owner)
	claim.Token = strings.TrimSpace(claim.Token)
	if operationID == "" || claim.Owner == "" || claim.Token == "" || claim.TTL <= 0 {
		return domain.PublicationOperation{}, false, fmt.Errorf("publication claim requires operation, owner, token, and positive TTL")
	}
	now := claim.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	expires := now.Add(claim.TTL)
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationOperation{}, false, err
	}
	result, err := db.ExecContext(ctx, `UPDATE daemon_publication_operations
		SET state=CASE WHEN state='queued' THEN 'preparing' ELSE state END,
			lease_owner=?,claim_token=?,claim_expires_at=?,started_at=COALESCE(started_at,?),updated_at=?
		WHERE operation_id=? AND state IN ('queued','preparing','validating','passed')
			AND (claim_token='' OR claim_expires_at IS NULL OR claim_expires_at<=?)
			AND NOT EXISTS (
				SELECT 1 FROM daemon_publication_operations AS earlier
				WHERE earlier.project_id=daemon_publication_operations.project_id
					AND earlier.target_branch=daemon_publication_operations.target_branch
					AND earlier.operation_id<>daemon_publication_operations.operation_id
					AND earlier.state IN ('queued','preparing','validating','passed')
					AND (earlier.created_at<daemon_publication_operations.created_at
						OR (earlier.created_at=daemon_publication_operations.created_at AND earlier.operation_id<daemon_publication_operations.operation_id))
			)`,
		claim.Owner, claim.Token, expires.UnixNano(), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), operationID, now.UnixNano())
	if err != nil {
		return domain.PublicationOperation{}, false, fmt.Errorf("claim publication operation: %w", err)
	}
	changed, _ := result.RowsAffected()
	operation, found, err := s.PublicationOperation(ctx, operationID)
	if err != nil {
		return domain.PublicationOperation{}, false, err
	}
	if !found {
		return domain.PublicationOperation{}, false, fmt.Errorf("publication operation %s not found", operationID)
	}
	return operation, changed == 1, nil
}

func (s *SQLiteStore) RenewPublicationOperationClaim(ctx context.Context, operationID, claimToken string, now time.Time, ttl time.Duration) (domain.PublicationOperation, error) {
	operationID, claimToken = strings.TrimSpace(operationID), strings.TrimSpace(claimToken)
	if operationID == "" || claimToken == "" || ttl <= 0 {
		return domain.PublicationOperation{}, fmt.Errorf("publication claim renewal requires operation, token, and positive TTL")
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	result, err := db.ExecContext(ctx, `UPDATE daemon_publication_operations SET claim_expires_at=?,updated_at=?
		WHERE operation_id=? AND claim_token=? AND claim_expires_at>? AND state IN ('preparing','validating','passed')`,
		now.Add(ttl).UnixNano(), now.Format(time.RFC3339Nano), operationID, claimToken, now.UnixNano())
	if err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("renew publication claim: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return domain.PublicationOperation{}, fmt.Errorf("publication operation %s claim is no longer owned", operationID)
	}
	updated, found, err := s.PublicationOperation(ctx, operationID)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	if !found {
		return domain.PublicationOperation{}, fmt.Errorf("publication operation %s disappeared after claim renewal", operationID)
	}
	return updated, nil
}

func (s *SQLiteStore) EnqueuePublication(ctx context.Context, operation domain.PublicationOperation, coalesceKey string) (domain.PublicationOperation, bool, error) {
	operation.OperationID = strings.TrimSpace(operation.OperationID)
	operation.ProjectID = strings.TrimSpace(operation.ProjectID)
	operation.IssueID = strings.TrimSpace(operation.IssueID)
	operation.IntentKey = strings.TrimSpace(operation.IntentKey)
	operation.State = domain.PublicationOperationQueued
	coalesceKey = strings.TrimSpace(coalesceKey)
	if err := operation.ValidateIntent(); err != nil {
		return domain.PublicationOperation{}, false, err
	}
	if coalesceKey == "" {
		return domain.PublicationOperation{}, false, fmt.Errorf("publication operation requires coalesce_key")
	}
	now := operation.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	operation.CreatedAt, operation.UpdatedAt = now, now
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationOperation{}, false, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO daemon_publication_operations(
		operation_id,project_id,issue_id,intent_key,request_fingerprint,actor_id,reviewer_kind,review_epoch_event_id,accepted_review_event_id,patch_evidence_id,target_id,target_branch,
		source_revision,base_revision,candidate_revision,policy_version,environment_fingerprint,
		validation_command,evidence_source,evidence_event_id,evidence_seq,evidence_digest,coalesce_key,state,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		operation.OperationID, operation.ProjectID, operation.IssueID, operation.IntentKey, operation.RequestFingerprint,
		operation.ActorID, operation.ReviewerKind, operation.ReviewEpochEventID, operation.AcceptedReviewEventID, operation.PatchEvidenceID,
		operation.TargetID, operation.TargetBranch, operation.SourceRevision, operation.BaseRevision,
		operation.CandidateRevision, operation.PolicyVersion, operation.EnvironmentFingerprint, operation.ValidationCommand, operation.EvidenceSource,
		operation.EvidenceEventID, operation.EvidenceSeq, operation.EvidenceDigest, coalesceKey, operation.State,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err == nil {
		return s.PublicationOperation(ctx, operation.OperationID)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		return domain.PublicationOperation{}, false, fmt.Errorf("enqueue publication operation: %w", err)
	}
	existing, getErr := scanPublicationOperation(db.QueryRowContext(ctx, publicationOperationSelect+` WHERE project_id=? AND (coalesce_key=? OR (issue_id=? AND intent_key=?)) ORDER BY created_at LIMIT 1`, operation.ProjectID, coalesceKey, operation.IssueID, operation.IntentKey))
	if getErr != nil {
		return domain.PublicationOperation{}, false, fmt.Errorf("resolve coalesced publication operation: %w", getErr)
	}
	if !publicationIntentCompatible(existing, operation) {
		return domain.PublicationOperation{}, false, fmt.Errorf("publication intent conflicts with existing operation %s", existing.OperationID)
	}
	existing.QueuePosition, _ = s.publicationQueuePosition(ctx, existing)
	return existing, false, nil
}

func (s *SQLiteStore) PublicationOperation(ctx context.Context, operationID string) (domain.PublicationOperation, bool, error) {
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationOperation{}, false, err
	}
	operation, err := scanPublicationOperation(db.QueryRowContext(ctx, publicationOperationSelect+` WHERE operation_id=?`, strings.TrimSpace(operationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PublicationOperation{}, false, nil
	}
	if err != nil {
		return domain.PublicationOperation{}, false, fmt.Errorf("read publication operation: %w", err)
	}
	operation.QueuePosition, _ = s.publicationQueuePosition(ctx, operation)
	return operation, true, nil
}

func (s *SQLiteStore) PublicationOperations(ctx context.Context, projectID, issueID string, nonterminalOnly bool) ([]domain.PublicationOperation, error) {
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	where := []string{"project_id=?"}
	args := []any{strings.TrimSpace(projectID)}
	if issueID = strings.TrimSpace(issueID); issueID != "" {
		where, args = append(where, "issue_id=?"), append(args, issueID)
	}
	if nonterminalOnly {
		where = append(where, "state IN ('queued','preparing','validating','passed')")
	}
	rows, err := db.QueryContext(ctx, publicationOperationSelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at,operation_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list publication operations: %w", err)
	}
	var out []domain.PublicationOperation
	for rows.Next() {
		operation, scanErr := scanPublicationOperation(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		out = append(out, operation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// The store intentionally uses one SQLite connection. Finish consuming and
	// close the list cursor before issuing the per-row queue-position queries.
	for i := range out {
		out[i].QueuePosition, _ = s.publicationQueuePosition(ctx, out[i])
	}
	return out, nil
}

func (s *SQLiteStore) UpdatePublicationOperation(ctx context.Context, operationID string, update PublicationOperationUpdate) (domain.PublicationOperation, error) {
	if !update.State.Valid() {
		return domain.PublicationOperation{}, fmt.Errorf("invalid publication operation state %q", update.State)
	}
	if len(update.ExpectedStates) == 0 {
		return domain.PublicationOperation{}, fmt.Errorf("publication update requires expected state")
	}
	if strings.TrimSpace(update.ExpectedClaimToken) == "" {
		return domain.PublicationOperation{}, fmt.Errorf("publication update requires expected claim token")
	}
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	now := update.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	placeholders := make([]string, len(update.ExpectedStates))
	args := []any{string(update.State), update.ReleaseClaim, update.ReleaseClaim, update.ReleaseClaim, strings.TrimSpace(update.CandidateRevision), strings.TrimSpace(update.ValidationRequestID), strings.TrimSpace(update.ReusedEvidenceID), strings.TrimSpace(update.FailureKind), strings.TrimSpace(update.FailureDetail), strings.TrimSpace(update.FailureArtifact), nullableTime(update.StartedAt), nullableTime(update.FinishedAt), now.Format(time.RFC3339Nano), strings.TrimSpace(operationID), strings.TrimSpace(update.ExpectedClaimToken), now.UnixNano()}
	for i, state := range update.ExpectedStates {
		placeholders[i] = "?"
		args = append(args, string(state))
	}
	result, err := db.ExecContext(ctx, `UPDATE daemon_publication_operations SET state=?,lease_owner=CASE WHEN ? THEN '' ELSE lease_owner END,claim_token=CASE WHEN ? THEN '' ELSE claim_token END,claim_expires_at=CASE WHEN ? THEN NULL ELSE claim_expires_at END,candidate_revision=?,validation_request_id=?,reused_evidence_id=?,failure_kind=?,failure_detail=?,failure_artifact=?,started_at=COALESCE(?,started_at),finished_at=?,updated_at=? WHERE operation_id=? AND claim_token=? AND claim_expires_at>? AND state IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("update publication operation: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return domain.PublicationOperation{}, fmt.Errorf("publication operation %s state changed concurrently", operationID)
	}
	updated, found, err := s.PublicationOperation(ctx, operationID)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	if !found {
		return domain.PublicationOperation{}, fmt.Errorf("publication operation %s disappeared after update", operationID)
	}
	return updated, nil
}

// TerminalizePublicationWithSuccessor atomically commits a claim-fenced stale
// predecessor and its durable nonterminal successor. A crash can observe
// neither change or both, never terminal history without continuation.
func (s *SQLiteStore) TerminalizePublicationWithSuccessor(ctx context.Context, operationID string, update PublicationOperationUpdate, successor domain.PublicationOperation, coalesceKey string) (domain.PublicationOperation, domain.PublicationOperation, error) {
	if !update.State.Terminal() || len(update.ExpectedStates) == 0 || strings.TrimSpace(update.ExpectedClaimToken) == "" {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, fmt.Errorf("atomic publication successor requires terminal state, expected state, and claim token")
	}
	successor.State = domain.PublicationOperationQueued
	coalesceKey = strings.TrimSpace(coalesceKey)
	if err := successor.ValidateIntent(); err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, err
	}
	if coalesceKey == "" {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, fmt.Errorf("atomic publication successor requires coalesce_key")
	}
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, err
	}
	now := update.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, fmt.Errorf("begin atomic publication successor: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	predecessor, err := getPublicationOperationTx(ctx, tx, operationID)
	if err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, err
	}
	if predecessor.ProjectID != successor.ProjectID || predecessor.IssueID != successor.IssueID || predecessor.TargetID != successor.TargetID || predecessor.TargetBranch != successor.TargetBranch || predecessor.SourceRevision != successor.SourceRevision || predecessor.RequestFingerprint != successor.RequestFingerprint {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, fmt.Errorf("publication successor identity does not match predecessor %s", operationID)
	}
	placeholders := make([]string, len(update.ExpectedStates))
	args := []any{string(update.State), update.ReleaseClaim, update.ReleaseClaim, update.ReleaseClaim, strings.TrimSpace(update.CandidateRevision), strings.TrimSpace(update.ValidationRequestID), strings.TrimSpace(update.ReusedEvidenceID), strings.TrimSpace(update.FailureKind), strings.TrimSpace(update.FailureDetail), strings.TrimSpace(update.FailureArtifact), nullableTime(update.StartedAt), nullableTime(update.FinishedAt), now.Format(time.RFC3339Nano), strings.TrimSpace(operationID), strings.TrimSpace(update.ExpectedClaimToken), now.UnixNano()}
	for i, state := range update.ExpectedStates {
		placeholders[i] = "?"
		args = append(args, string(state))
	}
	result, err := tx.ExecContext(ctx, `UPDATE daemon_publication_operations SET state=?,lease_owner=CASE WHEN ? THEN '' ELSE lease_owner END,claim_token=CASE WHEN ? THEN '' ELSE claim_token END,claim_expires_at=CASE WHEN ? THEN NULL ELSE claim_expires_at END,candidate_revision=?,validation_request_id=?,reused_evidence_id=?,failure_kind=?,failure_detail=?,failure_artifact=?,started_at=COALESCE(?,started_at),finished_at=?,updated_at=? WHERE operation_id=? AND claim_token=? AND claim_expires_at>? AND state IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, fmt.Errorf("terminalize publication predecessor: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, fmt.Errorf("publication operation %s state changed concurrently", operationID)
	}
	createdAt := successor.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = now
	}
	_, insertErr := tx.ExecContext(ctx, `INSERT INTO daemon_publication_operations(
		operation_id,project_id,issue_id,intent_key,request_fingerprint,actor_id,target_id,target_branch,
		reviewer_kind,review_epoch_event_id,accepted_review_event_id,patch_evidence_id,
		source_revision,base_revision,candidate_revision,policy_version,environment_fingerprint,
		validation_command,evidence_source,evidence_event_id,evidence_seq,evidence_digest,coalesce_key,state,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		successor.OperationID, successor.ProjectID, successor.IssueID, successor.IntentKey, successor.RequestFingerprint,
		successor.ActorID, successor.TargetID, successor.TargetBranch, successor.ReviewerKind, successor.ReviewEpochEventID, successor.AcceptedReviewEventID, successor.PatchEvidenceID,
		successor.SourceRevision, successor.BaseRevision,
		successor.CandidateRevision, successor.PolicyVersion, successor.EnvironmentFingerprint, successor.ValidationCommand, successor.EvidenceSource,
		successor.EvidenceEventID, successor.EvidenceSeq, successor.EvidenceDigest, coalesceKey, domain.PublicationOperationQueued,
		createdAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if insertErr != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, fmt.Errorf("insert atomic publication successor: %w", insertErr)
	}
	terminal, err := getPublicationOperationTx(ctx, tx, operationID)
	if err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, err
	}
	persistedSuccessor, err := getPublicationOperationTx(ctx, tx, successor.OperationID)
	if err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, fmt.Errorf("commit atomic publication successor: %w", err)
	}
	return terminal, persistedSuccessor, nil
}

func getPublicationOperationTx(ctx context.Context, tx *sql.Tx, operationID string) (domain.PublicationOperation, error) {
	operation, err := scanPublicationOperation(tx.QueryRowContext(ctx, publicationOperationSelect+` WHERE operation_id=?`, strings.TrimSpace(operationID)))
	if err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("read publication operation %s: %w", operationID, err)
	}
	return operation, nil
}

func (s *SQLiteStore) publicationQueuePosition(ctx context.Context, operation domain.PublicationOperation) (int, error) {
	if operation.State != domain.PublicationOperationQueued {
		return 0, nil
	}
	db, err := s.dbHandle()
	if err != nil {
		return 0, err
	}
	var position int
	err = db.QueryRowContext(ctx, `SELECT 1+COUNT(*) FROM daemon_publication_operations WHERE project_id=? AND target_branch=? AND state IN ('queued','preparing','validating','passed') AND (created_at<? OR (created_at=? AND operation_id<?))`, operation.ProjectID, operation.TargetBranch, operation.CreatedAt.Format(time.RFC3339Nano), operation.CreatedAt.Format(time.RFC3339Nano), operation.OperationID).Scan(&position)
	return position, err
}

func scanPublicationOperation(scanner validationScanner) (domain.PublicationOperation, error) {
	var operation domain.PublicationOperation
	var created, updated string
	var started, finished sql.NullString
	var claimExpiresAt sql.NullInt64
	err := scanner.Scan(&operation.OperationID, &operation.ProjectID, &operation.IssueID, &operation.IntentKey, &operation.RequestFingerprint, &operation.ActorID, &operation.ReviewerKind, &operation.ReviewEpochEventID, &operation.AcceptedReviewEventID, &operation.PatchEvidenceID, &operation.TargetID, &operation.TargetBranch, &operation.SourceRevision, &operation.BaseRevision, &operation.CandidateRevision, &operation.PolicyVersion, &operation.EnvironmentFingerprint, &operation.ValidationCommand, &operation.EvidenceSource, &operation.EvidenceEventID, &operation.EvidenceSeq, &operation.EvidenceDigest, &operation.State, &operation.LeaseOwner, &operation.ClaimToken, &claimExpiresAt, &operation.ValidationRequestID, &operation.ReusedEvidenceID, &operation.FailureKind, &operation.FailureDetail, &operation.FailureArtifact, &created, &updated, &started, &finished)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	operation.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	operation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	if started.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, started.String)
		if parseErr != nil {
			return domain.PublicationOperation{}, parseErr
		}
		operation.StartedAt = &value
	}
	if claimExpiresAt.Valid {
		value := time.Unix(0, claimExpiresAt.Int64).UTC()
		operation.ClaimExpiresAt = &value
	}
	if finished.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, finished.String)
		if parseErr != nil {
			return domain.PublicationOperation{}, parseErr
		}
		operation.FinishedAt = &value
	}
	return operation, nil
}

func publicationIntentCompatible(left, right domain.PublicationOperation) bool {
	return left.ProjectID == right.ProjectID && left.IssueID == right.IssueID &&
		left.RequestFingerprint == right.RequestFingerprint && left.TargetID == right.TargetID &&
		left.TargetBranch == right.TargetBranch && left.SourceRevision == right.SourceRevision &&
		left.BaseRevision == right.BaseRevision && left.PolicyVersion == right.PolicyVersion &&
		left.EnvironmentFingerprint == right.EnvironmentFingerprint && left.ValidationCommand == right.ValidationCommand && left.EvidenceDigest == right.EvidenceDigest &&
		strings.EqualFold(left.ActorID, right.ActorID) && left.ReviewerKind == right.ReviewerKind && left.ReviewEpochEventID == right.ReviewEpochEventID && left.PatchEvidenceID == right.PatchEvidenceID
}
