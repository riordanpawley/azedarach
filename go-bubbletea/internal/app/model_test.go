package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/testprofile"
	"github.com/riordanpawley/azedarach/internal/ui/board"
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

func TestResolveDaemonBinaryForRepo(t *testing.T) {
	t.Run("prefers azd sibling of running az executable", func(t *testing.T) {
		repoDir := t.TempDir()
		execDir := t.TempDir()
		azPath := filepath.Join(execDir, "az")
		azdPath := filepath.Join(execDir, "azd")
		if err := os.WriteFile(azPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write az fixture: %v", err)
		}
		if err := os.WriteFile(azdPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write azd fixture: %v", err)
		}

		origExecutablePath := executablePath
		t.Cleanup(func() { executablePath = origExecutablePath })
		executablePath = func() (string, error) { return azPath, nil }

		if got := resolveDaemonBinaryForRepo(repoDir); got != azdPath {
			t.Fatalf("expected %q, got %q", azdPath, got)
		}
	})

	t.Run("falls back to repo local bin azd when executable sibling missing", func(t *testing.T) {
		repoDir := t.TempDir()
		binDir := filepath.Join(repoDir, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("mkdir bin dir: %v", err)
		}
		azdPath := filepath.Join(binDir, "azd")
		if err := os.WriteFile(azdPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write azd binary fixture: %v", err)
		}

		origExecutablePath := executablePath
		t.Cleanup(func() { executablePath = origExecutablePath })
		executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "az"), nil }

		if got := resolveDaemonBinaryForRepo(repoDir); got != azdPath {
			t.Fatalf("expected %q, got %q", azdPath, got)
		}
	})

	t.Run("returns empty when neither source has azd", func(t *testing.T) {
		repoDir := t.TempDir()
		origExecutablePath := executablePath
		t.Cleanup(func() { executablePath = origExecutablePath })
		executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "az"), nil }

		if got := resolveDaemonBinaryForRepo(repoDir); got != "" {
			t.Fatalf("expected empty path, got %q", got)
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

func TestView_ShowsHiddenSelectionCount(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.viewMode = ViewModeBoard
	m.editor.Select("az-1")
	m.editor.Select("az-2")
	m.editor.ToggleStatusFilter(domain.StatusDone)

	view := m.View()
	if !strings.Contains(view, "Selected: 2 (2 hidden)") {
		t.Fatalf("view = %q, want hidden selection count", view)
	}
	if !strings.Contains(view, "NORMAL") {
		t.Fatalf("view = %q, want normal mode badge", view)
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

	t.Run("navigation works while loading", func(t *testing.T) {
		m := newTestModel()
		m.loading = true
		m.nav.SelectTask("az-1", 0)

		result, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		newModel := result.(Model)

		if cmd != nil {
			t.Fatalf("expected no command while navigating during loading, got %T", cmd)
		}
		if !newModel.loading {
			t.Fatal("expected loading state to remain true before hydration completes")
		}
		if newModel.nav.GetCursor().TaskID != "az-2" {
			t.Fatalf("expected navigation to advance to az-2 while loading, got %s", newModel.nav.GetCursor().TaskID)
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

	t.Run("sustained down input stays responsive on large lists", func(t *testing.T) {
		m := newTestModel()
		for i := 0; i < 50; i++ {
			m.tasks = append(m.tasks, domain.Task{
				ID:       string(rune('a'+i%26)) + string(rune('A'+i/26)),
				Title:    "Extra Task",
				Status:   domain.StatusOpen,
				Priority: domain.P3,
				Type:     domain.TypeTask,
			})
		}
		m.height = 24
		m.nav.SelectTask("az-1", 0)

		seen := map[string]struct{}{"az-1": {}}
		for i := 0; i < 20; i++ {
			result, cmd := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyDown})
			if cmd != nil {
				t.Fatalf("expected no command during sustained scroll input, got %T", cmd)
			}
			nextModel, ok := result.(Model)
			if !ok {
				t.Fatalf("updated model type = %T, want Model", result)
			}
			m = nextModel
			got := m.nav.GetCursor().TaskID
			if got == "" {
				t.Fatal("expected cursor to remain on a task while scrolling")
			}
			seen[got] = struct{}{}
		}

		if len(seen) < 5 {
			t.Fatalf("expected cursor to advance across multiple tasks, saw %d distinct ids", len(seen))
		}
		if view := m.View(); view == "" {
			t.Fatal("expected view to remain renderable during sustained scroll input")
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

	for _, r := range []rune{'a', 'z', '-', '1'} {
		_, _ = searchOverlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
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

func TestEventLogHotkeyPushesOverlay(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.addToast(Toast{
		Level:   ToastInfo,
		Message: "event seed",
		Expires: time.Now().Add(5 * time.Second),
	})

	updated, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})

	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}

	current := next.overlayStack.Current()
	logOverlay, ok := current.(*overlay.EventLogOverlay)
	if !ok {
		t.Fatalf("expected EventLogOverlay on stack, got %T", current)
	}

	if view := logOverlay.View(); !strings.Contains(view, "ui.toast") {
		t.Fatalf("expected event log to render runtime events, got %q", view)
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

	m.height = 24
	m.viewportStarts[0] = 8

	columns := m.buildColumns()
	availableHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	columnCount := board.VisibleColumnCount(len(columns), m.width)
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := m.width / columnCount
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))
	initialStart, initialEnd := board.VisibleTaskWindow(len(columns[0].Tasks), m.viewportStarts[0], availableHeight, linesPerCard)
	if initialEnd-initialStart < 2 {
		t.Fatalf("expected at least two visible tasks in initial window, got [%d,%d)", initialStart, initialEnd)
	}

	lastVisible := initialEnd - 1
	m.nav.SelectTask(columns[0].Tasks[lastVisible].ID, 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyUp})
	newModel := result.(Model)

	expectedTask := columns[0].Tasks[lastVisible-1].ID
	if got := newModel.nav.GetCursor().TaskID; got != expectedTask {
		t.Fatalf("expected cursor to move up one task to %s, got %s", expectedTask, got)
	}
	if newModel.viewportStarts[0] != initialStart {
		t.Fatalf("expected viewport start to remain %d after first up from bottom, got %d", initialStart, newModel.viewportStarts[0])
	}
}

func TestNormalModeDown_KeepsCursorVisibleWithIndicators(t *testing.T) {
	m := newTestModel()

	for i := 0; i < 30; i++ {
		m.tasks = append(m.tasks, domain.Task{
			ID:       fmt.Sprintf("open-%02d", i),
			Title:    fmt.Sprintf("Open Task %02d", i),
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}

	m.height = 24
	m.width = 80

	columns := m.buildColumns()
	if len(columns) == 0 || len(columns[0].Tasks) < 4 {
		t.Fatalf("expected enough open-column tasks for viewport test")
	}

	availableHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	columnCount := board.VisibleColumnCount(len(columns), m.width)
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := m.width / columnCount
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))

	start := len(columns[0].Tasks) / 2
	windowStart, windowEnd := board.VisibleTaskWindow(len(columns[0].Tasks), start, availableHeight, linesPerCard)
	if windowEnd-windowStart < 2 {
		t.Fatalf("expected at least two visible tasks in test window, got [%d,%d)", windowStart, windowEnd)
	}

	m.viewportStarts[0] = start
	m.nav.SelectTask(columns[0].Tasks[windowEnd-1].ID, 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyDown})
	next := result.(Model)

	nextColumns := next.buildColumns()
	pos := next.nav.GetPosition(nextColumns)
	if !pos.Valid || pos.Column != 0 {
		t.Fatalf("expected valid cursor in open column after moving down; got %+v", pos)
	}

	nextStart, nextEnd := board.VisibleTaskWindow(len(nextColumns[0].Tasks), next.viewportStarts[0], availableHeight, linesPerCard)
	if pos.Task < nextStart || pos.Task >= nextEnd {
		t.Fatalf(
			"cursor task index %d not visible in indicator-aware window [%d,%d) with viewport start %d",
			pos.Task,
			nextStart,
			nextEnd,
			next.viewportStarts[0],
		)
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

	t.Run("g boundary keys jump to expected positions", func(t *testing.T) {
		boundaryModel := newTestModel()
		for i := 0; i < 5; i++ {
			boundaryModel.tasks = append(boundaryModel.tasks, domain.Task{
				ID:       fmt.Sprintf("boundary-%d", i),
				Title:    "Boundary Task",
				Status:   domain.StatusOpen,
				Priority: domain.P3,
				Type:     domain.TypeTask,
			})
		}

		boundaryModel.nav.SelectTask("az-1", 0)
		boundaryModel.editor.EnterGoto()
		result, _ := boundaryModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
		next := result.(Model)
		pos := getCursorPosition(next)
		if pos.Column != 0 || pos.Task != len(boundaryModel.currentColumn())-1 {
			t.Fatalf("g e position = (%d,%d), want bottom of current column", pos.Column, pos.Task)
		}

		boundaryModel.nav.SelectTask("az-4", 2)
		boundaryModel.editor.EnterGoto()
		result, _ = boundaryModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
		next = result.(Model)
		pos = getCursorPosition(next)
		if pos.Column != 0 {
			t.Fatalf("g h column = %d, want 0", pos.Column)
		}

		boundaryModel.nav.SelectTask("az-1", 0)
		boundaryModel.editor.EnterGoto()
		result, _ = boundaryModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		next = result.(Model)
		pos = getCursorPosition(next)
		if pos.Column != 3 {
			t.Fatalf("g l column = %d, want 3", pos.Column)
		}
	})

	t.Run("gw opens jump labels and selects a double-char target", func(t *testing.T) {
		jumpModel := newTestModel()
		jumpModel.width = 120
		jumpModel.height = 120
		jumpModel.editor.EnterGoto()
		jumpModel.nav.SelectTask("az-1", 0)

		for i := 0; i < 9; i++ {
			jumpModel.tasks = append(jumpModel.tasks, domain.Task{
				ID:       fmt.Sprintf("jump-%02d", i),
				Title:    fmt.Sprintf("Jump Task %02d", i),
				Status:   domain.StatusOpen,
				Priority: domain.P3,
				Type:     domain.TypeTask,
			})
		}

		result, _ := jumpModel.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
		newModel := result.(Model)

		current := newModel.overlayStack.Current()
		if current == nil {
			t.Fatal("expected jump overlay to be pushed")
		}
		jump, ok := current.(*overlay.JumpMode)
		if !ok {
			t.Fatalf("overlay type = %T, want *overlay.JumpMode", current)
		}
		if got, want := jump.GetLabel(0), "a"; got != want {
			t.Fatalf("label 0 = %q, want %q", got, want)
		}
		if got, want := jump.GetLabel(10), "aa"; got != want {
			t.Fatalf("label 10 = %q, want %q", got, want)
		}

		seen := make(map[string]int, 11)
		for i := 0; i < 11; i++ {
			label := jump.GetLabel(i)
			if label == "" {
				t.Fatalf("label %d is empty", i)
			}
			if prev, ok := seen[label]; ok {
				t.Fatalf("label %q reused for indexes %d and %d", label, prev, i)
			}
			seen[label] = i
		}
		if got := len(seen); got != 11 {
			t.Fatalf("unique label count = %d, want 11", got)
		}

		if cmd := newModel.overlayStack.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}); cmd != nil {
			t.Fatal("expected first jump key to wait for a two-char label")
		}
		cmd := newModel.overlayStack.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		if cmd == nil {
			t.Fatal("expected second jump key to emit a selection command")
		}
		msg := cmd()
		selected, ok := msg.(overlay.JumpSelectedMsg)
		if !ok {
			t.Fatalf("jump command emitted %T, want overlay.JumpSelectedMsg", msg)
		}
		if got, want := selected.TaskIndex, 10; got != want {
			t.Fatalf("selected task index = %d, want %d", got, want)
		}

		result, _ = newModel.Update(msg)
		finalModel := result.(Model)
		pos := getCursorPosition(finalModel)
		if pos.Column != 0 || pos.Task != 10 {
			t.Fatalf("expected cursor on jump target at (0,10), got (%d,%d)", pos.Column, pos.Task)
		}
		if !finalModel.overlayStack.IsEmpty() {
			t.Fatal("expected jump overlay to close after selection")
		}
	})

	t.Run("gs opens spec workspace", func(t *testing.T) {
		m.editor.EnterGoto()
		m.nav.SelectTask("az-2", 0)
		before := getCursorPosition(m)

		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		newModel := result.(Model)

		if !newModel.editor.IsNormal() {
			t.Fatalf("expected goto mode to return to normal after opening spec workspace, got %v", newModel.editor.GetMode())
		}
		current := newModel.overlayStack.Current()
		if current == nil {
			t.Fatal("expected spec workspace overlay to be pushed")
		}
		if got := current.Title(); got != "Spec Workspace" {
			t.Fatalf("overlay title = %q, want Spec Workspace", got)
		}

		updated, _ := newModel.handleOverlayKey(tea.KeyMsg{Type: tea.KeyTab})
		cycled := updated.(Model)
		if got := cycled.overlayStack.Current().View(); got == "" {
			t.Fatal("expected spec workspace overlay to continue rendering after tab cycle")
		}

		_, closeCmd := cycled.handleOverlayKey(tea.KeyMsg{Type: tea.KeyEsc})
		if closeCmd == nil {
			t.Fatal("expected escape to emit a close command")
		}
		closeMsg := closeCmd()
		if _, ok := closeMsg.(overlay.CloseOverlayMsg); !ok {
			t.Fatalf("escape command emitted %T, want CloseOverlayMsg", closeMsg)
		}
		cycled.overlayStack.Update(closeMsg)
		finalModel := cycled
		if !finalModel.overlayStack.IsEmpty() {
			t.Fatal("expected spec workspace overlay to close on escape")
		}
		if finalPos := getCursorPosition(finalModel); finalPos != before {
			t.Fatalf("cursor position changed across spec workspace flow: before=%+v after=%+v", before, finalPos)
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
		m.editor.Select("az-1")
		result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		newModel := result.(Model)

		if !newModel.editor.IsNormal() {
			t.Errorf("Expected ModeNormal after escape from select, got %v", newModel.editor.GetMode())
		}
		if newModel.editor.HasSelection() {
			t.Fatal("expected selection to be cleared after escape from select")
		}
	})

	t.Run("v exits select mode and clears selection", func(t *testing.T) {
		m.editor.EnterSelect()
		m.editor.Select("az-1")
		result, _ := m.handleSelectMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		newModel := result.(Model)

		if !newModel.editor.IsNormal() {
			t.Errorf("Expected ModeNormal after v from select, got %v", newModel.editor.GetMode())
		}
		if newModel.editor.HasSelection() {
			t.Fatal("expected selection to be cleared after v from select")
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

	t.Run("tab still toggles board view in normal mode", func(t *testing.T) {
		m.editor.EnterNormal()
		m.viewMode = ViewModeBoard
		m.nav.SelectTask("az-2", 0)
		before := getCursorPosition(m)

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
		compactModel := result.(Model)

		if compactModel.viewMode != ViewModeCompact {
			t.Fatalf("expected tab to toggle to compact view, got %v", compactModel.viewMode)
		}
		if got := getCursorPosition(compactModel); got != before {
			t.Fatalf("cursor position changed after tab to compact view: before=%+v after=%+v", before, got)
		}
		if got := compactModel.toasts[len(compactModel.toasts)-1].Message; got != "Switched to compact view" {
			t.Fatalf("expected compact-view toast, got %q", got)
		}

		result, _ = compactModel.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
		boardModel := result.(Model)

		if boardModel.viewMode != ViewModeBoard {
			t.Fatalf("expected second tab to restore board view, got %v", boardModel.viewMode)
		}
		if got := getCursorPosition(boardModel); got != before {
			t.Fatalf("cursor position changed after tab back to board view: before=%+v after=%+v", before, got)
		}
		if got := boardModel.toasts[len(boardModel.toasts)-1].Message; got != "Switched to board view" {
			t.Fatalf("expected board-view toast, got %q", got)
		}
	})
}

func TestActionModeOperationalKeysFailFastWithGuidance(t *testing.T) {
	keys := []string{"u", "m", "P"}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			m := newTestModel()
			m.editor.EnterAction()

			before := len(m.toasts)
			result, cmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			newModel := result.(Model)

			if cmd != nil {
				t.Fatalf("expected no command for action key %q, got %T", key, cmd)
			}
			if got := len(newModel.toasts); got != before+1 {
				t.Fatalf("expected one toast for key %q, got %d", key, got-before)
			}
			last := newModel.toasts[len(newModel.toasts)-1]
			if !strings.Contains(last.Message, "no git operation was started") {
				t.Fatalf("toast for %q missing no-op guidance: %q", key, last.Message)
			}
			if !strings.Contains(last.Message, "continue/abort") {
				t.Fatalf("toast for %q missing continue/abort guidance: %q", key, last.Message)
			}
		})
	}
}

func TestLoadingStateAcceptsImmediateInteraction(t *testing.T) {
	m := newTestModel()
	m.loading = true
	m.editor.EnterNormal()
	m.nav.SelectTask("az-1", 0)

	if got := m.View(); !strings.Contains(got, "Loading issues") {
		t.Fatalf("expected loading view while hydrated state is pending, got %q", got)
	}

	t.Run("navigation works while loading", func(t *testing.T) {
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
		newModel := result.(Model)

		if !newModel.loading {
			t.Fatal("expected loading state to remain active during immediate interaction")
		}
		pos := getCursorPosition(newModel)
		if pos.Column != 1 {
			t.Fatalf("expected cursor to move while loading, got column %d", pos.Column)
		}
	})

	t.Run("mode changes work while loading", func(t *testing.T) {
		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		newModel := result.(Model)

		if !newModel.loading {
			t.Fatal("expected loading state to remain active during mode change")
		}
		if !newModel.editor.IsSelect() {
			t.Fatalf("expected select mode while loading, got %v", newModel.editor.GetMode())
		}
	})
}

func TestEpicDrillDownFlow(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()

	epicID := "az-epic"
	childID := "az-epic-child"
	m.tasks = append(m.tasks,
		domain.Task{
			ID:       epicID,
			Title:    "Parent Epic",
			Status:   domain.StatusOpen,
			Priority: domain.P1,
			Type:     domain.TypeEpic,
		},
		domain.Task{
			ID:       childID,
			Title:    "Epic Child",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			ParentID: &epicID,
		},
	)

	m.nav.SelectTask(epicID, 0)
	before := getCursorPosition(m)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyEnter})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	drillDown, ok := current.(*overlay.EpicDrillDown)
	if !ok {
		t.Fatalf("expected EpicDrillDown overlay, got %T", current)
	}
	if got := drillDown.Title(); got != "Epic: az-epic" {
		t.Fatalf("epic drill-down title = %q, want Epic: az-epic", got)
	}
	if got := drillDown.View(); !strings.Contains(got, "Parent Epic") || !strings.Contains(got, "Epic Child") {
		t.Fatalf("epic drill-down view does not render expected content: %q", got)
	}

	updated, closeCmd := newModel.handleOverlayKey(tea.KeyMsg{Type: tea.KeyEsc})
	if closeCmd == nil {
		t.Fatal("expected escape to emit a close command")
	}
	closeMsg := closeCmd()
	if _, ok := closeMsg.(overlay.CloseOverlayMsg); !ok {
		t.Fatalf("escape command emitted %T, want CloseOverlayMsg", closeMsg)
	}

	closed := updated.(Model)
	closed.overlayStack.Update(closeMsg)
	if !closed.overlayStack.IsEmpty() {
		t.Fatal("expected epic drill-down overlay to close on escape")
	}
	if finalPos := getCursorPosition(closed); finalPos != before {
		t.Fatalf("cursor position changed across epic drill-down flow: before=%+v after=%+v", before, finalPos)
	}
}

