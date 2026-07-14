package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type terminalFailureProbeRunner struct {
	mu     sync.Mutex
	output string
	calls  int
	args   []string
}

func TestEnrichTasksWithSessionStateAppliesTerminalFailureProbe(t *testing.T) {
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })

	const projectID, issueID = "proj-terminal-enrich", "dae"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	if err := upsertSessionStateFixture(runtimeStore, context.Background(), projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning,
		UpdatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed runtime state: %v", err)
	}
	if _, _, err := runtimeStore.ApplyPhysicalSessionObservation(context.Background(), daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed physical runtime observation: %v", err)
	}
	runner := &terminalFailureProbeRunner{output: "⚠ Selected model is at capacity. Please try a different model."}
	d := &Daemon{
		tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
	}
	projected, err := runtimeStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	d.observeTerminalFailureProbes(context.Background(), projectID, projected, projectID, sessionDisplayActivityByIssueKeyFromSessions(projected, projectID))

	tasks := d.enrichTasksWithSessionState(context.Background(), projectID, []domain.Task{{
		ID: naming.IssueID(issueID), Title: "Hook-silent capacity", Status: domain.StatusInProgress,
	}})
	if len(tasks) != 1 || tasks[0].Session == nil {
		t.Fatalf("tasks = %+v, want enriched session", tasks)
	}
	if tasks[0].Session.Activity != "error" || tasks[0].Session.ActivitySource != "terminal" {
		t.Fatalf("session activity = %s/%s, want error/terminal", tasks[0].Session.Activity, tasks[0].Session.ActivitySource)
	}
}

func (r *terminalFailureProbeRunner) Run(_ context.Context, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(args) > 0 && args[0] == "capture-pane" {
		r.calls++
		r.args = append([]string(nil), args...)
		return r.output, nil
	}
	return "", nil
}

func (r *terminalFailureProbeRunner) lastArgs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.args...)
}

func (r *terminalFailureProbeRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestApplyTerminalFailureProbesClassifiesHookSilentCapacityScreen(t *testing.T) {
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })

	runner := &terminalFailureProbeRunner{output: "⚠ Selected model is at capacity. Please try a different model."}
	d := &Daemon{tmux: tmux.NewClient(runner, slog.Default())}
	projectID, issueID := "proj", "dae"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	sessions := []daemonstate.Session{{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning}}
	activity := map[string]sessionDisplayActivity{sessionKey(issueID): {Activity: "busy", Source: "hooks", UpdatedAt: now.Add(-time.Minute)}}

	d.observeTerminalFailureProbes(context.Background(), projectID, sessions, projectID, activity)
	got := d.applyTerminalFailureProbes(context.Background(), projectID, sessions, projectID, activity)
	if got[sessionKey(issueID)].Activity != "error" || got[sessionKey(issueID)].Source != "terminal" {
		t.Fatalf("activity = %+v, want terminal error", got[sessionKey(issueID)])
	}
	if runner.callCount() != 1 {
		t.Fatalf("capture calls = %d, want 1", runner.callCount())
	}
	args := runner.lastArgs()
	if len(args) < 6 || args[len(args)-1] != "-8" {
		t.Fatalf("capture args = %v, want bounded 8-line tail", args)
	}

	d.tmux = nil
	got = d.applyTerminalFailureProbes(context.Background(), projectID, sessions, projectID, map[string]sessionDisplayActivity{
		sessionKey(issueID): {Activity: "busy", Source: "hooks", UpdatedAt: now.Add(-time.Minute)},
	})
	if got[sessionKey(issueID)].Activity != "error" || runner.callCount() != 1 {
		t.Fatalf("cached activity = %+v, calls = %d; want cached error without capture", got[sessionKey(issueID)], runner.callCount())
	}
}

