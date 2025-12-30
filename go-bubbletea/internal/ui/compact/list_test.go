package compact

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestNewCompactView(t *testing.T) {
	tasks := []domain.Task{
		{
			ID:        "az-1",
			Title:     "Test task",
			Status:    domain.StatusOpen,
			Priority:  domain.P0,
			Type:      domain.TypeTask,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	lv := NewCompactView(tasks, 80, 20)

	if lv == nil {
		t.Fatal("Expected non-nil ListView")
	}

	if len(lv.tasks) != 1 {
		t.Errorf("Expected 1 task, got %d", len(lv.tasks))
	}

	if lv.cursor != 0 {
		t.Errorf("Expected cursor at 0, got %d", lv.cursor)
	}

	if lv.width != 80 {
		t.Errorf("Expected width 80, got %d", lv.width)
	}

	if lv.height != 20 {
		t.Errorf("Expected height 20, got %d", lv.height)
	}
}

func TestSetCursor(t *testing.T) {
	tasks := createTestTasks(5)
	lv := NewCompactView(tasks, 80, 20)

	tests := []struct {
		name     string
		index    int
		expected int
	}{
		{"Normal position", 2, 2},
		{"Negative position", -1, 0},
		{"Beyond end", 10, 4},
		{"At end", 4, 4},
		{"Zero", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lv.SetCursor(tt.index)
			if lv.cursor != tt.expected {
				t.Errorf("Expected cursor at %d, got %d", tt.expected, lv.cursor)
			}
		})
	}
}

func TestSetSelected(t *testing.T) {
	tasks := createTestTasks(3)
	lv := NewCompactView(tasks, 80, 20)

	selected := map[string]bool{
		"az-1": true,
		"az-3": true,
	}

	lv.SetSelected(selected)

	if !lv.selected["az-1"] {
		t.Error("Expected az-1 to be selected")
	}

	if lv.selected["az-2"] {
		t.Error("Expected az-2 to not be selected")
	}

	if !lv.selected["az-3"] {
		t.Error("Expected az-3 to be selected")
	}
}

func TestRenderEmpty(t *testing.T) {
	lv := NewCompactView([]domain.Task{}, 80, 20)
	output := lv.Render()

	if !strings.Contains(output, "No tasks") {
		t.Error("Expected 'No tasks' message for empty list")
	}
}

func TestRenderWithTasks(t *testing.T) {
	tasks := createTestTasks(3)
	lv := NewCompactView(tasks, 80, 20)
	output := lv.Render()

	// Check header is present
	if !strings.Contains(output, "ID") {
		t.Error("Expected header to contain 'ID'")
	}

	if !strings.Contains(output, "Title") {
		t.Error("Expected header to contain 'Title'")
	}

	if !strings.Contains(output, "Status") {
		t.Error("Expected header to contain 'Status'")
	}

	// Check separator is present
	if !strings.Contains(output, "─") {
		t.Error("Expected separator line")
	}

	// Check task IDs are present
	for _, task := range tasks {
		if !strings.Contains(output, task.ID) {
			t.Errorf("Expected output to contain task ID %s", task.ID)
		}
	}
}

func TestRenderCursor(t *testing.T) {
	tasks := createTestTasks(3)
	lv := NewCompactView(tasks, 80, 20)

	// Set cursor to second task
	lv.SetCursor(1)
	output := lv.Render()

	// Check cursor indicator is present
	if !strings.Contains(output, "▶") {
		t.Error("Expected cursor indicator '▶' in output")
	}

	// Output should have cursor on line with az-2
	lines := strings.Split(output, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "▶") && strings.Contains(line, "az-2") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected cursor indicator on line with az-2")
	}
}

func TestRenderSelection(t *testing.T) {
	tasks := createTestTasks(3)
	lv := NewCompactView(tasks, 80, 20)

	// Select first and third tasks
	lv.SetSelected(map[string]bool{
		"az-1": true,
		"az-3": true,
	})

	output := lv.Render()

	// Check selection indicator is present
	if !strings.Contains(output, "●") {
		t.Error("Expected selection indicator '●' in output")
	}

	// Count how many times the selection indicator appears
	count := strings.Count(output, "●")
	if count < 2 {
		t.Errorf("Expected at least 2 selection indicators, got %d", count)
	}
}

