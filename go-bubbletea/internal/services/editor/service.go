// Package editor provides editing mode and view state management
package editor

import (
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/types"
)

// Re-export Mode type for convenience
type Mode = types.Mode

// Mode constants
const (
	ModeNormal = types.ModeNormal
	ModeSelect = types.ModeSelect
	ModeSearch = types.ModeSearch
	ModeGoto   = types.ModeGoto
	ModeAction = types.ModeAction
)

// Service manages editing state (mode, filter, sort, selections)
type Service struct {
	mode          Mode
	filter        *domain.Filter
	sort          *domain.Sort
	selectedTasks map[string]bool
}

// NewService creates a new editor service with defaults
func NewService() *Service {
	return &Service{
		mode:   ModeNormal,
		filter: domain.NewFilter(),
		sort: &domain.Sort{
			Field: domain.SortBySession,
			Order: domain.SortAsc,
		},
		selectedTasks: make(map[string]bool),
	}
}

// GetMode returns the current mode
func (s *Service) GetMode() Mode {
	return s.mode
}

// SetMode sets the current mode
func (s *Service) SetMode(mode Mode) {
	s.mode = mode
}

// EnterNormal switches to normal mode
func (s *Service) EnterNormal() {
	s.mode = ModeNormal
}

// EnterSelect switches to select mode
func (s *Service) EnterSelect() {
	s.mode = ModeSelect
}

// EnterSearch switches to search mode
func (s *Service) EnterSearch() {
	s.mode = ModeSearch
}

// EnterGoto switches to goto mode
func (s *Service) EnterGoto() {
	s.mode = ModeGoto
}

// EnterAction switches to action mode
func (s *Service) EnterAction() {
	s.mode = ModeAction
}

// ExitMode returns to normal mode if not already normal
func (s *Service) ExitMode() bool {
	if s.mode != ModeNormal {
		s.mode = ModeNormal
		return true
	}
	return false
}

// IsNormal returns true if in normal mode
func (s *Service) IsNormal() bool {
	return s.mode == ModeNormal
}

// IsSelect returns true if in select mode
func (s *Service) IsSelect() bool {
	return s.mode == ModeSelect
}

// IsSearch returns true if in search mode
func (s *Service) IsSearch() bool {
	return s.mode == ModeSearch
}

// IsGoto returns true if in goto mode
func (s *Service) IsGoto() bool {
	return s.mode == ModeGoto
}

// IsAction returns true if in action mode
func (s *Service) IsAction() bool {
	return s.mode == ModeAction
}

// Filter management

// GetFilter returns the current filter
func (s *Service) GetFilter() *domain.Filter {
	return s.filter
}

// SetFilter sets the filter
func (s *Service) SetFilter(filter *domain.Filter) {
	s.filter = filter
}

// SetSearchQuery updates the search query in the filter
func (s *Service) SetSearchQuery(query string) {
	s.filter.SearchQuery = query
}

// ClearSearch clears the search query
func (s *Service) ClearSearch() {
	s.filter.SearchQuery = ""
}

// ToggleStatusFilter toggles a status in the filter
func (s *Service) ToggleStatusFilter(status domain.Status) {
	if s.filter.Status[status] {
		delete(s.filter.Status, status)
	} else {
		s.filter.Status[status] = true
	}
}

// TogglePriorityFilter toggles a priority in the filter
func (s *Service) TogglePriorityFilter(priority domain.Priority) {
	if s.filter.Priority[priority] {
		delete(s.filter.Priority, priority)
	} else {
		s.filter.Priority[priority] = true
	}
}

// ToggleTypeFilter toggles a task type in the filter
func (s *Service) ToggleTypeFilter(taskType domain.TaskType) {
	if s.filter.Type[taskType] {
		delete(s.filter.Type, taskType)
	} else {
		s.filter.Type[taskType] = true
	}
}

// ToggleSessionFilter toggles a session state in the filter
func (s *Service) ToggleSessionFilter(state domain.SessionState) {
	if s.filter.SessionState[state] {
		delete(s.filter.SessionState, state)
	} else {
		s.filter.SessionState[state] = true
	}
}