func TestTaskDetailPanelIncludesTypedDependencies(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()

	currentID := "az-current"
	upstreamID := "az-upstream"
	downstreamID := "az-downstream"

	m.tasks = append(m.tasks,
		domain.Task{
			ID:       currentID,
			Title:    "Current task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: downstreamID, Type: domain.DependencyBlocks},
			},
		},
		domain.Task{
			ID:       upstreamID,
			Title:    "Upstream task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: currentID, Type: domain.DependencyRelatedTo},
			},
		},
		domain.Task{
			ID:       downstreamID,
			Title:    "Downstream task",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		},
	)

	m.nav.SelectTask(currentID, 0)

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyEnter})
	newModel := result.(Model)

	current := newModel.overlayStack.Current()
	detail, ok := current.(*overlay.DetailPanel)
	if !ok {
		t.Fatalf("expected DetailPanel overlay, got %T", current)
	}
	view := detail.View()
	if !strings.Contains(view, "Dependencies") {
		t.Fatalf("expected dependency section in view, got %q", view)
	}
	if !strings.Contains(view, "Outgoing") || !strings.Contains(view, "blocks -> az-downstream") {
		t.Fatalf("expected outgoing dependency edge in view, got %q", view)
	}
	if !strings.Contains(view, "Incoming") || !strings.Contains(view, "related_to <- az-upstream") {
		t.Fatalf("expected incoming dependency edge in view, got %q", view)
	}
	if strings.Index(view, "Outgoing") > strings.Index(view, "Incoming") {
		t.Fatalf("expected outgoing dependencies to render before incoming dependencies, got %q", view)
	}
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

