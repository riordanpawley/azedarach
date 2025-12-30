package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// MergeSelectOverlay allows selecting a merge target task
type MergeSelectOverlay struct {
	sourceTask domain.Task
	candidates []domain.Task
	cursor     int
	styles     *Styles
}

// MergeTargetSelectedMsg is sent when a merge target is selected
type MergeTargetSelectedMsg struct {
	SourceID string
	TargetID string
}

// NewMergeSelectOverlay creates a new merge target selection overlay
func NewMergeSelectOverlay(sourceTask domain.Task, candidates []domain.Task) *MergeSelectOverlay {
	return &MergeSelectOverlay{
		sourceTask: sourceTask,
		candidates: candidates,
		cursor:     0,
		styles:     New(),
	}
}

// Init initializes the overlay
func (m *MergeSelectOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *MergeSelectOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			// Cancel merge selection
			return m, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			if m.cursor < len(m.candidates)-1 {
				m.cursor++
			}
			return m, nil

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "enter":
			// Select current candidate
			if m.cursor >= 0 && m.cursor < len(m.candidates) {
				target := m.candidates[m.cursor]
				return m, func() tea.Msg {
					return SelectionMsg{
						Key: "merge",
						Value: MergeTargetSelectedMsg{
							SourceID: m.sourceTask.ID,
							TargetID: target.ID,
						},
					}
				}
			}
			return m, nil
		}
	}

	return m, nil
}

// View renders the overlay
func (m *MergeSelectOverlay) View() string {
	var b strings.Builder

	// Source task header
	header := m.styles.MenuItem.Bold(true).Render("Merge from:")
	b.WriteString(header)
	b.WriteString("\n")

	sourceInfo := m.formatTask(m.sourceTask, false)
	b.WriteString("  " + sourceInfo)
	b.WriteString("\n\n")

	// Target selection
	targetHeader := m.styles.MenuItem.Bold(true).Render("Select merge target:")
	b.WriteString(targetHeader)
	b.WriteString("\n")

	if len(m.candidates) == 0 {
		noTasks := m.styles.MenuItemDisabled.Render("  No eligible tasks found")
		b.WriteString(noTasks)
		b.WriteString("\n")
	} else {
		for i, task := range m.candidates {
			selected := i == m.cursor
			taskInfo := m.formatTask(task, selected)
			b.WriteString(taskInfo)
			b.WriteString("\n")
		}
	}

	// Footer
	b.WriteString("\n")
	footer := m.styles.Footer.Render("j/k: Navigate • Enter: Select • Esc: Cancel")
	b.WriteString(footer)

	return b.String()
}

// formatTask formats a task for display
func (m *MergeSelectOverlay) formatTask(task domain.Task, selected bool) string {
	var parts []string

	// Cursor indicator
	if selected {
		parts = append(parts, m.styles.MenuItemActive.Render("▸"))
	} else {
		parts = append(parts, " ")
	}

	// Task type and ID
	typeStyle := m.styles.MenuKey
	if selected {
		typeStyle = m.styles.MenuItemActive
	}
	parts = append(parts, typeStyle.Render("["+task.Type.Short()+"]"))

	// Task ID
	idStyle := m.styles.MenuItem
	if selected {
		idStyle = m.styles.MenuItemActive
	}
	parts = append(parts, idStyle.Render(task.ID))

	// Task title
	titleStyle := m.styles.MenuItem
	if selected {
		titleStyle = m.styles.MenuItemActive.Bold(true)
	}
	parts = append(parts, titleStyle.Render(task.Title))

	// Status badge
	statusBadge := m.formatStatus(task.Status, selected)
	parts = append(parts, statusBadge)

	return strings.Join(parts, " ")
}

// formatStatus formats a status badge
func (m *MergeSelectOverlay) formatStatus(status domain.Status, selected bool) string {
	var color = m.styles.MenuItem.GetForeground()

	switch status {
	case domain.StatusOpen:
		color = m.styles.Footer.GetForeground()
	case domain.StatusInProgress:
		color = m.styles.MenuKey.GetForeground()
	case domain.StatusBlocked:
		color = m.styles.MenuItemDisabled.GetForeground()
	case domain.StatusDone:
		color = m.styles.MenuItemActive.GetForeground()
	}

	style := m.styles.MenuItem.Foreground(color)
	if selected {
		style = style.Bold(true)
	}

	return style.Render("(" + string(status) + ")")
}

// Title returns the overlay title
func (m *MergeSelectOverlay) Title() string {
	return "Select Merge Target"
}

// Size returns the overlay dimensions
func (m *MergeSelectOverlay) Size() (width, height int) {
	// Width: enough for full task info
	// Height: header + source + candidates + footer + padding
	candidateLines := len(m.candidates)
	if candidateLines > 15 {
		candidateLines = 15 // Cap visible candidates
	}
	return 80, 6 + candidateLines // header(2) + source(2) + target header(1) + candidates + footer(2)
}
