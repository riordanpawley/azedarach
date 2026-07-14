package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/pr"
)

const (
	CommandPRCreate        = "pr.create"
	CommandPRGet           = "pr.get"
	CommandPRList          = "pr.list"
	CommandPRChecks        = "pr.checks"
	CommandPROpen          = "pr.open"
	CommandPRMerge         = "pr.merge"
	CommandGitBranchBehind = "git.branch_behind"
)

// PRHandler routes daemon commands for PR creation and branch-behind checks.
type PRHandler struct {
	prWorkflow      PRWorkflow
	workflowResolve func(context.Context, string) (PRWorkflow, error)
	gitClient       branchBehindService
	issueRefs       prIssueRefStore
}

type PRWorkflow interface {
	Create(context.Context, pr.CreatePRParams) (*pr.PRInfo, error)
	Get(context.Context, string) (*pr.PRInfo, error)
	List(context.Context, pr.ListPRParams) ([]pr.PRInfo, error)
	Checks(context.Context, string) ([]pr.CheckInfo, error)
	Open(context.Context, string) error
	Merge(context.Context, int, string) error
}

type branchBehindService interface {
	BranchBehind(context.Context, string, string, string, string) (int, int, error)
}

type prIssueRefStore interface {
	UpsertExternalIssueRef(context.Context, issues.UpsertExternalIssueRefParams) (domain.ExternalIssueRef, error)
}

type prCreateCommandBody struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
	Draft      bool   `json:"draft"`
	IssueID    string `json:"issue_id"`
}

type prBranchCommandBody struct {
	Branch string `json:"branch"`
}

type prChecksCommandBody struct {
	Ref string `json:"ref"`
}

type prListCommandBody struct {
	State string `json:"state"`
	Limit int    `json:"limit"`
}

type prMergeCommandBody struct {
	Branch   string `json:"branch"`
	Number   int    `json:"number"`
	Strategy string `json:"strategy"`
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

type prGetResultBody struct {
	PullRequest pr.PRInfo `json:"pull_request"`
}

type prListResultBody struct {
	State        string      `json:"state"`
	PullRequests []pr.PRInfo `json:"pull_requests"`
}

type prChecksResultBody struct {
	Ref          string         `json:"ref"`
	Checks       []pr.CheckInfo `json:"checks"`
	ChecksStatus string         `json:"checks_status"`
}

type prOpenResultBody struct {
	Branch string `json:"branch"`
}

type prMergeResultBody struct {
	Number   int    `json:"number"`
	Strategy string `json:"strategy"`
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
func NewPRHandler(prWorkflow PRWorkflow, gitClient branchBehindService, issueRefs ...prIssueRefStore) *PRHandler {
	var refs prIssueRefStore
	if len(issueRefs) > 0 {
		refs = issueRefs[0]
	}
	return &PRHandler{
		prWorkflow: prWorkflow,
		gitClient:  gitClient,
		issueRefs:  refs,
	}
}

// NewProjectPRHandler scopes PR operations to the repository selected by the
// request project ID.
func NewProjectPRHandler(prWorkflow PRWorkflow, gitClient branchBehindService, resolve func(context.Context, string) (PRWorkflow, error), issueRefs ...prIssueRefStore) *PRHandler {
	handler := NewPRHandler(prWorkflow, gitClient, issueRefs...)
	handler.workflowResolve = resolve
	return handler
}

func (h *PRHandler) workflow(ctx context.Context, req protocol.RequestEnvelope) (PRWorkflow, *protocol.ErrorEnvelope) {
	if h != nil && h.workflowResolve != nil {
		workflow, err := h.workflowResolve(ctx, req.Meta.ProjectID.String())
		if err != nil {
			return nil, &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInvalidRequest, Message: err.Error(), Retryable: false}
		}
		if workflow == nil {
			return nil, &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInvalidRequest, Message: "resolved PR project has no repository workflow; refusing repository fallback", Retryable: false}
		}
		return workflow, nil
	}
	if h != nil && h.prWorkflow != nil {
		return h.prWorkflow, nil
	}
	return nil, prWorkflowUnavailableError()
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
	case CommandPRGet:
		return h.handleGet(ctx, resp, req)
	case CommandPRList:
		return h.handleList(ctx, resp, req)
	case CommandPRChecks:
		return h.handleChecks(ctx, resp, req)
	case CommandPROpen:
		return h.handleOpen(ctx, resp, req)
	case CommandPRMerge:
		return h.handleMerge(ctx, resp, req)
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

func (h *PRHandler) handleList(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	workflow, resolveErr := h.workflow(ctx, req)
	if resolveErr != nil {
		resp.Error = resolveErr
		return resp
	}
	var cmd prListCommandBody
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			resp.Error = invalidPRBodyError(err)
			return resp
		}
	}
	cmd.State = strings.TrimSpace(cmd.State)
	if cmd.State == "" {
		cmd.State = "open"
	}
	prs, err := workflow.List(ctx, pr.ListPRParams{State: cmd.State, Limit: cmd.Limit})
	if err != nil {
		resp.Error = mapPRGitError(err)
		return resp
	}
	return marshalPRResponse(resp, prListResultBody{State: cmd.State, PullRequests: prs})
}

