package publish

import (
	"log/slog"
	"sort"
	"slices"
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
	id        int
	projectID string
	ch        chan protocol.EventEnvelope
	mu        sync.RWMutex
	closed    bool
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
	h.mu.Lock()
	h.backlogByProj[evt.ProjectID] = appendTrimmed(h.backlogByProj[evt.ProjectID], evt, h.maxBacklog)
	subs := h.subscribersForProjectLocked(evt.ProjectID)
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
		ch:        make(chan protocol.EventEnvelope, h.maxSubscriberQ),
	}
	h.subscribers[id] = sub
	backlog := h.backlogForProjectLocked(projectID)
	h.mu.Unlock()

	// Catch-up on subscribe with strict > fromRevision ordering.
	catchup := make([]protocol.EventEnvelope, 0, len(backlog))
	for _, evt := range backlog {
		if evt.Revision > fromRevision {
			catchup = append(catchup, evt)
		}
	}
	if overflow := len(catchup) - h.maxSubscriberQ; overflow > 0 {
		catchup = catchup[overflow:]
		h.logger.Warn(
			"daemon.event.subscribe.catchup_truncated",
			"project_id", projectID,
			"subscriber_id", sub.id,
			"from_revision", fromRevision,
			"dropped_events", overflow,
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- evt:
		return true
	default:
		return false
	}
}

func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
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
