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

func TestTrustedReviewOutcomeRequiresCommandOutcomePair(t *testing.T) {
	tests := []struct {
		name    string
		command string
		outcome string
		want    bool
	}{
		{name: "accepted", command: "review-accept", outcome: "accepted", want: true},
		{name: "accept integration failed", command: "review-accept", outcome: "integration_failed", want: true},
		{name: "returned", command: "review-return", outcome: "returned", want: true},
		{name: "return cannot accept", command: "review-return", outcome: "accepted"},
		{name: "accept cannot return", command: "review-accept", outcome: "returned"},
		{name: "return cannot record integration failure", command: "review-return", outcome: "integration_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := IssueObservationEvent{
				Type:          IssueEventReviewCompleted,
				Source:        "daemon-orchestration",
				SourceCommand: tt.command,
				Payload:       map[string]any{"outcome": tt.outcome, "actor_id": "reviewer"},
			}
			_, got := TrustedReviewOutcome(event)
			if got != tt.want {
				t.Fatalf("trusted = %v, want %v", got, tt.want)
			}
		})
	}
}
