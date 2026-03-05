package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRunCLIShowJSONSuccessIncludesCommandOKAndResultID(t *testing.T) {
	issueID := "AZE-123"
	stubShowDependencies(t, func(requestedID string) ([]domain.Task, error) {
		return []domain.Task{
			{
				ID:       requestedID,
				Title:    "Implement show command",
				Status:   domain.StatusOpen,
				Priority: domain.P2,
				Type:     domain.TypeTask,
			},
		}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"show", issueID, "--json"})
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
			ID string `json:"id"`
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
	if envelope.Result.ID != issueID {
		t.Fatalf("expected result.id=%q, got %q", issueID, envelope.Result.ID)
	}
}

func TestRunCLIShowJSONNotFoundReturnsExpectedErrorMessage(t *testing.T) {
	issueID := "AZE-404"
	stubShowDependencies(t, func(_ string) ([]domain.Task, error) {
		return []domain.Task{}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"show", issueID, "--json"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for not-found issue")
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr in JSON mode, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	expectedMessage := fmt.Sprintf("Issue not found internally nor externally: %s", issueID)
	if envelope.Command != "show" {
		t.Fatalf("expected command=show, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for not-found issue")
	}
	if envelope.Error.Message != expectedMessage {
		t.Fatalf("expected error.message=%q, got %q", expectedMessage, envelope.Error.Message)
	}
}

func TestRunCLIShowHumanNotFoundWritesExpectedStderrAndNonZeroExit(t *testing.T) {
	issueID := "AZE-999"
	stubShowDependencies(t, func(_ string) ([]domain.Task, error) {
		return []domain.Task{}, nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"show", issueID})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for not-found issue")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout in human not-found mode, got %q", stdout)
	}

	expectedMessage := fmt.Sprintf("Issue not found internally nor externally: %s\n", issueID)
	if stderr != expectedMessage {
		t.Fatalf("expected stderr=%q, got %q", expectedMessage, stderr)
	}
}

func stubShowDependencies(t *testing.T, search showSearchFunc) {
	t.Helper()

	originalLoadConfig := loadConfig
	originalNewShowSearcher := newShowSearcher

	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}
	newShowSearcher = func() showSearchFunc {
		return search
	}

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
		newShowSearcher = originalNewShowSearcher
	})
}
