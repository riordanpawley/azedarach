package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
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

func TestProjectSwitchResultRebindsProjectScopedServices(t *testing.T) {
	oldRepo := t.TempDir()
	newRepo := t.TempDir()

	m := newTestModel()
	m.config = &config.Config{
		Git: config.GitConfig{
			BaseBranch:   "main",
			WorkflowMode: "origin",
		},
	}
	m.repoDir = oldRepo
	m.rebuildProjectScopedServices()

	oldGitSync := m.gitSyncService
	oldAttachment := m.attachmentService

	next, _ := m.Update(projectSwitchResultMsg{
		project: config.Project{
			Name: "Chefy",
			Path: newRepo,
		},
		projectConfig: &config.Config{
			Git: config.GitConfig{
				BaseBranch:   "preview",
				WorkflowMode: "origin",
			},
		},
	})

	updated, ok := next.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", next)
	}
	if updated.gitSyncService == oldGitSync {
		t.Fatal("expected gitSyncService to be rebound for switched project")
	}
	if updated.attachmentService == oldAttachment {
		t.Fatal("expected attachmentService to be rebound for switched project")
	}

	sourceFile := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(sourceFile, []byte{0x89, 0x50, 0x4E, 0x47}, 0o644); err != nil {
		t.Fatalf("write source attachment: %v", err)
	}

	attached, err := updated.attachmentService.Attach(context.Background(), "che-1", sourceFile)
	if err != nil {
		t.Fatalf("attach image in switched project: %v", err)
	}
	wantPrefix := filepath.Join(newRepo, ".azedarach", "images", "che-1") + string(os.PathSeparator)
	if !strings.HasPrefix(attached.Path, wantPrefix) {
		t.Fatalf("attachment path = %q, want prefix %q", attached.Path, wantPrefix)
	}
}

func TestProjectSelectedMsgStartsVisibleRefreshWithoutDiscardingCurrentBoard(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.tasks = []domain.Task{
		{ID: "old-1", Title: "Old task"},
	}
	m.sessions = map[string]*domain.Session{
		"old-1": {Worktree: "/tmp/old-1"},
	}

	next, cmd := m.Update(overlay.ProjectSelectedMsg{
		Project: config.Project{Name: "beta", Path: "/work/beta"},
	})

	updated := next.(Model)
	if cmd == nil {
		t.Fatal("expected project switch command to be scheduled")
	}
	if !updated.loading {
		t.Fatal("expected loading state while switching projects")
	}
	if !updated.boardRefreshing {
		t.Fatal("expected board refresh indicator while switching projects")
	}
	if len(updated.tasks) != 1 || updated.tasks[0].ID != "old-1" {
		t.Fatalf("tasks = %+v, want previous board tasks preserved until switch succeeds", updated.tasks)
	}
}

func TestProjectSwitchFailureRetainsPreviousBoardState(t *testing.T) {
	m := newTestModel()
	m.loading = true
	m.boardRefreshing = true
	m.tasks = []domain.Task{
		{ID: "old-1", Title: "Old task"},
	}

	next, _ := m.Update(projectSwitchResultMsg{err: fmt.Errorf("boom")})
	updated := next.(Model)

	if updated.loading {
		t.Fatal("expected loading state cleared on switch failure")
	}
	if updated.boardRefreshing {
		t.Fatal("expected refresh indicator cleared on switch failure")
	}
	if len(updated.tasks) != 1 || updated.tasks[0].ID != "old-1" {
		t.Fatalf("tasks = %+v, want previous board tasks retained after switch failure", updated.tasks)
	}
}
