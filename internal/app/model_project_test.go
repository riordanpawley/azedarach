package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

	t.Run("uses pinned repoDir when available", func(t *testing.T) {
		m := Model{
			currentProject:  "beta",
			projectRegistry: registry,
			repoDir:         "/work/alpha",
		}
		if got := m.activeProjectPath(); got != "/work/alpha" {
			t.Fatalf("activeProjectPath() = %q, want %q", got, "/work/alpha")
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

	t.Run("falls back to registry path when repoDir is empty", func(t *testing.T) {
		m := Model{
			currentProject:  "beta",
			projectRegistry: registry,
		}
		if got := m.activeProjectPath(); got != "/work/beta" {
			t.Fatalf("activeProjectPath() = %q, want %q", got, "/work/beta")
		}
	})
}

func TestDaemonProjectIDForPath(t *testing.T) {
	got := daemonProjectIDForPath("/work/azedarach")
	if strings.TrimSpace(got) == "" {
		t.Fatal("expected non-empty daemon project id for valid path")
	}

	if got2 := daemonProjectIDForPath("/work/azedarach"); got2 != got {
		t.Fatalf("daemonProjectIDForPath() not deterministic: %q != %q", got2, got)
	}

	if got := daemonProjectIDForPath("   "); got != "" {
		t.Fatalf("daemonProjectIDForPath(blank) = %q, want empty", got)
	}
}

func TestDaemonProjectIDMismatch(t *testing.T) {
	if daemonProjectIDMismatch("abc", "abc") {
		t.Fatal("expected equal IDs not to mismatch")
	}
	if !daemonProjectIDMismatch("abc", "def") {
		t.Fatal("expected different IDs to mismatch")
	}
	if daemonProjectIDMismatch("", "def") {
		t.Fatal("expected empty actual ID to skip mismatch")
	}
	if daemonProjectIDMismatch("abc", "") {
		t.Fatal("expected empty expected ID to skip mismatch")
	}
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
	if updated.loading {
		t.Fatal("expected board to remain visible while switching projects")
	}
	if !updated.boardRefreshing {
		t.Fatal("expected board refresh indicator while switching projects")
	}
	if !updated.projectSwitchInFlight {
		t.Fatal("expected project switch in-flight flag while switching projects")
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
	if updated.projectSwitchInFlight {
		t.Fatal("expected project switch in-flight flag cleared on switch failure")
	}
	if len(updated.tasks) != 1 || updated.tasks[0].ID != "old-1" {
		t.Fatalf("tasks = %+v, want previous board tasks retained after switch failure", updated.tasks)
	}
}

func TestProjectSwitchInFlight_BlocksBoardKeyInteractions(t *testing.T) {
	m := newTestModel()
	m.editor.EnterNormal()
	m.projectSwitchInFlight = true

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	updated := next.(Model)

	if cmd != nil {
		t.Fatalf("expected no command while project switch is in flight, got %T", cmd)
	}
	if !updated.overlayStack.IsEmpty() {
		t.Fatal("expected no overlay to open while project switch is in flight")
	}

	next, cmd = updated.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	updated = next.(Model)
	if cmd != nil {
		t.Fatalf("expected refresh hotkey to be blocked while project switch is in flight, got %T", cmd)
	}
	if updated.boardRefreshing {
		t.Fatal("expected blocked refresh hotkey not to mutate refresh state")
	}
}

func TestProjectSwitchResult_IgnoresStaleSwitchCompletion(t *testing.T) {
	m := newTestModel()
	m.projectSwitchSeq = 2
	m.currentProject = "chefy"
	m.tasks = []domain.Task{
		{ID: "che-1", Title: "Chefy task"},
	}

	next, _ := m.Update(projectSwitchResultMsg{
		switchSeq: 1,
		project:   config.Project{Name: "az", Path: "/work/az"},
		tasks: []domain.Task{
			{ID: "az-1", Title: "Old project task"},
		},
	})

	updated := next.(Model)
	if updated.currentProject != "chefy" {
		t.Fatalf("currentProject = %q, want %q", updated.currentProject, "chefy")
	}
	if len(updated.tasks) != 1 || updated.tasks[0].ID != "che-1" {
		t.Fatalf("tasks = %+v, want stale switch result ignored", updated.tasks)
	}
}
