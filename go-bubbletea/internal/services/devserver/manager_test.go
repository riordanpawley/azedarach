package devserver

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestManager_StartSkipsOccupiedPortAndReusesReleasedPort(t *testing.T) {
	occupied := map[int]bool{9200: true}
	allocator := newTestPortAllocator(9200, func(port int) bool {
		return !occupied[port]
	})
	manager := NewManager(allocator, newDiscardLogger())
	ctx := context.Background()

	srv, err := manager.Start(ctx, "issue-1", "issue-1", "bun run dev")
	if err != nil {
		t.Fatalf("start issue-1: %v", err)
	}
	if srv.Port != 9201 {
		t.Fatalf("expected issue-1 to skip occupied port 9200, got %d", srv.Port)
	}
	if srv.Status != "running" {
		t.Fatalf("expected running server, got %+v", srv)
	}

	if err := manager.Stop(ctx, "issue-1"); err != nil {
		t.Fatalf("stop issue-1: %v", err)
	}
	occupied[9200] = false
	stopped, ok := manager.Get("issue-1")
	if !ok {
		t.Fatal("expected stopped server to remain tracked")
	}
	if stopped.Status != "stopped" {
		t.Fatalf("expected stopped status, got %+v", stopped)
	}

	reused, err := manager.Start(ctx, "issue-2", "issue-2", "bun run dev")
	if err != nil {
		t.Fatalf("start issue-2: %v", err)
	}
	if reused.Port != 9200 {
		t.Fatalf("expected released port 9200 to be reused, got %d", reused.Port)
	}
}

func TestManager_StartReturnsExistingRunningServer(t *testing.T) {
	allocator := newTestPortAllocator(9300, func(int) bool { return true })
	manager := NewManager(allocator, newDiscardLogger())
	ctx := context.Background()

	first, err := manager.Start(ctx, "issue-1", "issue-1", "bun run dev")
	if err != nil {
		t.Fatalf("start issue-1: %v", err)
	}

	second, err := manager.Start(ctx, "issue-1", "issue-1", "bun run dev")
	if err != nil {
		t.Fatalf("start issue-1 again: %v", err)
	}
	if first != second {
		t.Fatalf("expected running server to be returned unchanged")
	}
	if second.Port != first.Port {
		t.Fatalf("expected same port on repeated start, got %d and %d", first.Port, second.Port)
	}
}
