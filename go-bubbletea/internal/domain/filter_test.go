package domain

import (
	"testing"
	"time"
)

func TestNewFilter(t *testing.T) {
	f := NewFilter()
	if f.IsActive() {
		t.Error("NewFilter() should create inactive filter")
	}
}

func TestFilter_IsActive(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*Filter)
		active bool
	}{
		{
			name:   "empty filter is inactive",
			setup:  func(f *Filter) {},
			active: false,
		},
		{
			name: "status filter is active",
			setup: func(f *Filter) {
				f.ToggleStatus(StatusOpen)
			},
			active: true,
		},
		{
			name: "priority filter is active",
			setup: func(f *Filter) {
				f.TogglePriority(P0)
			},
			active: true,
		},
		{
			name: "type filter is active",
			setup: func(f *Filter) {
				f.ToggleType(TypeBug)
			},
			active: true,
		},
		{
			name: "session state filter is active",
			setup: func(f *Filter) {
				f.ToggleSessionState(SessionWaiting)
			},
			active: true,
		},
		{
			name: "search query is active",
			setup: func(f *Filter) {
				f.SearchQuery = "test"
			},
			active: true,
		},
		{
			name: "hide epic children is active",
			setup: func(f *Filter) {
				f.HideEpicChildren = true
			},
			active: true,
		},
		{
			name: "age max is active",
			setup: func(f *Filter) {
				days := 7
				f.AgeMaxDays = &days
			},
			active: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFilter()
			tt.setup(f)
			if got := f.IsActive(); got != tt.active {
				t.Errorf("IsActive() = %v, want %v", got, tt.active)
			}
		})
	}
}

func TestFilter_Matches_EmptyFilter(t *testing.T) {
	f := NewFilter()
	task := Task{
		ID:       "az-1",
		Title:    "Test task",
		Status:   StatusOpen,
		Priority: P1,
		Type:     TypeTask,
	}

	if !f.Matches(task) {
		t.Error("Empty filter should match all tasks")
	}
}

func TestFilter_Matches_Status(t *testing.T) {
	f := NewFilter()
	f.ToggleStatus(StatusOpen)
	f.ToggleStatus(StatusInProgress)

	tests := []struct {
		status  Status
		matches bool
	}{
		{StatusOpen, true},
		{StatusInProgress, true},
		{StatusBlocked, false},
		{StatusDone, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			task := Task{Status: tt.status}
			if got := f.Matches(task); got != tt.matches {
				t.Errorf("Matches() = %v, want %v for status %s", got, tt.matches, tt.status)
			}
		})
	}
}

func TestFilter_Matches_Priority(t *testing.T) {
	f := NewFilter()
	f.TogglePriority(P0)
	f.TogglePriority(P1)

	tests := []struct {
		priority Priority
		matches  bool
	}{
		{P0, true},
		{P1, true},
		{P2, false},
		{P3, false},
		{P4, false},
	}

	for _, tt := range tests {
		t.Run(tt.priority.String(), func(t *testing.T) {
			task := Task{Priority: tt.priority}
			if got := f.Matches(task); got != tt.matches {
				t.Errorf("Matches() = %v, want %v for priority %s", got, tt.matches, tt.priority)
			}
		})
	}
}

func TestFilter_Matches_Type(t *testing.T) {
	f := NewFilter()
	f.ToggleType(TypeBug)
	f.ToggleType(TypeFeature)

	tests := []struct {
		taskType TaskType
		matches  bool
	}{
		{TypeBug, true},
		{TypeFeature, true},
		{TypeTask, false},
		{TypeEpic, false},
		{TypeChore, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.taskType), func(t *testing.T) {
			task := Task{Type: tt.taskType}
			if got := f.Matches(task); got != tt.matches {
				t.Errorf("Matches() = %v, want %v for type %s", got, tt.matches, tt.taskType)
			}
		})
	}
}

func TestFilter_Matches_SessionState(t *testing.T) {
	f := NewFilter()
	f.ToggleSessionState(SessionWaiting)
	f.ToggleSessionState(SessionBusy)

	tests := []struct {
		name    string
		session *Session
		matches bool
	}{
		{
			name:    "waiting session matches",
			session: &Session{State: SessionWaiting},
			matches: true,
		},
		{
			name:    "busy session matches",
			session: &Session{State: SessionBusy},
			matches: true,
		},
		{
			name:    "idle session does not match",
			session: &Session{State: SessionIdle},
			matches: false,
		},
		{
			name:    "no session does not match",
			session: nil,
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := Task{Session: tt.session}
			if got := f.Matches(task); got != tt.matches {
				t.Errorf("Matches() = %v, want %v", got, tt.matches)
			}
		})
	}
}

