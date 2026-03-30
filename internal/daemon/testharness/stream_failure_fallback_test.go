package testharness

import (
	"testing"
)

type streamDelta struct {
	revision int
	taskID   string
	value    string
}

type streamDaemon struct {
	h          *Harness
	revision   int
	state      map[string]string
	backlog    []streamDelta
	backlogCap int
	subs       map[int]chan streamDelta
	nextSubID  int
}

func newStreamDaemon(h *Harness, backlogCap int) *streamDaemon {
	return &streamDaemon{
		h:          h,
		state:      make(map[string]string),
		backlogCap: backlogCap,
		subs:       make(map[int]chan streamDelta),
	}
}

func (d *streamDaemon) publish(taskID, value string) {
	d.revision++
	evt := streamDelta{
		revision: d.revision,
		taskID:   taskID,
		value:    value,
	}
	d.state[taskID] = value
	d.backlog = append(d.backlog, evt)
	if len(d.backlog) > d.backlogCap {
		d.backlog = d.backlog[len(d.backlog)-d.backlogCap:]
	}
	_ = d.h.appendEvent("daemon.stream.publish", map[string]any{
		"revision": evt.revision,
		"task_id":  taskID,
		"value":    value,
	})

	for id, ch := range d.subs {
		select {
		case ch <- evt:
		default:
			close(ch)
			delete(d.subs, id)
			_ = d.h.appendEvent("daemon.stream.subscriber_overflow", map[string]any{
				"subscriber_id": id,
				"revision":      evt.revision,
			})
		}
	}
}

func (d *streamDaemon) snapshot() (map[string]string, int) {
	cp := make(map[string]string, len(d.state))
	for k, v := range d.state {
		cp[k] = v
	}
	return cp, d.revision
}

func (d *streamDaemon) subscribe(afterRevision int) (int, chan streamDelta, bool, map[string]string, int) {
	if afterRevision < d.revision {
		if len(d.backlog) == 0 || afterRevision < d.backlog[0].revision-1 {
			snapshot, rev := d.snapshot()
			_ = d.h.appendEvent("client.rehydrate.snapshot", map[string]any{
				"after_revision": afterRevision,
				"snapshot_rev":   rev,
			})
			return 0, nil, true, snapshot, rev
		}
	}

	d.nextSubID++
	id := d.nextSubID
	ch := make(chan streamDelta, len(d.backlog)+1)
	for _, evt := range d.backlog {
		if evt.revision > afterRevision {
			ch <- evt
		}
	}
	d.subs[id] = ch
	_ = d.h.appendEvent("client.stream.resubscribe", map[string]any{
		"subscriber_id":  id,
		"after_revision": afterRevision,
	})
	return id, ch, false, nil, 0
}

func (d *streamDaemon) unsubscribe(id int) {
	if ch, ok := d.subs[id]; ok {
		close(ch)
		delete(d.subs, id)
		_ = d.h.appendEvent("client.stream.drop", map[string]any{
			"subscriber_id": id,
		})
	}
}

type projectedBoard struct {
	state map[string]string
	rev   int
}

func newProjectedBoard() *projectedBoard {
	return &projectedBoard{state: make(map[string]string)}
}

func (b *projectedBoard) apply(evt streamDelta) bool {
	if evt.revision <= b.rev {
		return false
	}
	b.state[evt.taskID] = evt.value
	b.rev = evt.revision
	return true
}

func (b *projectedBoard) hydrate(snapshot map[string]string, revision int) {
	b.state = make(map[string]string, len(snapshot))
	for k, v := range snapshot {
		b.state[k] = v
	}
	b.rev = revision
}

