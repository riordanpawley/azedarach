package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
)

type lifecycleRecordingTransport struct {
	lastReq                   protocol.RequestEnvelope
	subscribeCalls            int
	lastSubscribeProjectID    string
	lastSubscribeFromRevision uint64
	replyFn                   func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	subscribeFn               func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error)
}

func (t *lifecycleRecordingTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *lifecycleRecordingTransport) Command(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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

func (t *lifecycleRecordingTransport) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	t.subscribeCalls++
	t.lastSubscribeProjectID = projectID
	t.lastSubscribeFromRevision = fromRevision
	if t.subscribeFn != nil {
		return t.subscribeFn(ctx, projectID, fromRevision)
	}
	return nil, nil
}

func TestSessionLifecycleCommandsRouteThroughDaemon(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body, err := json.Marshal(commandOutputBody{Output: "ok"})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
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
	got, err := client.StartSession(context.Background(), "az-1", "main")
	if err != nil {
		t.Fatalf("StartSession error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("StartSession output = %q, want ok", got)
	}
	if transport.lastReq.Command != CommandSessionStart {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandSessionStart)
	}
	if transport.lastReq.Meta.ProjectID != "proj-a" {
		t.Fatalf("project_id = %q, want proj-a", transport.lastReq.Meta.ProjectID)
	}

	got, err = client.StopSession(context.Background(), "az-1")
	if err != nil {
		t.Fatalf("StopSession error: %v", err)
	}
	if got != "ok" {
		t.Fatalf("StopSession output = %q, want ok", got)
	}
	if transport.lastReq.Command != CommandSessionStop {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandSessionStop)
	}
}

