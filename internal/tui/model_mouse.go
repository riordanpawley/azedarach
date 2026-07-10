package app

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/board"
)

const mouseDoubleTapWindow = 700 * time.Millisecond

var mouseNow = time.Now

type boardMouseHit struct {
	column int
	task   int
	valid  bool
}

type mouseDragState struct {
	active  bool
	compact bool
	column  int
	lastY   int
}

type mouseTapState struct {
	taskID string
	at     time.Time
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.width <= 0 || m.height <= 0 {
		return m, nil
	}
	if !m.overlayStack.IsEmpty() {
		return m, m.overlayStack.Update(msg)
	}
	if m.projectSwitchInFlight {
		return m, nil
	}

	switch msg.Button {
	case tea.MouseButtonLeft:
		switch msg.Action {
		case tea.MouseActionPress:
			return m.handleMousePress(msg)
		case tea.MouseActionMotion:
			return m.handleMouseDrag(msg)
		case tea.MouseActionRelease:
			m.mouseDrag = mouseDragState{}
			return m, nil
		default:
			return m, nil
		}
	case tea.MouseButtonRight:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		return m.handleMouseAttach(msg)
	case tea.MouseButtonMiddle:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		return m.handleMouseAttach(msg)
	case tea.MouseButtonWheelUp:
		return m.handleMouseWheel(msg, -1)
	case tea.MouseButtonWheelDown:
		return m.handleMouseWheel(msg, 1)
	case tea.MouseButtonWheelLeft:
		return m.handleMouseHorizontalWheel(msg, -1)
	case tea.MouseButtonWheelRight:
		return m.handleMouseHorizontalWheel(msg, 1)
	default:
		return m, nil
	}
}

func (m Model) handleMousePress(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch m.viewMode {
	case ViewModeCompact:
		if taskID := m.compactTaskAtMouse(msg); taskID != "" {
			m.mouseDrag = mouseDragState{active: true, compact: true, lastY: msg.Y}
			columns := m.buildColumns()
			if m.nav.JumpToTaskByID(columns, taskID) {
				m.ensureCursorVisible(columns)
			}
			if m.isMouseDoubleTap(taskID) {
				return m.attachFocusedTask(taskID)
			}
			m.rememberMouseTap(taskID)
		} else {
			m.mouseDrag = mouseDragState{}
			m.mouseTap = mouseTapState{}
		}
	default:
		columns := m.buildColumns()
		if hit := m.boardTaskAtMouse(msg, columns); hit.valid {
			task := columns[hit.column].Tasks[hit.task]
			taskID := task.ID.String()
			m.mouseDrag = mouseDragState{active: true, column: hit.column, lastY: msg.Y}
			m.nav.JumpToTaskByID(columns, taskID)
			m.ensureCursorVisible(columns)
			if m.isMouseDoubleTap(taskID) {
				return m.attachFocusedTask(taskID)
			}
			m.rememberMouseTap(taskID)
		} else if col, ok := m.boardColumnAtMouse(msg, columns); ok {
			m.mouseDrag = mouseDragState{active: true, column: col, lastY: msg.Y}
			m.mouseTap = mouseTapState{}
			m.selectNearestTaskInColumn(columns, col)
			m.ensureCursorVisible(columns)
		} else {
			m.mouseDrag = mouseDragState{}
			m.mouseTap = mouseTapState{}
		}
	}
	return m, nil
}

func (m Model) handleMouseDrag(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.mouseDrag.active {
		return m, nil
	}

	deltaY := msg.Y - m.mouseDrag.lastY
	step := 1
	if !m.mouseDrag.compact {
		columns := m.buildColumns()
		layout := m.boardColumnLayout(columns)
		columnWidth := layout.WidthForColumn(m.mouseDrag.column)
		step = board.CardLineFootprint(m.styles, board.CardContentWidth(columnWidth))
	}
	if step < 1 {
		step = 1
	}
	if deltaY > -step && deltaY < step {
		return m, nil
	}
	m.mouseTap = mouseTapState{}

	direction := -1
	consumedSign := 1
	if deltaY < 0 {
		direction = 1
		consumedSign = -1
	}
	count := deltaY / step
	if count < 0 {
		count = -count
	}
	if count < 1 {
		count = 1
	}

	columns := m.buildColumns()
	if m.mouseDrag.compact {
		for i := 0; i < count; i++ {
			if direction > 0 {
				m.nav.MoveDown(columns)
			} else {
				m.nav.MoveUp(columns)
			}
		}
		m.ensureCursorVisible(columns)
		m.mouseDrag.lastY += consumedSign * count * step
		return m, nil
	}

	m.selectNearestTaskInColumn(columns, m.mouseDrag.column)
	for i := 0; i < count; i++ {
		if direction > 0 {
			m.nav.MoveDown(columns)
		} else {
			m.nav.MoveUp(columns)
		}
	}
	m.ensureCursorVisible(columns)
	m.mouseDrag.lastY += consumedSign * count * step
	return m, nil
}

