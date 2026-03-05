package main

import (
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestRunCLIDeleteJSONSuccessIncludesCommandOKAndResultID(t *testing.T) {
	stubDeleteDependencies(t, func(_ string) error {
		return nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"delete", "AZE-1", "--json"})
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

	if envelope.Command != "delete" {
		t.Fatalf("expected command=delete, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if envelope.Result.ID != "AZE-1" {
		t.Fatalf("expected result.id=AZE-1, got %q", envelope.Result.ID)
	}
}

func TestRunCLIDeleteJSONMissingIDReturnsInvalidArgument(t *testing.T) {
	stubDeleteDependencies(t, func(_ string) error {
		return nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"delete", "--json"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for missing issue-id")
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

	if envelope.Command != "delete" {
		t.Fatalf("expected command=delete, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for missing issue-id")
	}
	if envelope.Error.Code != "invalid_argument" {
		t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
	}
}

func stubDeleteDependencies(t *testing.T, deleter issueDeleteFunc) {
	t.Helper()

	originalLoadConfig := loadConfig
	originalNewIssueDeleter := newIssueDeleter

	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}
	newIssueDeleter = func() issueDeleteFunc {
		return deleter
	}

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
		newIssueDeleter = originalNewIssueDeleter
	})
}
