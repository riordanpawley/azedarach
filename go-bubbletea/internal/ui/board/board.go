package board

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const statusBarHeight = 1

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

	columnWidth := width / len(columns)

	var columnStrings []string
	for i, col := range columns {
		isActive := i == cursor.Column
		cursorTask := 0
		if isActive {
			cursorTask = cursor.Task
		}

		columnStr := renderColumn(
			col.Title,
			col.Tasks,
			cursorTask,
			isActive,
			selectedTasks,
			phaseData,
			showPhases,
			columnWidth,
			height,
			s,
		)

		sized := lipgloss.NewStyle().Width(columnWidth).Render(columnStr)
		columnStrings = append(columnStrings, sized)
	}

	// Join columns horizontally
	return lipgloss.JoinHorizontal(lipgloss.Top, columnStrings...)
}
