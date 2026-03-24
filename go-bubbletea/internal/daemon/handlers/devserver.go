package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
)

const (
	CommandDevServerStart  = "devserver.start"
	CommandDevServerStop   = "devserver.stop"
	CommandDevServerStatus = "devserver.status"
)

// DevServerManager captures the devserver service behavior needed by the daemon.
type DevServerManager interface {
	Start(ctx context.Context, beadID, name, command string) (*devserver.Server, error)
	Stop(ctx context.Context, beadID string) error
	Get(beadID string) (*devserver.Server, bool)
}

// DevServerHandler routes devserver lifecycle commands.
type DevServerHandler struct {
	manager DevServerManager
}

// NewDevServerHandler returns a devserver command handler.
func NewDevServerHandler(manager DevServerManager) *DevServerHandler {
	return &DevServerHandler{manager: manager}
}

type devServerCommandBody struct {
	BeadID string `json:"bead_id"`
}

type devServerResultBody struct {
	BeadID string           `json:"bead_id"`
	Server devserver.Server `json:"server"`
}

// Handle executes a devserver command from a daemon request envelope.
func (h *DevServerHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}

	var cmd devServerCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}
	if cmd.BeadID == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required field: bead_id",
			Retryable: false,
		}
		return resp
	}

	switch req.Command {
	case CommandDevServerStart:
		return h.handleStart(ctx, resp, cmd)
	case CommandDevServerStop:
		return h.handleStop(ctx, resp, cmd)
	case CommandDevServerStatus:
		return h.handleStatus(resp, cmd)
	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported devserver command",
			Retryable: false,
		}
		return resp
	}
}

func (h *DevServerHandler) handleStart(ctx context.Context, resp protocol.ResponseEnvelope, cmd devServerCommandBody) protocol.ResponseEnvelope {
	if srv, ok := h.manager.Get(cmd.BeadID); ok && srv.Status == "running" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeConflict,
			Message:   "devserver already running",
			Retryable: false,
		}
		return resp
	}

	name := cmd.BeadID
	command := ""
	if srv, ok := h.manager.Get(cmd.BeadID); ok {
		if srv.Name != "" {
			name = srv.Name
		}
		command = srv.Command
	}

	srv, err := h.manager.Start(ctx, cmd.BeadID, name, command)
	if err != nil {
		resp.Error = mapDevServerError(err)
		return resp
	}
	if srv == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "devserver start returned no server",
			Retryable: false,
		}
		return resp
	}

	return devServerSuccess(resp, cmd.BeadID, *srv)
}

func (h *DevServerHandler) handleStop(ctx context.Context, resp protocol.ResponseEnvelope, cmd devServerCommandBody) protocol.ResponseEnvelope {
	srv, ok := h.manager.Get(cmd.BeadID)
	if !ok {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "devserver not found",
			Retryable: false,
		}
		return resp
	}
	if srv.Status != "running" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeConflict,
			Message:   "devserver is not running",
			Retryable: false,
		}
		return resp
	}

	if err := h.manager.Stop(ctx, cmd.BeadID); err != nil {
		resp.Error = mapDevServerError(err)
		return resp
	}

	if srv, ok := h.manager.Get(cmd.BeadID); ok {
		return devServerSuccess(resp, cmd.BeadID, *srv)
	}

	resp.Error = &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInternal,
		Message:   "devserver stop removed server state unexpectedly",
		Retryable: false,
	}
	return resp
}

func (h *DevServerHandler) handleStatus(resp protocol.ResponseEnvelope, cmd devServerCommandBody) protocol.ResponseEnvelope {
	srv, ok := h.manager.Get(cmd.BeadID)
	if !ok {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "devserver not found",
			Retryable: false,
		}
		return resp
	}

	return devServerSuccess(resp, cmd.BeadID, *srv)
}

func devServerSuccess(resp protocol.ResponseEnvelope, beadID string, srv devserver.Server) protocol.ResponseEnvelope {
	body, err := json.Marshal(devServerResultBody{
		BeadID: beadID,
		Server: srv,
	})
	if err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   fmt.Sprintf("marshal response body: %v", err),
			Retryable: false,
		}
		return resp
	}

	resp.OK = true
	resp.Body = body
	return resp
}

func mapDevServerError(err error) *protocol.ErrorEnvelope {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeTimeout,
			Message:   err.Error(),
			Retryable: true,
		}
	case errors.Is(err, context.Canceled):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeTimeout,
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
