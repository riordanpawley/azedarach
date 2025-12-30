package board

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// Render renders the entire kanban board with 4 columns
func Render(
	columns []Column,
	cursor Cursor,
	selectedTasks map[string]bool,
	phaseData map[string]phases.TaskPhaseInfo,
	showPhases bool,
	s *styles.Styles,
	width int,
	height int,
) string {
	if len(columns) == 0 {
		return ""
	}

	// Calculate column width
	// 4 columns with gaps between them
	gap := 1
	totalGaps := (len(columns) - 1) * gap
	columnWidth := (width - totalGaps) / len(columns)

	// Render each column
	var columnStrings []string
	for i, col := range columns {
		isActive := i == cursor.Column
		cursorTask := 0
		if isActive {
			cursorTask = cursor.Task
		}

		// Subtract 1 from height to account for status bar
		columnStr := renderColumn(
			col.Title,
			col.Tasks,
			cursorTask,
			isActive,
			selectedTasks,
			phaseData,
			showPhases,
			columnWidth,
			height-1,
			s,
		)
		columnStrings = append(columnStrings, columnStr)
	}

	// Join columns horizontally with gaps
	return lipgloss.JoinHorizontal(lipgloss.Top, columnStrings...)
}
