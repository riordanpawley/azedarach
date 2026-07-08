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
				AheadRevRange: "origin/main..HEAD",
				CommitsAhead:  1,
				Ahead:         true,
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
	if !out.Behind || out.CommitsBehind != 2 || !out.Ahead || out.CommitsAhead != 1 || out.RevRange != "main..origin/main" || out.AheadRevRange != "origin/main..HEAD" {
		t.Fatalf("result = %+v", out)
	}
	if transport.lastReq.Command != CommandGitBranchBehind {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandGitBranchBehind)
	}
	if transport.lastReq.Meta.ProjectID != "proj-a" {
		t.Fatalf("project_id = %q, want proj-a", transport.lastReq.Meta.ProjectID)
	}
}

func TestPullRequestCommandsRouteAndDecodeResponses(t *testing.T) {
	tests := []struct {
		name    string
		command string
		call    func(*Client) error
	}{
		{
			name:    "get",
			command: CommandPRGet,
			call: func(client *Client) error {
				out, err := client.GetPullRequest(context.Background(), PullRequestBranchParams{Branch: "feature/add"})
				if err != nil {
					return err
				}
				if out.PullRequest.Number != 12 {
					t.Fatalf("get result = %+v", out)
				}
				return nil
			},
		},
		{
			name:    "checks",
			command: CommandPRChecks,
			call: func(client *Client) error {
				out, err := client.GetPullRequestChecks(context.Background(), PullRequestChecksParams{Ref: "feature/add"})
				if err != nil {
					return err
				}
				if out.ChecksStatus != "pass" || len(out.Checks) != 1 {
					t.Fatalf("checks result = %+v", out)
				}
				return nil
			},
		},
		{
			name:    "open",
			command: CommandPROpen,
			call: func(client *Client) error {
				return client.OpenPullRequest(context.Background(), PullRequestBranchParams{Branch: "feature/add"})
			},
		},
		{
			name:    "merge",
			command: CommandPRMerge,
			call: func(client *Client) error {
				out, err := client.MergePullRequest(context.Background(), PullRequestMergeParams{Branch: "feature/add", Strategy: "squash"})
				if err != nil {
					return err
				}
				if out.Number != 12 || out.Strategy != "squash" {
					t.Fatalf("merge result = %+v", out)
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &prRecordingTransport{
				replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					if req.Command != tt.command {
						t.Fatalf("command = %q, want %q", req.Command, tt.command)
					}
					body, err := pullRequestTestResponseBody(tt.command)
					if err != nil {
						t.Fatal(err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            body,
					}, nil
				},
			}
			client := New(transport).WithProjectID("proj-a")
			if err := tt.call(client); err != nil {
				t.Fatalf("call error: %v", err)
			}
			if transport.lastReq.Meta.ProjectID != "proj-a" {
				t.Fatalf("project_id = %q, want proj-a", transport.lastReq.Meta.ProjectID)
			}
		})
	}
}

func pullRequestTestResponseBody(command string) ([]byte, error) {
	switch command {
	case CommandPRGet:
		return json.Marshal(PullRequestGetResult{PullRequest: pr.PRInfo{Number: 12, Title: "PR", URL: "https://example.test/pr/12", State: "open", Branch: "feature/add", BaseRef: "main"}})
	case CommandPRChecks:
		return json.Marshal(PullRequestChecksResult{Ref: "feature/add", ChecksStatus: "pass", Checks: []pr.CheckInfo{{Name: "unit", Bucket: "pass"}}})
	case CommandPROpen:
		return json.Marshal(map[string]string{"branch": "feature/add"})
	case CommandPRMerge:
		return json.Marshal(PullRequestMergeResult{Number: 12, Strategy: "squash"})
	default:
		return nil, nil
	}
}
