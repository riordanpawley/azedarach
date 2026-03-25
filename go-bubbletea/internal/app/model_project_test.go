package app

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestResolveInitialProjectName(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "alpha", Path: "/work/alpha"},
			{Name: "beta", Path: "/work/beta"},
		},
		DefaultProject: "beta",
	}

	t.Run("cwd match wins over default", func(t *testing.T) {
		got := resolveInitialProjectName(registry, "/work/alpha/subdir")
		if got != "alpha" {
			t.Fatalf("resolveInitialProjectName() = %q, want %q", got, "alpha")
		}
	})

	t.Run("default wins when cwd is outside registry", func(t *testing.T) {
		got := resolveInitialProjectName(registry, "/work/other")
		if got != "beta" {
			t.Fatalf("resolveInitialProjectName() = %q, want %q", got, "beta")
		}
	})

	t.Run("cwd basename is the final fallback", func(t *testing.T) {
		got := resolveInitialProjectName(&config.ProjectsRegistry{}, "/tmp/project")
		if got != "project" {
			t.Fatalf("resolveInitialProjectName() = %q, want %q", got, "project")
		}
	})
}

func TestProjectSelectorCursor(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "alpha", Path: "/work/alpha"},
			{Name: "beta", Path: "/work/beta"},
		},
	}

	t.Run("uses current project when set", func(t *testing.T) {
		m := Model{
			currentProject:  "beta",
			projectRegistry: registry,
			repoDir:         "/work/alpha",
		}

		if got := m.projectSelectorCursor(); got != 1 {
			t.Fatalf("projectSelectorCursor() = %d, want %d", got, 1)
		}
	})

	t.Run("falls back to cwd-matched project", func(t *testing.T) {
		m := Model{
			projectRegistry: registry,
			repoDir:         "/work/alpha/subdir",
		}

		if got := m.projectSelectorCursor(); got != 0 {
			t.Fatalf("projectSelectorCursor() = %d, want %d", got, 0)
		}
	})
}
