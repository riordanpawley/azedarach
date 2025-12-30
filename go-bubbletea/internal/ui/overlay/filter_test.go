package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestNewFilterMenu(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	if menu == nil {
		t.Fatal("NewFilterMenu returned nil")
	}

	if menu.filter != filter {
		t.Error("FilterMenu should reference the provided filter")
	}

	if menu.mode != filterModeNormal {
		t.Errorf("Expected mode to be normal, got %v", menu.mode)
	}

	if menu.styles == nil {
		t.Error("FilterMenu should have styles initialized")
	}
}

func TestFilterMenuTitle(t *testing.T) {
	menu := NewFilterMenu(domain.NewFilter())
	title := menu.Title()

	if title != "Filter Tasks" {
		t.Errorf("Expected title 'Filter Tasks', got %q", title)
	}
}

func TestFilterMenuSize(t *testing.T) {
	menu := NewFilterMenu(domain.NewFilter())
	width, height := menu.Size()

	if width <= 0 {
		t.Error("Width should be positive")
	}

	if height <= 0 {
		t.Error("Height should be positive")
	}

	// Should be large enough for all filter options
	if width < 40 {
		t.Errorf("Width %d seems too small for filter menu", width)
	}

	if height < 10 {
		t.Errorf("Height %d seems too small for filter menu", height)
	}
}

func TestFilterMenuView_DisplaysFilterState(t *testing.T) {
	filter := domain.NewFilter()
	filter.ToggleStatus(domain.StatusOpen)
	filter.TogglePriority(domain.P0)
	filter.ToggleType(domain.TypeTask)
	filter.HideEpicChildren = true

	menu := NewFilterMenu(filter)
	view := menu.View()

	// Should contain filter categories
	if !strings.Contains(view, "Status:") {
		t.Error("View should contain 'Status:'")
	}
	if !strings.Contains(view, "Priority:") {
		t.Error("View should contain 'Priority:'")
	}
	if !strings.Contains(view, "Type:") {
		t.Error("View should contain 'Type:'")
	}
	if !strings.Contains(view, "Session:") {
		t.Error("View should contain 'Session:'")
	}

	// Should show active filters with indicator
	if !strings.Contains(view, "●") {
		t.Error("View should show active filter indicators (●)")
	}

	// Should show hide epic children option
	if !strings.Contains(view, "Hide epic children") {
		t.Error("View should contain 'Hide epic children'")
	}

	// Should show age filter
	if !strings.Contains(view, "Age:") {
		t.Error("View should contain 'Age:'")
	}

	// Should show clear all option
	if !strings.Contains(view, "Clear all") {
		t.Error("View should contain 'Clear all'")
	}
}

func TestFilterMenuView_ActiveIndicators(t *testing.T) {
	filter := domain.NewFilter()
	filter.ToggleStatus(domain.StatusOpen)
	menu := NewFilterMenu(filter)

	view := menu.View()

	// Count active indicators - should have at least one for the active status filter
	activeCount := strings.Count(view, "●")
	if activeCount == 0 {
		t.Error("View should show active filter indicator (●) for selected status")
	}
}

func TestFilterMenuView_HideEpicChildrenCheckbox(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		expected string
	}{
		{
			name:     "unchecked",
			enabled:  false,
			expected: "[ ]",
		},
		{
			name:     "checked",
			enabled:  true,
			expected: "[●]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := domain.NewFilter()
			filter.HideEpicChildren = tt.enabled
			menu := NewFilterMenu(filter)

			view := menu.View()

			if !strings.Contains(view, tt.expected) {
				t.Errorf("Expected checkbox to show %q, view:\n%s", tt.expected, view)
			}
		})
	}
}

func TestFilterMenu_StatusToggle(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// Enter status mode
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	menu = model.(*FilterMenu)
	if menu.mode != filterModeStatus {
		t.Error("Should enter status mode on 's' key")
	}

	// Toggle open status
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	menu = model.(*FilterMenu)
	if !filter.Status[domain.StatusOpen] {
		t.Error("Should toggle Open status")
	}
	if menu.mode != filterModeNormal {
		t.Error("Should return to normal mode after selection")
	}

	// Toggle again to disable
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	menu = model.(*FilterMenu)
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	menu = model.(*FilterMenu)
	if filter.Status[domain.StatusOpen] {
		t.Error("Should untoggle Open status")
	}
}

