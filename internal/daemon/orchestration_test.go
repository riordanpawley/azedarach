package daemon

import (
	"context"
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
	nested := protocol.OrchestrationSnapshot{NestedRoots: []protocol.OrchestrationNestedRoot{{IssueID: "nested"}}}
	if rootedOrchestratorContinuationRequired(true, nested) {
		t.Fatal("complete-check pass still requires continuation")
	}
	nested.Interactions = []domain.InteractionRequest{{ID: "human-acceptance"}}
	if rootedOrchestratorContinuationRequired(false, nested) {
		t.Fatal("unresolved human acceptance still requires continuation")
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

func TestProjectCandidateRoutesDoNotAutomaticallyRouteOwnedPrematureWork(t *testing.T) {
	snapshot := protocol.OrchestrationSnapshot{Candidates: []protocol.OrchestrationCandidate{{
		IssueID: "foreign", Classification: string(domain.OrchestrationCandidateOwnedElsewhere),
		Executability: domain.IssueExecutabilityAssessment{Disposition: domain.IssuePremature, Reasons: []string{"missing-scope"}},
	}}}
	if routes := projectCandidateRoutes(snapshot, nil); len(routes) != 0 {
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

func TestProjectStewardshipContextCollectsRecentRootMailboxEvents(t *testing.T) {
	repoDir := t.TempDir()
	created := time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC)
	for _, event := range []daemonMailEvent{
		{Seq: 1, ParentIssue: "az-a", IssueID: "az-worker-a", Type: "worker-progress", Body: "a", CreatedAt: created},
		{Seq: 1, ParentIssue: "az-b", IssueID: "az-worker-b", Type: "worker-integration-ready", Body: "b", CreatedAt: created.Add(time.Second)},
	} {
		if err := appendMailboxEvent(repoDir, event); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := protocol.OrchestrationSnapshot{Scope: domain.ProjectOrchestrationScope(), Roots: []string{"az-a", "az-b"}}
	authority := daemonOrchestrationAuthority{daemon: &Daemon{cfg: Config{RepoDir: repoDir}}}
	authority.enrichStewardshipContext(context.Background(), protocol.DefaultProjectID, &snapshot)
	if len(snapshot.RecentEvents) != 2 || snapshot.RecentEvents[0].ParentIssue != "az-a" || snapshot.RecentEvents[1].ParentIssue != "az-b" {
		t.Fatalf("recent events = %+v", snapshot.RecentEvents)
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
	health := orchestrationBoardHealth(tasks, map[string]domain.Task{"parent": parent, "missing-child": missing, "owned": malformedOwner}, 2, 2)
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
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}}
	authority := daemonOrchestrationAuthority{daemon: d}
	before, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(before.Candidates, id); got != "runnable" {
		t.Fatalf("before class = %q, want runnable", got)
	}
	_, err = writer.ClaimOwnershipWithRuntime(ctx, "proj", id, issues.OwnershipClaimParams{OwnerID: "other", OwnerKind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := authority.Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateClass(after.Candidates, id); got != string(domain.OrchestrationCandidateOwnedElsewhere) {
		t.Fatalf("after class = %q, want owned-elsewhere", got)
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

func TestProjectOrchestrationSnapshotKeepsExportedInteractionDecisionAcrossPostExportResolution(t *testing.T) {
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
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": reader}}
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
	if got := candidateClass(snapshot.Candidates, issueID); got != string(domain.OrchestrationCandidateDecisionWaiting) {
		t.Fatalf("candidate class = %q, want decision-waiting from exported checkpoint", got)
	}
	if reason := snapshot.Blocked[issueID]; !strings.Contains(reason, "unresolved interaction") {
		t.Fatalf("blocked reason = %q, want exported interaction blocker", reason)
	}
	if slices.Contains(snapshot.Runnable, issueID) {
		t.Fatalf("runnable = %v, must not contradict exported interaction", snapshot.Runnable)
	}
	if len(snapshot.Interactions) != 1 || snapshot.Interactions[0].ID != request.ID || snapshot.Interactions[0].State != domain.InteractionOpen {
		t.Fatalf("interactions = %+v, want one open request from exported checkpoint", snapshot.Interactions)
	}
	if snapshot.ProjectionRevision == 0 {
		t.Fatal("projection revision must identify the exported checkpoint")
	}
}

func TestProjectOrchestrationSnapshotCapturesAuxiliaryReadinessInputsOnce(t *testing.T) {
	ctx := context.Background()
	client := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	for i := range 50 {
		if _, err := client.Create(ctx, issues.CreateTaskParams{Title: fmt.Sprintf("Root %02d", i), Description: "Executable", Acceptance: "Done", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen}); err != nil {
			t.Fatal(err)
		}
	}
	operationReads := 0
	interactionReads := 0
	d := &Daemon{cfg: Config{Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"proj": client}}
	d.taskGraphOperationList = func(context.Context, daemonops.Query) ([]daemonops.Record, error) {
		operationReads++
		return nil, nil
	}
	d.taskGraphUnresolvedInteractionIDs = func(context.Context, string) (map[string]struct{}, error) {
		interactionReads++
		return nil, nil
	}
	for _, limit := range []int{1, 50} {
		operationReads, interactionReads = 0, 0
		snapshot, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Roots) != limit {
			t.Fatalf("limit %d roots = %d", limit, len(snapshot.Roots))
		}
		if operationReads != 3 {
			t.Fatalf("limit %d operation reads = %d, want three snapshot-wide maps", limit, operationReads)
		}
		if interactionReads != 0 {
			t.Fatalf("limit %d auxiliary interaction reads = %d, want exported interaction map only", limit, interactionReads)
		}
	}
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
	stop := make(chan struct{})
	writerErr := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				writerErr <- nil
				return
			default:
			}
			_, createErr := writer.Create(ctx, issues.CreateTaskParams{Title: fmt.Sprintf("Churn %d", i), Description: "Revision churn", Acceptance: "Recorded", Type: domain.TypeTask, Priority: domain.P4, Status: domain.StatusOpen})
			if createErr != nil {
				writerErr <- createErr
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	for i := range 25 {
		snapshot, snapshotErr := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "proj", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "self", Limit: 10})
		if snapshotErr != nil {
			close(stop)
			<-writerErr
			t.Fatalf("snapshot %d: %v", i, snapshotErr)
		}
		if got := candidateClass(snapshot.Candidates, guardedID); got != string(domain.OrchestrationCandidateDecisionWaiting) {
			close(stop)
			<-writerErr
			t.Fatalf("snapshot %d candidate class = %q", i, got)
		}
		if slices.Contains(snapshot.Runnable, guardedID) || !strings.Contains(snapshot.Blocked[guardedID], "unresolved interaction") {
			close(stop)
			<-writerErr
			t.Fatalf("snapshot %d readiness contradiction: runnable=%v blocked=%q", i, snapshot.Runnable, snapshot.Blocked[guardedID])
		}
	}
	close(stop)
	if err := <-writerErr; err != nil {
		t.Fatal(err)
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
	if builds != 3 {
		t.Fatalf("snapshot builds = %d, want three rooted attempts", builds)
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
	if len(snapshot.Roots) != 2 || len(snapshot.Runnable) != 2 {
		t.Fatalf("shared project projection = roots %v runnable %v", snapshot.Roots, snapshot.Runnable)
	}
	if snapshot.ProjectionRevision == 0 || snapshot.ProjectionAuthority != protocol.OrchestrationProjectionAuthoritySQLite {
		t.Fatalf("shared project projection consistency = revision %d authority %q", snapshot.ProjectionRevision, snapshot.ProjectionAuthority)
	}
}

func issueIDPtr(value string) *naming.IssueID { id := naming.IssueID(value); return &id }
