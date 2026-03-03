package app

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func stepModel(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("expected app.Model, got %T", next)
	}
	return nm
}

func makeLongColumnTasks(count int) []domain.Task {
	tasks := make([]domain.Task, 0, count+3)
	for i := 0; i < count; i++ {
		tasks = append(tasks, domain.Task{
			ID:       fmt.Sprintf("open-%02d", i),
			Title:    fmt.Sprintf("Open Task %02d", i),
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		})
	}
	tasks = append(tasks,
		domain.Task{ID: "ip-00", Title: "In Progress", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask},
		domain.Task{ID: "blk-00", Title: "Blocked", Status: domain.StatusBlocked, Priority: domain.P2, Type: domain.TypeTask},
		domain.Task{ID: "done-00", Title: "Done", Status: domain.StatusDone, Priority: domain.P2, Type: domain.TypeTask},
	)
	return tasks
}

func TestE2EScrolling_HalfPageRoundTrip(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.tasks = makeLongColumnTasks(40)
	m = stepModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m.nav.SelectTask("open-00", 0)

	start := getCursorPosition(m)
	m = stepModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlD})
	afterDown1 := getCursorPosition(m)
	m = stepModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlD})
	afterDown2 := getCursorPosition(m)

	if afterDown1.Task <= start.Task {
		t.Fatalf("expected first ctrl+d to move down: start=%d after=%d", start.Task, afterDown1.Task)
	}
	if afterDown2.Task <= afterDown1.Task {
		t.Fatalf("expected second ctrl+d to move further: first=%d second=%d", afterDown1.Task, afterDown2.Task)
	}

	m = stepModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	afterUp1 := getCursorPosition(m)
	m = stepModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	afterUp2 := getCursorPosition(m)

	if afterUp1.Task >= afterDown2.Task {
		t.Fatalf("expected ctrl+u to move up: down=%d up=%d", afterDown2.Task, afterUp1.Task)
	}
	if afterUp2.Task != 0 {
		t.Fatalf("expected second ctrl+u to clamp at top, got %d", afterUp2.Task)
	}
}

func TestE2EScrolling_ColumnSwitchAfterDeepScrollClampsSafely(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.tasks = makeLongColumnTasks(50)
	m = stepModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m.nav.SelectTask("open-00", 0)

	for i := 0; i < 6; i++ {
		m = stepModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlD})
	}
	deepPos := getCursorPosition(m)
	if deepPos.Column != 0 || deepPos.Task < 10 {
		t.Fatalf("expected deep scroll in open column, got col=%d task=%d", deepPos.Column, deepPos.Task)
	}

	m = stepModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	rightPos := getCursorPosition(m)
	if rightPos.Column != 1 {
		t.Fatalf("expected to move to in-progress column, got %d", rightPos.Column)
	}
	if rightPos.Task != 0 {
		t.Fatalf("expected row clamp in shorter column, got row=%d", rightPos.Task)
	}

	if m.nav.GetCursor().TaskID != "ip-00" {
		t.Fatalf("expected cursor task ip-00 after horizontal move, got %s", m.nav.GetCursor().TaskID)
	}
}

func TestE2EScrolling_FilterShrinkRecoversValidCursor(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.tasks = makeLongColumnTasks(30)
	m = stepModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	m.nav.SelectTask("open-00", 0)

	for i := 0; i < 4; i++ {
		m = stepModel(t, m, tea.KeyMsg{Type: tea.KeyCtrlD})
	}
	before := getCursorPosition(m)
	if before.Task < 4 {
		t.Fatalf("expected cursor deeper in list before filter, got %d", before.Task)
	}

	m = stepModel(t, m, overlay.SearchMsg{Query: "Open Task 00"})
	after := getCursorPosition(m)
	if !after.Valid {
		t.Fatal("expected valid cursor position after filter shrink")
	}
	if after.Column != 0 || after.Task != 0 {
		t.Fatalf("expected cursor to recover to first filtered task at (0,0), got (%d,%d)", after.Column, after.Task)
	}
	if m.nav.GetCursor().TaskID != "open-00" {
		t.Fatalf("expected cursor task to recover to open-00, got %s", m.nav.GetCursor().TaskID)
	}
}
