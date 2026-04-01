package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// SpecWorkspaceSection identifies the active Spec workspace subview.
type SpecWorkspaceSection int

const (
	SpecWorkspaceRequirements SpecWorkspaceSection = iota
	SpecWorkspaceCoverage
	SpecWorkspacePublish
)

// SpecWorkspaceOverlay provides a bounded Spec workspace shell.
type SpecWorkspaceOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	projectName string
	section     SpecWorkspaceSection
	styles      *Styles
}

// NewSpecWorkspaceOverlay creates the Spec workspace overlay.
func NewSpecWorkspaceOverlay(projectName string) *SpecWorkspaceOverlay {
	return &SpecWorkspaceOverlay{
		projectName: projectName,
		section:     SpecWorkspaceRequirements,
		styles:      New(),
	}
}

// Init initializes the overlay.
func (m *SpecWorkspaceOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *SpecWorkspaceOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, func() tea.Msg { return CloseOverlayMsg{} }
		case "tab":
			m.nextSection()
			return m, nil
		case "shift+tab", "backtab":
			m.previousSection()
			return m, nil
		case "h", "left":
			m.previousSection()
			return m, nil
		case "l", "right":
			m.nextSection()
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
	}

	return m, nil
}

// View renders the Spec workspace.
func (m *SpecWorkspaceOverlay) View() string {
	width, height := m.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.styles,
		width:             width,
		height:            height,
		title:             "Spec Workspace",
		rightSectionTitle: "Actions",
		breakpoint:        84,
		gap:               3,
		minLeft:           42,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return m.renderMainContent()
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(m.styles, []keybinds.Binding{
				{Key: "Tab", Description: "next section"},
				{Key: "Shift+Tab", Description: "previous"},
				{Key: "h/l", Description: "section left/right"},
				{Key: "Esc", Description: "close"},
			})
		},
	})
}

// Title returns the overlay title.
func (m *SpecWorkspaceOverlay) Title() string {
	return "Spec Workspace"
}

// Size returns the overlay dimensions.
func (m *SpecWorkspaceOverlay) Size() (width, height int) {
	return m.ClampResponsive(82, 18)
}

func (m *SpecWorkspaceOverlay) renderMainContent() string {
	var b strings.Builder
	if m.projectName != "" {
		b.WriteString(m.styles.Footer.Render(fmt.Sprintf("Project: %s", m.projectName)))
		b.WriteString("\n\n")
	}
	b.WriteString(m.renderSectionTabs())
	b.WriteString("\n\n")
	b.WriteString(m.renderSectionBody())
	return b.String()
}

func (m *SpecWorkspaceOverlay) renderSectionTabs() string {
	labels := []struct {
		section SpecWorkspaceSection
		label   string
	}{
		{SpecWorkspaceRequirements, "Requirements"},
		{SpecWorkspaceCoverage, "Coverage"},
		{SpecWorkspacePublish, "Publish"},
	}

	var parts []string
	for _, item := range labels {
		style := m.styles.MenuItem
		if item.section == m.section {
			style = m.styles.MenuItemActive
		}
		parts = append(parts, style.Render(item.label))
	}
	return strings.Join(parts, "  ")
}

func (m *SpecWorkspaceOverlay) renderSectionBody() string {
	switch m.section {
	case SpecWorkspaceCoverage:
		return strings.Join([]string{
			m.styles.MenuItem.Render("Coverage"),
			"",
			"- Review uncovered requirements.",
			"- Track parity gaps and follow-up work.",
		}, "\n")
	case SpecWorkspacePublish:
		return strings.Join([]string{
			m.styles.MenuItem.Render("Publish"),
			"",
			"- Prepare requirement evidence for publication.",
			"- Confirm the workspace is ready to export updates.",
		}, "\n")
	default:
		return strings.Join([]string{
			m.styles.MenuItem.Render("Requirements"),
			"",
			"- Inspect active requirements from the board context.",
			"- Keep issue and requirement state aligned.",
		}, "\n")
	}
}

func (m *SpecWorkspaceOverlay) nextSection() {
	m.section = (m.section + 1) % 3
}

func (m *SpecWorkspaceOverlay) previousSection() {
	m.section = (m.section + 2) % 3
}
