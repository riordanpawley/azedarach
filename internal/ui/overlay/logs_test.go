package overlay

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestEventLogOverlay_Metadata(t *testing.T) {
	overlay := NewEventLogOverlay(nil)

	if overlay == nil {
		t.Fatal("NewEventLogOverlay returned nil")
	}

	if overlay.Title() != "Event Log" {
		t.Fatalf("Title() = %q, want %q", overlay.Title(), "Event Log")
	}

	if EventLogHotkey != "L" {
		t.Fatalf("EventLogHotkey = %q, want %q", EventLogHotkey, "L")
	}

	if got := EventLogHotkeyHint(); got != "L: logs" {
		t.Fatalf("EventLogHotkeyHint() = %q, want %q", got, "L: logs")
	}

	if width, height := overlay.Size(); width <= 0 || height <= 0 {
		t.Fatalf("Size() returned non-positive dimensions: %d x %d", width, height)
	}
}

func TestEventLogOverlay_View_RendersNewestFirst(t *testing.T) {
	older := protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       "proj-a",
		Revision:        41,
		Event:           "daemon.event.older",
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Date(2026, time.March, 25, 17, 30, 0, 0, time.UTC),
		Meta: protocol.Metadata{
			SessionID:     "sess-1",
			CorrelationID: "corr-old",
		},
		Body: []byte("older payload"),
	}
	newer := protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       "proj-a",
		Revision:        42,
		Event:           "daemon.event.newer",
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Date(2026, time.March, 25, 17, 31, 0, 0, time.UTC),
		Meta: protocol.Metadata{
			SessionID:     "sess-1",
			CorrelationID: "corr-new",
		},
		Body: []byte("newer payload"),
	}

	overlay := NewEventLogOverlay([]protocol.EventEnvelope{older, newer})
	overlay.viewHeight = 20

	if got := overlay.events[len(overlay.events)-1].Revision; got != newer.Revision {
		t.Fatalf("events[last].Revision = %d, want newest revision %d", got, newer.Revision)
	}

	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 160, Height: 34})
	overlay = model.(*EventLogOverlay)

	view := overlay.View()
	if !strings.Contains(view, "Event Log") {
		t.Fatalf("View() missing title: %s", view)
	}
	if !strings.Contains(view, "stream") {
		t.Fatalf("View() missing stream hint: %s", view)
	}
	if strings.Contains(view, "Newest runtime events first") {
		t.Fatalf("View() still contains legacy header: %s", view)
	}
	if !strings.Contains(view, "TIME") || !strings.Contains(view, "SUMMARY") {
		t.Fatalf("View() missing compact table header: %s", view)
	}

	newerIdx := strings.Index(view, "Daemon event newer")
	olderIdx := strings.Index(view, "Daemon event older")
	if newerIdx < 0 || olderIdx < 0 {
		t.Fatalf("View() missing compact event summaries: %s", view)
	}
	if newerIdx > olderIdx {
		t.Fatalf("newer event rendered after older event: newer=%d older=%d view=%s", newerIdx, olderIdx, view)
	}

	if strings.Contains(view, "correlation=corr-new") {
		t.Fatalf("compact View() should hide detail metadata until expanded: %s", view)
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	overlay = model.(*EventLogOverlay)
	expanded := overlay.View()
	if !strings.Contains(expanded, newer.Event) || !strings.Contains(expanded, "correlation=corr-new") {
		t.Fatalf("expanded View() missing newer details: %s", expanded)
	}
}

func TestEventLogOverlay_View_EmptyState(t *testing.T) {
	overlay := NewEventLogOverlay(nil)
	overlay.viewHeight = 12

	view := overlay.View()
	if !strings.Contains(view, "No runtime events yet.") {
		t.Fatalf("View() missing empty state: %s", view)
	}
	if !strings.Contains(view, "Filter:") || !strings.Contains(view, "All") || !strings.Contains(view, "Hooks") {
		t.Fatalf("View() missing filter header: %s", view)
	}
	if !strings.Contains(view, "Esc/q/backspace") || !strings.Contains(view, "close") {
		t.Fatalf("View() missing close hint: %s", view)
	}
}

