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

	t.Run("worktree-like cwd basename prefix wins over default", func(t *testing.T) {
		registry := &config.ProjectsRegistry{
			Projects: []config.Project{
				{Name: "azedarach", Path: "/work/azedarach"},
				{Name: "beta", Path: "/work/beta"},
			},
			DefaultProject: "beta",
		}
		got := resolveInitialProjectName(registry, "/work/azedarach-afv/go-bubbletea")
		if got != "azedarach" {
			t.Fatalf("resolveInitialProjectName() = %q, want %q", got, "azedarach")
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

func TestActiveProjectPath(t *testing.T) {
	registry := &config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "alpha", Path: "/work/alpha"},
			{Name: "beta", Path: "/work/beta"},
		},
	}

	t.Run("uses current project path when available", func(t *testing.T) {
		m := Model{
			currentProject:  "beta",
			projectRegistry: registry,
			repoDir:         "/work/alpha",
		}
		if got := m.activeProjectPath(); got != "/work/beta" {
			t.Fatalf("activeProjectPath() = %q, want %q", got, "/work/beta")
		}
	})

	t.Run("falls back to repoDir when current project missing", func(t *testing.T) {
		m := Model{
			currentProject:  "missing",
			projectRegistry: registry,
			repoDir:         "/work/alpha",
		}
		if got := m.activeProjectPath(); got != "/work/alpha" {
			t.Fatalf("activeProjectPath() = %q, want %q", got, "/work/alpha")
		}
	})
}

func TestProjectSwitchResultUpdatesModelConfig(t *testing.T) {
	m := newTestModel()
	m.config = &config.Config{
		Git: config.GitConfig{BaseBranch: "main"},
	}

	next, _ := m.Update(projectSwitchResultMsg{
		project: config.Project{
			Name: "Chefy",
			Path: "/work/Chefy",
		},
		projectConfig: &config.Config{
			Git: config.GitConfig{BaseBranch: "preview"},
		},
	})

	updated, ok := next.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", next)
	}
	if got := updated.resolveBaseBranch(); got != "preview" {
		t.Fatalf("resolveBaseBranch() = %q, want %q", got, "preview")
	}
	if updated.currentProject != "Chefy" {
		t.Fatalf("currentProject = %q, want %q", updated.currentProject, "Chefy")
	}
	if updated.repoDir != "/work/Chefy" {
		t.Fatalf("repoDir = %q, want %q", updated.repoDir, "/work/Chefy")
	}
}
