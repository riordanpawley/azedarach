package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const defaultValidationLeaseTTL = 30 * time.Second

func (d *Daemon) handleValidationCommand(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var store *operationstore.SQLiteStore
	var storeErr error
	switch req.Command {
	case protocol.CommandPublicationEvidenceRecord, protocol.CommandPublicationEvidenceInvalidate,
		protocol.CommandPublicationEvidenceStatus, protocol.CommandPublicationEvidenceEvaluate:
		store, storeErr = d.publicationEvidenceProjectionStore()
	default:
		store, storeErr = d.validationProjectionStore()
	}
	if storeErr != nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, storeErr.Error()), nil
	}
	projectID := d.projectID(req.Meta)
	now := time.Now().UTC()
	switch req.Command {
	case protocol.CommandValidationAcquire:
		var body protocol.ValidationAcquireRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode validation acquire: %v", err)), nil
		}
		ttl := validationTTL(body.TTLSeconds)
		reviewerID, reviewEpochEventID, err := d.validationReviewAssignment(ctx, projectID, body)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		acquire := domain.ValidationAcquire{RequestID: strings.TrimSpace(body.RequestID), LeaseToken: strings.TrimSpace(body.LeaseToken), ProjectID: projectID, IssueID: strings.TrimSpace(body.IssueID), Class: body.Class, Scope: body.Scope, Purpose: body.Purpose, IsolationMode: strings.TrimSpace(body.IsolationMode), EnvironmentFingerprint: strings.TrimSpace(body.EnvironmentFingerprint), Override: body.Override, OverrideActor: strings.TrimSpace(body.OverrideActor), OverrideReason: strings.TrimSpace(body.OverrideReason), Profile: strings.TrimSpace(body.Profile), Command: strings.TrimSpace(body.Command), SourceRevision: strings.TrimSpace(body.SourceRevision), ReviewerID: reviewerID, ReviewEpochEventID: reviewEpochEventID, TTL: ttl}
		if err := acquire.Validate(); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		result, err := store.AcquireValidation(ctx, acquire, now)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.ValidationRequestResponse{Request: result})
	case protocol.CommandValidationHeartbeat:
		var body protocol.ValidationHeartbeatRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode validation heartbeat: %v", err)), nil
		}
		result, err := store.HeartbeatValidation(ctx, strings.TrimSpace(body.RequestID), strings.TrimSpace(body.LeaseToken), now, validationTTL(body.TTLSeconds))
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.ValidationRequestResponse{Request: result})
	case protocol.CommandValidationNested:
		var body protocol.ValidationAuthorizeNestedRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode nested validation authorization: %v", err)), nil
		}
		result, err := store.AuthorizeNestedValidation(ctx, domain.ValidationNestedAuthorization{RequestID: strings.TrimSpace(body.RequestID), LeaseToken: strings.TrimSpace(body.LeaseToken), Class: body.Class, Scope: body.Scope, Purpose: body.Purpose}, now, defaultValidationLeaseTTL)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.ValidationRequestResponse{Request: result})
	case protocol.CommandValidationFinish:
		var body protocol.ValidationFinishRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode validation finish: %v", err)), nil
		}
		result, err := store.FinishValidation(ctx, strings.TrimSpace(body.RequestID), strings.TrimSpace(body.LeaseToken), body.State, body.Outcome, body.Evidence, now, defaultValidationLeaseTTL)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.ValidationRequestResponse{Request: result})
	case protocol.CommandValidationStatus:
		snapshot, err := store.ValidationSnapshot(ctx, projectID, now, defaultValidationLeaseTTL)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.ValidationStatusResponse{Snapshot: snapshot})
	case protocol.CommandPublicationEvidenceRecord:
		var body protocol.PublicationEvidenceRecordRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode publication evidence: %v", err)), nil
		}
		body.Evidence.ProjectID = projectID
		if body.Evidence.CreatedAt.IsZero() {
			body.Evidence.CreatedAt = now
		}
		if err := d.validatePublicationEvidenceIssue(ctx, projectID, body.Evidence.IssueID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		evidence, err := store.RecordPublicationEvidence(ctx, body.Evidence)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		if _, err = d.publicationEvidenceSnapshot(ctx, projectID, body.Evidence.IssueID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.PublicationEvidenceRecordResponse{Evidence: evidence})
	case protocol.CommandPublicationEvidenceInvalidate:
		var body protocol.PublicationEvidenceInvalidateRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode publication evidence invalidation: %v", err)), nil
		}
		if body.Invalidation.CreatedAt.IsZero() {
			body.Invalidation.CreatedAt = now
		}
		snapshot, err := d.publicationEvidenceSnapshot(ctx, projectID, "")
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		found := false
		for _, evidence := range snapshot.Evidence {
			if evidence.EvidenceID == body.Invalidation.EvidenceID {
				found = true
				break
			}
		}
		if !found {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "publication evidence does not belong to this project"), nil
		}
		invalidation, err := store.RecordPublicationEvidenceInvalidation(ctx, body.Invalidation)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeConflict, err.Error()), nil
		}
		if _, err = d.publicationEvidenceSnapshot(ctx, projectID, ""); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.PublicationEvidenceInvalidateResponse{Invalidation: invalidation})
	case protocol.CommandPublicationEvidenceStatus:
		var body protocol.PublicationEvidenceStatusRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode publication evidence status: %v", err)), nil
		}
		snapshot, err := d.publicationEvidenceSnapshot(ctx, projectID, strings.TrimSpace(body.IssueID))
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		return d.validationSuccessResponse(req, protocol.PublicationEvidenceStatusResponse{Snapshot: snapshot})
	case protocol.CommandPublicationEvidenceEvaluate:
		var body protocol.PublicationEvidenceEvaluateRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode publication evidence evaluation: %v", err)), nil
		}
		if err := body.Candidate.Validate(); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		if err := body.Policy.Validate(); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		snapshot, err := d.publicationEvidenceSnapshot(ctx, projectID, strings.TrimSpace(body.IssueID))
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		assessments := make([]domain.PublicationEvidenceAssessment, 0, len(snapshot.Evidence))
		effectiveInvalidations := domain.EffectivePublicationEvidenceInvalidations(snapshot)
		for _, evidence := range snapshot.Evidence {
			assessment := domain.EvaluatePublicationEvidence(evidence, body.Candidate, body.Policy)
			assessments = append(assessments, domain.ApplyPublicationEvidenceInvalidations(assessment, effectiveInvalidations))
		}
		return d.validationSuccessResponse(req, protocol.PublicationEvidenceEvaluateResponse{Snapshot: snapshot, Assessments: assessments})
	default:
		return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported validation command"), nil
	}
}

