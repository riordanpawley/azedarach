package tmuxselector

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/logging"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

const selectorPopupStartEpochEnvKey = "AZEDARACH_TMUX_SELECTOR_POPUP_START_EPOCH"

func Run(cfg *config.Config) error {
	processStart := time.Now()
	logger := newSelectorLogger(cfg)
	slog.SetDefault(logger)
	logSelectorProcessStart(logger, processStart)
	tmuxClient := tmux.NewClient(&tmux.ExecRunner{}, logger)
	uiStateSocketPath := config.GlobalDaemonSocketPath()
	validationStart := time.Now()
	logger.Info("tmux selector shared daemon validation starting",
		"elapsed_ms", validationStart.Sub(processStart).Milliseconds(),
		"socket_path", uiStateSocketPath,
	)
	if err := validateSharedDaemonExecutable(uiStateSocketPath); err != nil {
		logger.Warn("tmux selector shared daemon validation failed",
			"elapsed_ms", time.Since(validationStart).Milliseconds(),
			"total_elapsed_ms", time.Since(processStart).Milliseconds(),
			"socket_path", uiStateSocketPath,
			"error", err,
		)
		return err
	}
	logger.Info("tmux selector shared daemon validation completed",
		"elapsed_ms", time.Since(validationStart).Milliseconds(),
		"total_elapsed_ms", time.Since(processStart).Milliseconds(),
		"socket_path", uiStateSocketPath,
	)
	model := New(
		NewDefaultGlobalInventoryLoader(tmuxClient, logger),
		WithSwitcher(tmuxClient),
		WithKiller(NewDaemonKiller(tmuxClient, logger)),
		WithDetailOpener(NewDaemonDetailOpener(logger)),
		WithUIStateStore(daemonclient.New(transport.NewClient(uiStateSocketPath))),
	)
	logger.Info("tmux selector model constructed",
		"elapsed_ms", time.Since(processStart).Milliseconds(),
		"ui_state_store", true,
	)
	p := tea.NewProgram(model, tea.WithAltScreen())
	runStart := time.Now()
	logger.Info("tmux selector tea program starting",
		"elapsed_ms", runStart.Sub(processStart).Milliseconds(),
	)
	_, err := p.Run()
	if err != nil {
		logger.Warn("tmux selector tea program failed",
			"elapsed_ms", time.Since(runStart).Milliseconds(),
			"total_elapsed_ms", time.Since(processStart).Milliseconds(),
			"error", err,
		)
		return err
	}
	logger.Info("tmux selector tea program exited",
		"elapsed_ms", time.Since(runStart).Milliseconds(),
		"total_elapsed_ms", time.Since(processStart).Milliseconds(),
	)
	return err
}

func logSelectorProcessStart(logger *slog.Logger, processStart time.Time) {
	if logger == nil {
		return
	}
	args := []any{
		"elapsed_ms", time.Since(processStart).Milliseconds(),
		"pid", os.Getpid(),
	}
	if executable, err := os.Executable(); err == nil && strings.TrimSpace(executable) != "" {
		args = append(args, "executable", executable)
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		args = append(args, "cwd", cwd)
	}
	if popupStartEpoch := strings.TrimSpace(os.Getenv(selectorPopupStartEpochEnvKey)); popupStartEpoch != "" {
		args = append(args, "popup_start_epoch", popupStartEpoch)
		if seconds, err := strconv.ParseInt(popupStartEpoch, 10, 64); err == nil && seconds > 0 {
			args = append(args, "popup_to_process_start_ms", processStart.Sub(time.Unix(seconds, 0)).Milliseconds())
		}
	}
	logger.Info("tmux selector process started", args...)
}

func newSelectorLogger(cfg *config.Config) *slog.Logger {
	logPath := selectorLogPath(cfg)
	if logPath == "" {
		return logging.NewDiscardLogger(slog.LevelInfo)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return logging.NewDiscardLogger(slog.LevelInfo)
	}
	logFile, err := logging.OpenRotatingFile(logPath, logging.DefaultMaxLogBytes, logging.DefaultLogBackups)
	if err != nil {
		return logging.NewDiscardLogger(slog.LevelInfo)
	}
	return logging.NewTextStreamLogger(logFile, slog.LevelInfo)
}

func selectorLogPath(cfg *config.Config) string {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	logDir := config.SessionLogDirFor(cfg, "")
	return filepath.Join(logDir, logging.TmuxSelectorLogFileName)
}
