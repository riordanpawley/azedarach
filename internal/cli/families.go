package cli

import (
	"bufio"
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
	hookEventStop              = "stop"
	hookEventSessionEnd        = "session_end"
	openCodePluginFilename     = "opencode-az.js"
	commandDevServerList       = "devserver.list"
)

var hookEventStatuses = map[string]string{
	hookEventIdlePrompt:        "waiting",
	hookEventPermissionRequest: "waiting",
	hookEventStop:              "stopped",
	hookEventSessionEnd:        "ended",
}

type NotifyOptions struct {
	Event   string
	IssueID string
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

func ParseNotifyArgs(args []string) (NotifyOptions, error) {
	opts := NotifyOptions{}
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")

	if err := fs.Parse(args); err != nil {
		return NotifyOptions{}, err
	}
	if fs.NArg() != 2 {
		return NotifyOptions{}, fmt.Errorf("usage: az notify <event> <issue-id> [--verbose]")
	}
	opts.Event = fs.Arg(0)
	opts.IssueID = fs.Arg(1)
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
		return GitHooksInstallOptions{}, fmt.Errorf("usage: az githooks install [--project-dir <dir>] [--verbose]")
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
	if fs.NArg() != 0 {
		return GitHooksRunOptions{}, fmt.Errorf("usage: az githooks run [--project-dir <dir>] [--verbose]")
	}
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

func NotifyCommand(_ *Dependencies, opts NotifyOptions) error {
	status, ok := hookEventStatuses[opts.Event]
	if !ok {
		return fmt.Errorf("invalid event type: %s", opts.Event)
	}

	if opts.Verbose {
		fmt.Printf("Hook notification: %s for %s -> %s\n", opts.Event, opts.IssueID, status)
		return nil
	}

	fmt.Printf("Hook notification: %s -> %s\n", opts.IssueID, status)
	return nil
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

	preCommitPath := filepath.Join(hooksDir, "pre-commit")
	const preCommit = "#!/usr/bin/env sh\nset -eu\naz githooks run \"$@\"\n"
	if err := os.WriteFile(preCommitPath, []byte(preCommit), 0o755); err != nil {
		return fmt.Errorf("write pre-commit hook: %w", err)
	}

	cmd := exec.Command("git", "-C", projectDir, "config", "core.hooksPath", ".githooks")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set git core.hooksPath: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	fmt.Printf("Installed git hooks in %s\n", hooksDir)
	fmt.Println("Configured git core.hooksPath=.githooks")
	if opts.Verbose {
		fmt.Println("pre-commit now delegates to: az githooks run")
	}
	return nil
}

func GitHooksRunCommand(deps *Dependencies, opts GitHooksRunOptions) error {
	projectDir, err := resolveProjectDir(opts.ProjectDir, deps)
	if err != nil {
		return err
	}

	cfg := (*config.Config)(nil)
	if deps != nil && deps.Config != nil {
		cfg = deps.Config
	} else {
		cfg, err = config.LoadConfig(projectDir)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	}

	specSyncCfg := cfg.GitHooks.SpecSync
	if specSyncCfg.Enabled {
		command := strings.TrimSpace(specSyncCfg.Command)
		if command == "" {
			command = "az spec sync --target md"
		}
		if opts.Verbose {
			fmt.Printf("githooks: spec sync command: %s\n", command)
		}
		if err := runShellCommand(projectDir, command); err != nil {
			return fmt.Errorf("githooks spec sync failed: %w", err)
		}

		if specSyncCfg.AutoStageDocs {
			if _, err := os.Stat(filepath.Join(projectDir, "docs", "spec")); err == nil {
				stageCmd := exec.Command("git", "-C", projectDir, "add", "docs/spec")
				if output, err := stageCmd.CombinedOutput(); err != nil {
					return fmt.Errorf("githooks spec sync auto-stage failed: %w (%s)", err, strings.TrimSpace(string(output)))
				}
			}
		}
	}

	boundaryCfg := cfg.GitHooks.BoundaryCheck
	if boundaryCfg.Enabled {
		if os.Getenv("AZEDARACH_SKIP_BOUNDARY_CHECK") == "1" {
			fmt.Println("githooks: boundary checks skipped (AZEDARACH_SKIP_BOUNDARY_CHECK=1)")
			return nil
		}
		shouldRun, err := hasBoundaryRelevantStagedPaths(projectDir)
		if err != nil {
			return fmt.Errorf("determine staged boundary paths: %w", err)
		}
		if shouldRun {
			command := strings.TrimSpace(boundaryCfg.Command)
			if command == "" {
				command = "just check-boundaries"
			}
			if opts.Verbose {
				fmt.Printf("githooks: boundary command: %s\n", command)
			}
			if err := runShellCommand(projectDir, command); err != nil {
				return fmt.Errorf("githooks boundary check failed: %w", err)
			}
		}
	}

	return nil
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

func PrintHooksUsage() {
	fmt.Println("Usage: az hooks install <issue-id> [--project-dir <dir>] [--verbose]")
	fmt.Println("Manage Claude Code hook configuration for session detection.")
}

func PrintGitHooksUsage() {
	fmt.Println("Usage: az githooks <install|run> [--project-dir <dir>] [--verbose]")
	fmt.Println("Manage repository git hooks and execute configured hook tasks.")
}

func PrintNotifyUsage() {
	fmt.Println("Usage: az notify <event> <issue-id> [--verbose]")
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

func runShellCommand(projectDir, command string) error {
	cmd := exec.Command("/bin/sh", "-lc", command)
	cmd.Dir = projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func hasBoundaryRelevantStagedPaths(projectDir string) (bool, error) {
	cmd := exec.Command("git", "-C", projectDir, "diff", "--cached", "--name-only", "--diff-filter=ACMR")
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		path := scanner.Text()
		if strings.HasPrefix(path, "internal/") ||
			strings.HasPrefix(path, "cmd/") ||
			strings.HasPrefix(path, "docs/") ||
			path == "justfile" ||
			path == "AGENTS.md" ||
			path == ".golangci-boundary.yml" ||
			path == "scripts/check-boundaries.sh" ||
			path == "scripts/afv-drift-sentinel.sh" {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func openCodePluginSource() string {
	return "// Generated by go-bubbletea az opencode plugin install.\n" +
		"// Pragmatic placeholder until the full OpenCode integration is ported.\n" +
		"module.exports = {}\n"
}