func TestEventLogOverlay_Update_NavigationAndClose(t *testing.T) {
	overlay := NewEventLogOverlay([]protocol.EventEnvelope{
		testEvent(1, "daemon.event.one"),
		testEvent(2, "daemon.event.two"),
		testEvent(3, "daemon.event.three"),
		testEvent(4, "daemon.event.four"),
		testEvent(5, "daemon.event.five"),
		testEvent(6, "daemon.event.six"),
		testEvent(7, "daemon.event.seven"),
		testEvent(8, "daemon.event.eight"),
		testEvent(9, "daemon.event.nine"),
		testEvent(10, "daemon.event.ten"),
		testEvent(11, "daemon.event.eleven"),
		testEvent(12, "daemon.event.twelve"),
	})
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 78, Height: 14})
	overlay = model.(*EventLogOverlay)

	// Prime maxScroll from the rendered content.
	_ = overlay.View()
	if overlay.maxScroll == 0 {
		t.Fatal("expected scrollable content")
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	overlay = model.(*EventLogOverlay)
	if overlay.scroll != 1 {
		t.Fatalf("scroll after j = %d, want 1", overlay.scroll)
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	overlay = model.(*EventLogOverlay)
	if overlay.scroll != 0 {
		t.Fatalf("scroll after k = %d, want 0", overlay.scroll)
	}

	overlay.scroll = overlay.maxScroll
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	overlay = model.(*EventLogOverlay)
	if overlay.scroll != 0 {
		t.Fatalf("scroll after g = %d, want 0", overlay.scroll)
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	overlay = model.(*EventLogOverlay)
	if overlay.scroll != overlay.maxScroll {
		t.Fatalf("scroll after G = %d, want %d", overlay.scroll, overlay.maxScroll)
	}

	overlay.scroll = 0
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	overlay = model.(*EventLogOverlay)
	if overlay.scroll <= 0 {
		t.Fatalf("scroll after ctrl+d = %d, want > 0", overlay.scroll)
	}

	afterHalfDown := overlay.scroll
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	overlay = model.(*EventLogOverlay)
	if overlay.scroll >= afterHalfDown {
		t.Fatalf("scroll after ctrl+u = %d, want < %d", overlay.scroll, afterHalfDown)
	}

	for _, keyMsg := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyBackspace},
	} {
		_, cmd := overlay.Update(keyMsg)
		if cmd == nil {
			t.Fatalf("Update(%q) returned nil command", keyMsg.String())
		}
		if _, ok := cmd().(CloseOverlayMsg); !ok {
			t.Fatalf("Update(%q) did not emit CloseOverlayMsg", keyMsg.String())
		}
	}
}

func TestEventLogOverlay_Update_LogActionKeys(t *testing.T) {
	overlay := NewEventLogOverlayWithLogFiles(nil, "/tmp/az-tui.log", "/tmp/azd.log")

	_, streamCmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if streamCmd == nil {
		t.Fatal("expected stream command")
	}
	streamMsg, ok := streamCmd().(SelectionMsg)
	if !ok {
		t.Fatalf("stream msg type = %T, want SelectionMsg", streamCmd())
	}
	if streamMsg.Key != "event-log-stream" {
		t.Fatalf("stream key = %q, want %q", streamMsg.Key, "event-log-stream")
	}
	paths, ok := streamMsg.Value.([]string)
	if !ok {
		t.Fatalf("stream value type = %T, want []string", streamMsg.Value)
	}
	if len(paths) != 2 || paths[0] != "/tmp/azd.log" || paths[1] != "/tmp/az-tui.log" {
		t.Fatalf("stream paths = %#v, want daemon+tui paths", paths)
	}

	_, tuiCmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if tuiCmd == nil {
		t.Fatal("expected TUI log editor command")
	}
	tuiMsg, ok := tuiCmd().(SelectionMsg)
	if !ok {
		t.Fatalf("tui msg type = %T, want SelectionMsg", tuiCmd())
	}
	if tuiMsg.Key != "event-log-editor" || tuiMsg.Value != "/tmp/az-tui.log" {
		t.Fatalf("tui editor msg = %#v, want event-log-editor /tmp/az-tui.log", tuiMsg)
	}

	_, daemonCmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if daemonCmd == nil {
		t.Fatal("expected daemon log editor command")
	}
	daemonMsg, ok := daemonCmd().(SelectionMsg)
	if !ok {
		t.Fatalf("daemon msg type = %T, want SelectionMsg", daemonCmd())
	}
	if daemonMsg.Key != "event-log-editor" || daemonMsg.Value != "/tmp/azd.log" {
		t.Fatalf("daemon editor msg = %#v, want event-log-editor /tmp/azd.log", daemonMsg)
	}
}

