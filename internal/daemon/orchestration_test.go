package daemon

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
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

func TestHybridActiveSessionCountRejectsProjectionTmuxDivergence(t *testing.T) {
	identity, err := domain.NewOrchestratorIdentity("project", domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	lease := daemonstate.OrchestratorScopeLease{Identity: identity, SessionID: "orchestrator"}
	issues := map[string]struct{}{"worker": {}}
	tests := []struct {
		name      string
		projected []daemonstate.Session
		live      []string
		want      int
	}{
		{name: "projection only", projected: []daemonstate.Session{{ID: "worker", IssueID: "worker", State: daemonstate.SessionStateRunning}}, want: 1},
		{name: "tmux only", live: []string{"worker"}, want: 1},
		{name: "both count once", projected: []daemonstate.Session{{ID: "worker", IssueID: "worker", State: daemonstate.SessionStateRunning}}, live: []string{"worker"}, want: 1},
		{name: "own orchestrator excluded", live: []string{"orchestrator"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hybridActiveSessionCount(lease, tt.projected, tt.live, issues); got != tt.want {
				t.Fatalf("active = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestOrchestratorWakeReasonPrioritizesActionableEvents(t *testing.T) {
	updated := time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)
	if got := orchestratorWakeReason(domain.OrchestratorLifecycleFacts{ReviewRequests: 1, OpenIssues: 1}, updated, updated.Add(-time.Second)); got != domain.OrchestratorWakeReviewRequest {
		t.Fatalf("review wake = %q", got)
	}
	if got := orchestratorWakeReason(domain.OrchestratorLifecycleFacts{OpenIssues: 1}, updated, updated.Add(-time.Second)); got != domain.OrchestratorWakeOpenWork {
		t.Fatalf("work wake = %q", got)
	}
	if got := orchestratorWakeReason(domain.OrchestratorLifecycleFacts{}, updated, updated.Add(-time.Second)); got != domain.OrchestratorWakeHumanAnswer {
		t.Fatalf("answer wake = %q", got)
	}
	if got := orchestratorWakeReason(domain.OrchestratorLifecycleFacts{}, updated, updated); got != "" {
		t.Fatalf("unchanged wake = %q", got)
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
