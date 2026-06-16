package tmuxselector

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/logging"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func Run(cfg *config.Config) error {
	logger := newSelectorLogger(cfg)
	slog.SetDefault(logger)
	tmuxClient := tmux.NewClient(&tmux.ExecRunner{}, logger)
	uiStateSocketPath := config.GlobalDaemonSocketPath()
	if err := validateSharedDaemonExecutable(uiStateSocketPath); err != nil {
		return err
	}
	model := New(
		NewDefaultGlobalInventoryLoader(tmuxClient, logger),
		WithSwitcher(tmuxClient),
		WithKiller(NewDaemonKiller(tmuxClient, logger)),
		WithDetailOpener(NewDaemonDetailOpener(logger)),
		WithUIStateStore(daemonclient.New(transport.NewClient(uiStateSocketPath))),
	)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func newSelectorLogger(cfg *config.Config) *slog.Logger {
	logPath := selectorLogPath(cfg)
	if logPath == "" {
		return logging.NewDiscardLogger(slog.LevelInfo)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return logging.NewDiscardLogger(slog.LevelInfo)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return logging.NewDiscardLogger(slog.LevelInfo)
	}
	return logging.NewTextStreamLogger(logFile, slog.LevelInfo)
}

func selectorLogPath(cfg *config.Config) string {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	logDir := strings.TrimSpace(cfg.Session.LogDir)
	if logDir == "" {
		if homeDir, err := os.UserHomeDir(); err == nil && strings.TrimSpace(homeDir) != "" {
			logDir = filepath.Join(homeDir, ".azedarach", "logs")
		} else {
			logDir = filepath.Join(".", ".azedarach", "logs")
		}
	}
	return filepath.Join(logDir, logging.TmuxSelectorLogFileName)
}
