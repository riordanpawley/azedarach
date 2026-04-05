package overlay

import (
	"bytes"
	"encoding/json"
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
	tuiLogFilePath     string
	daemonLogFilePath  string
	events             []protocol.EventEnvelope
	activeFilter       eventLogFilter
	cachedContentLines []string
	contentDirty       bool
	cachedPrettyStart  int
	cachedPrettyEnd    int
	scroll             int
	maxScroll          int
	viewHeight         int
	styles             *Styles
}

const (
	eventLogMaxRetainedEvents   = 2000
	eventLogApproxLinesPerEvent = 4
	eventLogPrettyWindowPadding = 2
)

type eventLogFilter int

const (
	eventLogFilterAll eventLogFilter = iota
	eventLogFilterOperation
	eventLogFilterGit
	eventLogFilterHook
	eventLogFilterCount
)

// NewEventLogOverlay creates an event log overlay from chronologically ordered events.
func NewEventLogOverlay(events []protocol.EventEnvelope) *EventLogOverlay {
	return NewEventLogOverlayWithLogFiles(events, "", "")
}

// NewEventLogOverlayWithLogFile creates an event log overlay with optional az.log path.
func NewEventLogOverlayWithLogFile(events []protocol.EventEnvelope, logFilePath string) *EventLogOverlay {
	return NewEventLogOverlayWithLogFiles(events, logFilePath, "")
}

// NewEventLogOverlayWithLogFiles creates an event log overlay with optional TUI and daemon log paths.
func NewEventLogOverlayWithLogFiles(events []protocol.EventEnvelope, tuiLogFilePath, daemonLogFilePath string) *EventLogOverlay {
	overlay := &EventLogOverlay{
		tuiLogFilePath:    strings.TrimSpace(tuiLogFilePath),
		daemonLogFilePath: strings.TrimSpace(daemonLogFilePath),
		contentDirty:      true,
		cachedPrettyStart: -1,
		cachedPrettyEnd:   -1,
		activeFilter:      eventLogFilterAll,
		scroll:            0,
		viewHeight:        18,
		styles:            New(),
	}
	overlay.SetEvents(events)
	return overlay
}

// SetEvents replaces the event list in chronological order.
func (o *EventLogOverlay) SetEvents(events []protocol.EventEnvelope) {
	o.events = append([]protocol.EventEnvelope(nil), events...)
	o.trimEventsToCap()
	o.contentDirty = true
	o.cachedPrettyStart = -1
	o.cachedPrettyEnd = -1
	o.clampScroll()
}

