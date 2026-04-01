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
	git       *GitHandler
	pr        *PRHandler
	spec      *SpecHandler
	operation OperationHandler
	worktree  *WorktreeHandler
	devserver *DevServerHandler
}

// OperationHandler is a marker interface so operation routes can be injected
// without colliding with existing handler Handle methods in the variadic ctor.
type OperationHandler interface {
	Handle(context.Context, protocol.RequestEnvelope) protocol.ResponseEnvelope
	HandlesOperationCommands()
}

// NewDispatcher returns a composed command router.
func NewDispatcher(session *SessionHandler, handlers ...any) *Dispatcher {
	d := &Dispatcher{session: session}
	for _, handler := range handlers {
		switch h := handler.(type) {
		case OperationHandler:
			d.operation = h
		case *GitHandler:
			d.git = h
		case *PRHandler:
			d.pr = h
		case *SpecHandler:
			d.spec = h
		case *WorktreeHandler:
			d.worktree = h
		case *DevServerHandler:
			d.devserver = h
		}
	}
	return &Dispatcher{
		session:   d.session,
		git:       d.git,
		pr:        d.pr,
		spec:      d.spec,
		operation: d.operation,
		worktree:  d.worktree,
		devserver: d.devserver,
	}
}

// Handle routes one request to the matching handler module.
func (d *Dispatcher) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	switch {
	case d.session != nil && strings.HasPrefix(req.Command, "session."):
		return d.session.Handle(ctx, req)
	case d.operation != nil && strings.HasPrefix(req.Command, "operation."):
		return d.operation.Handle(ctx, req)
	case d.pr != nil && req.Command == CommandGitBranchBehind:
		return d.pr.Handle(ctx, req)
	case d.pr != nil && strings.HasPrefix(req.Command, "pr."):
		return d.pr.Handle(ctx, req)
	case d.spec != nil && strings.HasPrefix(req.Command, "spec."):
		return d.spec.Handle(ctx, req)
	case d.git != nil && strings.HasPrefix(req.Command, "git."):
		return d.git.Handle(ctx, req)
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
