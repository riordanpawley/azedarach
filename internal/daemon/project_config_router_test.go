package daemon

import (
	"os"
	"path/filepath"
	"strings"
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

func TestRuntimeConfigForProjectSplitsWorktreeSyncAndAsyncInitCommands(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configJSON := `{
		"$version": 8,
		"worktree": {
			"initCommands": ["legacy sync"],
			"syncInitCommands": ["explicit sync"],
			"asyncInitCommands": ["async warmup"]
		}
	}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	d := New(Config{
		RepoDir:                   repoDir,
		WorktreeInitCommands:      []string{"default sync"},
		WorktreeAsyncInitCommands: []string{"default async"},
	})

	cfg := d.runtimeConfigForProject(filepath.Base(repoDir))
	if got, want := strings.Join(cfg.WorktreeInitCommands, ","), "legacy sync,explicit sync"; got != want {
		t.Fatalf("WorktreeInitCommands = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.WorktreeAsyncInitCommands, ","), "async warmup"; got != want {
		t.Fatalf("WorktreeAsyncInitCommands = %q, want %q", got, want)
	}
}

func TestRuntimeConfigForProjectUsesMigratedWorktreeInitCommands(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configJSON := `{
		"$version": 8,
		"worktree": {
			"initCommands": ["legacy sync"]
		}
	}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	d := New(Config{
		RepoDir:              repoDir,
		WorktreeInitCommands: []string{"default sync"},
	})

	cfg := d.runtimeConfigForProject(filepath.Base(repoDir))
	if got, want := strings.Join(cfg.WorktreeInitCommands, ","), "legacy sync"; got != want {
		t.Fatalf("WorktreeInitCommands = %q, want %q", got, want)
	}
}
