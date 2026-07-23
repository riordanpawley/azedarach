package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestRuntimeSignalIngestProjectsTerminalAgentError(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectID := "proj-signals"
	issueID := "dae"
	parentSessionID := "az-dae"
	if err := d.sessionRuntimeStateStore(projectID).UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID: parentSessionID, IssueID: issueID, State: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "runtime-signal-agent-capacity",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandRuntimeSignalIngest,
		Body: mustMarshal(t, protocol.RuntimeSignalIngestCommandBody{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   issueID,
			SessionID: parentSessionID + ".pane-42",
			Agent:     "codex",
			Hook:      "idle_prompt",
			Event:     "idle_prompt",
			Log:       true,
			Payload: map[string]any{
				"notification": map[string]any{
					"message": "Selected model is at capacity. Please try a different model.",
				},
			},
		}),
	})
	if err != nil {
		t.Fatalf("runtime.signal.ingest command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("runtime.signal.ingest response not ok: %+v", resp.Error)
	}
	var result protocol.RuntimeSignalIngestResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal runtime signal response: %v", err)
	}
	foundContinuation := false
	for _, stage := range result.Stages {
		if stage.Name == "orchestrator_continuation" && stage.OK {
			foundContinuation = true
			break
		}
	}
	if !foundContinuation {
		t.Fatalf("runtime signal stages = %+v, want successful orchestrator continuation reconciliation", result.Stages)
	}

	canonical, found, err := d.sessionRuntimeStateStore(projectID).GetSessionState(context.Background(), projectID, parentSessionID)
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found || canonical.Activity != "error" || canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v found=%t, want error/hooks", canonical, found)
	}
	events := d.listHookLogEvents(projectID, 10)
	if len(events) != 1 || events[0].Level != "error" || events[0].Blocking == nil || !*events[0].Blocking ||
		events[0].Message != "codex hook: idle_prompt (terminal agent failure: model_capacity)" {
		t.Fatalf("hook log events = %+v, want canonical terminal-failure metadata", events)
	}
}

func TestRuntimeSignalTerminalAgentErrorQueuesRetriesAndDeduplicatesOrchestratorWake(t *testing.T) {
	ctx := context.Background()
	repoDir := initRuntimeSignalGitRepo(t)
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t),
	}); err != nil {
		t.Fatal(err)
	}

	const projectID, orchestratorSessionID = "project", "project-orchestrator"
	workerSessionID := "az-" + issueID
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	runner.sessions[orchestratorSessionID] = true
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{projectID: store}
	d.tmux = tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	identity, err := domain.NewOrchestratorIdentity(projectID, domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).Acquire(ctx, identity, orchestratorSessionID, d.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	if err := d.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, identity.Scope, orchestratorSessionID); err != nil {
		t.Fatal(err)
	}
	seedReadyAgentInput(t, d, runner, projectID, orchestratorSessionID)
	if err := store.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID: workerSessionID, IssueID: issueID, State: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	d.agentInput = newAgentInputDeliveryService(d.sessionRuntimeStateStoreIfConfigured, d.issueClientForProject, refusingAuthoritativeReceiver{outcome: "not_ready"}, "terminal-wake-unavailable")
	d.agentInput.deliveryEligible = d.agentInputDeliveryEligible
	ingest := func(requestID string) {
		t.Helper()
		resp, err := d.command(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID(requestID),
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         protocol.CommandRuntimeSignalIngest,
			Body: mustMarshal(t, protocol.RuntimeSignalIngestCommandBody{
				Source:    protocol.RuntimeSignalSourceAgentHook,
				Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
				IssueID:   issueID,
				SessionID: workerSessionID + ".pane-42",
				Agent:     "codex",
				Hook:      "idle_prompt",
				Event:     "idle_prompt",
				Payload: map[string]any{
					"notification": map[string]any{
						"message": "Selected model is at capacity. Please try a different model.",
					},
				},
			}),
		})
		if err != nil || !resp.OK {
			t.Fatalf("runtime.signal.ingest response=%+v err=%v", resp.Error, err)
		}
	}

	ingest("terminal-wake-unavailable")
	pending, err := client.ListPendingAgentInputDeliveryIntents(ctx, projectID, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].State != "queued" || pending[0].Request.SessionID != orchestratorSessionID {
		t.Fatalf("pending terminal wake intents = %+v, want one queued exact-orchestrator intent", pending)
	}
	if len(runner.inputPayloads) != 0 {
		t.Fatalf("unavailable terminal wake delivered payloads = %+v, want queued only", runner.inputPayloads)
	}

	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}, sink: func(payload string) {
		runner.inputPayloads = append(runner.inputPayloads, payload)
	}}
	d.agentInput = newAgentInputDeliveryService(d.sessionRuntimeStateStoreIfConfigured, d.issueClientForProject, receiver, "terminal-wake-retry")
	d.agentInput.deliveryEligible = d.agentInputDeliveryEligible
	if err := d.agentInput.RetryPending(ctx, projectID, 10); err != nil {
		t.Fatal(err)
	}
	if receiver.calls != 1 || len(runner.inputPayloads) != 1 || !strings.Contains(runner.inputPayloads[0], issueID) {
		t.Fatalf("retried terminal wake calls=%d payloads=%+v, want one issue-bound native delivery", receiver.calls, runner.inputPayloads)
	}

	ingest("terminal-wake-duplicate")
	if err := d.reconcileOrchestratorLifecycles(ctx, projectID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := d.agentInput.RetryPending(ctx, projectID, 10); err != nil {
		t.Fatal(err)
	}
	if receiver.calls != 1 || len(runner.inputPayloads) != 1 {
		t.Fatalf("equivalent terminal wake calls=%d payloads=%+v, want exactly one delivery", receiver.calls, runner.inputPayloads)
	}
}

