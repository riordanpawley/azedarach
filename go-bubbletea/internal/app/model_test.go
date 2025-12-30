package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// Helper to create a test model with tasks
func newTestModel() Model {
	cfg := &config.Config{CLITool: "claude"}
	m := New(cfg)

	// Add some test tasks
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug},
		{ID: "az-3", Title: "Task 3", Status: domain.StatusInProgress, Priority: domain.P0, Type: domain.TypeFeature},
		{ID: "az-4", Title: "Task 4", Status: domain.StatusBlocked, Priority: domain.P1, Type: domain.TypeTask},
		{ID: "az-5", Title: "Task 5", Status: domain.StatusDone, Priority: domain.P3, Type: domain.TypeTask},
	}

	m.height = 24 // Set a reasonable terminal height for testing
	m.width = 80

	return m
}

func TestHelperMethods(t *testing.T) {
	m := newTestModel()

	t.Run("currentColumn", func(t *testing.T) {
		m.cursor.Column = 0 // Open column
		col := m.currentColumn()
		if len(col) != 2 {
			t.Errorf("Expected 2 tasks in Open column, got %d", len(col))
		}

		m.cursor.Column = 1 // In Progress column
		col = m.currentColumn()
		if len(col) != 1 {
			t.Errorf("Expected 1 task in In Progress column, got %d", len(col))
		}
	})

	t.Run("tasksInColumn", func(t *testing.T) {
		tasks := m.tasksInColumn(domain.StatusOpen)
		if len(tasks) != 2 {
			t.Errorf("Expected 2 tasks with StatusOpen, got %d", len(tasks))
		}

		tasks = m.tasksInColumn(domain.StatusDone)
		if len(tasks) != 1 {
			t.Errorf("Expected 1 task with StatusDone, got %d", len(tasks))
		}
	})

	t.Run("clampTaskIndex", func(t *testing.T) {
		m.cursor.Column = 0 // Open column (2 tasks)
		m.cursor.Task = 5
		clamped := m.clampTaskIndex()
		if clamped != 1 { // Should clamp to max index (1)
			t.Errorf("Expected task index 1, got %d", clamped)
		}

		m.cursor.Task = -1
		clamped = m.clampTaskIndex()
		if clamped != 0 {
			t.Errorf("Expected task index 0, got %d", clamped)
		}

		// Empty column
		m.cursor.Column = 2 // Blocked column (1 task)
		m.cursor.Task = 10
		clamped = m.clampTaskIndex()
		if clamped != 0 { // Should clamp to 0 (only one task)
			t.Errorf("Expected task index 0, got %d", clamped)
		}
	})

	t.Run("halfPage", func(t *testing.T) {
		m.height = 24
		half := m.halfPage()
		// (24 - 3) / 4 = 5 cards, half = 2
		if half < 1 {
			t.Errorf("Expected at least 1, got %d", half)
		}

		m.height = 4
		half = m.halfPage()
		if half != 1 {
			t.Errorf("Expected minimum of 1, got %d", half)
		}
	})
}

func TestNormalModeNavigation(t *testing.T) {
	m := newTestModel()

	t.Run("vertical navigation - down", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 0}
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		newModel := result.(Model)

		if newModel.cursor.Task != 1 {
			t.Errorf("Expected task index 1, got %d", newModel.cursor.Task)
		}
	})

	t.Run("vertical navigation - up", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 1}
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		newModel := result.(Model)

		if newModel.cursor.Task != 0 {
			t.Errorf("Expected task index 0, got %d", newModel.cursor.Task)
		}
	})

	t.Run("vertical navigation - up at boundary", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 0}
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		newModel := result.(Model)

		if newModel.cursor.Task != 0 {
			t.Errorf("Expected task index to stay at 0, got %d", newModel.cursor.Task)
		}
	})

	t.Run("horizontal navigation - right", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 1}
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		if newModel.cursor.Column != 1 {
			t.Errorf("Expected column 1, got %d", newModel.cursor.Column)
		}
		// Task index should be clamped
		if newModel.cursor.Task > 0 {
			t.Errorf("Expected task index to be clamped to 0, got %d", newModel.cursor.Task)
		}
	})

	t.Run("horizontal navigation - left", func(t *testing.T) {
		m.cursor = Cursor{Column: 1, Task: 0}
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		if newModel.cursor.Column != 0 {
			t.Errorf("Expected column 0, got %d", newModel.cursor.Column)
		}
	})

	t.Run("horizontal navigation - left at boundary", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 0}
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		if newModel.cursor.Column != 0 {
			t.Errorf("Expected column to stay at 0, got %d", newModel.cursor.Column)
		}
	})

	t.Run("horizontal navigation - right at boundary", func(t *testing.T) {
		m.cursor = Cursor{Column: 3, Task: 0}
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		if newModel.cursor.Column != 3 {
			t.Errorf("Expected column to stay at 3, got %d", newModel.cursor.Column)
		}
	})
}

