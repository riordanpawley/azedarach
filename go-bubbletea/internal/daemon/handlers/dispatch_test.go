package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/pr"
)

type fakeWorktreeService struct{}

func (f *fakeWorktreeService) List(context.Context, string) ([]git.Worktree, error) {
	return []git.Worktree{{Path: "/tmp/wt", Branch: "main", IssueID: "afk"}}, nil
}
func (f *fakeWorktreeService) Create(context.Context, string, string, string) (*git.Worktree, error) {
	return &git.Worktree{Path: "/tmp/wt", Branch: "main", IssueID: "afk"}, nil
}
func (f *fakeWorktreeService) Delete(context.Context, string, string) error { return nil }
func (f *fakeWorktreeService) CleanupOrphaned(context.Context, string) (*CleanupOrphanedResult, error) {
	return &CleanupOrphanedResult{ProjectID: "proj"}, nil
}

type routeDevServerManager struct {
	servers map[string]*devserver.Server
}

func newRouteDevServerManager() *routeDevServerManager {
	return &routeDevServerManager{
		servers: map[string]*devserver.Server{
			"afl": {Name: "afl", Command: "run", Status: "stopped"},
		},
	}
}

func (m *routeDevServerManager) Start(ctx context.Context, issueID, name, command string) (*devserver.Server, error) {
	srv := &devserver.Server{Name: name, Command: command, Status: "running"}
	m.servers[issueID] = srv
	return srv, nil
}
func (m *routeDevServerManager) Stop(ctx context.Context, issueID string) error {
	if srv, ok := m.servers[issueID]; ok {
		srv.Status = "stopped"
	}
	return nil
}
func (m *routeDevServerManager) Get(issueID string) (*devserver.Server, bool) {
	srv, ok := m.servers[issueID]
	return srv, ok
}

type routePRWorkflow struct {
	lastParams pr.CreatePRParams
}

func (w *routePRWorkflow) Create(_ context.Context, params pr.CreatePRParams) (*pr.PRInfo, error) {
	w.lastParams = params
	return &pr.PRInfo{
		Number:  42,
		Title:   params.Title,
		URL:     "https://github.com/example/repo/pull/42",
		State:   "open",
		Draft:   params.Draft,
		Branch:  params.Branch,
		BaseRef: params.BaseBranch,
	}, nil
}

type routeBranchBehindGit struct {
	fetched   bool
	revRanges []string
}

func (g *routeBranchBehindGit) Fetch(context.Context, string, string) error {
	g.fetched = true
	return nil
}

func (g *routeBranchBehindGit) RevListCount(_ context.Context, _ string, revRange string) (int, error) {
	g.revRanges = append(g.revRanges, revRange)
	if revRange == "origin/main..HEAD" {
		return 1, nil
	}
	return 3, nil
}

type routeOperationHandler struct {
	lastCommand string
	lastBody    protocol.OperationSubmitRequestBody
}

func (h *routeOperationHandler) HandlesOperationCommands() {}

func (h *routeOperationHandler) Handle(_ context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	h.lastCommand = req.Command
	if err := json.Unmarshal(req.Body, &h.lastBody); err != nil {
		return protocol.ResponseEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindResponse,
			CompletedAt:     time.Now().UTC(),
			Error: &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInvalidRequest,
				Message:   err.Error(),
				Retryable: false,
			},
		}
	}
	body, _ := json.Marshal(protocol.OperationSubmitResponseBody{
		Created: true,
		Operation: protocol.OperationRecord{
			OperationID:  "op-1",
			ProjectID:    h.lastBody.ProjectID,
			Kind:         h.lastBody.Kind,
			IssueID:      h.lastBody.IssueID,
			DedupeKey:    h.lastBody.DedupeKey,
			ResourceKeys: append([]string(nil), h.lastBody.ResourceKeys...),
			State:        protocol.OperationStateQueued,
		},
	})
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		CompletedAt:     time.Now().UTC(),
		OK:              true,
		Body:            body,
	}
}

func TestDispatcherMixedRouting(t *testing.T) {
	session := NewSessionHandler(daemonstate.NewStore())
	gitHandler := NewGitHandler(&fakeGitService{
		fetchFn: func(context.Context, string, string) error { return nil },
		mergeFn: func(context.Context, string, string) (*git.MergeResult, error) {
			return &git.MergeResult{Success: true}, nil
		},
		checkoutFn: func(context.Context, string, string) error { return nil },
	})
	worktree := NewWorktreeHandler(&fakeWorktreeService{})
	devserverH := NewDevServerHandler(newRouteDevServerManager())
	operationH := &routeOperationHandler{}
	dispatch := NewDispatcher(session, gitHandler, worktree, devserverH, operationH)

	mkReq := func(cmd string, body any) protocol.RequestEnvelope {
		b, _ := json.Marshal(body)
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-" + cmd,
			Kind:            protocol.EnvelopeKindCommand,
			Command:         cmd,
			Body:            b,
		}
	}

	r1 := dispatch.Handle(context.Background(), mkReq("session.start", map[string]string{
		"project_id": "proj",
		"session_id": "s1",
		"issue_id":   "aey",
	}))
	if !r1.OK {
		t.Fatalf("session route failed: %+v", r1.Error)
	}

	r2 := dispatch.Handle(context.Background(), mkReq("worktree.list", map[string]string{
		"project_id": "/tmp/proj",
	}))
	if !r2.OK {
		t.Fatalf("worktree route failed: %+v", r2.Error)
	}

	r3 := dispatch.Handle(context.Background(), mkReq("git.fetch", map[string]string{
		"worktree": "/tmp/az-1",
		"remote":   "origin",
	}))
	if !r3.OK {
		t.Fatalf("git route failed: %+v", r3.Error)
	}

	r4 := dispatch.Handle(context.Background(), mkReq("devserver.status", map[string]string{
		"issue_id": "afl",
	}))
	if !r4.OK {
		t.Fatalf("devserver route failed: %+v", r4.Error)
	}

	r5 := dispatch.Handle(context.Background(), mkReq("worktree.cleanup_orphaned", map[string]string{
		"project_id": "proj",
	}))
	if !r5.OK {
		t.Fatalf("cleanup route failed: %+v", r5.Error)
	}

	r6 := dispatch.Handle(context.Background(), mkReq(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID:    "proj",
		Kind:         "session.start",
		IssueID:      "aey",
		DedupeKey:    "proj::aey::session.start",
		ResourceKeys: []string{"issue:aey", "session:aey"},
	}))
	if !r6.OK {
		t.Fatalf("operation route failed: %+v", r6.Error)
	}
	if operationH.lastCommand != protocol.CommandOperationSubmit {
		t.Fatalf("operation handler command = %q, want %q", operationH.lastCommand, protocol.CommandOperationSubmit)
	}
	if operationH.lastBody.DedupeKey != "proj::aey::session.start" {
		t.Fatalf("operation handler body = %+v", operationH.lastBody)
	}
}

