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
	case "start":
		if len(commandArgs) != 1 {
			fmt.Fprintf(os.Stderr, "Usage: az start <issue-id>\n")
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StartCommand(deps, commandArgs[0])
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "attach":
		if len(commandArgs) != 1 {
			fmt.Fprintf(os.Stderr, "Usage: az attach <issue-id>\n")
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.AttachCommand(deps, commandArgs[0])
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "kill":
		if len(commandArgs) != 1 {
			fmt.Fprintf(os.Stderr, "Usage: az kill <issue-id>\n")
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.KillCommand(deps, commandArgs[0])
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "status":
		issueID := ""
		if len(commandArgs) == 1 {
			issueID = commandArgs[0]
		} else if len(commandArgs) > 1 {
			fmt.Fprintf(os.Stderr, "Usage: az status [issue-id]\n")
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StatusCommand(deps, issueID)
		}); err != nil {
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
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|get> [arguments]\n")
			os.Exit(1)
		}
		issueCommand := commandArgs[0]
		issueArgs := commandArgs[1:]
		if issueCommand == "help" || issueCommand == "-h" || issueCommand == "--help" {
			fmt.Printf("Usage: az issue <list|get> [arguments]\n\n")
			fmt.Printf("Commands:\n")
			fmt.Printf("  list [--json]          List issues from daemon-backed store\n")
			fmt.Printf("  get <issue-id> [--json]  Show a single issue by ID\n")
			os.Exit(0)
		}
		switch issueCommand {
		case "list":
			opts, err := cli.ParseIssueListArgs(issueArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Usage: az issue list [--json]\n")
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
				fmt.Fprintf(os.Stderr, "Usage: az issue get <issue-id> [--json]\n")
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if err := runCommand(cfg, func(deps *cli.Dependencies) error {
				return cli.IssueGetCommand(deps, opts)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

		default:
			fmt.Fprintf(os.Stderr, "Unknown issue command: %s\n", issueCommand)
			fmt.Fprintf(os.Stderr, "Usage: az issue <list|get> [arguments]\n")
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
