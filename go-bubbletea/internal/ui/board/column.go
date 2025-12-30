package board

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// renderColumn renders a kanban column with header and task cards
func renderColumn(
	title string,
	tasks []domain.Task,
	cursorTask int,
	isActive bool,
	selectedTasks map[string]bool,
	phaseData map[string]phases.TaskPhaseInfo,
	showPhases bool,
	width int,
	height int,
	s *styles.Styles,
) string {
	// Choose header style based on whether this column is active
	headerStyle := s.ColumnHeader
	if isActive {
		headerStyle = s.ColumnHeaderActive
	}

	// Render header with title (e.g., "─ Open ─────")
	headerText := "─ " + title + " "
	remainingWidth := width - len(headerText) - 2 // Account for padding
	if remainingWidth > 0 {
		headerText += strings.Repeat("─", remainingWidth)
	}
	header := headerStyle.Render(headerText)

	// Render cards
	var cardStrings []string
	cardWidth := width - 4 // Account for column border and padding
	for i, task := range tasks {
		isCursor := isActive && i == cursorTask
		isSelected := selectedTasks[task.ID]

		// Get phase info for this task if available
		var phaseInfo *phases.TaskPhaseInfo
		if info, exists := phaseData[task.ID]; exists {
			phaseInfo = &info
		}

		cardStrings = append(cardStrings, renderCard(task, isCursor, isSelected, cardWidth, phaseInfo, showPhases, s))
	}

	// Handle empty column
	content := ""
	if len(cardStrings) > 0 {
		content = strings.Join(cardStrings, "\n")
	}

	// Apply column style
	columnStyle := s.Column.Width(width).Height(height)
	columnContent := columnStyle.Render(content)

	// Join header and column
	return lipgloss.JoinVertical(lipgloss.Left, header, columnContent)
}