func TestEventLogOverlay_Update_ExpandUsesFirstVisibleEventWithLogPaths(t *testing.T) {
	newer := testEvent(2, "daemon.event.newer")
	older := testEvent(1, "daemon.event.older")
	overlay := NewEventLogOverlayWithLogFiles([]protocol.EventEnvelope{older, newer}, "/tmp/az-tui.log", "/tmp/azd.log")

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	overlay = model.(*EventLogOverlay)

	view := overlay.View()
	if !strings.Contains(view, newer.Event) {
		t.Fatalf("expanded view missing newest event: %s", view)
	}
	if strings.Contains(view, older.Event) {
		t.Fatalf("expanded view should not expand older event at top: %s", view)
	}
}

func TestEventLogOverlay_Update_ExpandsRevisionlessLocalEvent(t *testing.T) {
	overlay := NewEventLogOverlay([]protocol.EventEnvelope{
		{
			ProtocolVersion: protocol.CurrentVersion,
			ProjectID:       "proj-a",
			Event:           "ui.toast",
			Kind:            protocol.EnvelopeKind("info"),
			EmittedAt:       time.Date(2026, time.March, 25, 17, 0, 0, 0, time.UTC),
			Body:            []byte("Saved settings"),
		},
	})

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	overlay = model.(*EventLogOverlay)

	view := overlay.View()
	if !strings.Contains(view, "ui.toast") || !strings.Contains(view, "Saved settings") {
		t.Fatalf("expanded revisionless event missing details: %s", view)
	}
}

