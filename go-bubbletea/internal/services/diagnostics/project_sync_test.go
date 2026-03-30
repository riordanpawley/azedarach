package diagnostics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/linearsync"
)

func TestProjectSyncDiagnosticsFromLifecycle(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 3, 30, 10, 15, 0, 0, time.UTC)
	errored := time.Date(2026, 3, 30, 10, 20, 0, 0, time.UTC)

	healthyLifecycle, err := linearsync.NewProjectWorkerLifecycle("proj-a")
	if err != nil {
		t.Fatalf("NewProjectWorkerLifecycle(healthy) error = %v", err)
	}
	healthyLifecycle, err = healthyLifecycle.Transition(linearsync.WorkerTriggerHealthy)
	if err != nil {
		t.Fatalf("Transition(healthy) error = %v", err)
	}

	degradedLifecycle, err := linearsync.NewProjectWorkerLifecycle("proj-b")
	if err != nil {
		t.Fatalf("NewProjectWorkerLifecycle(degraded) error = %v", err)
	}
	degradedLifecycle, err = degradedLifecycle.Transition(linearsync.WorkerTriggerDegraded)
	if err != nil {
		t.Fatalf("Transition(degraded) error = %v", err)
	}

	tests := []struct {
		name          string
		projection    ProjectSyncProjection
		wantState     linearsync.WorkerState
		wantTransport string
		wantFallback  bool
		wantBackoff   time.Duration
	}{
		{
			name: "healthy worker stays on primary transport",
			projection: ProjectSyncProjection{
				Lifecycle:     healthyLifecycle,
				Fallback:      linearsync.WebhookFallbackStatus{Mode: "sdk", Healthy: true},
				LastSuccessAt: &started,
				RetryPolicy:   linearsync.RetryPolicy{MaxAttempts: 5, BaseDelay: 5 * time.Second, MaxExponent: 8},
			},
			wantState:     linearsync.WorkerStateHealthy,
			wantTransport: projectSyncTransportPrimary,
			wantFallback:  false,
			wantBackoff:   0,
		},
		{
			name: "degraded worker activates fallback with retry metadata",
			projection: ProjectSyncProjection{
				Lifecycle:     degradedLifecycle,
				Fallback:      linearsync.WebhookFallbackStatus{Mode: "failed", Healthy: false, Reason: "Timed out registering Linear webhook after 4000ms"},
				LastSuccessAt: &started,
				LastError:     "sync queue stalled",
				LastErrorAt:   &errored,
				RetryAttempts: 2,
				RetryPolicy:   linearsync.RetryPolicy{MaxAttempts: 5, BaseDelay: 5 * time.Second, MaxExponent: 8},
			},
			wantState:     linearsync.WorkerStateDegraded,
			wantTransport: projectSyncTransportFallback,
			wantFallback:  true,
			wantBackoff:   20 * time.Second,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			diag := ProjectSyncDiagnosticsFromLifecycle(tt.projection)

			if got, want := diag.ProjectID, tt.projection.Lifecycle.ProjectID; got != want {
				t.Fatalf("ProjectID = %q, want %q", got, want)
			}
			if got, want := diag.State, tt.wantState; got != want {
				t.Fatalf("State = %q, want %q", got, want)
			}
			if got, want := diag.Transport, tt.wantTransport; got != want {
				t.Fatalf("Transport = %q, want %q", got, want)
			}
			if got, want := diag.FallbackActive, tt.wantFallback; got != want {
				t.Fatalf("FallbackActive = %v, want %v", got, want)
			}
			if got := diag.FallbackReason; got == "" {
				t.Fatal("FallbackReason should not be empty")
			}
			if got, want := diag.RetryBackoff, tt.wantBackoff; got != want {
				t.Fatalf("RetryBackoff = %s, want %s", got, want)
			}
			if got, want := diag.RetryAttempts, tt.projection.RetryAttempts; got != want {
				t.Fatalf("RetryAttempts = %d, want %d", got, want)
			}
		})
	}

	degraded := ProjectSyncDiagnosticsFromLifecycle(ProjectSyncProjection{
		Lifecycle:     degradedLifecycle,
		Fallback:      linearsync.WebhookFallbackStatus{Mode: "failed", Healthy: false, Reason: "Timed out registering Linear webhook after 4000ms"},
		LastSuccessAt: &started,
		LastError:     "sync queue stalled",
		LastErrorAt:   &errored,
		RetryAttempts: 2,
		RetryPolicy:   linearsync.DefaultRetryPolicy(),
	})
	if degraded.LastSuccessAt == nil || degraded.LastErrorAt == nil {
		t.Fatal("expected projected timestamps to be retained")
	}
	if got, want := degraded.LastSuccessAt.Format(time.RFC3339), started.UTC().Format(time.RFC3339); got != want {
		t.Fatalf("LastSuccessAt = %s, want %s", got, want)
	}
	if got, want := degraded.LastErrorAt.Format(time.RFC3339), errored.UTC().Format(time.RFC3339); got != want {
		t.Fatalf("LastErrorAt = %s, want %s", got, want)
	}
}

