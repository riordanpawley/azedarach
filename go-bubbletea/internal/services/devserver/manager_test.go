package devserver

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewManager(NewPortAllocator(9300), logger)
}

func TestManager_StartRequiresWorktreeContext(t *testing.T) {
	manager := newTestManager(t)

	_, err := manager.Start(context.Background(), "az-1", "az-1", "npm run dev", "")
	if err == nil {
		t.Fatal("expected error when starting without worktree context")
	}
}

func TestManager_StartPersistsIssueScopedContext(t *testing.T) {
	manager := newTestManager(t)

	server, err := manager.Start(context.Background(), "az-2", "az-2", "npm run dev", "/tmp/az-2")
	if err != nil {
		t.Fatalf("expected start success, got error: %v", err)
	}
	if server.IssueID != "az-2" {
		t.Fatalf("expected issue_id az-2, got %q", server.IssueID)
	}
	if server.Worktree != "/tmp/az-2" {
		t.Fatalf("expected worktree /tmp/az-2, got %q", server.Worktree)
	}
	if server.Status != "running" {
		t.Fatalf("expected running status, got %q", server.Status)
	}
}

func TestManager_ToggleAndRestartWithContext(t *testing.T) {
	manager := newTestManager(t)

	if err := manager.Toggle(context.Background(), "az-3", "/tmp/az-3", "npm run dev"); err != nil {
		t.Fatalf("toggle start failed: %v", err)
	}
	server, ok := manager.Get("az-3")
	if !ok || server.Status != "running" {
		t.Fatalf("expected running server after first toggle, got ok=%v status=%q", ok, server.Status)
	}

	if err := manager.Toggle(context.Background(), "az-3", "/tmp/az-3", "npm run dev"); err != nil {
		t.Fatalf("toggle stop failed: %v", err)
	}
	server, ok = manager.Get("az-3")
	if !ok || server.Status != "stopped" {
		t.Fatalf("expected stopped server after second toggle, got ok=%v status=%q", ok, server.Status)
	}

	if err := manager.Restart(context.Background(), "az-3", "/tmp/az-3", "npm run dev"); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	server, ok = manager.Get("az-3")
	if !ok || server.Status != "running" {
		t.Fatalf("expected running server after restart, got ok=%v status=%q", ok, server.Status)
	}
}
