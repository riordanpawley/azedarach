package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// SessionInfo represents information about an active session for orchestration view
type SessionInfo struct {
	IssueID               string
	TaskTitle             string
	IssueStatus           domain.Status
	State                 domain.SessionState
	StartedAt             *time.Time
	Worktree              string
	HasTmuxSession        bool
	HasWorktree           bool
	GitAheadCount         int
	GitBehindCount        int
	HasUncommittedChanges bool
	HasConflicts          bool
	GitAdditions          int
	GitDeletions          int
	RecentOutput          string // Last few lines of output
}

// OrchestrationOverlay displays all active Claude sessions in a monitoring view
type OrchestrationOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	sessions []SessionInfo
	cursor   int
	styles   *Styles

	// Callbacks
	onAttach  func(issueID string) tea.Cmd
	onKill    func(issueID string) tea.Cmd
	onRefresh func() tea.Cmd
}

// NewOrchestrationOverlay creates a new orchestration overlay
func NewOrchestrationOverlay(
	sessions []SessionInfo,
	onAttach func(issueID string) tea.Cmd,
	onKill func(issueID string) tea.Cmd,
	onRefresh func() tea.Cmd,
) *OrchestrationOverlay {
	return &OrchestrationOverlay{
		sessions:  sessions,
		cursor:    0,
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
	case tea.WindowSizeMsg:
		o.ApplyWindowSize(msg)
		return o, nil
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
				issueID := o.sessions[o.cursor].IssueID
				if o.onAttach != nil {
					return o, o.onAttach(issueID)
				}
			}
			return o, nil

		case "x":
			// Kill selected session
			if o.cursor >= 0 && o.cursor < len(o.sessions) {
				issueID := o.sessions[o.cursor].IssueID
				if o.onKill != nil {
					return o, o.onKill(issueID)
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
	width, height := o.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            o.styles,
		width:             width,
		height:            height,
		title:             o.Title(),
		rightSectionTitle: "Keys",
		breakpoint:        58,
		gap:               3,
		minLeft:           30,
		minRight:          18,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			if len(o.sessions) == 0 {
				return o.renderEmptyState(width)
			}
			return o.renderSessions(width)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(o.styles, []keybinds.Binding{
				{Key: "j/k", Description: "navigate"},
				{Key: "Enter/a", Description: "switch"},
				{Key: "x", Description: "kill"},
				{Key: "r", Description: "refresh"},
				{Key: "Esc", Description: "close"},
			})
		},
	})
}

// Title returns the overlay title
func (o *OrchestrationOverlay) Title() string {
	return "Tmux Sessions"
}

// Size returns the overlay dimensions
func (o *OrchestrationOverlay) Size() (width, height int) {
	return o.Clamp(100, o.viewHeight())
}

func (o *OrchestrationOverlay) viewHeight() int {
	if len(o.sessions) == 0 {
		return 16
	}
	return max(16, len(o.sessions)*6+8)
}

