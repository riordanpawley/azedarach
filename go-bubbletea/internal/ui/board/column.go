package board

import (
	"fmt"
	"strings"

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
	viewportStart int,
	selectedTasks map[string]bool,
	runtimeSignalsByTask map[string]RuntimeSignals,
	childProgressByTask map[string]ChildProgress,
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

	bodyHeight := ColumnBodyHeight(height)
	cardWidth := CardContentWidth(width)
	linesPerCard := CardLineFootprint(s, cardWidth)
	start, end := VisibleTaskWindow(len(tasks), viewportStart, bodyHeight, linesPerCard)
	topIndicator := start > 0
	bottomIndicator := end < len(tasks)

	var cardContent strings.Builder
	if topIndicator {
		cardContent.WriteString(renderScrollIndicator(start, true, width, s))
	}

	for i := start; i < end; i++ {
		if cardContent.Len() > 0 {
			cardContent.WriteString("\n")
		}

		task := tasks[i]
		isCursor := isActive && i == cursorTask
		isSelected := selectedTasks[task.ID]

		var phaseInfo *phases.TaskPhaseInfo
		if info, exists := phaseData[task.ID]; exists {
			phaseInfo = &info
		}
		var childProgress *ChildProgress
		if progress, exists := childProgressByTask[task.ID]; exists && progress.Total > 0 {
			progressCopy := progress
			childProgress = &progressCopy
		}
		var runtimeSignals *RuntimeSignals
		if signals, exists := runtimeSignalsByTask[task.ID]; exists {
			signalsCopy := signals
			runtimeSignals = &signalsCopy
		}

		cardContent.WriteString(renderCard(task, runtimeSignals, isCursor, isSelected, cardWidth, childProgress, phaseInfo, showPhases, s))
	}

	if bottomIndicator {
		if cardContent.Len() > 0 {
			cardContent.WriteString("\n")
		}
		cardContent.WriteString(renderScrollIndicator(len(tasks)-end, false, width, s))
	}

	content := cardContent.String()
	if topIndicator && bottomIndicator {
		content = lipgloss.PlaceVertical(bodyHeight, lipgloss.Center, content)
	}

	columnBody := lipgloss.NewStyle().
		Width(width).
		Height(bodyHeight).
		Render(content)

	return lipgloss.JoinVertical(lipgloss.Left, header, columnBody)
}

// VisibleTaskWindow computes the rendered task window for a column.
// It accounts for conditional indicator rows at the top/bottom when overflow
// exists and returns [start, end) task indices that should be visible.
func VisibleTaskWindow(taskCount int, viewportStart int, bodyHeight int, linesPerCard int) (int, int) {
	if taskCount <= 0 {
		return 0, 0
	}

	topIndicator := false
	bottomIndicator := false
	start, end := 0, 0

	for i := 0; i < 6; i++ {
		reservedLines := 0
		if topIndicator {
			reservedLines++
		}
		if bottomIndicator {
			reservedLines++
		}

		cardHeight := bodyHeight - reservedLines
		start, end = visibleTaskRange(taskCount, viewportStart, cardHeight, linesPerCard)

		nextTop := start > 0
		nextBottom := end < taskCount
		if nextTop == topIndicator && nextBottom == bottomIndicator {
			break
		}
		topIndicator = nextTop
		bottomIndicator = nextBottom
	}

	return start, end
}

func visibleTaskRange(taskCount int, viewportStart int, availableHeight int, linesPerCard int) (int, int) {
	if taskCount <= 0 {
		return 0, 0
	}

	if linesPerCard < 1 {
		linesPerCard = 1
	}

	visibleCards := availableHeight / linesPerCard
	if visibleCards < 1 {
		visibleCards = 1
	}
	if visibleCards >= taskCount {
		return 0, taskCount
	}
	start := viewportStart
	if start < 0 {
		start = 0
	}
	maxStart := taskCount - visibleCards
	if start > maxStart {
		start = maxStart
	}
	return start, start + visibleCards
}

func renderScrollIndicator(count int, up bool, width int, s *styles.Styles) string {
	if count < 1 {
		return ""
	}

	arrow := "v"
	if up {
		arrow = "^"
	}
	text := fmt.Sprintf("%d more %s", count, arrow)
	return s.Separator.Copy().
		Foreground(styles.Text).
		Bold(true).
		Width(width).
		Align(lipgloss.Center).
		Render(text)
}
