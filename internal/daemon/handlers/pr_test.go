package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/pr"
)

type fakePRWorkflow struct {
	lastParams   pr.CreatePRParams
	lastBranch   string
	lastRef      string
	lastList     pr.ListPRParams
	lastNumber   int
	lastStrategy string
	result       *pr.PRInfo
	checks       []pr.CheckInfo
	err          error
}

func (f *fakePRWorkflow) Create(_ context.Context, params pr.CreatePRParams) (*pr.PRInfo, error) {
	f.lastParams = params
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &pr.PRInfo{
		Number:  7,
		Title:   params.Title,
		URL:     "https://github.com/example/repo/pull/7",
		State:   "open",
		Draft:   params.Draft,
		Branch:  params.Branch,
		BaseRef: params.BaseBranch,
	}, nil
}

func (f *fakePRWorkflow) Get(_ context.Context, branch string) (*pr.PRInfo, error) {
	f.lastBranch = branch
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &pr.PRInfo{Number: 7, Title: "Add feature", URL: "https://github.com/example/repo/pull/7", State: "open", Branch: branch, BaseRef: "main"}, nil
}

func (f *fakePRWorkflow) List(_ context.Context, params pr.ListPRParams) ([]pr.PRInfo, error) {
	f.lastList = params
	if f.err != nil {
		return nil, f.err
	}
	return []pr.PRInfo{{Number: 7, Title: "Add feature", URL: "https://github.com/example/repo/pull/7", State: "open", Branch: "feature/add", BaseRef: "main"}}, nil
}

func (f *fakePRWorkflow) Checks(_ context.Context, ref string) ([]pr.CheckInfo, error) {
	f.lastRef = ref
	if f.err != nil {
		return nil, f.err
	}
	if f.checks != nil {
		return f.checks, nil
	}
	return []pr.CheckInfo{{Name: "test", Bucket: "pass"}}, nil
}

func (f *fakePRWorkflow) Open(_ context.Context, branch string) error {
	f.lastBranch = branch
	return f.err
}

func (f *fakePRWorkflow) Merge(_ context.Context, number int, strategy string) error {
	f.lastNumber = number
	f.lastStrategy = strategy
	return f.err
}

type fakeBranchBehindGit struct {
	branchBehindCalls []struct {
		projectID  string
		worktree   string
		baseBranch string
		remote     string
	}
	aheadCount  int
	behindCount int
	err         error
}

type fakePRIssueRefStore struct {
	params []issues.UpsertExternalIssueRefParams
	err    error
}

func (s *fakePRIssueRefStore) UpsertExternalIssueRef(_ context.Context, params issues.UpsertExternalIssueRefParams) (domain.ExternalIssueRef, error) {
	s.params = append(s.params, params)
	if s.err != nil {
		return domain.ExternalIssueRef{}, s.err
	}
	return domain.ExternalIssueRef{IssueID: params.IssueID, Provider: params.Provider, RemoteKey: params.RemoteKey}, nil
}

