package overlay

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const (
	BoardViewSelectKey = "board_view_select"
	BoardViewSaveKey   = "board_view_save"
	BoardViewDeleteKey = "board_view_delete"
)

type BoardViewSelectMsg struct {
	ViewID string
}

type BoardViewSaveMsg struct {
	View  domain.BoardView
	Scope protocol.GlobalViewScope
}
type BoardViewDeleteMsg struct{ ViewID string }

type boardViewOverlayMode int

const (
	boardViewBrowse boardViewOverlayMode = iota
	boardViewEdit
	boardViewAdvancedEdit
	boardViewConfirmDelete
)

type viewConfigurator struct {
	view                                        domain.BoardView
	title                                       textinput.Model
	field                                       int
	filter                                      int
	grouping                                    int
	sortPreset                                  int
	scope                                       int
	projects                                    textinput.Model
	filterChanged, groupingChanged, sortChanged bool
}

type BoardViewOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	views          []domain.BoardViewRecord
	selectedViewID string
	cursor         int
	mode           boardViewOverlayMode
	editor         textarea.Model
	configurator   viewConfigurator
	errorText      string
	lockedEditID   domain.BoardViewID
	global         bool
	globalScopes   map[domain.BoardViewID]protocol.GlobalViewScope
	styles         *Styles
}

// NewGlobalBoardViewOverlay configures user-level views while retaining their
// daemon-owned project scopes.
func NewGlobalBoardViewOverlay(views []protocol.GlobalViewRecord, selectedViewID string) *BoardViewOverlay {
	records := make([]domain.BoardViewRecord, 0, len(views))
	scopes := make(map[domain.BoardViewID]protocol.GlobalViewScope, len(views))
	builtIns := make(map[domain.BoardViewID]struct{})
	for _, view := range domain.BuiltInBoardViews() {
		builtIns[view.ID] = struct{}{}
	}
	for _, record := range views {
		_, builtIn := builtIns[record.View.ID]
		records = append(records, domain.BoardViewRecord{ProjectID: "global", View: record.View, BuiltIn: builtIn})
		scopes[record.View.ID] = record.Scope
	}
	o := NewBoardViewOverlay(records, selectedViewID)
	o.global, o.globalScopes = true, scopes
	return o
}

func NewBoardViewOverlay(views []domain.BoardViewRecord, selectedViewID string) *BoardViewOverlay {
	copied := append([]domain.BoardViewRecord(nil), views...)
	selectedViewID = domain.NormalizeBoardViewID(selectedViewID)
	cursor := 0
	for i, record := range copied {
		if string(record.View.ID) == selectedViewID {
			cursor = i
			break
		}
	}
	editor := textarea.New()
	editor.Placeholder = "Advanced View definition JSON"
	editor.SetWidth(66)
	editor.SetHeight(16)
	return &BoardViewOverlay{
		views:          copied,
		selectedViewID: selectedViewID,
		cursor:         cursor,
		editor:         editor,
		styles:         New(),
	}
}

func (o *BoardViewOverlay) Init() tea.Cmd { return nil }

