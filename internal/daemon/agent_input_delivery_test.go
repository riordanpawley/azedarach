package daemon

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type agentInputRunner struct {
	mu       sync.Mutex
	capture  string
	attached string
	writes   [][]string
	payloads []string
}

func (r *agentInputRunner) Run(_ context.Context, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch args[0] {
	case "list-panes":
		return "az-1\t%7\t123\tcodex\t" + r.attached + "\n", nil
	case "capture-pane":
		return r.capture, nil
	case "paste-buffer", "send-keys":
		r.writes = append(r.writes, append([]string(nil), args...))
	}
	return "", nil
}

func (r *agentInputRunner) RunWithInput(_ context.Context, input string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payloads = append(r.payloads, input)
	r.writes = append(r.writes, append([]string(nil), args...))
	return "", nil
}

func TestAgentInputDeliveryFailsClosedAndPreservesComposer(t *testing.T) {
	ctx := context.Background()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	identity := daemonstate.ManagedAgentIdentity{ProjectID: "p", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: "inc-1", ObservedAt: now}
	if err := store.UpsertManagedAgentIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{ProjectID: "p", SessionID: "az-1", ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now, ObservedVersion: now.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	runner := &agentInputRunner{capture: "previous output\n› human draft\n", attached: "0"}
	service := newAgentInputDeliveryService(tmux.NewClient(runner, slog.Default()), func(string) *daemonstate.RuntimeStateStore { return store })
	request := domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "az-1", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: "inc-1"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "automation", IntentKey: "one", ExpiresAt: now.Add(time.Minute)}

	result, err := service.Deliver(ctx, request)
	if err != nil || result.Outcome != domain.AgentInputWaitingInputNonempty {
		t.Fatalf("nonempty result=%+v err=%v", result, err)
	}
	if len(runner.writes) != 0 || len(runner.payloads) != 0 {
		t.Fatalf("nonempty composer was modified: writes=%v", runner.writes)
	}

	runner.capture = "previous output\n›\n"
	runner.attached = "1"
	result, err = service.Deliver(ctx, request)
	if err != nil || result.Outcome != domain.AgentInputWaitingHumanAttached || len(runner.writes) != 0 {
		t.Fatalf("attached result=%+v err=%v writes=%v", result, err, runner.writes)
	}

	runner.attached = "0"
	result, err = service.Deliver(ctx, request)
	if err != nil || result.Outcome != domain.AgentInputDelivered {
		t.Fatalf("empty detached result=%+v err=%v", result, err)
	}
	if len(runner.payloads) != 1 || runner.payloads[0] != "automation" {
		t.Fatalf("payloads=%v", runner.payloads)
	}
	result, err = service.Deliver(ctx, request)
	if err != nil || result.Outcome != domain.AgentInputDelivered || len(runner.payloads) != 1 {
		t.Fatalf("duplicate result=%+v err=%v payloads=%v", result, err, runner.payloads)
	}
}

func TestAgentInputDeliveryRejectsStaleIncarnation(t *testing.T) {
	ctx := context.Background()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: "p", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 124, AgentIncarnation: "new", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	runner := &agentInputRunner{capture: "›\n", attached: "0"}
	service := newAgentInputDeliveryService(tmux.NewClient(runner, slog.Default()), func(string) *daemonstate.RuntimeStateStore { return store })
	result, err := service.Deliver(ctx, domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "az-1", Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "7", PanePID: 123, AgentIncarnation: "old"}, Tool: "codex", Payload: "automation", IntentKey: "stale"})
	if err != nil || result.Outcome != domain.AgentInputRejectedStaleTarget || len(runner.writes) != 0 {
		t.Fatalf("result=%+v err=%v writes=%v", result, err, runner.writes)
	}
}

func TestAgentComposerEmptyAdapters(t *testing.T) {
	tests := []struct {
		tool, capture string
		want          bool
	}{
		{"codex", "output\n›\n", true}, {"codex", "output\n› draft\n", false},
		{"claude", "output\n❯\n", true}, {"claude", "output\n❯ draft\n", false},
		{"opencode", "output\n>\n", true}, {"configured-agent", "output\n>\n", false},
		{"zsh", "output\n%\n", true}, {"zsh", "output\n% draft\n", false},
	}
	for _, tt := range tests {
		if got := agentComposerEmpty(tt.tool, tt.capture); got != tt.want {
			t.Errorf("agentComposerEmpty(%q, %q)=%v want %v", tt.tool, tt.capture, got, tt.want)
		}
	}
}