func (g *fakeBranchBehindGit) BranchBehind(_ context.Context, projectID, worktree, baseBranch, remote string) (int, int, error) {
	g.branchBehindCalls = append(g.branchBehindCalls, struct {
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
	if g.err != nil {
		return 0, 0, g.err
	}
	return g.aheadCount, g.behindCount, nil
}

func TestPRHandlerCreateAndBranchBehind(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		workflow := &fakePRWorkflow{}
		handler := NewPRHandler(workflow, nil)

		body, _ := json.Marshal(map[string]any{
			"title":       "Add feature",
			"body":        "Body",
			"branch":      "feature/add",
			"base_branch": "main",
			"draft":       true,
			"issue_id":    "az-1",
		})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-pr",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandPRCreate,
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("create response error: %+v", resp.Error)
		}
		var out prCreateResultBody
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if out.IssueID != "az-1" || out.PullRequest.Branch != "feature/add" || !out.PullRequest.Draft {
			t.Fatalf("response = %+v", out)
		}
		if workflow.lastParams.Title != "Add feature" || workflow.lastParams.BaseBranch != "main" {
			t.Fatalf("workflow params = %+v", workflow.lastParams)
		}
	})

	t.Run("branch behind", func(t *testing.T) {
		gitClient := &fakeBranchBehindGit{behindCount: 4, aheadCount: 2}
		handler := NewPRHandler(nil, gitClient)

		body, _ := json.Marshal(map[string]string{
			"worktree":    "/tmp/repo",
			"base_branch": "main",
			"remote":      "origin",
		})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-behind",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandGitBranchBehind,
			Meta:            protocol.Metadata{ProjectID: "proj-pr"},
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("branch-behind response error: %+v", resp.Error)
		}
		var out branchBehindResultBody
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if out.RevRange != "main..origin/main" || out.AheadRevRange != "origin/main..HEAD" || out.CommitsAhead != 2 || !out.Ahead || out.CommitsBehind != 4 || !out.Behind {
			t.Fatalf("response = %+v", out)
		}
		if len(gitClient.branchBehindCalls) != 1 {
			t.Fatalf("branch behind calls = %+v", gitClient.branchBehindCalls)
		}
		call := gitClient.branchBehindCalls[0]
		if call.projectID != "proj-pr" || call.worktree != "/tmp/repo" || call.baseBranch != "main" || call.remote != "origin" {
			t.Fatalf("branch behind call = %+v", call)
		}
	})

	t.Run("branch behind defaults base branch and remote", func(t *testing.T) {
		gitClient := &fakeBranchBehindGit{behindCount: 1, aheadCount: 0}
		handler := NewPRHandler(nil, gitClient)

		body, _ := json.Marshal(map[string]string{
			"worktree": "/tmp/repo",
		})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-behind-defaults",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandGitBranchBehind,
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("branch-behind response error: %+v", resp.Error)
		}
		if len(gitClient.branchBehindCalls) != 1 {
			t.Fatalf("branch behind calls = %+v", gitClient.branchBehindCalls)
		}
		call := gitClient.branchBehindCalls[0]
		if call.baseBranch != "main" || call.remote != "origin" {
			t.Fatalf("branch behind call = %+v, want main/origin defaults", call)
		}
	})
}

func TestPRHandlerResolvesWorkflowFromRequestProject(t *testing.T) {
	workflow := &fakePRWorkflow{}
	var resolvedProject string
	handler := NewProjectPRHandler(nil, func(_ context.Context, projectID string) (PRProjectResources, error) {
		resolvedProject = projectID
		if projectID == "missing-store" {
			return PRProjectResources{Workflow: workflow}, nil
		}
		if projectID != "selected-project" {
			return PRProjectResources{}, fmt.Errorf("unknown project %q", projectID)
		}
		return PRProjectResources{Workflow: workflow, IssueRefs: &fakePRIssueRefStore{}}, nil
	})
	body, _ := json.Marshal(prListCommandBody{State: "all", Limit: 20})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-project-routing",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRList,
		Meta:            protocol.Metadata{ProjectID: "selected-project"},
		Body:            body,
	})
	if !resp.OK {
		t.Fatalf("response error: %+v", resp.Error)
	}
	if resolvedProject != "selected-project" {
		t.Fatalf("resolved project = %q", resolvedProject)
	}

	resp = handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-invalid-project",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRList,
		Meta:            protocol.Metadata{ProjectID: "other-project"},
		Body:            body,
	})
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest || !strings.Contains(resp.Error.Message, "unknown project") {
		t.Fatalf("invalid project response = %+v", resp)
	}

	request := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-missing-store",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRList,
		Meta:            protocol.Metadata{ProjectID: "missing-store"},
		Body:            body,
	}
	resp = handler.Handle(context.Background(), request)
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest || !strings.Contains(resp.Error.Message, "missing repository workflow or issue store") {
		t.Fatalf("missing-store response = %+v", resp)
	}
}

