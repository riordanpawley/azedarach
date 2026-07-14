package domain

import (
	"regexp"
	"sort"
	"strings"
)

type AgentTerminalFailureReason string

const (
	AgentTerminalFailureModelCapacity  AgentTerminalFailureReason = "model_capacity"
	AgentTerminalFailureUsageLimit     AgentTerminalFailureReason = "usage_limit"
	AgentTerminalFailureQuotaExhausted AgentTerminalFailureReason = "quota_exhausted"
	AgentTerminalFailureAuthentication AgentTerminalFailureReason = "authentication"

	maxAgentHookPayloadTextValues = 64
	maxAgentHookPayloadTextBytes  = 8 * 1024
	maxAgentHookPayloadNodes      = 256
	maxAgentHookPayloadDepth      = 8
	maxAgentTerminalOutputBytes   = 8 * 1024
)

var terminalAgentFailurePatterns = []struct {
	reason  AgentTerminalFailureReason
	pattern *regexp.Regexp
}{
	{reason: AgentTerminalFailureModelCapacity, pattern: regexp.MustCompile(`(?i)\b(?:selected\s+)?model\b.{0,80}\b(?:at|reached|exceeded)\s+(?:its\s+)?capacity\b`)},
	{reason: AgentTerminalFailureModelCapacity, pattern: regexp.MustCompile(`(?i)\btry\s+(?:using\s+)?a\s+different\s+model\b`)},
	{reason: AgentTerminalFailureUsageLimit, pattern: regexp.MustCompile(`(?i)\b(?:usage|rate)\s+limit\b.{0,80}\b(?:reached|exceeded|exhausted)\b`)},
	{reason: AgentTerminalFailureQuotaExhausted, pattern: regexp.MustCompile(`(?i)\b(?:quota|credits?)\b.{0,80}\b(?:exceeded|exhausted|depleted)\b`)},
	{reason: AgentTerminalFailureAuthentication, pattern: regexp.MustCompile(`(?i)\b(?:authentication\s+failed|not\s+authenticated|invalid\s+(?:api\s+)?(?:key|token))\b`)},
}

var (
	agentTerminalANSIControlSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	agentTerminalANSIOSCSequence     = regexp.MustCompile(`\x1b\][^\x07]*(?:\x07|\x1b\\)`)
)

// ClassifyAgentTerminalOutput classifies the bounded tail of an agent pane.
// It is intentionally independent of lifecycle event shape because some agents
// render terminal failures without emitting a corresponding hook.
func ClassifyAgentTerminalOutput(output string) (AgentTerminalFailureReason, bool) {
	if len(output) > maxAgentTerminalOutputBytes {
		output = output[len(output)-maxAgentTerminalOutputBytes:]
	}
	output = agentTerminalANSIOSCSequence.ReplaceAllString(output, "")
	output = agentTerminalANSIControlSequence.ReplaceAllString(output, "")
	text := strings.Join(strings.Fields(output), " ")
	for _, candidate := range terminalAgentFailurePatterns {
		if candidate.pattern.MatchString(text) {
			return candidate.reason, true
		}
	}
	return "", false
}

// ClassifyAgentTerminalIdle recognizes the stable input affordance rendered by
// supported interactive agents after a turn completes. It deliberately
// requires a prompt glyph near the bounded pane tail so ordinary transcript
// prose and shell prompts do not override hook-backed busy evidence.
func ClassifyAgentTerminalIdle(output string) bool {
	if len(output) > maxAgentTerminalOutputBytes {
		output = output[len(output)-maxAgentTerminalOutputBytes:]
	}
	output = agentTerminalANSIOSCSequence.ReplaceAllString(output, "")
	output = agentTerminalANSIControlSequence.ReplaceAllString(output, "")
	normalized := strings.ToLower(strings.Join(strings.Fields(output), " "))
	if strings.Contains(normalized, "esc to interrupt") || strings.Contains(normalized, "• working (") {
		return false
	}
	lines := strings.Split(output, "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < 4; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seen++
		if line == "›" || strings.HasPrefix(line, "› ") || line == "❯" || strings.HasPrefix(line, "❯ ") {
			return true
		}
	}
	return false
}

func ClassifyAgentTerminalFailure(event string, payload map[string]any) (AgentTerminalFailureReason, bool) {
	if strings.TrimSpace(event) != "idle_prompt" || len(payload) == 0 {
		return "", false
	}
	values := boundedAgentHookPayloadText(payload)
	for _, candidate := range terminalAgentFailurePatterns {
		for _, value := range values {
			if candidate.pattern.MatchString(value) {
				return candidate.reason, true
			}
		}
	}
	return "", false
}

func boundedAgentHookPayloadText(payload map[string]any) []string {
	values := make([]string, 0, min(len(payload), maxAgentHookPayloadTextValues))
	remaining := maxAgentHookPayloadTextBytes
	visited := 0
	limitReached := func() bool {
		return visited >= maxAgentHookPayloadNodes || len(values) >= maxAgentHookPayloadTextValues || remaining <= 0
	}
	var collect func(any, int)
	collect = func(value any, depth int) {
		if limitReached() || depth > maxAgentHookPayloadDepth {
			return
		}
		visited++
		switch typed := value.(type) {
		case string:
			if len(typed) > remaining {
				typed = typed[:remaining]
			}
			remaining -= len(typed)
			text := strings.Join(strings.Fields(typed), " ")
			if text == "" {
				return
			}
			values = append(values, text)
		case map[string]any:
			if len(typed) > maxAgentHookPayloadNodes-visited {
				return
			}
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				collect(typed[key], depth+1)
				if limitReached() {
					break
				}
			}
		case []any:
			for _, nested := range typed {
				collect(nested, depth+1)
				if limitReached() {
					break
				}
			}
		}
	}
	collect(payload, 0)
	return values
}