// AddEvent appends a single event while preserving chronological order.
func (o *EventLogOverlay) AddEvent(evt protocol.EventEnvelope) {
	o.events = append(o.events, evt)
	o.trimEventsToCap()
	o.contentDirty = true
	o.cachedPrettyStart = -1
	o.cachedPrettyEnd = -1
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
		switch msg.Type {
		case tea.KeyCtrlD:
			o.scroll = min(o.maxScroll, o.scroll+o.halfPageStep())
			return o, nil
		case tea.KeyCtrlU:
			o.scroll = max(0, o.scroll-o.halfPageStep())
			return o, nil
		}
		switch msg.String() {
		case "esc", "q", "backspace":
			return o, func() tea.Msg { return CloseOverlayMsg{} }
		case "tab":
			o.cycleFilter(1)
			return o, nil
		case "shift+tab":
			o.cycleFilter(-1)
			return o, nil
		case "f":
			o.cycleFilter(1)
			return o, nil
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
			paths := o.streamLogPaths()
			if len(paths) > 0 {
				return o, func() tea.Msg {
					return SelectionMsg{
						Key:   "event-log-stream",
						Value: paths,
					}
				}
			}
			return o, nil
		case "t":
			if o.tuiLogFilePath != "" {
				return o, func() tea.Msg {
					return SelectionMsg{
						Key:   "event-log-editor",
						Value: o.tuiLogFilePath,
					}
				}
			}
			return o, nil
		case "d":
			if o.daemonLogFilePath != "" {
				return o, func() tea.Msg {
					return SelectionMsg{
						Key:   "event-log-editor",
						Value: o.daemonLogFilePath,
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
	neededViewHeight := len(o.contentLines()) + 1 // + footer row
	o.viewHeight = min(maxViewHeight, max(minViewHeight, neededViewHeight))
	return o.ClampResponsive(92, o.viewHeight+chromeHeight)
}

func (o *EventLogOverlay) renderContentLines() []string {
	lines := []string{o.filterHeaderLine()}
	return append(lines, o.renderRuntimeEventLines()...)
}

func (o *EventLogOverlay) renderRuntimeEventLines() []string {
	filtered := o.filteredEvents()
	lines := make([]string, 0, len(filtered)*4+4)
	if o.tuiLogFilePath != "" {
		lines = append(lines, o.styles.MenuItemDisabled.Render("TUI log: "+o.tuiLogFilePath))
	}
	if o.daemonLogFilePath != "" {
		lines = append(lines, o.styles.MenuItemDisabled.Render("Daemon log: "+o.daemonLogFilePath))
	}

	if len(filtered) == 0 {
		lines = append(lines, o.styles.MenuItemDisabled.Render("No runtime events yet."))
		return lines
	}

	prettyStart, prettyEnd := o.prettyWindowForCurrentScroll(len(filtered))
	for displayIndex := 0; displayIndex < len(filtered); displayIndex++ {
		eventIndex := len(filtered) - 1 - displayIndex
		prettyBody := displayIndex >= prettyStart && displayIndex <= prettyEnd
		lines = append(lines, o.renderEvent(filtered[eventIndex], prettyBody)...)
	}
	return lines
}

func (o *EventLogOverlay) contentLines() []string {
	prettyStart, prettyEnd := o.prettyWindowForCurrentScroll(len(o.filteredEvents()))
	if !o.contentDirty && o.cachedPrettyStart == prettyStart && o.cachedPrettyEnd == prettyEnd {
		return o.cachedContentLines
	}
	o.cachedContentLines = o.renderContentLines()
	o.contentDirty = false
	o.cachedPrettyStart = prettyStart
	o.cachedPrettyEnd = prettyEnd
	return o.cachedContentLines
}

func (o *EventLogOverlay) footerLine(scrollable bool) string {
	return o.styles.Footer.Render(keybinds.RenderPlain(o.actionBindings(scrollable), " • "))
}

func (o *EventLogOverlay) renderEvent(evt protocol.EventEnvelope, prettyBody bool) []string {
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
		metaParts = append(metaParts, "project="+evt.ProjectID.String())
	}
	if evt.Meta.SessionID != "" {
		metaParts = append(metaParts, "session="+evt.Meta.SessionID.String())
	}
	if evt.Meta.CorrelationID != "" {
		metaParts = append(metaParts, "correlation="+evt.Meta.CorrelationID.String())
	}
	if len(metaParts) > 0 {
		lines = append(lines, o.styles.MenuItem.Render("  "+strings.Join(metaParts, "  ")))
	}

	for _, bodyLine := range o.renderBodyLinesWithPretty(evt.Body, prettyBody) {
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
	return o.renderBodyLinesWithPretty(body, true)
}

func (o *EventLogOverlay) renderBodyLinesWithPretty(body []byte, pretty bool) []string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil
	}

	if pretty {
		if formatted, ok := formatJSONLogBody([]byte(text)); ok {
			text = formatted
		}
	}

	return strings.Split(text, "\n")
}

func (o *EventLogOverlay) trimEventsToCap() {
	if len(o.events) <= eventLogMaxRetainedEvents {
		return
	}
	o.events = append([]protocol.EventEnvelope(nil), o.events[len(o.events)-eventLogMaxRetainedEvents:]...)
}

func (o *EventLogOverlay) prettyWindowForCurrentScroll(itemCount int) (start, end int) {
	if itemCount == 0 {
		return -1, -1
	}

	approxContentHeight := max(1, o.viewHeight-1)
	windowStart := max(0, o.scroll/eventLogApproxLinesPerEvent-eventLogPrettyWindowPadding)
	windowSpan := max(6, approxContentHeight/eventLogApproxLinesPerEvent+eventLogPrettyWindowPadding*2)
	windowEnd := min(itemCount-1, windowStart+windowSpan-1)
	return windowStart, windowEnd
}

func (o *EventLogOverlay) clampScroll() {
	if o.scroll < 0 {
		o.scroll = 0
	}
	if o.scroll > o.maxScroll {
		o.scroll = o.maxScroll
	}
}

func (o *EventLogOverlay) renderScrollableContent(contentHeight int) string {
	contentLines := o.contentLines()
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

func (o *EventLogOverlay) halfPageStep() int {
	step := o.viewHeight / 2
	if step < 1 {
		return 1
	}
	return step
}

func (o *EventLogOverlay) actionBindings(scrollable bool) []keybinds.Binding {
	bindings := make([]keybinds.Binding, 0, 8)
	bindings = append(bindings, keybinds.Binding{Key: "Tab/Shift+Tab/f", Description: "cycle filter"})
	if scrollable {
		bindings = append(bindings,
			keybinds.Binding{Key: "j/k", Description: "scroll"},
			keybinds.Binding{Key: "ctrl+u/d", Description: "half-page"},
			keybinds.Binding{Key: "g/G", Description: "top/bottom"},
		)
	}
	bindings = append(bindings,
		keybinds.Binding{Key: "s", Description: "stream (Ctrl+C to stop)"},
	)
	if o.tuiLogFilePath != "" {
		bindings = append(bindings, keybinds.Binding{Key: "t", Description: "open TUI log"})
	}
	if o.daemonLogFilePath != "" {
		bindings = append(bindings, keybinds.Binding{Key: "d", Description: "open daemon log"})
	}
	bindings = append(bindings, keybinds.Binding{Key: "Esc/q/backspace", Description: "close"})
	return bindings
}

func (o *EventLogOverlay) streamLogPaths() []string {
	paths := make([]string, 0, 2)
	if o.daemonLogFilePath != "" {
		paths = append(paths, o.daemonLogFilePath)
	}
	if o.tuiLogFilePath != "" {
		paths = append(paths, o.tuiLogFilePath)
	}
	return paths
}

func (o *EventLogOverlay) filterHeaderLine() string {
	labels := []string{"All", "Operation", "Git", "Hooks"}
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		if i == int(o.activeFilter) {
			parts = append(parts, "["+label+"]")
		} else {
			parts = append(parts, label)
		}
	}
	return o.styles.MenuItemDisabled.Render("Filter: " + strings.Join(parts, "  |  "))
}

func (o *EventLogOverlay) cycleFilter(delta int) {
	next := (int(o.activeFilter) + delta) % int(eventLogFilterCount)
	if next < 0 {
		next += int(eventLogFilterCount)
	}
	o.activeFilter = eventLogFilter(next)
	o.scroll = 0
	o.contentDirty = true
	o.cachedPrettyStart = -1
	o.cachedPrettyEnd = -1
}

func (o *EventLogOverlay) filteredEvents() []protocol.EventEnvelope {
	if o.activeFilter == eventLogFilterAll {
		return o.events
	}
	filtered := make([]protocol.EventEnvelope, 0, len(o.events))
	for _, evt := range o.events {
		if o.matchesFilter(evt) {
			filtered = append(filtered, evt)
		}
	}
	return filtered
}

func (o *EventLogOverlay) matchesFilter(evt protocol.EventEnvelope) bool {
	event := strings.ToLower(strings.TrimSpace(evt.Event))
	switch o.activeFilter {
	case eventLogFilterOperation:
		return strings.HasPrefix(event, "operation.")
	case eventLogFilterGit:
		return strings.HasPrefix(event, "git.") || strings.HasPrefix(event, "worktree.")
	case eventLogFilterHook:
		return event == protocol.EventHookLogAppended
	default:
		return true
	}
}

func formatJSONLogBody(raw []byte) (string, bool) {
	if !json.Valid(raw) {
		return "", false
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return "", false
	}
	return buf.String(), true
}