func (o *BoardViewOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		o.ApplyWindowSize(size)
		return o, nil
	}
	if o.mode == boardViewEdit {
		return o.updateConfigurator(msg)
	}
	if o.mode == boardViewAdvancedEdit {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "esc":
				o.mode, o.errorText = boardViewEdit, ""
				o.editor.Blur()
				return o, nil
			case "ctrl+s":
				var view domain.BoardView
				if err := json.Unmarshal([]byte(o.editor.Value()), &view); err != nil {
					o.errorText = "Invalid JSON: " + err.Error()
					return o, nil
				}
				view = view.Normalized()
				if o.lockedEditID != "" && view.ID != o.lockedEditID {
					o.errorText = fmt.Sprintf("Validation: id is fixed at %q while editing; rename the title instead", o.lockedEditID)
					return o, nil
				}
				if err := view.Validate(); err != nil {
					o.errorText = "Validation: " + err.Error()
					return o, nil
				}
				return o, func() tea.Msg {
					return SelectionMsg{Key: BoardViewSaveKey, Value: BoardViewSaveMsg{View: view, Scope: o.configuredScope()}}
				}
			}
		}
		var cmd tea.Cmd
		o.editor, cmd = o.editor.Update(msg)
		return o, cmd
	}
	if o.mode == boardViewConfirmDelete {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "y", "Y":
				id := string(o.views[o.cursor].View.ID)
				return o, func() tea.Msg { return SelectionMsg{Key: BoardViewDeleteKey, Value: BoardViewDeleteMsg{ViewID: id}} }
			case "n", "N", "esc", "q":
				o.mode = boardViewBrowse
				return o, nil
			}
		}
		return o, nil
	}
	if mouse, ok := msg.(tea.MouseMsg); ok {
		switch mouse.Button {
		case tea.MouseButtonWheelUp:
			o.moveCursor(-1)
		case tea.MouseButtonWheelDown:
			o.moveCursor(1)
		}
		return o, nil
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		action, ok := keybinds.LookupBoardViewAction(msg.String())
		if !ok {
			return o, nil
		}
		switch action {
		case keybinds.ActionBoardViewClose:
			return o, func() tea.Msg { return CloseOverlayMsg{} }
		case keybinds.ActionBoardViewMoveDown:
			o.moveCursor(1)
			return o, nil
		case keybinds.ActionBoardViewMoveUp:
			o.moveCursor(-1)
			return o, nil
		case keybinds.ActionBoardViewSelect:
			if len(o.views) == 0 {
				return o, nil
			}
			viewID := string(o.views[o.cursor].View.ID)
			return o, func() tea.Msg {
				return SelectionMsg{Key: BoardViewSelectKey, Value: BoardViewSelectMsg{ViewID: viewID}}
			}
		case keybinds.ActionBoardViewCreate:
			view := domain.DefaultBoardView()
			view.ID, view.Title, view.Options.SortPolicy = domain.BoardViewID(o.uniqueViewID("custom")), "Custom", domain.BoardViewSortDefault
			o.beginEdit(view, "")
			return o, textinput.Blink
		case keybinds.ActionBoardViewDuplicate:
			if len(o.views) > 0 {
				view := o.views[o.cursor].View
				scope := o.scopeForView(view.ID)
				view.ID = domain.BoardViewID(o.uniqueViewID(string(view.ID) + "-copy"))
				view.Title += " Copy"
				view.Options.SortPolicy = domain.BoardViewSortDefault
				if o.global {
					o.globalScopes[view.ID] = scope
				}
				o.beginEdit(view, "")
				return o, textinput.Blink
			}
		case keybinds.ActionBoardViewEdit:
			if len(o.views) > 0 && !o.views[o.cursor].BuiltIn {
				o.beginEdit(o.views[o.cursor].View, o.views[o.cursor].View.ID)
				return o, textinput.Blink
			}
			o.errorText = "Built-in views cannot be edited; duplicate it first."
		case keybinds.ActionBoardViewDelete:
			if len(o.views) > 0 && !o.views[o.cursor].BuiltIn {
				o.mode = boardViewConfirmDelete
			} else {
				o.errorText = "Built-in views cannot be deleted."
			}
		case keybinds.ActionBoardViewToggleEmpty:
			if len(o.views) > 0 && !o.views[o.cursor].BuiltIn {
				view := o.views[o.cursor].View
				view.Options.HideEmptyColumns = !view.Options.HideEmptyColumns
				return o, func() tea.Msg {
					return SelectionMsg{Key: BoardViewSaveKey, Value: BoardViewSaveMsg{View: view, Scope: o.scopeForView(view.ID)}}
				}
			}
			o.errorText = "Built-in views cannot be edited; duplicate it first."
		}
	}
	return o, nil
}

func (o *BoardViewOverlay) View() string {
	width, height := o.Size()
	if o.mode == boardViewEdit {
		return o.renderConfigurator(width, height)
	}
	if o.mode == boardViewAdvancedEdit {
		o.editor.SetWidth(max(20, width-8))
		o.editor.SetHeight(max(5, height-8))
		body := o.editor.View()
		if o.errorText != "" {
			body += "\n" + o.styles.MenuItemDisabled.Render(clampOverlayLineWidth(o.errorText, width-6))
		}
		return o.renderSingleDialog(width, height, "Advanced View JSON", body, []keybinds.Binding{{Key: "Ctrl+S", Description: "save"}, {Key: "Esc", Description: "guided editor"}})
	}
	if o.mode == boardViewConfirmDelete {
		view := o.views[o.cursor].View
		body := fmt.Sprintf("Delete custom view %q (%s)?\n\nY confirm  N/Esc cancel", view.Title, view.ID)
		return o.renderSingleDialog(width, height, "Confirm Delete", body, []keybinds.Binding{{Key: "Y", Description: "confirm"}, {Key: "N/Esc", Description: "cancel"}})
	}
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            o.styles,
		width:             width,
		height:            height,
		title:             o.Title(),
		rightSectionTitle: o.detailSectionTitle(),
		breakpoint:        64,
		gap:               3,
		minLeft:           30,
		minRight:          24,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return o.renderList(width)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return o.renderDetails(width)
		},
	})
}

