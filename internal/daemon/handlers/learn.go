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
	Promote(context.Context, protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error)
	Relate(context.Context, protocol.LearnRelateRequestBody) (protocol.LearnRelateResponseBody, error)
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
		cmd.Status = protocol.LearningStatus(strings.TrimSpace(string(cmd.Status)))
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.Limit < 0 {
			return learnInvalidRequest(resp, "limit must be non-negative")
		}
		if cmd.ID == "" && (cmd.Status != "" || cmd.Note != "") {
			return learnInvalidRequest(resp, "id is required when status or note is provided")
		}
		if cmd.ID != "" && cmd.Status == "" {
			return learnInvalidRequest(resp, "review update requires status when id is provided")
		}
		if cmd.ID != "" && !learningReviewStatusValid(cmd.Status) {
			return learnInvalidRequest(resp, "invalid review status: expected accepted|rejected|stale")
		}
		if cmd.ID != "" && cmd.Note == "" {
			return learnInvalidRequest(resp, "review update requires note")
		}
		return learnJSONResponse(ctx, resp, h.service.Review, cmd)

	case protocol.CommandLearnPromote:
		var cmd protocol.LearnPromoteRequestBody
		if !decodeLearnRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		cmd.Target = protocol.LearningPromotionTarget(strings.TrimSpace(string(cmd.Target)))
		cmd.TargetID = strings.TrimSpace(cmd.TargetID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.ID == "" || cmd.Target == "" || cmd.TargetID == "" {
			return learnInvalidRequest(resp, "missing required fields: id/target/target_id")
		}
		if !cmd.Target.Valid() {
			return learnInvalidRequest(resp, "invalid target: expected rulesync|agents|skill|spec|decision")
		}
		return learnJSONResponse(ctx, resp, h.service.Promote, cmd)
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
	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported learn command",
			Retryable: false,
		}
		return resp
	}
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
