package board

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

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

	availableHeight := max(1, height-2)

	var cardContent strings.Builder
	cardWidth := width - 2
	cardStarts := make([]int, len(tasks))
	cardHeights := make([]int, len(tasks))
	totalLines := 0

	for i, task := range tasks {
		isCursor := isActive && i == cursorTask
		isSelected := selectedTasks[task.ID]

		var phaseInfo *phases.TaskPhaseInfo
		if info, exists := phaseData[task.ID]; exists {
			phaseInfo = &info
		}

		card := renderCard(task, isCursor, isSelected, cardWidth, phaseInfo, showPhases, s)
		cardStarts[i] = totalLines
		cardHeight := max(1, lipgloss.Height(card))
		cardHeights[i] = cardHeight
		totalLines += cardHeight

		cardContent.WriteString(card)
		if i < len(tasks)-1 {
			cardContent.WriteString("\n")
			totalLines++
		}
	}

	vp := viewport.New(width, availableHeight)
	vp.SetContent(cardContent.String())

	if cursorTask >= 0 && cursorTask < len(tasks) {
		cursorStart := cardStarts[cursorTask]
		cursorEnd := cursorStart + cardHeights[cursorTask] - 1
		offset := 0
		if cursorEnd >= availableHeight {
			offset = cursorEnd - availableHeight + 1
		}
		maxOffset := max(0, totalLines-availableHeight)
		if offset > maxOffset {
			offset = maxOffset
		}
		vp.YOffset = offset
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, vp.View())
}
