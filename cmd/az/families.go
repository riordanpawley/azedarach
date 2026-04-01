package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
)

func runNotifyCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintNotifyUsage()
		return nil
	}

	opts, err := cli.ParseNotifyArgs(args)
	if err != nil {
		cli.PrintNotifyUsage()
		return err
	}
	return runCommand(cfg, func(deps *cli.Dependencies) error {
		return cli.NotifyCommand(deps, opts)
	})
}

func runHooksCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintHooksUsage()
		return nil
	}

	switch args[0] {
	case "install":
		opts, err := cli.ParseHooksInstallArgs(args[1:])
		if err != nil {
			cli.PrintHooksUsage()
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.HooksInstallCommand(deps, opts)
		})
	default:
		return fmt.Errorf("unknown hooks command: %s", args[0])
	}
}

func runGitHooksCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintGitHooksUsage()
		return nil
	}

	switch args[0] {
	case "install":
		opts, err := cli.ParseGitHooksInstallArgs(args[1:])
		if err != nil {
			cli.PrintGitHooksUsage()
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.GitHooksInstallCommand(deps, opts)
		})
	case "update":
		opts, err := cli.ParseGitHooksInstallArgs(args[1:])
		if err != nil {
			cli.PrintGitHooksUsage()
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.GitHooksInstallCommand(deps, opts)
		})
	case "run":
		opts, err := cli.ParseGitHooksRunArgs(args[1:])
		if err != nil {
			cli.PrintGitHooksUsage()
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.GitHooksRunCommand(deps, opts)
		})
	case "notify":
		opts, err := cli.ParseGitHooksNotifyArgs(args[1:])
		if err != nil {
			cli.PrintGitHooksUsage()
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.GitHooksNotifyCommand(deps, opts)
		})
	default:
		return fmt.Errorf("unknown githooks command: %s", args[0])
	}
}

func runGateCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintGateUsage()
		return nil
	}

	opts, err := cli.ParseGateArgs(args)
	if err != nil {
		cli.PrintGateUsage()
		return err
	}
	return runCommand(cfg, func(deps *cli.Dependencies) error {
		return cli.GateCommand(deps, opts)
	})
}

func runDevCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printDevUsage()
		return nil
	}

	switch args[0] {
	case "gate":
		return runGateCommand(cfg, args[1:])
	case "start":
		opts, err := parseDevIssueArgs(args[1:], "az dev start <issue-id> [--project-dir <dir>] [--json] [--verbose]")
		if err != nil {
			printDevStartUsage()
			return err
		}
		return runDevWithRepo(cfg, opts.ProjectDir, func(deps *cli.Dependencies) error {
			return runDevStartCommand(deps, opts)
		})
	case "stop":
		opts, err := parseDevIssueArgs(args[1:], "az dev stop <issue-id> [--project-dir <dir>] [--json] [--verbose]")
		if err != nil {
			printDevStopUsage()
			return err
		}
		return runDevWithRepo(cfg, opts.ProjectDir, func(deps *cli.Dependencies) error {
			return runDevStopCommand(deps, opts)
		})
	case "restart":
		opts, err := parseDevIssueArgs(args[1:], "az dev restart <issue-id> [--project-dir <dir>] [--json] [--verbose]")
		if err != nil {
			printDevRestartUsage()
			return err
		}
		return runDevWithRepo(cfg, opts.ProjectDir, func(deps *cli.Dependencies) error {
			return runDevRestartCommand(deps, opts)
		})
	case "status":
		opts, err := parseDevIssueArgs(args[1:], "az dev status <issue-id> [--project-dir <dir>] [--json] [--verbose]")
		if err != nil {
			printDevStatusUsage()
			return err
		}
		return runDevWithRepo(cfg, opts.ProjectDir, func(deps *cli.Dependencies) error {
			return runDevStatusCommand(deps, opts)
		})
	case "list":
		opts, err := parseDevListArgs(args[1:], "az dev list [--project-dir <dir>] [--json] [--verbose]")
		if err != nil {
			printDevListUsage()
			return err
		}
		return runDevWithRepo(cfg, opts.ProjectDir, func(deps *cli.Dependencies) error {
			return runDevListCommand(deps, opts)
		})
	default:
		return fmt.Errorf("unknown dev command: %s", args[0])
	}
}

func runOpenCodeCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintOpenCodeUsage()
		return nil
	}

	switch args[0] {
	case "init":
		return runOpenCodeInitCommand(cfg, args[1:])
	case "plugin":
		return runOpenCodePluginCommand(cfg, args[1:])
	default:
		return fmt.Errorf("unknown opencode command: %s", args[0])
	}
}

func runOpenCodeInitCommand(cfg *config.Config, args []string) error {
	if len(args) > 0 && isHelpArg(args[0]) {
		cli.PrintOpenCodeInitUsage()
		return nil
	}

	opts, err := cli.ParseOpenCodeInitArgs(args)
	if err != nil {
		cli.PrintOpenCodeInitUsage()
		return err
	}
	return runCommand(cfg, func(deps *cli.Dependencies) error {
		return cli.OpenCodeInitCommand(deps, opts)
	})
}

func runOpenCodePluginCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintOpenCodePluginUsage()
		return nil
	}

	switch args[0] {
	case "install":
		opts, err := cli.ParseOpenCodePluginInstallArgs(args[1:])
		if err != nil {
			cli.PrintOpenCodePluginUsage()
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.OpenCodePluginInstallCommand(deps, opts)
		})
	default:
		return fmt.Errorf("unknown opencode plugin command: %s", args[0])
	}
}

func runCodexCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		cli.PrintCodexUsage()
		return nil
	}

	switch args[0] {
	case "install":
		opts, err := cli.ParseCodexInstallArgs(args[1:])
		if err != nil {
			cli.PrintCodexUsage()
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.CodexInstallCommand(deps, opts)
		})
	case "guard":
		opts, err := cli.ParseCodexGuardArgs(args[1:])
		if err != nil {
			cli.PrintCodexUsage()
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.CodexGuardCommand(deps, opts)
		})
	case "hook":
		if len(args) < 2 || isHelpArg(args[1]) {
			cli.PrintCodexUsage()
			return nil
		}
		switch args[1] {
		case "run":
			opts, err := cli.ParseCodexHookRunArgs(args[2:])
			if err != nil {
				cli.PrintCodexUsage()
				return err
			}
			return runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.CodexHookRunCommand(deps, opts)
			})
		default:
			return fmt.Errorf("unknown codex hook command: %s", args[1])
		}
	default:
		return fmt.Errorf("unknown codex command: %s", args[0])
	}
}

type projectAddOptions struct {
	Path string
	Name string
}

func runProjectCommand(cfg *config.Config, args []string) error {
	_ = cfg
	if len(args) == 0 || isHelpArg(args[0]) {
		printProjectUsage()
		return nil
	}

	switch args[0] {
	case "list":
		return runProjectListCommand(args[1:])
	case "add":
		return runProjectAddCommand(args[1:])
	case "remove":
		return runProjectRemoveCommand(args[1:])
	default:
		return fmt.Errorf("unknown project command: %s", args[0])
	}
}

func runProjectListCommand(args []string) error {
	jsonOutput := false
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&jsonOutput, "json", false, "json output")
	if err := fs.Parse(args); err != nil {
		printProjectListUsage()
		return err
	}
	if fs.NArg() != 0 {
		printProjectListUsage()
		return fmt.Errorf("usage: az project list [--json]")
	}

	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		return fmt.Errorf("load projects registry: %w", err)
	}

	if jsonOutput {
		return printJSON(map[string]any{
			"projects":        registry.Projects,
			"default_project": registry.DefaultProject,
		})
	}

	if len(registry.Projects) == 0 {
		fmt.Println("No projects registered.")
		return nil
	}

	fmt.Println("Registered projects:")
	for _, project := range registry.Projects {
		line := fmt.Sprintf("- %s\t%s", project.Name, project.Path)
		if strings.TrimSpace(project.Name) != "" && project.Name == registry.DefaultProject {
			line += " [default]"
		}
		fmt.Println(line)
	}
	return nil
}