// ToggleHideEpicChildren toggles the hide epic children setting
func (s *Service) ToggleHideEpicChildren() {
	s.filter.HideEpicChildren = !s.filter.HideEpicChildren
}

// SetAgeFilter sets the age filter (nil to disable)
func (s *Service) SetAgeFilter(maxDays *int) {
	s.filter.AgeMaxDays = maxDays
}

// ClearFilters clears all filters
func (s *Service) ClearFilters() {
	s.filter = domain.NewFilter()
}

// IsFilterActive returns true if any filter is active
func (s *Service) IsFilterActive() bool {
	return s.filter.IsActive()
}

// ApplyFilter filters a list of tasks
func (s *Service) ApplyFilter(tasks []domain.Task) []domain.Task {
	return s.filter.Apply(tasks)
}

// Sort management

// GetSort returns the current sort settings
func (s *Service) GetSort() *domain.Sort {
	return s.sort
}

// SetSort sets the sort settings
func (s *Service) SetSort(sort *domain.Sort) {
	s.sort = sort
}

// SetSortField sets the sort field
func (s *Service) SetSortField(field domain.SortField) {
	s.sort.Field = field
}

// SetSortOrder sets the sort order
func (s *Service) SetSortOrder(order domain.SortOrder) {
	s.sort.Order = order
}

// ToggleSort toggles between fields or direction
func (s *Service) ToggleSort(field domain.SortField) {
	s.sort.Toggle(field)
}

// ApplySort sorts a list of tasks
func (s *Service) ApplySort(tasks []domain.Task) []domain.Task {
	return s.sort.Apply(tasks)
}

// Selection management

// GetSelectedTasks returns the set of selected task IDs
func (s *Service) GetSelectedTasks() map[string]bool {
	return s.selectedTasks
}

// IsSelected returns true if the task is selected
func (s *Service) IsSelected(taskID string) bool {
	return s.selectedTasks[taskID]
}

// ToggleSelection toggles selection of a task
func (s *Service) ToggleSelection(taskID string) {
	if s.selectedTasks[taskID] {
		delete(s.selectedTasks, taskID)
	} else {
		s.selectedTasks[taskID] = true
	}
}

// Select adds a task to the selection
func (s *Service) Select(taskID string) {
	s.selectedTasks[taskID] = true
}

// Deselect removes a task from the selection
func (s *Service) Deselect(taskID string) {
	delete(s.selectedTasks, taskID)
}

// SelectAll selects all tasks from a list
func (s *Service) SelectAll(tasks []domain.Task) {
	for _, task := range tasks {
		s.selectedTasks[task.ID] = true
	}
}

// ClearSelection clears all selections
func (s *Service) ClearSelection() {
	s.selectedTasks = make(map[string]bool)
}

// SelectionCount returns the number of selected tasks
func (s *Service) SelectionCount() int {
	return len(s.selectedTasks)
}

// HasSelection returns true if any tasks are selected
func (s *Service) HasSelection() bool {
	return len(s.selectedTasks) > 0
}

// GetSelectedTasksList returns the selected task IDs as a slice
func (s *Service) GetSelectedTasksList() []string {
	result := make([]string, 0, len(s.selectedTasks))
	for id := range s.selectedTasks {
		result = append(result, id)
	}
	return result
}

// FilterAndSort applies both filter and sort to a task list
func (s *Service) FilterAndSort(tasks []domain.Task) []domain.Task {
	filtered := s.filter.Apply(tasks)
	return s.sort.Apply(filtered)
}

// FilterAndSortByStatus filters and sorts tasks, then groups by status
func (s *Service) FilterAndSortByStatus(tasks []domain.Task, status domain.Status) []domain.Task {
	var inStatus []domain.Task
	filtered := s.filter.Apply(tasks)
	for _, task := range filtered {
		if task.Status == status {
			inStatus = append(inStatus, task)
		}
	}
	return s.sort.Apply(inStatus)
}
