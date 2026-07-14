package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

var ErrDecisionUnavailable = errors.New("decision service unavailable")

type DecisionService interface {
	ListDecisions(context.Context, protocol.DecisionListRequestBody) (protocol.DecisionListResponseBody, error)
	GetDecision(context.Context, protocol.DecisionGetRequestBody) (protocol.DecisionGetResponseBody, error)
	RecordDecision(context.Context, protocol.DecisionRecordRequestBody) (protocol.DecisionRecordResponseBody, error)
	UpdateDecision(context.Context, protocol.DecisionUpdateRequestBody) (protocol.DecisionUpdateResponseBody, error)
	DeleteDecision(context.Context, protocol.DecisionDeleteRequestBody) (protocol.DecisionDeleteResponseBody, error)
	ListDecisionLinks(context.Context, protocol.DecisionLinkListRequestBody) (protocol.DecisionLinkListResponseBody, error)
	AddDecisionLink(context.Context, protocol.DecisionLinkAddRequestBody) (protocol.DecisionLinkAddResponseBody, error)
	RemoveDecisionLink(context.Context, protocol.DecisionLinkRemoveRequestBody) (protocol.DecisionLinkRemoveResponseBody, error)
	AcknowledgeDecision(context.Context, protocol.DecisionAcknowledgeRequestBody) (protocol.DecisionAcknowledgeResponseBody, error)
	SyncMD(context.Context, protocol.DecisionSyncMDRequestBody) (protocol.DecisionSyncMDResponseBody, error)
	ImportMD(context.Context, protocol.DecisionImportMDRequestBody) (protocol.DecisionImportMDResponseBody, error)
}

type DecisionHandler struct {
	service DecisionService
}

func NewDecisionHandler(service DecisionService) *DecisionHandler {
	return &DecisionHandler{service: service}
}

