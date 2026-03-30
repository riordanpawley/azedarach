package board

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const statusBarHeight = 1

// Render renders the visible kanban board columns.
func Render(
	columns []Column,
	cursor Cursor,
	selectedTasks map[string]bool,
	runtimeSignalsByTask map[string]RuntimeSignals,
	childProgressByTask map[string]ChildProgress,
	phaseData map[string]phases.TaskPhaseInfo,
	showPhases bool,
	activeViewportStart int,
	s *styles.Styles,
	width int,
	height int,
) string {
	if len(columns) == 0 {
		return ""
	}

	columnWidth := width / len(columns)

	columnStrings := make([]string, len(columns))
	for i, col := range columns {
		isActive := i == cursor.Column
		cursorTask := -1
		viewportStart := 0
		if isActive {
			cursorTask = cursor.Task
			viewportStart = activeViewportStart
		}

		columnStrings[i] = renderColumn(
			col.Title,
			col.Tasks,
			cursorTask,
			isActive,
			viewportStart,
			selectedTasks,
			runtimeSignalsByTask,
			childProgressByTask,
			phaseData,
			showPhases,
			columnWidth,
			height,
			s,
		)
	}

	// Join columns horizontally
	return lipgloss.JoinHorizontal(lipgloss.Top, columnStrings...)
}
