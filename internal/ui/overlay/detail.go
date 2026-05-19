package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	uistyles "github.com/riordanpawley/azedarach/internal/ui/styles"
)

// DetailPanel displays full task details with scrollable description
type DetailPanel struct {
	task           domain.Task
	relatedTasks   []domain.Task
	decisionLinks  []DecisionLinkSummary
	mutation       *TaskMutationProgress
	scrollY        int
	graphCursor    int
	graphFocused   bool
	contentHeight  int
	viewHeight     int
	descViewHeight int
	wrapWidth      int
	checkedAt      time.Time
	freshness      protocol.TaskListFreshness
	styles         *Styles
}

// DecisionLinkSummary is a read-only projection of a decision link rendered
// in the issue detail panel. The TUI model assembles these from daemonclient
// responses so the overlay package can stay decoupled from daemonclient.
type DecisionLinkSummary struct {
	DecisionID    string
	DecisionTitle string
	Status        string
	Relation      string
	Note          string
}

// TaskMutationProgress represents in-flight mutation metadata for a task.
type TaskMutationProgress struct {
	OperationID     string
	State           string
	ProgressPercent int
	ProgressMessage string
	PreviousStatus  domain.Status
	TargetStatus    domain.Status
}

type taskGraphLink struct {
	Direction string
	Relation  string
	Task      domain.Task
}

const (
	graphRelParent         = "parent"
	graphRelChild          = "child"
	graphRelBlocks         = "blocks"
	graphRelBlockedBy      = "blocked-by"
	graphRelRelated        = "related"
	graphRelDiscoveredFrom = "discovered-from"
	graphRelDiscovered     = "discovered"
)

var graphRelationOrder = []string{
	graphRelParent,
	graphRelChild,
	graphRelBlocks,
	graphRelBlockedBy,
	graphRelRelated,
	graphRelDiscoveredFrom,
	graphRelDiscovered,
}

var graphRelationLabels = map[string]string{
	graphRelParent:         "Parent",
	graphRelChild:          "Children",
	graphRelBlocks:         "Blocks",
	graphRelBlockedBy:      "Blocked by",
	graphRelRelated:        "Related",
	graphRelDiscoveredFrom: "Discovered from",
	graphRelDiscovered:     "Discovered",
}

var graphRelationDirection = map[string]string{
	graphRelParent:         "ascendant",
	graphRelBlockedBy:      "ascendant",
	graphRelDiscoveredFrom: "ascendant",
	graphRelChild:          "descendant",
	graphRelBlocks:         "descendant",
	graphRelDiscovered:     "descendant",
	graphRelRelated:        "descendant",
}

// NewDetailPanel creates a new detail panel for the given task and optional session
func NewDetailPanel(task domain.Task) *DetailPanel {
	// Calculate contentHeight based on description
	contentHeight := 0
	if task.Description != "" {
		contentHeight = len(strings.Split(task.Description, "\n"))
	}

	return &DetailPanel{
		task:           task,
		scrollY:        0,
		contentHeight:  contentHeight,
		viewHeight:     20, // Default, will be updated in Size()
		descViewHeight: 20,
		wrapWidth:      80,
		styles:         New(),
	}
}

// WithMutationProgress attaches in-flight mutation metadata for the selected task.
func (d *DetailPanel) WithMutationProgress(progress *TaskMutationProgress) *DetailPanel {
	d.mutation = cloneTaskMutationProgress(progress)
	return d
}

// WithRelatedTasks attaches the current task list so the panel can render
// incoming dependency edges alongside the task's outgoing dependencies.
func (d *DetailPanel) WithRelatedTasks(tasks []domain.Task) *DetailPanel {
	d.relatedTasks = append([]domain.Task(nil), tasks...)
	return d
}

// WithDecisionLinks attaches the set of decisions linked to this issue.
// The order of the slice is preserved in the rendered output.
func (d *DetailPanel) WithDecisionLinks(links []DecisionLinkSummary) *DetailPanel {
	d.decisionLinks = append([]DecisionLinkSummary(nil), links...)
	return d
}

// DecisionLinks returns the decision link summaries currently attached to the
// panel. Exported for tests and for the workspace overlay's syncing path.
func (d *DetailPanel) DecisionLinks() []DecisionLinkSummary {
	return append([]DecisionLinkSummary(nil), d.decisionLinks...)
}

