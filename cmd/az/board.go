package main

import (
	"fmt"
	"os"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
)

func runBoardCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: az board view <list|get|select|create|update|delete|explain> [arguments]")
	}
	switch args[0] {
	case "help", "-h", "--help":
		printBoardUsage()
		return nil
	case "view":
		return runBoardViewCommand(cfg, args[1:])
	default:
		return fmt.Errorf("unknown board command: %s", args[0])
	}
}

func runBoardViewCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: az board view <list|get|select|create|update|delete|explain> [arguments]")
	}
	command := args[0]
	if command == "help" || command == "-h" || command == "--help" {
		printBoardViewUsage()
		return nil
	}
	if sessionHelpRequested(args[1:]...) {
		if printBoardViewCommandUsage(command) {
			return nil
		}
	}
	opts, err := cli.ParseBoardViewArgs(command, args[1:])
	if err != nil {
		if !printBoardViewCommandUsage(command) {
			printBoardViewUsage()
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return runCommand(cfg, func(deps *cli.Dependencies) error {
		return cli.BoardViewCommand(deps, opts)
	})
}

func printBoardUsage() {
	fmt.Println("Usage: az board view <list|get|select|create|update|delete|explain> [arguments]")
}

func printBoardViewUsage() {
	fmt.Println("Usage:")
	fmt.Println("  az board view list [--project <project-id>] [--json]")
	fmt.Println("  az board view get [--project <project-id>] [--json] <view-id>")
	fmt.Println("  az board view select [--project <project-id>] [--json] <view-id>")
	fmt.Println("  az board view create [--project <project-id>] --file <path|-> [--select] [--json]")
	fmt.Println("  az board view update [--project <project-id>] --file <path|-> [--select] [--json]")
	fmt.Println("  az board view delete [--project <project-id>] --confirm [--json] <view-id>")
	fmt.Println("  az board view explain [--project <project-id>] [--view <view-id>] [--json] <issue-id>")
	fmt.Println()
	fmt.Println("Built-in views: default, orchestration, closeout (legacy current/activity aliases are accepted).")
}

func printBoardViewCommandUsage(command string) bool {
	switch command {
	case "list":
		fmt.Println("Usage: az board view list [--project <project-id>] [--json]")
	case "get":
		fmt.Println("Usage: az board view get [--project <project-id>] [--json] <view-id>")
	case "select":
		fmt.Println("Usage: az board view select [--project <project-id>] [--json] <view-id>")
	case "create":
		fmt.Println("Usage: az board view create [--project <project-id>] --file <path|-> [--select] [--json]")
	case "update":
		fmt.Println("Usage: az board view update [--project <project-id>] --file <path|-> [--select] [--json]")
	case "delete":
		fmt.Println("Usage: az board view delete [--project <project-id>] --confirm [--json] <view-id>")
	case "explain":
		fmt.Println("Usage: az board view explain [--project <project-id>] [--view <view-id>] [--json] <issue-id>")
	default:
		return false
	}
	return true
}
