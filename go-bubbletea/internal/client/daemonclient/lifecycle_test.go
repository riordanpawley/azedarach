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
	if _, err := client.StartSession(context.Background(), "az-1", "main"); err != nil {
		t.Fatalf("StartSession error: %v", err)
	}
	if transport.lastReq.Command != CommandSessionStart {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandSessionStart)
	}
	if transport.lastReq.Meta.ProjectID != "proj-a" {
		t.Fatalf("project_id = %q, want proj-a", transport.lastReq.Meta.ProjectID)
	}

	if _, err := client.StopSession(context.Background(), "az-1"); err != nil {
		t.Fatalf("StopSession error: %v", err)
	}
	if transport.lastReq.Command != CommandSessionStop {
		t.Fatalf("command = %q, want %q", transport.lastReq.Command, CommandSessionStop)
	}
}

func TestDevServerLifecycleHelpers(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body, err := json.Marshal(devServerResultBody{
				BeadID: "az-1",
				Server: devserver.Server{
					ID:     "az-1",
					Name:   "az-1",
					Port:   3001,
					Status: "running",
					BeadID: "az-1",
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
					{Path: "/tmp/az-1", Branch: "az/az-1", BeadID: "az-1"},
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
	if len(worktrees) != 1 || worktrees[0].BeadID != "az-1" {
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
