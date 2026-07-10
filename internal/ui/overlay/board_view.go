package overlay

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
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

type BoardViewSaveMsg struct{ View domain.BoardView }
type BoardViewDeleteMsg struct{ ViewID string }

type boardViewOverlayMode int

const (
	boardViewBrowse boardViewOverlayMode = iota
	boardViewEdit
	boardViewConfirmDelete
)

type BoardViewOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	views          []domain.BoardViewRecord
	selectedViewID string
	cursor         int
	mode           boardViewOverlayMode
	editor         textarea.Model
	errorText      string
	lockedEditID   domain.BoardViewID
	styles         *Styles
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
	editor.Placeholder = "Board view JSON"
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
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "esc":
				o.mode, o.errorText = boardViewBrowse, ""
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
				return o, func() tea.Msg { return SelectionMsg{Key: BoardViewSaveKey, Value: BoardViewSaveMsg{View: view}} }
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
			o.beginEdit(domain.BoardView{ID: domain.BoardViewID(o.uniqueViewID("custom")), Title: "Custom", Columns: domain.DefaultBoardView().Columns}, "")
			return o, textarea.Blink
		case keybinds.ActionBoardViewDuplicate:
			if len(o.views) > 0 {
				view := o.views[o.cursor].View
				view.ID = domain.BoardViewID(o.uniqueViewID(string(view.ID) + "-copy"))
				view.Title += " Copy"
				o.beginEdit(view, "")
				return o, textarea.Blink
			}
		case keybinds.ActionBoardViewEdit:
			if len(o.views) > 0 && !o.views[o.cursor].BuiltIn {
				o.beginEdit(o.views[o.cursor].View, o.views[o.cursor].View.ID)
				return o, textarea.Blink
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
				return o, func() tea.Msg { return SelectionMsg{Key: BoardViewSaveKey, Value: BoardViewSaveMsg{View: view}} }
			}
			o.errorText = "Built-in views cannot be edited; duplicate it first."
		}
	}
	return o, nil
}

func (o *BoardViewOverlay) View() string {
	width, height := o.Size()
	if o.mode == boardViewEdit {
		o.editor.SetWidth(max(20, width-8))
		o.editor.SetHeight(max(5, height-8))
		body := o.editor.View()
		if o.errorText != "" {
			body += "\n" + o.styles.MenuItemDisabled.Render(clampOverlayLineWidth(o.errorText, width-6))
		}
		return o.renderSingleDialog(width, height, "Edit Board View", body, []keybinds.Binding{{Key: "Ctrl+S", Description: "save"}, {Key: "Esc", Description: "cancel"}})
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
		rightSectionTitle: "Columns",
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

func (o *BoardViewOverlay) Title() string { return "Board View" }

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
		return o.styles.MenuItemDisabled.Render("No board views available")
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
		meta := fmt.Sprintf(" %s  %d cols", record.View.ID, len(record.View.Columns))
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
		o.styles.MenuItemDisabled.Render("Columns are query predicates over issue facts."),
		fmt.Sprintf("hide empty columns: %t", view.Options.HideEmptyColumns),
		"",
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
	data, _ := json.MarshalIndent(view, "", "  ")
	o.editor.SetValue(string(data))
	o.editor.CursorStart()
	o.editor.Focus()
	o.lockedEditID = lockedID
	o.mode, o.errorText = boardViewEdit, ""
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
