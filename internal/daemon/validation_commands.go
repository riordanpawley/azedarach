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
	store, storeErr := d.validationProjectionStore()
	if storeErr != nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "validation lease store unavailable"), nil
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
		result, err := store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: strings.TrimSpace(body.RequestID), LeaseToken: strings.TrimSpace(body.LeaseToken), ProjectID: projectID, IssueID: strings.TrimSpace(body.IssueID), Class: body.Class, Profile: strings.TrimSpace(body.Profile), Command: strings.TrimSpace(body.Command), SourceRevision: strings.TrimSpace(body.SourceRevision), ReviewerID: reviewerID, ReviewEpochEventID: reviewEpochEventID, TTL: ttl}, now)
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
		result, err := store.AuthorizeNestedValidation(ctx, domain.ValidationNestedAuthorization{RequestID: strings.TrimSpace(body.RequestID), LeaseToken: strings.TrimSpace(body.LeaseToken), Class: body.Class}, now, defaultValidationLeaseTTL)
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
	default:
		return d.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported validation command"), nil
	}
}

func (d *Daemon) validationReviewAssignment(ctx context.Context, projectID string, body protocol.ValidationAcquireRequest) (string, int64, error) {
	if body.Class != domain.ValidationClassAggregate {
		return "", 0, nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return "", 0, nil
	}
	issueID := strings.TrimSpace(body.IssueID)
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return "", 0, err
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
