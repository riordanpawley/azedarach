package tmuxselector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type DaemonDetailOpener struct {
	logger *slog.Logger
}

func NewDaemonDetailOpener(logger *slog.Logger) *DaemonDetailOpener {
	if logger == nil {
		logger = slog.Default()
	}
	return &DaemonDetailOpener{logger: logger}
}

func (o *DaemonDetailOpener) OpenDetail(ctx context.Context, entry InventoryEntry) error {
	totalStarted := time.Now()
	var logger *slog.Logger
	if o != nil {
		logger = o.logger
	}
	if logger == nil {
		logger = slog.Default()
	}
	issueID, err := naming.ParseIssueID(strings.TrimSpace(firstNonEmpty(entry.IssueID, entry.Task.ID.String())))
	if err != nil {
		return fmt.Errorf("invalid issue id: %w", err)
	}
	projectPath := strings.TrimSpace(firstNonEmpty(entry.ProjectPath, entry.Worktree))
	if projectPath == "" && entry.Task.Session != nil {
		projectPath = strings.TrimSpace(entry.Task.Session.Worktree)
	}
	if projectPath == "" {
		return fmt.Errorf("selected session has no project path")
	}
	rootStarted := time.Now()
	if projectRoot, err := config.ResolveProjectRoot(projectPath); err == nil && strings.TrimSpace(projectRoot) != "" {
		projectPath = projectRoot
	}
	logger.Info("tmux selector workspace open project root resolved",
		"elapsed_ms", time.Since(rootStarted).Milliseconds(),
		"issue_id", issueID.String(),
		"project_path", projectPath,
	)
	projectID := projectIDForPath(projectPath)
	if projectID == "" {
		projectID = strings.TrimSpace(entry.ProjectID)
	}
	if projectID == "" {
		return fmt.Errorf("resolve project id for %s", projectPath)
	}
	socketStarted := time.Now()
	socketPath := config.DaemonSocketPathFor(projectPath)
	logger.Info("tmux selector workspace open daemon socket resolved",
		"elapsed_ms", time.Since(socketStarted).Milliseconds(),
		"issue_id", issueID.String(),
		"project_id", projectID,
		"socket_path", socketPath,
	)
	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
	commandStarted := time.Now()
	if _, err := client.OpenTaskWorkspace(ctx, issueID); err != nil {
		logger.Warn("tmux selector workspace open daemon command failed",
			"elapsed_ms", time.Since(commandStarted).Milliseconds(),
			"total_elapsed_ms", time.Since(totalStarted).Milliseconds(),
			"issue_id", issueID.String(),
			"project_id", projectID,
			"error", err,
		)
		return err
	}
	logger.Info("tmux selector workspace open daemon command completed",
		"elapsed_ms", time.Since(commandStarted).Milliseconds(),
		"total_elapsed_ms", time.Since(totalStarted).Milliseconds(),
		"issue_id", issueID.String(),
		"project_id", projectID,
	)
	return nil
}
