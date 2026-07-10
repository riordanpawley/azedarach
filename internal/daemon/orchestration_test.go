package daemon

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestMergeOrchestrationSnapshotOrdersProjectResultsDeterministically(t *testing.T) {
	dst := protocol.OrchestrationSnapshot{Blocked: map[string]string{}, Runnable: []string{"z"}, Active: []string{"c"}}
	mergeOrchestrationSnapshot(&dst, protocol.OrchestrationSnapshot{
		Runnable:       []string{"a"},
		Active:         []string{"b"},
		Blocked:        map[string]string{"x": "dependency"},
		Capacity:       protocol.OrchestrationCapacity{DirectRunnableCount: 1, DirectActiveCount: 1},
		ActiveSessions: []protocol.OrchestrationSession{{IssueID: "z"}, {IssueID: "a"}},
	})
	sortOrchestrationSnapshot(&dst)
	if !reflect.DeepEqual(dst.Runnable, []string{"a", "z"}) || !reflect.DeepEqual(dst.Active, []string{"b", "c"}) {
		t.Fatalf("project ordering = runnable %v active %v", dst.Runnable, dst.Active)
	}
	if got := []string{dst.ActiveSessions[0].IssueID, dst.ActiveSessions[1].IssueID}; !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("session ordering = %v", got)
	}
	if dst.Blocked["x"] != "dependency" || dst.Capacity.DirectRunnableCount != 1 || dst.Capacity.DirectActiveCount != 1 {
		t.Fatalf("merged project snapshot = %+v", dst)
	}
}

func TestSortOrchestrationSnapshotUsesPriorityAgeAndID(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	tasks := map[string]domain.Task{
		"p2":  {ID: "p2", Priority: domain.P2, UpdatedAt: old},
		"b":   {ID: "b", Priority: domain.P1, UpdatedAt: newer},
		"a":   {ID: "a", Priority: domain.P1, UpdatedAt: newer},
		"old": {ID: "old", Priority: domain.P1, UpdatedAt: old},
	}
	snapshot := protocol.OrchestrationSnapshot{Runnable: []string{"p2", "b", "a", "old"}}
	sortOrchestrationSnapshot(&snapshot, tasks)
	if want := []string{"old", "a", "b", "p2"}; !reflect.DeepEqual(snapshot.Runnable, want) {
		t.Fatalf("runnable order = %v, want %v", snapshot.Runnable, want)
	}
}

func TestOrchestrationIntentRejectsInvalidShapeBeforeMutation(t *testing.T) {
	authority := daemonOrchestrationAuthority{daemon: &Daemon{}}
	_, err := authority.Apply(context.Background(), "project", protocol.OrchestrationIntentRequest{
		Scope:     domain.ProjectOrchestrationScope(),
		Kind:      "delete-everything",
		IntentKey: "intent-1",
	})
	if err == nil {
		t.Fatal("invalid intent unexpectedly succeeded")
	}
}

func TestOrchestrationAuthorityInterfaceStaysDeep(t *testing.T) {
	var _ orchestrationAuthority = daemonOrchestrationAuthority{}
}

func TestOrchestrationSkipReasonPreservesNestedRootAuthority(t *testing.T) {
	nested := map[string]struct{}{"az-nested": {}}
	active := map[string]struct{}{"az-active": {}}
	blocked := map[string]string{"az-blocked": "dependency remains open"}

	if got := orchestrationSkipReason("az-nested", nested, active, blocked); got != "nested-root-start-orchestrator-session: az session start az-nested" {
		t.Fatalf("nested skip reason = %q", got)
	}
	if got := orchestrationSkipReason("az-active", nested, active, blocked); got != "session-already-running" {
		t.Fatalf("active skip reason = %q", got)
	}
	if got := orchestrationSkipReason("az-blocked", nested, active, blocked); got != "dependency remains open" {
		t.Fatalf("blocked skip reason = %q", got)
	}
}

