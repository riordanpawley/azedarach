package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	hookEventIdlePrompt        = "idle_prompt"
	hookEventPermissionRequest = "permission_request"
	hookEventSessionStart      = "session_start"
	hookEventUserPromptSubmit  = "user_prompt_submit"
	hookEventPreToolUse        = "pre_tool_use"
	hookEventPostToolUse       = "post_tool_use"
	hookEventStop              = "stop"
	hookEventSessionEnd        = "session_end"
	openCodePluginFilename     = "opencode-az.js"
	commandDevServerList       = "devserver.list"
	gitHookManagedBlockStart   = "# >>> azedarach managed githook >>>"
	gitHookManagedBlockEnd     = "# <<< azedarach managed githook <<<"
)

var hookEventStatuses = map[string]string{
	hookEventIdlePrompt:        "waiting",
	hookEventPermissionRequest: "waiting",
	hookEventSessionStart:      "started",
	hookEventUserPromptSubmit:  "active",
	hookEventPreToolUse:        "running_tool",
	hookEventPostToolUse:       "active",
	hookEventStop:              "stopped",
	hookEventSessionEnd:        "ended",
}

type NotifyOptions struct {
	Event   string
	IssueID string
	JSON    bool
	Verbose bool
}

type HooksInstallOptions struct {
	IssueID    string
	ProjectDir string
	Verbose    bool
}

type GitHooksInstallOptions struct {
	ProjectDir string
	Verbose    bool
}

type GitHooksRunOptions struct {
	ProjectDir string
	Verbose    bool
	HookArgs   []string
}

type GitHooksNotifyOptions struct {
	ProjectDir string
	Hook       string
	Verbose    bool
	HookArgs   []string
}

type GitHooksHookOptions struct {
	ProjectDir string
	Hook       string
	Verbose    bool
	HookArgs   []string
}

type GateOptions struct {
	IssueID    string
	ProjectDir string
	Verbose    bool
	Fix        bool
}

type devServerListRequestBody struct {
	ProjectID string `json:"project_id"`
}

type devServerListResponseBody struct {
	ProjectID string          `json:"project_id"`
	Servers   []devServerView `json:"servers"`
}

type devServerView struct {
	Name    string        `json:"name"`
	Port    int           `json:"port"`
	Status  string        `json:"status"`
	IssueID string        `json:"issue_id"`
	Uptime  time.Duration `json:"uptime"`
}

type OpenCodeInitOptions struct {
	ProjectDir string
	Verbose    bool
}

type OpenCodePluginInstallOptions struct {
	GlobalDir  string
	ProjectDir string
	Verbose    bool
}

type CodexInstallOptions struct {
	ProjectDir string
	Verbose    bool
}

type CodexGuardOptions struct {
	Event string
	JSON  bool
}

type CodexHookRunOptions struct {
	Event string
	JSON  bool
}

func ParseNotifyArgs(args []string) (NotifyOptions, error) {
	opts := NotifyOptions{}
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "hook-json output")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return NotifyOptions{}, err
	}
	positionals := fs.Args()
	if len(positionals) < 1 || len(positionals) > 2 {
		return NotifyOptions{}, fmt.Errorf("usage: az notify [--json] [--verbose] <event> [<issue-id>]")
	}
	for _, arg := range positionals {
		if strings.HasPrefix(arg, "-") {
			return NotifyOptions{}, fmt.Errorf("flags must come before positional arguments")
		}
	}
	opts.Event = positionals[0]
	if len(positionals) == 2 {
		opts.IssueID = positionals[1]
	}
	if _, ok := hookEventStatuses[opts.Event]; !ok {
		return NotifyOptions{}, fmt.Errorf("invalid event type: %s", opts.Event)
	}
	return opts, nil
}

func ParseHooksInstallArgs(args []string) (HooksInstallOptions, error) {
	opts := HooksInstallOptions{}
	fs := flag.NewFlagSet("hooks install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return HooksInstallOptions{}, err
	}
	if fs.NArg() != 1 {
		return HooksInstallOptions{}, fmt.Errorf("usage: az hooks install <issue-id> [--project-dir <dir>] [--verbose]")
	}
	opts.IssueID = fs.Arg(0)
	return opts, nil
}

func ParseGitHooksInstallArgs(args []string) (GitHooksInstallOptions, error) {
	opts := GitHooksInstallOptions{}
	fs := flag.NewFlagSet("githooks install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return GitHooksInstallOptions{}, err
	}
	if fs.NArg() != 0 {
		return GitHooksInstallOptions{}, fmt.Errorf("usage: az githooks install [--project-dir <dir>] [--verbose] (alias: az githooks update)")
	}
	return opts, nil
}

func ParseGitHooksRunArgs(args []string) (GitHooksRunOptions, error) {
	opts := GitHooksRunOptions{}
	fs := flag.NewFlagSet("githooks run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return GitHooksRunOptions{}, err
	}
	opts.HookArgs = append([]string(nil), fs.Args()...)
	return opts, nil
}

func ParseGitHooksNotifyArgs(args []string) (GitHooksNotifyOptions, error) {
	opts := GitHooksNotifyOptions{}
	fs := flag.NewFlagSet("githooks notify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.StringVar(&opts.Hook, "hook", "", "hook name")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return GitHooksNotifyOptions{}, err
	}
	opts.HookArgs = append([]string(nil), fs.Args()...)
	return opts, nil
}

func ParseGitHooksHookArgs(args []string) (GitHooksHookOptions, error) {
	opts := GitHooksHookOptions{}
	fs := flag.NewFlagSet("githooks hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.StringVar(&opts.Hook, "hook", "", "hook name")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")
	if err := fs.Parse(args); err != nil {
		return GitHooksHookOptions{}, err
	}
	opts.Hook = strings.TrimSpace(opts.Hook)
	if opts.Hook == "" {
		return GitHooksHookOptions{}, fmt.Errorf("usage: az githooks hook --hook <name> [--project-dir <dir>] [--verbose] [hook-args...]")
	}
	opts.HookArgs = append([]string(nil), fs.Args()...)
	return opts, nil
}

func ParseGateArgs(args []string) (GateOptions, error) {
	opts := GateOptions{}
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")
	fs.BoolVar(&opts.Fix, "fix", false, "auto-fix lint issues")

	if err := fs.Parse(args); err != nil {
		return GateOptions{}, err
	}
	if fs.NArg() != 1 {
		return GateOptions{}, fmt.Errorf("usage: az gate <issue-id> [--project-dir <dir>] [--verbose] [--fix]")
	}
	opts.IssueID = fs.Arg(0)
	return opts, nil
}

