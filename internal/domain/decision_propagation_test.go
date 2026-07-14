package domain

import (
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestReducePendingDecisionChangesRequiresExactLatestRevision(t *testing.T) {
	now := time.Now().UTC()
	events := []IssueObservationEvent{
		{ID: 1, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4)}},
		{ID: 2, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionAcknowledged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4), "disposition": "compatible"}},
		{ID: 3, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(5), "title": "Protocol v2"}},
	}
	pending := ReducePendingDecisionChanges(events)
	if len(pending) != 1 || pending[0].DecisionID != "dec-1" || pending[0].Revision != 5 {
		t.Fatalf("pending = %+v, want dec-1 revision 5", pending)
	}
}

func TestReducePendingDecisionChangesReplacementSuppressesSupersededDecision(t *testing.T) {
	now := time.Now().UTC()
	events := []IssueObservationEvent{
		{ID: 1, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4)}},
		{ID: 2, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-2", "revision": int64(6), "supersedes_decision_id": "dec-1"}},
	}
	pending := ReducePendingDecisionChanges(events)
	if len(pending) != 1 || pending[0].DecisionID != "dec-2" {
		t.Fatalf("pending = %+v, want only replacement", pending)
	}
}

func TestReducePendingDecisionChangesWithdrawalReactivatesSupersededDecision(t *testing.T) {
	now := time.Now().UTC()
	events := []IssueObservationEvent{
		{ID: 1, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4)}},
		{ID: 2, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-2", "revision": int64(6), "supersedes_decision_id": "dec-1"}},
		{ID: 3, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-2", "revision": int64(7), "withdrawn": true}},
	}
	pending := ReducePendingDecisionChanges(events)
	if len(pending) != 1 || pending[0].DecisionID != "dec-1" {
		t.Fatalf("pending = %+v, want predecessor reactivated", pending)
	}
}

func TestReducePendingDecisionChangesIgnoresMalformedAndAcceptsExactAcknowledgement(t *testing.T) {
	now := time.Now().UTC()
	events := []IssueObservationEvent{
		{ID: 1, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4)}},
		{ID: 2, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionAcknowledged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4), "disposition": "seen"}},
		{ID: 3, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionAcknowledged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4), "disposition": "reconciled"}},
	}
	if pending := ReducePendingDecisionChanges(events); len(pending) != 0 {
		t.Fatalf("pending = %+v, want none", pending)
	}
}

func TestReducePendingDecisionChangesUsesDurableEventOrderDespiteClockSkew(t *testing.T) {
	now := time.Now().UTC()
	events := []IssueObservationEvent{
		{ID: 1, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionChanged, ObservedAt: now.Add(time.Minute), Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4)}},
		{ID: 2, IssueID: naming.IssueID("worker"), Type: IssueEventDecisionAcknowledged, ObservedAt: now, Payload: map[string]any{"decision_id": "dec-1", "revision": int64(4), "disposition": "reconciled"}},
	}
	if pending := ReducePendingDecisionChanges(events); len(pending) != 0 {
		t.Fatalf("pending = %+v, want acknowledgement ordered after change by event id", pending)
	}
}