func TestDispatcherSessionRoutingRequiresSessionHandler(t *testing.T) {
	mkReq := func() protocol.RequestEnvelope {
		body, _ := json.Marshal(map[string]string{
			"project_id": "proj",
			"session_id": "s1",
			"issue_id":   "aey",
		})
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-session",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         "session.start",
			Body:            body,
		}
	}

	nilSessionDispatch := NewDispatcher(nil, nil, nil, nil)
	resp := nilSessionDispatch.Handle(context.Background(), mkReq())
	if resp.OK {
		t.Fatal("expected session command to be rejected when dispatcher has no session handler")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("unexpected error mapping for nil-session dispatcher: %+v", resp.Error)
	}

	sessionDispatch := NewDispatcher(NewSessionHandler(daemonstate.NewStore()), nil, nil, nil)
	resp = sessionDispatch.Handle(context.Background(), mkReq())
	if !resp.OK {
		t.Fatalf("expected session command to route when handler is present: %+v", resp.Error)
	}
	if resp.Revision != 1 {
		t.Fatalf("session start revision = %d, want 1", resp.Revision)
	}
}

func TestDispatcherUnknownCommand(t *testing.T) {
	dispatch := NewDispatcher(nil, nil, nil, nil)
	resp := dispatch.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-x",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "foo.bar",
	})
	if resp.OK {
		t.Fatalf("expected unsupported command response")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("unexpected error mapping: %+v", resp.Error)
	}
}

func TestDispatcherOperationRoutingRequiresOperationHandler(t *testing.T) {
	body, _ := json.Marshal(protocol.OperationGetRequestBody{
		ProjectID:   "proj",
		OperationID: "op-1",
	})
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-operation",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandOperationGet,
		Body:            body,
	}

	dispatch := NewDispatcher(nil)
	resp := dispatch.Handle(context.Background(), req)
	if resp.OK {
		t.Fatal("expected operation command to be rejected when dispatcher has no operation handler")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("unexpected error mapping: %+v", resp.Error)
	}
}

func TestDispatcherRoutesPRAndBranchBehindCommands(t *testing.T) {
	prWorkflow := &routePRWorkflow{}
	gitClient := &routeBranchBehindGit{}
	dispatch := NewDispatcher(nil, NewPRHandler(prWorkflow, gitClient), nil, nil)

	prBody, _ := json.Marshal(map[string]any{
		"title":       "Add feature",
		"body":        "Body",
		"branch":      "feature/add",
		"base_branch": "main",
		"draft":       true,
		"issue_id":    "az-1",
	})
	prResp := dispatch.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-pr",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRCreate,
		Body:            prBody,
	})
	if !prResp.OK {
		t.Fatalf("PR create route failed: %+v", prResp.Error)
	}
	var prOut prCreateResultBody
	if err := json.Unmarshal(prResp.Body, &prOut); err != nil {
		t.Fatalf("unmarshal PR response: %v", err)
	}
	if prOut.IssueID != "az-1" || prOut.PullRequest.Branch != "feature/add" {
		t.Fatalf("PR response = %+v", prOut)
	}
	if prWorkflow.lastParams.Title != "Add feature" || !prWorkflow.lastParams.Draft {
		t.Fatalf("PR workflow params = %+v", prWorkflow.lastParams)
	}

	behindBody, _ := json.Marshal(map[string]string{
		"worktree":    "/tmp/repo",
		"base_branch": "main",
		"remote":      "origin",
	})
	behindResp := dispatch.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-behind",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandGitBranchBehind,
		Body:            behindBody,
	})
	if !behindResp.OK {
		t.Fatalf("branch-behind route failed: %+v", behindResp.Error)
	}
	var behindOut branchBehindResultBody
	if err := json.Unmarshal(behindResp.Body, &behindOut); err != nil {
		t.Fatalf("unmarshal branch-behind response: %v", err)
	}
	if behindOut.CommitsBehind != 3 || behindOut.RevRange != "main..origin/main" || behindOut.CommitsAhead != 1 || behindOut.AheadRevRange != "origin/main..HEAD" {
		t.Fatalf("branch-behind response = %+v", behindOut)
	}
	if !gitClient.fetched || len(gitClient.revRanges) != 2 {
		t.Fatalf("git service calls = fetched:%v revRanges:%v", gitClient.fetched, gitClient.revRanges)
	}
}
