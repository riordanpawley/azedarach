package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// AgentInstallTarget identifies a hook install destination. The unified
// `az ai install` command accepts one or more targets and dispatches to the
// matching installer adapter.
type AgentInstallTarget string

const (
	AgentInstallTargetAuto     AgentInstallTarget = "auto"
	AgentInstallTargetRulesync AgentInstallTarget = "rulesync"
	AgentInstallTargetClaude   AgentInstallTarget = "claude"
	AgentInstallTargetCodex    AgentInstallTarget = "codex"
	AgentInstallTargetOpencode AgentInstallTarget = "opencode"
)

func (t AgentInstallTarget) IsKnown() bool {
	switch t {
	case AgentInstallTargetAuto, AgentInstallTargetRulesync, AgentInstallTargetClaude, AgentInstallTargetCodex, AgentInstallTargetOpencode:
		return true
	}
	return false
}

// AIInstallOptions configures `az ai install`.
type AIInstallOptions struct {
	Targets    []AgentInstallTarget
	IssueID    string
	ProjectDir string
	Generate   string // "auto" (default), "never"
	Verbose    bool
}

// ParseAIInstallArgs parses `az ai install` arguments.
//
// Usage: az ai install [--target=auto|rulesync|claude|codex|opencode,...] [--issue <id>] [--project-dir <dir>] [--generate=auto|never] [--verbose]
func ParseAIInstallArgs(args []string) (AIInstallOptions, error) {
	opts := AIInstallOptions{Generate: "auto"}
	fs := flag.NewFlagSet("ai install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	targetCSV := "auto"
	fs.StringVar(&targetCSV, "target", targetCSV, "comma-separated install targets")
	fs.StringVar(&opts.IssueID, "issue", "", "issue id to scope claude hooks (defaults to AZEDARACH_ISSUE_ID)")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.StringVar(&opts.Generate, "generate", opts.Generate, "auto|never — when target=rulesync, whether to run rulesync generate")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")
	if err := fs.Parse(args); err != nil {
		return AIInstallOptions{}, err
	}
	if fs.NArg() != 0 {
		return AIInstallOptions{}, fmt.Errorf("usage: az ai install [--target=...] [--issue <id>] [--project-dir <dir>] [--generate=auto|never] [--verbose]")
	}

	targets := make([]AgentInstallTarget, 0)
	seen := make(map[AgentInstallTarget]struct{})
	for _, raw := range strings.Split(targetCSV, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		target := AgentInstallTarget(name)
		if !target.IsKnown() {
			return AIInstallOptions{}, fmt.Errorf("unsupported install target: %q (want auto, rulesync, claude, codex, opencode)", name)
		}
		if _, dup := seen[target]; dup {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		targets = append(targets, AgentInstallTargetAuto)
	}
	opts.Targets = targets

	switch strings.ToLower(strings.TrimSpace(opts.Generate)) {
	case "auto", "never", "":
	default:
		return AIInstallOptions{}, fmt.Errorf("unsupported --generate value: %q (want auto or never)", opts.Generate)
	}
	return opts, nil
}

// PrintAIInstallUsage prints usage for `az ai install`.
func PrintAIInstallUsage() {
	fmt.Println("Usage: az ai install [--target=auto|rulesync|claude|codex|opencode,...] [--issue <id>] [--project-dir <dir>] [--generate=auto|never] [--verbose]")
	fmt.Println("Install az hooks into one or more AI agent harnesses. Auto-detects targets when --target=auto (default).")
}

// agentInstaller is the port each install adapter implements.
type agentInstaller interface {
	Name() AgentInstallTarget
	Install(ctx context.Context, deps *Dependencies, opts AIInstallOptions) error
}

// AIInstallCommand orchestrates per-target installs and (for rulesync target)
// optionally runs `rulesync generate` via the project's package manager.
func AIInstallCommand(deps *Dependencies, opts AIInstallOptions) error {
	projectDir, err := resolveProjectDir(opts.ProjectDir, deps)
	if err != nil {
		return err
	}
	opts.ProjectDir = projectDir

	if strings.TrimSpace(opts.IssueID) == "" {
		opts.IssueID = strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	}

	resolved, autoNote := resolveInstallTargets(projectDir, opts.Targets)
	if len(resolved) == 0 {
		fmt.Printf("az ai install: no install targets detected under %s — nothing to do\n", projectDir)
		fmt.Println("  Hint: pass --target=claude|codex|opencode|rulesync to force a specific install.")
		return nil
	}
	if autoNote != "" && opts.Verbose {
		fmt.Println(autoNote)
	}

	ctx := context.Background()
	for _, target := range resolved {
		installer := installerForTarget(target)
		if installer == nil {
			return fmt.Errorf("no installer registered for target: %s", target)
		}
		if err := installer.Install(ctx, deps, opts); err != nil {
			return fmt.Errorf("install target %s: %w", target, err)
		}
	}

	if containsTarget(resolved, AgentInstallTargetRulesync) {
		if strings.ToLower(strings.TrimSpace(opts.Generate)) == "never" {
			fmt.Println("  (skipping rulesync generate — --generate=never)")
			return nil
		}
		if err := runRulesyncGenerate(ctx, projectDir, opts.Verbose); err != nil {
			fmt.Fprintf(os.Stderr, "rulesync generate failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "  Hooks source is written; re-run rulesync generate manually to fan out.")
		}
	}
	return nil
}

// resolveInstallTargets expands an auto target via filesystem detection.
// Detection precedence:
//  1. If .rulesync/ exists in the project, prefer rulesync only — the user
//     has opted into the unified rulesync source-of-truth model and we
//     should not duplicate writes to per-tool configs.
//  2. Otherwise install to each agent whose marker is present (.claude/,
//     .codex/, .opencode/, opencode.json).
//
// rulesync being globally on PATH alone does NOT bootstrap rulesync — that
// would surprise users who happen to have it installed but haven't opted
// into managing this project with it.
func resolveInstallTargets(projectDir string, requested []AgentInstallTarget) ([]AgentInstallTarget, string) {
	if !containsTarget(requested, AgentInstallTargetAuto) {
		return requested, ""
	}
	if rulesyncProjectMarker(projectDir) {
		return []AgentInstallTarget{AgentInstallTargetRulesync}, "az ai install: detected .rulesync/; installing canonical hooks source only."
	}
	detected := make([]AgentInstallTarget, 0, 3)
	if fileExists(filepath.Join(projectDir, ".claude")) {
		detected = append(detected, AgentInstallTargetClaude)
	}
	if fileExists(filepath.Join(projectDir, ".codex")) {
		detected = append(detected, AgentInstallTargetCodex)
	}
	if fileExists(filepath.Join(projectDir, "opencode.json")) || fileExists(filepath.Join(projectDir, ".opencode")) {
		detected = append(detected, AgentInstallTargetOpencode)
	}
	if len(detected) == 0 {
		return nil, "az ai install: no agent markers detected (.rulesync/, .claude/, .codex/, .opencode/, opencode.json)."
	}
	return detected, fmt.Sprintf("az ai install: auto-detected targets: %s", joinTargets(detected))
}

func rulesyncProjectMarker(projectDir string) bool {
	return fileExists(filepath.Join(projectDir, ".rulesync"))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func joinTargets(targets []AgentInstallTarget) string {
	out := make([]string, len(targets))
	for i, t := range targets {
		out[i] = string(t)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func containsTarget(targets []AgentInstallTarget, want AgentInstallTarget) bool {
	for _, t := range targets {
		if t == want {
			return true
		}
	}
	return false
}

func installerForTarget(target AgentInstallTarget) agentInstaller {
	switch target {
	case AgentInstallTargetRulesync:
		return rulesyncInstaller{}
	case AgentInstallTargetClaude:
		return claudeInstaller{}
	case AgentInstallTargetCodex:
		return codexInstaller{}
	case AgentInstallTargetOpencode:
		return opencodeInstaller{}
	}
	return nil
}

// ----- claude adapter --------------------------------------------------------

type claudeInstaller struct{}

func (claudeInstaller) Name() AgentInstallTarget { return AgentInstallTargetClaude }

func (claudeInstaller) Install(_ context.Context, deps *Dependencies, opts AIInstallOptions) error {
	issueID := strings.TrimSpace(opts.IssueID)
	if issueID == "" {
		return fmt.Errorf("claude install requires --issue <id> or AZEDARACH_ISSUE_ID")
	}
	return HooksInstallCommand(deps, HooksInstallOptions{
		IssueID:    issueID,
		ProjectDir: opts.ProjectDir,
		Verbose:    opts.Verbose,
	})
}

// ----- codex adapter ---------------------------------------------------------

type codexInstaller struct{}

func (codexInstaller) Name() AgentInstallTarget { return AgentInstallTargetCodex }

func (codexInstaller) Install(_ context.Context, deps *Dependencies, opts AIInstallOptions) error {
	return CodexInstallCommand(deps, CodexInstallOptions{
		ProjectDir: opts.ProjectDir,
		Verbose:    opts.Verbose,
	})
}

// ----- opencode adapter ------------------------------------------------------

type opencodeInstaller struct{}

func (opencodeInstaller) Name() AgentInstallTarget { return AgentInstallTargetOpencode }

func (opencodeInstaller) Install(_ context.Context, deps *Dependencies, opts AIInstallOptions) error {
	if err := OpenCodeInitCommand(deps, OpenCodeInitOptions{
		ProjectDir: opts.ProjectDir,
		Verbose:    opts.Verbose,
	}); err != nil {
		return err
	}
	return OpenCodePluginInstallCommand(deps, OpenCodePluginInstallOptions{
		ProjectDir: opts.ProjectDir,
		Verbose:    opts.Verbose,
	})
}

// ----- rulesync adapter ------------------------------------------------------

type rulesyncInstaller struct{}

func (rulesyncInstaller) Name() AgentInstallTarget { return AgentInstallTargetRulesync }

func (rulesyncInstaller) Install(_ context.Context, deps *Dependencies, opts AIInstallOptions) error {
	hooksPath := filepath.Join(opts.ProjectDir, ".rulesync", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		return fmt.Errorf("create .rulesync directory: %w", err)
	}
	config, err := readJSONObject(hooksPath)
	if err != nil {
		return fmt.Errorf("read rulesync hooks config: %w", err)
	}
	if _, ok := config["version"]; !ok {
		config["version"] = 1
	}

	mergeRulesyncSection(config, "claudecode", rulesyncClaudeHookEntries(opts))
	mergeRulesyncSection(config, "codexcli", rulesyncCodexHookEntries(opts))

	if err := writeJSONObject(hooksPath, config); err != nil {
		return fmt.Errorf("write rulesync hooks config: %w", err)
	}

	fmt.Printf("Installed rulesync canonical hooks in %s\n", hooksPath)
	if opts.Verbose {
		fmt.Println("  Tool overrides: claudecode, codexcli")
	}
	return nil
}

// rulesyncHookEntry is the flat schema rulesync uses inside event arrays.
type rulesyncHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
}

func rulesyncClaudeHookEntries(opts AIInstallOptions) map[string][]rulesyncHookEntry {
	issueArg := ""
	if id := strings.TrimSpace(opts.IssueID); id != "" {
		issueArg = " --issue=" + id
	}
	cmd := func(event string) string {
		return fmt.Sprintf("az ai hook run --agent=claude --json %s%s", event, issueArg)
	}
	return map[string][]rulesyncHookEntry{
		"sessionStart":       {{Type: "command", Command: cmd("session_start")}},
		"sessionEnd":         {{Type: "command", Command: cmd("session_end")}},
		"beforeSubmitPrompt": {{Type: "command", Command: cmd("user_prompt_submit")}},
		"preToolUse":         {{Type: "command", Command: cmd("pre_tool_use")}},
		"postToolUse":        {{Type: "command", Command: cmd("post_tool_use")}},
		"stop":               {{Type: "command", Command: cmd("stop")}},
		"notification":       {{Type: "command", Matcher: "idle_prompt", Command: cmd("idle_prompt")}},
		"permissionRequest":  {{Type: "command", Command: cmd("permission_request")}},
	}
}

func rulesyncCodexHookEntries(_ AIInstallOptions) map[string][]rulesyncHookEntry {
	cmd := func(event string) string {
		return fmt.Sprintf("az ai hook run --agent=codex --json %s", event)
	}
	return map[string][]rulesyncHookEntry{
		"sessionStart":       {{Type: "command", Matcher: "startup|resume", Command: cmd("session_start")}},
		"beforeSubmitPrompt": {{Type: "command", Command: cmd("user_prompt_submit")}},
		"preToolUse":         {{Type: "command", Command: cmd("pre_tool_use")}},
		"postToolUse":        {{Type: "command", Command: cmd("post_tool_use")}},
		"permissionRequest":  {{Type: "command", Command: cmd("permission_request")}},
		"stop":               {{Type: "command", Command: cmd("stop")}},
	}
}

// mergeRulesyncSection merges the supplied event→entries map into the
// per-tool override section, preserving any pre-existing user hooks and
// avoiding duplicate az-managed commands.
func mergeRulesyncSection(config map[string]any, sectionKey string, desired map[string][]rulesyncHookEntry) {
	section, _ := config[sectionKey].(map[string]any)
	if section == nil {
		section = map[string]any{}
	}
	hooks, _ := section["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	for event, entries := range desired {
		existing := normalizeAnySlice(hooks[event])
		azManagedCommands := make([]string, 0, len(entries))
		for _, entry := range entries {
			azManagedCommands = append(azManagedCommands, entry.Command)
		}
		// Strip any previously managed az command so a re-install replaces
		// stale flag changes (e.g., issue id) rather than accreting copies.
		existing = pruneRulesyncManagedEntries(existing, "az ai hook run")
		// Also strip any explicit copies of our exact target commands.
		existing = pruneRulesyncManagedEntries(existing, azManagedCommands...)
		for _, entry := range entries {
			existing = append(existing, rulesyncEntryToMap(entry))
		}
		hooks[event] = existing
	}
	section["hooks"] = hooks
	config[sectionKey] = section
}

func rulesyncEntryToMap(entry rulesyncHookEntry) map[string]any {
	out := map[string]any{
		"type":    entry.Type,
		"command": entry.Command,
	}
	if strings.TrimSpace(entry.Matcher) != "" {
		out["matcher"] = entry.Matcher
	}
	return out
}

// pruneRulesyncManagedEntries removes entries whose command starts with any
// of the supplied prefixes (or matches exactly). Used to clear stale az
// managed commands before re-emitting.
func pruneRulesyncManagedEntries(entries []any, prefixes ...string) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		typed, ok := entry.(map[string]any)
		if !ok {
			out = append(out, entry)
			continue
		}
		cmd, _ := typed["command"].(string)
		cmd = strings.TrimSpace(cmd)
		drop := false
		for _, prefix := range prefixes {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" {
				continue
			}
			if cmd == prefix || strings.HasPrefix(cmd, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return out
}

// ----- rulesync generate runner ---------------------------------------------

// rulesyncRunnerCmd represents a chosen invocation of `rulesync generate`.
type rulesyncRunnerCmd struct {
	Argv []string
	Why  string // human-readable detection reason
}

// resolveRulesyncRunner picks the right way to invoke rulesync for this
// project, in precedence order: pnpm > bun > yarn > npm > PATH binary.
func resolveRulesyncRunner(projectDir string) (rulesyncRunnerCmd, bool) {
	type detection struct {
		marker string
		argv   []string
		why    string
	}
	detections := []detection{
		{"pnpm-lock.yaml", []string{"pnpm", "exec", "rulesync", "generate"}, "pnpm (detected pnpm-lock.yaml)"},
		{"bun.lockb", []string{"bunx", "rulesync", "generate"}, "bun (detected bun.lockb)"},
		{"bun.lock", []string{"bunx", "rulesync", "generate"}, "bun (detected bun.lock)"},
		{"yarn.lock", []string{"yarn", "rulesync", "generate"}, "yarn (detected yarn.lock)"},
		{"package-lock.json", []string{"npm", "exec", "--", "rulesync", "generate"}, "npm (detected package-lock.json)"},
	}
	for _, d := range detections {
		if !fileExists(filepath.Join(projectDir, d.marker)) {
			continue
		}
		if _, err := exec.LookPath(d.argv[0]); err != nil {
			continue
		}
		return rulesyncRunnerCmd{Argv: d.argv, Why: d.why}, true
	}
	if _, err := exec.LookPath("rulesync"); err == nil {
		return rulesyncRunnerCmd{Argv: []string{"rulesync", "generate"}, Why: "PATH-installed rulesync"}, true
	}
	return rulesyncRunnerCmd{}, false
}

func runRulesyncGenerate(ctx context.Context, projectDir string, verbose bool) error {
	runner, ok := resolveRulesyncRunner(projectDir)
	if !ok {
		fmt.Fprintln(os.Stderr, "rulesync generate skipped: no package-manager lockfile and rulesync not on PATH.")
		fmt.Fprintln(os.Stderr, "  Install rulesync (e.g. pnpm add -D rulesync) and run `<pkg-mgr> rulesync generate`.")
		return nil
	}
	if verbose {
		fmt.Printf("Running rulesync generate via %s: %s\n", runner.Why, strings.Join(runner.Argv, " "))
	} else {
		fmt.Printf("Running %s ...\n", strings.Join(runner.Argv, " "))
	}
	cmd := exec.CommandContext(ctx, runner.Argv[0], runner.Argv[1:]...)
	cmd.Dir = projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(runner.Argv, " "), err)
	}
	return nil
}