func (h *PRHandler) handleGet(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	workflow, resolveErr := h.workflow(ctx, req)
	if resolveErr != nil {
		resp.Error = resolveErr
		return resp
	}
	var cmd prBranchCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = invalidPRBodyError(err)
		return resp
	}
	cmd.Branch = strings.TrimSpace(cmd.Branch)
	if cmd.Branch == "" {
		resp.Error = missingPRFieldsError("missing required fields: branch")
		return resp
	}
	info, err := workflow.Get(ctx, cmd.Branch)
	if err != nil {
		resp.Error = mapPRGitError(err)
		return resp
	}
	if info == nil {
		resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInternal, Message: "get PR returned no result", Retryable: false}
		return resp
	}
	return marshalPRResponse(resp, prGetResultBody{PullRequest: *info})
}

func (h *PRHandler) handleChecks(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	workflow, resolveErr := h.workflow(ctx, req)
	if resolveErr != nil {
		resp.Error = resolveErr
		return resp
	}
	var cmd prChecksCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = invalidPRBodyError(err)
		return resp
	}
	cmd.Ref = strings.TrimSpace(cmd.Ref)
	if cmd.Ref == "" {
		resp.Error = missingPRFieldsError("missing required fields: ref")
		return resp
	}
	checks, err := workflow.Checks(ctx, cmd.Ref)
	if err != nil {
		resp.Error = mapPRGitError(err)
		return resp
	}
	return marshalPRResponse(resp, prChecksResultBody{Ref: cmd.Ref, Checks: checks, ChecksStatus: summarizeChecksStatus(checks)})
}

func (h *PRHandler) handleOpen(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	workflow, resolveErr := h.workflow(ctx, req)
	if resolveErr != nil {
		resp.Error = resolveErr
		return resp
	}
	var cmd prBranchCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = invalidPRBodyError(err)
		return resp
	}
	cmd.Branch = strings.TrimSpace(cmd.Branch)
	if cmd.Branch == "" {
		resp.Error = missingPRFieldsError("missing required fields: branch")
		return resp
	}
	if err := workflow.Open(ctx, cmd.Branch); err != nil {
		resp.Error = mapPRGitError(err)
		return resp
	}
	return marshalPRResponse(resp, prOpenResultBody{Branch: cmd.Branch})
}

