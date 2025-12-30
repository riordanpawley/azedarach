package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// SessionInfo represents information about an active session for orchestration view
type SessionInfo struct {
	BeadID       string
	TaskTitle    string
	State        domain.SessionState
	StartedAt    *time.Time
	Worktree     string
	RecentOutput string // Last few lines of output
}

// OrchestrationOverlay displays all active Claude sessions in a monitoring view
type OrchestrationOverlay struct {
	sessions []SessionInfo
	cursor   int
	width    int
	height   int
	styles   *Styles

	// Callbacks
	onAttach  func(beadID string) tea.Cmd
	onKill    func(beadID string) tea.Cmd
	onRefresh func() tea.Cmd
}

// NewOrchestrationOverlay creates a new orchestration overlay
func NewOrchestrationOverlay(
	sessions []SessionInfo,
	onAttach func(beadID string) tea.Cmd,
	onKill func(beadID string) tea.Cmd,
	onRefresh func() tea.Cmd,
) *OrchestrationOverlay {
	return &OrchestrationOverlay{
		sessions:  sessions,
		cursor:    0,
		width:     100,
		height:    30,
		styles:    New(),
		onAttach:  onAttach,
		onKill:    onKill,
		onRefresh: onRefresh,
	}
}

// Init initializes the overlay
func (o *OrchestrationOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (o *OrchestrationOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "O":
			return o, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			if o.cursor < len(o.sessions)-1 {
				o.cursor++
			}
			return o, nil

		case "k", "up":
			if o.cursor > 0 {
				o.cursor--
			}
			return o, nil

		case "g":
			// Go to top
			o.cursor = 0
			return o, nil

		case "G":
			// Go to bottom
			if len(o.sessions) > 0 {
				o.cursor = len(o.sessions) - 1
			}
			return o, nil

		case "enter", "a":
			// Attach to selected session
			if o.cursor >= 0 && o.cursor < len(o.sessions) {
				beadID := o.sessions[o.cursor].BeadID
				if o.onAttach != nil {
					return o, o.onAttach(beadID)
				}
			}
			return o, nil

		case "x":
			// Kill selected session
			if o.cursor >= 0 && o.cursor < len(o.sessions) {
				beadID := o.sessions[o.cursor].BeadID
				if o.onKill != nil {
					return o, o.onKill(beadID)
				}
			}
			return o, nil

		case "r":
			// Refresh
			if o.onRefresh != nil {
				return o, o.onRefresh()
			}
			return o, nil
		}
	}

	return o, nil
}

// View renders the overlay
func (o *OrchestrationOverlay) View() string {
	if len(o.sessions) == 0 {
		return o.renderEmptyState()
	}

	var b strings.Builder

	// Header with session count
	headerStyle := lipgloss.NewStyle().
		Foreground(styles.Text).
		Bold(true).
		Padding(0, 1)
	header := headerStyle.Render(fmt.Sprintf("Active Sessions: %d", len(o.sessions)))
	b.WriteString(header)
	b.WriteString("\n\n")

	// Render each session
	for i, session := range o.sessions {
		b.WriteString(o.renderSession(i, session))
		if i < len(o.sessions)-1 {
			b.WriteString("\n")
			// Separator line
			b.WriteString(o.styles.Separator.Render(strings.Repeat("─", o.width-4)))
			b.WriteString("\n")
		}
	}

	// Help text at bottom
	b.WriteString("\n")
	helpText := o.renderHelp()
	b.WriteString(helpText)

	return b.String()
}

// Title returns the overlay title
func (o *OrchestrationOverlay) Title() string {
	return "Session Orchestration"
}

// Size returns the overlay dimensions
func (o *OrchestrationOverlay) Size() (width, height int) {
	return o.width, o.height
}

