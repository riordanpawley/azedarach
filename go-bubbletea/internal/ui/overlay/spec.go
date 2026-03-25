package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	}

	return m, nil
}

// View renders the Spec workspace.
func (m *SpecWorkspaceOverlay) View() string {
	var b strings.Builder

	b.WriteString(m.styles.Title.Render("Spec Workspace"))
	if m.projectName != "" {
		b.WriteString("\n")
		b.WriteString(m.styles.Footer.Render(fmt.Sprintf("Project: %s", m.projectName)))
	}
	b.WriteString("\n\n")
	b.WriteString(m.renderSectionTabs())
	b.WriteString("\n\n")
	b.WriteString(m.renderSectionBody())
	b.WriteString("\n\n")
	b.WriteString(m.styles.Footer.Render("Tab: next section • Shift+Tab: previous • Esc: close"))

	return b.String()
}

// Title returns the overlay title.
func (m *SpecWorkspaceOverlay) Title() string {
	return "Spec Workspace"
}

// Size returns the overlay dimensions.
func (m *SpecWorkspaceOverlay) Size() (width, height int) {
	return 82, 18
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
