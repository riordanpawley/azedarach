package overlay

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	uistyles "github.com/riordanpawley/azedarach/internal/ui/styles"
)

// ScopeSelectedMsg is sent when the authoritative board scope changes.
// A zero Project with Global set selects the root user projection.
type ScopeSelectedMsg struct {
	Global  bool
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

// ProjectSelector is the authoritative Global/project scope selector and
// project-registry manager.
type ProjectSelector struct {
	dialogViewportState
	registry           *config.ProjectsRegistry
	cursor             int
	mode               projectSelectorMode
	styles             *Styles
	currentProjectName string
	currentGlobal      bool
	filtering          bool
	query              string
}

type scopeSelectorEntry struct {
	global       bool
	project      config.Project
	projectIndex int
}

type projectSelectorMode int

const (
	projectModeList projectSelectorMode = iota
	projectModeActions
)

// NewProjectSelector creates a new project selector overlay
func NewProjectSelector(registry *config.ProjectsRegistry) *ProjectSelector {
	s := New()
	return &ProjectSelector{
		registry: registry,
		cursor:   0,
		mode:     projectModeList,
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
		if m.mode == projectModeList && m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering, m.query, m.cursor = false, "", 0
				return m, nil
			case "enter":
				return m, m.selectProject()
			case "down":
				m.moveCursorDown()
				return m, nil
			case "up":
				m.moveCursorUp()
				return m, nil
			case "backspace":
				if runes := []rune(m.query); len(runes) > 0 {
					m.query = string(runes[:len(runes)-1])
					m.cursor = 0
				}
				return m, nil
			default:
				if msg.String() == "0" {
					return m, m.selectScopeByProjectIndex(-1)
				}
				if index, ok := parseOneBasedProjectIndex(msg.String()); ok && index < len(m.registry.Projects) {
					return m, m.selectScopeByProjectIndex(index)
				}
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if unicode.IsPrint(r) {
							m.query += string(r)
						}
					}
					m.cursor = 0
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "esc", "q":
			if m.mode == projectModeActions {
				// Return to list mode
				m.mode = projectModeList
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
			if m.mode == projectModeList {
				// Select project
				return m, m.selectProject()
			} else {
				// Execute action
				return m, m.executeAction()
			}

		case "d":
			if m.mode == projectModeList && len(m.registry.Projects) > 0 {
				// Set as default
				return m, m.setAsDefault()
			}

		case "x":
			if m.mode == projectModeList && len(m.registry.Projects) > 0 {
				// Remove project
				return m, m.removeProject()
			}

		case "a":
			if m.mode == projectModeList {
				// Add new project (open actions mode)
				m.mode = projectModeActions
				m.cursor = 0
				return m, nil
			}

		case "D":
			if m.mode == projectModeList {
				// Detect from cwd
				return m, m.detectAndAdd()
			}

		case "/":
			if m.mode == projectModeList {
				m.filtering = true
				m.query = ""
				m.cursor = 0
				return m, nil
			}

		default:
			if m.mode == projectModeList {
				if msg.String() == "0" {
					return m, m.selectScopeByProjectIndex(-1)
				}
				index, ok := parseOneBasedProjectIndex(msg.String())
				if ok && index < len(m.registry.Projects) {
					return m, m.selectScopeByProjectIndex(index)
				}
			}
		}
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
	}

	return m, nil
}

// View renders the project selector
func (m *ProjectSelector) View() string {
	if m.mode == projectModeActions {
		return m.viewActions()
	}
	return m.viewList()
}

