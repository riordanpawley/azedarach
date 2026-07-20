package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// AgentSource identifies which AI agent fired a hook. The port treats events
// uniformly; agent-specific behavior (e.g. codex prime-evidence guard) is gated
// here, not duplicated in CLI adapters.
type AgentSource string

const (
	AgentClaude   AgentSource = "claude"
	AgentCodex    AgentSource = "codex"
	AgentOpenCode AgentSource = "opencode"

	hookBestEffortDaemonTimeout = 2 * time.Second
)

// IsKnown reports whether the agent source is one the port understands.
func (a AgentSource) IsKnown() bool {
	switch a {
	case AgentClaude, AgentCodex, AgentOpenCode:
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
	GuardResponse          map[string]any
	ActivationConfirmation *protocol.LearnActivationConfirmRequestBody
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
	contextualGuidance := ""
	contextualActivationID := ""

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
	if agent == AgentCodex && deps != nil && deps.DaemonClient != nil && (event == hookEventSessionStart || event == hookEventUserPromptSubmit) && strings.TrimSpace(hookCtx.IssueID) != "" {
		activationCtx, cancel := context.WithTimeout(ctx, hookBestEffortDaemonTimeout)
		purpose := domain.LearningPurposeContextTransition
		if event == hookEventSessionStart {
			purpose = domain.LearningPurposeSessionStart
		}
		query := agentHookPromptText(hookCtx.Payload)
		projectID := strings.TrimSpace(deps.ProjectID)
		if projectID == "" {
			projectID = protocol.DefaultProjectID
		}
		activation, activationErr := deps.DaemonClient.ActivateContextualLearnings(activationCtx, protocol.LearnContextualActivateRequestBody{
			Purpose: string(purpose), Surface: "agent_hook", SessionID: naming.SessionID(naming.CanonicalSessionID(projectID, hookCtx.IssueID)), ContextIssueID: naming.IssueID(hookCtx.IssueID), Query: query, TokenBudget: 192,
		})
		if activationErr == nil && activation.Proposal != nil && len(activation.Learnings) > 0 {
			contextualGuidance = renderContextualLearningGuidance(activation.Proposal.ActivationID, activation.Learnings)
			contextualActivationID = activation.Proposal.ActivationID
		}
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
	if contextualGuidance != "" {
		renderedGuidance := contextualGuidance
		existing, _ := outcome.GuardResponse["systemMessage"].(string)
		if strings.TrimSpace(existing) != "" {
			contextualGuidance = strings.TrimSpace(existing) + "\n\n" + contextualGuidance
		}
		outcome.GuardResponse["systemMessage"] = contextualGuidance
		outcome.ActivationConfirmation = &protocol.LearnActivationConfirmRequestBody{ActivationID: contextualActivationID, TokenCost: domain.RenderedLearningTokenCost(renderedGuidance)}
	}

	return outcome, nil
}

func agentHookPromptText(payload map[string]any) string {
	for _, key := range []string{"prompt", "user_prompt", "message"} {
		if value, ok := payload[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func renderContextualLearningGuidance(activationID string, learnings []protocol.Learning) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Relevant project guidance [activation: %s]:\n", activationID)
	for _, learning := range learnings {
		fmt.Fprintf(&b, "- %s: %s\n", learning.ID, learning.Summary)
	}
	fmt.Fprintf(&b, "Feedback: `az learn feedback --idempotency-key <key> --outcome helpful|followed|contradicted|unknown %s`\n", activationID)
	return strings.TrimSpace(b.String())
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
		Source:           protocol.RuntimeSignalSourceAgentHook,
		Kind:             protocol.RuntimeSignalKindAgentActivityChanged,
		ProjectID:        projectID,
		IssueID:          issueID,
		SessionID:        agentHookSessionID(projectID, issueID),
		Worktree:         strings.TrimSpace(hookCtx.ProjectDir),
		TmuxPane:         strings.TrimSpace(os.Getenv("TMUX_PANE")),
		AgentIncarnation: agentHookIncarnation(hookCtx.Payload),
		AgentThreadID:    agentHookThreadID(hookCtx.Payload),
		Agent:            string(hookCtx.Agent),
		Hook:             event,
		Event:            event,
		Log:              shouldAppendHookLogEvent(event),
		Message:          fmt.Sprintf("%s hook: %s", hookCtx.Agent, event),
		Payload:          hookCtx.Payload,
	}
	if explicit := strings.TrimSpace(os.Getenv("AZEDARACH_AGENT_INCARNATION")); explicit != "" {
		signal.AgentIncarnation = explicit
	}
	if signal.AgentIncarnation != "" {
		signal.LogicalPaneID = strings.TrimSpace(os.Getenv("AZEDARACH_LOGICAL_PANE_ID"))
		if signal.LogicalPaneID == "" {
			signal.LogicalPaneID = "agent"
		}
		if panePID, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AZEDARACH_PANE_PID"))); err == nil && panePID > 0 {
			signal.PanePID = panePID
		}
	}
	if event == hookEventSessionEnd {
		signal.ExitStatus = agentProcessExitStatus(hookCtx.Payload)
		if signal.ExitStatus != nil {
			signal.Message = fmt.Sprintf("%s process exited with status %d", hookCtx.Agent, *signal.ExitStatus)
			if *signal.ExitStatus != 0 {
				signal.Level = "error"
			}
		}
	}
	_, err := deps.DaemonClient.RuntimeSignalIngest(ctx, signal)
	return err
}

func agentHookIncarnation(payload map[string]any) string {
	for _, key := range []string{"session_id", "conversation_id", "thread_id", "thread-id"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func agentHookThreadID(payload map[string]any) string {
	for _, key := range []string{"thread_id", "thread-id"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func agentProcessExitStatus(payload map[string]any) *int {
	value, ok := payload["exit_status"]
	if !ok {
		return nil
	}
	var status int
	switch typed := value.(type) {
	case float64:
		status = int(typed)
		if float64(status) != typed {
			return nil
		}
	case int:
		status = typed
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return nil
		}
		status = int(parsed)
	default:
		return nil
	}
	return &status
}

func agentHookSessionID(projectID, issueID string) string {
	if explicit := strings.TrimSpace(os.Getenv("AZEDARACH_SESSION_ID")); explicit != "" && strings.TrimSpace(issueID) == "" {
		return explicit
	}
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
	fs.StringVar(&agent, "agent", "", "agent source (claude|codex|opencode)")
	fs.BoolVar(&opts.JSON, "json", false, "hook-json output")
	if err := fs.Parse(args); err != nil {
		return AIHookRunOptions{}, err
	}
	if fs.NArg() != 1 {
		return AIHookRunOptions{}, fmt.Errorf("usage: az ai hook run --agent=<claude|codex|opencode> [--json] <event>")
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		return AIHookRunOptions{}, fmt.Errorf("--agent is required")
	}
	source := AgentSource(agent)
	if !source.IsKnown() {
		return AIHookRunOptions{}, fmt.Errorf("unsupported agent: %q (want claude, codex, or opencode)", agent)
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
	return aiHookRunCommandTo(deps, opts, os.Stdout)
}

func aiHookRunCommandTo(deps *Dependencies, opts AIHookRunOptions, stdout io.Writer) error {
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
	return emitAgentHookOutcome(deps, opts, outcome, stdout)
}

func emitAgentHookOutcome(deps *Dependencies, opts AIHookRunOptions, outcome AgentHookOutcome, stdout io.Writer) error {
	fail := func(reason string, deliveryErr error) error {
		if outcome.ActivationConfirmation == nil || deps == nil || deps.DaemonClient == nil {
			return deliveryErr
		}
		ctx, cancel := context.WithTimeout(context.Background(), hookBestEffortDaemonTimeout)
		defer cancel()
		_, abandonErr := deps.DaemonClient.AbandonLearningActivation(ctx, protocol.LearnActivationAbandonRequestBody{ActivationID: outcome.ActivationConfirmation.ActivationID, Reason: reason})
		return errors.Join(deliveryErr, abandonErr)
	}
	switch opts.Agent {
	case AgentCodex:
		if opts.JSON {
			encoded, err := json.Marshal(outcome.GuardResponse)
			if err != nil {
				return fail("render_failed", err)
			}
			if _, err := fmt.Fprintln(stdout, string(encoded)); err != nil {
				return fail("write_failed", err)
			}
			break
		}
		notifyOutput, err := renderNotifyOutput(opts.Event, false, false, "")
		if err != nil {
			return fail("render_failed", err)
		}
		if _, err := fmt.Fprintln(stdout, notifyOutput); err != nil {
			return fail("write_failed", err)
		}
		if err := writeCodexGuardResponse(stdout, outcome.GuardResponse); err != nil {
			return fail("write_failed", err)
		}
	default: // claude
		notifyOutput, err := renderNotifyOutput(opts.Event, opts.JSON, false, "")
		if err != nil {
			return fail("render_failed", err)
		}
		if _, err := fmt.Fprintln(stdout, notifyOutput); err != nil {
			return fail("write_failed", err)
		}
	}
	if outcome.ActivationConfirmation != nil {
		ctx, cancel := context.WithTimeout(context.Background(), hookBestEffortDaemonTimeout)
		defer cancel()
		if _, err := deps.DaemonClient.ConfirmLearningActivation(ctx, *outcome.ActivationConfirmation); err != nil {
			return err
		}
	}
	return nil
}

// PrintAIUsage prints usage for the `az ai` family.
func PrintAIUsage() {
	fmt.Println("Usage: az ai <account|install|status|uninstall|migrate|hook> [arguments]")
	fmt.Println("       az ai hook run --agent=<claude|codex|opencode> [--json] <event>")
	fmt.Println("Manage AI accounts and agent hooks through Azedarach.")
}