func TestPRHandlerCreatePersistsExternalRefInRequestProjectOnly(t *testing.T) {
	workflows := map[string]*fakePRWorkflow{
		"startup-project":  {},
		"selected-project": {},
	}
	stores := map[string]*fakePRIssueRefStore{
		"startup-project":  {},
		"selected-project": {},
	}
	handler := NewProjectPRHandler(nil, func(_ context.Context, projectID string) (PRProjectResources, error) {
		workflow, workflowOK := workflows[projectID]
		store, storeOK := stores[projectID]
		if !workflowOK || !storeOK {
			return PRProjectResources{}, fmt.Errorf("unknown project %q", projectID)
		}
		return PRProjectResources{Workflow: workflow, IssueRefs: store}, nil
	})
	body, _ := json.Marshal(prCreateCommandBody{
		Title: "Selected change", Body: "Body", Branch: "feature/selected",
		BaseBranch: "release", IssueID: "az-selected",
	})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-selected-create",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRCreate,
		Meta:            protocol.Metadata{ProjectID: "selected-project"},
		Body:            body,
	})
	if !resp.OK {
		t.Fatalf("create response error: %+v", resp.Error)
	}
	if len(stores["startup-project"].params) != 0 {
		t.Fatalf("startup project external refs = %+v, want none", stores["startup-project"].params)
	}
	if len(stores["selected-project"].params) != 1 || stores["selected-project"].params[0].IssueID != "az-selected" {
		t.Fatalf("selected project external refs = %+v", stores["selected-project"].params)
	}
	if workflows["startup-project"].lastParams.IssueID != "" || workflows["selected-project"].lastParams.IssueID != "az-selected" {
		t.Fatalf("workflow create projects startup=%+v selected=%+v", workflows["startup-project"].lastParams, workflows["selected-project"].lastParams)
	}
}

func TestPRHandlerCreatePersistsGitHubExternalRef(t *testing.T) {
	workflow := &fakePRWorkflow{}
	refs := &fakePRIssueRefStore{}
	handler := NewPRHandler(workflow, nil, refs)

	body, _ := json.Marshal(map[string]any{
		"title":       "Add feature",
		"body":        "Body",
		"branch":      "feature/add",
		"base_branch": "main",
		"draft":       true,
		"issue_id":    "az-1",
	})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-pr",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRCreate,
		Body:            body,
	})
	if !resp.OK {
		t.Fatalf("create response error: %+v", resp.Error)
	}
	if len(refs.params) != 1 {
		t.Fatalf("external ref writes = %+v", refs.params)
	}
	got := refs.params[0]
	if got.IssueID != "az-1" || got.Provider != "github" || got.RemoteKey != "7" || got.DisplayKey != "#7" {
		t.Fatalf("external ref params = %+v", got)
	}
	if got.Metadata["state"] != "open" || got.Metadata["draft"] != "true" {
		t.Fatalf("external ref metadata = %+v", got.Metadata)
	}
}

func TestPRHandlerCreateReportsExternalRefPersistenceFailure(t *testing.T) {
	workflow := &fakePRWorkflow{}
	refs := &fakePRIssueRefStore{err: errors.New("write failed")}
	handler := NewPRHandler(workflow, nil, refs)
	body, _ := json.Marshal(prCreateCommandBody{Title: "Add feature", Body: "Body", Branch: "feature/add", BaseBranch: "main", IssueID: "az-1"})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-pr-ref-failure",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRCreate,
		Body:            body,
	})
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInternal || !strings.Contains(resp.Error.Message, "persist GitHub pull request reference") {
		t.Fatalf("persistence failure response = %+v", resp)
	}
	if workflow.lastParams.IssueID != "az-1" || len(refs.params) != 1 {
		t.Fatalf("create/persistence calls workflow=%+v refs=%+v", workflow.lastParams, refs.params)
	}
}

