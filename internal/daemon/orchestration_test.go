package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestRootedOrchestratorIdleWakeCarriesDurableCursorAndDirectNestedRoots(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-continuation"
	repoDir := t.TempDir()
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })

	rootID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	nestedID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "Nested", Type: domain.TypeEpic, Status: domain.StatusInProgress, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	leafID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "Nested leaf", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &nestedID})
	if err != nil {
		t.Fatal(err)
	}

	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	const parentSession = "root-orchestrator"
	if _, err := store.AcquireOrchestratorScopeLease(ctx, identity, parentSession, func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceOrchestratorScopeCursor(ctx, identity, 7); err != nil {
		t.Fatal(err)
	}
	for _, session := range []daemonstate.Session{
		{ID: parentSession, IssueID: rootID, Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: rootID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: time.Now().UTC()},
		{ID: "nested-orchestrator", IssueID: nestedID, Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: nestedID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: time.Now().UTC()},
		{ID: "nested-leaf", IssueID: leafID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: time.Now().UTC()},
	} {
		if err := upsertSessionStateFixture(store, ctx, projectID, session); err != nil {
			t.Fatal(err)
		}
	}

	runner := newSessionStartTmuxRunner()
	runner.sessions[parentSession] = true
	runner.sessions["nested-orchestrator"] = true
	runner.sessions["nested-leaf"] = true
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:                 issueClient,
		issueClientsByProject:  map[string]*issues.Client{projectID: issueClient},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		tmux:                   tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	seedReadyAgentInput(t, d, runner, projectID, parentSession)
	if err := d.reconcileOrchestratorLifecycles(ctx, projectID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(runner.inputPayloads) != 1 {
		t.Fatalf("continuation payloads = %d, want 1", len(runner.inputPayloads))
	}
	prompt := runner.inputPayloads[0]
	for _, want := range []string{"cursor=7", "--since 7", nestedID, "without flattening"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "direct nested roots ["+leafID+"]") || strings.Contains(prompt, ","+leafID+"]") {
		t.Fatalf("prompt flattened nested descendant %s: %s", leafID, prompt)
	}
}

func TestBootstrapRecoveryResumesReplacedRootedOrchestratorOnceWithDurableCursor(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	projectID := d.canonicalProjectID(protocol.DefaultProjectID)

	rootID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "Recovered root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	nestedID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "Recovered nested root", Type: domain.TypeEpic, Status: domain.StatusInProgress, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "Recovered nested leaf", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &nestedID}); err != nil {
		t.Fatal(err)
	}

	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(repoDir, "runtime.db")
	storeA := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	if _, err := storeA.AcquireOrchestratorScopeLease(ctx, identity, "expired-auth-session", func(context.Context, string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.AdvanceOrchestratorScopeCursor(ctx, identity, 23); err != nil {
		t.Fatal(err)
	}
	if err := storeA.Close(); err != nil {
		t.Fatal(err)
	}

	storeB := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeB.Close() })
	replaced, err := storeB.AcquireOrchestratorScopeLease(ctx, identity, "replacement-session", func(_ context.Context, sessionID string) (bool, error) {
		if sessionID != "expired-auth-session" {
			t.Fatalf("replacement probed %q", sessionID)
		}
		return false, nil
	})
	if err != nil || replaced.Disposition != daemonstate.OrchestratorLeaseRecoveredStale || replaced.Lease.Cursor != 23 {
		t.Fatalf("replacement = %+v err=%v", replaced, err)
	}
	if err := upsertSessionStateFixture(storeB, ctx, projectID, daemonstate.Session{
		ID: "replacement-session", IssueID: rootID, Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: rootID, State: daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	runner := newSessionStartTmuxRunner()
	runner.sessions["replacement-session"] = true
	d.issueClientsByProject = map[string]*issues.Client{projectID: issueClient}
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{projectID: storeB}
	d.tmux = tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedReadyAgentInput(t, d, runner, projectID, "replacement-session")
	d.syncBootstrapFn = func(context.Context) error { return nil }
	if err := d.bootstrapSyncOrchestrator(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.bootstrapSyncOrchestrator(ctx); err != nil {
		t.Fatal(err)
	}
	if len(runner.inputPayloads) != 1 {
		t.Fatalf("recovery continuation payloads = %d, want exactly 1", len(runner.inputPayloads))
	}
	prompt := runner.inputPayloads[0]
	for _, want := range []string{"cursor=23", "--since 23", nestedID} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("recovery prompt missing %q: %s", want, prompt)
		}
	}
}

func TestRootedOrchestratorContinuationSuppressesCompleteAndHumanWait(t *testing.T) {
	nested := protocol.OrchestrationSnapshot{NestedRoots: []protocol.OrchestrationNestedRoot{{IssueID: "nested", Status: "startable"}}}
	if rootedOrchestratorContinuationRequired(true, nested) {
		t.Fatal("complete-check pass still requires continuation")
	}
	nested.Interactions = []domain.InteractionRequest{{ID: "human-acceptance"}}
	if rootedOrchestratorContinuationRequired(false, nested) {
		t.Fatal("unresolved human acceptance still requires continuation")
	}
	nested.Interactions = nil
	nested.NestedRoots[0].Status = "not_counting_capacity"
	nested.NestedRoots[0].ExclusionReasons = []string{"lifecycle-backlog"}
	if rootedOrchestratorContinuationRequired(false, nested) {
		t.Fatal("non-actionable backlog-contained nested root still requires continuation")
	}
}

func TestProjectOrchestrationCompletionIsDaemonOwned(t *testing.T) {
	complete := projectOrchestrationCompletion(protocol.OrchestrationSnapshot{Health: protocol.OrchestrationHealth{Healthy: true}})
	if !complete.Pass || len(complete.Reasons) != 0 {
		t.Fatalf("complete = %+v", complete)
	}
	incomplete := projectOrchestrationCompletion(protocol.OrchestrationSnapshot{
		Health:       protocol.OrchestrationHealth{Healthy: false, OpenIssueCount: 2, Diagnostics: []string{"inspection truncated"}},
		Reviews:      []protocol.OrchestrationCandidate{{IssueID: "review"}},
		Interactions: []domain.InteractionRequest{{ID: "human"}},
	})
	if incomplete.Pass {
		t.Fatalf("incomplete = %+v", incomplete)
	}
	joined := strings.Join(incomplete.Reasons, "\n")
	for _, want := range []string{"2 open issues remain", "1 review requests remain", "1 human interactions remain unresolved", "inspection truncated"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("reasons missing %q: %s", want, joined)
		}
	}
}

func TestAcquireRootedOrchestratorLeasePersistsSessionAuthority(t *testing.T) {
	projectID := "proj-acquire-root"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	runner.sessions["root-session"] = true
	d := &Daemon{
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		tmux:                   tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	if err := d.acquireRootedOrchestratorLease(context.Background(), projectID, "az-root", "root-session"); err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, domain.OrchestrationScope{Kind: domain.OrchestrationScopeRooted, RootIssueID: "az-root"})
	if err != nil {
		t.Fatal(err)
	}
	lease, found, err := store.GetOrchestratorScopeLease(context.Background(), identity)
	if err != nil || !found || lease.SessionID != "root-session" {
		t.Fatalf("lease = %+v, found=%v err=%v", lease, found, err)
	}
}

func TestMergeOrchestrationSnapshotOrdersProjectResultsDeterministically(t *testing.T) {
	dst := protocol.OrchestrationSnapshot{Blocked: map[string]string{}, Runnable: []string{"z"}, Active: []string{"c"}}
	mergeOrchestrationSnapshot(&dst, protocol.OrchestrationSnapshot{
		Runnable:       []string{"a"},
		Active:         []string{"b"},
		Blocked:        map[string]string{"x": "dependency"},
		Capacity:       protocol.OrchestrationCapacity{DirectRunnableCount: 1, DirectActiveCount: 1},
		ActiveSessions: []protocol.OrchestrationSession{{IssueID: "z"}, {IssueID: "a"}},
	})
	sortOrchestrationSnapshot(&dst)
	if !reflect.DeepEqual(dst.Runnable, []string{"a", "z"}) || !reflect.DeepEqual(dst.Active, []string{"b", "c"}) {
		t.Fatalf("project ordering = runnable %v active %v", dst.Runnable, dst.Active)
	}
	if got := []string{dst.ActiveSessions[0].IssueID, dst.ActiveSessions[1].IssueID}; !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("session ordering = %v", got)
	}
	if dst.Blocked["x"] != "dependency" || dst.Capacity.DirectRunnableCount != 1 || dst.Capacity.DirectActiveCount != 1 {
		t.Fatalf("merged project snapshot = %+v", dst)
	}
}

func TestFinalizeOrchestrationSnapshotSourceCoversSemanticResultOnly(t *testing.T) {
	first := protocol.OrchestrationSnapshot{
		GeneratedAt: time.Now().UTC(),
		Revision:    7,
		Source:      protocol.MaterializedSnapshotMetadata{IssueChecksum: "issues", RuntimeChecksum: "runtime"},
		Runnable:    []string{"az-1"},
	}
	second := first
	second.GeneratedAt = first.GeneratedAt.Add(time.Minute)
	second.Revision = 99
	finalizeOrchestrationSnapshotSource(&first)
	finalizeOrchestrationSnapshotSource(&second)
	if first.Source.SemanticChecksum == "" || first.Source.SemanticChecksum != second.Source.SemanticChecksum {
		t.Fatalf("normalized checksums differ: %q/%q", first.Source.SemanticChecksum, second.Source.SemanticChecksum)
	}
	second.Interactions = []domain.InteractionRequest{{ID: "interaction-1"}}
	finalizeOrchestrationSnapshotSource(&second)
	if first.Source.SemanticChecksum == second.Source.SemanticChecksum {
		t.Fatal("interaction projection did not change orchestration semantic checksum")
	}
}

func TestHybridActiveSessionCountRejectsProjectionTmuxDivergence(t *testing.T) {
	identity, err := domain.NewOrchestratorIdentity("project", domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	lease := daemonstate.OrchestratorScopeLease{Identity: identity, SessionID: "orchestrator"}
	issues := map[string]struct{}{"worker": {}}
	tests := []struct {
		name      string
		projected []daemonstate.Session
		live      []string
		want      int
	}{
		{name: "projection only", projected: []daemonstate.Session{{ID: "worker", IssueID: "worker", State: daemonstate.SessionStateRunning}}, want: 1},
		{name: "tmux only", live: []string{"worker"}, want: 1},
		{name: "both count once", projected: []daemonstate.Session{{ID: "worker", IssueID: "worker", State: daemonstate.SessionStateRunning}}, live: []string{"worker"}, want: 1},
		{name: "own orchestrator excluded", live: []string{"orchestrator"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hybridActiveSessionCount(lease, tt.projected, tt.live, issues); got != tt.want {
				t.Fatalf("active = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestOrchestratorWakeReasonPrioritizesActionableEvents(t *testing.T) {
	updated := time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)
	if got := orchestratorWakeReason(domain.OrchestratorLifecycleFacts{ReviewRequests: 1, OpenIssues: 1}, updated, updated.Add(-time.Second)); got != domain.OrchestratorWakeReviewRequest {
		t.Fatalf("review wake = %q", got)
	}
	if got := orchestratorWakeReason(domain.OrchestratorLifecycleFacts{OpenIssues: 1}, updated, updated.Add(-time.Second)); got != domain.OrchestratorWakeOpenWork {
		t.Fatalf("work wake = %q", got)
	}
	if got := orchestratorWakeReason(domain.OrchestratorLifecycleFacts{}, updated, updated.Add(-time.Second)); got != domain.OrchestratorWakeHumanAnswer {
		t.Fatalf("answer wake = %q", got)
	}
	if got := orchestratorWakeReason(domain.OrchestratorLifecycleFacts{}, updated, updated); got != "" {
		t.Fatalf("unchanged wake = %q", got)
	}
}

func TestSortOrchestrationSnapshotUsesPriorityAgeAndID(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	tasks := map[string]domain.Task{
		"p2":  {ID: "p2", Priority: domain.P2, UpdatedAt: old},
		"b":   {ID: "b", Priority: domain.P1, UpdatedAt: newer},
		"a":   {ID: "a", Priority: domain.P1, UpdatedAt: newer},
		"old": {ID: "old", Priority: domain.P1, UpdatedAt: old},
	}
	snapshot := protocol.OrchestrationSnapshot{Runnable: []string{"p2", "b", "a", "old"}}
	sortOrchestrationSnapshot(&snapshot, tasks)
	if want := []string{"old", "a", "b", "p2"}; !reflect.DeepEqual(snapshot.Runnable, want) {
		t.Fatalf("runnable order = %v, want %v", snapshot.Runnable, want)
	}
}

func TestOrchestrationIntentRejectsInvalidShapeBeforeMutation(t *testing.T) {
	authority := daemonOrchestrationAuthority{daemon: &Daemon{}}
	_, err := authority.Apply(context.Background(), "project", protocol.OrchestrationIntentRequest{
		Scope:     domain.ProjectOrchestrationScope(),
		Kind:      "delete-everything",
		IntentKey: "intent-1",
	})
	if err == nil {
		t.Fatal("invalid intent unexpectedly succeeded")
	}
}

func TestClaimAndSubmitStartCompensatesInvalidLaunchResult(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(protocol.OperationSubmitResponseBody{})
	if err != nil {
		t.Fatal(err)
	}
	authority := daemonOrchestrationAuthority{
		daemon: &Daemon{issueClientsByProject: map[string]*issues.Client{"project": client}},
		submitStart: func(_ context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
			return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: body}
		},
	}
	scope, err := domain.RootedOrchestrationScope("root")
	if err != nil {
		t.Fatal(err)
	}
	_, err = authority.claimAndSubmitStart(ctx, "project", protocol.OrchestrationIntentRequest{Scope: scope, IntentKey: "start-invalid", ActorID: "orchestrator", RepoDir: t.TempDir()}, issueID)
	var invalid *invalidOrchestrationLaunchError
	if !errors.As(err, &invalid) || invalid.Field != "operation_id" {
		t.Fatalf("claimAndSubmitStart error = %v, want typed missing operation_id", err)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusOpen || task.Ownership != nil || task.HasTmuxSession {
		t.Fatalf("failed launch left partial claim/session state: %+v", task)
	}
	pending, err := client.PendingOrchestrationStarts(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("failed launch left pending start attempts: %+v", pending)
	}
}

func TestClaimAndSubmitStartUsesFreshOperationForNewIntentAfterCompensation(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	client := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}

	operationByDedupe := map[string]string{}
	submittedPayloads := map[string]json.RawMessage{}
	submitCalls := 0
	authority := daemonOrchestrationAuthority{
		daemon: &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}},
		submitStart: func(_ context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
			submitCalls++
			var body protocol.OperationSubmitRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("decode operation submit: %v", err)
			}
			operationID := operationByDedupe[body.DedupeKey]
			created := operationID == ""
			if created {
				operationID = fmt.Sprintf("op-%d", len(operationByDedupe)+1)
				operationByDedupe[body.DedupeKey] = operationID
				submittedPayloads[operationID] = append(json.RawMessage(nil), body.Payload...)
			}
			encoded, err := json.Marshal(protocol.OperationSubmitResponseBody{Created: created, Operation: protocol.OperationRecord{
				OperationID: naming.OperationID(operationID), ProjectID: "project", IssueID: naming.IssueID(issueID),
				Kind: "session.start", DedupeKey: body.DedupeKey, State: protocol.OperationStateQueued,
			}})
			if err != nil {
				t.Fatal(err)
			}
			return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: encoded}
		},
	}

	firstRequest := protocol.OrchestrationIntentRequest{IntentKey: "operator-attempt-1", ActorID: "orchestrator", RepoDir: t.TempDir()}
	first, err := authority.claimAndSubmitStartWithPrompt(ctx, "project", firstRequest, issueID, "obsolete prompt")
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatalf("close issue store before restart replay: %v", err)
	}
	client = newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	authority.daemon.issueClientsByProject["project"] = client

	replayed, err := authority.claimAndSubmitStartWithPrompt(ctx, "project", firstRequest, issueID, "ignored replay payload")
	if err != nil {
		t.Fatalf("same live intent after restart: %v", err)
	}
	if replayed.OperationID != first.OperationID {
		t.Fatalf("same live intent operation = %s, want %s", replayed.OperationID, first.OperationID)
	}
	authority.daemon.reconcileOrchestrationStartOperation(ctx, daemonops.Record{
		ID: first.OperationID, ProjectID: "project", IssueID: issueID, Kind: "session.start",
		DedupeKey: operationDedupeKeyForTest(t, submittedPayloads, first.OperationID, operationByDedupe), State: daemonops.StateFailed,
		ErrorMessage: "obsolete launch failed",
	})

	secondRequest := protocol.OrchestrationIntentRequest{IntentKey: "operator-attempt-2", ActorID: "orchestrator", RepoDir: firstRequest.RepoDir}
	second, err := authority.claimAndSubmitStartWithPrompt(ctx, "project", secondRequest, issueID, "current prompt")
	if err != nil {
		t.Fatalf("retry with fresh intent: %v", err)
	}
	if second.OperationID == first.OperationID {
		t.Fatalf("fresh intent replayed compensated operation %s", second.OperationID)
	}
	if submitCalls != 3 || len(operationByDedupe) != 2 {
		t.Fatalf("submit calls=%d operation dedupe entries=%d, want one replay and two distinct attempts", submitCalls, len(operationByDedupe))
	}
	var payload sessionCommandBody
	if err := json.Unmarshal(submittedPayloads[second.OperationID], &payload); err != nil {
		t.Fatalf("decode retry payload: %v", err)
	}
	if payload.Prompt != "current prompt" {
		t.Fatalf("retry prompt = %q, want current payload", payload.Prompt)
	}
}

