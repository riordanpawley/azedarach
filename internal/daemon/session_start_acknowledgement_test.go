package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func newSessionStartAcknowledgementTestDaemon(t *testing.T) (*Daemon, *daemonstate.RuntimeStateStore, *sessionStartTmuxRunner) {
	t.Helper()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{
		cfg:                    Config{RepoDir: t.TempDir(), SessionShell: "zsh", CLITool: "codex", Logger: slog.Default()},
		tmux:                   tmux.NewClient(runner, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{"project": store},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
	}
	return d, store, runner
}

func TestSessionStartLaunchPlanProgressMustPersistBeforeTmux(t *testing.T) {
	wantErr := errors.New("operation progress store unavailable")
	var captured daemonops.Progress
	ctx := daemonops.WithProgressReporter(context.Background(), func(_ context.Context, progress daemonops.Progress) error {
		captured = progress
		return wantErr
	})
	err := reportSessionStartIncarnationProgress(ctx, "tmux_launch", "planned", 70, "inc-1", "/runtime/launch.prompt")
	if !errors.Is(err, wantErr) {
		t.Fatalf("progress error = %v, want %v", err, wantErr)
	}
	if captured.AgentIncarnation != "inc-1" || captured.PromptHandoffPath != "/runtime/launch.prompt" || captured.Phase != "tmux_launch" {
		t.Fatalf("captured progress = %+v", captured)
	}
	if captured.AgentLaunchRequired == nil || !*captured.AgentLaunchRequired {
		t.Fatalf("captured progress = %+v, want agent launch required", captured)
	}
}

func TestTmuxOnlySessionStartPlanPersistsBeforeTmux(t *testing.T) {
	var captured daemonops.Progress
	ctx := daemonops.WithProgressReporter(context.Background(), func(_ context.Context, progress daemonops.Progress) error {
		captured = progress
		return nil
	})
	if err := reportTmuxOnlySessionStartProgress(ctx, "tmux_launch", "planned", 70); err != nil {
		t.Fatal(err)
	}
	if captured.AgentLaunchRequired == nil || *captured.AgentLaunchRequired {
		t.Fatalf("captured progress = %+v, want explicit tmux-only plan", captured)
	}
}

func TestSessionStartAcknowledgedProgressRetainsRecoveryFence(t *testing.T) {
	var phases []daemonops.Progress
	ctx := daemonops.WithProgressReporter(context.Background(), func(_ context.Context, progress daemonops.Progress) error {
		phases = append(phases, progress)
		return nil
	})
	for _, phase := range []string{"tmux_launch", "agent_launch"} {
		if err := reportSessionStartIncarnationProgress(ctx, phase, "progress", 90, "inc-1", "/runtime/launch.prompt"); err != nil {
			t.Fatal(err)
		}
	}
	if len(phases) != 2 {
		t.Fatalf("progress phases = %+v", phases)
	}
	for _, progress := range phases {
		if progress.AgentIncarnation != "inc-1" || progress.PromptHandoffPath != "/runtime/launch.prompt" || progress.AgentLaunchRequired == nil || !*progress.AgentLaunchRequired {
			t.Fatalf("progress lost recovery fence: %+v", progress)
		}
	}
}

func TestInitialManagedAgentAcknowledgementRequiresExactIncarnationAndConsumedPrompt(t *testing.T) {
	d, store, runner := newSessionStartAcknowledgementTestDaemon(t)
	runner.sessions["az-1"] = true
	runner.panes["az-1"] = []string{"%7"}
	runner.panePIDs["az-1"] = 123
	runner.currentCommand = "codex"
	now := time.Now().UTC()
	if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "planned", ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "consumed.prompt")
	if err := d.waitForInitialManagedAgentAcknowledgement(context.Background(), "project", "az-1", "planned", sessionPromptHandoff{PromptPath: prompt}); err != nil {
		t.Fatalf("exact acknowledgement rejected: %v", err)
	}
	commands := runner.commands
	if len(commands) == 0 || !slices.Contains(commands[len(commands)-1], "-t") || !slices.Contains(commands[len(commands)-1], "az-1") || slices.Contains(commands[len(commands)-1], "-a") {
		t.Fatalf("bootstrap pane probe = %v, want exact-session list-panes", commands)
	}
}

