package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	uistyles "github.com/riordanpawley/azedarach/internal/ui/styles"
)

type orchestrationProjectRef struct {
	name      string
	path      string
	projectID string
}

func (m Model) loadOrchestrationOverviewCmd() tea.Cmd {
	projects := m.orchestrationOverviewProjects()
	if len(projects) == 0 {
		return func() tea.Msg {
			return orchestrationOverviewLoadedMsg{}
		}
	}
	socketPath := strings.TrimSpace(m.daemonSocketPath)
	readPolicy := daemonclient.DefaultReadWaitPolicy()
	if m.daemonClient != nil {
		readPolicy = m.daemonClient.ReadWaitPolicy()
	}
	currentProjectID := m.daemonProjectID()
	currentProjectPath := m.activeProjectPath()
	currentTasks := overviewLocalSessionTasks(m.tasks, m.sessions)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()

		overview := make([]orchestrationProjectOverview, 0, len(projects))
		hiddenLabels := make([]string, 0)
		backendErrors := 0
		hiddenTasks := 0
		for _, project := range projects {
			projectSocket := socketPath
			if strings.TrimSpace(project.path) != "" {
				projectSocket = config.DaemonSocketPathFor(project.path)
			}
			if strings.TrimSpace(projectSocket) == "" {
				projectSocket = socketPath
			}
			client := daemonclient.New(transport.NewClient(projectSocket)).
				WithProjectID(project.projectID).
				WithReadWaitPolicy(readPolicy)
			snapshot, err := client.ListTasksSnapshotWithMode(ctx, daemonclient.ReadWaitModeDefault)
			entry := orchestrationProjectOverview{
				Name:      project.name,
				Path:      project.path,
				ProjectID: project.projectID,
				Err:       err,
			}
			if err == nil {
				var entryHiddenTasks int
				entry.Tasks, entryHiddenTasks = sessionOverviewTasks(snapshot.Tasks)
				entry.MailByTask = overviewLatestMailByTask(ctx, client, project.path, snapshot.Tasks)
				entry.Observations, entry.ObservationErrs = overviewWorkerObservations(ctx, client, snapshot.Tasks)
				entry.Tasks = overviewMergeObservationTasks(entry.Tasks, snapshot.Tasks, entry.Observations)
				entry.Observations = overviewSessionObservations(entry.Observations, entry.Tasks)
				hiddenTasks += entryHiddenTasks
				entry.Revision = snapshot.Revision
				entry.LastCheckedAt = snapshot.LastCheckedAt
				entry.Freshness = snapshot.Freshness
			} else {
				backendErrors++
				if overviewProjectMatchesCurrent(project, currentProjectID, currentProjectPath) {
					entry.Tasks, _ = sessionOverviewTasks(currentTasks)
					entry.Fallback = "local state"
				} else if fallbackTasks, fallbackErr := overviewTasksFromSessionStatus(ctx, client); fallbackErr == nil {
					entry.Tasks = fallbackTasks
					if len(fallbackTasks) > 0 {
						entry.Fallback = "session status"
					}
				}
			}
			if len(entry.Tasks) > 0 {
				if entry.MailByTask == nil {
					entry.MailByTask = overviewLatestMailByTask(ctx, client, project.path, entry.Tasks)
				}
				overview = append(overview, entry)
			} else {
				hiddenLabels = append(hiddenLabels, overviewHiddenProjectLabel(project.name, err))
			}
		}
		hiddenProjects := len(projects) - len(overview)
		sort.SliceStable(overview, func(i, j int) bool {
			return strings.ToLower(overview[i].Name) < strings.ToLower(overview[j].Name)
		})
		return orchestrationOverviewLoadedMsg{
			projects:       overview,
			hiddenProjects: hiddenProjects,
			hiddenTasks:    hiddenTasks,
			backendErrors:  backendErrors,
			hiddenLabels:   hiddenLabels,
		}
	}
}

func (m Model) loadOrchestrationOverviewIfVisibleCmd() tea.Cmd {
	if m.viewMode != ViewModeOverview {
		return nil
	}
	return m.loadOrchestrationOverviewCmd()
}

func overviewHiddenProjectLabel(name string, err error) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "project"
	}
	if err != nil {
		return name + " degraded: " + overviewDegradedReason(err)
	}
	return name + " no sessions"
}

func overviewDegradedReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "timed out"), strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return "backend timeout"
	case strings.Contains(msg, "no such column"), strings.Contains(msg, "schema"):
		return "store schema mismatch"
	case strings.Contains(msg, "taskstore"), strings.Contains(msg, "issue store"):
		return "task store error"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "socket"), strings.Contains(msg, "no such file"):
		return "daemon unavailable"
	default:
		return "backend unavailable"
	}
}

func overviewProjectMatchesCurrent(project orchestrationProjectRef, currentProjectID, currentProjectPath string) bool {
	if currentProjectID != "" && project.projectID == currentProjectID {
		return true
	}
	return strings.TrimSpace(project.path) != "" && strings.TrimSpace(project.path) == strings.TrimSpace(currentProjectPath)
}

