package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/linear"
	"github.com/riordanpawley/azedarach/internal/services/monitor"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
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

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlD})
		newModel := result.(Model)

		newPos := getCursorPosition(newModel)
		if newPos.Task <= initialPos.Task {
			t.Errorf("Expected task index to increase, got %d (was %d)", newPos.Task, initialPos.Task)
		}
	})

	t.Run("ctrl+u scrolls up", func(t *testing.T) {
		// Start at task 'e' (index 5) in Open column
		m.nav.SelectTask("e", 0)
		m.height = 24
		initialPos := getCursorPosition(m)

		result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyCtrlU})
		newModel := result.(Model)

		newPos := getCursorPosition(newModel)
		if newPos.Task >= initialPos.Task {
			t.Errorf("Expected task index to decrease, got %d (was %d)", newPos.Task, initialPos.Task)
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

func TestGotoModeJumpOverlayAndProjectSelector(t *testing.T) {
	t.Run("gw jump label index maps to visible cards only", func(t *testing.T) {
		m := newTestModel()

		// Grow first column beyond visible capacity so flat-index mapping can drift.
		for i := 0; i < 5; i++ {
			m.tasks = append(m.tasks, domain.Task{
				ID:       fmt.Sprintf("open-extra-%d", i),
				Title:    "Extra Open Task",
				Status:   domain.StatusOpen,
				Priority: domain.P3,
				Type:     domain.TypeTask,
			})
		}

		m.height = 24
		m.nav.SelectTask("az-1", 0)
		m.editor.EnterGoto()

		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
		m = result.(Model)

		if m.overlayStack.IsEmpty() {
			t.Fatal("expected jump overlay after g w")
		}
		if _, ok := m.overlayStack.Current().(*overlay.JumpMode); !ok {
			t.Fatalf("expected JumpMode overlay, got %T", m.overlayStack.Current())
		}

		// With height=24, visible-per-column=3. Index 3 should be first In Progress card (az-3).
		result, _ = m.Update(overlay.JumpSelectedMsg{TaskIndex: 3})
		m = result.(Model)

		if got := m.nav.GetCursor().TaskID; got != "az-3" {
			t.Fatalf("expected visible jump target az-3, got %s", got)
		}
	})

	t.Run("gp opens project selector and exits goto mode", func(t *testing.T) {
		m := newTestModel()
		m.editor.EnterGoto()

		result, _ := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		m = result.(Model)

		if !m.editor.IsNormal() {
			t.Fatalf("expected normal mode after g p, got %v", m.editor.GetMode())
		}
		if m.overlayStack.IsEmpty() {
			t.Fatal("expected project selector overlay after g p")
		}
		if _, ok := m.overlayStack.Current().(*overlay.ProjectSelector); !ok {
			t.Fatalf("expected ProjectSelector overlay, got %T", m.overlayStack.Current())
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

func TestActionMenuSpaceThenIOpensImageAttachOverlay(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = result.(Model)

	if m.overlayStack.IsEmpty() {
		t.Fatal("expected action menu overlay after pressing space")
	}
	if _, ok := m.overlayStack.Current().(*overlay.ActionMenu); !ok {
		t.Fatalf("expected ActionMenu overlay, got %T", m.overlayStack.Current())
	}

	result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	m = result.(Model)

	if cmd == nil {
		t.Fatal("expected selection command from action menu for key 'i'")
	}

	msg := cmd()
	selectionMsg, ok := msg.(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("expected SelectionMsg from action menu, got %T", msg)
	}
	if selectionMsg.Key != "i" {
		t.Fatalf("expected selection key 'i', got %q", selectionMsg.Key)
	}

	result, _ = m.Update(selectionMsg)
	m = result.(Model)

	if m.overlayStack.IsEmpty() {
		t.Fatal("expected image attachment overlay to be open")
	}
	if _, ok := m.overlayStack.Current().(*overlay.ImageAttachOverlay); !ok {
		t.Fatalf("expected ImageAttachOverlay, got %T", m.overlayStack.Current())
	}
}

func TestActionMenuSpaceThenSStartsSessionWithBootstrapPromptAndStatusUpdate(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)

	issueRunner := &scriptedLinearRunner{listPayload: []byte(`[]`)}
	m.issueClient = linear.NewClient(issueRunner, slog.Default())

	gitRunner := &gitScriptRunner{}
	m.worktreeManager = git.NewWorktreeManager(gitRunner, "/tmp/azedarach", slog.Default())

	tmuxRunner := &tmuxScriptRunner{}
	m.tmuxClient = tmux.NewClient(tmuxRunner, slog.Default())

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	result, cmd := m.Update(selectionMsg)
	m = result.(Model)
	if cmd == nil {
		t.Fatal("expected start session + work command")
	}

	msg := cmd()
	started, ok := msg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("expected sessionStartedMsg, got %T", msg)
	}
	if started.variant != sessionStartVariantWithWorkPrompt {
		t.Fatalf("expected with-work start variant, got %q", started.variant)
	}

	sent := tmuxRunner.sentKeys["az-1"]
	if len(sent) < 2 {
		t.Fatalf("expected launch + bootstrap tmux commands, got %v", sent)
	}
	if sent[0] != "claude" {
		t.Fatalf("expected default cli launch command 'claude', got %q", sent[0])
	}
	if !containsAll(sent[1], []string{"az prime", "az show az-1"}) {
		t.Fatalf("expected bootstrap guidance to include az prime/show, got %q", sent[1])
	}

	result, _ = m.Update(started)
	m = result.(Model)
	if !issueRunner.hasStatusUpdate("az-1", domain.StatusInProgress) {
		t.Fatal("expected issue status update call to in_progress on start")
	}
	for _, task := range m.tasks {
		if task.ID == "az-1" && task.Status != domain.StatusInProgress {
			t.Fatalf("expected az-1 status in_progress after session start, got %s", task.Status)
		}
	}
}

func TestActionMenuSpaceThenSkipPermissionVariantUsesYoloAndAcknowledges(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)

	m.issueClient = linear.NewClient(&scriptedLinearRunner{listPayload: []byte(`[]`)}, slog.Default())
	m.worktreeManager = git.NewWorktreeManager(&gitScriptRunner{}, "/tmp/azedarach", slog.Default())
	tmuxRunner := &tmuxScriptRunner{}
	m.tmuxClient = tmux.NewClient(tmuxRunner, slog.Default())

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})
	result, cmd := m.Update(selectionMsg)
	m = result.(Model)
	if cmd == nil {
		t.Fatal("expected skip-permission start command")
	}

	msg := cmd()
	started, ok := msg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("expected sessionStartedMsg, got %T", msg)
	}
	if started.variant != sessionStartVariantSkipPermission {
		t.Fatalf("expected skip-permission variant, got %q", started.variant)
	}

	sent := tmuxRunner.sentKeys["az-1"]
	if len(sent) == 0 {
		t.Fatal("expected tmux launch command")
	}
	if !strings.Contains(sent[0], "--yolo") {
		t.Fatalf("expected skip-permission launch command to include --yolo, got %q", sent[0])
	}

	result, _ = m.Update(started)
	m = result.(Model)
	last := m.toasts[len(m.toasts)-1]
	if !containsAll(last.Message, []string{"skip-permission", "az-1"}) {
		t.Fatalf("expected visible skip-permission acknowledgement toast, got %q", last.Message)
	}
}