// WithSnapshotFreshness attaches daemon-authored snapshot freshness metadata.
func (d *DetailPanel) WithSnapshotFreshness(checkedAt time.Time, freshness protocol.TaskListFreshness) *DetailPanel {
	d.checkedAt = checkedAt
	d.freshness = freshness
	return d
}

// Init initializes the detail panel
func (d *DetailPanel) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (d *DetailPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return d, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			if d.scrollY < d.maxScroll() {
				d.scrollY++
			}
			return d, nil

		case "k", "up":
			if d.scrollY > 0 {
				d.scrollY--
			}
			return d, nil
		case "h", "left":
			return d, d.graphNavigationCmd("ascendant")
		case "l", "right":
			return d, d.graphNavigationCmd("descendant")
		case "[":
			if d.GraphLinkCount() > 0 {
				d.MoveGraphCursor(-1)
			}
			return d, nil
		case "]":
			if d.GraphLinkCount() > 0 {
				d.MoveGraphCursor(1)
			}
			return d, nil
		case "<":
			return d, d.graphNavigationCmd("ascendant")
		case ">":
			return d, d.graphNavigationCmd("descendant")
		case "ctrl+d":
			d.scrollY = min(d.maxScroll(), d.scrollY+d.halfPageStep())
			return d, nil
		case "ctrl+u":
			d.scrollY = max(0, d.scrollY-d.halfPageStep())
			return d, nil

		case "g":
			// Jump to top
			d.scrollY = 0
			return d, nil

		case "G":
			// Jump to bottom
			d.scrollY = d.maxScroll()
			return d, nil
		}
	}

	return d, nil
}

// View renders the detail panel
func (d *DetailPanel) View() string {
	lines, graphCursorLine := d.buildLines()

	visible := max(1, d.viewHeight)
	d.descViewHeight = visible
	d.contentHeight = len(lines)

	if d.graphFocused && graphCursorLine >= 0 {
		if graphCursorLine < d.scrollY {
			d.scrollY = graphCursorLine
		} else if graphCursorLine >= d.scrollY+visible {
			d.scrollY = graphCursorLine - visible + 1
		}
	}

	if d.scrollY > d.maxScroll() {
		d.scrollY = d.maxScroll()
	}
	if d.scrollY < 0 {
		d.scrollY = 0
	}

	start := d.scrollY
	end := min(len(lines), start+visible)

	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(lines[i])
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	if d.maxScroll() > 0 {
		b.WriteString("\n")
		b.WriteString(d.styles.Footer.Render(
			fmt.Sprintf("[j/k or ctrl+u/d to scroll, g/G to jump] (line %d/%d)", d.scrollY+1, d.contentHeight),
		))
	}

	return b.String()
}

