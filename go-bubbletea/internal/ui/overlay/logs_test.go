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

	if got := overlay.events[0].Revision; got != newer.Revision {
		t.Fatalf("events[0].Revision = %d, want newest revision %d", got, newer.Revision)
	}

	view := overlay.View()
	if !strings.Contains(view, "Event Log") {
		t.Fatalf("View() missing title: %s", view)
	}
	if !strings.Contains(view, "s: stream log") {
		t.Fatalf("View() missing stream hint: %s", view)
	}
	if !strings.Contains(view, "L: logs") {
		t.Fatalf("View() missing board hotkey hint: %s", view)
	}

	newerIdx := strings.Index(view, newer.Event)
	olderIdx := strings.Index(view, older.Event)
	if newerIdx < 0 || olderIdx < 0 {
		t.Fatalf("View() missing event names: %s", view)
	}
	if newerIdx > olderIdx {
		t.Fatalf("newer event rendered after older event: newer=%d older=%d view=%s", newerIdx, olderIdx, view)
	}

	if !strings.Contains(view, "correlation=corr-new") {
		t.Fatalf("View() missing newer metadata: %s", view)
	}
}

func TestEventLogOverlay_View_EmptyState(t *testing.T) {
	overlay := NewEventLogOverlay(nil)
	overlay.viewHeight = 12

	view := overlay.View()
	if !strings.Contains(view, "No runtime events yet.") {
		t.Fatalf("View() missing empty state: %s", view)
	}
	if !strings.Contains(view, "Esc/q/backspace: close") {
		t.Fatalf("View() missing close hint: %s", view)
	}
}

func TestEventLogOverlay_Update_NavigationAndClose(t *testing.T) {
	overlay := NewEventLogOverlay([]protocol.EventEnvelope{
		testEvent(1, "daemon.event.one"),
		testEvent(2, "daemon.event.two"),
		testEvent(3, "daemon.event.three"),
		testEvent(4, "daemon.event.four"),
	})
	overlay.viewHeight = 4

	// Prime maxScroll from the rendered content.
	_ = overlay.View()
	if overlay.maxScroll == 0 {
		t.Fatal("expected scrollable content")
	}

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
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
