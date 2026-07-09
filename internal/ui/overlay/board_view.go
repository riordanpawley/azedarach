package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const BoardViewSelectKey = "board_view_select"

type BoardViewSelectMsg struct {
	ViewID string
}

type BoardViewOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	views          []domain.BoardViewRecord
	selectedViewID string
	cursor         int
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
	return &BoardViewOverlay{
		views:          copied,
		selectedViewID: selectedViewID,
		cursor:         cursor,
		styles:         New(),
	}
}

func (o *BoardViewOverlay) Init() tea.Cmd { return nil }

func (o *BoardViewOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return o, func() tea.Msg { return CloseOverlayMsg{} }
		case "j", "down":
			o.moveCursor(1)
			return o, nil
		case "k", "up":
			o.moveCursor(-1)
			return o, nil
		case "enter":
			if len(o.views) == 0 {
				return o, nil
			}
			viewID := string(o.views[o.cursor].View.ID)
			return o, func() tea.Msg {
				return SelectionMsg{Key: BoardViewSelectKey, Value: BoardViewSelectMsg{ViewID: viewID}}
			}
		}
	case tea.WindowSizeMsg:
		o.ApplyWindowSize(msg)
	}
	return o, nil
}

func (o *BoardViewOverlay) View() string {
	width, height := o.Size()
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
	return []keybinds.Binding{
		{Key: "j/k", Description: "view"},
		{Key: "Enter", Description: "select"},
		{Key: "Esc", Description: "close"},
	}
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
		"",
	}
	for _, column := range view.Columns {
		line := fmt.Sprintf("%s: %s", column.Title, boardViewPredicateSummary(column.Predicates))
		lines = append(lines, clampOverlayLineWidth(line, max(12, width-1)))
	}
	return strings.Join(lines, "\n")
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
