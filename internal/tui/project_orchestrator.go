package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func (m Model) loadProjectOrchestratorSnapshotCmd() tea.Cmd {
	client := m.daemonClient
	if client == nil {
		return nil
	}
	project := projectOrchestratorSnapshot{
		Name:      strings.TrimSpace(m.currentProject),
		Path:      strings.TrimSpace(m.activeProjectPath()),
		ProjectID: strings.TrimSpace(m.daemonProjectID()),
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		scope := domain.ProjectOrchestrationScope()
		snapshot, snapshotErr := client.OrchestrationSnapshot(ctx, protocol.OrchestrationSnapshotRequest{Scope: scope, RepoDir: project.Path})
		session, sessionErr := client.OrchestratorSessionStatus(ctx, protocol.OrchestratorSessionRequest{Scope: scope})
		if snapshotErr == nil {
			project.Snapshot = &snapshot
		}
		if sessionErr == nil {
			project.Session = &session
		}
		project.OrchestrationErr = errors.Join(snapshotErr, sessionErr)
		return projectOrchestratorLoadedMsg{project: project, err: project.OrchestrationErr}
	}
}

func (m Model) projectOrchestratorActionCmd(project projectOrchestratorSnapshot, action string) tea.Cmd {
	socketPath := strings.TrimSpace(m.daemonSocketPath)
	if strings.TrimSpace(project.Path) != "" {
		socketPath = config.DaemonSocketPathFor(project.Path)
	}
	projectID := strings.TrimSpace(project.ProjectID)
	target := projectOrchestratorTarget{ProjectID: projectID, ProjectPath: strings.TrimSpace(project.Path), SocketPath: socketPath}
	readPolicy := daemonclient.DefaultReadWaitPolicy()
	if m.daemonClient != nil {
		readPolicy = m.daemonClient.ReadWaitPolicy()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		request := protocol.OrchestratorSessionRequest{Scope: domain.ProjectOrchestrationScope()}
		runner := m.projectOrchestratorActionRunner
		if runner == nil {
			runner = func(ctx context.Context, target projectOrchestratorTarget, action string, request protocol.OrchestratorSessionRequest) (protocol.OrchestratorSessionResult, error) {
				client := daemonclient.New(transport.NewClient(target.SocketPath)).WithProjectID(target.ProjectID).WithReadWaitPolicy(readPolicy)
				switch action {
				case "start":
					return client.StartOrchestratorSession(ctx, request)
				case "attach":
					return client.AttachOrchestratorSession(ctx, request)
				default:
					return protocol.OrchestratorSessionResult{}, fmt.Errorf("unsupported project orchestrator action %q", action)
				}
			}
		}
		result, err := runner(ctx, target, action, request)
		return projectOrchestratorActionMsg{projectID: projectID, action: action, result: result, err: err}
	}
}

func (m Model) openProjectOrchestratorOverlay() tea.Cmd {
	project := projectOrchestratorSnapshot{Name: strings.TrimSpace(m.currentProject), Path: strings.TrimSpace(m.activeProjectPath()), ProjectID: strings.TrimSpace(m.daemonProjectID())}
	refresh := m.loadProjectOrchestratorSnapshotCmd()
	if m.projectOrchestrator != nil {
		project = *m.projectOrchestrator
		refresh = nil
	}
	details := projectOrchestratorDetails(project)
	open := m.openOverlay(overlay.NewProjectOrchestratorOverlay(details, func(action string) tea.Cmd {
		return m.projectOrchestratorActionCmd(project, action)
	}))
	return tea.Batch(open, refresh)
}

func projectOrchestratorDetails(project projectOrchestratorSnapshot) overlay.ProjectOrchestratorDetails {
	status := "loading project orchestrator status"
	if project.Snapshot != nil || project.Session != nil {
		status = projectOrchestratorStatus(project)
	} else if project.OrchestrationErr != nil {
		status = "project orchestrator status unavailable"
	}
	details := overlay.ProjectOrchestratorDetails{Project: project.Name, Status: status}
	if project.Snapshot != nil {
		details.Ready = len(project.Snapshot.Runnable)
		details.Review = len(project.Snapshot.Reviews)
		details.WaitingHuman = len(project.Snapshot.Interactions)
		details.OwnedElsewhere = len(project.Snapshot.OwnershipConflicts)
	}
	return details
}

func (m Model) orchestratorChromeStatus() string {
	if m.projectOrchestrator == nil || (m.projectOrchestrator.Snapshot == nil && m.projectOrchestrator.Session == nil) {
		return ""
	}
	return projectOrchestratorStatus(*m.projectOrchestrator)
}

func projectOrchestratorStatus(project projectOrchestratorSnapshot) string {
	state := domain.OrchestratorLifecycle("inactive")
	live := false
	if project.Snapshot != nil && project.Snapshot.Lifecycle != "" {
		state = project.Snapshot.Lifecycle
	}
	if project.Session != nil {
		live = project.Session.Live
		if project.Session.Lifecycle != "" {
			state = project.Session.Lifecycle
		}
	}
	workers, capacity := 0, 0
	if project.Snapshot != nil {
		workers = project.Snapshot.Capacity.TotalCountingCapacityCount
		capacity = project.Snapshot.Constraints.AgentCapacity
	}
	return fmt.Sprintf("orchestrator %s live=%t  workers %d/%d", state, live, workers, capacity)
}