func runProjectAddCommand(args []string) error {
	opts, err := parseProjectAddArgs(args)
	if err != nil {
		printProjectAddUsage()
		return err
	}

	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		return fmt.Errorf("load projects registry: %w", err)
	}

	if err := registry.Add(opts.Name, opts.Path); err != nil {
		return fmt.Errorf("add project: %w", err)
	}
	if err := config.SaveProjectsRegistry(registry); err != nil {
		return fmt.Errorf("save projects registry: %w", err)
	}

	fmt.Printf("Added project %s (%s)\n", opts.Name, opts.Path)
	return nil
}

func runProjectRemoveCommand(args []string) error {
	fs := flag.NewFlagSet("project remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		printProjectRemoveUsage()
		return err
	}
	if fs.NArg() != 1 {
		printProjectRemoveUsage()
		return fmt.Errorf("usage: az project remove <name>")
	}
	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		printProjectRemoveUsage()
		return fmt.Errorf("usage: az project remove <name>")
	}

	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		return fmt.Errorf("load projects registry: %w", err)
	}
	if err := registry.Remove(name); err != nil {
		return fmt.Errorf("remove project: %w", err)
	}
	if err := config.SaveProjectsRegistry(registry); err != nil {
		return fmt.Errorf("save projects registry: %w", err)
	}

	fmt.Printf("Removed project %s\n", name)
	return nil
}

func parseProjectAddArgs(args []string) (projectAddOptions, error) {
	opts := projectAddOptions{}
	fs := flag.NewFlagSet("project add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Name, "name", "", "project name")
	if err := fs.Parse(args); err != nil {
		return projectAddOptions{}, err
	}
	if fs.NArg() != 1 {
		return projectAddOptions{}, fmt.Errorf("usage: az project add <path> [--name <name>]")
	}
	absPath, err := filepath.Abs(strings.TrimSpace(fs.Arg(0)))
	if err != nil {
		return projectAddOptions{}, fmt.Errorf("resolve project path: %w", err)
	}
	opts.Path = absPath
	opts.Name = strings.TrimSpace(opts.Name)
	if opts.Name == "" {
		opts.Name = filepath.Base(absPath)
	}
	return opts, nil
}

func isHelpArg(arg string) bool {
	return strings.EqualFold(arg, "help") || arg == "-h" || arg == "--help"
}

type devIssueOptions struct {
	IssueID    string
	ProjectDir string
	Verbose    bool
	JSON       bool
}

type devListOptions struct {
	ProjectDir string
	Verbose    bool
	JSON       bool
}

type devServerRow struct {
	IssueID   string `json:"issue_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Port      int    `json:"port,omitempty"`
	Uptime    string `json:"uptime,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

func runDevWithRepo(cfg *config.Config, projectDir string, fn func(*cli.Dependencies) error) error {
	if strings.TrimSpace(projectDir) != "" {
		return runCommandAtRepoDir(cfg, projectDir, fn)
	}
	return runCommand(cfg, fn)
}

func parseDevIssueArgs(args []string, usage string) (devIssueOptions, error) {
	opts := devIssueOptions{}
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	if err := fs.Parse(args); err != nil {
		return devIssueOptions{}, err
	}
	if fs.NArg() != 1 {
		return devIssueOptions{}, fmt.Errorf("usage: %s", usage)
	}
	opts.IssueID = fs.Arg(0)
	return opts, nil
}

func parseDevListArgs(args []string, usage string) (devListOptions, error) {
	opts := devListOptions{}
	fs := flag.NewFlagSet("dev list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")
	fs.BoolVar(&opts.JSON, "json", false, "json output")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	if err := fs.Parse(args); err != nil {
		return devListOptions{}, err
	}
	if fs.NArg() != 0 {
		return devListOptions{}, fmt.Errorf("usage: %s", usage)
	}
	return opts, nil
}

func runDevStartCommand(deps *cli.Dependencies, opts devIssueOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := deps.DaemonClient.StartDevServer(ctx, opts.IssueID)
	if err != nil {
		return formatDevServerError("start", opts.IssueID, err)
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id": opts.IssueID,
			"server":   srv,
		})
	}

	fmt.Printf("Started dev server for %s\n", opts.IssueID)
	printDevServerSummary(srv, opts.Verbose)
	return nil
}

