package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

type LearnService interface {
	Add(context.Context, protocol.LearnAddRequestBody) (protocol.LearnAddResponseBody, error)
	Recall(context.Context, protocol.LearnRecallRequestBody) (protocol.LearnRecallResponseBody, error)
	Show(context.Context, protocol.LearnShowRequestBody) (protocol.LearnShowResponseBody, error)
	Review(context.Context, protocol.LearnReviewRequestBody) (protocol.LearnReviewResponseBody, error)
	Stale(context.Context, protocol.LearnStaleRequestBody) (protocol.LearnStaleResponseBody, error)
	Demote(context.Context, protocol.LearnDemoteRequestBody) (protocol.LearnDemoteResponseBody, error)
	Promote(context.Context, protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error)
	Retire(context.Context, protocol.LearnRetireRequestBody) (protocol.LearnRetireResponseBody, error)
	Relate(context.Context, protocol.LearnRelateRequestBody) (protocol.LearnRelateResponseBody, error)
	Supersede(context.Context, protocol.LearnSupersedeRequestBody) (protocol.LearnSupersedeResponseBody, error)
	Doctor(context.Context, protocol.LearnDoctorRequestBody) (protocol.LearnDoctorResponseBody, error)
	GC(context.Context, protocol.LearnGCRequestBody) (protocol.LearnGCResponseBody, error)
}

type LearnHandler struct {
	service LearnService
}

func NewLearnHandler(service LearnService) *LearnHandler {
	return &LearnHandler{service: service}
}

