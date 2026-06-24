package publish

import (
	"log/slog"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
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
	h.Publish(makeEvent("proj", 3, "r3"))

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

func TestSubscribeCatchupOverflowTruncatesToLatestEvents(t *testing.T) {
	h := NewHub(32, 1, slog.Default())
	h.Publish(makeEvent("proj", 1, "r1"))
	h.Publish(makeEvent("proj", 2, "r2"))

	ch, cancel := h.Subscribe("proj", 0)
	defer cancel()

	// Catch-up should truncate to the newest event that fits subscriber queue.
	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("expected one queued catch-up event")
		}
		if evt.Revision != 2 {
			t.Fatalf("unexpected catch-up revision: %d", evt.Revision)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out reading catch-up event")
	}

	// Subscriber should remain attached and receive future events.
	h.Publish(makeEvent("proj", 3, "r3"))
	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("subscriber unexpectedly closed after catch-up")
		}
		if evt.Revision != 3 {
			t.Fatalf("unexpected post-catch-up revision: %d", evt.Revision)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for post-catch-up event")
	}
}

func TestTaskMutationUsesSeparateLaneFromRuntimeTelemetry(t *testing.T) {
	h := NewHub(32, 2, slog.Default())
	ch, cancel := h.Subscribe("proj", 0)
	defer cancel()

	h.Publish(makeEvent("proj", 1, protocol.EventGitStatusUpdated))
	h.Publish(makeEvent("proj", 2, protocol.EventWorktreeProjectionUpdated))
	h.Publish(makeEvent("proj", 3, protocol.EventTaskCreated))

	for _, want := range []struct {
		revision uint64
		event    string
	}{
		{revision: 1, event: protocol.EventGitStatusUpdated},
		{revision: 2, event: protocol.EventWorktreeProjectionUpdated},
		{revision: 3, event: protocol.EventTaskCreated},
	} {
		got := recv(t, ch)
		if got.Revision != want.revision || got.Event != want.event {
			t.Fatalf("event = %s:%d, want %s:%d", got.Event, got.Revision, want.event, want.revision)
		}
	}
}

func TestDurableEventLaneOverflowsIndependentlyOfTelemetryLane(t *testing.T) {
	h := NewHub(32, 3, slog.Default())
	ch, _ := h.Subscribe("proj", 0)

	h.Publish(makeEvent("proj", 1, protocol.EventGitStatusUpdated))
	h.Publish(makeEvent("proj", 2, protocol.EventUICommandRequested))
	h.Publish(makeEvent("proj", 3, protocol.EventWorktreeProjectionUpdated))
	h.Publish(makeEvent("proj", 4, protocol.EventTaskCreated))
	h.Publish(makeEvent("proj", 5, protocol.EventTaskUpdated))
	h.Publish(makeEvent("proj", 6, protocol.EventTaskDeleted))

	expectClosed(t, ch)
}

func TestPriorityTaskMutationDoesNotEvictQueuedTaskMutations(t *testing.T) {
	h := NewHub(32, 1, slog.Default())
	ch, _ := h.Subscribe("proj", 0)

	h.Publish(makeEvent("proj", 1, protocol.EventTaskUpdated))
	h.Publish(makeEvent("proj", 2, protocol.EventTaskCreated))
	h.Publish(makeEvent("proj", 3, protocol.EventTaskDeleted))

	expectClosed(t, ch)
}

func TestProjectionLaneCoalescesInsteadOfOverflowing(t *testing.T) {
	h := NewHub(32, 1, slog.Default())
	ch, cancel := h.Subscribe("proj", 0)
	defer cancel()

	h.Publish(makeEvent("proj", 1, protocol.EventGitStatusUpdated))
	h.Publish(makeEvent("proj", 2, protocol.EventGitStatusUpdated))
	h.Publish(makeEvent("proj", 3, protocol.EventGitStatusUpdated))

	recvUntilRevision(t, ch, 3)

	h.Publish(makeEvent("proj", 4, protocol.EventGitStatusUpdated))
	if got := recv(t, ch); got.Revision != 4 {
		t.Fatalf("post-coalesce revision = %d, want 4", got.Revision)
	}
}

