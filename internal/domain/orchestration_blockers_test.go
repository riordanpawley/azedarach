package domain

import (
	"reflect"
	"testing"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestUnresolvedBlockersPreservesMissingAndNonterminalDependencies(t *testing.T) {
	blocked := Task{Dependencies: []Dependency{
		{ID: naming.IssueID("az-settled"), Type: DependencyBlocks},
		{ID: naming.IssueID("az-active"), Type: DependencyBlocks},
		{ID: naming.IssueID("az-missing"), Type: DependencyBlocks},
		{ID: naming.IssueID("az-related"), Type: DependencyRelatedTo},
	}}
	byID := map[naming.IssueID]Task{
		"az-settled": {ID: "az-settled", Status: StatusDone},
		"az-active":  {ID: "az-active", Status: StatusInProgress},
	}

	want := []string{"az-active", "az-missing(missing)"}
	if got := UnresolvedBlockers(blocked, byID); !reflect.DeepEqual(got, want) {
		t.Fatalf("UnresolvedBlockers() = %v, want %v", got, want)
	}
}

func TestAssessRootedOrchestrationAdmissionInheritsAncestorBlockers(t *testing.T) {
	rootID := naming.IssueID("az-root")
	nestedID := naming.IssueID("az-nested")
	blockerID := naming.IssueID("az-blocker")
	byID := map[naming.IssueID]Task{
		rootID:    {ID: rootID, Type: TypeEpic, Status: StatusOpen, Dependencies: []Dependency{{ID: blockerID, Type: DependencyBlocks}}},
		nestedID:  {ID: nestedID, Type: TypeEpic, Status: StatusOpen, ParentID: &rootID},
		blockerID: {ID: blockerID, Type: TypeTask, Status: StatusInProgress},
	}

	admission, err := AssessRootedOrchestrationAdmission(nestedID, byID)
	if err != nil {
		t.Fatal(err)
	}
	if admission.RequestedRootID != nestedID || admission.BlockingRootID != rootID || !reflect.DeepEqual(admission.Blockers, []string{blockerID.String()}) {
		t.Fatalf("admission = %+v, want requested=%s root=%s blockers=[%s]", admission, nestedID, rootID, blockerID)
	}

	byID[blockerID] = Task{ID: blockerID, Type: TypeTask, Status: StatusDone}
	admission, err = AssessRootedOrchestrationAdmission(nestedID, byID)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Blocked() {
		t.Fatalf("settled admission = %+v, want unblocked", admission)
	}
}
