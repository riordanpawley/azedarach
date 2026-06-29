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
				hiddenTasks += entryHiddenTasks
				entry.MailByTask = overviewLatestMailByTask(ctx, client, project.path, snapshot.Tasks)
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
		if task.Session == nil && !task.HasTmuxSession {
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
		return m.orchestrationOverview
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
	case width >= 150:
		return 50
	case width >= 110:
		return 44
	case width >= 80:
		return 40
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
	if project.Err == nil && len(project.Tasks) == 0 {
		lines = append(lines, m.styles.StatusHint.Render("No active sessions"))
	}
	remaining := innerHeight - len(lines)
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

func overviewProjectSessionCount(project orchestrationProjectOverview) int {
	count := 0
	for _, task := range project.Tasks {
		if task.Session != nil || task.HasTmuxSession {
			count++
		}
	}
	return count
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