func TestActionMenuSpaceThenChatVariantIncludesChatBootstrapGuidance(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)

	m.issueClient = linear.NewClient(&scriptedLinearRunner{listPayload: []byte(`[]`)}, slog.Default())
	m.worktreeManager = git.NewWorktreeManager(&gitScriptRunner{}, "/tmp/azedarach", slog.Default())
	tmuxRunner := &tmuxScriptRunner{}
	m.tmuxClient = tmux.NewClient(tmuxRunner, slog.Default())

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	result, cmd := m.Update(selectionMsg)
	m = result.(Model)
	if cmd == nil {
		t.Fatal("expected chat-variant session start command")
	}

	msg := cmd()
	started, ok := msg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("expected sessionStartedMsg, got %T", msg)
	}
	if started.variant != sessionStartVariantChat {
		t.Fatalf("expected chat variant, got %q", started.variant)
	}

	sent := tmuxRunner.sentKeys["az-1"]
	if len(sent) < 2 {
		t.Fatalf("expected launch + bootstrap tmux commands, got %v", sent)
	}
	if !containsAll(sent[1], []string{"chat-oriented", "az prime", "az show az-1"}) {
		t.Fatalf("expected chat bootstrap guidance with az contract, got %q", sent[1])
	}

	result, _ = m.Update(started)
	m = result.(Model)
	last := m.toasts[len(m.toasts)-1]
	if !containsAll(last.Message, []string{"chat", "az-1"}) {
		t.Fatalf("expected visible chat variant acknowledgement toast, got %q", last.Message)
	}
}

func TestActionMenuSpaceThenSReusesExistingWorktreeAndTmuxSession(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)

	issueRunner := &scriptedLinearRunner{listPayload: []byte(`[]`)}
	m.issueClient = linear.NewClient(issueRunner, slog.Default())

	gitRunner := &gitScriptRunner{
		worktreeListOutput: `worktree /tmp/azedarach
HEAD abc123
branch refs/heads/main

worktree /tmp/azedarach-az-1
HEAD def456
branch refs/heads/az/az-1
`,
	}
	m.worktreeManager = git.NewWorktreeManager(gitRunner, "/tmp/azedarach", slog.Default())

	tmuxRunner := &tmuxScriptRunner{
		hasSessions: map[string]bool{
			"az-1": true,
		},
	}
	m.tmuxClient = tmux.NewClient(tmuxRunner, slog.Default())

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	result, cmd := m.Update(selectionMsg)
	m = result.(Model)
	if cmd == nil {
		t.Fatal("expected start session command")
	}

	msg := cmd()
	started, ok := msg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("expected sessionStartedMsg, got %T", msg)
	}
	if started.variant != sessionStartVariantStandard {
		t.Fatalf("expected standard start variant, got %q", started.variant)
	}

	if len(gitRunner.addCalls) != 0 {
		t.Fatalf("expected no new worktree add when existing worktree is present, got %v", gitRunner.addCalls)
	}
	if len(tmuxRunner.sentKeys["az-1"]) != 0 {
		t.Fatalf("expected no duplicate bootstrap commands when tmux session already exists, got %v", tmuxRunner.sentKeys["az-1"])
	}

	result, _ = m.Update(started)
	m = result.(Model)
	if !issueRunner.hasStatusUpdate("az-1", domain.StatusInProgress) {
		t.Fatal("expected issue status update call to in_progress on idempotent start")
	}
	session := m.sessions["az-1"]
	if session == nil {
		t.Fatal("expected session indicator after idempotent start")
	}
	if session.Worktree != "/tmp/azedarach-az-1" {
		t.Fatalf("expected recovered existing worktree path, got %q", session.Worktree)
	}
}