func TestRenderCursorAndSelection(t *testing.T) {
	tasks := createTestTasks(3)
	lv := NewCompactView(tasks, 80, 20)

	// Set cursor to first task and select it
	lv.SetCursor(0)
	lv.SetSelected(map[string]bool{
		"az-1": true,
	})

	output := lv.Render()

	// Should contain both indicators on the same line
	lines := strings.Split(output, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "●") && strings.Contains(line, "▶") && strings.Contains(line, "az-1") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected both cursor and selection indicators on line with az-1")
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		width    int
		expected string
	}{
		{"No truncation needed", "Short", 10, "Short"},
		{"Exact fit", "12345", 5, "12345"},
		{"Truncate with ellipsis", "This is a very long title", 15, "This is a ve..."},
		{"Very short width", "Long text", 5, "Lo..."},
		{"Width too small for ellipsis", "Text", 2, ".."},
		{"Width 1", "Text", 1, "."},
		{"Empty string", "", 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateString(tt.input, tt.width)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}

			// Check that result doesn't exceed width
			if len([]rune(result)) > tt.width {
				t.Errorf("Result %q exceeds width %d", result, tt.width)
			}
		})
	}
}

func TestRenderStatusAbbreviations(t *testing.T) {
	tests := []struct {
		status   domain.Status
		expected string
	}{
		{domain.StatusOpen, "open"},
		{domain.StatusInProgress, "prog"},
		{domain.StatusBlocked, "bloc"},
		{domain.StatusDone, "done"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			task := domain.Task{
				ID:        "az-test",
				Title:     "Test",
				Status:    tt.status,
				Priority:  domain.P2,
				Type:      domain.TypeTask,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}

			lv := NewCompactView([]domain.Task{task}, 80, 20)
			output := lv.Render()

			if !strings.Contains(output, tt.expected) {
				t.Errorf("Expected output to contain status abbreviation %q", tt.expected)
			}
		})
	}
}

func TestRenderPriority(t *testing.T) {
	priorities := []domain.Priority{domain.P0, domain.P1, domain.P2, domain.P3, domain.P4}

	for _, pri := range priorities {
		t.Run(pri.String(), func(t *testing.T) {
			task := domain.Task{
				ID:        "az-test",
				Title:     "Test",
				Status:    domain.StatusOpen,
				Priority:  pri,
				Type:      domain.TypeTask,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}

			lv := NewCompactView([]domain.Task{task}, 80, 20)
			output := lv.Render()

			if !strings.Contains(output, pri.String()) {
				t.Errorf("Expected output to contain priority %q", pri.String())
			}
		})
	}
}

func TestRenderSessionIcon(t *testing.T) {
	states := []domain.SessionState{
		domain.SessionBusy,
		domain.SessionWaiting,
		domain.SessionDone,
		domain.SessionError,
		domain.SessionPaused,
	}

	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			task := domain.Task{
				ID:       "az-test",
				Title:    "Test",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
				Session: &domain.Session{
					BeadID: "az-test",
					State:  state,
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}

			lv := NewCompactView([]domain.Task{task}, 80, 20)
			output := lv.Render()

			icon := state.Icon()
			if !strings.Contains(output, icon) {
				t.Errorf("Expected output to contain session icon %q for state %s", icon, state)
			}
		})
	}
}

// Helper functions

func createTestTasks(count int) []domain.Task {
	tasks := make([]domain.Task, count)
	now := time.Now()

	for i := 0; i < count; i++ {
		tasks[i] = domain.Task{
			ID:        fmt.Sprintf("az-%d", i+1),
			Title:     fmt.Sprintf("Task %d", i+1),
			Status:    domain.StatusOpen,
			Priority:  domain.Priority(i % 5),
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	return tasks
}
