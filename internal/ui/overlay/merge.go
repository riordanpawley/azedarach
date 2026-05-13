package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// MergeTarget describes one candidate row rendered in the merge overlay. The
// same struct is reused by the merge-pick mode (on the board) and the upstream
// source picker (this overlay).
type MergeTarget struct {
	ID          string        // "main" sentinel or task ID
	Label       string        // Display label
	IsMain      bool          // Whether this is the base branch target
	Status      domain.Status // Task status (if not base branch target)
	HasWorktree bool          // Whether this target has a worktree
}

// MergeTargetSelectedMsg is sent when a merge target/source is selected. The
// payload always names the side that will be merged FROM as SourceID and the
// side that will receive the merge as TargetID.
type MergeTargetSelectedMsg struct {
	SourceID                   string
	TargetID                   string
	SkipPreflightStatusRefresh bool
}

// MergeSourceSelectOverlay lets the user pick which upstream issue's branch
// to merge INTO the currently-focused task. It is only used when the focused
// task has multiple eligible upstream sources (parents, blockers). Picking a
// merge TARGET (the reverse direction) happens on the board via the merge-pick
// mode and does not need an overlay.
type MergeSourceSelectOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	target        *domain.Task  // the focused task (the merge destination)
	candidates    []MergeTarget // upstream sources whose branches can be merged in
	cursor        int
	onMerge       func(sourceID string) tea.Cmd
	onCancel      func() tea.Cmd
	overlayStyles *Styles
}

// NewMergeSourceSelectOverlay creates a new upstream-source selection overlay.
func NewMergeSourceSelectOverlay(
	target *domain.Task,
	candidates []MergeTarget,
	onMerge func(sourceID string) tea.Cmd,
	onCancel func() tea.Cmd,
) *MergeSourceSelectOverlay {
	return &MergeSourceSelectOverlay{
		target:        target,
		candidates:    candidates,
		cursor:        0,
		onMerge:       onMerge,
		onCancel:      onCancel,
		overlayStyles: New(),
	}
}

// Init initializes the overlay
func (m *MergeSourceSelectOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *MergeSourceSelectOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
	}

	return m, nil
}

// View renders the overlay
func (m *MergeSourceSelectOverlay) View() string {
	width, height := m.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.overlayStyles,
		width:             width,
		height:            height,
		title:             m.Title(),
		rightSectionTitle: "Actions",
		breakpoint:        58,
		gap:               3,
		minLeft:           34,
		minRight:          18,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return m.renderContent()
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(m.overlayStyles, []keybinds.Binding{
				{Key: "j/k", Description: "navigate"},
				{Key: "Enter", Description: "select"},
				{Key: "Esc", Description: "cancel"},
			})
		},
	})
}

func (m *MergeSourceSelectOverlay) renderContent() string {
	var b strings.Builder

	header := fmt.Sprintf("Merge into %s from:", m.overlayStyles.MenuKey.Render(m.target.ID.String()))
	b.WriteString(m.overlayStyles.Title.Render(header))
	b.WriteString("\n\n")

	if len(m.candidates) == 0 {
		noTasks := m.overlayStyles.MenuItemDisabled.Render("No eligible upstream sources found")
		b.WriteString("  " + noTasks)
		return strings.TrimRight(b.String(), "\n")
	}

	for i, candidate := range m.candidates {
		line := m.renderCandidate(candidate, i == m.cursor)
		b.WriteString(line)
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderCandidate renders a single upstream-source candidate
func (m *MergeSourceSelectOverlay) renderCandidate(target MergeTarget, isActive bool) string {
	var parts []string

	// Cursor indicator
	cursor := "  "
	if isActive {
		cursor = lipgloss.NewStyle().Foreground(styles.Blue).Render("▸ ")
	}
	parts = append(parts, cursor)

	// Base branch gets special rendering.
	if target.IsMain {
		label := target.Label
		if label == "" {
			label = "base branch"
		}
		if isActive {
			label = m.overlayStyles.MenuItemActive.Render(label)
		} else {
			label = lipgloss.NewStyle().
				Foreground(styles.Green).
				Bold(true).
				Render(label)
		}
		parts = append(parts, label)
		parts = append(parts, m.overlayStyles.MenuItemDisabled.Render("(base branch)"))
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
func (m *MergeSourceSelectOverlay) moveCursorDown() {
	if len(m.candidates) == 0 {
		return
	}
	m.cursor = (m.cursor + 1) % len(m.candidates)
}

// moveCursorUp moves the cursor to the previous candidate
func (m *MergeSourceSelectOverlay) moveCursorUp() {
	if len(m.candidates) == 0 {
		return
	}
	m.cursor = (m.cursor - 1 + len(m.candidates)) % len(m.candidates)
}

// selectCurrent selects the current candidate
func (m *MergeSourceSelectOverlay) selectCurrent() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.candidates) {
		return nil
	}

	source := m.candidates[m.cursor]
	if m.onMerge != nil {
		return m.onMerge(source.ID)
	}

	return func() tea.Msg {
		return SelectionMsg{
			Key: "merge",
			Value: MergeTargetSelectedMsg{
				SourceID: source.ID,
				TargetID: m.target.ID.String(),
			},
		}
	}
}

// Title returns the overlay title
func (m *MergeSourceSelectOverlay) Title() string {
	return "Select Upstream Source"
}

// Size returns the overlay dimensions
func (m *MergeSourceSelectOverlay) Size() (width, height int) {
	return m.Clamp(60, m.sizeHeight())
}

func (m *MergeSourceSelectOverlay) sizeHeight() int {
	candidateLines := len(m.candidates)
	if candidateLines == 0 {
		candidateLines = 1
	}
	if candidateLines > 15 {
		candidateLines = 15
	}
	return max(10, candidateLines+8)
}