func ParseOpenCodeInitArgs(args []string) (OpenCodeInitOptions, error) {
	opts := OpenCodeInitOptions{}
	fs := flag.NewFlagSet("opencode init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return OpenCodeInitOptions{}, err
	}
	if fs.NArg() != 0 {
		return OpenCodeInitOptions{}, fmt.Errorf("usage: az opencode init [--project-dir <dir>] [--verbose]")
	}
	return opts, nil
}

func ParseOpenCodePluginInstallArgs(args []string) (OpenCodePluginInstallOptions, error) {
	opts := OpenCodePluginInstallOptions{}
	fs := flag.NewFlagSet("opencode plugin install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.GlobalDir, "global-dir", "", "global plugin directory")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return OpenCodePluginInstallOptions{}, err
	}
	if fs.NArg() != 0 {
		return OpenCodePluginInstallOptions{}, fmt.Errorf("usage: az opencode plugin install [--global-dir <dir>] [--project-dir <dir>] [--verbose]")
	}
	return opts, nil
}

func ParseCodexInstallArgs(args []string) (CodexInstallOptions, error) {
	opts := CodexInstallOptions{}
	fs := flag.NewFlagSet("codex install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return CodexInstallOptions{}, err
	}
	if fs.NArg() != 0 {
		return CodexInstallOptions{}, fmt.Errorf("usage: az codex install [--project-dir <dir>] [--verbose]")
	}
	return opts, nil
}

func ParseCodexGuardArgs(args []string) (CodexGuardOptions, error) {
	opts := CodexGuardOptions{}
	fs := flag.NewFlagSet("codex guard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "hook-json output")
	if err := fs.Parse(args); err != nil {
		return CodexGuardOptions{}, err
	}
	if fs.NArg() != 1 {
		return CodexGuardOptions{}, fmt.Errorf("usage: az codex guard [--json] <session-start|user-prompt-submit|pre-tool-use|post-tool-use|stop>")
	}
	opts.Event = strings.TrimSpace(fs.Arg(0))
	if !isCodexGuardEvent(opts.Event) {
		return CodexGuardOptions{}, fmt.Errorf("unsupported codex guard event: %s", opts.Event)
	}
	return opts, nil
}

func ParseCodexHookRunArgs(args []string) (CodexHookRunOptions, error) {
	opts := CodexHookRunOptions{}
	fs := flag.NewFlagSet("codex hook run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "hook-json output")
	if err := fs.Parse(args); err != nil {
		return CodexHookRunOptions{}, err
	}
	if fs.NArg() != 1 {
		return CodexHookRunOptions{}, fmt.Errorf("usage: az codex hook run [--json] <session-start|user-prompt-submit|pre-tool-use|post-tool-use|stop>")
	}
	opts.Event = strings.TrimSpace(fs.Arg(0))
	if !isCodexGuardEvent(opts.Event) {
		return CodexHookRunOptions{}, fmt.Errorf("unsupported codex hook event: %s", opts.Event)
	}
	return opts, nil
}

func isCodexGuardEvent(event string) bool {
	switch event {
	case "session-start", "user-prompt-submit", "pre-tool-use", "post-tool-use", "stop":
		return true
	default:
		return false
	}
}

func NotifyCommand(deps *Dependencies, opts NotifyOptions) error {
	issueID := strings.TrimSpace(opts.IssueID)
	if issueID == "" {
		issueID = strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	}
	if issueID != "" && deps != nil && deps.DaemonClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := notifyDaemonSessionStatus(ctx, deps, issueID, opts.Event); err != nil && opts.Verbose {
			fmt.Fprintf(os.Stderr, "notify daemon update failed: %v\n", err)
		}
	}

	output, err := renderNotifyOutput(opts)
	if err != nil {
		return err
	}
	fmt.Println(output)
	return nil
}

func notifyDaemonSessionStatus(ctx context.Context, deps *Dependencies, issueID, event string) error {
	switch event {
	case hookEventIdlePrompt, hookEventPermissionRequest, hookEventStop, hookEventSessionEnd:
		_, err := deps.DaemonClient.PauseSession(ctx, issueID)
		return err
	case hookEventSessionStart, hookEventUserPromptSubmit, hookEventPreToolUse, hookEventPostToolUse:
		_, err := deps.DaemonClient.ResumeSession(ctx, issueID)
		return err
	default:
		return nil
	}
}

func renderNotifyOutput(opts NotifyOptions) (string, error) {
	status, ok := hookEventStatuses[opts.Event]
	if !ok {
		return "", fmt.Errorf("invalid event type: %s", opts.Event)
	}

	if opts.JSON {
		// Hook-compatible command output: an empty JSON object is accepted by Codex
		// hook schemas and avoids text parsing failures.
		return "{}", nil
	}

	if opts.Verbose {
		if strings.TrimSpace(opts.IssueID) != "" {
			return fmt.Sprintf("Hook notification: %s for %s -> %s", opts.Event, opts.IssueID, status), nil
		}
		return fmt.Sprintf("Hook notification: %s -> %s", opts.Event, status), nil
	}

	if strings.TrimSpace(opts.IssueID) != "" {
		return fmt.Sprintf("Hook notification: %s -> %s", opts.IssueID, status), nil
	}
	return fmt.Sprintf("Hook notification: %s -> %s", opts.Event, status), nil
}

func HooksInstallCommand(deps *Dependencies, opts HooksInstallOptions) error {
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" && deps != nil {
		projectDir = deps.RepoDir
	}
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve project directory: %w", err)
		}
		projectDir = cwd
	}

	settingsPath := filepath.Join(projectDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}

	settings, err := readJSONObject(settingsPath)
	if err != nil {
		return fmt.Errorf("read hooks settings: %w", err)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	hooks["Notification"] = mergeHookEntries(
		hooks["Notification"],
		map[string]any{
			"matcher": hookEventIdlePrompt,
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": fmt.Sprintf("az notify %s %s", hookEventIdlePrompt, opts.IssueID),
				},
			},
		},
		fmt.Sprintf("az notify %s %s", hookEventIdlePrompt, opts.IssueID),
	)
	hooks["PermissionRequest"] = mergeHookEntries(
		hooks["PermissionRequest"],
		map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": fmt.Sprintf("az notify %s %s", hookEventPermissionRequest, opts.IssueID),
				},
			},
		},
		fmt.Sprintf("az notify %s %s", hookEventPermissionRequest, opts.IssueID),
	)
	hooks["Stop"] = mergeHookEntries(
		hooks["Stop"],
		map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": fmt.Sprintf("az notify %s %s", hookEventStop, opts.IssueID),
				},
			},
		},
		fmt.Sprintf("az notify %s %s", hookEventStop, opts.IssueID),
	)
	hooks["SessionEnd"] = mergeHookEntries(
		hooks["SessionEnd"],
		map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": fmt.Sprintf("az notify %s %s", hookEventSessionEnd, opts.IssueID),
				},
			},
		},
		fmt.Sprintf("az notify %s %s", hookEventSessionEnd, opts.IssueID),
	)

	settings["hooks"] = hooks
	if err := writeJSONObject(settingsPath, settings); err != nil {
		return fmt.Errorf("write hooks settings: %w", err)
	}

	fmt.Printf("Installed hooks for issue %s\n", opts.IssueID)
	fmt.Printf("  File: %s\n", settingsPath)
	fmt.Println("  Events: idle_prompt, permission_request, stop, session_end")
	return nil
}

