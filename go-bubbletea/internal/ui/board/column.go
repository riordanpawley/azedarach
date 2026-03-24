package board

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const cardHeight = 5

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
	headerStyle := s.ColumnHeader
	if isActive {
		headerStyle = s.ColumnHeaderActive
	}

	headerText := fmt.Sprintf("%s (%d)", title, len(tasks))
	header := headerStyle.Width(width).Render(headerText)

	availableHeight := height - 2
	if availableHeight < 0 {
		availableHeight = 0
	}
	start, end := visibleTaskRange(len(tasks), cursorTask, availableHeight)

	var cardContent strings.Builder
	cardWidth := width - 2

	for i := start; i < end; i++ {
		task := tasks[i]
		isCursor := isActive && i == cursorTask
		isSelected := selectedTasks[task.ID]

		var phaseInfo *phases.TaskPhaseInfo
		if info, exists := phaseData[task.ID]; exists {
			phaseInfo = &info
		}

		cardContent.WriteString(renderCard(task, isCursor, isSelected, cardWidth, phaseInfo, showPhases, s))
		cardContent.WriteString("\n")
	}

	content := strings.TrimSuffix(cardContent.String(), "\n")
	columnBody := lipgloss.NewStyle().
		Width(width).
		Height(availableHeight).
		Render(content)

	return lipgloss.JoinVertical(lipgloss.Left, header, columnBody)
}

func visibleTaskRange(taskCount int, cursorTask int, availableHeight int) (int, int) {
	if taskCount <= 0 {
		return 0, 0
	}

	linesPerCard := cardHeight + 1
	visibleCards := availableHeight / linesPerCard
	if availableHeight%linesPerCard != 0 {
		visibleCards++
	}
	if visibleCards < 1 {
		visibleCards = 1
	}
	if visibleCards >= taskCount {
		return 0, taskCount
	}

	if cursorTask < 0 || cursorTask >= taskCount {
		return 0, visibleCards
	}

	start := cursorTask - visibleCards + 1
	if start < 0 {
		start = 0
	}
	maxStart := taskCount - visibleCards
	if start > maxStart {
		start = maxStart
	}
	return start, start + visibleCards
}