func operationDedupeKeyForTest(t *testing.T, payloads map[string]json.RawMessage, operationID string, operations map[string]string) string {
	t.Helper()
	if _, ok := payloads[operationID]; !ok {
		t.Fatalf("missing submitted payload for operation %s", operationID)
	}
	for dedupeKey, id := range operations {
		if id == operationID {
			return dedupeKey
		}
	}
	t.Fatalf("missing dedupe key for operation %s", operationID)
	return ""
}

func TestTerminalSessionStartFailureCompensatesOrchestrationClaim(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.BeginOrchestrationStart(ctx, "project", issueID, "start-failure", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteOrchestrationStart(ctx, attempt, "op-failure"); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	d.reconcileOrchestrationStartOperation(ctx, daemonops.Record{ID: "op-failure", ProjectID: "project", IssueID: issueID, Kind: "session.start", DedupeKey: attempt.DedupeKey, State: daemonops.StateFailed, ErrorMessage: "tmux launch failed"})
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusOpen || task.Ownership != nil || task.HasTmuxSession {
		t.Fatalf("terminal failure left partial claim/session state: %+v", task)
	}
}

func TestTerminalSessionStartFailurePreservesClaimForLiveRuntime(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.BeginOrchestrationStart(ctx, "project", issueID, "start-live", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteOrchestrationStart(ctx, attempt, "op-live"); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:                   Config{Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{"project": client},
		tmux:                  tmux.NewClient(&sessionStartCompensationTmuxRunner{live: true}, slog.Default()),
	}
	d.reconcileOrchestrationStartOperation(ctx, daemonops.Record{ID: "op-live", ProjectID: "project", IssueID: issueID, Kind: "session.start", DedupeKey: attempt.DedupeKey, ResourceKeys: []string{"session:worker-session"}, State: daemonops.StateFailed, ErrorMessage: "cleanup failed"})
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Ownership == nil || task.Ownership.OwnerID != "orchestrator" {
		t.Fatalf("live runtime lost orchestration claim: %+v", task.Ownership)
	}
}

func TestProjectOrchestrationApplyAutomaticallyBacklogsPrematureCandidate(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	id, err := client.Create(ctx, issues.CreateTaskParams{Title: "Thin candidate", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "proj", id, issues.OwnershipClaimParams{OwnerID: "steward", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseExecution}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	result, err := (daemonOrchestrationAuthority{daemon: d}).Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "wave-1", ActorID: "steward"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Routed) != 1 || result.Routed[0].IssueID != id || result.Routed[0].Kind != domain.OrchestrationRouteBacklog {
		t.Fatalf("routed = %+v", result.Routed)
	}
	task, err := client.GetWithRuntime(ctx, "proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if task.State.Workflow() != domain.IssueWorkflowBacklog || task.Ownership != nil {
		t.Fatalf("routed task = %+v", task)
	}
}

func TestProjectOrchestrationSnapshotIncludesOnlyLiveUnparentedRoots(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Root", Description: "Root scope", Acceptance: "Root done", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Child", Description: "Child scope", Acceptance: "Child done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	closedID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Closed root", Type: domain.TypeTask, Status: domain.StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "steward", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Candidates) != 1 || snapshot.Candidates[0].IssueID != rootID {
		t.Fatalf("project candidates = %+v, want only root %s (child %s and closed root %s excluded)", snapshot.Candidates, rootID, childID, closedID)
	}
	for _, issueID := range append(append([]string(nil), snapshot.Runnable...), snapshot.Active...) {
		if issueID == childID || issueID == closedID {
			t.Fatalf("project actionable IDs contain out-of-scope issue %s: runnable=%v active=%v", issueID, snapshot.Runnable, snapshot.Active)
		}
	}
	for _, review := range snapshot.ReviewQueue {
		if review.IssueID == childID || review.IssueID == closedID {
			t.Fatalf("project review queue contains out-of-scope issue: %+v", snapshot.ReviewQueue)
		}
	}
}

func TestProjectOrchestrationRejectsDirectStartOfParentedTicket(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Child", Description: "Child scope", Acceptance: "Child done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	result, err := d.orchestrationAuthority().Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "reject-child", ActorID: "steward", IssueIDs: []string{childID}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped[childID] != "outside-project-root-candidate-scope" || len(result.Started) != 0 {
		t.Fatalf("direct child start result = %+v", result)
	}
	if len(result.Results) != 1 || result.Results[0].Summary.Role != domain.WorkflowRoleWorker || result.Results[0].Summary.Status != "skipped" {
		t.Fatalf("bounded worker result = %+v", result.Results)
	}
}

func TestRootedOrchestrationReviewQueueStopsAtDirectChildren(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Nested root", Type: domain.TypeEpic, Status: domain.StatusInReview, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	grandchildID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Nested worker", Type: domain.TypeTask, Status: domain.StatusInReview, ParentID: &childID})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: scope, ActorID: "parent-orchestrator", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var reviewIDs []string
	for _, review := range snapshot.ReviewQueue {
		reviewIDs = append(reviewIDs, review.IssueID)
	}
	if !slices.Contains(reviewIDs, childID) {
		t.Fatalf("review queue = %v, want direct child %s", reviewIDs, childID)
	}
	if slices.Contains(reviewIDs, grandchildID) {
		t.Fatalf("review queue = %v, nested orchestrator must own descendant %s", reviewIDs, grandchildID)
	}
	result, err := d.orchestrationAuthority().Apply(ctx, "proj", protocol.OrchestrationIntentRequest{
		Scope: scope, Kind: protocol.OrchestrationIntentStart, IntentKey: "reject-grandchild", ActorID: "parent-orchestrator", IssueIDs: []string{grandchildID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Skipped[grandchildID]; !strings.Contains(got, "outside-root-direct-child-scope") || !strings.Contains(got, "direct parent orchestrator") {
		t.Fatalf("skipped[%s] = %q, want chain-of-command refusal", grandchildID, got)
	}
	reviewResult, err := d.orchestrationAuthority().Apply(ctx, "proj", protocol.OrchestrationIntentRequest{
		Scope: scope, Kind: protocol.OrchestrationIntentReviewReturn, IntentKey: "reject-grandchild-review", ActorID: "parent-orchestrator", IssueIDs: []string{grandchildID},
		Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "nested worker finding"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewResult.Skipped[grandchildID]; !strings.Contains(got, "outside-root-direct-child-scope") {
		t.Fatalf("review skipped[%s] = %q, want chain-of-command refusal", grandchildID, got)
	}
}

func TestRootedOrchestrationSnapshotRejectsStaleRuntimeOverlayAfterCanonicalConvergence(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Child", Description: "ready", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	materializer, err := d.bootstrapEmbeddedProjectReadMaterializer(ctx, "proj", false)
	if err != nil {
		t.Fatal(err)
	}
	materializer.mu.Lock()
	staleChild := materializer.tasks[childID]
	materializer.runtimeKeys.remove("task-runtime", childID, staleChild)
	staleChild.HasTmuxSession = true
	staleChild.Session = &domain.Session{IssueID: naming.IssueID(childID), State: domain.SessionBusy, Activity: "stale-live", UpdatedAt: time.Now().UTC()}
	materializer.tasks[childID] = staleChild
	materializer.runtimeKeys.add("task-runtime", childID, staleChild)
	materializer.metadata.RuntimeChecksum = materializer.runtimeKeys.sum()
	materializer.metadata.SemanticChecksum = joinedMaterializedChecksum(materializer.metadata)
	materializer.mu.Unlock()
	failedRefresh := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshRuntime)
	materializer.finishAuthoritativeReadRefresh(failedRefresh, errors.New("unrelated runtime refresh deadline"))
	d.materializers = map[string]*projectReadMaterializer{"proj": materializer}
	d.materializersStarted = true
	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: scope, ActorID: "parent-orchestrator", Limit: 20})
	if err == nil || !strings.Contains(err.Error(), "runtime refresh deadline") {
		t.Fatalf("rooted orchestration snapshot = %+v err=%v, want fail-visible stale runtime health", snapshot, err)
	}
	if len(snapshot.Runnable) != 0 || len(snapshot.Active) != 0 {
		t.Fatalf("stale runtime overlay produced classification: runnable=%v active=%v", snapshot.Runnable, snapshot.Active)
	}
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, "runtime refresh deadline") {
		t.Fatalf("canonical convergence falsely cleared runtime health: %q", got)
	}
}

func TestProjectOrchestrationExplicitIssueRoutesOnlyRequestedRoot(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	requestedID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Requested thin root", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	unrelatedID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Unrelated thin root", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	result, err := d.orchestrationAuthority().Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "explicit-root", ActorID: "steward", IssueIDs: []string{requestedID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Routed) != 1 || result.Routed[0].IssueID != requestedID {
		t.Fatalf("routed = %+v, want only requested root %s", result.Routed, requestedID)
	}
	unrelated, err := client.GetWithRuntime(ctx, "proj", unrelatedID)
	if err != nil {
		t.Fatal(err)
	}
	if unrelated.State.Workflow() != domain.IssueWorkflowOpen {
		t.Fatalf("unrelated root workflow = %s, want open", unrelated.State.Workflow())
	}
}

func TestProjectOrchestrationExplicitStartQueuesBeforeSnapshotAdmissionContention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Requested", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	d.orchestrationSnapshotBuild = func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		return protocol.OrchestrationSnapshot{}, orchestrationAdmissionContentionError(protocol.OrchestrationAdmissionProjectionCheckpoint, errors.New("checkpoint unavailable"))
	}
	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "explicit-contended", ActorID: "steward", IssueIDs: []string{issueID}}
	result, err := d.orchestrationAuthority().Apply(ctx, "proj", request)
	if err != nil {
		t.Fatalf("explicit start should return durable queued progress: %v", err)
	}
	if len(result.Pending) != 1 || result.Pending[0].IssueID != issueID || result.Pending[0].Phase != "projection_source_checkpoint" || !result.Pending[0].Retryable {
		t.Fatalf("queued progress = %+v", result.Pending)
	}
	if len(result.Results) != 1 || result.Results[0].Summary.Role != domain.WorkflowRoleWorker || result.Results[0].Summary.Status != "pending" {
		t.Fatalf("bounded pending worker result = %+v", result.Results)
	}
	queued, err := client.PendingRequestedOrchestrationStarts(ctx, "proj")
	if err != nil || len(queued) != 1 || queued[0].IssueID != issueID || queued[0].IntentKey != request.IntentKey {
		t.Fatalf("durable requested starts = %+v err=%v", queued, err)
	}

	reopened := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reopened.CloseDB() })
	recovered, err := reopened.PendingRequestedOrchestrationStarts(ctx, "proj")
	if err != nil || len(recovered) != 1 || recovered[0].DedupeKey != queued[0].DedupeKey {
		t.Fatalf("restarted durable requested starts = %+v err=%v", recovered, err)
	}
}