func (m Model) handleMouseAttach(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	var taskID string
	switch m.viewMode {
	case ViewModeCompact:
		taskID = m.compactTaskAtMouse(msg)
		if taskID == "" {
			return m, nil
		}
		columns := m.buildColumns()
		m.nav.JumpToTaskByID(columns, taskID)
		m.ensureCursorVisible(columns)
	default:
		columns := m.buildColumns()
		hit := m.boardTaskAtMouse(msg, columns)
		if !hit.valid {
			return m, nil
		}
		task := columns[hit.column].Tasks[hit.task]
		taskID = task.ID.String()
		m.nav.JumpToTaskByID(columns, taskID)
		m.ensureCursorVisible(columns)
	}
	return m.attachFocusedTask(taskID)
}

func (m Model) attachFocusedTask(taskID string) (tea.Model, tea.Cmd) {
	m.mouseTap = mouseTapState{}
	m.mouseDrag = mouseDragState{}
	m.beginMutationFeedback(fmt.Sprintf("Attach queued for %s", taskID))
	return m, m.attachSessionCmd(taskID)
}

func (m Model) isMouseDoubleTap(taskID string) bool {
	if taskID == "" || m.mouseTap.taskID != taskID || m.mouseTap.at.IsZero() {
		return false
	}
	return mouseNow().Sub(m.mouseTap.at) <= mouseDoubleTapWindow
}

func (m *Model) rememberMouseTap(taskID string) {
	m.mouseTap = mouseTapState{taskID: taskID, at: mouseNow()}
}

func (m Model) handleMouseWheel(msg tea.MouseMsg, delta int) (tea.Model, tea.Cmd) {
	switch m.viewMode {
	case ViewModeCompact:
		if !m.compactMouseInBounds(msg) {
			return m, nil
		}
		columns := m.buildColumns()
		if taskID := m.compactTaskAtMouse(msg); taskID != "" {
			m.nav.JumpToTaskByID(columns, taskID)
		}
		if delta < 0 {
			m.nav.MoveUp(columns)
		} else {
			m.nav.MoveDown(columns)
		}
		m.ensureCursorVisible(columns)
	default:
		columns := m.buildColumns()
		if hit := m.boardTaskAtMouse(msg, columns); hit.valid {
			task := columns[hit.column].Tasks[hit.task]
			m.nav.JumpToTaskByID(columns, task.ID.String())
		} else if col, ok := m.boardColumnAtMouse(msg, columns); ok {
			m.selectNearestTaskInColumn(columns, col)
		} else {
			return m, nil
		}
		if delta < 0 {
			m.nav.MoveUp(columns)
		} else {
			m.nav.MoveDown(columns)
		}
		m.ensureCursorVisible(columns)
	}
	return m, nil
}

func (m Model) handleMouseHorizontalWheel(msg tea.MouseMsg, delta int) (tea.Model, tea.Cmd) {
	if m.viewMode == ViewModeCompact {
		return m, nil
	}
	columns := m.buildColumns()
	col, ok := m.boardColumnAtMouse(msg, columns)
	if !ok {
		return m, nil
	}
	m.selectNearestTaskInColumn(columns, col)
	if delta < 0 {
		m.nav.MoveLeft(columns)
	} else {
		m.nav.MoveRight(columns)
	}
	m.ensureCursorVisible(columns)
	return m, nil
}