func (d *DetailPanel) buildLines() ([]string, int) {
	graphCursorLine := -1

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)
	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(12).
		Align(lipgloss.Right)
	valueStyle := d.styles.MenuItem

	var lines []string
	addLine := func(line string) {
		lines = append(lines, line)
	}

	addLine(headerStyle.Render(fmt.Sprintf("[%s] %s", d.task.ID, d.task.Title)))
	addLine("")
	addLine(labelStyle.Render("Issue:") + "  " + d.formatIssueCardSummary())
	if metadata := d.renderIssueMetadataLines(labelStyle, valueStyle); metadata != "" {
		for _, line := range strings.Split(strings.TrimSuffix(metadata, "\n"), "\n") {
			addLine(line)
		}
	}
	if d.mutation != nil {
		addLine(labelStyle.Render("Issue Ops:") + "  " + valueStyle.Render(d.formatMutationProgress()))
	}
	if d.task.ParentID != nil {
		addLine(labelStyle.Render("Parent:") + "  " + valueStyle.Render(d.task.ParentID.String()))
	}
	if total, done := d.childProgress(); total > 0 {
		addLine(labelStyle.Render("Children:") + "  " + valueStyle.Render(fmt.Sprintf("%d total (%d done)", total, done)))
	}
	if deps := d.renderDependencies(); deps != "" {
		addLine("")
		addLine(headerStyle.Render("Dependencies"))
		for _, line := range strings.Split(deps, "\n") {
			addLine(line)
		}
	}
	addLine("")
	addLine(labelStyle.Render("Created:") + "  " + valueStyle.Render(d.formatTime(d.task.CreatedAt)))
	addLine(labelStyle.Render("Updated:") + "  " + valueStyle.Render(d.formatTime(d.task.UpdatedAt)))
	if d.hasSnapshotFreshness() {
		addLine(labelStyle.Render("Freshness:") + "  " + d.formatSnapshotFreshness())
		addLine(labelStyle.Render("Checked:") + "  " + valueStyle.Render(d.formatTime(d.checkedAt)))
	}

	if d.showRuntimeSections() {
		addLine("")
		addLine(headerStyle.Render("Runtime"))
		addLine(labelStyle.Render("Session:") + "  " + d.formatSessionSummary())
		addLine(labelStyle.Render("Worktree:") + "  " + valueStyle.Render(d.formatGitWorktreeSummary()))
	}

	d.addTextSectionLines(&lines, headerStyle, valueStyle, "Design", d.task.Design)
	d.addTextSectionLines(&lines, headerStyle, valueStyle, "Acceptance", d.task.Acceptance)
	d.addTextSectionLines(&lines, headerStyle, valueStyle, "Notes", d.task.Notes)

	if len(d.decisionLinks) > 0 {
		addLine("")
		addLine(headerStyle.Render("Decisions"))
		for _, link := range d.decisionLinks {
			addLine(valueStyle.Render(d.formatDecisionLink(link)))
		}
	}

	if links := d.graphLinks(); len(links) > 0 {
		if d.graphCursor >= len(links) {
			d.graphCursor = len(links) - 1
		}
		if d.graphCursor < 0 {
			d.graphCursor = 0
		}
		addLine("")
		addLine(headerStyle.Render("Graph"))
		for _, rel := range graphRelationOrder {
			if !hasRelation(links, rel) {
				continue
			}
			addLine(d.styles.MenuItem.Render(graphRelationLabels[rel]))
			graphCursorLine = d.appendGraphRows(&lines, links, rel, graphCursorLine)
		}
	}

	if d.task.Description != "" {
		addLine("")
		addLine(headerStyle.Render("Description"))
		wrapWidth := d.wrapWidth
		if wrapWidth < 10 {
			wrapWidth = 10
		}
		descLines := wrapDescriptionLines(d.task.Description, wrapWidth)
		for _, line := range descLines {
			addLine(valueStyle.Render(line))
		}
	}

	return lines, graphCursorLine
}

func (d *DetailPanel) appendGraphRows(lines *[]string, links []taskGraphLink, relation string, cursorLine int) int {
	for i, link := range links {
		if link.Relation != relation {
			continue
		}
		prefix := d.graphDirectionPrefix(link.Direction, false)
		style := d.styles.MenuItem
		if d.graphFocused && i == d.graphCursor {
			prefix = d.graphDirectionPrefix(link.Direction, true)
			style = d.styles.MenuItemActive
			cursorLine = len(*lines)
		}
		*lines = append(*lines, style.Render(fmt.Sprintf("%s %s [%s] %s", prefix, link.Task.ID, d.formatStatus(link.Task.Status), link.Task.Title)))
	}
	return cursorLine
}

func hasRelation(links []taskGraphLink, relation string) bool {
	for _, link := range links {
		if link.Relation == relation {
			return true
		}
	}
	return false
}

func (d *DetailPanel) renderDependencies() string {
	outgoing := d.task.Dependencies
	incoming := d.incomingDependencies()
	if len(outgoing) == 0 && len(incoming) == 0 {
		return ""
	}

	var b strings.Builder
	if len(outgoing) > 0 {
		b.WriteString(d.styles.MenuItem.Render("Outgoing"))
		b.WriteString("\n")
		for _, dep := range outgoing {
			b.WriteString(fmt.Sprintf("- %s -> %s\n", dep.Type, dep.ID))
		}
	}
	if len(incoming) > 0 {
		b.WriteString(d.styles.MenuItem.Render("Incoming"))
		b.WriteString("\n")
		for _, dep := range incoming {
			b.WriteString(fmt.Sprintf("- %s <- %s\n", dep.Type, dep.ID))
		}
	}

	return strings.TrimSuffix(b.String(), "\n")
}

func (d *DetailPanel) renderIssueMetadataLines(labelStyle, valueStyle lipgloss.Style) string {
	var b strings.Builder
	write := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		b.WriteString(labelStyle.Render(label + ":"))
		b.WriteString("  ")
		b.WriteString(valueStyle.Render(value))
		b.WriteString("\n")
	}
	write("Assignee", d.task.Assignee)
	if d.task.Estimate != nil {
		write("Estimate", fmt.Sprintf("%d", *d.task.Estimate))
	}
	if len(d.task.Labels) > 0 {
		write("Labels", strings.Join(d.task.Labels, ", "))
	}
	if len(d.task.Implementations) > 0 {
		write("Impls", strings.Join(d.task.Implementations, ", "))
	}
	return b.String()
}