func TestOrchestrationBoardHealthDiagnosesUnsafeProject(t *testing.T) {
	parent := domain.Task{ID: "parent", Status: domain.StatusOpen}
	missing := domain.Task{ID: "missing-child", Status: domain.StatusOpen, ParentID: issueIDPtr("absent")}
	malformedOwner := domain.Task{ID: "owned", Status: domain.StatusOpen, Ownership: &domain.IssueOwnership{OwnerKind: "agent"}}
	tasks := []domain.Task{parent, missing, malformedOwner}
	health := orchestrationBoardHealth(tasks, map[string]domain.Task{"parent": parent, "missing-child": missing, "owned": malformedOwner}, 2, 2)
	if health.Healthy {
		t.Fatal("unsafe board reported healthy")
	}
	if health.OpenIssueCount != 3 || health.InspectLimit != 2 || health.OpenIssueLimit != 2 {
		t.Fatalf("health counts = %+v", health)
	}
	joined := strings.Join(health.Diagnostics, "\n")
	for _, want := range []string{"missing parent absent", "incomplete owner identity", "exceeds refusal threshold"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostics %q missing %q", joined, want)
		}
	}
}

func TestOrchestrationCandidateOrderingIsStable(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tasks := map[string]domain.Task{
		"excluded": {ID: "excluded", Priority: domain.P1, UpdatedAt: old},
		"b":        {ID: "b", Priority: domain.P1, UpdatedAt: old},
		"a":        {ID: "a", Priority: domain.P1, UpdatedAt: old},
		"p2":       {ID: "p2", Priority: domain.P2, UpdatedAt: old},
	}
	candidates := []protocol.OrchestrationCandidate{{IssueID: "p2", Included: true}, {IssueID: "excluded"}, {IssueID: "b", Included: true}, {IssueID: "a", Included: true}}
	sort.SliceStable(candidates, func(i, j int) bool { return orchestrationCandidateLess(candidates[i], candidates[j], tasks) })
	got := []string{candidates[0].IssueID, candidates[1].IssueID, candidates[2].IssueID, candidates[3].IssueID}
	if want := []string{"a", "b", "excluded", "p2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestStableRequestedCandidatesFollowsPolicyOrder(t *testing.T) {
	got := stableRequestedCandidates([]string{"p2", "unknown", "p1", "p2"}, []string{"p1", "p2", "p3"})
	if want := []string{"p1", "p2", "unknown"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requested = %v, want %v", got, want)
	}
}

func TestBoardHealthOverrideOnlyAllowsOpenIssueThreshold(t *testing.T) {
	if !orchestrationHealthOverrideAllowed(protocol.OrchestrationHealth{Diagnostics: []string{"open issue count 101 exceeds refusal threshold 100"}}) {
		t.Fatal("threshold override rejected")
	}
	if orchestrationHealthOverrideAllowed(protocol.OrchestrationHealth{Diagnostics: []string{"malformed graph: x has missing parent y"}}) {
		t.Fatal("malformed graph override allowed")
	}
}

func TestCandidateOwnershipAllowsSameActor(t *testing.T) {
	now := time.Now().UTC()
	task := domain.Task{ID: "x", Ownership: &domain.IssueOwnership{OwnerID: "worker", OwnerKind: "agent"}}
	if got := orchestrationCandidateForTask(task, "worker", now, nil); got.Classification == "owned-elsewhere" {
		t.Fatalf("same actor excluded: %+v", got)
	}
	if got := orchestrationCandidateForTask(task, "other", now, nil); got.Classification != "owned-elsewhere" {
		t.Fatalf("other actor not excluded: %+v", got)
	}
}

func TestOrchestrationGlobalActiveCountIncludesUninspectedRoots(t *testing.T) {
	tasks := []domain.Task{{ID: "visible", HasTmuxSession: true}, {ID: "outside-limit", HasTmuxSession: true}, {ID: "inactive"}}
	if got := orchestrationGlobalActiveCount(tasks); got != 2 {
		t.Fatalf("active count = %d, want 2", got)
	}
}

func issueIDPtr(value string) *naming.IssueID { id := naming.IssueID(value); return &id }