func (m Model) boardTaskAtMouse(msg tea.MouseMsg, columns []board.Column) boardMouseHit {
	col, ok := m.boardColumnAtMouse(msg, columns)
	if !ok || col < 0 || col >= len(columns) {
		return boardMouseHit{}
	}
	_, boardTop, boardHeight := m.boardMouseLayout()
	bodyY := msg.Y - boardTop - board.ColumnHeaderLines
	bodyHeight := board.ColumnBodyHeight(boardHeight)
	if bodyY < 0 || bodyY >= bodyHeight {
		return boardMouseHit{}
	}

	layout := m.boardColumnLayout(columns)
	cardWidth := board.CardContentWidth(layout.WidthForColumn(col))
	linesPerCard := board.CardLineFootprint(m.styles, cardWidth)
	if linesPerCard < 1 {
		linesPerCard = 1
	}

	viewportStart := 0
	if col >= 0 && col < len(m.viewportStarts) {
		viewportStart = m.viewportStarts[col]
	}
	tasks := columns[col].Tasks
	start, end := board.VisibleTaskWindow(len(tasks), viewportStart, bodyHeight, linesPerCard)
	if start >= end {
		return boardMouseHit{}
	}

	topIndicator, bottomIndicator := board.VisibleScrollIndicators(len(tasks), start, end, bodyHeight, linesPerCard)
	if topIndicator && bottomIndicator {
		contentHeight := (end - start) * linesPerCard
		if topIndicator {
			contentHeight++
		}
		if bottomIndicator {
			contentHeight++
		}
		if padding := (bodyHeight - contentHeight) / 2; padding > 0 {
			bodyY -= padding
		}
	}
	if topIndicator {
		if bodyY == 0 {
			return boardMouseHit{}
		}
		bodyY--
	}
	if bodyY < 0 {
		return boardMouseHit{}
	}
	offset := bodyY / linesPerCard
	task := start + offset
	if task < start || task >= end {
		return boardMouseHit{}
	}
	return boardMouseHit{column: col, task: task, valid: true}
}

func (m Model) boardColumnAtMouse(msg tea.MouseMsg, columns []board.Column) (int, bool) {
	if msg.X < 0 || msg.X >= m.width {
		return 0, false
	}
	_, boardTop, boardHeight := m.boardMouseLayout()
	if msg.Y < boardTop || msg.Y >= boardTop+boardHeight {
		return 0, false
	}

	return m.boardColumnLayout(columns).ColumnAt(msg.X)
}

func (m Model) boardMouseLayout() (toolbarHeight int, boardTop int, boardHeight int) {
	boardHeight = board.BoardContentHeight(m.height)
	if m.isDrillDownActive() {
		toolbarHeight += lipgloss.Height(m.renderDrillDownToolbar())
		boardHeight -= toolbarHeight + 1
	}
	if pickToolbar := m.renderMergePickToolbar(); pickToolbar != "" {
		pickHeight := lipgloss.Height(pickToolbar)
		if toolbarHeight == 0 {
			toolbarHeight = pickHeight
			boardHeight -= pickHeight + 1
		} else {
			toolbarHeight += pickHeight
			boardHeight -= pickHeight
		}
	}
	if boardHeight < 6 {
		boardHeight = 6
	}
	return toolbarHeight, toolbarHeight, boardHeight
}

func (m *Model) selectNearestTaskInColumn(columns []board.Column, col int) {
	if col < 0 || col >= len(columns) || len(columns[col].Tasks) == 0 {
		return
	}
	pos := m.nav.GetPosition(columns)
	taskIdx := pos.Task
	if !pos.Valid || pos.Column != col {
		taskIdx = 0
	}
	if taskIdx >= len(columns[col].Tasks) {
		taskIdx = len(columns[col].Tasks) - 1
	}
	if taskIdx < 0 {
		taskIdx = 0
	}
	task := columns[col].Tasks[taskIdx]
	m.nav.JumpToTaskByID(columns, task.ID.String())
}

func (m Model) compactTaskAtMouse(msg tea.MouseMsg) string {
	if !m.compactMouseInBounds(msg) {
		return ""
	}
	row := msg.Y - 2
	tasks := m.compactRenderedTasks()
	if row < 0 || row >= len(tasks) {
		return ""
	}
	return tasks[row].ID.String()
}

func (m Model) compactMouseInBounds(msg tea.MouseMsg) bool {
	if msg.X < 0 || msg.X >= m.width {
		return false
	}
	contentHeight := board.BoardContentHeight(m.height)
	return msg.Y >= 2 && msg.Y < contentHeight
}