func (h *PRHandler) handleMerge(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	workflow, resolveErr := h.workflow(ctx, req)
	if resolveErr != nil {
		resp.Error = resolveErr
		return resp
	}
	var cmd prMergeCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = invalidPRBodyError(err)
		return resp
	}
	cmd.Branch = strings.TrimSpace(cmd.Branch)
	cmd.Strategy = strings.TrimSpace(cmd.Strategy)
	if cmd.Strategy == "" {
		cmd.Strategy = "squash"
	}
	number := cmd.Number
	if number == 0 {
		if cmd.Branch == "" {
			resp.Error = missingPRFieldsError("missing required fields: number or branch")
			return resp
		}
		info, err := workflow.Get(ctx, cmd.Branch)
		if err != nil {
			resp.Error = mapPRGitError(err)
			return resp
		}
		if info == nil || info.Number == 0 {
			resp.Error = &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInternal, Message: "resolved PR has no number", Retryable: false}
			return resp
		}
		number = info.Number
	}
	if err := workflow.Merge(ctx, number, cmd.Strategy); err != nil {
		resp.Error = mapPRGitError(err)
		return resp
	}
	return marshalPRResponse(resp, prMergeResultBody{Number: number, Strategy: cmd.Strategy})
}

func (h *PRHandler) handleCreate(ctx context.Context, resp protocol.ResponseEnvelope, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	workflow, resolveErr := h.workflow(ctx, req)
	if resolveErr != nil {
		resp.Error = resolveErr
		return resp
	}

	var cmd prCreateCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		resp.Error = invalidPRBodyError(err)
		return resp
	}

	if cmd.Title == "" || cmd.Body == "" || cmd.Branch == "" || cmd.BaseBranch == "" || cmd.IssueID == "" {
		resp.Error = missingPRFieldsError("missing required fields: title/body/branch/base_branch/issue_id")
		return resp
	}

	info, err := workflow.Create(ctx, pr.CreatePRParams{
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
	h.persistPullRequestRef(ctx, cmd.IssueID, *info)

	return marshalPRResponse(resp, prCreateResultBody{
		IssueID:     cmd.IssueID,
		PullRequest: *info,
	})
}

func (h *PRHandler) persistPullRequestRef(ctx context.Context, issueID string, info pr.PRInfo) {
	if h == nil || h.issueRefs == nil || strings.TrimSpace(issueID) == "" || info.Number == 0 {
		return
	}
	metadata := map[string]string{
		"state": info.State,
		"draft": fmt.Sprintf("%t", info.Draft),
	}
	if checks := strings.TrimSpace(info.ChecksStatus); checks != "" {
		metadata["checks_status"] = checks
	}
	_, _ = h.issueRefs.UpsertExternalIssueRef(ctx, issues.UpsertExternalIssueRefParams{
		IssueID:    strings.TrimSpace(issueID),
		Provider:   "github",
		RemoteKey:  fmt.Sprintf("%d", info.Number),
		DisplayKey: fmt.Sprintf("#%d", info.Number),
		URL:        strings.TrimSpace(info.URL),
		Metadata:   metadata,
	})
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

func prWorkflowUnavailableError() *protocol.ErrorEnvelope {
	return &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInternal,
		Message:   "PR workflow unavailable",
		Retryable: false,
	}
}

func invalidPRBodyError(err error) *protocol.ErrorEnvelope {
	return &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInvalidRequest,
		Message:   fmt.Sprintf("invalid command body: %v", err),
		Retryable: false,
	}
}

func missingPRFieldsError(message string) *protocol.ErrorEnvelope {
	return &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInvalidRequest,
		Message:   message,
		Retryable: false,
	}
}

func marshalPRResponse(resp protocol.ResponseEnvelope, payload any) protocol.ResponseEnvelope {
	body, err := json.Marshal(payload)
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

func summarizeChecksStatus(checks []pr.CheckInfo) string {
	if len(checks) == 0 {
		return "unknown"
	}
	hasPending := false
	for _, check := range checks {
		switch strings.ToLower(strings.TrimSpace(check.Bucket)) {
		case "fail", "cancel":
			return "fail"
		case "pending":
			hasPending = true
		}
	}
	if hasPending {
		return "pending"
	}
	return "pass"
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
