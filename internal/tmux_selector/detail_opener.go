package tmuxselector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
	projectID := projectIDForPath(projectPath)
	if projectID == "" {
		projectID = strings.TrimSpace(entry.ProjectID)
	}
	if projectID == "" {
		return fmt.Errorf("resolve project id for %s", projectPath)
	}
	socketPath := config.DaemonSocketPathFor(projectPath)
	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
	if _, err := client.OpenTaskWorkspace(ctx, issueID); err != nil {
		return err
	}
	return nil
}
