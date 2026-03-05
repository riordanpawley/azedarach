package main

import (
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestRunCLICreateJSONSuccessIncludesCommandOKAndResultID(t *testing.T) {
	stubCreateDependencies(t, func(_ string) (string, error) {
		return "AZE-301", nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"create", "Title", "--json"})
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

	if envelope.Command != "create" {
		t.Fatalf("expected command=create, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if envelope.Result.ID == "" {
		t.Fatalf("expected non-empty result.id")
	}
}

func TestRunCLIQJSONSuccessIncludesCommandOKAndResultID(t *testing.T) {
	stubCreateDependencies(t, func(_ string) (string, error) {
		return "AZE-302", nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"q", "Quick title", "--json"})
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

	if envelope.Command != "q" {
		t.Fatalf("expected command=q, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if envelope.Result.ID == "" {
		t.Fatalf("expected non-empty result.id")
	}
}

func TestRunCLICreateJSONMissingTitleReturnsInvalidArgument(t *testing.T) {
	stubCreateDependencies(t, func(_ string) (string, error) {
		return "AZE-303", nil
	})

	exitCode, stdout, stderr := runCLIForTest([]string{"create", "--json"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for missing title")
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

	if envelope.Command != "create" {
		t.Fatalf("expected command=create, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for missing title")
	}
	if envelope.Error.Code != "invalid_argument" {
		t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
	}
}

func stubCreateDependencies(t *testing.T, creator issueCreateFunc) {
	t.Helper()

	originalLoadConfig := loadConfig
	originalNewIssueCreator := newIssueCreator

	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}
	newIssueCreator = func() issueCreateFunc {
		return creator
	}

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
		newIssueCreator = originalNewIssueCreator
	})
}
