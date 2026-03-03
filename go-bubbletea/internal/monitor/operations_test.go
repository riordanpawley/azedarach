package monitor

import (
	"testing"
	"time"
)

func TestTimelineDeterministicSequenceAndCapacity(t *testing.T) {
	timeline := NewTimeline(3)
	base := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)

	timeline.Record(base.Add(3*time.Second), "sync", "queue", OperationQueued, "queued")
	timeline.Record(base.Add(1*time.Second), "sync", "run", OperationRunning, "running")
	timeline.Record(base.Add(2*time.Second), "sync", "done", OperationSucceeded, "done")
	timeline.Record(base.Add(4*time.Second), "build", "run", OperationFailed, "failed")

	events := timeline.Events()
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}

	if events[0].Sequence != 2 || events[1].Sequence != 3 || events[2].Sequence != 4 {
		t.Fatalf("unexpected sequence order: %+v", []int64{events[0].Sequence, events[1].Sequence, events[2].Sequence})
	}

	if events[2].Severity != SeverityError {
		t.Fatalf("events[2].Severity = %s, want %s", events[2].Severity, SeverityError)
	}
}

func TestNotificationPolicyEvaluate(t *testing.T) {
	policy := DefaultNotificationPolicy()

	tests := []struct {
		name       string
		transition OperationTransition
		wantNotify bool
		wantLevel  Severity
	}{
		{
			name: "running is suppressed by default",
			transition: OperationTransition{
				Operation: "sync",
				From:      OperationQueued,
				To:        OperationRunning,
			},
			wantNotify: false,
			wantLevel:  SeverityInfo,
		},
		{
			name: "success notifies",
			transition: OperationTransition{
				Operation: "sync",
				From:      OperationRunning,
				To:        OperationSucceeded,
			},
			wantNotify: true,
			wantLevel:  SeverityInfo,
		},
		{
			name: "failure notifies as error",
			transition: OperationTransition{
				Operation: "build",
				From:      OperationRunning,
				To:        OperationFailed,
			},
			wantNotify: true,
			wantLevel:  SeverityError,
		},
		{
			name: "same state never notifies",
			transition: OperationTransition{
				Operation: "build",
				From:      OperationFailed,
				To:        OperationFailed,
			},
			wantNotify: false,
			wantLevel:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := policy.Evaluate(tt.transition)
			if decision.Notify != tt.wantNotify {
				t.Fatalf("Notify = %v, want %v", decision.Notify, tt.wantNotify)
			}
			if decision.Level != tt.wantLevel {
				t.Fatalf("Level = %q, want %q", decision.Level, tt.wantLevel)
			}
		})
	}
}

func TestBuildDiagnosticsViewModelDeterministic(t *testing.T) {
	now := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
	events := []OperationEvent{
		{Sequence: 3, Operation: "sync", Stage: "run", Status: OperationRunning, Message: "running", At: now.Add(-3 * time.Second)},
		{Sequence: 1, Operation: "build", Stage: "queue", Status: OperationQueued, Message: "queued", At: now.Add(-5 * time.Second)},
		{Sequence: 4, Operation: "sync", Stage: "done", Status: OperationSucceeded, Message: "done", At: now.Add(-1 * time.Second)},
		{Sequence: 2, Operation: "build", Stage: "run", Status: OperationFailed, Message: "failed", At: now.Add(-4 * time.Second)},
	}

	vm := BuildDiagnosticsViewModel(now, events, 2)

	if vm.Succeeded != 1 || vm.Failed != 1 {
		t.Fatalf("unexpected status counts: succeeded=%d failed=%d", vm.Succeeded, vm.Failed)
	}

	if vm.TotalEvents != 4 {
		t.Fatalf("TotalEvents = %d, want 4", vm.TotalEvents)
	}

	if len(vm.Recent) != 2 {
		t.Fatalf("len(Recent) = %d, want 2", len(vm.Recent))
	}

	if vm.Recent[0].Sequence != 4 || vm.Recent[1].Sequence != 3 {
		t.Fatalf("recent sequence order = [%d %d], want [4 3]", vm.Recent[0].Sequence, vm.Recent[1].Sequence)
	}
}
