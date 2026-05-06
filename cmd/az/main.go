package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/buildinfo"
	"github.com/riordanpawley/azedarach/internal/cli"
	clitext "github.com/riordanpawley/azedarach/internal/cli/text"
	"github.com/riordanpawley/azedarach/internal/config"
	app "github.com/riordanpawley/azedarach/internal/tui"
)

func main() {
	args := os.Args[1:]
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
			fmt.Fprintf(os.Stderr, "Usage: az session <start|attach|stop|status> [arguments]\n")
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
			fmt.Fprintf(os.Stderr, "Usage: az operation <get|list|logs|cancel> [arguments]\n")
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
			fmt.Fprintf(os.Stderr, "Usage: az operation <get|list|logs|cancel> [arguments]\n")
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
			fmt.Fprintf(os.Stderr, "Usage: az config set spec.enabled <true|false> [--project-dir <dir>]\n")
			os.Exit(1)
		}
		configCommand := commandArgs[0]
		configArgs := commandArgs[1:]
		if configCommand == "help" || configCommand == "-h" || configCommand == "--help" {
			fmt.Println("Usage: az config set spec.enabled <true|false> [--project-dir <dir>]")
			os.Exit(0)
		}
		switch configCommand {
		case "set":
			opts, err := cli.ParseConfigSetArgs(configArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az config set spec.enabled <true|false> [--project-dir <dir>]\n")
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
			fmt.Fprintf(os.Stderr, "Usage: az config set spec.enabled <true|false> [--project-dir <dir>]\n")
			os.Exit(1)
		}

	case "spec":
		if err := runSpecCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "sync":
		if len(commandArgs) > 0 {
			if commandArgs[0] == "help" || commandArgs[0] == "-h" || commandArgs[0] == "--help" {
				fmt.Println("Usage: az sync [--all] [<directory>] [--project-dir <dir>]")
				os.Exit(0)
			}
		}
		syncOpts, err := cli.ParseSyncArgs(commandArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Usage: az sync [--all] [<directory>] [--project-dir <dir>]\n")
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

	case "notify":
		if err := runNotifyCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hooks":
		if err := runHooksCommand(cfg, commandArgs); err != nil {
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

	case "opencode":
		if err := runOpenCodeCommand(cfg, commandArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "codex":
		if err := runCodexCommand(cfg, commandArgs); err != nil {
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
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|get|get-many|check|doctor|create|update|close|delete|image|dep|bulk-create|bulk-update|fanout> [arguments]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue list [--project <project-id>] [--json] [--deps] [--status <status> ...] [--statuses a,b,c] [--limit N] [--id <id> ...] [--ids a,b,c]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue get [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueGetCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "get-many":
			opts, err := cli.ParseIssueGetManyArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue get-many [--project <project-id>] --id <issue-id> [--id <issue-id> ...] [--ids a,b,c] [--json]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue create [--project <project-id>] [--impl <implementation> ...] [--deferred] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--description text] [--json] <title>\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueCreateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "update":
			opts, err := cli.ParseIssueUpdateArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue update [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>] [--title text] [--description text] [--notes text] [--append-notes text] [--status open|in_progress|blocked|closed] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--update-impl <impl> ...]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue close [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]\n")
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
					fmt.Fprintf(os.Stderr, "Usage: az issue dep add [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from] [--json]\n")
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
					fmt.Fprintf(os.Stderr, "Usage: az issue dep remove [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from] [--confirm] [--json]\n")
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
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|get|get-many|check|doctor|create|update|close|delete|image|dep|bulk-create|bulk-update|fanout> [arguments]\n")
			os.Exit(1)
		}

	case "mail":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az mail <send|list|watch> [arguments]\n")
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
		default:
			fmt.Fprintf(os.Stderr, "Unknown mail command: %s\n", commandArgs[0])
			fmt.Fprintf(os.Stderr, "Usage: az mail <send|list|watch> [arguments]\n")
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
	model := app.NewWithOptions(cfg, opts...)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func validateTUILaunchContext() error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	if !isLinkedGitWorktree(cwd) || tuiScopedDaemonRuntimeEnabled() {
		return nil
	}
	return fmt.Errorf("refusing to start the TUI from a linked worktree without the scoped just-run environment; run `just run` from this worktree so ./bin/az and ./bin/azd are paired and AZEDARACH_DAEMON_SCOPE=worktree is set")
}

func tuiScopedDaemonRuntimeEnabled() bool {
	mode := strings.TrimSpace(strings.ToLower(os.Getenv("AZEDARACH_DAEMON_SCOPE")))
	source := strings.TrimSpace(strings.ToLower(os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE")))
	modeEnabled := mode == "worktree" || mode == "scoped" || mode == "local"
	return modeEnabled && source == "just-run"
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
		"session <subcommand>  Session commands (start|attach|kill|status)":             "session <subcommand>  Session commands (start|attach|stop|status|resolve-conflict)",
		"branch <subcommand>   Branch commands (merge)":                                 "branch <subcommand>   Branch commands (merge|agent-merge)",
		"kill <issue-id>       Alias for 'az session kill <issue-id>'":                  "stop <issue-id>       Alias for 'az session stop <issue-id>'",
		"az session kill az-123    # Kill issue az-123's session":                       "az session stop az-123    # Stop issue az-123's session",
		"az session status az-123  # Show status for az-123":                            "az session status az-123  # Show status for az-123\n  az session resolve-conflict az-123 --file README.md",
		"az branch merge az-123    # Merge issue branch into base branch (daemon path)": "az branch merge az-123    # Merge issue branch into base branch (daemon path)\n  az branch agent-merge az-123 --target base",
	}
	for old, new := range replacements {
		usage = strings.ReplaceAll(usage, old, new)
	}
	usage = strings.TrimRight(usage, "\n") + "\n\nDeprecated aliases:\n  az session kill <issue-id> [--wait]  Alias for az session stop\n  az kill <issue-id> [--wait]          Alias for az stop\n"
	fmt.Print(usage)
}

func printSessionUsage() {
	fmt.Println("Usage: az session <start|attach|stop|status|resolve-conflict> [arguments]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start <issue-id>      Start a session for an issue")
	fmt.Println("  attach <issue-id>     Attach to an existing issue session")
	fmt.Println("  stop <issue-id>       Stop an issue session")
	fmt.Println("  status [issue-id]     Show all sessions or one issue session status")
	fmt.Println("  resolve-conflict <issue-id> [--worktree <path>] [--file <path> ...] [--prompt <text>]")
	fmt.Println("                        Launch conflict-resolution agent")
	fmt.Println()
	fmt.Println("Deprecated aliases:")
	fmt.Println("  kill <issue-id>       Deprecated alias for stop")
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
			return "usage: az session start <issue-id> [--wait]", true
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

// runCommand executes a CLI command with dependency injection
func runCommand(cfg *config.Config, fn func(*cli.Dependencies) error) error {
	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize dependencies: %w", err)
	}
	return fn(deps)
}

func runCommandAtRepoDir(cfg *config.Config, repoDir string, fn func(*cli.Dependencies) error) error {
	deps, err := cli.NewDependenciesAt(cfg, repoDir)
	if err != nil {
		return fmt.Errorf("failed to initialize dependencies: %w", err)
	}
	return fn(deps)
}

func runSessionCommand(cfg *config.Config, command string, args []string, namespaced bool) error {
	switch command {
	case "start":
		if sessionHelpRequested(args...) {
			if namespaced {
				return fmt.Errorf("usage: az session start <issue-id> [--wait]")
			}
			return fmt.Errorf("usage: az start <issue-id> [--wait]")
		}
		if len(args) < 1 || len(args) > 2 {
			if namespaced {
				return fmt.Errorf("usage: az session start <issue-id> [--wait]")
			}
			return fmt.Errorf("usage: az start <issue-id> [--wait]")
		}
		opts := cli.SessionCommandOptions{}
		if len(args) == 2 {
			if args[1] != "--wait" {
				if namespaced {
					return fmt.Errorf("usage: az session start <issue-id> [--wait]")
				}
				return fmt.Errorf("usage: az start <issue-id> [--wait]")
			}
			opts.Wait = true
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StartCommandWithOptions(deps, args[0], opts)
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
			return fmt.Errorf("unknown session command: %s (usage: az session <start|attach|stop|status|resolve-conflict>)", command)
		}
		return fmt.Errorf("unknown session command: %s", command)
	}
}

func runBranchCommand(cfg *config.Config, command string, args []string) error {
	switch command {
	case "merge", "merge-to-base":
		if len(args) > 1 {
			return fmt.Errorf("usage: az branch merge [issue-id]")
		}
		issueID := ""
		if len(args) == 1 {
			issueID = args[0]
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.BranchMergeToBaseCommand(deps, issueID)
		})
	case "agent-merge":
		if sessionHelpRequested(args...) {
			return fmt.Errorf("usage: az branch agent-merge <issue-id> [--target base|<issue-id>]")
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
		return "usage: az branch merge [issue-id]", true
	case "agent-merge":
		return "usage: az branch agent-merge <issue-id> [--target base|<issue-id>]", true
	default:
		return "", false
	}
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
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "--target":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("usage: az branch agent-merge <issue-id> [--target base|<issue-id>]")
			}
			opts.Target = args[i]
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("usage: az branch agent-merge <issue-id> [--target base|<issue-id>]")
			}
			if strings.TrimSpace(opts.IssueID) != "" {
				return opts, fmt.Errorf("usage: az branch agent-merge <issue-id> [--target base|<issue-id>]")
			}
			opts.IssueID = arg
		}
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return opts, fmt.Errorf("usage: az branch agent-merge <issue-id> [--target base|<issue-id>]")
	}
	return opts, nil
}