func (d *DetailPanel) addTextSectionLines(lines *[]string, headerStyle, valueStyle lipgloss.Style, title, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	*lines = append(*lines, "", headerStyle.Render(title))
	for _, line := range wrapDescriptionLines(text, max(10, d.wrapWidth)) {
		*lines = append(*lines, valueStyle.Render(line))
	}
}

func (d *DetailPanel) graphDirectionPrefix(direction string, selected bool) string {
	switch direction {
	case "ascendant":
		return "<"
	case "descendant":
		return ">"
	default:
		if selected {
			return ">"
		}
		return "-"
	}
}

func (d *DetailPanel) graphLinks() []taskGraphLink {
	if len(d.relatedTasks) == 0 {
		return nil
	}
	byID := make(map[naming.IssueID]domain.Task, len(d.relatedTasks))
	for _, task := range d.relatedTasks {
		byID[task.ID] = task
	}

	var links []taskGraphLink
	seen := make(map[string]struct{})
	add := func(relation string, task domain.Task) {
		if task.ID == d.task.ID {
			return
		}
		key := relation + ":" + task.ID.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		links = append(links, taskGraphLink{
			Relation:  relation,
			Direction: graphRelationDirection[relation],
			Task:      task,
		})
	}

	if parent, ok := d.directParent(byID); ok {
		add(graphRelParent, parent)
	}
	for _, child := range d.collectChildTree(byID) {
		add(graphRelChild, child)
	}

	for _, dep := range d.task.Dependencies {
		target, ok := byID[dep.ID]
		if !ok {
			continue
		}
		switch dep.Type {
		case domain.DependencyBlocks:
			add(graphRelBlocks, target)
		case domain.DependencyBlockedBy:
			add(graphRelBlockedBy, target)
		case domain.DependencyRelatedTo:
			add(graphRelRelated, target)
		case domain.DependencyDiscovered:
			add(graphRelDiscoveredFrom, target)
		}
	}

	for _, other := range d.relatedTasks {
		if other.ID == d.task.ID {
			continue
		}
		for _, dep := range other.Dependencies {
			if dep.ID != d.task.ID {
				continue
			}
			switch dep.Type {
			case domain.DependencyBlocks:
				add(graphRelBlockedBy, other)
			case domain.DependencyBlockedBy:
				add(graphRelBlocks, other)
			case domain.DependencyRelatedTo:
				add(graphRelRelated, other)
			case domain.DependencyDiscovered:
				add(graphRelDiscovered, other)
			}
		}
	}

	return links
}

func (d *DetailPanel) GraphLinkCount() int {
	return len(d.graphLinks())
}

func (d *DetailPanel) MoveGraphCursor(delta int) {
	count := d.GraphLinkCount()
	if count == 0 {
		d.graphCursor = 0
		return
	}
	d.graphCursor = (d.graphCursor + delta + count) % count
}

func (d *DetailPanel) SelectedGraphTaskID() (string, bool) {
	return d.SelectedGraphTaskIDForDirection("")
}

func (d *DetailPanel) SelectedGraphTaskIDForDirection(direction string) (string, bool) {
	links := d.graphLinks()
	if len(links) == 0 {
		return "", false
	}
	if d.graphCursor < 0 {
		d.graphCursor = 0
	}
	if d.graphCursor >= len(links) {
		d.graphCursor = len(links) - 1
	}
	if direction != "" && links[d.graphCursor].Direction != direction {
		return "", false
	}
	return links[d.graphCursor].Task.ID.String(), true
}

func (d *DetailPanel) graphNavigationCmd(direction string) tea.Cmd {
	taskID, ok := d.SelectedGraphTaskIDForDirection(direction)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return SelectionMsg{Key: "task_workspace_open_task", Value: taskID}
	}
}

// directParent resolves the current task's direct parent if any, preferring
// the ParentID field and falling back to a parent-child dependency edge.
func (d *DetailPanel) directParent(byID map[naming.IssueID]domain.Task) (domain.Task, bool) {
	parentID, ok := parentOf(d.task)
	if !ok || parentID == d.task.ID {
		return domain.Task{}, false
	}
	task, ok := byID[parentID]
	return task, ok
}

