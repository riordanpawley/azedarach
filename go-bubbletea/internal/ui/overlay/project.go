package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
)

// ProjectSelectedMsg is sent when a project is selected
type ProjectSelectedMsg struct {
	Project config.Project
}

// ProjectAction represents an action in the project selector
type ProjectAction int

const (
	ProjectActionSwitch ProjectAction = iota
	ProjectActionSetDefault
	ProjectActionAdd
	ProjectActionRemove
	ProjectActionDetect
)

// ProjectSelector is an overlay for selecting and managing projects
type ProjectSelector struct {
	registry *config.ProjectsRegistry
	cursor   int
	mode     projectSelectorMode
	styles   *Styles
}

type projectSelectorMode int

const (
	modeList projectSelectorMode = iota
	modeActions
)

// NewProjectSelector creates a new project selector overlay
func NewProjectSelector(registry *config.ProjectsRegistry) *ProjectSelector {
	s := New()
	return &ProjectSelector{
		registry: registry,
		cursor:   0,
		mode:     modeList,
		styles:   s,
	}
}

// Init initializes the overlay
func (m *ProjectSelector) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *ProjectSelector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			if m.mode == modeActions {
				// Return to list mode
				m.mode = modeList
				return m, nil
			}
			// Close overlay
			return m, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			m.moveCursorDown()
			return m, nil

		case "k", "up":
			m.moveCursorUp()
			return m, nil

		case "enter":
			if m.mode == modeList {
				// Select project
				return m, m.selectProject()
			} else {
				// Execute action
				return m, m.executeAction()
			}

		case "d":
			if m.mode == modeList && len(m.registry.Projects) > 0 {
				// Set as default
				return m, m.setAsDefault()
			}

		case "x":
			if m.mode == modeList && len(m.registry.Projects) > 0 {
				// Remove project
				return m, m.removeProject()
			}

		case "a":
			if m.mode == modeList {
				// Add new project (open actions mode)
				m.mode = modeActions
				m.cursor = 0
				return m, nil
			}

		case "D":
			if m.mode == modeList {
				// Detect from cwd
				return m, m.detectAndAdd()
			}
		}
	}

	return m, nil
}

// View renders the project selector
func (m *ProjectSelector) View() string {
	if m.mode == modeActions {
		return m.viewActions()
	}
	return m.viewList()
}

// viewList renders the project list
func (m *ProjectSelector) viewList() string {
	var b strings.Builder

	if len(m.registry.Projects) == 0 {
		b.WriteString(m.styles.MenuItem.Render("No projects registered"))
		b.WriteString("\n\n")
		b.WriteString(m.styles.Footer.Render("a: add project • D: detect from cwd • esc: close"))
		return b.String()
	}

	for i, project := range m.registry.Projects {
		var style = m.styles.MenuItem
		if i == m.cursor {
			style = m.styles.MenuItemActive
		}

		// Format: name (path) [default]
		line := fmt.Sprintf("%s", project.Name)
		if project.Name == m.registry.DefaultProject {
			line += " " + m.styles.MenuKey.Render("[default]")
		}

		b.WriteString(style.Render(line))
		b.WriteString("\n")
		b.WriteString(m.styles.Footer.Render(fmt.Sprintf("  %s", project.Path)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.styles.Footer.Render("enter: switch • d: set default • x: remove • a: add • D: detect • esc: close"))

	return b.String()
}

// viewActions renders the actions menu for adding projects
func (m *ProjectSelector) viewActions() string {
	var b strings.Builder

	actions := []struct {
		key   string
		label string
	}{
		{"1", "Add project manually"},
		{"2", "Detect from current directory"},
		{"3", "Cancel"},
	}

	for i, action := range actions {
		var style, keyStyle = m.styles.MenuItem, m.styles.MenuKey
		if i == m.cursor {
			style = m.styles.MenuItemActive
		}

		line := keyStyle.Render("["+action.key+"]") + " " + style.Render(action.label)
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.styles.Footer.Render("enter: select • esc: back"))

	return b.String()
}

// Title returns the overlay title
func (m *ProjectSelector) Title() string {
	if m.mode == modeActions {
		return "Add Project"
	}
	return "Projects"
}

// Size returns the overlay dimensions
func (m *ProjectSelector) Size() (width, height int) {
	if m.mode == modeActions {
		return 50, 10
	}

	// Dynamic height based on number of projects
	height = len(m.registry.Projects)*2 + 6
	if height < 10 {
		height = 10
	}
	return 70, height
}

// moveCursorDown moves the cursor down
func (m *ProjectSelector) moveCursorDown() {
	maxCursor := m.getMaxCursor()
	if m.cursor < maxCursor {
		m.cursor++
	}
}

// moveCursorUp moves the cursor up
func (m *ProjectSelector) moveCursorUp() {
	if m.cursor > 0 {
		m.cursor--
	}
}

// getMaxCursor returns the maximum cursor position
func (m *ProjectSelector) getMaxCursor() int {
	if m.mode == modeActions {
		return 2 // 3 actions (0, 1, 2)
	}
	if len(m.registry.Projects) == 0 {
		return 0
	}
	return len(m.registry.Projects) - 1
}

// selectProject selects the current project
func (m *ProjectSelector) selectProject() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.registry.Projects) {
		return nil
	}

	project := m.registry.Projects[m.cursor]

	return func() tea.Msg {
		return ProjectSelectedMsg{
			Project: project,
		}
	}
}

