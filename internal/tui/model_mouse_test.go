package app

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/board"
)

func TestMousePressFocusesBoardCard(t *testing.T) {
	m := newTestModel()
	columns := m.buildColumns()
	columnWidth := m.boardColumnLayout(columns).WidthForColumn(0)
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))

	updatedAny, cmd := m.Update(tea.MouseMsg{
		X:      2,
		Y:      1 + linesPerCard,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	if cmd != nil {
		t.Fatalf("expected no command")
	}
	updated := updatedAny.(Model)

	pos := getCursorPosition(updated)
	if !pos.Valid || pos.Column != 0 || pos.Task != 1 {
		t.Fatalf("cursor position = %+v, want open column task 1", pos)
	}
}

func TestMouseWheelMovesBoardCursorUnderPointer(t *testing.T) {
	m := newTestModel()

	updatedAny, cmd := m.Update(tea.MouseMsg{
		X:      2,
		Y:      2,
		Button: tea.MouseButtonWheelDown,
	})
	if cmd != nil {
		t.Fatalf("expected no command")
	}
	updated := updatedAny.(Model)

	pos := getCursorPosition(updated)
	if !pos.Valid || pos.Column != 0 || pos.Task != 1 {
		t.Fatalf("cursor position = %+v, want wheel to move from first to second open task", pos)
	}
}

func TestMouseDragScrollsBoardColumn(t *testing.T) {
	m := newTestModel()
	columns := m.buildColumns()
	columnWidth := m.boardColumnLayout(columns).WidthForColumn(0)
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))

	pressedAny, _ := m.Update(tea.MouseMsg{
		X:      2,
		Y:      1,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	pressed := pressedAny.(Model)
	draggedAny, cmd := pressed.Update(tea.MouseMsg{
		X:      2,
		Y:      1 - linesPerCard,
		Action: tea.MouseActionMotion,
		Button: tea.MouseButtonLeft,
	})
	if cmd != nil {
		t.Fatalf("expected no command")
	}
	dragged := draggedAny.(Model)

	pos := getCursorPosition(dragged)
	if !pos.Valid || pos.Column != 0 || pos.Task != 1 {
		t.Fatalf("cursor position = %+v, want drag up to move down within open column", pos)
	}
}

func TestRightMousePressAttachesBoardCard(t *testing.T) {
	m := newTestModel()
	columns := m.buildColumns()
	columnWidth := m.boardColumnLayout(columns).WidthForColumn(0)
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))

	updatedAny, cmd := m.Update(tea.MouseMsg{
		X:      2,
		Y:      1 + linesPerCard,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonRight,
	})
	if cmd == nil {
		t.Fatalf("expected attach command")
	}
	updated := updatedAny.(Model)

	pos := getCursorPosition(updated)
	if !pos.Valid || pos.Column != 0 || pos.Task != 1 {
		t.Fatalf("cursor position = %+v, want open column task 1", pos)
	}
	if len(updated.toasts) == 0 {
		t.Fatal("expected attach feedback toast")
	}
	if got := updated.toasts[len(updated.toasts)-1].Message; got != "Attach queued for az-2" {
		t.Fatalf("toast = %q, want attach feedback for az-2", got)
	}
}

func TestDoubleTapAttachesBoardCard(t *testing.T) {
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	mouseNow = func() time.Time { return now }
	t.Cleanup(func() { mouseNow = time.Now })

	m := newTestModel()
	columns := m.buildColumns()
	columnWidth := m.boardColumnLayout(columns).WidthForColumn(0)
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))
	msg := tea.MouseMsg{
		X:      2,
		Y:      1 + linesPerCard,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	}

	pressedAny, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("expected first tap to focus without command")
	}
	now = now.Add(300 * time.Millisecond)
	updatedAny, cmd := pressedAny.(Model).Update(msg)
	if cmd == nil {
		t.Fatalf("expected second tap to attach")
	}
	updated := updatedAny.(Model)

	pos := getCursorPosition(updated)
	if !pos.Valid || pos.Column != 0 || pos.Task != 1 {
		t.Fatalf("cursor position = %+v, want open column task 1", pos)
	}
	if len(updated.toasts) == 0 {
		t.Fatal("expected attach feedback toast")
	}
	if got := updated.toasts[len(updated.toasts)-1].Message; got != "Attach queued for az-2" {
		t.Fatalf("toast = %q, want attach feedback for az-2", got)
	}
}

func TestSlowSecondTapDoesNotAttachBoardCard(t *testing.T) {
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	mouseNow = func() time.Time { return now }
	t.Cleanup(func() { mouseNow = time.Now })

	m := newTestModel()
	columns := m.buildColumns()
	columnWidth := m.boardColumnLayout(columns).WidthForColumn(0)
	linesPerCard := board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))
	msg := tea.MouseMsg{
		X:      2,
		Y:      1 + linesPerCard,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	}

	pressedAny, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("expected first tap to focus without command")
	}
	now = now.Add(mouseDoubleTapWindow + time.Millisecond)
	_, cmd = pressedAny.(Model).Update(msg)
	if cmd != nil {
		t.Fatalf("expected slow second tap to focus without attach command")
	}
}

