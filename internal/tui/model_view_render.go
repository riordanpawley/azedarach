package app

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/core/phases"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/navigation"
	"github.com/riordanpawley/azedarach/internal/types"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/compact"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
	"github.com/riordanpawley/azedarach/internal/ui/statusbar"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
	"github.com/riordanpawley/azedarach/internal/ui/toast"
)

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	if m.loading {
		return m.renderLoading()
	}

	currentOverlay := m.overlayStack.Current()
	contentHeight := board.BoardContentHeight(m.height)
	mainView := ""
	if currentOverlay == nil || overlayUsesFullScreen(currentOverlay) {
		if m.viewMode == ViewModeCompact {
			mainView = m.renderCompactView()
		} else if m.viewMode == ViewModeOverview {
			mainView = m.renderOrchestrationOverview()
		} else if m.boardView.Normalized().Layout == domain.BoardViewLayoutTreeList {
			mainView = m.renderConfiguredListView()
		} else if m.boardView.Normalized().Layout == domain.BoardViewLayoutHorizontalGrid {
			mainView = m.renderConfiguredHorizontalGrid()
		} else {
			mainView = m.renderBoardView()
		}
	} else {
		mainView = m.renderModalBackdrop(contentHeight)
	}

	// Clamp board/compact content to the space above the footer to keep
	// column headers and card rows stable even when internal render paths
	// overproduce lines (for example via wrapped content or spacing styles).
	mainView = lipgloss.NewStyle().
		Height(contentHeight).
		MaxHeight(contentHeight).
		Render(mainView)

	sb := statusbar.New(m.statusBarMode(), m.width, m.styles)
	sb.SetCurrentProject(m.currentProject)
	sb.SetSelectionSummary(m.selectionSummary())
	sb.SetFilterSummary(m.filterSummary())
	sb.SetSortSummary(m.sortSummary())
	sb.SetAlertIndicator(m.alertIndicator())
	if m.boardRefreshing {
		sb.SetModeSuffix(m.spinner.View())
	}
	if currentOverlay == nil && m.viewMode == ViewModeOverview {
		sb.SetHintBindings(orchestrationOverviewStatusBindings())
	}
	if current := currentOverlay; current != nil {
		bindings := []keybinds.Binding(nil)
		if hintOverlay, ok := current.(interface {
			StatusBindings() []keybinds.Binding
		}); ok {
			bindings = append(bindings, hintOverlay.StatusBindings()...)
		}
		bindings = append(bindings, keybinds.Binding{Key: "ctrl+g", Description: "close all"})
		if len(bindings) > 0 {
			sb.SetHintBindings(bindings)
		}
	}
	statusBarView := sb.Render()

	contentView := lipgloss.NewStyle().
		MaxWidth(m.width).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Render(mainView)

	if currentOverlay != nil {
		current := currentOverlay
		overlayView := current.View()
		if overlayUsesFullScreen(current) {
			contentView = lipgloss.NewStyle().
				Width(m.width).
				MaxWidth(m.width).
				Height(contentHeight).
				MaxHeight(contentHeight).
				Render(overlayView)
			contentView = m.layerNotificationStack(contentView, m.width, contentHeight)
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

	contentView = m.layerNotificationStack(contentView, m.width, contentHeight)
	return lipgloss.JoinVertical(lipgloss.Left, contentView, statusBarView)
}

func (m Model) renderConfiguredHorizontalGrid() string {
	visible := m.boardVisibleTasks(m.tasks)
	visibleByID := make(map[string]domain.Task, len(visible))
	for _, task := range visible {
		visibleByID[task.ID.String()] = task
	}
	ordered := make([]domain.Task, 0, len(visible))
	for _, projected := range m.boardOrdered {
		if task, ok := visibleByID[projected.ID.String()]; ok {
			ordered = append(ordered, task)
		}
	}
	if len(m.boardOrdered) == 0 {
		projection, err := domain.ProjectTasksByBoardView(m.boardView, visible)
		if err == nil {
			ordered = projection.OrderedTasks()
		}
	}
	if sortState := m.editor.GetSort(); sortState != nil && m.editor.IsSortExplicit() {
		ordered = sortState.ApplyInPlace(ordered)
	}
	if len(ordered) == 0 {
		return ""
	}
	cardWidth, columns := horizontalGridGeometry(m.width)
	selectedID := ""
	if m.nav != nil && m.nav.GetCursor() != nil {
		selectedID = m.nav.GetCursor().TaskID
	}
	cards := make([]string, 0, len(ordered))
	for _, task := range ordered {
		style := lipgloss.NewStyle().Width(max(1, cardWidth-4)).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(styles.Surface0)
		if task.ID.String() == selectedID {
			style = style.BorderForeground(styles.Blue).Bold(true)
		}
		bodyWidth := max(1, cardWidth-4)
		body := ansi.Truncate(task.ID.String()+" "+task.Title, bodyWidth, "…") + "\n" + ansi.Truncate(task.Status.String(), bodyWidth, "…")
		cards = append(cards, style.Render(body))
	}
	rows := make([]string, 0, (len(cards)+columns-1)/columns)
	for start := 0; start < len(cards); start += columns {
		end := min(len(cards), start+columns)
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards[start:end]...))
	}
	return m.overlayFreshnessIndicator(strings.Join(rows, "\n"), board.BoardContentHeight(m.height))
}

