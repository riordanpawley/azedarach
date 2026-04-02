package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

const (
	CommandGitFetch          = "git.fetch"
	CommandGitMerge          = "git.merge"
	CommandGitCheckout       = "git.checkout"
	CommandGitAbortMerge     = "git.abort_merge"
	CommandGitDiffStat       = "git.diff_stat"
	CommandGitStatus         = "git.status"
	CommandGitRuntimeSignals = "git.runtime_signals"
	CommandGitStatusSync     = "git.status_sync"
	CommandGitMergePreflight = "git.merge_preflight"
	CommandGitDiscardChanges = "git.discard_changes"
	CommandGitCheckpoint     = "git.checkpoint"
)

// GitService captures the daemon-owned git operations needed by client workflows.
type GitService interface {
	Fetch(ctx context.Context, projectID, worktree, remote string) error
	Merge(ctx context.Context, projectID, worktree, branch string) (*git.MergeResult, error)
	Checkout(ctx context.Context, projectID, worktree, branch string) error
	AbortMerge(ctx context.Context, projectID, worktree string) error
	DiffStat(ctx context.Context, projectID, worktree, baseBranch string) (string, error)
	Status(ctx context.Context, projectID, worktree string) (*git.GitStatus, error)
	StatusSync(ctx context.Context, projectID, worktree string, status git.GitStatus, forcePublish bool) error
	RuntimeSignals(ctx context.Context, projectID string, targets []GitRuntimeSignalsTarget, baseBranch string, compareRemote bool, remote string) ([]GitRuntimeSignalsResult, int, error)
}

type GitMergePreflightService interface {
	MergePreflight(ctx context.Context, projectID string, req GitMergePreflightRequest) (*GitMergePreflightResult, error)
}

type GitDiscardChangesService interface {
	DiscardChanges(ctx context.Context, projectID, worktree string) (*GitDiscardChangesResult, error)
}

type GitCheckpointService interface {
	Checkpoint(ctx context.Context, projectID string, req GitCheckpointRequest) (*GitCheckpointResult, error)
}

// GitHandler routes daemon git workflow commands.
type GitHandler struct {
	service     GitService
	longRunning GitLongRunningExecutor
}

type GitLongRunningExecutor interface {
	Execute(ctx context.Context, req protocol.RequestEnvelope, command string, exec func(context.Context) protocol.ResponseEnvelope) protocol.ResponseEnvelope
}

type GitHandlerOption func(*GitHandler)

func WithGitLongRunningExecutor(executor GitLongRunningExecutor) GitHandlerOption {
	return func(handler *GitHandler) {
		handler.longRunning = executor
	}
}

// NewGitHandler returns a git workflow handler.
func NewGitHandler(service GitService, opts ...GitHandlerOption) *GitHandler {
	handler := &GitHandler{service: service}
	for _, opt := range opts {
		if opt != nil {
			opt(handler)
		}
	}
	return handler
}

type gitCommandBody struct {
	ProjectID     string                    `json:"project_id,omitempty"`
	Worktree      string                    `json:"worktree"`
	Remote        string                    `json:"remote,omitempty"`
	Branch        string                    `json:"branch,omitempty"`
	BaseBranch    string                    `json:"base_branch,omitempty"`
	Targets       []GitRuntimeSignalsTarget `json:"targets,omitempty"`
	CompareRemote bool                      `json:"compare_remote,omitempty"`
}

type GitRuntimeSignalsTarget struct {
	IssueID  string `json:"issue_id"`
	Worktree string `json:"worktree"`
}

type GitRuntimeSignalsResult struct {
	IssueID               string `json:"issue_id"`
	Worktree              string `json:"worktree"`
	HasUncommittedChanges bool   `json:"has_uncommitted_changes"`
	GitAdditions          int    `json:"git_additions"`
	GitDeletions          int    `json:"git_deletions"`
	GitAheadCount         int    `json:"git_ahead_count"`
	GitBehindCount        int    `json:"git_behind_count"`
}

type GitMergePreflightRequest struct {
	SourceID       string `json:"source_id,omitempty"`
	SourceWorktree string `json:"source_worktree"`
	TargetID       string `json:"target_id,omitempty"`
	TargetWorktree string `json:"target_worktree"`
	TargetRef      string `json:"target_ref,omitempty"`
	SourceBranch   string `json:"source_branch,omitempty"`
}

type GitMergePreflightResult struct {
	SourceID       string   `json:"source_id,omitempty"`
	SourceWorktree string   `json:"source_worktree"`
	TargetID       string   `json:"target_id,omitempty"`
	TargetWorktree string   `json:"target_worktree"`
	Clean          bool     `json:"clean"`
	Reasons        []string `json:"reasons,omitempty"`
	SourceFiles    []string `json:"source_files,omitempty"`
	TargetFiles    []string `json:"target_files,omitempty"`
	ConflictFiles  []string `json:"conflict_files,omitempty"`
}