// collectChildTree walks the child sub-tree rooted at the current task in BFS
// order. Parent → child edges come from either a child's ParentID or a
// parent-child dependency on the child pointing at the parent.
func (d *DetailPanel) collectChildTree(byID map[naming.IssueID]domain.Task) []domain.Task {
	childrenOf := make(map[naming.IssueID][]naming.IssueID)
	for _, task := range d.relatedTasks {
		if task.ParentID != nil {
			childrenOf[*task.ParentID] = append(childrenOf[*task.ParentID], task.ID)
		}
		for _, dep := range task.Dependencies {
			if dep.Type == domain.DependencyParentChild {
				childrenOf[dep.ID] = append(childrenOf[dep.ID], task.ID)
			}
		}
	}

	visited := map[naming.IssueID]struct{}{d.task.ID: {}}
	queue := append([]naming.IssueID(nil), childrenOf[d.task.ID]...)
	var out []domain.Task
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, seen := visited[id]; seen {
			continue
		}
		visited[id] = struct{}{}
		if task, ok := byID[id]; ok {
			out = append(out, task)
		}
		queue = append(queue, childrenOf[id]...)
	}
	return out
}

func parentOf(task domain.Task) (naming.IssueID, bool) {
	if task.ParentID != nil {
		return *task.ParentID, true
	}
	for _, dep := range task.Dependencies {
		if dep.Type == domain.DependencyParentChild {
			return dep.ID, true
		}
	}
	return "", false
}

func (d *DetailPanel) incomingDependencies() []domain.Dependency {
	if len(d.relatedTasks) == 0 {
		return nil
	}

	var incoming []domain.Dependency
	for _, task := range d.relatedTasks {
		if task.ID == d.task.ID {
			continue
		}
		for _, dep := range task.Dependencies {
			if dep.ID == d.task.ID {
				incoming = append(incoming, domain.Dependency{
					ID:   task.ID,
					Type: dep.Type,
				})
			}
		}
	}

	return incoming
}

func (d *DetailPanel) childProgress() (total int, done int) {
	if len(d.relatedTasks) == 0 {
		return 0, 0
	}
	for _, task := range d.relatedTasks {
		if task.ID == d.task.ID {
			continue
		}
		if task.ParentID != nil && *task.ParentID == d.task.ID {
			total++
			if task.Status == domain.StatusDone {
				done++
			}
			continue
		}
		for _, dep := range task.Dependencies {
			if dep.Type == domain.DependencyParentChild && dep.ID == d.task.ID {
				total++
				if task.Status == domain.StatusDone {
					done++
				}
				break
			}
		}
	}
	return total, done
}

// Title returns the overlay title
func (d *DetailPanel) Title() string {
	return "Task Details"
}

// Size returns the overlay dimensions
func (d *DetailPanel) Size() (width, height int) {
	d.viewHeight = 15 // Description viewing area
	return 70, 30     // Total overlay size
}

// formatStatus formats a status for display
func (d *DetailPanel) formatStatus(status domain.Status) string {
	switch status {
	case domain.StatusOpen:
		return "Open"
	case domain.StatusInProgress:
		return "In Progress"
	case domain.StatusBlocked:
		return "Blocked"
	case domain.StatusDone:
		return "Done"
	default:
		return string(status)
	}
}

// formatTime formats a timestamp for display
func (d *DetailPanel) formatTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

// formatDecisionLink renders a single Decisions section row. Format:
//
//	relation  decision-slug [status]  Title (optional)  — note (optional)
//
// All fields except DecisionID are optional and skipped when empty.
func (d *DetailPanel) formatDecisionLink(link DecisionLinkSummary) string {
	relation := strings.TrimSpace(link.Relation)
	if relation == "" {
		relation = "relates"
	}
	status := strings.TrimSpace(link.Status)
	title := strings.TrimSpace(link.DecisionTitle)
	note := strings.TrimSpace(link.Note)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-13s %s", relation, link.DecisionID))
	if status != "" {
		b.WriteString(fmt.Sprintf(" [%s]", status))
	}
	if title != "" {
		b.WriteString("  ")
		b.WriteString(title)
	}
	if note != "" {
		b.WriteString("  — ")
		b.WriteString(note)
	}
	return b.String()
}

