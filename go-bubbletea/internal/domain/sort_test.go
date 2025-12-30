package domain

import (
	"testing"
	"time"
)

func TestSort_Toggle(t *testing.T) {
	tests := []struct {
		name      string
		initial   Sort
		toggleTo  SortField
		wantField SortField
		wantOrder SortOrder
	}{
		{
			name:      "toggle to new field sets asc",
			initial:   Sort{Field: SortByPriority, Order: SortDesc},
			toggleTo:  SortBySession,
			wantField: SortBySession,
			wantOrder: SortAsc,
		},
		{
			name:      "toggle same field asc to desc",
			initial:   Sort{Field: SortByPriority, Order: SortAsc},
			toggleTo:  SortByPriority,
			wantField: SortByPriority,
			wantOrder: SortDesc,
		},
		{
			name:      "toggle same field desc to asc",
			initial:   Sort{Field: SortByPriority, Order: SortDesc},
			toggleTo:  SortByPriority,
			wantField: SortByPriority,
			wantOrder: SortAsc,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.initial
			s.Toggle(tt.toggleTo)

			if s.Field != tt.wantField {
				t.Errorf("Toggle() field = %v, want %v", s.Field, tt.wantField)
			}
			if s.Order != tt.wantOrder {
				t.Errorf("Toggle() order = %v, want %v", s.Order, tt.wantOrder)
			}
		})
	}
}

func TestSort_Apply_Priority(t *testing.T) {
	tasks := []Task{
		{ID: "az-1", Priority: P2},
		{ID: "az-2", Priority: P0},
		{ID: "az-3", Priority: P1},
		{ID: "az-4", Priority: P4},
		{ID: "az-5", Priority: P0},
	}

	t.Run("ascending", func(t *testing.T) {
		s := Sort{Field: SortByPriority, Order: SortAsc}
		result := s.Apply(tasks)

		// P0 < P1 < P2 < P4 (lower number = higher priority, should come first in asc)
		want := []string{"az-2", "az-5", "az-3", "az-1", "az-4"}
		for i, task := range result {
			if task.ID != want[i] {
				t.Errorf("Apply()[%d] = %s, want %s", i, task.ID, want[i])
			}
		}
	})

	t.Run("descending", func(t *testing.T) {
		s := Sort{Field: SortByPriority, Order: SortDesc}
		result := s.Apply(tasks)

		// P4 > P2 > P1 > P0 (higher number = lower priority, should come first in desc)
		want := []string{"az-4", "az-1", "az-3", "az-2", "az-5"}
		for i, task := range result {
			if task.ID != want[i] {
				t.Errorf("Apply()[%d] = %s, want %s", i, task.ID, want[i])
			}
		}
	})
}

func TestSort_Apply_Updated(t *testing.T) {
	now := time.Now()
	tasks := []Task{
		{ID: "az-1", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "az-2", UpdatedAt: now.Add(-5 * time.Hour)},
		{ID: "az-3", UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: "az-4", UpdatedAt: now.Add(-10 * time.Hour)},
	}

	t.Run("ascending (oldest first)", func(t *testing.T) {
		s := Sort{Field: SortByUpdated, Order: SortAsc}
		result := s.Apply(tasks)

		want := []string{"az-4", "az-2", "az-1", "az-3"}
		for i, task := range result {
			if task.ID != want[i] {
				t.Errorf("Apply()[%d] = %s, want %s", i, task.ID, want[i])
			}
		}
	})

	t.Run("descending (newest first)", func(t *testing.T) {
		s := Sort{Field: SortByUpdated, Order: SortDesc}
		result := s.Apply(tasks)

		want := []string{"az-3", "az-1", "az-2", "az-4"}
		for i, task := range result {
			if task.ID != want[i] {
				t.Errorf("Apply()[%d] = %s, want %s", i, task.ID, want[i])
			}
		}
	})
}

func TestSort_Apply_Session(t *testing.T) {
	tasks := []Task{
		{ID: "az-1", Session: &Session{State: SessionBusy}},
		{ID: "az-2", Session: &Session{State: SessionDone}},
		{ID: "az-3", Session: &Session{State: SessionWaiting}},
		{ID: "az-4", Session: &Session{State: SessionError}},
		{ID: "az-5", Session: &Session{State: SessionPaused}},
		{ID: "az-6", Session: &Session{State: SessionIdle}},
		{ID: "az-7", Session: nil},
	}

	t.Run("ascending (waiting first)", func(t *testing.T) {
		s := Sort{Field: SortBySession, Order: SortAsc}
		result := s.Apply(tasks)

		// Waiting > Busy > Paused > Error > Done > Idle > nil
		want := []string{"az-3", "az-1", "az-5", "az-4", "az-2", "az-6", "az-7"}
		for i, task := range result {
			if task.ID != want[i] {
				t.Errorf("Apply()[%d] = %s, want %s", i, task.ID, want[i])
			}
		}
	})

	t.Run("descending (idle last)", func(t *testing.T) {
		s := Sort{Field: SortBySession, Order: SortDesc}
		result := s.Apply(tasks)

		// nil > Idle > Done > Error > Paused > Busy > Waiting
		want := []string{"az-7", "az-6", "az-2", "az-4", "az-5", "az-1", "az-3"}
		for i, task := range result {
			if task.ID != want[i] {
				t.Errorf("Apply()[%d] = %s, want %s", i, task.ID, want[i])
			}
		}
	})
}

func TestSort_SessionStatePriority(t *testing.T) {
	// Verify the priority ordering is correct
	tests := []struct {
		state    SessionState
		priority int
	}{
		{SessionWaiting, 6},
		{SessionBusy, 5},
		{SessionPaused, 4},
		{SessionError, 3},
		{SessionDone, 2},
		{SessionIdle, 1},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			got := sessionStatePriority(tt.state)
			if got != tt.priority {
				t.Errorf("sessionStatePriority(%s) = %d, want %d", tt.state, got, tt.priority)
			}
		})
	}
}

func TestSort_Apply_EmptyTasks(t *testing.T) {
	s := Sort{Field: SortByPriority, Order: SortAsc}
	result := s.Apply([]Task{})

	if len(result) != 0 {
		t.Errorf("Apply(empty) should return empty slice, got %d tasks", len(result))
	}
}

func TestSort_Apply_StableSort(t *testing.T) {
	// Tasks with same priority should maintain relative order
	tasks := []Task{
		{ID: "az-1", Priority: P1, UpdatedAt: time.Now().Add(-1 * time.Hour)},
		{ID: "az-2", Priority: P1, UpdatedAt: time.Now().Add(-2 * time.Hour)},
		{ID: "az-3", Priority: P1, UpdatedAt: time.Now().Add(-3 * time.Hour)},
	}

	s := Sort{Field: SortByPriority, Order: SortAsc}
	result := s.Apply(tasks)

	// Should maintain original order when priorities are equal
	want := []string{"az-1", "az-2", "az-3"}
	for i, task := range result {
		if task.ID != want[i] {
			t.Errorf("Apply()[%d] = %s, want %s (stable sort failed)", i, task.ID, want[i])
		}
	}
}
