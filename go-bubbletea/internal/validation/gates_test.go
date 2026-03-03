package validation

import (
	"context"
	"errors"
	"testing"
)

type stubRunner struct {
	responses map[GateName]error
	runs      []GateName
	defs      map[string]GateName
}

func (s *stubRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	_ = ctx
	_ = name

	if len(args) == 0 {
		return "", errors.New("no args")
	}

	gate := s.defs[args[len(args)-1]]
	if gate == "" && len(args) > 1 {
		gate = s.defs[args[1]]
	}
	s.runs = append(s.runs, gate)

	err := s.responses[gate]
	if err != nil {
		return "failed", err
	}

	return "ok", nil
}

func TestGateRunnerRunDeterministicOrder(t *testing.T) {
	runner := &stubRunner{
		responses: map[GateName]error{},
		defs: map[string]GateName{
			"Unit":        GateUnit,
			"Integration": GateIntegration,
			"E2E":         GateE2E,
			"Snapshot":    GateSnapshot,
			"./...":       GateBuild,
		},
	}

	gateRunner := NewGateRunner(runner)
	report := gateRunner.Run(context.Background())

	if len(report.Results) != 5 {
		t.Fatalf("len(results) = %d, want 5", len(report.Results))
	}

	wantOrder := []GateName{GateUnit, GateIntegration, GateE2E, GateSnapshot, GateBuild}
	for i, want := range wantOrder {
		if report.Results[i].Name != want {
			t.Fatalf("results[%d].Name = %s, want %s", i, report.Results[i].Name, want)
		}
	}

	if !report.ReadyToComplete {
		t.Fatal("ReadyToComplete = false, want true")
	}
}

func TestGateRunnerFailureUpdatesChecklist(t *testing.T) {
	runner := &stubRunner{
		responses: map[GateName]error{GateE2E: errors.New("boom")},
		defs: map[string]GateName{
			"Unit":        GateUnit,
			"Integration": GateIntegration,
			"E2E":         GateE2E,
			"Snapshot":    GateSnapshot,
			"./...":       GateBuild,
		},
	}

	gateRunner := NewGateRunner(runner)
	report := gateRunner.Run(context.Background())

	if report.ReadyToComplete {
		t.Fatal("ReadyToComplete = true, want false")
	}

	failedE2E := false
	for _, item := range report.Checklist {
		if item.Name == GateE2E {
			failedE2E = true
			if item.Done {
				t.Fatal("e2e checklist unexpectedly done")
			}
		}
	}

	if !failedE2E {
		t.Fatal("expected e2e checklist item")
	}
}

func TestBuildCompletionChecklistMarksMissingGateAsNotRun(t *testing.T) {
	report := Report{
		Results: []GateResult{{Name: GateUnit, Passed: true}},
	}

	checklist := BuildCompletionChecklist(report)
	if len(checklist) != 5 {
		t.Fatalf("len(checklist) = %d, want 5", len(checklist))
	}

	for _, item := range checklist {
		if item.Name == GateIntegration {
			if item.Done {
				t.Fatal("integration should not be done")
			}
			if item.Detail != "not run" {
				t.Fatalf("integration detail = %q, want %q", item.Detail, "not run")
			}
		}
	}
}
