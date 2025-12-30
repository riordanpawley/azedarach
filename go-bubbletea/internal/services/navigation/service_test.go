package navigation

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/board"
)

func makeTestColumns() []board.Column {
	return []board.Column{
		{
			Title: "Open",
			Tasks: []domain.Task{
				{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen},
				{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen},
			},
		},
		{
			Title: "In Progress",
			Tasks: []domain.Task{
				{ID: "az-3", Title: "Task 3", Status: domain.StatusInProgress},
			},
		},
		{
			Title: "Blocked",
			Tasks: []domain.Task{
				{ID: "az-4", Title: "Task 4", Status: domain.StatusBlocked},
			},
		},
		{
			Title: "Done",
			Tasks: []domain.Task{
				{ID: "az-5", Title: "Task 5", Status: domain.StatusDone},
			},
		},
	}
}

func TestNewService(t *testing.T) {
	svc := NewService()
	if svc == nil {
		t.Fatal("NewService returned nil")
	}
	if svc.GetCursor() == nil {
		t.Fatal("GetCursor returned nil")
	}
}

func TestService_GetPosition(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Initially, cursor has no task selected
	pos := svc.GetPosition(columns)
	if !pos.Valid {
		t.Error("Expected valid position with tasks available")
	}
	if pos.Column != 0 {
		t.Errorf("Expected column 0, got %d", pos.Column)
	}

	// Select a specific task
	svc.SelectTask("az-3", 1)
	pos = svc.GetPosition(columns)
	if pos.Column != 1 || pos.Task != 0 {
		t.Errorf("Expected (1,0), got (%d,%d)", pos.Column, pos.Task)
	}
}

func TestService_MoveDownUp(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Start at first task
	svc.SelectTask("az-1", 0)

	// Move down
	svc.MoveDown(columns)
	pos := svc.GetPosition(columns)
	if pos.Task != 1 {
		t.Errorf("Expected task 1 after MoveDown, got %d", pos.Task)
	}

	// Move down at boundary (should stay)
	svc.MoveDown(columns)
	pos = svc.GetPosition(columns)
	if pos.Task != 1 {
		t.Errorf("Expected task 1 at boundary, got %d", pos.Task)
	}

	// Move up
	svc.MoveUp(columns)
	pos = svc.GetPosition(columns)
	if pos.Task != 0 {
		t.Errorf("Expected task 0 after MoveUp, got %d", pos.Task)
	}

	// Move up at boundary (should stay)
	svc.MoveUp(columns)
	pos = svc.GetPosition(columns)
	if pos.Task != 0 {
		t.Errorf("Expected task 0 at boundary, got %d", pos.Task)
	}
}

func TestService_MoveLeftRight(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Start at first task in Open column
	svc.SelectTask("az-1", 0)

	// Move right
	svc.MoveRight(columns)
	pos := svc.GetPosition(columns)
	if pos.Column != 1 {
		t.Errorf("Expected column 1 after MoveRight, got %d", pos.Column)
	}

	// Move left
	svc.MoveLeft(columns)
	pos = svc.GetPosition(columns)
	if pos.Column != 0 {
		t.Errorf("Expected column 0 after MoveLeft, got %d", pos.Column)
	}

	// Move left at boundary (should stay at 0)
	svc.MoveLeft(columns)
	pos = svc.GetPosition(columns)
	if pos.Column != 0 {
		t.Errorf("Expected column 0 at boundary, got %d", pos.Column)
	}
}

func TestService_GotoTopBottom(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Start at second task
	svc.SelectTask("az-2", 0)
	pos := svc.GetPosition(columns)
	if pos.Task != 1 {
		t.Errorf("Expected task 1, got %d", pos.Task)
	}

	// Goto top
	svc.GotoTop(columns)
	pos = svc.GetPosition(columns)
	if pos.Task != 0 {
		t.Errorf("Expected task 0 after GotoTop, got %d", pos.Task)
	}

	// Goto bottom
	svc.GotoBottom(columns)
	pos = svc.GetPosition(columns)
	if pos.Task != 1 {
		t.Errorf("Expected task 1 after GotoBottom, got %d", pos.Task)
	}
}

