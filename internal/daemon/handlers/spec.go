package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

var ErrSpecUnavailable = errors.New("spec service unavailable")

var (
	errSpecConflict = errors.New("spec conflict")
	errSpecNotFound = errors.New("spec record not found")
)

type SpecService interface {
	ListRequirements(context.Context, protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error)
	GetRequirement(context.Context, protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error)
	CreateRequirement(context.Context, protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error)
	UpdateRequirement(context.Context, protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error)
	DeleteRequirement(context.Context, protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error)
	ListLinks(context.Context, protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error)
	AddLink(context.Context, protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error)
	RemoveLink(context.Context, protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error)
	Read(context.Context, protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error)
	Lint(context.Context, protocol.SpecLintRequestBody) (protocol.SpecLintResponseBody, error)
	Parity(context.Context, protocol.SpecParityRequestBody) (protocol.SpecParityResponseBody, error)
	SyncMD(context.Context, protocol.SpecSyncMDRequestBody) (protocol.SpecSyncMDResponseBody, error)
}

type SpecHandler struct {
	service SpecService
}

func NewSpecHandler(service SpecService) *SpecHandler {
	return &SpecHandler{service: service}
}

func (h *SpecHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
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
			Message:   ErrSpecUnavailable.Error(),
			Retryable: true,
		}
		return resp
	}

	switch req.Command {
	case protocol.CommandSpecRequirementList:
		var cmd protocol.SpecRequirementListRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.IssueID = strings.TrimSpace(cmd.IssueID)
		cmd.IDs = uniqueTrimmed(cmd.IDs)
		if cmd.Status != "" && !cmd.Status.Valid() {
			return specInvalidRequest(resp, "invalid status: expected open|accepted|superseded")
		}
		return specJSONResponse(ctx, resp, h.service.ListRequirements, cmd)

	case protocol.CommandSpecRequirementGet:
		var cmd protocol.SpecRequirementGetRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		if cmd.ID == "" {
			return specInvalidRequest(resp, "missing required field: id")
		}
		return specJSONResponse(ctx, resp, h.service.GetRequirement, cmd)

	case protocol.CommandSpecRequirementCreate:
		var cmd protocol.SpecRequirementCreateRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		cmd.Title = strings.TrimSpace(cmd.Title)
		cmd.Description = strings.TrimSpace(cmd.Description)
		cmd.IssueID = strings.TrimSpace(cmd.IssueID)
		if cmd.ID == "" || cmd.Title == "" {
			return specInvalidRequest(resp, "missing required fields: id/title")
		}
		return specJSONResponse(ctx, resp, h.service.CreateRequirement, cmd)

	case protocol.CommandSpecRequirementUpdate:
		var cmd protocol.SpecRequirementUpdateRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		cmd.Title = trimOptionalString(cmd.Title)
		cmd.Description = trimOptionalString(cmd.Description)
		if cmd.Status != nil {
			trimmed := protocol.SpecRequirementStatus(strings.TrimSpace(string(*cmd.Status)))
			cmd.Status = &trimmed
		}
		if cmd.ID == "" {
			return specInvalidRequest(resp, "missing required field: id")
		}
		if cmd.Title != nil && *cmd.Title == "" {
			return specInvalidRequest(resp, "title must be non-empty when provided")
		}
		if cmd.Status != nil && !cmd.Status.Valid() {
			return specInvalidRequest(resp, "invalid status: expected open|accepted|superseded")
		}
		if cmd.Title == nil && cmd.Description == nil && cmd.Status == nil {
			return specInvalidRequest(resp, "missing required fields: title/description/status")
		}
		return specJSONResponse(ctx, resp, h.service.UpdateRequirement, cmd)

	case protocol.CommandSpecRequirementDelete:
		var cmd protocol.SpecRequirementDeleteRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		if cmd.ID == "" {
			return specInvalidRequest(resp, "missing required field: id")
		}
		if !cmd.Confirm {
			return specInvalidRequest(resp, "spec requirement delete requires confirm=true")
		}
		return specJSONResponse(ctx, resp, h.service.DeleteRequirement, cmd)

	case protocol.CommandSpecLinkList:
		var cmd protocol.SpecLinkListRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.IssueID = strings.TrimSpace(cmd.IssueID)
		cmd.ReqID = strings.TrimSpace(cmd.ReqID)
		cmd.IDs = uniqueTrimmed(cmd.IDs)
		return specJSONResponse(ctx, resp, h.service.ListLinks, cmd)

	case protocol.CommandSpecLinkAdd:
		var cmd protocol.SpecLinkAddRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.IssueID = strings.TrimSpace(cmd.IssueID)
		cmd.ReqID = strings.TrimSpace(cmd.ReqID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.Role == "" {
			cmd.Role = protocol.SpecLinkRoleImplements
		} else {
			cmd.Role = protocol.SpecLinkRole(strings.TrimSpace(string(cmd.Role)))
		}
		if cmd.IssueID == "" || cmd.ReqID == "" {
			return specInvalidRequest(resp, "missing required fields: issue_id/req_id")
		}
		if !cmd.Role.Valid() {
			return specInvalidRequest(resp, "invalid role: expected implements|verifies|relates")
		}
		return specJSONResponse(ctx, resp, h.service.AddLink, cmd)

	case protocol.CommandSpecLinkRemove:
		var cmd protocol.SpecLinkRemoveRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.IssueID = strings.TrimSpace(cmd.IssueID)
		cmd.ReqID = strings.TrimSpace(cmd.ReqID)
		if cmd.IssueID == "" || cmd.ReqID == "" {
			return specInvalidRequest(resp, "missing required fields: issue_id/req_id")
		}
		return specJSONResponse(ctx, resp, h.service.RemoveLink, cmd)

	case protocol.CommandSpecRead:
		var cmd protocol.SpecReadRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.IssueID = strings.TrimSpace(cmd.IssueID)
		cmd.ReqID = strings.TrimSpace(cmd.ReqID)
		return specJSONResponse(ctx, resp, h.service.Read, cmd)

	case protocol.CommandSpecLint:
		var cmd protocol.SpecLintRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		return specJSONResponse(ctx, resp, h.service.Lint, cmd)

	case protocol.CommandSpecParity:
		var cmd protocol.SpecParityRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		return specJSONResponse(ctx, resp, h.service.Parity, cmd)

	case protocol.CommandSpecSync, protocol.CommandSpecSyncMD:
		var cmd protocol.SpecSyncMDRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.Target = strings.TrimSpace(cmd.Target)
		if cmd.Target == "" {
			cmd.Target = "md"
		}
		if cmd.Target != "md" {
			return specInvalidRequest(resp, "invalid target: expected md")
		}
		return specJSONResponse(ctx, resp, h.service.SyncMD, cmd)

	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported spec command",
			Retryable: false,
		}
		return resp
	}
}