func overviewLocalSessionTasks(tasks []domain.Task, sessions map[string]*domain.Session) []domain.Task {
	merged := append([]domain.Task(nil), tasks...)
	seen := make(map[string]struct{}, len(merged))
	for _, task := range merged {
		seen[taskIDKey(task.ID.String())] = struct{}{}
	}
	for taskID, session := range sessions {
		if session == nil {
			continue
		}
		key := taskIDKey(taskID)
		if key == "" {
			key = taskIDKey(session.IssueID.String())
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		issueID := session.IssueID
		if strings.TrimSpace(issueID.String()) == "" {
			issueID = naming.IssueID(taskID)
		}
		merged = append(merged, domain.Task{
			ID:             issueID,
			Title:          issueID.String(),
			Status:         domain.StatusInProgress,
			Priority:       domain.P2,
			Session:        cloneSession(session),
			HasTmuxSession: true,
			HasWorktree:    strings.TrimSpace(session.Worktree) != "",
		})
		seen[key] = struct{}{}
	}
	return merged
}

func (m Model) orchestrationOverviewProjects() []orchestrationProjectRef {
	seen := map[string]struct{}{}
	projects := make([]orchestrationProjectRef, 0)
	addProject := func(name, path string) {
		name = strings.TrimSpace(name)
		path = strings.TrimSpace(path)
		if name == "" && path != "" {
			name = filepath.Base(path)
		}
		if name == "" {
			name = "current"
		}
		projectID := overviewProjectID(name, path)
		key := projectID
		if key == "" {
			key = strings.ToLower(name + "\x00" + path)
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		projects = append(projects, orchestrationProjectRef{name: name, path: path, projectID: projectID})
	}
	if m.projectRegistry != nil {
		for _, project := range m.projectRegistry.Projects {
			addProject(project.Name, project.Path)
		}
	}
	addProject(m.currentProject, m.activeProjectPath())
	return projects
}

func overviewProjectID(name, path string) string {
	if projectID := daemonProjectIDForPath(path); strings.TrimSpace(projectID) != "" {
		return projectID
	}
	if parsed, err := naming.ParseProjectID(protocol.NormalizeProjectID(name)); err == nil {
		return parsed.String()
	}
	return protocol.DefaultProjectID
}

func sessionOverviewTasks(tasks []domain.Task) ([]domain.Task, int) {
	active := make([]domain.Task, 0, len(tasks))
	hidden := 0
	for _, task := range tasks {
		if task.Status == domain.StatusDone {
			continue
		}
		if !overviewTaskHasRuntimeSession(task) {
			hidden++
			continue
		}
		active = append(active, task)
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].Status != active[j].Status {
			return overviewStatusRank(active[i].Status) < overviewStatusRank(active[j].Status)
		}
		if active[i].Priority != active[j].Priority {
			return active[i].Priority < active[j].Priority
		}
		if !active[i].UpdatedAt.Equal(active[j].UpdatedAt) {
			return active[i].UpdatedAt.After(active[j].UpdatedAt)
		}
		return strings.ToLower(active[i].ID.String()) < strings.ToLower(active[j].ID.String())
	})
	return active, hidden
}

func overviewTaskHasRuntimeSession(task domain.Task) bool {
	return task.Session != nil || task.HasTmuxSession
}

func overviewWorkerObservations(ctx context.Context, client *daemonclient.Client, tasks []domain.Task) ([]domain.WorkerObservation, []string) {
	candidates := overviewObservationIssueIDs(tasks)
	if len(candidates) == 0 {
		return nil, nil
	}
	byIssue := make(map[string]domain.WorkerObservation)
	warnings := make([]string, 0)
	for _, issueID := range candidates {
		ready, err := client.TaskGraphReadiness(ctx, issueID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", issueID, overviewDegradedReason(err)))
			continue
		}
		for _, observation := range ready.WorkerObservations {
			key := strings.TrimSpace(observation.IssueID)
			if key == "" {
				continue
			}
			if _, exists := byIssue[key]; exists {
				continue
			}
			byIssue[key] = observation
		}
	}
	if len(byIssue) == 0 {
		return nil, warnings
	}
	observations := make([]domain.WorkerObservation, 0, len(byIssue))
	for _, observation := range byIssue {
		observations = append(observations, observation)
	}
	sort.SliceStable(observations, func(i, j int) bool {
		left := overviewObservationGroupRank(overviewObservationGroup(observations[i].State))
		right := overviewObservationGroupRank(overviewObservationGroup(observations[j].State))
		if left != right {
			return left < right
		}
		leftAge, leftOK := overviewObservationEventTime(observations[i])
		rightAge, rightOK := overviewObservationEventTime(observations[j])
		if leftOK && rightOK && !leftAge.Equal(rightAge) {
			return leftAge.Before(rightAge)
		}
		if leftOK != rightOK {
			return leftOK
		}
		return strings.ToLower(observations[i].IssueID) < strings.ToLower(observations[j].IssueID)
	})
	return observations, warnings
}

