package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestRunAgentHookForwardsTerminalCapacityNotificationForDaemonClassification(t *testing.T) {
	t.Setenv("TMUX_PANE", "%42")
	var signal protocol.RuntimeSignalIngestCommandBody
	transport := &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandRuntimeSignalIngest {
				t.Fatalf("command = %q, want runtime signal ingest", req.Command)
			}
			if err := json.Unmarshal(req.Body, &signal); err != nil {
				t.Fatalf("unmarshal runtime signal: %v", err)
			}
			return responseWithJSON(req, protocol.RuntimeSignalIngestResponseBody{Accepted: true}), nil
		},
	}
	deps := &Dependencies{
		DaemonClient: daemonclient.New(transport).WithProjectID("proj-1"),
		ProjectID:    "proj-1",
	}

	_, err := RunAgentHook(context.Background(), deps, AgentHookContext{
		Agent:      AgentCodex,
		Event:      hookEventIdlePrompt,
		IssueID:    "dae",
		ProjectDir: "/worktree/dae",
		Payload: map[string]any{
			"notification": map[string]any{
				"message": "Selected model is at capacity. Please try a different model.",
			},
		},
	})
	if err != nil {
		t.Fatalf("RunAgentHook error: %v", err)
	}
	if signal.Activity != "" || signal.Level != "" || signal.Blocking != nil {
		t.Fatalf("CLI preclassified signal activity/level/blocking = %q/%q/%v", signal.Activity, signal.Level, signal.Blocking)
	}
	notification, ok := signal.Payload["notification"].(map[string]any)
	if !ok || notification["message"] != "Selected model is at capacity. Please try a different model." {
		t.Fatalf("forwarded payload = %#v", signal.Payload)
	}
	if signal.Message != "codex hook: idle_prompt" {
		t.Fatalf("message = %q", signal.Message)
	}
}