func TestProjectOrchestrationExplicitStartCompletesIntentOnTerminalAdmissionRefusal(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Requested", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	d.orchestrationSnapshotBuild = func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		return protocol.OrchestrationSnapshot{Health: protocol.OrchestrationHealth{Diagnostics: []string{"malformed graph"}}}, nil
	}
	_, err = d.orchestrationAuthority().Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "terminal-refusal", ActorID: "steward", IssueIDs: []string{issueID}})
	if err == nil || !strings.Contains(err.Error(), "board health refused") {
		t.Fatalf("terminal refusal error = %v", err)
	}
	if pending, pendingErr := client.PendingRequestedOrchestrationStarts(ctx, "proj"); pendingErr != nil || len(pending) != 0 {
		t.Fatalf("pending requested starts after terminal refusal = %+v err=%v", pending, pendingErr)
	}
}

func TestOrchestrationStartAdmissionPhaseIsTyped(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want protocol.OrchestrationAdmissionPhase
	}{
		{name: "projection checkpoint", err: orchestrationAdmissionContentionError(protocol.OrchestrationAdmissionProjectionCheckpoint, errors.New("context canceled")), want: protocol.OrchestrationAdmissionProjectionCheckpoint},
		{name: "operations store", err: fmt.Errorf("outer active-path wrapper: %w", orchestrationAdmissionContentionError(protocol.OrchestrationAdmissionOperationsStore, errors.New("database is locked"))), want: protocol.OrchestrationAdmissionOperationsStore},
		{name: "observation projection", err: orchestrationAdmissionContentionError(protocol.OrchestrationAdmissionObservationProjection, errors.New("context canceled")), want: protocol.OrchestrationAdmissionObservationProjection},
		{name: "misleading untyped wording is not inferred", err: fmt.Errorf("operation observation projection source checkpoint: %w", errOrchestrationSnapshotAdmissionContended), want: protocol.OrchestrationAdmissionSnapshot},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := orchestrationStartAdmissionPhase(test.err); got != test.want {
				t.Fatalf("phase=%q want=%q", got, test.want)
			}
		})
	}
}

func TestNormalizedRequestedStartIssueIDsPreservesStoredIdentity(t *testing.T) {
	got := normalizedRequestedStartIssueIDs([]string{" az-2 ", "AZ-1", "az-1", "", "Az-2"})
	if want := []string{"AZ-1", "az-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized requested issue IDs = %v, want %v", got, want)
	}
}

func TestProjectOrchestrationExplicitStartIsDurableBeforeContendedAdmissionPhases(t *testing.T) {
	tests := []struct {
		name  string
		phase protocol.OrchestrationAdmissionPhase
	}{
		{name: "projection checkpoint", phase: protocol.OrchestrationAdmissionProjectionCheckpoint},
		{name: "operations store", phase: protocol.OrchestrationAdmissionOperationsStore},
		{name: "observation projection", phase: protocol.OrchestrationAdmissionObservationProjection},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Requested", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
			if err != nil {
				t.Fatal(err)
			}
			admissionEntered := make(chan struct{})
			releaseAdmission := make(chan struct{})
			d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
			d.orchestrationSnapshotBuild = func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
				close(admissionEntered)
				<-releaseAdmission
				return protocol.OrchestrationSnapshot{}, orchestrationAdmissionContentionError(test.phase, errors.New("active-path admission unavailable"))
			}
			type applyResult struct {
				result protocol.OrchestrationIntentResult
				err    error
			}
			done := make(chan applyResult, 1)
			go func() {
				result, applyErr := d.orchestrationAuthority().Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "contended-" + string(test.phase), ActorID: "steward", IssueIDs: []string{issueID}})
				done <- applyResult{result: result, err: applyErr}
			}()
			select {
			case <-admissionEntered:
			case early := <-done:
				t.Fatalf("apply returned before admission barrier: result=%+v err=%v", early.result, early.err)
			}
			queued, err := client.PendingRequestedOrchestrationStarts(ctx, "proj")
			if err != nil || len(queued) != 1 || queued[0].Phase != "snapshot_admission" {
				close(releaseAdmission)
				t.Fatalf("pre-admission durable queue=%+v err=%v", queued, err)
			}
			close(releaseAdmission)
			got := <-done
			if got.err != nil || len(got.result.Pending) != 1 || got.result.Pending[0].Phase != test.phase || !got.result.Pending[0].Retryable {
				t.Fatalf("result=%+v err=%v", got.result, got.err)
			}
		})
	}
}

func TestProjectOrchestrationExplicitStartIntentWaitsForObservationWriterThenQueues(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Requested", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	holderEntered := make(chan struct{})
	releaseHolder := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderCtx := issues.ContextWithMutationOperation(ctx, "project_observation_projection")
		holderDone <- client.WithMutationLock(holderCtx, func(context.Context) error {
			close(holderEntered)
			<-releaseHolder
			return nil
		})
	}()
	<-holderEntered

	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	d.orchestrationSnapshotBuild = func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		return protocol.OrchestrationSnapshot{}, orchestrationAdmissionContentionError(protocol.OrchestrationAdmissionObservationProjection, errors.New("capture project observation events"))
	}
	queuedAtLock := make(chan struct{}, 1)
	applyCtx := issues.WithMutationLockWaitHookForTest(ctx, func(waiter, holder string) {
		if holder == "project_observation_projection" {
			queuedAtLock <- struct{}{}
		}
	})
	resultDone := make(chan struct {
		result protocol.OrchestrationIntentResult
		err    error
	}, 1)
	go func() {
		result, applyErr := d.orchestrationAuthority().Apply(applyCtx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "observation-contended", ActorID: "steward", IssueIDs: []string{issueID}})
		resultDone <- struct {
			result protocol.OrchestrationIntentResult
			err    error
		}{result: result, err: applyErr}
	}()
	<-queuedAtLock
	close(releaseHolder)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	got := <-resultDone
	if got.err != nil || len(got.result.Pending) != 1 || got.result.Pending[0].Phase != "project_observation_projection" {
		t.Fatalf("result=%+v err=%v", got.result, got.err)
	}
}

func TestProjectOrchestrationExplicitStartRealBuilderCarriesTypedAdmissionPhase(t *testing.T) {
	tests := []struct {
		name      string
		wantPhase protocol.OrchestrationAdmissionPhase
		configure func(*Daemon, *context.CancelFunc)
	}{
		{
			name:      "operations store wrapper",
			wantPhase: protocol.OrchestrationAdmissionOperationsStore,
			configure: func(d *Daemon, cancel *context.CancelFunc) {
				d.taskGraphOperationList = func(ctx context.Context, _ daemonops.Query) ([]daemonops.Record, error) {
					(*cancel)()
					return nil, ctx.Err()
				}
			},
		},
		{
			name:      "observation projection wrapper",
			wantPhase: protocol.OrchestrationAdmissionObservationProjection,
			configure: func(d *Daemon, cancel *context.CancelFunc) {
				d.orchestrationSnapshotAuxiliaryRead = func(context.Context) error {
					(*cancel)()
					return orchestrationAdmissionBoundaryError(protocol.OrchestrationAdmissionObservationProjection, context.Canceled)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Requested", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
			if err != nil {
				t.Fatal(err)
			}
			d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
			var admissionCancel context.CancelFunc = func() {}
			d.snapshotAdmissionContext = func(parent context.Context) (context.Context, context.CancelFunc) {
				admissionCtx, cancel := context.WithCancel(parent)
				admissionCancel = cancel
				return admissionCtx, cancel
			}
			test.configure(d, &admissionCancel)
			result, applyErr := d.orchestrationAuthority().Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "real-" + string(test.wantPhase), ActorID: "steward", IssueIDs: []string{issueID}})
			if applyErr != nil {
				t.Fatalf("explicit start should queue typed progress: %v", applyErr)
			}
			if len(result.Pending) != 1 || result.Pending[0].Phase != test.wantPhase || !result.Pending[0].Retryable {
				t.Fatalf("typed queued progress = %+v, want phase %q", result.Pending, test.wantPhase)
			}
		})
	}
}

func TestProjectCandidateRoutesDoNotAutomaticallyRouteOwnedPrematureWork(t *testing.T) {
	snapshot := protocol.OrchestrationSnapshot{Candidates: []protocol.OrchestrationCandidate{{
		IssueID: "foreign", Classification: string(domain.OrchestrationCandidateOwnedElsewhere),
		Executability: domain.IssueExecutabilityAssessment{Disposition: domain.IssuePremature, Reasons: []string{"missing-scope"}},
	}}}
	if routes := projectCandidateRoutes(snapshot, nil, nil); len(routes) != 0 {
		t.Fatalf("owned candidate routes = %+v", routes)
	}
}

