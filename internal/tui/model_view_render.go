package app

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/navigation"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/compact"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
	"github.com/riordanpawley/azedarach/internal/ui/statusbar"
)

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	if m.loading {
		return m.renderLoading()
	}

	var mainView string
	if m.viewMode == ViewModeCompact {
		mainView = m.renderCompactView()
	} else {
		mainView = m.renderBoardView()
	}

	// Clamp board/compact content to the space above the footer to keep
	// column headers and card rows stable even when internal render paths
	// overproduce lines (for example via wrapped content or spacing styles).
	mainView = lipgloss.NewStyle().
		Height(board.BoardContentHeight(m.height)).
		MaxHeight(board.BoardContentHeight(m.height)).
		Render(mainView)

	sb := statusbar.New(m.statusBarMode(), m.width, m.styles)
	sb.SetEventTicker(m.eventTicker)
	sb.SetCurrentProject(m.daemonProjectID())
	sb.SetSelectionSummary(m.selectionSummary())
	sb.SetFilterSummary(m.filterSummary())
	sb.SetSortSummary(m.sortSummary())
	if m.boardRefreshing {
		sb.SetModeSuffix(m.spinner.View())
	} else if m.runtimeSignalsBusy {
		sb.SetLoadingIndicator("Loading runtime status...")
	}
	if current := m.overlayStack.Current(); current != nil {
		if hintOverlay, ok := current.(interface {
			StatusBindings() []keybinds.Binding
		}); ok {
			sb.SetHintBindings(hintOverlay.StatusBindings())
		}
	}
	statusBarView := sb.Render()

	contentHeight := board.BoardContentHeight(m.height)
	contentView := lipgloss.NewStyle().
		MaxWidth(m.width).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Render(mainView)

	if !m.overlayStack.IsEmpty() {
		current := m.overlayStack.Current()
		overlayView := current.View()
		if overlayUsesFullScreen(current) {
			contentView = lipgloss.NewStyle().
				Width(m.width).
				MaxWidth(m.width).
				Height(contentHeight).
				MaxHeight(contentHeight).
				Render(overlayView)
			return lipgloss.JoinVertical(lipgloss.Left, contentView, statusBarView)
		}

		overlayWidth, overlayHeight := current.Size()

		if overlayWidth == 0 {
			contentView = lipgloss.NewStyle().
				Height(contentHeight).
				MaxHeight(contentHeight).
				Render(lipgloss.JoinVertical(lipgloss.Left, contentView, overlayView))
		} else {
			title := current.Title()
			if title != "" && !overlayUsesInternalTitle(current) {
				titleView := m.styles.OverlayTitle.Render(title)
				overlayView = lipgloss.JoinVertical(lipgloss.Left, titleView, overlayView)
			}
			if overlayUsesAppFrame(current) {
				overlayView = m.styles.Overlay.
					Width(overlayWidth).
					Height(overlayHeight).
					Render(overlayView)
			} else {
				overlayView = lipgloss.NewStyle().
					Width(overlayWidth).
					Height(overlayHeight).
					Render(overlayView)
			}
			overlayWidth, overlayHeight = renderedBlockSize(overlayView)

			contentView = lipgloss.NewStyle().
				MaxWidth(m.width).
				Height(contentHeight).
				MaxHeight(contentHeight).
				Render(contentView)
			contentView = m.layerCenteredOverlay(contentView, overlayView, m.width, contentHeight, overlayWidth, overlayHeight)
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, contentView, statusBarView)
}

func (m Model) statusBarMode() types.Mode {
	mode := m.editor.GetMode()
	current := m.overlayStack.Current()
	if current == nil {
		return mode
	}
	if modeOverlay, ok := current.(interface {
		StatusMode() types.Mode
	}); ok {
		return modeOverlay.StatusMode()
	}
	return mode
}

func (m Model) openOverlay(o overlay.Overlay) tea.Cmd {
	return tea.Batch(
		m.overlayStack.Push(o),
		func() tea.Msg {
			return tea.WindowSizeMsg{Width: m.width, Height: m.height}
		},
	)
}

func (m Model) layer(bottom, top string) string {
	return m.layerWithinHeight(bottom, top, m.height)
}

func overlayUsesInternalTitle(current overlay.Overlay) bool {
	internalTitleOverlay, ok := current.(interface {
		UsesInternalTitle() bool
	})
	return ok && internalTitleOverlay.UsesInternalTitle()
}

func overlayUsesAppFrame(current overlay.Overlay) bool {
	appFrameOverlay, ok := current.(interface {
		UsesAppFrame() bool
	})
	if !ok {
		return true
	}
	return appFrameOverlay.UsesAppFrame()
}