func (d *Daemon) validatePublicationEvidenceIssue(ctx context.Context, projectID, issueID string) error {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return fmt.Errorf("publication evidence requires issue identity")
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		return fmt.Errorf("publication evidence requires issue store")
	}
	if _, err := client.GetWithRuntime(ctx, projectID, issueID); err != nil {
		return fmt.Errorf("resolve publication evidence issue %s: %w", issueID, err)
	}
	return nil
}

func (d *Daemon) validationReviewAssignment(ctx context.Context, projectID string, body protocol.ValidationAcquireRequest) (string, int64, error) {
	if body.Scope == domain.ValidationScopeRepository {
		if strings.TrimSpace(body.IssueID) != "" {
			return "", 0, fmt.Errorf("repository-scoped validation must not identify a ticket")
		}
		return "", 0, nil
	}
	if body.Scope != domain.ValidationScopeTicket {
		return "", 0, fmt.Errorf("validation requires explicit repository or ticket scope")
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return "", 0, fmt.Errorf("ticket-scoped validation requires issue store")
	}
	issueID := strings.TrimSpace(body.IssueID)
	if issueID == "" {
		return "", 0, fmt.Errorf("ticket-scoped validation requires ticket identity")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return "", 0, fmt.Errorf("resolve ticket-scoped validation %s: %w", issueID, err)
	}
	if body.Purpose != domain.ValidationPurposeReviewEvidence {
		return "", 0, nil
	}
	if body.Class != domain.ValidationClassAggregate {
		return "", 0, fmt.Errorf("review evidence requires aggregate class and ticket scope")
	}
	if task.Status != domain.StatusInReview {
		return "", 0, nil
	}
	reviewerID := strings.TrimSpace(body.ReviewerID)
	if reviewerID == "" {
		return "", 0, fmt.Errorf("review-assigned aggregate validation requires reviewer identity")
	}
	reviewLease := coordinationLease(task, domain.CoordinationLeaseReview)
	if reviewLease == nil || reviewLease.IsExpired(time.Now().UTC()) {
		return "", 0, fmt.Errorf("review-assigned aggregate validation requires an active durable review lease")
	}
	if !strings.EqualFold(strings.TrimSpace(reviewLease.OwnerID), reviewerID) {
		return "", 0, fmt.Errorf("review-assigned aggregate validation reviewer %s does not own review lease held by %s", reviewerID, reviewLease.OwnerID)
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventIssueStatusChanged}, NewestIDFirst: true})
	if err != nil {
		return "", 0, err
	}
	for _, event := range events {
		if domain.IsReviewRequestTransition(event) {
			return reviewerID, event.ID, nil
		}
	}
	return "", 0, fmt.Errorf("review-assigned aggregate validation has no current review epoch")
}