func GitHooksInstallCommand(deps *Dependencies, opts GitHooksInstallOptions) error {
	projectDir, err := resolveProjectDir(opts.ProjectDir, deps)
	if err != nil {
		return err
	}

	hooksDir := filepath.Join(projectDir, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create .githooks directory: %w", err)
	}

	type gitHookInstallSpec struct {
		command string
		legacy  string
	}
	hookScripts := map[string]gitHookInstallSpec{
		"pre-commit": {
			command: "az githooks hook --hook pre-commit \"$@\" >/dev/null 2>&1 || true",
			legacy:  "#!/usr/bin/env sh\nset -eu\naz githooks run \"$@\"\n",
		},
		"post-commit": {
			command: "az githooks hook --hook post-commit \"$@\" >/dev/null 2>&1 || true",
			legacy:  "#!/usr/bin/env sh\nset -eu\naz githooks notify --hook post-commit >/dev/null 2>&1 || true\n",
		},
		"post-merge": {
			command: "az githooks hook --hook post-merge \"$@\" >/dev/null 2>&1 || true",
			legacy:  "#!/usr/bin/env sh\nset -eu\naz githooks notify --hook post-merge >/dev/null 2>&1 || true\n",
		},
		"post-checkout": {
			command: "az githooks hook --hook post-checkout \"$@\" >/dev/null 2>&1 || true",
			legacy:  "#!/usr/bin/env sh\nset -eu\naz githooks notify --hook post-checkout >/dev/null 2>&1 || true\n",
		},
		"post-rewrite": {
			command: "az githooks hook --hook post-rewrite \"$@\" >/dev/null 2>&1 || true",
			legacy:  "#!/usr/bin/env sh\nset -eu\naz githooks notify --hook post-rewrite >/dev/null 2>&1 || true\n",
		},
	}
	for hookName, spec := range hookScripts {
		hookPath := filepath.Join(hooksDir, hookName)
		if err := upsertManagedGitHookFile(hookPath, spec.command, spec.legacy); err != nil {
			return fmt.Errorf("write %s hook: %w", hookName, err)
		}
	}
	if err := setGitHooksPath(projectDir, ".githooks"); err != nil {
		return fmt.Errorf("set git core.hooksPath: %w", err)
	}

	fmt.Printf("Installed/updated git hooks in %s\n", hooksDir)
	fmt.Println("Configured git core.hooksPath=.githooks")
	if opts.Verbose {
		fmt.Println("preserved existing hook scripts and upserted Azedarach-managed blocks")
		fmt.Println("use az githooks update to refresh managed hook commands after future Azedarach changes")
	}
	return nil
}

func GitHooksRunCommand(deps *Dependencies, opts GitHooksRunOptions) error {
	return GitHooksHookCommand(deps, GitHooksHookOptions{
		ProjectDir: opts.ProjectDir,
		Hook:       "pre-commit",
		Verbose:    opts.Verbose,
		HookArgs:   append([]string(nil), opts.HookArgs...),
	})
}

func GitHooksNotifyCommand(deps *Dependencies, opts GitHooksNotifyOptions) error {
	return GitHooksHookCommand(deps, GitHooksHookOptions{
		ProjectDir: opts.ProjectDir,
		Hook:       strings.TrimSpace(opts.Hook),
		Verbose:    opts.Verbose,
		HookArgs:   append([]string(nil), opts.HookArgs...),
	})
}

func GitHooksHookCommand(deps *Dependencies, opts GitHooksHookOptions) error {
	projectDir, err := resolveProjectDir(opts.ProjectDir, deps)
	if err != nil {
		return err
	}
	cfg, err := loadConfigForHook(projectDir, deps)
	if err != nil {
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "githooks hook: load config failed: %v\n", err)
		}
		return nil
	}
	hookName := strings.TrimSpace(opts.Hook)
	if hookName == "" {
		if opts.Verbose {
			fmt.Fprintln(os.Stderr, "githooks hook: hook name is required")
		}
		return nil
	}

	for _, command := range configuredCommandsForHook(cfg, hookName) {
		if opts.Verbose {
			fmt.Printf("githooks %s: %s\n", hookName, command)
		}
		if runErr := runShellCommand(projectDir, command); runErr != nil {
			if !cfg.GitHooks.BestEffort {
				return fmt.Errorf("githooks %s command failed: %w", hookName, runErr)
			}
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "githooks %s command failed: %v\n", hookName, runErr)
			}
		}
	}
	if cfg.GitHooks.Restage.Enabled {
		if err := restageHookChanges(projectDir, cfg.GitHooks.Restage.Paths); err != nil {
			if !cfg.GitHooks.BestEffort {
				return fmt.Errorf("githooks %s restage failed: %w", hookName, err)
			}
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "githooks %s restage failed: %v\n", hookName, err)
			}
		}
	}

	return reconcileDaemonGitState(projectDir, deps, hookName, opts.Verbose)
}

func loadConfigForHook(projectDir string, deps *Dependencies) (*config.Config, error) {
	if deps != nil && deps.Config != nil {
		return deps.Config, nil
	}
	cfg, err := config.LoadConfig(projectDir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func configuredCommandsForHook(cfg *config.Config, hookName string) []string {
	commands := make([]string, 0)
	if cfg != nil && cfg.GitHooks.Commands != nil {
		for _, command := range cfg.GitHooks.Commands[hookName] {
			command = strings.TrimSpace(command)
			if command != "" {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

func restageHookChanges(projectDir string, paths []string) error {
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" {
			cleaned = append(cleaned, path)
		}
	}
	if len(cleaned) == 0 {
		return runShellCommand(projectDir, "git add -A")
	}
	for _, path := range cleaned {
		if err := runShellCommand(projectDir, "git add -A -- "+shellSingleQuote(path)); err != nil {
			return err
		}
	}
	return nil
}

func reconcileDaemonGitState(projectDir string, deps *Dependencies, hookName string, verbose bool) error {
	worktreeRoot, err := config.ResolveWorktreeRoot(projectDir)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "githooks notify: resolve worktree root failed: %v\n", err)
		}
		return nil
	}
	if deps == nil || deps.DaemonClient == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := deps.DaemonClient.GitStatus(ctx, worktreeRoot); err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "githooks hook: daemon git status refresh failed for %s: %v\n", worktreeRoot, err)
		}
		return nil
	}
	if verbose {
		fmt.Printf("githooks hook: refreshed daemon git state for %s (%s)\n", worktreeRoot, hookName)
	}
	return nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func GateCommand(_ *Dependencies, opts GateOptions) error {
	if strings.TrimSpace(opts.IssueID) == "" {
		return fmt.Errorf("issue id is required")
	}

	if opts.Verbose {
		fmt.Printf("Running quality gates for: %s\n", opts.IssueID)
	} else {
		fmt.Printf("Quality gate requested for: %s\n", opts.IssueID)
	}
	if strings.TrimSpace(opts.ProjectDir) != "" {
		fmt.Printf("  Project dir: %s\n", opts.ProjectDir)
	}
	if opts.Fix {
		fmt.Println("  Fix mode requested; no automated gates are wired in go-bubbletea yet.")
	}
	fmt.Println("  go-bubbletea exposes the family and help surface; full gate automation is still being wired.")
	return nil
}