func runDevStopCommand(deps *cli.Dependencies, opts devIssueOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := deps.DaemonClient.StopDevServer(ctx, opts.IssueID)
	if err != nil {
		return formatDevServerError("stop", opts.IssueID, err)
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id": opts.IssueID,
			"server":   srv,
		})
	}

	fmt.Printf("Stopped dev server for %s\n", opts.IssueID)
	printDevServerSummary(srv, opts.Verbose)
	return nil
}

func runDevRestartCommand(deps *cli.Dependencies, opts devIssueOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := deps.DaemonClient.RestartDevServer(ctx, opts.IssueID)
	if err != nil {
		return formatDevServerError("restart", opts.IssueID, err)
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id": opts.IssueID,
			"server":   srv,
		})
	}

	fmt.Printf("Restarted dev server for %s\n", opts.IssueID)
	printDevServerSummary(srv, opts.Verbose)
	return nil
}

func runDevStatusCommand(deps *cli.Dependencies, opts devIssueOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := deps.DaemonClient.DevServerStatus(ctx, opts.IssueID)
	if err != nil {
		return formatDevServerError("status", opts.IssueID, err)
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id": opts.IssueID,
			"server":   srv,
		})
	}

	fmt.Printf("Dev server for %s:\n", opts.IssueID)
	printDevServerSummary(srv, opts.Verbose)
	return nil
}

func runDevListCommand(deps *cli.Dependencies, opts devListOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	snapshot, err := deps.DaemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load task snapshot: %w", err)
	}

	issueIDs := make([]string, 0, len(snapshot.Tasks))
	seen := map[string]struct{}{}
	for _, task := range snapshot.Tasks {
		issueID := strings.TrimSpace(task.ID)
		if issueID == "" {
			continue
		}
		if _, ok := seen[issueID]; ok {
			continue
		}
		seen[issueID] = struct{}{}
		issueIDs = append(issueIDs, issueID)
	}
	sort.Strings(issueIDs)

	rows := make([]devServerRow, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		srv, err := deps.DaemonClient.DevServerStatus(ctx, issueID)
		if err != nil {
			if isMissingDevServerError(err) {
				continue
			}
			return formatDevServerError("list", issueID, err)
		}
		if !strings.EqualFold(strings.TrimSpace(srv.Status), "running") {
			continue
		}
		rows = append(rows, devServerRow{
			IssueID:   issueID,
			Name:      srv.Name,
			Status:    srv.Status,
			Port:      srv.Port,
			Uptime:    formatDevServerUptime(srv),
			StartedAt: formatDevServerStartedAt(srv),
		})
	}

	if opts.JSON {
		return printJSON(map[string]any{"servers": rows})
	}

	if len(rows) == 0 {
		fmt.Println("No dev servers running.")
		return nil
	}

	fmt.Println("Running dev servers:")
	fmt.Println()
	fmt.Println("  ISSUE         SERVER    PORT    UPTIME")
	fmt.Println("  ─────────────────────────────────────────")
	for _, row := range rows {
		port := "-"
		if row.Port > 0 {
			port = fmt.Sprintf("%d", row.Port)
		}
		uptime := row.Uptime
		if uptime == "" {
			uptime = "-"
		}
		name := row.Name
		if strings.TrimSpace(name) == "" {
			name = row.IssueID
		}
		fmt.Printf("  %-12s %-9s %-7s %s\n", row.IssueID, name, port, uptime)
		if opts.Verbose && row.StartedAt != "" {
			fmt.Printf("      Started at: %s\n", row.StartedAt)
		}
	}
	fmt.Println()
	fmt.Printf("%d server(s) running\n", len(rows))
	return nil
}

