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

	var cardContent strings.Builder
	cardWidth := width - 2

	for i, task := range tasks {
		isCursor := isActive && i == cursorTask
		isSelected := selectedTasks[task.ID]

		var phaseInfo *phases.TaskPhaseInfo
		if info, exists := phaseData[task.ID]; exists {
			phaseInfo = &info
		}

		cardContent.WriteString(renderCard(task, isCursor, isSelected, cardWidth, phaseInfo, showPhases, s))
		cardContent.WriteString("\n")
	}

	vp := viewport.New(width, availableHeight)
	vp.SetContent(cardContent.String())

	// Calculate viewport offset to keep cursor visible
	// Each card is roughly cardHeight tall + 1 for newline
	// But let's just use LineDown based on index for now
	if cursorTask >= 0 && cursorTask < len(tasks) {
		linesPerCard := cardHeight + 1
		scrollLine := cursorTask * linesPerCard

		// Ensure we don't scroll past content
		vp.GotoTop()
		for i := 0; i < scrollLine; i++ {
			vp.LineDown(1)
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, vp.View())
}
