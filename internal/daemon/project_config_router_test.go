package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeConfigForProjectLoadsWorkflowMode(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configJSON := `{
		"$version": 8,
		"git": {
			"baseBranch": "main",
			"workflowMode": "local"
		}
	}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	d := New(Config{
		RepoDir:         repoDir,
		BaseBranch:      "preview",
		GitWorkflowMode: "origin",
	})

	cfg := d.runtimeConfigForProject(filepath.Base(repoDir))
	if cfg.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want main", cfg.BaseBranch)
	}
	if cfg.WorkflowMode != "local" {
		t.Fatalf("WorkflowMode = %q, want local", cfg.WorkflowMode)
	}
}