func (d *Daemon) validationProjectionStore() (*operationstore.SQLiteStore, error) {
	if sourceForInvariant(daemonInvariantValidationCapacity) != daemonInvariantSourceProjection {
		return nil, fmt.Errorf("invariant %s requires projection source", daemonInvariantValidationCapacity)
	}
	if d.operationRuntime == nil || d.operationRuntime.store == nil {
		return nil, fmt.Errorf("validation projection store unavailable")
	}
	return d.operationRuntime.store, nil
}

func (d *Daemon) publicationEvidenceProjectionStore() (*operationstore.SQLiteStore, error) {
	if sourceForInvariant(daemonInvariantPublicationEvidence) != daemonInvariantSourceProjection {
		return nil, fmt.Errorf("invariant %s requires projection source", daemonInvariantPublicationEvidence)
	}
	if d.operationRuntime == nil || d.operationRuntime.store == nil {
		return nil, fmt.Errorf("publication evidence projection store unavailable")
	}
	return d.operationRuntime.store, nil
}

// publicationEvidenceSnapshot enforces the projection invariant's
// refresh-then-cache read contract. Every read replaces the complete project
// cache from durable SQLite before selecting an issue, so another daemon's
// write cannot leave this process evaluating stale evidence.
func (d *Daemon) publicationEvidenceSnapshot(ctx context.Context, projectID, issueID string) (domain.PublicationEvidenceSnapshot, error) {
	store, err := d.publicationEvidenceProjectionStore()
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, err
	}
	refreshed, err := store.PublicationEvidenceSnapshot(ctx, projectID, "")
	if err != nil {
		return domain.PublicationEvidenceSnapshot{}, fmt.Errorf("refresh publication evidence projection: %w", err)
	}
	d.publicationEvidenceMu.Lock()
	if d.publicationEvidenceCache == nil {
		d.publicationEvidenceCache = make(map[string]domain.PublicationEvidenceSnapshot)
	}
	d.publicationEvidenceCache[projectID] = refreshed
	d.publicationEvidenceMu.Unlock()

	d.publicationEvidenceMu.RLock()
	cached := d.publicationEvidenceCache[projectID]
	d.publicationEvidenceMu.RUnlock()
	return publicationEvidenceSnapshotForIssue(cached, strings.TrimSpace(issueID)), nil
}

func publicationEvidenceSnapshotForIssue(snapshot domain.PublicationEvidenceSnapshot, issueID string) domain.PublicationEvidenceSnapshot {
	if issueID == "" {
		return snapshot
	}
	filtered := domain.PublicationEvidenceSnapshot{
		Schema: snapshot.Schema, ProjectID: snapshot.ProjectID, IssueID: issueID, Revision: snapshot.Revision,
	}
	evidenceIDs := make(map[string]struct{})
	for _, evidence := range snapshot.Evidence {
		if evidence.IssueID == issueID {
			filtered.Evidence = append(filtered.Evidence, evidence)
			evidenceIDs[evidence.EvidenceID] = struct{}{}
		}
	}
	for _, invalidation := range snapshot.Invalidations {
		if _, ok := evidenceIDs[invalidation.EvidenceID]; ok {
			filtered.Invalidations = append(filtered.Invalidations, invalidation)
		}
	}
	return filtered
}

func (d *Daemon) validationSuccessResponse(req protocol.RequestEnvelope, value any) (protocol.ResponseEnvelope, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(d.projectID(req.Meta))
	return resp, nil
}

func validationTTL(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultValidationLeaseTTL
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}