func TestEventLogOverlay_Update_FilterCycles(t *testing.T) {
	hookBody := []byte(`{"source":"githooks.hook","message":"queued daemon git status refresh"}`)
	overlay := NewEventLogOverlayWithLogFiles([]protocol.EventEnvelope{
		testEvent(1, "operation.running"),
		{
			ProtocolVersion: protocol.CurrentVersion,
			ProjectID:       "proj-a",
			Revision:        2,
			Event:           protocol.EventTaskUpdated,
			Kind:            protocol.EnvelopeKindEvent,
			EmittedAt:       time.Date(2026, time.March, 25, 17, 0, 2, 0, time.UTC),
			Body:            []byte(`{"project_id":"proj-a","task_id":"az-2"}`),
		},
		testEvent(3, "git.status.updated"),
		{
			ProtocolVersion: protocol.CurrentVersion,
			ProjectID:       "proj-a",
			Revision:        4,
			Event:           protocol.EventHookLogAppended,
			Kind:            protocol.EnvelopeKindEvent,
			EmittedAt:       time.Date(2026, time.March, 25, 17, 0, 4, 0, time.UTC),
			Body:            hookBody,
		},
	}, "", "")

	runtimeView := overlay.View()
	if !strings.Contains(runtimeView, "[All]") {
		t.Fatalf("all filter not highlighted: %s", runtimeView)
	}

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = model.(*EventLogOverlay)
	opView := overlay.View()
	if !strings.Contains(opView, "[Ops]") {
		t.Fatalf("operation filter not highlighted after tab: %s", opView)
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = model.(*EventLogOverlay)
	mutationView := overlay.View()
	if !strings.Contains(mutationView, "[Tasks]") {
		t.Fatalf("mutation filter not highlighted after tab: %s", mutationView)
	}
	if !strings.Contains(mutationView, "az-2") || !strings.Contains(mutationView, "task") {
		t.Fatalf("mutation filter view missing task event: %s", mutationView)
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = model.(*EventLogOverlay)
	gitView := overlay.View()
	if !strings.Contains(gitView, "[Git]") {
		t.Fatalf("git filter not highlighted after tab: %s", gitView)
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = model.(*EventLogOverlay)
	hookView := overlay.View()
	if !strings.Contains(hookView, "[Hooks]") {
		t.Fatalf("hook filter not highlighted after tab: %s", hookView)
	}
	if !strings.Contains(hookView, protocol.EventHookLogAppended) && !strings.Contains(hookView, "Hook log") {
		t.Fatalf("hook filter view missing hook event: %s", hookView)
	}
}

func TestEventLogOverlay_RenderBodyLines_DoesNotTruncateLongLine(t *testing.T) {
	overlay := NewEventLogOverlay(nil)
	body := []byte(strings.Repeat("x", 140))

	lines := overlay.renderBodyLines(body)
	if len(lines) != 1 {
		t.Fatalf("line count = %d, want 1", len(lines))
	}
	if got := lines[0]; got != string(body) {
		t.Fatalf("line mismatch: len(got)=%d len(want)=%d", len(got), len(body))
	}
	if strings.Contains(lines[0], "...") {
		t.Fatalf("line should not be truncated: %q", lines[0])
	}
}

func TestEventLogOverlay_RenderBodyLines_PrettyPrintsJSON(t *testing.T) {
	overlay := NewEventLogOverlay(nil)
	body := []byte(`{"operation":{"id":"20260401134847.225250000","state":"running"},"project_id":"azedarach","progress":"step1"}`)

	lines := overlay.renderBodyLines(body)
	rendered := strings.Join(lines, "\n")

	if len(lines) < 3 {
		t.Fatalf("expected multiline pretty JSON, got %d lines: %q", len(lines), rendered)
	}
	if !strings.Contains(rendered, "\n  \"operation\": {") {
		t.Fatalf("missing pretty-printed nested object: %q", rendered)
	}
	if !strings.Contains(rendered, "\n  \"project_id\": \"azedarach\"") {
		t.Fatalf("missing pretty-printed field: %q", rendered)
	}
}

func TestEventLogOverlay_ContentCacheInvalidation(t *testing.T) {
	overlay := NewEventLogOverlay([]protocol.EventEnvelope{
		testEvent(1, "daemon.event.one"),
		testEvent(2, "daemon.event.two"),
	})

	if !overlay.contentDirty {
		t.Fatal("expected initial content cache to be dirty")
	}

	first := overlay.contentLines(80)
	if overlay.contentDirty {
		t.Fatal("expected cache to be clean after rendering content lines")
	}
	second := overlay.contentLines(80)
	if len(first) != len(second) {
		t.Fatalf("cache size mismatch: %d vs %d", len(first), len(second))
	}

	overlay.AddEvent(testEvent(3, "daemon.event.three"))
	if !overlay.contentDirty {
		t.Fatal("expected cache invalidation after AddEvent")
	}
}

func TestEventLogOverlay_EventCapAndChronologicalAppend(t *testing.T) {
	overlay := NewEventLogOverlay(nil)
	for i := 1; i <= eventLogMaxRetainedEvents+25; i++ {
		overlay.AddEvent(testEvent(uint64(i), "daemon.event.cap"))
	}

	if len(overlay.events) != eventLogMaxRetainedEvents {
		t.Fatalf("retained events = %d, want %d", len(overlay.events), eventLogMaxRetainedEvents)
	}
	if got := overlay.events[0].Revision; got != 26 {
		t.Fatalf("oldest retained revision = %d, want 26", got)
	}
	if got := overlay.events[len(overlay.events)-1].Revision; got != eventLogMaxRetainedEvents+25 {
		t.Fatalf("newest retained revision = %d, want %d", got, eventLogMaxRetainedEvents+25)
	}
}

func testEvent(revision uint64, event string) protocol.EventEnvelope {
	return protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       "proj-a",
		Revision:        revision,
		Event:           event,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Date(2026, time.March, 25, 17, 0, int(revision), 0, time.UTC),
	}
}
