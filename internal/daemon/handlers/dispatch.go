package handlers

import (
	"context"
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
	target, ok := DispatcherTarget(req.Command)
	if !ok {
		return unsupportedCommandResponse(req)
	}

	switch target {
	case CommandDispatchSession:
		if d.session == nil {
			return unsupportedCommandResponse(req)
		}
		return d.session.Handle(ctx, req)
	case CommandDispatchOperation:
		if d.operation == nil {
			return unsupportedCommandResponse(req)
		}
		return d.operation.Handle(ctx, req)
	case CommandDispatchPR:
		if d.pr == nil {
			return unsupportedCommandResponse(req)
		}
		return d.pr.Handle(ctx, req)
	case CommandDispatchSpec:
		if d.spec == nil {
			return unsupportedCommandResponse(req)
		}
		return d.spec.Handle(ctx, req)
	case CommandDispatchGit:
		if d.git == nil {
			return unsupportedCommandResponse(req)
		}
		return d.git.Handle(ctx, req)
	case CommandDispatchWorktree:
		if d.worktree == nil {
			return unsupportedCommandResponse(req)
		}
		return d.worktree.Handle(ctx, req)
	case CommandDispatchDevServer:
		if d.devserver == nil {
			return unsupportedCommandResponse(req)
		}
		return d.devserver.Handle(ctx, req)
	default:
		return unsupportedCommandResponse(req)
	}
}

func unsupportedCommandResponse(req protocol.RequestEnvelope) protocol.ResponseEnvelope {
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
