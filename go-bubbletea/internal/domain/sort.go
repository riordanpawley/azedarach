package domain

import "sort"

// SortField represents a field to sort by
type SortField string

const (
	SortBySession  SortField = "session"
	SortByPriority SortField = "priority"
	SortByUpdated  SortField = "updated"
)

// SortOrder represents sort direction
type SortOrder int

const (
	SortAsc SortOrder = iota
	SortDesc
)

// Sort represents sorting state
type Sort struct {
	Field SortField
	Order SortOrder
}

// Toggle toggles the sort field or direction
// If field is different, sets new field with ascending order
// If field is same, toggles between ascending and descending
func (s *Sort) Toggle(field SortField) {
	if s.Field == field {
		// Toggle order
		if s.Order == SortAsc {
			s.Order = SortDesc
		} else {
			s.Order = SortAsc
		}
	} else {
		// New field, default to ascending
		s.Field = field
		s.Order = SortAsc
	}
}

// Apply sorts a list of tasks
func (s *Sort) Apply(tasks []Task) []Task {
	if len(tasks) == 0 {
		return tasks
	}

	// Make a copy to avoid modifying the input slice
	result := make([]Task, len(tasks))
	copy(result, tasks)

	// Sort based on field
	switch s.Field {
	case SortByPriority:
		sort.SliceStable(result, func(i, j int) bool {
			if s.Order == SortAsc {
				return result[i].Priority < result[j].Priority
			}
			return result[i].Priority > result[j].Priority
		})

	case SortByUpdated:
		sort.SliceStable(result, func(i, j int) bool {
			if s.Order == SortAsc {
				return result[i].UpdatedAt.Before(result[j].UpdatedAt)
			}
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		})

	case SortBySession:
		sort.SliceStable(result, func(i, j int) bool {
			pi := sessionStatePriority(getSessionState(result[i]))
			pj := sessionStatePriority(getSessionState(result[j]))

			if s.Order == SortAsc {
				return pi > pj // Higher priority first in ascending
			}
			return pi < pj // Lower priority first in descending
		})
	}

	return result
}

// getSessionState returns the session state for a task, handling nil sessions
func getSessionState(t Task) SessionState {
	if t.Session == nil {
		return "" // Empty string for nil session
	}
	return t.Session.State
}

// sessionStatePriority returns the priority value for session states
// Higher values = higher priority (should appear first in ascending sort)
// Waiting (highest) > Busy > Paused > Error > Done > Idle (lowest)
func sessionStatePriority(state SessionState) int {
	switch state {
	case SessionWaiting:
		return 6
	case SessionBusy:
		return 5
	case SessionPaused:
		return 4
	case SessionError:
		return 3
	case SessionDone:
		return 2
	case SessionIdle:
		return 1
	default:
		return 0 // nil session
	}
}
