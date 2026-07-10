package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// AgentSource identifies which AI agent fired a hook. The port treats events
// uniformly; agent-specific behavior (e.g. codex prime-evidence guard) is gated
// here, not duplicated in CLI adapters.
type AgentSource string

const (
	AgentClaude AgentSource = "claude"
	AgentCodex  AgentSource = "codex"

	hookBestEffortDaemonTimeout = 2 * time.Second
	maxHookPayloadTextValues    = 64
	maxHookPayloadTextBytes     = 8 * 1024
	maxHookPayloadNodes         = 256
	maxHookPayloadDepth         = 8
)

var terminalAgentFailurePatterns = []struct {
	reason  string
	pattern *regexp.Regexp
}{
	{reason: "model_capacity", pattern: regexp.MustCompile(`(?i)\b(?:selected\s+)?model\b.{0,80}\b(?:at|reached|exceeded)\s+(?:its\s+)?capacity\b`)},
	{reason: "model_capacity", pattern: regexp.MustCompile(`(?i)\btry\s+(?:using\s+)?a\s+different\s+model\b`)},
	{reason: "usage_limit", pattern: regexp.MustCompile(`(?i)\b(?:usage|rate)\s+limit\b.{0,80}\b(?:reached|exceeded|exhausted)\b`)},
	{reason: "quota_exhausted", pattern: regexp.MustCompile(`(?i)\b(?:quota|credits?)\b.{0,80}\b(?:exceeded|exhausted|depleted)\b`)},
	{reason: "authentication", pattern: regexp.MustCompile(`(?i)\b(?:authentication\s+failed|not\s+authenticated|invalid\s+(?:api\s+)?(?:key|token))\b`)},
}

// IsKnown reports whether the agent source is one the port understands.
func (a AgentSource) IsKnown() bool {
	switch a {
	case AgentClaude, AgentCodex:
		return true
	}
	return false
}

// AgentHookContext is the agent-agnostic carrier of a hook invocation.
type AgentHookContext struct {
	Agent      AgentSource
	Event      string // canonical underscore form (e.g. "session_start", "permission_request")
	IssueID    string
	ProjectDir string
	Payload    map[string]any
}

// AgentHookOutcome is the result of running a hook through the port.
type AgentHookOutcome struct {
	// GuardResponse holds JSON fields agents may act on (systemMessage,
	// decision, etc.). Always non-nil; empty means "allow / no advice".
	GuardResponse map[string]any
}

// RunAgentHook is the shared hook-handling port called by every CLI adapter.
// It performs:
//  1. runtime signal emission (best effort, tagged with the agent source)
//  2. agent-specific guard logic (currently: codex prime-evidence threading)
//
// CLI adapters should render the returned GuardResponse in whatever format
// their agent's hook protocol expects.
func RunAgentHook(ctx context.Context, deps *Dependencies, hookCtx AgentHookContext) (AgentHookOutcome, error) {
	outcome := AgentHookOutcome{GuardResponse: map[string]any{}}

	agent := hookCtx.Agent
	if !agent.IsKnown() {
		return outcome, fmt.Errorf("unknown agent source: %q", string(agent))
	}

	event := strings.TrimSpace(hookCtx.Event)
	if _, ok := hookEventStatuses[event]; !ok {
		return outcome, fmt.Errorf("invalid hook event: %q", event)
	}

	if deps != nil && deps.DaemonClient != nil && shouldIngestAgentRuntimeSignal(hookCtx, event) {
		notifyCtx, cancel := context.WithTimeout(ctx, hookBestEffortDaemonTimeout)
		_ = ingestAgentHookRuntimeSignalBestEffort(notifyCtx, deps, hookCtx, event)
		cancel()
	}

	switch agent {
	case AgentCodex:
		guardEvent, ok := codexGuardEventForNotifyEvent(event)
		if !ok {
			break
		}
		response, err := codexGuardResponse(hookCtx.ProjectDir, guardEvent, hookCtx.Payload)
		if err != nil {
			return outcome, err
		}
		if response != nil {
			outcome.GuardResponse = response
		}
	case AgentClaude:
		// Claude has no port-layer guard today.
	}

	return outcome, nil
}

func shouldAppendHookLogEvent(event string) bool {
	switch event {
	case hookEventPreToolUse, hookEventPostToolUse:
		return false
	default:
		return true
	}
}

