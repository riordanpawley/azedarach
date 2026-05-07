package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
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

	t.Run("diff stat", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandGitDiffStat {
					t.Fatalf("command = %q, want %q", req.Command, CommandGitDiffStat)
				}
				var body GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Worktree != worktree {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(gitOutputBody{
					Output: " README.md | 2 ++\n 1 file changed, 2 insertions(+)",
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
		output, err := client.GitDiffStat(context.Background(), worktree, "main")
		if err != nil {
			t.Fatalf("GitDiffStat error: %v", err)
		}
		if output == "" {
			t.Fatal("expected diff stat output")
		}
		var reqBody GitCommandRequest
		if err := json.Unmarshal(transport.lastReq.Body, &reqBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		if reqBody.BaseBranch != "main" {
			t.Fatalf("base branch = %q, want main", reqBody.BaseBranch)
		}
		if transport.lastReq.Meta.ProjectID != wantProjectID {
			t.Fatalf("project_id = %q, want %q", transport.lastReq.Meta.ProjectID, wantProjectID)
		}
	})

	t.Run("abort merge", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandGitAbortMerge {
					t.Fatalf("command = %q, want %q", req.Command, CommandGitAbortMerge)
				}
				var body GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Worktree != worktree {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(GitCommandResponse{
					Worktree: worktree,
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
		resp, err := client.GitAbortMerge(context.Background(), worktree)
		if err != nil {
			t.Fatalf("GitAbortMerge error: %v", err)
		}
		if resp.Worktree != worktree {
			t.Fatalf("response = %+v", resp)
		}
		if transport.lastReq.Meta.ProjectID != wantProjectID {
			t.Fatalf("project_id = %q, want %q", transport.lastReq.Meta.ProjectID, wantProjectID)
		}
	})
}

func TestGitStatusRefreshCommandSetsRefreshFlag(t *testing.T) {
	const worktree = "/tmp/az-1"
	transport := &gitRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != CommandGitStatus {
				t.Fatalf("command = %q, want %q", req.Command, CommandGitStatus)
			}
			var body GitCommandRequest
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if body.Worktree != worktree || !body.Refresh {
				t.Fatalf("request body = %+v, want worktree with refresh", body)
			}
			respBody, err := json.Marshal(gitStatusBody{
				Status: git.GitStatus{HasChanges: true, Modified: []string{"README.md"}},
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

	status, err := New(transport).GitStatusRefresh(context.Background(), worktree)
	if err != nil {
		t.Fatalf("GitStatusRefresh error: %v", err)
	}
	if !status.HasChanges {
		t.Fatalf("status = %+v, want refreshed dirty status", status)
	}
}

func TestGitCommandsDecodeNestedOperationResult(t *testing.T) {
	const worktree = "/tmp/az-1"
	transport := &gitRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			nested, err := json.Marshal(GitMergeCommandResponse{
				Worktree: worktree,
				Branch:   "main",
				Result: git.MergeResult{
					Success: true,
					Message: "merged",
				},
			})
			if err != nil {
				t.Fatalf("marshal nested response: %v", err)
			}
			body, err := json.Marshal(map[string]any{
				"operation_id": "op-merge",
				"state":        "done",
				"result":       json.RawMessage(nested),
			})
			if err != nil {
				t.Fatalf("marshal wrapped response: %v", err)
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

	client := New(transport).WithProjectID("proj-git")
	resp, err := client.GitMerge(context.Background(), worktree, "main")
	if err != nil {
		t.Fatalf("GitMerge error: %v", err)
	}
	if resp.Worktree != worktree || resp.Branch != "main" || !resp.Result.Success {
		t.Fatalf("response = %+v", resp)
	}
}

func TestGitCommandsReturnPendingOperationError(t *testing.T) {
	const worktree = "/tmp/az-1"
	transport := &gitRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body, err := json.Marshal(map[string]any{
				"operation_id": "op-merge",
				"state":        string(protocol.OperationStateRunning),
			})
			if err != nil {
				t.Fatalf("marshal wrapped response: %v", err)
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

	client := New(transport).WithProjectID("proj-git")
	_, err := client.GitMerge(context.Background(), worktree, "main")
	var pending *OperationPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("GitMerge error = %v, want OperationPendingError", err)
	}
	if pending.OperationID != "op-merge" {
		t.Fatalf("operation id = %q, want op-merge", pending.OperationID)
	}
	if pending.State != protocol.OperationStateRunning {
		t.Fatalf("state = %q, want running", pending.State)
	}
}

func TestGitMergePreflightDiscardAndCheckpointCommandsRouteThroughDaemon(t *testing.T) {
	const wantProjectID = "proj-git"
	const worktree = "/tmp/az-1"

	t.Run("preflight", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandGitMergePreflight {
					t.Fatalf("command = %q, want %q", req.Command, CommandGitMergePreflight)
				}
				var body GitMergePreflightRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.SourceID != "az-1" || body.SourceWorktree != worktree {
					t.Fatalf("request body = %+v", body)
				}
				if body.TargetID != "main" || body.TargetWorktree != "/tmp/main" || body.TargetRef != "main" || body.SourceBranch != "feature/one" {
					t.Fatalf("request body = %+v", body)
				}
				resultBody, err := json.Marshal(GitMergePreflightResponse{
					SourceID:       "az-1",
					SourceWorktree: worktree,
					TargetID:       "main",
					TargetWorktree: "/tmp/main",
					Clean:          false,
					ConflictFiles:  []string{"README.md"},
					Reasons:        []string{"Merge would conflict in 1 files: README.md"},
				})
				if err != nil {
					t.Fatalf("marshal response result: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            resultBody,
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		resp, err := client.GitMergePreflight(context.Background(), "az-1", worktree, "main", "/tmp/main", "main", "feature/one")
		if err != nil {
			t.Fatalf("GitMergePreflight error: %v", err)
		}
		if resp.SourceWorktree != worktree || resp.TargetWorktree != "/tmp/main" || resp.SourceID != "az-1" || resp.TargetID != "main" {
			t.Fatalf("response = %+v", resp)
		}
		if resp.Clean || len(resp.ConflictFiles) != 1 || resp.ConflictFiles[0] != "README.md" {
			t.Fatalf("response = %+v", resp)
		}
		if transport.lastReq.Meta.ProjectID != wantProjectID {
			t.Fatalf("project_id = %q, want %q", transport.lastReq.Meta.ProjectID, wantProjectID)
		}
	})

	t.Run("discard", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandGitDiscard {
					t.Fatalf("command = %q, want %q", req.Command, CommandGitDiscard)
				}
				var body GitDiscardRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Worktree != worktree {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(GitDiscardResponse{Worktree: worktree})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				wrappedBody, err := json.Marshal(map[string]any{
					"operation_id": "op-discard",
					"state":        string(protocol.OperationStateDone),
					"result":       json.RawMessage(respBody),
				})
				if err != nil {
					t.Fatalf("marshal wrapped response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            wrappedBody,
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		resp, err := client.GitDiscardChanges(context.Background(), worktree)
		if err != nil {
			t.Fatalf("GitDiscardChanges error: %v", err)
		}
		if resp.Worktree != worktree {
			t.Fatalf("response = %+v", resp)
		}
		if transport.lastReq.Meta.ProjectID != wantProjectID {
			t.Fatalf("project_id = %q, want %q", transport.lastReq.Meta.ProjectID, wantProjectID)
		}
	})

	t.Run("checkpoint", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandGitCheckpoint {
					t.Fatalf("command = %q, want %q", req.Command, CommandGitCheckpoint)
				}
				var body GitCheckpointRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Worktree != worktree || body.Message != "chore: pre-merge checkpoint" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(GitCheckpointResponse{Worktree: worktree})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				wrappedBody, err := json.Marshal(map[string]any{
					"operation_id": "op-checkpoint",
					"state":        string(protocol.OperationStateDone),
					"result":       json.RawMessage(respBody),
				})
				if err != nil {
					t.Fatalf("marshal wrapped response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            wrappedBody,
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		resp, err := client.GitCheckpointCommit(context.Background(), worktree, "chore: pre-merge checkpoint")
		if err != nil {
			t.Fatalf("GitCheckpointCommit error: %v", err)
		}
		if resp.Worktree != worktree {
			t.Fatalf("response = %+v", resp)
		}
		if transport.lastReq.Meta.ProjectID != wantProjectID {
			t.Fatalf("project_id = %q, want %q", transport.lastReq.Meta.ProjectID, wantProjectID)
		}
	})
}

func TestGitMergePreflightDiscardAndCheckpointPropagatePendingAndErrors(t *testing.T) {
	const worktree = "/tmp/az-1"

	t.Run("preflight error", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:      protocol.ErrorCodeConflict,
						Message:   "preflight blocked",
						Retryable: false,
					},
				}, nil
			},
		}

		_, err := New(transport).GitMergePreflight(context.Background(), "az-1", worktree, "main", "/tmp/main", "main", "feature/one")
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) {
			t.Fatalf("GitMergePreflight error = %v, want CommandError", err)
		}
		if cmdErr.Code != protocol.ErrorCodeConflict || cmdErr.Message != "preflight blocked" {
			t.Fatalf("command error = %+v", cmdErr)
		}
	})

	t.Run("checkpoint error", func(t *testing.T) {
		transport := &gitRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:      protocol.ErrorCodeConflict,
						Message:   "checkpoint blocked",
						Retryable: false,
					},
				}, nil
			},
		}

		_, err := New(transport).GitCheckpointCommit(context.Background(), worktree, "chore: pre-merge checkpoint")
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) {
			t.Fatalf("GitCheckpointCommit error = %v, want CommandError", err)
		}
		if cmdErr.Code != protocol.ErrorCodeConflict || cmdErr.Message != "checkpoint blocked" {
			t.Fatalf("command error = %+v", cmdErr)
		}
	})
}