func overlayUsesFullScreen(current overlay.Overlay) bool {
	fullScreenOverlay, ok := current.(interface {
		UsesFullScreen() bool
	})
	return ok && fullScreenOverlay.UsesFullScreen()
}

func (m Model) layerWithinHeight(bottom, top string, height int) string {
	if height < 1 {
		height = 1
	}

	bLines := strings.Split(lipgloss.NewStyle().Height(height).MaxHeight(height).Render(bottom), "\n")
	tLines := strings.Split(lipgloss.NewStyle().Height(height).MaxHeight(height).Render(top), "\n")

	res := make([]string, height)
	for i := 0; i < height; i++ {
		var b, t string
		if i < len(bLines) {
			b = bLines[i]
		}
		if i < len(tLines) {
			t = tLines[i]
		}

		if strings.TrimSpace(t) == "" {
			res[i] = b
		} else {
			res[i] = t
		}
	}

	return strings.Join(res, "\n")
}

func (m Model) layerWithinHeightTransparent(bottom, top string, height int) string {
	if height < 1 {
		height = 1
	}

	bLines := strings.Split(lipgloss.NewStyle().Height(height).MaxHeight(height).Render(bottom), "\n")
	tLines := strings.Split(lipgloss.NewStyle().Height(height).MaxHeight(height).Render(top), "\n")

	res := make([]string, height)
	for i := 0; i < height; i++ {
		var b, t string
		if i < len(bLines) {
			b = bLines[i]
		}
		if i < len(tLines) {
			t = tLines[i]
		}

		if lineIsVisuallyEmpty(t) {
			res[i] = b
		} else {
			res[i] = mergeOverlayLine(b, t)
		}
	}

	return strings.Join(res, "\n")
}

func mergeOverlayLine(bottom, top string) string {
	left, right, ok := nonSpaceBounds(top)
	if !ok {
		return bottom
	}
	bottomWidth := ansi.StringWidth(bottom)
	if bottomWidth == 0 {
		return top
	}
	if left < 0 {
		left = 0
	}
	if right > bottomWidth {
		right = bottomWidth
	}
	if left >= right {
		return bottom
	}
	return ansi.Cut(bottom, 0, left) + ansi.Cut(top, left, right) + ansi.Cut(bottom, right, bottomWidth)
}

func (m Model) layerCenteredOverlay(bottom, overlayView string, width, height, overlayWidth, overlayHeight int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	if overlayWidth < 1 || overlayHeight < 1 {
		return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(bottom)
	}
	if overlayWidth > width {
		overlayWidth = width
	}
	if overlayHeight > height {
		overlayHeight = height
	}

	bLines := strings.Split(lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(bottom), "\n")
	oLines := strings.Split(lipgloss.NewStyle().Width(overlayWidth).Height(overlayHeight).MaxHeight(overlayHeight).Render(overlayView), "\n")

	x := max(0, (width-overlayWidth)/2)
	y := max(0, (height-overlayHeight)/2)
	res := make([]string, len(bLines))
	copy(res, bLines)

	for i := 0; i < overlayHeight && i < len(oLines); i++ {
		row := y + i
		if row < 0 || row >= len(res) {
			continue
		}
		base := res[row]
		overlaySlice := lipgloss.NewStyle().Width(overlayWidth).Render(ansi.Cut(oLines[i], 0, overlayWidth))
		res[row] = ansi.Cut(base, 0, x) + overlaySlice + ansi.Cut(base, x+overlayWidth, width)
	}

	return strings.Join(res, "\n")
}

func nonSpaceBounds(line string) (left int, right int, ok bool) {
	stripped := ansi.Strip(line)
	cellPos := 0
	left = -1
	right = -1
	for _, r := range stripped {
		width := ansi.StringWidth(string(r))
		if width < 1 {
			continue
		}
		if !unicode.IsSpace(r) {
			if left == -1 {
				left = cellPos
			}
			right = cellPos + width
		}
		cellPos += width
	}
	if left == -1 || right <= left {
		return 0, 0, false
	}
	return left, right, true
}

func lineIsVisuallyEmpty(line string) bool {
	withoutANSI := ansiEscapeLinePattern.ReplaceAllString(line, "")
	return strings.TrimSpace(withoutANSI) == ""
}

func renderedBlockSize(view string) (width, height int) {
	width = lipgloss.Width(view)
	height = lipgloss.Height(view)
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return width, height
}

