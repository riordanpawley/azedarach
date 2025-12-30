package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
)

func TestNewProjectSelector(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test", Path: "/tmp/test"},
		},
		DefaultProject: "test",
	}

	selector := NewProjectSelector(registry)

	if selector == nil {
		t.Fatal("expected selector to be created")
	}

	if selector.registry != registry {
		t.Error("expected registry to be set")
	}

	if selector.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", selector.cursor)
	}

	if selector.mode != modeList {
		t.Errorf("expected mode to be modeList, got %d", selector.mode)
	}
}

func TestProjectSelector_Title(t *testing.T) {
	registry := &config.ProjectsRegistry{}
	selector := NewProjectSelector(registry)

	// Test list mode
	title := selector.Title()
	if title != "Projects" {
		t.Errorf("expected title 'Projects', got %s", title)
	}

	// Test actions mode
	selector.mode = modeActions
	title = selector.Title()
	if title != "Add Project" {
		t.Errorf("expected title 'Add Project', got %s", title)
	}
}

func TestProjectSelector_Size(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
		},
	}

	selector := NewProjectSelector(registry)

	// Test list mode
	width, height := selector.Size()
	if width <= 0 {
		t.Errorf("expected positive width, got %d", width)
	}
	if height <= 0 {
		t.Errorf("expected positive height, got %d", height)
	}

	// Test actions mode
	selector.mode = modeActions
	width, height = selector.Size()
	if width <= 0 {
		t.Errorf("expected positive width, got %d", width)
	}
	if height != 10 {
		t.Errorf("expected height 10 for actions mode, got %d", height)
	}
}

func TestProjectSelector_MoveCursor(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
			{Name: "test3", Path: "/tmp/test3"},
		},
	}

	selector := NewProjectSelector(registry)

	// Should start at 0
	if selector.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", selector.cursor)
	}

	// Move down
	selector.moveCursorDown()
	if selector.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", selector.cursor)
	}

	// Move down again
	selector.moveCursorDown()
	if selector.cursor != 2 {
		t.Errorf("expected cursor at 2, got %d", selector.cursor)
	}

	// Move down should not go past max
	selector.moveCursorDown()
	if selector.cursor != 2 {
		t.Errorf("expected cursor to stay at 2, got %d", selector.cursor)
	}

	// Move up
	selector.moveCursorUp()
	if selector.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", selector.cursor)
	}

	// Move up to 0
	selector.moveCursorUp()
	if selector.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", selector.cursor)
	}

	// Move up should not go below 0
	selector.moveCursorUp()
	if selector.cursor != 0 {
		t.Errorf("expected cursor to stay at 0, got %d", selector.cursor)
	}
}

func TestProjectSelector_SelectProject(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
		},
	}

	selector := NewProjectSelector(registry)

	// Select first project
	cmd := selector.selectProject()
	if cmd == nil {
		t.Fatal("expected command to be returned")
	}

	result := cmd()
	msg, ok := result.(ProjectSelectedMsg)
	if !ok {
		t.Fatalf("expected ProjectSelectedMsg, got %T", result)
	}

	if msg.Project.Name != "test1" {
		t.Errorf("expected project 'test1', got %s", msg.Project.Name)
	}

	// Move to second project and select
	selector.moveCursorDown()
	cmd = selector.selectProject()
	result = cmd()
	msg, ok = result.(ProjectSelectedMsg)
	if !ok {
		t.Fatalf("expected ProjectSelectedMsg, got %T", result)
	}

	if msg.Project.Name != "test2" {
		t.Errorf("expected project 'test2', got %s", msg.Project.Name)
	}
}

func TestProjectSelector_SetAsDefault(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
		},
		DefaultProject: "test1",
	}

	selector := NewProjectSelector(registry)

	// Move to second project
	selector.moveCursorDown()

	// Set as default (but don't execute the command to avoid file I/O)
	// Just test that the registry is updated correctly
	if err := registry.SetDefault("test2"); err != nil {
		t.Fatalf("SetDefault() error = %v", err)
	}

	if registry.DefaultProject != "test2" {
		t.Errorf("expected default project to be 'test2', got %s", registry.DefaultProject)
	}

	// Test that the command is created correctly
	cmd := selector.setAsDefault()
	if cmd == nil {
		t.Fatal("expected command to be returned")
	}
}

func TestProjectSelector_RemoveProject(t *testing.T) {
	// Test 1: Verify registry.Remove() works correctly
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
		},
		DefaultProject: "test1",
	}

	if err := registry.Remove("test2"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if len(registry.Projects) != 1 {
		t.Errorf("expected 1 project remaining, got %d", len(registry.Projects))
	}

	if registry.Projects[0].Name != "test1" {
		t.Errorf("expected remaining project to be 'test1', got %s", registry.Projects[0].Name)
	}

	// Test 2: Verify removeProject() command is created correctly
	registry2 := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
		},
		DefaultProject: "test1",
	}

	selector := NewProjectSelector(registry2)
	// Move to second project
	selector.moveCursorDown()

	// Test that the command is created correctly
	cmd := selector.removeProject()
	if cmd == nil {
		t.Fatal("expected command to be returned")
	}
}