func horizontalGridGeometry(width int) (cardWidth, columns int) {
	if width < 1 {
		width = 1
	}
	cardWidth = min(32, max(4, width/3))
	if cardWidth > width {
		cardWidth = width
	}
	columns = max(1, width/cardWidth)
	return cardWidth, columns
}

func orchestrationOverviewStatusBindings() []keybinds.Binding {
	return []keybinds.Binding{
		{Key: "↑/↓ j/k", Description: "row"},
		{Key: "←/→ h/l", Description: "project"},
		{Key: "Home/End g/G", Description: "top/end"},
		{Key: "Enter", Description: "open"},
		{Key: "o/A", Description: "start/attach orchestrator"},
		{Key: "r", Description: "refresh status"},
		{Key: "Tab", Description: "switch view"},
		{Key: "/", Description: "search"},
		{Key: "f", Description: "filter"},
	}
}

func (m Model) layerNotificationStack(contentView string, width, height int) string {
	if width < 1 || height < 1 || len(m.toasts) == 0 {
		return contentView
	}

	stack := toast.New(m.styles).RenderWithin(m.toasts, width, notificationStackHeight(height))
	if strings.TrimSpace(ansi.Strip(stack)) == "" {
		return contentView
	}

	stackWidth, stackHeight := renderedBlockSize(stack)
	if stackWidth > width {
		stackWidth = width
	}
	if stackHeight > height {
		stackHeight = height
	}

	x := width - stackWidth
	if x < 0 {
		x = 0
	}
	y := height - stackHeight
	if y < 0 {
		y = 0
	}

	stackLines := strings.Split(lipgloss.NewStyle().
		Width(stackWidth).
		MaxWidth(stackWidth).
		Height(stackHeight).
		MaxHeight(stackHeight).
		Render(stack), "\n")
	contentLines := strings.Split(lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Height(height).
		MaxHeight(height).
		Render(contentView), "\n")
	if len(contentLines) < height {
		for len(contentLines) < height {
			contentLines = append(contentLines, lipgloss.NewStyle().Width(width).Render(""))
		}
	}
	for i, line := range stackLines {
		row := y + i
		if row < 0 || row >= height || row >= len(contentLines) {
			continue
		}
		base := contentLines[row]
		stackSlice := lipgloss.NewStyle().
			Width(stackWidth).
			MaxWidth(stackWidth).
			Render(ansi.Cut(line, 0, stackWidth))
		contentLines[row] = ansi.Cut(base, 0, x) + stackSlice + ansi.Cut(base, x+stackWidth, width)
	}

	return strings.Join(contentLines[:height], "\n")
}

