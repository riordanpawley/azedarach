package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func (s *SQLiteStore) RecordPublicationEvidence(ctx context.Context, evidence domain.PublicationEvidence) (domain.PublicationEvidence, error) {
	if evidence.CreatedAt.IsZero() {
		evidence.CreatedAt = time.Now().UTC()
	}
	if err := evidence.Validate(); err != nil {
		return domain.PublicationEvidence{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	coverageJSON, err := json.Marshal(evidence.Coverage)
	if err != nil {
		return domain.PublicationEvidence{}, fmt.Errorf("encode publication evidence coverage: %w", err)
	}
	costJSON, err := json.Marshal(evidence.Cost)
	if err != nil {
		return domain.PublicationEvidence{}, fmt.Errorf("encode publication evidence cost: %w", err)
	}
	evidence.CreatedAt = evidence.CreatedAt.UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PublicationEvidence{}, fmt.Errorf("begin publication evidence record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if sourceID := strings.TrimSpace(evidence.ReusedFromEvidenceID); sourceID != "" {
		source, sourceErr := scanPublicationEvidence(tx.QueryRowContext(ctx, publicationEvidenceSelect+` WHERE evidence_id=?`, sourceID))
		if sourceErr != nil {
			return domain.PublicationEvidence{}, fmt.Errorf("resolve reused publication evidence %s: %w", sourceID, sourceErr)
		}
		if !publicationEvidenceReusableAs(source, evidence) {
			return domain.PublicationEvidence{}, fmt.Errorf("reused publication evidence %s does not match immutable proof identity", sourceID)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO daemon_publication_evidence(
		evidence_id,project_id,issue_id,layer,patch_digest,source_revision,base_revision,result_revision,producer,policy_version,environment_fingerprint,reused_from_evidence_id,coverage_json,cost_json,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, evidence.EvidenceID, evidence.ProjectID, evidence.IssueID, evidence.Layer, evidence.PatchDigest, evidence.SourceRevision, evidence.BaseRevision, evidence.ResultRevision, evidence.Producer, evidence.PolicyVersion, evidence.EnvironmentFingerprint, nullablePublicationEvidenceID(evidence.ReusedFromEvidenceID), string(coverageJSON), string(costJSON), evidence.CreatedAt.Format(time.RFC3339Nano))
	if err == nil {
		if err = tx.Commit(); err != nil {
			return domain.PublicationEvidence{}, fmt.Errorf("commit publication evidence record: %w", err)
		}
		return evidence, nil
	}
	_ = tx.Rollback()
	existing, getErr := s.publicationEvidenceByID(ctx, evidence.EvidenceID)
	if getErr == nil && (publicationEvidenceSemanticallyEqual(existing, evidence) || domain.SamePatchReviewIdentity(existing, evidence)) {
		return existing, nil
	}
	if getErr == nil {
		return domain.PublicationEvidence{}, fmt.Errorf("publication evidence %s conflicts with immutable record", evidence.EvidenceID)
	}
	return domain.PublicationEvidence{}, fmt.Errorf("record publication evidence: %w", err)
}

func (s *SQLiteStore) RecordPublicationEvidenceInvalidation(ctx context.Context, invalidation domain.PublicationEvidenceInvalidation) (domain.PublicationEvidenceInvalidation, error) {
	if invalidation.CreatedAt.IsZero() {
		invalidation.CreatedAt = time.Now().UTC()
	}
	if err := invalidation.Validate(); err != nil {
		return domain.PublicationEvidenceInvalidation{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationEvidenceInvalidation{}, err
	}
	invalidation.CreatedAt = invalidation.CreatedAt.UTC()
	_, err = db.ExecContext(ctx, `INSERT INTO daemon_publication_evidence_invalidations(invalidation_id,evidence_id,reason,details,created_at) VALUES(?,?,?,?,?)`, invalidation.InvalidationID, invalidation.EvidenceID, invalidation.Reason, invalidation.Details, invalidation.CreatedAt.Format(time.RFC3339Nano))
	if err == nil {
		return invalidation, nil
	}
	existing, getErr := publicationInvalidationByID(ctx, db, invalidation.InvalidationID)
	if getErr == nil && publicationInvalidationSemanticallyEqual(existing, invalidation) {
		return existing, nil
	}
	if getErr == nil {
		return domain.PublicationEvidenceInvalidation{}, fmt.Errorf("publication invalidation %s conflicts with immutable record", invalidation.InvalidationID)
	}
	return domain.PublicationEvidenceInvalidation{}, fmt.Errorf("record publication evidence invalidation: %w", err)
}

// RecordPublicationEvidenceInvalidations appends one authoritative assessment
// atomically. Lost-response retries are idempotent by stable invalidation ID and
// semantic fields; the first server timestamp remains authoritative.
func (s *SQLiteStore) RecordPublicationEvidenceInvalidations(ctx context.Context, invalidations []domain.PublicationEvidenceInvalidation) error {
	if len(invalidations) == 0 {
		return nil
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publication evidence assessment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, invalidation := range invalidations {
		if invalidation.CreatedAt.IsZero() {
			invalidation.CreatedAt = time.Now().UTC()
		}
		if err := invalidation.Validate(); err != nil {
			return err
		}
		invalidation.CreatedAt = invalidation.CreatedAt.UTC()
		_, err = tx.ExecContext(ctx, `INSERT INTO daemon_publication_evidence_invalidations(invalidation_id,evidence_id,reason,details,created_at) VALUES(?,?,?,?,?)`, invalidation.InvalidationID, invalidation.EvidenceID, invalidation.Reason, invalidation.Details, invalidation.CreatedAt.Format(time.RFC3339Nano))
		if err == nil {
			continue
		}
		existing, getErr := scanPublicationInvalidation(tx.QueryRowContext(ctx, `SELECT invalidation_id,evidence_id,reason,details,created_at FROM daemon_publication_evidence_invalidations WHERE invalidation_id=?`, invalidation.InvalidationID))
		if getErr != nil || !publicationInvalidationSemanticallyEqual(existing, invalidation) {
			return fmt.Errorf("record publication evidence invalidation %s: %w", invalidation.InvalidationID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit publication evidence assessment: %w", err)
	}
	return nil
}

func (s *SQLiteStore) PublicationEvidenceSnapshot(ctx context.Context, projectID, issueID string) (domain.PublicationEvidenceSnapshot, error) {
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, err
	}
	projectID, issueID = strings.TrimSpace(projectID), strings.TrimSpace(issueID)
	if projectID == "" {
		return domain.PublicationEvidenceSnapshot{}, fmt.Errorf("publication evidence snapshot requires project identity")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, fmt.Errorf("begin publication evidence snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	query := publicationEvidenceSelect + ` WHERE project_id=?`
	args := []any{projectID}
	if issueID != "" {
		query += ` AND issue_id=?`
		args = append(args, issueID)
	}
	query += ` ORDER BY created_at,evidence_id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, fmt.Errorf("list publication evidence: %w", err)
	}
	snapshot := domain.PublicationEvidenceSnapshot{Schema: "azedarach.publication_evidence.v1", ProjectID: projectID, IssueID: issueID, Evidence: []domain.PublicationEvidence{}, Invalidations: []domain.PublicationEvidenceInvalidation{}}
	for rows.Next() {
		evidence, scanErr := scanPublicationEvidence(rows)
		if scanErr != nil {
			_ = rows.Close()
			return domain.PublicationEvidenceSnapshot{}, scanErr
		}
		snapshot.Evidence = append(snapshot.Evidence, evidence)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return domain.PublicationEvidenceSnapshot{}, err
	}
	if err = rows.Close(); err != nil {
		return domain.PublicationEvidenceSnapshot{}, err
	}
	invalidQuery := `SELECT i.invalidation_id,i.evidence_id,i.reason,i.details,i.created_at FROM daemon_publication_evidence_invalidations i JOIN daemon_publication_evidence e ON e.evidence_id=i.evidence_id WHERE e.project_id=?`
	invalidArgs := []any{projectID}
	if issueID != "" {
		invalidQuery += ` AND e.issue_id=?`
		invalidArgs = append(invalidArgs, issueID)
	}
	invalidQuery += ` ORDER BY i.created_at,i.invalidation_id`
	invalidRows, err := tx.QueryContext(ctx, invalidQuery, invalidArgs...)
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, fmt.Errorf("list publication evidence invalidations: %w", err)
	}
	for invalidRows.Next() {
		invalidation, scanErr := scanPublicationInvalidation(invalidRows)
		if scanErr != nil {
			_ = invalidRows.Close()
			return domain.PublicationEvidenceSnapshot{}, scanErr
		}
		snapshot.Invalidations = append(snapshot.Invalidations, invalidation)
	}
	if err = invalidRows.Err(); err != nil {
		_ = invalidRows.Close()
		return domain.PublicationEvidenceSnapshot{}, err
	}
	if err = invalidRows.Close(); err != nil {
		return domain.PublicationEvidenceSnapshot{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM daemon_publication_evidence_state WHERE project_id=?),0)`, projectID).Scan(&snapshot.Revision); err != nil {
		return domain.PublicationEvidenceSnapshot{}, fmt.Errorf("read publication evidence revision: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return domain.PublicationEvidenceSnapshot{}, fmt.Errorf("commit publication evidence snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *SQLiteStore) publicationEvidenceByID(ctx context.Context, evidenceID string) (domain.PublicationEvidence, error) {
	db, err := s.dbHandle()
	if err != nil {
		return domain.PublicationEvidence{}, err
	}
	return scanPublicationEvidence(db.QueryRowContext(ctx, publicationEvidenceSelect+` WHERE evidence_id=?`, evidenceID))
}

const publicationEvidenceSelect = `SELECT evidence_id,project_id,issue_id,layer,patch_digest,source_revision,base_revision,result_revision,producer,policy_version,environment_fingerprint,reused_from_evidence_id,coverage_json,cost_json,created_at FROM daemon_publication_evidence`

func scanPublicationEvidence(scanner validationScanner) (domain.PublicationEvidence, error) {
	var evidence domain.PublicationEvidence
	var coverageJSON, costJSON, createdAt string
	var reusedFrom sql.NullString
	if err := scanner.Scan(&evidence.EvidenceID, &evidence.ProjectID, &evidence.IssueID, &evidence.Layer, &evidence.PatchDigest, &evidence.SourceRevision, &evidence.BaseRevision, &evidence.ResultRevision, &evidence.Producer, &evidence.PolicyVersion, &evidence.EnvironmentFingerprint, &reusedFrom, &coverageJSON, &costJSON, &createdAt); err != nil {
		return evidence, err
	}
	evidence.ReusedFromEvidenceID = reusedFrom.String
	if err := json.Unmarshal([]byte(coverageJSON), &evidence.Coverage); err != nil {
		return evidence, fmt.Errorf("decode publication evidence coverage: %w", err)
	}
	if err := json.Unmarshal([]byte(costJSON), &evidence.Cost); err != nil {
		return evidence, fmt.Errorf("decode publication evidence cost: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return evidence, fmt.Errorf("decode publication evidence creation time: %w", err)
	}
	evidence.CreatedAt = parsed
	return evidence, nil
}

func publicationInvalidationByID(ctx context.Context, db *sql.DB, invalidationID string) (domain.PublicationEvidenceInvalidation, error) {
	return scanPublicationInvalidation(db.QueryRowContext(ctx, `SELECT invalidation_id,evidence_id,reason,details,created_at FROM daemon_publication_evidence_invalidations WHERE invalidation_id=?`, invalidationID))
}

func scanPublicationInvalidation(scanner validationScanner) (domain.PublicationEvidenceInvalidation, error) {
	var invalidation domain.PublicationEvidenceInvalidation
	var createdAt string
	if err := scanner.Scan(&invalidation.InvalidationID, &invalidation.EvidenceID, &invalidation.Reason, &invalidation.Details, &createdAt); err != nil {
		return invalidation, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return invalidation, err
	}
	invalidation.CreatedAt = parsed
	return invalidation, nil
}

func publicationEvidenceSemanticallyEqual(left, right domain.PublicationEvidence) bool {
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	left.Coverage, _ = domain.CanonicalizePublicationCoverage(left.Coverage)
	right.Coverage, _ = domain.CanonicalizePublicationCoverage(right.Coverage)
	return reflect.DeepEqual(left, right)
}

func publicationInvalidationSemanticallyEqual(left, right domain.PublicationEvidenceInvalidation) bool {
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(left, right)
}

func publicationEvidenceReusableAs(source, target domain.PublicationEvidence) bool {
	if source.ProjectID != target.ProjectID || source.IssueID != target.IssueID || source.Layer != target.Layer || source.PatchDigest != target.PatchDigest || source.SourceRevision != target.SourceRevision || source.PolicyVersion != target.PolicyVersion || source.EnvironmentFingerprint != target.EnvironmentFingerprint || !reflect.DeepEqual(source.Coverage, target.Coverage) {
		return false
	}
	if source.Layer == domain.PublicationEvidenceMergeResult {
		return source.BaseRevision == target.BaseRevision && source.ResultRevision == target.ResultRevision
	}
	return true
}

func nullablePublicationEvidenceID(value string) any {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return nil
}
