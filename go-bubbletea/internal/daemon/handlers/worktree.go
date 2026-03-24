package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

const (
	CommandWorktreeList            = "worktree.list"
	CommandWorktreeCreate          = "worktree.create"
	CommandWorktreeRemove          = "worktree.remove"
	CommandWorktreeCleanupOrphaned = "worktree.cleanup_orphaned"
)

var (
	ErrWorktreeNotFound       = errors.New("worktree not found")
	ErrWorktreeAlreadyExists  = errors.New("worktree already exists")
	ErrWorktreeInvalidRequest = errors.New("invalid worktree request")

	ErrCleanupOrphanedNotFound       = errors.New("cleanup orphaned worktree not found")
	ErrCleanupOrphanedConflict       = errors.New("cleanup orphaned conflict")
	ErrCleanupOrphanedInvalidRequest = errors.New("invalid cleanup orphaned request")
)

// WorktreeHandler routes daemon worktree commands.
type WorktreeHandler struct {
	service worktreeService
}

type worktreeService interface {
	List(context.Context, string) ([]git.Worktree, error)
	Create(context.Context, string, string, string) (*git.Worktree, error)
	Delete(context.Context, string, string) error
}

// NewWorktreeHandler returns a daemon worktree command handler.
func NewWorktreeHandler(service worktreeService) *WorktreeHandler {
	return &WorktreeHandler{service: service}
}

type worktreeCommandBody struct {
	ProjectID  string `json:"project_id"`
	BeadID     string `json:"bead_id,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type worktreePayload struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	BeadID string `json:"bead_id"`
}

type worktreeListResultBody struct {
	ProjectID string            `json:"project_id"`
	Worktrees []worktreePayload `json:"worktrees"`
}

type worktreeResultBody struct {
	ProjectID string          `json:"project_id"`
	Worktree  worktreePayload `json:"worktree"`
}

type worktreeRemoveResultBody struct {
	ProjectID string `json:"project_id"`
	BeadID    string `json:"bead_id"`
}

// Handle executes one worktree command from a daemon request envelope.
func (h *WorktreeHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}

	var cmd worktreeCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}

	switch req.Command {
	case CommandWorktreeList:
		if cmd.ProjectID == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required fields: project_id",
				Retryable: false,
			}
			return resp
		}

		worktrees, err := h.service.List(ctx, cmd.ProjectID)
		if err != nil {
			resp.Error = mapWorktreeError(err)
			return resp
		}

		body, err := json.Marshal(worktreeListResultBody{
			ProjectID: cmd.ProjectID,
			Worktrees: mapWorktreePayloads(worktrees),
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

	case CommandWorktreeCreate:
		if cmd.ProjectID == "" || cmd.BeadID == "" || cmd.BaseBranch == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required fields: project_id/bead_id/base_branch",
				Retryable: false,
			}
			return resp
		}

		worktree, err := h.service.Create(ctx, cmd.ProjectID, cmd.BeadID, cmd.BaseBranch)
		if err != nil {
			resp.Error = mapWorktreeError(err)
			return resp
		}

		body, err := json.Marshal(worktreeResultBody{
			ProjectID: cmd.ProjectID,
			Worktree:  mapWorktreePayload(worktree),
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

	case CommandWorktreeRemove:
		if cmd.ProjectID == "" || cmd.BeadID == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required fields: project_id/bead_id",
				Retryable: false,
			}
			return resp
		}

		if err := h.service.Delete(ctx, cmd.ProjectID, cmd.BeadID); err != nil {
			resp.Error = mapWorktreeError(err)
			return resp
		}

		body, err := json.Marshal(worktreeRemoveResultBody{
			ProjectID: cmd.ProjectID,
			BeadID:    cmd.BeadID,
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

	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported worktree command",
			Retryable: false,
		}
		return resp
	}
}

func mapWorktreePayload(wt *git.Worktree) worktreePayload {
	if wt == nil {
		return worktreePayload{}
	}

	return worktreePayload{
		Path:   wt.Path,
		Branch: wt.Branch,
		BeadID: wt.BeadID,
	}
}

func mapWorktreePayloads(worktrees []git.Worktree) []worktreePayload {
	out := make([]worktreePayload, 0, len(worktrees))
	for _, wt := range worktrees {
		out = append(out, mapWorktreePayload(&wt))
	}
	return out
}

func mapWorktreeError(err error) *protocol.ErrorEnvelope {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrWorktreeNotFound), errors.Is(err, ErrWorktreeInvalidRequest):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   err.Error(),
			Retryable: false,
		}
	case errors.Is(err, ErrWorktreeAlreadyExists):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeConflict,
			Message:   err.Error(),
			Retryable: false,
		}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeTimeout,
			Message:   err.Error(),
			Retryable: true,
		}
	default:
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   err.Error(),
			Retryable: false,
		}
	}
}

func mapCleanupOrphanedError(err error) *protocol.ErrorEnvelope {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCleanupOrphanedInvalidRequest), errors.Is(err, ErrCleanupOrphanedNotFound):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   err.Error(),
			Retryable: false,
		}
	case errors.Is(err, ErrCleanupOrphanedConflict):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeConflict,
			Message:   err.Error(),
			Retryable: false,
		}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeTimeout,
			Message:   err.Error(),
			Retryable: true,
		}
	default:
		return &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   err.Error(),
			Retryable: false,
		}
	}
}
