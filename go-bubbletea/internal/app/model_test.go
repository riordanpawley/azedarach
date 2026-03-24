package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/testprofile"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

// Helper to create a test model with tasks
func newTestModel() Model {
	cfg := &config.Config{CLITool: "claude"}
	m := New(cfg)

	// Disable placeholder data for tests so we can control the tasks
	m.usePlaceholder = false

	// Add some test tasks
	// Open column: az-1 (index 0), az-2 (index 1)
	// InProgress column: az-3 (index 0)
	// Blocked column: az-4 (index 0)
	// Done column: az-5 (index 0)
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

// Helper to get cursor position in a model
func getCursorPosition(m Model) Position {
	columns := m.buildColumns()
	return m.nav.GetPosition(columns)
}

func TestHelperMethods(t *testing.T) {
	m := newTestModel()

	t.Run("currentColumn", func(t *testing.T) {
		// Set cursor to task in Open column
		m.nav.SelectTask("az-1", 0)
		col := m.currentColumn()
		if len(col) != 2 {
			t.Errorf("Expected 2 tasks in Open column, got %d", len(col))
		}

		// Set cursor to task in InProgress column
		m.nav.SelectTask("az-3", 1)
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

	t.Run("NavigationService.GetPosition", func(t *testing.T) {
		columns := m.buildColumns()

		// Test finding task by ID
		m.nav.SelectTask("az-1", 0)
		pos := m.nav.GetPosition(columns)
		if !pos.Valid {
			t.Error("Expected valid position for az-1")
		}
		if pos.Column != 0 || pos.Task != 0 {
			t.Errorf("Expected az-1 at (0,0), got (%d,%d)", pos.Column, pos.Task)
		}

		// Test finding second task in Open column
		m.nav.SelectTask("az-2", 0)
		pos = m.nav.GetPosition(columns)
		if pos.Column != 0 || pos.Task != 1 {
			t.Errorf("Expected az-2 at (0,1), got (%d,%d)", pos.Column, pos.Task)
		}

		// Test finding task in different column
		m.nav.SelectTask("az-4", 2)
		pos = m.nav.GetPosition(columns)
		if pos.Column != 2 || pos.Task != 0 {
			t.Errorf("Expected az-4 at (2,0), got (%d,%d)", pos.Column, pos.Task)
		}

		// Test fallback when task not found
		m.nav.SelectTask("nonexistent", 1)
		pos = m.nav.GetPosition(columns)
		if pos.Column != 1 {
			t.Errorf("Expected fallback to column 1, got %d", pos.Column)
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

func TestView_CanonicalProfiles(t *testing.T) {
	profiles := []testprofile.Profile{
		testprofile.Smoke,
		testprofile.Integration,
		testprofile.Scale,
	}

	for _, profile := range profiles {
		t.Run(profile.Name, func(t *testing.T) {
			m := newTestModel()
			m.tasks = append([]domain.Task(nil), profile.Tasks...)
			m.config.Git.BaseBranch = profile.BaseBranch
			m.width = profile.Width
			m.height = profile.Height
			m.loading = false
			m.viewMode = ViewModeBoard
			m.editor.EnterNormal()

			phases := m.computePhases()
			switch profile.Name {
			case testprofile.Smoke.Name:
				phase, ok := phases["az-smoke-child"]
				if !ok || phase.Phase != 1 {
					t.Fatalf("expected smoke dependency chain to produce phase 1 child, got: %+v", phases)
				}
			case testprofile.Integration.Name:
				phase, ok := phases["az-int-child"]
				if !ok || phase.Phase != 1 {
					t.Fatalf("expected integration dependency graph to produce phase 1 child, got: %+v", phases)
				}
			case testprofile.Scale.Name:
				phase, ok := phases["az-scale-hierarchy"]
				if !ok || phase.Phase != 2 {
					t.Fatalf("expected scale dependency graph to produce deeper phase chain, got: %+v", phases)
				}
			}

			view := m.View()
			if view == "" {
				t.Fatalf("expected %s profile view to render", profile.Name)
			}
			if !strings.Contains(view, "NORMAL") {
				t.Fatalf("expected status bar mode badge to remain visible, got: %s", view)
			}
			if profile.Width < 80 && strings.Contains(view, "h/l: columns") {
				t.Fatalf("expected compact status bar without full hints for %s profile, got: %s", profile.Name, view)
			}
			if profile.Width >= 80 && !strings.Contains(view, "h/l: columns") {
				t.Fatalf("expected full hints for %s profile, got: %s", profile.Name, view)
			}
		})
	}
}

func TestNormalModeNavigation(t *testing.T) {
	m := newTestModel()

	t.Run("vertical navigation - down", func(t *testing.T) {
		// Start at first task in Open column (az-1)
		m.nav.SelectTask("az-1", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != 1 {
			t.Errorf("Expected task index 1, got %d", pos.Task)
		}
		if newModel.nav.GetCursor().TaskID != "az-2" {
			t.Errorf("Expected cursor on az-2, got %s", newModel.nav.GetCursor().TaskID)
		}
	})

	t.Run("vertical navigation - up", func(t *testing.T) {
		// Start at second task in Open column (az-2)
		m.nav.SelectTask("az-2", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != 0 {
			t.Errorf("Expected task index 0, got %d", pos.Task)
		}
		if newModel.nav.GetCursor().TaskID != "az-1" {
			t.Errorf("Expected cursor on az-1, got %s", newModel.nav.GetCursor().TaskID)
		}
	})

	t.Run("vertical navigation - up at boundary", func(t *testing.T) {
		// Start at first task in Open column (az-1)
		m.nav.SelectTask("az-1", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != 0 {
			t.Errorf("Expected task index to stay at 0, got %d", pos.Task)
		}
		if newModel.nav.GetCursor().TaskID != "az-1" {
			t.Errorf("Expected cursor to stay on az-1, got %s", newModel.nav.GetCursor().TaskID)
		}
	})

	t.Run("horizontal navigation - right", func(t *testing.T) {
		// Start at second task in Open column (az-2, index 1)
		m.nav.SelectTask("az-2", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 1 {
			t.Errorf("Expected column 1, got %d", pos.Column)
		}
		// InProgress column only has 1 task (az-3), so task index should be 0
		if pos.Task != 0 {
			t.Errorf("Expected task index to be clamped to 0, got %d", pos.Task)
		}
		if newModel.nav.GetCursor().TaskID != "az-3" {
			t.Errorf("Expected cursor on az-3, got %s", newModel.nav.GetCursor().TaskID)
		}
	})

	t.Run("horizontal navigation - left", func(t *testing.T) {
		// Start at task in InProgress column (az-3)
		m.nav.SelectTask("az-3", 1)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 0 {
			t.Errorf("Expected column 0, got %d", pos.Column)
		}
	})

	t.Run("horizontal navigation - left at boundary", func(t *testing.T) {
		// Start at first task in Open column (az-1)
		m.nav.SelectTask("az-1", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 0 {
			t.Errorf("Expected column to stay at 0, got %d", pos.Column)
		}
	})

	t.Run("horizontal navigation - right at boundary", func(t *testing.T) {
		// Start at task in Done column (az-5)
		m.nav.SelectTask("az-5", 3)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 3 {
			t.Errorf("Expected column to stay at 3, got %d", pos.Column)
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
		// Start at first task in Open column
		m.nav.SelectTask("az-1", 0)
		m.height = 24
		initialPos := getCursorPosition(m)
		initialTaskID := m.nav.GetCursor().TaskID

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlD})
		newModel := result.(Model)

		newPos := getCursorPosition(newModel)
		if newPos.Task <= initialPos.Task {
			t.Errorf("Expected task index to increase, got %d (was %d)", newPos.Task, initialPos.Task)
		}
		if newModel.nav.GetCursor().TaskID == initialTaskID {
			t.Errorf("Expected selected task to change after ctrl+d, still on %s", initialTaskID)
		}
	})

	t.Run("ctrl+u scrolls up", func(t *testing.T) {
		// Start at task 'e' (index 5) in Open column
		m.nav.SelectTask("e", 0)
		m.height = 24
		initialPos := getCursorPosition(m)
		initialTaskID := m.nav.GetCursor().TaskID

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)

		newPos := getCursorPosition(newModel)
		if newPos.Task >= initialPos.Task {
			t.Errorf("Expected task index to decrease, got %d (was %d)", newPos.Task, initialPos.Task)
		}
		if newModel.nav.GetCursor().TaskID == initialTaskID {
			t.Errorf("Expected selected task to change after ctrl+u, still on %s", initialTaskID)
		}
	})

	t.Run("ctrl+u at top stays at 0", func(t *testing.T) {
		// Start at first task
		m.nav.SelectTask("az-1", 0)
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)

		newPos := getCursorPosition(newModel)
		if newPos.Task != 0 {
			t.Errorf("Expected task index to stay at 0, got %d", newPos.Task)
		}
	})
}

func TestSelectModeHalfPageNavigation(t *testing.T) {
	m := newTestModel()

	for i := 0; i < 10; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       string(rune('a' + i)),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	m.height = 24
	m.editor.EnterSelect()

	t.Run("ctrl+d moves selection in select mode", func(t *testing.T) {
		m.nav.SelectTask("az-1", 0)
		initialTaskID := m.nav.GetCursor().TaskID
		initialPos := getCursorPosition(m)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyCtrlD})
		newModel := result.(Model)
		newPos := getCursorPosition(newModel)

		if newPos.Task <= initialPos.Task {
			t.Errorf("Expected task index to increase, got %d (was %d)", newPos.Task, initialPos.Task)
		}
		if newModel.nav.GetCursor().TaskID == initialTaskID {
			t.Errorf("Expected selected task to change after ctrl+d in select mode, still on %s", initialTaskID)
		}
	})

	t.Run("ctrl+u moves selection in select mode", func(t *testing.T) {
		m.nav.SelectTask("e", 0)
		initialTaskID := m.nav.GetCursor().TaskID
		initialPos := getCursorPosition(m)

		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)
		newPos := getCursorPosition(newModel)

		if newPos.Task >= initialPos.Task {
			t.Errorf("Expected task index to decrease, got %d (was %d)", newPos.Task, initialPos.Task)
		}
		if newModel.nav.GetCursor().TaskID == initialTaskID {
			t.Errorf("Expected selected task to change after ctrl+u in select mode, still on %s", initialTaskID)
		}
	})
}

func TestSearchOverlayLiveFilteringAndModeTransitions(t *testing.T) {
	m := newTestModel()

	updated, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if cmd == nil {
		t.Fatal("expected search overlay push command")
	}

	searchModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if !searchModel.editor.IsSearch() {
		t.Fatalf("expected search mode after '/'")
	}

	searchOverlay, ok := searchModel.overlayStack.Current().(*overlay.SearchOverlay)
	if !ok {
		t.Fatalf("expected SearchOverlay on stack, got %T", searchModel.overlayStack.Current())
	}

	updated, _ = searchModel.Update(overlay.SearchMsg{Query: "az-1"})
	searchModel, ok = updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if got := searchModel.editor.GetFilter().SearchQuery; got != "az-1" {
		t.Fatalf("search query = %q, want az-1", got)
	}

	searchOverlay, ok = searchModel.overlayStack.Current().(*overlay.SearchOverlay)
	if !ok {
		t.Fatalf("expected SearchOverlay on stack, got %T", searchModel.overlayStack.Current())
	}
	if got := searchOverlay.View(); !strings.Contains(got, "1 matches") {
		t.Fatalf("expected live match count in search overlay view, got %q", got)
	}

	updated, _ = searchModel.Update(overlay.CloseOverlayMsg{})
	searchModel, ok = updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if !searchModel.editor.IsNormal() {
		t.Fatalf("expected normal mode after closing search overlay")
	}
	if !searchModel.overlayStack.IsEmpty() {
		t.Fatal("expected search overlay stack to be empty after close")
	}
}

func TestSearchModeEscClearsQuery(t *testing.T) {
	m := newTestModel()
	m.editor.EnterSearch()
	m.editor.SetSearchQuery("az-1")

	updated, _ := m.handleSearchMode(tea.KeyMsg{Type: tea.KeyEsc})
	searchModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if !searchModel.editor.IsNormal() {
		t.Fatalf("expected normal mode after Esc in search mode")
	}
	if got := searchModel.editor.GetFilter().SearchQuery; got != "" {
		t.Fatalf("search query = %q, want empty", got)
	}
}

func TestNormalModeUpFromBottom_DoesNotTopSnapViewport(t *testing.T) {
	m := newTestModel()

	for i := 0; i < 10; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       string(rune('a' + i)),
			Title:    "Extra Task",
			Status:   domain.StatusOpen,
			Priority: domain.P3,
			Type:     domain.TypeTask,
		})
	}

	// With this height, board window shows 4 cards per column.
	m.height = 24
	// Simulate being at bottom of Open column with visible window [8..11].
	m.viewportStarts[0] = 8
	m.nav.SelectTask("j", 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyUp})
	newModel := result.(Model)

	if got := newModel.nav.GetCursor().TaskID; got != "i" {
		t.Fatalf("expected cursor to move up one task to i, got %s", got)
	}
	if newModel.viewportStarts[0] != 8 {
		t.Fatalf("expected viewport start to remain 8 after first up from bottom, got %d", newModel.viewportStarts[0])
	}
}

func TestHorizontalColumnViewportFollowsCursorOnNarrowWidth(t *testing.T) {
	m := newTestModel()
	m.width = 80

	columns := m.buildColumns()
	m.nav.SelectTask("az-1", 0)
	m.ensureCursorVisible(columns)
	if m.columnViewportStart != 0 {
		t.Fatalf("expected initial column viewport start 0, got %d", m.columnViewportStart)
	}

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRight})
	m1 := result.(Model)
	if got := getCursorPosition(m1).Column; got != 1 {
		t.Fatalf("expected cursor column 1 after first right, got %d", got)
	}
	if m1.columnViewportStart != 0 {
		t.Fatalf("expected viewport to stay at 0 while cursor remains visible, got %d", m1.columnViewportStart)
	}

	result, _ = m1.handleNormalMode(tea.KeyMsg{Type: tea.KeyRight})
	m2 := result.(Model)
	if got := getCursorPosition(m2).Column; got != 2 {
		t.Fatalf("expected cursor column 2 after second right, got %d", got)
	}
	if m2.columnViewportStart != 1 {
		t.Fatalf("expected viewport to advance to 1 at right edge, got %d", m2.columnViewportStart)
	}

	result, _ = m2.handleNormalMode(tea.KeyMsg{Type: tea.KeyRight})
	m3 := result.(Model)
	if got := getCursorPosition(m3).Column; got != 3 {
		t.Fatalf("expected cursor column 3 after third right, got %d", got)
	}
	if m3.columnViewportStart != 2 {
		t.Fatalf("expected viewport to advance to 2 at right edge, got %d", m3.columnViewportStart)
	}

	result, _ = m3.handleNormalMode(tea.KeyMsg{Type: tea.KeyLeft})
	m4 := result.(Model)
	if got := getCursorPosition(m4).Column; got != 2 {
		t.Fatalf("expected cursor column 2 after first left, got %d", got)
	}
	if m4.columnViewportStart != 2 {
		t.Fatalf("expected viewport to stay at 2 while cursor remains visible, got %d", m4.columnViewportStart)
	}

	result, _ = m4.handleNormalMode(tea.KeyMsg{Type: tea.KeyLeft})
	m5 := result.(Model)
	if got := getCursorPosition(m5).Column; got != 1 {
		t.Fatalf("expected cursor column 1 after second left, got %d", got)
	}
	if m5.columnViewportStart != 1 {
		t.Fatalf("expected viewport to move back to 1 when crossing left edge, got %d", m5.columnViewportStart)
	}
}