func TestIssuesLoadedKeepsCursorOnValidTaskAfterRefresh(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-5", 3)

	result, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-2", Title: "Task 2", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeBug},
			{ID: "az-3", Title: "Task 3", Status: domain.StatusDone, Priority: domain.P2, Type: domain.TypeTask},
		},
		revision: 10,
	})
	newModel := result.(Model)

	cursor := newModel.nav.GetCursor()
	if cursor.TaskID == "" {
		t.Fatal("expected cursor to stay on a valid task after refresh")
	}
	validTaskIDs := map[string]struct{}{
		"az-1": {},
		"az-2": {},
		"az-3": {},
	}
	if _, ok := validTaskIDs[cursor.TaskID]; !ok {
		t.Fatalf("cursor task %q not present in refreshed task set", cursor.TaskID)
	}
}

func TestIssuesLoadedSyncsDaemonSessionProjection(t *testing.T) {
	startedAt := time.Date(2026, time.March, 25, 10, 30, 0, 0, time.UTC)
	devServer := &domain.DevServer{Port: 4242, Command: "npm run dev", Running: true}
	sourceSession := &domain.Session{
		IssueID:   "az-1",
		State:     domain.SessionBusy,
		StartedAt: &startedAt,
		Worktree:  "/tmp/az-1",
		DevServer: devServer,
	}

	m := newTestModel()
	m.sessions["stale"] = &domain.Session{
		IssueID:  "stale",
		State:    domain.SessionPaused,
		Worktree: "/tmp/stale",
	}

	result, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{
				ID:       "az-1",
				Title:    "Task 1",
				Status:   domain.StatusInProgress,
				Priority: domain.P1,
				Type:     domain.TypeTask,
				Session:  sourceSession,
			},
			{
				ID:       "az-2",
				Title:    "Task 2",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeBug,
			},
		},
		revision: 12,
	})
	newModel := result.(Model)

	got, ok := newModel.sessions["az-1"]
	if !ok || got == nil {
		t.Fatalf("session projection = %+v, want az-1 session", got)
	}
	if got == sourceSession {
		t.Fatal("session projection should clone daemon snapshot data, not alias it")
	}
	if got.IssueID != sourceSession.IssueID || got.State != sourceSession.State || got.Worktree != sourceSession.Worktree {
		t.Fatalf("session projection = %+v, want %+v", got, sourceSession)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(startedAt) {
		t.Fatalf("startedAt = %+v, want %v", got.StartedAt, startedAt)
	}
	if got.DevServer == nil || got.DevServer == devServer {
		t.Fatalf("dev server projection = %+v, want cloned dev server", got.DevServer)
	}
	if got.DevServer.Port != devServer.Port || got.DevServer.Command != devServer.Command || got.DevServer.Running != devServer.Running {
		t.Fatalf("dev server projection = %+v, want %+v", got.DevServer, devServer)
	}
	if _, ok := newModel.sessions["stale"]; ok {
		t.Fatal("expected stale projection entry to be cleared by daemon snapshot refresh")
	}
}