func TestApplyTerminalFailureProbesSkipsFreshAndOrdinaryActivity(t *testing.T) {
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })

	runner := &terminalFailureProbeRunner{output: "Tests passed.\n› Continue"}
	d := &Daemon{tmux: tmux.NewClient(runner, slog.Default())}
	projectID, issueID := "proj", "dae"
	sessions := []daemonstate.Session{{ID: naming.CanonicalSessionID(projectID, issueID), IssueID: issueID, State: daemonstate.SessionStateRunning}}

	fresh := map[string]sessionDisplayActivity{sessionKey(issueID): {Activity: "busy", Source: "hooks", UpdatedAt: now.Add(-time.Second)}}
	if got := d.applyTerminalFailureProbes(context.Background(), projectID, sessions, projectID, fresh); got[sessionKey(issueID)].Activity != "busy" || runner.callCount() != 0 {
		t.Fatalf("fresh activity = %+v, calls = %d; want busy without capture", got[sessionKey(issueID)], runner.callCount())
	}
	stale := map[string]sessionDisplayActivity{sessionKey(issueID): {Activity: "busy", Source: "hooks", UpdatedAt: now.Add(-time.Minute)}}
	d.observeTerminalFailureProbes(context.Background(), projectID, sessions, projectID, stale)
	if got := d.applyTerminalFailureProbes(context.Background(), projectID, sessions, projectID, stale); got[sessionKey(issueID)].Activity != "busy" || runner.callCount() != 1 {
		t.Fatalf("ordinary stale activity = %+v, calls = %d; want busy after one capture", got[sessionKey(issueID)], runner.callCount())
	}
	if got := d.applyTerminalFailureProbes(context.Background(), projectID, sessions, projectID, stale); got[sessionKey(issueID)].Activity != "busy" || runner.callCount() != 1 {
		t.Fatalf("backed-off activity = %+v, calls = %d; want busy without another capture", got[sessionKey(issueID)], runner.callCount())
	}
}

func TestApplyTerminalFailureProbesNewHookSupersedesHandledScreen(t *testing.T) {
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })

	runner := &terminalFailureProbeRunner{output: "⚠ Selected model is at capacity. Please try a different model."}
	d := &Daemon{tmux: tmux.NewClient(runner, slog.Default())}
	projectID, issueID := "proj", "dae"
	sessions := []daemonstate.Session{{ID: naming.CanonicalSessionID(projectID, issueID), IssueID: issueID, State: daemonstate.SessionStateRunning}}
	oldHookAt := now.Add(-time.Minute)
	oldActivity := map[string]sessionDisplayActivity{
		sessionKey(issueID): {Activity: "busy", Source: "hooks", UpdatedAt: oldHookAt},
	}
	d.observeTerminalFailureProbes(context.Background(), projectID, sessions, projectID, oldActivity)
	d.applyTerminalFailureProbes(context.Background(), projectID, sessions, projectID, oldActivity)

	newHookAt := now.Add(20 * time.Second)
	now = now.Add(40 * time.Second)
	newActivity := map[string]sessionDisplayActivity{
		sessionKey(issueID): {Activity: "busy", Source: "hooks", UpdatedAt: newHookAt},
	}
	d.observeTerminalFailureProbes(context.Background(), projectID, sessions, projectID, newActivity)
	got := d.applyTerminalFailureProbes(context.Background(), projectID, sessions, projectID, newActivity)
	if got[sessionKey(issueID)].Activity != "busy" {
		t.Fatalf("activity after newer hook = %+v, want busy", got[sessionKey(issueID)])
	}
	if runner.callCount() != 2 {
		t.Fatalf("capture calls = %d, want 2", runner.callCount())
	}
	got = d.applyTerminalFailureProbes(context.Background(), projectID, sessions, projectID, map[string]sessionDisplayActivity{
		sessionKey(issueID): {Activity: "busy", Source: "hooks", UpdatedAt: newHookAt},
	})
	if got[sessionKey(issueID)].Activity != "busy" || runner.callCount() != 2 {
		t.Fatalf("repeated activity = %+v, calls = %d; want stable busy without re-capture", got[sessionKey(issueID)], runner.callCount())
	}
}
