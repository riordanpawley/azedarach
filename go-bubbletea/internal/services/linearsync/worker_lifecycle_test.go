package linearsync

import (
	"strings"
	"testing"
	"time"
)

func TestProjectWorkerLifecycleTransitionsAreProjectScoped(t *testing.T) {
	t.Parallel()

	left, err := NewProjectWorkerLifecycle("proj-a")
	if err != nil {
		t.Fatalf("NewProjectWorkerLifecycle(left) error = %v", err)
	}
	right, err := NewProjectWorkerLifecycle("proj-b")
	if err != nil {
		t.Fatalf("NewProjectWorkerLifecycle(right) error = %v", err)
	}

	left, err = left.Transition(WorkerTriggerHealthy)
	if err != nil {
		t.Fatalf("left healthy transition error = %v", err)
	}
	right, err = right.Transition(WorkerTriggerDegraded)
	if err != nil {
		t.Fatalf("right degraded transition error = %v", err)
	}
	right, err = right.Transition(WorkerTriggerRetrying)
	if err != nil {
		t.Fatalf("right retrying transition error = %v", err)
	}

	if got, want := left.ProjectID, "proj-a"; got != want {
		t.Fatalf("left ProjectID = %q, want %q", got, want)
	}
	if got, want := left.State, WorkerStateHealthy; got != want {
		t.Fatalf("left State = %q, want %q", got, want)
	}
	if got, want := right.ProjectID, "proj-b"; got != want {
		t.Fatalf("right ProjectID = %q, want %q", got, want)
	}
	if got, want := right.State, WorkerStateRetrying; got != want {
		t.Fatalf("right State = %q, want %q", got, want)
	}
}

func TestProjectWorkerLifecycleRejectsInvalidTransition(t *testing.T) {
	t.Parallel()

	lifecycle, err := NewProjectWorkerLifecycle("proj-a")
	if err != nil {
		t.Fatalf("NewProjectWorkerLifecycle() error = %v", err)
	}

	lifecycle, err = lifecycle.Transition(WorkerTriggerStopped)
	if err != nil {
		t.Fatalf("stop transition error = %v", err)
	}

	if _, err := lifecycle.Transition(WorkerTriggerRetrying); err == nil {
		t.Fatal("Transition(stopped, retrying) should fail")
	}
}

func TestRetryPolicyBoundsRetryAndBackoff(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 5 * time.Second, MaxExponent: 8}

	if !policy.CanRetry(0) {
		t.Fatal("attempt 0 should be retryable")
	}
	if !policy.CanRetry(1) {
		t.Fatal("attempt 1 should be retryable")
	}
	if policy.CanRetry(2) {
		t.Fatal("attempt 2 should not be retryable with max attempts 3")
	}

	if got, want := policy.DelayForAttempt(0), 5*time.Second; got != want {
		t.Fatalf("DelayForAttempt(0) = %s, want %s", got, want)
	}
	if got, want := policy.DelayForAttempt(3), 40*time.Second; got != want {
		t.Fatalf("DelayForAttempt(3) = %s, want %s", got, want)
	}
	if got, want := policy.DelayForAttempt(9), 1280*time.Second; got != want {
		t.Fatalf("DelayForAttempt(9) = %s, want %s", got, want)
	}
}

func TestResolveProjectFallbackIsProjectScoped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		lifecycle   ProjectWorkerLifecycle
		status      WebhookFallbackStatus
		wantActive  bool
		wantReason  string
		wantState   WorkerState
		wantProject string
	}{
		{
			name: "healthy worker stays on primary transport",
			lifecycle: ProjectWorkerLifecycle{
				ProjectID: "proj-a",
				State:     WorkerStateHealthy,
			},
			status: WebhookFallbackStatus{
				Mode:    "sdk",
				Healthy: true,
				Reason:  "",
			},
			wantActive:  false,
			wantReason:  "remains on primary transport",
			wantState:   WorkerStateHealthy,
			wantProject: "proj-a",
		},
		{
			name: "degraded worker activates project fallback on unhealthy transport",
			lifecycle: ProjectWorkerLifecycle{
				ProjectID: "proj-b",
				State:     WorkerStateDegraded,
			},
			status: WebhookFallbackStatus{
				Mode:    "misconfigured",
				Healthy: false,
				Reason:  "Timed out registering Linear webhook after 4000ms",
			},
			wantActive:  true,
			wantReason:  "Timed out registering Linear webhook after 4000ms",
			wantState:   WorkerStateDegraded,
			wantProject: "proj-b",
		},
		{
			name: "stopped worker retains project fallback",
			lifecycle: ProjectWorkerLifecycle{
				ProjectID: "proj-c",
				State:     WorkerStateStopped,
			},
			status: WebhookFallbackStatus{
				Mode:    "failed",
				Healthy: false,
				Reason:  "Missing LINEAR_WEBHOOK_PUBLIC_URL prevents webhook fallback",
			},
			wantActive:  true,
			wantReason:  "retains fallback while worker is stopped",
			wantState:   WorkerStateStopped,
			wantProject: "proj-c",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision := tt.lifecycle.ResolveFallback(tt.status)

			if got, want := decision.Active, tt.wantActive; got != want {
				t.Fatalf("Active = %v, want %v", got, want)
			}
			if got, want := decision.ProjectID, tt.wantProject; got != want {
				t.Fatalf("ProjectID = %q, want %q", got, want)
			}
			if got, want := decision.State, tt.wantState; got != want {
				t.Fatalf("State = %q, want %q", got, want)
			}
			if got := decision.Reason; got == "" || !strings.Contains(got, tt.wantReason) {
				t.Fatalf("Reason = %q, want substring %q", got, tt.wantReason)
			}
		})
	}
}

func TestNewProjectWorkerLifecycleRejectsBlankProjectID(t *testing.T) {
	t.Parallel()

	if _, err := NewProjectWorkerLifecycle("   "); err == nil {
		t.Fatal("blank project id should fail")
	}
}