func TestFilter_Matches_SearchQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		task    Task
		matches bool
	}{
		{
			name:  "matches title case-insensitive",
			query: "auth",
			task: Task{
				ID:    "az-1",
				Title: "Implement Authentication",
			},
			matches: true,
		},
		{
			name:  "matches ID",
			query: "az-42",
			task: Task{
				ID:    "az-42",
				Title: "Some task",
			},
			matches: true,
		},
		{
			name:  "partial ID match",
			query: "42",
			task: Task{
				ID:    "az-42",
				Title: "Some task",
			},
			matches: true,
		},
		{
			name:  "no match",
			query: "database",
			task: Task{
				ID:    "az-1",
				Title: "Fix authentication",
			},
			matches: false,
		},
		{
			name:  "case insensitive",
			query: "FIX",
			task: Task{
				ID:    "az-1",
				Title: "fix authentication",
			},
			matches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFilter()
			f.SearchQuery = tt.query
			if got := f.Matches(tt.task); got != tt.matches {
				t.Errorf("Matches() = %v, want %v for query %q", got, tt.matches, tt.query)
			}
		})
	}
}

func TestFilter_Matches_HideEpicChildren(t *testing.T) {
	f := NewFilter()
	f.HideEpicChildren = true

	parentID := "az-epic"
	tests := []struct {
		name     string
		parentID *string
		matches  bool
	}{
		{
			name:     "task with parent is hidden",
			parentID: &parentID,
			matches:  false,
		},
		{
			name:     "task without parent is shown",
			parentID: nil,
			matches:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := Task{ParentID: tt.parentID}
			if got := f.Matches(task); got != tt.matches {
				t.Errorf("Matches() = %v, want %v", got, tt.matches)
			}
		})
	}
}

func TestFilter_Matches_AgeMaxDays(t *testing.T) {
	now := time.Now()
	maxDays := 7
	f := NewFilter()
	f.AgeMaxDays = &maxDays

	tests := []struct {
		name      string
		updatedAt time.Time
		matches   bool
	}{
		{
			name:      "updated today matches",
			updatedAt: now,
			matches:   true,
		},
		{
			name:      "updated 3 days ago matches",
			updatedAt: now.Add(-3 * 24 * time.Hour),
			matches:   true,
		},
		{
			name:      "updated 7 days ago matches (boundary)",
			updatedAt: now.Add(-7 * 24 * time.Hour),
			matches:   true,
		},
		{
			name:      "updated 8 days ago does not match",
			updatedAt: now.Add(-8 * 24 * time.Hour),
			matches:   false,
		},
		{
			name:      "updated 30 days ago does not match",
			updatedAt: now.Add(-30 * 24 * time.Hour),
			matches:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := Task{UpdatedAt: tt.updatedAt}
			if got := f.Matches(task); got != tt.matches {
				t.Errorf("Matches() = %v, want %v", got, tt.matches)
			}
		})
	}
}

func TestFilter_Matches_Combined(t *testing.T) {
	// Test AND behavior between different filter types
	f := NewFilter()
	f.ToggleStatus(StatusOpen)
	f.TogglePriority(P0)
	f.ToggleType(TypeBug)
	f.SearchQuery = "auth"

	tests := []struct {
		name    string
		task    Task
		matches bool
	}{
		{
			name: "all criteria match",
			task: Task{
				ID:       "az-1",
				Title:    "Fix authentication bug",
				Status:   StatusOpen,
				Priority: P0,
				Type:     TypeBug,
			},
			matches: true,
		},
		{
			name: "wrong status",
			task: Task{
				ID:       "az-2",
				Title:    "Fix authentication bug",
				Status:   StatusDone,
				Priority: P0,
				Type:     TypeBug,
			},
			matches: false,
		},
		{
			name: "wrong priority",
			task: Task{
				ID:       "az-3",
				Title:    "Fix authentication bug",
				Status:   StatusOpen,
				Priority: P1,
				Type:     TypeBug,
			},
			matches: false,
		},
		{
			name: "wrong type",
			task: Task{
				ID:       "az-4",
				Title:    "Fix authentication bug",
				Status:   StatusOpen,
				Priority: P0,
				Type:     TypeTask,
			},
			matches: false,
		},
		{
			name: "search does not match",
			task: Task{
				ID:       "az-5",
				Title:    "Fix database bug",
				Status:   StatusOpen,
				Priority: P0,
				Type:     TypeBug,
			},
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := f.Matches(tt.task); got != tt.matches {
				t.Errorf("Matches() = %v, want %v", got, tt.matches)
			}
		})
	}
}