// setAsDefault sets the current project as default
func (m *ProjectSelector) setAsDefault() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.registry.Projects) {
		return nil
	}

	project := m.registry.Projects[m.cursor]

	return func() tea.Msg {
		if err := m.registry.SetDefault(project.Name); err != nil {
			return SelectionMsg{
				Key:   "set-default-error",
				Value: err,
			}
		}

		// Save registry
		if err := config.SaveProjectsRegistry(m.registry); err != nil {
			return SelectionMsg{
				Key:   "save-error",
				Value: err,
			}
		}

		return SelectionMsg{
			Key:   "set-default-success",
			Value: project.Name,
		}
	}
}

// removeProject removes the current project
func (m *ProjectSelector) removeProject() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.registry.Projects) {
		return nil
	}

	project := m.registry.Projects[m.cursor]

	return func() tea.Msg {
		if err := m.registry.Remove(project.Name); err != nil {
			return SelectionMsg{
				Key:   "remove-error",
				Value: err,
			}
		}

		// Save registry
		if err := config.SaveProjectsRegistry(m.registry); err != nil {
			return SelectionMsg{
				Key:   "save-error",
				Value: err,
			}
		}

		// Adjust cursor if needed
		if m.cursor >= len(m.registry.Projects) && m.cursor > 0 {
			m.cursor--
		}

		return SelectionMsg{
			Key:   "remove-success",
			Value: project.Name,
		}
	}
}

// detectAndAdd detects and adds a project from the current directory
func (m *ProjectSelector) detectAndAdd() tea.Cmd {
	return func() tea.Msg {
		// Detect project from cwd
		project, err := config.DetectProjectFromCwd()
		if err != nil {
			return SelectionMsg{
				Key:   "detect-error",
				Value: err,
			}
		}

		// Add to registry
		if err := m.registry.Add(project.Name, project.Path); err != nil {
			return SelectionMsg{
				Key:   "add-error",
				Value: err,
			}
		}

		// Save registry
		if err := config.SaveProjectsRegistry(m.registry); err != nil {
			return SelectionMsg{
				Key:   "save-error",
				Value: err,
			}
		}

		return SelectionMsg{
			Key:   "detect-success",
			Value: project.Name,
		}
	}
}

// executeAction executes the selected action in actions mode
func (m *ProjectSelector) executeAction() tea.Cmd {
	switch m.cursor {
	case 0:
		// Add project manually (would need input form)
		return func() tea.Msg {
			return SelectionMsg{
				Key:   "manual-add",
				Value: nil,
			}
		}
	case 1:
		// Detect from current directory
		m.mode = modeList
		return m.detectAndAdd()
	case 2:
		// Cancel
		m.mode = modeList
		return nil
	}
	return nil
}

// ProjectSelectorOption is a function that configures a ProjectSelector
type ProjectSelectorOption func(*ProjectSelector)

// WithInitialCursor sets the initial cursor position
func WithInitialCursor(cursor int) ProjectSelectorOption {
	return func(p *ProjectSelector) {
		p.cursor = cursor
	}
}

// NewProjectSelectorWithOptions creates a new project selector with options
func NewProjectSelectorWithOptions(registry *config.ProjectsRegistry, opts ...ProjectSelectorOption) *ProjectSelector {
	p := NewProjectSelector(registry)
	for _, opt := range opts {
		opt(p)
	}
	return p
}
