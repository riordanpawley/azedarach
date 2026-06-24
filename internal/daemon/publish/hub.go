package publish

import (
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

// Hub fanouts ordered revision-tagged events and supports resume catch-up.
type Hub struct {
	mu             sync.RWMutex
	nextSubID      int
	subscribers    map[int]*subscriber
	backlogByProj  map[string][]protocol.EventEnvelope
	maxBacklog     int
	maxSubscriberQ int
	logger         *slog.Logger
}

type subscriber struct {
	id              int
	projectID       string
	ch              chan protocol.EventEnvelope
	notify          chan struct{}
	done            chan struct{}
	mu              sync.RWMutex
	closed          bool
	laneLimit       int
	queue           []protocol.EventEnvelope
	telemetryQueued int
	durableQueued   int
}

// NewHub returns an event publish/subscribe hub.
func NewHub(maxBacklog, maxSubscriberQ int, logger *slog.Logger) *Hub {
	if maxBacklog <= 0 {
		maxBacklog = 512
	}
	if maxSubscriberQ <= 0 {
		maxSubscriberQ = 64
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		subscribers:    make(map[int]*subscriber),
		backlogByProj:  make(map[string][]protocol.EventEnvelope),
		maxBacklog:     maxBacklog,
		maxSubscriberQ: maxSubscriberQ,
		logger:         logger,
	}
}

// Publish appends event to backlog and attempts non-blocking fanout.
func (h *Hub) Publish(evt protocol.EventEnvelope) {
	projectID := evt.ProjectID.String()
	h.mu.Lock()
	h.backlogByProj[projectID] = appendTrimmed(h.backlogByProj[projectID], evt, h.maxBacklog)
	subs := h.subscribersForProjectLocked(projectID)
	h.mu.Unlock()

	h.logger.Info(
		"daemon.event.publish",
		"event", evt.Event,
		"project_id", evt.ProjectID,
		"revision", evt.Revision,
		"correlation_id", evt.Meta.CorrelationID,
	)

	for _, sub := range subs {
		if !sub.trySend(evt) {
			h.logger.Warn(
				"daemon.event.subscriber_overflow",
				"project_id", evt.ProjectID,
				"subscriber_id", sub.id,
				"revision", evt.Revision,
			)
			h.unsubscribeByID(sub.id)
		}
	}
}

// Subscribe attaches subscriber and sends backlog events with revision > fromRevision.
func (h *Hub) Subscribe(projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, func()) {
	h.mu.Lock()
	h.nextSubID++
	id := h.nextSubID
	sub := &subscriber{
		id:        id,
		projectID: projectID,
		ch:        make(chan protocol.EventEnvelope),
		notify:    make(chan struct{}, 1),
		done:      make(chan struct{}),
		laneLimit: h.maxSubscriberQ,
	}
	h.subscribers[id] = sub
	backlog := h.backlogForProjectLocked(projectID)
	h.mu.Unlock()
	go sub.pump()

	// Catch-up on subscribe with strict > fromRevision ordering.
	catchup := make([]protocol.EventEnvelope, 0, len(backlog))
	for _, evt := range backlog {
		if evt.Revision > fromRevision {
			catchup = append(catchup, evt)
		}
	}
	delivered := truncateCatchupByLane(catchup, h.maxSubscriberQ)
	if dropped := len(catchup) - len(delivered); dropped > 0 {
		catchup = delivered
		h.logger.Warn(
			"daemon.event.subscribe.catchup_truncated",
			"project_id", projectID,
			"subscriber_id", sub.id,
			"from_revision", fromRevision,
			"dropped_events", dropped,
			"delivered_events", len(catchup),
		)
	}
	for _, evt := range catchup {
		if !sub.trySend(evt) {
			h.logger.Warn(
				"daemon.event.subscriber_overflow",
				"project_id", projectID,
				"subscriber_id", sub.id,
				"revision", evt.Revision,
			)
			h.unsubscribeByID(sub.id)
			break
		}
	}

	cancel := func() { h.unsubscribeByID(id) }
	return sub.ch, cancel
}

func (h *Hub) subscribersForProjectLocked(projectID string) []*subscriber {
	out := make([]*subscriber, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		if sub.projectID == projectID || sub.projectID == protocol.GlobalEventStreamProjectID {
			out = append(out, sub)
		}
	}
	return out
}

func (h *Hub) backlogForProjectLocked(projectID string) []protocol.EventEnvelope {
	if projectID != protocol.GlobalEventStreamProjectID {
		return append([]protocol.EventEnvelope(nil), h.backlogByProj[projectID]...)
	}
	merged := make([]protocol.EventEnvelope, 0, h.maxBacklog)
	for _, events := range h.backlogByProj {
		merged = append(merged, events...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		ti := merged[i].EmittedAt
		tj := merged[j].EmittedAt
		if ti.Equal(tj) {
			if merged[i].ProjectID == merged[j].ProjectID {
				return merged[i].Revision < merged[j].Revision
			}
			return merged[i].ProjectID < merged[j].ProjectID
		}
		if ti.IsZero() && !tj.IsZero() {
			return true
		}
		if !ti.IsZero() && tj.IsZero() {
			return false
		}
		return ti.Before(tj)
	})
	if len(merged) <= h.maxBacklog {
		return merged
	}
	return append([]protocol.EventEnvelope(nil), merged[len(merged)-h.maxBacklog:]...)
}

func (h *Hub) unsubscribeByID(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub, ok := h.subscribers[id]
	if !ok {
		return
	}
	delete(h.subscribers, id)
	sub.close()
}

func (s *subscriber) trySend(evt protocol.EventEnvelope) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if isCoalescibleProjectionEvent(evt) {
		if s.telemetryQueued >= s.laneLimit {
			if !s.dropQueuedProjectionEventLocked(evt) {
				return false
			}
		}
		s.queue = append(s.queue, evt)
		s.telemetryQueued++
		s.notifyPumpLocked()
		return true
	}

	if s.durableQueued >= s.laneLimit {
		return false
	}
	s.queue = append(s.queue, evt)
	s.durableQueued++
	s.notifyPumpLocked()
	return true
}

