package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/app"
	"github.com/riordanpawley/azedarach/internal/buildinfo"
	"github.com/riordanpawley/azedarach/internal/cli"
	clitext "github.com/riordanpawley/azedarach/internal/cli/text"
	"github.com/riordanpawley/azedarach/internal/config"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "version", "-v", "--version":
			fmt.Println(buildinfo.VersionString())
			return
		case "help", "-h", "--help":
			cli.PrintUsage()
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
			fmt.Fprintf(os.Stderr, "Usage: az session <start|attach|kill|status> [arguments]\n")
			os.Exit(1)
		}
		sessionCommand := commandArgs[0]
		sessionArgs := commandArgs[1:]
		if sessionCommand == "help" || sessionCommand == "-h" || sessionCommand == "--help" {
			helpText, err := clitext.Render("session_help", nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Print(helpText)
			os.Exit(0)
		}
		if err := runSessionCommand(cfg, sessionCommand, sessionArgs, true); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "branch":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az branch <merge> [arguments]\n")
			os.Exit(1)
		}
		branchCommand := commandArgs[0]
		branchArgs := commandArgs[1:]
		if err := runBranchCommand(cfg, branchCommand, branchArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "start":
		if err := runSessionCommand(cfg, command, commandArgs, false); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "attach":
		if err := runSessionCommand(cfg, command, commandArgs, false); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "kill":
		if err := runSessionCommand(cfg, command, commandArgs, false); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "status":
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
			fmt.Fprintf(os.Stderr, "Usage: az impl delete --confirm <implementation>\n")
			os.Exit(1)
		}
		implCommand := commandArgs[0]
		implArgs := commandArgs[1:]
		if implCommand == "help" || implCommand == "-h" || implCommand == "--help" {
			fmt.Println("Usage: az impl delete --confirm <implementation>")
			os.Exit(0)
		}
		switch implCommand {
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
		default:
			fmt.Fprintf(os.Stderr, "Unknown impl command: %s\n", implCommand)
			fmt.Fprintf(os.Stderr, "Usage: az impl delete --confirm <implementation>\n")
			os.Exit(1)
		}

	case "opencode":
		if err := runOpenCodeCommand(cfg, commandArgs); err != nil {
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
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|get|get-many|check|doctor|create|update|close|delete|dep|bulk-create|bulk-update|fanout> [arguments]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue list [--project <project-id>] [--json] [--deps] [--limit N] [--id <id> ...] [--ids a,b,c]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue doctor [--project <project-id>] [--id <issue-id>] [<issue-id>]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue create [--project <project-id>] <title> --impl <implementation> [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--description text]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue update [--project <project-id>] [--id <issue-id>] [<issue-id>] [--title text] [--description text] [--append-notes text] [--status open|in_progress|blocked|closed] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--update-impl <impl> ...]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue close [--project <project-id>] [--id <issue-id>] [<issue-id>]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue delete [--project <project-id>] --confirm [--id <issue-id>] [<issue-id>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueDeleteCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
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
					fmt.Fprintf(os.Stderr, "Usage: az issue dep add [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from]\n")
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
					fmt.Fprintf(os.Stderr, "Usage: az issue dep remove [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from] [--confirm]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue bulk-create [--project <project-id>] --impl <implementation> --input <path> [--dry-run]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue bulk-update [--project <project-id>] --impl <implementation> --input <path> [--dry-run]\n")
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
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|get|get-many|check|doctor|create|update|close|delete|dep|bulk-create|bulk-update|fanout> [arguments]\n")
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
		if len(commandArgs) != 1 || commandArgs[0] != "restart" {
			fmt.Fprintf(os.Stderr, "Usage: az daemon restart\n")
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.RestartDaemonCommand(deps)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
		cli.PrintUsage()
		os.Exit(1)
	}
}

// runTUI starts the terminal user interface
func runTUI(cfg *config.Config) {
	model := app.New(cfg)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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
		if len(args) != 1 {
			if namespaced {
				return fmt.Errorf("usage: az session attach <issue-id>")
			}
			return fmt.Errorf("usage: az attach <issue-id>")
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.AttachCommand(deps, args[0])
		})
	case "kill":
		if len(args) < 1 || len(args) > 2 {
			if namespaced {
				return fmt.Errorf("usage: az session kill <issue-id> [--wait]")
			}
			return fmt.Errorf("usage: az kill <issue-id> [--wait]")
		}
		opts := cli.SessionCommandOptions{}
		if len(args) == 2 {
			if args[1] != "--wait" {
				if namespaced {
					return fmt.Errorf("usage: az session kill <issue-id> [--wait]")
				}
				return fmt.Errorf("usage: az kill <issue-id> [--wait]")
			}
			opts.Wait = true
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.KillCommandWithOptions(deps, args[0], opts)
		})
	case "status":
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
	default:
		if namespaced {
			return fmt.Errorf("unknown session command: %s (usage: az session <start|attach|kill|status>)", command)
		}
		return fmt.Errorf("unknown session command: %s", command)
	}
}

func runBranchCommand(cfg *config.Config, command string, args []string) error {
	switch command {
	case "merge", "merge-to-main":
		if len(args) > 1 {
			return fmt.Errorf("usage: az branch merge [issue-id]")
		}
		issueID := ""
		if len(args) == 1 {
			issueID = args[0]
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.BranchMergeToMainCommand(deps, issueID)
		})
	default:
		if command == "m2m" {
			return fmt.Errorf("unknown branch command: %s (did you mean `az branch merge`?)", command)
		}
		return fmt.Errorf("unknown branch command: %s (usage: az branch <merge>)", command)
	}
}
