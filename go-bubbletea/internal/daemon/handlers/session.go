package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
)

const (
	CommandSessionStart  = "session.start"
	CommandSessionAttach = "session.attach"
	CommandSessionPause  = "session.pause"
	CommandSessionResume = "session.resume"
	CommandSessionStop   = "session.stop"
)

// SessionHandler routes and applies session lifecycle commands.
type SessionHandler struct {
	store *daemonstate.Store
}

// NewSessionHandler returns a lifecycle command handler.
func NewSessionHandler(store *daemonstate.Store) *SessionHandler {
	return &SessionHandler{store: store}
}

type sessionCommandBody struct {
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id"`
	IssueID   string `json:"issue_id,omitempty"`
}

type sessionResultBody struct {
	ProjectID string                    `json:"project_id"`
	Session   daemonstate.Session       `json:"session"`
	Event     daemonstate.SessionEvent  `json:"event"`
}

// Handle executes one session lifecycle command from a daemon request envelope.
func (h *SessionHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	_ = ctx
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}

	var cmd sessionCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}
	if cmd.ProjectID == "" || cmd.SessionID == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: project_id/session_id",
			Retryable: false,
		}
		return resp
	}

	nextState, ok := mapCommandToState(req.Command)
	if !ok {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported session command",
			Retryable: false,
		}
		return resp
	}

	event, err := h.store.UpsertSession(cmd.ProjectID, cmd.SessionID, cmd.IssueID, nextState)
	if err != nil {
		resp.Error = mapSessionError(err)
		return resp
	}

	session, err := h.store.Session(cmd.ProjectID, cmd.SessionID)
	if err != nil {
		resp.Error = mapSessionError(err)
		return resp
	}

	body, err := json.Marshal(sessionResultBody{
		ProjectID: cmd.ProjectID,
		Session:   session,
		Event:     event,
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
	resp.Revision = event.Revision
	resp.Body = body
	return resp
}

func mapCommandToState(command string) (daemonstate.SessionState, bool) {
	switch command {
	case CommandSessionStart:
		return daemonstate.SessionStateStarting, true
	case CommandSessionAttach:
		return daemonstate.SessionStateAttached, true
	case CommandSessionPause:
		return daemonstate.SessionStatePaused, true
	case CommandSessionResume:
		return daemonstate.SessionStateAttached, true
	case CommandSessionStop:
		return daemonstate.SessionStateStopped, true
	default:
		return "", false
	}
}

func mapSessionError(err error) *protocol.ErrorEnvelope {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, daemonstate.ErrInvalidTransition):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeConflict,
			Message:   err.Error(),
			Retryable: false,
		}
	case errors.Is(err, daemonstate.ErrSessionNotFound):
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
