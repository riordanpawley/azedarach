package domain

import "testing"

func TestEvaluateInvestigationAcceptance(t *testing.T) {
	task := Task{Type: TypeInvestigation}
	declaration := IssueObservationEvent{Type: IssueEventInvestigationDisposition, Payload: map[string]any{"disposition": "internal_review"}}
	accepted := IssueObservationEvent{Type: IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"outcome": "accepted", "actor_id": "reviewer"}}
	returned := IssueObservationEvent{Type: IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-return", Payload: map[string]any{"outcome": "returned", "actor_id": "reviewer"}}
	tests := []struct {
		name   string
		events []IssueObservationEvent
		want   bool
	}{
		{name: "legacy defaults human gated"},
		{name: "human accepted", events: []IssueObservationEvent{{Type: IssueEventHumanInputProvided, Payload: map[string]any{"investigation_findings_accepted": true}}}, want: true},
		{name: "internal reviewer accepted", events: []IssueObservationEvent{declaration, accepted}, want: true},
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
}