func TestProjectOrchestrationRoutesContinueAfterCandidateFailure(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	foreignID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Foreign", Description: "Choose policy", Acceptance: "Policy chosen", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	readyID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Ready", Description: "Choose policy", Acceptance: "Policy chosen", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "proj", foreignID, issues.OwnershipClaimParams{OwnerID: "other", OwnerKind: "agent", Purpose: domain.CoordinationLeaseExecution}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	interaction := func(id, issueID string) *domain.InteractionRequest {
		return &domain.InteractionRequest{ID: id, IssueID: issueID, DecisionKey: "policy", OrchestrationScope: "project", Question: "Which policy?", Why: "Product judgment is required", RequiredDecisions: []string{"select policy"}, Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose policy"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	}
	routes := []domain.OrchestrationCandidateRoute{
		{IssueID: foreignID, Kind: domain.OrchestrationRouteInteraction, Reason: "product judgment", Interaction: interaction("foreign-request", foreignID)},
		{IssueID: readyID, Kind: domain.OrchestrationRouteInteraction, Reason: "product judgment", Interaction: interaction("ready-request", readyID)},
	}
	hub := publish.NewHub(16, 16, slog.Default())
	events, cancel := hub.Subscribe("proj", 0)
	defer cancel()
	d := &Daemon{cfg: Config{Logger: slog.Default()}, hub: hub, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	result, err := (daemonOrchestrationAuthority{daemon: d}).Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "wave-2", ActorID: "steward", Routes: routes})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed[foreignID] == "" {
		t.Fatalf("foreign route failure missing: %+v", result)
	}
	if len(result.Routed) != 1 || result.Routed[0].IssueID != readyID || result.Routed[0].InteractionID != "ready-request" {
		t.Fatalf("unrelated route did not continue: %+v", result)
	}
	requests, err := client.InteractionsForIssue(ctx, readyID)
	if err != nil || !domain.IssueWaitingHuman(readyID, requests) {
		t.Fatalf("waiting-human projection = requests %+v err %v", requests, err)
	}
	select {
	case event := <-events:
		var body protocol.TaskEventBody
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Task == nil || !body.Task.IssueFacts().WaitingHuman || body.Task.IssueFacts().WaitingHumanSource != domain.WaitingHumanSourceInteractionRequest {
			t.Fatalf("task event does not project Waiting Human: %+v", body.Task)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for routed task event")
	}
}

func TestRootedOrchestrationRejectsProjectCandidateRoutes(t *testing.T) {
	scope, err := domain.RootedOrchestrationScope("root")
	if err != nil {
		t.Fatal(err)
	}
	_, err = (daemonOrchestrationAuthority{daemon: &Daemon{}}).Apply(context.Background(), "proj", protocol.OrchestrationIntentRequest{Scope: scope, Kind: protocol.OrchestrationIntentStart, IntentKey: "wave", ActorID: "steward", Routes: []domain.OrchestrationCandidateRoute{{IssueID: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "only for project orchestration") {
		t.Fatalf("rooted route error = %v", err)
	}
}

func TestOrchestrationAuthorityInterfaceStaysDeep(t *testing.T) {
	var _ orchestrationAuthority = daemonOrchestrationAuthority{}
}

func TestOrchestrationSnapshotCacheCoalescesDuplicateRevision(t *testing.T) {
	projectID := "project"
	scope, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{revision: map[string]uint64{projectID: 7}}
	request := protocol.OrchestrationSnapshotRequest{Scope: scope, ActorID: "agent-a", Limit: 12}

	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	builds := 0
	build := func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		mu.Lock()
		builds++
		call := builds
		mu.Unlock()
		if call == 1 {
			close(entered)
			<-release
		}
		return protocol.OrchestrationSnapshot{Scope: scope, Runnable: []string{"az-child"}, Blocked: map[string]string{}}, nil
	}

	type result struct {
		snapshot protocol.OrchestrationSnapshot
		stable   bool
		err      error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			snapshot, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build)
			results <- result{snapshot: snapshot, stable: stable, err: err}
		}()
		if i == 0 {
			<-entered
		}
	}
	close(release)
	for range 2 {
		got := <-results
		if got.err != nil || !got.stable || len(got.snapshot.Runnable) != 1 {
			t.Fatalf("concurrent result = %+v", got)
		}
	}

	if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable {
		t.Fatalf("cached load stable=%v err=%v", stable, err)
	}
	mu.Lock()
	if builds != 1 {
		t.Fatalf("snapshot builds at one revision = %d, want 1", builds)
	}
	mu.Unlock()

	d.nextRevision(projectID)
	if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable {
		t.Fatalf("load after revision change stable=%v err=%v", stable, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if builds != 2 {
		t.Fatalf("snapshot builds after revision change = %d, want 2", builds)
	}
}

func TestRootedOrchestrationSnapshotCacheHonorsReadinessSemanticExpiry(t *testing.T) {
	projectID := "project"
	rootID := "az-root"
	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		revision:                map[string]uint64{projectID: 7},
		taskGraphReadinessCache: map[string]taskGraphReadinessCacheEntry{},
	}
	request := protocol.OrchestrationSnapshotRequest{Scope: scope, ActorID: "agent-a"}
	builds := 0
	build := func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		builds++
		expiresAt := time.Now().Add(time.Minute)
		if builds == 1 {
			expiresAt = time.Now().Add(-time.Second)
		}
		d.taskGraphReadinessCache[taskGraphReadinessLoadKey(projectID, rootID, request.ActorID)] = taskGraphReadinessCacheEntry{
			revision:  7,
			expiresAt: expiresAt,
		}
		return protocol.OrchestrationSnapshot{Scope: scope, Runnable: []string{"az-child"}, Blocked: map[string]string{}}, nil
	}

	if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable {
		t.Fatalf("initial snapshot stable=%v err=%v", stable, err)
	}
	if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable {
		t.Fatalf("snapshot after semantic expiry stable=%v err=%v", stable, err)
	}
	if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable {
		t.Fatalf("cached snapshot after refresh stable=%v err=%v", stable, err)
	}
	if builds != 2 {
		t.Fatalf("snapshot builds = %d, want expired entry rebuilt once", builds)
	}
}

func TestOrchestrationSnapshotCacheRecoversFromRevisionChurn(t *testing.T) {
	projectID := "project"
	scope, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{revision: map[string]uint64{projectID: 1}}
	request := protocol.OrchestrationSnapshotRequest{Scope: scope}
	builds := 0
	build := func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		builds++
		if builds == 1 {
			d.nextRevision(projectID)
		}
		return protocol.OrchestrationSnapshot{Scope: scope, Blocked: map[string]string{}}, nil
	}

	if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || stable {
		t.Fatalf("churning load stable=%v err=%v, want unstable without conflict loop", stable, err)
	}
	if _, revision, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable || revision != 2 {
		t.Fatalf("recovery load revision=%d stable=%v err=%v", revision, stable, err)
	}
	if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable {
		t.Fatalf("cached recovery stable=%v err=%v", stable, err)
	}
	if builds != 2 {
		t.Fatalf("snapshot builds = %d, want one churned build plus one stable build", builds)
	}
}

func TestOrchestrationSnapshotHandlerReturnsPromptConflictDuringContinuousChurn(t *testing.T) {
	const projectID = "project"
	d := &Daemon{revision: map[string]uint64{projectID: 1}}
	builds := 0
	d.orchestrationSnapshotBuild = func(_ context.Context, gotProjectID string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		builds++
		d.nextRevision(gotProjectID)
		return protocol.OrchestrationSnapshot{Scope: request.Scope, Blocked: map[string]string{}}, nil
	}
	body, err := json.Marshal(protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope()})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	resp, err := d.handleOrchestrationSnapshot(context.Background(), protocol.RequestEnvelope{
		Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !resp.OK || resp.Error != nil {
		t.Fatalf("response = %+v, want converged project snapshot", resp)
	}
	if builds != 1 {
		t.Fatalf("snapshot builds = %d, want one prompt attempt", builds)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("continuous churn conflict took %s, want <= 100ms", elapsed)
	}
}

func TestOrchestrationSnapshotCacheIncludesCanonicalEffectiveRepoDir(t *testing.T) {
	projectID := "project"
	firstRepo := t.TempDir()
	secondRepo := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(firstRepo, alias); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{revision: map[string]uint64{projectID: 1}}
	request := protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), RepoDir: firstRepo}
	builds := 0
	var builtRepoDirs []string
	build := func(_ context.Context, _ string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		builds++
		builtRepoDirs = append(builtRepoDirs, request.RepoDir)
		return protocol.OrchestrationSnapshot{Scope: request.Scope, Blocked: map[string]string{}}, nil
	}
	for _, repoDir := range []string{firstRepo, alias, secondRepo} {
		request.RepoDir = repoDir
		if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable {
			t.Fatalf("load repo %q stable=%v err=%v", repoDir, stable, err)
		}
	}
	if builds != 2 {
		t.Fatalf("snapshot builds = %d, want alias reuse plus distinct-repo rebuild", builds)
	}
	wantFirst, err := filepath.EvalSymlinks(firstRepo)
	if err != nil {
		t.Fatal(err)
	}
	wantSecond, err := filepath.EvalSymlinks(secondRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(builtRepoDirs) != 2 || builtRepoDirs[0] != filepath.Clean(wantFirst) || builtRepoDirs[1] != filepath.Clean(wantSecond) {
		t.Fatalf("canonical build repo dirs = %v", builtRepoDirs)
	}
}

func TestOrchestrationProjectSnapshotCacheReusesRevision(t *testing.T) {
	projectID := "project"
	d := &Daemon{revision: map[string]uint64{projectID: 3}}
	request := protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), Limit: 50}
	builds := 0
	build := func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		builds++
		return protocol.OrchestrationSnapshot{Scope: request.Scope, Blocked: map[string]string{}}, nil
	}
	for range 2 {
		if _, revision, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable || revision != 3 {
			t.Fatalf("project snapshot revision=%d stable=%v err=%v", revision, stable, err)
		}
	}
	if builds != 1 {
		t.Fatalf("project snapshot builds = %d, want 1 at unchanged revision", builds)
	}
}

func TestOrchestrationSnapshotCacheConcurrentReadP95Budget(t *testing.T) {
	const (
		readers    = 5
		iterations = 20
		p95Budget  = 25 * time.Millisecond
	)
	projectID := "project"
	scope, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{revision: map[string]uint64{projectID: 1}}
	request := protocol.OrchestrationSnapshotRequest{Scope: scope, ActorID: "agent-a"}
	builds := 0
	build := func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		builds++
		return protocol.OrchestrationSnapshot{Scope: scope, Runnable: []string{"az-child"}, Blocked: map[string]string{}}, nil
	}
	if _, _, stable, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build); err != nil || !stable {
		t.Fatalf("warm cache stable=%v err=%v", stable, err)
	}

	durations := make(chan time.Duration, readers*iterations)
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				started := time.Now()
				_, _, _, err := d.loadOrchestrationSnapshot(context.Background(), projectID, request, build)
				if err != nil {
					t.Errorf("cached snapshot: %v", err)
					return
				}
				durations <- time.Since(started)
			}
		}()
	}
	wg.Wait()
	close(durations)
	measured := make([]time.Duration, 0, readers*iterations)
	for duration := range durations {
		measured = append(measured, duration)
	}
	sort.Slice(measured, func(i, j int) bool { return measured[i] < measured[j] })
	p95 := measured[(len(measured)*95+99)/100-1]
	if p95 > p95Budget {
		t.Fatalf("cached concurrent read p95 = %s, budget %s", p95, p95Budget)
	}
	if builds != 1 {
		t.Fatalf("snapshot builds = %d, want one warm build across concurrent reads", builds)
	}
	t.Logf("cached concurrent read p95=%s budget=%s samples=%d readers=%d", p95, p95Budget, len(measured), readers)
}

func TestResolveOrchestratorSessionUsesDurableLeaseScope(t *testing.T) {
	projectID := "project"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	rooted, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, rooted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireOrchestratorScopeLease(context.Background(), identity, "az-root-orchestrator", func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	d := &Daemon{runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}}
	lease, found, err := d.resolveOrchestratorSession(context.Background(), projectID, "az-root-orchestrator")
	if err != nil || !found {
		t.Fatalf("resolve = found %v err %v", found, err)
	}
	if lease.Identity.Scope != rooted {
		t.Fatalf("scope = %+v, want %+v", lease.Identity.Scope, rooted)
	}
	if _, found, err := d.resolveOrchestratorSession(context.Background(), projectID, "worker"); err != nil || found {
		t.Fatalf("worker resolve = found %v err %v, want no orchestrator role", found, err)
	}
}

func TestOrchestrationSnapshotAdvancesCursorOnlyForOwningSessionAndScope(t *testing.T) {
	projectID := "project"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireOrchestratorScopeLease(context.Background(), identity, "owner-session", func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}}

	request := func(sessionID string, requestedScope domain.OrchestrationScope, cursor int64) {
		body, marshalErr := json.Marshal(protocol.OrchestrationSnapshotRequest{Scope: requestedScope, SessionID: sessionID, ObservedCursor: cursor})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		_, _ = d.handleOrchestrationSnapshot(context.Background(), protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	}
	request("worker-session", scope, 99)
	lease, _, err := store.GetOrchestratorScopeLease(context.Background(), identity)
	if err != nil || lease.Cursor != 0 {
		t.Fatalf("foreign cursor = %d, err=%v", lease.Cursor, err)
	}
	otherScope, err := domain.RootedOrchestrationScope("az-nested")
	if err != nil {
		t.Fatal(err)
	}
	request("owner-session", otherScope, 99)
	lease, _, err = store.GetOrchestratorScopeLease(context.Background(), identity)
	if err != nil || lease.Cursor != 0 {
		t.Fatalf("cross-scope owner cursor = %d, err=%v", lease.Cursor, err)
	}
	request("owner-session", scope, 41)
	lease, _, err = store.GetOrchestratorScopeLease(context.Background(), identity)
	if err != nil || lease.Cursor != 41 {
		t.Fatalf("owner cursor = %d, err=%v", lease.Cursor, err)
	}
}

func TestOrchestrationScopeCommandsKeepRootAuthorityExplicit(t *testing.T) {
	rooted, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range orchestrationScopeCommands(rooted) {
		if !strings.Contains(command, "--root az-root") {
			t.Fatalf("rooted command lost scope: %q", command)
		}
	}
	for _, command := range orchestrationScopeCommands(domain.ProjectOrchestrationScope()) {
		if strings.Contains(command, "--root") {
			t.Fatalf("project command invented root: %q", command)
		}
	}
}

func TestProjectRecentEventsComeFromDurableObservations(t *testing.T) {
	created := time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC)
	rootA, rootB := naming.IssueID("az-a"), naming.IssueID("az-b")
	workerA, workerB := naming.IssueID("az-worker-a"), naming.IssueID("az-worker-b")
	tasks := []domain.Task{
		{ID: rootA},
		{ID: workerA, ParentID: &rootA},
		{ID: rootB},
		{ID: workerB, ParentID: &rootB},
	}
	events := projectRecentObservationEvents(tasks, map[string][]domain.IssueObservationEvent{
		workerB.String(): {{ID: 12, IssueID: workerB, Type: domain.IssueEventEvidenceSubmitted, ObservedAt: created.Add(time.Second), Source: "worker-b", Payload: map[string]any{"summary": "b"}}},
		workerA.String(): {
			{ID: 10, IssueID: workerA, Type: domain.IssueEventIssueCreated, ObservedAt: created.Add(-time.Second), Source: "store"},
			{ID: 11, IssueID: workerA, Type: domain.IssueEventProgressRecorded, ObservedAt: created, Source: "worker-a", Payload: map[string]any{"body": "a"}},
		},
	})
	if len(events) != 2 {
		t.Fatalf("recent events = %+v", events)
	}
	if events[0].Seq != 11 || events[0].ParentIssue != rootA.String() || events[0].IssueID != workerA || events[0].Type != "worker-progress" || events[0].From != "worker-a" || events[0].Body != "a" {
		t.Fatalf("first recent event = %+v", events[0])
	}
	if events[1].Seq != 12 || events[1].ParentIssue != rootB.String() || events[1].IssueID != workerB || events[1].Type != "worker-integration-ready" || events[1].From != "worker-b" || events[1].Body != `{"summary":"b"}` {
		t.Fatalf("second recent event = %+v", events[1])
	}
}

func TestOrchestrationSkipReasonPreservesNestedRootAuthority(t *testing.T) {
	nested := map[string]struct{}{"az-nested": {}}
	active := map[string]struct{}{"az-active": {}}
	blocked := map[string]string{"az-blocked": "dependency remains open"}

	if got := orchestrationSkipReason("az-nested", nested, active, blocked); got != "nested-root-start-orchestrator-session: az orchestrator-session start --root az-nested" {
		t.Fatalf("nested skip reason = %q", got)
	}
	if got := orchestrationSkipReason("az-active", nested, active, blocked); got != "session-already-running" {
		t.Fatalf("active skip reason = %q", got)
	}
	if got := orchestrationSkipReason("az-blocked", nested, active, blocked); got != "dependency remains open" {
		t.Fatalf("blocked skip reason = %q", got)
	}
}

func TestOrchestrationBoardHealthDiagnosesUnsafeProject(t *testing.T) {
	parent := domain.Task{ID: "parent", Status: domain.StatusOpen}
	missing := domain.Task{ID: "missing-child", Status: domain.StatusOpen, ParentID: issueIDPtr("absent")}
	malformedOwner := domain.Task{ID: "owned", Status: domain.StatusOpen, Ownership: &domain.IssueOwnership{OwnerKind: "agent"}}
	tasks := []domain.Task{parent, missing, malformedOwner}
	health := orchestrationBoardHealth(tasks, map[string]domain.Task{"parent": parent, "missing-child": missing, "owned": malformedOwner}, 3, 2, 2)
	if health.Healthy {
		t.Fatal("unsafe board reported healthy")
	}
	if health.OpenIssueCount != 3 || health.InspectLimit != 2 || health.OpenIssueLimit != 2 {
		t.Fatalf("health counts = %+v", health)
	}
	joined := strings.Join(health.Diagnostics, "\n")
	for _, want := range []string{"missing parent absent", "incomplete owner identity", "exceeds refusal threshold"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostics %q missing %q", joined, want)
		}
	}
}