func (o *BoardViewOverlay) Title() string { return "Views" }

func (o *BoardViewOverlay) Size() (int, int) {
	return o.ClampResponsive(74, 24)
}

func (o *BoardViewOverlay) StatusBindings() []keybinds.Binding {
	return keybinds.BoardViewHintBindings()
}

func (o *BoardViewOverlay) moveCursor(delta int) {
	if len(o.views) == 0 {
		o.cursor = 0
		return
	}
	o.cursor = (o.cursor + delta + len(o.views)) % len(o.views)
}

func (o *BoardViewOverlay) renderList(width int) string {
	if len(o.views) == 0 {
		return o.styles.MenuItemDisabled.Render("No views available")
	}
	lines := make([]string, 0, len(o.views)+1)
	for i, record := range o.views {
		prefix := "  "
		if i == o.cursor {
			prefix = lipgloss.NewStyle().Foreground(styles.Blue).Render("▸ ")
		}
		selected := " "
		if string(record.View.ID) == o.selectedViewID {
			selected = "*"
		}
		label := fmt.Sprintf("%s%s %s", prefix, selected, record.View.Title)
		if i == o.cursor {
			label = o.styles.MenuItemActive.Render(label)
		}
		meta := fmt.Sprintf(" %s  %s", record.View.ID, viewLayoutLabel(record.View.Layout))
		if record.BuiltIn {
			meta += "  built-in"
		}
		lines = append(lines, clampOverlayLineWidth(label+o.styles.MenuItemDisabled.Render(meta), max(12, width-1)))
	}
	return strings.Join(lines, "\n")
}

func (o *BoardViewOverlay) renderDetails(width int) string {
	if len(o.views) == 0 || o.cursor < 0 || o.cursor >= len(o.views) {
		return ""
	}
	view := o.views[o.cursor].View
	lines := []string{
		o.styles.Title.Render(string(view.ID)),
		o.styles.MenuItemDisabled.Render(viewLayoutDescription(view.Layout)),
		fmt.Sprintf("hide empty columns: %t", view.Options.HideEmptyColumns),
		fmt.Sprintf("show children: %t", view.Options.ShowChildren),
		"",
	}
	if o.views[o.cursor].BuiltIn {
		lines = append(lines, "Built-in · immutable", "Press d to duplicate before editing.", "")
	}
	for _, column := range view.Columns {
		line := fmt.Sprintf("%s: %s", column.Title, boardViewPredicateSummary(column.Predicates))
		lines = append(lines, clampOverlayLineWidth(line, max(12, width-1)))
	}
	if o.errorText != "" {
		lines = append(lines, "", clampOverlayLineWidth(o.errorText, max(12, width-1)))
	}
	return strings.Join(lines, "\n")
}

