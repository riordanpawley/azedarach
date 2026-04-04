package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/pr"
)

const (
	CommandPRCreate        = "pr.create"
	CommandGitBranchBehind = "git.branch_behind"
)

// PRHandler routes daemon commands for PR creation and branch-behind checks.
type PRHandler struct {
	prWorkflow prWorkflow
	gitClient  branchBehindService
}

type prWorkflow interface {
	Create(context.Context, pr.CreatePRParams) (*pr.PRInfo, error)
}

type branchBehindService interface {
	BranchBehind(context.Context, string, string, string, string) (int, int, error)
}

type prCreateCommandBody struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
	Draft      bool   `json:"draft"`
	IssueID    string `json:"issue_id"`
}

type branchBehindCommandBody struct {
	Worktree   string `json:"worktree"`
	BaseBranch string `json:"base_branch"`
	Remote     string `json:"remote"`
}

type prCreateResultBody struct {
	IssueID     string    `json:"issue_id"`
	PullRequest pr.PRInfo `json:"pull_request"`
}

type branchBehindResultBody struct {
	Worktree      string `json:"worktree"`
	BaseBranch    string `json:"base_branch"`
	Remote        string `json:"remote"`
	RevRange      string `json:"rev_range"`
	AheadRevRange string `json:"ahead_rev_range"`
	CommitsAhead  int    `json:"commits_ahead"`
	Ahead         bool   `json:"ahead"`
	CommitsBehind int    `json:"commits_behind"`
	Behind        bool   `json:"behind"`
}

// NewPRHandler returns a daemon handler for PR creation and branch-behind checks.
func NewPRHandler(prWorkflow prWorkflow, gitClient branchBehindService) *PRHandler {
	return &PRHandler{
		prWorkflow: prWorkflow,
		gitClient:  gitClient,
	}
}

// Handle executes one PR or branch-behind command from a daemon request envelope.
func (h *PRHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}

	switch req.Command {
	case CommandPRCreate:
		return h.handleCreate(ctx, resp, req)
	case CommandGitBranchBehind:
		return h.handleBranchBehind(ctx, resp, req)
	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported pr command",
			Retryable: false,
		}
		return resp
	}
}

func (h *PRHandler) handleCreate(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	if h.prWorkflow == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "PR workflow unavailable",
			Retryable: false,
		}
		return resp
	}

	var cmd prCreateCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}

	if cmd.Title == "" || cmd.Body == "" || cmd.Branch == "" || cmd.BaseBranch == "" || cmd.IssueID == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: title/body/branch/base_branch/issue_id",
			Retryable: false,
		}
		return resp
	}

	info, err := h.prWorkflow.Create(ctx, pr.CreatePRParams{
		Title:      cmd.Title,
		Body:       cmd.Body,
		Branch:     cmd.Branch,
		BaseBranch: cmd.BaseBranch,
		Draft:      cmd.Draft,
		IssueID:    cmd.IssueID,
	})
	if err != nil {
		resp.Error = mapPRGitError(err)
		return resp
	}
	if info == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "create PR returned no result",
			Retryable: false,
		}
		return resp
	}

	body, err := json.Marshal(prCreateResultBody{
		IssueID:     cmd.IssueID,
		PullRequest: *info,
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

func (h *PRHandler) handleBranchBehind(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	if h.gitClient == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git client unavailable",
			Retryable: false,
		}
		return resp
	}

	var cmd branchBehindCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}

	cmd.Worktree = strings.TrimSpace(cmd.Worktree)
	cmd.BaseBranch = strings.TrimSpace(cmd.BaseBranch)
	cmd.Remote = strings.TrimSpace(cmd.Remote)
	if cmd.Worktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree",
			Retryable: false,
		}
		return resp
	}
	if cmd.BaseBranch == "" {
		cmd.BaseBranch = "main"
	}
	if cmd.Remote == "" {
		cmd.Remote = "origin"
	}

	projectID := resolveProjectID("", req.Meta)
	revRange := fmt.Sprintf("%s..%s/%s", cmd.BaseBranch, cmd.Remote, cmd.BaseBranch)
	aheadRevRange := fmt.Sprintf("%s/%s..HEAD", cmd.Remote, cmd.BaseBranch)
	commitsAhead, commitsBehind, err := h.gitClient.BranchBehind(ctx, projectID, cmd.Worktree, cmd.BaseBranch, cmd.Remote)
	if err != nil {
		resp.Error = mapPRGitError(err)
		return resp
	}

	body, err := json.Marshal(branchBehindResultBody{
		Worktree:      cmd.Worktree,
		BaseBranch:    cmd.BaseBranch,
		Remote:        cmd.Remote,
		RevRange:      revRange,
		AheadRevRange: aheadRevRange,
		CommitsAhead:  commitsAhead,
		Ahead:         commitsAhead > 0,
		CommitsBehind: commitsBehind,
		Behind:        commitsBehind > 0,
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

func mapPRGitError(err error) *protocol.ErrorEnvelope {
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
