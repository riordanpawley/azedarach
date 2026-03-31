package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
)

type fakeDevServerManager struct {
	getFn   func(issueID string) (*devserver.Server, bool)
	startFn func(ctx context.Context, issueID, name, command string) (*devserver.Server, error)
	stopFn  func(ctx context.Context, issueID string) error
	listFn  func() []*devserver.Server
}

func (m fakeDevServerManager) Start(ctx context.Context, issueID, name, command string) (*devserver.Server, error) {
	if m.startFn != nil {
		return m.startFn(ctx, issueID, name, command)
	}
	return nil, nil
}

func (m fakeDevServerManager) Stop(ctx context.Context, issueID string) error {
	if m.stopFn != nil {
		return m.stopFn(ctx, issueID)
	}
	return nil
}

func (m fakeDevServerManager) Get(issueID string) (*devserver.Server, bool) {
	if m.getFn != nil {
		return m.getFn(issueID)
	}
	return nil, false
}

func (m fakeDevServerManager) List() []*devserver.Server {
	if m.listFn != nil {
		return m.listFn()
	}
	return nil
}

func TestDevServerHandlerStartStopStatusFlow(t *testing.T) {
	var current *devserver.Server
	manager := &fakeDevServerManager{}
	handler := NewDevServerHandler(manager)
	ctx := context.Background()

	startedAt := time.Date(2026, time.March, 24, 13, 0, 0, 0, time.UTC)
	manager.startFn = func(context.Context, string, string, string) (*devserver.Server, error) {
		current = &devserver.Server{
			ID:        "issue-1",
			Name:      "issue-1",
			Port:      3001,
			Status:    "running",
			Command:   "bun run dev",
			StartedAt: startedAt,
		}
		return current, nil
	}
	manager.getFn = func(issueID string) (*devserver.Server, bool) {
		if issueID != "issue-1" || current == nil {
			return nil, false
		}
		return current, true
	}

	req := func(command string) protocol.RequestEnvelope {
		body, _ := json.Marshal(map[string]string{"issue_id": "issue-1"})
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-" + command,
			Kind:            protocol.EnvelopeKindCommand,
			Command:         command,
			Body:            body,
		}
	}

	startResp := handler.Handle(ctx, req(CommandDevServerStart))
	if !startResp.OK {
		t.Fatalf("start response = %+v", startResp)
	}
	var startBody devServerResultBody
	if err := json.Unmarshal(startResp.Body, &startBody); err != nil {
		t.Fatalf("unmarshal start response: %v", err)
	}
	if startBody.IssueID != "issue-1" || startBody.Server.Status != "running" {
		t.Fatalf("start body = %+v", startBody)
	}

	statusResp := handler.Handle(ctx, req(CommandDevServerStatus))
	if !statusResp.OK {
		t.Fatalf("status response = %+v", statusResp)
	}
	var statusBody devServerResultBody
	if err := json.Unmarshal(statusResp.Body, &statusBody); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if statusBody.Server.Status != "running" {
		t.Fatalf("status body = %+v", statusBody)
	}

	manager.stopFn = func(context.Context, string) error {
		if current == nil {
			t.Fatal("stop called before start")
		}
		current.Status = "stopped"
		current.Uptime = 5 * time.Second
		return nil
	}
	stopResp := handler.Handle(ctx, req(CommandDevServerStop))
	if !stopResp.OK {
		t.Fatalf("stop response = %+v", stopResp)
	}
	var stopBody devServerResultBody
	if err := json.Unmarshal(stopResp.Body, &stopBody); err != nil {
		t.Fatalf("unmarshal stop response: %v", err)
	}
	if stopBody.Server.Status != "stopped" {
		t.Fatalf("stop body = %+v", stopBody)
	}
	if stopBody.Server.Uptime != 5*time.Second {
		t.Fatalf("stop uptime = %s, want 5s", stopBody.Server.Uptime)
	}
}

func TestDevServerHandlerListReturnsAllServers(t *testing.T) {
	handler := NewDevServerHandler(fakeDevServerManager{
		listFn: func() []*devserver.Server {
			return []*devserver.Server{
				{
					ID:      "issue-1",
					Name:    "issue-1",
					Port:    3001,
					Status:  "running",
					Command: "bun run dev",
				},
				{
					ID:      "issue-2",
					Name:    "issue-2",
					Port:    3002,
					Status:  "stopped",
					Command: "bun run dev",
				},
			}
		},
	})

	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandDevServerList,
		Body:            mustJSON(t, map[string]string{}),
	})
	if !resp.OK {
		t.Fatalf("list response = %+v", resp)
	}

	var body devServerListBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(body.Servers) != 2 {
		t.Fatalf("servers = %+v", body.Servers)
	}
	if body.Servers[0].Status != "running" || body.Servers[1].Status != "stopped" {
		t.Fatalf("servers = %+v", body.Servers)
	}
}

func TestDevServerHandlerInvalidStateAndFailureMappings(t *testing.T) {
	t.Run("start when already running", func(t *testing.T) {
		current := &devserver.Server{ID: "issue-1", Name: "issue-1", Status: "running"}
		handler := NewDevServerHandler(fakeDevServerManager{
			getFn: func(issueID string) (*devserver.Server, bool) {
				return current, issueID == "issue-1"
			},
		})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-start",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandDevServerStart,
			Body:            mustJSON(t, map[string]string{"issue_id": "issue-1"}),
		})
		if resp.OK {
			t.Fatalf("expected conflict, got %+v", resp)
		}
		if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeConflict {
			t.Fatalf("error mapping = %+v", resp.Error)
		}
	})

	t.Run("stop missing server", func(t *testing.T) {
		handler := NewDevServerHandler(fakeDevServerManager{})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-stop",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandDevServerStop,
			Body:            mustJSON(t, map[string]string{"issue_id": "issue-2"}),
		})
		if resp.OK {
			t.Fatalf("expected invalid request, got %+v", resp)
		}
		if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
			t.Fatalf("error mapping = %+v", resp.Error)
		}
	})

	t.Run("backend failure maps to internal", func(t *testing.T) {
		handler := NewDevServerHandler(fakeDevServerManager{
			startFn: func(context.Context, string, string, string) (*devserver.Server, error) {
				return nil, errors.New("boom")
			},
		})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-fail",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandDevServerStart,
			Body:            mustJSON(t, map[string]string{"issue_id": "issue-3"}),
		})
		if resp.OK {
			t.Fatalf("expected failure, got %+v", resp)
		}
		if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInternal {
			t.Fatalf("error mapping = %+v", resp.Error)
		}
	})

	t.Run("deadline exceeded maps to timeout", func(t *testing.T) {
		handler := NewDevServerHandler(fakeDevServerManager{
			startFn: func(context.Context, string, string, string) (*devserver.Server, error) {
				return nil, context.DeadlineExceeded
			},
		})
		resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-timeout",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandDevServerStart,
			Body:            mustJSON(t, map[string]string{"issue_id": "issue-4"}),
		})
		if resp.OK {
			t.Fatalf("expected timeout, got %+v", resp)
		}
		if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeTimeout || !resp.Error.Retryable {
			t.Fatalf("error mapping = %+v", resp.Error)
		}
	})
}

func TestDevServerHandlerUnsupportedCommand(t *testing.T) {
	handler := NewDevServerHandler(fakeDevServerManager{})
	resp := handler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-x",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "devserver.restart",
		Body:            mustJSON(t, map[string]string{"issue_id": "issue-1"}),
	})
	if resp.OK {
		t.Fatalf("expected unsupported command error")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("error mapping = %+v", resp.Error)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	return body
}
