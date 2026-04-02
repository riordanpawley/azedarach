package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/riordanpawley/azedarach/internal/buildinfo"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/daemon"
	"github.com/riordanpawley/azedarach/internal/logging"
)

func main() {
	var socketPath string
	var lockPath string
	var repoDir string
	var showVersion bool

	flag.StringVar(&socketPath, "socket", "", "unix socket path")
	flag.StringVar(&lockPath, "lock", "", "lock file path")
	flag.StringVar(&repoDir, "repo", "", "repository root")
	flag.BoolVar(&showVersion, "version", false, "print version")
	flag.BoolVar(&showVersion, "v", false, "print version")
	flag.Parse()

	if showVersion || (len(flag.Args()) == 1 && flag.Args()[0] == "version") {
		fmt.Println(buildinfo.VersionString())
		return
	}

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
		socketPath = config.DaemonSocketPathFor(repoDir)
	}
	if lockPath == "" {
		lockPath = config.DaemonLockPathFor(repoDir)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if scopeWatchPath := resolveScopedWorktreeWatchPath(repoDir); scopeWatchPath != "" {
		startWorktreeExistenceWatch(ctx, cancel, scopeWatchPath, 2*time.Second)
	}

	d := daemon.New(daemon.Config{
		RepoDir:              repoDir,
		SocketPath:           socketPath,
		LockPath:             lockPath,
		BaseBranch:           cfg.Git.BaseBranch,
		CLITool:              cfg.CLITool,
		SessionShell:         cfg.Session.Shell,
		SessionInitCommands:  cfg.Session.InitCommands,
		Logger:              newDaemonLogger(),
		WorktreeInitCommands: cfg.Worktree.InitCommands,
	})
	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "daemon failed: %v\n", err)
		os.Exit(1)
	}
}

func newDaemonLogger() *slog.Logger {
	// Daemon stderr is redirected to daemon.log by the launcher.
	return logging.NewTextStreamLogger(os.Stderr, slog.LevelInfo)
}

func resolveScopedWorktreeWatchPath(repoDir string) string {
	if !isScopedDaemonMode(os.Getenv("AZEDARACH_DAEMON_SCOPE"), os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE")) {
		return ""
	}
	if strings.TrimSpace(repoDir) == "" {
		return ""
	}
	root, err := config.ResolveWorktreeRoot(repoDir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(root)
}

func isScopedDaemonMode(mode, source string) bool {
	mode = strings.TrimSpace(strings.ToLower(mode))
	source = strings.TrimSpace(strings.ToLower(source))
	switch mode {
	case "worktree", "scoped", "local":
		return source == "just-run"
	default:
		return false
	}
}

func startWorktreeExistenceWatch(ctx context.Context, stop context.CancelFunc, watchPath string, interval time.Duration) {
	if strings.TrimSpace(watchPath) == "" || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(watchPath); err != nil {
					stop()
					return
				}
			}
		}
	}()
}
