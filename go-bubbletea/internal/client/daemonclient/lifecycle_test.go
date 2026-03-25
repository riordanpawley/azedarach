package daemonclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
)

type lifecycleRecordingTransport struct {
	lastReq protocol.RequestEnvelope
	replyFn func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
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

func (t *lifecycleRecordingTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
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