func (o *BoardViewOverlay) uniqueViewID(base string) string {
	base = domain.NormalizeBoardViewID(base)
	if base == "" {
		base = "custom"
	}
	used := make(map[string]struct{}, len(o.views))
	for _, record := range o.views {
		used[string(record.View.ID)] = struct{}{}
	}
	if _, exists := used[base]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func (o *BoardViewOverlay) beginEdit(view domain.BoardView, lockedID domain.BoardViewID) {
	view = view.Normalized()
	title := textinput.New()
	title.SetValue(view.Title)
	title.Focus()
	projects := textinput.New()
	scope := o.scopeForView(view.ID)
	projects.SetValue(scopeProjectValue(scope))
	o.configurator = viewConfigurator{view: view, title: title, filter: filterPresetIndex(view.Filters), grouping: groupingPresetIndex(view), sortPreset: sortPresetIndex(view.Sort), scope: globalScopePresetIndex(scope), projects: projects}
	o.lockedEditID = lockedID
	o.mode, o.errorText = boardViewEdit, ""
}

func (o *BoardViewOverlay) beginAdvancedEdit() {
	view := o.configuredView()
	data, _ := json.MarshalIndent(view, "", "  ")
	o.editor.SetValue(string(data))
	o.editor.CursorStart()
	o.editor.Focus()
	o.mode, o.errorText = boardViewAdvancedEdit, ""
}

var configuratorFields = []string{"Title", "Layout", "Filters", "Grouping", "Ordered sorting", "Hide empty", "Show children"}
var globalConfiguratorFields = []string{"Title", "Layout", "Project scope", "Scope projects", "Filters", "Grouping", "Ordered sorting", "Hide empty", "Show children"}
var globalScopePresets = []string{"All projects", "Selected projects", "Current project"}
var filterPresets = []string{"All issues", "Open only", "Active only", "Review ready"}
var groupingPresets = []string{"Workflow columns", "Single list"}
var sortPresets = []string{"Attention ↓, priority ↑, updated ↓", "Priority ↑, updated ↓", "Updated ↓", "Issue ID ↑"}

func (o *BoardViewOverlay) updateConfigurator(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return o, nil
	}
	switch key.String() {
	case "esc":
		o.configurator.title.Blur()
		o.mode, o.errorText = boardViewBrowse, ""
		return o, nil
	case "tab", "down":
		o.configurator.field = (o.configurator.field + 1) % len(o.configuratorFieldLabels())
	case "shift+tab", "up":
		o.configurator.field = (o.configurator.field - 1 + len(o.configuratorFieldLabels())) % len(o.configuratorFieldLabels())
	case "left":
		o.adjustConfigurator(-1)
	case "right", "enter":
		o.adjustConfigurator(1)
	case "ctrl+s":
		view := o.configuredView().Normalized()
		if err := view.Validate(); err != nil {
			o.errorText = "Validation: " + err.Error()
			return o, nil
		}
		scope := o.configuredScope()
		if o.global {
			if err := scope.Validate(); err != nil {
				o.errorText = "Validation: " + err.Error()
				return o, nil
			}
		}
		return o, func() tea.Msg {
			return SelectionMsg{Key: BoardViewSaveKey, Value: BoardViewSaveMsg{View: view, Scope: scope}}
		}
	case "ctrl+j":
		o.beginAdvancedEdit()
		return o, textarea.Blink
	default:
		if o.configurator.field == 0 || (o.global && o.configurator.field == 3) {
			var cmd tea.Cmd
			if o.configurator.field == 0 {
				o.configurator.title, cmd = o.configurator.title.Update(msg)
			} else {
				o.configurator.projects, cmd = o.configurator.projects.Update(msg)
			}
			return o, cmd
		}
	}
	o.configurator.title.Blur()
	o.configurator.projects.Blur()
	if o.configurator.field == 0 {
		o.configurator.title.Focus()
	} else if o.global && o.configurator.field == 3 {
		o.configurator.projects.Focus()
	}
	return o, nil
}

func (o *BoardViewOverlay) adjustConfigurator(delta int) {
	c := &o.configurator
	if o.global {
		switch c.field {
		case 2:
			c.scope = (c.scope + delta + len(globalScopePresets)) % len(globalScopePresets)
			return
		case 3:
			return
		default:
			if c.field > 3 {
				c.field -= 2
				defer func() { c.field += 2 }()
			}
		}
	}
	switch c.field {
	case 1:
		layouts := []domain.BoardViewLayout{domain.BoardViewLayoutHorizontalGrid, domain.BoardViewLayoutColumnBoard, domain.BoardViewLayoutTreeList}
		i := 0
		for n, layout := range layouts {
			if c.view.Layout == layout {
				i = n
			}
		}
		c.view.Layout = layouts[(i+delta+len(layouts))%len(layouts)]
	case 2:
		c.filter = (c.filter + delta + len(filterPresets)) % len(filterPresets)
		c.filterChanged = true
	case 3:
		c.grouping = (c.grouping + delta + len(groupingPresets)) % len(groupingPresets)
		c.groupingChanged = true
	case 4:
		c.sortPreset = (c.sortPreset + delta + len(sortPresets)) % len(sortPresets)
		c.sortChanged = true
	case 5:
		c.view.Options.HideEmptyColumns = !c.view.Options.HideEmptyColumns
	case 6:
		c.view.Options.ShowChildren = !c.view.Options.ShowChildren
	}
}