func overviewObservationIssueIDs(tasks []domain.Task) []string {
	ids := make([]string, 0, len(tasks))
	seen := map[string]struct{}{}
	for _, task := range tasks {
		if task.ID.IsZero() || !overviewObservationCandidate(task) {
			continue
		}
		id := task.ID.String()
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func overviewObservationCandidate(task domain.Task) bool {
	return overviewTaskHasRuntimeSession(task)
}

func overviewMergeObservationTasks(active, source []domain.Task, observations []domain.WorkerObservation) []domain.Task {
	if len(observations) == 0 {
		return active
	}
	byID := make(map[string]domain.Task, len(source))
	for _, task := range source {
		if task.ID.IsZero() {
			continue
		}
		byID[task.ID.String()] = task
	}
	seen := make(map[string]struct{}, len(active)+len(observations))
	for _, task := range active {
		if !task.ID.IsZero() {
			seen[task.ID.String()] = struct{}{}
		}
	}
	merged := append([]domain.Task(nil), active...)
	for _, observation := range observations {
		issueID := strings.TrimSpace(observation.IssueID)
		if issueID == "" {
			continue
		}
		if _, ok := seen[issueID]; ok {
			continue
		}
		if task, ok := byID[issueID]; ok {
			if overviewTaskHasRuntimeSession(task) {
				merged = append(merged, task)
			}
		}
		seen[issueID] = struct{}{}
	}
	return merged
}

func overviewSessionObservations(observations []domain.WorkerObservation, tasks []domain.Task) []domain.WorkerObservation {
	if len(observations) == 0 {
		return nil
	}
	taskByID := overviewTasksByID(tasks)
	out := make([]domain.WorkerObservation, 0, len(observations))
	for _, observation := range observations {
		task, ok := taskByID[strings.TrimSpace(observation.IssueID)]
		if !ok || !overviewTaskHasRuntimeSession(task) {
			continue
		}
		out = append(out, observation)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func overviewStatusFromObservation(state domain.WorkerObservationState) domain.Status {
	switch state {
	case domain.WorkerObservationReviewReady:
		return domain.StatusInReview
	case domain.WorkerObservationDone, domain.WorkerObservationCleanupPending:
		return domain.StatusDone
	case domain.WorkerObservationRunnable:
		return domain.StatusOpen
	default:
		return domain.StatusInProgress
	}
}

func overviewObservationGroup(state domain.WorkerObservationState) string {
	switch state {
	case domain.WorkerObservationWaitingHuman:
		return "needs_you"
	case domain.WorkerObservationReviewReady:
		return "review_ready"
	case domain.WorkerObservationBlocked, domain.WorkerObservationFailed, domain.WorkerObservationStale:
		return "blocked_failed_stale"
	case domain.WorkerObservationCleanupPending, domain.WorkerObservationDone:
		return "cleanup"
	default:
		return "working"
	}
}

func overviewObservationGroupRank(group string) int {
	for i, spec := range overviewObservationGroupOrder {
		if spec.name == group {
			return i
		}
	}
	return len(overviewObservationGroupOrder)
}

func overviewObservationsInGroup(observations []domain.WorkerObservation, group string) []domain.WorkerObservation {
	out := make([]domain.WorkerObservation, 0)
	for _, observation := range observations {
		if overviewObservationGroup(observation.State) == group {
			out = append(out, observation)
		}
	}
	return out
}

var overviewObservationGroupOrder = []struct {
	name  string
	title string
}{
	{name: "needs_you", title: "Needs You"},
	{name: "review_ready", title: "Review Ready"},
	{name: "blocked_failed_stale", title: "Blocked/Failed/Stale"},
	{name: "working", title: "Working"},
	{name: "cleanup", title: "Cleanup"},
}

func overviewTasksFromSessionStatus(ctx context.Context, client *daemonclient.Client) ([]domain.Task, error) {
	status, err := client.SessionStatus(ctx, "")
	if err != nil {
		return nil, err
	}
	tasks := parseOverviewSessionStatusTasks(status)
	if len(tasks) == 0 {
		return nil, nil
	}
	return sessionOverviewTasksNoHidden(tasks), nil
}

func parseOverviewSessionStatusTasks(status string) []domain.Task {
	lines := strings.Split(status, "\n")
	tasks := make([]domain.Task, 0)
	inRows := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "ISSUE ID") {
			inRows = true
			continue
		}
		if !inRows || strings.HasPrefix(line, "-------") {
			continue
		}
		if strings.HasPrefix(line, "Use 'az attach") {
			break
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		issueID := strings.TrimSpace(fields[0])
		statusRaw := strings.TrimSpace(fields[1])
		activity := strings.TrimSpace(fields[2])
		title := strings.Join(fields[3:], " ")
		if !overviewSessionStatusRowIsIssue(issueID, statusRaw, title) {
			continue
		}
		parsedIssueID, err := naming.ParseIssueID(issueID)
		if err != nil {
			continue
		}
		taskStatus := domain.Status(statusRaw)
		if taskStatus == "" || taskStatus == domain.StatusDone {
			continue
		}
		state := overviewSessionStateFromActivity(activity)
		tasks = append(tasks, domain.Task{
			ID:             parsedIssueID,
			Title:          title,
			Status:         taskStatus,
			Priority:       domain.P2,
			Type:           domain.TypeTask,
			Session:        &domain.Session{IssueID: parsedIssueID, State: state, Activity: activity},
			HasTmuxSession: true,
		})
	}
	return tasks
}

func overviewSessionStatusRowIsIssue(issueID, status, title string) bool {
	if strings.TrimSpace(issueID) == "" || strings.EqualFold(issueID, "az") {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(status), "unknown") {
		return false
	}
	return strings.TrimSpace(title) != "" && !strings.EqualFold(strings.TrimSpace(title), "(not in issues)")
}

func overviewSessionStateFromActivity(activity string) domain.SessionState {
	switch domain.SessionState(strings.TrimSpace(activity)) {
	case domain.SessionBusy:
		return domain.SessionBusy
	case domain.SessionWaiting:
		return domain.SessionWaiting
	case domain.SessionError:
		return domain.SessionError
	case domain.SessionPaused:
		return domain.SessionPaused
	case "no-agent":
		return "no-agent"
	default:
		return domain.SessionIdle
	}
}

func sessionOverviewTasksNoHidden(tasks []domain.Task) []domain.Task {
	active, _ := sessionOverviewTasks(tasks)
	return active
}

func overviewRuntimeSessionTasks(tasks []domain.Task) []domain.Task {
	active := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if overviewTaskHasRuntimeSession(task) {
			active = append(active, task)
		}
	}
	return active
}

func overviewStatusRank(status domain.Status) int {
	switch status {
	case domain.StatusInProgress:
		return 0
	case domain.StatusInReview:
		return 1
	case domain.StatusOpen:
		return 2
	default:
		return 3
	}
}

func (m Model) handleOverviewModeKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	taskRefs := orchestrationOverviewTaskRefs(m.overviewProjectsForInteraction())
	if len(taskRefs) == 0 {
		switch msg.String() {
		case "j", "down", "k", "up", "h", "left", "l", "right", "g", "home", "G", "end", "enter", " ":
			return m, nil, true
		default:
			return m, nil, overviewConsumesBoardActionKey(msg.String())
		}
	}
	switch msg.String() {
	case "j", "down":
		m.orchestrationOverviewCursor = clampInt(m.orchestrationOverviewCursor+1, 0, len(taskRefs)-1)
		return m, nil, true
	case "k", "up":
		m.orchestrationOverviewCursor = clampInt(m.orchestrationOverviewCursor-1, 0, len(taskRefs)-1)
		return m, nil, true
	case "l", "right":
		m.orchestrationOverviewCursor = overviewMoveProjectCursor(taskRefs, m.orchestrationOverviewCursor, 1)
		return m, nil, true
	case "h", "left":
		m.orchestrationOverviewCursor = overviewMoveProjectCursor(taskRefs, m.orchestrationOverviewCursor, -1)
		return m, nil, true
	case "g", "home":
		m.orchestrationOverviewCursor = 0
		return m, nil, true
	case "G", "end":
		m.orchestrationOverviewCursor = len(taskRefs) - 1
		return m, nil, true
	case "enter", " ":
		m.orchestrationOverviewCursor = clampInt(m.orchestrationOverviewCursor, 0, len(taskRefs)-1)
		return openOverviewTaskWorkspace(m, taskRefs[m.orchestrationOverviewCursor])
	}
	return m, nil, overviewConsumesBoardActionKey(msg.String())
}

func overviewConsumesBoardActionKey(key string) bool {
	switch key {
	case "h", "left", "l", "right", "a", "s", "S", "!", "p", "R", "x", "u", "m", "b", "P", "e", "c", "T", "d", "v":
		return true
	default:
		return false
	}
}

func overviewMoveProjectCursor(taskRefs []orchestrationOverviewTaskRef, cursor, delta int) int {
	if len(taskRefs) == 0 {
		return 0
	}
	cursor = clampInt(cursor, 0, len(taskRefs)-1)
	current := taskRefs[cursor]
	if delta > 0 {
		for i := cursor + 1; i < len(taskRefs); i++ {
			if !overviewSameProjectRef(current.Project, taskRefs[i].Project) {
				return i
			}
		}
		return cursor
	}
	if delta < 0 {
		currentStart := cursor
		for currentStart > 0 && overviewSameProjectRef(current.Project, taskRefs[currentStart-1].Project) {
			currentStart--
		}
		if currentStart == 0 {
			return cursor
		}
		previous := taskRefs[currentStart-1]
		previousStart := currentStart - 1
		for previousStart > 0 && overviewSameProjectRef(previous.Project, taskRefs[previousStart-1].Project) {
			previousStart--
		}
		return previousStart
	}
	return cursor
}

func overviewSameProjectRef(a, b orchestrationProjectOverview) bool {
	if a.ProjectID != "" && b.ProjectID != "" {
		return a.ProjectID == b.ProjectID
	}
	if strings.TrimSpace(a.Path) != "" && strings.TrimSpace(b.Path) != "" {
		return strings.TrimSpace(a.Path) == strings.TrimSpace(b.Path)
	}
	return strings.TrimSpace(a.Name) == strings.TrimSpace(b.Name)
}

func openOverviewTaskWorkspace(m Model, taskRef orchestrationOverviewTaskRef) (Model, tea.Cmd, bool) {
	taskID := taskRef.Task.ID.String()
	if task, _, ok := m.taskAndSessionByID(taskID); !ok || task == nil {
		projectName := strings.TrimSpace(taskRef.Project.Name)
		if projectName == "" {
			projectName = "that project"
		}
		m.addToast(Toast{
			Level:   ToastWarning,
			Message: fmt.Sprintf("Switch to %s to open %s", projectName, taskID),
			Expires: time.Now().Add(3 * time.Second),
		})
		return m, nil, true
	}
	updated, cmd := m.openTaskWorkspaceByID(taskID)
	next, ok := updated.(Model)
	if !ok {
		return m, cmd, true
	}
	return next, cmd, true
}

func (m Model) overviewProjectsForInteraction() []orchestrationProjectOverview {
	if len(m.orchestrationOverview) > 0 {
		return overviewVisibleProjects(m.orchestrationOverview)
	}
	if !m.orchestrationOverviewLoadedAt.IsZero() {
		return nil
	}
	tasks, _ := sessionOverviewTasks(overviewLocalSessionTasks(m.tasks, m.sessions))
	name := strings.TrimSpace(m.currentProject)
	if name == "" {
		name = "current"
	}
	return []orchestrationProjectOverview{{
		Name:      name,
		Path:      m.activeProjectPath(),
		ProjectID: m.daemonProjectID(),
		Tasks:     tasks,
	}}
}

type orchestrationOverviewTaskRef struct {
	Project orchestrationProjectOverview
	Task    domain.Task
}

func orchestrationOverviewTaskRefs(projects []orchestrationProjectOverview) []orchestrationOverviewTaskRef {
	count := orchestrationOverviewTaskCount(projects)
	taskRefs := make([]orchestrationOverviewTaskRef, 0, count)
	for _, project := range projects {
		sessionObservations := overviewSessionObservations(project.Observations, project.Tasks)
		if len(sessionObservations) > 0 {
			taskByID := overviewTasksByID(project.Tasks)
			for _, group := range overviewObservationGroupOrder {
				for _, observation := range overviewObservationsInGroup(sessionObservations, group.name) {
					taskRefs = append(taskRefs, orchestrationOverviewTaskRef{
						Project: project,
						Task:    taskByID[strings.TrimSpace(observation.IssueID)],
					})
				}
			}
			continue
		}
		for _, task := range project.Tasks {
			taskRefs = append(taskRefs, orchestrationOverviewTaskRef{
				Project: project,
				Task:    task,
			})
		}
	}
	return taskRefs
}

func orchestrationOverviewTaskCount(projects []orchestrationProjectOverview) int {
	count := 0
	for _, project := range projects {
		sessionObservations := overviewSessionObservations(project.Observations, project.Tasks)
		if len(sessionObservations) > 0 {
			count += len(sessionObservations)
			continue
		}
		count += len(project.Tasks)
	}
	return count
}

func (m Model) renderOrchestrationOverview() string {
	height := board.BoardContentHeight(m.height)
	width := m.width
	if width < 1 || height < 1 {
		return ""
	}
	projects := m.orchestrationOverview
	counts := orchestrationOverviewHeaderCounts{
		hiddenProjects: m.orchestrationOverviewHiddenProjects,
		hiddenTasks:    m.orchestrationOverviewHiddenTasks,
		backendErrors:  m.orchestrationOverviewBackendErrors,
	}
	if len(projects) == 0 {
		if !m.orchestrationOverviewLoadedAt.IsZero() {
			return m.renderEmptyOrchestrationOverview(width, height)
		}
		tasks, hidden := sessionOverviewTasks(overviewLocalSessionTasks(m.tasks, m.sessions))
		projects = []orchestrationProjectOverview{{
			Name:      strings.TrimSpace(m.currentProject),
			Path:      m.activeProjectPath(),
			ProjectID: m.daemonProjectID(),
			Tasks:     tasks,
		}}
		counts.hiddenTasks = hidden
		if projects[0].Name == "" {
			projects[0].Name = "current"
		}
	}
	projects = overviewVisibleProjects(projects)
	if len(projects) == 0 {
		return m.renderEmptyOrchestrationOverview(width, height)
	}

	header := m.renderOverviewHeader(projects, width, counts)
	headerHeight := lipgloss.Height(header)
	availableHeight := height - headerHeight
	if availableHeight < 1 {
		return lipgloss.NewStyle().Width(width).Height(height).Render(header)
	}

	hiddenLine := m.renderOverviewHiddenProjectsLine(width)
	if hiddenLine != "" {
		availableHeight -= lipgloss.Height(hiddenLine)
		if availableHeight < 1 {
			return lipgloss.NewStyle().Width(width).Height(height).Render(lipgloss.JoinVertical(lipgloss.Left, header, hiddenLine))
		}
	}

	cards := m.renderOverviewCards(projects, width, availableHeight)
	if hiddenLine != "" {
		cards = lipgloss.JoinVertical(lipgloss.Left, hiddenLine, cards)
	}
	content := lipgloss.JoinVertical(lipgloss.Left, header, cards)
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(content)
}

func overviewVisibleProjects(projects []orchestrationProjectOverview) []orchestrationProjectOverview {
	if len(projects) == 0 {
		return nil
	}
	out := make([]orchestrationProjectOverview, 0, len(projects))
	for _, project := range projects {
		project.Tasks = overviewRuntimeSessionTasks(project.Tasks)
		project.Observations = overviewSessionObservations(project.Observations, project.Tasks)
		if len(project.Tasks) == 0 && len(project.Observations) == 0 {
			continue
		}
		out = append(out, project)
	}
	return out
}

func (m Model) renderOverviewHiddenProjectsLine(width int) string {
	if len(m.orchestrationOverviewHiddenLabels) == 0 || width < 30 {
		return ""
	}
	limit := len(m.orchestrationOverviewHiddenLabels)
	if limit > 4 {
		limit = 4
	}
	labels := append([]string(nil), m.orchestrationOverviewHiddenLabels[:limit]...)
	if remaining := len(m.orchestrationOverviewHiddenLabels) - limit; remaining > 0 {
		labels = append(labels, fmt.Sprintf("+%d more", remaining))
	}
	line := " hidden projects: " + strings.Join(labels, ", ")
	return lipgloss.NewStyle().
		Foreground(uistyles.Overlay1).
		Width(width).
		MaxWidth(width).
		Render(ansi.Truncate(line, width, ""))
}

func (m Model) renderEmptyOrchestrationOverview(width, height int) string {
	header := m.renderOverviewHeader(nil, width, orchestrationOverviewHeaderCounts{
		hiddenProjects: m.orchestrationOverviewHiddenProjects,
		hiddenTasks:    m.orchestrationOverviewHiddenTasks,
		backendErrors:  m.orchestrationOverviewBackendErrors,
	})
	headerHeight := lipgloss.Height(header)
	body := lipgloss.NewStyle().
		Width(width).
		Height(max(1, height-headerHeight)).
		Align(lipgloss.Center, lipgloss.Center).
		Render("No active sessions across registered projects.")
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		MaxHeight(height).
		Render(lipgloss.JoinVertical(lipgloss.Left, header, body))
}

type orchestrationOverviewHeaderCounts struct {
	hiddenProjects int
	hiddenTasks    int
	backendErrors  int
}

func (m Model) renderOverviewHeader(projects []orchestrationProjectOverview, width int, counts orchestrationOverviewHeaderCounts) string {
	projectCount := len(projects)
	sessionCount := 0
	for _, project := range projects {
		for _, task := range project.Tasks {
			if task.Session != nil || task.HasTmuxSession {
				sessionCount++
			}
		}
	}
	parts := []string{
		overviewHeaderText("Orchestration"),
		overviewChip(fmt.Sprintf("%d projects", projectCount), uistyles.Blue),
		overviewChip(fmt.Sprintf("%d sessions", sessionCount), uistyles.Green),
	}
	if counts.hiddenProjects > 0 {
		parts = append(parts, overviewChip(fmt.Sprintf("%d hidden", counts.hiddenProjects), uistyles.Overlay1))
	}
	if counts.hiddenTasks > 0 {
		parts = append(parts, overviewChip(fmt.Sprintf("%d non-session hidden", counts.hiddenTasks), uistyles.Overlay1))
	}
	if counts.backendErrors > 0 {
		parts = append(parts, overviewChip(fmt.Sprintf("%d degraded", counts.backendErrors), uistyles.Yellow))
	}
	if !m.orchestrationOverviewLoadedAt.IsZero() {
		parts = append(parts, overviewHeaderDim("updated "+m.orchestrationOverviewLoadedAt.Format("15:04:05")))
	}
	if freshness := m.renderFreshnessIndicator(); freshness != "" && width >= 72 {
		parts = append(parts, freshness)
	}
	if width < 72 {
		return m.renderCompactOverviewHeader(projectCount, sessionCount, width, counts)
	}
	return ansi.Truncate(lipgloss.NewStyle().Width(width).MaxWidth(width).Render(" "+strings.Join(parts, "  ")), width, "")
}

func (m Model) renderCompactOverviewHeader(projectCount, sessionCount, width int, counts orchestrationOverviewHeaderCounts) string {
	parts := []string{
		"Orch",
		fmt.Sprintf("%dp", projectCount),
		fmt.Sprintf("%ds", sessionCount),
	}
	if counts.hiddenProjects > 0 {
		parts = append(parts, fmt.Sprintf("%dh", counts.hiddenProjects))
	}
	if counts.hiddenTasks > 0 {
		parts = append(parts, fmt.Sprintf("%d non-session", counts.hiddenTasks))
	}
	if counts.backendErrors > 0 {
		parts = append(parts, fmt.Sprintf("degraded:%d", counts.backendErrors))
	}
	if m.taskSnapshotCheckedAt.IsZero() || !m.taskSnapshotFreshness.Valid() {
		if !m.orchestrationOverviewLoadedAt.IsZero() {
			parts = append(parts, m.orchestrationOverviewLoadedAt.Format("15:04"))
		}
	} else {
		parts = append(parts, string(m.taskSnapshotFreshness), m.taskSnapshotCheckedAt.UTC().Format("15:04"))
	}
	line := " " + strings.Join(parts, " ")
	return lipgloss.NewStyle().
		Foreground(uistyles.Text).
		Background(uistyles.Surface0).
		Width(width).
		MaxWidth(width).
		Render(ansi.Truncate(line, width, ""))
}

func overviewHeaderText(text string) string {
	return lipgloss.NewStyle().Foreground(uistyles.Text).Bold(true).Render(text)
}

func overviewHeaderDim(text string) string {
	return lipgloss.NewStyle().Foreground(uistyles.Overlay1).Render(text)
}

func overviewChip(text string, color lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(color).
		Background(uistyles.Surface0).
		Padding(0, 1).
		Bold(true).
		Render(text)
}

func (m Model) renderOverviewCards(projects []orchestrationProjectOverview, width, height int) string {
	cols := overviewColumnCount(projects, width)
	rows := max(1, (len(projects)+cols-1)/cols)
	cardHeights := overviewRowHeights(height, rows)
	renderedRows := make([]string, 0, rows)
	cursor := 0
	for row := 0; row < rows; row++ {
		cardHeight := cardHeights[row]
		rowProjects := projects[row*cols:]
		if len(rowProjects) > cols {
			rowProjects = rowProjects[:cols]
		}
		cardWidths := overviewCardWidths(rowProjects, width, cols)
		rowCards := make([]string, 0, cols)
		for col := 0; col < cols; col++ {
			idx := row*cols + col
			cardWidth := cardWidths[col]
			if idx >= len(projects) {
				rowCards = append(rowCards, lipgloss.NewStyle().Width(cardWidth).Height(cardHeight).Render(""))
				continue
			}
			rowCards = append(rowCards, m.renderOverviewProjectCard(projects[idx], cardWidth, cardHeight, &cursor))
		}
		renderedRows = append(renderedRows, lipgloss.JoinHorizontal(lipgloss.Top, rowCards...))
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(lipgloss.JoinVertical(lipgloss.Left, renderedRows...))
}

func overviewColumnCount(projects []orchestrationProjectOverview, width int) int {
	cardWidth := overviewBaseCardWidth(width)
	if cardWidth < 24 {
		cardWidth = max(18, width)
	}
	cols := max(1, width/cardWidth)
	if cols > len(projects) && len(projects) > 0 {
		cols = len(projects)
	}
	return max(1, cols)
}

func overviewBaseCardWidth(width int) int {
	switch {
	case width >= 120:
		return 60
	case width >= 80:
		return 44
	default:
		return width
	}
}

func overviewRowHeights(height, rows int) []int {
	if rows < 1 {
		rows = 1
	}
	out := make([]int, rows)
	base := height / rows
	if base < 1 {
		base = 1
	}
	for i := range out {
		out[i] = base
	}
	extra := height - base*rows
	for i := 0; i < extra; i++ {
		out[i%rows]++
	}
	if height >= rows*3 {
		for i := range out {
			out[i] = max(3, out[i])
		}
	}
	return out
}

func overviewCardWidths(projects []orchestrationProjectOverview, width, cols int) []int {
	if cols < 1 {
		return []int{width}
	}
	out := make([]int, cols)
	if cols == 2 && width >= 120 && len(projects) == 2 {
		leftWeight := max(1, len(projects[0].Tasks))
		rightWeight := max(1, len(projects[1].Tasks))
		total := leftWeight + rightWeight
		first := width * leftWeight / total
		minWidth := max(36, width/4)
		maxWidth := width - minWidth
		first = clampInt(first, minWidth, maxWidth)
		out[0] = first
		out[1] = width - first
		return out
	}
	base := width / cols
	for i := range out {
		out[i] = base
	}
	for i := 0; i < width-base*cols; i++ {
		out[i%cols]++
	}
	return out
}

func (m Model) renderOverviewProjectCard(project orchestrationProjectOverview, width, height int, cursor *int) string {
	contentWidth := max(1, width-4)
	contentHeight := max(1, height-2)
	innerWidth := max(8, contentWidth)
	innerHeight := max(1, contentHeight)
	title := strings.TrimSpace(project.Name)
	if title == "" {
		title = project.ProjectID
	}
	if title == "" {
		title = "project"
	}
	titleLine := ansi.Truncate(title, innerWidth, "...")
	sessionCount := overviewProjectSessionCount(project)
	meta := fmt.Sprintf("%d sessions", sessionCount)
	if project.Err != nil && project.Fallback != "" {
		meta = fmt.Sprintf("%s fallback  %s  %d sessions", project.Fallback, overviewDegradedReason(project.Err), sessionCount)
	} else if project.Err != nil {
		meta = fmt.Sprintf("degraded: %s  %d sessions", overviewDegradedReason(project.Err), sessionCount)
	} else if project.Freshness != "" {
		meta = fmt.Sprintf("%s  %d sessions", project.Freshness, sessionCount)
	}
	lines := []string{
		lipgloss.NewStyle().Foreground(uistyles.Text).Bold(true).Render(titleLine),
		m.styles.StatusHint.Render(ansi.Truncate(meta, innerWidth, "...")),
	}
	if project.Err == nil && len(project.Tasks) == 0 && len(project.Observations) == 0 {
		lines = append(lines, m.styles.StatusHint.Render("No active sessions"))
	}
	remaining := innerHeight - len(lines)
	if len(project.ObservationErrs) > 0 && remaining > 0 {
		warning := "observations degraded: " + strings.Join(project.ObservationErrs, "; ")
		lines = append(lines, m.styles.StatusHint.Render(ansi.Truncate(warning, innerWidth, "...")))
		remaining = innerHeight - len(lines)
	}
	sessionObservations := overviewSessionObservations(project.Observations, project.Tasks)
	if len(sessionObservations) > 0 {
		taskByID := overviewTasksByID(project.Tasks)
		for _, group := range overviewObservationGroupOrder {
			groupObservations := overviewObservationsInGroup(sessionObservations, group.name)
			if len(groupObservations) == 0 || remaining <= 0 {
				continue
			}
			groupLine := lipgloss.NewStyle().Foreground(uistyles.Overlay1).Bold(true).Render(group.title)
			lines = append(lines, ansi.Truncate(groupLine, innerWidth, "..."))
			remaining = innerHeight - len(lines)
			for _, observation := range groupObservations {
				if remaining <= 0 {
					break
				}
				selected := false
				if cursor != nil {
					selected = *cursor == m.orchestrationOverviewCursor
					*cursor = *cursor + 1
				}
				task := taskByID[strings.TrimSpace(observation.IssueID)]
				taskLines := m.renderOverviewObservationLines(project, task, observation, innerWidth, remaining, selected)
				lines = append(lines, taskLines...)
				remaining = innerHeight - len(lines)
			}
		}
	} else {
		for _, task := range project.Tasks {
			if remaining <= 0 {
				break
			}
			selected := false
			if cursor != nil {
				selected = *cursor == m.orchestrationOverviewCursor
				*cursor = *cursor + 1
			}
			taskLines := m.renderOverviewTaskLines(project, task, innerWidth, remaining, selected)
			lines = append(lines, taskLines...)
			remaining = innerHeight - len(lines)
		}
	}
	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return lipgloss.NewStyle().
		Width(contentWidth).
		Height(contentHeight).
		MaxHeight(height).
		Border(lipgloss.NormalBorder()).
		BorderForeground(overviewProjectBorderColor(project)).
		Padding(0, 1).
		Render(content)
}

func overviewTasksByID(tasks []domain.Task) map[string]domain.Task {
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		if task.ID.IsZero() {
			continue
		}
		byID[task.ID.String()] = task
	}
	return byID
}

func overviewProjectSessionCount(project orchestrationProjectOverview) int {
	count := 0
	for _, task := range project.Tasks {
		if overviewTaskHasRuntimeSession(task) {
			count++
		}
	}
	return count
}

func (m Model) renderOverviewObservationLines(project orchestrationProjectOverview, task domain.Task, observation domain.WorkerObservation, width, budget int, selected bool) []string {
	if budget <= 0 {
		return nil
	}
	issueID := strings.TrimSpace(observation.IssueID)
	if issueID == "" && !task.ID.IsZero() {
		issueID = task.ID.String()
	}
	state := strings.TrimSpace(string(observation.State))
	if state == "" {
		state = "unknown"
	}
	age := m.overviewObservationAge(observation)
	flags := overviewObservationEvidenceFlags(observation)
	headParts := []string{
		issueID,
		state,
		"age " + age,
	}
	if len(flags) > 0 {
		headParts = append(headParts, "evidence "+strings.Join(flags, ","))
	}
	lineStyle := lipgloss.NewStyle()
	prefix := "  "
	if selected {
		prefix = "> "
		lineStyle = lineStyle.Background(uistyles.Surface0).Width(width).MaxWidth(width)
	}
	lines := []string{lineStyle.Render(ansi.Truncate(prefix+strings.Join(headParts, " "), width, "..."))}
	if budget == 1 {
		return lines
	}
	reason := strings.TrimSpace(observation.Reason)
	if reason == "" {
		reason = "no observation reason"
	}
	action := overviewObservationPrimaryAction(observation)
	reasonLine := "  reason: " + reason
	if action != "" {
		reasonLine += " | action: " + action
	}
	if selected {
		reasonLine = lipgloss.NewStyle().Background(uistyles.Surface0).Width(width).MaxWidth(width).Render(ansi.Truncate(reasonLine, width, "..."))
	} else {
		reasonLine = ansi.Truncate(reasonLine, width, "...")
	}
	lines = append(lines, reasonLine)
	if budget == 2 {
		return lines
	}
	signal := overviewObservationLastEventSignal(observation)
	taskSignal := overviewObservationSignal(task)
	if signal != "" && taskSignal != "" {
		signal += " | " + taskSignal
	} else if signal == "" {
		signal = taskSignal
	}
	if signal != "" {
		signalLine := "  signal: " + signal
		if title := strings.TrimSpace(task.Title); title != "" && title != issueID {
			signalLine += " | " + title
		}
		if selected {
			signalLine = lipgloss.NewStyle().Background(uistyles.Surface0).Width(width).MaxWidth(width).Render(ansi.Truncate(signalLine, width, "..."))
		} else {
			signalLine = ansi.Truncate(signalLine, width, "...")
		}
		lines = append(lines, signalLine)
	}
	return lines
}

func (m Model) overviewObservationAge(observation domain.WorkerObservation) string {
	eventAt, ok := overviewObservationEventTime(observation)
	if !ok {
		return "unknown"
	}
	now := m.orchestrationOverviewLoadedAt
	if now.IsZero() {
		now = time.Now()
	}
	age := now.Sub(eventAt)
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String()
}

func overviewObservationEventTime(observation domain.WorkerObservation) (time.Time, bool) {
	if observation.LastEvent == nil || observation.LastEvent.At.IsZero() {
		return time.Time{}, false
	}
	return observation.LastEvent.At, true
}

func overviewObservationPrimaryAction(observation domain.WorkerObservation) string {
	for _, action := range observation.NextActions {
		if trimmed := strings.TrimSpace(action); trimmed != "" {
			return trimmed
		}
	}
	switch observation.State {
	case domain.WorkerObservationWaitingHuman:
		return "inspect worker prompt"
	case domain.WorkerObservationReviewReady:
		return "validate evidence"
	case domain.WorkerObservationBlocked, domain.WorkerObservationFailed:
		return "inspect blocker"
	case domain.WorkerObservationCleanupPending, domain.WorkerObservationDone:
		return "cleanup integrated worker"
	case domain.WorkerObservationRunnable:
		return "start worker"
	default:
		return "monitor worker"
	}
}

func overviewObservationEvidenceFlags(observation domain.WorkerObservation) []string {
	flags := make([]string, 0, 6)
	if observation.LastEvent != nil {
		flags = append(flags, "last")
		switch strings.TrimSpace(observation.LastEvent.Kind) {
		case "mailbox":
			flags = append(flags, "mail")
		case "issue_event":
			flags = append(flags, "issue")
		}
	}
	if len(observation.EvidenceSummary) > 0 {
		flags = append(flags, "evidence")
	}
	if len(observation.Risks) > 0 {
		flags = append(flags, "risk")
	}
	if len(observation.NextActions) > 0 {
		flags = append(flags, "action")
	}
	return flags
}

func overviewObservationSignal(task domain.Task) string {
	parts := make([]string, 0, 3)
	if task.Session != nil {
		label := strings.TrimSpace(task.Session.DisplayLabel())
		if label == "" {
			label = string(task.Session.State)
		}
		if label != "" {
			parts = append(parts, "session "+label)
		}
	} else if task.HasTmuxSession {
		parts = append(parts, "session")
	}
	if git := overviewGitSummary(task); git != "" {
		parts = append(parts, "git "+git)
	}
	if len(parts) == 0 && task.Status != "" {
		parts = append(parts, "status "+task.Status.String())
	}
	return strings.Join(parts, " ")
}

func overviewObservationLastEventSignal(observation domain.WorkerObservation) string {
	if observation.LastEvent == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if kind := strings.TrimSpace(observation.LastEvent.Kind); kind != "" {
		parts = append(parts, kind)
	}
	if typ := strings.TrimSpace(observation.LastEvent.Type); typ != "" {
		parts = append(parts, typ)
	}
	if summary := strings.TrimSpace(observation.LastEvent.Summary); summary != "" {
		parts = append(parts, summary)
	}
	return strings.Join(parts, " ")
}

func (m Model) renderOverviewTaskLines(project orchestrationProjectOverview, task domain.Task, width, budget int, selected bool) []string {
	if budget <= 0 {
		return nil
	}
	sessionLabel := "no-agent"
	if task.Session != nil {
		sessionLabel = task.Session.DisplayLabel()
	} else if task.HasTmuxSession {
		sessionLabel = "session"
	}
	gitSummary := overviewGitSummary(task)
	head := strings.Join([]string{
		lipgloss.NewStyle().Foreground(uistyles.Mauve).Bold(true).Render(task.ID.String()),
		overviewStatusBadge(task.Status),
		overviewSessionBadge(m, task, sessionLabel),
	}, " ")
	if gitSummary != "" {
		head += " " + overviewGitBadge(task, gitSummary)
	}
	prefix := "  "
	lineStyle := lipgloss.NewStyle()
	if selected {
		prefix = "> "
		lineStyle = lineStyle.Background(uistyles.Surface0).Width(width).MaxWidth(width)
	}
	lines := []string{lineStyle.Render(ansi.Truncate(prefix+head, width, "..."))}
	if budget == 1 {
		return lines
	}
	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = "(untitled)"
	}
	titleStyle := lipgloss.NewStyle().Foreground(uistyles.Text)
	if selected {
		titleStyle = titleStyle.Background(uistyles.Surface0).Width(width).MaxWidth(width)
	}
	lines = append(lines, titleStyle.Render(ansi.Truncate("  "+title, width, "...")))
	if budget == 2 {
		return lines
	}
	source, context := overviewTaskContext(project, task)
	if context != "" {
		sourceLabel := overviewProgressSourceBadge(source)
		body := m.styles.StatusHint.Render(ansi.Truncate(context, max(1, width-lipgloss.Width(sourceLabel+" ")), "..."))
		contextLine := sourceLabel + " " + body
		if selected {
			contextLine = lipgloss.NewStyle().Background(uistyles.Surface0).Width(width).MaxWidth(width).Render(contextLine)
		}
		lines = append(lines, contextLine)
	}
	return lines
}

