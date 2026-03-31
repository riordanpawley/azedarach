package publish

import (
	"log/slog"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestSubscribeReceivesOrderedPublishedEvents(t *testing.T) {
	h := NewHub(32, 8, slog.Default())
	ch, cancel := h.Subscribe("proj", 0)
	defer cancel()

	h.Publish(makeEvent("proj", 1, "session.started"))
	h.Publish(makeEvent("proj", 2, "session.attached"))

	e1 := recv(t, ch)
	e2 := recv(t, ch)
	if e1.Revision != 1 || e2.Revision != 2 {
		t.Fatalf("unexpected revisions: %d, %d", e1.Revision, e2.Revision)
	}
}

func TestSubscribeCatchupFromRevision(t *testing.T) {
	h := NewHub(32, 8, slog.Default())
	h.Publish(makeEvent("proj", 1, "r1"))
	h.Publish(makeEvent("proj", 2, "r2"))
	h.Publish(makeEvent("proj", 3, "r3"))

	ch, cancel := h.Subscribe("proj", 1)
	defer cancel()
	e2 := recv(t, ch)
	e3 := recv(t, ch)

	if e2.Revision != 2 || e3.Revision != 3 {
		t.Fatalf("unexpected catch-up revisions: %d, %d", e2.Revision, e3.Revision)
	}
}

func TestOverflowSubscriberRemovedDeterministically(t *testing.T) {
	h := NewHub(32, 1, slog.Default())
	ch, _ := h.Subscribe("proj", 0)

	// Fill queue and trigger overflow unsubscribe.
	h.Publish(makeEvent("proj", 1, "r1"))
	h.Publish(makeEvent("proj", 2, "r2"))

	// Channel should eventually close after overflow unsubscribe.
	select {
	case _, ok := <-ch:
		if ok {
			// One queued event may still be present; next read must close.
			select {
			case _, ok2 := <-ch:
				if ok2 {
					t.Fatal("subscriber channel should close after overflow")
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("timed out waiting for overflow close")
			}
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out reading subscriber channel")
	}
}

func recv(t *testing.T, ch <-chan protocol.EventEnvelope) protocol.EventEnvelope {
	t.Helper()
	select {
	case evt := <-ch:
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return protocol.EventEnvelope{}
	}
}

func makeEvent(projectID string, revision uint64, event string) protocol.EventEnvelope {
	return protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       projectID,
		Revision:        revision,
		Event:           event,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Meta: protocol.Metadata{
			CorrelationID: "corr-" + event,
		},
	}
}
