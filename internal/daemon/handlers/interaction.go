package handlers

import (
	"context"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"strings"
	"time"
)

type InteractionService interface {
	CreateInteraction(context.Context, protocol.InteractionCreateRequestBody) (protocol.InteractionResponseBody, error)
	ListInteractions(context.Context, protocol.InteractionListRequestBody) (protocol.InteractionListResponseBody, error)
	GetInteraction(context.Context, protocol.InteractionGetRequestBody) (protocol.InteractionResponseBody, error)
	MutateInteraction(context.Context, string, protocol.InteractionMutationRequestBody) (protocol.InteractionResponseBody, error)
	ResolveInteraction(context.Context, protocol.InteractionResolveRequestBody) (protocol.InteractionResponseBody, error)
}
type InteractionHandler struct{ service InteractionService }

func NewInteractionHandler(s InteractionService) *InteractionHandler {
	return &InteractionHandler{service: s}
}
func (h *InteractionHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, CompletedAt: time.Now().UTC()}
	if h.service == nil {
		resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeUnavailable, Message: "interaction service unavailable", Retryable: true}
		return resp
	}
	switch req.Command {
	case protocol.CommandInteractionCreate:
		var cmd protocol.InteractionCreateRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		return specJSONResponse(ctx, resp, h.service.CreateInteraction, cmd)
	case protocol.CommandInteractionList:
		var cmd protocol.InteractionListRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.IssueID = strings.TrimSpace(cmd.IssueID)
		return specJSONResponse(ctx, resp, h.service.ListInteractions, cmd)
	case protocol.CommandInteractionGet:
		var cmd protocol.InteractionGetRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.ID = strings.TrimSpace(cmd.ID)
		if cmd.ID == "" {
			return specInvalidRequest(resp, "missing required field: id")
		}
		return specJSONResponse(ctx, resp, h.service.GetInteraction, cmd)
	case protocol.CommandInteractionResolve:
		var cmd protocol.InteractionResolveRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		if err := validateInteractionMutation(cmd.InteractionMutationRequestBody, true); err != "" {
			return specInvalidRequest(resp, err)
		}
		if !interactionHumanActor(cmd.Actor) {
			return specInvalidRequest(resp, "only the human respondent may resolve interaction requests")
		}
		return specJSONResponse(ctx, resp, h.service.ResolveInteraction, cmd)
	case protocol.CommandInteractionDiscuss, protocol.CommandInteractionPropose, protocol.CommandInteractionAnswer, protocol.CommandInteractionWithdraw:
		var cmd protocol.InteractionMutationRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		if err := validateInteractionMutation(cmd, req.Command == protocol.CommandInteractionPropose || req.Command == protocol.CommandInteractionAnswer); err != "" {
			return specInvalidRequest(resp, err)
		}
		if req.Command == protocol.CommandInteractionAnswer && !interactionHumanActor(cmd.Actor) {
			return specInvalidRequest(resp, "only the human respondent may answer interaction requests")
		}
		return specJSONResponse(ctx, resp, func(ctx context.Context, in protocol.InteractionMutationRequestBody) (protocol.InteractionResponseBody, error) {
			return h.service.MutateInteraction(ctx, req.Command, in)
		}, cmd)
	default:
		resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeUnsupportedCommand, Message: "unsupported interaction command"}
		return resp
	}
}

func interactionHumanActor(actor string) bool {
	a := strings.ToLower(strings.TrimSpace(actor))
	return a == "human" || strings.HasPrefix(a, "human:")
}
func validateInteractionMutation(cmd protocol.InteractionMutationRequestBody, answer bool) string {
	cmd.ID = strings.TrimSpace(cmd.ID)
	if cmd.ID == "" || cmd.ExpectedRevision < 1 || strings.TrimSpace(cmd.Actor) == "" {
		return "missing required fields: id/expected_revision/actor"
	}
	if answer && (strings.TrimSpace(cmd.Answer.SelectedOption) == "" || strings.TrimSpace(cmd.Answer.Rationale) == "" || cmd.Answer.Revision < 1) {
		return "missing required field: answer"
	}
	return ""
}
