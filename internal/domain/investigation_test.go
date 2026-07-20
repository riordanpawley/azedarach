package domain

import "testing"

func TestEvaluateInvestigationAcceptance(t *testing.T) {
	task := Task{Type: TypeInvestigation}
	declaration := IssueObservationEvent{ID: 1, Type: IssueEventInvestigationDisposition, Payload: map[string]any{"disposition": "internal_review"}}
	accepted := IssueObservationEvent{ID: 3, Type: IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"outcome": "accepted", "actor_id": "reviewer", "actor_kind": ReviewerOwnerKindOrchestrator}}
	returned := IssueObservationEvent{ID: 4, Type: IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-return", Payload: map[string]any{"outcome": "returned", "actor_id": "reviewer", "actor_kind": ReviewerOwnerKindOrchestrator}}
	reviewEpoch := IssueObservationEvent{ID: 2, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}}
	newReviewEpoch := IssueObservationEvent{ID: 5, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}}
	tests := []struct {
		name   string
		events []IssueObservationEvent
		want   bool
	}{
		{name: "legacy defaults human gated"},
		{name: "human accepted", events: []IssueObservationEvent{reviewEpoch, {ID: 3, Type: IssueEventHumanInputProvided, Payload: map[string]any{"investigation_findings_accepted": true}}}, want: true},
		{name: "stale human acceptance", events: []IssueObservationEvent{{ID: 1, Type: IssueEventHumanInputProvided, Payload: map[string]any{"investigation_findings_accepted": true}}, reviewEpoch}},
		{name: "internal reviewer accepted", events: []IssueObservationEvent{declaration, reviewEpoch, accepted}, want: true},
		{name: "stale internal reviewer acceptance", events: []IssueObservationEvent{declaration, accepted, newReviewEpoch}},
		{name: "manual accepted event is not trusted", events: []IssueObservationEvent{declaration, {Type: IssueEventReviewCompleted, Payload: map[string]any{"outcome": "accepted", "actor_id": "reviewer"}}}},
		{name: "returned findings unresolved", events: []IssueObservationEvent{declaration, accepted, returned}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateInvestigationAcceptance(task, tt.events)
			if got.Accepted != tt.want {
				t.Fatalf("accepted = %v, want %v; result=%+v", got.Accepted, tt.want, got)
			}
		})
	}
}

func TestHasInternalReviewArtifactDoesNotGrantAcceptance(t *testing.T) {
	task := Task{Type: TypeInvestigation}
	declaration := IssueObservationEvent{ID: 1, Type: IssueEventInvestigationDisposition, Payload: map[string]any{"disposition": "internal_review"}}
	artifact := IssueObservationEvent{ID: 2, Type: IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "summary": "consumed findings"}}
	events := []IssueObservationEvent{declaration, artifact}
	if !HasInternalReviewArtifact(task, events) {
		t.Fatal("structured internal review artifact was not detected")
	}
	if EvaluateInvestigationAcceptance(task, events).Accepted {
		t.Fatal("untrusted review artifact granted terminal acceptance")
	}
	if HasInternalReviewArtifact(task, append(events, IssueObservationEvent{ID: 3, Type: IssueEventInvestigationDisposition, Payload: map[string]any{"disposition": "human_findings"}})) {
		t.Fatal("superseded internal review artifact remained eligible")
	}
	reviewEpoch := IssueObservationEvent{ID: 3, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}}
	if HasInternalReviewArtifact(task, append(events, reviewEpoch)) {
		t.Fatal("pre-epoch internal review artifact remained eligible")
	}
	if !HasInternalReviewArtifact(task, append(events, reviewEpoch, IssueObservationEvent{ID: 4, Type: IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "ratified"}})) {
		t.Fatal("same-epoch internal review artifact was not eligible")
	}
}

func TestTrustedReviewOutcomeRequiresCommandOutcomePair(t *testing.T) {
	tests := []struct {
		name    string
		command string
		outcome string
		kind    string
		want    bool
	}{
		{name: "accepted", command: "review-accept", outcome: "accepted", kind: ReviewerOwnerKindOrchestrator, want: true},
		{name: "accept integration failed", command: "review-accept", outcome: "integration_failed", kind: ReviewerOwnerKindOrchestrator, want: true},
		{name: "returned", command: "review-return", outcome: "returned", kind: ReviewerOwnerKindOrchestrator, want: true},
		{name: "legacy untyped actor", command: "review-accept", outcome: "accepted"},
		{name: "same id wrong actor kind", command: "review-accept", outcome: "accepted", kind: "agent"},
		{name: "return cannot accept", command: "review-return", outcome: "accepted", kind: ReviewerOwnerKindOrchestrator},
		{name: "accept cannot return", command: "review-accept", outcome: "returned", kind: ReviewerOwnerKindOrchestrator},
		{name: "return cannot record integration failure", command: "review-return", outcome: "integration_failed", kind: ReviewerOwnerKindOrchestrator},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := IssueObservationEvent{
				Type:          IssueEventReviewCompleted,
				Source:        "daemon-orchestration",
				SourceCommand: tt.command,
				Payload:       map[string]any{"outcome": tt.outcome, "actor_id": "reviewer", "actor_kind": tt.kind},
			}
			_, got := TrustedReviewOutcome(event)
			if got != tt.want {
				t.Fatalf("trusted = %v, want %v", got, tt.want)
			}
		})
	}
}