// buildColumns converts tasks into board columns, applying filter and sort
func (m Model) buildColumns() []board.Column {
	// For Phase 1, use placeholder data
	if m.usePlaceholder {
		return board.CreatePlaceholderData()
	}

	// Apply filter to tasks and enforce board-level child hiding semantics.
	filteredTasks := m.boardVisibleTasks(m.tasks)

	// Build columns from filtered tasks
	return []board.Column{
		{Title: "Open", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusOpen)},
		{Title: "In Progress", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusInProgress)},
		{Title: "Blocked", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusBlocked)},
		{Title: "Done", Tasks: m.sortTasksInColumn(filteredTasks, domain.StatusDone)},
	}
}

func (m Model) boardVisibleTasks(tasks []domain.Task) []domain.Task {
	if m.isDrillDownActive() {
		filter := *m.editor.GetFilter()
		filter.HideEpicChildren = false
		filtered := filter.Apply(tasks)
		parentID := strings.TrimSpace(m.drillDownParentID)
		result := make([]domain.Task, 0, len(filtered))
		for _, task := range filtered {
			if isChildOfParent(task, parentID) {
				result = append(result, task)
			}
		}
		return result
	}

	filtered := m.editor.ApplyFilter(tasks)
	result := make([]domain.Task, 0, len(filtered))
	for _, task := range filtered {
		if task.ParentID != nil && strings.TrimSpace(*task.ParentID) != "" {
			continue
		}
		childByDependency := false
		for _, dep := range task.Dependencies {
			depType := strings.TrimSpace(string(dep.Type))
			if (depType == string(domain.DependencyParentChild) || depType == "parent_child") && strings.TrimSpace(dep.ID) != "" {
				childByDependency = true
				break
			}
		}
		if childByDependency {
			continue
		}
		result = append(result, task)
	}
	return result
}

func (m Model) runtimeSignalRefreshTasks() []domain.Task {
	if m.viewMode == ViewModeCompact {
		return m.compactRenderedTasks()
	}
	return m.boardRenderedTasks()
}

func (m Model) boardRenderedTasks() []domain.Task {
	columns := m.buildColumns()
	if len(columns) == 0 {
		return nil
	}
	visibleStart, visibleEnd := m.boardVisibleColumnRange(columns)
	visibleColumns := columns[visibleStart:visibleEnd]
	if len(visibleColumns) == 0 {
		return nil
	}

	bodyHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	columnCount := m.boardVisibleColumnCount(len(columns))
	if columnCount < 1 {
		columnCount = board.DefaultColumnCount
	}
	columnWidth := m.width / columnCount
	if columnWidth < 1 {
		columnWidth = 1
	}
	cardWidth := board.CardContentWidth(columnWidth)
	linesPerCard := board.CardLineFootprint(m.styles, cardWidth)
	if linesPerCard < 1 {
		linesPerCard = 1
	}

	rendered := make([]domain.Task, 0, len(m.tasks))
	seen := make(map[string]struct{}, len(m.tasks))
	for localColumn, col := range visibleColumns {
		globalColumn := visibleStart + localColumn
		viewportStart := 0
		if globalColumn >= 0 && globalColumn < len(m.viewportStarts) {
			viewportStart = m.viewportStarts[globalColumn]
		}
		start, end := board.VisibleTaskWindow(len(col.Tasks), viewportStart, bodyHeight, linesPerCard)
		for i := start; i < end; i++ {
			task := col.Tasks[i]
			if _, exists := seen[task.ID]; exists {
				continue
			}
			seen[task.ID] = struct{}{}
			rendered = append(rendered, task)
		}
	}
	return rendered
}

func (m Model) compactRenderedTasks() []domain.Task {
	filtered := m.editor.ApplySort(m.boardVisibleTasks(m.tasks))
	if len(filtered) == 0 {
		return nil
	}

	columns := m.buildColumns()
	pos := m.nav.GetPosition(columns)
	cursor := m.getFlatIndexFromPosition(pos, columns)
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(filtered) {
		cursor = len(filtered) - 1
	}

	visibleRows := board.BoardContentHeight(m.height) - 2
	if visibleRows < 1 {
		visibleRows = 1
	}

	scrollOffset := 0
	if cursor >= visibleRows {
		scrollOffset = cursor - visibleRows + 1
	}
	maxOffset := len(filtered) - visibleRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if scrollOffset > maxOffset {
		scrollOffset = maxOffset
	}

	end := scrollOffset + visibleRows
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[scrollOffset:end]
}