func notificationStackHeight(height int) int {
	if height < 1 {
		return 0
	}
	limit := height / 2
	if limit < 3 {
		limit = height
	}
	if limit > 12 {
		limit = 12
	}
	if limit > height {
		limit = height
	}
	return limit
}

func (m Model) renderModalBackdrop(contentHeight int) string {
	if m.width < 1 || contentHeight < 1 {
		return ""
	}
	header := m.styles.StatusInfo.Render(fmt.Sprintf(
		" %s  %d issues ",
		strings.TrimSpace(m.currentProject),
		len(m.tasks),
	))
	if strings.TrimSpace(m.currentProject) == "" {
		header = m.styles.StatusInfo.Render(fmt.Sprintf(" %d issues ", len(m.tasks)))
	}
	return lipgloss.NewStyle().
		Width(m.width).
		Height(contentHeight).
		MaxHeight(contentHeight).
		Render(header)
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
	// Apply filter to tasks and enforce board-level child hiding semantics.
	filteredTasks := m.boardVisibleTasks(m.tasks)
	if m.boardView.Normalized().Layout == domain.BoardViewLayoutHorizontalGrid {
		return m.horizontalGridNavigationColumns(filteredTasks)
	}
	if columns, ok := m.configuredBoardColumns(filteredTasks); ok {
		return m.applyBoardColumnSort(columns)
	}

	phases := domain.IssueDisplayPhasesForTasks(filteredTasks)
	columns := make([]board.Column, 0, len(phases))
	columnByPhase := make(map[domain.IssueDisplayPhase]int, len(phases))
	for _, phase := range phases {
		columnByPhase[phase] = len(columns)
		columns = append(columns, board.Column{Title: phase.Label()})
	}
	for _, task := range filteredTasks {
		facts := task.IssueFacts()
		column, ok := columnByPhase[facts.DisplayPhase]
		if !ok || column < 0 || column >= len(columns) {
			continue
		}
		columns[column].Tasks = append(columns[column].Tasks, task)
	}

	return m.applyBoardColumnSort(columns)
}

func (m Model) horizontalGridNavigationColumns(visible []domain.Task) []board.Column {
	visibleByID := make(map[string]domain.Task, len(visible))
	for _, task := range visible {
		visibleByID[task.ID.String()] = task
	}
	ordered := make([]domain.Task, 0, len(visible))
	for _, projected := range m.boardOrdered {
		if task, ok := visibleByID[projected.ID.String()]; ok {
			ordered = append(ordered, task)
		}
	}
	if len(m.boardOrdered) == 0 {
		if projection, err := domain.ProjectTasksByBoardView(m.boardView, visible); err == nil {
			ordered = projection.OrderedTasks()
		}
	}
	_, columnCount := horizontalGridGeometry(m.width)
	columns := make([]board.Column, min(columnCount, len(ordered)))
	for i, task := range ordered {
		columns[i%len(columns)].Tasks = append(columns[i%len(columns)].Tasks, task)
	}
	return columns
}

func (m Model) configuredBoardColumns(filteredTasks []domain.Task) ([]board.Column, bool) {
	if len(m.boardColumns) > 0 {
		return m.boardColumnsFromSnapshot(filteredTasks), true
	}
	if !hasConfiguredBoardView(m.boardView) {
		return nil, false
	}
	grouped, err := domain.GroupTasksByBoardView(m.boardView, filteredTasks)
	if err != nil {
		return nil, false
	}
	return boardColumnsFromViewSnapshots(grouped, m.boardView), true
}