func ParseDevIssueArg(command string, args []string) (string, error) {
	if len(args) == 0 || isHelpArg(args[0]) {
		return "", fmt.Errorf("usage: az dev %s <issue-id>", command)
	}
	if len(args) != 1 {
		return "", fmt.Errorf("usage: az dev %s <issue-id>", command)
	}
	return strings.TrimSpace(args[0]), nil
}

func DevServerStartCommand(deps *Dependencies, issueID string) error {
	srv, err := deps.DaemonClient.StartDevServer(context.Background(), issueID)
	if err != nil {
		return err
	}
	printDevServerAction("Started", issueID, devServerView{
		Name:    srv.Name,
		Port:    srv.Port,
		Status:  srv.Status,
		IssueID: srv.IssueID,
		Uptime:  srv.Uptime,
	})
	return nil
}

func DevServerStopCommand(deps *Dependencies, issueID string) error {
	srv, err := deps.DaemonClient.StopDevServer(context.Background(), issueID)
	if err != nil {
		return err
	}
	printDevServerAction("Stopped", issueID, devServerView{
		Name:    srv.Name,
		Port:    srv.Port,
		Status:  srv.Status,
		IssueID: srv.IssueID,
		Uptime:  srv.Uptime,
	})
	return nil
}

func DevServerRestartCommand(deps *Dependencies, issueID string) error {
	srv, err := deps.DaemonClient.RestartDevServer(context.Background(), issueID)
	if err != nil {
		return err
	}
	printDevServerAction("Restarted", issueID, devServerView{
		Name:    srv.Name,
		Port:    srv.Port,
		Status:  srv.Status,
		IssueID: srv.IssueID,
		Uptime:  srv.Uptime,
	})
	return nil
}

func DevServerStatusCommand(deps *Dependencies, issueID string) error {
	srv, err := deps.DaemonClient.DevServerStatus(context.Background(), issueID)
	if err != nil {
		return err
	}
	printDevServerStatus(issueID, devServerView{
		Name:    srv.Name,
		Port:    srv.Port,
		Status:  srv.Status,
		IssueID: srv.IssueID,
		Uptime:  srv.Uptime,
	})
	return nil
}

func DevServerListCommand(deps *Dependencies) error {
	ctx := context.Background()
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       fmt.Sprintf("%s-%d", commandDevServerList, time.Now().UTC().UnixNano()),
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: deps.ProjectID,
		},
		Command: commandDevServerList,
		SentAt:  time.Now().UTC(),
		Body: mustJSONBody(devServerListRequestBody{
			ProjectID: deps.ProjectID,
		}),
	}
	resp, err := deps.DaemonClient.Command(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to list dev servers: %w", err)
	}
	if err := responseError(resp, "failed to list dev servers"); err != nil {
		return err
	}

	var out devServerListResponseBody
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return fmt.Errorf("decode dev server list: %w", err)
		}
	}
	printDevServerList(out.Servers)
	return nil
}

func mustJSONBody(v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return body
}

func printDevServerAction(action, issueID string, srv devServerView) {
	name := strings.TrimSpace(srv.Name)
	if name == "" {
		name = issueID
	}
	fmt.Printf("%s dev server '%s' for %s\n", action, name, issueID)
	if srv.Port > 0 {
		fmt.Printf("  Port: %d\n", srv.Port)
	}
}

func printDevServerStatus(issueID string, srv devServerView) {
	name := strings.TrimSpace(srv.Name)
	if name == "" {
		name = issueID
	}
	fmt.Printf("Dev server '%s' for %s\n", name, issueID)
	fmt.Printf("  Status: %s\n", srv.Status)
	if srv.Port > 0 {
		fmt.Printf("  Port: %d\n", srv.Port)
	}
	if uptime := formatDevServerUptime(srv.Uptime); uptime != "" {
		fmt.Printf("  Uptime: %s\n", uptime)
	}
}

func printDevServerList(servers []devServerView) {
	running := make([]devServerView, 0, len(servers))
	for _, srv := range servers {
		if strings.EqualFold(strings.TrimSpace(srv.Status), "running") {
			running = append(running, srv)
		}
	}
	if len(running) == 0 {
		fmt.Println("No dev servers running.")
		return
	}

	sort.Slice(running, func(i, j int) bool {
		if running[i].IssueID != running[j].IssueID {
			return running[i].IssueID < running[j].IssueID
		}
		return running[i].Name < running[j].Name
	})

	fmt.Println("Running dev servers:")
	fmt.Println("")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ISSUE\tSERVER\tPORT\tUPTIME")
	fmt.Fprintln(w, "-------\t------\t----\t------")
	for _, srv := range running {
		name := strings.TrimSpace(srv.Name)
		if name == "" {
			name = srv.IssueID
		}
		port := "-"
		if srv.Port > 0 {
			port = fmt.Sprintf("%d", srv.Port)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", srv.IssueID, name, port, formatDevServerUptime(srv.Uptime))
	}
	_ = w.Flush()

	fmt.Println("")
	fmt.Printf("%d server(s) running\n", len(running))
}

func formatDevServerUptime(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	seconds := int(d.Seconds())
	minutes := seconds / 60
	hours := minutes / 60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes%60)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds%60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func isHelpArg(arg string) bool {
	return strings.EqualFold(arg, "help") || arg == "-h" || arg == "--help"
}

func OpenCodeInitCommand(deps *Dependencies, opts OpenCodeInitOptions) error {
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" && deps != nil {
		projectDir = deps.RepoDir
	}
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve project directory: %w", err)
		}
		projectDir = cwd
	}

	configPath := filepath.Join(projectDir, "opencode.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("prepare opencode config directory: %w", err)
	}

	config, err := readJSONObject(configPath)
	if err != nil {
		return fmt.Errorf("read opencode config: %w", err)
	}

	if _, ok := config["$schema"]; !ok {
		config["$schema"] = "https://opencode.ai/config.json"
	}
	if _, ok := config["instructions"]; !ok {
		config["instructions"] = []string{"CLAUDE.md"}
	}
	if _, ok := config["theme"]; !ok {
		config["theme"] = "tokyonight"
	}
	config["plugins"] = mergeStrings(config["plugins"], "opencode-tracker")

	if err := writeJSONObject(configPath, config); err != nil {
		return fmt.Errorf("write opencode config: %w", err)
	}

	fmt.Printf("Initialized OpenCode support in %s\n", projectDir)
	fmt.Printf("  Config: %s\n", configPath)
	if opts.Verbose {
		fmt.Printf("  Plugins: %s\n", strings.Join(normalizeStrings(config["plugins"]), ", "))
	}
	return nil
}

