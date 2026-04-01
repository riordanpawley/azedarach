// Package navigation provides cursor and navigation state management
package navigation

import (
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/board"
)

// Position represents a computed position in the board
type Position struct {
	Column int  // 0=Open, 1=InProgress, 2=Blocked, 3=Done
	Task   int  // Index within the column
	Valid  bool // Whether the position is valid
}

// Cursor tracks the selected task by ID (survives filter/sort changes)
type Cursor struct {
	TaskID         string // Primary state: selected task ID
	FallbackColumn int    // Column to use when TaskID not found
	FallbackTask   int    // Row hint to preserve neighbor position when TaskID not found
}

// FindPosition computes the position of the cursor's task in the given columns
func (c *Cursor) FindPosition(columns []board.Column) Position {
	if c.TaskID == "" {
		// No task selected, use fallback column and row hint.
		col := c.FallbackColumn
		if col >= len(columns) {
			col = 0
		}
		if col < len(columns) && len(columns[col].Tasks) > 0 {
			task := c.FallbackTask
			if task < 0 {
				task = 0
			}
			if task >= len(columns[col].Tasks) {
				task = len(columns[col].Tasks) - 1
			}
			return Position{Column: col, Task: task, Valid: true}
		}
		return Position{Column: col, Task: 0, Valid: false}
	}

	// Search for the task by ID
	for colIdx, col := range columns {
		for taskIdx, task := range col.Tasks {
			if task.ID == c.TaskID {
				c.FallbackColumn = colIdx
				c.FallbackTask = taskIdx
				return Position{Column: colIdx, Task: taskIdx, Valid: true}
			}
		}
	}

	// Task not found (filtered out/deleted?), use fallback location hint.
	col := c.FallbackColumn
	if col >= len(columns) {
		col = 0
	}
	if col < len(columns) && len(columns[col].Tasks) > 0 {
		task := c.FallbackTask
		if task < 0 {
			task = 0
		}
		if task >= len(columns[col].Tasks) {
			task = len(columns[col].Tasks) - 1
		}
		return Position{Column: col, Task: task, Valid: true}
	}
	return Position{Column: col, Task: 0, Valid: false}
}

// SetTask updates the cursor to point to a specific task
func (c *Cursor) SetTask(taskID string, column int) {
	c.SetTaskAt(taskID, column, 0)
}

// SetTaskAt updates the cursor to point to a specific task and row hint.
func (c *Cursor) SetTaskAt(taskID string, column int, task int) {
	c.TaskID = taskID
	c.FallbackColumn = column
	c.FallbackTask = task
}

// MoveVertical moves up or down within a column, returns new task ID
func (c *Cursor) MoveVertical(columns []board.Column, delta int) string {
	pos := c.FindPosition(columns)
	if !pos.Valid || pos.Column >= len(columns) {
		return c.TaskID
	}

	col := columns[pos.Column]
	newIdx := pos.Task + delta

	// Clamp to column bounds
	if newIdx < 0 {
		newIdx = 0
	}
	if newIdx >= len(col.Tasks) {
		newIdx = len(col.Tasks) - 1
	}

	if newIdx >= 0 && newIdx < len(col.Tasks) {
		c.TaskID = col.Tasks[newIdx].ID
		c.FallbackColumn = pos.Column
		c.FallbackTask = newIdx
	}
	return c.TaskID
}

// MoveHorizontal moves left or right to adjacent column
func (c *Cursor) MoveHorizontal(columns []board.Column, delta int) string {
	if len(columns) == 0 || delta == 0 {
		return c.TaskID
	}

	pos := c.FindPosition(columns)
	startCol := pos.Column
	if startCol < 0 {
		startCol = 0
	}
	if startCol >= len(columns) {
		startCol = len(columns) - 1
	}

	targetCol := -1
	for nextCol := startCol + delta; nextCol >= 0 && nextCol < len(columns); nextCol += delta {
		if len(columns[nextCol].Tasks) > 0 {
			targetCol = nextCol
			break
		}
	}

	if targetCol == -1 {
		// No non-empty column in the requested direction.
		return c.TaskID
	}

	c.FallbackColumn = targetCol
	taskIdx := pos.Task
	if taskIdx >= len(columns[targetCol].Tasks) {
		taskIdx = len(columns[targetCol].Tasks) - 1
	}
	if taskIdx < 0 {
		taskIdx = 0
	}
	c.TaskID = columns[targetCol].Tasks[taskIdx].ID
	c.FallbackTask = taskIdx
	return c.TaskID
}

// JumpToStart moves to first task in current column
func (c *Cursor) JumpToStart(columns []board.Column) string {
	pos := c.FindPosition(columns)
	if pos.Column < len(columns) && len(columns[pos.Column].Tasks) > 0 {
		c.TaskID = columns[pos.Column].Tasks[0].ID
		c.FallbackTask = 0
	}
	return c.TaskID
}

// JumpToEnd moves to last task in current column
func (c *Cursor) JumpToEnd(columns []board.Column) string {
	pos := c.FindPosition(columns)
	if pos.Column < len(columns) {
		col := columns[pos.Column]
		if len(col.Tasks) > 0 {
			c.TaskID = col.Tasks[len(col.Tasks)-1].ID
			c.FallbackTask = len(col.Tasks) - 1
		}
	}
	return c.TaskID
}

