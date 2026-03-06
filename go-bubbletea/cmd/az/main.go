package main

import (
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/app"
	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
)

var loadConfig = config.Load

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	// Standalone prime command should not depend on runtime config.
	if len(args) > 0 && args[0] == "prime" {
		return handlePrimeCommand(args[1:], stdout, stderr)
	}
	// Standalone init command should not depend on runtime config.
	if len(args) > 0 && args[0] == "init" {
		return handleInitCommand(args[1:], stdout, stderr)
	}
	// Show command owns its own argument parsing and search dependency setup.
	if len(args) > 0 && args[0] == "show" {
		return handleShowCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "list" {
		return handleListCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "create" {
		return handleCreateCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "q" {
		return handleQuickCaptureCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "close" {
		return handleCloseCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "reopen" {
		return handleReopenCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "update" {
		return handleUpdateCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "delete" {
		return handleDeleteCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "ready" {
		return handleReadyCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "blocked" {
		return handleBlockedCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "search" {
		return handleSearchCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "stale" {
		return handleStaleCommand(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "count" {
		return handleCountCommand(args[1:], stdout, stderr)
	}

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "Error loading config: %v\n", err)
		return 1
	}

	// If no arguments, run the TUI
	if len(args) == 0 {
		return runTUI(cfg, stderr)
	}

	// Handle subcommands
	command := args[0]
	commandArgs := args[1:]

	switch command {
	case "start":
		if len(commandArgs) != 1 {
			fmt.Fprintf(stderr, "Usage: az start <bead-id>\n")
			return 1
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StartCommand(deps, commandArgs[0])
		}); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}

	case "attach":
		if len(commandArgs) != 1 {
			fmt.Fprintf(stderr, "Usage: az attach <bead-id>\n")
			return 1
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.AttachCommand(deps, commandArgs[0])
		}); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}

	case "kill":
		if len(commandArgs) != 1 {
			fmt.Fprintf(stderr, "Usage: az kill <bead-id>\n")
			return 1
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.KillCommand(deps, commandArgs[0])
		}); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}

	case "status":
		beadID := ""
		if len(commandArgs) == 1 {
			beadID = commandArgs[0]
		} else if len(commandArgs) > 1 {
			fmt.Fprintf(stderr, "Usage: az status [bead-id]\n")
			return 1
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StatusCommand(deps, beadID)
		}); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}

	case "help", "-h", "--help":
		cli.PrintUsage()

	default:
		fmt.Fprintf(stderr, "Unknown command: %s\n\n", command)
		cli.PrintUsage()
		return 1
	}

	return 0
}

// runTUI starts the terminal user interface
func runTUI(cfg *config.Config, stderr io.Writer) int {
	model := app.New(cfg)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	return 0
}

// runCommand executes a CLI command with dependency injection
func runCommand(cfg *config.Config, fn func(*cli.Dependencies) error) error {
	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize dependencies: %w", err)
	}
	return fn(deps)
}
