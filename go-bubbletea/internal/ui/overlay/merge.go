package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// MergeTarget represents a target that can be merged into
type MergeTarget struct {
	ID          string        // "main" or task ID
	Label       string        // Display label
	IsMain      bool          // Whether this is the main branch
	Status      domain.Status // Task status (if not main)
	HasWorktree bool          // Whether this target has a worktree
}

// MergeSelectOverlay allows selecting a merge target task
type MergeSelectOverlay struct {
	source     *domain.Task  // The bead being merged FROM
	candidates []MergeTarget // Beads that can be merged INTO (including main)
	cursor     int
	onMerge    func(targetID string) tea.Cmd
	onCancel   func() tea.Cmd
	overlayStyles     *Styles
}

// MergeTargetSelectedMsg is sent when a merge target is selected
type MergeTargetSelectedMsg struct {
	SourceID string
	TargetID string
}

// NewMergeSelectOverlay creates a new merge target selection overlay
func NewMergeSelectOverlay(
	source *domain.Task,
	candidates []MergeTarget,
	onMerge func(targetID string) tea.Cmd,
	onCancel func() tea.Cmd,
) *MergeSelectOverlay {
	return &MergeSelectOverlay{
		source:     source,
		candidates: candidates,
		cursor:     0,
		onMerge:    onMerge,
		onCancel:   onCancel,
		overlayStyles:     New(),
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
			if m.onCancel != nil {
				return m, m.onCancel()
			}
			return m, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			m.moveCursorDown()
			return m, nil

		case "k", "up":
			m.moveCursorUp()
			return m, nil

		case "enter":
			return m, m.selectCurrent()
		}
	}

	return m, nil
}

// View renders the overlay
func (m *MergeSelectOverlay) View() string {
	var b strings.Builder

	// Header showing source bead
	header := fmt.Sprintf("Merge %s into:", m.overlayStyles.MenuKey.Render(m.source.ID))
	b.WriteString(m.overlayStyles.Title.Render(header))
	b.WriteString("\n\n")

	// List of candidates
	if len(m.candidates) == 0 {
		noTasks := m.overlayStyles.MenuItemDisabled.Render("No eligible merge targets found")
		b.WriteString("  " + noTasks)
		b.WriteString("\n")
	} else {
		for i, candidate := range m.candidates {
			line := m.renderCandidate(candidate, i == m.cursor)
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	// Footer with help text
	b.WriteString("\n")
	footer := "j/k: navigate • Enter: select • Esc: cancel"
	b.WriteString(m.overlayStyles.Footer.Render(footer))

	return b.String()
}

// renderCandidate renders a single merge target candidate
func (m *MergeSelectOverlay) renderCandidate(target MergeTarget, isActive bool) string {
	var parts []string

	// Cursor indicator
	cursor := "  "
	if isActive {
		cursor = lipgloss.NewStyle().Foreground(styles.Blue).Render("▸ ")
	}
	parts = append(parts, cursor)

	// Main branch gets special rendering
	if target.IsMain {
		label := "main"
		if isActive {
			label = m.overlayStyles.MenuItemActive.Render(label)
		} else {
			label = lipgloss.NewStyle().
				Foreground(styles.Green).
				Bold(true).
				Render(label)
		}
		parts = append(parts, label)
		parts = append(parts, m.overlayStyles.MenuItemDisabled.Render("(main branch)"))
		return strings.Join(parts, "")
	}

	// Task ID
	idStyle := m.overlayStyles.MenuKey
	if isActive {
		idStyle = lipgloss.NewStyle().Foreground(styles.Yellow).Bold(true)
	}
	parts = append(parts, idStyle.Render(target.ID))

	// Status indicator with color
	statusColor := styles.StatusColors[target.Status.String()]
	statusStyle := lipgloss.NewStyle().Foreground(statusColor)
	statusText := fmt.Sprintf("[%s]", target.Status)
	parts = append(parts, statusStyle.Render(statusText))

	// Label (task title)
	labelStyle := m.overlayStyles.MenuItem
	if isActive {
		labelStyle = m.overlayStyles.MenuItemActive
	}
	parts = append(parts, labelStyle.Render(target.Label))

	// Worktree indicator
	if !target.HasWorktree {
		parts = append(parts, m.overlayStyles.MenuItemDisabled.Render("(no worktree)"))
	}

	return strings.Join(parts, " ")
}

// moveCursorDown moves the cursor to the next candidate
func (m *MergeSelectOverlay) moveCursorDown() {
	if len(m.candidates) == 0 {
		return
	}
	m.cursor = (m.cursor + 1) % len(m.candidates)
}

// moveCursorUp moves the cursor to the previous candidate
func (m *MergeSelectOverlay) moveCursorUp() {
	if len(m.candidates) == 0 {
		return
	}
	m.cursor = (m.cursor - 1 + len(m.candidates)) % len(m.candidates)
}

// selectCurrent selects the current candidate
func (m *MergeSelectOverlay) selectCurrent() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.candidates) {
		return nil
	}

	target := m.candidates[m.cursor]
	if m.onMerge != nil {
		return m.onMerge(target.ID)
	}

	return func() tea.Msg {
		return SelectionMsg{
			Key: "merge",
			Value: MergeTargetSelectedMsg{
				SourceID: m.source.ID,
				TargetID: target.ID,
			},
		}
	}
}

// Title returns the overlay title
func (m *MergeSelectOverlay) Title() string {
	return "Select Merge Target"
}

// Size returns the overlay dimensions
func (m *MergeSelectOverlay) Size() (width, height int) {
	// Width: enough for the longest line
	// Height: header + candidates + footer + padding
	candidateLines := len(m.candidates)
	if candidateLines == 0 {
		candidateLines = 1 // "No eligible merge targets" message
	}
	if candidateLines > 15 {
		candidateLines = 15 // Cap visible candidates
	}
	return 60, 4 + candidateLines // header(2) + candidates + footer(2)
}