func shouldIngestAgentRuntimeSignal(hookCtx AgentHookContext, event string) bool {
	if !shouldAppendHookLogEvent(event) && !agentHookRuntimeSignalLifecycleRelevant(event) {
		return false
	}
	if strings.TrimSpace(hookCtx.IssueID) != "" && agentHookRuntimeSignalLifecycleRelevant(event) {
		return true
	}
	return strings.TrimSpace(hookCtx.ProjectDir) != "" && shouldAppendHookLogEvent(event)
}

func agentHookRuntimeSignalLifecycleRelevant(event string) bool {
	switch event {
	case hookEventIdlePrompt, hookEventPermissionRequest, hookEventStop, hookEventSubagentStop, hookEventSessionEnd,
		hookEventSessionStart, hookEventSubagentStart, hookEventUserPromptSubmit, hookEventPreToolUse:
		return true
	default:
		return false
	}
}

func ingestAgentHookRuntimeSignalBestEffort(ctx context.Context, deps *Dependencies, hookCtx AgentHookContext, event string) error {
	projectID := strings.TrimSpace(deps.ProjectID)
	if projectID == "" {
		projectID = protocol.DefaultProjectID
	}
	issueID := strings.TrimSpace(hookCtx.IssueID)
	signal := protocol.RuntimeSignalIngestCommandBody{
		Source:    protocol.RuntimeSignalSourceAgentHook,
		Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
		ProjectID: projectID,
		IssueID:   issueID,
		SessionID: agentHookSessionID(projectID, issueID),
		Worktree:  strings.TrimSpace(hookCtx.ProjectDir),
		TmuxPane:  strings.TrimSpace(os.Getenv("TMUX_PANE")),
		Agent:     string(hookCtx.Agent),
		Hook:      event,
		Event:     event,
		Log:       shouldAppendHookLogEvent(event),
		Message:   fmt.Sprintf("%s hook: %s", hookCtx.Agent, event),
		Payload:   hookCtx.Payload,
	}
	if reason, ok := classifyTerminalAgentFailure(event, hookCtx.Payload); ok {
		blocking := true
		signal.Activity = "error"
		signal.Level = "error"
		signal.Blocking = &blocking
		signal.Message = fmt.Sprintf("%s hook: %s (terminal agent failure: %s)", hookCtx.Agent, event, reason)
	}
	if issueID == "" {
		signal.SessionID = ""
	}
	_, err := deps.DaemonClient.RuntimeSignalIngest(ctx, signal)
	return err
}

func classifyTerminalAgentFailure(event string, payload map[string]any) (string, bool) {
	if strings.TrimSpace(event) != hookEventIdlePrompt || len(payload) == 0 {
		return "", false
	}
	for _, value := range boundedHookPayloadText(payload) {
		for _, candidate := range terminalAgentFailurePatterns {
			if candidate.pattern.MatchString(value) {
				return candidate.reason, true
			}
		}
	}
	return "", false
}