func OpenCodePluginInstallCommand(deps *Dependencies, opts OpenCodePluginInstallOptions) error {
	globalDir := strings.TrimSpace(opts.GlobalDir)
	if globalDir == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			if deps != nil && strings.TrimSpace(deps.RepoDir) != "" {
				configDir = filepath.Join(deps.RepoDir, ".config")
			} else {
				return fmt.Errorf("resolve user config dir: %w", err)
			}
		}
		globalDir = filepath.Join(configDir, "opencode", "plugins")
	}

	globalPath := filepath.Join(globalDir, openCodePluginFilename)
	if err := writePlaceholderFile(globalPath, openCodePluginSource()); err != nil {
		return fmt.Errorf("install global opencode plugin: %w", err)
	}

	if strings.TrimSpace(opts.ProjectDir) != "" {
		projectPath := filepath.Join(opts.ProjectDir, ".opencode", "plugins", openCodePluginFilename)
		if err := writePlaceholderFile(projectPath, openCodePluginSource()); err != nil {
			return fmt.Errorf("install project opencode plugin: %w", err)
		}
		fmt.Printf("Installed project plugin: %s\n", projectPath)
	}

	fmt.Printf("Installed global plugin: %s\n", globalPath)
	if opts.Verbose {
		fmt.Println("  OpenCode plugin source is a pragmatic placeholder in go-bubbletea.")
	}
	return nil
}

func CodexInstallCommand(deps *Dependencies, opts CodexInstallOptions) error {
	projectDir, err := resolveProjectDir(opts.ProjectDir, deps)
	if err != nil {
		return err
	}

	hooksPath := filepath.Join(projectDir, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		return fmt.Errorf("create codex hooks directory: %w", err)
	}

	hooksConfig, err := readJSONObject(hooksPath)
	if err != nil {
		return fmt.Errorf("read codex hooks config: %w", err)
	}

	hooks, _ := hooksConfig["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	mergeCodexHookEntry := func(eventName, command, matcher string) {
		entry := map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": command,
				},
			},
		}
		if matcher != "" {
			entry["matcher"] = matcher
		}
		hooks[eventName] = mergeHookEntries(hooks[eventName], entry, command)
	}

	type codexHookInstallSpec struct {
		eventName   string
		notifyEvent string
		guardEvent  string
		matcher     string
	}
	specs := []codexHookInstallSpec{
		{eventName: "SessionStart", notifyEvent: hookEventSessionStart, guardEvent: "session-start", matcher: "startup|resume"},
		{eventName: "UserPromptSubmit", notifyEvent: hookEventUserPromptSubmit, guardEvent: "user-prompt-submit"},
		// {eventName: "PreToolUse", notifyEvent: hookEventPreToolUse, guardEvent: "pre-tool-use"},
		// Intentionally disabled for now: current Codex clients print very noisy
		// per-tool hook status lines ("Running PreToolUse hook"), which overwhelms
		// normal output when multiple tools run in quick succession.
		{eventName: "PostToolUse", notifyEvent: hookEventPostToolUse, guardEvent: "post-tool-use"},
		{eventName: "Stop", notifyEvent: hookEventStop, guardEvent: "stop"},
	}
	for _, spec := range specs {
		legacyNotifyCommand := fmt.Sprintf("az notify --json %s", spec.notifyEvent)
		legacyGuardCommand := fmt.Sprintf("az codex guard --json %s", spec.guardEvent)
		combinedCommand := fmt.Sprintf("az codex hook run --json %s", spec.guardEvent)
		hooks[spec.eventName] = removeHookCommands(hooks[spec.eventName], legacyNotifyCommand, legacyGuardCommand, combinedCommand)
		shouldInstall := spec.eventName == "SessionStart" || spec.eventName == "Stop"
		if shouldInstall {
			mergeCodexHookEntry(spec.eventName, combinedCommand, spec.matcher)
		} else if len(normalizeAnySlice(hooks[spec.eventName])) == 0 {
			delete(hooks, spec.eventName)
		}
	}

	hooksConfig["hooks"] = hooks
	if err := writeJSONObject(hooksPath, hooksConfig); err != nil {
		return fmt.Errorf("write codex hooks config: %w", err)
	}

	fmt.Printf("Installed Codex hooks in %s\n", hooksPath)
	if opts.Verbose {
		fmt.Println("  Events: SessionStart, Stop")
	}
	return nil
}