func (h *LearnHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}
	if h.service == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnavailable,
			Message:   "learn service unavailable",
			Retryable: true,
		}
		return resp
	}
	switch req.Command {
	case protocol.CommandLearnAdd:
		var cmd protocol.LearnAddRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ProjectID = strings.TrimSpace(cmd.ProjectID)
		cmd.Summary = strings.TrimSpace(cmd.Summary)
		cmd.Evidence = strings.TrimSpace(cmd.Evidence)
		if cmd.Evidence == "" {
			return learnInvalidRequest(resp, "missing required field: evidence")
		}
		return learnJSONResponse(ctx, resp, h.service.Add, cmd)

	case protocol.CommandLearnRecall:
		var cmd protocol.LearnRecallRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.Query = strings.TrimSpace(cmd.Query)
		if cmd.Limit < 0 {
			return learnInvalidRequest(resp, "limit must be non-negative")
		}
		for _, status := range cmd.Statuses {
			if !status.Valid() {
				return learnInvalidRequest(resp, "invalid status: expected candidate|accepted|rejected|promoted|stale")
			}
		}
		return learnJSONResponse(ctx, resp, h.service.Recall, cmd)

	case protocol.CommandLearnShow:
		var cmd protocol.LearnShowRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		if cmd.ID == "" {
			return learnInvalidRequest(resp, "missing required field: id")
		}
		return learnJSONResponse(ctx, resp, h.service.Show, cmd)

	case protocol.CommandLearnReview:
		var cmd protocol.LearnReviewRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		cmd.IDs = compactStrings(cmd.IDs)
		cmd.Status = protocol.LearningStatus(strings.TrimSpace(string(cmd.Status)))
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.Limit < 0 {
			return learnInvalidRequest(resp, "limit must be non-negative")
		}
		if cmd.OlderThanSeconds < 0 {
			return learnInvalidRequest(resp, "older_than_seconds must be non-negative")
		}
		for _, status := range cmd.QueueStatuses {
			if !status.Valid() {
				return learnInvalidRequest(resp, "invalid queue status: expected candidate|accepted|rejected|promoted|stale")
			}
		}
		for _, state := range cmd.TargetStates {
			if !state.Valid() {
				return learnInvalidRequest(resp, "invalid target state: expected active|retired|drifted|missing")
			}
		}
		if cmd.ID != "" {
			cmd.IDs = append([]string{cmd.ID}, cmd.IDs...)
		}
		cmd.IDs = compactStrings(cmd.IDs)
		if len(cmd.IDs) == 0 && (cmd.Status != "" || cmd.Note != "") && !cmd.BulkStale {
			return learnInvalidRequest(resp, "id is required when status or note is provided")
		}
		if len(cmd.IDs) > 0 && cmd.Status == "" {
			return learnInvalidRequest(resp, "review update requires status when id is provided")
		}
		if len(cmd.IDs) > 0 && !learningReviewStatusValid(cmd.Status) {
			return learnInvalidRequest(resp, "invalid review status: expected accepted|rejected|stale")
		}
		if len(cmd.IDs) > 0 && cmd.Note == "" {
			return learnInvalidRequest(resp, "review update requires note")
		}
		if cmd.BulkStale {
			if cmd.Status != "" {
				return learnInvalidRequest(resp, "bulk stale does not accept status")
			}
			if cmd.Note == "" {
				return learnInvalidRequest(resp, "bulk stale requires note")
			}
			if cmd.OlderThanSeconds <= 0 {
				return learnInvalidRequest(resp, "bulk stale requires older_than_seconds")
			}
		}
		return learnJSONResponse(ctx, resp, h.service.Review, cmd)

	case protocol.CommandLearnStale:
		var cmd protocol.LearnStaleRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.ID == "" {
			return learnInvalidRequest(resp, "missing required field: id")
		}
		if cmd.Note == "" {
			return learnInvalidRequest(resp, "stale note is required")
		}
		return learnJSONResponse(ctx, resp, h.service.Stale, cmd)

	case protocol.CommandLearnDemote:
		var cmd protocol.LearnDemoteRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.ID == "" {
			return learnInvalidRequest(resp, "missing required field: id")
		}
		if cmd.Note == "" {
			return learnInvalidRequest(resp, "demotion note is required")
		}
		return learnJSONResponse(ctx, resp, h.service.Demote, cmd)

	case protocol.CommandLearnPromote:
		var cmd protocol.LearnPromoteRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		cmd.Target = protocol.LearningPromotionTarget(strings.TrimSpace(string(cmd.Target)))
		cmd.TargetID = strings.TrimSpace(cmd.TargetID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		cmd.TargetTitle = strings.TrimSpace(cmd.TargetTitle)
		cmd.TargetDescription = strings.TrimSpace(cmd.TargetDescription)
		cmd.DecisionRationale = strings.TrimSpace(cmd.DecisionRationale)
		cmd.DecisionContext = strings.TrimSpace(cmd.DecisionContext)
		cmd.DecisionConsequences = strings.TrimSpace(cmd.DecisionConsequences)
		if cmd.ID == "" || cmd.Target == "" {
			return learnInvalidRequest(resp, "missing required fields: id/target")
		}
		if !cmd.Target.Valid() {
			return learnInvalidRequest(resp, "invalid target: expected rulesync|agents|skill|spec|decision")
		}
		if cmd.TargetID == "" && (!cmd.CreateTarget || cmd.Target != protocol.LearningPromotionTargetDecision) {
			return learnInvalidRequest(resp, "missing required field: target_id")
		}
		return learnJSONResponse(ctx, resp, h.service.Promote, cmd)
	case protocol.CommandLearnRetire:
		var cmd protocol.LearnRetireRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.ID == "" {
			return learnInvalidRequest(resp, "missing required field: id")
		}
		if cmd.Note == "" {
			return learnInvalidRequest(resp, "retirement note is required")
		}
		return learnJSONResponse(ctx, resp, h.service.Retire, cmd)
	case protocol.CommandLearnRelate:
		var cmd protocol.LearnRelateRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.Type = protocol.LearningRelationType(strings.TrimSpace(string(cmd.Type)))
		cmd.SourceLearningID = strings.TrimSpace(cmd.SourceLearningID)
		cmd.TargetLearningID = strings.TrimSpace(cmd.TargetLearningID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.Type == "" || cmd.SourceLearningID == "" || cmd.TargetLearningID == "" {
			return learnInvalidRequest(resp, "missing required fields: type/source_learning_id/target_learning_id")
		}
		if !cmd.Type.Valid() {
			return learnInvalidRequest(resp, "invalid relation type: expected supersedes|conflicts")
		}
		if cmd.Note == "" {
			return learnInvalidRequest(resp, "relation note is required")
		}
		return learnJSONResponse(ctx, resp, h.service.Relate, cmd)
	case protocol.CommandLearnSupersede:
		var cmd protocol.LearnSupersedeRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.NewLearningID = strings.TrimSpace(cmd.NewLearningID)
		cmd.OldLearningID = strings.TrimSpace(cmd.OldLearningID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.NewLearningID == "" || cmd.OldLearningID == "" {
			return learnInvalidRequest(resp, "missing required fields: new_learning_id/old_learning_id")
		}
		if cmd.Note == "" {
			return learnInvalidRequest(resp, "supersede note is required")
		}
		return learnJSONResponse(ctx, resp, h.service.Supersede, cmd)
	case protocol.CommandLearnDoctor:
		var cmd protocol.LearnDoctorRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ProjectID = strings.TrimSpace(cmd.ProjectID)
		if cmd.CandidateOlderThanDays < 0 || cmd.InactiveOlderThanDays < 0 {
			return learnInvalidRequest(resp, "age thresholds must be non-negative")
		}
		if cmd.Limit < 0 {
			return learnInvalidRequest(resp, "limit must be non-negative")
		}
		return learnJSONResponse(ctx, resp, h.service.Doctor, cmd)
	case protocol.CommandLearnGC:
		var cmd protocol.LearnGCRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ProjectID = strings.TrimSpace(cmd.ProjectID)
		if cmd.CandidateOlderThanDays < 0 || cmd.InactiveOlderThanDays < 0 {
			return learnInvalidRequest(resp, "age thresholds must be non-negative")
		}
		if cmd.Limit < 0 {
			return learnInvalidRequest(resp, "limit must be non-negative")
		}
		return learnJSONResponse(ctx, resp, h.service.GC, cmd)
	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported learn command",
			Retryable: false,
		}
		return resp
	}
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func learningReviewStatusValid(status protocol.LearningStatus) bool {
	switch status {
	case protocol.LearningStatusAccepted, protocol.LearningStatusRejected, protocol.LearningStatusStale:
		return true
	default:
		return false
	}
}

func decodeLearnRequest(body []byte, dest any, resp *protocol.ResponseEnvelope) bool {
	if err := json.Unmarshal(body, dest); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return false
	}
	return true
}

func learnInvalidRequest(resp protocol.ResponseEnvelope, message string) protocol.ResponseEnvelope {
	resp.Error = &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInvalidRequest,
		Message:   message,
		Retryable: false,
	}
	return resp
}

func learnJSONResponse[Req any, Resp any](ctx context.Context, resp protocol.ResponseEnvelope, fn func(context.Context, Req) (Resp, error), req Req) protocol.ResponseEnvelope {
	out, err := fn(ctx, req)
	if err != nil {
		resp.Error = learnErrorEnvelope(err)
		return resp
	}
	payload, err := json.Marshal(out)
	if err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   fmt.Sprintf("encode response: %v", err),
			Retryable: false,
		}
		return resp
	}
	resp.OK = true
	resp.Body = payload
	return resp
}

func learnErrorEnvelope(err error) *protocol.ErrorEnvelope {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeTimeout,
			Message:   err.Error(),
			Retryable: true,
		}
	case errors.Is(err, domain.ErrConflict):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeConflict,
			Message:   err.Error(),
			Retryable: false,
		}
	case errors.Is(err, domain.ErrNotFound):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   err.Error(),
			Retryable: false,
		}
	default:
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   err.Error(),
			Retryable: false,
		}
	}
}