func TestActionMenuSpaceThenPTransitionsBusySessionToPaused(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)
	m.sessions["az-1"] = &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionBusy,
		Worktree: "/tmp/az-1",
	}

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	result, cmd := m.Update(selectionMsg)
	m = result.(Model)
	if cmd != nil {
		t.Fatal("expected pause transition to run synchronously")
	}

	session := m.sessions["az-1"]
	if session == nil {
		t.Fatal("expected session indicator to remain after pause")
	}
	if session.State != domain.SessionPaused {
		t.Fatalf("expected session state paused, got %v", session.State)
	}
}

func TestActionMenuSpaceThenRTransitionsPausedSessionToBusy(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)
	m.sessions["az-1"] = &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionPaused,
		Worktree: "/tmp/az-1",
	}

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	result, cmd := m.Update(selectionMsg)
	m = result.(Model)
	if cmd != nil {
		t.Fatal("expected resume transition to run synchronously")
	}

	session := m.sessions["az-1"]
	if session == nil {
		t.Fatal("expected session indicator to remain after resume")
	}
	if session.State != domain.SessionBusy {
		t.Fatalf("expected session state busy after resume, got %v", session.State)
	}
}

func TestActionMenuSpaceThenXStopsSessionAndRemovesIndicator(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)
	tmuxRunner := &tmuxScriptRunner{}
	m.tmuxClient = tmux.NewClient(tmuxRunner, slog.Default())
	m.sessions["az-1"] = &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionBusy,
		Worktree: "/tmp/az-1",
	}

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	result, cmd := m.Update(selectionMsg)
	m = result.(Model)
	if cmd == nil {
		t.Fatal("expected stop command for x action")
	}
	if msg := cmd(); msg != nil {
		if _, isErr := msg.(sessionErrorMsg); isErr {
			t.Fatalf("expected stop command success, got error msg: %#v", msg)
		}
	}

	if _, ok := m.sessions["az-1"]; ok {
		t.Fatal("expected session indicator to be removed after stop")
	}
	if len(tmuxRunner.killedSessions) != 1 || tmuxRunner.killedSessions[0] != "az-1" {
		t.Fatalf("expected tmux kill-session for az-1, got %v", tmuxRunner.killedSessions)
	}
}

func TestOpenImagePreviewMsgPushesPreviewOverlay(t *testing.T) {
	m := newTestModel()

	result, _ := m.Update(overlay.OpenImagePreviewMsg{
		IssueID:      "az-1",
		InitialIndex: 0,
	})
	m = result.(Model)

	if m.overlayStack.IsEmpty() {
		t.Fatal("expected image preview overlay to be open")
	}
	if _, ok := m.overlayStack.Current().(*overlay.ImagePreviewOverlay); !ok {
		t.Fatalf("expected ImagePreviewOverlay, got %T", m.overlayStack.Current())
	}
}

func TestActionMenuSpaceThenRTogglesDevServerForCurrentIssue(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)
	m.sessions["az-1"] = &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionBusy,
		Worktree: "/tmp/az-1",
	}

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	result, runCmd := m.Update(selectionMsg)
	m = result.(Model)
	if runCmd == nil {
		t.Fatal("expected toggle dev server command")
	}
	if msg := runCmd(); msg != nil {
		if _, isErr := msg.(sessionErrorMsg); isErr {
			t.Fatalf("expected toggle command success, got error msg: %#v", msg)
		}
	}

	srv, ok := m.devServerManager.Get("az-1")
	if !ok {
		t.Fatal("expected running dev server for issue az-1")
	}
	if srv.Status != "running" {
		t.Fatalf("expected running dev server status, got %q", srv.Status)
	}

	session := m.sessions["az-1"]
	if session == nil || session.DevServer == nil || !session.DevServer.Running {
		t.Fatal("expected session dev server indicator to be populated and running")
	}
}

func TestActionMenuSpaceThenRWithoutSessionShowsActionableError(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	result, runCmd := m.Update(selectionMsg)
	m = result.(Model)
	if runCmd == nil {
		t.Fatal("expected toggle dev server command")
	}

	msg := runCmd()
	errMsg, ok := msg.(sessionErrorMsg)
	if !ok {
		t.Fatalf("expected sessionErrorMsg from toggle, got %T", msg)
	}

	result, _ = m.Update(errMsg)
	m = result.(Model)
	if len(m.toasts) == 0 {
		t.Fatal("expected toast error for missing worktree context")
	}
	last := m.toasts[len(m.toasts)-1]
	if last.Level != ToastError {
		t.Fatalf("expected error toast, got %v", last.Level)
	}
	if !containsAll(last.Message, []string{"dev server", "start", "session"}) {
		t.Fatalf("expected actionable guidance in toast, got %q", last.Message)
	}
}

func TestActionMenuSpaceThenVShowsAttachCommandToastWhenRunning(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)
	m.sessions["az-1"] = &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionBusy,
		Worktree: "/tmp/az-1",
	}

	if _, err := m.devServerManager.Start(
		context.Background(),
		"az-1",
		"az-1",
		"npm run dev",
		"/tmp/az-1",
	); err != nil {
		t.Fatalf("failed to seed running dev server: %v", err)
	}
	srv, ok := m.devServerManager.Get("az-1")
	if !ok {
		t.Fatal("expected seeded server")
	}
	m.sessions["az-1"].DevServer = &domain.DevServer{Port: srv.Port, Command: srv.Command, Running: true}

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	result, runCmd := m.Update(selectionMsg)
	m = result.(Model)
	if runCmd == nil {
		t.Fatal("expected view dev server command")
	}
	viewMsg := runCmd()
	toast, ok := viewMsg.(Toast)
	if !ok {
		t.Fatalf("expected Toast from view command, got %T", viewMsg)
	}
	result, _ = m.Update(toast)
	m = result.(Model)

	if len(m.toasts) == 0 {
		t.Fatal("expected toast from view command")
	}
	last := m.toasts[len(m.toasts)-1]
	if !containsAll(last.Message, []string{"tmux attach-session", "devserver-az-1"}) {
		t.Fatalf("expected attach instructions toast, got %q", last.Message)
	}
}

