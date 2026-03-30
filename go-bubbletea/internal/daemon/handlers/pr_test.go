package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/pr"
)

type fakePRWorkflow struct {
	lastParams pr.CreatePRParams
	result     *pr.PRInfo
	err        error
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

type fakeBranchBehindGit struct {
	fetchCalls    []string
	revRangeCalls []string
	aheadCount    int
	behindCount   int
	err           error
}

func (g *fakeBranchBehindGit) Fetch(_ context.Context, _ string, remote string) error {
	g.fetchCalls = append(g.fetchCalls, remote)
	return g.err
}

func (g *fakeBranchBehindGit) RevListCount(_ context.Context, _ string, revRange string) (int, error) {
	g.revRangeCalls = append(g.revRangeCalls, revRange)
	if g.err != nil {
		return 0, g.err
	}
	if strings.HasSuffix(revRange, "..HEAD") {
		return g.aheadCount, nil
	}
	return g.behindCount, nil
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
		if len(gitClient.fetchCalls) != 1 || gitClient.fetchCalls[0] != "origin" {
			t.Fatalf("fetch calls = %+v", gitClient.fetchCalls)
		}
		if len(gitClient.revRangeCalls) != 2 || gitClient.revRangeCalls[0] != "main..origin/main" || gitClient.revRangeCalls[1] != "origin/main..HEAD" {
			t.Fatalf("rev range calls = %+v", gitClient.revRangeCalls)
		}
	})
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