func TestSessionAttachAndStatusCommandsRouteThroughDaemon(t *testing.T) {
	tests := []struct {
		name        string
		wantCommand string
		sessionID   string
		output      string
	}{
		{
			name:        "attach",
			wantCommand: CommandSessionAttach,
			sessionID:   "az-1",
			output:      "attached",
		},
		{
			name:        "status",
			wantCommand: CommandSessionStatus,
			sessionID:   "",
			output:      "Active Sessions (1)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			transport := &lifecycleRecordingTransport{
				replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					gotReq = req
					body, err := json.Marshal(commandOutputBody{Output: tt.output})
					if err != nil {
						t.Fatalf("marshal response: %v", err)
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

			var (
				got string
				err error
			)
			switch tt.wantCommand {
			case CommandSessionAttach:
				got, err = client.AttachSession(context.Background(), tt.sessionID)
			case CommandSessionStatus:
				got, err = client.SessionStatus(context.Background(), tt.sessionID)
			default:
				t.Fatalf("unexpected command %q", tt.wantCommand)
			}
			if err != nil {
				t.Fatalf("%s error: %v", tt.name, err)
			}
			if got != tt.output {
				t.Fatalf("output = %q, want %q", got, tt.output)
			}
			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			if gotReq.Meta.ProjectID != "proj-a" {
				t.Fatalf("project_id = %q, want proj-a", gotReq.Meta.ProjectID)
			}
			var body sessionCommandBody
			if err := json.Unmarshal(gotReq.Body, &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if body.ProjectID != "proj-a" {
				t.Fatalf("body project_id = %q, want proj-a", body.ProjectID)
			}
			if body.SessionID != tt.sessionID {
				t.Fatalf("body session_id = %q, want %q", body.SessionID, tt.sessionID)
			}
		})
	}
}

func TestSessionLifecycleCommandsDecodeNestedOperationResult(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			nested, err := json.Marshal(commandOutputBody{Output: "wrapped"})
			if err != nil {
				t.Fatalf("marshal nested response: %v", err)
			}
			body, err := json.Marshal(map[string]any{
				"operation_id": "op-123",
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

	client := New(transport).WithProjectID("proj-a")
	got, err := client.StartSession(context.Background(), "az-1", "main")
	if err != nil {
		t.Fatalf("StartSession error: %v", err)
	}
	if got != "wrapped" {
		t.Fatalf("StartSession output = %q, want wrapped", got)
	}
}

func TestSessionLifecycleCommandsReturnPendingOperationError(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body, err := json.Marshal(map[string]any{
				"operation_id": "op-123",
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

	client := New(transport).WithProjectID("proj-a")
	_, err := client.StartSession(context.Background(), "az-1", "main")
	var pending *OperationPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("StartSession error = %v, want OperationPendingError", err)
	}
	if pending.OperationID != "op-123" {
		t.Fatalf("operation id = %q, want op-123", pending.OperationID)
	}
	if pending.State != protocol.OperationStateRunning {
		t.Fatalf("state = %q, want running", pending.State)
	}
}

func TestDevServerLifecycleHelpers(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body, err := json.Marshal(devServerResultBody{
				IssueID: "az-1",
				Server: devserver.Server{
					ID:      "az-1",
					Name:    "az-1",
					Port:    3001,
					Status:  "running",
					IssueID: "az-1",
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
				Body:            body,
			}, nil
		},
	}

	client := New(transport).WithProjectID("proj-a")
	if _, err := client.StartDevServer(context.Background(), "az-1"); err != nil {
		t.Fatalf("StartDevServer error: %v", err)
	}
	if transport.lastReq.Command != CommandDevServerStart {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandDevServerStart)
	}

	if _, err := client.StopDevServer(context.Background(), "az-1"); err != nil {
		t.Fatalf("StopDevServer error: %v", err)
	}
	if transport.lastReq.Command != CommandDevServerStop {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandDevServerStop)
	}
}

func TestListWorktreesUsesProjectRoute(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body, err := json.Marshal(worktreeListBody{
				ProjectID: "proj-a",
				Worktrees: []worktreePayload{
					{Path: "/tmp/az-1", Branch: "az/az-1", IssueID: "az-1"},
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
				Body:            body,
			}, nil
		},
	}

	client := New(transport).WithProjectID("proj-a")
	worktrees, err := client.ListWorktrees(context.Background())
	if err != nil {
		t.Fatalf("ListWorktrees error: %v", err)
	}
	if transport.lastReq.Command != CommandWorktreeList {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandWorktreeList)
	}
	if len(worktrees) != 1 || worktrees[0].IssueID != "az-1" {
		t.Fatalf("worktrees = %+v", worktrees)
	}
}

func TestRemoveWorktreeUsesProjectRoute(t *testing.T) {
	transport := &lifecycleRecordingTransport{}
	client := New(transport).WithProjectID("proj-a")

	if err := client.RemoveWorktree(context.Background(), "az-1"); err != nil {
		t.Fatalf("RemoveWorktree error: %v", err)
	}
	if transport.lastReq.Command != CommandWorktreeRemove {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandWorktreeRemove)
	}
	if transport.lastReq.Meta.ProjectID != "proj-a" {
		t.Fatalf("project_id = %q, want proj-a", transport.lastReq.Meta.ProjectID)
	}

	var body worktreeCommandBody
	if err := json.Unmarshal(transport.lastReq.Body, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if body.ProjectID != "proj-a" || body.IssueID != "az-1" {
		t.Fatalf("request body = %+v, want project_id=proj-a issue_id=az-1", body)
	}
}

func TestCleanupOrphanedWorktreesRoutesAndDecodesResponse(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body, err := json.Marshal(protocol.CleanupOrphanedResponseBody{
				ProjectID:        "proj-a",
				WorktreesRemoved: 2,
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
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
	removed, err := client.CleanupOrphanedWorktrees(context.Background())
	if err != nil {
		t.Fatalf("CleanupOrphanedWorktrees error: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if transport.lastReq.Command != protocol.CommandWorktreeCleanupOrphaned {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, protocol.CommandWorktreeCleanupOrphaned)
	}
	if transport.lastReq.Meta.ProjectID != "proj-a" {
		t.Fatalf("project_id = %q, want proj-a", transport.lastReq.Meta.ProjectID)
	}

	var body protocol.CleanupOrphanedRequestBody
	if err := json.Unmarshal(transport.lastReq.Body, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if body.ProjectID != "proj-a" {
		t.Fatalf("request project_id = %q, want proj-a", body.ProjectID)
	}
}

func TestCleanupOrphanedWorktreesDecodesNestedOperationResult(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			nested, err := json.Marshal(protocol.CleanupOrphanedResponseBody{
				ProjectID:        "proj-a",
				WorktreesRemoved: 3,
			})
			if err != nil {
				t.Fatalf("marshal nested response: %v", err)
			}
			body, err := json.Marshal(map[string]any{
				"operation_id": "op-cleanup",
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

	client := New(transport).WithProjectID("proj-a")
	removed, err := client.CleanupOrphanedWorktrees(context.Background())
	if err != nil {
		t.Fatalf("CleanupOrphanedWorktrees error: %v", err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
}

func TestCleanupOrphanedWorktreesReturnsPendingOperationError(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body, err := json.Marshal(map[string]any{
				"operation_id": "op-cleanup",
				"state":        string(protocol.OperationStateQueued),
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

	client := New(transport).WithProjectID("proj-a")
	_, err := client.CleanupOrphanedWorktrees(context.Background())
	var pending *OperationPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("CleanupOrphanedWorktrees error = %v, want OperationPendingError", err)
	}
	if pending.OperationID != "op-cleanup" {
		t.Fatalf("operation id = %q, want op-cleanup", pending.OperationID)
	}
	if pending.State != protocol.OperationStateQueued {
		t.Fatalf("state = %q, want queued", pending.State)
	}
}

func TestSubscribeRetriesUseProjectFallbackAndFromRevision(t *testing.T) {
	transport := &lifecycleRecordingTransport{}
	transport.subscribeFn = func(_ context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
		if transport.subscribeCalls < 3 {
			return nil, errors.New("not ready")
		}
		ch := make(chan protocol.EventEnvelope, 1)
		ch <- protocol.EventEnvelope{
			Revision: 23,
			Event:    "daemon.event.publish",
			Kind:     protocol.EnvelopeKindEvent,
		}
		return ch, nil
	}

	client := New(transport).WithProjectID("proj-a").WithReconnectPolicy(reconnect.Policy{
		MaxAttempts: 5,
		BaseBackoff: 0,
		MaxBackoff:  0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch, err := client.Subscribe(ctx, "", 17)
	if err != nil {
		t.Fatalf("Subscribe error: %v", err)
	}

	evt := <-ch
	if evt.Revision != 23 {
		t.Fatalf("event revision = %d, want 23", evt.Revision)
	}
	if transport.subscribeCalls != 3 {
		t.Fatalf("subscribe calls = %d, want 3", transport.subscribeCalls)
	}
	if transport.lastSubscribeProjectID != "proj-a" {
		t.Fatalf("subscribe project_id = %q, want proj-a", transport.lastSubscribeProjectID)
	}
	if transport.lastSubscribeFromRevision != 17 {
		t.Fatalf("subscribe from_revision = %d, want 17", transport.lastSubscribeFromRevision)
	}
}