// renderSession renders a single session entry
func (o *OrchestrationOverlay) renderSession(index int, session SessionInfo, width int) string {
	isActive := index == o.cursor

	// Base style
	baseStyle := lipgloss.NewStyle().
		Foreground(styles.Text).
		Padding(0, 1)
	if isActive {
		baseStyle = baseStyle.Background(styles.Surface0)
	}

	var b strings.Builder

	// Line 1: Cursor indicator + Issue ID + State
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
	idStr := idStyle.Render(session.IssueID)

	statusStyle := lipgloss.NewStyle().
		Foreground(styles.Overlay1).
		Bold(true)
	statusStr := statusStyle.Render(fmt.Sprintf(" %s ", session.IssueStatus.String()))

	line1 := baseStyle.Render(fmt.Sprintf("%s%s %s %s", cursor, idStr, statusStr, stateStr))
	b.WriteString(line1)
	b.WriteString("\n")

	// Line 2: Task title
	titleStyle := lipgloss.NewStyle().
		Foreground(styles.Text).
		Padding(0, 1, 0, 3) // Indent to align with issue ID
	if isActive {
		titleStyle = titleStyle.Background(styles.Surface0)
	}

	title := session.TaskTitle
	if limit := max(0, width-10); len(title) > limit {
		if limit > 3 {
			title = title[:limit-3] + "..."
		} else {
			title = title[:limit]
		}
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

	// Line 4: tmux + git state
	runtimeStyle := lipgloss.NewStyle().
		Foreground(styles.Overlay1).
		Padding(0, 1, 0, 3)
	if isActive {
		runtimeStyle = runtimeStyle.Background(styles.Surface0)
	}
	line4 := runtimeStyle.Render(fmt.Sprintf("tmux %s  worktree %s  git %s",
		formatBoolSignal(session.HasTmuxSession),
		formatBoolSignal(session.HasWorktree || strings.TrimSpace(session.Worktree) != ""),
		formatSessionGitStatus(session),
	))
	b.WriteString(line4)

	// Line 5: Recent output preview (if available)
	if session.RecentOutput != "" {
		b.WriteString("\n")
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
			if limit := max(0, width-10); len(preview) > limit {
				if limit > 3 {
					preview = preview[:limit-3] + "..."
				} else {
					preview = preview[:limit]
				}
			}
		}

		if preview != "" {
			line5 := outputStyle.Render(fmt.Sprintf("💬 %s", preview))
			b.WriteString(line5)
		}
	}

	return b.String()
}

// renderSessions renders the session list.
func (o *OrchestrationOverlay) renderSessions(width int) string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(styles.Text).
		Bold(true).
		Padding(0, 1)
	header := headerStyle.Render(fmt.Sprintf("Azedarach tmux sessions: %d", len(o.sessions)))
	b.WriteString(header)
	b.WriteString("\n\n")

	for i, session := range o.sessions {
		b.WriteString(o.renderSession(i, session, width))
		if i < len(o.sessions)-1 {
			b.WriteString("\n")
			b.WriteString(o.styles.Separator.Render(strings.Repeat("─", max(6, width-4))))
			b.WriteString("\n")
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderEmptyState renders the empty state when no sessions are active
func (o *OrchestrationOverlay) renderEmptyState(width int) string {
	emptyStyle := lipgloss.NewStyle().
		Foreground(styles.Overlay1).
		Italic(true).
		Align(lipgloss.Center).
		Width(max(1, width-4)).
		Padding(4, 0)

	return emptyStyle.Render("No tmux sessions\n\nStart a session from a task workspace")
}

func formatBoolSignal(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func formatSessionGitStatus(session SessionInfo) string {
	if session.HasConflicts {
		return "conflicts"
	}

	hasTelemetry := session.HasUncommittedChanges ||
		session.GitAheadCount > 0 ||
		session.GitBehindCount > 0 ||
		session.GitAdditions > 0 ||
		session.GitDeletions > 0
	if !hasTelemetry {
		if session.HasWorktree || strings.TrimSpace(session.Worktree) != "" {
			return "clean"
		}
		return "unknown"
	}

	status := "clean"
	if session.HasUncommittedChanges {
		status = "dirty"
	}

	parts := make([]string, 0, 2)
	if session.GitAdditions > 0 || session.GitDeletions > 0 {
		parts = append(parts, fmt.Sprintf("+%d/-%d", session.GitAdditions, session.GitDeletions))
	}
	if divergence := formatSessionAheadBehind(session.GitAheadCount, session.GitBehindCount); divergence != "" {
		parts = append(parts, divergence)
	}
	if len(parts) == 0 {
		return status
	}
	return fmt.Sprintf("%s (%s)", status, strings.Join(parts, "; "))
}

func formatSessionAheadBehind(ahead, behind int) string {
	if ahead > 0 && behind > 0 {
		return fmt.Sprintf("↑%d/↓%d", ahead, behind)
	}
	if ahead > 0 {
		return fmt.Sprintf("↑%d", ahead)
	}
	if behind > 0 {
		return fmt.Sprintf("↓%d", behind)
	}
	return ""
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
