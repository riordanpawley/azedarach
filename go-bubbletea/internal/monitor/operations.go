package monitor

import (
	"sort"
	"sync"
	"time"
)

// OperationStatus represents an operation lifecycle state.
type OperationStatus string

const (
	OperationQueued    OperationStatus = "queued"
	OperationRunning   OperationStatus = "running"
	OperationSucceeded OperationStatus = "succeeded"
	OperationFailed    OperationStatus = "failed"
	OperationCanceled  OperationStatus = "canceled"
)

// Severity indicates how urgent an event is for notifications.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// OperationEvent is one point in the operation timeline.
type OperationEvent struct {
	Sequence  int64
	At        time.Time
	Operation string
	Stage     string
	Status    OperationStatus
	Message   string
	Severity  Severity
}

// Timeline stores a bounded, deterministic stream of operation events.
type Timeline struct {
	mu       sync.Mutex
	capacity int
	nextSeq  int64
	events   []OperationEvent
}

// NewTimeline creates a timeline with a fixed event capacity.
func NewTimeline(capacity int) *Timeline {
	if capacity <= 0 {
		capacity = 128
	}

	return &Timeline{capacity: capacity}
}

// Record appends a new operation event and returns the stored event.
func (t *Timeline) Record(at time.Time, operation string, stage string, status OperationStatus, message string) OperationEvent {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextSeq++
	event := OperationEvent{
		Sequence:  t.nextSeq,
		At:        at,
		Operation: operation,
		Stage:     stage,
		Status:    status,
		Message:   message,
		Severity:  severityFromStatus(status),
	}

	t.events = append(t.events, event)
	if len(t.events) > t.capacity {
		start := len(t.events) - t.capacity
		t.events = append([]OperationEvent(nil), t.events[start:]...)
	}

	return event
}

// Events returns a copy of timeline events in deterministic sequence order.
func (t *Timeline) Events() []OperationEvent {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := append([]OperationEvent(nil), t.events...)
	sort.SliceStable(out, func(i int, j int) bool {
		return out[i].Sequence < out[j].Sequence
	})
	return out
}

// NotificationPolicy controls which operation transitions emit notifications.
type NotificationPolicy struct {
	NotifyOnStart   bool
	NotifyOnSuccess bool
	NotifyOnFailure bool
	NotifyOnCancel  bool
}

// DefaultNotificationPolicy returns production-safe defaults.
func DefaultNotificationPolicy() NotificationPolicy {
	return NotificationPolicy{
		NotifyOnStart:   false,
		NotifyOnSuccess: true,
		NotifyOnFailure: true,
		NotifyOnCancel:  true,
	}
}

// OperationTransition represents a state change for an operation.
type OperationTransition struct {
	Operation string
	From      OperationStatus
	To        OperationStatus
	Message   string
}

// NotificationDecision captures how a transition should be surfaced.
type NotificationDecision struct {
	Notify bool
	Level  Severity
	Title  string
	Body   string
}

// Evaluate determines if a transition should trigger a notification.
func (p NotificationPolicy) Evaluate(change OperationTransition) NotificationDecision {
	if change.From == change.To {
		return NotificationDecision{}
	}

	decision := NotificationDecision{
		Level: severityFromStatus(change.To),
		Title: change.Operation,
		Body:  change.Message,
	}

	switch change.To {
	case OperationRunning:
		decision.Notify = p.NotifyOnStart
	case OperationSucceeded:
		decision.Notify = p.NotifyOnSuccess
	case OperationFailed:
		decision.Notify = p.NotifyOnFailure
	case OperationCanceled:
		decision.Notify = p.NotifyOnCancel
	default:
		decision.Notify = false
	}

	return decision
}

// DiagnosticEventRow is a render-friendly diagnostics row.
type DiagnosticEventRow struct {
	Sequence  int64
	Operation string
	Stage     string
	Status    OperationStatus
	Message   string
	At        time.Time
}

// DiagnosticsViewModel provides operation-focused diagnostics state for UI.
type DiagnosticsViewModel struct {
	Active      int
	Queued      int
	Succeeded   int
	Failed      int
	Canceled    int
	TotalEvents int
	LastUpdated time.Time
	Recent      []DiagnosticEventRow
}

// BuildDiagnosticsViewModel builds deterministic diagnostics data from timeline events.
func BuildDiagnosticsViewModel(now time.Time, events []OperationEvent, maxRecent int) DiagnosticsViewModel {
	if maxRecent <= 0 {
		maxRecent = 10
	}

	ordered := append([]OperationEvent(nil), events...)
	sort.SliceStable(ordered, func(i int, j int) bool {
		return ordered[i].Sequence < ordered[j].Sequence
	})

	type opState struct {
		sequence int64
		status   OperationStatus
	}

	byOperation := make(map[string]opState)
	for _, event := range ordered {
		state, ok := byOperation[event.Operation]
		if !ok || event.Sequence > state.sequence {
			byOperation[event.Operation] = opState{sequence: event.Sequence, status: event.Status}
		}
	}

	vm := DiagnosticsViewModel{TotalEvents: len(ordered), LastUpdated: now}
	for _, state := range byOperation {
		switch state.status {
		case OperationQueued:
			vm.Queued++
		case OperationRunning:
			vm.Active++
		case OperationSucceeded:
			vm.Succeeded++
		case OperationFailed:
			vm.Failed++
		case OperationCanceled:
			vm.Canceled++
		}
	}

	start := 0
	if len(ordered) > maxRecent {
		start = len(ordered) - maxRecent
	}

	for i := len(ordered) - 1; i >= start; i-- {
		event := ordered[i]
		vm.Recent = append(vm.Recent, DiagnosticEventRow{
			Sequence:  event.Sequence,
			Operation: event.Operation,
			Stage:     event.Stage,
			Status:    event.Status,
			Message:   event.Message,
			At:        event.At,
		})
	}

	return vm
}

func severityFromStatus(status OperationStatus) Severity {
	switch status {
	case OperationFailed:
		return SeverityError
	case OperationCanceled:
		return SeverityWarn
	default:
		return SeverityInfo
	}
}
