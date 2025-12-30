package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// DetailPanel displays full task details with scrollable description
type DetailPanel struct {
	task          domain.Task
	session       *domain.Session
	scrollY       int
	contentHeight int
	viewHeight    int
	styles        *Styles
}

// NewDetailPanel creates a new detail panel for the given task and optional session
func NewDetailPanel(task domain.Task, session *domain.Session) *DetailPanel {
	// Calculate contentHeight based on description
	contentHeight := 0
	if task.Description != "" {
		contentHeight = len(strings.Split(task.Description, "\n"))
	}

	return &DetailPanel{
		task:          task,
		session:       session,
		scrollY:       0,
		contentHeight: contentHeight,
		viewHeight:    20, // Default, will be updated in Size()
		styles:        New(),
	}
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
	var b strings.Builder

	// Section style for headers
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(12).
		Align(lipgloss.Right)

	valueStyle := d.styles.MenuItem

	// Task ID and Title
	b.WriteString(headerStyle.Render(fmt.Sprintf("[%s] %s", d.task.ID, d.task.Title)))
	b.WriteString("\n\n")

	// Status, Priority, Type
	b.WriteString(labelStyle.Render("Status:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(d.formatStatus(d.task.Status)))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Priority:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(d.task.Priority.String()))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Type:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(string(d.task.Type)))
	b.WriteString("\n")

	// Parent ID if present
	if d.task.ParentID != nil {
		b.WriteString(labelStyle.Render("Parent:"))
		b.WriteString("  ")
		b.WriteString(valueStyle.Render(*d.task.ParentID))
		b.WriteString("\n")
	}

	// Timestamps
	b.WriteString(labelStyle.Render("Created:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(d.formatTime(d.task.CreatedAt)))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Updated:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(d.formatTime(d.task.UpdatedAt)))
	b.WriteString("\n")

	// Session info if present
	if d.session != nil {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("Session"))
		b.WriteString("\n")

		b.WriteString(labelStyle.Render("State:"))
		b.WriteString("  ")
		b.WriteString(valueStyle.Render(fmt.Sprintf("%s %s", d.session.State.Icon(), string(d.session.State))))
		b.WriteString("\n")

		if d.session.StartedAt != nil {
			b.WriteString(labelStyle.Render("Started:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.formatTime(*d.session.StartedAt)))
			b.WriteString("\n")

			// Calculate elapsed time
			elapsed := time.Since(*d.session.StartedAt)
			b.WriteString(labelStyle.Render("Elapsed:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.formatDuration(elapsed)))
			b.WriteString("\n")
		}

		if d.session.Worktree != "" {
			b.WriteString(labelStyle.Render("Worktree:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.session.Worktree))
			b.WriteString("\n")
		}

		if d.session.DevServer != nil && d.session.DevServer.Running {
			b.WriteString(labelStyle.Render("Dev Server:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(fmt.Sprintf(":%d (%s)", d.session.DevServer.Port, d.session.DevServer.Command)))
			b.WriteString("\n")
		}
	}

	// Description section with scrolling
	if d.task.Description != "" {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("Description"))
		b.WriteString("\n")

		// Split description into lines and apply scroll
		descLines := strings.Split(d.task.Description, "\n")
		d.contentHeight = len(descLines)

		start := d.scrollY
		end := min(d.scrollY+d.viewHeight, len(descLines))

		for i := start; i < end; i++ {
			b.WriteString(valueStyle.Render(descLines[i]))
			b.WriteString("\n")
		}

		// Scroll indicator if needed
		if d.maxScroll() > 0 {
			scrollInfo := d.styles.Footer.Render(
				fmt.Sprintf("[j/k to scroll, g/G to jump] (line %d/%d)", d.scrollY+1, d.contentHeight),
			)
			b.WriteString("\n")
			b.WriteString(scrollInfo)
		}
	}

	return b.String()
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

// maxScroll returns the maximum scroll position
func (d *DetailPanel) maxScroll() int {
	return max(0, d.contentHeight-d.viewHeight)
}