func (h *DecisionHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
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
			Message:   ErrDecisionUnavailable.Error(),
			Retryable: true,
		}
		return resp
	}

	switch req.Command {
	case protocol.CommandDecisionList:
		var cmd protocol.DecisionListRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.IDs = uniqueTrimmedStrings(cmd.IDs)
		return specJSONResponse(ctx, resp, h.service.ListDecisions, cmd)

	case protocol.CommandDecisionGet:
		var cmd protocol.DecisionGetRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		if cmd.ID == "" {
			return specInvalidRequest(resp, "missing required field: id")
		}
		return specJSONResponse(ctx, resp, h.service.GetDecision, cmd)

	case protocol.CommandDecisionRecord:
		var cmd protocol.DecisionRecordRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.Title = strings.TrimSpace(cmd.Title)
		cmd.Rationale = strings.TrimSpace(cmd.Rationale)
		cmd.Context = strings.TrimSpace(cmd.Context)
		cmd.Consequences = strings.TrimSpace(cmd.Consequences)
		if cmd.Title == "" || cmd.Rationale == "" {
			return specInvalidRequest(resp, "missing required fields: title/rationale")
		}
		return specJSONResponse(ctx, resp, h.service.RecordDecision, cmd)

	case protocol.CommandDecisionUpdate:
		var cmd protocol.DecisionUpdateRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		if cmd.ID == "" {
			return specInvalidRequest(resp, "missing required field: id")
		}
		cmd.Title = trimOptionalString(cmd.Title)
		cmd.Rationale = trimOptionalString(cmd.Rationale)
		cmd.Context = trimOptionalString(cmd.Context)
		cmd.Consequences = trimOptionalString(cmd.Consequences)
		if cmd.Title == nil && cmd.Rationale == nil && cmd.Context == nil && cmd.Consequences == nil {
			return specInvalidRequest(resp, "no update fields provided")
		}
		if cmd.Title != nil && *cmd.Title == "" {
			return specInvalidRequest(resp, "title must be non-empty when provided")
		}
		if cmd.Rationale != nil && *cmd.Rationale == "" {
			return specInvalidRequest(resp, "rationale must be non-empty when provided")
		}
		return specJSONResponse(ctx, resp, h.service.UpdateDecision, cmd)

	case protocol.CommandDecisionDelete:
		var cmd protocol.DecisionDeleteRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		if cmd.ID == "" {
			return specInvalidRequest(resp, "missing required field: id")
		}
		if !cmd.Confirm {
			return specInvalidRequest(resp, "decision delete requires confirm=true")
		}
		return specJSONResponse(ctx, resp, h.service.DeleteDecision, cmd)

	case protocol.CommandDecisionLinkList:
		var cmd protocol.DecisionLinkListRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.DecisionID = strings.TrimSpace(cmd.DecisionID)
		cmd.TargetID = strings.TrimSpace(cmd.TargetID)
		if cmd.TargetKind != "" && !cmd.TargetKind.Valid() {
			return specInvalidRequest(resp, "invalid target kind: expected issue|requirement|decision")
		}
		return specJSONResponse(ctx, resp, h.service.ListDecisionLinks, cmd)

	case protocol.CommandDecisionLinkAdd:
		var cmd protocol.DecisionLinkAddRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.DecisionID = strings.TrimSpace(cmd.DecisionID)
		cmd.TargetID = strings.TrimSpace(cmd.TargetID)
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.DecisionID == "" || cmd.TargetID == "" {
			return specInvalidRequest(resp, "missing required fields: decision_id/target_id")
		}
		if !cmd.TargetKind.Valid() {
			return specInvalidRequest(resp, "invalid target kind: expected issue|requirement|decision")
		}
		if cmd.Relation == "" {
			cmd.Relation = protocol.DecisionRelationAppliesTo
		}
		if !cmd.Relation.Valid() {
			return specInvalidRequest(resp, "invalid relation: expected applies-to|revises|informs|governs")
		}
		return specJSONResponse(ctx, resp, h.service.AddDecisionLink, cmd)

	case protocol.CommandDecisionSyncMD:
		var cmd protocol.DecisionSyncMDRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		return specJSONResponse(ctx, resp, h.service.SyncMD, cmd)

	case protocol.CommandDecisionImportMD:
		var cmd protocol.DecisionImportMDRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		return specJSONResponse(ctx, resp, h.service.ImportMD, cmd)

	case protocol.CommandDecisionLinkRemove:
		var cmd protocol.DecisionLinkRemoveRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.DecisionID = strings.TrimSpace(cmd.DecisionID)
		cmd.TargetID = strings.TrimSpace(cmd.TargetID)
		if cmd.DecisionID == "" || cmd.TargetID == "" {
			return specInvalidRequest(resp, "missing required fields: decision_id/target_id")
		}
		if !cmd.TargetKind.Valid() {
			return specInvalidRequest(resp, "invalid target kind: expected issue|requirement|decision")
		}
		return specJSONResponse(ctx, resp, h.service.RemoveDecisionLink, cmd)

	case protocol.CommandDecisionAcknowledge:
		var cmd protocol.DecisionAcknowledgeRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.DecisionID = strings.TrimSpace(cmd.DecisionID)
		cmd.Disposition = strings.ToLower(strings.TrimSpace(cmd.Disposition))
		cmd.Note = strings.TrimSpace(cmd.Note)
		if cmd.IssueID == "" || cmd.DecisionID == "" || cmd.Revision <= 0 {
			return specInvalidRequest(resp, "missing required fields: issue_id/decision_id/revision")
		}
		if cmd.Disposition != "reconciled" && cmd.Disposition != "compatible" {
			return specInvalidRequest(resp, "invalid disposition: expected reconciled|compatible")
		}
		return specJSONResponse(ctx, resp, h.service.AcknowledgeDecision, cmd)

	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported decision command",
			Retryable: false,
		}
		return resp
	}
}

func uniqueTrimmedStrings(values []string) []string {
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
