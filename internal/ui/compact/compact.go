package compact

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// CompactView represents a full-featured compact list view for tasks
// This is an alternative to the kanban board view
type CompactView struct {
	tasks    []domain.Task
	cursor   int
	selected map[string]bool
	styles   *Styles
	width    int
	height   int

	// Scrolling state
	scrollOffset int
}

// NewCompactView creates a new CompactView with the given tasks and dimensions
func NewCompactView(tasks []domain.Task, width, height int) *CompactView {
	return &CompactView{
		tasks:        tasks,
		cursor:       0,
		selected:     make(map[string]bool),
		styles:       NewStyles(),
		width:        width,
		height:       height,
		scrollOffset: 0,
	}
}

// SetTasks updates the task list
func (cv *CompactView) SetTasks(tasks []domain.Task) {
	cv.tasks = tasks
	// Clamp cursor to valid range
	if cv.cursor >= len(cv.tasks) {
		cv.cursor = max(0, len(cv.tasks)-1)
	}
}

// SetCursor sets the cursor position
func (cv *CompactView) SetCursor(index int) {
	if index < 0 {
		cv.cursor = 0
	} else if index >= len(cv.tasks) {
		cv.cursor = max(0, len(cv.tasks)-1)
	} else {
		cv.cursor = index
	}
	cv.ensureCursorVisible()
}

// GetCursor returns the current cursor position
func (cv *CompactView) GetCursor() int {
	return cv.cursor
}

// MoveUp moves cursor up by n positions
func (cv *CompactView) MoveUp(n int) {
	cv.SetCursor(cv.cursor - n)
}

// MoveDown moves cursor down by n positions
func (cv *CompactView) MoveDown(n int) {
	cv.SetCursor(cv.cursor + n)
}

// GotoTop moves cursor to the first task
func (cv *CompactView) GotoTop() {
	cv.SetCursor(0)
}

// GotoBottom moves cursor to the last task
func (cv *CompactView) GotoBottom() {
	cv.SetCursor(len(cv.tasks) - 1)
}

// GetCurrentTask returns the task at the cursor position
func (cv *CompactView) GetCurrentTask() *domain.Task {
	if cv.cursor >= 0 && cv.cursor < len(cv.tasks) {
		return &cv.tasks[cv.cursor]
	}
	return nil
}

// SetSelected sets the selected tasks map
func (cv *CompactView) SetSelected(selected map[string]bool) {
	cv.selected = selected
}

// SetDimensions updates the view dimensions
func (cv *CompactView) SetDimensions(width, height int) {
	cv.width = width
	cv.height = height
	cv.ensureCursorVisible()
}

// Render renders the full compact view
func (cv *CompactView) Render() string {
	if len(cv.tasks) == 0 {
		return cv.renderEmptyState()
	}

	var b strings.Builder

	// Render header
	b.WriteString(cv.renderHeader())
	b.WriteString("\n")
	b.WriteString(cv.renderSeparator())
	b.WriteString("\n")

	// Calculate visible range
	visibleRows := cv.calculateVisibleRows()
	startIdx := cv.scrollOffset
	endIdx := min(startIdx+visibleRows, len(cv.tasks))

	// Render visible rows
	for i := startIdx; i < endIdx; i++ {
		b.WriteString(cv.renderRow(i, cv.tasks[i]))
		if i < endIdx-1 {
			b.WriteString("\n")
		}
	}

	// Add scroll indicator if needed
	if endIdx < len(cv.tasks) {
		b.WriteString("\n")
		scrollInfo := cv.styles.Separator.Render(
			fmt.Sprintf(" ↓ %d more tasks ↓ ", len(cv.tasks)-endIdx),
		)
		b.WriteString(scrollInfo)
	}

	return b.String()
}

// renderEmptyState renders the empty state
func (cv *CompactView) renderEmptyState() string {
	emptyStyle := lipgloss.NewStyle().
		Foreground(cv.styles.Row.GetForeground()).
		Italic(true).
		Align(lipgloss.Center).
		Width(cv.width).
		Height(cv.height / 2)

	return emptyStyle.Render("No tasks to display\n\nPress 'c' to create a task or '/' to search")
}

