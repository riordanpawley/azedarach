package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/daemon"
)

func main() {
	var socketPath string
	var lockPath string
	var repoDir string

	flag.StringVar(&socketPath, "socket", "", "unix socket path")
	flag.StringVar(&lockPath, "lock", "", "lock file path")
	flag.StringVar(&repoDir, "repo", "", "repository root")
	flag.Parse()

	if repoDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to resolve cwd: %v\n", err)
			os.Exit(1)
		}
		repoDir = cwd
	}

	cfg, err := config.LoadConfig(repoDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if socketPath == "" {
		socketPath = config.GlobalDaemonSocketPath()
	}
	if lockPath == "" {
		lockPath = config.GlobalDaemonLockPath()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d := daemon.New(daemon.Config{
		RepoDir:             repoDir,
		SocketPath:          socketPath,
		LockPath:            lockPath,
		BaseBranch:          cfg.Git.BaseBranch,
		CLITool:             cfg.CLITool,
		SessionShell:        cfg.Session.Shell,
		SessionInitCommands: cfg.Session.InitCommands,
	})
	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "daemon failed: %v\n", err)
		os.Exit(1)
	}
}
