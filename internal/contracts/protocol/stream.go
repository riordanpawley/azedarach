package protocol

// StreamProjectionDecision captures how a live daemon event should affect a client projection.
type StreamProjectionDecision uint8

const (
	// StreamProjectionDecisionIgnore drops stale or duplicate events.
	StreamProjectionDecisionIgnore StreamProjectionDecision = iota
	// StreamProjectionDecisionApply advances the projection by one revision.
	StreamProjectionDecisionApply
	// StreamProjectionDecisionResync asks the client to rehydrate from snapshot state.
	StreamProjectionDecisionResync
)

// Valid reports whether the decision is part of the projection taxonomy.
func (d StreamProjectionDecision) Valid() bool {
	switch d {
	case StreamProjectionDecisionIgnore, StreamProjectionDecisionApply, StreamProjectionDecisionResync:
		return true
	default:
		return false
	}
}

// StreamCursor tracks the last projection revision applied by a client.
type StreamCursor struct {
	Revision uint64 `json:"revision" msgpack:"revision"`
}

// Decide evaluates the next stream event against the current cursor.
//
// The contract is intentionally simple:
// - stale or duplicate events are ignored
// - the next sequential revision is applied idempotently
// - any gap beyond the next sequential revision requires a snapshot rehydrate
func (c StreamCursor) Decide(evt EventEnvelope) StreamProjectionDecision {
	switch {
	case evt.Revision <= c.Revision:
		return StreamProjectionDecisionIgnore
	case evt.Revision > c.Revision+1:
		return StreamProjectionDecisionResync
	default:
		return StreamProjectionDecisionApply
	}
}

// Advance returns the next monotonic cursor after applying evt.
//
// Callers should only advance after a successful sequential apply. Gap events
// are intentionally left at the current cursor so the client can rehydrate
// from a fresh snapshot instead of speculatively skipping ahead.
func (c StreamCursor) Advance(evt EventEnvelope) StreamCursor {
	if evt.Revision == c.Revision+1 {
		c.Revision = evt.Revision
	}
	return c
}