func TestOrchestrationBoardHealthUsesCanonicalOpenCountForThreshold(t *testing.T) {
	backlog, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	tasks := make([]domain.Task, 0, 102)
	byID := make(map[string]domain.Task, 102)
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("backlog-%03d", i)
		task := domain.Task{ID: naming.IssueID(id), Status: domain.StatusOpen, State: backlog}
		tasks = append(tasks, task)
		byID[id] = task
	}
	open := domain.Task{ID: "open", Status: domain.StatusOpen}
	tasks = append(tasks, open)
	byID[open.ID.String()] = open

	health := orchestrationBoardHealth(tasks, byID, 1, 200, 100)
	if !health.Healthy || health.OpenIssueCount != 1 {
		t.Fatalf("health = %+v, want healthy canonical open count 1 despite backlog context", health)
	}
	for _, diagnostic := range health.Diagnostics {
		if strings.Contains(diagnostic, "open issue count") {
			t.Fatalf("health diagnostics = %v, want no threshold diagnostic", health.Diagnostics)
		}
	}

	health = orchestrationBoardHealth(tasks, byID, 101, 200, 100)
	if health.Healthy || health.OpenIssueCount != 101 {
		t.Fatalf("health = %+v, want canonical threshold refusal at 101", health)
	}
	if got := strings.Join(health.Diagnostics, "\n"); !strings.Contains(got, "open issue count 101 exceeds refusal threshold 100") {
		t.Fatalf("health diagnostics = %q, want exact canonical count", got)
	}
}

func TestMaterializedProjectOrchestrationContextLimitsCanonicalOpenBeforeBacklogContext(t *testing.T) {
	backlog, err := domain.NewIssueState(domain.IssueStateParts{Workflow: domain.IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tasks := make([]domain.Task, 0, 201)
	for i := 0; i < 189; i++ {
		id := fmt.Sprintf("backlog-%03d", i)
		tasks = append(tasks, domain.Task{ID: naming.IssueID(id), Status: domain.StatusOpen, State: backlog, Priority: domain.P0, UpdatedAt: old})
	}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("open-%02d", i)
		tasks = append(tasks, domain.Task{ID: naming.IssueID(id), Status: domain.StatusOpen, Priority: domain.P1, UpdatedAt: old.Add(time.Duration(i) * time.Minute)})
	}

	context := materializedProjectOrchestrationContext(tasks, 5)
	openIDs := make([]string, 0, 5)
	backlogIDs := make([]string, 0, 1)
	for _, task := range context {
		switch task.IssueFacts().LifecycleState {
		case domain.IssueWorkflowOpen:
			openIDs = append(openIDs, task.ID.String())
		case domain.IssueWorkflowBacklog:
			backlogIDs = append(backlogIDs, task.ID.String())
		}
	}
	if want := []string{"open-00", "open-01", "open-02", "open-03", "open-04"}; !reflect.DeepEqual(openIDs, want) {
		t.Fatalf("open context = %v, want canonical bounded window %v", openIDs, want)
	}
	if len(backlogIDs) != 0 {
		t.Fatalf("backlog context = %v, want backlog outside the root candidate window", backlogIDs)
	}
}

func TestProjectOrchestrationCandidateRootsRetainsActiveRootsBeyondLimit(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	roots := make([]domain.Task, 0, 8)
	for i := 0; i < 5; i++ {
		roots = append(roots, domain.Task{ID: naming.IssueID(fmt.Sprintf("runnable-%d", i)), Status: domain.StatusOpen, Priority: domain.P1, UpdatedAt: old.Add(time.Duration(i) * time.Minute)})
	}
	for i := 0; i < 3; i++ {
		roots = append(roots, domain.Task{ID: naming.IssueID(fmt.Sprintf("active-%d", i)), Status: domain.StatusInProgress, Priority: domain.P4, UpdatedAt: old.Add(time.Duration(10+i) * time.Minute), HasTmuxSession: true})
	}

	selected := projectOrchestrationCandidateRoots(roots, 2)
	got := make([]string, 0, len(selected))
	for _, task := range selected {
		got = append(got, task.ID.String())
	}
	if want := []string{"runnable-0", "runnable-1", "active-0", "active-1", "active-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected roots = %v, want bounded runnable roots plus every active root %v", got, want)
	}

	snapshot := protocol.OrchestrationSnapshot{
		Runnable:       []string{"runnable-0", "runnable-1"},
		Active:         []string{"active-0", "active-1", "active-2"},
		ActiveSessions: []protocol.OrchestrationSession{{IssueID: "active-0"}, {IssueID: "active-1"}, {IssueID: "active-2"}},
	}
	constrainProjectOrchestrationSnapshotToRoots(&snapshot, selected)
	if snapshot.Capacity.DirectRunnableCount != 2 || snapshot.Capacity.DirectActiveCount != 3 {
		t.Fatalf("capacity = %+v, want runnable=2 active=3", snapshot.Capacity)
	}
	if len(snapshot.Active) != 3 || len(snapshot.ActiveSessions) != 3 {
		t.Fatalf("active inventory = %v sessions=%+v, want all three roots", snapshot.Active, snapshot.ActiveSessions)
	}
}

func TestOrchestrationCandidateOrderingIsStable(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tasks := map[string]domain.Task{
		"excluded": {ID: "excluded", Priority: domain.P1, UpdatedAt: old},
		"b":        {ID: "b", Priority: domain.P1, UpdatedAt: old},
		"a":        {ID: "a", Priority: domain.P1, UpdatedAt: old},
		"p2":       {ID: "p2", Priority: domain.P2, UpdatedAt: old},
	}
	candidates := []protocol.OrchestrationCandidate{{IssueID: "p2", Included: true}, {IssueID: "excluded"}, {IssueID: "b", Included: true}, {IssueID: "a", Included: true}}
	sort.SliceStable(candidates, func(i, j int) bool { return orchestrationCandidateLess(candidates[i], candidates[j], tasks) })
	got := []string{candidates[0].IssueID, candidates[1].IssueID, candidates[2].IssueID, candidates[3].IssueID}
	if want := []string{"a", "b", "excluded", "p2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestStableRequestedCandidatesFollowsPolicyOrder(t *testing.T) {
	got := stableRequestedCandidates([]string{"p2", "unknown", "p1", "p2"}, []string{"p1", "p2", "p3"})
	if want := []string{"p1", "p2", "unknown"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requested = %v, want %v", got, want)
	}
}

func TestBoardHealthOverrideOnlyAllowsOpenIssueThreshold(t *testing.T) {
	if !orchestrationHealthOverrideAllowed(protocol.OrchestrationHealth{Diagnostics: []string{"open issue count 101 exceeds refusal threshold 100"}}) {
		t.Fatal("threshold override rejected")
	}
	if orchestrationHealthOverrideAllowed(protocol.OrchestrationHealth{Diagnostics: []string{"malformed graph: x has missing parent y"}}) {
		t.Fatal("malformed graph override allowed")
	}
}

func TestCandidateOwnershipAllowsSameActor(t *testing.T) {
	now := time.Now().UTC()
	task := domain.Task{ID: "x", Ownership: &domain.IssueOwnership{OwnerID: "worker", OwnerKind: "agent"}}
	if got := orchestrationCandidateForTask(task, "worker", now, nil); got.Classification == "owned-elsewhere" {
		t.Fatalf("same actor excluded: %+v", got)
	}
	if got := orchestrationCandidateForTask(task, "other", now, nil); got.Classification != "owned-elsewhere" {
		t.Fatalf("other actor not excluded: %+v", got)
	}
}

func TestExplainOrchestrationCandidatesPreservesReviewAndDecisionClasses(t *testing.T) {
	snapshot := protocol.OrchestrationSnapshot{
		Runnable: []string{"review", "decision", "open"},
		Blocked:  map[string]string{},
		Candidates: []protocol.OrchestrationCandidate{
			{IssueID: "review", Classification: string(domain.OrchestrationCandidateReviewReady)},
			{IssueID: "decision", Classification: string(domain.OrchestrationCandidateDecisionWaiting)},
			{IssueID: "open", Classification: string(domain.OrchestrationCandidateOpen)},
		},
	}
	explainOrchestrationCandidates(&snapshot)
	if got := candidateClass(snapshot.Candidates, "review"); got != string(domain.OrchestrationCandidateReviewReady) {
		t.Fatalf("review class = %q", got)
	}
	if got := candidateClass(snapshot.Candidates, "decision"); got != string(domain.OrchestrationCandidateDecisionWaiting) {
		t.Fatalf("decision class = %q", got)
	}
	if got := candidateClass(snapshot.Candidates, "open"); got != "runnable" {
		t.Fatalf("open class = %q", got)
	}
}

func TestOrchestrationGlobalActiveCountIncludesUninspectedRoots(t *testing.T) {
	tasks := []domain.Task{{ID: "visible", HasTmuxSession: true}, {ID: "outside-limit", HasTmuxSession: true}, {ID: "inactive"}}
	if got := orchestrationGlobalActiveCount(tasks); got != 2 {
		t.Fatalf("active count = %d, want 2", got)
	}
}

func TestProjectOrchestrationSnapshotRefreshesCrossProcessOwnership(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	id, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: "Worker completes the scoped change", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	runtimeRepoDir := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: runtimeRepoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	_, err = runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "focused", LeaseToken: "test-secret", ProjectID: "proj", IssueID: id, Class: domain.ValidationClassShared, Profile: "focused", Command: "go test ./internal/daemon", SourceRevision: "abc123", TTL: time.Minute}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}, operationRuntime: runtime}
	authority := daemonOrchestrationAuthority{daemon: d}
	before, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(before.Candidates, id); got != "runnable" {
		t.Fatalf("before class = %q, want runnable", got)
	}
	if before.ValidationCapacity == nil || len(before.ValidationCapacity.Active) != 1 {
		t.Fatalf("validation capacity = %+v, want active focused validation", before.ValidationCapacity)
	}
	if before.Health.OpenIssueCount != 1 {
		t.Fatalf("before open issue count = %d, want 1", before.Health.OpenIssueCount)
	}
	_, err = writer.ClaimOwnershipWithRuntime(ctx, "proj", id, issues.OwnershipClaimParams{OwnerID: "other", OwnerKind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	validationWriter, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(runtimeRepoDir, ".azedarach", "azedarach.db"))+"?_pragma=busy_timeout(100)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = validationWriter.Close() })
	validationTx, err := validationWriter.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = validationTx.Rollback() })
	if _, err := validationTx.ExecContext(ctx, `UPDATE daemon_validation_state SET revision=revision WHERE project_id='proj'`); err != nil {
		t.Fatal(err)
	}
	after, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(after.Candidates, id); got != string(domain.OrchestrationCandidateOwnedElsewhere) {
		t.Fatalf("after class = %q, want owned-elsewhere", got)
	}
	if after.ValidationCapacity == nil || after.ValidationCapacity.Freshness != domain.ValidationSnapshotFresh {
		t.Fatalf("validation capacity = %+v, want fresh snapshot during writer", after.ValidationCapacity)
	}
}

func TestProjectOrchestrationSnapshotRefreshesCrossProcessBacklogLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	id, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: "Worker completes the scoped change", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}}
	authority := daemonOrchestrationAuthority{daemon: d}
	before, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(before.Candidates, id); got != "runnable" {
		t.Fatalf("before class = %q, want runnable", got)
	}
	backlog := domain.IssueWorkflowBacklog
	acceptance := "Worker completes the scoped change"
	if err := writer.UpdateDetails(ctx, id, issues.UpdateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: &acceptance, Type: domain.TypeTask, Priority: domain.P1, Lifecycle: &backlog}); err != nil {
		t.Fatal(err)
	}
	after, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(after.Candidates, id); got != "" {
		t.Fatalf("after class = %q, want backlog outside actionable candidate window", got)
	}
	if after.Health.OpenIssueCount != 0 {
		t.Fatalf("after open issue count = %d, want backlog excluded", after.Health.OpenIssueCount)
	}
	for _, candidate := range after.Candidates {
		if candidate.IssueID == id {
			t.Fatalf("after candidate = %+v, want backlog visible only outside actionable candidates", candidate)
		}
	}
}

func TestProjectOrchestrationSnapshotUsesExactSQLiteEpochInsteadOfStaleMaterializer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	id, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := reader.ExportOrchestrationProjection(ctx, "proj", 10)
	if err != nil {
		t.Fatal(err)
	}
	canonical := map[string]domain.Task{id: initial.Tasks[0]}
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, canonical)
	materializer := newProjectReadMaterializer("proj", nil, nil)
	materializer.replaceBootstrap(canonical, canonical, materializedMetadata(1, 1, issueProjectionProjector(), nil, issueKeys.sum(), runtimeKeys.sum(), "healthy"), issueKeys, runtimeKeys)
	d := &Daemon{
		cfg:                   Config{Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{"proj": reader},
		materializers:         map[string]*projectReadMaterializer{"proj": materializer},
		materializersStarted:  true,
	}

	backlog := domain.IssueWorkflowBacklog
	acceptance := "Done"
	if err := writer.UpdateDetails(ctx, id, issues.UpdateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: &acceptance, Type: domain.TypeTask, Priority: domain.P1, Lifecycle: &backlog}); err != nil {
		t.Fatal(err)
	}
	expectedCheckpoint, err := reader.ProjectionSourceCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(snapshot.Candidates, id); got != "" {
		t.Fatalf("candidate class = %q, want exact SQLite backlog epoch instead of stale materialized open state", got)
	}
	if snapshot.Health.OpenIssueCount != 0 {
		t.Fatalf("open issue count = %d, want exact SQLite count 0", snapshot.Health.OpenIssueCount)
	}
	if snapshot.ProjectionAuthority != protocol.OrchestrationProjectionAuthoritySQLite || snapshot.ProjectionRevision != expectedCheckpoint {
		t.Fatalf("projection authority/revision = %q/%d, want %q/%d", snapshot.ProjectionAuthority, snapshot.ProjectionRevision, protocol.OrchestrationProjectionAuthoritySQLite, expectedCheckpoint)
	}
}

