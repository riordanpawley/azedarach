package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	hookEventIdlePrompt        = "idle_prompt"
	hookEventPermissionRequest = "permission_request"
	hookEventStop              = "stop"
	hookEventSessionEnd        = "session_end"
	openCodePluginFilename     = "opencode-az.js"
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

type GateOptions struct {
	IssueID    string
	ProjectDir string
	Verbose    bool
	Fix        bool
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
	fmt.Println("  go-bubbletea exposes the family and help surface; ts-opentui remains the execution path for full gates.")
	return nil
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

func PrintNotifyUsage() {
	fmt.Println("Usage: az notify <event> <issue-id> [--verbose]")
	fmt.Println("Handle Claude Code hook notifications (internal use).")
}

func PrintGateUsage() {
	fmt.Println("Usage: az gate <issue-id> [--project-dir <dir>] [--verbose] [--fix]")
	fmt.Println("Run quality gates for a task.")
}

func PrintDevUsage() {
	fmt.Println("Usage: az dev gate <issue-id> [--project-dir <dir>] [--verbose] [--fix]")
	fmt.Println("Manage dev server-adjacent quality gates.")
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

func openCodePluginSource() string {
	return "// Generated by go-bubbletea az opencode plugin install.\n" +
		"// Pragmatic placeholder until the full OpenCode integration is ported.\n" +
		"module.exports = {}\n"
}