func (o *BoardViewOverlay) configuredView() domain.BoardView {
	c := o.configurator
	view := c.view
	view.Title = strings.TrimSpace(c.title.Value())
	if c.filterChanged {
		view.Filters = filterPreset(c.filter)
	}
	if c.groupingChanged {
		if c.grouping == 1 {
			view.Columns = []domain.BoardColumn{{
				ID:    "issues",
				Title: "Issues",
				Predicates: []domain.BoardColumnPredicate{{
					Kind: domain.BoardPredicateLifecycle,
					Lifecycle: []domain.IssueWorkflow{
						domain.IssueWorkflowBacklog,
						domain.IssueWorkflowOpen,
						domain.IssueWorkflowActive,
						domain.IssueWorkflowClosed,
					},
				}},
			}}
		} else {
			view.Columns = domain.DefaultBoardView().Columns
		}
	}
	if c.sortChanged {
		view.Sort = sortPreset(c.sortPreset)
	}
	view.Options.SortPolicy = domain.BoardViewSortDefault
	return view
}

func (o *BoardViewOverlay) renderConfigurator(width, height int) string {
	view := o.configuredView()
	labels := o.configuratorFieldLabels()
	values := []string{o.configurator.title.View(), viewLayoutLabel(view.Layout), filterPresets[o.configurator.filter], groupingPresets[o.configurator.grouping], sortPresets[o.configurator.sortPreset], fmt.Sprintf("%t", view.Options.HideEmptyColumns), fmt.Sprintf("%t", view.Options.ShowChildren)}
	if o.global {
		values = []string{o.configurator.title.View(), viewLayoutLabel(view.Layout), globalScopePresets[o.configurator.scope], o.configurator.projects.View(), filterPresets[o.configurator.filter], groupingPresets[o.configurator.grouping], sortPresets[o.configurator.sortPreset], fmt.Sprintf("%t", view.Options.HideEmptyColumns), fmt.Sprintf("%t", view.Options.ShowChildren)}
	}
	lines := []string{"Configure the View with guided fields; no JSON required.", ""}
	for i, label := range labels {
		prefix := "  "
		if i == o.configurator.field {
			prefix = "▸ "
		}
		lines = append(lines, fmt.Sprintf("%s%-16s %s", prefix, label, values[i]))
	}
	if o.errorText != "" {
		lines = append(lines, "", o.errorText)
	}
	return o.renderSingleDialog(width, height, "View Configurator", strings.Join(lines, "\n"), []keybinds.Binding{{Key: "Tab", Description: "field"}, {Key: "←/→", Description: "choice"}, {Key: "Ctrl+S", Description: "save"}, {Key: "Ctrl+J", Description: "advanced JSON"}, {Key: "Esc", Description: "cancel"}})
}

func (o *BoardViewOverlay) configuratorFieldLabels() []string {
	if o.global {
		return globalConfiguratorFields
	}
	return configuratorFields
}

func (o *BoardViewOverlay) scopeForView(id domain.BoardViewID) protocol.GlobalViewScope {
	if scope, ok := o.globalScopes[id]; ok {
		return scope
	}
	return protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeAllProjects}
}

func globalScopePresetIndex(scope protocol.GlobalViewScope) int {
	switch scope.Kind {
	case protocol.GlobalViewScopeSelectedProjects:
		return 1
	case protocol.GlobalViewScopeCurrentProject:
		return 2
	default:
		return 0
	}
}

func scopeProjectValue(scope protocol.GlobalViewScope) string {
	if scope.Kind == protocol.GlobalViewScopeCurrentProject {
		return scope.CurrentProjectID.String()
	}
	parts := make([]string, 0, len(scope.ProjectIDs))
	for _, id := range scope.ProjectIDs {
		parts = append(parts, id.String())
	}
	return strings.Join(parts, ",")
}

func (o *BoardViewOverlay) configuredScope() protocol.GlobalViewScope {
	if !o.global {
		return protocol.GlobalViewScope{}
	}
	value := strings.TrimSpace(o.configurator.projects.Value())
	switch o.configurator.scope {
	case 1:
		ids := make([]naming.ProjectID, 0)
		for _, part := range strings.Split(value, ",") {
			if id := strings.TrimSpace(part); id != "" {
				ids = append(ids, naming.ProjectID(id))
			}
		}
		return protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: ids}
	case 2:
		return protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeCurrentProject, CurrentProjectID: naming.ProjectID(value)}
	default:
		return protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeAllProjects}
	}
}

