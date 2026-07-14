package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type activityRaceRunner struct {
	store     *daemonstate.RuntimeStateStore
	projectID string
	sessionID string
	issueID   string
	newerAt   time.Time
}

func (r *activityRaceRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 || args[0] != "capture-pane" {
		return "", nil
	}
	evidence := daemonstate.SessionActivityEvidence{
		ProjectID: r.projectID, SessionID: r.sessionID, IssueID: r.issueID,
		Activity: "busy", ActivitySource: "hooks", SourceSessionID: r.sessionID,
		Event: "user_prompt_submit", ObservedAt: r.newerAt, UpdatedAt: r.newerAt,
	}
	if err := r.store.UpsertSessionActivityEvidence(ctx, evidence); err != nil {
		return "", err
	}
	_, _, err := r.store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
		ProjectID: r.projectID, SessionID: r.sessionID, ObservedState: daemonstate.SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: r.newerAt,
	})
	return "Turn completed.\n› Continue", err
}

func TestReconcileStaleBusySessionActivityPersistsTerminalPromptIdle(t *testing.T) {
	now := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })

	ctx := context.Background()
	const projectID, issueID = "project", "dgf"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSessionActivityEvidence(ctx, daemonstate.SessionActivityEvidence{
		ProjectID: projectID, SessionID: sessionID, IssueID: issueID,
		Activity: "busy", ActivitySource: "hooks", SourceSessionID: sessionID,
		Agent: "codex", Hook: "user_prompt_submit", Event: "user_prompt_submit",
		ObservedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	runner := &terminalFailureProbeRunner{output: "Validation complete.\n› Continue"}
	d := &Daemon{
		cfg: Config{Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
	}

	converged, err := d.reconcileStaleBusySessionActivity(ctx, projectID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if converged != 1 || runner.callCount() != 1 {
		t.Fatalf("converged=%d captures=%d, want one bounded convergence", converged, runner.callCount())
	}
	evidence, found, err := store.GetSessionActivityEvidence(ctx, projectID, sessionID)
	if err != nil || !found {
		t.Fatalf("evidence found=%t err=%v", found, err)
	}
	if evidence.Activity != "idle" || evidence.ActivitySource != "terminal" || evidence.Event != "idle_prompt_recovered" {
		t.Fatalf("evidence = %+v, want persisted terminal idle", evidence)
	}
	session, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found {
		t.Fatalf("session found=%t err=%v", found, err)
	}
	if session.Activity != "idle" || session.ActivitySource != "terminal" {
		t.Fatalf("session = %+v, want canonical idle/terminal", session)
	}
}

func TestReconcileStaleBusySessionActivitySkipsFreshAndActivePane(t *testing.T) {
	now := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })
	ctx := context.Background()
	const projectID, issueID = "project", "dgf"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning}); err != nil {
		t.Fatal(err)
	}
	seed := func(at time.Time) {
		t.Helper()
		if err := store.UpsertSessionActivityEvidence(ctx, daemonstate.SessionActivityEvidence{ProjectID: projectID, SessionID: sessionID, IssueID: issueID, Activity: "busy", ActivitySource: "hooks", SourceSessionID: sessionID, ObservedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	runner := &terminalFailureProbeRunner{output: "• Running tests"}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}}
	seed(now.Add(-time.Second))
	if count, err := d.reconcileStaleBusySessionActivity(ctx, projectID, nil); err != nil || count != 0 || runner.callCount() != 0 {
		t.Fatalf("fresh count=%d calls=%d err=%v", count, runner.callCount(), err)
	}
	now = now.Add(time.Minute)
	seed(now.Add(-time.Minute))
	if count, err := d.reconcileStaleBusySessionActivity(ctx, projectID, nil); err != nil || count != 0 || runner.callCount() != 1 {
		t.Fatalf("active count=%d calls=%d err=%v", count, runner.callCount(), err)
	}
	now = now.Add(29 * time.Second)
	if count, err := d.reconcileStaleBusySessionActivity(ctx, projectID, nil); err != nil || count != 0 || runner.callCount() != 1 {
		t.Fatalf("backoff count=%d calls=%d err=%v", count, runner.callCount(), err)
	}
}

func TestReconcileStaleBusySessionActivityDoesNotOverwriteNewerBusyHook(t *testing.T) {
	now := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })
	ctx := context.Background()
	const projectID, issueID = "project", "dgf"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	store := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	otherDaemonStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = otherDaemonStore.Close() })
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, UpdatedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSessionActivityEvidence(ctx, daemonstate.SessionActivityEvidence{ProjectID: projectID, SessionID: sessionID, IssueID: issueID, Activity: "busy", ActivitySource: "hooks", SourceSessionID: sessionID, ObservedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	runner := &activityRaceRunner{store: otherDaemonStore, projectID: projectID, sessionID: sessionID, issueID: issueID, newerAt: now.Add(time.Second)}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}}

	converged, err := d.reconcileStaleBusySessionActivity(ctx, projectID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if converged != 0 {
		t.Fatalf("converged = %d, want newer hook to win", converged)
	}
	evidence, found, err := store.GetSessionActivityEvidence(ctx, projectID, sessionID)
	if err != nil || !found || evidence.Activity != "busy" || evidence.ActivitySource != "hooks" || !evidence.ObservedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("evidence = %+v found=%t err=%v, want newer busy hook", evidence, found, err)
	}
	session, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || session.Activity != "busy" || session.ActivitySource != "hooks" {
		t.Fatalf("session = %+v found=%t err=%v, want newer busy hook", session, found, err)
	}
}
