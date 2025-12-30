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

// NewFilter creates a new empty filter
func NewFilter() *Filter {
	return &Filter{
		Status:       make(map[Status]bool),
		Priority:     make(map[Priority]bool),
		Type:         make(map[TaskType]bool),
		SessionState: make(map[SessionState]bool),
	}
}

// IsActive returns true if any filter is active
func (f *Filter) IsActive() bool {
	return len(f.Status) > 0 ||
		len(f.Priority) > 0 ||
		len(f.Type) > 0 ||
		len(f.SessionState) > 0 ||
		f.HideEpicChildren ||
		f.AgeMaxDays != nil ||
		f.SearchQuery != ""
}

// Apply filters a list of tasks
func (f *Filter) Apply(tasks []Task) []Task {
	if !f.IsActive() {
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

	// Hide epic children
	if f.HideEpicChildren {
		if t.ParentID != nil {
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
		id := strings.ToLower(t.ID)

		if !strings.Contains(title, query) && !strings.Contains(id, query) {
			return false
		}
	}

	return true
}

// Clear resets all filters
func (f *Filter) Clear() {
	f.Status = make(map[Status]bool)
	f.Priority = make(map[Priority]bool)
	f.Type = make(map[TaskType]bool)
	f.SessionState = make(map[SessionState]bool)
	f.HideEpicChildren = false
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
