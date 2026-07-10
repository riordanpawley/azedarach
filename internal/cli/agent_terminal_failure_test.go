package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestRunAgentHookClassifiesTerminalCapacityNotification(t *testing.T) {
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
	if signal.Activity != "error" || signal.Level != "error" {
		t.Fatalf("activity/level = %q/%q, want error/error", signal.Activity, signal.Level)
	}
	if signal.Blocking == nil || !*signal.Blocking {
		t.Fatalf("blocking = %v, want true", signal.Blocking)
	}
	if signal.Message != "codex hook: idle_prompt (terminal agent failure: model_capacity)" {
		t.Fatalf("message = %q", signal.Message)
	}
}

func TestClassifyTerminalAgentFailurePreservesOrdinaryIdleNotifications(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload map[string]any
		want    string
	}{
		{name: "capacity", event: hookEventIdlePrompt, payload: map[string]any{"message": "Selected model is at capacity. Please try a different model."}, want: "model_capacity"},
		{name: "wrapped capacity", event: hookEventIdlePrompt, payload: map[string]any{"message": "Selected model is at\ncapacity. Please try a different model."}, want: "model_capacity"},
		{name: "usage limit", event: hookEventIdlePrompt, payload: map[string]any{"message": "Usage limit has been reached"}, want: "usage_limit"},
		{name: "ordinary idle", event: hookEventIdlePrompt, payload: map[string]any{"message": "Codex is waiting for input"}},
		{name: "non idle payload", event: hookEventUserPromptSubmit, payload: map[string]any{"message": "Selected model is at capacity"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := classifyTerminalAgentFailure(tt.event, tt.payload)
			if ok != (tt.want != "") || got != tt.want {
				t.Fatalf("classification = %q/%t, want %q/%t", got, ok, tt.want, tt.want != "")
			}
		})
	}
}

func TestClassifyTerminalAgentFailureBoundsPayloadTraversal(t *testing.T) {
	payload := map[string]any{"message": "ordinary notification"}
	payload["self"] = payload

	if reason, ok := classifyTerminalAgentFailure(hookEventIdlePrompt, payload); ok {
		t.Fatalf("cyclic ordinary payload classified as %q", reason)
	}
}
