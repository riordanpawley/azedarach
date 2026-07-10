package domain

import (
	"testing"
	"time"
)

func TestResolveOrchestrationScopePrecedence(t *testing.T) {
	tests := []struct {
		name, explicit, environment string
		wantKind                    OrchestrationScopeKind
		wantRoot                    string
	}{
		{name: "explicit wins", explicit: "dbk", environment: "dbf", wantKind: OrchestrationScopeRooted, wantRoot: "dbk"},
		{name: "environment supplies implicit root", environment: "dbf", wantKind: OrchestrationScopeRooted, wantRoot: "dbf"},
		{name: "omission selects project", wantKind: OrchestrationScopeProject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveOrchestrationScope(tt.explicit, tt.environment)
			if err != nil {
				t.Fatalf("ResolveOrchestrationScope() error = %v", err)
			}
			if got.Kind != tt.wantKind || got.RootIssueID.String() != tt.wantRoot {
				t.Fatalf("scope = %+v, want kind=%q root=%q", got, tt.wantKind, tt.wantRoot)
			}
		})
	}
}

func TestOrchestratorIdentityIncludesProjectAndTypedScope(t *testing.T) {
	projectScope := ProjectOrchestrationScope()
	rootedScope, err := RootedOrchestrationScope("dbk")
	if err != nil {
		t.Fatal(err)
	}
	project, err := NewOrchestratorIdentity("project-a", projectScope)
	if err != nil {
		t.Fatal(err)
	}
	rooted, err := NewOrchestratorIdentity("project-a", rootedScope)
	if err != nil {
		t.Fatal(err)
	}
	if project == rooted {
		t.Fatalf("project and rooted identity must differ: %+v", project)
	}
}

func TestEvaluateOrchestratorLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	graceStarted := now.Add(-time.Minute)
	graceExpired := now.Add(-10 * time.Minute)
	policy := OrchestratorLifecyclePolicy{CompleteGrace: 5 * time.Minute}
	tests := []struct {
		name  string
		facts OrchestratorLifecycleFacts
		want  OrchestratorLifecycle
	}{
		{name: "working", facts: OrchestratorLifecycleFacts{OpenIssues: 1}, want: OrchestratorWorking},
		{name: "human interaction is quiescent but incomplete", facts: OrchestratorLifecycleFacts{UnresolvedInteractions: 1}, want: OrchestratorQuiescent},
		{name: "newly complete enters grace", facts: OrchestratorLifecycleFacts{}, want: OrchestratorCompleteGrace},
		{name: "complete within grace", facts: OrchestratorLifecycleFacts{CompleteSince: &graceStarted}, want: OrchestratorCompleteGrace},
		{name: "complete after grace pauses", facts: OrchestratorLifecycleFacts{CompleteSince: &graceExpired}, want: OrchestratorPaused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EvaluateOrchestratorLifecycle(now, tt.facts, policy); got != tt.want {
				t.Fatalf("lifecycle = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOrchestratorWakePolicyDebouncesRepeatedWake(t *testing.T) {
	now := time.Now()
	policy := OrchestratorLifecyclePolicy{WakeDebounce: 2 * time.Second}
	if policy.WakeAllowed(now.Add(-time.Second), now) {
		t.Fatal("wake inside debounce window should be coalesced")
	}
	if !policy.WakeAllowed(now.Add(-3*time.Second), now) {
		t.Fatal("wake after debounce window should be allowed")
	}
}

func TestParseOrchestratorLifecyclePolicy(t *testing.T) {
	got, err := ParseOrchestratorLifecyclePolicy("10m", "500ms")
	if err != nil {
		t.Fatal(err)
	}
	if got.CompleteGrace != 10*time.Minute || got.WakeDebounce != 500*time.Millisecond {
		t.Fatalf("policy = %+v", got)
	}
	if _, err := ParseOrchestratorLifecyclePolicy("-1s", "2s"); err == nil {
		t.Fatal("negative grace should fail")
	}
	if _, err := ParseOrchestratorLifecyclePolicy("5m", "eventually"); err == nil {
		t.Fatal("invalid debounce should fail")
	}
}
