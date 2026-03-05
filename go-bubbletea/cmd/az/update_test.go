package main

import (
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRunCLIUpdateJSONSuccessIncludesCommandOKAndResultFields(t *testing.T) {
	called := false
	capturedIssueID := ""
	capturedStatus := ""

	stubUpdateDependencies(t, func(issueID string, status domain.Status) error {
		called = true
		capturedIssueID = issueID
		capturedStatus = string(status)
		return nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"update", "AZE-1", "--status", "in_progress", "--json"})
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
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "update" {
		t.Fatalf("expected command=update, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if envelope.Result.ID != "AZE-1" {
		t.Fatalf("expected result.id=AZE-1, got %q", envelope.Result.ID)
	}
	if envelope.Result.Status != "in_progress" {
		t.Fatalf("expected result.status=in_progress, got %q", envelope.Result.Status)
	}
	if !called {
		t.Fatalf("expected updater to be called")
	}
	if capturedIssueID != "AZE-1" {
		t.Fatalf("expected updater issue-id=AZE-1, got %q", capturedIssueID)
	}
	if capturedStatus != "in_progress" {
		t.Fatalf("expected updater status=in_progress, got %q", capturedStatus)
	}
}

func TestRunCLIUpdateJSONMissingStatusReturnsInvalidArgument(t *testing.T) {
	called := false

	stubUpdateDependencies(t, func(_ string, _ domain.Status) error {
		called = true
		return nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"update", "AZE-1", "--json"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for missing --status")
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

	if envelope.Command != "update" {
		t.Fatalf("expected command=update, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for missing --status")
	}
	if envelope.Error.Code != "invalid_argument" {
		t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr in JSON mode, got %q", stderr)
	}
	if called {
		t.Fatalf("expected updater not to be called for invalid arguments")
	}
}

func TestRunCLIUpdateJSONInvalidStatusReturnsInvalidArgument(t *testing.T) {
	called := false

	stubUpdateDependencies(t, func(_ string, _ domain.Status) error {
		called = true
		return nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"update", "AZE-1", "--status", "nope", "--json"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for invalid status")
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

	if envelope.Command != "update" {
		t.Fatalf("expected command=update, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for invalid status")
	}
	if envelope.Error.Code != "invalid_argument" {
		t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr in JSON mode, got %q", stderr)
	}
	if called {
		t.Fatalf("expected updater not to be called for invalid status")
	}
}

func stubUpdateDependencies(t *testing.T, updater issueUpdateFunc) {
	t.Helper()

	originalLoadConfig := loadConfig
	originalNewIssueUpdater := newIssueUpdater

	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}

	newIssueUpdater = func() issueUpdateFunc {
		return updater
	}

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
		newIssueUpdater = originalNewIssueUpdater
	})
}
