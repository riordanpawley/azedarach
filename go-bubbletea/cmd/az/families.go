package main

import (
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
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
		cli.PrintDevUsage()
		return nil
	}

	switch args[0] {
	case "gate":
		return runGateCommand(cfg, args[1:])
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
	if len(args) == 0 || isHelpArg(args[0]) {
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

func isHelpArg(arg string) bool {
	return strings.EqualFold(arg, "help") || arg == "-h" || arg == "--help"
}