// renderSession renders a single session entry
func (o *OrchestrationOverlay) renderSession(index int, session SessionInfo) string {
	isActive := index == o.cursor

	// Base style
	baseStyle := lipgloss.NewStyle().
		Foreground(styles.Text).
		Padding(0, 1)
	if isActive {
		baseStyle = baseStyle.Background(styles.Surface0)
	}

	var b strings.Builder

	// Line 1: Cursor indicator + Bead ID + State
	cursor := "  "
	if isActive {
		cursor = lipgloss.NewStyle().
			Foreground(styles.Blue).
			Bold(true).
			Render("▶ ")
	}

	stateIcon := session.State.Icon()
	stateStyle := o.getStateStyle(session.State)
	stateStr := stateStyle.Render(fmt.Sprintf(" %s %s ", stateIcon, session.State.String()))

	idStyle := lipgloss.NewStyle().
		Foreground(styles.Mauve).
		Bold(true)
	idStr := idStyle.Render(session.BeadID)

	line1 := baseStyle.Render(fmt.Sprintf("%s%s %s", cursor, idStr, stateStr))
	b.WriteString(line1)
	b.WriteString("\n")

	// Line 2: Task title
	titleStyle := lipgloss.NewStyle().
		Foreground(styles.Text).
		Padding(0, 1, 0, 3) // Indent to align with bead ID
	if isActive {
		titleStyle = titleStyle.Background(styles.Surface0)
	}

	title := session.TaskTitle
	if len(title) > o.width-10 {
		title = title[:o.width-13] + "..."
	}
	line2 := titleStyle.Render(title)
	b.WriteString(line2)
	b.WriteString("\n")

	// Line 3: Elapsed time + Worktree
	elapsedStr := "not started"
	if session.StartedAt != nil {
		elapsed := time.Since(*session.StartedAt)
		elapsedStr = formatElapsed(elapsed)
	}

	detailStyle := lipgloss.NewStyle().
		Foreground(styles.Overlay1).
		Padding(0, 1, 0, 3)
	if isActive {
		detailStyle = detailStyle.Background(styles.Surface0)
	}

	worktreeShort := session.Worktree
	if len(worktreeShort) > 40 {
		parts := strings.Split(worktreeShort, "/")
		if len(parts) > 2 {
			worktreeShort = ".../" + strings.Join(parts[len(parts)-2:], "/")
		}
	}

	line3 := detailStyle.Render(fmt.Sprintf("⏱ %s  📁 %s", elapsedStr, worktreeShort))
	b.WriteString(line3)
	b.WriteString("\n")

	// Line 4: Recent output preview (if available)
	if session.RecentOutput != "" {
		outputStyle := lipgloss.NewStyle().
			Foreground(styles.Overlay0).
			Italic(true).
			Padding(0, 1, 0, 3)
		if isActive {
			outputStyle = outputStyle.Background(styles.Surface0)
		}

		output := session.RecentOutput
		// Truncate and escape
		lines := strings.Split(output, "\n")
		preview := ""
		if len(lines) > 0 {
			preview = lines[len(lines)-1]
			if len(preview) > o.width-10 {
				preview = preview[:o.width-13] + "..."
			}
		}

		if preview != "" {
			line4 := outputStyle.Render(fmt.Sprintf("💬 %s", preview))
			b.WriteString(line4)
		}
	}

	return b.String()
}

// renderEmptyState renders the empty state when no sessions are active
func (o *OrchestrationOverlay) renderEmptyState() string {
	emptyStyle := lipgloss.NewStyle().
		Foreground(styles.Overlay1).
		Italic(true).
		Align(lipgloss.Center).
		Width(o.width - 4).
		Padding(4, 0)

	return emptyStyle.Render("No active sessions\n\nPress Space on a task to start a session")
}

// renderHelp renders the help text at the bottom
func (o *OrchestrationOverlay) renderHelp() string {
	helpStyle := lipgloss.NewStyle().
		Foreground(styles.Overlay1).
		Padding(1, 1)

	help := []string{
		"j/k: navigate",
		"enter/a: attach",
		"x: kill",
		"r: refresh",
		"esc: close",
	}

	return helpStyle.Render(strings.Join(help, " • "))
}

// getStateStyle returns the appropriate style for a session state
func (o *OrchestrationOverlay) getStateStyle(state domain.SessionState) lipgloss.Style {
	base := lipgloss.NewStyle().Bold(true).Padding(0, 1)

	switch state {
	case domain.SessionBusy:
		return base.Foreground(styles.Yellow).Background(styles.Surface1)
	case domain.SessionWaiting:
		return base.Foreground(styles.Blue).Background(styles.Surface1)
	case domain.SessionDone:
		return base.Foreground(styles.Green).Background(styles.Surface1)
	case domain.SessionError:
		return base.Foreground(styles.Red).Background(styles.Surface1)
	case domain.SessionPaused:
		return base.Foreground(styles.Overlay1).Background(styles.Surface1)
	default:
		return base.Foreground(styles.Text).Background(styles.Surface1)
	}
}

// formatElapsed formats a duration as HH:MM:SS
func formatElapsed(d time.Duration) string {
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