type GitDiscardChangesRequest struct {
	Worktree string `json:"worktree"`
}

type GitDiscardChangesResult struct {
	Worktree string `json:"worktree"`
}

type GitCheckpointRequest struct {
	Worktree string `json:"worktree"`
	Message  string `json:"message,omitempty"`
}

type GitCheckpointResult struct {
	Worktree string `json:"worktree"`
	Message  string `json:"message"`
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

type gitStatusSyncBody struct {
	Worktree     string        `json:"worktree"`
	Status       git.GitStatus `json:"status"`
	ForcePublish bool          `json:"force_publish,omitempty"`
}

type gitMergeResultBody struct {
	Worktree string          `json:"worktree"`
	Branch   string          `json:"branch"`
	Result   git.MergeResult `json:"result"`
}

type gitRuntimeSignalsBody struct {
	Signals         []GitRuntimeSignalsResult `json:"signals"`
	PartialFailures int                       `json:"partial_failures"`
}

// Handle executes one git workflow command from a daemon request envelope.
func (h *GitHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	if h.longRunning != nil && isGitLongRunningCommand(req.Command) {
		return h.longRunning.Execute(ctx, req, req.Command, func(execCtx context.Context) protocol.ResponseEnvelope {
			return h.HandleDirect(execCtx, req)
		})
	}
	return h.HandleDirect(ctx, req)
}

// HandleDirect executes a git command without passing through the long-running wrapper.
func (h *GitHandler) HandleDirect(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
	}

	switch req.Command {
	case CommandGitFetch:
		cmd, ok := decodeGitCommandBody(&resp, req)
		if !ok {
			return resp
		}
		return h.handleFetch(ctx, resp, cmd)
	case CommandGitMerge:
		cmd, ok := decodeGitCommandBody(&resp, req)
		if !ok {
			return resp
		}
		return h.handleMerge(ctx, resp, cmd)
	case CommandGitCheckout:
		cmd, ok := decodeGitCommandBody(&resp, req)
		if !ok {
			return resp
		}
		return h.handleCheckout(ctx, resp, cmd)
	case CommandGitAbortMerge:
		cmd, ok := decodeGitCommandBody(&resp, req)
		if !ok {
			return resp
		}
		return h.handleAbortMerge(ctx, resp, cmd)
	case CommandGitDiffStat:
		cmd, ok := decodeGitCommandBody(&resp, req)
		if !ok {
			return resp
		}
		return h.handleDiffStat(ctx, resp, cmd)
	case CommandGitStatus:
		cmd, ok := decodeGitCommandBody(&resp, req)
		if !ok {
			return resp
		}
		return h.handleStatus(ctx, resp, cmd)
	case CommandGitRuntimeSignals:
		cmd, ok := decodeGitCommandBody(&resp, req)
		if !ok {
			return resp
		}
		return h.handleRuntimeSignals(ctx, resp, cmd)
	case CommandGitStatusSync:
		return h.handleStatusSync(ctx, resp, req)
	case CommandGitMergePreflight:
		return h.handleMergePreflight(ctx, resp, req)
	case CommandGitDiscardChanges:
		return h.handleDiscardChanges(ctx, resp, req)
	case CommandGitCheckpoint:
		return h.handleCheckpoint(ctx, resp, req)
	default:
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeUnsupportedCommand,
			Message:   "unsupported git command",
			Retryable: false,
		}
		return resp
	}
}

func isGitLongRunningCommand(command string) bool {
	switch command {
	case CommandGitFetch, CommandGitMerge, CommandGitCheckout, CommandGitAbortMerge, CommandGitDiscardChanges, CommandGitCheckpoint:
		return true
	default:
		return false
	}
}

func decodeGitCommandBody(resp *protocol.ResponseEnvelope, req protocol.RequestEnvelope) (gitCommandBody, bool) {
	var cmd gitCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return gitCommandBody{}, false
	}
	cmd.ProjectID = resolveProjectID(cmd.ProjectID, req.Meta)
	return cmd, true
}

