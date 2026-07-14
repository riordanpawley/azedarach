package tmuxselector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type tmuxSessionKiller interface {
	KillSession(ctx context.Context, sessionName string) error
}

type daemonStopFunc func(ctx context.Context, socketPath, projectID, issueID string) error
type daemonOrchestratorStatusFunc func(ctx context.Context, socketPath, projectID string, scope domain.OrchestrationScope) (protocol.OrchestratorSessionResult, error)
type daemonOrchestratorStopFunc func(ctx context.Context, socketPath, projectID string, scope domain.OrchestrationScope, expectedSessionID string) error

type DaemonKiller struct {
	tmux                     tmuxSessionKiller
	daemonStop               daemonStopFunc
	daemonOrchestratorStatus daemonOrchestratorStatusFunc
	daemonOrchestratorStop   daemonOrchestratorStopFunc
	logger                   *slog.Logger
}

func NewDaemonKiller(tmuxClient tmuxSessionKiller, logger *slog.Logger) *DaemonKiller {
	if logger == nil {
		logger = slog.Default()
	}
	return &DaemonKiller{
		tmux:                     tmuxClient,
		daemonStop:               defaultDaemonStop,
		daemonOrchestratorStatus: defaultDaemonOrchestratorStatus,
		daemonOrchestratorStop:   defaultDaemonOrchestratorStop,
		logger:                   logger,
	}
}

func (k *DaemonKiller) KillSession(ctx context.Context, entry InventoryEntry) error {
	socketPath, projectID, ok, err := resolveDaemonProjectTarget(entry)
	if err != nil {
		return err
	}
	if ok {
		if scope, candidate, scopeErr := orchestratorScopeCandidate(entry); scopeErr != nil {
			return scopeErr
		} else if candidate {
			status, statusErr := k.daemonOrchestratorStatus(ctx, socketPath, projectID, scope)
			if statusErr != nil {
				return fmt.Errorf("inspect daemon orchestrator session %s: %w", killTargetLabel(entry), statusErr)
			}
			if strings.TrimSpace(status.SessionID) != "" {
				if status.SessionID != strings.TrimSpace(entry.SessionID) {
					return fmt.Errorf("orchestrator scope now belongs to session %s; refresh before stopping", status.SessionID)
				}
				if err := k.daemonOrchestratorStop(ctx, socketPath, projectID, scope, status.SessionID); err != nil {
					return fmt.Errorf("daemon stop orchestrator session %s: %w", status.SessionID, err)
				}
				return nil
			}
			if scope.Kind == domain.OrchestrationScopeProject {
				return fmt.Errorf("daemon has no authoritative project orchestrator lease for %s; refresh or reconcile before stopping", killTargetLabel(entry))
			}
		}

		issueID, issueOK := resolveIssueID(entry)
		if !issueOK {
			return k.killTmuxFallback(ctx, entry)
		}
		if err := k.daemonStop(ctx, socketPath, projectID, issueID); err != nil {
			return fmt.Errorf("daemon stop session %s: %w", issueID, err)
		}
		return nil
	}
	return k.killTmuxFallback(ctx, entry)
}

func (k *DaemonKiller) killTmuxFallback(ctx context.Context, entry InventoryEntry) error {
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

func resolveDaemonProjectTarget(entry InventoryEntry) (socketPath, projectID string, ok bool, err error) {
	projectPath := strings.TrimSpace(firstNonEmpty(entry.ProjectPath, entry.Worktree))
	if projectPath == "" && entry.Task.Session != nil {
		projectPath = strings.TrimSpace(entry.Task.Session.Worktree)
	}
	if projectPath == "" {
		return "", "", false, nil
	}
	if root, err := config.ResolveProjectRoot(projectPath); err == nil && strings.TrimSpace(root) != "" {
		projectPath = root
	}
	pid := projectIDForPath(projectPath)
	if pid == "" {
		pid = strings.TrimSpace(entry.ProjectID)
	}
	if pid == "" {
		return "", "", false, nil
	}
	socketPath = config.DaemonSocketPathFor(projectPath)
	if err := validateSharedDaemonExecutable(socketPath); err != nil {
		return "", "", false, err
	}
	return socketPath, pid, true, nil
}

func resolveIssueID(entry InventoryEntry) (string, bool) {
	rawIssueID := strings.TrimSpace(firstNonEmpty(entry.IssueID, entry.Task.ID.String()))
	parsed, err := naming.ParseIssueID(rawIssueID)
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

func orchestratorScopeCandidate(entry InventoryEntry) (domain.OrchestrationScope, bool, error) {
	if strings.HasSuffix(strings.TrimSpace(entry.SessionID), "-orchestrator-project") {
		return domain.ProjectOrchestrationScope(), true, nil
	}
	issueID, ok := resolveIssueID(entry)
	if !ok {
		return domain.OrchestrationScope{}, false, nil
	}
	scope, err := domain.RootedOrchestrationScope(issueID)
	if err != nil {
		return domain.OrchestrationScope{}, false, err
	}
	return scope, true, nil
}

func defaultDaemonStop(ctx context.Context, socketPath, projectID, issueID string) error {
	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
	_, err := client.StopSession(ctx, issueID)
	return err
}

func defaultDaemonOrchestratorStatus(ctx context.Context, socketPath, projectID string, scope domain.OrchestrationScope) (protocol.OrchestratorSessionResult, error) {
	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
	return client.OrchestratorSessionStatus(ctx, protocol.OrchestratorSessionRequest{Scope: scope})
}

func defaultDaemonOrchestratorStop(ctx context.Context, socketPath, projectID string, scope domain.OrchestrationScope, expectedSessionID string) error {
	client := daemonclient.New(transport.NewClient(socketPath)).WithProjectID(projectID)
	_, err := client.StopOrchestratorSession(ctx, protocol.OrchestratorSessionRequest{Scope: scope, ExpectedSessionID: expectedSessionID})
	return err
}
