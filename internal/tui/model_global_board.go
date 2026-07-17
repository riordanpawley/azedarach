package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type globalBoardLoadedMsg struct {
	snapshot protocol.GlobalSnapshotResponseBody
	err      error
	seq      uint64
}

func (m Model) loadGlobalBoardCmd(seq uint64) tea.Cmd {
	var showChildren *bool
	if m.sessionTreeFilterOnly {
		show := true
		showChildren = &show
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		client := daemonclient.New(transport.NewClient(config.GlobalDaemonSocketPath()))
		snapshot, err := client.GlobalViewSnapshot(ctx, protocol.GlobalSnapshotRequestBody{Consumer: protocol.GlobalViewConsumerBoard, ShowChildren: showChildren})
		return globalBoardLoadedMsg{snapshot: snapshot, err: err, seq: seq}
	}
}

func globalTaskKey(identity protocol.ScopedIssueID) string {
	return protocol.NormalizeProjectID(identity.ProjectID.String()) + "::" + identity.IssueID.String()
}

func (m *Model) applyGlobalBoardSnapshot(snapshot protocol.GlobalSnapshotResponseBody) {
	m.scope = globalTUIScope()
	m.projectOrchestrator = nil
	m.globalTaskScopes = make(map[string]protocol.ScopedIssueID, len(snapshot.Projection.Items))
	m.globalTaskProjects = make(map[string]config.Project, len(snapshot.Projection.Items))
	projects := make(map[string]config.Project, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		projects[protocol.NormalizeProjectID(project.ProjectID)] = config.Project{ID: project.ProjectID, Name: project.Name, Path: project.Path}
	}
	items := make([]domain.BoardViewProjectedItem, 0, len(snapshot.Projection.Items))
	groups := make(map[domain.BoardColumnID][]naming.IssueID, len(snapshot.Projection.Groups))
	for _, item := range snapshot.Projection.Items {
		key := globalTaskKey(item.Identity)
		task := item.Task
		task.ID = naming.IssueID(key)
		if task.ParentID != nil {
			parent := naming.IssueID(protocol.NormalizeProjectID(item.Identity.ProjectID.String()) + "::" + task.ParentID.String())
			task.ParentID = &parent
		}
		for i := range task.Dependencies {
			if !task.Dependencies[i].ID.IsZero() {
				task.Dependencies[i].ID = naming.IssueID(protocol.NormalizeProjectID(item.Identity.ProjectID.String()) + "::" + task.Dependencies[i].ID.String())
			}
		}
		m.globalTaskScopes[key] = item.Identity
		m.globalTaskProjects[key] = projects[protocol.NormalizeProjectID(item.Identity.ProjectID.String())]
		items = append(items, domain.BoardViewProjectedItem{Task: task, GroupID: item.GroupID, Depth: item.Depth, OrchestrationState: item.OrchestrationState})
		groups[item.GroupID] = append(groups[item.GroupID], task.ID)
	}
	projection := domain.BoardViewProjection{View: snapshot.Projection.View, Items: items}
	for _, group := range snapshot.Projection.Groups {
		projection.Groups = append(projection.Groups, domain.BoardViewProjectedGroup{GroupID: group.GroupID, TaskIDs: groups[group.GroupID]})
	}
	for _, identity := range snapshot.Projection.KnownTaskIDs {
		projection.KnownTaskIDs = append(projection.KnownTaskIDs, naming.IssueID(globalTaskKey(identity)))
	}
	for _, progress := range snapshot.Projection.ChildProgress {
		projection.ChildProgress = append(projection.ChildProgress, domain.BoardChildProgress{
			ParentID: naming.IssueID(globalTaskKey(progress.ParentID)),
			Done:     progress.Done,
			Total:    progress.Total,
		})
	}
	m.boardProjection = projection
	m.boardView = projection.View
	m.selectedBoardViewID = string(projection.View.ID)
	m.tasks = projection.OrderedTasks()
	m.boardOrdered = projection.OrderedTasks()
	m.boardColumns = projection.ColumnSnapshots()
	m.editor.ReconcileSelection(m.tasks)
}

func (m Model) leaveGlobalBoardForCurrentTask() (tea.Model, tea.Cmd) {
	task, _ := m.getCurrentTaskAndSession()
	if task == nil {
		return m, nil
	}
	project, ok := m.globalTaskProjects[task.ID.String()]
	identity := m.globalTaskScopes[task.ID.String()]
	if !ok || strings.TrimSpace(project.Path) == "" {
		m.addToast(Toast{Level: ToastError, Message: fmt.Sprintf("Project unavailable for %s", identity.IssueID), Expires: time.Now().Add(4 * time.Second)})
		return m, nil
	}
	m.scope = projectTUIScope()
	m.beginBoardViewScopeTransition()
	m.projectSwitchFromGlobal = true
	m.pendingUIOpenTaskID = identity.IssueID.String()
	m.loading = false
	m.boardRefreshing = true
	m.projectSwitchInFlight = true
	m.issueRefreshSeq++
	m.projectSwitchSeq++
	m.beginMutationFeedback(fmt.Sprintf("Opening %s in %s", identity.IssueID, project.Name))
	return m, m.switchProjectCmd(project)
}
