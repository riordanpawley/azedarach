package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRunCLIListJSONSuccessIncludesCommandOKAndResultItems(t *testing.T) {
	stubListDependencies(t, func() ([]domain.Task, error) {
		return []domain.Task{
			{
				ID:       "AZE-101",
				Title:    "Implement list JSON output",
				Status:   domain.StatusOpen,
				Priority: domain.P1,
				Type:     domain.TypeTask,
			},
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"list", "--json"})
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
			Items []json.RawMessage `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "list" {
		t.Fatalf("expected command=list, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if len(envelope.Result.Items) == 0 {
		t.Fatalf("expected result.items to be non-empty")
	}
}

func TestRunCLIListJSONWithExtraPositionalArgsReturnsInvalidArgument(t *testing.T) {
	stubListDependencies(t, func() ([]domain.Task, error) {
		return []domain.Task{}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"list", "--json", "unexpected-arg"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for invalid positional args")
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

	if envelope.Command != "list" {
		t.Fatalf("expected command=list, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for invalid positional args")
	}
	if envelope.Error.Code != "invalid_argument" {
		t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
	}
}

func TestRunCLIListHumanSuccessPrintsIssueIDs(t *testing.T) {
	expectedTasks := []domain.Task{
		{
			ID:       "AZE-201",
			Title:    "Wire list command parser",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
		},
		{
			ID:       "AZE-202",
			Title:    "Add list command tests",
			Status:   domain.StatusInProgress,
			Priority: domain.P1,
			Type:     domain.TypeTask,
		},
	}

	stubListDependencies(t, func() ([]domain.Task, error) {
		return expectedTasks, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"list"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	for _, task := range expectedTasks {
		if !strings.Contains(stdout, task.ID) {
			t.Fatalf("expected human output to contain issue ID %q.\noutput:\n%s", task.ID, stdout)
		}
	}
}

func stubListDependencies(t *testing.T, fetch listFetchFunc) {
	t.Helper()

	originalLoadConfig := loadConfig
	originalNewListFetcher := newListFetcher

	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}
	newListFetcher = func() listFetchFunc {
		return fetch
	}

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
		newListFetcher = originalNewListFetcher
	})
}