func TestRenderBoardView_NarrowWidthShowsVisibleColumnWindow(t *testing.T) {
	m := newTestModel()
	m.width = 80
	m.height = 24
	m.loading = false

	m.nav.SelectTask("az-1", 0)
	m.ensureCursorVisible(m.buildColumns())
	view := m.renderBoardView()
	if !strings.Contains(view, "Open (2)") {
		t.Fatalf("expected open column header in narrow view")
	}
	if !strings.Contains(view, "In Progress (1)") {
		t.Fatalf("expected in progress column header in narrow view")
	}
	if strings.Contains(view, "Blocked (1)") {
		t.Fatalf("expected blocked column to be out of view when window is on first two columns")
	}

	m.nav.SelectTask("az-4", 2)
	m.ensureCursorVisible(m.buildColumns())
	view = m.renderBoardView()
	if !strings.Contains(view, "In Progress (1)") || !strings.Contains(view, "Blocked (1)") {
		t.Fatalf("expected in progress and blocked columns in shifted narrow view")
	}
	if strings.Contains(view, "Open (2)") {
		t.Fatalf("expected open column to be out of view after horizontal shift")
	}
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
		m.editor.EnterNormal()
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		newModel := result.(Model)

		if newModel.editor.GetMode() != ModeGoto {
			t.Errorf("Expected ModeGoto, got %v", newModel.editor.GetMode())
		}
	})

	t.Run("gg goes to top", func(t *testing.T) {
		// Start at task 'e' (index 5) in Open column
		m.nav.SelectTask("e", 0)
		m.editor.EnterGoto()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != 0 {
			t.Errorf("Expected task index 0, got %d", pos.Task)
		}
		if !newModel.editor.IsNormal() {
			t.Errorf("Expected to return to ModeNormal, got %v", newModel.editor.GetMode())
		}
	})

	t.Run("ge goes to end", func(t *testing.T) {
		// Start at first task in Open column
		m.nav.SelectTask("az-1", 0)
		m.editor.EnterGoto()
		col := m.currentColumn()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Task != len(col)-1 {
			t.Errorf("Expected task index %d, got %d", len(col)-1, pos.Task)
		}
	})

	t.Run("gh goes to first column", func(t *testing.T) {
		// Start at task in Blocked column
		m.nav.SelectTask("az-4", 2)
		m.editor.EnterGoto()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 0 {
			t.Errorf("Expected column 0, got %d", pos.Column)
		}
	})

	t.Run("gl goes to last column", func(t *testing.T) {
		// Start at first task in Open column
		m.nav.SelectTask("az-1", 0)
		m.editor.EnterGoto()
		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		pos := getCursorPosition(newModel)
		if pos.Column != 3 {
			t.Errorf("Expected column 3, got %d", pos.Column)
		}
	})
}

