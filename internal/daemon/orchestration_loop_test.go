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

func TestProjectOrchestratorReviewWakeUsesDurableInputAndCoalescesEquivalentState(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	const projectID, sessionID = "project", "project-orchestrator"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{projectID: store}
	d.tmux = tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	identity, _ := domain.NewOrchestratorIdentity(projectID, domain.ProjectOrchestrationScope())
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).Acquire(ctx, identity, sessionID, d.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	if err := d.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, identity.Scope, sessionID); err != nil {
		t.Fatal(err)
	}
	seedReadyAgentInput(t, d, runner, projectID, sessionID)
	if err := d.reconcileOrchestratorLifecycles(ctx, projectID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := d.reconcileOrchestratorLifecycles(ctx, projectID, time.Now().UTC().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(runner.inputPayloads) != 1 {
		t.Fatalf("project review continuation payloads = %d, want exactly 1", len(runner.inputPayloads))
	}
	prompt := runner.inputPayloads[0]
	if !strings.Contains(prompt, "scope=project") || !strings.Contains(prompt, "kind=review") || !strings.Contains(prompt, issueID) {
		t.Fatalf("project review prompt = %q", prompt)
	}
	if strings.Contains(prompt, "orchestrate watch") {
		t.Fatalf("project review prompt retained model watch: %q", prompt)
	}
	for _, required := range []string{"source=builtin:portable-v1", "digest=", "composition_mode=builtin", "review_epoch=" + issueID + ":", "coverage_contract=", "full diff", "analogous or sibling", "lifecycle ending", "trust and authority boundary", "every instance"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("project review prompt missing %q: %q", required, prompt)
		}
	}
}