func decodeSpecRequest(body []byte, dest any, resp *protocol.ResponseEnvelope) bool {
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

func specInvalidRequest(resp protocol.ResponseEnvelope, message string) protocol.ResponseEnvelope {
	resp.Error = &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInvalidRequest,
		Message:   message,
		Retryable: false,
	}
	return resp
}

func specJSONResponse[Req any, Resp any](
	ctx context.Context,
	resp protocol.ResponseEnvelope,
	fn func(context.Context, Req) (Resp, error),
	cmd Req,
) protocol.ResponseEnvelope {
	body, err := fn(ctx, cmd)
	if err != nil {
		resp.Error = mapSpecError(err)
		return resp
	}
	payload, err := json.Marshal(body)
	if err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   fmt.Sprintf("marshal response body: %v", err),
			Retryable: false,
		}
		return resp
	}
	resp.OK = true
	resp.Body = payload
	return resp
}

func trimOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func uniqueTrimmed(values []string) []string {
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

func mapSpecError(err error) *protocol.ErrorEnvelope {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeTimeout,
			Message:   err.Error(),
			Retryable: true,
		}
	case errors.Is(err, ErrSpecUnavailable):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnavailable,
			Message:   err.Error(),
			Retryable: true,
		}
	case errors.Is(err, errSpecConflict):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeConflict,
			Message:   err.Error(),
			Retryable: false,
		}
	case errors.Is(err, errSpecNotFound):
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
