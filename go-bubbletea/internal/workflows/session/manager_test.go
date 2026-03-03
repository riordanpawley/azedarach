package session

import (
	"context"
	"testing"

	"github.com/riordanpawley/azedarach/internal/core/ops"
)

func TestManagerLifecycleTransitions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fakeTmux := newFakeTmuxClient()
	mgr := NewManager(fakeTmux, ops.NewOrchestrator(), NewDeterministicPortAllocator(5000, 100))

	started, err := mgr.Start(ctx, "sess-1", "/tmp/work", "bd-1")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if started.State != StateBusy {
		t.Fatalf("expected busy after start, got %s", started.State)
	}

	if err := mgr.Attach(ctx, "sess-1"); err != nil {
		t.Fatalf("attach existing session: %v", err)
	}

	if err := mgr.Pause(ctx, "sess-1"); err != nil {
		t.Fatalf("pause session: %v", err)
	}

	state, err := mgr.DetectState(ctx, "sess-1")
	if err != nil {
		t.Fatalf("detect state after pause: %v", err)
	}
	if state != StatePaused {
		t.Fatalf("expected paused state, got %s", state)
	}

	if err := mgr.Resume(ctx, "sess-1"); err != nil {
		t.Fatalf("resume session: %v", err)
	}

	state, err = mgr.DetectState(ctx, "sess-1")
	if err != nil {
		t.Fatalf("detect state after resume: %v", err)
	}
	if state != StateBusy {
		t.Fatalf("expected busy state, got %s", state)
	}

	enabled, port, err := mgr.ToggleDevServer(ctx, "sess-1")
	if err != nil {
		t.Fatalf("enable devserver: %v", err)
	}
	if !enabled {
		t.Fatalf("expected devserver to be enabled")
	}
	if port != started.Port {
		t.Fatalf("expected deterministic port %d, got %d", started.Port, port)
	}

	restartPort, err := mgr.RestartDevServer(ctx, "sess-1")
	if err != nil {
		t.Fatalf("restart devserver: %v", err)
	}
	if restartPort != started.Port {
		t.Fatalf("expected restart to keep same port %d, got %d", started.Port, restartPort)
	}

	enabled, _, err = mgr.ToggleDevServer(ctx, "sess-1")
	if err != nil {
		t.Fatalf("disable devserver: %v", err)
	}
	if enabled {
		t.Fatalf("expected devserver to be disabled")
	}

	if err := mgr.Stop(ctx, "sess-1"); err != nil {
		t.Fatalf("stop session: %v", err)
	}

	state, err = mgr.DetectState(ctx, "sess-1")
	if err != nil {
		t.Fatalf("detect state after stop: %v", err)
	}
	if state != StateDone {
		t.Fatalf("expected done state after stop, got %s", state)
	}
}

func TestManagerDetectStateMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   State
	}{
		{name: "waiting", output: "Waiting for your input [y/n]", want: StateWaiting},
		{name: "error", output: "panic: runtime error", want: StateError},
		{name: "done", output: "All done. Tests passed", want: StateDone},
		{name: "busy", output: "compiling package", want: StateBusy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			fakeTmux := newFakeTmuxClient()
			fakeTmux.sessions["sess-state"] = tt.output

			mgr := NewManager(fakeTmux, ops.NewOrchestrator(), NewDeterministicPortAllocator(4000, 100))
			if _, err := mgr.Start(ctx, "sess-state", "/tmp/work", "bd-2"); err != nil {
				t.Fatalf("start session: %v", err)
			}

			state, err := mgr.DetectState(ctx, "sess-state")
			if err != nil {
				t.Fatalf("detect state: %v", err)
			}
			if state != tt.want {
				t.Fatalf("DetectState() = %s, want %s", state, tt.want)
			}
		})
	}
}

func TestReconcileOrphanSessions(t *testing.T) {
	t.Parallel()

	known := []string{"sess-a", "sess-b", "sess-e"}
	live := []string{"sess-d", "sess-b", "sess-c"}

	orphans := ReconcileOrphanSessions(known, live)
	if len(orphans) != 2 {
		t.Fatalf("expected 2 orphans, got %d", len(orphans))
	}
	if orphans[0] != "sess-c" || orphans[1] != "sess-d" {
		t.Fatalf("unexpected orphan sessions: %v", orphans)
	}
}

type fakeTmuxClient struct {
	sessions map[string]string
}

func newFakeTmuxClient() *fakeTmuxClient {
	return &fakeTmuxClient{sessions: map[string]string{}}
}

func (f *fakeTmuxClient) HasSession(_ context.Context, name string) (bool, error) {
	_, ok := f.sessions[name]
	return ok, nil
}

func (f *fakeTmuxClient) NewSession(_ context.Context, name string, _ string) error {
	if _, ok := f.sessions[name]; !ok {
		f.sessions[name] = ""
	}
	return nil
}

func (f *fakeTmuxClient) KillSession(_ context.Context, name string) error {
	delete(f.sessions, name)
	return nil
}

func (f *fakeTmuxClient) CapturePane(_ context.Context, name string, _ int) (string, error) {
	return f.sessions[name], nil
}
