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
			fmt.Fprintf(os.Stderr, "Usage: az start <bead-id>\n")
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
			fmt.Fprintf(os.Stderr, "Usage: az attach <bead-id>\n")
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
			fmt.Fprintf(os.Stderr, "Usage: az kill <bead-id>\n")
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.KillCommand(deps, commandArgs[0])
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "status":
		beadID := ""
		if len(commandArgs) == 1 {
			beadID = commandArgs[0]
		} else if len(commandArgs) > 1 {
			fmt.Fprintf(os.Stderr, "Usage: az status [bead-id]\n")
			os.Exit(1)
		}
		if err := runCommand(cfg, func(deps *cli.Dependencies) error {
			return cli.StatusCommand(deps, beadID)
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
