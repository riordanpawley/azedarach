package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
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

func TestRuntimeConfigForProjectLoadsCodexAppServer(t *testing.T) {
	t.Parallel()
	repoDir := t.TempDir()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"$version":11,"cliTool":"codex","session":{"codexAppServer":true}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	d := New(Config{RepoDir: repoDir, CLITool: "codex"})
	if cfg := d.runtimeConfigForProject(filepath.Base(repoDir)); !cfg.CodexAppServer {
		t.Fatal("CodexAppServer = false, want true")
	}
}

func TestRuntimeConfigForProjectLoadsGateFailureArtifactPaths(t *testing.T) {
	t.Parallel()
	repoDir := t.TempDir()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"$version":11,"gate":{"failureArtifactPaths":["build/junit","coverage/raw"]}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	d := New(Config{RepoDir: repoDir})
	got := d.runtimeConfigForProject(filepath.Base(repoDir)).GateFailureArtifactPaths
	if strings.Join(got, ",") != "build/junit,coverage/raw" {
		t.Fatalf("GateFailureArtifactPaths = %v", got)
	}
}

func TestRuntimeConfigForProjectDoesNotInheritRootFailureArtifactPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rootRepo := t.TempDir()
	consumerRepo := t.TempDir()
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: "consumer", Name: "Consumer", Path: consumerRepo}}}); err != nil {
		t.Fatal(err)
	}
	d := New(Config{RepoDir: rootRepo, GateFailureArtifactPaths: []string{".tmp/root-only"}})
	if got := d.runtimeConfigForProject("consumer").GateFailureArtifactPaths; len(got) != 0 {
		t.Fatalf("registered consumer inherited root failure artifact paths: %v", got)
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
