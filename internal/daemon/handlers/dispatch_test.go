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
func (f *fakeWorktreeService) Delete(context.Context, string, string, bool) error { return nil }
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
func (m *routeDevServerManager) List() []*devserver.Server {
	servers := make([]*devserver.Server, 0, len(m.servers))
	for _, srv := range m.servers {
		servers = append(servers, srv)
	}
	return servers
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
	calls []struct {
		projectID  string
		worktree   string
		baseBranch string
		remote     string
	}
}

func (g *routeBranchBehindGit) BranchBehind(_ context.Context, projectID, worktree, baseBranch, remote string) (int, int, error) {
	g.calls = append(g.calls, struct {
		projectID  string
		worktree   string
		baseBranch string
		remote     string
	}{
		projectID:  projectID,
		worktree:   worktree,
		baseBranch: baseBranch,
		remote:     remote,
	})
	return 1, 3, nil
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

type routeSpecService struct {
	lastRead protocol.SpecReadRequestBody
}

func (s *routeSpecService) ListRequirements(context.Context, protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error) {
	return protocol.SpecRequirementListResponseBody{}, nil
}

func (s *routeSpecService) GetRequirement(context.Context, protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error) {
	return protocol.SpecRequirementGetResponseBody{}, nil
}

func (s *routeSpecService) CreateRequirement(context.Context, protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error) {
	return protocol.SpecRequirementCreateResponseBody{}, nil
}

func (s *routeSpecService) UpdateRequirement(context.Context, protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error) {
	return protocol.SpecRequirementUpdateResponseBody{}, nil
}

func (s *routeSpecService) DeleteRequirement(context.Context, protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error) {
	return protocol.SpecRequirementDeleteResponseBody{}, nil
}

func (s *routeSpecService) ListLinks(context.Context, protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error) {
	return protocol.SpecLinkListResponseBody{}, nil
}

func (s *routeSpecService) AddLink(context.Context, protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error) {
	return protocol.SpecLinkAddResponseBody{}, nil
}

func (s *routeSpecService) RemoveLink(context.Context, protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error) {
	return protocol.SpecLinkRemoveResponseBody{}, nil
}

func (s *routeSpecService) Read(_ context.Context, req protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error) {
	s.lastRead = req
	return protocol.SpecReadResponseBody{
		Requirements: []protocol.SpecRequirement{{ID: "REQ-1", Status: protocol.SpecRequirementStatusOpen}},
	}, nil
}

func (s *routeSpecService) Lint(context.Context, protocol.SpecLintRequestBody) (protocol.SpecLintResponseBody, error) {
	return protocol.SpecLintResponseBody{}, nil
}

func (s *routeSpecService) Parity(context.Context, protocol.SpecParityRequestBody) (protocol.SpecParityResponseBody, error) {
	return protocol.SpecParityResponseBody{}, nil
}

func (s *routeSpecService) SyncMD(context.Context, protocol.SpecSyncMDRequestBody) (protocol.SpecSyncMDResponseBody, error) {
	return protocol.SpecSyncMDResponseBody{}, nil
}

func TestDispatcherMixedRouting(t *testing.T) {
	session := NewSessionHandler(daemonstate.NewStore())
	gitHandler := NewGitHandler(&fakeGitService{
		fetchFn: func(context.Context, string, string, string) error { return nil },
		mergeFn: func(context.Context, string, string, string) (*git.MergeResult, error) {
			return &git.MergeResult{Success: true}, nil
		},
		checkoutFn: func(context.Context, string, string, string) error { return nil },
	})
	worktree := NewWorktreeHandler(&fakeWorktreeService{})
	devserverH := NewDevServerHandler(newRouteDevServerManager())
	operationH := &routeOperationHandler{}
	specService := &routeSpecService{}
	dispatch := NewDispatcher(session, gitHandler, worktree, devserverH, operationH, NewSpecHandler(specService))

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

	r5 := dispatch.Handle(context.Background(), mkReq("devserver.list", map[string]string{}))
	if !r5.OK {
		t.Fatalf("devserver list route failed: %+v", r5.Error)
	}
	var listBody devServerListBody
	if err := json.Unmarshal(r5.Body, &listBody); err != nil {
		t.Fatalf("unmarshal devserver list body: %v", err)
	}
	if len(listBody.Servers) == 0 {
		t.Fatalf("expected at least one devserver in list body: %+v", listBody.Servers)
	}

	r6 := dispatch.Handle(context.Background(), mkReq("worktree.cleanup_orphaned", map[string]string{
		"project_id": "proj",
	}))
	if !r6.OK {
		t.Fatalf("cleanup route failed: %+v", r6.Error)
	}

	r7 := dispatch.Handle(context.Background(), mkReq(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID:    "proj",
		Kind:         "session.start",
		IssueID:      "aey",
		DedupeKey:    "proj::aey::session.start",
		ResourceKeys: []string{"issue:aey", "session:aey"},
	}))
	if !r7.OK {
		t.Fatalf("operation route failed: %+v", r7.Error)
	}
	if operationH.lastCommand != protocol.CommandOperationSubmit {
		t.Fatalf("operation handler command = %q, want %q", operationH.lastCommand, protocol.CommandOperationSubmit)
	}
	if operationH.lastBody.DedupeKey != "proj::aey::session.start" {
		t.Fatalf("operation handler body = %+v", operationH.lastBody)
	}

	r8 := dispatch.Handle(context.Background(), mkReq(protocol.CommandSpecRead, protocol.SpecReadRequestBody{
		IssueID: "proj-issue",
		ReqID:   "REQ-1",
	}))
	if !r8.OK {
		t.Fatalf("spec route failed: %+v", r8.Error)
	}
	if specService.lastRead.IssueID != "proj-issue" || specService.lastRead.ReqID != "REQ-1" {
		t.Fatalf("spec service request = %+v", specService.lastRead)
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
	if len(gitClient.calls) != 1 {
		t.Fatalf("git service branch-behind calls = %+v", gitClient.calls)
	}
	call := gitClient.calls[0]
	if call.projectID != "default" || call.worktree != "/tmp/repo" || call.baseBranch != "main" || call.remote != "origin" {
		t.Fatalf("git service branch-behind call = %+v", call)
	}
}
