package app

import (
	"log/slog"
	"path/filepath"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/backup"
)

type backupRunner interface {
	OnOpen()
	OnMutationSuccess()
}

type noOpBackupRunner struct{}

func (noOpBackupRunner) OnOpen() {}

func (noOpBackupRunner) OnMutationSuccess() {}

type backupWarningCollector struct {
	mu       sync.Mutex
	warnings []string
}

func newBackupWarningCollector() *backupWarningCollector {
	return &backupWarningCollector{
		warnings: make([]string, 0),
	}
}

func (collector *backupWarningCollector) Add(message string) {
	if collector == nil || message == "" {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.warnings = append(collector.warnings, message)
}

func (collector *backupWarningCollector) Drain() []string {
	if collector == nil {
		return nil
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.warnings) == 0 {
		return nil
	}
	drained := append([]string(nil), collector.warnings...)
	collector.warnings = collector.warnings[:0]
	return drained
}

type backupWrappedMsg struct {
	inner    tea.Msg
	warnings []string
}

func newAppBackupRunner(
	cfg *config.Config,
	repoDir string,
	logger *slog.Logger,
	collector *backupWarningCollector,
) backupRunner {
	if cfg == nil {
		return noOpBackupRunner{}
	}

	backupCfg := cfg.IssueTracker.Local.Backups
	canonicalDBPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")

	return backup.NewRuntime(
		repoDir,
		canonicalDBPath,
		backup.RuntimeConfig{
			Enabled:              backupCfg.Enabled,
			IntervalMinutes:      backupCfg.IntervalMinutes,
			WriteCooldownSeconds: backupCfg.WriteCooldownSeconds,
			MaxBackups:           backupCfg.MaxBackups,
			Directory:            backupCfg.Directory,
		},
		backup.WithWarningHandler(func(message string) {
			if logger != nil {
				logger.Warn("tui backup runtime warning", "message", message)
			}
			if collector != nil {
				collector.Add(message)
			}
		}),
	)
}

func (m Model) runBackupOnOpen() {
	if m.backupRunner == nil {
		return
	}
	m.backupRunner.OnOpen()
}

func (m Model) runBackupOnMutationSuccess() {
	if m.backupRunner == nil {
		return
	}
	m.backupRunner.OnMutationSuccess()
}

func (m Model) wrapBackupWarnings(inner tea.Msg) tea.Msg {
	if m.backupWarnings == nil {
		return inner
	}
	warnings := m.backupWarnings.Drain()
	if len(warnings) == 0 {
		return inner
	}
	return backupWrappedMsg{
		inner:    inner,
		warnings: warnings,
	}
}