func TestStreamDropResubscribeCatchup(t *testing.T) {
	h := New(Config{
		BaseDir:   t.TempDir(),
		ProjectID: "proj-afq-drop",
	})
	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() {
		if err := h.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	daemon := newStreamDaemon(h, 8)
	board := newProjectedBoard()

	subID, stream, needSnapshot, _, _ := daemon.subscribe(0)
	if needSnapshot {
		t.Fatalf("unexpected snapshot fallback on first subscribe")
	}

	daemon.publish("T-1", "todo")
	evt := <-stream
	if !board.apply(evt) {
		t.Fatalf("failed to apply first live event: %+v", evt)
	}
	daemon.publish("T-2", "in_progress")
	evt = <-stream
	if !board.apply(evt) {
		t.Fatalf("failed to apply second live event: %+v", evt)
	}

	daemon.unsubscribe(subID)
	daemon.publish("T-2", "done")
	daemon.publish("T-3", "todo")

	subID, stream, needSnapshot, snapshot, rev := daemon.subscribe(board.rev)
	if needSnapshot {
		t.Fatalf("did not expect snapshot fallback for in-window catchup: rev=%d snapshot=%v", rev, snapshot)
	}
	defer daemon.unsubscribe(subID)

	for i := 0; i < 2; i++ {
		evt := <-stream
		if !board.apply(evt) {
			t.Fatalf("failed to apply catchup event: %+v", evt)
		}
	}

	if board.rev != 4 {
		t.Fatalf("board revision = %d, want 4", board.rev)
	}
	if got := board.state["T-2"]; got != "done" {
		t.Fatalf("T-2 state = %q, want done", got)
	}
}

func TestStreamOverflowGapFallbackAndIdempotency(t *testing.T) {
	h := New(Config{
		BaseDir:   t.TempDir(),
		ProjectID: "proj-afq-gap",
	})
	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() {
		if err := h.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	daemon := newStreamDaemon(h, 2)
	board := newProjectedBoard()

	subID, stream, needSnapshot, _, _ := daemon.subscribe(0)
	if needSnapshot {
		t.Fatalf("unexpected snapshot fallback on initial subscribe")
	}
	daemon.publish("T-1", "todo")
	first := <-stream
	if !board.apply(first) {
		t.Fatalf("failed to apply first event")
	}

	// Do not consume next publish; this forces overflow for this subscriber.
	daemon.publish("T-2", "todo")
	daemon.publish("T-3", "todo")
	_ = subID

	daemon.publish("T-1", "in_progress")
	daemon.publish("T-4", "todo")

	_, _, needSnapshot, snapshot, rev := daemon.subscribe(board.rev)
	if !needSnapshot {
		t.Fatalf("expected snapshot fallback after overflow+gap")
	}
	board.hydrate(snapshot, rev)

	if board.rev != 5 {
		t.Fatalf("board revision after snapshot = %d, want 5", board.rev)
	}
	if got := board.state["T-1"]; got != "in_progress" {
		t.Fatalf("T-1 state = %q, want in_progress", got)
	}

	// Idempotency: duplicate and out-of-order deltas must be ignored.
	if board.apply(streamDelta{revision: 5, taskID: "T-1", value: "bad"}) {
		t.Fatalf("duplicate revision should be ignored")
	}
	if board.apply(streamDelta{revision: 4, taskID: "T-1", value: "bad"}) {
		t.Fatalf("out-of-order revision should be ignored")
	}
	if got := board.state["T-1"]; got != "in_progress" {
		t.Fatalf("idempotency failed, T-1 = %q", got)
	}

	events := readHarnessEvents(t, h.LogFilePath())
	if countEvent(events, "daemon.stream.subscriber_overflow") < 1 {
		t.Fatalf("expected subscriber_overflow event")
	}
	if countEvent(events, "client.rehydrate.snapshot") < 1 {
		t.Fatalf("expected snapshot rehydrate event")
	}
}

func TestStreamSnapshotFallbackAfterSubscriberOverflow(t *testing.T) {
	h := New(Config{
		BaseDir:   t.TempDir(),
		ProjectID: "proj-afq-overflow",
	})
	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() {
		if err := h.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	daemon := newStreamDaemon(h, 1)
	board := newProjectedBoard()

	subID, stream, needSnapshot, _, _ := daemon.subscribe(0)
	if needSnapshot {
		t.Fatalf("unexpected snapshot fallback on initial subscribe")
	}
	defer daemon.unsubscribe(subID)

	daemon.publish("T-1", "todo")
	first := <-stream
	if !board.apply(first) {
		t.Fatalf("failed to apply first event: %+v", first)
	}

	daemon.publish("T-2", "doing")
	daemon.publish("T-3", "blocked")
	daemon.publish("T-4", "done")

	_, _, needSnapshot, snapshot, rev := daemon.subscribe(board.rev)
	if !needSnapshot {
		t.Fatalf("expected snapshot fallback after subscriber overflow")
	}
	board.hydrate(snapshot, rev)

	if board.rev != 4 {
		t.Fatalf("board revision after hydrate = %d, want 4", board.rev)
	}
	if got := board.state["T-4"]; got != "done" {
		t.Fatalf("T-4 state = %q, want done", got)
	}
	if got := board.state["T-1"]; got != "todo" {
		t.Fatalf("T-1 state = %q, want todo", got)
	}

	events := readHarnessEvents(t, h.LogFilePath())
	if countEvent(events, "daemon.stream.subscriber_overflow") < 1 {
		t.Fatalf("expected subscriber_overflow event")
	}
	if countEvent(events, "client.rehydrate.snapshot") != 1 {
		t.Fatalf("expected exactly one snapshot rehydrate event, got %d", countEvent(events, "client.rehydrate.snapshot"))
	}
}
