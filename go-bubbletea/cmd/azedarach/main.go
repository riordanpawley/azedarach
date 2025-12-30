// Package main provides the entry point for the Azedarach TUI application.
//
// Azedarach is a TUI Kanban board for orchestrating parallel Claude Code sessions
// with Beads task tracking. This Go/Bubbletea implementation uses The Elm
// Architecture (TEA) for state management.
//
// Usage:
//
//	azedarach [options]
//
// For more information, see the PLAN.md file in this directory.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/app"
	"github.com/riordanpawley/azedarach/internal/config"
)

func main() {
	// TODO: Parse CLI arguments
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	model := app.New(cfg)
	program := tea.NewProgram(
		model,
		tea.WithAltScreen(),       // Use alternate screen buffer
		tea.WithMouseCellMotion(), // Enable mouse support
	)

	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