func TestSelectModeEntry(t *testing.T) {
	m := newTestModel()

	t.Run("v enters select mode from normal", func(t *testing.T) {
		m.editor.EnterNormal()
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		newModel := result.(Model)

		if newModel.editor.GetMode() != ModeSelect {
			t.Errorf("Expected ModeSelect, got %v", newModel.editor.GetMode())
		}
	})

	t.Run("escape exits select mode back to normal", func(t *testing.T) {
		m.editor.EnterSelect()
		result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		newModel := result.(Model)

		if !newModel.editor.IsNormal() {
			t.Errorf("Expected ModeNormal after escape from select, got %v", newModel.editor.GetMode())
		}
	})
}

func TestModeTransitions(t *testing.T) {
	m := newTestModel()

	t.Run("escape exits non-normal modes", func(t *testing.T) {
		modes := []Mode{ModeGoto, ModeSearch, ModeAction, ModeSelect}

		for _, mode := range modes {
			m.editor.SetMode(mode)
			result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
			newModel := result.(Model)

			if !newModel.editor.IsNormal() {
				t.Errorf("Expected ModeNormal after escape from %v, got %v", mode, newModel.editor.GetMode())
			}
		}
	})

	t.Run("global keys work in all modes", func(t *testing.T) {
		// Test ctrl+c (quit)
		m.editor.EnterGoto()
		_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil {
			t.Error("Expected quit command, got nil")
		}

		// Test ctrl+l (clear screen)
		m.editor.EnterAction()
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

func TestIssuesLoadedReconcilesSelection(t *testing.T) {
	m := newTestModel()
	m.editor.Select("az-1")
	m.editor.Select("ghost")

	result, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug},
		},
		revision: 9,
	})
	newModel := result.(Model)

	if got := newModel.editor.SelectionCount(); got != 1 {
		t.Fatalf("selection count = %d, want 1", got)
	}
	if !newModel.editor.IsSelected("az-1") {
		t.Fatal("expected az-1 to remain selected after refresh")
	}
	if newModel.editor.IsSelected("ghost") {
		t.Fatal("expected ghost selection to be pruned after refresh")
	}
}

func TestTmuxActionsDegradeOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")

	m := newTestModel()

	msg := m.attachSessionCmd("az-1")()
	toast, ok := msg.(Toast)
	if !ok {
		t.Fatalf("attachSessionCmd() returned %T, want Toast", msg)
	}
	if toast.Level != ToastWarning {
		t.Fatalf("attachSessionCmd() toast level = %v, want warning", toast.Level)
	}
	if !strings.Contains(toast.Message, "unavailable outside tmux") {
		t.Fatalf("attachSessionCmd() toast message = %q, want tmux-unavailable guidance", toast.Message)
	}

	msg = m.viewDevServer("devserver-1")()
	toast, ok = msg.(Toast)
	if !ok {
		t.Fatalf("viewDevServer() returned %T, want Toast", msg)
	}
	if toast.Level != ToastWarning {
		t.Fatalf("viewDevServer() toast level = %v, want warning", toast.Level)
	}
	if !strings.Contains(toast.Message, "unavailable outside tmux") {
		t.Fatalf("viewDevServer() toast message = %q, want tmux-unavailable guidance", toast.Message)
	}

	m.tasks[0].Session = &domain.Session{IssueID: "az-1", Worktree: "/tmp/az-1"}
	m.nav.SelectTask("az-1", 0)

	result, _ := m.handleConflictResolution(overlay.ConflictResolutionMsg{ResolveWithClaude: true})
	newModel := result.(Model)
	if len(newModel.toasts) == 0 {
		t.Fatal("expected tmux-unavailable toast from conflict resolution")
	}
	lastToast := newModel.toasts[len(newModel.toasts)-1]
	if lastToast.Level != ToastWarning {
		t.Fatalf("conflict resolution toast level = %v, want warning", lastToast.Level)
	}
	if !strings.Contains(lastToast.Message, "unavailable outside tmux") {
		t.Fatalf("conflict resolution toast message = %q, want tmux-unavailable guidance", lastToast.Message)
	}
}