func TestProjectSelector_KeyboardNavigation(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
		},
	}

	selector := NewProjectSelector(registry)

	tests := []struct {
		name           string
		key            string
		expectedCursor int
	}{
		{"j key", "j", 1},
		{"down arrow", "down", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset cursor
			selector.cursor = 0

			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			if tt.key == "down" {
				msg = tea.KeyMsg{Type: tea.KeyDown}
			}

			selector.Update(msg)

			if selector.cursor != tt.expectedCursor {
				t.Errorf("expected cursor at %d, got %d", tt.expectedCursor, selector.cursor)
			}
		})
	}
}

func TestProjectSelector_EscapeKey(t *testing.T) {
	registry := &config.ProjectsRegistry{}
	selector := NewProjectSelector(registry)

	// Test escape in list mode
	msg := tea.KeyMsg{Type: tea.KeyEscape}
	_, cmd := selector.Update(msg)

	if cmd == nil {
		t.Fatal("expected command to be returned")
	}

	result := cmd()
	if _, ok := result.(CloseOverlayMsg); !ok {
		t.Errorf("expected CloseOverlayMsg, got %T", result)
	}

	// Test escape in actions mode
	selector.mode = modeActions
	_, cmd = selector.Update(msg)

	// Should return to list mode, not close
	if selector.mode != modeList {
		t.Error("expected to return to list mode")
	}
}

func TestProjectSelector_EnterKey(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
		},
	}

	selector := NewProjectSelector(registry)

	// Test enter in list mode (should select project)
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	_, cmd := selector.Update(msg)

	if cmd == nil {
		t.Fatal("expected command to be returned")
	}

	result := cmd()
	if _, ok := result.(ProjectSelectedMsg); !ok {
		t.Errorf("expected ProjectSelectedMsg, got %T", result)
	}
}

func TestProjectSelector_ActionMode(t *testing.T) {
	registry := &config.ProjectsRegistry{}
	selector := NewProjectSelector(registry)

	// Press 'a' to enter action mode
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}
	selector.Update(msg)

	if selector.mode != modeActions {
		t.Error("expected to enter actions mode")
	}

	if selector.cursor != 0 {
		t.Errorf("expected cursor to reset to 0, got %d", selector.cursor)
	}
}

func TestProjectSelector_View_EmptyProjects(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{},
	}

	selector := NewProjectSelector(registry)
	view := selector.View()

	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain "No projects registered"
	if !strings.Contains(view, "No projects registered") {
		t.Error("expected view to contain 'No projects registered'")
	}
}

func TestProjectSelector_View_WithProjects(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
		},
		DefaultProject: "test1",
	}

	selector := NewProjectSelector(registry)
	view := selector.View()

	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain project names
	if !strings.Contains(view, "test1") {
		t.Error("expected view to contain 'test1'")
	}

	if !strings.Contains(view, "test2") {
		t.Error("expected view to contain 'test2'")
	}

	// Should show default marker
	if !strings.Contains(view, "[default]") {
		t.Error("expected view to contain '[default]' marker")
	}
}

func TestProjectSelector_View_ActionsMode(t *testing.T) {
	registry := &config.ProjectsRegistry{}
	selector := NewProjectSelector(registry)
	selector.mode = modeActions

	view := selector.View()

	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain action options
	if !strings.Contains(view, "Add project manually") {
		t.Error("expected view to contain 'Add project manually'")
	}

	if !strings.Contains(view, "Detect from current directory") {
		t.Error("expected view to contain 'Detect from current directory'")
	}

	if !strings.Contains(view, "Cancel") {
		t.Error("expected view to contain 'Cancel'")
	}
}

func TestNewProjectSelectorWithOptions(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "test1", Path: "/tmp/test1"},
			{Name: "test2", Path: "/tmp/test2"},
		},
	}

	// Test with initial cursor option
	selector := NewProjectSelectorWithOptions(registry, WithInitialCursor(1))

	if selector.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", selector.cursor)
	}
}

func TestProjectSelector_GetMaxCursor(t *testing.T) {
	tests := []struct {
		name           string
		numProjects    int
		mode           projectSelectorMode
		expectedMax    int
	}{
		{"empty list mode", 0, modeList, 0},
		{"one project list mode", 1, modeList, 0},
		{"three projects list mode", 3, modeList, 2},
		{"actions mode", 0, modeActions, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projects := make([]config.Project, tt.numProjects)
			for i := 0; i < tt.numProjects; i++ {
				projects[i] = config.Project{
					Name: "test",
					Path: "/tmp/test",
				}
			}

			registry := &config.ProjectsRegistry{
				Projects: projects,
			}

			selector := NewProjectSelector(registry)
			selector.mode = tt.mode

			max := selector.getMaxCursor()
			if max != tt.expectedMax {
				t.Errorf("expected max cursor %d, got %d", tt.expectedMax, max)
			}
		})
	}
}
