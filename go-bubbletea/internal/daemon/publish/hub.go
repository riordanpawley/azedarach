package publish

import (
	"log/slog"
	"slices"
	"sync"

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
		select {
		case sub.ch <- evt:
		default:
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
	backlog := append([]protocol.EventEnvelope(nil), h.backlogByProj[projectID]...)
	h.mu.Unlock()

	// Catch-up on subscribe with strict > fromRevision ordering.
	for _, evt := range backlog {
		if evt.Revision > fromRevision {
			sub.ch <- evt
		}
	}

	cancel := func() { h.unsubscribeByID(id) }
	return sub.ch, cancel
}

func (h *Hub) subscribersForProjectLocked(projectID string) []*subscriber {
	out := make([]*subscriber, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		if sub.projectID == projectID {
			out = append(out, sub)
		}
	}
	return out
}

func (h *Hub) unsubscribeByID(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub, ok := h.subscribers[id]
	if !ok {
		return
	}
	delete(h.subscribers, id)
	close(sub.ch)
}

func appendTrimmed(list []protocol.EventEnvelope, evt protocol.EventEnvelope, max int) []protocol.EventEnvelope {
	out := append(list, evt)
	if len(out) <= max {
		return out
	}
	trim := len(out) - max
	return slices.Clone(out[trim:])
}