// JumpToColumn moves to a specific column, keeping relative row position
func (c *Cursor) JumpToColumn(columns []board.Column, colIdx int) string {
	if colIdx < 0 {
		colIdx = 0
	}
	if colIdx >= len(columns) {
		colIdx = len(columns) - 1
	}

	pos := c.FindPosition(columns)
	c.FallbackColumn = colIdx

	if colIdx < len(columns) && len(columns[colIdx].Tasks) > 0 {
		// Try to keep same row position, or clamp to column size
		taskIdx := pos.Task
		if taskIdx >= len(columns[colIdx].Tasks) {
			taskIdx = len(columns[colIdx].Tasks) - 1
		}
		c.TaskID = columns[colIdx].Tasks[taskIdx].ID
		c.FallbackTask = taskIdx
	} else {
		c.TaskID = "" // No task in target column
		c.FallbackTask = 0
	}
	return c.TaskID
}

// Service manages navigation state
type Service struct {
	cursor Cursor
}

// NewService creates a new navigation service
func NewService() *Service {
	return &Service{
		cursor: Cursor{},
	}
}

// GetCursor returns the current cursor (for read access)
func (s *Service) GetCursor() *Cursor {
	return &s.cursor
}

// GetPosition returns the computed position of the cursor in the given columns
func (s *Service) GetPosition(columns []board.Column) Position {
	return s.cursor.FindPosition(columns)
}

// GetCurrentTask returns the currently selected task and its session
func (s *Service) GetCurrentTask(columns []board.Column) (*domain.Task, *domain.Session) {
	pos := s.cursor.FindPosition(columns)
	if !pos.Valid || pos.Column >= len(columns) {
		return nil, nil
	}

	col := columns[pos.Column]
	if pos.Task >= len(col.Tasks) {
		return nil, nil
	}

	task := col.Tasks[pos.Task]
	return &task, task.Session
}

// GetCurrentStatus returns the status for the current column
func (s *Service) GetCurrentStatus(columns []board.Column) domain.Status {
	statuses := []domain.Status{
		domain.StatusOpen,
		domain.StatusInProgress,
		domain.StatusBlocked,
		domain.StatusDone,
	}
	pos := s.cursor.FindPosition(columns)
	if pos.Column < 0 || pos.Column >= len(statuses) {
		return domain.StatusOpen
	}
	return statuses[pos.Column]
}

// MoveDown moves cursor down in current column
func (s *Service) MoveDown(columns []board.Column) {
	s.cursor.MoveVertical(columns, 1)
}

// MoveUp moves cursor up in current column
func (s *Service) MoveUp(columns []board.Column) {
	s.cursor.MoveVertical(columns, -1)
}

// MoveLeft moves cursor to left column
func (s *Service) MoveLeft(columns []board.Column) {
	s.cursor.MoveHorizontal(columns, -1)
}

// MoveRight moves cursor to right column
func (s *Service) MoveRight(columns []board.Column) {
	s.cursor.MoveHorizontal(columns, 1)
}

// HalfPageDown moves cursor half a page down
func (s *Service) HalfPageDown(columns []board.Column, halfPage int) {
	s.cursor.MoveVertical(columns, halfPage)
}

// HalfPageUp moves cursor half a page up
func (s *Service) HalfPageUp(columns []board.Column, halfPage int) {
	s.cursor.MoveVertical(columns, -halfPage)
}

// GotoTop moves cursor to first task in column
func (s *Service) GotoTop(columns []board.Column) {
	s.cursor.JumpToStart(columns)
}

// GotoBottom moves cursor to last task in column
func (s *Service) GotoBottom(columns []board.Column) {
	s.cursor.JumpToEnd(columns)
}

// GotoFirstColumn moves cursor to first column
func (s *Service) GotoFirstColumn(columns []board.Column) {
	s.cursor.JumpToColumn(columns, 0)
}

// GotoLastColumn moves cursor to last column
func (s *Service) GotoLastColumn(columns []board.Column) {
	s.cursor.JumpToColumn(columns, len(columns)-1)
}

// SelectTask directly sets the cursor to a specific task
func (s *Service) SelectTask(taskID string, column int) {
	s.cursor.SetTask(taskID, column)
}

// JumpToTaskByIndex finds task by flat index across all columns
func (s *Service) JumpToTaskByIndex(columns []board.Column, flatIndex int) bool {
	currentIndex := 0
	for colIdx, col := range columns {
		for taskIdx, task := range col.Tasks {
			if currentIndex == flatIndex {
				s.cursor.SetTaskAt(task.ID, colIdx, taskIdx)
				return true
			}
			currentIndex++
		}
	}
	return false
}

// JumpToTaskByID finds and selects a task by ID
func (s *Service) JumpToTaskByID(columns []board.Column, taskID string) bool {
	for colIdx, col := range columns {
		for taskIdx, task := range col.Tasks {
			if task.ID == taskID {
				s.cursor.SetTaskAt(task.ID, colIdx, taskIdx)
				return true
			}
		}
	}
	return false
}