func printDevServerSummary(srv devserver.Server, verbose bool) {
	if srv.Port > 0 {
		fmt.Printf("  Port: %d\n", srv.Port)
	}
	if strings.TrimSpace(srv.Status) != "" {
		fmt.Printf("  Status: %s\n", srv.Status)
	}
	if verbose && !srv.StartedAt.IsZero() {
		fmt.Printf("  Started at: %s\n", srv.StartedAt.UTC().Format(time.RFC3339))
	}
	if verbose {
		if uptime := formatDevServerUptime(srv); uptime != "" {
			fmt.Printf("  Uptime: %s\n", uptime)
		}
	}
}

func formatDevServerUptime(srv devserver.Server) string {
	if srv.StartedAt.IsZero() {
		return ""
	}
	uptime := time.Since(srv.StartedAt).Round(time.Second)
	if uptime < 0 {
		return ""
	}
	return uptime.String()
}

func formatDevServerStartedAt(srv devserver.Server) string {
	if srv.StartedAt.IsZero() {
		return ""
	}
	return srv.StartedAt.UTC().Format(time.RFC3339)
}

func formatDevServerError(action, issueID string, err error) error {
	if isMissingDevServerError(err) {
		return fmt.Errorf("dev server not found for issue %s", issueID)
	}
	var cmdErr *daemonclient.CommandError
	if errors.As(err, &cmdErr) {
		return fmt.Errorf("dev server %s failed for %s: %s", action, issueID, cmdErr.Message)
	}
	return fmt.Errorf("dev server %s failed for %s: %w", action, issueID, err)
}

func isMissingDevServerError(err error) bool {
	var cmdErr *daemonclient.CommandError
	if !errors.As(err, &cmdErr) {
		return false
	}
	return cmdErr.Code == protocol.ErrorCodeInvalidRequest
}

func printDevUsage() {
	fmt.Println("Usage: az dev <gate|start|stop|restart|status|list>")
	fmt.Println("Manage dev servers and gate shortcuts.")
	fmt.Println("  gate <issue-id>               Run quality gates for an issue")
	fmt.Println("  start <issue-id> [--project-dir <dir>] [--json] [--verbose]")
	fmt.Println("  stop <issue-id> [--project-dir <dir>] [--json] [--verbose]")
	fmt.Println("  restart <issue-id> [--project-dir <dir>] [--json] [--verbose]")
	fmt.Println("  status <issue-id> [--project-dir <dir>] [--json] [--verbose]")
	fmt.Println("  list [--project-dir <dir>] [--json] [--verbose]")
}

func printDevStartUsage() {
	fmt.Println("Usage: az dev start <issue-id> [--project-dir <dir>] [--json] [--verbose]")
}
func printDevStopUsage() {
	fmt.Println("Usage: az dev stop <issue-id> [--project-dir <dir>] [--json] [--verbose]")
}
func printDevRestartUsage() {
	fmt.Println("Usage: az dev restart <issue-id> [--project-dir <dir>] [--json] [--verbose]")
}
func printDevStatusUsage() {
	fmt.Println("Usage: az dev status <issue-id> [--project-dir <dir>] [--json] [--verbose]")
}
func printDevListUsage() {
	fmt.Println("Usage: az dev list [--project-dir <dir>] [--json] [--verbose]")
}

func printProjectUsage() {
	fmt.Println("Usage: az project <list|add|remove>")
	fmt.Println("Manage registered projects.")
	fmt.Println("  list [--json]")
	fmt.Println("  add <path> [--name <name>]")
	fmt.Println("  remove <name>")
}

func printProjectListUsage() {
	fmt.Println("Usage: az project list [--json]")
}

func printProjectAddUsage() {
	fmt.Println("Usage: az project add <path> [--name <name>]")
}

func printProjectRemoveUsage() {
	fmt.Println("Usage: az project remove <name>")
}

func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
