package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	CleanupOrphaned(context.Context, string) (*CleanupOrphanedResult, error)
}

// CleanupOrphanedResult captures the deterministic cleanup outcome used by the handler.
type CleanupOrphanedResult struct {
	ProjectID string
	Removed   []git.Worktree
	Skipped   []git.Worktree
}

// NewWorktreeHandler returns a daemon worktree command handler.
func NewWorktreeHandler(service worktreeService) *WorktreeHandler {
	return &WorktreeHandler{service: service}
}

type worktreeCommandBody struct {
	ProjectID  string `json:"project_id"`
	IssueID    string `json:"issue_id,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type worktreePayload struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	IssueID string `json:"issue_id"`
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
	IssueID   string `json:"issue_id"`
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
		if cmd.ProjectID == "" || cmd.IssueID == "" || cmd.BaseBranch == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required fields: project_id/issue_id/base_branch",
				Retryable: false,
			}
			return resp
		}

		worktree, err := h.service.Create(ctx, cmd.ProjectID, cmd.IssueID, cmd.BaseBranch)
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
		if cmd.ProjectID == "" || cmd.IssueID == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required fields: project_id/issue_id",
				Retryable: false,
			}
			return resp
		}

		if err := h.service.Delete(ctx, cmd.ProjectID, cmd.IssueID); err != nil {
			resp.Error = mapWorktreeError(err)
			return resp
		}

		body, err := json.Marshal(worktreeRemoveResultBody{
			ProjectID: cmd.ProjectID,
			IssueID:   cmd.IssueID,
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

	case CommandWorktreeCleanupOrphaned:
		if cmd.ProjectID == "" {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   "missing required fields: project_id",
				Retryable: false,
			}
			return resp
		}

		result, err := h.service.CleanupOrphaned(ctx, cmd.ProjectID)
		if err != nil {
			resp.Error = mapCleanupOrphanedError(err)
			return resp
		}
		if result == nil {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInternal,
				Message:   "cleanup orphaned returned no result",
				Retryable: false,
			}
			return resp
		}

		normalizeCleanupOrphanedResult(result)

		body, err := json.Marshal(protocol.CleanupOrphanedResponseBody{
			ProjectID:        cmd.ProjectID,
			WorktreesRemoved: len(result.Removed),
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
		Path:    wt.Path,
		Branch:  wt.Branch,
		IssueID: wt.IssueID,
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

func normalizeCleanupOrphanedResult(result *CleanupOrphanedResult) {
	if result == nil {
		return
	}

	sortWorktrees := func(worktrees []git.Worktree) {
		sort.SliceStable(worktrees, func(i, j int) bool {
			if worktrees[i].IssueID != worktrees[j].IssueID {
				return worktrees[i].IssueID < worktrees[j].IssueID
			}
			if worktrees[i].Path != worktrees[j].Path {
				return worktrees[i].Path < worktrees[j].Path
			}
			return worktrees[i].Branch < worktrees[j].Branch
		})
	}

	sortWorktrees(result.Removed)
	sortWorktrees(result.Skipped)
}