func TestActionMenuSpaceThenCtrlRRestartsDevServer(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)
	m.sessions["az-1"] = &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionBusy,
		Worktree: "/tmp/az-1",
	}

	if _, err := m.devServerManager.Start(
		context.Background(),
		"az-1",
		"az-1",
		"npm run dev",
		"/tmp/az-1",
	); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	before, _ := m.devServerManager.Get("az-1")
	beforeStartedAt := before.StartedAt

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyCtrlR})
	if selectionMsg.Key != "ctrl+r" {
		t.Fatalf("expected selection key ctrl+r, got %q", selectionMsg.Key)
	}
	result, runCmd := m.Update(selectionMsg)
	m = result.(Model)
	if runCmd == nil {
		t.Fatal("expected restart command from ctrl+r selection")
	}
	restartMsg := runCmd()
	if restartMsg != nil {
		if _, isErr := restartMsg.(sessionErrorMsg); isErr {
			t.Fatalf("expected restart success, got error msg: %#v", restartMsg)
		}
	}

	after, ok := m.devServerManager.Get("az-1")
	if !ok {
		t.Fatal("expected dev server entry after restart")
	}
	if after.Status != "running" {
		t.Fatalf("expected running status after restart, got %q", after.Status)
	}
	if !after.StartedAt.After(beforeStartedAt) {
		t.Fatalf("expected restart to refresh startedAt; before=%v after=%v", beforeStartedAt, after.StartedAt)
	}
}

func TestActionMenuAttachWithIndicatorTmuxMismatchOpensReconciliationOverlay(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)
	m.sessions["az-1"] = &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionBusy,
		Worktree: "/tmp/az-1",
	}
	m.tmuxClient = tmux.NewClient(&tmuxScriptRunner{
		hasSessions: map[string]bool{
			"az-1": false,
		},
	}, slog.Default())

	m, selectionMsg := openActionAndSelect(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	result, cmd := m.Update(selectionMsg)
	m = result.(Model)
	if cmd == nil {
		t.Fatal("expected session mismatch probe command")
	}

	probeMsg := cmd()
	result, _ = m.Update(probeMsg)
	m = result.(Model)
	if m.overlayStack.IsEmpty() {
		t.Fatal("expected reconciliation overlay to be opened")
	}
	if _, ok := m.overlayStack.Current().(*overlay.SessionReconciliationOverlay); !ok {
		t.Fatalf("expected SessionReconciliationOverlay, got %T", m.overlayStack.Current())
	}
}

func TestActionMenuAttachWithoutSessionShowsIssueSpecificNextStep(t *testing.T) {
	m := newTestModel()

	result, _ := m.Update(sessionAttachProbeMsg{
		mismatch: diagnostics.SessionMismatch{
			IssueID:          "az-1",
			IndicatorPresent: false,
			TmuxPresent:      false,
		},
		trigger: "attach",
	})
	m = result.(Model)

	if len(m.toasts) == 0 {
		t.Fatal("expected warning toast when no active session exists")
	}
	last := m.toasts[len(m.toasts)-1]
	if last.Level != ToastWarning {
		t.Fatalf("expected warning toast level, got %v", last.Level)
	}
	if !containsAll(last.Message, []string{"az-1", "Space s"}) {
		t.Fatalf("expected issue-specific next-step guidance, got %q", last.Message)
	}
}

func TestBranchBehindMsgWithCommitsBehindOpensMergeChoiceOverlay(t *testing.T) {
	m := newTestModel()

	result, _ := m.Update(branchBehindMsg{
		issueID:       "az-1",
		worktree:      "/tmp/az-1",
		commitsBehind: 2,
	})
	m = result.(Model)

	if m.overlayStack.IsEmpty() {
		t.Fatal("expected merge choice overlay for stale branch attach flow")
	}
	if _, ok := m.overlayStack.Current().(*overlay.MergeChoiceOverlay); !ok {
		t.Fatalf("expected MergeChoiceOverlay, got %T", m.overlayStack.Current())
	}
}

func TestMergeAttachSelectionWithoutSessionShowsActionableWarning(t *testing.T) {
	m := newTestModel()
	result, _ := m.Update(overlay.SelectionMsg{
		Key:   "merge_attach",
		Value: "az-1",
	})
	m = result.(Model)

	if len(m.toasts) == 0 {
		t.Fatal("expected warning toast when merge attach selected without active session")
	}
	last := m.toasts[len(m.toasts)-1]
	if last.Level != ToastWarning {
		t.Fatalf("expected warning toast level, got %v", last.Level)
	}
	if !containsAll(last.Message, []string{"az-1", "Space s"}) {
		t.Fatalf("expected actionable warning with issue id and next step, got %q", last.Message)
	}
}