func TestProjectOrchestrationSnapshotDoesNotHoldIssueMutationLockWhileRuntimeWriterIsBlocked(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	d.snapshotAdmissionContext = context.WithCancel
	writer := newRuntimeProjectionWriter(d)
	releaseWriter, err := writer.lockProjectionWriter(ctx, "proj", "background.projection_refresh")
	if err != nil {
		t.Fatal(err)
	}

	snapshotWaiting := make(chan struct{})
	var waitOnce sync.Once
	d.orchestrationSnapshotAuxiliaryRead = func(waitCtx context.Context) error {
		waitOnce.Do(func() { close(snapshotWaiting) })
		unlock, lockErr := writer.lockProjectionWriter(waitCtx, "proj", "orchestration.snapshot")
		if lockErr != nil {
			return lockErr
		}
		unlock()
		return nil
	}
	snapshotDone := make(chan error, 1)
	go func() {
		_, snapshotErr := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
		snapshotDone <- snapshotErr
	}()
	<-snapshotWaiting

	mutations := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "update", run: func(mutationCtx context.Context) error {
			return client.Update(mutationCtx, issueID, domain.StatusInProgress)
		}},
		{name: "event append", run: func(mutationCtx context.Context) error {
			_, appendErr := client.AppendIssueObservationEvent(mutationCtx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventProgressRecorded, Source: "test", Payload: map[string]any{"summary": "still admissible"}})
			return appendErr
		}},
		{name: "create", run: func(mutationCtx context.Context) error {
			_, createErr := client.Create(mutationCtx, issues.CreateTaskParams{Title: "Concurrent", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
			return createErr
		}},
		{name: "mail send", run: func(mutationCtx context.Context) error {
			body := mustMarshal(t, protocol.MailSendCommandBody{RepoDir: repoDir, ParentIssue: issueID, IssueID: naming.IssueID(issueID), Type: "worker-progress", From: "worker", Body: "snapshot must not block mail"})
			resp, sendErr := d.handleMailSend(mutationCtx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "dqq-mail", Command: protocol.CommandMailSend, Meta: protocol.Metadata{ProjectID: "proj"}, Body: body})
			if sendErr != nil {
				return sendErr
			}
			if !resp.OK {
				return fmt.Errorf("mail response: %+v", resp.Error)
			}
			return nil
		}},
	}

	blockedBySnapshot := ""
	mutationFailure := ""
	var blockedMutationDone <-chan error
	for _, mutation := range mutations {
		mutationWaited := make(chan struct{}, 1)
		mutationCtx := issues.WithMutationLockWaitHookForTest(ctx, func(_, _ string) {
			mutationWaited <- struct{}{}
		})
		mutationDone := make(chan error, 1)
		go func() { mutationDone <- mutation.run(mutationCtx) }()
		select {
		case err := <-mutationDone:
			if err != nil {
				mutationFailure = fmt.Sprintf("concurrent %s: %v", mutation.name, err)
			}
		case <-mutationWaited:
			blockedBySnapshot = mutation.name
			blockedMutationDone = mutationDone
		}
		if blockedBySnapshot != "" {
			break
		}
		if mutationFailure != "" {
			break
		}
	}
	releaseWriter()
	snapshotErr := <-snapshotDone
	if blockedMutationDone != nil {
		if err := <-blockedMutationDone; err != nil {
			mutationFailure = fmt.Sprintf("blocked %s: %v", blockedBySnapshot, err)
		}
	}
	if blockedBySnapshot != "" {
		t.Fatalf("project snapshot held the issue mutation lock while waiting for the runtime projection writer; blocked %s", blockedBySnapshot)
	}
	if mutationFailure != "" {
		t.Fatal(mutationFailure)
	}
	if snapshotErr != nil {
		t.Fatalf("project snapshot: %v", snapshotErr)
	}
}

func TestProjectOrchestrationSnapshotMapsCanceledRuntimeWriterAdmissionToUnavailable(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	if _, err := client.Create(ctx, issues.CreateTaskParams{Title: "Candidate", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen}); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	writer := newRuntimeProjectionWriter(d)
	releaseWriter, err := writer.lockProjectionWriter(ctx, "proj", "background.projection_refresh")
	if err != nil {
		t.Fatal(err)
	}

	writerWaited := make(chan struct{})
	d.snapshotAdmissionContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		admissionCtx, cancel := context.WithCancel(parent)
		admissionCtx = withRuntimeProjectionWriterQueuedHookForTest(admissionCtx, func(waiterOperation string) {
			if waiterOperation != "orchestration.snapshot" {
				t.Errorf("runtime writer queued waiter=%q", waiterOperation)
			}
			close(writerWaited)
			cancel()
		})
		return admissionCtx, cancel
	}
	d.orchestrationSnapshotAuxiliaryRead = func(waitCtx context.Context) error {
		unlock, lockErr := writer.lockProjectionWriter(waitCtx, "proj", "orchestration.snapshot")
		if lockErr != nil {
			return lockErr
		}
		unlock()
		return nil
	}

	body := mustMarshal(t, protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	resp, handleErr := d.handleOrchestrationSnapshot(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Command: protocol.CommandOrchestrationSnapshot, Meta: protocol.Metadata{ProjectID: "proj"}, Body: body})
	select {
	case <-writerWaited:
	default:
		releaseWriter()
		t.Fatalf("runtime writer wait hook was not observed; response error = %+v, handler error = %v", resp.Error, handleErr)
	}
	releaseWriter()
	if handleErr != nil {
		t.Fatal(handleErr)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnavailable || !resp.Error.Retryable {
		t.Fatalf("canceled runtime-writer admission response = %+v, want retryable unavailable", resp.Error)
	}
}

func TestProjectOrchestrationStartNeverTouchesExplicitBacklogIssue(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	id, err := client.Create(ctx, issues.CreateTaskParams{Title: "Paused candidate", Description: "Executable", Acceptance: "Worker completes the scoped change", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen, Lifecycle: domain.IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	result, err := (daemonOrchestrationAuthority{daemon: d}).Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "backlog-explicit", ActorID: "steward", IssueIDs: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Skipped[id]; got != "outside-project-root-candidate-scope" {
		t.Fatalf("skipped[%s] = %q, want backlog outside project root candidate scope", id, got)
	}
	if len(result.Started) != 0 || len(result.Launched) != 0 || len(result.Pending) != 0 {
		t.Fatalf("backlog start result = %+v, want no start side effects", result)
	}
	task, err := client.GetWithRuntime(ctx, "proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if task.State.Workflow() != domain.IssueWorkflowBacklog || task.Ownership != nil || task.HasTmuxSession {
		t.Fatalf("backlog task mutated by start: %+v", task)
	}
}

func TestProjectOrchestrationBacklogRootDoesNotExposeOpenChildren(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Paused root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusOpen, Lifecycle: domain.IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Open child", Description: "Executable", Acceptance: "Worker completes scoped change", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	nestedID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Open nested root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(ctx, issues.CreateTaskParams{Title: "Nested child", Description: "Executable", Acceptance: "Worker completes scoped change", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen, ParentID: &nestedID}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	authority := daemonOrchestrationAuthority{daemon: d}
	snapshot, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "steward", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Roots) != 0 || len(snapshot.Runnable) != 0 {
		t.Fatalf("snapshot = %+v, want backlog root and its children outside project actions", snapshot)
	}
	if got := candidateClass(snapshot.Candidates, childID); got != "" {
		t.Fatalf("child class = %q, want parented child outside project candidates", got)
	}
	if len(snapshot.NestedRoots) != 0 {
		t.Fatalf("nested roots = %+v, want parented nested root outside project actions", snapshot.NestedRoots)
	}
	result, err := authority.Apply(ctx, "proj", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "backlog-root-child", ActorID: "steward", IssueIDs: []string{childID, nestedID}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Skipped[childID]; got != "outside-project-root-candidate-scope" {
		t.Fatalf("skipped[%s] = %q, want root-scope exclusion", childID, got)
	}
	if got := result.Skipped[nestedID]; got != "outside-project-root-candidate-scope" {
		t.Fatalf("skipped[%s] = %q, want root-scope exclusion", nestedID, got)
	}
	child, err := client.GetWithRuntime(ctx, "proj", childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.State.Workflow() != domain.IssueWorkflowOpen || child.Ownership != nil || child.HasTmuxSession {
		t.Fatalf("contained child mutated by project start: %+v", child)
	}
	nested, err := client.GetWithRuntime(ctx, "proj", nestedID)
	if err != nil {
		t.Fatal(err)
	}
	if nested.State.Workflow() != domain.IssueWorkflowOpen || nested.Ownership != nil || nested.HasTmuxSession {
		t.Fatalf("contained nested root mutated by project start: %+v", nested)
	}
}

func TestProjectOrchestrationRootScopeDoesNotPromoteOpenChildFromContext(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	contextRootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Active dependency context", Type: domain.TypeEpic, Priority: domain.P0, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	backlogRootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Paused root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusOpen, Lifecycle: domain.IssueWorkflowBacklog})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Selected open child", Description: "Executable", Acceptance: "Worker completes scoped change", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen, ParentID: &backlogRootID})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddDependency(ctx, childID, contextRootID, string(domain.DependencyBlocks)); err != nil {
		t.Fatal(err)
	}

	snapshot, err := (daemonOrchestrationAuthority{daemon: &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "steward", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Roots) != 0 || len(snapshot.Runnable) != 0 {
		t.Fatalf("snapshot roots=%v runnable=%v, want no project actions for active/backlog roots or parented child", snapshot.Roots, snapshot.Runnable)
	}
	if candidateClass(snapshot.Candidates, childID) != "" {
		t.Fatalf("snapshot = %+v, want parented child excluded from project candidates", snapshot)
	}
	if snapshot.Health.OpenIssueCount != 1 {
		t.Fatalf("open issue count = %d, want canonical board inventory to retain the open child", snapshot.Health.OpenIssueCount)
	}
}

func candidateClass(candidates []protocol.OrchestrationCandidate, issueID string) string {
	for _, candidate := range candidates {
		if candidate.IssueID == issueID {
			return candidate.Classification
		}
	}
	return ""
}

func TestExplainOrchestrationCandidatesRejectsInsufficientContracts(t *testing.T) {
	snapshot := protocol.OrchestrationSnapshot{
		Runnable: []string{"thin"},
		Blocked:  map[string]string{},
		Candidates: []protocol.OrchestrationCandidate{{
			IssueID:        "thin",
			Included:       true,
			Eligible:       true,
			Sufficient:     false,
			Classification: string(domain.OrchestrationCandidateOpen),
			Executability: domain.IssueExecutabilityAssessment{
				Disposition: domain.IssuePremature,
				Reasons:     []string{"missing-acceptance"},
			},
		}},
	}
	explainOrchestrationCandidates(&snapshot)
	got := snapshot.Candidates[0]
	if got.Included || got.Eligible || got.Classification != string(domain.IssuePremature) || got.Reason != "excluded: missing-acceptance" {
		t.Fatalf("candidate = %+v", got)
	}
}

func TestProjectOrchestrationSnapshotRefreshesCrossProcessInteractions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	id, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}}
	authority := daemonOrchestrationAuthority{daemon: d}
	before, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(before.Candidates, id); got != "runnable" {
		t.Fatalf("before class = %q", got)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "cross-process", IssueID: id, DecisionKey: "policy", OrchestrationScope: "project", Question: "Which policy?", Why: "Human choice required", RequiredDecisions: []string{"select policy"}, Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose policy"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := writer.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	after, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(after.Candidates, id); got != string(domain.OrchestrationCandidateDecisionWaiting) {
		t.Fatalf("after class = %q, want decision-waiting", got)
	}
	resolvedAt := now.Add(time.Second)
	request.FinalAnswer = &domain.InteractionAnswerAudit{Answer: domain.InteractionAnswerPayload{
		SelectedOption:             "use the safe policy",
		Rationale:                  "The safe policy preserves the required constraints.",
		SignificanceRecommendation: domain.InteractionSignificanceMaterial,
		Revision:                   request.Revision,
	}, Actor: "human", CreatedAt: resolvedAt}
	resolved, err := request.Transition(domain.InteractionResolved, 1, resolvedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.UpdateInteraction(ctx, resolved, 1); err != nil {
		t.Fatal(err)
	}
	woken, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(woken.Candidates, id); got != "runnable" {
		t.Fatalf("resolved class = %q, want runnable", got)
	}
}

func TestProjectOrchestrationSnapshotCapturesPostPreparationInteractionState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	issueID, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "barrier-resolution", IssueID: issueID, DecisionKey: "policy", OrchestrationScope: "project", Question: "Which policy?", Why: "Human choice required", RequiredDecisions: []string{"select policy"}, Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose policy"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := reader.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	exportedCheckpoint, err := reader.ProjectionSourceCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}}
	d.snapshotAdmissionContext = context.WithCancel
	d.orchestrationProjectionExported = func() {
		resolvedAt := now.Add(time.Second)
		request.FinalAnswer = &domain.InteractionAnswerAudit{Answer: domain.InteractionAnswerPayload{SelectedOption: "safe", Rationale: "preserve constraints", SignificanceRecommendation: domain.InteractionSignificanceMaterial, Revision: request.Revision}, Actor: "human", CreatedAt: resolvedAt}
		resolved, transitionErr := request.Transition(domain.InteractionResolved, 1, resolvedAt)
		if transitionErr != nil {
			t.Fatal(transitionErr)
		}
		if updateErr := writer.UpdateInteraction(ctx, resolved, 1); updateErr != nil {
			t.Fatal(updateErr)
		}
		d.orchestrationProjectionExported = nil
	}
	snapshot, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(snapshot.Candidates, issueID); got != "runnable" {
		t.Fatalf("candidate class = %q, want runnable from final coherent export", got)
	}
	if reason := snapshot.Blocked[issueID]; strings.Contains(reason, "decision") {
		t.Fatalf("blocked reason = %q, must not retain superseded interaction", reason)
	}
	if !slices.Contains(snapshot.Runnable, issueID) {
		t.Fatalf("runnable = %v, want resolved candidate", snapshot.Runnable)
	}
	if len(snapshot.Interactions) != 0 {
		t.Fatalf("interactions = %+v, want resolved request absent", snapshot.Interactions)
	}
	checkpoint, err := reader.ProjectionSourceCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProjectionRevision != checkpoint || checkpoint == exportedCheckpoint {
		t.Fatalf("projection revision = %d, initial=%d current=%d; want final coherent cursor", snapshot.ProjectionRevision, exportedCheckpoint, checkpoint)
	}
}

