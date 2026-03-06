package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestRunCLIInitJSONSuccessIncludesCommandOKAndWorkspacePaths(t *testing.T) {
	projectRoot := chdirToTempProjectRoot(t)
	stubInitLoadConfig(t)

	exitCode, stdout, stderr := runCLIForTest([]string{"init", "--json"})
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
			Artifacts []struct {
				Path   string `json:"path"`
				Status string `json:"status"`
			} `json:"artifacts"`
			CreatedPaths  []string `json:"createdPaths"`
			ExistingPaths []string `json:"existingPaths"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "init" {
		t.Fatalf("expected command=init, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}

	expectedArtifacts := []string{
		".azedarach",
		".azedarach/azedarach.db",
		".azedarach/.gitkeep",
	}
	if len(envelope.Result.Artifacts) != len(expectedArtifacts) {
		t.Fatalf("expected %d artifacts, got %d", len(expectedArtifacts), len(envelope.Result.Artifacts))
	}

	for index, expectedPath := range expectedArtifacts {
		artifact := envelope.Result.Artifacts[index]
		if artifact.Path != expectedPath {
			t.Fatalf("expected result.artifacts[%d].path=%q, got %q", index, expectedPath, artifact.Path)
		}
		if artifact.Status != "created" && artifact.Status != "existing" {
			t.Fatalf("expected result.artifacts[%d].status to be created|existing, got %q", index, artifact.Status)
		}
	}

	for _, expectedPath := range expectedArtifacts {
		absolutePath := filepath.Join(projectRoot, expectedPath)
		if _, err := os.Stat(absolutePath); err != nil {
			t.Fatalf("expected artifact %q to exist on disk: %v", absolutePath, err)
		}
	}
}

func TestRunCLIInitHumanSuccessPrintsDeterministicSummaryLines(t *testing.T) {
	chdirToTempProjectRoot(t)
	stubInitLoadConfig(t)

	exitCode, stdout, stderr := runCLIForTest([]string{"init"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	requiredSummaryFragments := []string{
		"Workspace artifacts:",
		".azedarach",
		".azedarach/azedarach.db",
		".azedarach/.gitkeep",
	}
	for _, fragment := range requiredSummaryFragments {
		if !strings.Contains(stdout, fragment) {
			t.Fatalf("expected human output to include %q.\noutput:\n%s", fragment, stdout)
		}
	}
}

func TestRunCLIInitInvalidArgsFailDeterministically(t *testing.T) {
	stubInitLoadConfig(t)

	t.Run("json mode returns invalid_argument for extra positional", func(t *testing.T) {
		exitCode, stdout, stderr := runCLIForTest([]string{"init", "unexpected", "--json"})
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

		if envelope.Command != "init" {
			t.Fatalf("expected command=init, got %q", envelope.Command)
		}
		if envelope.OK {
			t.Fatalf("expected ok=false for invalid positional args")
		}
		if envelope.Error.Code != "invalid_argument" {
			t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
		}
	})

	t.Run("json mode returns invalid_argument for unknown flag", func(t *testing.T) {
		exitCode, stdout, stderr := runCLIForTest([]string{"init", "--nope", "--json"})
		if exitCode == 0 {
			t.Fatalf("expected non-zero exit code for unknown flag")
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

		if envelope.Command != "init" {
			t.Fatalf("expected command=init, got %q", envelope.Command)
		}
		if envelope.OK {
			t.Fatalf("expected ok=false for unknown flag")
		}
		if envelope.Error.Code != "invalid_argument" {
			t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
		}
	})

	t.Run("human mode prints usage for invalid args", func(t *testing.T) {
		exitCode, stdout, stderr := runCLIForTest([]string{"init", "--nope"})
		if exitCode == 0 {
			t.Fatalf("expected non-zero exit code for unknown flag")
		}
		if stdout != "" {
			t.Fatalf("expected empty stdout for invalid args in human mode, got %q", stdout)
		}
		if !strings.Contains(stderr, "Usage: az init") {
			t.Fatalf("expected usage text in stderr, got %q", stderr)
		}
	})
}

func chdirToTempProjectRoot(t *testing.T) string {
	t.Helper()

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	projectRoot := t.TempDir()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("failed to change directory to temp project root %q: %v", projectRoot, err)
	}

	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("failed to restore working directory %q: %v", originalWD, err)
		}
	})

	return projectRoot
}

func stubInitLoadConfig(t *testing.T) {
	t.Helper()

	originalLoadConfig := loadConfig
	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
	})
}
