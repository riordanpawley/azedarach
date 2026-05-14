package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

// AgentSource identifies which AI agent fired a hook. The port treats events
// uniformly; agent-specific behavior (e.g. codex prime-evidence guard) is gated
// here, not duplicated in CLI adapters.
type AgentSource string

const (
	AgentClaude AgentSource = "claude"
	AgentCodex  AgentSource = "codex"
)

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
//  1. hook-log emission (best effort, tagged with the agent source)
//  2. daemon session-lifecycle notify (best effort, scoped to issue ID)
//  3. agent-specific guard logic (currently: codex prime-evidence threading)
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

	if strings.TrimSpace(hookCtx.ProjectDir) != "" {
		appendHookLogEventBestEffort(deps, protocol.HookLogEvent{
			Hook:     event,
			Worktree: strings.TrimSpace(hookCtx.ProjectDir),
			Source:   fmt.Sprintf("%s.hook", agent),
			Level:    "info",
			Message:  fmt.Sprintf("%s hook: %s", agent, event),
		})
	}

	if issueID := strings.TrimSpace(hookCtx.IssueID); issueID != "" && deps != nil && deps.DaemonClient != nil {
		notifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = notifyDaemonSessionStatus(notifyCtx, deps, issueID, event)
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
	fmt.Println("Usage: az ai hook run --agent=<claude|codex> [--json] <event>")
	fmt.Println("Run an agent hook through the shared internal port. Renders agent-appropriate output.")
}