func TestService_GotoFirstLastColumn(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Start in middle column
	svc.SelectTask("az-3", 1)

	// Goto first column
	svc.GotoFirstColumn(columns)
	pos := svc.GetPosition(columns)
	if pos.Column != 0 {
		t.Errorf("Expected column 0, got %d", pos.Column)
	}

	// Goto last column
	svc.GotoLastColumn(columns)
	pos = svc.GetPosition(columns)
	if pos.Column != 3 {
		t.Errorf("Expected column 3, got %d", pos.Column)
	}
}

func TestService_JumpToTaskByIndex(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Jump to flat index 3 (az-4 in Blocked column)
	// Flat: az-1(0), az-2(1), az-3(2), az-4(3), az-5(4)
	found := svc.JumpToTaskByIndex(columns, 3)
	if !found {
		t.Error("Expected to find task at index 3")
	}

	pos := svc.GetPosition(columns)
	if pos.Column != 2 {
		t.Errorf("Expected column 2, got %d", pos.Column)
	}

	cursor := svc.GetCursor()
	if cursor.TaskID != "az-4" {
		t.Errorf("Expected TaskID 'az-4', got '%s'", cursor.TaskID)
	}
}

func TestService_JumpToTaskByID(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Jump to az-5 in Done column
	found := svc.JumpToTaskByID(columns, "az-5")
	if !found {
		t.Error("Expected to find task az-5")
	}

	pos := svc.GetPosition(columns)
	if pos.Column != 3 {
		t.Errorf("Expected column 3, got %d", pos.Column)
	}

	// Try to jump to non-existent task
	found = svc.JumpToTaskByID(columns, "nonexistent")
	if found {
		t.Error("Should not find nonexistent task")
	}
}

func TestService_GetCurrentTask(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Select a task
	svc.SelectTask("az-3", 1)

	task, session := svc.GetCurrentTask(columns)
	if task == nil {
		t.Fatal("Expected task, got nil")
	}

	if task.ID != "az-3" {
		t.Errorf("Expected task ID 'az-3', got '%s'", task.ID)
	}

	// Session should be nil for our test data
	if session != nil {
		t.Error("Expected nil session for test task")
	}
}

func TestService_GetCurrentStatus(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	tests := []struct {
		taskID         string
		column         int
		expectedStatus domain.Status
	}{
		{"az-1", 0, domain.StatusOpen},
		{"az-3", 1, domain.StatusInProgress},
		{"az-4", 2, domain.StatusBlocked},
		{"az-5", 3, domain.StatusDone},
	}

	for _, tt := range tests {
		svc.SelectTask(tt.taskID, tt.column)
		status := svc.GetCurrentStatus(columns)
		if status != tt.expectedStatus {
			t.Errorf("For task %s: expected status %v, got %v", tt.taskID, tt.expectedStatus, status)
		}
	}
}

func TestService_HalfPageScroll(t *testing.T) {
	// Create a column with many tasks
	columns := []board.Column{
		{
			Title: "Open",
			Tasks: []domain.Task{
				{ID: "t-1"}, {ID: "t-2"}, {ID: "t-3"}, {ID: "t-4"}, {ID: "t-5"},
				{ID: "t-6"}, {ID: "t-7"}, {ID: "t-8"}, {ID: "t-9"}, {ID: "t-10"},
			},
		},
	}

	svc := NewService()
	svc.SelectTask("t-1", 0)

	// Half page down with page size 3
	svc.HalfPageDown(columns, 3)
	pos := svc.GetPosition(columns)
	if pos.Task != 3 {
		t.Errorf("Expected task 3, got %d", pos.Task)
	}

	// Half page up
	svc.HalfPageUp(columns, 3)
	pos = svc.GetPosition(columns)
	if pos.Task != 0 {
		t.Errorf("Expected task 0, got %d", pos.Task)
	}
}

func TestCursor_EmptyColumns(t *testing.T) {
	columns := []board.Column{
		{Title: "Empty", Tasks: []domain.Task{}},
	}

	svc := NewService()
	pos := svc.GetPosition(columns)

	// Should return invalid position for empty columns
	if pos.Valid {
		t.Error("Expected invalid position for empty columns")
	}
}

func TestCursor_TaskNotFound(t *testing.T) {
	svc := NewService()
	columns := makeTestColumns()

	// Set cursor to a task that doesn't exist
	svc.SelectTask("nonexistent", 2)

	pos := svc.GetPosition(columns)
	// Should fall back to column 2
	if pos.Column != 2 {
		t.Errorf("Expected fallback to column 2, got %d", pos.Column)
	}
}