func overviewProjectBorderColor(project orchestrationProjectOverview) lipgloss.Color {
	if project.Err != nil {
		return uistyles.Yellow
	}
	for _, task := range project.Tasks {
		if task.Session != nil {
			if task.Session.IsPartial() {
				return uistyles.Peach
			}
			switch task.Session.State {
			case domain.SessionBusy:
				return uistyles.Yellow
			case domain.SessionWaiting:
				return uistyles.Blue
			case domain.SessionError:
				return uistyles.Red
			}
		}
	}
	return uistyles.Surface2
}

func overviewStatusBadge(status domain.Status) string {
	color := uistyles.Blue
	switch status {
	case domain.StatusInProgress:
		color = uistyles.Mauve
	case domain.StatusInReview:
		color = uistyles.Yellow
	case domain.StatusDone:
		color = uistyles.Green
	}
	return lipgloss.NewStyle().
		Foreground(color).
		Background(uistyles.Surface0).
		Padding(0, 1).
		Bold(true).
		Render(status.String())
}

func overviewSessionBadge(m Model, task domain.Task, label string) string {
	style := m.styles.Session(task.Session).Bold(true).Background(uistyles.Surface0).Padding(0, 1)
	if task.Session == nil && task.HasTmuxSession {
		style = lipgloss.NewStyle().Foreground(uistyles.Blue).Background(uistyles.Surface0).Bold(true).Padding(0, 1)
	}
	return style.Render(label)
}

