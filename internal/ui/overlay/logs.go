package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// EventLogHotkey is the board hotkey scaffold for opening the event log overlay.
const EventLogHotkey = "L"

// EventLogHotkeyHint returns the user-facing hint for the board hotkey scaffold.
func EventLogHotkeyHint() string {
	return EventLogHotkey + ": logs"
}

// EventLogOverlay renders daemon/client runtime events with newest entries first.
type EventLogOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	logFilePath string
	events      []protocol.EventEnvelope
	scroll      int
	maxScroll   int
	viewHeight  int
	styles      *Styles
}

// NewEventLogOverlay creates an event log overlay from chronologically ordered events.
func NewEventLogOverlay(events []protocol.EventEnvelope) *EventLogOverlay {
	return NewEventLogOverlayWithLogFile(events, "")
}

// NewEventLogOverlayWithLogFile creates an event log overlay with optional az.log path.
func NewEventLogOverlayWithLogFile(events []protocol.EventEnvelope, logFilePath string) *EventLogOverlay {
	overlay := &EventLogOverlay{
		logFilePath: logFilePath,
		scroll:      0,
		viewHeight:  18,
		styles:      New(),
	}
	overlay.SetEvents(events)
	return overlay
}

// SetEvents replaces the event list and normalizes it to newest-first order.
func (o *EventLogOverlay) SetEvents(events []protocol.EventEnvelope) {
	o.events = reverseEvents(events)
	o.clampScroll()
}

// AddEvent prepends a single event so the newest event stays at the top.
func (o *EventLogOverlay) AddEvent(evt protocol.EventEnvelope) {
	o.events = append([]protocol.EventEnvelope{evt}, o.events...)
	o.clampScroll()
}

// Init initializes the overlay.
func (o *EventLogOverlay) Init() tea.Cmd {
	return nil
}

// Update handles key navigation and close/back behavior.
func (o *EventLogOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "backspace":
			return o, func() tea.Msg { return CloseOverlayMsg{} }
		case "j", "down":
			if o.scroll < o.maxScroll {
				o.scroll++
			}
			return o, nil
		case "k", "up":
			if o.scroll > 0 {
				o.scroll--
			}
			return o, nil
		case "g":
			o.scroll = 0
			return o, nil
		case "G":
			o.scroll = o.maxScroll
			return o, nil
		case "s":
			if strings.TrimSpace(o.logFilePath) != "" {
				return o, func() tea.Msg {
					return SelectionMsg{
						Key:   "event-log-stream",
						Value: o.logFilePath,
					}
				}
			}
			return o, nil
		case "e":
			if strings.TrimSpace(o.logFilePath) != "" {
				return o, func() tea.Msg {
					return SelectionMsg{
						Key:   "event-log-editor",
						Value: o.logFilePath,
					}
				}
			}
			return o, nil
		}
	case tea.WindowSizeMsg:
		o.ApplyWindowSize(msg)
	}

	return o, nil
}

// View renders the event log overlay content.
func (o *EventLogOverlay) View() string {
	width, height := o.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            o.styles,
		width:             width,
		height:            height,
		title:             "Event Log",
		rightSectionTitle: "Actions",
		breakpoint:        88,
		gap:               3,
		minLeft:           52,
		minRight:          22,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return o.renderScrollableContent(height)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(o.styles, o.actionBindings(o.maxScroll > 0), width)
		},
	})
}

// Title returns the overlay title.
func (o *EventLogOverlay) Title() string {
	return "Event Log"
}

// UsesInternalTitle indicates this overlay renders its own internal title line.
func (o *EventLogOverlay) UsesInternalTitle() bool {
	return true
}

// Size returns the overlay dimensions.
func (o *EventLogOverlay) Size() (width, height int) {
	const (
		minViewHeight = 5
		maxViewHeight = 16
		chromeHeight  = 4
	)

	// Keep footer pinned to bottom while avoiding oversized empty interiors.
	neededViewHeight := len(o.renderContentLines()) + 1 // + footer row
	o.viewHeight = min(maxViewHeight, max(minViewHeight, neededViewHeight))
	return o.Clamp(92, o.viewHeight+chromeHeight)
}

