package main

import (
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRunCLIShowJSONDefaultDepsModeReturnsTypedCounts(t *testing.T) {
	stubShowDependencies(t, func(requestedID string) ([]domain.Task, error) {
		return []domain.Task{
			{
				ID:       requestedID,
				Title:    "Target issue",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
				Dependencies: []domain.Dependency{
					{ID: "AZE-200", Type: domain.DependencyBlocks},
					{ID: "AZE-201", Type: domain.DependencyBlockedBy},
					{ID: "AZE-202", Type: domain.DependencyRelatedTo},
				},
			},
			{ID: "AZE-200", Title: "Downstream", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "AZE-201", Title: "Upstream", Status: domain.StatusBlocked, Priority: domain.P1, Type: domain.TypeBug},
			{ID: "AZE-202", Title: "Related", Status: domain.StatusInProgress, Priority: domain.P3, Type: domain.TypeFeature},
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"show", "AZE-100", "--json"})
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
			Dependencies struct {
				Mode   string         `json:"mode"`
				Counts map[string]int `json:"counts"`
			} `json:"dependencies"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "show" {
		t.Fatalf("expected command=show, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if envelope.Result.Dependencies.Mode != "counts" {
		t.Fatalf("expected dependencies.mode=counts, got %q", envelope.Result.Dependencies.Mode)
	}
	if envelope.Result.Dependencies.Counts["blocking"] != 1 {
		t.Fatalf("expected counts.blocking=1, got %d", envelope.Result.Dependencies.Counts["blocking"])
	}
	if envelope.Result.Dependencies.Counts["blocked-by"] != 1 {
		t.Fatalf("expected counts.blocked-by=1, got %d", envelope.Result.Dependencies.Counts["blocked-by"])
	}
}

func TestRunCLIShowJSONDepsDirectModeSupportsDepTypeFilter(t *testing.T) {
	stubShowDependencies(t, func(requestedID string) ([]domain.Task, error) {
		return []domain.Task{
			{
				ID:       requestedID,
				Title:    "Target issue",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
				Dependencies: []domain.Dependency{
					{ID: "AZE-210", Type: domain.DependencyBlocks},
					{ID: "AZE-211", Type: domain.DependencyBlockedBy},
				},
			},
			{ID: "AZE-210", Title: "Downstream", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "AZE-211", Title: "Upstream", Status: domain.StatusBlocked, Priority: domain.P1, Type: domain.TypeBug},
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{
		"show",
		"AZE-101",
		"--deps=direct",
		"--dep-type",
		"blocking",
		"--json",
	})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Result struct {
			Dependencies struct {
				Mode   string `json:"mode"`
				Direct map[string][]struct {
					ID string `json:"id"`
				} `json:"direct"`
			} `json:"dependencies"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Result.Dependencies.Mode != "direct" {
		t.Fatalf("expected dependencies.mode=direct, got %q", envelope.Result.Dependencies.Mode)
	}
	if len(envelope.Result.Dependencies.Direct["blocking"]) != 1 {
		t.Fatalf(
			"expected one blocking dependency after filter, got %#v",
			envelope.Result.Dependencies.Direct["blocking"],
		)
	}
	if envelope.Result.Dependencies.Direct["blocking"][0].ID != "AZE-210" {
		t.Fatalf(
			"expected filtered blocking dependency id AZE-210, got %q",
			envelope.Result.Dependencies.Direct["blocking"][0].ID,
		)
	}
	if _, exists := envelope.Result.Dependencies.Direct["blocked-by"]; exists {
		t.Fatalf("expected blocked-by relation to be filtered out")
	}
}

func TestRunCLIShowJSONDepDepthZeroSuppressesDirectExpansion(t *testing.T) {
	stubShowDependencies(t, func(requestedID string) ([]domain.Task, error) {
		return []domain.Task{
			{
				ID:       requestedID,
				Title:    "Target issue",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
				Dependencies: []domain.Dependency{
					{ID: "AZE-220", Type: domain.DependencyBlocks},
				},
			},
			{ID: "AZE-220", Title: "Downstream", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{
		"show",
		"AZE-102",
		"--deps=direct",
		"--dep-depth",
		"0",
		"--json",
	})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Result struct {
			Dependencies struct {
				Direct map[string][]json.RawMessage `json:"direct"`
			} `json:"dependencies"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if len(envelope.Result.Dependencies.Direct) != 0 {
		t.Fatalf(
			"expected no direct dependency expansion when --dep-depth=0, got %#v",
			envelope.Result.Dependencies.Direct,
		)
	}
}

func TestRunCLIShowJSONInvalidDepDepthReturnsInvalidArgument(t *testing.T) {
	stubShowDependencies(t, func(_ string) ([]domain.Task, error) {
		return []domain.Task{}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{
		"show",
		"AZE-103",
		"--dep-depth",
		"-1",
		"--json",
	})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for invalid dep-depth")
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr in JSON mode, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "show" {
		t.Fatalf("expected command=show, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for invalid dep-depth")
	}
	if envelope.Error.Code != "invalid_argument" {
		t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
	}
}