func isChildOfParent(task domain.Task, parentID string) bool {
	if parentID == "" {
		return false
	}
	if task.ParentID != nil && strings.TrimSpace(*task.ParentID) == parentID {
		return true
	}
	for _, dep := range task.Dependencies {
		depType := strings.TrimSpace(string(dep.Type))
		if (depType == string(domain.DependencyParentChild) || depType == "parent_child") && strings.TrimSpace(dep.ID) == parentID {
			return true
		}
	}
	return false
}

func (m Model) renderBoardView() string {
	// Build columns for the board
	columns := m.buildColumns()
	if len(columns) == 0 {
		return ""
	}
	visibleStart, visibleEnd := m.boardVisibleColumnRange(columns)
	visibleColumns := columns[visibleStart:visibleEnd]

	// Create cursor for board package using computed position
	pos := m.nav.GetPosition(columns)
	localColumn := pos.Column - visibleStart
	cursor := board.Cursor{
		Column: localColumn,
		Task:   pos.Task,
	}
	if localColumn < 0 || localColumn >= len(visibleColumns) {
		cursor.Column = -1
	}
	activeViewportStart := 0
	if pos.Column >= 0 && pos.Column < len(m.viewportStarts) {
		activeViewportStart = m.viewportStarts[pos.Column]
	}

	// Compute phase data if showPhases is enabled
	phaseData := make(map[string]phases.TaskPhaseInfo)
	if m.editor.GetShowPhases() {
		phaseData = m.computePhases()
	}

	contentHeight := board.BoardContentHeight(m.height)
	toolbar := ""
	if m.isDrillDownActive() {
		toolbar = m.renderDrillDownToolbar()
		contentHeight -= lipgloss.Height(toolbar) + 1
	}
	if contentHeight < 6 {
		contentHeight = 6
	}

	boardView := board.Render(
		visibleColumns,
		cursor,
		m.editor.GetSelectedTasks(),
		m.runtimeSignalsForBoard(),
		board.BuildChildProgress(m.tasks),
		phaseData,
		m.editor.GetShowPhases(),
		activeViewportStart,
		m.styles,
		m.width,
		contentHeight,
	)
	if toolbar == "" {
		return boardView
	}
	parts := make([]string, 0, 2)
	if toolbar != "" {
		parts = append(parts, toolbar)
	}
	parts = append(parts, boardView)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) runtimeSignalsForBoard() map[string]board.RuntimeSignals {
	activeDescendantSessionByTask := buildActiveDescendantSessionByTask(m.tasks)
	signalsByTask := make(map[string]board.RuntimeSignals, len(m.tasks)+len(m.pendingStatuses)+len(m.pendingOpsByTask)+len(activeDescendantSessionByTask))
	for _, task := range m.tasks {
		signalsByTask[task.ID] = board.RuntimeSignals{
			HasTmuxSession:        task.Session != nil,
			HasWorktree:           task.HasWorktree,
			GitAheadCount:         task.GitAheadCount,
			GitBehindCount:        task.GitBehindCount,
			HasUncommittedChanges: task.HasUncommittedChanges,
			GitAdditions:          task.GitAdditions,
			GitDeletions:          task.GitDeletions,
		}
	}

	for _, task := range m.tasks {
		pending, ok := m.pendingStatuses[taskIDKey(task.ID)]
		if !ok {
			continue
		}
		signals := signalsByTask[task.ID]
		signals.PendingOperationState = string(pending.state)
		signals.PendingOperationID = pending.operationID
		signalsByTask[task.ID] = signals
	}
	for _, task := range m.tasks {
		pending, ok := m.pendingOpsByTask[taskIDKey(task.ID)]
		if !ok {
			continue
		}
		signals := signalsByTask[task.ID]
		signals.PendingOperationState = string(pending.state)
		signals.PendingOperationID = pending.operationID
		signals.PendingOperationPercent = pending.percent
		signalsByTask[task.ID] = signals
	}
	for taskID := range activeDescendantSessionByTask {
		signals := signalsByTask[taskID]
		signals.HasDescendantTmuxSession = true
		signalsByTask[taskID] = signals
	}

	return signalsByTask
}

