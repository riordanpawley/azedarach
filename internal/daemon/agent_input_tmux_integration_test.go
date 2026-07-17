package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestRealTmuxHumanDraftAndConcurrentTypingArePreservedWhenNativeProofUnavailable(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	socket := "az-dlb-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	session := "agent-input"
	readyChannel := socket + "-shell-ready"
	run := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("new-session", "-d", "-s", session, "zsh -f -c 'tmux wait-for -S "+readyChannel+"; exec zsh -f'")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	// The pane shell signals through the tmux server after it has started, giving
	// the test a deterministic barrier before metadata inspection and typing.
	run("wait-for", readyChannel)
	meta := strings.Split(strings.TrimSpace(run("display-message", "-p", "-t", session, "#{pane_id}\t#{pane_pid}")), "\t")
	if len(meta) != 2 {
		t.Fatalf("meta=%q", meta)
	}
	pid, err := strconv.Atoi(meta[1])
	if err != nil {
		t.Fatal(err)
	}
	run("send-keys", "-l", "-t", meta[0], "human draft")
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), nil)
	defer runtimeStore.Close()
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	now := time.Now().UTC()
	paneID := strings.TrimPrefix(meta[0], "%")
	if err := runtimeStore.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: "p", SessionID: session, LogicalPaneID: "agent", TmuxPaneID: paneID, PanePID: pid, AgentIncarnation: "inc", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtimeStore.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{ProjectID: "p", SessionID: session, ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now, ObservedVersion: now.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	service := newAgentInputDeliveryService(func(string) *daemonstate.RuntimeStateStore { return runtimeStore }, func(string) *issues.Client { return client }, nil, "test")
	done := make(chan struct{})
	go func() { run("send-keys", "-l", "-t", meta[0], " plus typing"); close(done) }()
	result, err := service.Deliver(ctx, domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: session, Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: paneID, PanePID: pid, AgentIncarnation: "inc"}, Tool: "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "AUTOMATION MUST NOT APPEAR", IntentKey: "race", ExpiresAt: now.Add(time.Minute)})
	if err != nil || result.Outcome != domain.AgentInputWaitingNotReady {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	<-done
	capture := run("capture-pane", "-p", "-t", meta[0])
	if !strings.Contains(capture, "human draft") || !strings.Contains(capture, "plus typing") || strings.Contains(capture, "AUTOMATION MUST NOT APPEAR") {
		t.Fatalf("draft preservation failed: %q", capture)
	}
}
