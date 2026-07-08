package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/buildinfo"
	"github.com/riordanpawley/azedarach/internal/cli"
	clitext "github.com/riordanpawley/azedarach/internal/cli/text"
	"github.com/riordanpawley/azedarach/internal/client/daemonprocess"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	app "github.com/riordanpawley/azedarach/internal/tui"
)

var processStartedAt = time.Now()

type tuiDaemonStopper interface {
	Stop(context.Context) error
}

type tuiProgramRunner interface {
	Run() (tea.Model, error)
}

var newTUIDaemonStopper = func(repoDir, socketPath string) tuiDaemonStopper {
	return daemonprocess.NewLauncher(repoDir, socketPath)
}

var newTUIProgramRunner = func(model tea.Model) tuiProgramRunner {
	return tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
}

var exitProcess = os.Exit

func main() {
	args := os.Args[1:]
	if maybePrintCommandHelp(args) {
		return
	}
	if len(args) > 0 {
		switch args[0] {
		case "version", "-v", "--version":
			fmt.Println(buildinfo.VersionString())
			return
		case "help", "-h", "--help":
			printRootUsage()
			return
		}
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	latencytrace.SetConfigEnabled(cfg.Diagnostics.LatencyTrace)

	// If no arguments, run the TUI
	if len(args) == 0 {
		runTUI(cfg)
		return
	}

	// Handle subcommands
	command := args[0]
	commandArgs := args[1:]

	switch command {
	case "session":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az session <start|attach|stop|status|diagnose|restart-all|resolve-conflict> [arguments]\n")
			os.Exit(1)
		}
		sessionCommand := commandArgs[0]
		sessionArgs := commandArgs[1:]
		if sessionCommand == "help" || sessionCommand == "-h" || sessionCommand == "--help" {
			printSessionUsage()
			os.Exit(0)
		}
		if sessionHelpRequested(sessionArgs...) && printSessionCommandUsage(sessionCommand, true) {
			os.Exit(0)
		}
		if err := runSessionCommand(cfg, sessionCommand, sessionArgs, true); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "branch":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az branch <merge|agent-merge> [arguments]\n")
			os.Exit(1)
		}
		branchCommand := commandArgs[0]
		branchArgs := commandArgs[1:]
		if sessionHelpRequested(branchArgs...) {
			if usage, ok := branchCommandUsage(branchCommand); ok {
				fmt.Println(usage)
				os.Exit(0)
			}
		}
		if err := runBranchCommand(cfg, branchCommand, branchArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "worktree":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az worktree create [--project <project-id>] [--base <branch>] [--json] <issue-id>\n")
			os.Exit(1)
		}
		worktreeCommand := commandArgs[0]
		worktreeArgs := commandArgs[1:]
		if worktreeCommand == "help" || worktreeCommand == "-h" || worktreeCommand == "--help" {
			printWorktreeUsage()
			os.Exit(0)
		}
		if sessionHelpRequested(worktreeArgs...) {
			if worktreeCommand == "create" {
				fmt.Println("Usage: az worktree create [--project <project-id>] [--base <branch>] [--json] <issue-id>")
				os.Exit(0)
			}
		}
		switch worktreeCommand {
		case "create":
			opts, err := cli.ParseWorktreeCreateArgs(worktreeArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az worktree create [--project <project-id>] [--base <branch>] [--json] <issue-id>\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.WorktreeCreateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unknown worktree command: %s\n", worktreeCommand)
			fmt.Fprintf(os.Stderr, "Usage: az worktree create [--project <project-id>] [--base <branch>] [--json] <issue-id>\n")
			os.Exit(1)
		}

	case "start":
		if sessionHelpRequested(commandArgs...) && printSessionCommandUsage(command, false) {
			os.Exit(0)
		}
		if err := runSessionCommand(cfg, command, commandArgs, false); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "attach":
		if sessionHelpRequested(commandArgs...) && printSessionCommandUsage(command, false) {
			os.Exit(0)
		}
		if err := runSessionCommand(cfg, command, commandArgs, false); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "stop":
		if sessionHelpRequested(commandArgs...) && printSessionCommandUsage(command, false) {
			os.Exit(0)
		}
		if err := runSessionCommand(cfg, command, commandArgs, false); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "kill":
		if sessionHelpRequested(commandArgs...) && printSessionCommandUsage(command, false) {
			os.Exit(0)
		}
		if err := runSessionCommand(cfg, command, commandArgs, false); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "status":
		if sessionHelpRequested(commandArgs...) && printSessionCommandUsage(command, false) {
			os.Exit(0)
		}
		if err := runSessionCommand(cfg, command, commandArgs, false); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "operation":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az operation <get|list|queue|logs|cancel> [arguments]\n")
			os.Exit(1)
		}
		opCommand := commandArgs[0]
		opArgs := commandArgs[1:]
		switch opCommand {
		case "get":
			opts, err := cli.ParseOperationGetArgs(opArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az operation get --id <operation-id> [--wait]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OperationGetCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "list":
			opts, err := cli.ParseOperationListArgs(opArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az operation list [--issue-id <issue-id>] [--state <state>] [--kind <kind>] [--limit N]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OperationListCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "queue":
			opts, err := cli.ParseOperationQueueArgs(opArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az operation queue [--issue <issue-id>] [--state <state>] [--kind <kind>] [--limit N] [--tree] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OperationQueueCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "logs":
			opts, err := cli.ParseOperationLogsArgs(opArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az operation logs --id <operation-id>\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OperationLogsCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "cancel":
			opts, err := cli.ParseOperationCancelArgs(opArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az operation cancel --id <operation-id> [--reason <reason>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OperationCancelCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unknown operation command: %s\n", opCommand)
			fmt.Fprintf(os.Stderr, "Usage: az operation <get|list|queue|logs|cancel> [arguments]\n")
			os.Exit(1)
		}

	case "export":
		exportOpts, err := cli.ParseExportArgs(commandArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Usage: az export --format json [--out <path>]\n")
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.ExportCommand(deps, exportOpts)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "log":
		logOpts, err := cli.ParseLogArgs(commandArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Usage: az log [--source daemon,tui,cli] [--lines N] [--follow|--no-follow] [daemon|tui|cli ...]\n")
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.LogCommand(deps, logOpts)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "config":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az config set <key> <value> [--project-dir <dir>]\n")
			os.Exit(1)
		}
		configCommand := commandArgs[0]
		configArgs := commandArgs[1:]
		if configCommand == "help" || configCommand == "-h" || configCommand == "--help" {
			fmt.Println("Usage: az config set <key> <value> [--project-dir <dir>]")
			os.Exit(0)
		}
		switch configCommand {
		case "set":
			opts, err := cli.ParseConfigSetArgs(configArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az config set <key> <value> [--project-dir <dir>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			run := runCommand
			if opts.ProjectDir != "" {
				run = func(cfg *config.Config, fn func(*cli.Dependencies) error) error {
					return runCommandAtRepoDir(cfg, opts.ProjectDir, fn)
				}
			}
			if err := run(cfg, func(deps *cli.Dependencies) error {
				return cli.ConfigSetCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unknown config command: %s\n", configCommand)
			fmt.Fprintf(os.Stderr, "Usage: az config set <key> <value> [--project-dir <dir>]\n")
			os.Exit(1)
		}

	case "spec":
		if err := runSpecCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "decision":
		if err := runDecisionCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "learn":
		if err := runLearnCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "sync":
		if len(commandArgs) > 0 {
			if commandArgs[0] == "help" || commandArgs[0] == "-h" || commandArgs[0] == "--help" {
				fmt.Println("Usage: az sync [conflicts] [--all] [<directory>] [--project-dir <dir>] [--json]")
				os.Exit(0)
			}
		}
		syncOpts, err := cli.ParseSyncArgs(commandArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Usage: az sync [conflicts] [--all] [<directory>] [--project-dir <dir>] [--json]\n")
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		run := runCommand
		if syncOpts.ProjectDir != "" {
			run = func(cfg *config.Config, fn func(*cli.Dependencies) error) error {
				return runCommandAtRepoDir(cfg, syncOpts.ProjectDir, fn)
			}
		}
		if err := run(cfg, func(deps *cli.Dependencies) error {
			return cli.SyncCommand(deps, syncOpts)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "githooks":
		if err := runGitHooksCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "gate":
		if err := runGateCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "dev":
		if err := runDevCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "project":
		if err := runProjectCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "impl":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az impl <list|delete|migrate> [arguments]\n")
			os.Exit(1)
		}
		implCommand := commandArgs[0]
		implArgs := commandArgs[1:]
		if implCommand == "help" || implCommand == "-h" || implCommand == "--help" {
			fmt.Println("Usage:")
			fmt.Println("  az impl list")
			fmt.Println("  az impl delete --confirm <implementation>")
			fmt.Println("  az impl migrate <from-implementation> <to-implementation>")
			os.Exit(0)
		}
		switch implCommand {
		case "list":
			opts, err := cli.ParseImplListArgs(implArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az impl list\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.ImplListCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "delete":
			opts, err := cli.ParseImplDeleteArgs(implArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az impl delete --confirm <implementation>\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.ImplDeleteCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "migrate":
			opts, err := cli.ParseImplMigrateArgs(implArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az impl migrate <from-implementation> <to-implementation>\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.ImplMigrateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unknown impl command: %s\n", implCommand)
			fmt.Fprintf(os.Stderr, "Usage: az impl <list|delete|migrate> [arguments]\n")
			os.Exit(1)
		}

	case "ai":
		if err := runAICommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "tmux":
		if err := runTmuxCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "prime":
		if len(commandArgs) > 0 {
			fmt.Fprintf(os.Stderr, "Usage: az prime\n")
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.PrimeCommand(deps)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "issue":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|search|get|claim|takeover|release|events|context-risk|get-many|check|doctor|create|split|update|close|delete|image|document|dep|bulk-create|bulk-update|fanout> [arguments]\n")
			os.Exit(1)
		}
		issueCommand := commandArgs[0]
		issueArgs := commandArgs[1:]
		if issueCommand == "help" || issueCommand == "-h" || issueCommand == "--help" {
			helpText, err := clitext.Render("issue_help", nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Print(helpText)
			os.Exit(0)
		}
		switch issueCommand {
		case "list":
			opts, err := cli.ParseIssueListArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue list [--project <project-id>] [--json] [--deps] [--query <text>|-q <text>] [--created-after YYYY-MM-DD] [--created-before YYYY-MM-DD] [--updated-after YYYY-MM-DD] [--updated-before YYYY-MM-DD] [--status <status> ...] [--statuses a,b,c] [--limit N] [--id <id> ...] [--ids a,b,c] [--parent <id> ...] [--parents a,b,c] [--depends-on <id> ...] [--depends-on-ids a,b,c]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueListCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "search":
			opts, err := cli.ParseIssueSearchArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue search [--project <project-id>] [--json] [--deps] [--created-after YYYY-MM-DD] [--created-before YYYY-MM-DD] [--updated-after YYYY-MM-DD] [--updated-before YYYY-MM-DD] [--status <status> ...] [--statuses a,b,c] [--limit N] [--id <id> ...] [--ids a,b,c] [--parent <id> ...] [--parents a,b,c] [--depends-on <id> ...] [--depends-on-ids a,b,c] (--query <text>|-q <text>|<query>)\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueListCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "get":
			opts, err := cli.ParseIssueGetArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue get [--project <project-id>] [--id <issue-id>] [--json] [--with-notes] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueGetCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "claim", "takeover":
			opts, err := cli.ParseIssueOwnershipArgs(issueArgs, issueCommand)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue %s [--project <project-id>] [--id <issue-id>] [--owner <owner-id>] [--kind human|agent|orchestrator] [--ttl 2h] [--force] [--json] [<issue-id>]\n", issueCommand)
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if issueCommand == "takeover" {
				opts.Force = true
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueClaimCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "release":
			opts, err := cli.ParseIssueOwnershipArgs(issueArgs, issueCommand)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue release [--project <project-id>] [--id <issue-id>] [--owner <owner-id>] [--force] [--json] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueReleaseCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "events":
			opts, err := cli.ParseIssueEventsArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue events [--project <project-id>] [--id <issue-id>] [--json] [--jq-help] [--type <event-type> ...] [--types a,b] [--limit N] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueEventsCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "context-risk":
			opts, err := cli.ParseIssueContextRiskArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue context-risk [--project <project-id>] [--id <issue-id>] [--since 14d] [--summary|--full] [--json] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueContextRiskCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "get-many":
			opts, err := cli.ParseIssueGetManyArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue get-many [--project <project-id>] --id <issue-id> [--id <issue-id> ...] [--ids a,b,c] [--json] [--with-notes]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueGetManyCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "check":
			opts, err := cli.ParseIssueCheckArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue check [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueCheckCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "doctor":
			opts, err := cli.ParseIssueDoctorArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue doctor [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueDoctorCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "create":
			opts, err := cli.ParseIssueCreateArgs(issueArgs)
			if err != nil {
				printIssueCreateUsage(os.Stderr)
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueCreateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "split":
			opts, err := cli.ParseIssueSplitArgs(issueArgs)
			if err != nil {
				printIssueSplitUsage(os.Stderr)
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueSplitCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "update":
			opts, err := cli.ParseIssueUpdateArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue update [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>] [--title text] [--description text] [--notes text] [--append-notes text] [--status open|in_progress|in_review|closed] [--cascade-children] [--force-worktree] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--update-impl <impl> ...]\n")
				fmt.Fprintf(os.Stderr, "Note: setting --status closed integrates the issue branch, cleans session/worktree attachments, then closes; --force-worktree only applies to closed status.\n")
				fmt.Fprintf(os.Stderr, "Note: --cascade-children only applies to --status in_review and moves open/in_progress descendants to in_review first.\n")
				fmt.Fprintf(os.Stderr, "Note: --update-impl is only for changing implementation assignments; normal field updates do not require it.\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueUpdateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "close":
			opts, err := cli.ParseIssueCloseArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue close [--project <project-id>] [--id <issue-id>|-i <issue-id>] [--json] [--force-worktree] [--close-clean-children] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Note: close integrates the issue branch, cleans session/worktree attachments, then writes closed status.\n")
				fmt.Fprintf(os.Stderr, "Note: --force-worktree forces worktree removal after integration.\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueCloseCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "delete":
			opts, err := cli.ParseIssueDeleteArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue delete [--project <project-id>] --confirm [--id <issue-id>] [--json] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueDeleteCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "image":
			if len(issueArgs) == 0 {
				fmt.Fprintf(os.Stderr, "Usage: az issue image <add|remove> [arguments]\n")
				os.Exit(1)
			}
			imageCommand := issueArgs[0]
			imageArgs := issueArgs[1:]
			switch imageCommand {
			case "add":
				opts, err := cli.ParseIssueImageAddArgs(imageArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue image add [--project <project-id>] [--issue-id <issue-id>] [--path <file>] [<issue-id> <file>] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueImageAddCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			case "remove":
				opts, err := cli.ParseIssueImageRemoveArgs(imageArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue image remove [--project <project-id>] [--issue-id <issue-id>] [--attachment-id <attachment-id>] [<issue-id> <attachment-id>] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueImageRemoveCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			default:
				fmt.Fprintf(os.Stderr, "Unknown issue image command: %s\n", imageCommand)
				fmt.Fprintf(os.Stderr, "Usage: az issue image <add|remove> [arguments]\n")
				os.Exit(1)
			}

		case "document":
			if len(issueArgs) == 0 {
				fmt.Fprintf(os.Stderr, "Usage: az issue document <add|list|remove> [arguments]\n")
				os.Exit(1)
			}
			documentCommand := issueArgs[0]
			documentArgs := issueArgs[1:]
			switch documentCommand {
			case "add":
				opts, err := cli.ParseIssueDocumentAddArgs(documentArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue document add [--project <project-id>] [--issue-id <issue-id>] [--path <file>] [<issue-id> <file>] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueDocumentAddCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			case "list":
				opts, err := cli.ParseIssueDocumentListArgs(documentArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue document list [--project <project-id>] [--issue-id <issue-id>] [<issue-id>] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueDocumentListCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			case "remove":
				opts, err := cli.ParseIssueDocumentRemoveArgs(documentArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue document remove [--project <project-id>] [--issue-id <issue-id>] [--attachment-id <attachment-id>] [<issue-id> <attachment-id>] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueDocumentRemoveCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			default:
				fmt.Fprintf(os.Stderr, "Unknown issue document command: %s\n", documentCommand)
				fmt.Fprintf(os.Stderr, "Usage: az issue document <add|list|remove> [arguments]\n")
				os.Exit(1)
			}

		case "dep":
			if len(issueArgs) == 0 {
				fmt.Fprintf(os.Stderr, "Usage: az issue dep <add|remove|bulk> [arguments]\n")
				os.Exit(1)
			}
			depCommand := issueArgs[0]
			depArgs := issueArgs[1:]
			switch depCommand {
			case "add":
				opts, err := cli.ParseIssueDependencyAddArgs(depArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue dep add [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--force-parent-change] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueDependencyAddCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			case "remove":
				opts, err := cli.ParseIssueDependencyRemoveArgs(depArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue dep remove [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--confirm] [--confirm-parent-orphan] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueDependencyRemoveCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			case "bulk":
				if len(depArgs) == 0 || depArgs[0] != "apply" {
					fmt.Fprintf(os.Stderr, "Usage: az issue dep bulk apply [--project <project-id>] --input <path> [--dry-run] [--json]\n")
					os.Exit(1)
				}
				opts, err := cli.ParseIssueDependencyBulkApplyArgs(depArgs[1:])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue dep bulk apply [--project <project-id>] --input <path> [--dry-run] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueDependencyBulkApplyCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			default:
				fmt.Fprintf(os.Stderr, "Unknown issue dep command: %s\n", depCommand)
				fmt.Fprintf(os.Stderr, "Usage: az issue dep <add|remove|bulk> [arguments]\n")
				os.Exit(1)
			}

		case "bulk-create":
			opts, err := cli.ParseIssueBulkCreateArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue bulk-create [--project <project-id>] [--impl <implementation>] --input <path> [--dry-run] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueBulkCreateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "bulk-update":
			opts, err := cli.ParseIssueBulkUpdateArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue bulk-update [--project <project-id>] [--impl <implementation>] --input <path> [--dry-run] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueBulkUpdateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "fanout":
			if len(issueArgs) == 0 {
				opts, err := cli.ParseIssueFanoutArgs(issueArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue fanout [--project <project-id>] --input <path> [--apply] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueFanoutCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				break
			}
			switch issueArgs[0] {
			case "ready":
				opts, err := cli.ParseIssueFanoutReadyArgs(issueArgs[1:])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue fanout ready [--project <project-id>] --root <issue-id> [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueFanoutReadyCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			case "drift":
				opts, err := cli.ParseIssueFanoutDriftArgs(issueArgs[1:])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue fanout drift [--project <project-id>] --issue <issue-id> [--worktree <path>] [--json] [--fail-on-out]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueFanoutDriftCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			default:
				opts, err := cli.ParseIssueFanoutArgs(issueArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue fanout [--project <project-id>] --input <path> [--apply] [--json]\n")
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				if err := runCommand(cfg, func(deps *cli.Dependencies) error {
					return cli.IssueFanoutCommand(deps, opts)
				}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			}

		default:
			fmt.Fprintf(os.Stderr, "Unknown issue command: %s\n", issueCommand)
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|search|get|claim|takeover|release|events|context-risk|get-many|check|doctor|create|split|update|close|delete|image|document|dep|bulk-create|bulk-update|fanout> [arguments]\n")
			os.Exit(1)
		}

	case "mail":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az mail <send|list|watch|validate-evidence> [arguments]\n")
			os.Exit(1)
		}
		switch commandArgs[0] {
		case "send":
			opts, err := cli.ParseMailSendArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az mail send --parent <issue-id> --type <event-type> --body <text> [--issue <issue-id>] [--from <actor>] [--to <actor>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.MailSendCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "list":
			opts, err := cli.ParseMailListArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az mail list --parent <issue-id> [--since <seq>] [--limit <n>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.MailListCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "watch":
			opts, err := cli.ParseMailWatchArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az mail watch --parent <issue-id> [--since <seq>] [--jsonl] [--once]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.MailWatchCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "validate-evidence":
			opts, err := cli.ParseEvidenceValidateArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az mail validate-evidence [--body <json>|--file <path>] [--fix] [--template] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.EvidenceValidateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unknown mail command: %s\n", commandArgs[0])
			fmt.Fprintf(os.Stderr, "Usage: az mail <send|list|watch|validate-evidence> [arguments]\n")
			os.Exit(1)
		}
	case "evidence":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az evidence <validate> [arguments]\n")
			os.Exit(1)
		}
		switch commandArgs[0] {
		case "validate":
			opts, err := cli.ParseEvidenceValidateArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az evidence validate [--body <json>|--file <path>] [--fix] [--template] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.EvidenceValidateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unknown evidence command: %s\n", commandArgs[0])
			fmt.Fprintf(os.Stderr, "Usage: az evidence <validate> [arguments]\n")
			os.Exit(1)
		}
	case "observe":
		opts, err := cli.ParseObserveArgs(commandArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Usage: az observe [--root <issue-id>] [--project <project-id>] [--json]\n")
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.ObserveCommand(deps, opts)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "orchestrate":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az orchestrate <status|start|group|watch|observe|prompt|message|complete-check|integrate|close-session> [arguments]\n")
			os.Exit(1)
		}
		switch commandArgs[0] {
		case "status":
			opts, err := cli.ParseOrchestrateStatusArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate status --root <issue-id> [--project <project-id>] [--since <seq>] [--limit <n>] [--json] [--summary|--full]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateStatusCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "start":
			opts, err := cli.ParseOrchestrateStartArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate start --root <issue-id> [--project <project-id>] [--limit <n>] [--issue <issue-id> ...] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateStartCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "group":
			opts, err := cli.ParseOrchestrateGroupArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate group --root <issue-id> --nested <issue-id> --issue <issue-id> ... [--project <project-id>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateGroupCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "watch":
			opts, err := cli.ParseOrchestrateWatchArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate watch --root <issue-id> [--project <project-id>] [--since <seq>] [--jsonl] [--once] [--verbose|--full]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateWatchCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "observe":
			opts, err := cli.ParseOrchestrateObserveArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate observe --root <issue-id> [--project <project-id>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateObserveCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "prompt":
			opts, err := cli.ParseOrchestratePromptArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate prompt --issue <issue-id> [--root <issue-id>] [--coordination native|mailbox] [--project <project-id>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestratePromptCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "message":
			opts, err := cli.ParseOrchestrateMessageArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate message --root <issue-id> --issue <issue-id> --body <text> [--type <event-type>] [--force-self-delivery] [--project <project-id>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateMessageCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "complete-check":
			opts, err := cli.ParseOrchestrateCompleteCheckArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate complete-check --root <issue-id> [--project <project-id>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateCompleteCheckCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "integrate":
			opts, err := cli.ParseOrchestrateIntegrateArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate integrate --issue <issue-id> [--apply] [--project <project-id>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateIntegrateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		case "close-session":
			opts, err := cli.ParseOrchestrateCloseSessionArgs(commandArgs[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az orchestrate close-session --issue <issue-id> [--project <project-id>] [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.OrchestrateCloseSessionCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unknown orchestrate command: %s\n", commandArgs[0])
			fmt.Fprintf(os.Stderr, "Usage: az orchestrate <status|start|group|watch|observe|prompt|message|complete-check|integrate|close-session> [arguments]\n")
			os.Exit(1)
		}

	case "daemon":
		if len(commandArgs) != 1 {
			fmt.Fprintf(os.Stderr, "Usage: az daemon <start|stop|restart>\n")
			os.Exit(1)
		}
		var err error
		switch commandArgs[0] {
		case "start":
			err = runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.StartDaemonCommand(deps)
			})
		case "stop":
			err = runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.StopDaemonCommand(deps)
			})
		case "restart":
			err = runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.RestartDaemonCommand(deps)
			})
		default:
			fmt.Fprintf(os.Stderr, "Usage: az daemon <start|stop|restart>\n")
			os.Exit(1)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
		printRootUsage()
		os.Exit(1)
	}
}

// runTUI starts the terminal user interface
func runTUI(cfg *config.Config) {
	runTUIWithOptions(cfg)
}

var runTmuxSelectorForCommand = runGlobalTmuxSelector

func runTUIWithOptions(cfg *config.Config, opts ...app.Option) {
	if err := validateTUILaunchContext(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	audit := beginCommandAudit(nil, nil, "tui", os.Args[1:])
	var runErr error
	exitCode := 0
	defer func() {
		if exitCode != 0 {
			exitProcess(exitCode)
		}
	}()
	if cleanup := ownedJustRunScopedDaemonCleanup(); cleanup != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := cleanup(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to stop worktree-scoped dev daemon: %v\n", err)
			}
		}()
	}
	defer func() {
		finishCommandAudit(nil, audit, runErr)
	}()
	model := app.NewWithOptions(cfg, opts...)
	p := newTUIProgramRunner(model)

	if _, err := p.Run(); err != nil {
		runErr = err
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCode = 1
		return
	}
}

func validateTUILaunchContext() error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	if !config.IsAzedarachDevelopmentWorktree(cwd) || config.UseScopedDaemonRuntimeFor(cwd) {
		return nil
	}
	return fmt.Errorf("refusing to start the TUI from an Azedarach development worktree while AZEDARACH_DAEMON_SCOPE=%q uses the shared production daemon; set AZEDARACH_DAEMON_SCOPE=worktree when intentionally testing this worktree's azd", os.Getenv("AZEDARACH_DAEMON_SCOPE"))
}

func ownedJustRunScopedDaemonCleanup() func(context.Context) error {
	if strings.TrimSpace(os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE")) != "just-run" {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil || !config.UseScopedDaemonRuntimeFor(cwd) || !config.IsAzedarachDevelopmentWorktree(cwd) {
		return nil
	}
	worktreeRoot, err := config.ResolveWorktreeRoot(cwd)
	if err != nil || strings.TrimSpace(worktreeRoot) == "" {
		return nil
	}
	socketPath := config.ScopedDaemonSocketPath(worktreeRoot)
	if filepath.Clean(socketPath) == filepath.Clean(config.GlobalDaemonSocketPath()) {
		return nil
	}
	stopper := newTUIDaemonStopper(worktreeRoot, socketPath)
	if stopper == nil {
		return nil
	}
	return stopper.Stop
}

func isLinkedGitWorktree(startDir string) bool {
	worktreeRoot, err := config.ResolveWorktreeRoot(startDir)
	if err != nil {
		return false
	}
	projectRoot, err := config.ResolveProjectRoot(startDir)
	if err != nil {
		return false
	}
	if strings.TrimSpace(worktreeRoot) == "" || strings.TrimSpace(projectRoot) == "" {
		return false
	}
	return filepath.Clean(worktreeRoot) != filepath.Clean(projectRoot)
}

func printRootUsage() {
	usage, err := clitext.Render("root_usage", nil)
	if err != nil {
		cli.PrintUsage()
		return
	}
	replacements := map[string]string{
		"session <subcommand>  Session commands (start|attach|stop|status|diagnose)":                                     "session <subcommand>  Session commands (start|attach|stop|status|diagnose|restart-all|resolve-conflict)",
		"branch <subcommand>   Branch commands (merge)":                                                                  "branch <subcommand>   Branch commands (merge|agent-merge)",
		"az session status az-123  # Show status for az-123":                                                             "az session status az-123  # Show status for az-123\n  az session restart-all --force-busy\n  az session resolve-conflict az-123 --file README.md",
		"az branch merge az-123    # Repair/manual merge into resolved target branch (normal close uses az issue close)": "az branch merge az-123    # Repair/manual merge into resolved target branch (normal close uses az issue close)\n  az branch agent-merge az-123 --target base",
	}
	for old, new := range replacements {
		usage = strings.ReplaceAll(usage, old, new)
	}
	usage = strings.TrimRight(usage, "\n") + "\n\nDeprecated aliases:\n  az session kill <issue-id> [--wait]  Alias for az session stop\n  az kill <issue-id> [--wait]          Alias for az stop\n"
	fmt.Print(usage)
}

func printSessionUsage() {
	fmt.Println("Usage: az session <start|attach|stop|status|diagnose|restart-all|resolve-conflict> [arguments]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start <issue-id>      Start a session for an issue")
	fmt.Println("  attach <issue-id>     Attach to an existing issue session")
	fmt.Println("  stop <issue-id>       Stop an issue session")
	fmt.Println("  status [issue-id]     Show all sessions or one issue session status")
	fmt.Println("  diagnose <issue-id>   Collect session, worktree, operation, hook, and log diagnostics")
	fmt.Println("  restart-all           Restart idle AI sessions and tell them to continue; use --force-busy to include busy sessions")
	fmt.Println("  resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
	fmt.Println("                        Launch conflict-resolution agent")
	fmt.Println()
	fmt.Println("Issue IDs:")
	fmt.Println("  Bare IDs are scoped to the current project. Use the tmux session form")
	fmt.Println("  <project-short-slug>-<issue-id> (for example az-bxc), or <project>:<issue-id>,")
	fmt.Println("  to target a registered project from another repo.")
	fmt.Println()
	fmt.Println("Deprecated aliases:")
	fmt.Println("  kill <issue-id>       Deprecated alias for stop")
}

func printWorktreeUsage() {
	fmt.Println("Usage: az worktree create [--project <project-id>] [--base <branch>] [--json] <issue-id>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  create <issue-id>     Create an issue worktree without starting a session")
}

func printIssueCreateUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: az issue create [--project <project-id>] [--parent <issue-id>] [--impl <implementation> ...] [--deferred] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--title text] [--description text] [--json] [<title>]")
	fmt.Fprintln(w, "Note: `az issue create \"Child task\"` auto-parents to AZEDARACH_ISSUE_ID when set; use `--parent <issue-id>` for another parent/root.")
	fmt.Fprintln(w, "Note: --impl only assigns implementation/spec variant metadata; it is not parent/root selection.")
}

func printIssueSplitUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: az issue split [--project <project-id>] [--parent <issue-id>] [--impl <implementation> ...] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--description text] [--json] <title>")
	fmt.Fprintln(w, "Note: use --parent or AZEDARACH_ISSUE_ID for parentage; --impl only assigns implementation/spec variant metadata.")
}

func printSessionCommandUsage(command string, namespaced bool) bool {
	usage, ok := sessionCommandUsage(command, namespaced)
	if !ok {
		return false
	}
	fmt.Println(usage)
	return true
}

func sessionCommandUsage(command string, namespaced bool) (string, bool) {
	switch command {
	case "start":
		if namespaced {
			return "usage: az session start [--project <project-id>] <issue-id> [--wait]", true
		}
		return "usage: az start <issue-id> [--wait]", true
	case "attach":
		if namespaced {
			return "usage: az session attach <issue-id>", true
		}
		return "usage: az attach <issue-id>", true
	case "stop":
		if namespaced {
			return "usage: az session stop <issue-id> [--wait]", true
		}
		return "usage: az stop <issue-id> [--wait]", true
	case "kill":
		if namespaced {
			return "usage: az session kill <issue-id> [--wait] (deprecated alias for az session stop)", true
		}
		return "usage: az kill <issue-id> [--wait] (deprecated alias for az stop)", true
	case "status":
		if namespaced {
			return "usage: az session status [issue-id]", true
		}
		return "usage: az status [issue-id]", true
	case "diagnose":
		if namespaced {
			return "usage: az session diagnose <issue-id>", true
		}
		return "", false
	case "restart-all":
		if namespaced {
			return "usage: az session restart-all [--force-busy] [--yolo] [--json]", true
		}
		return "", false
	case "resolve-conflict":
		if namespaced {
			return "usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]", true
		}
		return "", false
	default:
		return "", false
	}
}

func sessionHelpRequested(values ...string) bool {
	for _, value := range values {
		switch strings.TrimSpace(value) {
		case "-h", "--help":
			return true
		}
	}
	return false
}

func sessionStartUsage(namespaced bool) string {
	if namespaced {
		return "usage: az session start [--project <project-id>] <issue-id> [--wait]"
	}
	return "usage: az start <issue-id> [--wait]"
}

func parseSessionStartArgs(args []string, namespaced bool) (string, cli.SessionCommandOptions, error) {
	return cli.ParseSessionStartArgs(args, namespaced, sessionStartUsage(namespaced))
}

// runCommand executes a CLI command with dependency injection
func runCommand(cfg *config.Config, fn func(*cli.Dependencies) error) error {
	depsStartedAt := time.Now()
	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize dependencies: %w", err)
	}
	commandShape := latencytrace.CommandShape(os.Args[1:])
	latencytrace.LogPhase(deps.Logger, "cli", "dependencies_init", depsStartedAt, "command_shape", commandShape)
	latencytrace.LogPhase(deps.Logger, "cli", "process_to_dependencies_ready", processStartedAt, "command_shape", commandShape)
	audit := beginCommandAudit(deps.Logger, deps, commandShape, os.Args[1:])
	commandStartedAt := time.Now()
	err = fn(deps)
	finishCommandAudit(deps.Logger, audit, err)
	attrs := []any{"command_shape", commandShape}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	latencytrace.LogPhase(deps.Logger, "cli", "command_execute", commandStartedAt, attrs...)
	return err
}

func runCommandAtRepoDir(cfg *config.Config, repoDir string, fn func(*cli.Dependencies) error) error {
	depsStartedAt := time.Now()
	deps, err := cli.NewDependenciesAt(cfg, repoDir)
	if err != nil {
		return fmt.Errorf("failed to initialize dependencies: %w", err)
	}
	commandShape := latencytrace.CommandShape(os.Args[1:])
	latencytrace.LogPhase(deps.Logger, "cli", "dependencies_init", depsStartedAt, "command_shape", commandShape, "repo_dir", repoDir)
	latencytrace.LogPhase(deps.Logger, "cli", "process_to_dependencies_ready", processStartedAt, "command_shape", commandShape, "repo_dir", repoDir)
	audit := beginCommandAudit(deps.Logger, deps, commandShape, os.Args[1:])
	commandStartedAt := time.Now()
	err = fn(deps)
	finishCommandAudit(deps.Logger, audit, err)
	attrs := []any{"command_shape", commandShape, "repo_dir", repoDir}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	latencytrace.LogPhase(deps.Logger, "cli", "command_execute", commandStartedAt, attrs...)
	return err
}

func runSessionCommand(cfg *config.Config, command string, args []string, namespaced bool) error {
	switch command {
	case "start":
		if sessionHelpRequested(args...) {
			if namespaced {
				return fmt.Errorf("%s", sessionStartUsage(namespaced))
			}
			return fmt.Errorf("%s", sessionStartUsage(namespaced))
		}
		issueID, opts, err := parseSessionStartArgs(args, namespaced)
		if err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StartCommandWithOptions(deps, issueID, opts)
		})
	case "attach":
		if sessionHelpRequested(args...) {
			if namespaced {
				return fmt.Errorf("usage: az session attach <issue-id>")
			}
			return fmt.Errorf("usage: az attach <issue-id>")
		}
		if len(args) != 1 {
			if namespaced {
				return fmt.Errorf("usage: az session attach <issue-id>")
			}
			return fmt.Errorf("usage: az attach <issue-id>")
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.AttachCommand(deps, args[0])
		})
	case "stop", "kill":
		canonicalUsage := "usage: az session stop <issue-id> [--wait]"
		aliasUsage := "usage: az stop <issue-id> [--wait]"
		if command == "kill" {
			if namespaced {
				canonicalUsage = "usage: az session kill <issue-id> [--wait] (deprecated alias for az session stop)"
			} else {
				aliasUsage = "usage: az kill <issue-id> [--wait] (deprecated alias for az stop)"
			}
		}
		if sessionHelpRequested(args...) {
			if namespaced {
				return fmt.Errorf("%s", canonicalUsage)
			}
			return fmt.Errorf("%s", aliasUsage)
		}
		if len(args) < 1 || len(args) > 2 {
			if namespaced {
				return fmt.Errorf("%s", canonicalUsage)
			}
			return fmt.Errorf("%s", aliasUsage)
		}
		opts := cli.SessionCommandOptions{}
		if len(args) == 2 {
			if args[1] != "--wait" {
				if namespaced {
					return fmt.Errorf("%s", canonicalUsage)
				}
				return fmt.Errorf("%s", aliasUsage)
			}
			opts.Wait = true
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.KillCommandWithOptions(deps, args[0], opts)
		})
	case "status":
		if sessionHelpRequested(args...) {
			if namespaced {
				return fmt.Errorf("usage: az session status [issue-id]")
			}
			return fmt.Errorf("usage: az status [issue-id]")
		}
		issueID := ""
		if len(args) == 1 {
			issueID = args[0]
		} else if len(args) > 1 {
			if namespaced {
				return fmt.Errorf("usage: az session status [issue-id]")
			}
			return fmt.Errorf("usage: az status [issue-id]")
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StatusCommand(deps, issueID)
		})
	case "diagnose":
		if !namespaced {
			return fmt.Errorf("unknown session command: %s", command)
		}
		if sessionHelpRequested(args...) || len(args) != 1 {
			return fmt.Errorf("usage: az session diagnose <issue-id>")
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.SessionDiagnoseCommand(deps, args[0])
		})
	case "restart-all":
		if !namespaced {
			return fmt.Errorf("unknown session command: %s", command)
		}
		if sessionHelpRequested(args...) {
			return fmt.Errorf("usage: az session restart-all [--force-busy] [--yolo] [--json]")
		}
		opts, err := cli.ParseSessionRestartAllArgs(args)
		if err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.SessionRestartAllCommand(deps, opts)
		})
	case "resolve-conflict":
		if !namespaced {
			return fmt.Errorf("unknown session command: %s", command)
		}
		if sessionHelpRequested(args...) {
			return fmt.Errorf("usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
		}
		opts, err := parseSessionResolveConflictArgs(args)
		if err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.SessionResolveConflictCommand(deps, opts)
		})
	default:
		if namespaced {
			return fmt.Errorf("unknown session command: %s (usage: az session <start|attach|stop|status|diagnose|restart-all|resolve-conflict>)", command)
		}
		return fmt.Errorf("unknown session command: %s", command)
	}
}

func runBranchCommand(cfg *config.Config, command string, args []string) error {
	switch command {
	case "merge", "merge-to-base":
		opts, err := parseBranchMergeArgs(args)
		if err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.BranchMergeToBaseCommandWithOptions(deps, opts)
		})
	case "agent-merge":
		if sessionHelpRequested(args...) {
			return fmt.Errorf("usage: az branch agent-merge [--project <project-id>] <issue-id> [--target base|<issue-id>]")
		}
		opts, err := parseBranchAgentMergeArgs(args)
		if err != nil {
			return err
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.BranchAgentMergeCommand(deps, opts)
		})
	default:
		if command == "m2m" {
			return fmt.Errorf("unknown branch command: %s (did you mean `az branch merge`?)", command)
		}
		return fmt.Errorf("unknown branch command: %s (usage: az branch <merge|agent-merge>)", command)
	}
}

func branchCommandUsage(command string) (string, bool) {
	switch command {
	case "merge", "merge-to-base":
		return "usage: az branch merge [--project <project-id>] [issue-id]", true
	case "agent-merge":
		return "usage: az branch agent-merge [--project <project-id>] <issue-id> [--target base|<issue-id>]", true
	default:
		return "", false
	}
}

func parseBranchMergeArgs(args []string) (cli.BranchMergeToBaseOptions, error) {
	opts := cli.BranchMergeToBaseOptions{}
	usageErr := func() (cli.BranchMergeToBaseOptions, error) {
		return cli.BranchMergeToBaseOptions{}, fmt.Errorf("usage: az branch merge [--project <project-id>] [issue-id]")
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		trimmed := strings.TrimSpace(arg)
		switch trimmed {
		case "--allow-base-for-child":
			opts.AllowBaseForChild = true
		case "--project":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(strings.TrimSpace(args[i+1]), "-") {
				return usageErr()
			}
			i++
			opts.Project = strings.TrimSpace(args[i])
		case "":
			continue
		default:
			if strings.HasPrefix(trimmed, "--project=") {
				opts.Project = strings.TrimSpace(strings.TrimPrefix(trimmed, "--project="))
				if opts.Project == "" {
					return usageErr()
				}
				continue
			}
			if strings.HasPrefix(trimmed, "--") {
				return usageErr()
			}
			if opts.IssueID != "" {
				return usageErr()
			}
			opts.IssueID = trimmed
		}
	}
	return opts, nil
}

func parseSessionResolveConflictArgs(args []string) (cli.SessionResolveConflictOptions, error) {
	opts := cli.SessionResolveConflictOptions{}
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--worktree":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
			}
			opts.Worktree = args[i]
		case "--file":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
			}
			opts.ConflictFiles = append(opts.ConflictFiles, args[i])
		case "--prompt":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
			}
			opts.Prompt = args[i]
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
			}
			if strings.TrimSpace(opts.IssueID) != "" {
				return opts, fmt.Errorf("usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
			}
			opts.IssueID = arg
		}
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return opts, fmt.Errorf("usage: az session resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
	}
	return opts, nil
}

func parseBranchAgentMergeArgs(args []string) (cli.BranchAgentMergeOptions, error) {
	opts := cli.BranchAgentMergeOptions{Target: "base"}
	usageErr := func() (cli.BranchAgentMergeOptions, error) {
		return opts, fmt.Errorf("usage: az branch agent-merge [--project <project-id>] <issue-id> [--target base|<issue-id>]")
	}
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--project":
			i++
			if i >= len(args) || strings.TrimSpace(args[i]) == "" || strings.HasPrefix(strings.TrimSpace(args[i]), "-") {
				return usageErr()
			}
			opts.Project = strings.TrimSpace(args[i])
		case "--target":
			i++
			if i >= len(args) {
				return usageErr()
			}
			opts.Target = args[i]
		default:
			if strings.HasPrefix(arg, "--project=") {
				opts.Project = strings.TrimSpace(strings.TrimPrefix(arg, "--project="))
				if opts.Project == "" {
					return usageErr()
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return usageErr()
			}
			if strings.TrimSpace(opts.IssueID) != "" {
				return usageErr()
			}
			opts.IssueID = arg
		}
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return usageErr()
	}
	return opts, nil
}