func overviewGitBadge(task domain.Task, summary string) string {
	color := uistyles.Green
	if task.HasConflicts {
		color = uistyles.Red
	} else if task.HasUncommittedChanges || task.GitBehindCount > 0 {
		color = uistyles.Yellow
	} else if task.GitAheadCount > 0 {
		color = uistyles.Blue
	}
	return lipgloss.NewStyle().
		Foreground(color).
		Background(uistyles.Surface0).
		Padding(0, 1).
		Render("git " + summary)
}

func overviewGitSummary(task domain.Task) string {
	parts := make([]string, 0, 4)
	if task.HasConflicts {
		parts = append(parts, "conflicts")
	}
	if task.HasUncommittedChanges {
		parts = append(parts, "dirty")
	}
	if task.GitAdditions != 0 || task.GitDeletions != 0 {
		parts = append(parts, fmt.Sprintf("+%s/-%s", overviewCompactCount(task.GitAdditions), overviewCompactCount(task.GitDeletions)))
	}
	if task.GitAheadCount != 0 || task.GitBehindCount != 0 {
		parts = append(parts, fmt.Sprintf("ahead %s behind %s", overviewCompactCount(task.GitAheadCount), overviewCompactCount(task.GitBehindCount)))
	}
	return strings.Join(parts, " ")
}