func (m Model) boardColumnsFromSnapshot(filteredTasks []domain.Task) []board.Column {
	visibleByID := make(map[string]domain.Task, len(filteredTasks))
	for _, task := range filteredTasks {
		if id := strings.TrimSpace(task.ID.String()); id != "" {
			visibleByID[id] = task
		}
	}
	columns := make([]board.Column, 0, len(m.boardColumns))
	for _, snapshotColumn := range m.boardColumns {
		column := board.Column{Title: strings.TrimSpace(snapshotColumn.Definition.Title)}
		if column.Title == "" {
			column.Title = string(snapshotColumn.Definition.ID)
		}
		for _, task := range snapshotColumn.Tasks {
			if current, ok := visibleByID[strings.TrimSpace(task.ID.String())]; ok {
				column.Tasks = append(column.Tasks, current)
			}
		}
		if m.boardView.Options.HideEmptyColumns && len(column.Tasks) == 0 {
			continue
		}
		columns = append(columns, column)
	}
	return columns
}

func boardColumnsFromViewSnapshots(snapshots []domain.BoardViewColumnSnapshot, view domain.BoardView) []board.Column {
	columns := make([]board.Column, 0, len(snapshots))
	for _, snapshot := range snapshots {
		column := board.Column{Title: strings.TrimSpace(snapshot.Definition.Title)}
		if column.Title == "" {
			column.Title = string(snapshot.Definition.ID)
		}
		column.Tasks = append(column.Tasks, snapshot.Tasks...)
		if view.Options.HideEmptyColumns && len(column.Tasks) == 0 {
			continue
		}
		columns = append(columns, column)
	}
	return columns
}

func hasConfiguredBoardView(view domain.BoardView) bool {
	return view.ID != "" || strings.TrimSpace(view.Title) != "" || len(view.Columns) > 0
}

func (m Model) applyBoardColumnSort(columns []board.Column) []board.Column {
	if len(columns) == 0 {
		return columns
	}
	var activeDescendantSessionByTask map[string]bool
	sortState := m.editor.GetSort()
	if m.editor.IsSortExplicit() && sortState != nil && sortState.Field == domain.SortBySession {
		activeDescendantSessionByTask = buildActiveDescendantSessionByTask(m.tasks)
	}
	for i := range columns {
		if len(activeDescendantSessionByTask) > 0 {
			for j := range columns[i].Tasks {
				if activeDescendantSessionByTask[columns[i].Tasks[j].ID.String()] {
					columns[i].Tasks[j].HasTmuxSession = true
				}
			}
		}
		if m.editor.IsSortExplicit() && sortState != nil {
			columns[i].Tasks = sortState.ApplyInPlace(columns[i].Tasks)
		}
	}
	return columns
}

