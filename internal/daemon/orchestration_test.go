package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

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

func TestProjectOrchestrationApplyAutomaticallyBacklogsPrematureCandidate(t *testing.T) {
	ctx := context.Background()
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
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
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
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

func TestOrchestrationSkipReasonPreservesNestedRootAuthority(t *testing.T) {
	nested := map[string]struct{}{"az-nested": {}}
	active := map[string]struct{}{"az-active": {}}
	blocked := map[string]string{"az-blocked": "dependency remains open"}

	if got := orchestrationSkipReason("az-nested", nested, active, blocked); got != "nested-root-start-orchestrator-session: az session start az-nested" {
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

func TestOrchestrationGlobalActiveCountIncludesUninspectedRoots(t *testing.T) {
	tasks := []domain.Task{{ID: "visible", HasTmuxSession: true}, {ID: "outside-limit", HasTmuxSession: true}, {ID: "inactive"}}
	if got := orchestrationGlobalActiveCount(tasks); got != 2 {
		t.Fatalf("active count = %d, want 2", got)
	}
}

func TestProjectOrchestrationSnapshotRefreshesCrossProcessOwnership(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := issues.NewClientAtPath(path, slog.Default())
	writer := issues.NewClientAtPath(path, slog.Default())
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

func TestProjectOrchestrationSnapshotRefreshesCrossProcessInteractions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	reader := issues.NewClientAtPath(path, slog.Default())
	writer := issues.NewClientAtPath(path, slog.Default())
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
	request.FinalAnswer = &domain.InteractionAnswerAudit{Answer: "use the safe policy", Actor: "human", CreatedAt: resolvedAt}
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

func candidateClass(candidates []protocol.OrchestrationCandidate, issueID string) string {
	for _, candidate := range candidates {
		if candidate.IssueID == issueID {
			return candidate.Classification
		}
	}
	return ""
}

func issueIDPtr(value string) *naming.IssueID { id := naming.IssueID(value); return &id }