func (o *EventLogOverlay) renderContentLines() []string {
	lines := make([]string, 0, len(o.events)*4+6)
	if strings.TrimSpace(o.logFilePath) != "" {
		lines = append(lines, o.styles.MenuItemDisabled.Render("Log file: "+o.logFilePath))
	}

	if len(o.events) == 0 {
		lines = append(lines, o.styles.MenuItemDisabled.Render("No runtime events yet."))
		return lines
	}

	for _, evt := range o.events {
		lines = append(lines, o.renderEvent(evt)...)
	}

	return lines
}

func (o *EventLogOverlay) footerLine(scrollable bool) string {
	return o.styles.Footer.Render(keybinds.RenderPlain(o.actionBindings(scrollable), " • "))
}

func (o *EventLogOverlay) renderEvent(evt protocol.EventEnvelope) []string {
	lines := make([]string, 0, 3)

	headerParts := []string{
		o.renderTimestamp(evt.EmittedAt),
	}
	if evt.Revision > 0 {
		headerParts = append(headerParts, fmt.Sprintf("#%d", evt.Revision))
	}
	if evt.Event != "" {
		headerParts = append(headerParts, evt.Event)
	}
	if evt.Kind != "" {
		headerParts = append(headerParts, fmt.Sprintf("[%s]", evt.Kind))
	}
	lines = append(lines, o.styles.MenuItemActive.Render(strings.Join(headerParts, "  ")))

	metaParts := make([]string, 0, 3)
	if evt.ProjectID != "" {
		metaParts = append(metaParts, "project="+evt.ProjectID)
	}
	if evt.Meta.SessionID != "" {
		metaParts = append(metaParts, "session="+evt.Meta.SessionID)
	}
	if evt.Meta.CorrelationID != "" {
		metaParts = append(metaParts, "correlation="+evt.Meta.CorrelationID)
	}
	if len(metaParts) > 0 {
		lines = append(lines, o.styles.MenuItem.Render("  "+strings.Join(metaParts, "  ")))
	}

	for _, bodyLine := range o.renderBodyLines(evt.Body) {
		lines = append(lines, o.styles.MenuItem.Render("  "+bodyLine))
	}
	return lines
}

func (o *EventLogOverlay) renderTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return "unknown-time"
	}
	return ts.UTC().Format("2006-01-02 15:04:05")
}

func (o *EventLogOverlay) renderBodyLines(body []byte) []string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil
	}

	const maxLineLen = 96
	rawLines := strings.Split(text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		lines = append(lines, truncateRunes(line, maxLineLen))
	}
	return lines
}

func (o *EventLogOverlay) clampScroll() {
	if o.scroll < 0 {
		o.scroll = 0
	}
	if o.scroll > o.maxScroll {
		o.scroll = o.maxScroll
	}
}

func reverseEvents(events []protocol.EventEnvelope) []protocol.EventEnvelope {
	if len(events) == 0 {
		return nil
	}

	out := make([]protocol.EventEnvelope, len(events))
	for i := range events {
		out[len(events)-1-i] = events[i]
	}
	return out
}

func (o *EventLogOverlay) renderScrollableContent(contentHeight int) string {
	contentLines := o.renderContentLines()
	contentHeight = max(1, contentHeight-1)
	o.maxScroll = max(0, len(contentLines)-contentHeight)
	if o.scroll > o.maxScroll {
		o.scroll = o.maxScroll
	}
	if o.scroll < 0 {
		o.scroll = 0
	}

	start := o.scroll
	end := min(o.scroll+contentHeight, len(contentLines))
	if start > len(contentLines) {
		start = len(contentLines)
	}
	if end < start {
		end = start
	}

	visibleContent := append([]string{}, contentLines[start:end]...)
	for len(visibleContent) < contentHeight {
		visibleContent = append(visibleContent, "")
	}
	visibleContent = append(visibleContent, o.footerLine(o.maxScroll > 0))
	return strings.Join(visibleContent, "\n")
}

func (o *EventLogOverlay) actionBindings(scrollable bool) []keybinds.Binding {
	bindings := make([]keybinds.Binding, 0, 5)
	if scrollable {
		bindings = append(bindings,
			keybinds.Binding{Key: "j/k", Description: "scroll"},
			keybinds.Binding{Key: "g/G", Description: "top/bottom"},
		)
	}
	bindings = append(bindings,
		keybinds.Binding{Key: "s", Description: "stream (Ctrl+C then q)"},
		keybinds.Binding{Key: "e", Description: "edit"},
	)
	bindings = append(bindings, keybinds.Binding{Key: "Esc/q/backspace", Description: "close"})
	return bindings
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}

	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