func overviewCompactCount(n int) string {
	sign := ""
	value := n
	if value < 0 {
		sign = "-"
		value = -value
	}
	switch {
	case value >= 1000000:
		return fmt.Sprintf("%s%.1fM", sign, float64(value)/1000000)
	case value >= 1000:
		return fmt.Sprintf("%s%.1fk", sign, float64(value)/1000)
	default:
		return fmt.Sprintf("%s%d", sign, value)
	}
}

func overviewProgressSourceBadge(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "progress"
	}
	return lipgloss.NewStyle().Foreground(uistyles.Teal).Bold(true).Render(source)
}

func overviewTaskContext(project orchestrationProjectOverview, task domain.Task) (string, string) {
	taskID := strings.TrimSpace(task.ID.String())
	if project.MailByTask != nil {
		if evt, ok := project.MailByTask[taskID]; ok {
			eventType := strings.TrimSpace(evt.Type)
			body := firstNonEmptyLine(evt.Body)
			if body != "" && eventType != "" {
				return "mail", eventType + ": " + body
			}
			if body != "" {
				return "mail", body
			}
		}
	}
	if context := firstNonEmptyLine(task.Notes); context != "" {
		return "notes", context
	}
	if context := firstNonEmptyLine(task.Acceptance); context != "" {
		return "ac", context
	}
	if context := firstNonEmptyLine(task.Description); context != "" {
		return "desc", context
	}
	if context := firstNonEmptyLine(task.Design); context != "" {
		return "design", context
	}
	if task.Session != nil {
		if label := strings.TrimSpace(task.Session.DisplayLabel()); label != "" {
			return "activity", label
		}
	}
	if task.HasTmuxSession {
		return "activity", "session"
	}
	return "", ""
}

