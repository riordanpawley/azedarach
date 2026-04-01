package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// DetailPanel displays full task details with scrollable description
type DetailPanel struct {
	task           domain.Task
	relatedTasks   []domain.Task
	mutation       *TaskMutationProgress
	session        *domain.Session
	scrollY        int
	contentHeight  int
	viewHeight     int
	descViewHeight int
	wrapWidth      int
	styles         *Styles
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

// NewDetailPanel creates a new detail panel for the given task and optional session
func NewDetailPanel(task domain.Task, session *domain.Session) *DetailPanel {
	// Calculate contentHeight based on description
	contentHeight := 0
	if task.Description != "" {
		contentHeight = len(strings.Split(task.Description, "\n"))
	}

	return &DetailPanel{
		task:           task,
		session:        session,
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

	if d.mutation != nil {
		b.WriteString(labelStyle.Render("Mutation:"))
		b.WriteString("  ")
		b.WriteString(valueStyle.Render(d.formatMutationProgress()))
		b.WriteString("\n")
	}

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
	if total, done := d.childProgress(); total > 0 {
		b.WriteString(labelStyle.Render("Children:"))
		b.WriteString("  ")
		b.WriteString(valueStyle.Render(fmt.Sprintf("%d total (%d done)", total, done)))
		b.WriteString("\n")
	}

	if deps := d.renderDependencies(); deps != "" {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("Dependencies"))
		b.WriteString("\n")
		b.WriteString(deps)
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

	// Worktree/session runtime info if present
	if d.session != nil {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("Worktree"))
		b.WriteString("\n")

		b.WriteString(labelStyle.Render("State:"))
		b.WriteString("  ")
		b.WriteString(valueStyle.Render(fmt.Sprintf("%s %s", d.session.State.Icon(), string(d.session.State))))
		b.WriteString("\n")

		if d.hasGitStatusData() {
			b.WriteString(labelStyle.Render("Git:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.formatGitStatus()))
			b.WriteString("\n")
		}

		if d.session.StartedAt != nil {
			b.WriteString(labelStyle.Render("Created:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.formatTime(*d.session.StartedAt)))
			b.WriteString("\n")

			age := time.Since(*d.session.StartedAt)
			b.WriteString(labelStyle.Render("Age:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.formatDuration(age)))
			b.WriteString("\n")
		}

		if d.session.Worktree != "" {
			b.WriteString(labelStyle.Render("Path:"))
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

		wrapWidth := d.wrapWidth
		if wrapWidth < 10 {
			wrapWidth = 10
		}
		descLines := wrapDescriptionLines(d.task.Description, wrapWidth)
		d.contentHeight = len(descLines)
		reservedLines := lipgloss.Height(b.String())
		descViewport := max(1, d.viewHeight-reservedLines-2)
		d.descViewHeight = descViewport

		start := d.scrollY
		end := min(d.scrollY+d.descViewHeight, len(descLines))

		for i := start; i < end; i++ {
			b.WriteString(valueStyle.Render(descLines[i]))
			b.WriteString("\n")
		}

		// Scroll indicator if needed
		if d.maxScroll() > 0 {
			scrollInfo := d.styles.Footer.Render(
				fmt.Sprintf("[j/k or ctrl+u/d to scroll, g/G to jump] (line %d/%d)", d.scrollY+1, d.contentHeight),
			)
			b.WriteString("\n")
			b.WriteString(scrollInfo)
		}
	}

	return b.String()
}

func (d *DetailPanel) renderDependencies() string {
	outgoing := d.task.Dependencies
	incoming := d.incomingDependencies()
	if len(outgoing) == 0 && len(incoming) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(d.styles.MenuItem.Render("Outgoing"))
	b.WriteString("\n")
	if len(outgoing) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, dep := range outgoing {
			b.WriteString(fmt.Sprintf("- %s -> %s\n", dep.Type, dep.ID))
		}
	}

	b.WriteString(d.styles.MenuItem.Render("Incoming"))
	b.WriteString("\n")
	if len(incoming) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, dep := range incoming {
			b.WriteString(fmt.Sprintf("- %s <- %s\n", dep.Type, dep.ID))
		}
	}

	return strings.TrimSuffix(b.String(), "\n")
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

func (d *DetailPanel) hasGitStatusData() bool {
	if d.task.HasWorktree {
		return true
	}
	if d.session != nil && strings.TrimSpace(d.session.Worktree) != "" {
		return true
	}
	if d.task.HasUncommittedChanges {
		return true
	}
	return d.task.GitAheadCount > 0 ||
		d.task.GitBehindCount > 0 ||
		d.task.GitAdditions > 0 ||
		d.task.GitDeletions > 0
}

func (d *DetailPanel) formatGitStatus() string {
	status := "clean"
	if d.task.HasUncommittedChanges || d.task.GitAdditions > 0 || d.task.GitDeletions > 0 {
		status = "dirty"
	}

	details := make([]string, 0, 2)
	if d.task.GitAdditions > 0 || d.task.GitDeletions > 0 {
		details = append(details, fmt.Sprintf("+%d/-%d", d.task.GitAdditions, d.task.GitDeletions))
	}
	if d.task.GitAheadCount > 0 || d.task.GitBehindCount > 0 {
		details = append(details, fmt.Sprintf("up %d, down %d", d.task.GitAheadCount, d.task.GitBehindCount))
	}

	if len(details) == 0 {
		return status
	}
	return fmt.Sprintf("%s (%s)", status, strings.Join(details, "; "))
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