// viewList renders the project list
func (m *ProjectSelector) viewList() string {
	width, height := m.dialogSize()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.styles,
		width:             width,
		height:            height,
		title:             "SCOPE SELECTOR",
		rightSectionTitle: "Actions",
		breakpoint:        84,
		gap:               3,
		minLeft:           44,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return m.renderProjectListContent(height)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			bindings := []keybinds.Binding{
				{Key: "0-9", Description: "switch scope"},
				{Key: "Enter", Description: "switch"},
				{Key: "/", Description: "search"},
				{Key: "d", Description: "default"},
				{Key: "x", Description: "remove"},
				{Key: "a", Description: "add"},
				{Key: "D", Description: "detect"},
				{Key: "Esc", Description: "close"},
			}
			if mode == dialogLayoutStacked {
				bindings = []keybinds.Binding{
					{Key: "0-9/Enter", Description: "switch"},
					{Key: "/", Description: "search"},
					{Key: "d/x", Description: "default/remove"},
					{Key: "a/D", Description: "add/detect"},
					{Key: "Esc", Description: "close"},
				}
			}
			return keybinds.RenderKeyTable(bindings, 0, keybinds.Theme{
				KeyStyle:         m.styles.MenuKey,
				DescriptionStyle: m.styles.Footer.MarginTop(0),
				FooterStyle:      m.styles.Footer.MarginTop(0),
			})
		},
	})
}

func (m *ProjectSelector) renderProjectListContent(height int) string {
	var list strings.Builder
	entries := m.scopeEntries()
	if m.filtering || strings.TrimSpace(m.query) != "" {
		list.WriteString(m.styles.Footer.MarginTop(0).Render("Search: " + m.query + "▏"))
		list.WriteString("\n")
	}
	start, end := scopeSelectorWindow(m.cursor, len(entries), max(1, (height-3)/2))
	if start > 0 {
		list.WriteString(m.styles.Footer.MarginTop(0).Render(fmt.Sprintf("  ↑ %d more", start)))
		list.WriteString("\n")
	}
	for i := start; i < end; i++ {
		entry := entries[i]
		style := m.styles.MenuItem
		if i == m.cursor {
			style = m.styles.MenuItemActive
		}
		isCurrent := entry.global && m.currentGlobal || !entry.global && !m.currentGlobal && entry.project.Name == m.currentProjectName
		if isCurrent {
			style = lipgloss.NewStyle().
				Foreground(uistyles.Green).
				Bold(true)
		}
		line := "0. Global"
		detail := "   User views · all registered projects"
		if !entry.global {
			line = fmt.Sprintf("%d. %s", entry.projectIndex+1, entry.project.Name)
			detail = fmt.Sprintf("   %s", entry.project.Path)
		}
		if isCurrent {
			line += " " + lipgloss.NewStyle().Foreground(uistyles.Green).Render("(current)")
		}
		if !entry.global && entry.project.Name == m.registry.DefaultProject {
			line += " " + m.styles.MenuKey.Render("[default]")
		}

		list.WriteString(style.Render(line))
		list.WriteString("\n")
		list.WriteString(m.styles.Footer.MarginTop(0).Render(detail))
		list.WriteString("\n")
	}
	if end < len(entries) {
		list.WriteString(m.styles.Footer.MarginTop(0).Render(fmt.Sprintf("  ↓ %d more", len(entries)-end)))
	}
	return strings.TrimRight(list.String(), "\n")
}

func (m *ProjectSelector) listModeHeight() int {
	return max(12, min(26, (len(m.registry.Projects)+1)*2+10))
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
	b.WriteString(keybinds.RenderKeyTable([]keybinds.Binding{
		{Key: "Enter", Description: "select"},
		{Key: "Esc", Description: "back"},
	}, 0, keybinds.Theme{
		KeyStyle:         m.styles.MenuKey,
		DescriptionStyle: m.styles.Footer,
		FooterStyle:      m.styles.Footer,
	}))

	return b.String()
}

// Title returns the overlay title
func (m *ProjectSelector) Title() string {
	if m.mode == projectModeActions {
		return "Project Selector"
	}
	return ""
}

// Size returns the overlay dimensions
func (m *ProjectSelector) Size() (width, height int) {
	return m.dialogSize()
}