func TestFilterMenu_PriorityToggle(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// Enter priority mode
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	menu = model.(*FilterMenu)
	if menu.mode != filterModePriority {
		t.Error("Should enter priority mode on 'p' key")
	}

	// Toggle P0
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	menu = model.(*FilterMenu)
	if !filter.Priority[domain.P0] {
		t.Error("Should toggle P0 priority")
	}
	if menu.mode != filterModeNormal {
		t.Error("Should return to normal mode after selection")
	}

	// Test all priorities
	priorities := []struct {
		key      rune
		priority domain.Priority
	}{
		{'1', domain.P1},
		{'2', domain.P2},
		{'3', domain.P3},
		{'4', domain.P4},
	}

	for _, tt := range priorities {
		model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		menu = model.(*FilterMenu)
		model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tt.key}})
		menu = model.(*FilterMenu)
		if !filter.Priority[tt.priority] {
			t.Errorf("Should toggle priority %v", tt.priority)
		}
	}
}

func TestFilterMenu_TypeToggle(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// Enter type mode
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	menu = model.(*FilterMenu)
	if menu.mode != filterModeType {
		t.Error("Should enter type mode on 't' key")
	}

	// Test all types
	types := []struct {
		key      rune
		taskType domain.TaskType
	}{
		{'T', domain.TypeTask},
		{'B', domain.TypeBug},
		{'F', domain.TypeFeature},
		{'E', domain.TypeEpic},
		{'C', domain.TypeChore},
	}

	for _, tt := range types {
		model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
		menu = model.(*FilterMenu)
		model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tt.key}})
		menu = model.(*FilterMenu)
		if !filter.Type[tt.taskType] {
			t.Errorf("Should toggle type %v", tt.taskType)
		}
		if menu.mode != filterModeNormal {
			t.Error("Should return to normal mode after selection")
		}
	}
}

func TestFilterMenu_SessionStateToggle(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// Enter session mode
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	menu = model.(*FilterMenu)
	if menu.mode != filterModeSession {
		t.Error("Should enter session mode on 'S' key")
	}

	// Test all session states
	states := []struct {
		key   rune
		state domain.SessionState
	}{
		{'I', domain.SessionIdle},
		{'U', domain.SessionBusy},
		{'W', domain.SessionWaiting},
		{'D', domain.SessionDone},
		{'X', domain.SessionError},
		{'P', domain.SessionPaused},
	}

	for _, tt := range states {
		model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
		menu = model.(*FilterMenu)
		model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tt.key}})
		menu = model.(*FilterMenu)
		if !filter.SessionState[tt.state] {
			t.Errorf("Should toggle session state %v", tt.state)
		}
		if menu.mode != filterModeNormal {
			t.Error("Should return to normal mode after selection")
		}
	}
}

func TestFilterMenu_HideEpicChildrenToggle(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	if filter.HideEpicChildren {
		t.Error("Should start with HideEpicChildren false")
	}

	// Toggle on
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	menu = model.(*FilterMenu)
	if !filter.HideEpicChildren {
		t.Error("Should toggle HideEpicChildren to true")
	}

	// Toggle off
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	menu = model.(*FilterMenu)
	if filter.HideEpicChildren {
		t.Error("Should toggle HideEpicChildren to false")
	}
}

func TestFilterMenu_AgeFilter(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	if filter.AgeMaxDays != nil {
		t.Error("Should start with no age filter")
	}

	// Set to 24h (1 day)
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	menu = model.(*FilterMenu)
	if filter.AgeMaxDays == nil || *filter.AgeMaxDays != 1 {
		t.Error("Should set age filter to 1 day")
	}

	// Set to 7 days
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	menu = model.(*FilterMenu)
	if filter.AgeMaxDays == nil || *filter.AgeMaxDays != 7 {
		t.Error("Should set age filter to 7 days")
	}

	// Set to 30 days
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	menu = model.(*FilterMenu)
	if filter.AgeMaxDays == nil || *filter.AgeMaxDays != 30 {
		t.Error("Should set age filter to 30 days")
	}

	// Clear (All)
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	menu = model.(*FilterMenu)
	if filter.AgeMaxDays != nil {
		t.Error("Should clear age filter")
	}
}