func TestHorizontalMouseWheelMovesAcrossBoardColumns(t *testing.T) {
	m := newTestModel()

	updatedAny, cmd := m.Update(tea.MouseMsg{
		X:      2,
		Y:      2,
		Button: tea.MouseButtonWheelRight,
	})
	if cmd != nil {
		t.Fatalf("expected no command")
	}
	updated := updatedAny.(Model)

	pos := getCursorPosition(updated)
	if !pos.Valid || pos.Column != 1 || pos.Task != 0 {
		t.Fatalf("cursor position = %+v, want first task in next column", pos)
	}
}

func TestMousePressFocusesTreeRow(t *testing.T) {
	m := newTestModel()
	m.boardView = domain.TreeBoardView()

	rendered := m.treeRenderedTasks()
	if len(rendered) == 0 {
		t.Fatal("expected tree tasks")
	}

	updatedAny, cmd := m.Update(tea.MouseMsg{
		X:      2,
		Y:      2,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	if cmd != nil {
		t.Fatalf("expected no command")
	}
	updated := updatedAny.(Model)

	task, _ := updated.getCurrentTaskAndSession()
	if task == nil || task.ID != rendered[0].ID {
		t.Fatalf("focused task = %v, want %s", task, rendered[0].ID)
	}
}

func TestMouseDragScrollsTreeList(t *testing.T) {
	m := newTestModel()
	m.boardView = domain.TreeBoardView()

	rendered := m.treeRenderedTasks()
	if len(rendered) < 2 {
		t.Fatal("expected at least two tree tasks")
	}

	pressedAny, _ := m.Update(tea.MouseMsg{
		X:      2,
		Y:      2,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	pressed := pressedAny.(Model)
	draggedAny, cmd := pressed.Update(tea.MouseMsg{
		X:      2,
		Y:      1,
		Action: tea.MouseActionMotion,
		Button: tea.MouseButtonLeft,
	})
	if cmd != nil {
		t.Fatalf("expected no command")
	}
	dragged := draggedAny.(Model)

	task, _ := dragged.getCurrentTaskAndSession()
	if task == nil || task.ID != rendered[1].ID {
		t.Fatalf("focused task = %v, want %s after drag", task, rendered[1].ID)
	}
}

func TestRightMousePressAttachesTreeRow(t *testing.T) {
	m := newTestModel()
	m.boardView = domain.TreeBoardView()

	rendered := m.treeRenderedTasks()
	if len(rendered) == 0 {
		t.Fatal("expected tree tasks")
	}

	updatedAny, cmd := m.Update(tea.MouseMsg{
		X:      2,
		Y:      2,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonRight,
	})
	if cmd == nil {
		t.Fatalf("expected attach command")
	}
	updated := updatedAny.(Model)

	task, _ := updated.getCurrentTaskAndSession()
	if task == nil || task.ID != rendered[0].ID {
		t.Fatalf("focused task = %v, want %s", task, rendered[0].ID)
	}
	if len(updated.toasts) == 0 {
		t.Fatal("expected attach feedback toast")
	}
	if got := updated.toasts[len(updated.toasts)-1].Message; got != "Attach queued for "+rendered[0].ID.String() {
		t.Fatalf("toast = %q, want attach feedback for %s", got, rendered[0].ID)
	}
}

func TestDoubleTapAttachesTreeRow(t *testing.T) {
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	mouseNow = func() time.Time { return now }
	t.Cleanup(func() { mouseNow = time.Now })

	m := newTestModel()
	m.boardView = domain.TreeBoardView()

	rendered := m.treeRenderedTasks()
	if len(rendered) == 0 {
		t.Fatal("expected tree tasks")
	}
	msg := tea.MouseMsg{
		X:      2,
		Y:      2,
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	}

	pressedAny, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("expected first tap to focus without command")
	}
	now = now.Add(300 * time.Millisecond)
	updatedAny, cmd := pressedAny.(Model).Update(msg)
	if cmd == nil {
		t.Fatalf("expected second tap to attach")
	}
	updated := updatedAny.(Model)

	task, _ := updated.getCurrentTaskAndSession()
	if task == nil || task.ID != rendered[0].ID {
		t.Fatalf("focused task = %v, want %s", task, rendered[0].ID)
	}
	if len(updated.toasts) == 0 {
		t.Fatal("expected attach feedback toast")
	}
	if got := updated.toasts[len(updated.toasts)-1].Message; got != "Attach queued for "+rendered[0].ID.String() {
		t.Fatalf("toast = %q, want attach feedback for %s", got, rendered[0].ID)
	}
}