func viewLayoutLabel(layout domain.BoardViewLayout) string {
	switch layout {
	case domain.BoardViewLayoutHorizontalGrid:
		return "Grid"
	case domain.BoardViewLayoutTreeList:
		return "Tree"
	default:
		return "Board"
	}
}
func viewLayoutDescription(layout domain.BoardViewLayout) string {
	switch layout {
	case domain.BoardViewLayoutHorizontalGrid:
		return "Grid groups issues in horizontal lanes."
	case domain.BoardViewLayoutTreeList:
		return "Tree shows issue hierarchy with indentation."
	default:
		return "Board places issues into configured columns."
	}
}
func (o *BoardViewOverlay) detailSectionTitle() string {
	if len(o.views) == 0 || o.cursor >= len(o.views) {
		return "Details"
	}
	switch o.views[o.cursor].View.Layout {
	case domain.BoardViewLayoutTreeList:
		return "Hierarchy"
	case domain.BoardViewLayoutHorizontalGrid:
		return "Grid groups"
	default:
		return "Columns"
	}
}
func filterPresetIndex(filters []domain.BoardColumnPredicate) int {
	if len(filters) == 0 {
		return 0
	}
	switch filters[0].Kind {
	case domain.BoardPredicateLifecycle:
		return 1
	case domain.BoardPredicateDisplayPhase:
		return 2
	case domain.BoardPredicateReviewReady:
		return 3
	}
	return 0
}
func filterPreset(i int) []domain.BoardColumnPredicate {
	switch i {
	case 1:
		return []domain.BoardColumnPredicate{{Kind: domain.BoardPredicateLifecycle, Lifecycle: []domain.IssueWorkflow{domain.IssueWorkflowOpen}}}
	case 2:
		return []domain.BoardColumnPredicate{{Kind: domain.BoardPredicateDisplayPhase, DisplayPhases: []domain.IssueDisplayPhase{domain.IssueDisplayActive}}}
	case 3:
		return []domain.BoardColumnPredicate{{Kind: domain.BoardPredicateReviewReady}}
	}
	return nil
}
func groupingPresetIndex(view domain.BoardView) int {
	if len(view.Columns) == 1 {
		return 1
	}
	return 0
}
func sortPresetIndex(r []domain.BoardViewSortRule) int {
	if len(r) == 1 {
		if r[0].Key == domain.BoardViewSortKeyUpdated {
			return 2
		}
		if r[0].Key == domain.BoardViewSortKeyIssueID {
			return 3
		}
	}
	if len(r) > 0 && r[0].Key == domain.BoardViewSortKeyPriority {
		return 1
	}
	return 0
}
func sortPreset(i int) []domain.BoardViewSortRule {
	switch i {
	case 1:
		return []domain.BoardViewSortRule{{Key: domain.BoardViewSortKeyPriority, Direction: domain.BoardViewSortAscending}, {Key: domain.BoardViewSortKeyUpdated, Direction: domain.BoardViewSortDescending}}
	case 2:
		return []domain.BoardViewSortRule{{Key: domain.BoardViewSortKeyUpdated, Direction: domain.BoardViewSortDescending}}
	case 3:
		return []domain.BoardViewSortRule{{Key: domain.BoardViewSortKeyIssueID, Direction: domain.BoardViewSortAscending}}
	}
	return domain.DefaultBoardViewSortRules()
}

func (o *BoardViewOverlay) renderSingleDialog(width, height int, title, body string, actions []keybinds.Binding) string {
	if len(actions) > 0 {
		body += "\n" + renderDialogActions(o.styles, actions, max(12, width-6))
	}
	return renderDialogTwoPane(dialogLayoutConfig{
		styles: o.styles, width: width, height: height, title: title,
		renderLeft: func(_ dialogLayoutMode, paneWidth, _ int) string {
			lines := strings.Split(body, "\n")
			for i := range lines {
				lines[i] = clampOverlayLineWidth(lines[i], max(12, paneWidth-1))
			}
			return strings.Join(lines, "\n")
		},
	})
}

func boardViewPredicateSummary(predicates []domain.BoardColumnPredicate) string {
	if len(predicates) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(predicates))
	for _, predicate := range predicates {
		switch predicate.Kind {
		case domain.BoardPredicateLifecycle:
			parts = append(parts, "lifecycle="+joinBoardPredicateValues(predicate.Lifecycle))
		case domain.BoardPredicateDisplayPhase:
			parts = append(parts, "phase="+joinBoardPredicateValues(predicate.DisplayPhases))
		case domain.BoardPredicateClosedOutcome:
			parts = append(parts, "outcome="+joinBoardPredicateValues(predicate.ClosedOutcomes))
		default:
			parts = append(parts, string(predicate.Kind))
		}
	}
	return strings.Join(parts, " + ")
}

func joinBoardPredicateValues[T ~string](values []T) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return strings.Join(out, ",")
}