func TestSessionReconciliationActionAdoptCreatesIndicatorAndEvidenceToast(t *testing.T) {
	m := newTestModel()
	delete(m.sessions, "az-1")

	result, cmd := m.Update(overlay.SessionReconciliationActionMsg{
		Mismatch: diagnostics.SessionMismatch{
			IssueID:          "az-1",
			Kind:             diagnostics.SessionMismatchKindOrphanTmux,
			IndicatorPresent: false,
			TmuxPresent:      true,
		},
		Action:  overlay.ReconciliationActionAdoptIndicator,
		Trigger: "attach",
	})
	if cmd != nil {
		t.Fatal("expected adopt to run synchronously with no command")
	}
	m = result.(Model)

	session := m.sessions["az-1"]
	if session == nil {
		t.Fatal("expected adopted session indicator for az-1")
	}
	if session.State != domain.SessionIdle {
		t.Fatalf("expected adopted session state idle, got %v", session.State)
	}

	if len(m.toasts) == 0 {
		t.Fatal("expected reconciliation toast evidence")
	}
	found := false
	for _, entry := range m.toasts {
		if containsAll(entry.Message, []string{"reconciliation", "adopt", "az-1"}) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected auditable adopt toast, got %+v", m.toasts)
	}
}

func TestSessionReconciliationActionClearRemovesIndicatorAndEvidenceToast(t *testing.T) {
	m := newTestModel()
	m.sessions["az-1"] = &domain.Session{
		IssueID: "az-1",
		State:   domain.SessionPaused,
	}

	result, cmd := m.Update(overlay.SessionReconciliationActionMsg{
		Mismatch: diagnostics.SessionMismatch{
			IssueID:          "az-1",
			Kind:             diagnostics.SessionMismatchKindStaleIndicator,
			IndicatorPresent: true,
			TmuxPresent:      false,
		},
		Action:  overlay.ReconciliationActionClearIndicator,
		Trigger: "attach",
	})
	if cmd != nil {
		t.Fatal("expected clear to run synchronously with no command")
	}
	m = result.(Model)

	if _, ok := m.sessions["az-1"]; ok {
		t.Fatal("expected stale session indicator to be cleared")
	}
	if len(m.toasts) == 0 {
		t.Fatal("expected reconciliation toast evidence")
	}
	last := m.toasts[len(m.toasts)-1]
	if !containsAll(last.Message, []string{"reconciliation", "clear", "az-1"}) {
		t.Fatalf("expected auditable clear toast, got %q", last.Message)
	}
}

func TestSessionReconciliationActionTerminateOrphanTmuxSession(t *testing.T) {
	runner := &tmuxScriptRunner{}
	m := newTestModel()
	m.tmuxClient = tmux.NewClient(runner, slog.Default())

	result, cmd := m.Update(overlay.SessionReconciliationActionMsg{
		Mismatch: diagnostics.SessionMismatch{
			IssueID:          "az-1",
			Kind:             diagnostics.SessionMismatchKindOrphanTmux,
			IndicatorPresent: false,
			TmuxPresent:      true,
		},
		Action:  overlay.ReconciliationActionTerminateOrphanTmux,
		Trigger: "attach",
	})
	m = result.(Model)
	if cmd == nil {
		t.Fatal("expected terminate action command")
	}

	msg := cmd()
	result, _ = m.Update(msg)
	m = result.(Model)

	if len(runner.killedSessions) != 1 || runner.killedSessions[0] != "az-1" {
		t.Fatalf("expected tmux kill-session for az-1, got %v", runner.killedSessions)
	}
	if len(m.toasts) == 0 {
		t.Fatal("expected reconciliation toast evidence")
	}
	last := m.toasts[len(m.toasts)-1]
	if !containsAll(last.Message, []string{"reconciliation", "terminate", "az-1"}) {
		t.Fatalf("expected auditable terminate toast, got %q", last.Message)
	}
}

func TestBulkSetStatusCmdCapturesPerItemFailureDetails(t *testing.T) {
	m := newTestModel()
	m.issueClient = linear.NewClient(&scriptedLinearRunner{
		listPayload: []byte(`[]`),
		updateErr:   errors.New("simulated update failure"),
	}, slog.Default())

	cmd := m.bulkSetStatusCmd([]string{"az-1", "az-2"}, domain.StatusDone)
	if cmd == nil {
		t.Fatal("expected bulk set status command")
	}

	msg := cmd()
	var result bulkStatusResultMsg
	switch typed := msg.(type) {
	case bulkStatusResultMsg:
		result = typed
	case backupWrappedMsg:
		inner, ok := typed.inner.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("expected backupWrappedMsg inner bulkStatusResultMsg, got %T", typed.inner)
		}
		result = inner
	default:
		t.Fatalf("expected bulkStatusResultMsg or backupWrappedMsg, got %T", msg)
	}

	if result.updated != 0 || result.failed != 2 {
		t.Fatalf("expected 0 updated / 2 failed, got %d/%d", result.updated, result.failed)
	}
	if len(result.failedDetails) != 2 {
		t.Fatalf("expected 2 failure detail entries, got %v", result.failedDetails)
	}
	if !containsAll(strings.Join(result.failedDetails, " "), []string{"az-1", "az-2", "issue tracker update"}) {
		t.Fatalf("expected per-item failure details with issue IDs, got %v", result.failedDetails)
	}
}

func TestBulkStatusResultMsgShowsFailureDetailToast(t *testing.T) {
	m := newTestModel()

	result, _ := m.Update(bulkStatusResultMsg{
		updated: 1,
		failed:  2,
		failedDetails: []string{
			"az-1: simulated update failure",
			"az-2: simulated update failure",
		},
	})
	m = result.(Model)

	if len(m.toasts) < 2 {
		t.Fatalf("expected success + warning toasts for bulk failure details, got %+v", m.toasts)
	}

	found := false
	for _, entry := range m.toasts {
		if containsAll(entry.Message, []string{"Failed items", "az-1", "az-2"}) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected warning toast with per-item failure details, got %+v", m.toasts)
	}
}

