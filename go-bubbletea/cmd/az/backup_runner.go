package main

import (
	"fmt"
	"io"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/backup"
)

type issueCommandBackupRunner interface {
	OnOpen()
	OnMutationSuccess()
}

type issueCommandBackupRunnerFactory func(
	cfg *config.Config,
	project H2ProjectContext,
	warnings io.Writer,
) issueCommandBackupRunner

var newIssueCommandBackupRunner issueCommandBackupRunnerFactory = defaultIssueCommandBackupRunnerFactory

type noOpIssueCommandBackupRunner struct{}

func (noOpIssueCommandBackupRunner) OnOpen() {}

func (noOpIssueCommandBackupRunner) OnMutationSuccess() {}

func defaultIssueCommandBackupRunnerFactory(
	cfg *config.Config,
	project H2ProjectContext,
	warnings io.Writer,
) issueCommandBackupRunner {
	if cfg == nil {
		return noOpIssueCommandBackupRunner{}
	}

	backupConfig := cfg.IssueTracker.Local.Backups
	return backup.NewRuntime(
		project.Path,
		project.CanonicalDBPath,
		backup.RuntimeConfig{
			Enabled:              backupConfig.Enabled,
			IntervalMinutes:      backupConfig.IntervalMinutes,
			WriteCooldownSeconds: backupConfig.WriteCooldownSeconds,
			MaxBackups:           backupConfig.MaxBackups,
			Directory:            backupConfig.Directory,
		},
		backup.WithWarningHandler(func(message string) {
			if warnings == nil {
				return
			}
			_, _ = fmt.Fprintf(warnings, "Warning: %s\n", message)
		}),
	)
}