func (m *ProjectSelector) dialogSize() (width, height int) {
	if m.mode == projectModeActions {
		return m.Clamp(50, 10)
	}

	return m.ClampResponsive(88, m.listModeHeight())
}

func (m *ProjectSelector) UsesAppFrame() bool {
	return m.mode == projectModeActions
}

func (m *ProjectSelector) UsesInternalTitle() bool {
	return m.mode != projectModeActions
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
	if m.mode == projectModeActions {
		return 2 // 3 actions (0, 1, 2)
	}
	return max(0, len(m.scopeEntries())-1)
}

// selectProject selects the current project
func (m *ProjectSelector) selectProject() tea.Cmd {
	entries := m.scopeEntries()
	if m.cursor < 0 || m.cursor >= len(entries) {
		return nil
	}
	entry := entries[m.cursor]

	return func() tea.Msg {
		return ScopeSelectedMsg{Global: entry.global, Project: entry.project}
	}
}

// selectScopeByProjectIndex preserves the stable 0-9 shortcuts even when the
// visible list is filtered. Global is represented by projectIndex -1.
func (m *ProjectSelector) selectScopeByProjectIndex(projectIndex int) tea.Cmd {
	if projectIndex < 0 {
		m.cursor = 0
		return func() tea.Msg { return ScopeSelectedMsg{Global: true} }
	}
	if projectIndex >= len(m.registry.Projects) {
		return nil
	}
	m.cursor = m.cursorForProjectIndex(projectIndex)
	project := m.registry.Projects[projectIndex]
	return func() tea.Msg { return ScopeSelectedMsg{Project: project} }
}

// setAsDefault sets the current project as default
func (m *ProjectSelector) setAsDefault() tea.Cmd {
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}

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
	project, ok := m.selectedProject()
	if !ok {
		return nil
	}

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
		m.mode = projectModeList
		return m.detectAndAdd()
	case 2:
		// Cancel
		m.mode = projectModeList
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

// WithCurrentProjectName sets the project currently active in the app.
func WithCurrentProjectName(name string) ProjectSelectorOption {
	return func(p *ProjectSelector) {
		p.currentProjectName = name
	}
}

// WithGlobalScope marks Global as the currently active scope.
func WithGlobalScope(global bool) ProjectSelectorOption {
	return func(p *ProjectSelector) {
		p.currentGlobal = global
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

func parseOneBasedProjectIndex(key string) (int, bool) {
	if len(key) != 1 {
		return 0, false
	}
	n, err := strconv.Atoi(key)
	if err != nil || n < 1 || n > 9 {
		return 0, false
	}
	return n - 1, true
}

func (m *ProjectSelector) scopeEntries() []scopeSelectorEntry {
	entries := []scopeSelectorEntry{{global: true, projectIndex: -1}}
	if m.registry == nil {
		return entries
	}
	query := strings.ToLower(strings.TrimSpace(m.query))
	for i, project := range m.registry.Projects {
		if query != "" && !strings.Contains(strings.ToLower(project.Name+" "+project.Path), query) {
			continue
		}
		entries = append(entries, scopeSelectorEntry{project: project, projectIndex: i})
	}
	return entries
}

func (m *ProjectSelector) selectedProject() (config.Project, bool) {
	entries := m.scopeEntries()
	if m.cursor < 0 || m.cursor >= len(entries) || entries[m.cursor].global {
		return config.Project{}, false
	}
	return entries[m.cursor].project, true
}

func (m *ProjectSelector) cursorForProjectIndex(index int) int {
	for i, entry := range m.scopeEntries() {
		if !entry.global && entry.projectIndex == index {
			return i
		}
	}
	return 0
}

func scopeSelectorWindow(cursor, total, capacity int) (int, int) {
	if total <= 0 || capacity <= 0 {
		return 0, 0
	}
	if capacity >= total {
		return 0, total
	}
	start := cursor - capacity/2
	if start < 0 {
		start = 0
	}
	if start+capacity > total {
		start = total - capacity
	}
	return start, start + capacity
}
