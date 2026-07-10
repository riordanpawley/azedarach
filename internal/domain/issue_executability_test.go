package domain

import (
	"reflect"
	"testing"
)

func TestAssessIssueExecutability(t *testing.T) {
	tests := []struct {
		name     string
		task     Task
		blockers []string
		proposal IssueContractProposal
		want     IssueExecutabilityDisposition
	}{
		{name: "complete", task: Task{Description: "Scope", Acceptance: "Done when tested"}, want: IssueExecutable},
		{name: "missing contract", task: Task{}, want: IssuePremature},
		{name: "blocked", task: Task{Description: "Scope", Acceptance: "Done"}, blockers: []string{"dep-b", "dep-a"}, want: IssuePremature},
		{name: "safe enrichment", task: Task{}, proposal: IssueContractProposal{Description: "Observed scope", Acceptance: "Observed check", Evidence: []string{"spec:req-1"}}, want: IssueNeedsEnrichment},
		{name: "safe decomposition", task: Task{Description: "Broad scope", Acceptance: "All children pass"}, proposal: IssueContractProposal{Evidence: []string{"graph:parent"}, Children: []IssueChildProposal{{Title: "Child", Description: "Bounded scope", Acceptance: "Focused test passes"}}}, want: IssueNeedsDecomposition},
		{name: "unknown", task: Task{Description: "Scope", Acceptance: "Done"}, proposal: IssueContractProposal{MaterialUnknowns: []string{"choose retention"}}, want: IssueNeedsInteraction},
		{name: "judgment suppresses proposed mutation", task: Task{}, proposal: IssueContractProposal{Description: "Guess", Evidence: []string{"nearby issue"}, RequiresProductJudgment: true}, want: IssueNeedsInteraction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessIssueExecutability(tt.task, tt.blockers, tt.proposal)
			if got.Disposition != tt.want {
				t.Fatalf("disposition = %q, want %q; assessment=%+v", got.Disposition, tt.want, got)
			}
			if tt.want == IssueNeedsInteraction && (len(got.Changes) != 0 || len(got.Children) != 0) {
				t.Fatalf("interaction assessment proposed unsafe changes: %+v", got)
			}
		})
	}
}

func TestAssessIssueExecutabilityChangesAreAdditiveAndAuditable(t *testing.T) {
	got := AssessIssueExecutability(Task{Description: "Existing scope"}, nil, IssueContractProposal{
		Description: "Replacement must be ignored",
		Acceptance:  "Focused test passes",
		Evidence:    []string{" spec:req-b ", "spec:req-a", "spec:req-a"},
	})
	if got.Disposition != IssueNeedsEnrichment || len(got.Changes) != 1 || got.Changes[0].Field != "acceptance" {
		t.Fatalf("assessment = %+v", got)
	}
	if !reflect.DeepEqual(got.Changes[0].Evidence, []string{"spec:req-a", "spec:req-b"}) {
		t.Fatalf("evidence = %#v", got.Changes[0].Evidence)
	}
}

func TestAssessIssueExecutabilityReasonsAreStable(t *testing.T) {
	got := AssessIssueExecutability(Task{}, []string{"z", "a"}, IssueContractProposal{MaterialUnknowns: []string{"y", "b"}})
	want := []string{"missing-scope", "missing-acceptance", "blocked-by:a", "blocked-by:z", "material-unknown:b", "material-unknown:y"}
	if !reflect.DeepEqual(got.Reasons, want) {
		t.Fatalf("reasons = %#v, want %#v", got.Reasons, want)
	}
}

func TestAssessIssueExecutabilitySortsCompleteChildContracts(t *testing.T) {
	got := AssessIssueExecutability(Task{Description: "Scope", Acceptance: "Done"}, nil, IssueContractProposal{
		Evidence: []string{"spec:req"},
		Children: []IssueChildProposal{
			{Title: " Z ", Description: " Second ", Acceptance: " Done "},
			{Title: "A", Description: "First", Acceptance: "Done"},
		},
	})
	if got.Disposition != IssueNeedsDecomposition || got.Children[0].Title != "A" {
		t.Fatalf("assessment = %+v", got)
	}
}
