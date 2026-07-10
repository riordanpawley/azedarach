package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

type AIAccountService interface {
	Backup(context.Context, protocol.AIAccountBackupRequestBody) (protocol.AIAccountBackupResponseBody, error)
	List(context.Context, protocol.AIAccountListRequestBody) (protocol.AIAccountListResponseBody, error)
	Status(context.Context, protocol.AIAccountStatusRequestBody) (protocol.AIAccountStatusResponseBody, error)
	Activate(context.Context, protocol.AIAccountActivateRequestBody) (protocol.AIAccountActivateResponseBody, error)
	Delete(context.Context, protocol.AIAccountDeleteRequestBody) (protocol.AIAccountDeleteResponseBody, error)
}

type AIAccountHandler struct {
	service AIAccountService
}

func NewAIAccountHandler(service AIAccountService) *AIAccountHandler {
	return &AIAccountHandler{service: service}
}

func (h *AIAccountHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}
	if h.service == nil {
		resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeUnavailable, Message: "AI account service unavailable", Retryable: true}
		return resp
	}

	switch req.Command {
	case protocol.CommandAIAccountBackup:
		var cmd protocol.AIAccountBackupRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.Name = strings.TrimSpace(cmd.Name)
		if !validateAIAccountProfile(cmd.Provider, cmd.Name, &resp) {
			return resp
		}
		if strings.HasPrefix(cmd.Name, "_") {
			return specInvalidRequest(resp, "AI account profile names beginning with underscore are reserved")
		}
		return aiAccountJSONResponse(ctx, resp, h.service.Backup, cmd)
	case protocol.CommandAIAccountList:
		var cmd protocol.AIAccountListRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) || !validateOptionalAIAccountProvider(cmd.Provider, &resp) {
			return resp
		}
		return aiAccountJSONResponse(ctx, resp, h.service.List, cmd)
	case protocol.CommandAIAccountStatus:
		var cmd protocol.AIAccountStatusRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) || !validateOptionalAIAccountProvider(cmd.Provider, &resp) {
			return resp
		}
		return aiAccountJSONResponse(ctx, resp, h.service.Status, cmd)
	case protocol.CommandAIAccountActivate:
		var cmd protocol.AIAccountActivateRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.Name = strings.TrimSpace(cmd.Name)
		if !validateAIAccountProfile(cmd.Provider, cmd.Name, &resp) {
			return resp
		}
		return aiAccountJSONResponse(ctx, resp, h.service.Activate, cmd)
	case protocol.CommandAIAccountDelete:
		var cmd protocol.AIAccountDeleteRequestBody
		if !decodeSpecRequest(req.Body, &cmd, &resp) {
			return resp
		}
		cmd.Name = strings.TrimSpace(cmd.Name)
		if !validateAIAccountProfile(cmd.Provider, cmd.Name, &resp) {
			return resp
		}
		if !cmd.Confirm {
			return specInvalidRequest(resp, "AI account delete requires confirm=true")
		}
		if strings.HasPrefix(cmd.Name, "_") {
			return specInvalidRequest(resp, "protected AI account system profiles cannot be deleted")
		}
		return aiAccountJSONResponse(ctx, resp, h.service.Delete, cmd)
	default:
		resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeUnsupportedCommand, Message: "unsupported AI account command"}
		return resp
	}
}

func validateOptionalAIAccountProvider(provider protocol.AIAccountProvider, resp *protocol.ResponseEnvelope) bool {
	if provider == "" || provider.Valid() {
		return true
	}
	resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInvalidRequest, Message: fmt.Sprintf("unsupported AI account provider %q (want claude or codex)", provider)}
	return false
}

func validateAIAccountProfile(provider protocol.AIAccountProvider, name string, resp *protocol.ResponseEnvelope) bool {
	if !validateOptionalAIAccountProvider(provider, resp) || provider == "" {
		if provider == "" && resp.Error == nil {
			resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInvalidRequest, Message: "missing required field: provider"}
		}
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInvalidRequest, Message: "invalid AI account profile name"}
		return false
	}
	for i, r := range name {
		if (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._@+-", r)) && !(i == 0 && r == '.') {
			continue
		}
		resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInvalidRequest, Message: "AI account profile name contains unsupported characters"}
		return false
	}
	return true
}

func aiAccountJSONResponse[Req any, Resp any](ctx context.Context, resp protocol.ResponseEnvelope, fn func(context.Context, Req) (Resp, error), cmd Req) protocol.ResponseEnvelope {
	body, err := fn(ctx, cmd)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeTimeout, Message: err.Error(), Retryable: true}
		case errors.Is(err, domain.ErrConflict):
			resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeConflict, Message: err.Error()}
		case errors.Is(err, domain.ErrNotFound):
			resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInvalidRequest, Message: err.Error()}
		default:
			resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInternal, Message: err.Error()}
		}
		return resp
	}
	payload, err := json.Marshal(body)
	if err != nil {
		resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInternal, Message: fmt.Sprintf("marshal AI account response: %v", err)}
		return resp
	}
	resp.OK = true
	resp.Body = payload
	return resp
}
