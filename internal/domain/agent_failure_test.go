package domain

import (
	"strconv"
	"strings"
	"testing"
)

func TestClassifyAgentTerminalFailurePreservesOrdinaryIdleNotifications(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload map[string]any
		want    AgentTerminalFailureReason
	}{
		{name: "capacity", event: "idle_prompt", payload: map[string]any{"message": "Selected model is at capacity. Please try a different model."}, want: AgentTerminalFailureModelCapacity},
		{name: "wrapped capacity", event: "idle_prompt", payload: map[string]any{"message": "Selected model is at\ncapacity. Please try a different model."}, want: AgentTerminalFailureModelCapacity},
		{name: "usage limit", event: "idle_prompt", payload: map[string]any{"message": "Usage limit has been reached"}, want: AgentTerminalFailureUsageLimit},
		{name: "ordinary idle", event: "idle_prompt", payload: map[string]any{"message": "Codex is waiting for input"}},
		{name: "non idle payload", event: "user_prompt_submit", payload: map[string]any{"message": "Selected model is at capacity"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifyAgentTerminalFailure(tt.event, tt.payload)
			if ok != (tt.want != "") || got != tt.want {
				t.Fatalf("classification = %q/%t, want %q/%t", got, ok, tt.want, tt.want != "")
			}
		})
	}
}

func TestClassifyAgentTerminalFailureRejectsOversizedMapsDeterministically(t *testing.T) {
	payload := make(map[string]any, maxAgentHookPayloadNodes+1)
	for i := range maxAgentHookPayloadNodes + 1 {
		payload[strconv.Itoa(i)] = "ordinary notification"
	}
	payload["capacity"] = "Selected model is at capacity"
	if reason, ok := ClassifyAgentTerminalFailure("idle_prompt", payload); ok {
		t.Fatalf("oversized map classified as %q", reason)
	}
}

func TestClassifyAgentTerminalFailureBoundsPayloadTraversal(t *testing.T) {
	payload := map[string]any{"message": "ordinary notification"}
	payload["self"] = payload

	if reason, ok := ClassifyAgentTerminalFailure("idle_prompt", payload); ok {
		t.Fatalf("cyclic ordinary payload classified as %q", reason)
	}
}

func TestClassifyAgentTerminalFailureBoundsTextBeforeNormalization(t *testing.T) {
	payload := map[string]any{
		"message": strings.Repeat("ordinary ", maxAgentHookPayloadTextBytes) + "Selected model is at capacity",
	}
	if reason, ok := ClassifyAgentTerminalFailure("idle_prompt", payload); ok {
		t.Fatalf("text beyond scan budget classified as %q", reason)
	}
}

func TestClassifyAgentTerminalFailureUsesDeterministicReasonPriority(t *testing.T) {
	payload := map[string]any{
		"z_auth":     "Authentication failed",
		"a_capacity": "Selected model is at capacity",
	}
	for range 20 {
		reason, ok := ClassifyAgentTerminalFailure("idle_prompt", payload)
		if !ok || reason != AgentTerminalFailureModelCapacity {
			t.Fatalf("classification = %q/%t, want model_capacity/true", reason, ok)
		}
	}
}