func overviewLatestMailByTask(ctx context.Context, client *daemonclient.Client, repoDir string, tasks []domain.Task) map[string]protocol.MailEvent {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return nil
	}
	parents := overviewMailParents(tasks, os.Getenv("AZEDARACH_ISSUE_ID"))
	if len(parents) == 0 {
		return nil
	}
	latest := make(map[string]protocol.MailEvent)
	for _, parent := range parents {
		events, err := client.MailList(ctx, protocol.MailListCommandBody{
			RepoDir:     repoDir,
			ParentIssue: parent,
			Limit:       40,
		})
		if err != nil {
			continue
		}
		for _, evt := range events {
			issueID := strings.TrimSpace(evt.IssueID.String())
			if issueID == "" {
				continue
			}
			if existing, ok := latest[issueID]; ok && !mailEventAfter(evt, existing) {
				continue
			}
			latest[issueID] = evt
		}
	}
	if len(latest) == 0 {
		return nil
	}
	return latest
}

func overviewMailParents(tasks []domain.Task, activeIssueID string) []string {
	seen := map[string]struct{}{}
	parents := make([]string, 0)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		parents = append(parents, id)
	}
	add(activeIssueID)
	for _, task := range tasks {
		if task.ParentID != nil {
			add(task.ParentID.String())
		}
		if task.Session != nil || task.HasTmuxSession {
			add(task.ID.String())
		}
	}
	return parents
}

func mailEventAfter(a, b protocol.MailEvent) bool {
	if a.Seq != b.Seq {
		return a.Seq > b.Seq
	}
	aTime, aErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(a.CreatedAt))
	bTime, bErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(b.CreatedAt))
	if aErr == nil && bErr == nil && !aTime.Equal(bTime) {
		return aTime.After(bTime)
	}
	return strings.TrimSpace(a.Body) > strings.TrimSpace(b.Body)
}

func firstNonEmptyLine(values ...string) string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
