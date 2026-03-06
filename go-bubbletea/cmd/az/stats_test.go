package main

import (
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRunCLIStatsJSONSuccessIncludesDeterministicAggregates(t *testing.T) {
	stubStatsDependencies(t, func() ([]domain.Task, error) {
		return []domain.Task{
			newStatsTask("AZE-801", domain.StatusOpen),
			newStatsTask("AZE-802", domain.StatusOpen),
			newStatsTask("AZE-803", domain.StatusInProgress),
			newStatsTask("AZE-804", domain.StatusBlocked),
			newStatsTask("AZE-805", domain.StatusDone),
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"stats", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Result  struct {
			Total    int            `json:"total"`
			ByStatus map[string]int `json:"byStatus"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "stats" {
		t.Fatalf("expected command=stats, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if envelope.Result.Total != 5 {
		t.Fatalf("expected result.total=5, got %d", envelope.Result.Total)
	}
	if envelope.Result.ByStatus == nil {
		t.Fatalf("expected result.byStatus map to be present")
	}
	if envelope.Result.ByStatus[string(domain.StatusOpen)] != 2 {
		t.Fatalf(
			"expected result.byStatus[%q]=2, got %d",
			domain.StatusOpen,
			envelope.Result.ByStatus[string(domain.StatusOpen)],
		)
	}
	if envelope.Result.ByStatus[string(domain.StatusInProgress)] != 1 {
		t.Fatalf(
			"expected result.byStatus[%q]=1, got %d",
			domain.StatusInProgress,
			envelope.Result.ByStatus[string(domain.StatusInProgress)],
		)
	}
	if envelope.Result.ByStatus[string(domain.StatusBlocked)] != 1 {
		t.Fatalf(
			"expected result.byStatus[%q]=1, got %d",
			domain.StatusBlocked,
			envelope.Result.ByStatus[string(domain.StatusBlocked)],
		)
	}
	if envelope.Result.ByStatus[string(domain.StatusDone)] != 1 {
		t.Fatalf(
			"expected result.byStatus[%q]=1, got %d",
			domain.StatusDone,
			envelope.Result.ByStatus[string(domain.StatusDone)],
		)
	}
}

func TestRunCLIStatsJSONEmptyDatasetIsZeroSafe(t *testing.T) {
	stubStatsDependencies(t, func() ([]domain.Task, error) {
		return []domain.Task{}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"stats", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Result  struct {
			Total    int            `json:"total"`
			ByStatus map[string]int `json:"byStatus"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "stats" {
		t.Fatalf("expected command=stats, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if envelope.Result.Total != 0 {
		t.Fatalf("expected result.total=0 for empty dataset, got %d", envelope.Result.Total)
	}
	if envelope.Result.ByStatus == nil {
		t.Fatalf("expected result.byStatus map to be present for empty dataset")
	}
	for status, count := range envelope.Result.ByStatus {
		if count != 0 {
			t.Fatalf("expected zero-safe per-status map values for empty dataset, got %q=%d", status, count)
		}
	}
}

func newStatsTask(id string, status domain.Status) domain.Task {
	return domain.Task{
		ID:       id,
		Title:    "Stats command test task",
		Status:   status,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
}

func stubStatsDependencies(t *testing.T, query taskQueryFunc) {
	t.Helper()

	originalLoadConfig := loadConfig
	originalNewTaskQuery := newTaskQuery

	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}
	newTaskQuery = func() taskQueryFunc {
		return query
	}

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
		newTaskQuery = originalNewTaskQuery
	})
}