func (m Model) boardVisibleTasks(tasks []domain.Task) []domain.Task {
	if m.isDrillDownActive() {
		filter := *m.editor.GetFilter()
		filter.HideEpicChildren = false
		filtered := filter.Apply(tasks)
		filtered = m.applySessionTreeFilter(filtered)
		parentID := strings.TrimSpace(m.drillDownParentID)
		result := make([]domain.Task, 0, len(filtered))
		for _, task := range filtered {
			if isChildOfParent(task, parentID) {
				result = append(result, task)
			}
		}
		return result
	}
	if m.boardProjection.View.ID != "" {
		filter := *m.editor.GetFilter()
		filter.HideEpicChildren = false
		return m.applySessionTreeFilter(filter.Apply(tasks))
	}

	if m.sessionTreeFilterOnly {
		filter := *m.editor.GetFilter()
		filter.HideEpicChildren = false
		filtered := filter.Apply(tasks)
		return m.applySessionTreeFilter(filtered)
	}

	filter := m.editor.GetFilter()
	result := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if filter != nil && !filter.Matches(task) {
			continue
		}
		if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
			continue
		}
		childByDependency := false
		for _, dep := range task.Dependencies {
			depType := strings.TrimSpace(string(dep.Type))
			if (depType == string(domain.DependencyParentChild) || depType == "parent_child") && strings.TrimSpace(dep.ID.String()) != "" {
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

func (m Model) applySessionTreeFilter(tasks []domain.Task) []domain.Task {
	if !m.sessionTreeFilterOnly {
		return tasks
	}
	visibleByID := taskIDsWithSessionInTree(m.tasks)
	if len(visibleByID) == 0 {
		return nil
	}
	result := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if visibleByID[strings.TrimSpace(task.ID.String())] {
			result = append(result, task)
		}
	}
	return result
}

func taskIDsWithSessionInTree(tasks []domain.Task) map[string]bool {
	descendantSessionByTask := buildActiveDescendantSessionByTask(tasks)
	result := make(map[string]bool, len(descendantSessionByTask))
	for taskID := range descendantSessionByTask {
		result[taskID] = true
	}
	for _, task := range tasks {
		taskID := strings.TrimSpace(task.ID.String())
		if taskID == "" {
			continue
		}
		if task.Session != nil || task.HasTmuxSession {
			result[taskID] = true
		}
	}
	if len(result) == 0 {
		return nil
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
	layout := m.boardColumnLayout(columns)
	visibleStart, visibleEnd := layout.Range()
	visibleColumns := columns[visibleStart:visibleEnd]
	if len(visibleColumns) == 0 {
		return nil
	}

	bodyHeight := board.ColumnBodyHeight(board.BoardContentHeight(m.height))
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	rendered := make([]domain.Task, 0, len(m.tasks))
	seen := make(map[string]struct{}, len(m.tasks))
	for localColumn, col := range visibleColumns {
		globalColumn := visibleStart + localColumn
		cardWidth := board.CardContentWidth(layout.WidthForColumn(globalColumn))
		linesPerCard := board.CardLineFootprint(m.styles, cardWidth)
		if linesPerCard < 1 {
			linesPerCard = 1
		}
		viewportStart := 0
		if globalColumn >= 0 && globalColumn < len(m.viewportStarts) {
			viewportStart = m.viewportStarts[globalColumn]
		}
		start, end := board.VisibleTaskWindow(len(col.Tasks), viewportStart, bodyHeight, linesPerCard)
		for i := start; i < end; i++ {
			task := col.Tasks[i]
			taskID := task.ID.String()
			if _, exists := seen[taskID]; exists {
				continue
			}
			seen[taskID] = struct{}{}
			rendered = append(rendered, task)
		}
	}
	return rendered
}

func (m Model) jumpLabelsByTask() map[string]string {
	if m.jumpMode == nil || len(m.jumpTargets) == 0 {
		return nil
	}
	labels := make(map[string]string, len(m.jumpTargets))
	for i, taskID := range m.jumpTargets {
		taskID = strings.TrimSpace(taskID)
		label := strings.TrimSpace(m.jumpMode.GetLabel(i))
		if taskID != "" && label != "" {
			labels[taskID] = label
		}
	}
	return labels
}

func (m Model) compactRenderedTasks() []domain.Task {
	filtered := m.boardVisibleTasks(m.tasks)
	if sortState := m.editor.GetSort(); sortState != nil {
		filtered = sortState.ApplyInPlace(filtered)
	}
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
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) == parentID {
		return true
	}
	for _, dep := range task.Dependencies {
		depType := strings.TrimSpace(string(dep.Type))
		if (depType == string(domain.DependencyParentChild) || depType == "parent_child") && strings.TrimSpace(dep.ID.String()) == parentID {
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
	layout := m.boardColumnLayout(columns)
	visibleStart, visibleEnd := layout.Range()
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
	if pickToolbar := m.renderMergePickToolbar(); pickToolbar != "" {
		if toolbar != "" {
			toolbar = lipgloss.JoinVertical(lipgloss.Left, toolbar, pickToolbar)
			contentHeight -= lipgloss.Height(pickToolbar)
		} else {
			toolbar = pickToolbar
			contentHeight -= lipgloss.Height(pickToolbar) + 1
		}
	}
	if contentHeight < 6 {
		contentHeight = 6
	}

	renderOpts := []board.RenderOption{}
	if candidates := m.mergePickCandidatesByTask(); len(candidates) > 0 {
		renderOpts = append(renderOpts, board.WithMergeCandidates(candidates))
	}

	boardView := board.Render(
		visibleColumns,
		cursor,
		m.editor.GetSelectedTasks(),
		m.runtimeSignalsForBoard(),
		board.BuildChildProgress(m.tasks),
		phaseData,
		m.editor.GetShowPhases(),
		m.jumpLabelsByTask(),
		activeViewportStart,
		m.styles,
		m.width,
		contentHeight,
		renderOpts...,
	)
	boardView = m.overlayFreshnessIndicator(boardView, contentHeight)
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
	if len(m.pendingStatuses) == 0 && len(m.pendingOpsByTask) == 0 && len(m.pendingFailures) == 0 && len(activeDescendantSessionByTask) == 0 && len(m.tasks) == 0 {
		return nil
	}

	signalsByTask := make(map[string]board.RuntimeSignals, len(m.tasks)+len(m.pendingStatuses)+len(m.pendingOpsByTask)+len(m.pendingFailures)+len(activeDescendantSessionByTask))
	for i := range m.tasks {
		task := m.tasks[i]
		signals := board.RuntimeSignals{
			HasTmuxSession:        task.Session != nil || task.HasTmuxSession,
			HasWorktree:           task.HasWorktree,
			GitAheadCount:         task.GitAheadCount,
			GitBehindCount:        task.GitBehindCount,
			HasUncommittedChanges: task.HasUncommittedChanges,
			HasConflicts:          task.HasConflicts,
			ConflictFiles:         append([]string(nil), task.ConflictFiles...),
			GitAdditions:          task.GitAdditions,
			GitDeletions:          task.GitDeletions,
		}
		if task.Session != nil {
			signals.TmuxAttached = task.Session.TmuxAttached
			signals.TmuxAttachedCount = task.Session.TmuxAttachedCount
		}
		taskID := task.ID.String()
		if runtime, ok := m.runtimeSignalsByTask[taskID]; ok {
			signals.TmuxAttached = runtime.TmuxAttached
			signals.TmuxAttachedCount = runtime.TmuxAttachedCount
			signals.PendingOperationState = runtime.PendingOperationState
			signals.PendingOperationID = runtime.PendingOperationID
			signals.PendingOperationPercent = runtime.PendingOperationPercent
			if m.shouldSuppressWorktreeOperationMarker(task, "git.runtime", protocol.OperationState(runtime.PendingOperationState)) {
				signals.PendingOperationState = ""
				signals.PendingOperationID = ""
				signals.PendingOperationPercent = 0
			}
		}
		taskKey := taskIDKey(taskID)
		pending, ok := m.pendingStatuses[taskKey]
		if !ok {
			signalsByTask[taskID] = signals
			continue
		}
		signals.PendingOperationState = string(pending.state)
		signals.PendingOperationID = pending.operationID
		signalsByTask[taskID] = signals
	}
	for i := range m.tasks {
		task := m.tasks[i]
		taskID := task.ID.String()
		pending, ok := m.pendingOpsByTask[taskIDKey(taskID)]
		if !ok {
			continue
		}
		if m.shouldSuppressWorktreeOperationMarker(task, pending.kind, pending.state) {
			continue
		}
		signals := signalsByTask[taskID]
		signals.PendingOperationState = string(pending.state)
		signals.PendingOperationID = pending.operationID
		signals.PendingOperationPercent = pending.percent
		signalsByTask[taskID] = signals
	}
	for i := range m.tasks {
		task := m.tasks[i]
		taskID := task.ID.String()
		failure, ok := m.pendingFailures[taskIDKey(taskID)]
		if !ok {
			continue
		}
		if m.shouldSuppressWorktreeOperationMarker(task, failure.action, protocol.OperationStateFailed) {
			continue
		}
		signals := signalsByTask[taskID]
		signals.PendingOperationState = string(protocol.OperationStateFailed)
		signals.PendingOperationID = failure.operationID
		signals.PendingOperationPercent = 0
		signalsByTask[taskID] = signals
	}
	for taskID := range activeDescendantSessionByTask {
		signals := signalsByTask[taskID]
		signals.HasDescendantTmuxSession = true
		signalsByTask[taskID] = signals
	}

	return signalsByTask
}

func (m Model) shouldSuppressWorktreeOperationMarker(task domain.Task, kind string, state protocol.OperationState) bool {
	if task.Status != domain.StatusOpen {
		return false
	}
	if state != protocol.OperationStateFailed && state != protocol.OperationStateCancelled {
		return false
	}
	return pendingOperationRequiresWorktree(kind) && !m.taskHasWorktreeRuntimeSignal(task)
}

func (m Model) taskHasWorktreeRuntimeSignal(task domain.Task) bool {
	if task.HasWorktree {
		return true
	}
	if task.Session != nil && strings.TrimSpace(task.Session.Worktree) != "" {
		return true
	}
	taskID := strings.TrimSpace(task.ID.String())
	if taskID == "" {
		return false
	}
	if path := strings.TrimSpace(m.runtimeSignalWorktreeByTask[taskID]); path != "" {
		return true
	}
	if signals, ok := m.runtimeSignalsByTask[taskID]; ok && signals.HasWorktree {
		return true
	}
	return false
}

func pendingOperationRequiresWorktree(kind string) bool {
	kind = strings.TrimSpace(kind)
	return strings.HasPrefix(kind, "git.") || strings.HasPrefix(kind, "worktree.")
}

func buildActiveDescendantSessionByTask(tasks []domain.Task) map[string]bool {
	if len(tasks) == 0 {
		return nil
	}

	parentsByTask := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		taskID := strings.TrimSpace(task.ID.String())
		if taskID == "" {
			continue
		}

		parentSet := make(map[string]struct{}, 2)
		if task.ParentID != nil {
			if parentID := strings.TrimSpace(task.ParentID.String()); parentID != "" {
				parentSet[parentID] = struct{}{}
			}
		}
		for _, dep := range task.Dependencies {
			depType := strings.TrimSpace(string(dep.Type))
			if depType != string(domain.DependencyParentChild) && depType != "parent_child" {
				continue
			}
			if parentID := strings.TrimSpace(dep.ID.String()); parentID != "" {
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
		taskID := strings.TrimSpace(task.ID.String())
		if taskID == "" {
			continue
		}
		if task.Session == nil && !task.HasTmuxSession {
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
	sortedTasks := filteredTasks
	if sortState := m.editor.GetSort(); sortState != nil {
		sortedTasks = sortState.ApplyInPlace(sortedTasks)
	}

	// Create compact view
	compactView := compact.NewCompactView(sortedTasks, m.width, board.BoardContentHeight(m.height))

	compactView.SetCursor(m.compactCursorIndex(sortedTasks))

	// Set selected tasks
	compactView.SetSelected(m.editor.GetSelectedTasks())

	rendered := compactView.Render()
	return m.overlayFreshnessIndicator(rendered, board.BoardContentHeight(m.height))
}

func (m Model) renderConfiguredListView() string {
	visible := m.boardVisibleTasks(m.tasks)
	visibleByID := make(map[string]domain.Task, len(visible))
	for _, task := range visible {
		visibleByID[task.ID.String()] = task
	}
	ordered := make([]domain.Task, 0, len(visible))
	projectedItems := m.boardProjection.Items
	for _, projected := range m.boardOrdered {
		if task, ok := visibleByID[projected.ID.String()]; ok {
			ordered = append(ordered, task)
			delete(visibleByID, projected.ID.String())
		}
	}
	if len(m.boardOrdered) == 0 {
		projection, err := domain.ProjectTasksByBoardView(m.boardView, visible)
		if err == nil {
			ordered = projection.OrderedTasks()
			projectedItems = projection.Items
		}
	}
	if sortState := m.editor.GetSort(); sortState != nil && m.editor.IsSortExplicit() {
		ordered = sortState.ApplyInPlace(ordered)
	}
	ordered = decorateConfiguredTreeTasks(ordered, projectedItems)
	compactView := compact.NewCompactView(ordered, m.width, board.BoardContentHeight(m.height))
	compactView.SetCursor(m.compactCursorIndex(ordered))
	compactView.SetSelected(m.editor.GetSelectedTasks())
	return m.overlayFreshnessIndicator(compactView.Render(), board.BoardContentHeight(m.height))
}

func decorateConfiguredTreeTasks(tasks []domain.Task, items []domain.BoardViewProjectedItem) []domain.Task {
	depths := make(map[string]int, len(items))
	for _, item := range items {
		depths[item.Task.ID.String()] = item.Depth
	}
	result := append([]domain.Task(nil), tasks...)
	for i := range result {
		depth := depths[result[i].ID.String()]
		if depth > 0 {
			result[i].Title = strings.Repeat("  ", depth-1) + "└ " + result[i].Title
		}
	}
	return result
}

func (m Model) compactCursorIndex(tasks []domain.Task) int {
	if len(tasks) == 0 || m.nav == nil || m.nav.GetCursor() == nil {
		return 0
	}
	cursor := m.nav.GetCursor()
	taskID := strings.TrimSpace(cursor.TaskID)
	if taskID != "" {
		for i := range tasks {
			if tasks[i].ID.String() == taskID {
				return i
			}
		}
	}
	index := cursor.FallbackTask
	if cursor.FallbackColumn > 0 {
		statuses := []domain.Status{
			domain.StatusOpen,
			domain.StatusInProgress,
			domain.StatusInReview,
			domain.StatusDone,
		}
		if cursor.FallbackColumn < len(statuses) {
			targetStatus := statuses[cursor.FallbackColumn]
			seenInColumn := 0
			for i := range tasks {
				if tasks[i].Status != targetStatus {
					continue
				}
				if seenInColumn >= cursor.FallbackTask {
					return i
				}
				seenInColumn++
			}
		}
	}
	return clampInt(index, 0, len(tasks)-1)
}

func (m Model) overlayFreshnessIndicator(content string, height int) string {
	indicator := m.renderFreshnessIndicator()
	if indicator == "" || m.width <= 0 || height <= 0 {
		return content
	}
	overlayLines := make([]string, height)
	overlayLines[0] = lipgloss.NewStyle().
		Width(m.width).
		MaxWidth(m.width).
		Align(lipgloss.Right).
		Render(indicator)
	return m.layerWithinHeightTransparent(content, strings.Join(overlayLines, "\n"), height)
}

func (m Model) renderFreshnessIndicator() string {
	if m.taskSnapshotCheckedAt.IsZero() || !m.taskSnapshotFreshness.Valid() {
		return ""
	}

	label := string(m.taskSnapshotFreshness)
	if m.taskSnapshotFreshness == protocol.TaskListFreshnessFresh {
		label = "fresh"
	}
	if m.taskSnapshotFreshness == protocol.TaskListFreshnessStale {
		label = "stale"
	}

	timestamp := m.taskSnapshotCheckedAt.UTC().Format("15:04:05")
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#a6adc8")).
		Background(lipgloss.Color("#313244")).
		Padding(0, 1)
	switch m.taskSnapshotFreshness {
	case protocol.TaskListFreshnessFresh:
		style = style.Foreground(lipgloss.Color("#a6e3a1"))
	case protocol.TaskListFreshnessStale:
		style = style.Foreground(lipgloss.Color("#f9e2af"))
	}
	return style.Render(fmt.Sprintf("%s %s", label, timestamp))
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
	right := m.styles.StatusHint.Render("Esc: back  Space: details  a: attach")
	return lipgloss.JoinHorizontal(lipgloss.Left, left+"  ", body+"  ", right)
}

// openOrchestrationOverlay creates and opens the orchestration overlay
