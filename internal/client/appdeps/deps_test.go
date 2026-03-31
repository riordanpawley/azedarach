package appdeps

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestEnsureActiveProjectPresentAddsGitRootProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "azedarach")
	repoDir := filepath.Join(root, "go-bubbletea")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create git marker: %v", err)
	}

	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "chefy", Path: "/tmp/chefy"},
		},
		DefaultProject: "chefy",
	}

	ensureActiveProjectPresent(registry, repoDir)

	if len(registry.Projects) != 2 {
		t.Fatalf("project count = %d, want 2", len(registry.Projects))
	}
	added := registry.Projects[1]
	if added.Name != "azedarach" {
		t.Fatalf("added name = %q, want azedarach", added.Name)
	}
	if added.Path != root {
		t.Fatalf("added path = %q, want %q", added.Path, root)
	}
}

func TestEnsureActiveProjectPresentNoDuplicate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "azedarach")
	repoDir := filepath.Join(root, "go-bubbletea")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create git marker: %v", err)
	}

	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "azedarach", Path: root},
		},
		DefaultProject: "azedarach",
	}

	ensureActiveProjectPresent(registry, repoDir)

	if len(registry.Projects) != 1 {
		t.Fatalf("project count = %d, want 1", len(registry.Projects))
	}
}
