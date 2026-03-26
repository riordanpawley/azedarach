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
	CommandGitFetch      = "git.fetch"
	CommandGitMerge      = "git.merge"
	CommandGitCheckout   = "git.checkout"
	CommandGitAbortMerge = "git.abort_merge"
	CommandGitDiffStat   = "git.diff_stat"
	CommandGitStatus     = "git.status"
)

// GitService captures the daemon-owned git operations needed by client workflows.
type GitService interface {
	Fetch(ctx context.Context, worktree, remote string) error
	Merge(ctx context.Context, worktree, branch string) (*git.MergeResult, error)
	Checkout(ctx context.Context, worktree, branch string) error
	AbortMerge(ctx context.Context, worktree string) error
	DiffStat(ctx context.Context, worktree string) (string, error)
	Status(ctx context.Context, worktree string) (*git.GitStatus, error)
}

// GitHandler routes daemon git workflow commands.
type GitHandler struct {
	service GitService
}

// NewGitHandler returns a git workflow handler.
func NewGitHandler(service GitService) *GitHandler {
	return &GitHandler{service: service}
}

type gitCommandBody struct {
	Worktree string `json:"worktree"`
	Remote   string `json:"remote,omitempty"`
	Branch   string `json:"branch,omitempty"`
}

type gitActionResultBody struct {
	Worktree string `json:"worktree"`
	Remote   string `json:"remote,omitempty"`
	Branch   string `json:"branch,omitempty"`
}

type gitOutputResultBody struct {
	Worktree string `json:"worktree"`
	Output   string `json:"output"`
}

type gitStatusResultBody struct {
	Worktree string        `json:"worktree"`
	Status   git.GitStatus `json:"status"`
}

type gitMergeResultBody struct {
	Worktree string          `json:"worktree"`
	Branch   string          `json:"branch"`
	Result   git.MergeResult `json:"result"`
}

// Handle executes one git workflow command from a daemon request envelope.
func (h *GitHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}

	var cmd gitCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}

	switch req.Command {
	case CommandGitFetch:
		return h.handleFetch(ctx, resp, cmd)
	case CommandGitMerge:
		return h.handleMerge(ctx, resp, cmd)
	case CommandGitCheckout:
		return h.handleCheckout(ctx, resp, cmd)
	case CommandGitAbortMerge:
		return h.handleAbortMerge(ctx, resp, cmd)
	case CommandGitDiffStat:
		return h.handleDiffStat(ctx, resp, cmd)
	case CommandGitStatus:
		return h.handleStatus(ctx, resp, cmd)
	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported git command",
			Retryable: false,
		}
		return resp
	}
}

func (h *GitHandler) handleFetch(ctx context.Context, resp protocol.ResponseEnvelope, cmd gitCommandBody) protocol.ResponseEnvelope {
	if cmd.Worktree == "" || cmd.Remote == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree/remote",
			Retryable: false,
		}
		return resp
	}

	if err := h.service.Fetch(ctx, cmd.Worktree, cmd.Remote); err != nil {
		resp.Error = mapGitError(err)
		return resp
	}

	body, err := json.Marshal(gitActionResultBody{
		Worktree: cmd.Worktree,
		Remote:   cmd.Remote,
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

func (h *GitHandler) handleMerge(ctx context.Context, resp protocol.ResponseEnvelope, cmd gitCommandBody) protocol.ResponseEnvelope {
	if cmd.Worktree == "" || cmd.Branch == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree/branch",
			Retryable: false,
		}
		return resp
	}

	result, err := h.service.Merge(ctx, cmd.Worktree, cmd.Branch)
	if err != nil {
		resp.Error = mapGitError(err)
		return resp
	}
	if result == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git merge returned no result",
			Retryable: false,
		}
		return resp
	}

	body, err := json.Marshal(gitMergeResultBody{
		Worktree: cmd.Worktree,
		Branch:   cmd.Branch,
		Result:   *result,
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

func (h *GitHandler) handleCheckout(ctx context.Context, resp protocol.ResponseEnvelope, cmd gitCommandBody) protocol.ResponseEnvelope {
	if cmd.Worktree == "" || cmd.Branch == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree/branch",
			Retryable: false,
		}
		return resp
	}

	if err := h.service.Checkout(ctx, cmd.Worktree, cmd.Branch); err != nil {
		resp.Error = mapGitError(err)
		return resp
	}

	body, err := json.Marshal(gitActionResultBody{
		Worktree: cmd.Worktree,
		Branch:   cmd.Branch,
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

func (h *GitHandler) handleAbortMerge(ctx context.Context, resp protocol.ResponseEnvelope, cmd gitCommandBody) protocol.ResponseEnvelope {
	if cmd.Worktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree",
			Retryable: false,
		}
		return resp
	}

	if err := h.service.AbortMerge(ctx, cmd.Worktree); err != nil {
		resp.Error = mapGitError(err)
		return resp
	}

	body, err := json.Marshal(gitActionResultBody{
		Worktree: cmd.Worktree,
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

func (h *GitHandler) handleDiffStat(ctx context.Context, resp protocol.ResponseEnvelope, cmd gitCommandBody) protocol.ResponseEnvelope {
	if cmd.Worktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree",
			Retryable: false,
		}
		return resp
	}

	output, err := h.service.DiffStat(ctx, cmd.Worktree)
	if err != nil {
		resp.Error = mapGitError(err)
		return resp
	}

	body, err := json.Marshal(gitOutputResultBody{
		Worktree: cmd.Worktree,
		Output:   output,
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

func (h *GitHandler) handleStatus(ctx context.Context, resp protocol.ResponseEnvelope, cmd gitCommandBody) protocol.ResponseEnvelope {
	if cmd.Worktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree",
			Retryable: false,
		}
		return resp
	}

	status, err := h.service.Status(ctx, cmd.Worktree)
	if err != nil {
		resp.Error = mapGitError(err)
		return resp
	}
	if status == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git status returned no result",
			Retryable: false,
		}
		return resp
	}

	body, err := json.Marshal(gitStatusResultBody{
		Worktree: cmd.Worktree,
		Status:   *status,
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

func mapGitError(err error) *protocol.ErrorEnvelope {
	switch {
	case err == nil:
		return nil
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