func (s *subscriber) pump() {
	defer close(s.ch)
	for {
		s.mu.RLock()
		if len(s.queue) == 0 {
			s.mu.RUnlock()
			select {
			case <-s.done:
				return
			case <-s.notify:
				continue
			}
		}
		evt := s.queue[0]
		s.mu.RUnlock()

		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			continue
		}
		evt = s.queue[0]
		if isCoalescibleProjectionEvent(evt) {
			s.telemetryQueued--
		} else {
			s.durableQueued--
		}
		s.queue = s.queue[1:]
		s.mu.Unlock()

		select {
		case <-s.done:
			return
		case s.ch <- evt:
		}
	}
}

func (s *subscriber) notifyPumpLocked() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
}

func (s *subscriber) dropQueuedProjectionEventLocked(next protocol.EventEnvelope) bool {
	dropIndex := -1
	for i, queued := range s.queue {
		if isCoalescibleProjectionEvent(queued) && queued.Event == next.Event {
			dropIndex = i
			break
		}
	}
	if dropIndex == -1 {
		for i, queued := range s.queue {
			if isCoalescibleProjectionEvent(queued) {
				dropIndex = i
				break
			}
		}
	}
	if dropIndex == -1 {
		return false
	}
	copy(s.queue[dropIndex:], s.queue[dropIndex+1:])
	s.queue = s.queue[:len(s.queue)-1]
	s.telemetryQueued--
	return true
}

func isCoalescibleProjectionEvent(evt protocol.EventEnvelope) bool {
	switch evt.Event {
	case protocol.EventGitStatusUpdated,
		protocol.EventWorktreeProjectionUpdated,
		protocol.EventSessionUpdated:
		return true
	default:
		return false
	}
}

func truncateCatchupByLane(events []protocol.EventEnvelope, laneLimit int) []protocol.EventEnvelope {
	if laneLimit <= 0 {
		return nil
	}
	telemetryCount := 0
	durableCount := 0
	keep := make([]protocol.EventEnvelope, 0, laneLimit*2)
	for i := len(events) - 1; i >= 0; i-- {
		evt := events[i]
		if isCoalescibleProjectionEvent(evt) {
			if telemetryCount >= laneLimit {
				continue
			}
			telemetryCount++
		} else {
			if durableCount >= laneLimit {
				continue
			}
			durableCount++
		}
		keep = append(keep, evt)
	}
	for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
		keep[i], keep[j] = keep[j], keep[i]
	}
	return keep
}

func appendTrimmed(list []protocol.EventEnvelope, evt protocol.EventEnvelope, max int) []protocol.EventEnvelope {
	if evt.EmittedAt.IsZero() {
		evt.EmittedAt = time.Now().UTC()
	}
	out := append(list, evt)
	if len(out) <= max {
		return out
	}
	trim := len(out) - max
	return slices.Clone(out[trim:])
}