func TestProjectOrchestrationSnapshotCapturesCandidateInsertedAfterPreparation(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	initialID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Initial", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}

	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	d.snapshotAdmissionContext = context.WithCancel
	preparedIDs := make([][]string, 0, 2)
	insertedID := ""
	d.orchestrationSnapshotPrepared = func(_ uint64, issueIDs []string) {
		preparedIDs = append(preparedIDs, append([]string(nil), issueIDs...))
		if insertedID != "" {
			return
		}
		insertedID, err = client.Create(ctx, issues.CreateTaskParams{Title: "Inserted", Description: "Added after preparation", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
		if err != nil {
			t.Fatal(err)
		}
	}

	snapshot, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(preparedIDs) != 1 {
		t.Fatalf("preparation attempts = %d, want one coherent export", len(preparedIDs))
	}
	if !slices.Contains(preparedIDs[0], initialID) || slices.Contains(preparedIDs[0], insertedID) {
		t.Fatalf("initial prepared candidates = %v, want only initial %s before insertion %s", preparedIDs[0], initialID, insertedID)
	}
	if got := candidateClass(snapshot.Candidates, insertedID); got != "runnable" {
		t.Fatalf("inserted candidate class = %q, want final coherent candidate", got)
	}
	checkpoint, err := client.ProjectionSourceCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProjectionRevision != checkpoint {
		t.Fatalf("accepted projection revision = %d, want final cursor %d", snapshot.ProjectionRevision, checkpoint)
	}
}

func TestProjectOrchestrationSnapshotCapturesPostPreparationReviewEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	issueID, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Review candidate", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	exportedCheckpoint, err := reader.ProjectionSourceCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}}
	d.snapshotAdmissionContext = context.WithCancel
	var appended domain.IssueObservationEvent
	d.orchestrationProjectionExported = func() {
		d.orchestrationProjectionExported = nil
		appended, err = writer.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := reader.ProjectionSourceCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProjectionRevision != checkpoint || checkpoint == exportedCheckpoint || appended.ID == 0 {
		t.Fatalf("snapshot revision=%d initial=%d current=%d appended=%d, want final coherent cursor", snapshot.ProjectionRevision, exportedCheckpoint, checkpoint, appended.ID)
	}
	if len(snapshot.ReviewQueue) != 1 || snapshot.ReviewQueue[0].IssueID != issueID || snapshot.ReviewQueue[0].Evidence == nil {
		t.Fatalf("review queue = %+v, want independently revalidated exact review admission", snapshot.ReviewQueue)
	}
	foundRecent := false
	for _, event := range snapshot.RecentEvents {
		foundRecent = foundRecent || event.Seq == appended.ID
	}
	if !foundRecent {
		t.Fatalf("recent events = %+v, want final exported event %d", snapshot.RecentEvents, appended.ID)
	}
}

func TestProjectOrchestrationSnapshotExcludesPostCursorDecisionAndReviewOutcomes(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	path := filepath.Join(repoDir, "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	decisionIssue, err := reader.Create(ctx, issues.CreateTaskParams{Title: "decision candidate", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	reviewIssue := createReviewTask(t, ctx, reader, domain.P1, "orchestrator")
	d := newOrchestrationReviewTestDaemon(repoDir, reader)
	d.orchestrationSnapshotAuxiliaryRead = func(context.Context) error {
		d.orchestrationSnapshotAuxiliaryRead = nil
		if _, appendErr := writer.AppendIssueObservationEvent(ctx, decisionIssue, issues.IssueObservationEventParams{
			Type: domain.IssueEventDecisionChanged, Source: "daemon-decision", SourceCommand: protocol.CommandDecisionUpdate,
			Payload: map[string]any{"decision_id": "decision-after-cursor", "revision": int64(1), "material": true},
		}); appendErr != nil {
			return appendErr
		}
		_, appendErr := writer.AppendIssueObservationEvent(ctx, reviewIssue, issues.IssueObservationEventParams{
			Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: string(protocol.OrchestrationIntentReviewAccept),
			Payload: map[string]any{"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": "reviewer"},
		})
		return appendErr
	}

	current, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if pending := current.PendingDecisions[decisionIssue]; len(pending) != 0 {
		t.Fatalf("current cursor pending decisions = %+v, want post-cursor decision excluded", pending)
	}
	if review := reviewByIssueID(current.ReviewQueue, reviewIssue); slices.Contains(review.Reasons, "accepted-close-pending") {
		t.Fatalf("current cursor review = %+v, want post-cursor trusted outcome excluded", review)
	}

	next, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if pending := next.PendingDecisions[decisionIssue]; len(pending) != 1 || pending[0].DecisionID != "decision-after-cursor" {
		t.Fatalf("next cursor pending decisions = %+v, want appended decision", pending)
	}
	if review := reviewByIssueID(next.ReviewQueue, reviewIssue); !slices.Contains(review.Reasons, "accepted-close-pending") {
		t.Fatalf("next cursor review = %+v, want appended trusted outcome", review)
	}
	if next.ProjectionRevision == current.ProjectionRevision {
		t.Fatalf("projection revision did not advance: current=%d next=%d", current.ProjectionRevision, next.ProjectionRevision)
	}
}

func reviewByIssueID(queue []protocol.OrchestrationReview, issueID string) protocol.OrchestrationReview {
	for _, review := range queue {
		if review.IssueID == issueID {
			return review
		}
	}
	return protocol.OrchestrationReview{}
}

func TestProjectOrchestrationSnapshotRemainsAvailableDuringPostExportRevisionChurn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	issueID, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Candidate", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	exportedCheckpoint, err := reader.ProjectionSourceCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}}
	d.snapshotAdmissionContext = context.WithCancel
	exports := 0
	d.orchestrationProjectionExported = func() {
		exports++
		if _, appendErr := writer.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventProgressRecorded, Source: "churn", Payload: map[string]any{"attempt": exports},
		}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	body := mustMarshal(t, protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	resp, err := d.handleOrchestrationSnapshot(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Command: protocol.CommandOrchestrationSnapshot, Meta: protocol.Metadata{ProjectID: "proj"}, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Error != nil {
		t.Fatalf("churning snapshot response = %+v, want available coherent export", resp.Error)
	}
	if exports != 1 {
		t.Fatalf("projection export attempts = %d, want one", exports)
	}
	var snapshot protocol.OrchestrationSnapshot
	if err := json.Unmarshal(resp.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	currentCheckpoint, checkpointErr := reader.ProjectionSourceCheckpoint(ctx)
	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
	if snapshot.ProjectionRevision != currentCheckpoint || currentCheckpoint == exportedCheckpoint {
		t.Fatalf("projection revision = %d, initial=%d current=%d; want final coherent cursor", snapshot.ProjectionRevision, exportedCheckpoint, currentCheckpoint)
	}
}

func TestProjectOrchestrationSnapshotCapturesAuxiliaryReadinessInputsOnce(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	worktrees := make([]gitservice.Worktree, 0, 100)
	gitSubjects := make(map[string]string, 100)
	for i := range 50 {
		rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: fmt.Sprintf("Root %02d", i), Description: "Coordinate", Acceptance: "Children done", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusOpen})
		if err != nil {
			t.Fatal(err)
		}
		closedID, err := client.Create(ctx, issues.CreateTaskParams{Title: fmt.Sprintf("Closed %02d", i), Description: "Integrated", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusDone, ParentID: &rootID})
		if err != nil {
			t.Fatal(err)
		}
		activeID, err := client.Create(ctx, issues.CreateTaskParams{Title: fmt.Sprintf("Active %02d", i), Description: "Working", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen, ParentID: &rootID})
		if err != nil {
			t.Fatal(err)
		}
		rootBranch, activeBranch := "root/"+rootID, "active/"+activeID
		worktrees = append(worktrees,
			gitservice.Worktree{IssueID: rootID, Path: "/repo", Branch: rootBranch},
			gitservice.Worktree{IssueID: activeID, Path: "/repo", Branch: activeBranch},
		)
		gitSubjects[rootBranch] = closedID + ": integrated evidence"
		gitSubjects[activeBranch] = activeID + ": active work"
	}
	operationReads, interactionReads, observationReads, worktreeReads, mailboxReads := 0, 0, 0, 0, 0
	gitRunner := &snapshotCountingGitRunner{subjects: gitSubjects}
	d := &Daemon{cfg: Config{RepoDir: t.TempDir(), Logger: slog.Default()}, git: gitservice.NewClient(gitRunner, slog.Default()), issueClientsByProject: map[string]*issues.Client{"proj": client}}
	d.taskGraphOperationList = func(context.Context, daemonops.Query) ([]daemonops.Record, error) {
		operationReads++
		return nil, nil
	}
	d.taskGraphUnresolvedInteractionIDs = func(context.Context, string) (map[string]struct{}, error) {
		interactionReads++
		return nil, nil
	}
	d.taskGraphObservationEvents = func(_ context.Context, _ string, issueIDs []string) issues.ProjectIssueObservationCapture {
		observationReads++
		if len(issueIDs) == 0 {
			return issues.ProjectIssueObservationCapture{}
		}
		issueID := naming.IssueID(issueIDs[0])
		events := map[string][]domain.IssueObservationEvent{
			issueID.String(): {{ID: 77, IssueID: issueID, Type: domain.IssueEventProgressRecorded, ObservedAt: time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC), Source: "worker", Payload: map[string]any{"body": "still working"}}},
		}
		return issues.ProjectIssueObservationCapture{RecentByIssue: events, StewardshipByIssue: events}
	}
	d.taskGraphWorktrees = func(context.Context, string) ([]gitservice.Worktree, error) {
		worktreeReads++
		return worktrees, nil
	}
	d.taskGraphMailboxRead = func(string, string) ([]daemonMailEvent, error) {
		mailboxReads++
		return nil, nil
	}
	for _, tc := range []struct{ limit, roots int }{{2, 2}, {100, 50}} {
		limit := tc.limit
		operationReads, interactionReads, observationReads, worktreeReads, mailboxReads, gitRunner.calls = 0, 0, 0, 0, 0, 0
		snapshot, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Roots) != tc.roots {
			t.Fatalf("limit %d roots = %d, want %d", limit, len(snapshot.Roots), tc.roots)
		}
		if operationReads != 3 {
			t.Fatalf("limit %d operation reads = %d, want three snapshot-wide maps", limit, operationReads)
		}
		if interactionReads != 0 {
			t.Fatalf("limit %d auxiliary interaction reads = %d, want exported interaction map only", limit, interactionReads)
		}
		if observationReads != 1 || worktreeReads != 1 || gitRunner.calls != 0 {
			t.Fatalf("limit %d project captures = observations:%d worktrees:%d git:%d, want projection reads and zero Git calls", limit, observationReads, worktreeReads, gitRunner.calls)
		}
		if mailboxReads != 0 {
			t.Fatalf("limit %d mailbox file reads = %d, want durable observation projection only", limit, mailboxReads)
		}
		if len(snapshot.RecentEvents) != 1 || snapshot.RecentEvents[0].Seq != 77 || snapshot.RecentEvents[0].Type != "worker-progress" || snapshot.RecentEvents[0].Body != "still working" {
			t.Fatalf("limit %d recent durable events = %+v", limit, snapshot.RecentEvents)
		}
	}
}

func TestContainmentCaptureUsesProjectedBehindCountWithoutGit(t *testing.T) {
	root, closed, active := naming.IssueID("root"), naming.IssueID("closed"), naming.IssueID("active")
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusOpen, HasWorktree: true},
		{ID: closed, ParentID: &root, Type: domain.TypeTask, Status: domain.StatusDone},
		{ID: active, ParentID: &root, Type: domain.TypeTask, Status: domain.StatusOpen, HasWorktree: true, GitBehindCount: 3},
	}
	risks := captureProjectedTaskGraphContainmentRisks(tasks, []string{root.String()}, []gitservice.Worktree{
		{IssueID: root.String(), Path: "/repo", Branch: "root"},
		{IssueID: active.String(), Path: "/repo", Branch: "active"},
	}, nil)[root.String()]
	if len(risks) != 1 || risks[0].Classification != "stale_child_branch" || risks[0].IssueID != active.String() || risks[0].RootBranch != "root" || !strings.Contains(risks[0].Message, "behind ancestor branch root by 3 commit(s)") {
		t.Fatalf("risks = %+v, want projected stale-child risk", risks)
	}
}

func TestContainmentCaptureSurfacesWorktreeProjectionFailure(t *testing.T) {
	root, closed, active := naming.IssueID("root"), naming.IssueID("closed"), naming.IssueID("active")
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusOpen, HasWorktree: true},
		{ID: closed, ParentID: &root, Type: domain.TypeTask, Status: domain.StatusDone},
		{ID: active, ParentID: &root, Type: domain.TypeTask, Status: domain.StatusInProgress, HasWorktree: true},
	}
	risks := captureProjectedTaskGraphContainmentRisks(tasks, []string{root.String()}, nil, errors.New("transient worktree list failure"))[root.String()]
	if len(risks) != 1 || risks[0].Classification != "containment_evidence_incomplete" || risks[0].IssueID != active.String() || !strings.Contains(risks[0].Message, "capture failed") {
		t.Fatalf("list failure risks = %+v, want explicit incomplete active-branch risk", risks)
	}
}

func TestContainmentCaptureSurfacesProjectedButMissingWorktrees(t *testing.T) {
	root, closed, active := naming.IssueID("root"), naming.IssueID("closed"), naming.IssueID("active")
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusOpen, HasWorktree: true},
		{ID: closed, ParentID: &root, Type: domain.TypeTask, Status: domain.StatusDone},
		{ID: active, ParentID: &root, Type: domain.TypeTask, Status: domain.StatusInProgress, HasWorktree: true},
	}
	risks := captureProjectedTaskGraphContainmentRisks(tasks, []string{root.String()}, []gitservice.Worktree{
		{IssueID: root.String(), Path: "/repo", Branch: "root"},
	}, nil)[root.String()]
	if len(risks) != 1 || risks[0].IssueID != active.String() || !strings.Contains(risks[0].Message, "active worktree projection is missing") {
		t.Fatalf("missing active projection risks = %+v, want explicit incomplete result", risks)
	}

	risks = captureProjectedTaskGraphContainmentRisks(tasks, []string{root.String()}, []gitservice.Worktree{
		{IssueID: active.String(), Path: "/repo", Branch: "active"},
	}, nil)[root.String()]
	if len(risks) != 1 || risks[0].IssueID != active.String() || !strings.Contains(risks[0].Message, "root worktree projection is missing") {
		t.Fatalf("missing root projection risks = %+v, want explicit incomplete result", risks)
	}
}