func buildActiveDescendantSessionByTask(tasks []domain.Task) map[string]bool {
	if len(tasks) == 0 {
		return nil
	}

	parentsByTask := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		taskID := strings.TrimSpace(task.ID)
		if taskID == "" {
			continue
		}

		parentSet := make(map[string]struct{}, 2)
		if task.ParentID != nil {
			if parentID := strings.TrimSpace(*task.ParentID); parentID != "" {
				parentSet[parentID] = struct{}{}
			}
		}
		for _, dep := range task.Dependencies {
			depType := strings.TrimSpace(string(dep.Type))
			if depType != string(domain.DependencyParentChild) && depType != "parent_child" {
				continue
			}
			if parentID := strings.TrimSpace(dep.ID); parentID != "" {
				parentSet[parentID] = struct{}{}
			}
		}

		if len(parentSet) == 0 {
			continue
		}
		parentIDs := make([]string, 0, len(parentSet))
		for parentID := range parentSet {
			parentIDs = append(parentIDs, parentID)
		}
		parentsByTask[taskID] = parentIDs
	}

	activeAncestorSessionByTask := make(map[string]bool)
	for _, task := range tasks {
		taskID := strings.TrimSpace(task.ID)
		if taskID == "" {
			continue
		}
		if task.Session == nil {
			continue
		}

		queue := append([]string(nil), parentsByTask[taskID]...)
		seen := make(map[string]struct{}, len(queue))
		for len(queue) > 0 {
			parentID := queue[0]
			queue = queue[1:]
			if parentID == "" {
				continue
			}
			if _, ok := seen[parentID]; ok {
				continue
			}
			seen[parentID] = struct{}{}
			activeAncestorSessionByTask[parentID] = true
			if grandparents := parentsByTask[parentID]; len(grandparents) > 0 {
				queue = append(queue, grandparents...)
			}
		}
	}

	if len(activeAncestorSessionByTask) == 0 {
		return nil
	}
	return activeAncestorSessionByTask
}

// renderCompactView renders the compact list view
func (m Model) renderCompactView() string {
	// Get all filtered and sorted tasks
	filteredTasks := m.boardVisibleTasks(m.tasks)
	sortedTasks := m.editor.ApplySort(filteredTasks)

	// Create compact view
	compactView := compact.NewCompactView(sortedTasks, m.width, board.BoardContentHeight(m.height))

	// Set cursor position based on current navigation
	// In compact mode, we use the flat task index
	columns := m.buildColumns()
	pos := m.nav.GetPosition(columns)
	flatIndex := m.getFlatIndexFromPosition(pos, columns)
	compactView.SetCursor(flatIndex)

	// Set selected tasks
	compactView.SetSelected(m.editor.GetSelectedTasks())

	return compactView.Render()
}

// getFlatIndexFromPosition converts a column/task position to a flat index
func (m Model) getFlatIndexFromPosition(pos navigation.Position, columns []board.Column) int {
	index := 0
	for i := 0; i < pos.Column && i < len(columns); i++ {
		index += len(columns[i].Tasks)
	}
	if pos.Column < len(columns) {
		index += pos.Task
	}
	return index
}

func (m Model) isDrillDownActive() bool {
	return strings.TrimSpace(m.drillDownParentID) != ""
}

func (m *Model) enterDrillDown(parentID, parentName string) {
	id := strings.TrimSpace(parentID)
	if id == "" {
		return
	}
	if m.isDrillDownActive() {
		m.drillDownTrail = append(m.drillDownTrail, drillDownContext{
			parentID:   strings.TrimSpace(m.drillDownParentID),
			parentName: strings.TrimSpace(m.drillDownParentName),
		})
	}
	m.drillDownParentID = id
	m.drillDownParentName = strings.TrimSpace(parentName)
}

func (m *Model) exitCurrentDrillDown() string {
	exitedParentID := strings.TrimSpace(m.drillDownParentID)
	if len(m.drillDownTrail) == 0 {
		m.clearDrillDown()
		return exitedParentID
	}

	prev := m.drillDownTrail[len(m.drillDownTrail)-1]
	m.drillDownTrail = m.drillDownTrail[:len(m.drillDownTrail)-1]
	m.drillDownParentID = prev.parentID
	m.drillDownParentName = prev.parentName
	return exitedParentID
}

func (m *Model) clearDrillDown() {
	m.drillDownParentID = ""
	m.drillDownParentName = ""
	m.drillDownTrail = nil
}

func (m Model) renderDrillDownToolbar() string {
	parentID := strings.TrimSpace(m.drillDownParentID)
	parentName := strings.TrimSpace(m.drillDownParentName)
	target := parentID
	if parentName != "" {
		target = fmt.Sprintf("%s %s", parentID, parentName)
	}
	left := m.styles.OverlayTitle.Render("Drill-down")
	body := m.styles.MenuItem.Render("Children of " + target)
	right := m.styles.StatusHint.Render("Esc: back to board  Space: details+actions")
	return lipgloss.JoinHorizontal(lipgloss.Left, left+"  ", body+"  ", right)
}

// openOrchestrationOverlay creates and opens the orchestration overlay
