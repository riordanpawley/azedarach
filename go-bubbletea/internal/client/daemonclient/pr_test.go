package daemonclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/pr"
)

type prRecordingTransport struct {
	lastReq protocol.RequestEnvelope
	replyFn func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
}

func (t *prRecordingTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *prRecordingTransport) Command(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	t.lastReq = req
	if t.replyFn != nil {
		return t.replyFn(req)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
	}, nil
}

func (t *prRecordingTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, nil
}

func TestCreatePullRequestRoutesAndDecodesResponse(t *testing.T) {
	transport := &prRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != CommandPRCreate {
				t.Fatalf("command = %q, want %q", req.Command, CommandPRCreate)
			}
			var body CreatePullRequestParams
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if body.Title != "Add feature" || !body.Draft || body.IssueID != "az-1" {
				t.Fatalf("request body = %+v", body)
			}
			respBody, err := json.Marshal(CreatePullRequestResult{
				IssueID: "az-1",
				PullRequest: pr.PRInfo{
					Number:  12,
					Title:   body.Title,
					URL:     "https://github.com/example/repo/pull/12",
					State:   "open",
					Draft:   body.Draft,
					Branch:  body.Branch,
					BaseRef: body.BaseBranch,
				},
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}

	client := New(transport).WithProjectID("proj-a")
	out, err := client.CreatePullRequest(context.Background(), CreatePullRequestParams{
		Title:      "Add feature",
		Body:       "Body",
		Branch:     "feature/add",
		BaseBranch: "main",
		Draft:      true,
		IssueID:    "az-1",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest error: %v", err)
	}
	if out.IssueID != "az-1" || out.PullRequest.Number != 12 {
		t.Fatalf("result = %+v", out)
	}
	if transport.lastReq.Command != CommandPRCreate {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandPRCreate)
	}
	if transport.lastReq.Meta.ProjectID != "proj-a" {
		t.Fatalf("project_id = %q, want proj-a", transport.lastReq.Meta.ProjectID)
	}
}

func TestCheckBranchBehindRoutesAndDecodesResponse(t *testing.T) {
	transport := &prRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != CommandGitBranchBehind {
				t.Fatalf("command = %q, want %q", req.Command, CommandGitBranchBehind)
			}
			var body BranchBehindCheckParams
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if body.Worktree != "/tmp/repo" || body.BaseBranch != "main" || body.Remote != "origin" {
				t.Fatalf("request body = %+v", body)
			}
			respBody, err := json.Marshal(BranchBehindCheckResult{
				Worktree:      body.Worktree,
				BaseBranch:    body.BaseBranch,
				Remote:        body.Remote,
				RevRange:      "main..origin/main",
				CommitsBehind: 2,
				Behind:        true,
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}

	client := New(transport).WithProjectID("proj-a")
	out, err := client.CheckBranchBehind(context.Background(), BranchBehindCheckParams{
		Worktree:   "/tmp/repo",
		BaseBranch: "main",
		Remote:     "origin",
	})
	if err != nil {
		t.Fatalf("CheckBranchBehind error: %v", err)
	}
	if !out.Behind || out.CommitsBehind != 2 || out.RevRange != "main..origin/main" {
		t.Fatalf("result = %+v", out)
	}
	if transport.lastReq.Command != CommandGitBranchBehind {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandGitBranchBehind)
	}
	if transport.lastReq.Meta.ProjectID != "proj-a" {
		t.Fatalf("project_id = %q, want proj-a", transport.lastReq.Meta.ProjectID)
	}
}
