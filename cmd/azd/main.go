package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/riordanpawley/azedarach/internal/buildinfo"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/daemon"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/logging"
)

var daemonExecutable = os.Executable

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
	latencytrace.SetConfigEnabled(cfg.Diagnostics.LatencyTrace)

	if socketPath == "" {
		socketPath = config.DaemonSocketPathFor(repoDir)
	}
	if lockPath == "" {
		lockPath = config.DaemonLockPathFor(repoDir)
	}
	if err := validateDaemonLaunchFence(socketPath); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	scopedRuntime := config.UseScopedDaemonRuntimeFor(repoDir)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if scopeWatchPath := resolveScopedWorktreeWatchPath(repoDir); scopeWatchPath != "" {
		startWorktreeExistenceWatch(ctx, cancel, scopeWatchPath, 2*time.Second)
	}
	outputRedirect, err := redirectDaemonProcessOutput(repoDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to configure daemon log rotation: %v\n", err)
	} else {
		defer outputRedirect.Close()
	}
	logger := newDaemonLogger()
	slog.SetDefault(logger)

	d := daemon.New(daemon.Config{
		RepoDir:                    repoDir,
		SocketPath:                 socketPath,
		LockPath:                   lockPath,
		ScopedRuntime:              scopedRuntime,
		BaseBranch:                 cfg.Git.BaseBranch,
		GitWorkflowMode:            cfg.Git.WorkflowMode,
		CLITool:                    cfg.CLITool,
		DangerouslySkipPermissions: cfg.Session.DangerouslySkipPermissions,
		SessionShell:               cfg.Session.Shell,
		SessionInitCommands:        cfg.Session.InitCommands,
		SessionSideEffectCommands:  cfg.Session.SideEffectCommands,
		Logger:                     logger,
		WorktreeInitCommands:       cfg.Worktree.InitCommands,
		IssueResources:             cfg.IssueResources,
	})
	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "daemon failed: %v\n", err)
		os.Exit(1)
	}
}

func newDaemonLogger() *slog.Logger {
	// Daemon stderr is redirected to a rotating azd.log by this process.
	return logging.NewTextStreamLogger(os.Stderr, slog.LevelInfo)
}

type daemonProcessOutputRedirect struct {
	previousStdout *os.File
	previousStderr *os.File
	reader         *os.File
	writer         *os.File
	logFile        io.Closer
	done           chan struct{}
}

func redirectDaemonProcessOutput(repoDir string) (*daemonProcessOutputRedirect, error) {
	logFile, err := logging.OpenRotatingFile(filepath.Join(repoDir, ".azedarach", logging.DaemonLogFileName), logging.DefaultMaxLogBytes, logging.DefaultLogBackups)
	if err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("create daemon output pipe: %w", err)
	}
	redirect := &daemonProcessOutputRedirect{
		previousStdout: os.Stdout,
		previousStderr: os.Stderr,
		reader:         reader,
		writer:         writer,
		logFile:        logFile,
		done:           make(chan struct{}),
	}
	os.Stdout = writer
	os.Stderr = writer
	go func() {
		defer close(redirect.done)
		defer reader.Close()
		_, _ = io.Copy(logFile, reader)
	}()
	return redirect, nil
}

func (r *daemonProcessOutputRedirect) Close() error {
	if r == nil {
		return nil
	}
	os.Stdout = r.previousStdout
	os.Stderr = r.previousStderr
	err := r.writer.Close()
	<-r.done
	if closeErr := r.logFile.Close(); err == nil {
		err = closeErr
	}
	return err
}

func validateDaemonLaunchFence(socketPath string) error {
	executable, err := daemonExecutable()
	if err != nil || strings.TrimSpace(executable) == "" {
		return nil
	}
	return config.ValidateSharedDaemonExecutable(socketPath, executable)
}

func resolveScopedWorktreeWatchPath(repoDir string) string {
	if !config.UseScopedDaemonRuntimeFor(repoDir) {
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
