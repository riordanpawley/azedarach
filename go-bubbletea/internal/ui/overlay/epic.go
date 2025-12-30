package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// EpicDrillDown is an overlay that shows epic details with child tasks
type EpicDrillDown struct {
	epic     domain.Task
	children []domain.Task
	cursor   int
	styles   *Styles
}

// NewEpicDrillDown creates a new epic drill-down overlay
func NewEpicDrillDown(epic domain.Task, children []domain.Task) *EpicDrillDown {
	return &EpicDrillDown{
		epic:     epic,
		children: children,
		cursor:   0,
		styles:   New(),
	}
}

// Init initializes the epic drill-down
func (e *EpicDrillDown) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (e *EpicDrillDown) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc":
			return e, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			if e.cursor < len(e.children)-1 {
				e.cursor++
			}
			return e, nil

		case "k", "up":
			if e.cursor > 0 {
				e.cursor--
			}
			return e, nil

		case "enter":
			if e.cursor >= 0 && e.cursor < len(e.children) {
				child := e.children[e.cursor]
				return e, func() tea.Msg {
					return SelectionMsg{
						Key:   "select_child",
						Value: child.ID,
					}
				}
			}
			return e, nil
		}
	}

	return e, nil
}

// View renders the epic drill-down
func (e *EpicDrillDown) View() string {
	var b strings.Builder

	// Epic header
	epicTitle := e.styles.Title.Render(e.epic.Title)
	b.WriteString(epicTitle)
	b.WriteString("\n")

	// Progress bar
	progressBar := e.renderProgressBar()
	b.WriteString(progressBar)
	b.WriteString("\n\n")

	// Child tasks
	if len(e.children) == 0 {
		noChildren := e.styles.MenuItem.Foreground(styles.Overlay0).Render("No child tasks")
		b.WriteString(noChildren)
		b.WriteString("\n")
	} else {
		for i, child := range e.children {
			line := e.renderChild(child, i == e.cursor)
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	// Footer
	b.WriteString("\n")
	footer := e.styles.Footer.Render("Enter: select • j/k: navigate • q/Esc: close")
	b.WriteString(footer)

	return b.String()
}

// Title returns the overlay title
func (e *EpicDrillDown) Title() string {
	return "Epic: " + e.epic.ID
}

// Size returns the overlay dimensions
func (e *EpicDrillDown) Size() (width, height int) {
	// Width: wide enough for task cards
	// Height: header + progress + children + footer + padding
	height = 6 + len(e.children)
	if len(e.children) == 0 {
		height = 8 // Minimum height for "no children" message
	}
	return 60, height
}

// renderProgressBar creates a visual progress bar for the epic
func (e *EpicDrillDown) renderProgressBar() string {
	total := len(e.children)
	if total == 0 {
		return e.styles.Footer.Render("0/0 (0%)")
	}

	closed := 0
	for _, child := range e.children {
		if child.Status == domain.StatusDone {
			closed++
		}
	}

	percentage := float64(closed) / float64(total) * 100

	// Create progress bar with 40 characters
	barWidth := 40
	filled := int(float64(barWidth) * float64(closed) / float64(total))

	var bar strings.Builder
	bar.WriteString("│")
	for i := 0; i < barWidth; i++ {
		if i < filled {
			bar.WriteString("█")
		} else {
			bar.WriteString("░")
		}
	}
	bar.WriteString("│")

	// Stats
	stats := fmt.Sprintf(" %d/%d (%.0f%%)", closed, total, percentage)

	progressStyle := lipgloss.NewStyle().Foreground(styles.Green)
	if percentage < 33 {
		progressStyle = progressStyle.Foreground(styles.Red)
	} else if percentage < 66 {
		progressStyle = progressStyle.Foreground(styles.Yellow)
	}

	return progressStyle.Render(bar.String()) + e.styles.Footer.Render(stats)
}

// renderChild renders a single child task
func (e *EpicDrillDown) renderChild(child domain.Task, active bool) string {
	var b strings.Builder

	// Status badge
	statusBadge := e.renderStatusBadge(child.Status)
	b.WriteString(statusBadge)
	b.WriteString(" ")

	// Task ID
	idStyle := lipgloss.NewStyle().Foreground(styles.Overlay1).Bold(true)
	b.WriteString(idStyle.Render(child.ID))
	b.WriteString(" ")

	// Task title
	titleStyle := e.styles.MenuItem
	if active {
		titleStyle = e.styles.MenuItemActive
	}
	b.WriteString(titleStyle.Render(child.Title))

	// Priority badge
	b.WriteString(" ")
	priorityStyle := lipgloss.NewStyle().
		Foreground(styles.Base).
		Background(styles.PriorityColors[min(int(child.Priority), len(styles.PriorityColors)-1)]).
		Padding(0, 1)
	b.WriteString(priorityStyle.Render(child.Priority.String()))

	// Session indicator
	if child.Session != nil {
		b.WriteString(" ")
		sessionStyle := lipgloss.NewStyle().Foreground(styles.Blue)
		b.WriteString(sessionStyle.Render(child.Session.State.Icon()))
	}

	return b.String()
}

// renderStatusBadge renders a status badge with color
func (e *EpicDrillDown) renderStatusBadge(status domain.Status) string {
	var icon string
	var color lipgloss.Color

	switch status {
	case domain.StatusOpen:
		icon = "○"
		color = styles.Blue
	case domain.StatusInProgress:
		icon = "◐"
		color = styles.Yellow
	case domain.StatusBlocked:
		icon = "◯"
		color = styles.Red
	case domain.StatusDone:
		icon = "●"
		color = styles.Green
	default:
		icon = "?"
		color = styles.Overlay0
	}

	style := lipgloss.NewStyle().Foreground(color).Bold(true)
	return style.Render(icon)
}
