package domain

import (
	"reflect"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestAssessOrchestrationCandidateClasses(t *testing.T) {
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	backlog, err := NewIssueState(IssueStateParts{Workflow: IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	review, err := NewIssueState(IssueStateParts{Workflow: IssueWorkflowActive, Review: IssueReviewRequested})
	if err != nil {
		t.Fatal(err)
	}
	waiting := &Session{Activity: "waiting_human"}
	tests := []struct {
		name     string
		task     Task
		actor    string
		blockers []string
		want     OrchestrationCandidateClass
	}{
		{name: "open", task: Task{ID: "open", Title: "Open", Description: "Executable", Acceptance: "Done", Status: StatusOpen}, want: OrchestrationCandidateOpen},
		{name: "active", task: Task{ID: "active", Title: "Active", Description: "Executable", Status: StatusInProgress}, want: OrchestrationCandidateActive},
		{name: "review", task: Task{ID: "review", Title: "Review", Description: "Executable", Status: StatusInReview, State: review}, want: OrchestrationCandidateReviewReady},
		{name: "decision", task: Task{ID: "decision", Title: "Decision", Description: "Executable", Status: StatusInProgress, Session: waiting}, want: OrchestrationCandidateDecisionWaiting},
		{name: "backlog", task: Task{ID: "backlog", Title: "Backlog", Description: "Executable", Status: StatusOpen, State: backlog}, want: OrchestrationCandidateBacklog},
		{name: "owned", task: Task{ID: "owned", Title: "Owned", Description: "Executable", Status: StatusOpen, Ownership: &IssueOwnership{OwnerID: "other", OwnerKind: "agent"}}, actor: "self", want: OrchestrationCandidateOwnedElsewhere},
		{name: "blocked", task: Task{ID: "blocked", Title: "Blocked", Description: "Executable", Status: StatusOpen}, blockers: []string{"waiting on dep"}, want: OrchestrationCandidateBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessOrchestrationCandidate(tt.task, tt.actor, now, tt.blockers)
			if got.Classification != tt.want {
				t.Fatalf("classification = %q, want %q", got.Classification, tt.want)
			}
			if tt.want == OrchestrationCandidateOpen && (!got.Eligible || !reflect.DeepEqual(got.Sufficiency, []string{"title-present", "execution-context-present"})) {
				t.Fatalf("open assessment = %+v", got)
			}
			if tt.want != OrchestrationCandidateOpen && got.Eligible {
				t.Fatalf("excluded assessment eligible: %+v", got)
			}
			if tt.want == OrchestrationCandidateDecisionWaiting && got.Executability.Disposition != IssueNeedsInteraction {
				t.Fatalf("decision executability = %+v", got.Executability)
			}
		})
	}
}

func TestAssessOrchestrationCandidateReportsInsufficientContext(t *testing.T) {
	got := AssessOrchestrationCandidate(Task{ID: naming.IssueID("thin"), Status: StatusOpen}, "", time.Now(), nil)
	if !got.Eligible || got.Sufficient || !reflect.DeepEqual(got.Sufficiency, []string{"missing-title", "missing-scope", "missing-acceptance"}) {
		t.Fatalf("assessment = %+v", got)
	}
}