func TestInitialManagedAgentAcknowledgementAcceptsExactCodexPromptSubmissionBeforeHandoffConsumption(t *testing.T) {
	d, store, runner := newSessionStartAcknowledgementTestDaemon(t)
	runner.sessions["az-1"] = true
	runner.panes["az-1"] = []string{"%7"}
	runner.panePIDs["az-1"] = 123
	runner.currentCommand = "codex"
	boundAt := time.Date(2026, time.July, 19, 2, 46, 6, 531478000, time.UTC)
	identity := daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "planned", ObservedAt: boundAt,
	}
	if err := store.UpsertManagedAgentIdentity(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	acknowledged, err := store.AcknowledgeManagedAgentIdentity(context.Background(), identity, boundAt.Add(150*time.Millisecond))
	if err != nil || !acknowledged {
		t.Fatalf("acknowledge exact generated Codex prompt submission: acknowledged=%t err=%v", acknowledged, err)
	}
	prompt := filepath.Join(t.TempDir(), "still-present.prompt")
	if err := os.WriteFile(prompt, []byte("real Codex bootstrap prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.waitForInitialManagedAgentAcknowledgement(context.Background(), "project", "az-1", "planned", sessionPromptHandoff{PromptPath: prompt}); err != nil {
		t.Fatalf("exact hook-backed prompt submission rejected: %v", err)
	}
	if _, err := os.Stat(prompt); err != nil {
		t.Fatalf("acknowledgement should not remove the owner-only handoff: %v", err)
	}
}

func TestInitialManagedAgentAcknowledgementConvergesFromProjectionWhenDurableReadIsBlocked(t *testing.T) {
	d, store, runner := newSessionStartAcknowledgementTestDaemon(t)
	runner.sessions["az-1"] = true
	runner.panes["az-1"] = []string{"%7"}
	runner.panePIDs["az-1"] = 123
	runner.currentCommand = "codex"
	boundAt := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	identity := daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "planned", ObservedAt: boundAt,
	}
	if err := store.UpsertManagedAgentIdentity(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	d.recordManagedAgentIdentityProjection(identity, false)
	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	d.managedAgentIdentityRead = func(context.Context, *daemonstate.RuntimeStateStore, string, string, string) (daemonstate.ManagedAgentIdentity, bool, error) {
		close(readEntered)
		<-releaseRead
		return daemonstate.ManagedAgentIdentity{}, false, context.DeadlineExceeded
	}
	poll := make(chan time.Time, 1)
	done := make(chan error, 1)
	go func() {
		done <- d.waitForInitialManagedAgentAcknowledgementWithPoll(context.Background(), store, "project", "az-1", "agent", "planned", sessionPromptHandoff{PromptPath: filepath.Join(t.TempDir(), "consumed.prompt")}, poll)
	}()
	<-readEntered
	close(releaseRead)
	acknowledgedAt := boundAt.Add(time.Second)
	acknowledged, err := store.AcknowledgeManagedAgentIdentity(context.Background(), identity, acknowledgedAt)
	if err != nil || !acknowledged {
		t.Fatalf("durable acknowledgement: acknowledged=%t err=%v", acknowledged, err)
	}
	identity.UpdatedAt = acknowledgedAt
	d.recordManagedAgentIdentityProjection(identity, true)
	poll <- acknowledgedAt
	if err := <-done; err != nil {
		t.Fatalf("projection-backed acknowledgement rejected after blocked durable read: %v", err)
	}
}

func TestInitialManagedAgentAcknowledgementIgnoresStaleAcknowledgedProjectionFromOtherIncarnation(t *testing.T) {
	d, store, runner := newSessionStartAcknowledgementTestDaemon(t)
	runner.sessions["az-1"] = true
	runner.panes["az-1"] = []string{"%7"}
	runner.panePIDs["az-1"] = 222
	runner.currentCommand = "codex"
	boundAt := time.Date(2026, time.July, 19, 10, 30, 0, 0, time.UTC)
	d.recordManagedAgentIdentityProjection(daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 111, AgentIncarnation: "incarnation-a", ObservedAt: boundAt, UpdatedAt: boundAt.Add(time.Second),
	}, true)
	current := daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 222, AgentIncarnation: "incarnation-b", ObservedAt: boundAt.Add(2 * time.Second),
	}
	if err := store.UpsertManagedAgentIdentity(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	acknowledgedAt := current.ObservedAt.Add(time.Second)
	acknowledged, err := store.AcknowledgeManagedAgentIdentity(context.Background(), current, acknowledgedAt)
	if err != nil || !acknowledged {
		t.Fatalf("durable B acknowledgement: acknowledged=%t err=%v", acknowledged, err)
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.waitForInitialManagedAgentAcknowledgementWithPoll(waitCtx, store, "project", "az-1", "agent", "incarnation-b", sessionPromptHandoff{PromptPath: filepath.Join(t.TempDir(), "consumed.prompt")}, make(chan time.Time)); err != nil {
		t.Fatalf("durable B acknowledgement hidden by stale local A: %v", err)
	}
	projected, found := d.projectedManagedAgentIdentity("project", "az-1", "agent")
	if !found || !projected.acknowledged || projected.identity.AgentIncarnation != "incarnation-b" {
		t.Fatalf("converged projection = %+v found=%t", projected, found)
	}
}

func TestInitialManagedAgentAcknowledgementDoesNotTreatFirstInventoryOmissionAsExit(t *testing.T) {
	d, store, runner := newSessionStartAcknowledgementTestDaemon(t)
	runner.sessions["az-1"] = true
	runner.sessionsWithoutPanes["az-1"] = true
	runner.currentCommand = "codex"
	observedAt := time.Date(2026, time.July, 19, 4, 13, 45, 0, time.UTC)
	identity := daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "planned", ObservedAt: observedAt,
	}
	if err := store.UpsertManagedAgentIdentity(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	acknowledged, err := store.AcknowledgeManagedAgentIdentity(context.Background(), identity, observedAt.Add(time.Second))
	if err != nil || !acknowledged {
		t.Fatalf("seed exact acknowledgement: acknowledged=%t err=%v", acknowledged, err)
	}
	runner.onListPanes = func(call int) {
		if call != 2 {
			return
		}
		delete(runner.sessionsWithoutPanes, "az-1")
		runner.panes["az-1"] = []string{"%7"}
		runner.panePIDs["az-1"] = 123
	}
	poll := make(chan time.Time, 1)
	poll <- observedAt
	err = d.waitForInitialManagedAgentAcknowledgementWithPoll(context.Background(), store, "project", "az-1", "agent", "planned", sessionPromptHandoff{PromptPath: filepath.Join(t.TempDir(), "consumed.prompt")}, poll)
	if err != nil {
		t.Fatalf("first inventory omission rejected before exact acknowledgement: %v", err)
	}
	if runner.listPanesCalls != 2 {
		t.Fatalf("list pane calls = %d, want deterministic second sample", runner.listPanesCalls)
	}
}

func TestInitialManagedAgentAcknowledgementTreatsAbsenceAfterExactPresenceAsExit(t *testing.T) {
	d, store, runner := newSessionStartAcknowledgementTestDaemon(t)
	runner.sessions["az-1"] = true
	runner.panes["az-1"] = []string{"%7"}
	runner.panePIDs["az-1"] = 123
	runner.currentCommand = "codex"
	observedAt := time.Date(2026, time.July, 19, 4, 13, 45, 0, time.UTC)
	if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "planned", ObservedAt: observedAt,
	}); err != nil {
		t.Fatal(err)
	}
	runner.onListPanes = func(call int) {
		if call == 2 {
			runner.sessionsWithoutPanes["az-1"] = true
		}
	}
	prompt := filepath.Join(t.TempDir(), "pending.prompt")
	if err := os.WriteFile(prompt, []byte("worker prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	poll := make(chan time.Time, 1)
	poll <- observedAt
	err := d.waitForInitialManagedAgentAcknowledgementWithPoll(context.Background(), store, "project", "az-1", "agent", "planned", sessionPromptHandoff{PromptPath: prompt}, poll)
	var bootstrapErr *sessionStartBootstrapError
	if !errors.As(err, &bootstrapErr) || bootstrapErr.Reason != sessionStartBootstrapAgentExited {
		t.Fatalf("error = %#v, want exact observed pane disappearance", err)
	}
	if runner.listPanesCalls != 2 {
		t.Fatalf("list pane calls = %d, want deterministic disappearance sample", runner.listPanesCalls)
	}
}

func TestInitialManagedAgentAcknowledgementTreatsNeverObservedInventoryAbsenceAsLost(t *testing.T) {
	d, store, runner := newSessionStartAcknowledgementTestDaemon(t)
	runner.sessions["az-1"] = true
	runner.sessionsWithoutPanes["az-1"] = true
	prompt := filepath.Join(t.TempDir(), "pending.prompt")
	if err := os.WriteFile(prompt, []byte("worker prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	poll := make(chan time.Time)
	err := d.waitForInitialManagedAgentAcknowledgementWithPoll(ctx, store, "project", "az-1", "agent", "planned", sessionPromptHandoff{PromptPath: prompt}, poll)
	var bootstrapErr *sessionStartBootstrapError
	if !errors.As(err, &bootstrapErr) || bootstrapErr.Reason != sessionStartBootstrapAcknowledgementLost {
		t.Fatalf("error = %#v, want never-observed inventory absence classified as acknowledgement loss", err)
	}
	if runner.listPanesCalls != 1 {
		t.Fatalf("list pane calls = %d, want one explicit never-observed sample", runner.listPanesCalls)
	}
	if identity, found, err := store.GetManagedAgentIdentity(context.Background(), "project", "az-1", "agent"); err != nil || found {
		t.Fatalf("managed identity = %+v found=%t err=%v, want absent", identity, found, err)
	}
}

func TestInitialManagedAgentAcknowledgementRejectsExactIdentityAfterShellFallback(t *testing.T) {
	d, store, runner := newSessionStartAcknowledgementTestDaemon(t)
	runner.sessions["az-1"] = true
	runner.panes["az-1"] = []string{"%7"}
	runner.panePIDs["az-1"] = 123
	runner.currentCommand = "zsh"
	if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "planned", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "consumed.prompt")
	err := d.waitForInitialManagedAgentAcknowledgement(context.Background(), "project", "az-1", "planned", sessionPromptHandoff{PromptPath: prompt})
	var bootstrapErr *sessionStartBootstrapError
	if !errors.As(err, &bootstrapErr) || bootstrapErr.Reason != sessionStartBootstrapShellFallback {
		t.Fatalf("error = %#v, want exact identity rejected as shell fallback", err)
	}
}

func TestInitialManagedAgentAcknowledgementClassifiesBootstrapFailuresDeterministically(t *testing.T) {
	for _, tc := range []struct {
		name          string
		configure     func(*sessionStartTmuxRunner)
		promptPresent bool
		cancel        bool
		wantReason    sessionStartBootstrapFailureReason
		wantText      string
	}{
		{
			name: "MCP bootstrap error and shell fallback", configure: func(r *sessionStartTmuxRunner) {
				r.sessions["az-1"] = true
				r.currentCommand = "zsh"
				r.captureOutput = "Error: required Floop MCP initialization failed"
			}, promptPresent: true, cancel: true, wantReason: sessionStartBootstrapDiagnosticError, wantText: "Floop MCP",
		},
		{
			name: "shell fallback without diagnostics", configure: func(r *sessionStartTmuxRunner) {
				r.sessions["az-1"] = true
				r.currentCommand = "zsh"
			}, promptPresent: true, cancel: true, wantReason: sessionStartBootstrapShellFallback,
		},
		{
			name: "acknowledgement loss", configure: func(r *sessionStartTmuxRunner) {
				r.sessions["az-1"] = true
				r.currentCommand = "codex"
			}, promptPresent: true, cancel: true, wantReason: sessionStartBootstrapAcknowledgementLost,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, runner := newSessionStartAcknowledgementTestDaemon(t)
			tc.configure(runner)
			prompt := filepath.Join(t.TempDir(), "launch.prompt")
			if tc.promptPresent {
				if err := os.WriteFile(prompt, []byte("worker prompt"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			if tc.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				runner.onListPanes = func(call int) {
					if call == 1 {
						cancel()
					}
				}
				ctx = cancelled
			}
			err := d.waitForInitialManagedAgentAcknowledgement(ctx, "project", "az-1", "planned", sessionPromptHandoff{PromptPath: prompt})
			var bootstrapErr *sessionStartBootstrapError
			if !errors.As(err, &bootstrapErr) || bootstrapErr.Reason != tc.wantReason {
				t.Fatalf("error = %#v, want bootstrap reason %s", err, tc.wantReason)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("error = %q, want diagnostics %q", err, tc.wantText)
			}
		})
	}
}