func TestFilter_Apply(t *testing.T) {
	tasks := []Task{
		{ID: "az-1", Title: "Task 1", Status: StatusOpen, Priority: P0, Type: TypeBug},
		{ID: "az-2", Title: "Task 2", Status: StatusInProgress, Priority: P1, Type: TypeFeature},
		{ID: "az-3", Title: "Task 3", Status: StatusOpen, Priority: P0, Type: TypeTask},
		{ID: "az-4", Title: "Task 4", Status: StatusDone, Priority: P2, Type: TypeBug},
	}

	f := NewFilter()
	f.ToggleStatus(StatusOpen)
	f.TogglePriority(P0)

	result := f.Apply(tasks)

	// Should match az-1 and az-3 (both open and P0)
	if len(result) != 2 {
		t.Errorf("Apply() returned %d tasks, want 2", len(result))
	}

	if result[0].ID != "az-1" || result[1].ID != "az-3" {
		t.Errorf("Apply() returned wrong tasks: %v", result)
	}
}

func TestFilter_Clear(t *testing.T) {
	f := NewFilter()
	f.ToggleStatus(StatusOpen)
	f.TogglePriority(P0)
	f.ToggleType(TypeBug)
	f.SearchQuery = "test"
	f.HideEpicChildren = true
	days := 7
	f.AgeMaxDays = &days

	if !f.IsActive() {
		t.Error("Filter should be active before Clear()")
	}

	f.Clear()

	if f.IsActive() {
		t.Error("Filter should be inactive after Clear()")
	}

	// Verify all fields are cleared
	if len(f.Status) > 0 || len(f.Priority) > 0 || len(f.Type) > 0 || len(f.SessionState) > 0 {
		t.Error("Clear() should empty all filter maps")
	}
	if f.SearchQuery != "" {
		t.Error("Clear() should clear search query")
	}
	if f.HideEpicChildren {
		t.Error("Clear() should reset HideEpicChildren")
	}
	if f.AgeMaxDays != nil {
		t.Error("Clear() should clear AgeMaxDays")
	}
}

func TestFilter_Toggle(t *testing.T) {
	t.Run("ToggleStatus", func(t *testing.T) {
		f := NewFilter()

		// Toggle on
		f.ToggleStatus(StatusOpen)
		if !f.Status[StatusOpen] {
			t.Error("First toggle should enable status")
		}

		// Toggle off
		f.ToggleStatus(StatusOpen)
		if f.Status[StatusOpen] {
			t.Error("Second toggle should disable status")
		}
	})

	t.Run("TogglePriority", func(t *testing.T) {
		f := NewFilter()

		// Toggle on
		f.TogglePriority(P0)
		if !f.Priority[P0] {
			t.Error("First toggle should enable priority")
		}

		// Toggle off
		f.TogglePriority(P0)
		if f.Priority[P0] {
			t.Error("Second toggle should disable priority")
		}
	})

	t.Run("ToggleType", func(t *testing.T) {
		f := NewFilter()

		// Toggle on
		f.ToggleType(TypeBug)
		if !f.Type[TypeBug] {
			t.Error("First toggle should enable type")
		}

		// Toggle off
		f.ToggleType(TypeBug)
		if f.Type[TypeBug] {
			t.Error("Second toggle should disable type")
		}
	})

	t.Run("ToggleSessionState", func(t *testing.T) {
		f := NewFilter()

		// Toggle on
		f.ToggleSessionState(SessionWaiting)
		if !f.SessionState[SessionWaiting] {
			t.Error("First toggle should enable session state")
		}

		// Toggle off
		f.ToggleSessionState(SessionWaiting)
		if f.SessionState[SessionWaiting] {
			t.Error("Second toggle should disable session state")
		}
	})
}