func TestFilterMenu_ClearAll(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// Set various filters
	filter.ToggleStatus(domain.StatusOpen)
	filter.TogglePriority(domain.P0)
	filter.ToggleType(domain.TypeTask)
	filter.ToggleSessionState(domain.SessionIdle)
	filter.HideEpicChildren = true
	days := 7
	filter.AgeMaxDays = &days

	// Verify filters are active
	if !filter.IsActive() {
		t.Error("Filters should be active before clearing")
	}

	// Clear all
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	menu = model.(*FilterMenu)

	// Verify all filters are cleared
	if filter.IsActive() {
		t.Error("All filters should be cleared")
	}
	if len(filter.Status) > 0 {
		t.Error("Status filters should be cleared")
	}
	if len(filter.Priority) > 0 {
		t.Error("Priority filters should be cleared")
	}
	if len(filter.Type) > 0 {
		t.Error("Type filters should be cleared")
	}
	if len(filter.SessionState) > 0 {
		t.Error("Session state filters should be cleared")
	}
	if filter.HideEpicChildren {
		t.Error("HideEpicChildren should be false")
	}
	if filter.AgeMaxDays != nil {
		t.Error("AgeMaxDays should be nil")
	}
}

func TestFilterMenu_EscapeKey(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// From normal mode
	_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Error("Escape should trigger close overlay command")
	}

	msg := cmd()
	if _, ok := msg.(CloseOverlayMsg); !ok {
		t.Error("Should return CloseOverlayMsg")
	}
}

func TestFilterMenu_EscapeFromSubMode(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// Enter status mode
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	menu = model.(*FilterMenu)
	if menu.mode != filterModeStatus {
		t.Error("Should be in status mode")
	}

	// Escape should return to normal mode
	model, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyEsc})
	menu = model.(*FilterMenu)
	if menu.mode != filterModeNormal {
		t.Error("Escape should return to normal mode")
	}
	if cmd != nil {
		t.Error("Escape from sub-mode should not close overlay")
	}
}

func TestFilterMenu_ModeFooterHint(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// Normal mode - no hint
	view := menu.View()
	if strings.Contains(view, "Press key to toggle") {
		t.Error("Normal mode should not show selection hint")
	}

	// Status mode - should show hint
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	menu = model.(*FilterMenu)
	view = menu.View()
	if !strings.Contains(view, "Press key to toggle") {
		t.Error("Selection mode should show hint")
	}
}

func TestFilterMenu_MultipleFiltersActive(t *testing.T) {
	filter := domain.NewFilter()
	menu := NewFilterMenu(filter)

	// Enable multiple filters
	model, _ := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	menu = model.(*FilterMenu)
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	menu = model.(*FilterMenu)

	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	menu = model.(*FilterMenu)
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	menu = model.(*FilterMenu)

	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	menu = model.(*FilterMenu)
	model, _ = menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	menu = model.(*FilterMenu)

	// Verify multiple filters are active
	if !filter.Status[domain.StatusOpen] {
		t.Error("Open status should be active")
	}
	if !filter.Status[domain.StatusInProgress] {
		t.Error("InProgress status should be active")
	}
	if !filter.Priority[domain.P0] {
		t.Error("P0 priority should be active")
	}

	// View should show multiple active indicators
	view := menu.View()
	activeCount := strings.Count(view, "●")
	if activeCount < 3 {
		t.Errorf("Expected at least 3 active indicators, got %d", activeCount)
	}
}

func TestFilterMenu_ImplementsOverlayInterface(t *testing.T) {
	var _ Overlay = (*FilterMenu)(nil)
}

func TestFilterMenu_ImplementsTeaModel(t *testing.T) {
	var _ tea.Model = (*FilterMenu)(nil)
}
