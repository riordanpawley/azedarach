package domain

import (
	"strconv"
	"strings"
	"testing"
)

func TestClassifyAgentTerminalOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output string
		want   AgentTerminalFailureReason
		ok     bool
	}{
		{name: "capacity screen", output: "⚠ Selected model is at capacity. Please try a different model.", want: AgentTerminalFailureModelCapacity, ok: true},
		{name: "colored capacity screen", output: "\x1b[33m⚠ Selected \x1b[1mmodel\x1b[0m is at capacity.\x1b[0m Please try a different model.", want: AgentTerminalFailureModelCapacity, ok: true},
		{name: "ordinary prompt", output: "Tests passed.\n› Continue", ok: false},
		{name: "bounded tail ignores old failure", output: "Selected model is at capacity.\n" + strings.Repeat("x", maxAgentTerminalOutputBytes+1), ok: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ClassifyAgentTerminalOutput(tt.output)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ClassifyAgentTerminalOutput() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestClassifyAgentTerminalIdle(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "codex prompt", output: "Tests passed.\n› Continue", want: true},
		{name: "claude prompt", output: "Done.\n❯", want: true},
		{name: "quoted prompt outside tail", output: "› example\nline 1\nline 2\nline 3\nline 4\nline 5"},
		{name: "prompt remains visible while working", output: "• Working (49s • esc to interrupt)\n› Continue"},
		{name: "shell prompt", output: "tests passed\n$ "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyAgentTerminalIdle(tt.output); got != tt.want {
				t.Fatalf("ClassifyAgentTerminalIdle() = %t, want %t", got, tt.want)
			}
		})
	}
}

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