func TestIssuesLoadedStartsPeriodicRefreshLoop(t *testing.T) {
	m := newTestModel()
	m.hasRefreshLoop = false

	result, cmd := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		},
		revision: 11,
	})
	newModel := result.(Model)

	if !newModel.hasRefreshLoop {
		t.Fatal("expected issuesLoaded to start periodic refresh loop")
	}
	if cmd == nil {
		t.Fatal("expected periodic refresh command batch after issuesLoaded")
	}
}

func TestTmuxActionsDegradeOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")

	m := newTestModel()
	m.daemonClient = nil

	msg := m.attachSessionCmd("az-1")()
	errMsg, ok := msg.(sessionErrorMsg)
	if !ok {
		t.Fatalf("attachSessionCmd() returned %T, want sessionErrorMsg", msg)
	}
	if errMsg.err == nil || !strings.Contains(errMsg.err.Error(), "daemon client unavailable") {
		t.Fatalf("attachSessionCmd() error = %v, want daemon client unavailable", errMsg.err)
	}

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionAttach {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionAttach)
			}
			var body struct {
				ProjectID string `json:"project_id"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal attach request: %v", err)
			}
			if body.SessionID != "devserver-devserver-1" {
				t.Fatalf("session id = %q, want devserver-devserver-1", body.SessionID)
			}
			respBody, err := json.Marshal(struct {
				Output string `json:"output"`
			}{Output: "attached"})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	m.daemonClient = daemonclient.New(transport)

	msg = m.viewDevServer("devserver-1")()
	attached, ok := msg.(sessionAttachedMsg)
	if !ok {
		t.Fatalf("viewDevServer() returned %T, want sessionAttachedMsg", msg)
	}
	if attached.issueID != "devserver-devserver-1" {
		t.Fatalf("viewDevServer() issue = %q, want devserver-devserver-1", attached.issueID)
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionAttach {
		t.Fatalf("requests = %v", transport.requests)
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