func openActionAndSelect(t *testing.T, m Model, key tea.KeyMsg) (Model, overlay.SelectionMsg) {
	t.Helper()

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = result.(Model)
	if m.overlayStack.IsEmpty() {
		t.Fatal("expected action menu overlay after pressing space")
	}
	if _, ok := m.overlayStack.Current().(*overlay.ActionMenu); !ok {
		t.Fatalf("expected ActionMenu overlay, got %T", m.overlayStack.Current())
	}

	result, cmd := m.Update(key)
	m = result.(Model)
	if cmd == nil {
		t.Fatalf("expected selection command for key %q", key.String())
	}
	msg := cmd()
	selectionMsg, ok := msg.(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("expected SelectionMsg from action menu, got %T", msg)
	}
	return m, selectionMsg
}

func containsAll(message string, fragments []string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(strings.ToLower(message), strings.ToLower(fragment)) {
			return false
		}
	}
	return true
}

func TestLoadIssuesCmdInvokesBackupOpenAndShowsWarningsNonBlocking(t *testing.T) {
	m := newTestModel()
	collector := newBackupWarningCollector()
	backupRunner := &recordingBackupRunner{
		onOpenHook: func() {
			collector.Add("Local backup attempt failed (non-blocking): simulated open warning")
		},
	}
	m.backupRunner = backupRunner
	m.backupWarnings = collector
	m.issueClient = linear.NewClient(&scriptedLinearRunner{
		listPayload: []byte(`[]`),
	}, slog.Default())

	cmd := m.loadIssuesCmd()
	msg := cmd()

	result, _ := m.Update(msg)
	m = result.(Model)

	if backupRunner.openCalls != 1 {
		t.Fatalf("expected OnOpen to run once, got %d", backupRunner.openCalls)
	}
	if m.loading {
		t.Fatal("expected load issues flow to remain non-blocking and clear loading state")
	}
	if len(m.toasts) == 0 {
		t.Fatal("expected warning toast from backup warning collector")
	}
	found := false
	for _, toast := range m.toasts {
		if toast.Level != ToastWarning {
			continue
		}
		if strings.Contains(strings.ToLower(toast.Message), "local backup attempt failed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected actionable backup warning toast, got %+v", m.toasts)
	}
}

func TestMoveTaskStatusCmdInvokesBackupOpenAndMutationSuccess(t *testing.T) {
	m := newTestModel()
	backupRunner := &recordingBackupRunner{}
	m.backupRunner = backupRunner
	m.issueClient = linear.NewClient(&scriptedLinearRunner{
		listPayload: []byte(`[]`),
	}, slog.Default())

	cmd := m.moveTaskStatusCmd("az-1", 1)
	msg := cmd()

	statusMsg, ok := msg.(taskStatusResultMsg)
	if !ok {
		t.Fatalf("expected taskStatusResultMsg, got %T", msg)
	}
	if statusMsg.err != nil {
		t.Fatalf("expected successful status move, got err=%v", statusMsg.err)
	}
	if backupRunner.openCalls != 1 {
		t.Fatalf("expected OnOpen to run once, got %d", backupRunner.openCalls)
	}
	if backupRunner.mutationCalls != 1 {
		t.Fatalf("expected OnMutationSuccess to run once, got %d", backupRunner.mutationCalls)
	}
}

func TestMoveTaskStatusCmdSkipsMutationBackupWhenIssueUpdateFails(t *testing.T) {
	m := newTestModel()
	backupRunner := &recordingBackupRunner{}
	m.backupRunner = backupRunner
	m.issueClient = linear.NewClient(&scriptedLinearRunner{
		listPayload: []byte(`[]`),
		updateErr:   errors.New("update failed"),
	}, slog.Default())

	cmd := m.moveTaskStatusCmd("az-1", 1)
	msg := cmd()

	statusMsg, ok := msg.(taskStatusResultMsg)
	if !ok {
		t.Fatalf("expected taskStatusResultMsg, got %T", msg)
	}
	if statusMsg.err == nil {
		t.Fatal("expected status update error")
	}
	if backupRunner.openCalls != 1 {
		t.Fatalf("expected OnOpen to run once, got %d", backupRunner.openCalls)
	}
	if backupRunner.mutationCalls != 0 {
		t.Fatalf("expected OnMutationSuccess not to run on failed update, got %d", backupRunner.mutationCalls)
	}
}

type recordingBackupRunner struct {
	openCalls     int
	mutationCalls int
	onOpenHook    func()
}

func (r *recordingBackupRunner) OnOpen() {
	r.openCalls++
	if r.onOpenHook != nil {
		r.onOpenHook()
	}
}

func (r *recordingBackupRunner) OnMutationSuccess() {
	r.mutationCalls++
}

type scriptedLinearRunner struct {
	listPayload []byte
	updateErr   error
	updateCalls []linearStatusUpdateCall
}

func (r *scriptedLinearRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "az" {
		args = args[1:]
	}
	if len(args) < 2 || args[0] != "issue" {
		return []byte(`{}`), nil
	}

	switch args[1] {
	case "list":
		if len(r.listPayload) == 0 {
			return []byte(`[]`), nil
		}
		return r.listPayload, nil
	case "update":
		if len(args) >= 4 {
			r.updateCalls = append(r.updateCalls, linearStatusUpdateCall{
				IssueID: args[2],
				Status:  strings.TrimPrefix(args[3], "--status="),
			})
		}
		if r.updateErr != nil {
			return nil, r.updateErr
		}
		return []byte(`{}`), nil
	case "create":
		return []byte(`{"id":"az-created"}`), nil
	case "delete", "archive", "close":
		return []byte(`{}`), nil
	default:
		return []byte(`{}`), nil
	}
}