func TestHalfPageScroll(t *testing.T) {
	m := newTestModel()

	// Add more tasks to Open column for scrolling
	for i := 0; i < 10; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       string(rune('a' + i)),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	t.Run("ctrl+d scrolls down", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 0}
		m.height = 24
		initialTask := m.cursor.Task

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlD})
		newModel := result.(Model)

		if newModel.cursor.Task <= initialTask {
			t.Errorf("Expected task index to increase, got %d (was %d)", newModel.cursor.Task, initialTask)
		}
	})

	t.Run("ctrl+u scrolls up", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 5}
		m.height = 24
		initialTask := m.cursor.Task

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)

		if newModel.cursor.Task >= initialTask {
			t.Errorf("Expected task index to decrease, got %d (was %d)", newModel.cursor.Task, initialTask)
		}
	})

	t.Run("ctrl+u at top stays at 0", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 0}
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)

		if newModel.cursor.Task != 0 {
			t.Errorf("Expected task index to stay at 0, got %d", newModel.cursor.Task)
		}
	})
}

func TestGotoMode(t *testing.T) {
	m := newTestModel()

	// Add more tasks to Open column
	for i := 0; i < 5; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       string(rune('a' + i)),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	t.Run("g enters goto mode", func(t *testing.T) {
		m.mode = ModeNormal
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		newModel := result.(Model)

		if newModel.mode != ModeGoto {
			t.Errorf("Expected ModeGoto, got %v", newModel.mode)
		}
	})

	t.Run("gg goes to top", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 5}
		m.mode = ModeGoto
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		newModel := result.(Model)

		if newModel.cursor.Task != 0 {
			t.Errorf("Expected task index 0, got %d", newModel.cursor.Task)
		}
		if newModel.mode != ModeNormal {
			t.Errorf("Expected to return to ModeNormal, got %v", newModel.mode)
		}
	})

	t.Run("ge goes to end", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 0}
		m.mode = ModeGoto
		col := m.currentColumn()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
		newModel := result.(Model)

		if newModel.cursor.Task != len(col)-1 {
			t.Errorf("Expected task index %d, got %d", len(col)-1, newModel.cursor.Task)
		}
	})

	t.Run("gh goes to first column", func(t *testing.T) {
		m.cursor = Cursor{Column: 2, Task: 0}
		m.mode = ModeGoto
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		if newModel.cursor.Column != 0 {
			t.Errorf("Expected column 0, got %d", newModel.cursor.Column)
		}
	})

	t.Run("gl goes to last column", func(t *testing.T) {
		m.cursor = Cursor{Column: 0, Task: 0}
		m.mode = ModeGoto
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		if newModel.cursor.Column != 3 {
			t.Errorf("Expected column 3, got %d", newModel.cursor.Column)
		}
	})
}

func TestModeTransitions(t *testing.T) {
	m := newTestModel()

	t.Run("escape exits non-normal modes", func(t *testing.T) {
		modes := []Mode{ModeGoto, ModeSearch, ModeAction, ModeSelect}

		for _, mode := range modes {
			m.mode = mode
			result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
			newModel := result.(Model)

			if newModel.mode != ModeNormal {
				t.Errorf("Expected ModeNormal after escape from %v, got %v", mode, newModel.mode)
			}
		}
	})

	t.Run("global keys work in all modes", func(t *testing.T) {
		// Test ctrl+c (quit)
		m.mode = ModeGoto
		_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil {
			t.Error("Expected quit command, got nil")
		}

		// Test ctrl+l (clear screen)
		m.mode = ModeAction
		_, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlL})
		if cmd == nil {
			t.Error("Expected clear screen command, got nil")
		}
	})
}

func TestModeStrings(t *testing.T) {
	tests := []struct {
		mode     Mode
		expected string
	}{
		{ModeNormal, "NORMAL"},
		{ModeSelect, "SELECT"},
		{ModeSearch, "SEARCH"},
		{ModeGoto, "GOTO"},
		{ModeAction, "ACTION"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.mode.String() != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.mode.String())
			}
		})
	}
}