func boundedHookPayloadText(payload map[string]any) []string {
	values := make([]string, 0, min(len(payload), maxHookPayloadTextValues))
	remaining := maxHookPayloadTextBytes
	visited := 0
	limitReached := func() bool {
		return visited >= maxHookPayloadNodes || len(values) >= maxHookPayloadTextValues || remaining <= 0
	}
	var collect func(any, int)
	collect = func(value any, depth int) {
		if limitReached() || depth > maxHookPayloadDepth {
			return
		}
		visited++
		switch typed := value.(type) {
		case string:
			text := strings.Join(strings.Fields(typed), " ")
			if text == "" {
				return
			}
			if len(text) > remaining {
				text = text[:remaining]
			}
			values = append(values, text)
			remaining -= len(text)
		case map[string]any:
			for _, nested := range typed {
				collect(nested, depth+1)
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

func agentHookSessionID(projectID, issueID string) string {
	sessionID := naming.CanonicalSessionID(projectID, strings.TrimSpace(issueID))
	paneID := sanitizeAgentSessionIDPart(os.Getenv("TMUX_PANE"))
	if paneID == "" {
		return sessionID
	}
	return sessionID + ".pane-" + paneID
}

func sanitizeAgentSessionIDPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-_.")
}

// codexGuardEventForNotifyEvent maps the canonical (underscore) event name back
// to the dashed codex-CLI form expected by codexGuardResponse. Returns false
// when the event is not part of the codex guard surface.
func codexGuardEventForNotifyEvent(event string) (string, bool) {
	switch event {
	case hookEventSessionStart:
		return "session-start", true
	case hookEventUserPromptSubmit:
		return "user-prompt-submit", true
	case hookEventPreToolUse:
		return "pre-tool-use", true
	case hookEventPostToolUse:
		return "post-tool-use", true
	case hookEventPermissionRequest:
		return "permission-request", true
	case hookEventStop:
		return "stop", true
	case hookEventSubagentStart:
		return "subagent-start", true
	case hookEventSubagentStop:
		return "subagent-stop", true
	default:
		return "", false
	}
}

// AIHookRunOptions configures the unified `az ai hook run` command.
type AIHookRunOptions struct {
	Agent AgentSource
	Event string // canonical underscore form
	JSON  bool
}

// ParseAIHookRunArgs parses arguments for `az ai hook run --agent=<agent>
// [--json] <event>`. Event accepts either the canonical underscore form
// (e.g. session_start) or the dashed CLI form codex uses (session-start);
// they are normalized to the underscore form.
func ParseAIHookRunArgs(args []string) (AIHookRunOptions, error) {
	opts := AIHookRunOptions{}
	fs := flag.NewFlagSet("ai hook run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agent := ""
	fs.StringVar(&agent, "agent", "", "agent source (claude|codex)")
	fs.BoolVar(&opts.JSON, "json", false, "hook-json output")
	if err := fs.Parse(args); err != nil {
		return AIHookRunOptions{}, err
	}
	if fs.NArg() != 1 {
		return AIHookRunOptions{}, fmt.Errorf("usage: az ai hook run --agent=<claude|codex> [--json] <event>")
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return AIHookRunOptions{}, fmt.Errorf("--agent is required")
	}
	source := AgentSource(agent)
	if !source.IsKnown() {
		return AIHookRunOptions{}, fmt.Errorf("unsupported agent: %q (want claude or codex)", agent)
	}
	opts.Agent = source
	opts.Event = strings.ReplaceAll(strings.TrimSpace(fs.Arg(0)), "-", "_")
	if _, ok := hookEventStatuses[opts.Event]; !ok {
		return AIHookRunOptions{}, fmt.Errorf("invalid hook event: %q", opts.Event)
	}
	return opts, nil
}

// AIHookRunCommand is the unified CLI entrypoint that delegates to the
// shared RunAgentHook port. Agent-specific output rendering happens here.
func AIHookRunCommand(deps *Dependencies, opts AIHookRunOptions) error {
	projectDir, err := resolveProjectDir("", deps)
	if err != nil {
		return err
	}
	payloadMap, err := parseHookPayload(os.Stdin)
	if err != nil {
		return err
	}
	issueID := strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	outcome, err := RunAgentHook(ctx, deps, AgentHookContext{
		Agent:      opts.Agent,
		Event:      opts.Event,
		IssueID:    issueID,
		ProjectDir: projectDir,
		Payload:    payloadMap,
	})
	if err != nil {
		return err
	}

	switch opts.Agent {
	case AgentCodex:
		if opts.JSON {
			encoded, err := json.Marshal(outcome.GuardResponse)
			if err != nil {
				return err
			}
			fmt.Println(string(encoded))
			return nil
		}
		notifyOutput, err := renderNotifyOutput(opts.Event, false, false, "")
		if err != nil {
			return err
		}
		fmt.Println(notifyOutput)
		printCodexGuardResponse(outcome.GuardResponse)
	default: // claude
		notifyOutput, err := renderNotifyOutput(opts.Event, opts.JSON, false, "")
		if err != nil {
			return err
		}
		fmt.Println(notifyOutput)
	}
	return nil
}

// PrintAIUsage prints usage for the `az ai` family.
func PrintAIUsage() {
	fmt.Println("Usage: az ai <install|status|uninstall|migrate|hook> [arguments]")
	fmt.Println("       az ai hook run --agent=<claude|codex> [--json] <event>")
	fmt.Println("Manage and run AI agent hooks through the shared internal port.")
}