func TestProjectOrchestratorLoopPrioritizesReviewAndPersistsCursor(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{"project": runtimeStore}
	events, cancel := d.hub.Subscribe("project", 0)
	defer cancel()
	identity, err := domain.NewOrchestratorIdentity("project", domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	lease := daemonstate.OrchestratorScopeLease{Identity: identity, SessionID: "steward", Lifecycle: domain.OrchestratorWorking}
	result, err := d.runProjectOrchestratorLoopStep(ctx, lease, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.ActionKind != "review" || result.ActionStatus != "pending:"+issueID || !result.Advanced || result.Cursor == 0 {
		t.Fatalf("loop result = %+v", result)
	}
	select {
	case event := <-events:
		if event.Event != protocol.EventOrchestrationLoopUpdated {
			t.Fatalf("event = %q", event.Event)
		}
		var body protocol.OrchestrationLoopEventBody
		if err := json.Unmarshal(event.Body, &body); err != nil || body.WatchCursor != result.Cursor || body.ActionKey != result.ActionKey {
			t.Fatalf("loop event = %+v err=%v", body, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for project loop event")
	}
	pending, err := client.PendingOrchestrationStarts(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("review-first loop started workers: %+v", pending)
	}
	recovered, found, err := runtimeStore.GetOrchestratorLoopCheckpoint(ctx, identity)
	if err != nil || !found || recovered.WatchCursor != result.Cursor || recovered.LastActionKey != result.ActionKey {
		t.Fatalf("checkpoint = %+v found=%t err=%v", recovered, found, err)
	}
}

func TestProjectOrchestratorSnapshotKeepsStartsActionableAlongsideReview(t *testing.T) {
	snapshot := protocol.OrchestrationSnapshot{
		Runnable:    []string{"new-work"},
		ReviewQueue: []protocol.OrchestrationReview{{IssueID: "ready-for-review", Actionable: true}},
		Health:      protocol.OrchestrationHealth{Healthy: true},
		Constraints: protocol.OrchestrationConstraints{AgentCapacity: 4},
	}
	kind, status := projectOrchestratorNextAction(snapshot)
	if kind != "start" || status != "idle" {
		t.Fatalf("next action = %s %s, want start while review is queued", kind, status)
	}
	snapshot.Capacity.TotalCountingCapacityCount = 4
	kind, status = projectOrchestratorNextAction(snapshot)
	if kind != "review" || status != "pending:ready-for-review" {
		t.Fatalf("full-capacity next action = %s %s, want queued review", kind, status)
	}
}

func TestProjectOrchestratorActionKeyIsRestartStableAndStateSensitive(t *testing.T) {
	snapshot := protocol.OrchestrationSnapshot{Runnable: []string{"a"}, Capacity: protocol.OrchestrationCapacity{DirectRunnableCount: 1}, Health: protocol.OrchestrationHealth{Healthy: true}}
	first, err := projectOrchestratorActionKey("project", 7, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectOrchestratorActionKey("project", 7, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("restart key changed: %q != %q", first, second)
	}
	snapshot.Runnable = append(snapshot.Runnable, "b")
	changed, err := projectOrchestratorActionKey("project", 7, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatalf("action key did not change with actionable state: %q", changed)
	}
}

func TestProjectOrchestratorUnhealthySnapshotIsObserveOnly(t *testing.T) {
	snapshot := protocol.OrchestrationSnapshot{Runnable: []string{"ready"}, Health: protocol.OrchestrationHealth{Healthy: false, Diagnostics: []string{"malformed graph"}}}
	if projectOrchestratorSnapshotActionable(snapshot) {
		t.Fatal("unhealthy project snapshot must not enter an automatic start retry loop")
	}
}

func TestProjectOrchestratorLoopMultiDaemonReplayDoesNotDuplicateCheckpointAction(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	clientA := newMigratedIssueClient(t, repoDir, slog.Default())
	clientB := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = clientA.CloseDB(); _ = clientB.CloseDB() })
	issueID := createReviewTask(t, ctx, clientA, domain.P1, "worker")
	if _, err := clientA.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(t.TempDir(), "runtime.db")
	storeA := daemonstate.NewRuntimeStateStoreAtPath(runtimePath, slog.Default())
	storeB := daemonstate.NewRuntimeStateStoreAtPath(runtimePath, slog.Default())
	t.Cleanup(func() { _ = storeA.Close(); _ = storeB.Close() })
	identity, err := domain.NewOrchestratorIdentity("project", domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	lease := daemonstate.OrchestratorScopeLease{Identity: identity, SessionID: "steward", Lifecycle: domain.OrchestratorWorking}
	daemonA := newOrchestrationReviewTestDaemon(repoDir, clientA)
	daemonA.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{"project": storeA}
	first, err := daemonA.runProjectOrchestratorLoopStep(ctx, lease, time.Now().UTC())
	if err != nil || !first.Advanced {
		t.Fatalf("first daemon = %+v err=%v", first, err)
	}
	daemonB := newOrchestrationReviewTestDaemon(repoDir, clientB)
	daemonB.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{"project": storeB}
	replayed, err := daemonB.runProjectOrchestratorLoopStep(ctx, lease, time.Now().UTC().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ActionKey != first.ActionKey || replayed.Advanced {
		t.Fatalf("replay = %+v first=%+v, want same key without duplicate advance", replayed, first)
	}
}

func TestProjectOrchestratorLoopRecoversApplyingCheckpointWithSameActionKey(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	identity, err := domain.NewOrchestratorIdentity("project", domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.AdvanceOrchestratorLoopCheckpoint(ctx, daemonstate.OrchestratorLoopCheckpoint{Identity: identity, WatchCursor: 4, LastActionKey: "persisted-before-crash", LastActionKind: "start", LastActionStatus: "applying", UpdatedAt: time.Now()}, 0)
	if err != nil || !claimed {
		t.Fatalf("seed applying checkpoint = %t, %v", claimed, err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{"project": store}
	result, err := d.runProjectOrchestratorLoopStep(ctx, daemonstate.OrchestratorScopeLease{Identity: identity, SessionID: "steward", Lifecycle: domain.OrchestratorWorking}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.ActionKey != "persisted-before-crash" || result.ActionKind != "start" || result.ActionStatus == "applying" {
		t.Fatalf("recovered result = %+v", result)
	}
	checkpoint, found, err := store.GetOrchestratorLoopCheckpoint(ctx, identity)
	if err != nil || !found || checkpoint.LastActionKey != "persisted-before-crash" || checkpoint.LastActionStatus == "applying" {
		t.Fatalf("recovered checkpoint = %+v found=%t err=%v", checkpoint, found, err)
	}
}
