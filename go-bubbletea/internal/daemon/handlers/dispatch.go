package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

// Dispatcher composes daemon command handlers and routes by command namespace.
type Dispatcher struct {
	session   *SessionHandler
	worktree  *WorktreeHandler
	devserver *DevServerHandler
}

// NewDispatcher returns a composed command router.
func NewDispatcher(session *SessionHandler, worktree *WorktreeHandler, devserver *DevServerHandler) *Dispatcher {
	return &Dispatcher{
		session:   session,
		worktree:  worktree,
		devserver: devserver,
	}
}

// Handle routes one request to the matching handler module.
func (d *Dispatcher) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	switch {
	case d.session != nil && strings.HasPrefix(req.Command, "session."):
		return d.session.Handle(ctx, req)
	case d.worktree != nil && strings.HasPrefix(req.Command, "worktree."):
		return d.worktree.Handle(ctx, req)
	case d.devserver != nil && strings.HasPrefix(req.Command, "devserver."):
		return d.devserver.Handle(ctx, req)
	default:
		return protocol.ResponseEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindResponse,
			Meta:            req.Meta,
			CompletedAt:     time.Now().UTC(),
			Error: &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeUnsupportedCommand,
				Message:   "unsupported command",
				Retryable: false,
			},
		}
	}
}
