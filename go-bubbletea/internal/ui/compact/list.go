package compact

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// ListView represents a table-based list view for tasks
type ListView struct {
	tasks    []domain.Task
	cursor   int
	selected map[string]bool
	styles   *Styles
	width    int
	height   int
}

// NewListView creates a new ListView with the given tasks and dimensions
func NewListView(tasks []domain.Task, width, height int) *ListView {
	return &ListView{
		tasks:    tasks,
		cursor:   0,
		selected: make(map[string]bool),
		styles:   NewStyles(),
		width:    width,
		height:   height,
	}
}

// SetCursor sets the cursor position
func (lv *ListView) SetCursor(index int) {
	if index < 0 {
		lv.cursor = 0
	} else if index >= len(lv.tasks) {
		lv.cursor = max(0, len(lv.tasks)-1)
	} else {
		lv.cursor = index
	}
}

// SetSelected sets the selected tasks map
func (lv *ListView) SetSelected(selected map[string]bool) {
	lv.selected = selected
}

// Render renders the full table
func (lv *ListView) Render() string {
	if len(lv.tasks) == 0 {
		return lv.styles.Row.Render("No tasks to display")
	}

	var b strings.Builder

	// Render header
	b.WriteString(lv.renderHeader())
	b.WriteString("\n")
	b.WriteString(lv.renderSeparator())
	b.WriteString("\n")

	// Render rows
	for i, task := range lv.tasks {
		b.WriteString(lv.renderRow(i, task))
		if i < len(lv.tasks)-1 {
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderHeader renders the table header
func (lv *ListView) renderHeader() string {
	// Calculate title width (remaining width after fixed columns)
	// # (5) + ID (10) + Status (7) + Pri (4) + Session (8) + padding/separators (10)
	fixedWidth := 44
	titleWidth := max(10, lv.width-fixedWidth)

	cells := []string{
		lv.styles.HeaderCell.Width(5).Render("#"),
		lv.styles.HeaderCell.Width(10).Render("ID"),
		lv.styles.HeaderCell.Width(titleWidth).Render("Title"),
		lv.styles.HeaderCell.Width(7).Render("Status"),
		lv.styles.HeaderCell.Width(4).Render("Pri"),
		lv.styles.HeaderCell.Width(8).Render("Session"),
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

// renderSeparator renders the separator line
func (lv *ListView) renderSeparator() string {
	return lv.styles.Separator.Render(strings.Repeat("─", lv.width))
}

// renderRow renders a single task row
func (lv *ListView) renderRow(index int, task domain.Task) string {
	isActive := index == lv.cursor
	isSelected := lv.selected[task.ID]

	// Choose row style
	rowStyle := lv.styles.Row
	if isSelected {
		rowStyle = lv.styles.RowSelected
	} else if isActive {
		rowStyle = lv.styles.RowActive
	}

	// Calculate title width (must match header)
	fixedWidth := 44
	titleWidth := max(10, lv.width-fixedWidth)

	// Build cells
	cells := []string{
		lv.renderNumberCell(index, isActive, isSelected, rowStyle),
		lv.renderIDCell(task.ID, rowStyle),
		lv.renderTitleCell(task.Title, titleWidth, rowStyle),
		lv.renderStatusCell(task.Status),
		lv.renderPriorityCell(task.Priority),
		lv.renderSessionCell(task.Session),
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

// renderNumberCell renders the row number with indicators
func (lv *ListView) renderNumberCell(index int, isActive, isSelected bool, rowStyle lipgloss.Style) string {
	var indicator string
	if isActive && isSelected {
		indicator = lv.styles.Selected.Render("●▶")
	} else if isActive {
		indicator = lv.styles.Cursor.Render("▶ ")
	} else if isSelected {
		indicator = lv.styles.Selected.Render("● ")
	} else {
		indicator = "  "
	}

	number := fmt.Sprintf("%2d", index+1)
	content := indicator + number

	return rowStyle.Copy().Width(5).Render(content)
}

// renderIDCell renders the task ID
func (lv *ListView) renderIDCell(id string, rowStyle lipgloss.Style) string {
	return rowStyle.Copy().
		Width(10).
		Foreground(lv.styles.ColID.GetForeground()).
		Bold(true).
		Render(id)
}

// renderTitleCell renders the task title with truncation
func (lv *ListView) renderTitleCell(title string, width int, rowStyle lipgloss.Style) string {
	truncated := truncateString(title, width)
	return rowStyle.Copy().Width(width).Render(truncated)
}

// renderStatusCell renders the status with color and abbreviation
func (lv *ListView) renderStatusCell(status domain.Status) string {
	var abbrev string
	var style lipgloss.Style

	switch status {
	case domain.StatusOpen:
		abbrev = "open"
		style = lv.styles.StatusOpen
	case domain.StatusInProgress:
		abbrev = "prog"
		style = lv.styles.StatusInProgress
	case domain.StatusBlocked:
		abbrev = "bloc"
		style = lv.styles.StatusBlocked
	case domain.StatusDone:
		abbrev = "done"
		style = lv.styles.StatusDone
	default:
		abbrev = "????"
		style = lv.styles.Row
	}

	return style.Width(7).Align(lipgloss.Center).Render(abbrev)
}

// renderPriorityCell renders the priority with color
func (lv *ListView) renderPriorityCell(priority domain.Priority) string {
	var style lipgloss.Style

	switch priority {
	case domain.P0:
		style = lv.styles.PriorityP0
	case domain.P1:
		style = lv.styles.PriorityP1
	case domain.P2:
		style = lv.styles.PriorityP2
	case domain.P3:
		style = lv.styles.PriorityP3
	case domain.P4:
		style = lv.styles.PriorityP4
	default:
		style = lv.styles.Row
	}

	return style.Width(4).Align(lipgloss.Center).Render(priority.String())
}

// renderSessionCell renders the session state icon
func (lv *ListView) renderSessionCell(session *domain.Session) string {
	if session == nil {
		return lv.styles.ColSession.Width(8).Render(" ")
	}

	var style lipgloss.Style
	switch session.State {
	case domain.SessionBusy:
		style = lv.styles.StatusInProgress // Yellow
	case domain.SessionWaiting:
		style = lv.styles.StatusOpen // Blue
	case domain.SessionDone:
		style = lv.styles.StatusDone // Green
	case domain.SessionError:
		style = lv.styles.StatusBlocked // Red
	case domain.SessionPaused:
		style = lv.styles.PriorityP4 // Gray
	default:
		style = lv.styles.Row
	}

	return style.Width(8).Align(lipgloss.Center).Render(session.State.Icon())
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
