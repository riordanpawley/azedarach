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

// MergeTarget represents a target that can be merged into
type MergeTarget struct {
	ID          string        // "main" or task ID
	Label       string        // Display label
	IsMain      bool          // Whether this is the main branch
	Status      domain.Status // Task status (if not main)
	HasWorktree bool          // Whether this target has a worktree
}

// MergeSelectMode determines whether the overlay is picking a merge target or an upstream source.
type MergeSelectMode int

const (
	MergeSelectModeTarget MergeSelectMode = iota
	MergeSelectModeUpstreamSource
)

// MergeSelectOverlay allows selecting a merge target task
type MergeSelectOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	source        *domain.Task  // The issue being merged FROM
	candidates    []MergeTarget // Issues that can be merged INTO (including main)
	cursor        int
	onMerge       func(targetID string) tea.Cmd
	onCancel      func() tea.Cmd
	overlayStyles *Styles
	mode          MergeSelectMode
}

// MergeTargetSelectedMsg is sent when a merge target is selected
type MergeTargetSelectedMsg struct {
	SourceID                   string
	TargetID                   string
	SkipPreflightStatusRefresh bool
}

// NewMergeSelectOverlay creates a new merge target selection overlay
func NewMergeSelectOverlay(
	source *domain.Task,
	candidates []MergeTarget,
	onMerge func(targetID string) tea.Cmd,
	onCancel func() tea.Cmd,
) *MergeSelectOverlay {
	return newMergeSelectOverlay(source, candidates, onMerge, onCancel, MergeSelectModeTarget)
}

// NewMergeSourceSelectOverlay creates a new upstream-source selection overlay.
func NewMergeSourceSelectOverlay(
	target *domain.Task,
	candidates []MergeTarget,
	onMerge func(targetID string) tea.Cmd,
	onCancel func() tea.Cmd,
) *MergeSelectOverlay {
	return newMergeSelectOverlay(target, candidates, onMerge, onCancel, MergeSelectModeUpstreamSource)
}

func newMergeSelectOverlay(
	source *domain.Task,
	candidates []MergeTarget,
	onMerge func(targetID string) tea.Cmd,
	onCancel func() tea.Cmd,
	mode MergeSelectMode,
) *MergeSelectOverlay {
	return &MergeSelectOverlay{
		source:        source,
		candidates:    candidates,
		cursor:        0,
		onMerge:       onMerge,
		onCancel:      onCancel,
		overlayStyles: New(),
		mode:          mode,
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
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
	}

	return m, nil
}

// View renders the overlay
func (m *MergeSelectOverlay) View() string {
	width, height := m.Clamp(60, m.sizeHeight())
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
			return m.renderMergeContent()
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

func (m *MergeSelectOverlay) renderMergeContent() string {
	var b strings.Builder

	header := fmt.Sprintf("Merge %s into:", m.overlayStyles.MenuKey.Render(m.source.ID))
	if m.mode == MergeSelectModeUpstreamSource {
		header = fmt.Sprintf("Merge into %s from:", m.overlayStyles.MenuKey.Render(m.source.ID))
	}
	b.WriteString(m.overlayStyles.Title.Render(header))
	b.WriteString("\n\n")

	if len(m.candidates) == 0 {
		noTasks := m.overlayStyles.MenuItemDisabled.Render("No eligible merge targets found")
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

// renderCandidate renders a single merge target candidate
func (m *MergeSelectOverlay) renderCandidate(target MergeTarget, isActive bool) string {
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
			label = "main"
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
		if m.mode == MergeSelectModeUpstreamSource {
			return SelectionMsg{
				Key: "merge",
				Value: MergeTargetSelectedMsg{
					SourceID: target.ID,
					TargetID: m.source.ID,
				},
			}
		}
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
	if m.mode == MergeSelectModeUpstreamSource {
		return "Select Upstream Source"
	}
	return "Select Merge Target"
}

// Size returns the overlay dimensions
func (m *MergeSelectOverlay) Size() (width, height int) {
	return m.ClampResponsive(60, m.sizeHeight())
}

func (m *MergeSelectOverlay) sizeHeight() int {
	candidateLines := len(m.candidates)
	if candidateLines == 0 {
		candidateLines = 1
	}
	if candidateLines > 15 {
		candidateLines = 15
	}
	return max(10, candidateLines+8)
}
