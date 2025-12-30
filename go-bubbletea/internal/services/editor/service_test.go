package editor

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestNewService(t *testing.T) {
	svc := NewService()
	if svc == nil {
		t.Fatal("NewService returned nil")
	}

	if svc.GetMode() != ModeNormal {
		t.Errorf("Expected ModeNormal, got %v", svc.GetMode())
	}

	if svc.GetFilter() == nil {
		t.Error("Expected non-nil filter")
	}

	if svc.GetSort() == nil {
		t.Error("Expected non-nil sort")
	}
}

func TestService_ModeTransitions(t *testing.T) {
	svc := NewService()

	// Test enter modes
	tests := []struct {
		name     string
		enter    func()
		check    func() bool
		expected Mode
	}{
		{"EnterSelect", svc.EnterSelect, svc.IsSelect, ModeSelect},
		{"EnterSearch", svc.EnterSearch, svc.IsSearch, ModeSearch},
		{"EnterGoto", svc.EnterGoto, svc.IsGoto, ModeGoto},
		{"EnterAction", svc.EnterAction, svc.IsAction, ModeAction},
		{"EnterNormal", svc.EnterNormal, svc.IsNormal, ModeNormal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.enter()
			if svc.GetMode() != tt.expected {
				t.Errorf("Expected mode %v, got %v", tt.expected, svc.GetMode())
			}
			if !tt.check() {
				t.Errorf("Check function returned false for %s", tt.name)
			}
		})
	}
}

func TestService_ExitMode(t *testing.T) {
	svc := NewService()

	// From normal mode, ExitMode should return false
	if svc.ExitMode() {
		t.Error("ExitMode from Normal should return false")
	}

	// From other modes, ExitMode should return true and switch to normal
	svc.EnterSelect()
	if !svc.ExitMode() {
		t.Error("ExitMode from Select should return true")
	}
	if !svc.IsNormal() {
		t.Error("Should be in Normal mode after ExitMode")
	}
}

func TestService_SearchQuery(t *testing.T) {
	svc := NewService()

	svc.SetSearchQuery("test query")
	if svc.GetFilter().SearchQuery != "test query" {
		t.Errorf("Expected 'test query', got '%s'", svc.GetFilter().SearchQuery)
	}

	svc.ClearSearch()
	if svc.GetFilter().SearchQuery != "" {
		t.Error("Expected empty search query after ClearSearch")
	}
}

func TestService_ToggleFilters(t *testing.T) {
	svc := NewService()

	// Toggle status filter
	svc.ToggleStatusFilter(domain.StatusOpen)
	if !svc.GetFilter().Status[domain.StatusOpen] {
		t.Error("Expected StatusOpen to be toggled on")
	}
	svc.ToggleStatusFilter(domain.StatusOpen)
	if svc.GetFilter().Status[domain.StatusOpen] {
		t.Error("Expected StatusOpen to be toggled off")
	}

	// Toggle priority filter
	svc.TogglePriorityFilter(domain.P0)
	if !svc.GetFilter().Priority[domain.P0] {
		t.Error("Expected P0 to be toggled on")
	}

	// Toggle type filter
	svc.ToggleTypeFilter(domain.TypeEpic)
	if !svc.GetFilter().Type[domain.TypeEpic] {
		t.Error("Expected TypeEpic to be toggled on")
	}

	// Toggle session filter
	svc.ToggleSessionFilter(domain.SessionBusy)
	if !svc.GetFilter().SessionState[domain.SessionBusy] {
		t.Error("Expected SessionBusy to be toggled on")
	}

	// Toggle hide epic children
	svc.ToggleHideEpicChildren()
	if !svc.GetFilter().HideEpicChildren {
		t.Error("Expected HideEpicChildren to be true")
	}
	svc.ToggleHideEpicChildren()
	if svc.GetFilter().HideEpicChildren {
		t.Error("Expected HideEpicChildren to be false")
	}
}

func TestService_AgeFilter(t *testing.T) {
	svc := NewService()

	days := 7
	svc.SetAgeFilter(&days)
	if svc.GetFilter().AgeMaxDays == nil || *svc.GetFilter().AgeMaxDays != 7 {
		t.Error("Expected AgeMaxDays to be 7")
	}

	svc.SetAgeFilter(nil)
	if svc.GetFilter().AgeMaxDays != nil {
		t.Error("Expected AgeMaxDays to be nil")
	}
}

func TestService_ClearFilters(t *testing.T) {
	svc := NewService()

	// Set various filters
	svc.ToggleStatusFilter(domain.StatusOpen)
	svc.TogglePriorityFilter(domain.P0)
	svc.SetSearchQuery("test")
	svc.ToggleHideEpicChildren()

	if !svc.IsFilterActive() {
		t.Error("Expected filter to be active")
	}

	svc.ClearFilters()

	if svc.IsFilterActive() {
		t.Error("Expected filter to be inactive after clear")
	}
}