func TestRuntimeSignalIngestKeepsOrdinaryIdlePromptWaiting(t *testing.T) {
	repoDir := initRuntimeSignalGitRepo(t)
	d := New(Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	projectID := "proj-signals-idle"
	parentSessionID := "az-dae-idle"
	if err := d.sessionRuntimeStateStore(projectID).UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID: parentSessionID, IssueID: "dae", State: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "runtime-signal-agent-ordinary-idle",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Command:         protocol.CommandRuntimeSignalIngest,
		Body: mustMarshal(t, protocol.RuntimeSignalIngestCommandBody{
			Source:    protocol.RuntimeSignalSourceAgentHook,
			Kind:      protocol.RuntimeSignalKindAgentActivityChanged,
			IssueID:   "dae",
			SessionID: parentSessionID + ".pane-42",
			Agent:     "codex",
			Hook:      "idle_prompt",
			Event:     "idle_prompt",
			Payload:   map[string]any{"message": "Codex is waiting for input"},
		}),
	})
	if err != nil {
		t.Fatalf("runtime.signal.ingest command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("runtime.signal.ingest response not ok: %+v", resp.Error)
	}

	canonical, found, err := d.sessionRuntimeStateStore(projectID).GetSessionState(context.Background(), projectID, parentSessionID)
	if err != nil {
		t.Fatalf("GetSessionState canonical: %v", err)
	}
	if !found || canonical.Activity != "waiting" || canonical.ActivitySource != "hooks" {
		t.Fatalf("canonical session projection = %+v found=%t, want waiting/hooks", canonical, found)
	}
}

func TestRuntimeSignalAgentLifecycleProjectsExplicitError(t *testing.T) {
	command, activity, ok := runtimeSignalAgentLifecycle(protocol.RuntimeSignalIngestCommandBody{
		Event:    "idle_prompt",
		Activity: "error",
	})
	if !ok || command != "session.pause" || activity != "error" {
		t.Fatalf("lifecycle = %q/%q/%t, want session.pause/error/true", command, activity, ok)
	}
	if !orchestratorActivityWakeRequired(activity) {
		t.Fatal("terminal error activity did not trigger orchestrator continuation reconciliation")
	}
}

func TestSessionHookActivityPreservesTerminalError(t *testing.T) {
	now := time.Now().UTC()
	got := sessionHookActivityByIssueKeyFromSessions([]daemonstate.Session{
		{
			ID:             "az-dae.pane-42",
			IssueID:        "dae",
			State:          daemonstate.SessionStatePaused,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "error",
			ActivitySource: "hooks",
			UpdatedAt:      now,
		},
	}, "az")[sessionKey("dae")]
	activity, source := sessionActivityLabel(got)
	if activity != "error" || source != "hooks" {
		t.Fatalf("activity = %q/%q, want error/hooks", activity, source)
	}
	if updatedAt := sessionHookActivityUpdatedAt(got); !updatedAt.Equal(now) {
		t.Fatalf("updated at = %s, want %s", updatedAt, now)
	}
}

func TestSessionHookActivityBusySiblingTakesPriorityOverError(t *testing.T) {
	now := time.Now().UTC()
	got := sessionHookActivityByIssueKeyFromSessions([]daemonstate.Session{
		{ID: "az-dae.pane-42", IssueID: "dae", State: daemonstate.SessionStatePaused, Activity: "error", UpdatedAt: now},
		{ID: "az-dae.pane-43", IssueID: "dae", State: daemonstate.SessionStateRunning, Activity: "busy", UpdatedAt: now.Add(time.Second)},
	}, "az")[sessionKey("dae")]
	activity, source := sessionActivityLabel(got)
	if activity != "busy" || source != "hooks" {
		t.Fatalf("activity = %q/%q, want busy/hooks", activity, source)
	}
}