func TestCollectDiagnosticsIncludesProjectSyncDiagnostics(t *testing.T) {
	tmux := &mockTmuxClient{sessions: []string{}}
	ports := &mockPortAllocator{}
	network := &mockNetworkChecker{online: true, lastCheck: time.Now()}
	service := NewService(tmux, ports, network)

	started := time.Date(2026, 3, 30, 10, 15, 0, 0, time.UTC)

	healthyLifecycle, err := linearsync.NewProjectWorkerLifecycle("proj-a")
	if err != nil {
		t.Fatalf("NewProjectWorkerLifecycle(healthy) error = %v", err)
	}
	healthyLifecycle, err = healthyLifecycle.Transition(linearsync.WorkerTriggerHealthy)
	if err != nil {
		t.Fatalf("Transition(healthy) error = %v", err)
	}
	degradedLifecycle, err := linearsync.NewProjectWorkerLifecycle("proj-b")
	if err != nil {
		t.Fatalf("NewProjectWorkerLifecycle(degraded) error = %v", err)
	}
	degradedLifecycle, err = degradedLifecycle.Transition(linearsync.WorkerTriggerDegraded)
	if err != nil {
		t.Fatalf("Transition(degraded) error = %v", err)
	}
	degradedLifecycle, err = degradedLifecycle.Transition(linearsync.WorkerTriggerRetrying)
	if err != nil {
		t.Fatalf("Transition(retrying) error = %v", err)
	}

	service.SetProjectSyncProjection(ProjectSyncProjection{
		Lifecycle:     degradedLifecycle,
		Fallback:      linearsync.WebhookFallbackStatus{Mode: "failed", Healthy: false, Reason: "Missing LINEAR_WEBHOOK_PUBLIC_URL prevents webhook fallback"},
		LastSuccessAt: &started,
		LastError:     "retrying after webhook fallback",
		RetryAttempts: 1,
		RetryPolicy:   linearsync.RetryPolicy{MaxAttempts: 5, BaseDelay: 5 * time.Second, MaxExponent: 8},
	})
	service.SetProjectSyncProjection(ProjectSyncProjection{
		Lifecycle:     healthyLifecycle,
		Fallback:      linearsync.WebhookFallbackStatus{Mode: "sdk", Healthy: true},
		LastSuccessAt: &started,
		RetryAttempts: 0,
		RetryPolicy:   linearsync.DefaultRetryPolicy(),
	})

	diag := service.CollectDiagnostics(context.Background(), map[string]*domain.Session{}, nil)
	if diag == nil {
		t.Fatal("CollectDiagnostics() returned nil")
	}
	if got, want := len(diag.ProjectSync), 2; got != want {
		t.Fatalf("len(ProjectSync) = %d, want %d", got, want)
	}
	if got, want := diag.ProjectSync[0].ProjectID, "proj-a"; got != want {
		t.Fatalf("first ProjectID = %q, want %q", got, want)
	}
	if got, want := diag.ProjectSync[0].State, linearsync.WorkerStateHealthy; got != want {
		t.Fatalf("proj-a state = %q, want %q", got, want)
	}
	if got, want := diag.ProjectSync[0].Transport, projectSyncTransportPrimary; got != want {
		t.Fatalf("proj-a transport = %q, want %q", got, want)
	}
	if got, want := diag.ProjectSync[1].ProjectID, "proj-b"; got != want {
		t.Fatalf("second ProjectID = %q, want %q", got, want)
	}
	if got, want := diag.ProjectSync[1].State, linearsync.WorkerStateRetrying; got != want {
		t.Fatalf("proj-b state = %q, want %q", got, want)
	}
	if got, want := diag.ProjectSync[1].Transport, projectSyncTransportFallback; got != want {
		t.Fatalf("proj-b transport = %q, want %q", got, want)
	}
	if !strings.Contains(diag.ProjectSync[1].FallbackReason, "Missing LINEAR_WEBHOOK_PUBLIC_URL") {
		t.Fatalf("proj-b fallback reason = %q, want missing-public-url guidance", diag.ProjectSync[1].FallbackReason)
	}
}

func TestFormatDiagnosticsPreservesLegacySectionsWhenProjectSyncPresent(t *testing.T) {
	now := time.Date(2026, 3, 30, 10, 15, 0, 0, time.UTC)
	diag := &SystemDiagnostics{
		Timestamp:    now,
		OverallState: HealthHealthy,
		Ports:        []PortInfo{},
		Sessions:     []SessionInfo{},
		Worktrees:    []WorktreeInfo{},
		Network: NetworkInfo{
			IsOnline:  true,
			LastCheck: now,
		},
		System: SystemInfo{
			GoVersion:    "go1.21",
			OS:           "linux",
			Arch:         "amd64",
			NumGoroutine: 10,
			MemoryUsage:  1024 * 1024,
		},
		ProjectSync: []ProjectSyncDiagnostics{
			{
				ProjectID:     "proj-a",
				State:         linearsync.WorkerStateHealthy,
				Transport:     projectSyncTransportPrimary,
				RetryAttempts: 0,
				RetryBackoff:  0,
			},
		},
		Warnings: []string{},
		Errors:   []string{},
	}

	service := NewService(&mockTmuxClient{}, &mockPortAllocator{}, &mockNetworkChecker{})
	output := service.FormatDiagnostics(diag)

	expectedSections := []string{
		"System Status",
		"NETWORK:",
		"SESSIONS:",
		"SYSTEM:",
		"PROJECT SYNC:",
	}
	for _, section := range expectedSections {
		if !strings.Contains(output, section) {
			t.Fatalf("FormatDiagnostics() missing section: %s", section)
		}
	}
	if !strings.Contains(output, "proj-a: state=healthy transport=primary") {
		t.Fatalf("FormatDiagnostics() missing project sync line: %s", output)
	}
}