func TestSessionProjectionLaneCoalescesInsteadOfOverflowing(t *testing.T) {
	h := NewHub(32, 1, slog.Default())
	ch, cancel := h.Subscribe("proj", 0)
	defer cancel()

	h.Publish(makeEvent("proj", 1, protocol.EventSessionUpdated))
	h.Publish(makeEvent("proj", 2, protocol.EventSessionUpdated))
	h.Publish(makeEvent("proj", 3, protocol.EventSessionUpdated))

	recvUntilRevision(t, ch, 3)

	h.Publish(makeEvent("proj", 4, protocol.EventSessionUpdated))
	if got := recv(t, ch); got.Revision != 4 {
		t.Fatalf("post-coalesce revision = %d, want 4", got.Revision)
	}
}

func TestProjectionLaneCoalescesIndependentlyOfDurableLane(t *testing.T) {
	h := NewHub(32, 2, slog.Default())
	ch, cancel := h.Subscribe("proj", 0)
	defer cancel()

	h.Publish(makeEvent("proj", 1, protocol.EventGitStatusUpdated))
	h.Publish(makeEvent("proj", 2, protocol.EventWorktreeProjectionUpdated))
	h.Publish(makeEvent("proj", 3, protocol.EventTaskCreated))
	h.Publish(makeEvent("proj", 4, protocol.EventGitStatusUpdated))
	h.Publish(makeEvent("proj", 5, protocol.EventWorktreeProjectionUpdated))

	sawTaskMutation := false
	sawLatestProjection := false
	for !sawTaskMutation || !sawLatestProjection {
		got := recv(t, ch)
		if got.Revision == 3 && got.Event == protocol.EventTaskCreated {
			sawTaskMutation = true
		}
		if got.Revision == 5 && got.Event == protocol.EventWorktreeProjectionUpdated {
			sawLatestProjection = true
		}
	}
}

func TestTelemetryLaneCapacityRecoversAfterSubscriberDrain(t *testing.T) {
	h := NewHub(32, 2, slog.Default())
	ch, cancel := h.Subscribe("proj", 0)
	defer cancel()

	h.Publish(makeEvent("proj", 1, protocol.EventGitStatusUpdated))
	h.Publish(makeEvent("proj", 2, protocol.EventWorktreeProjectionUpdated))
	if got := recv(t, ch); got.Revision != 1 {
		t.Fatalf("first drained revision = %d, want 1", got.Revision)
	}

	h.Publish(makeEvent("proj", 3, protocol.EventGitStatusUpdated))

	for _, want := range []uint64{2, 3} {
		got := recv(t, ch)
		if got.Revision != want {
			t.Fatalf("drained revision = %d, want %d", got.Revision, want)
		}
	}
}

func recv(t *testing.T, ch <-chan protocol.EventEnvelope) protocol.EventEnvelope {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed unexpectedly")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return protocol.EventEnvelope{}
	}
}

func recvUntilRevision(t *testing.T, ch <-chan protocol.EventEnvelope, revision uint64) protocol.EventEnvelope {
	t.Helper()
	for {
		evt := recv(t, ch)
		if evt.Revision == revision {
			return evt
		}
		if evt.Revision > revision {
			t.Fatalf("received revision %d after expected revision %d", evt.Revision, revision)
		}
	}
}

func expectClosed(t *testing.T, ch <-chan protocol.EventEnvelope) {
	t.Helper()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for subscriber channel to close")
		}
	}
}

func makeEvent(projectID string, revision uint64, event string) protocol.EventEnvelope {
	return protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(projectID),
		Revision:        revision,
		Event:           event,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Meta: protocol.Metadata{
			CorrelationID: naming.CorrelationID("corr-" + event),
		},
	}
}
