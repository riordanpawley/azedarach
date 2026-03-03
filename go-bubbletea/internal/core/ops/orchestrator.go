package ops

import (
	"errors"
	"fmt"
	"sync"
)

type State string

const (
	StateQueued     State = "queued"
	StateRunning    State = "running"
	StateSucceeded  State = "succeeded"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
	StateRolledBack State = "rolled_back"
)

func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled, StateRolledBack:
		return true
	default:
		return false
	}
}

type Request struct {
	IssueKey       string
	IdempotencyKey string
	CorrelationID  string
}

type Operation struct {
	ID             string
	IssueKey       string
	IdempotencyKey string
	CorrelationID  string
	State          State
	Reason         string
}

type Event struct {
	Sequence      uint64 `json:"sequence"`
	OperationID   string `json:"operation_id"`
	IssueKey      string `json:"issue_key"`
	State         State  `json:"state"`
	CorrelationID string `json:"correlation_id"`
	Reason        string `json:"reason,omitempty"`
}

type Orchestrator struct {
	mu sync.Mutex

	nextOperationID uint64
	nextEventSeq    uint64

	queue          []string
	operations     map[string]*Operation
	runningByIssue map[string]string
	byIdempotency  map[string]string
	events         []Event
}

func NewOrchestrator() *Orchestrator {
	return &Orchestrator{
		nextOperationID: 1,
		nextEventSeq:    1,
		operations:      map[string]*Operation{},
		runningByIssue:  map[string]string{},
		byIdempotency:   map[string]string{},
		events:          make([]Event, 0, 64),
	}
}

func (o *Orchestrator) Queue(req Request) (Operation, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if req.IdempotencyKey != "" {
		if existingID, ok := o.byIdempotency[req.IdempotencyKey]; ok {
			existing, ok := o.operations[existingID]
			if !ok {
				return Operation{}, false, errors.New("idempotency index is corrupted")
			}
			return *existing, false, nil
		}
	}

	opID := fmt.Sprintf("op-%06d", o.nextOperationID)
	o.nextOperationID++

	correlationID := req.CorrelationID
	if correlationID == "" {
		correlationID = opID
	}

	op := &Operation{
		ID:             opID,
		IssueKey:       req.IssueKey,
		IdempotencyKey: req.IdempotencyKey,
		CorrelationID:  correlationID,
		State:          StateQueued,
	}

	o.operations[op.ID] = op
	o.queue = append(o.queue, op.ID)
	if op.IdempotencyKey != "" {
		o.byIdempotency[op.IdempotencyKey] = op.ID
	}
	o.appendEventLocked(op, StateQueued, "")

	return *op, true, nil
}

func (o *Orchestrator) StartNext() (Operation, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	for idx, id := range o.queue {
		op := o.operations[id]
		if op.State != StateQueued {
			continue
		}
		if op.IssueKey != "" {
			if _, blocked := o.runningByIssue[op.IssueKey]; blocked {
				continue
			}
		}

		o.queue = append(o.queue[:idx], o.queue[idx+1:]...)
		op.State = StateRunning
		op.Reason = ""
		if op.IssueKey != "" {
			o.runningByIssue[op.IssueKey] = op.ID
		}
		o.appendEventLocked(op, StateRunning, "")
		return *op, true
	}

	return Operation{}, false
}

func (o *Orchestrator) Succeed(operationID string) (Operation, error) {
	return o.transition(operationID, StateSucceeded, "")
}

func (o *Orchestrator) Fail(operationID string, reason string) (Operation, error) {
	return o.transition(operationID, StateFailed, reason)
}

func (o *Orchestrator) RollBack(operationID string, reason string) (Operation, error) {
	return o.transition(operationID, StateRolledBack, reason)
}

func (o *Orchestrator) Cancel(operationID string, reason string) (Operation, error) {
	return o.transition(operationID, StateCancelled, reason)
}

func (o *Orchestrator) Get(operationID string) (Operation, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	op, ok := o.operations[operationID]
	if !ok {
		return Operation{}, false
	}

	return *op, true
}

func (o *Orchestrator) Events(correlationID string) []Event {
	o.mu.Lock()
	defer o.mu.Unlock()

	if correlationID == "" {
		out := make([]Event, len(o.events))
		copy(out, o.events)
		return out
	}

	filtered := make([]Event, 0, len(o.events))
	for _, event := range o.events {
		if event.CorrelationID == correlationID {
			filtered = append(filtered, event)
		}
	}

	return filtered
}

func (o *Orchestrator) transition(operationID string, next State, reason string) (Operation, error) {
	if next != StateSucceeded && next != StateFailed && next != StateCancelled && next != StateRolledBack {
		return Operation{}, fmt.Errorf("invalid transition target: %s", next)
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	op, ok := o.operations[operationID]
	if !ok {
		return Operation{}, fmt.Errorf("operation not found: %s", operationID)
	}

	if op.State.Terminal() {
		return *op, nil
	}

	if op.State == StateQueued {
		o.removeFromQueueLocked(operationID)
	}

	if op.State == StateRunning && op.IssueKey != "" {
		delete(o.runningByIssue, op.IssueKey)
	}

	op.State = next
	op.Reason = reason
	o.appendEventLocked(op, next, reason)
	return *op, nil
}

func (o *Orchestrator) removeFromQueueLocked(operationID string) {
	for idx, id := range o.queue {
		if id == operationID {
			o.queue = append(o.queue[:idx], o.queue[idx+1:]...)
			return
		}
	}
}

func (o *Orchestrator) appendEventLocked(op *Operation, state State, reason string) {
	event := Event{
		Sequence:      o.nextEventSeq,
		OperationID:   op.ID,
		IssueKey:      op.IssueKey,
		State:         state,
		CorrelationID: op.CorrelationID,
		Reason:        reason,
	}
	o.nextEventSeq++
	o.events = append(o.events, event)
}