// renderHeader renders the table header
func (cv *CompactView) renderHeader() string {
	// Calculate column widths
	widths := cv.calculateColumnWidths()

	cells := []string{
		cv.styles.HeaderCell.Width(widths.number).Render("#"),
		cv.styles.HeaderCell.Width(widths.id).Render("ID"),
		cv.styles.HeaderCell.Width(widths.title).Render("Title"),
		cv.styles.HeaderCell.Width(widths.status).Render("Status"),
		cv.styles.HeaderCell.Width(widths.priority).Render("Pri"),
		cv.styles.HeaderCell.Width(widths.type_).Render("Type"),
		cv.styles.HeaderCell.Width(widths.session).Render("Session"),
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

// renderSeparator renders the separator line
func (cv *CompactView) renderSeparator() string {
	return cv.styles.Separator.Render(strings.Repeat("─", cv.width))
}

// renderRow renders a single task row
func (cv *CompactView) renderRow(index int, task domain.Task) string {
	isActive := index == cv.cursor
	isSelected := cv.selected[task.ID]

	// Choose row style
	rowStyle := cv.styles.Row
	if isSelected {
		rowStyle = cv.styles.RowSelected
	} else if isActive {
		rowStyle = cv.styles.RowActive
	}

	// Calculate column widths
	widths := cv.calculateColumnWidths()

	// Build cells
	cells := []string{
		cv.renderNumberCell(index, isActive, isSelected, rowStyle, widths.number),
		cv.renderIDCell(task.ID, rowStyle, widths.id),
		cv.renderTitleCell(task.Title, rowStyle, widths.title),
		cv.renderStatusCell(task.Status, widths.status),
		cv.renderPriorityCell(task.Priority, widths.priority),
		cv.renderTypeCell(task.Type, widths.type_),
		cv.renderSessionCell(task.Session, widths.session),
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

// renderNumberCell renders the row number with indicators
func (cv *CompactView) renderNumberCell(index int, isActive, isSelected bool, rowStyle lipgloss.Style, width int) string {
	var indicator string
	if isActive && isSelected {
		indicator = cv.styles.Selected.Render("●▶")
	} else if isActive {
		indicator = cv.styles.Cursor.Render("▶ ")
	} else if isSelected {
		indicator = cv.styles.Selected.Render("● ")
	} else {
		indicator = "  "
	}

	number := fmt.Sprintf("%2d", index+1)
	content := indicator + number

	return rowStyle.Copy().Width(width).Render(content)
}

// renderIDCell renders the task ID
func (cv *CompactView) renderIDCell(id string, rowStyle lipgloss.Style, width int) string {
	return rowStyle.Copy().
		Width(width).
		Foreground(cv.styles.ColID.GetForeground()).
		Bold(true).
		Render(id)
}

// renderTitleCell renders the task title with truncation
func (cv *CompactView) renderTitleCell(title string, rowStyle lipgloss.Style, width int) string {
	truncated := truncateString(title, width)
	return rowStyle.Copy().Width(width).Render(truncated)
}

// renderStatusCell renders the status with color and abbreviation
func (cv *CompactView) renderStatusCell(status domain.Status, width int) string {
	var abbrev string
	var style lipgloss.Style

	switch status {
	case domain.StatusOpen:
		abbrev = "open"
		style = cv.styles.StatusOpen
	case domain.StatusInProgress:
		abbrev = "prog"
		style = cv.styles.StatusInProgress
	case domain.StatusBlocked:
		abbrev = "bloc"
		style = cv.styles.StatusBlocked
	case domain.StatusDone:
		abbrev = "done"
		style = cv.styles.StatusDone
	default:
		abbrev = "????"
		style = cv.styles.Row
	}

	return style.Width(width).Align(lipgloss.Center).Render(abbrev)
}

// renderPriorityCell renders the priority with color
func (cv *CompactView) renderPriorityCell(priority domain.Priority, width int) string {
	var style lipgloss.Style

	switch priority {
	case domain.P0:
		style = cv.styles.PriorityP0
	case domain.P1:
		style = cv.styles.PriorityP1
	case domain.P2:
		style = cv.styles.PriorityP2
	case domain.P3:
		style = cv.styles.PriorityP3
	case domain.P4:
		style = cv.styles.PriorityP4
	default:
		style = cv.styles.Row
	}

	return style.Width(width).Align(lipgloss.Center).Render(priority.String())
}

// renderTypeCell renders the task type
func (cv *CompactView) renderTypeCell(taskType domain.TaskType, width int) string {
	var style lipgloss.Style

	switch taskType {
	case domain.TypeEpic:
		style = cv.styles.TypeEpic
	case domain.TypeFeature:
		style = cv.styles.TypeFeature
	case domain.TypeBug:
		style = cv.styles.TypeBug
	case domain.TypeTask:
		style = cv.styles.TypeTask
	case domain.TypeChore:
		style = cv.styles.TypeChore
	default:
		style = cv.styles.Row
	}

	return style.Width(width).Align(lipgloss.Center).Render(taskType.Short())
}

// renderSessionCell renders the session state icon
func (cv *CompactView) renderSessionCell(session *domain.Session, width int) string {
	if session == nil {
		return cv.styles.ColSession.Width(width).Render(" ")
	}

	var style lipgloss.Style
	switch session.State {
	case domain.SessionBusy:
		style = cv.styles.StatusInProgress // Yellow
	case domain.SessionWaiting:
		style = cv.styles.StatusOpen // Blue
	case domain.SessionDone:
		style = cv.styles.StatusDone // Green
	case domain.SessionError:
		style = cv.styles.StatusBlocked // Red
	case domain.SessionPaused:
		style = cv.styles.PriorityP4 // Gray
	default:
		style = cv.styles.Row
	}

	return style.Width(width).Align(lipgloss.Center).Render(session.State.Icon())
}

// columnWidths holds the calculated column widths
type columnWidths struct {
	number   int
	id       int
	title    int
	status   int
	priority int
	type_    int
	session  int
}

// calculateColumnWidths calculates responsive column widths based on available space
func (cv *CompactView) calculateColumnWidths() columnWidths {
	// Fixed widths for most columns
	const (
		numberWidth   = 5
		idWidth       = 10
		statusWidth   = 7
		priorityWidth = 4
		typeWidth     = 5
		sessionWidth  = 8
	)

	fixedWidth := numberWidth + idWidth + statusWidth + priorityWidth + typeWidth + sessionWidth
	titleWidth := max(20, cv.width-fixedWidth)

	return columnWidths{
		number:   numberWidth,
		id:       idWidth,
		title:    titleWidth,
		status:   statusWidth,
		priority: priorityWidth,
		type_:    typeWidth,
		session:  sessionWidth,
	}
}

// calculateVisibleRows calculates how many rows can fit in the visible area
func (cv *CompactView) calculateVisibleRows() int {
	// Account for header (1 line) + separator (1 line) + status bar (handled outside)
	availableHeight := cv.height - 2
	if availableHeight < 1 {
		return 1
	}
	return availableHeight
}

// ensureCursorVisible adjusts scroll offset to keep cursor visible
func (cv *CompactView) ensureCursorVisible() {
	visibleRows := cv.calculateVisibleRows()

	// Cursor is above visible area
	if cv.cursor < cv.scrollOffset {
		cv.scrollOffset = cv.cursor
	}

	// Cursor is below visible area
	if cv.cursor >= cv.scrollOffset+visibleRows {
		cv.scrollOffset = cv.cursor - visibleRows + 1
	}

	// Clamp scroll offset
	maxOffset := max(0, len(cv.tasks)-visibleRows)
	if cv.scrollOffset > maxOffset {
		cv.scrollOffset = maxOffset
	}
	if cv.scrollOffset < 0 {
		cv.scrollOffset = 0
	}
}

// truncateString truncates a string to fit within the given width
// If truncated, adds "..." at the end
func truncateString(s string, width int) string {
	if width <= 3 {
		return strings.Repeat(".", min(width, 3))
	}

	// Account for potential wide characters, but for simplicity we'll use rune count
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}

	return string(runes[:width-3]) + "..."
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
