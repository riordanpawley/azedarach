package linearsync

import (
	"reflect"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestNormalizeBootstrapProjectPolicyInput(t *testing.T) {
	policy := NormalizeBootstrapProjectPolicyInput([]config.Project{
		{Name: " beta ", Path: " /tmp/projects/z/ "},
		{Name: "alpha", Path: "/tmp/projects/a"},
		{Name: "", Path: "/tmp/projects/ignore"},
		{Name: "gamma", Path: ""},
		{Name: " ", Path: " "},
	})

	wantCandidates := []BootstrapProjectCandidate{
		{Name: "alpha", Path: "/tmp/projects/a"},
		{Name: "beta", Path: "/tmp/projects/z"},
	}
	if !reflect.DeepEqual(policy.Candidates, wantCandidates) {
		t.Fatalf("candidates = %#v, want %#v", policy.Candidates, wantCandidates)
	}

	if got, want := len(policy.Exclusions), 3; got != want {
		t.Fatalf("exclusions = %d, want %d", got, want)
	}

	reasons := []string{
		policy.Exclusions[0].Reason,
		policy.Exclusions[1].Reason,
		policy.Exclusions[2].Reason,
	}
	wantReasons := []string{
		"missing project name",
		"missing project path",
		"missing project name and path",
	}
	if !reflect.DeepEqual(reasons, wantReasons) {
		t.Fatalf("exclusion reasons = %#v, want %#v", reasons, wantReasons)
	}
}

func TestSelectBootstrapProjectSetIsDeterministic(t *testing.T) {
	inputA := []config.Project{
		{Name: "beta", Path: "/tmp/projects/b"},
		{Name: "alpha", Path: "/tmp/projects/a"},
		{Name: "beta-copy", Path: "/tmp/projects/b"},
		{Name: "alpha", Path: "/tmp/projects/c"},
	}
	inputB := []config.Project{
		{Name: "alpha", Path: "/tmp/projects/c"},
		{Name: "beta-copy", Path: "/tmp/projects/b"},
		{Name: "alpha", Path: "/tmp/projects/a"},
		{Name: "beta", Path: "/tmp/projects/b"},
	}

	selectionA := SelectBootstrapProjectSet(NormalizeBootstrapProjectPolicyInput(inputA))
	selectionB := SelectBootstrapProjectSet(NormalizeBootstrapProjectPolicyInput(inputB))

	wantProjects := []BootstrapProjectCandidate{
		{Name: "alpha", Path: "/tmp/projects/a"},
		{Name: "beta", Path: "/tmp/projects/b"},
	}
	if !reflect.DeepEqual(selectionA.Projects, wantProjects) {
		t.Fatalf("selected projects = %#v, want %#v", selectionA.Projects, wantProjects)
	}
	if !reflect.DeepEqual(selectionA.Projects, selectionB.Projects) {
		t.Fatalf("selectionA projects = %#v, selectionB projects = %#v", selectionA.Projects, selectionB.Projects)
	}

	wantExclusions := []BootstrapProjectExclusion{
		{Candidate: BootstrapProjectCandidate{Name: "beta-copy", Path: "/tmp/projects/b"}, Reason: "duplicate project path"},
		{Candidate: BootstrapProjectCandidate{Name: "alpha", Path: "/tmp/projects/c"}, Reason: "duplicate project name"},
	}
	if !reflect.DeepEqual(selectionA.Exclusions, wantExclusions) {
		t.Fatalf("selection exclusions = %#v, want %#v", selectionA.Exclusions, wantExclusions)
	}
	if !reflect.DeepEqual(selectionA.Exclusions, selectionB.Exclusions) {
		t.Fatalf("selectionA exclusions = %#v, selectionB exclusions = %#v", selectionA.Exclusions, selectionB.Exclusions)
	}
}

func TestBootstrapProjectSnapshotString(t *testing.T) {
	policy := NormalizeBootstrapProjectPolicyInput([]config.Project{
		{Name: "alpha", Path: "/tmp/projects/a"},
		{Name: "beta", Path: "/tmp/projects/b"},
		{Name: "beta-copy", Path: "/tmp/projects/b"},
		{Name: "", Path: "/tmp/projects/ignore"},
	})
	selection := SelectBootstrapProjectSet(policy)
	snapshot := NewBootstrapProjectSnapshot(policy, selection)

	out := snapshot.String()
	for _, want := range []string{
		"bootstrap project set",
		"candidates: 3",
		"candidate: name=alpha path=/tmp/projects/a",
		"candidate: name=beta path=/tmp/projects/b",
		"normalization exclusions:",
		"reason=missing project name",
		"selected: 2",
		"selection exclusions:",
		"reason=duplicate project path",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("snapshot output missing %q:\n%s", want, out)
		}
	}
}