func (r *scriptedLinearRunner) hasStatusUpdate(issueID string, status domain.Status) bool {
	for _, call := range r.updateCalls {
		if call.IssueID == issueID && call.Status == string(status) {
			return true
		}
	}
	return false
}

type linearStatusUpdateCall struct {
	IssueID string
	Status  string
}

type gitScriptRunner struct {
	worktreeListOutput string
	addErr             error
	addCalls           []gitWorktreeAddCall
}

func (r *gitScriptRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		return r.worktreeListOutput, nil
	}
	if len(args) >= 6 && args[0] == "worktree" && args[1] == "add" {
		r.addCalls = append(r.addCalls, gitWorktreeAddCall{
			Branch:      args[3],
			WorktreeDir: args[4],
			BaseBranch:  args[5],
		})
		if r.addErr != nil {
			return "", r.addErr
		}
		return "", nil
	}
	return "", nil
}

type gitWorktreeAddCall struct {
	Branch      string
	WorktreeDir string
	BaseBranch  string
}

type monitorStubTmux struct {
	output string
}

func (m *monitorStubTmux) CapturePane(_ context.Context, _ string) (string, error) {
	return m.output, nil
}

func TestTickMsgSyncsSessionStatesFromMonitor(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected domain.SessionState
	}{
		{name: "waiting", output: "Do you want to continue? [y/n]", expected: domain.SessionWaiting},
		{name: "done", output: "Task completed successfully", expected: domain.SessionDone},
		{name: "error", output: "Error: something went wrong", expected: domain.SessionError},
		{name: "busy", output: "Processing files...", expected: domain.SessionBusy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			tmux := &monitorStubTmux{output: tt.output}
			m.sessionMonitor = monitor.NewSessionMonitor(tmux)
			m.sessions["az-1"] = &domain.Session{
				IssueID: "az-1",
				State:   domain.SessionIdle,
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer m.sessionMonitor.StopAll()
			m.sessionMonitor.Start(ctx, "az-1", nil)

			// Wait for the monitor poll interval to detect state.
			time.Sleep(600 * time.Millisecond)

			result, _ := m.Update(tickMsg(time.Now()))
			updated := result.(Model)

			session := updated.sessions["az-1"]
			if session == nil {
				t.Fatal("expected tracked session for az-1")
			}
			if session.State != tt.expected {
				t.Fatalf("expected session state %v, got %v", tt.expected, session.State)
			}
		})
	}
}

type tmuxScriptRunner struct {
	hasSessions    map[string]bool
	killErr        error
	killedSessions []string
	newSessionErr  error
	sendKeysErr    error
	sentKeys       map[string][]string
}