func TestPRHandlerRejectsInvalidBodyAndUnsupportedCommand(t *testing.T) {
	handler := NewPRHandler(&fakePRWorkflow{}, &fakeBranchBehindGit{})

	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-bad",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRCreate,
		Body:            []byte(`{"title":"x"}`),
	})
	if resp.OK {
		t.Fatal("expected invalid request error")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("error = %+v", resp.Error)
	}

	resp = handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-unsupported",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "pr.unknown",
	})
	if resp.OK {
		t.Fatal("expected unsupported command error")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("error = %+v", resp.Error)
	}
}

func TestPRHandlerGetChecksOpenAndMerge(t *testing.T) {
	t.Run("get", func(t *testing.T) {
		workflow := &fakePRWorkflow{}
		handler := NewPRHandler(workflow, nil)
		body, _ := json.Marshal(map[string]string{"branch": "feature/add"})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-pr-get",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandPRGet,
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("get response error: %+v", resp.Error)
		}
		var out prGetResultBody
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if out.PullRequest.Branch != "feature/add" || workflow.lastBranch != "feature/add" {
			t.Fatalf("get result = %+v workflow branch=%q", out, workflow.lastBranch)
		}
	})

	t.Run("list", func(t *testing.T) {
		workflow := &fakePRWorkflow{}
		handler := NewPRHandler(workflow, nil)
		body, _ := json.Marshal(map[string]any{"state": "all", "limit": 12})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-pr-list",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandPRList,
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("list response error: %+v", resp.Error)
		}
		var out prListResultBody
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if out.State != "all" || len(out.PullRequests) != 1 || workflow.lastList.State != "all" || workflow.lastList.Limit != 12 {
			t.Fatalf("list result = %+v workflow params=%+v", out, workflow.lastList)
		}
	})

	t.Run("checks", func(t *testing.T) {
		workflow := &fakePRWorkflow{checks: []pr.CheckInfo{{Name: "unit", Bucket: "pass"}, {Name: "lint", Bucket: "pending"}}}
		handler := NewPRHandler(workflow, nil)
		body, _ := json.Marshal(map[string]string{"ref": "feature/add"})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-pr-checks",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandPRChecks,
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("checks response error: %+v", resp.Error)
		}
		var out prChecksResultBody
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if out.ChecksStatus != "pending" || workflow.lastRef != "feature/add" {
			t.Fatalf("checks result = %+v workflow ref=%q", out, workflow.lastRef)
		}
	})

	t.Run("open", func(t *testing.T) {
		workflow := &fakePRWorkflow{}
		handler := NewPRHandler(workflow, nil)
		body, _ := json.Marshal(map[string]string{"branch": "feature/add"})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-pr-open",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandPROpen,
			Body:            body,
		})
		if !resp.OK || workflow.lastBranch != "feature/add" {
			t.Fatalf("open response error=%+v workflow branch=%q", resp.Error, workflow.lastBranch)
		}
	})

	t.Run("merge resolves branch", func(t *testing.T) {
		workflow := &fakePRWorkflow{result: &pr.PRInfo{Number: 88, Branch: "feature/add"}}
		handler := NewPRHandler(workflow, nil)
		body, _ := json.Marshal(map[string]string{"branch": "feature/add", "strategy": "rebase"})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-pr-merge",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandPRMerge,
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("merge response error: %+v", resp.Error)
		}
		if workflow.lastNumber != 88 || workflow.lastStrategy != "rebase" {
			t.Fatalf("merge call number=%d strategy=%q", workflow.lastNumber, workflow.lastStrategy)
		}
	})
}

func TestPRHandlerMapsServiceErrors(t *testing.T) {
	handler := NewPRHandler(&fakePRWorkflow{err: errors.New("boom")}, &fakeBranchBehindGit{err: errors.New("boom")})

	createBody, _ := json.Marshal(map[string]any{
		"title":       "Add feature",
		"body":        "Body",
		"branch":      "feature/add",
		"base_branch": "main",
		"draft":       false,
		"issue_id":    "az-1",
	})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-create-err",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandPRCreate,
		Body:            createBody,
	})
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInternal {
		t.Fatalf("create error = %+v", resp.Error)
	}
}