func (h *GitHandler) handleRuntimeSignals(ctx context.Context, resp protocol.ResponseEnvelope, cmd gitCommandBody) protocol.ResponseEnvelope {
	signals, partialFailures, err := h.service.RuntimeSignals(ctx, cmd.ProjectID, cmd.Targets, cmd.BaseBranch, cmd.CompareRemote, cmd.Remote)
	if err != nil {
		resp.Error = mapGitError(err)
		return resp
	}

	body, err := json.Marshal(gitRuntimeSignalsBody{
		Signals:         signals,
		PartialFailures: partialFailures,
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

func (h *GitHandler) handleFetch(ctx context.Context, resp protocol.ResponseEnvelope, cmd gitCommandBody) protocol.ResponseEnvelope {
	cmd.Worktree = strings.TrimSpace(cmd.Worktree)
	cmd.Remote = strings.TrimSpace(cmd.Remote)
	if cmd.Worktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree",
			Retryable: false,
		}
		return resp
	}
	if cmd.Remote == "" {
		cmd.Remote = "origin"
	}

	if err := h.service.Fetch(ctx, cmd.ProjectID, cmd.Worktree, cmd.Remote); err != nil {
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

	result, err := h.service.Merge(ctx, cmd.ProjectID, cmd.Worktree, cmd.Branch)
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

	if err := h.service.Checkout(ctx, cmd.ProjectID, cmd.Worktree, cmd.Branch); err != nil {
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

	if err := h.service.AbortMerge(ctx, cmd.ProjectID, cmd.Worktree); err != nil {
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

	output, err := h.service.DiffStat(ctx, cmd.ProjectID, cmd.Worktree, cmd.BaseBranch)
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

	status, err := h.service.Status(ctx, cmd.ProjectID, cmd.Worktree)
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

func (h *GitHandler) handleStatusSync(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var cmd gitStatusSyncBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid git status sync body: %v", err),
			Retryable: false,
		}
		return resp
	}
	cmd.Worktree = strings.TrimSpace(cmd.Worktree)
	if cmd.Worktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "worktree is required",
			Retryable: false,
		}
		return resp
	}
	projectID := resolveProjectID("", req.Meta)
	if err := h.service.StatusSync(ctx, projectID, cmd.Worktree, cmd.Status, cmd.ForcePublish); err != nil {
		resp.Error = mapGitError(err)
		return resp
	}
	body, err := json.Marshal(gitStatusResultBody{Worktree: cmd.Worktree, Status: cmd.Status})
	if err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   fmt.Sprintf("failed to marshal git status sync result: %v", err),
			Retryable: false,
		}
		return resp
	}
	resp.Body = body
	return resp
}

func (h *GitHandler) handleMergePreflight(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	service, ok := h.service.(GitMergePreflightService)
	if !ok {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git merge preflight unavailable",
			Retryable: false,
		}
		return resp
	}

	var cmd GitMergePreflightRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}

	cmd.SourceWorktree = strings.TrimSpace(cmd.SourceWorktree)
	cmd.TargetWorktree = strings.TrimSpace(cmd.TargetWorktree)
	cmd.TargetRef = strings.TrimSpace(cmd.TargetRef)
	cmd.SourceBranch = strings.TrimSpace(cmd.SourceBranch)
	if cmd.SourceWorktree == "" || cmd.TargetWorktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: source_worktree/target_worktree",
			Retryable: false,
		}
		return resp
	}

	result, err := service.MergePreflight(ctx, resolveProjectID("", req.Meta), cmd)
	if err != nil {
		resp.Error = mapGitError(err)
		return resp
	}
	if result == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git merge preflight returned no result",
			Retryable: false,
		}
		return resp
	}

	body, err := json.Marshal(result)
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

func (h *GitHandler) handleDiscardChanges(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	service, ok := h.service.(GitDiscardChangesService)
	if !ok {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git discard changes unavailable",
			Retryable: false,
		}
		return resp
	}

	var cmd GitDiscardChangesRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}
	cmd.Worktree = strings.TrimSpace(cmd.Worktree)
	if cmd.Worktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree",
			Retryable: false,
		}
		return resp
	}

	result, err := service.DiscardChanges(ctx, resolveProjectID("", req.Meta), cmd.Worktree)
	if err != nil {
		resp.Error = mapGitError(err)
		return resp
	}
	if result == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git discard changes returned no result",
			Retryable: false,
		}
		return resp
	}

	body, err := json.Marshal(result)
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

func (h *GitHandler) handleCheckpoint(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	service, ok := h.service.(GitCheckpointService)
	if !ok {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git checkpoint unavailable",
			Retryable: false,
		}
		return resp
	}

	var cmd GitCheckpointRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   fmt.Sprintf("invalid command body: %v", err),
			Retryable: false,
		}
		return resp
	}
	cmd.Worktree = strings.TrimSpace(cmd.Worktree)
	cmd.Message = strings.TrimSpace(cmd.Message)
	if cmd.Worktree == "" {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInvalidRequest,
			Message:   "missing required fields: worktree",
			Retryable: false,
		}
		return resp
	}

	result, err := service.Checkpoint(ctx, resolveProjectID("", req.Meta), cmd)
	if err != nil {
		resp.Error = mapGitError(err)
		return resp
	}
	if result == nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   "git checkpoint returned no result",
			Retryable: false,
		}
		return resp
	}

	body, err := json.Marshal(result)
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
