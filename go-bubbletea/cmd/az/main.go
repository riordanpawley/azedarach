package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/app"
	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Parse command-line arguments
	args := os.Args[1:]

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
			fmt.Printf("Usage: az session <start|attach|kill|status> [arguments]\n\n")
			fmt.Printf("Commands:\n")
			fmt.Printf("  start <issue-id>      Start a session for an issue\n")
			fmt.Printf("  attach <issue-id>     Attach to an existing issue session\n")
			fmt.Printf("  kill <issue-id>       Kill an issue session\n")
			fmt.Printf("  status [issue-id]     Show all sessions or one issue session status\n")
			os.Exit(0)
		}
		if err := runSessionCommand(cfg, sessionCommand, sessionArgs, true); err != nil {
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

	case "issue":
		if len(commandArgs) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|get|get-many|check|doctor|create|update|status|close|delete|dep|bulk-create|bulk-update> [arguments]\n")
			os.Exit(1)
		}
		issueCommand := commandArgs[0]
		issueArgs := commandArgs[1:]
		if issueCommand == "help" || issueCommand == "-h" || issueCommand == "--help" {
			fmt.Printf("Usage: az issue <list|get|get-many|check|doctor|create|update|status|close|delete|dep|bulk-create|bulk-update> [arguments]\n\n")
			fmt.Printf("Commands:\n")
			fmt.Printf("  list [--json] [--deps] [--limit N] [--id <id> ...] [--ids a,b,c]  List issues from daemon-backed store\n")
			fmt.Printf("  get <issue-id> [--json] [--deps]  Show a single issue by ID\n")
			fmt.Printf("  get-many --id <issue-id> [--id <issue-id> ...] [--ids a,b,c] [--json]  Fetch many issues in requested order\n")
			fmt.Printf("  check <issue-id> [--json] [--deps]  Alias for get\n")
			fmt.Printf("  doctor <issue-id>  Run integrity checks for one issue\n")
			fmt.Printf("  create <title> --impl <implementation> [--type ...] [--priority ...] [--description ...]  Create an issue\n")
			fmt.Printf("  update <issue-id> --impl <implementation> [--title ...] [--description ...] [--type ...] [--priority ...]  Update issue fields\n")
			fmt.Printf("  status <issue-id> <open|in_progress|blocked|closed> --impl <implementation>  Set issue status\n")
			fmt.Printf("  close <issue-id> --impl <implementation>       Close an issue (sets status=closed)\n")
			fmt.Printf("  delete <issue-id> --impl <implementation> --confirm  Permanently delete an issue\n")
			fmt.Printf("  dep add --impl <implementation> --issue-id <issue-id> --depends-on-id <depends-on-id> [--type blocks|related|parent-child|discovered-from]  Add a dependency edge\n")
			fmt.Printf("  dep remove --impl <implementation> --issue-id <issue-id> --depends-on-id <depends-on-id> [--type blocks|related|parent-child|discovered-from] [--confirm]  Remove a dependency edge\n")
			fmt.Printf("  dep bulk apply --impl <implementation> --input <path> [--dry-run] [--json]  Apply dependency edge mutations\n")
			fmt.Printf("  bulk-create --impl <implementation> --input <path> [--dry-run]  Execute bulk create operations\n")
			fmt.Printf("  bulk-update --impl <implementation> --input <path> [--dry-run]  Execute bulk update operations\n")
			os.Exit(0)
		}
		switch issueCommand {
		case "list":
			opts, err := cli.ParseIssueListArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue list [--json] [--deps] [--limit N] [--id <id> ...] [--ids a,b,c]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue get [--id <issue-id>] [--json] [--deps] [<issue-id>]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue get-many --id <issue-id> [--id <issue-id> ...] [--ids a,b,c] [--json]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue check [--id <issue-id>] [--json] [--deps] [<issue-id>]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue doctor [--id <issue-id>] [<issue-id>]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue create <title> --impl <implementation> [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--description text]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue update --impl <implementation> [--id <issue-id>] [<issue-id>] [--title text] [--description text] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueUpdateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "status":
			opts, err := cli.ParseIssueStatusArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue status --impl <implementation> [--id <issue-id>] [--status <state>] [<issue-id>] [<state>]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueStatusCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		case "close":
			opts, err := cli.ParseIssueCloseArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue close --impl <implementation> [--id <issue-id>] [<issue-id>]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue delete --impl <implementation> --confirm [--id <issue-id>] [<issue-id>]\n")
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
					fmt.Fprintf(os.Stderr, "Usage: az issue dep add --impl <implementation> [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from]\n")
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
					fmt.Fprintf(os.Stderr, "Usage: az issue dep remove --impl <implementation> [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from] [--confirm]\n")
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
					fmt.Fprintf(os.Stderr, "Usage: az issue dep bulk apply --impl <implementation> --input <path> [--dry-run] [--json]\n")
					os.Exit(1)
				}
				opts, err := cli.ParseIssueDependencyBulkApplyArgs(depArgs[1:])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Usage: az issue dep bulk apply --impl <implementation> --input <path> [--dry-run] [--json]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue bulk-create --impl <implementation> --input <path> [--dry-run]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue bulk-update --impl <implementation> --input <path> [--dry-run]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueBulkUpdateCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		default:
			fmt.Fprintf(os.Stderr, "Unknown issue command: %s\n", issueCommand)
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|get|get-many|check|doctor|create|update|status|close|delete|dep|bulk-create|bulk-update> [arguments]\n")
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

	case "help", "-h", "--help":
		cli.PrintUsage()

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

func runSessionCommand(cfg *config.Config, command string, args []string, namespaced bool) error {
	switch command {
	case "start":
		if len(args) != 1 {
			if namespaced {
				return fmt.Errorf("usage: az session start <issue-id>")
			}
			return fmt.Errorf("usage: az start <issue-id>")
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StartCommand(deps, args[0])
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
		if len(args) != 1 {
			if namespaced {
				return fmt.Errorf("usage: az session kill <issue-id>")
			}
			return fmt.Errorf("usage: az kill <issue-id>")
		}
		return runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.KillCommand(deps, args[0])
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
