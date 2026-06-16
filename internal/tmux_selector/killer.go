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

type tmuxSessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type daemonStopFunc func(ctx context.Context, socketPath, projectID, issueID string) error

type DaemonKiller struct {
	tmux       tmuxSessionKiller
	daemonStop daemonStopFunc
	logger     *slog.Logger
}

func NewDaemonKiller(tmuxClient tmuxSessionKiller, logger *slog.Logger) *DaemonKiller {
	if logger == nil {
		logger = slog.Default()
	}
	return &DaemonKiller{
		tmux:       tmuxClient,
		daemonStop: defaultDaemonStop,
		logger:     logger,
	}
}

func (k *DaemonKiller) KillSession(ctx context.Context, entry InventoryEntry) error {
	socketPath, projectID, issueID, ok, err := resolveDaemonStopTarget(entry)
	if err != nil {
		return err
	}
	if ok {
		if err := k.daemonStop(ctx, socketPath, projectID, issueID); err != nil {
			return fmt.Errorf("daemon stop session %s: %w", issueID, err)
		}
		return nil
	}
	sessionName := strings.TrimSpace(entry.SessionID)
	if sessionName == "" {
		return fmt.Errorf("entry has no tmux session id to kill")
	}
	if k.tmux == nil {
		return fmt.Errorf("tmux killer unavailable")
	}
	if err := k.tmux.KillSession(ctx, sessionName); err != nil {
		return fmt.Errorf("tmux kill-session %s: %w", sessionName, err)
	}
	return nil
}

func resolveDaemonStopTarget(entry InventoryEntry) (socketPath, projectID, issueID string, ok bool, err error) {
	rawIssueID := strings.TrimSpace(firstNonEmpty(entry.IssueID, entry.Task.ID.String()))
	if rawIssueID == "" {
		return "", "", "", false, nil
	}
	parsed, err := naming.ParseIssueID(rawIssueID)
	if err != nil {
		return "", "", "", false, nil
	}
	projectPath := strings.TrimSpace(firstNonEmpty(entry.ProjectPath, entry.Worktree))
	if projectPath == "" && entry.Task.Session != nil {
		projectPath = strings.TrimSpace(entry.Task.Session.Worktree)
	}
	if projectPath == "" {
		return "", "", "", false, nil
	}
	if root, err := config.ResolveProjectRoot(projectPath); err == nil && strings.TrimSpace(root) != "" {
		projectPath = root
	}
	pid := projectIDForPath(projectPath)
	if pid == "" {
		pid = strings.TrimSpace(entry.ProjectID)
	}
	if pid == "" {
		return "", "", "", false, nil
	}
	socketPath = config.DaemonSocketPathFor(projectPath)
	if err := validateSharedDaemonExecutable(socketPath); err != nil {
		return "", "", "", false, err
	}
	return socketPath, pid, parsed.String(), true, nil
}

func defaultDaemonStop(ctx context.Context, socketPath, projectID, issueID string) error {
	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
	_, err := client.StopSession(ctx, issueID)
	return err
}
