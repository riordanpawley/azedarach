package daemon

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
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
