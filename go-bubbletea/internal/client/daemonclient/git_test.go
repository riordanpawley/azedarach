package daemonclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type gitRecordingTransport struct {
	lastReq protocol.RequestEnvelope
	replyFn func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
}

func (t *gitRecordingTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *gitRecordingTransport) Command(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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

func (t *gitRecordingTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, nil
}

func TestGitFetchCheckoutAndMergeCommandsRouteThroughDaemon(t *testing.T) {
	const wantProjectID = "proj-git"
	const worktree = "/tmp/az-1"

	t.Run("fetch", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandGitFetch {
					t.Fatalf("command = %q, want %q", req.Command, CommandGitFetch)
				}
				var body GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Worktree != worktree || body.Remote != "origin" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(GitCommandResponse{
					Worktree: worktree,
					Remote:   "origin",
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

		client := New(transport).WithProjectID(wantProjectID)
		resp, err := client.GitFetch(context.Background(), worktree, "origin")
		if err != nil {
			t.Fatalf("GitFetch error: %v", err)
		}
		if resp.Worktree != worktree || resp.Remote != "origin" {
			t.Fatalf("response = %+v", resp)
		}
		if transport.lastReq.Meta.ProjectID != wantProjectID {
			t.Fatalf("project_id = %q, want %q", transport.lastReq.Meta.ProjectID, wantProjectID)
		}
	})

	t.Run("checkout", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandGitCheckout {
					t.Fatalf("command = %q, want %q", req.Command, CommandGitCheckout)
				}
				var body GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Worktree != worktree || body.Branch != "feature/one" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(GitCommandResponse{
					Worktree: worktree,
					Branch:   "feature/one",
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

		client := New(transport).WithProjectID(wantProjectID)
		resp, err := client.GitCheckout(context.Background(), worktree, "feature/one")
		if err != nil {
			t.Fatalf("GitCheckout error: %v", err)
		}
		if resp.Worktree != worktree || resp.Branch != "feature/one" {
			t.Fatalf("response = %+v", resp)
		}
		if transport.lastReq.Meta.ProjectID != wantProjectID {
			t.Fatalf("project_id = %q, want %q", transport.lastReq.Meta.ProjectID, wantProjectID)
		}
	})

	t.Run("merge", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandGitMerge {
					t.Fatalf("command = %q, want %q", req.Command, CommandGitMerge)
				}
				var body GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Worktree != worktree || body.Branch != "main" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(GitMergeCommandResponse{
					Worktree: worktree,
					Branch:   "main",
					Result: git.MergeResult{
						Success:       true,
						HasConflicts:  true,
						ConflictFiles: []string{"README.md"},
						Message:       "merge conflicted",
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

		client := New(transport).WithProjectID(wantProjectID)
		resp, err := client.GitMerge(context.Background(), worktree, "main")
		if err != nil {
			t.Fatalf("GitMerge error: %v", err)
		}
		if resp.Worktree != worktree || resp.Branch != "main" {
			t.Fatalf("response = %+v", resp)
		}
		if !resp.Result.HasConflicts || len(resp.Result.ConflictFiles) != 1 || resp.Result.ConflictFiles[0] != "README.md" {
			t.Fatalf("merge result = %+v", resp.Result)
		}
		if transport.lastReq.Meta.ProjectID != wantProjectID {
			t.Fatalf("project_id = %q, want %q", transport.lastReq.Meta.ProjectID, wantProjectID)
		}
	})
}
