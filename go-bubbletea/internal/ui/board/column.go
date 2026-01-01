package board

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// cardHeight is the approximate height of a rendered card in lines
// Used for viewport scrolling calculations
const cardHeight = 5

// renderColumn renders a kanban column with header and task cards
// Uses viewport scrolling to only render visible cards
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

	// Render header with title and count (e.g., "Open (47)")
	headerText := fmt.Sprintf("%s (%d)", title, len(tasks))
	header := headerStyle.Width(width).Render(headerText)

	// Calculate available height for cards (subtract header)
	availableHeight := height - 2
	if availableHeight < cardHeight {
		availableHeight = cardHeight
	}

	// Calculate how many cards fit in the viewport
	visibleCount := availableHeight / cardHeight
	if visibleCount < 1 {
		visibleCount = 1
	}

	// Calculate scroll offset to keep cursor visible
	scrollOffset := 0
	if len(tasks) > visibleCount {
		// Center cursor in viewport when possible
		scrollOffset = cursorTask - visibleCount/2
		if scrollOffset < 0 {
			scrollOffset = 0
		}
		maxOffset := len(tasks) - visibleCount
		if scrollOffset > maxOffset {
			scrollOffset = maxOffset
		}
	}

	// Determine range of tasks to render
	startIdx := scrollOffset
	endIdx := scrollOffset + visibleCount
	if endIdx > len(tasks) {
		endIdx = len(tasks)
	}

	// Render only visible cards
	var cardStrings []string
	cardWidth := width - 2 // Small padding
	for i := startIdx; i < endIdx; i++ {
		task := tasks[i]
		isCursor := isActive && i == cursorTask
		isSelected := selectedTasks[task.ID]

		var phaseInfo *phases.TaskPhaseInfo
		if info, exists := phaseData[task.ID]; exists {
			phaseInfo = &info
		}

		cardStrings = append(cardStrings, renderCard(task, isCursor, isSelected, cardWidth, phaseInfo, showPhases, s))
	}

	// Add scroll indicator if needed
	var scrollIndicator string
	if len(tasks) > visibleCount {
		above := scrollOffset
		below := len(tasks) - endIdx
		if above > 0 && below > 0 {
			scrollIndicator = fmt.Sprintf("↑%d more  ↓%d more", above, below)
		} else if above > 0 {
			scrollIndicator = fmt.Sprintf("↑%d more", above)
		} else if below > 0 {
			scrollIndicator = fmt.Sprintf("↓%d more", below)
		}
	}

	// Build content
	content := ""
	if len(cardStrings) > 0 {
		content = strings.Join(cardStrings, "\n")
	}

	// Add scroll indicator at bottom
	if scrollIndicator != "" {
		indicatorStyle := lipgloss.NewStyle().Foreground(styles.Overlay0).Italic(true)
		content = content + "\n" + indicatorStyle.Render(scrollIndicator)
	}

	// Render content without additional column border/style
	// The board.go will handle positioning with lipgloss.Place
	return lipgloss.JoinVertical(lipgloss.Left, header, content)
}