func TestService_Sort(t *testing.T) {
	svc := NewService()

	// Default sort
	if svc.GetSort().Field != domain.SortBySession {
		t.Errorf("Expected default sort by session, got %v", svc.GetSort().Field)
	}

	// Change sort field
	svc.SetSortField(domain.SortByPriority)
	if svc.GetSort().Field != domain.SortByPriority {
		t.Error("Expected sort by priority")
	}

	// Change sort order
	svc.SetSortOrder(domain.SortDesc)
	if svc.GetSort().Order != domain.SortDesc {
		t.Error("Expected sort order desc")
	}

	// Toggle sort (same field toggles direction)
	svc.ToggleSort(domain.SortByPriority)
	if svc.GetSort().Order != domain.SortAsc {
		t.Error("Expected sort order to toggle back to asc")
	}

	// Toggle sort (different field changes field)
	svc.ToggleSort(domain.SortByUpdated)
	if svc.GetSort().Field != domain.SortByUpdated {
		t.Error("Expected sort field to change to updated")
	}
}

func TestService_Selection(t *testing.T) {
	svc := NewService()

	// Initially empty
	if svc.HasSelection() {
		t.Error("Expected no selection initially")
	}
	if svc.SelectionCount() != 0 {
		t.Error("Expected selection count 0")
	}

	// Select a task
	svc.Select("task-1")
	if !svc.IsSelected("task-1") {
		t.Error("Expected task-1 to be selected")
	}
	if svc.SelectionCount() != 1 {
		t.Error("Expected selection count 1")
	}

	// Toggle selection
	svc.ToggleSelection("task-1")
	if svc.IsSelected("task-1") {
		t.Error("Expected task-1 to be deselected after toggle")
	}

	svc.ToggleSelection("task-2")
	if !svc.IsSelected("task-2") {
		t.Error("Expected task-2 to be selected after toggle")
	}

	// Deselect
	svc.Deselect("task-2")
	if svc.IsSelected("task-2") {
		t.Error("Expected task-2 to be deselected")
	}

	// Select all
	tasks := []domain.Task{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	svc.SelectAll(tasks)
	if svc.SelectionCount() != 3 {
		t.Errorf("Expected 3 selected, got %d", svc.SelectionCount())
	}

	// Get selected list
	selected := svc.GetSelectedTasksList()
	if len(selected) != 3 {
		t.Errorf("Expected 3 in list, got %d", len(selected))
	}

	// Clear selection
	svc.ClearSelection()
	if svc.HasSelection() {
		t.Error("Expected no selection after clear")
	}
}

func TestService_ApplyFilter(t *testing.T) {
	svc := NewService()

	tasks := []domain.Task{
		{ID: "1", Status: domain.StatusOpen, Priority: domain.P0},
		{ID: "2", Status: domain.StatusOpen, Priority: domain.P1},
		{ID: "3", Status: domain.StatusDone, Priority: domain.P0},
		{ID: "4", Status: domain.StatusDone, Priority: domain.P1},
	}

	// No filter
	filtered := svc.ApplyFilter(tasks)
	if len(filtered) != 4 {
		t.Errorf("Expected 4 tasks without filter, got %d", len(filtered))
	}

	// Filter by status
	svc.ToggleStatusFilter(domain.StatusOpen)
	filtered = svc.ApplyFilter(tasks)
	if len(filtered) != 2 {
		t.Errorf("Expected 2 open tasks, got %d", len(filtered))
	}

	// Add priority filter
	svc.TogglePriorityFilter(domain.P0)
	filtered = svc.ApplyFilter(tasks)
	if len(filtered) != 1 {
		t.Errorf("Expected 1 task (open AND P0), got %d", len(filtered))
	}
}

func TestService_FilterAndSort(t *testing.T) {
	svc := NewService()

	tasks := []domain.Task{
		{ID: "1", Status: domain.StatusOpen, Priority: domain.P2},
		{ID: "2", Status: domain.StatusOpen, Priority: domain.P0},
		{ID: "3", Status: domain.StatusDone, Priority: domain.P1},
	}

	// Filter to open only, sort by priority
	svc.ToggleStatusFilter(domain.StatusOpen)
	svc.SetSortField(domain.SortByPriority)
	svc.SetSortOrder(domain.SortAsc)

	result := svc.FilterAndSort(tasks)
	if len(result) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(result))
	}

	// Verify sorted by priority (P0 before P2)
	if result[0].Priority != domain.P0 {
		t.Error("Expected first task to be P0")
	}
}

func TestService_FilterAndSortByStatus(t *testing.T) {
	svc := NewService()

	tasks := []domain.Task{
		{ID: "1", Status: domain.StatusOpen, Priority: domain.P2},
		{ID: "2", Status: domain.StatusOpen, Priority: domain.P0},
		{ID: "3", Status: domain.StatusDone, Priority: domain.P1},
		{ID: "4", Status: domain.StatusOpen, Priority: domain.P1},
	}

	svc.SetSortField(domain.SortByPriority)
	svc.SetSortOrder(domain.SortAsc)

	result := svc.FilterAndSortByStatus(tasks, domain.StatusOpen)
	if len(result) != 3 {
		t.Errorf("Expected 3 open tasks, got %d", len(result))
	}

	// Verify sorted by priority
	if result[0].Priority != domain.P0 {
		t.Error("Expected first task to be P0")
	}
	if result[1].Priority != domain.P1 {
		t.Error("Expected second task to be P1")
	}
	if result[2].Priority != domain.P2 {
		t.Error("Expected third task to be P2")
	}
}