func CodexHookRunCommand(deps *Dependencies, opts CodexHookRunOptions) error {
	projectDir, err := resolveProjectDir("", deps)
	if err != nil {
		return err
	}
	payloadMap, err := parseHookPayload(os.Stdin)
	if err != nil {
		return err
	}

	notifyEvent, err := codexNotifyEventForGuardEvent(opts.Event)
	if err != nil {
		return err
	}
	if !opts.JSON {
		notifyOutput, err := renderNotifyOutput(NotifyOptions{Event: notifyEvent})
		if err != nil {
			return err
		}
		fmt.Println(notifyOutput)
	}

	response, err := codexGuardResponse(projectDir, CodexGuardOptions{Event: opts.Event}, payloadMap)
	if err != nil {
		return err
	}
	if opts.JSON {
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	printCodexGuardResponse(response)
	return nil
}

type codexGuardState struct {
	Threads map[string]codexGuardThreadState `json:"threads"`
}

type codexGuardThreadState struct {
	Primed       bool      `json:"primed"`
	NeedsRefresh bool      `json:"needs_refresh"`
	LastPrimeAt  time.Time `json:"last_prime_at,omitempty"`
}

func CodexGuardCommand(deps *Dependencies, opts CodexGuardOptions) error {
	projectDir, err := resolveProjectDir("", deps)
	if err != nil {
		return err
	}
	payloadMap, err := parseHookPayload(os.Stdin)
	if err != nil {
		return err
	}
	response, err := codexGuardResponse(projectDir, opts, payloadMap)
	if err != nil {
		return err
	}
	if opts.JSON {
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	printCodexGuardResponse(response)
	return nil
}

func parseHookPayload(r io.Reader) (map[string]any, error) {
	payload, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read hook payload: %w", err)
	}
	payloadMap := map[string]any{}
	if len(strings.TrimSpace(string(payload))) > 0 {
		_ = json.Unmarshal(payload, &payloadMap)
	}
	return payloadMap, nil
}

func codexNotifyEventForGuardEvent(event string) (string, error) {
	switch event {
	case "session-start":
		return hookEventSessionStart, nil
	case "user-prompt-submit":
		return hookEventUserPromptSubmit, nil
	case "pre-tool-use":
		return hookEventPreToolUse, nil
	case "post-tool-use":
		return hookEventPostToolUse, nil
	case "stop":
		return hookEventStop, nil
	default:
		return "", fmt.Errorf("unsupported codex hook event: %s", event)
	}
}

func codexGuardResponse(projectDir string, opts CodexGuardOptions, payloadMap map[string]any) (map[string]any, error) {
	threadID := codexGuardThreadID(payloadMap)
	if threadID == "" {
		threadID = "default"
	}
	statePath := filepath.Join(projectDir, ".azedarach", "codex-guard-state.json")
	state := readCodexGuardState(statePath)
	threadState := state.Threads[threadID]

	response := map[string]any{}
	switch opts.Event {
	case "session-start":
		threadState = codexGuardThreadState{}
		state.Threads[threadID] = threadState
		if !codexGuardPromptMentionsPrime(payloadMap) {
			response["systemMessage"] = "Run `az prime` now before any other shell commands."
		}
	case "user-prompt-submit":
		if codexGuardCompactionDetected(payloadMap) {
			threadState.Primed = false
			threadState.NeedsRefresh = true
			state.Threads[threadID] = threadState
			response["systemMessage"] = "Context compaction detected. Run `az prime` to refresh issue context."
		}
	case "pre-tool-use":
		command := codexGuardCommandFromPayload(payloadMap)
		if codexGuardIsPrimeCommand(command) {
			// Temporary tradeoff: we mark prime success during PreToolUse to reduce Codex
			// hook spam by removing PostToolUse hooks. Revert this once hook noise is fixed.
			threadState.Primed = true
			threadState.NeedsRefresh = false
			threadState.LastPrimeAt = time.Now().UTC()
			state.Threads[threadID] = threadState
			break
		}
		if strings.TrimSpace(command) != "" && (!threadState.Primed || threadState.NeedsRefresh) {
			response["decision"] = "block"
			if threadState.NeedsRefresh {
				response["reason"] = "Run `az prime` before continuing so your context is refreshed after compaction."
			} else {
				response["reason"] = "Run `az prime` before any other shell command in this session."
			}
		}
	case "post-tool-use":
	case "stop":
		delete(state.Threads, threadID)
	}

	if err := writeCodexGuardState(statePath, state); err != nil {
		return nil, err
	}
	return response, nil
}

func printCodexGuardResponse(response map[string]any) {
	if len(response) == 0 {
		fmt.Println("codex guard: allow")
		return
	}
	if message, ok := response["systemMessage"].(string); ok {
		fmt.Println(message)
	}
	if reason, ok := response["reason"].(string); ok {
		fmt.Println(reason)
	}
}

func PrintHooksUsage() {
	fmt.Println("Usage: az hooks install <issue-id> [--project-dir <dir>] [--verbose]")
	fmt.Println("Manage Claude Code hook configuration for session detection.")
}

func PrintGitHooksUsage() {
	fmt.Println("Usage: az githooks <install|update|run|notify|hook> [--project-dir <dir>] [--verbose]")
	fmt.Println("Manage repository git hooks and execute configured hook tasks.")
}

func upsertManagedGitHookFile(path, managedCommand, legacyScript string) error {
	info, err := os.Stat(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	content := ""
	if exists {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content = string(raw)
	}
	normalizedExisting := strings.ReplaceAll(content, "\r\n", "\n")
	normalizedLegacy := strings.ReplaceAll(legacyScript, "\r\n", "\n")
	if strings.TrimSpace(normalizedExisting) == strings.TrimSpace(normalizedLegacy) {
		content = ""
	}

	managedBlock := fmt.Sprintf("%s\n%s\n%s\n", gitHookManagedBlockStart, managedCommand, gitHookManagedBlockEnd)
	base := stripManagedGitHookBlocks(content)
	if strings.TrimSpace(base) == "" {
		base = "#!/usr/bin/env sh\nset -eu\n\n"
	}
	final := injectManagedGitHookBlock(base, managedBlock)

	mode := os.FileMode(0o755)
	if exists {
		mode = info.Mode().Perm() | 0o111
	}
	return os.WriteFile(path, []byte(final), mode)
}

func stripManagedGitHookBlocks(content string) string {
	out := content
	for {
		start := strings.Index(out, gitHookManagedBlockStart)
		if start < 0 {
			break
		}
		endRel := strings.Index(out[start:], gitHookManagedBlockEnd)
		if endRel < 0 {
			break
		}
		end := start + endRel + len(gitHookManagedBlockEnd)
		if end < len(out) && out[end] == '\n' {
			end++
		}
		out = out[:start] + out[end:]
	}
	return out
}

func injectManagedGitHookBlock(base, managedBlock string) string {
	normalized := strings.ReplaceAll(base, "\r\n", "\n")
	if !strings.HasSuffix(normalized, "\n") {
		normalized += "\n"
	}
	if strings.HasPrefix(normalized, "#!") {
		if lineEnd := strings.IndexByte(normalized, '\n'); lineEnd >= 0 {
			head := normalized[:lineEnd+1]
			rest := normalized[lineEnd+1:]
			if strings.TrimSpace(rest) == "" {
				return head + managedBlock
			}
			if !strings.HasPrefix(rest, "\n") {
				rest = "\n" + rest
			}
			return head + managedBlock + rest
		}
	}
	if strings.TrimSpace(normalized) == "" {
		return managedBlock
	}
	if !strings.HasPrefix(normalized, "\n") {
		normalized = "\n" + normalized
	}
	return managedBlock + normalized
}

func PrintNotifyUsage() {
	fmt.Println("Usage: az notify [--json] [--verbose] <event> [<issue-id>]")
	fmt.Println("Handle Claude Code hook notifications (internal use).")
}

func PrintGateUsage() {
	fmt.Println("Usage: az gate <issue-id> [--project-dir <dir>] [--verbose] [--fix]")
	fmt.Println("Run quality gates for a task.")
}

func PrintDevUsage() {
	fmt.Println("Usage: az dev <gate|start|stop|restart|status|list>")
	fmt.Println("Manage dev servers and dev server-adjacent quality gates.")
	fmt.Println("  az dev gate <issue-id> [--project-dir <dir>] [--verbose] [--fix]")
	fmt.Println("  az dev start <issue-id>")
	fmt.Println("  az dev stop <issue-id>")
	fmt.Println("  az dev restart <issue-id>")
	fmt.Println("  az dev status <issue-id>")
	fmt.Println("  az dev list")
}

func PrintOpenCodeUsage() {
	fmt.Println("Usage: az opencode <init|plugin>")
	fmt.Println("OpenCode integration commands.")
}

func PrintOpenCodeInitUsage() {
	fmt.Println("Usage: az opencode init [--project-dir <dir>] [--verbose]")
}

func PrintOpenCodePluginUsage() {
	fmt.Println("Usage: az opencode plugin install [--global-dir <dir>] [--project-dir <dir>] [--verbose]")
}

func PrintCodexUsage() {
	fmt.Println("Usage: az codex <install|guard|hook> [--project-dir <dir>] [--verbose]")
	fmt.Println("Install Codex hook configuration and run Codex hook/guard commands.")
}

func PrintSpecUsage() {
	fmt.Println("Usage: az spec <req|link|read|lint|parity|sync> [arguments]")
	fmt.Println("  req      Manage spec requirements (list|get|create|update|delete)")
	fmt.Println("  link     Manage issue/requirement traceability links (list|add|remove)")
	fmt.Println("  read     Read consolidated spec view")
	fmt.Println("  lint     Validate spec consistency")
	fmt.Println("  parity   Report issue/spec drift")
	fmt.Println("  sync     Sync spec artifacts to Markdown (phase-1 target: md)")
	fmt.Println("")
	fmt.Println("Requirement commands:")
	fmt.Println("  az spec req list [--json] [--issue <issue-id>] [--status <open|accepted|superseded>] [--id <req-id> ...] [--ids a,b,c]")
	fmt.Println("  az spec req get --id <req-id> [--json]")
	fmt.Println("  az spec req create --id <req-id> --title <text> [--description <text>] [--issue <issue-id>] [--json]")
	fmt.Println("  az spec req update --id <req-id> [--title <text>] [--description <text>] [--status <open|accepted|superseded>] [--json]")
	fmt.Println("  az spec req delete --id <req-id> --confirm [--json]")
	fmt.Println("")
	fmt.Println("Link commands:")
	fmt.Println("  az spec link list [--json] [--issue <issue-id>] [--req <req-id>] [--id <link-id> ...] [--ids a,b,c]")
	fmt.Println("  az spec link add --issue <issue-id> --req <req-id> [--role <implements|verifies|relates>] [--note <text>] [--json]")
	fmt.Println("  az spec link remove --issue <issue-id> --req <req-id> [--json]")
	fmt.Println("")
	fmt.Println("Read/lint/parity/sync:")
	fmt.Println("  az spec read [--json] [--issue <issue-id>] [--req <req-id>]")
	fmt.Println("  az spec lint [--json] [--strict]")
	fmt.Println("  az spec parity [--json] [--fail-on-out]")
	fmt.Println("  az spec sync --target md [--check] [--json]")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  az spec req list --json")
	fmt.Println("  az spec req get --id bfs-req-1")
	fmt.Println("  az spec req create --id bfs-req-1 --title \"Restore az spec grammar\" --issue bgh")
	fmt.Println("  az spec link list --issue az-123")
	fmt.Println("  az spec link add --issue bgh --req bfs-req-1 --role implements")
	fmt.Println("  az spec read --issue az-123")
	fmt.Println("  az spec lint --strict")
	fmt.Println("  az spec parity --fail-on-out")
	fmt.Println("  az spec sync --target md --check")
}

func PrintSpecReqUsage() {
	fmt.Println("Usage: az spec req <list|get|create|update|delete> [arguments]")
	fmt.Println("  list    List requirements")
	fmt.Println("  get     Show a requirement by id")
	fmt.Println("  create  Create a requirement")
	fmt.Println("  update  Update a requirement")
	fmt.Println("  delete  Delete a requirement")
	fmt.Println("")
	fmt.Println("Grammar:")
	fmt.Println("  az spec req list [--json] [--issue <issue-id>] [--status <open|accepted|superseded>] [--id <req-id> ...] [--ids a,b,c]")
	fmt.Println("  az spec req get --id <req-id> [--json]")
	fmt.Println("  az spec req create --id <req-id> --title <text> [--description <text>] [--issue <issue-id>] [--json]")
	fmt.Println("  az spec req update --id <req-id> [--title <text>] [--description <text>] [--status <open|accepted|superseded>] [--json]")
	fmt.Println("  az spec req delete --id <req-id> --confirm [--json]")
}

func PrintSpecLinkUsage() {
	fmt.Println("Usage: az spec link <list|add|remove> [arguments]")
	fmt.Println("  list    List issue/requirement links")
	fmt.Println("  add     Create a traceability link")
	fmt.Println("  remove  Remove a traceability link")
	fmt.Println("")
	fmt.Println("Grammar:")
	fmt.Println("  az spec link list [--json] [--issue <issue-id>] [--req <req-id>] [--id <link-id> ...] [--ids a,b,c]")
	fmt.Println("  az spec link add --issue <issue-id> --req <req-id> [--role <implements|verifies|relates>] [--note <text>] [--json]")
	fmt.Println("  az spec link remove --issue <issue-id> --req <req-id> [--json]")
}

func PrintSpecReadUsage() {
	fmt.Println("Usage: az spec read [--json] [--issue <issue-id>] [--req <req-id>]")
}

func PrintSpecLintUsage() {
	fmt.Println("Usage: az spec lint [--json] [--strict]")
}

func PrintSpecParityUsage() {
	fmt.Println("Usage: az spec parity [--json] [--fail-on-out]")
}

func PrintSpecSyncUsage() {
	fmt.Println("Usage: az spec sync --target md [--check] [--json]")
}

func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return map[string]any{}, nil
	}

	settings := map[string]any{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func writeJSONObject(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func mergeHookEntries(existing any, desired map[string]any, command string) []any {
	entries := normalizeAnySlice(existing)
	for _, entry := range entries {
		if hookEntryContainsCommand(entry, command) {
			return entries
		}
	}
	return append(entries, desired)
}

func hookEntryContainsCommand(entry any, command string) bool {
	switch typed := entry.(type) {
	case map[string]any:
		if value, ok := typed["command"].(string); ok && strings.TrimSpace(value) == command {
			return true
		}
		if nested, ok := typed["hooks"]; ok {
			for _, child := range normalizeAnySlice(nested) {
				if hookEntryContainsCommand(child, command) {
					return true
				}
			}
		}
	}
	return false
}

func removeHookCommands(existing any, commands ...string) []any {
	removeSet := map[string]struct{}{}
	for _, command := range commands {
		trimmed := strings.TrimSpace(command)
		if trimmed != "" {
			removeSet[trimmed] = struct{}{}
		}
	}
	return pruneHookEntries(normalizeAnySlice(existing), removeSet)
}

func pruneHookEntries(entries []any, removeSet map[string]struct{}) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		pruned, keep := pruneHookEntry(entry, removeSet)
		if keep {
			out = append(out, pruned)
		}
	}
	return out
}

func pruneHookEntry(entry any, removeSet map[string]struct{}) (any, bool) {
	typed, ok := entry.(map[string]any)
	if !ok {
		return entry, true
	}
	if value, ok := typed["command"].(string); ok {
		if _, remove := removeSet[strings.TrimSpace(value)]; remove {
			return nil, false
		}
	}
	if nested, ok := typed["hooks"]; ok {
		prunedNested := pruneHookEntries(normalizeAnySlice(nested), removeSet)
		if len(prunedNested) == 0 {
			delete(typed, "hooks")
		} else {
			typed["hooks"] = prunedNested
		}
	}
	if _, hasCommand := typed["command"]; hasCommand {
		return typed, true
	}
	if nested, hasNested := typed["hooks"]; hasNested && len(normalizeAnySlice(nested)) > 0 {
		return typed, true
	}
	return nil, false
}

func normalizeAnySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...)
	default:
		return nil
	}
}

func normalizeStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func mergeStrings(value any, desired string) []string {
	items := normalizeStrings(value)
	if !slices.Contains(items, desired) {
		items = append(items, desired)
	}
	if len(items) == 0 {
		return []string{desired}
	}
	return items
}

func writePlaceholderFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == content {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func resolveProjectDir(projectDir string, deps *Dependencies) (string, error) {
	resolved := strings.TrimSpace(projectDir)
	if resolved == "" && deps != nil {
		resolved = strings.TrimSpace(deps.RepoDir)
	}
	if resolved == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve project directory: %w", err)
		}
		resolved = cwd
	}
	return resolved, nil
}

func readCodexGuardState(path string) codexGuardState {
	state := codexGuardState{Threads: map[string]codexGuardThreadState{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return codexGuardState{Threads: map[string]codexGuardThreadState{}}
	}
	if state.Threads == nil {
		state.Threads = map[string]codexGuardThreadState{}
	}
	return state
}

func writeCodexGuardState(path string, state codexGuardState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create codex guard state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode codex guard state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write codex guard state: %w", err)
	}
	return nil
}

func codexGuardThreadID(payload map[string]any) string {
	for _, key := range []string{"thread_id", "thread-id", "threadId"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func codexGuardCommandFromPayload(payload map[string]any) string {
	if toolInput, ok := payload["tool_input"].(map[string]any); ok {
		if value, ok := toolInput["command"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	if toolInput, ok := payload["toolInput"].(map[string]any); ok {
		if value, ok := toolInput["command"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	if value, ok := payload["command"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func codexGuardCompactionDetected(payload map[string]any) bool {
	for _, key := range []string{"last_assistant_message", "last-assistant-message", "lastAssistantMessage"} {
		if value, ok := payload[key].(string); ok {
			lower := strings.ToLower(value)
			if strings.Contains(lower, "context compact") {
				return true
			}
		}
	}
	return false
}

func codexGuardIsPrimeCommand(command string) bool {
	normalized := strings.TrimSpace(command)
	return strings.HasPrefix(normalized, "az prime") ||
		strings.HasPrefix(normalized, "./bin/az prime") ||
		strings.HasPrefix(normalized, "go run ./cmd/az prime")
}

func codexGuardPromptMentionsPrime(payload map[string]any) bool {
	for _, key := range []string{"prompt", "user_prompt", "user-prompt", "input_messages", "input-messages", "inputMessages"} {
		if value, ok := payload[key]; ok && codexGuardValueMentionsPrime(value) {
			return true
		}
	}
	return false
}

func codexGuardValueMentionsPrime(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(strings.ToLower(typed), "az prime")
	case []any:
		for _, item := range typed {
			if codexGuardValueMentionsPrime(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if codexGuardValueMentionsPrime(item) {
				return true
			}
		}
	}
	return false
}

func runShellCommand(projectDir, command string) error {
	cmd := exec.Command("/bin/sh", "-lc", command)
	cmd.Dir = projectDir
	cmd.Env = gitExecEnvWithoutRoutingVars()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func setGitHooksPath(projectDir, hooksPath string) error {
	configPath, err := resolveGitConfigPath(projectDir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read git config: %w", err)
	}
	content := string(raw)
	if strings.Contains(content, "hooksPath = "+hooksPath) {
		return nil
	}

	if strings.Contains(content, "[core]") {
		lines := strings.Split(content, "\n")
		inserted := false
		for i := 0; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "[core]" {
				continue
			}
			j := i + 1
			for ; j < len(lines); j++ {
				trimmed := strings.TrimSpace(lines[j])
				if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
					break
				}
				if strings.HasPrefix(trimmed, "hooksPath") {
					lines[j] = "\thooksPath = " + hooksPath
					inserted = true
					break
				}
			}
			if !inserted {
				block := append([]string{}, lines[:j]...)
				block = append(block, "\thooksPath = "+hooksPath)
				block = append(block, lines[j:]...)
				lines = block
				inserted = true
			}
			break
		}
		if inserted {
			content = strings.Join(lines, "\n")
		}
	} else {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "[core]\n\thooksPath = " + hooksPath + "\n"
	}

	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write git config: %w", err)
	}
	return nil
}

func openCodePluginSource() string {
	return "// Generated by go-bubbletea az opencode plugin install.\n" +
		"// Pragmatic placeholder until the full OpenCode integration is ported.\n" +
		"module.exports = {}\n"
}

func resolveGitConfigPath(projectDir string) (string, error) {
	gitPath := filepath.Join(projectDir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", fmt.Errorf("stat .git: %w", err)
	}
	if info.IsDir() {
		return filepath.Join(gitPath, "config"), nil
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", fmt.Errorf("read .git file: %w", err)
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(strings.ToLower(line), prefix) {
		return "", fmt.Errorf("unsupported .git file format")
	}
	dir := strings.TrimSpace(line[len(prefix):])
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projectDir, dir)
	}
	return filepath.Join(dir, "config"), nil
}