type snapshotCountingGitRunner struct {
	calls    int
	subjects map[string]string
}

func (r *snapshotCountingGitRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls++
	baseHash := fmt.Sprintf("%040x", 1)
	var out strings.Builder
	index := 2
	for _, arg := range args {
		if _, ok := r.subjects[arg]; !ok {
			continue
		}
		hash := fmt.Sprintf("%040x", index)
		index++
		fmt.Fprintf(&out, "\x1e%s\x00%s\x00refs/heads/%s\x00%s\n\nshared.go\n", hash, baseHash, arg, r.subjects[arg])
	}
	fmt.Fprintf(&out, "\x1e%s\x00\x00\x00base\n", baseHash)
	return out.String(), nil
}

func TestProjectOrchestrationRealBuilderRemainsCoherentDuringProjectionChurn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	guardedID, err := reader.Create(ctx, issues.CreateTaskParams{Title: "Guarded", Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "churn-guard", IssueID: guardedID, DecisionKey: "policy", OrchestrationScope: "project", Question: "Which policy?", Why: "Human choice required", RequiredDecisions: []string{"select policy"}, Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose policy"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := reader.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}}
	d.snapshotAdmissionContext = context.WithCancel
	mutateAfterExport := false
	mutation := 0
	d.orchestrationSnapshotAuxiliaryRead = func(context.Context) error {
		if !mutateAfterExport {
			return nil
		}
		mutateAfterExport = false
		mutation++
		if _, createErr := writer.Create(ctx, issues.CreateTaskParams{Title: fmt.Sprintf("Churn %d", mutation), Description: "Revision churn", Acceptance: "Recorded", Type: domain.TypeTask, Priority: domain.P4, Status: domain.StatusOpen}); createErr != nil {
			return createErr
		}
		return nil
	}
	for i := range 25 {
		mutateAfterExport = true
		snapshot, snapshotErr := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
		if snapshotErr != nil {
			t.Fatalf("snapshot %d: %v", i, snapshotErr)
		}
		if got := candidateClass(snapshot.Candidates, guardedID); got != string(domain.OrchestrationCandidateDecisionWaiting) {
			t.Fatalf("snapshot %d candidate class = %q", i, got)
		}
		if slices.Contains(snapshot.Runnable, guardedID) || snapshot.Blocked[guardedID] == "" {
			t.Fatalf("snapshot %d readiness contradiction: runnable=%v blocked=%q", i, snapshot.Runnable, snapshot.Blocked[guardedID])
		}
	}
	if mutation != 25 {
		t.Fatalf("post-export mutations = %d, want one deterministic revision advance per snapshot", mutation)
	}
}

func TestProjectOrchestrationSnapshotConvergesDuringContinuousRevisionChurn(t *testing.T) {
	const projectID = "project"
	d := &Daemon{revision: map[string]uint64{projectID: 1}}
	builds := 0
	d.orchestrationSnapshotBuild = func(_ context.Context, gotProjectID string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		builds++
		d.nextRevision(gotProjectID)
		return protocol.OrchestrationSnapshot{Scope: request.Scope, ProjectionRevision: 41, ProjectionAuthority: protocol.OrchestrationProjectionAuthoritySQLite, Blocked: map[string]string{}}, nil
	}
	body, err := json.Marshal(protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope()})
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	for call := 1; call <= 100; call++ {
		resp, err := d.handleOrchestrationSnapshot(context.Background(), protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
		if err != nil {
			t.Fatalf("call %d handler error: %v", call, err)
		}
		if !resp.OK || resp.Error != nil {
			t.Fatalf("call %d response = %+v, want successful project snapshot", call, resp)
		}
		var snapshot protocol.OrchestrationSnapshot
		if err := json.Unmarshal(resp.Body, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Revision != uint64(call+1) || snapshot.ProjectionRevision != 41 || snapshot.ProjectionAuthority != protocol.OrchestrationProjectionAuthoritySQLite {
			t.Fatalf("call %d consistency = event %d projection %d authority %q", call, snapshot.Revision, snapshot.ProjectionRevision, snapshot.ProjectionAuthority)
		}
	}
	if builds != 100 {
		t.Fatalf("snapshot builds = %d, want one per finite call", builds)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("100 continuously churning project snapshots took %s, want <= 2s", elapsed)
	}
}

func TestRootedOrchestrationSnapshotRetainsStableRevisionGuard(t *testing.T) {
	const projectID = "project"
	d := &Daemon{revision: map[string]uint64{projectID: 1}}
	builds := 0
	d.orchestrationSnapshotBuild = func(_ context.Context, gotProjectID string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		builds++
		d.nextRevision(gotProjectID)
		return protocol.OrchestrationSnapshot{Scope: request.Scope, Blocked: map[string]string{}}, nil
	}
	scope, err := domain.RootedOrchestrationScope("root")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(protocol.OrchestrationSnapshotRequest{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleOrchestrationSnapshot(context.Background(), protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("response = %+v, want rooted projection conflict", resp)
	}
	if builds != 1 {
		t.Fatalf("snapshot builds = %d, want one coalesced rooted attempt", builds)
	}
}

func TestOrchestrationSnapshotSingleflightCoalescesWatchAndFiniteReads(t *testing.T) {
	d := &Daemon{}
	started := make(chan struct{})
	release := make(chan struct{})
	builds := 0
	var buildsMu sync.Mutex
	d.orchestrationSnapshotBuild = func(_ context.Context, _ string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		buildsMu.Lock()
		builds++
		if builds == 1 {
			close(started)
		}
		buildsMu.Unlock()
		<-release
		return protocol.OrchestrationSnapshot{Scope: request.Scope, ProjectionRevision: 9}, nil
	}
	authority := d.orchestrationAuthority()
	request := protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", Limit: 50}
	errCh := make(chan error, 2)
	go func() {
		defaultLimitRequest := request
		defaultLimitRequest.Limit = 0
		_, err := authority.Snapshot(context.Background(), "project", defaultLimitRequest)
		errCh <- err
	}()
	<-started
	go func() {
		_, err := authority.Snapshot(context.Background(), "project", request)
		errCh <- err
	}()
	key := orchestrationSnapshotLoadKey("project", request)
	deadline := time.Now().Add(time.Second)
	for {
		d.orchestrationSnapshotLoadMu.Lock()
		load := d.orchestrationSnapshotLoads[key]
		joined := load != nil && load.waiters > 0
		d.orchestrationSnapshotLoadMu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second snapshot caller did not join shared load")
		}
		runtime.Gosched()
	}
	close(release)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	buildsMu.Lock()
	defer buildsMu.Unlock()
	if builds != 1 {
		t.Fatalf("snapshot builds = %d, want one shared build", builds)
	}
}

func TestOrchestrationSnapshotKeysSeparateAndCanonicalizeReviewIssueScope(t *testing.T) {
	base := protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", Limit: 50}
	left := base
	left.ReviewIssueIDs = normalizedReviewSnapshotIssueIDs([]string{"TICKET-B", "ticket-a", "Ticket-B"})
	right := base
	right.ReviewIssueIDs = normalizedReviewSnapshotIssueIDs([]string{"ticket-a", "ticket-b"})
	other := base
	other.ReviewIssueIDs = normalizedReviewSnapshotIssueIDs([]string{"ticket-c"})

	if orchestrationSnapshotLoadKey("project", left) != orchestrationSnapshotLoadKey("project", right) {
		t.Fatal("equivalent requested-ticket review scopes produced different singleflight keys")
	}
	if orchestrationSnapshotCacheKey("project", left) != orchestrationSnapshotCacheKey("project", right) {
		t.Fatal("equivalent requested-ticket review scopes produced different cache keys")
	}
	if orchestrationSnapshotLoadKey("project", left) == orchestrationSnapshotLoadKey("project", other) {
		t.Fatal("different requested-ticket review scopes shared a singleflight key")
	}
	if orchestrationSnapshotCacheKey("project", left) == orchestrationSnapshotCacheKey("project", other) {
		t.Fatal("different requested-ticket review scopes shared a cache key")
	}
}

func TestRequestedReviewSnapshotDoesNotJoinActiveProjectWatchLoad(t *testing.T) {
	d := &Daemon{}
	watchStarted := make(chan struct{})
	releaseWatch := make(chan struct{})
	d.orchestrationSnapshotBuild = func(_ context.Context, _ string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		if len(request.ReviewIssueIDs) > 0 {
			return protocol.OrchestrationSnapshot{
				Scope: request.Scope, ReviewQueue: []protocol.OrchestrationReview{{IssueID: request.ReviewIssueIDs[0]}},
			}, nil
		}
		close(watchStarted)
		<-releaseWatch
		return protocol.OrchestrationSnapshot{Scope: request.Scope}, nil
	}
	authority := d.orchestrationAuthority()
	watchDone := make(chan error, 1)
	go func() {
		_, err := authority.Snapshot(context.Background(), "project", protocol.OrchestrationSnapshotRequest{
			Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator",
		})
		watchDone <- err
	}()
	<-watchStarted

	review, err := authority.Snapshot(context.Background(), "project", protocol.OrchestrationSnapshotRequest{
		Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", ReviewIssueIDs: []string{"review-me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(review.ReviewQueue) != 1 || !naming.IssueIDsEqual(review.ReviewQueue[0].IssueID, "review-me") {
		t.Fatalf("requested review snapshot = %+v, want independent bounded load", review.ReviewQueue)
	}
	close(releaseWatch)
	if err := <-watchDone; err != nil {
		t.Fatal(err)
	}
}

func TestOrchestrationSnapshotSingleflightLeaderCancellationDoesNotPoisonJoiner(t *testing.T) {
	d := &Daemon{}
	started := make(chan struct{})
	release := make(chan struct{})
	d.orchestrationSnapshotBuild = func(ctx context.Context, _ string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		close(started)
		select {
		case <-ctx.Done():
			return protocol.OrchestrationSnapshot{}, ctx.Err()
		case <-release:
			return protocol.OrchestrationSnapshot{Scope: request.Scope, ProjectionRevision: 9}, nil
		}
	}
	authority := d.orchestrationAuthority()
	request := protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", Limit: 50}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := authority.Snapshot(leaderCtx, "project", request)
		leaderErr <- err
	}()
	<-started
	joinerResult := make(chan error, 1)
	go func() {
		_, err := authority.Snapshot(context.Background(), "project", request)
		joinerResult <- err
	}()
	key := orchestrationSnapshotLoadKey("project", request)
	deadline := time.Now().Add(time.Second)
	for {
		d.orchestrationSnapshotLoadMu.Lock()
		load := d.orchestrationSnapshotLoads[key]
		joined := load != nil && load.waiters > 0
		d.orchestrationSnapshotLoadMu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("joiner did not attach to leader load")
		}
		runtime.Gosched()
	}
	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context canceled", err)
	}
	close(release)
	if err := <-joinerResult; err != nil {
		t.Fatalf("joiner inherited leader cancellation: %v", err)
	}
}

func TestOrchestrationSnapshotSingleflightCanonicalizesEffectiveRepoDir(t *testing.T) {
	repoDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(repoDir, alias); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir}}
	started := make(chan struct{})
	release := make(chan struct{})
	builds := 0
	var buildsMu sync.Mutex
	d.orchestrationSnapshotBuild = func(_ context.Context, _ string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		buildsMu.Lock()
		builds++
		if builds == 1 {
			close(started)
		}
		buildsMu.Unlock()
		<-release
		return protocol.OrchestrationSnapshot{Scope: request.Scope}, nil
	}
	authority := d.orchestrationAuthority()
	base := protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator"}
	errCh := make(chan error, 2)
	go func() {
		_, err := authority.Snapshot(context.Background(), "project", base)
		errCh <- err
	}()
	<-started
	withAlias := base
	withAlias.RepoDir = alias
	go func() {
		_, err := authority.Snapshot(context.Background(), "project", withAlias)
		errCh <- err
	}()
	keyRequest := base
	keyRequest.Limit = defaultOrchestrationInspectLimit
	keyRequest.RepoDir = d.canonicalOrchestrationRepoDir("project", repoDir)
	key := orchestrationSnapshotLoadKey("project", keyRequest)
	deadline := time.Now().Add(time.Second)
	for {
		d.orchestrationSnapshotLoadMu.Lock()
		load := d.orchestrationSnapshotLoads[key]
		joined := load != nil && load.waiters > 0
		d.orchestrationSnapshotLoadMu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("repo alias request did not join effective repo load")
		}
		runtime.Gosched()
	}
	close(release)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	buildsMu.Lock()
	defer buildsMu.Unlock()
	if builds != 1 {
		t.Fatalf("snapshot builds = %d, want one canonical repo build", builds)
	}
}

func TestProjectOrchestrationSnapshotDerivesRootsFromSharedProjection(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	for _, root := range []string{"Root A", "Root B"} {
		rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: root, Description: "Coordinate children", Acceptance: "Children complete", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusOpen})
		if err != nil {
			t.Fatal(err)
		}
		childID, err := client.Create(ctx, issues.CreateTaskParams{Title: root + " child", Description: "Implement scoped work", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.AddDependency(ctx, childID, rootID, string(domain.DependencyParentChild)); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	snapshot, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Roots) != 2 || len(snapshot.Runnable) != 0 {
		t.Fatalf("shared project projection = roots %v runnable %v", snapshot.Roots, snapshot.Runnable)
	}
	if snapshot.Source.Projector.ID == "" || snapshot.Source.SemanticChecksum == "" {
		t.Fatalf("shared project projection source = %+v", snapshot.Source)
	}
}

func issueIDPtr(value string) *naming.IssueID { id := naming.IssueID(value); return &id }
