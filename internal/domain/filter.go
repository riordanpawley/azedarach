package domain

import (
	"strings"
	"time"
)

// Filter represents task filtering state
type Filter struct {
	Status           map[Status]bool
	Priority         map[Priority]bool
	Type             map[TaskType]bool
	SessionState     map[SessionState]bool
	HideEpicChildren bool
	AgeMaxDays       *int
	SearchQuery      string
}

const defaultHideChildIssues = true

// NewFilter creates a new empty filter
func NewFilter() *Filter {
	return &Filter{
		Status:           make(map[Status]bool),
		Priority:         make(map[Priority]bool),
		Type:             make(map[TaskType]bool),
		SessionState:     make(map[SessionState]bool),
		HideEpicChildren: defaultHideChildIssues,
	}
}

// IsActive returns true if any filter is active
func (f *Filter) IsActive() bool {
	return len(f.Status) > 0 ||
		len(f.Priority) > 0 ||
		len(f.Type) > 0 ||
		len(f.SessionState) > 0 ||
		f.HideEpicChildren != defaultHideChildIssues ||
		f.AgeMaxDays != nil ||
		f.SearchQuery != ""
}

// Apply filters a list of tasks
func (f *Filter) Apply(tasks []Task) []Task {
	if len(f.Status) == 0 &&
		len(f.Priority) == 0 &&
		len(f.Type) == 0 &&
		len(f.SessionState) == 0 &&
		!f.HideEpicChildren &&
		f.AgeMaxDays == nil &&
		f.SearchQuery == "" {
		return tasks
	}

	result := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if f.Matches(task) {
			result = append(result, task)
		}
	}
	return result
}

// Matches returns true if the task passes all active filters
// Uses AND logic between filter types, OR logic within filter types
func (f *Filter) Matches(t Task) bool {
	// Status filter (OR within)
	if len(f.Status) > 0 {
		if !f.Status[t.Status] {
			return false
		}
	}

	// Priority filter (OR within)
	if len(f.Priority) > 0 {
		if !f.Priority[t.Priority] {
			return false
		}
	}

	// Type filter (OR within)
	if len(f.Type) > 0 {
		if !f.Type[t.Type] {
			return false
		}
	}

	// Session state filter (OR within)
	if len(f.SessionState) > 0 {
		if t.Session == nil {
			return false
		}
		if !f.SessionState[t.Session.State] {
			return false
		}
	}

	// Hide child issues from board-level views.
	if f.HideEpicChildren {
		if taskIsChildIssue(t) {
			return false
		}
	}

	// Age filter
	if f.AgeMaxDays != nil {
		// Calculate days since update (truncate to day boundaries for consistent comparison)
		now := time.Now().Truncate(24 * time.Hour)
		updated := t.UpdatedAt.Truncate(24 * time.Hour)
		daysSince := int(now.Sub(updated) / (24 * time.Hour))

		if daysSince > *f.AgeMaxDays {
			return false
		}
	}

	// Search query (case-insensitive, matches title or ID)
	if f.SearchQuery != "" {
		query := strings.ToLower(f.SearchQuery)
		title := strings.ToLower(t.Title)
		id := strings.ToLower(t.ID.String())

		if !strings.Contains(title, query) && !strings.Contains(id, query) {
			return false
		}
	}

	return true
}

// FilterTasksByContentQuery returns tasks matching the issue content search
// surface used by daemon-backed issue search commands.
func FilterTasksByContentQuery(tasks []Task, query string) []Task {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if TaskMatchesContentQuery(task, query) {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

// TaskMatchesContentQuery checks the durable issue fields that should be
// discoverable through content search.
func TaskMatchesContentQuery(task Task, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	fields := []string{
		task.ID.String(),
		task.Title,
		task.Description,
		task.Notes,
		task.Design,
		task.Acceptance,
		task.Assignee,
		string(task.Status),
		task.Priority.String(),
		string(task.Type),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	for _, label := range task.Labels {
		if strings.Contains(strings.ToLower(label), query) {
			return true
		}
	}
	for _, impl := range task.Implementations {
		if strings.Contains(strings.ToLower(impl), query) {
			return true
		}
	}
	return false
}

// Clear resets all filters
func (f *Filter) Clear() {
	f.Status = make(map[Status]bool)
	f.Priority = make(map[Priority]bool)
	f.Type = make(map[TaskType]bool)
	f.SessionState = make(map[SessionState]bool)
	f.HideEpicChildren = defaultHideChildIssues
	f.AgeMaxDays = nil
	f.SearchQuery = ""
}

// ToggleStatus toggles a status filter
func (f *Filter) ToggleStatus(s Status) {
	if f.Status[s] {
		delete(f.Status, s)
	} else {
		f.Status[s] = true
	}
}

// TogglePriority toggles a priority filter
func (f *Filter) TogglePriority(p Priority) {
	if f.Priority[p] {
		delete(f.Priority, p)
	} else {
		f.Priority[p] = true
	}
}

// ToggleType toggles a type filter
func (f *Filter) ToggleType(t TaskType) {
	if f.Type[t] {
		delete(f.Type, t)
	} else {
		f.Type[t] = true
	}
}

// ToggleSessionState toggles a session state filter
func (f *Filter) ToggleSessionState(s SessionState) {
	if f.SessionState[s] {
		delete(f.SessionState, s)
	} else {
		f.SessionState[s] = true
	}
}

func taskIsChildIssue(t Task) bool {
	if t.ParentID != nil && strings.TrimSpace(t.ParentID.String()) != "" {
		return true
	}
	for _, dep := range t.Dependencies {
		depType := strings.TrimSpace(string(dep.Type))
		if (depType == string(DependencyParentChild) || depType == "parent_child") && strings.TrimSpace(dep.ID.String()) != "" {
			return true
		}
	}
	return false
}