func (r *tmuxScriptRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "new-session":
		if r.newSessionErr != nil {
			return "", r.newSessionErr
		}
		return "", nil
	case "send-keys":
		if len(args) < 4 {
			return "", errors.New("missing send-keys target")
		}
		target := args[2]
		if r.sentKeys == nil {
			r.sentKeys = make(map[string][]string)
		}
		r.sentKeys[target] = append(r.sentKeys[target], args[3])
		if r.sendKeysErr != nil {
			return "", r.sendKeysErr
		}
		return "", nil
	case "has-session":
		if len(args) < 3 {
			return "", errors.New("missing target")
		}
		target := args[2]
		if r.hasSessions != nil && r.hasSessions[target] {
			return "", nil
		}
		return "", errors.New("session not found")
	case "kill-session":
		if len(args) < 3 {
			return "", errors.New("missing target")
		}
		target := args[2]
		r.killedSessions = append(r.killedSessions, target)
		if r.killErr != nil {
			return "", r.killErr
		}
		return "", nil
	case "list-sessions":
		return "", nil
	default:
		return "", nil
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

func TestViewModeToggleAlternatesAndPreservesFocus(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.toasts = nil
	m.nav.SelectTask("az-2", 0)

	if got := m.nav.GetCursor().TaskID; got != "az-2" {
		t.Fatalf("expected initial focus az-2, got %q", got)
	}
	if m.viewMode != ViewModeBoard {
		t.Fatalf("expected initial board view, got %v", m.viewMode)
	}
	if !strings.Contains(m.View(), "VIEW:KAN") {
		t.Fatalf("expected board status output to include VIEW:KAN, got %q", m.View())
	}

	result, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
	m = result.(Model)
	if m.viewMode != ViewModeCompact {
		t.Fatalf("expected compact view after first tab, got %v", m.viewMode)
	}
	if got := m.nav.GetCursor().TaskID; got != "az-2" {
		t.Fatalf("expected focus to remain az-2 after toggle, got %q", got)
	}
	m.toasts = nil
	if !strings.Contains(m.View(), "VIEW:LST") {
		t.Fatalf("expected compact status output to include VIEW:LST, got %q", m.View())
	}

	result, _ = m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
	m = result.(Model)
	if m.viewMode != ViewModeBoard {
		t.Fatalf("expected board view after second tab, got %v", m.viewMode)
	}
	if got := m.nav.GetCursor().TaskID; got != "az-2" {
		t.Fatalf("expected focus to remain az-2 after second toggle, got %q", got)
	}
	m.toasts = nil
	if !strings.Contains(m.View(), "VIEW:KAN") {
		t.Fatalf("expected board status output to include VIEW:KAN after second toggle, got %q", m.View())
	}
}

func TestSortSelectionMsgAppliesSortWithoutActionMenuCollision(t *testing.T) {
	m := newTestModel()
	m.editor.SetSort(&domain.Sort{Field: domain.SortBySession, Order: domain.SortAsc})

	selection := overlay.SelectionMsg{
		Key:   "p",
		Value: &domain.Sort{Field: domain.SortByPriority, Order: domain.SortDesc},
	}
	result, cmd := m.Update(selection)
	m = result.(Model)

	if cmd != nil {
		t.Fatal("expected no async command from sort selection")
	}
	if got := m.editor.GetSort().Field; got != domain.SortByPriority {
		t.Fatalf("expected sort field to update to priority, got %q", got)
	}
	if got := m.editor.GetSort().Order; got != domain.SortDesc {
		t.Fatalf("expected sort order to remain desc, got %v", got)
	}

	for _, toast := range m.toasts {
		if containsAll(toast.Message, []string{"pause", "session"}) {
			t.Fatalf("expected sort selection not to trigger session action toast, got %q", toast.Message)
		}
	}
}

func TestSearchFilterSortParityPersistsAcrossViewToggles(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.tasks = []domain.Task{
		{ID: "az-101", Title: "alpha-open-high", Status: domain.StatusOpen, Priority: domain.P0, Type: domain.TypeTask},
		{ID: "az-102", Title: "alpha-open-low", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask},
		{ID: "az-103", Title: "alpha-prog-mid", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeTask},
		{ID: "az-104", Title: "beta-done", Status: domain.StatusDone, Priority: domain.P0, Type: domain.TypeTask},
		{ID: "az-105", Title: "alpha-prog-low", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask},
	}
	m.nav.SelectTask("az-101", 0)

	// Apply search/filter/sort while on board view.
	result, _ := m.Update(overlay.SearchMsg{Query: "alpha"})
	m = result.(Model)
	filter := m.editor.GetFilter()
	filter.ToggleStatus(domain.StatusOpen)
	filter.ToggleStatus(domain.StatusInProgress)
	m.editor.SetSort(&domain.Sort{Field: domain.SortByPriority, Order: domain.SortAsc})

	initialColumns := m.buildColumns()
	initialBoardByStatus := boardIDsByStatus(initialColumns)
	initialCompactByStatus := compactIDsByStatus(m.editor.ApplySort(m.editor.ApplyFilter(m.tasks)))
	if !reflect.DeepEqual(initialBoardByStatus, initialCompactByStatus) {
		t.Fatalf("expected board/compact status-order parity, board=%v compact=%v", initialBoardByStatus, initialCompactByStatus)
	}

	result, _ = m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
	m = result.(Model)
	if m.viewMode != ViewModeCompact {
		t.Fatalf("expected compact view after tab, got %v", m.viewMode)
	}

	result, _ = m.handleNormalMode(tea.KeyMsg{Type: tea.KeyTab})
	m = result.(Model)
	if m.viewMode != ViewModeBoard {
		t.Fatalf("expected board view after second tab, got %v", m.viewMode)
	}

	afterColumns := m.buildColumns()
	afterBoardByStatus := boardIDsByStatus(afterColumns)
	afterCompactByStatus := compactIDsByStatus(m.editor.ApplySort(m.editor.ApplyFilter(m.tasks)))

	if !reflect.DeepEqual(initialBoardByStatus, afterBoardByStatus) {
		t.Fatalf("expected board ordering to remain stable across toggles, before=%v after=%v", initialBoardByStatus, afterBoardByStatus)
	}
	if !reflect.DeepEqual(initialCompactByStatus, afterCompactByStatus) {
		t.Fatalf("expected compact ordering semantics to remain stable across toggles, before=%v after=%v", initialCompactByStatus, afterCompactByStatus)
	}
	if !reflect.DeepEqual(afterBoardByStatus, afterCompactByStatus) {
		t.Fatalf("expected board/compact parity after toggles, board=%v compact=%v", afterBoardByStatus, afterCompactByStatus)
	}
}

func boardIDsByStatus(columns []board.Column) map[domain.Status][]string {
	byStatus := map[domain.Status][]string{
		domain.StatusOpen:       {},
		domain.StatusInProgress: {},
		domain.StatusBlocked:    {},
		domain.StatusDone:       {},
	}

	if len(columns) > 0 {
		for _, task := range columns[0].Tasks {
			byStatus[domain.StatusOpen] = append(byStatus[domain.StatusOpen], task.ID)
		}
	}
	if len(columns) > 1 {
		for _, task := range columns[1].Tasks {
			byStatus[domain.StatusInProgress] = append(byStatus[domain.StatusInProgress], task.ID)
		}
	}
	if len(columns) > 2 {
		for _, task := range columns[2].Tasks {
			byStatus[domain.StatusBlocked] = append(byStatus[domain.StatusBlocked], task.ID)
		}
	}
	if len(columns) > 3 {
		for _, task := range columns[3].Tasks {
			byStatus[domain.StatusDone] = append(byStatus[domain.StatusDone], task.ID)
		}
	}

	return byStatus
}

func compactIDsByStatus(tasks []domain.Task) map[domain.Status][]string {
	byStatus := map[domain.Status][]string{
		domain.StatusOpen:       {},
		domain.StatusInProgress: {},
		domain.StatusBlocked:    {},
		domain.StatusDone:       {},
	}

	for _, task := range tasks {
		byStatus[task.Status] = append(byStatus[task.Status], task.ID)
	}

	return byStatus
}
