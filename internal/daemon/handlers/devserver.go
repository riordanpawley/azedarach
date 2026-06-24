package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
)

const (
	CommandDevServerStart  = "devserver.start"
	CommandDevServerStop   = "devserver.stop"
	CommandDevServerStatus = "devserver.status"
	CommandDevServerList   = "devserver.list"
)

// DevServerManager captures the devserver service behavior needed by the daemon.
type DevServerManager interface {
	Start(ctx context.Context, issueID, name, command string) (*devserver.Server, error)
	Stop(ctx context.Context, issueID string) error
	Get(issueID string) (*devserver.Server, bool)
	List() []*devserver.Server
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
	IssueID string `json:"issue_id"`
}

type devServerResultBody struct {
	IssueID string           `json:"issue_id"`
	Server  devserver.Server `json:"server"`
}

type devServerListBody struct {
	Servers []devserver.Server `json:"servers"`
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

	switch req.Command {
	case CommandDevServerStart:
		if cmd.IssueID == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required field: issue_id",
				Retryable: false,
			}
			return resp
		}
		return h.handleStart(ctx, resp, req.Meta, cmd)
	case CommandDevServerStop:
		if cmd.IssueID == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required field: issue_id",
				Retryable: false,
			}
			return resp
		}
		return h.handleStop(ctx, resp, req.Meta, cmd)
	case CommandDevServerStatus:
		if cmd.IssueID == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required field: issue_id",
				Retryable: false,
			}
			return resp
		}
		return h.handleStatus(resp, req.Meta, cmd)
	case CommandDevServerList:
		return h.handleList(resp, req.Meta)
	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported devserver command",
			Retryable: false,
		}
		return resp
	}
}

func (h *DevServerHandler) handleStart(ctx context.Context, resp protocol.ResponseEnvelope, meta protocol.Metadata, cmd devServerCommandBody) protocol.ResponseEnvelope {
	storageIssueID := devServerStorageIssueID(meta, cmd.IssueID)
	if srv, ok := h.manager.Get(storageIssueID); ok && srv.Status == "running" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeConflict,
			Message:   "devserver already running",
			Retryable: false,
		}
		return resp
	}

	name := cmd.IssueID
	command := ""
	if srv, ok := h.manager.Get(storageIssueID); ok {
		if srv.Name != "" {
			name = srv.Name
		}
		command = srv.Command
	}

	srv, err := h.manager.Start(ctx, storageIssueID, name, command)
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

	return devServerSuccess(resp, cmd.IssueID, devServerPublicServer(meta, cmd.IssueID, *srv))
}

func (h *DevServerHandler) handleStop(ctx context.Context, resp protocol.ResponseEnvelope, meta protocol.Metadata, cmd devServerCommandBody) protocol.ResponseEnvelope {
	storageIssueID := devServerStorageIssueID(meta, cmd.IssueID)
	srv, ok := h.manager.Get(storageIssueID)
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

	if err := h.manager.Stop(ctx, storageIssueID); err != nil {
		resp.Error = mapDevServerError(err)
		return resp
	}

	if srv, ok := h.manager.Get(storageIssueID); ok {
		return devServerSuccess(resp, cmd.IssueID, devServerPublicServer(meta, cmd.IssueID, *srv))
	}

	resp.Error = &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInternal,
		Message:   "devserver stop removed server state unexpectedly",
		Retryable: false,
	}
	return resp
}

func (h *DevServerHandler) handleStatus(resp protocol.ResponseEnvelope, meta protocol.Metadata, cmd devServerCommandBody) protocol.ResponseEnvelope {
	srv, ok := h.manager.Get(devServerStorageIssueID(meta, cmd.IssueID))
	if !ok {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "devserver not found",
			Retryable: false,
		}
		return resp
	}

	return devServerSuccess(resp, cmd.IssueID, devServerPublicServer(meta, cmd.IssueID, *srv))
}

func (h *DevServerHandler) handleList(resp protocol.ResponseEnvelope, meta protocol.Metadata) protocol.ResponseEnvelope {
	servers := h.manager.List()
	out := make([]devserver.Server, 0, len(servers))
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		public, ok := devServerPublicServerForProject(meta, *srv)
		if !ok {
			continue
		}
		out = append(out, public)
	}

	body, err := json.Marshal(devServerListBody{Servers: out})
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

func devServerStorageIssueID(meta protocol.Metadata, issueID string) string {
	issueID = strings.TrimSpace(issueID)
	projectID := strings.TrimSpace(resolveProjectID("", meta))
	if projectID == "" {
		projectID = protocol.DefaultProjectID
	}
	return projectID + ":" + issueID
}

func devServerPublicServer(meta protocol.Metadata, issueID string, srv devserver.Server) devserver.Server {
	srv.ID = issueID
	srv.IssueID = issueID
	return srv
}

func devServerPublicServerForProject(meta protocol.Metadata, srv devserver.Server) (devserver.Server, bool) {
	prefix := strings.TrimSpace(resolveProjectID("", meta))
	if prefix == "" {
		prefix = protocol.DefaultProjectID
	}
	prefix += ":"
	if !strings.HasPrefix(srv.IssueID, prefix) {
		return devserver.Server{}, false
	}
	issueID := strings.TrimPrefix(srv.IssueID, prefix)
	return devServerPublicServer(meta, issueID, srv), true
}

func devServerSuccess(resp protocol.ResponseEnvelope, issueID string, srv devserver.Server) protocol.ResponseEnvelope {
	body, err := json.Marshal(devServerResultBody{
		IssueID: issueID,
		Server:  srv,
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
