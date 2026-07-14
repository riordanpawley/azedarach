package daemonclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestTypedOrchestratorSessionCommands(t *testing.T) {
	tests := []struct {
		name    string
		command string
		call    func(*Client, protocol.OrchestratorSessionRequest) (protocol.OrchestratorSessionResult, error)
	}{
		{"start", protocol.CommandOrchestratorSessionStart, func(c *Client, r protocol.OrchestratorSessionRequest) (protocol.OrchestratorSessionResult, error) {
			return c.StartOrchestratorSession(context.Background(), r)
		}},
		{"attach", protocol.CommandOrchestratorSessionAttach, func(c *Client, r protocol.OrchestratorSessionRequest) (protocol.OrchestratorSessionResult, error) {
			return c.AttachOrchestratorSession(context.Background(), r)
		}},
		{"status", protocol.CommandOrchestratorSessionStatus, func(c *Client, r protocol.OrchestratorSessionRequest) (protocol.OrchestratorSessionResult, error) {
			return c.OrchestratorSessionStatus(context.Background(), r)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &fakeTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != tt.command {
					t.Fatalf("command = %q, want %q", req.Command, tt.command)
				}
				var body protocol.OrchestratorSessionRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatal(err)
				}
				if body.Scope.Kind != domain.OrchestrationScopeProject {
					t.Fatalf("body = %+v", body)
				}
				encoded, _ := json.Marshal(protocol.OrchestratorSessionResult{Scope: body.Scope, SessionID: "az-orchestrator-project", Live: true})
				return protocol.ResponseEnvelope{OK: true, Body: encoded}, nil
			}}
			result, err := tt.call(New(transport), protocol.OrchestratorSessionRequest{Scope: domain.ProjectOrchestrationScope()})
			if err != nil {
				t.Fatal(err)
			}
			if result.SessionID != "az-orchestrator-project" || !result.Live {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}