// formatDuration formats a duration for display
func (d *DetailPanel) formatDuration(dur time.Duration) string {
	hours := int(dur.Hours())
	minutes := int(dur.Minutes()) % 60
	seconds := int(dur.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

func (d *DetailPanel) formatMutationProgress() string {
	if d.mutation == nil {
		return ""
	}
	state := strings.TrimSpace(strings.ToLower(d.mutation.State))
	if state == "" {
		state = "pending"
	}
	progress := state
	if d.mutation.TargetStatus != "" {
		progress = fmt.Sprintf("%s (%s -> %s)", state, d.formatStatus(d.mutation.PreviousStatus), d.formatStatus(d.mutation.TargetStatus))
	}
	if operationID := strings.TrimSpace(d.mutation.OperationID); operationID != "" {
		if d.mutation.ProgressPercent > 0 || strings.TrimSpace(d.mutation.ProgressMessage) != "" {
			progressBits := make([]string, 0, 2)
			if d.mutation.ProgressPercent > 0 {
				progressBits = append(progressBits, fmt.Sprintf("%d%%", d.mutation.ProgressPercent))
			}
			if message := strings.TrimSpace(d.mutation.ProgressMessage); message != "" {
				progressBits = append(progressBits, message)
			}
			return fmt.Sprintf("%s [operation %s] [%s]", progress, operationID, strings.Join(progressBits, " - "))
		}
		return fmt.Sprintf("%s [operation %s]", progress, operationID)
	}
	if message := strings.TrimSpace(d.mutation.ProgressMessage); message != "" {
		return fmt.Sprintf("%s [%s]", progress, message)
	}
	return progress
}

// maxScroll returns the maximum scroll position
func (d *DetailPanel) maxScroll() int {
	visible := d.descViewHeight
	if visible < 1 {
		visible = d.viewHeight
	}
	return max(0, d.contentHeight-visible)
}

func cloneTaskMutationProgress(progress *TaskMutationProgress) *TaskMutationProgress {
	if progress == nil {
		return nil
	}
	cloned := *progress
	return &cloned
}

func (d *DetailPanel) halfPageStep() int {
	step := d.descViewHeight / 2
	if step < 1 {
		return 1
	}
	return step
}

func (d *DetailPanel) showRuntimeSections() bool {
	return d.task.Session != nil || d.hasGitStatusData()
}

func (d *DetailPanel) hasSnapshotFreshness() bool {
	return !d.checkedAt.IsZero() && d.freshness.Valid()
}

func (d *DetailPanel) formatSnapshotFreshness() string {
	label := string(d.freshness)
	style := lipgloss.NewStyle().Foreground(uistyles.Subtext0).Bold(true)
	switch d.freshness {
	case protocol.TaskListFreshnessFresh:
		style = lipgloss.NewStyle().Foreground(uistyles.Green).Bold(true)
	case protocol.TaskListFreshnessStale:
		style = lipgloss.NewStyle().Foreground(uistyles.Yellow).Bold(true)
	}
	return style.Render(label)
}

func (d *DetailPanel) formatSessionState() string {
	if d.task.Session == nil {
		return d.styles.MenuItem.Render("none")
	}
	stateLabel := fmt.Sprintf("%s %s", d.task.Session.State.Icon(), string(d.task.Session.State))
	return d.sessionStateStyle(d.task.Session.State).Render(stateLabel)
}

func (d *DetailPanel) sessionStateStyle(state domain.SessionState) lipgloss.Style {
	switch state {
	case domain.SessionBusy:
		return lipgloss.NewStyle().Foreground(uistyles.Blue).Bold(true)
	case domain.SessionWaiting:
		return lipgloss.NewStyle().Foreground(uistyles.Yellow).Bold(true)
	case domain.SessionDone:
		return lipgloss.NewStyle().Foreground(uistyles.Green).Bold(true)
	case domain.SessionError:
		return lipgloss.NewStyle().Foreground(uistyles.Red).Bold(true)
	case domain.SessionPaused:
		return lipgloss.NewStyle().Foreground(uistyles.Overlay0).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(uistyles.Subtext0).Bold(true)
	}
}

func (d *DetailPanel) formatWorktreeSummary() string {
	if d.task.Session != nil {
		if worktreePath := strings.TrimSpace(d.task.Session.Worktree); worktreePath != "" {
			return worktreePath
		}
	}
	if d.task.HasWorktree {
		return "present"
	}
	return "unknown"
}

func (d *DetailPanel) formatIssueCardSummary() string {
	priority := d.task.Priority
	if priority < 0 {
		priority = 0
	}
	if int(priority) >= len(uistyles.PriorityColors) {
		priority = domain.P4
	}
	priorityBadge := lipgloss.NewStyle().
		Foreground(uistyles.Base).
		Background(uistyles.PriorityColors[int(priority)]).
		Bold(true).
		Padding(0, 1).
		Render(d.task.Priority.String())

	typeColor := uistyles.Surface1
	switch d.task.Type {
	case domain.TypeEpic:
		typeColor = uistyles.Mauve
	case domain.TypeFeature:
		typeColor = uistyles.Green
	case domain.TypeBug:
		typeColor = uistyles.Red
	case domain.TypeTask:
		typeColor = uistyles.Blue
	case domain.TypeChore:
		typeColor = uistyles.Yellow
	}
	typeBadge := lipgloss.NewStyle().
		Foreground(uistyles.Base).
		Background(typeColor).
		Bold(true).
		Padding(0, 1).
		Render(d.task.Type.Short())

	statusColor, ok := uistyles.StatusColors[string(d.task.Status)]
	if !ok {
		statusColor = uistyles.Subtext0
	}
	status := lipgloss.NewStyle().
		Foreground(statusColor).
		Bold(true).
		Render(d.formatStatus(d.task.Status))

	return strings.Join([]string{priorityBadge, typeBadge, status}, " ")
}

func (d *DetailPanel) formatSessionSummary() string {
	if d.task.Session == nil {
		return d.styles.MenuItem.Render("none")
	}
	parts := []string{d.formatSessionState()}
	if d.task.Session.StartedAt != nil {
		parts = append(parts, "Age "+d.formatDuration(time.Since(*d.task.Session.StartedAt)))
	} else {
		parts = append(parts, "Age N/A")
	}
	if d.task.Session.DevServer != nil && d.task.Session.DevServer.Running {
		parts = append(parts, fmt.Sprintf("Dev :%d", d.task.Session.DevServer.Port))
	}
	return strings.Join(parts, " | ")
}

func (d *DetailPanel) formatGitWorktreeSummary() string {
	return d.formatGitStatus() + " | " + d.formatWorktreeSummary()
}

func (d *DetailPanel) hasGitStatusData() bool {
	if d.task.HasWorktree {
		return true
	}
	if d.task.Session != nil && strings.TrimSpace(d.task.Session.Worktree) != "" {
		return true
	}
	return d.hasGitTelemetrySignal()
}

func (d *DetailPanel) hasGitTelemetrySignal() bool {
	if d.task.HasUncommittedChanges {
		return true
	}
	return d.task.GitAheadCount > 0 ||
		d.task.GitBehindCount > 0 ||
		d.task.GitAdditions > 0 ||
		d.task.GitDeletions > 0
}

func (d *DetailPanel) formatGitStatus() string {
	if d.task.HasWorktree && !d.hasGitTelemetrySignal() {
		return "clean"
	}
	if !d.hasGitTelemetrySignal() {
		return "unknown"
	}

	status := "clean"
	if d.task.HasUncommittedChanges {
		status = "dirty"
	}

	details := make([]string, 0, 2)
	if d.task.GitAdditions > 0 || d.task.GitDeletions > 0 {
		details = append(details, fmt.Sprintf("+%d/-%d", d.task.GitAdditions, d.task.GitDeletions))
	}
	if divergence := formatAheadBehindSummary(d.task.GitAheadCount, d.task.GitBehindCount); divergence != "" {
		details = append(details, divergence)
	}

	if len(details) == 0 {
		return status
	}
	return fmt.Sprintf("%s (%s)", status, strings.Join(details, "; "))
}

func formatAheadBehindSummary(ahead, behind int) string {
	if ahead > 0 && behind > 0 {
		return fmt.Sprintf("↑%d/↓%d", ahead, behind)
	}
	if behind > 0 {
		return fmt.Sprintf("↓%d", behind)
	}
	if ahead > 0 {
		return fmt.Sprintf("↑%d", ahead)
	}
	return ""
}

func wrapDescriptionLines(description string, width int) []string {
	if width < 1 {
		return strings.Split(description, "\n")
	}
	wordWrapped := ansi.Wrap(description, width, "-/")
	lines := strings.Split(wordWrapped, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if ansi.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	return out
}
