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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/observability/tracesqlite"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestProjectReviewQueuePrioritizesReviewAndExcludesForeignOwnedWork(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	first := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	foreign := createReviewTask(t, ctx, client, domain.P2, "worker-b")
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", foreign, issues.OwnershipClaimParams{OwnerID: "another-orchestrator", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseOrchestration}); err != nil {
		t.Fatal(err)
	}
	open, err := client.Create(ctx, issues.CreateTaskParams{Title: "new work", Description: "Executable", Acceptance: "done", Type: domain.TypeTask, Priority: domain.P0, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)

	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ReviewQueue) != 2 || snapshot.ReviewQueue[0].IssueID != first || snapshot.ReviewQueue[1].IssueID != foreign {
		t.Fatalf("review queue = %+v, want priority ordered %s,%s", snapshot.ReviewQueue, first, foreign)
	}
	if !snapshot.ReviewQueue[0].Actionable {
		t.Fatalf("owned review = %+v, want actionable", snapshot.ReviewQueue[0])
	}
	if snapshot.ReviewQueue[1].Actionable || !strings.Contains(strings.Join(snapshot.ReviewQueue[1].Reasons, " "), "orchestration-owned-by-another-orchestrator") {
		t.Fatalf("foreign review = %+v, want excluded ownership reason", snapshot.ReviewQueue[1])
	}
	if got := candidateClass(snapshot.Candidates, open); got != "runnable" {
		t.Fatalf("open candidate class = %q, want runnable while review queue remains separately prioritized", got)
	}
}

func TestReviewInspectionKeepsStableScopeAcrossCandidateRevisions(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	runner := &revisionReviewGitRunner{headRevision: "head-revision-1"}
	d.git = git.NewClient(runner, slog.Default())
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	worktrees := map[string]git.Worktree{issueID: {IssueID: issueID, Path: repoDir, Branch: "feature/review"}}

	authority := daemonOrchestrationAuthority{daemon: d}
	first := authority.reviewInspection(ctx, "project", repoDir, "orchestrator", task, map[string]domain.Task{issueID: task}, worktrees)
	runner.headRevision = "head-revision-2"
	second := authority.reviewInspection(ctx, "project", repoDir, "orchestrator", task, map[string]domain.Task{issueID: task}, worktrees)

	if first.DiffBaseRevision != "base-revision" || first.HeadRevision != "head-revision-1" {
		t.Fatalf("first revisions = base:%q head:%q", first.DiffBaseRevision, first.HeadRevision)
	}
	if second.DiffBaseRevision != "base-revision" || second.HeadRevision != "head-revision-2" {
		t.Fatalf("second revisions = base:%q head:%q", second.DiffBaseRevision, second.HeadRevision)
	}
	wantScope := "issue:" + issueID + ":base:main@base-revision"
	if first.DiffScope != wantScope || second.DiffScope != wantScope {
		t.Fatalf("diff scopes = first:%q second:%q, want stable %q", first.DiffScope, second.DiffScope, wantScope)
	}
	if first.DiffRange != "base-revision..head-revision-1" || second.DiffRange != "base-revision..head-revision-2" {
		t.Fatalf("diff ranges = first:%q second:%q", first.DiffRange, second.DiffRange)
	}
	if first.ReviewContext == nil || first.IntegrationContext == nil {
		t.Fatalf("bounded contexts missing: review=%+v integration=%+v", first.ReviewContext, first.IntegrationContext)
	}
	if first.ReviewContext.Role != domain.WorkflowRoleReviewer || first.IntegrationContext.Role != domain.WorkflowRoleIntegrator || first.ReviewContext.Provenance.SourceRevision != "head-revision-1" {
		t.Fatalf("bounded contexts = review:%+v integration:%+v", first.ReviewContext, first.IntegrationContext)
	}
	for _, packet := range []*domain.WorkflowContextPacket{first.ReviewContext, first.IntegrationContext} {
		encoded, err := domain.MarshalWorkflowContextPacket(*packet)
		if err != nil || len(encoded) > domain.WorkflowContextPacketMaxBytes {
			t.Fatalf("bounded context bytes=%d err=%v", len(encoded), err)
		}
		if strings.Contains(string(encoded), repoDir) {
			t.Fatalf("bounded context leaked local worktree path: %s", encoded)
		}
	}
	if incremental := first.HeadRevision + ".." + second.HeadRevision; incremental != "head-revision-1..head-revision-2" {
		t.Fatalf("incremental range = %q, want prior reviewed head through current head", incremental)
	}
}

type revisionReviewGitRunner struct {
	headRevision string
}

func (r *revisionReviewGitRunner) Run(_ context.Context, args ...string) (string, error) {
	command := strings.Join(args, " ")
	switch {
	case strings.Contains(command, " merge-base ") && strings.HasSuffix(command, " "+r.headRevision):
		return "base-revision\n", nil
	case strings.Contains(command, " rev-parse --verify HEAD"):
		return r.headRevision + "\n", nil
	case strings.Contains(command, " diff "):
		return "1 file changed, 1 insertion(+)\n", nil
	default:
		return "", fmt.Errorf("unexpected git command: %s", command)
	}
}

func TestProjectReviewQueueConsumesProjectionWithoutGitStatusRefresh(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker")
	tasks, err := client.GetManyWithRuntime(ctx, "project", []string{issueID})
	if err != nil {
		t.Fatal(err)
	}
	materializer := newProjectReadMaterializer("project", nil, nil)
	for _, task := range tasks {
		materializer.canonical[task.ID.String()] = task
		materializer.tasks[task.ID.String()] = task
	}
	materializer.metadata.Health = "healthy"
	materializer.replaceWorktrees(map[string]git.Worktree{issueID: {IssueID: issueID, Path: filepath.Join(repoDir, "review-worktree"), Branch: "worker/review"}})

	gitCalls := make([]string, 0, 2)
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		command := strings.Join(args, " ")
		gitCalls = append(gitCalls, command)
		switch {
		case strings.Contains(command, " rev-parse --verify HEAD"):
			return "projected-review-head\n", nil
		case strings.Contains(command, " merge-base "):
			return "projected-review-base\n", nil
		default:
			return "", errors.New("Git status/diff refresh must not run from orchestration snapshot")
		}
	}}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.git = git.NewClient(runner, slog.Default())
	d.materializersStarted = true
	d.materializers = map[string]*projectReadMaterializer{"project": materializer}
	d.worktreeManagersByProject = map[string]*git.WorktreeManager{"project": git.NewWorktreeManager(runner, repoDir, slog.Default())}

	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir})
	if err != nil {
		t.Fatalf("orchestration snapshot: %v", err)
	}
	if len(snapshot.ReviewQueue) != 1 || snapshot.ReviewQueue[0].IssueID != issueID {
		t.Fatalf("reviews = %+v, want projected review %s", snapshot.ReviewQueue, issueID)
	}
	for _, command := range gitCalls {
		if strings.Contains(command, " status ") || strings.Contains(command, " diff ") || strings.Contains(command, " rev-list ") || strings.Contains(command, " log ") || strings.Contains(command, " worktree ") {
			t.Fatalf("Git calls = %+v, want only immutable review identity pinning", gitCalls)
		}
	}
}

func TestProjectReviewQueueUsesOneObservationQueryForLargeOrdinaryGraph(t *testing.T) {
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	tasks := make([]domain.Task, 1000)
	for i := range tasks {
		tasks[i] = domain.Task{ID: naming.IssueID(fmt.Sprintf("ordinary-%04d", i)), Status: domain.StatusOpen}
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	counter := &tracesqlite.QueryCounter{}
	ctx := tracesqlite.WithQueryCounter(context.Background(), counter)
	queue, err := (daemonOrchestrationAuthority{daemon: d}).reviewQueue(ctx, "project", protocol.OrchestrationSnapshotRequest{ActorID: "orchestrator", Limit: 10}, tasks)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 0 {
		t.Fatalf("review queue = %+v, want no ordinary work", queue)
	}
	if got := counter.Count(); got != 1 {
		t.Fatalf("SQLite query count = %d for %d ordinary tasks, want one bounded latest-outcome query", got, len(tasks))
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (daemonOrchestrationAuthority{daemon: d}).reviewQueue(canceled, "project", protocol.OrchestrationSnapshotRequest{ActorID: "orchestrator", Limit: 10}, tasks); err == nil {
		t.Fatal("canceled accepted-outcome projection read succeeded, want fail-closed review queue")
	}
}

func TestProjectStartIntentDoesNotGloballyBlockOnActionableReview(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, "issues.db")
	client := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	reviewID := createReviewTask(t, ctx, client, domain.P1, "worker")
	openID, err := client.Create(ctx, issues.CreateTaskParams{Title: "new work", Description: "Executable", Acceptance: "done", Type: domain.TypeTask, Priority: domain.P0, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	if err := upsertSessionStateFixture(runtimeStore, ctx, "project", daemonstate.Session{ID: reviewID, IssueID: reviewID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.cfg.Orchestration.AgentCapacity = 1
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{"project": runtimeStore}

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "start-alongside-review", ActorID: "orchestrator", IssueIDs: []string{openID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Skipped[openID]; got != "global-agent-capacity-reached" {
		t.Fatalf("result = %+v, want capacity gate rather than review-priority:%s", result, reviewID)
	}
}

func TestProjectStartIntentRoutesPrematureWorkWhileReviewRemainsQueued(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	reviewID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	prematureID, err := client.Create(ctx, issues.CreateTaskParams{Title: "thin work", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", prematureID, issues.OwnershipClaimParams{OwnerID: "orchestrator", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseExecution}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "route-alongside-review", ActorID: "orchestrator", IssueIDs: []string{prematureID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Routed) != 1 || result.Routed[0].IssueID != prematureID || result.Skipped[prematureID] != "candidate-routed-backlog" {
		t.Fatalf("result = %+v, want premature candidate routed while review stays queued", result)
	}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir})
	if err != nil || len(snapshot.ReviewQueue) != 1 || snapshot.ReviewQueue[0].IssueID != reviewID {
		t.Fatalf("review queue = %+v err=%v, want %s preserved", snapshot.ReviewQueue, err, reviewID)
	}
	premature, err := client.GetWithRuntime(ctx, "project", prematureID)
	if err != nil {
		t.Fatal(err)
	}
	if premature.State.Workflow() != domain.IssueWorkflowBacklog || premature.Ownership != nil {
		t.Fatalf("premature task = %+v, want backlog with execution ownership released", premature)
	}
}

func TestReviewIntentValidationRejectsNonActionableOrConflictingOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		request protocol.OrchestrationIntentRequest
		want    string
	}{
		{name: "return without findings", request: protocol.OrchestrationIntentRequest{Kind: protocol.OrchestrationIntentReviewReturn}, want: "requires at least one"},
		{name: "empty finding", request: protocol.OrchestrationIntentRequest{Kind: protocol.OrchestrationIntentReviewReturn, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high"}}}, want: "requires finding text"},
		{name: "accept with findings", request: protocol.OrchestrationIntentRequest{Kind: protocol.OrchestrationIntentReviewAccept, Findings: []protocol.OrchestrationReviewFinding{{Finding: "conflict"}}}, want: "cannot include findings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateOrchestrationReviewIntent(tt.request); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReviewIntentScopesSnapshotAndRetriesPreAdmissionFailureAfterRestart(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	request := protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept,
		IntentKey: "retry-pre-admission-after-restart", ActorID: "orchestrator", IssueIDs: []string{strings.ToUpper(issueID)}, RepoDir: repoDir,
	}

	firstDaemon := newOrchestrationReviewTestDaemon(repoDir, client)
	firstDaemon.orchestrationSnapshotBuild = func(_ context.Context, _ string, snapshotRequest protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		want := []string{naming.CanonicalIssueIDKey(issueID)}
		if !slices.Equal(snapshotRequest.ReviewIssueIDs, want) {
			return protocol.OrchestrationSnapshot{}, fmt.Errorf("review snapshot issue IDs = %v, want canonical requested ticket %v", snapshotRequest.ReviewIssueIDs, want)
		}
		return protocol.OrchestrationSnapshot{}, errors.New("transient pre-admission failure")
	}
	if _, err := firstDaemon.orchestrationAuthority().Apply(ctx, "project", request); err == nil || !strings.Contains(err.Error(), "transient pre-admission failure") {
		t.Fatalf("first review acceptance error = %v, want transient pre-admission failure", err)
	}
	assertNoReviewAcceptanceSideEffects(t, ctx, client, issueID)

	restarted := newOrchestrationReviewTestDaemon(repoDir, client)
	churn := 0
	var churnErr error
	restarted.orchestrationProjectionExported = func() {
		churn++
		_, churnErr = client.Create(ctx, issues.CreateTaskParams{
			Title: "Unrelated restart churn", Description: "Advance projection during requested review admission", Acceptance: "Recorded",
			Type: domain.TypeTask, Priority: domain.P4, Status: domain.StatusOpen,
		})
	}
	result, err := restarted.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if churn != 1 {
		t.Fatalf("restarted requested-review exports = %d, want one coherent export", churn)
	}
	if churnErr != nil {
		t.Fatalf("advance unrelated projection after restart: %v", churnErr)
	}
	if failure := result.Failed[issueID]; !strings.Contains(failure, "authoritative close") {
		t.Fatalf("retried review acceptance = %+v, want progress through admission to expected close-adapter failure", result)
	}
	replayed, err := restarted.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if failure := replayed.Failed[issueID]; !strings.Contains(failure, "authoritative close") {
		t.Fatalf("replayed review acceptance = %+v, want idempotent accepted-close retry", replayed)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 1 || reviewEvents[0].Payload["outcome"] != "accepted" {
		t.Fatalf("review events = %+v, want exactly one accepted outcome after retry and replay", reviewEvents)
	}
}

func TestOrchestrationReviewRequestedTicketScopePreservesStandaloneAndRootedAuthority(t *testing.T) {
	root := domain.Task{ID: naming.IssueID("root"), Status: domain.StatusInReview}
	childParent := naming.IssueID("root")
	child := domain.Task{ID: naming.IssueID("child"), ParentID: &childParent, Status: domain.StatusInReview}
	standalone := domain.Task{ID: naming.IssueID("standalone"), Status: domain.StatusInReview, Dependencies: []domain.Dependency{{ID: naming.IssueID("root"), Type: domain.DependencyCreatedIn}}}
	tasks := []domain.Task{root, child, standalone}

	projectScoped := orchestrationReviewScopeTasks(tasks, domain.ProjectOrchestrationScope(), []string{"STANDALONE"})
	if got := taskIDsFromTasks(projectScoped); !slices.Equal(got, []string{"standalone"}) {
		t.Fatalf("project requested-ticket scope = %v, want standalone authoritative review", got)
	}
	if got := reviewWorktreeRefreshIssueIDs(projectScoped, tasks); !slices.Equal(got, []string{"standalone"}) {
		t.Fatalf("standalone worktree refresh scope = %v, want unrelated worktrees excluded", got)
	}
	rootedScope, err := domain.RootedOrchestrationScope("root")
	if err != nil {
		t.Fatal(err)
	}
	rootedChild := orchestrationReviewScopeTasks(tasks, rootedScope, []string{"child"})
	if got := taskIDsFromTasks(rootedChild); !slices.Equal(got, []string{"child"}) {
		t.Fatalf("rooted child scope = %v, want direct child", got)
	}
	if got := reviewWorktreeRefreshIssueIDs(rootedChild, tasks); !slices.Equal(got, []string{"child", "root"}) {
		t.Fatalf("rooted child worktree refresh scope = %v, want child plus authoritative ancestor only", got)
	}
	for _, requested := range []string{"root", "standalone"} {
		if got := orchestrationReviewScopeTasks(tasks, rootedScope, []string{requested}); len(got) != 0 {
			t.Fatalf("rooted requested-ticket scope for %s = %v, want self/provenance-only rejection", requested, taskIDsFromTasks(got))
		}
	}
}

func TestRequestedTicketReviewSnapshotSkipsUnrelatedProjectEvaluation(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	requested := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	unrelatedReview := createReviewTask(t, ctx, client, domain.P2, "other-worker")
	unrelatedOpen, err := client.Create(ctx, issues.CreateTaskParams{Title: "unrelated open root", Description: "Executable", Acceptance: "done", Type: domain.TypeTask, Priority: domain.P0, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	var preparedIDs []string
	d.orchestrationSnapshotPrepared = func(_ uint64, issueIDs []string) {
		preparedIDs = append([]string(nil), issueIDs...)
	}

	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{
		Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir, ReviewIssueIDs: []string{strings.ToUpper(requested)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(preparedIDs, []string{requested}) {
		t.Fatalf("review preparation IDs = %v, want only requested ticket %s", preparedIDs, requested)
	}
	if len(snapshot.ReviewQueue) != 1 || snapshot.ReviewQueue[0].IssueID != requested {
		t.Fatalf("review queue = %+v, want only requested ticket; unrelated review=%s", snapshot.ReviewQueue, unrelatedReview)
	}
	if len(snapshot.Candidates) != 0 || len(snapshot.Roots) != 0 || len(snapshot.Runnable) != 0 {
		t.Fatalf("review-only snapshot evaluated unrelated project candidates: candidates=%+v roots=%v runnable=%v unrelated=%s", snapshot.Candidates, snapshot.Roots, snapshot.Runnable, unrelatedOpen)
	}
}

func TestRequestedTicketReviewSnapshotConvergesDuringUnrelatedProjectionChurn(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	reader := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	writer := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	requested := createReviewTask(t, ctx, reader, domain.P1, "orchestrator")
	d := newOrchestrationReviewTestDaemon(filepath.Dir(dbPath), reader)
	exports := 0
	var churnErr error
	d.orchestrationProjectionExported = func() {
		exports++
		_, churnErr = writer.Create(ctx, issues.CreateTaskParams{
			Title:       fmt.Sprintf("Unrelated churn %d", exports),
			Description: "Advance the project projection after the requested review export",
			Acceptance:  "Recorded",
			Type:        domain.TypeTask,
			Priority:    domain.P4,
			Status:      domain.StatusOpen,
		})
	}

	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{
		Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", ReviewIssueIDs: []string{requested},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exports != 1 {
		t.Fatalf("requested review projection exports = %d, want one coherent export", exports)
	}
	if churnErr != nil {
		t.Fatalf("advance unrelated projection: %v", churnErr)
	}
	if len(snapshot.ReviewQueue) != 1 || snapshot.ReviewQueue[0].IssueID != requested {
		t.Fatalf("review queue = %+v, want only requested ticket %s", snapshot.ReviewQueue, requested)
	}
}

func TestRequestedReviewCandidateMutationAfterExportFailsBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t),
	}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	var mutationErr error
	d.orchestrationProjectionExported = func() {
		d.orchestrationProjectionExported = nil
		mutationErr = client.Update(ctx, issueID, domain.StatusInProgress)
	}

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept,
		IntentKey: "candidate-mutated-after-export", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutationErr != nil {
		t.Fatalf("advance requested candidate after export: %v", mutationErr)
	}
	if slices.Contains(result.Closed, issueID) {
		t.Fatalf("mutated candidate was accepted: %+v", result)
	}
	assertNoReviewAcceptanceSideEffects(t, ctx, client, issueID)
}

func TestRequestedReviewAcceptEpochReplacementFailsBeforeSideEffects(t *testing.T) {
	for _, rooted := range []bool{false, true} {
		for _, kind := range []protocol.OrchestrationIntentKind{protocol.OrchestrationIntentReviewAccept} {
			name := "project/" + string(kind)
			if rooted {
				name = "rooted-child/" + string(kind)
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				repoDir := t.TempDir()
				client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
				t.Cleanup(func() { _ = client.CloseDB() })
				issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
				if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
					Type: domain.IssueEventEvidenceSubmitted, Source: "first-worker", Payload: mustWorkerEvidencePayload(t),
				}); err != nil {
					t.Fatal(err)
				}
				scope := domain.ProjectOrchestrationScope()
				mailParent := issueID
				if rooted {
					rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
					if err != nil {
						t.Fatal(err)
					}
					if err := client.AddDependencyWithParentChange(ctx, issueID, rootID, string(domain.DependencyParentChild), false); err != nil {
						t.Fatal(err)
					}
					scope, err = domain.RootedOrchestrationScope(rootID)
					if err != nil {
						t.Fatal(err)
					}
					mailParent = rootID
				}
				oldEpoch := latestReviewEpochEventID(t, ctx, client, issueID)
				sourceOID := "first-source"
				d := newOrchestrationReviewTestDaemon(repoDir, client)
				d.reviewAcceptedSourceOID = func(context.Context, string, string) (string, error) { return sourceOID, nil }
				var replacementErr error
				d.reviewAdmissionSnapshotLoaded = func() {
					d.reviewAdmissionSnapshotLoaded = nil
					if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
						replacementErr = err
						return
					}
					replacement := mustWorkerEvidencePayload(t)
					replacement["summary"] = "replacement epoch evidence"
					if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "replacement-worker", Payload: replacement}); err != nil {
						replacementErr = err
						return
					}
					if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
						replacementErr = err
						return
					}
					sourceOID = "replacement-source"
				}
				request := protocol.OrchestrationIntentRequest{
					Scope: scope, Kind: kind, IntentKey: "epoch-replacement-" + string(kind), ActorID: "orchestrator",
					IssueIDs: []string{issueID}, RepoDir: repoDir, RestartWorker: true,
				}
				if kind == protocol.OrchestrationIntentReviewReturn {
					request.Findings = []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "stale return must not escape"}}
				}
				result, err := d.orchestrationAuthority().Apply(ctx, "project", request)
				if err != nil {
					t.Fatal(err)
				}
				if replacementErr != nil {
					t.Fatalf("replace exported review epoch: %v", replacementErr)
				}
				if newEpoch := latestReviewEpochEventID(t, ctx, client, issueID); newEpoch == oldEpoch {
					t.Fatalf("review epoch remained %d, want replacement", oldEpoch)
				}
				if slices.Contains(result.Closed, issueID) || slices.Contains(result.Returned, issueID) || len(result.Launched) != 0 || len(result.Pending) != 0 {
					t.Fatalf("stale review escaped admission fence: %+v", result)
				}
				if got := result.Skipped[issueID]; !strings.Contains(got, "review epoch changed") && !strings.Contains(got, "epoch or evidence identity changed") {
					t.Fatalf("result = %+v, want exact epoch/evidence mismatch", result)
				}
				assertNoReviewAcceptanceSideEffects(t, ctx, client, issueID)
				mail, err := readMailboxEvents(repoDir, mailParent)
				if err != nil {
					t.Fatal(err)
				}
				if len(mail) != 0 {
					t.Fatalf("stale review manufactured findings mail: %+v", mail)
				}
			})
		}
	}
}

func TestRequestedReviewAcceptExactCandidateReplacementFailsBeforeSideEffects(t *testing.T) {
	for _, kind := range []protocol.OrchestrationIntentKind{protocol.OrchestrationIntentReviewAccept} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			repoDir := t.TempDir()
			client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
			if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t)}); err != nil {
				t.Fatal(err)
			}
			sourceOID := "exported-source"
			d := newOrchestrationReviewTestDaemon(repoDir, client)
			d.reviewAcceptedSourceOID = func(context.Context, string, string) (string, error) { return sourceOID, nil }
			d.reviewAdmissionSnapshotLoaded = func() {
				d.reviewAdmissionSnapshotLoaded = nil
				sourceOID = "replacement-source"
			}
			request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: kind, IntentKey: "candidate-replacement-" + string(kind), ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir, RestartWorker: true}
			if kind == protocol.OrchestrationIntentReviewReturn {
				request.Findings = []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "stale candidate return"}}
			}
			result, err := d.orchestrationAuthority().Apply(ctx, "project", request)
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Skipped[issueID]; !strings.Contains(got, "review candidate changed") {
				t.Fatalf("result = %+v, want exact candidate mismatch", result)
			}
			assertNoReviewAcceptanceSideEffects(t, ctx, client, issueID)
			mail, err := readMailboxEvents(repoDir, issueID)
			if err != nil {
				t.Fatal(err)
			}
			if len(mail) != 0 || len(result.Launched) != 0 || len(result.Pending) != 0 {
				t.Fatalf("stale exact candidate manufactured side effects: result=%+v mail=%+v", result, mail)
			}
		})
	}
}

func TestActiveReviewAdmissionLeaseFreezesEpochEvidenceAndParentIdentity(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootA, err := client.Create(ctx, issues.CreateTaskParams{Title: "root a", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	rootB, err := client.Create(ctx, issues.CreateTaskParams{Title: "root b", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	if err := client.AddDependencyWithParentChange(ctx, issueID, rootA, string(domain.DependencyParentChild), false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	admission, err := client.CaptureReviewAdmissionPin(ctx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview, ExpectedReviewAdmission: &admission, ExpectedParentIssueID: rootA, ReviewSourceOID: "source"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInProgress); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("review epoch mutation error = %v, want conflict", err)
	}
	replacement := mustWorkerEvidencePayload(t)
	replacement["summary"] = "replacement"
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: replacement}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("review evidence mutation error = %v, want conflict", err)
	}
	if err := client.AddDependencyWithParentChange(ctx, issueID, rootB, string(domain.DependencyParentChild), true); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("review parent mutation error = %v, want conflict", err)
	}
	current, err := client.CaptureReviewAdmissionPin(ctx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ReviewEpochEventID != admission.ReviewEpochEventID || current.Evidence == nil || admission.Evidence == nil || *current.Evidence != *admission.Evidence {
		t.Fatalf("review admission identity changed under lease: before=%+v after=%+v", admission, current)
	}
}

func TestRootedRequestedReviewRevalidatesDirectParentAfterExport(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	firstRoot, err := client.Create(ctx, issues.CreateTaskParams{Title: "first root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	secondRoot, err := client.Create(ctx, issues.CreateTaskParams{Title: "second root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueID, err := client.Create(ctx, issues.CreateTaskParams{
		Title: "rooted internal review", Description: "read-only findings", Acceptance: "consumed",
		Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview, ParentID: &firstRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "agent", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "consumer": firstRoot}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
			t.Fatal(err)
		}
	}
	scope, err := domain.RootedOrchestrationScope(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	var reparentErr error
	d.orchestrationProjectionExported = func() {
		d.orchestrationProjectionExported = nil
		reparentErr = client.AddDependencyWithParentChange(ctx, issueID, secondRoot, string(domain.DependencyParentChild), true)
	}
	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{
		Scope: scope, Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "rooted-reparent-after-export",
		ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reparentErr != nil {
		t.Fatalf("reparent requested review after export: %v", reparentErr)
	}
	if !strings.Contains(result.Skipped[issueID], "outside-root-direct-child-scope") {
		t.Fatalf("reparented candidate result = %+v, want exact rooted-scope rejection", result)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if lease := coordinationLease(task, domain.CoordinationLeaseReview); lease != nil {
		t.Fatalf("reparented candidate manufactured review lease: %+v", lease)
	}
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Source == "daemon-orchestration" {
			t.Fatalf("reparented candidate manufactured daemon review outcome: %+v", event)
		}
	}
}

func assertNoReviewAcceptanceSideEffects(t *testing.T, ctx context.Context, client *issues.Client, issueID string) {
	t.Helper()
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if lease := coordinationLease(task, domain.CoordinationLeaseReview); lease != nil {
		t.Fatalf("pre-admission failure manufactured review lease: %+v", lease)
	}
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted, domain.IssueEventReviewCloseFailed, domain.IssueEventTaskIntegrationCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("pre-admission failure manufactured review evidence: %+v", events)
	}
}

func TestExactReviewCandidateWorktreeBindsProjectionToLiveIdentity(t *testing.T) {
	ctx := context.Background()
	projectID, issueID := "project", "dlc"
	storePath := filepath.Join(t.TempDir(), "runtime.db")
	store := daemonstate.NewRuntimeStateStoreAtPath(storePath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	writer := daemonstate.NewRuntimeStateStoreAtPath(storePath, slog.Default())
	t.Cleanup(func() { _ = writer.Close() })
	projectedPath := t.TempDir()
	if err := store.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: projectID, IssueID: issueID, Path: projectedPath, Branch: "riordan/dlc/review-candidate", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	runner := &staticWorktreeListRunner{output: fmt.Sprintf("worktree %s\nHEAD deadbeef\nbranch refs/heads/riordan/dlc/review-candidate\n\n", projectedPath)}
	d := &Daemon{worktreeAdapter: &worktreeServiceAdapter{manager: git.NewWorktreeManager(runner, t.TempDir(), slog.Default()), runtimeStateStore: store}}

	path, err := d.exactReviewCandidateWorktree(ctx, projectID, issueID)
	if err != nil || path != filepath.Clean(projectedPath) {
		t.Fatalf("exact candidate path=%q err=%v", path, err)
	}

	// A second daemon's durable projection update must be observed before the
	// live comparison; stale in-memory worktree identity is not authoritative.
	if err := writer.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: projectID, IssueID: issueID, Path: t.TempDir(), Branch: "riordan/dlc/review-candidate", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.exactReviewCandidateWorktree(ctx, projectID, issueID); err == nil || !strings.Contains(err.Error(), "candidate_path_mismatch") {
		t.Fatalf("cross-daemon projection refresh error=%v, want typed candidate_path_mismatch diagnostic", err)
	}
	if err := writer.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: projectID, IssueID: issueID, Path: projectedPath, Branch: "riordan/dlc/review-candidate", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	runner.output = fmt.Sprintf("worktree %s\nHEAD deadbeef\nbranch refs/heads/riordan/other/reused\n\n", projectedPath)
	if _, err := d.exactReviewCandidateWorktree(ctx, projectID, issueID); err == nil || !strings.Contains(err.Error(), "candidate_path_reused") {
		t.Fatalf("reused path error=%v, want typed candidate_path_reused diagnostic", err)
	}

	runner.output = ""
	if _, err := d.exactReviewCandidateWorktree(ctx, projectID, issueID); err == nil || !strings.Contains(err.Error(), "candidate_projection_stale") {
		t.Fatalf("stale projection error=%v, want typed candidate_projection_stale diagnostic", err)
	}
}

func TestReviewAcceptRejectsCandidateMutationAfterInspection(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, string)
		want   string
	}{
		{name: "head", mutate: func(t *testing.T, repo, candidate string) {
			requireNoError(t, os.WriteFile(filepath.Join(candidate, "late.go"), []byte("package consumer\n"), 0o644))
			runDaemonTestGit(t, candidate, "add", "late.go")
			runDaemonTestGit(t, candidate, "commit", "-q", "-m", "late candidate")
		}, want: "reviewed candidate revision changed"},
		{name: "dirty", mutate: func(t *testing.T, _, candidate string) {
			requireNoError(t, os.WriteFile(filepath.Join(candidate, "dirty.txt"), []byte("unreviewed\n"), 0o644))
		}, want: "reviewed candidate worktree is dirty"},
		{name: "path-reused", mutate: func(t *testing.T, repo, candidate string) {
			runDaemonTestGit(t, repo, "worktree", "remove", "--force", candidate)
			runDaemonTestGit(t, repo, "worktree", "add", "-q", "-b", "riordan/foreign/reused", candidate, "main")
		}, want: "candidate_path_reused"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repo := t.TempDir()
			runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
			runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
			runDaemonTestGit(t, repo, "config", "user.name", "Test User")
			requireNoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644))
			runDaemonTestGit(t, repo, "add", "base.txt")
			runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
			client := newMigratedIssueClientAtPath(t, filepath.Join(repo, "issues.db"), slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			issueID := createReviewTask(t, ctx, client, domain.P1, "worker")
			_, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t)})
			requireNoError(t, err)
			candidate := filepath.Join(t.TempDir(), "candidate")
			branch := "riordan/" + issueID + "/review-candidate"
			runDaemonTestGit(t, repo, "worktree", "add", "-q", "-b", branch, candidate, "main")
			candidate, err = filepath.EvalSymlinks(candidate)
			requireNoError(t, err)
			requireNoError(t, os.WriteFile(filepath.Join(candidate, "patch.txt"), []byte("reviewed\n"), 0o644))
			runDaemonTestGit(t, candidate, "add", "patch.txt")
			runDaemonTestGit(t, candidate, "commit", "-q", "-m", "reviewed patch")
			state := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
			t.Cleanup(func() { _ = state.Close() })
			requireNoError(t, state.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: "project", IssueID: issueID, Path: candidate, Branch: branch, UpdatedAt: time.Now().UTC()}))
			d := newOrchestrationReviewTestDaemon(repo, client)
			d.reviewCandidateCheck = nil
			d.git = git.NewClient(git.NewExecRunner(repo), slog.Default())
			d.worktreeAdapter = &worktreeServiceAdapter{manager: git.NewWorktreeManager(git.NewExecRunner(repo), repo, slog.Default()), runtimeStateStore: state}
			task, err := client.GetWithRuntime(ctx, "project", issueID)
			requireNoError(t, err)
			authority := daemonOrchestrationAuthority{daemon: d}
			inspection := authority.reviewInspection(ctx, "project", repo, "reviewer", task, map[string]domain.Task{issueID: task}, map[string]git.Worktree{issueID: {IssueID: issueID, Path: candidate, Branch: branch}})
			if inspection.HeadRevision == "" || inspection.Evidence == nil {
				t.Fatalf("inspection = %+v, want immutable candidate and evidence", inspection)
			}
			d.reviewAcceptanceBeforeCandidateCheck = func() { test.mutate(t, repo, candidate) }
			_, acceptErr := authority.acceptReview(ctx, "project", protocol.OrchestrationIntentRequest{Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-mutated-" + test.name, ActorID: "reviewer", RepoDir: repo}, inspection, &protocol.OrchestrationIntentResult{})
			if acceptErr == nil || !strings.Contains(acceptErr.Error(), test.want) {
				t.Fatalf("accept error = %v, want %q", acceptErr, test.want)
			}
			assertNoReviewAcceptanceSideEffects(t, ctx, client, issueID)
		})
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectReviewQueueRefreshesCrossProcessReviewLease(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	path := filepath.Join(repoDir, "issues.db")
	reader := newMigratedIssueClientAtPath(t, path, slog.Default())
	writer := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB(); _ = writer.CloseDB() })
	issueID := createReviewTask(t, ctx, reader, domain.P1, "orchestrator")
	d := newOrchestrationReviewTestDaemon(repoDir, reader)
	authority := d.orchestrationAuthority()
	if err := d.ensureLegacyMailboxObservationProjection(ctx, "project", repoDir); err != nil {
		t.Fatal(err)
	}
	cutover, err := reader.MailboxObservationProjectionCutoverState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cutover.State != "complete" {
		t.Fatalf("legacy mailbox cutover state = %q, want complete before snapshot admission", cutover.State)
	}

	before, err := authority.Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.ReviewQueue) != 1 || !before.ReviewQueue[0].Actionable {
		t.Fatalf("before = %+v, want actionable review", before.ReviewQueue)
	}
	if _, err := writer.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "foreign-reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview}); err != nil {
		t.Fatal(err)
	}
	after, err := authority.Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.ReviewQueue) != 1 || after.ReviewQueue[0].Actionable || after.ReviewQueue[0].ReviewOwner != "foreign-reviewer" {
		t.Fatalf("after = %+v, want refreshed foreign review lease", after.ReviewQueue)
	}
}

func TestReviewReturnPreservesWorkerOwnerAndDurablyDeliversFindings(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	tmuxRunner := newSessionStartTmuxRunner()
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.tmux = tmux.NewClient(tmuxRunner, slog.Default())
	canonicalSessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope("project"), naming.IssueID(issueID)).String()
	tmuxRunner.sessions[canonicalSessionID] = true
	seedReadyAgentInput(t, d, tmuxRunner, "project", canonicalSessionID)

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{
		Scope:         domain.ProjectOrchestrationScope(),
		Kind:          protocol.OrchestrationIntentReviewReturn,
		IntentKey:     "review-return-1",
		ActorID:       "orchestrator",
		IssueIDs:      []string{issueID},
		RepoDir:       repoDir,
		RestartWorker: true,
		Findings: []protocol.OrchestrationReviewFinding{{
			Severity:     "high",
			File:         "internal/daemon/orchestration.go",
			Line:         42,
			Finding:      "review result must stay durable",
			SuggestedFix: "record before delivery",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Returned) != 1 || result.Returned[0] != issueID || len(result.Launched) != 0 || len(result.Pending) != 0 || len(result.Failed) != 0 {
		t.Fatalf("return result = %+v", result)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Ownership == nil || task.Ownership.OwnerID != "worker-a" {
		t.Fatalf("execution ownership = %+v, want preserved", task.Ownership)
	}
	if task.Status != domain.StatusInProgress {
		t.Fatalf("status = %q, want %q after review handback", task.Status, domain.StatusInProgress)
	}
	if lease := coordinationLease(task, domain.CoordinationLeaseReview); lease != nil {
		t.Fatalf("review lease = %+v, want released after handback", lease)
	}
	events, err := readMailboxEvents(repoDir, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "review-finding" || !strings.Contains(events[0].Body, "record before delivery") {
		t.Fatalf("mail events = %+v, want durable review finding", events)
	}
	if len(tmuxRunner.inputPayloads) != 1 || !strings.Contains(tmuxRunner.inputPayloads[0], "review result must stay durable") {
		t.Fatalf("delivered prompts = %+v", tmuxRunner.inputPayloads)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 1 || reviewEvents[0].Payload["outcome"] != "returned" {
		t.Fatalf("review events = %+v", reviewEvents)
	}
	replayed, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{
		Scope:         domain.ProjectOrchestrationScope(),
		Kind:          protocol.OrchestrationIntentReviewReturn,
		IntentKey:     "review-return-1",
		ActorID:       "orchestrator",
		IssueIDs:      []string{issueID},
		RepoDir:       repoDir,
		RestartWorker: true,
		Findings: []protocol.OrchestrationReviewFinding{{
			Severity:     "high",
			File:         "internal/daemon/orchestration.go",
			Line:         42,
			Finding:      "review result must stay durable",
			SuggestedFix: "record before delivery",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Returned) != 1 || len(replayed.Failed) != 0 {
		t.Fatalf("replayed result = %+v", replayed)
	}
	replayedMail, err := readMailboxEvents(repoDir, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayedMail) != 1 || len(tmuxRunner.inputPayloads) != 1 {
		t.Fatalf("replayed side effects mail=%d prompts=%d, want one each", len(replayedMail), len(tmuxRunner.inputPayloads))
	}
	conflict, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn, IntentKey: "review-return-1", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "different request"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conflict.Failed[issueID], "different review request") {
		t.Fatalf("conflicting replay = %+v, want explicit idempotency conflict", conflict)
	}
}

func TestReviewReturnDoesNotRequireProjectSnapshotAdmission(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	tmuxRunner := newSessionStartTmuxRunner()
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.tmux = tmux.NewClient(tmuxRunner, slog.Default())
	d.orchestrationSnapshotBuild = func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
		return protocol.OrchestrationSnapshot{}, fmt.Errorf("%w: injected unrelated project churn", errOrchestrationSnapshotAdmissionContended)
	}
	canonicalSessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope("project"), naming.IssueID(issueID)).String()
	tmuxRunner.sessions[canonicalSessionID] = true
	seedReadyAgentInput(t, d, tmuxRunner, "project", canonicalSessionID)
	request := protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn,
		IntentKey: "review-return-with-project-churn", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
		Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "repair the bounded worker issue"}},
	}
	result, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Returned) != 1 || result.Returned[0] != issueID || len(result.Failed) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("review return result = %+v, want bounded return despite unrelated project snapshot contention", result)
	}
	replayed, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Returned) != 1 || len(replayed.Failed) != 0 || len(replayed.Skipped) != 0 {
		t.Fatalf("review return replay = %+v, want idempotent convergence without project snapshot admission", replayed)
	}
}

func TestReviewReturnAcceptsFailedAggregateGateFromCurrentReviewEpochAfterWorkerMovesActive(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	reviewEpochEventID := latestReviewEpochEventID(t, ctx, client, issueID)
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	now := time.Now().UTC()
	_, err := runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "review-gate", LeaseToken: "secret", ProjectID: "project", IssueID: issueID, Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "candidate-a", ReviewerID: "orchestrator", ReviewEpochEventID: reviewEpochEventID, TTL: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.store.FinishValidation(ctx, "review-gate", "secret", domain.ValidationRequestFailed, "exit 1", domain.ValidationEvidence{Held: true, RequestID: "review-gate", Class: domain.ValidationClassAggregate, Profile: "cold", SourceRevision: "candidate-a", Present: true}, now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tmuxRunner := newSessionStartTmuxRunner()
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	attachIsolatedRuntimeStore(t, d, "project")
	d.operationRuntime = runtime
	d.git = git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) { return "candidate-a\n", nil }}, slog.Default())
	d.tmux = tmux.NewClient(tmuxRunner, slog.Default())
	sessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope("project"), naming.IssueID(issueID)).String()
	tmuxRunner.sessions[sessionID] = true
	seedReadyAgentInput(t, d, tmuxRunner, "project", sessionID)
	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn, IntentKey: "failed-review-gate", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "gate found a regression"}}}

	result, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Returned) != 1 || result.Returned[0] != issueID || len(result.Failed) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("result = %+v, want formal returned outcome", result)
	}
	replayed, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil || len(replayed.Returned) != 1 || len(tmuxRunner.inputPayloads) != 1 {
		t.Fatalf("replay = %+v err=%v prompts=%d, want idempotent return", replayed, err, len(tmuxRunner.inputPayloads))
	}
}

func TestReviewReturnRejectsCompletedExitZeroAggregateGateDuringActiveValidation(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	reviewEpochEventID := latestReviewEpochEventID(t, ctx, client, issueID)
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	now := time.Now().UTC()
	_, err := runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "successful-review-gate", LeaseToken: "secret", ProjectID: "project", IssueID: issueID, Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "candidate-a", ReviewerID: "orchestrator", ReviewEpochEventID: reviewEpochEventID, TTL: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.store.FinishValidation(ctx, "successful-review-gate", "secret", domain.ValidationRequestCompleted, "exit 0", domain.ValidationEvidence{Held: true, RequestID: "successful-review-gate", Class: domain.ValidationClassAggregate, Profile: "cold", SourceRevision: "candidate-a", Present: true}, now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.operationRuntime = runtime
	d.git = git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) { return "candidate-a\n", nil }}, slog.Default())
	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn, IntentKey: "successful-gate-return", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "must not return after successful validation"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped[issueID] != "not-review-ready" || len(result.Returned) != 0 {
		t.Fatalf("result = %+v, want completed exit 0 gate rejected", result)
	}
}

func TestReviewReturnRejectsActiveValidationAssignmentForWrongActorOrRevision(t *testing.T) {
	for _, tc := range []struct {
		name         string
		gateReviewer string
		gateRevision string
		headRevision string
		actor        string
	}{
		{name: "wrong actor", gateReviewer: "assigned-reviewer", gateRevision: "candidate-a", headRevision: "candidate-a", actor: "other-reviewer"},
		{name: "wrong revision", gateReviewer: "assigned-reviewer", gateRevision: "candidate-a", headRevision: "candidate-b", actor: "assigned-reviewer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repoDir := t.TempDir()
			client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
			t.Cleanup(func() { _ = client.CloseDB() })
			issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
			epochID := latestReviewEpochEventID(t, ctx, client, issueID)
			runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
			t.Cleanup(func() { _ = runtime.Close() })
			now := time.Now().UTC()
			_, err := runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "review-gate", LeaseToken: "secret", ProjectID: "project", IssueID: issueID, Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: tc.gateRevision, ReviewerID: tc.gateReviewer, ReviewEpochEventID: epochID, TTL: time.Minute}, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
				t.Fatal(err)
			}
			_, err = runtime.store.FinishValidation(ctx, "review-gate", "secret", domain.ValidationRequestFailed, "failed", domain.ValidationEvidence{Held: true, RequestID: "review-gate", Class: domain.ValidationClassAggregate, Profile: "cold", SourceRevision: tc.gateRevision, Present: true}, now.Add(time.Second), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			d := newOrchestrationReviewTestDaemon(repoDir, client)
			d.operationRuntime = runtime
			d.git = git.NewClient(&recordingGitRunner{runFn: func(args ...string) (string, error) { return tc.headRevision + "\n", nil }}, slog.Default())
			result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn, IntentKey: "rejected-return", ActorID: tc.actor, IssueIDs: []string{issueID}, RepoDir: repoDir, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "must be rejected"}}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Skipped[issueID] != "not-review-ready" || len(result.Returned) != 0 {
				t.Fatalf("result = %+v, want assignment mismatch rejected", result)
			}
		})
	}
}

func TestReviewReturnBoundsBlockedLiveDeliveryAndPublishesFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	runner := newSessionStartTmuxRunner()
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	attachIsolatedRuntimeStore(t, d, "project")
	d.tmux = tmux.NewClient(runner, slog.Default())
	sessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope("project"), naming.IssueID(issueID)).String()
	runner.sessions[sessionID] = true
	seedReadyAgentInput(t, d, runner, "project", sessionID)
	deliver := func(runCtx context.Context, _ authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
		<-runCtx.Done()
		return authoritativeAgentInputAcknowledgement{}, runCtx.Err()
	}
	d.agentInput = newAgentInputDeliveryService(
		d.sessionRuntimeStateStoreIfConfigured,
		d.issueClientForProject,
		scriptedAuthoritativeReceiver{deliver: func(ctx context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
			return deliver(ctx, request)
		}},
		"review-test",
	)
	var gotDeliveryTimeout time.Duration
	authority := daemonOrchestrationAuthority{
		daemon:                d,
		reviewDeliveryTimeout: 25 * time.Millisecond,
		reviewDeliveryContext: func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
			gotDeliveryTimeout = timeout
			done := make(chan struct{})
			close(done)
			return deadlineExceededTestContext{Context: parent, done: done}, func() {}
		},
	}
	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn, IntentKey: "review-return-blocked-delivery", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "delivery must be bounded"}}}

	result, err := authority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if gotDeliveryTimeout != authority.reviewDeliveryTimeout {
		t.Fatalf("review delivery timeout = %v, want %v", gotDeliveryTimeout, authority.reviewDeliveryTimeout)
	}
	failure := result.Failed[issueID]
	if !strings.Contains(failure, "stage=live_delivery") || !strings.Contains(failure, "target="+issueID) || !strings.Contains(failure, context.DeadlineExceeded.Error()) {
		t.Fatalf("result = %+v, want stage, target, and timeout failure", result)
	}
	mail, err := readMailboxEvents(repoDir, issueID)
	if err != nil || len(mail) != 1 {
		t.Fatalf("mail events = %+v err=%v, want durable findings before delivery", mail, err)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 1 || reviewEvents[0].Payload["outcome"] != "delivery_failed" || !strings.Contains(fmt.Sprint(reviewEvents[0].Payload["failure"]), "stage=live_delivery target="+issueID) {
		t.Fatalf("review events = %+v, want durable stage-aware delivery failure", reviewEvents)
	}

	deliver = func(context.Context, authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
		return authoritativeAgentInputAcknowledgement{}, errors.New("delivery path unavailable")
	}
	authority.reviewDeliveryContext = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}
	replayed, err := authority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if replayFailure := replayed.Failed[issueID]; !strings.Contains(replayFailure, "stage=live_delivery") || !strings.Contains(replayFailure, "target="+issueID) || !strings.Contains(replayFailure, "delivery path unavailable") {
		t.Fatalf("replayed result = %+v, want stage-aware unavailable-path failure", replayed)
	}
	replayedMail, err := readMailboxEvents(repoDir, issueID)
	if err != nil || len(replayedMail) != 1 {
		t.Fatalf("replayed mail events = %+v err=%v, want idempotent durable finding", replayedMail, err)
	}
}

type deadlineExceededTestContext struct {
	context.Context
	done <-chan struct{}
}

func (c deadlineExceededTestContext) Deadline() (time.Time, bool) { return time.Time{}, true }
func (c deadlineExceededTestContext) Done() <-chan struct{}       { return c.done }
func (deadlineExceededTestContext) Err() error                    { return context.DeadlineExceeded }

func TestReviewReturnRejectsFailedAggregateGateFromPriorReviewEpoch(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	now := time.Now().UTC()
	_, err := runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "stale-review-gate", LeaseToken: "secret", ProjectID: "project", IssueID: issueID, Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "candidate-a", TTL: time.Minute}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.store.FinishValidation(ctx, "stale-review-gate", "secret", domain.ValidationRequestFailed, "failed", domain.ValidationEvidence{}, now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.operationRuntime = runtime
	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn, IntentKey: "stale-gate-return", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "stale finding"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped[issueID] != "not-review-ready" || len(result.Returned) != 0 {
		t.Fatalf("result = %+v, want stale epoch rejected", result)
	}
}

func TestReviewReturnReplayConvergesAfterReviewLeaseReleaseFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	tmuxRunner := newSessionStartTmuxRunner()
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.tmux = tmux.NewClient(tmuxRunner, slog.Default())
	canonicalSessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope("project"), naming.IssueID(issueID)).String()
	tmuxRunner.sessions[canonicalSessionID] = true
	seedReadyAgentInput(t, d, tmuxRunner, "project", canonicalSessionID)
	authority := daemonOrchestrationAuthority{daemon: d}
	releaseCalls := 0
	authority.releaseReviewLease = func(ctx context.Context, projectID, issueID, actorID string) error {
		releaseCalls++
		if releaseCalls == 1 {
			return errors.New("injected review lease release failure")
		}
		_, err := client.ReleaseOwnershipWithRuntime(ctx, projectID, issueID, issues.OwnershipClaimParams{OwnerID: actorID, Purpose: domain.CoordinationLeaseReview})
		return err
	}
	request := protocol.OrchestrationIntentRequest{
		Scope:     domain.ProjectOrchestrationScope(),
		Kind:      protocol.OrchestrationIntentReviewReturn,
		IntentKey: "review-return-release-retry",
		ActorID:   "orchestrator",
		IssueIDs:  []string{issueID},
		RepoDir:   repoDir,
		Findings:  []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "clean up the review lease on replay"}},
	}

	first, err := authority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Failed[issueID], "injected review lease release failure") || len(first.Returned) != 0 {
		t.Fatalf("first result = %+v, want release failure after durable return", first)
	}
	firstTask, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if firstTask.Status != domain.StatusInReview {
		t.Fatalf("first status = %q, want review state retained after release failure", firstTask.Status)
	}
	if lease := coordinationLease(firstTask, domain.CoordinationLeaseReview); lease == nil || lease.OwnerID != "orchestrator" {
		t.Fatalf("first review lease = %+v, want failed-release lease retained", lease)
	}

	replayed, err := authority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Returned) != 1 || replayed.Returned[0] != issueID || len(replayed.Failed) != 0 {
		t.Fatalf("replayed result = %+v, want converged returned", replayed)
	}
	finalTask, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if finalTask.Status != domain.StatusInProgress || coordinationLease(finalTask, domain.CoordinationLeaseReview) != nil {
		t.Fatalf("final task = %+v, want in_progress without review lease", finalTask)
	}
	mail, err := readMailboxEvents(repoDir, issueID)
	if err != nil {
		t.Fatal(err)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if releaseCalls != 2 || len(mail) != 1 || len(tmuxRunner.inputPayloads) != 1 || len(reviewEvents) != 1 || reviewEvents[0].Payload["outcome"] != "returned" {
		t.Fatalf("replay side effects release_calls=%d mail=%d prompts=%d outcomes=%+v, want one handback and two release attempts", releaseCalls, len(mail), len(tmuxRunner.inputPayloads), reviewEvents)
	}
}

func TestReviewReturnRestartConvergesAfterRealStartTransitionAndDaemonReplay(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	tmuxRunner := newSessionStartTmuxRunner()
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.tmux = tmux.NewClient(tmuxRunner, slog.Default())
	authority := daemonOrchestrationAuthority{daemon: d}
	submitCalls := 0
	authority.submitStart = func(_ context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
		submitCalls++
		var submitted protocol.OperationSubmitRequestBody
		if err := json.Unmarshal(req.Body, &submitted); err != nil {
			t.Fatal(err)
		}
		var start sessionCommandBody
		if err := json.Unmarshal(submitted.Payload, &start); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(start.Prompt, "resume with durable finding") {
			t.Fatalf("start prompt = %q, want review finding", start.Prompt)
		}
		body, err := json.Marshal(protocol.OperationSubmitResponseBody{Operation: protocol.OperationRecord{
			OperationID: naming.OperationID("op-review-restart"),
			ProjectID:   naming.ProjectID("project"),
			Kind:        "session.start",
			IssueID:     naming.IssueID(issueID),
			State:       protocol.OperationStateQueued,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return protocol.ResponseEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: req.RequestID, OK: true, Body: body}
	}
	request := protocol.OrchestrationIntentRequest{
		Scope:         domain.ProjectOrchestrationScope(),
		Kind:          protocol.OrchestrationIntentReviewReturn,
		IntentKey:     "review-return-restart",
		ActorID:       "orchestrator",
		IssueIDs:      []string{issueID},
		RepoDir:       repoDir,
		RestartWorker: true,
		Findings:      []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "resume with durable finding"}},
	}

	first, err := authority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Pending) != 1 || first.Pending[0].OperationState != string(protocol.OperationStateQueued) || len(first.Failed) != 0 {
		t.Fatalf("first return = %+v, want queued restart", first)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 1 || reviewEvents[0].Payload["outcome"] != "restart_submitted" {
		t.Fatalf("review events = %+v, want durable queued restart outcome", reviewEvents)
	}
	if reviewEvents[0].OperationID != "op-review-restart" || reviewEvents[0].Payload["operation_state"] != string(protocol.OperationStateQueued) {
		t.Fatalf("restart relation = %+v, want typed operation id/state", reviewEvents[0])
	}
	if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		t.Fatalf("model real session.start status transition: %v", err)
	}
	restartedDaemon := newOrchestrationReviewTestDaemon(repoDir, client)
	restartedAuthority := daemonOrchestrationAuthority{daemon: restartedDaemon}
	operationState := protocol.OperationStateQueued
	restartedAuthority.lookupOperation = func(_ context.Context, operationID string) (protocol.OperationRecord, error) {
		if operationID != "op-review-restart" {
			return protocol.OperationRecord{}, fmt.Errorf("unexpected operation %s", operationID)
		}
		return protocol.OperationRecord{OperationID: naming.OperationID(operationID), ProjectID: "project", IssueID: naming.IssueID(issueID), Kind: "session.start", State: operationState}, nil
	}

	pendingReplay, err := restartedAuthority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingReplay.Pending) != 1 || pendingReplay.Pending[0].OperationState != string(protocol.OperationStateQueued) || len(pendingReplay.Failed) != 0 {
		t.Fatalf("pending replay = %+v, want queued outside ReviewQueue", pendingReplay)
	}
	operationState = protocol.OperationStateDone
	replayed, err := restartedAuthority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Returned) != 1 || replayed.Returned[0] != issueID || len(replayed.Pending) != 0 || len(replayed.Failed) != 0 {
		t.Fatalf("replayed return = %+v, want converged handback", replayed)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusInProgress || coordinationLease(task, domain.CoordinationLeaseReview) != nil {
		t.Fatalf("task = %+v, want in_progress without review lease", task)
	}
	events, err := readMailboxEvents(repoDir, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || submitCalls != 1 || len(tmuxRunner.inputPayloads) != 0 {
		t.Fatalf("side effects mail=%d submits=%d direct_prompts=%d, want one durable finding and one start prompt", len(events), submitCalls, len(tmuxRunner.inputPayloads))
	}
	reviewEvents, err = client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 2 || reviewEvents[0].Payload["outcome"] != "restart_submitted" || reviewEvents[1].Payload["outcome"] != "returned" {
		t.Fatalf("review events = %+v, want returned after restart_submitted", reviewEvents)
	}
	replayedAgain, err := restartedAuthority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayedAgain.Returned) != 1 || len(replayedAgain.Failed) != 0 || submitCalls != 1 {
		t.Fatalf("second replay = %+v submits=%d, want idempotent returned", replayedAgain, submitCalls)
	}
	reviewEvents, err = client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 2 {
		t.Fatalf("second replay duplicated outcomes: %+v", reviewEvents)
	}
}

func TestReviewReturnRestartReplaySurfacesDelayedTerminalFailureOutsideReviewQueue(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.tmux = tmux.NewClient(newSessionStartTmuxRunner(), slog.Default())
	authority := daemonOrchestrationAuthority{daemon: d}
	authority.submitStart = func(_ context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
		body, err := json.Marshal(protocol.OperationSubmitResponseBody{Operation: protocol.OperationRecord{
			OperationID: naming.OperationID("op-review-delayed-failure"), ProjectID: "project", Kind: "session.start",
			IssueID: naming.IssueID(issueID), State: protocol.OperationStateQueued,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return protocol.ResponseEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: req.RequestID, OK: true, Body: body}
	}
	request := protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn,
		IntentKey: "review-return-delayed-failure", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
		RestartWorker: true, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "surface delayed failure"}},
	}
	first, err := authority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Pending) != 1 || len(first.Failed) != 0 {
		t.Fatalf("first result = %+v, want queued restart", first)
	}
	if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	restarted := daemonOrchestrationAuthority{daemon: newOrchestrationReviewTestDaemon(repoDir, client)}
	restarted.lookupOperation = func(_ context.Context, operationID string) (protocol.OperationRecord, error) {
		return protocol.OperationRecord{
			OperationID: naming.OperationID(operationID), ProjectID: "project", Kind: "session.start", IssueID: naming.IssueID(issueID),
			State: protocol.OperationStateFailed, Error: &protocol.OperationError{Message: "tmux start collided", Code: protocol.ErrorCodeConflict},
		}, nil
	}

	replayed, err := restarted.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Pending) != 0 || !strings.Contains(replayed.Failed[issueID], "terminal failed: tmux start collided") {
		t.Fatalf("replayed result = %+v, want delayed terminal failure", replayed)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 2 || reviewEvents[0].Payload["outcome"] != "restart_submitted" || reviewEvents[1].Payload["outcome"] != "delivery_failed" {
		t.Fatalf("review events = %+v, want submitted then terminal failure", reviewEvents)
	}
}

func TestReviewReturnDoesNotReportTerminalFailedRestartAsSubmitted(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.tmux = tmux.NewClient(newSessionStartTmuxRunner(), slog.Default())
	authority := daemonOrchestrationAuthority{daemon: d}
	authority.submitStart = func(_ context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
		body, err := json.Marshal(protocol.OperationSubmitResponseBody{Operation: protocol.OperationRecord{
			OperationID: naming.OperationID("op-review-failed"),
			ProjectID:   naming.ProjectID("project"),
			Kind:        "session.start",
			IssueID:     naming.IssueID(issueID),
			State:       protocol.OperationStateFailed,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return protocol.ResponseEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: req.RequestID, OK: true, Body: body}
	}
	request := protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn,
		IntentKey: "review-return-failed", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
		RestartWorker: true, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "retry safely"}},
	}

	result, err := authority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pending) != 0 || !strings.Contains(result.Failed[issueID], "terminal failed") {
		t.Fatalf("result = %+v, want terminal restart failure without pending", result)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	foundDeliveryFailed := false
	for _, event := range reviewEvents {
		if event.Payload["outcome"] == "restart_submitted" {
			t.Fatalf("review events = %+v, terminal failed restart must not be submitted", reviewEvents)
		}
		if event.Payload["outcome"] == "delivery_failed" && strings.Contains(fmt.Sprint(event.Payload["failure"]), "terminal failed") {
			foundDeliveryFailed = true
		}
	}
	if !foundDeliveryFailed {
		t.Fatalf("review events = %+v, want durable terminal delivery failure", reviewEvents)
	}
}

func TestReviewIntentLeavesForeignOwnedWorkUntouched(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "foreign", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseOrchestration}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{
		Scope:     domain.ProjectOrchestrationScope(),
		Kind:      protocol.OrchestrationIntentReviewReturn,
		IntentKey: "foreign-review",
		ActorID:   "orchestrator",
		IssueIDs:  []string{issueID},
		RepoDir:   repoDir,
		Findings:  []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "must not deliver"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Skipped[issueID], "orchestration-owned-by-foreign") {
		t.Fatalf("result = %+v, want foreign ownership skip", result)
	}
	events, err := readMailboxEvents(repoDir, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("foreign mailbox events = %+v, want none", events)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	orchestrationOwner := coordinationLease(task, domain.CoordinationLeaseOrchestration)
	if task.Ownership == nil || task.Ownership.OwnerID != "worker-a" || coordinationLease(task, domain.CoordinationLeaseReview) != nil || orchestrationOwner == nil || orchestrationOwner.OwnerID != "foreign" {
		t.Fatalf("foreign task mutated = %+v", task)
	}
}

func TestReviewAcceptRequiresCompleteEvidenceAndLeavesIssueReviewReady(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	d := newOrchestrationReviewTestDaemon(repoDir, client)

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-without-evidence", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Failed[issueID], "complete worker_evidence.v1") {
		t.Fatalf("result = %+v, want explicit evidence failure", result)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusInReview || task.Ownership == nil || task.Ownership.OwnerID != "orchestrator" {
		t.Fatalf("failed accepted review mutated issue = %+v", task)
	}
}

func TestLatestTrustedReviewOutcomeRejectsMissingReviewerProvenance(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"outcome": "accepted"}}); err != nil {
		t.Fatal(err)
	}
	authority := daemonOrchestrationAuthority{daemon: newOrchestrationReviewTestDaemon(repoDir, client)}
	outcome, err := authority.latestTrustedReviewOutcome(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "" {
		t.Fatalf("missing actor provenance produced trusted outcome %q", outcome)
	}
}

func TestReviewRestartSubmissionIgnoresUntrustedIntentConflict(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	request := protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn,
		IntentKey: "untrusted-restart", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
		RestartWorker: true, Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "real finding"}},
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventReviewCompleted, Source: "agent", SourceCommand: "review-return", OperationID: "op-forged",
		Payload: map[string]any{"outcome": "restart_submitted", "actor_id": "orchestrator", "intent_key": request.IntentKey, "request_fingerprint": "forged", "operation_state": "queued"},
	}); err != nil {
		t.Fatal(err)
	}
	authority := daemonOrchestrationAuthority{daemon: newOrchestrationReviewTestDaemon(repoDir, client)}
	if submission, found, err := authority.reviewRestartSubmission(ctx, client, issueID, request); err != nil || found {
		t.Fatalf("submission = %+v found=%t err=%v, want untrusted event ignored", submission, found, err)
	}
}

func TestReviewAcceptTrustsDecisionOverInternalReviewArtifactWithoutTreatingArtifactAsAcceptance(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "read-only review", Description: "review findings", Acceptance: "consumed by parent", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "agent", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "summary": "parent consumed findings"}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
			t.Fatal(err)
		}
	}
	before, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if domain.EvaluateInvestigationAcceptance(task, before).Accepted {
		t.Fatal("agent-authored artifact unexpectedly satisfied acceptance")
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-internal-review", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 0 || len(result.Closed) != 1 || result.Closed[0] != issueID {
		t.Fatalf("result = %+v, want authoritative tracking-only close", result)
	}
	after, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	task, err = client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if !domain.EvaluateInvestigationAcceptance(task, after).Accepted || task.Status != domain.StatusDone {
		t.Fatalf("closed investigation = %+v acceptance=%+v", task, domain.EvaluateInvestigationAcceptance(task, after))
	}
	if err := client.Update(ctx, issueID, domain.StatusOpen); err != nil {
		t.Fatal(err)
	}
	reopened, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-internal-review", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Failed) != 0 || len(reopened.Closed) != 1 || reopened.Closed[0] != issueID {
		t.Fatalf("reopened retry = %+v, want current-state authoritative close", reopened)
	}
	task, err = client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusDone {
		t.Fatalf("reopened review status = %s, want done after same-key recovery", task.Status)
	}
}

func TestReviewAcceptRejectsInternalReviewArtifactFromPriorEpoch(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "stale internal artifact", Description: "review findings", Acceptance: "consumed by parent", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "agent", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "ratified", "summary": "parent consumed findings"}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Update(ctx, issueID, domain.StatusOpen); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "reject-stale-artifact", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Failed[issueID]; !strings.Contains(got, "durable accepted/ratified review artifact") {
		t.Fatalf("result = %+v, want stale-artifact rejection", result)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "ratified", "summary": "current findings consumed"}}); err != nil {
		t.Fatal(err)
	}
	result, err = d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-current-artifact", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Closed) != 1 || result.Closed[0] != issueID || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want same-epoch artifact acceptance", result)
	}
}

func TestReviewAcceptSameIntentRecoversAfterCloseFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	issuesDBPath := filepath.Join(repoDir, "issues.db")
	client := newMigratedIssueClientAtPath(t, issuesDBPath, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "owned read-only review", Description: "review findings", Acceptance: "consumed by parent", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "agent", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "summary": "parent consumed findings"}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "worker", OwnerKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", issuesDBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_terminal_close
		BEFORE UPDATE ON issues
		WHEN NEW.lifecycle_state = 'closed'
		BEGIN
			SELECT RAISE(ABORT, 'injected terminal status write failure');
		END`); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-owned-review", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir}
	failed, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failed.Failed[issueID], "authoritative close") {
		t.Fatalf("first close = %+v, want injected authoritative close failure", failed)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !domain.EvaluateInvestigationAcceptance(task, events).Accepted {
		t.Fatal("operational close failure revoked semantic reviewer acceptance")
	}
	if task.Ownership == nil || task.Ownership.OwnerID != "worker" {
		t.Fatalf("failed terminal transaction ownership = %+v, want rolled-back execution lease", task.Ownership)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER fail_terminal_close`); err != nil {
		t.Fatal(err)
	}
	retried, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(retried.Failed) != 0 || len(retried.Closed) != 1 || retried.Closed[0] != issueID {
		t.Fatalf("same-intent retry = %+v, want successful close", retried)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	closeFailures, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCloseFailed}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 2 || len(closeFailures) != 1 {
		t.Fatalf("review events=%+v close failures=%+v, want one agent artifact, one semantic acceptance, one close failure", reviewEvents, closeFailures)
	}
	if err := client.Update(ctx, issueID, domain.StatusOpen); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventReviewCompleted,
		Source:        "daemon-orchestration",
		SourceCommand: string(protocol.OrchestrationIntentReviewReturn),
		Payload:       map[string]any{"actor_id": "orchestrator", "intent_key": "newer-return", "outcome": "returned"},
	}); err != nil {
		t.Fatal(err)
	}
	superseded, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Skipped[issueID] != "review-intent-superseded" {
		t.Fatalf("superseded retry = %+v, want stale acceptance skipped", superseded)
	}
	task, err = client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusOpen {
		t.Fatalf("superseded retry status = %s, want current reopened state preserved", task.Status)
	}
}

func TestReviewAcceptClosesMultipleInternalReviewsBeforeDependentCompletion(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "dependent synthesis", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 2)
	for _, title := range []string{"review A", "review B"} {
		id, err := client.Create(ctx, issues.CreateTaskParams{Title: title, Description: "read-only findings", Acceptance: "consumed", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview, ParentID: &rootID})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range []issues.IssueObservationEventParams{
			{Type: domain.IssueEventInvestigationDisposition, Source: "agent", Payload: map[string]any{"disposition": "internal_review"}},
			{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "consumer": rootID}},
		} {
			if _, err := client.AppendIssueObservationEvent(ctx, id, event); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, id)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	churn := 0
	var churnErr error
	d.orchestrationProjectionExported = func() {
		churn++
		_, churnErr = client.Create(ctx, issues.CreateTaskParams{
			Title: "Unrelated rooted churn", Description: "Advance projection during rooted requested review admission", Acceptance: "Recorded",
			Type: domain.TypeTask, Priority: domain.P4, Status: domain.StatusOpen,
		})
	}
	rootedScope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.OrchestrationIntentRequest{Scope: rootedScope, Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-review-batch", ActorID: "orchestrator", IssueIDs: ids, RepoDir: repoDir}
	authority := daemonOrchestrationAuthority{daemon: d}
	if err := authority.recordReviewOutcome(ctx, "project", ids[0], request, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	result, err := authority.Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 0 || len(result.Skipped) != 0 || len(result.Closed) != 2 {
		t.Fatalf("batch result = %+v", result)
	}
	if churn != 1 {
		t.Fatalf("rooted requested-review exports = %d, want one coherent export", churn)
	}
	if churnErr != nil {
		t.Fatalf("advance unrelated rooted projection: %v", churnErr)
	}
	d.orchestrationProjectionExported = nil
	for _, id := range ids {
		task, err := client.GetWithRuntime(ctx, "project", id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != domain.StatusDone {
			t.Fatalf("review %s status = %s", id, task.Status)
		}
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, ids[0], issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	acceptedCount := 0
	for _, event := range reviewEvents {
		if event.Payload["outcome"] == "accepted" && event.Source == "daemon-orchestration" {
			acceptedCount++
		}
	}
	if acceptedCount != 1 {
		t.Fatalf("accepted restart replay count = %d, events=%+v", acceptedCount, reviewEvents)
	}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ReviewQueue) != 0 {
		t.Fatalf("stale review queue after accepted closeout: %+v", snapshot.ReviewQueue)
	}
}

func TestReviewAcceptSurfacesAuthoritativeCloseFailureAndKeepsReviewState(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-ready", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Failed[issueID], "authoritative close") || !strings.Contains(result.Failed[issueID], "worktree adapter unavailable") {
		t.Fatalf("result = %+v, want explicit authoritative close failure", result)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("failed integration task = %+v, want review state preserved", task)
	}
	if lease := coordinationLease(task, domain.CoordinationLeaseReview); lease != nil {
		t.Fatalf("failed integration review lease = %+v, want released after durable acceptance", lease)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 1 || reviewEvents[0].Payload["outcome"] != "accepted" {
		t.Fatalf("review events = %+v, want one semantic acceptance", reviewEvents)
	}
	retried, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-ready", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(retried.Failed[issueID], "authoritative close") {
		t.Fatalf("retry result = %+v", retried)
	}
	reviewEvents, err = client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 1 {
		t.Fatalf("retry duplicated durable outcomes: %+v", reviewEvents)
	}
	closeFailures, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCloseFailed}})
	if err != nil {
		t.Fatal(err)
	}
	if len(closeFailures) != 1 {
		t.Fatalf("retry duplicated close failure: %+v", closeFailures)
	}
}

func TestReviewAcceptRetryRejectsBranchMutationAfterDurableAcceptance(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	sourceOID := "reviewed-source-a"
	d.reviewAcceptedSourceOID = func(context.Context, string, string) (string, error) { return sourceOID, nil }
	request := protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept,
		IntentKey: "accept-before-branch-mutation", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
	}

	first, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Failed[issueID], "authoritative close") {
		t.Fatalf("first acceptance = %+v, want accepted-close retry state", first)
	}
	sourceOID = "unreviewed-source-b"
	retried, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(retried.Failed[issueID], "fresh review required") || strings.Contains(retried.Failed[issueID], "authoritative close") {
		t.Fatalf("mutated retry = %+v, want fail-closed before task.close", retried)
	}
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	var acceptedOID string
	integrationFailed := false
	for _, event := range events {
		switch event.Payload["outcome"] {
		case "accepted":
			acceptedOID, _ = event.Payload["reviewed_source_oid"].(string)
		case "integration_failed":
			integrationFailed = true
		}
	}
	if acceptedOID != "reviewed-source-a" || !integrationFailed {
		t.Fatalf("review events = %+v, want pinned accepted OID plus integration_failed", events)
	}

	superseded, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Skipped[issueID] != "review-intent-superseded" {
		t.Fatalf("same intent after mutation = %+v, want fresh intent requirement", superseded)
	}
	request.IntentKey = "fresh-review-after-branch-mutation"
	fresh, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fresh.Failed[issueID], "authoritative close") {
		t.Fatalf("fresh review = %+v, want new acceptance to reach close", fresh)
	}
}

func TestReviewAcceptTerminalCloseRejectsEvidenceMutationAfterLeaseRelease(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.reviewAcceptedSourceOID = func(context.Context, string, string) (string, error) { return "reviewed-source", nil }
	request := protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept,
		IntentKey: "accept-before-evidence-mutation", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
	}
	first, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Failed[issueID], "authoritative close") {
		t.Fatalf("first acceptance = %+v, want accepted-close retry state", first)
	}
	mutated := mustWorkerEvidencePayload(t)
	mutated["summary"] = "new evidence after review acceptance"
	d.reviewLeaseReleasedBeforeClose = func(hookCtx context.Context, _, releasedIssueID string) error {
		d.reviewLeaseReleasedBeforeClose = nil
		_, err := client.AppendIssueObservationEvent(hookCtx, releasedIssueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventEvidenceSubmitted, Source: "late-worker", Payload: mutated,
		})
		return err
	}
	retried, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if failure := retried.Failed[issueID]; !strings.Contains(failure, "reviewed evidence changed") || !strings.Contains(failure, "authoritative close") {
		t.Fatalf("post-release evidence mutation = %+v, want terminal close revalidation failure", retried)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusInReview {
		t.Fatalf("task = %+v, want review state preserved", task)
	}
	integrationEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(integrationEvents) != 0 {
		t.Fatalf("integration events = %+v, want no integration before rejected close", integrationEvents)
	}
}

func TestReviewAcceptUsesReplayedTicketIntegrationReadyEvidenceIdempotently(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "worker", Description: "Executable", Acceptance: "validated", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "worker", OwnerKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: "worker-integration-ready", Source: "az issue record", Payload: map[string]any{"worker_evidence": mustWorkerEvidencePayload(t)},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 75; i++ {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventProgressRecorded, Source: "test", Payload: map[string]any{"summary": fmt.Sprintf("unrelated event %d", i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	mail, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: "project"}}, repoDir, rootID)
	if err != nil || len(mail) != 1 {
		t.Fatalf("replay=%+v err=%v", mail, err)
	}
	if _, validation := domain.ParseWorkerEvidencePacketBody(mail[0].Body); !validation.Complete {
		t.Fatalf("replayed body=%s validation=%+v", mail[0].Body, validation)
	}
	rootedScope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.OrchestrationIntentRequest{Scope: rootedScope, Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-replayed-ticket-evidence", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir}
	first, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if failure := first.Failed[issueID]; !strings.Contains(failure, "authoritative close") || strings.Contains(failure, "requires complete worker_evidence") {
		t.Fatalf("first=%+v, want evidence accepted before expected close-adapter failure", first)
	}
	afterFirst, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.Status != domain.StatusInReview {
		t.Fatalf("status after first close failure = %s, want in_review", afterFirst.Status)
	}
	afterFirstSnapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterFirstSnapshot.ReviewQueue) != 1 || afterFirstSnapshot.ReviewQueue[0].Evidence == nil || !strings.Contains(strings.Join(afterFirstSnapshot.ReviewQueue[0].Reasons, "\n"), "accepted-close-pending") {
		t.Fatalf("review queue after first close failure = %+v, want accepted retry with preserved evidence", afterFirstSnapshot.ReviewQueue)
	}
	second, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Failed[issueID], "authoritative close") {
		t.Fatalf("second=%+v, want idempotent accepted-close retry", second)
	}
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Payload["outcome"] != "accepted" {
		t.Fatalf("review events=%+v, want one durable acceptance", events)
	}
}

func TestReviewReplayAndAcceptShareObservedAtEvidenceOrdering(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	rootID, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "worker", Description: "Executable", Acceptance: "validated", Type: domain.TypeTask, Status: domain.StatusOpen, ParentID: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "worker", OwnerKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
	malformed, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: "worker-integration-ready", ObservedAt: base.Add(2 * time.Second), Source: "legacy", Payload: map[string]any{"schema": domain.WorkerEvidenceSchemaV1, "summary": "latest by authority time but incomplete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventEvidenceSubmitted, ObservedAt: base.Add(time.Second), Source: "legacy", Payload: mustWorkerEvidencePayload(t),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventIssueStatusChanged, ObservedAt: base.Add(3 * time.Second), Source: "issue-store", Payload: map[string]any{"from_status": "in_progress", "to_status": "in_review"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	mail, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: "project"}}, repoDir, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mail) != 0 {
		t.Fatalf("replay=%+v, want no readiness for latest incomplete evidence", mail)
	}
	rootedScope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.OrchestrationIntentRequest{Scope: rootedScope, Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "reject-authoritatively-latest-incomplete", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir}
	result, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if failure := result.Failed[issueID]; !strings.Contains(failure, "requires complete worker_evidence") {
		t.Fatalf("result=%+v, want acceptance to reject the same incomplete evidence", result)
	}
	readiness, err := d.taskIntegrationReadiness(ctx, "project", issueID, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.EvidenceEventID != malformed.ID || !readiness.EvidenceIncomplete {
		t.Fatalf("readiness=%+v, want malformed event %d selected", readiness, malformed.ID)
	}
}

func TestReviewAcceptReleasesLeaseBeforeAcceptedInternalReviewClose(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "accepted internal review", Description: "Executable", Acceptance: "validated and reviewed", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "test", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "summary": "parent consumed findings"}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingGitRunner{}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.git = git.NewClient(runner, slog.Default())
	d.worktreeAdapter = &worktreeServiceAdapter{manager: git.NewWorktreeManager(runner, repoDir, slog.Default()), logger: slog.Default()}

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-internal-review", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Closed) != 1 || result.Closed[0] != issueID || len(result.Failed) != 0 {
		t.Fatalf("accept result = %+v, want accepted terminal close", result)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusDone || coordinationLease(task, domain.CoordinationLeaseReview) != nil {
		t.Fatalf("closed task = %+v, want done without review lease", task)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 2 {
		t.Fatalf("review events = %+v, want internal artifact and accepted outcome without integration_failed", reviewEvents)
	}
	allEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted, domain.IssueEventIssueOwnershipChanged, domain.IssueEventIssueStatusChanged}})
	if err != nil {
		t.Fatal(err)
	}
	acceptedID, releaseID, closeID := int64(0), int64(0), int64(0)
	for _, event := range allEvents {
		switch {
		case event.Type == domain.IssueEventReviewCompleted && event.Payload["outcome"] == "accepted":
			acceptedID = event.ID
		case event.Type == domain.IssueEventIssueOwnershipChanged && event.Payload["action"] == "released" && event.Payload["purpose"] == string(domain.CoordinationLeaseReview):
			releaseID = event.ID
		case event.Type == domain.IssueEventIssueStatusChanged && event.Payload["to_status"] == string(domain.StatusDone):
			closeID = event.ID
		}
	}
	if acceptedID == 0 || releaseID <= acceptedID || closeID <= releaseID {
		t.Fatalf("event order accepted=%d release=%d close=%d, want accepted < review release < close", acceptedID, releaseID, closeID)
	}

	replayed, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-internal-review", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Closed) != 1 || replayed.Closed[0] != issueID || len(replayed.Failed) != 0 {
		t.Fatalf("replayed accept result = %+v, want idempotent closed result", replayed)
	}
	reviewEvents, err = client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 2 {
		t.Fatalf("review events after replay = %+v, want no duplicate outcome", reviewEvents)
	}
}

func TestReviewAcceptFenceBlocksSecondDaemonReturnBetweenReleaseAndClose(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	path := filepath.Join(repoDir, "issues.db")
	acceptClient := newMigratedIssueClientAtPath(t, path, slog.Default())
	returnClient := newMigratedIssueClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = acceptClient.CloseDB(); _ = returnClient.CloseDB() })
	issueID, err := acceptClient.Create(ctx, issues.CreateTaskParams{Title: "cross-daemon accepted review", Description: "Executable", Acceptance: "validated and reviewed", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "test", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "summary": "parent consumed findings"}},
	} {
		if _, err := acceptClient.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
			t.Fatal(err)
		}
	}
	acceptDaemon := newOrchestrationReviewTestDaemon(repoDir, acceptClient)
	returnDaemon := newOrchestrationReviewTestDaemon(repoDir, returnClient)
	runner := &recordingGitRunner{}
	acceptDaemon.git = git.NewClient(runner, slog.Default())
	acceptDaemon.worktreeAdapter = &worktreeServiceAdapter{manager: git.NewWorktreeManager(runner, repoDir, slog.Default()), logger: slog.Default()}
	var competing protocol.OrchestrationIntentResult
	acceptDaemon.reviewLeaseReleasedBeforeClose = func(hookCtx context.Context, projectID, releasedIssueID string) error {
		var hookErr error
		competing, hookErr = returnDaemon.orchestrationAuthority().Apply(hookCtx, projectID, protocol.OrchestrationIntentRequest{
			Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewReturn,
			IntentKey: "second-daemon-return", ActorID: "second-orchestrator", IssueIDs: []string{releasedIssueID}, RepoDir: repoDir,
			Findings: []protocol.OrchestrationReviewFinding{{Severity: "high", Finding: "late finding"}},
		})
		return hookErr
	}
	accepted, err := acceptDaemon.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept,
		IntentKey: "first-daemon-accept", ActorID: "first-orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted.Closed) != 1 || accepted.Closed[0] != issueID || len(accepted.Failed) != 0 {
		t.Fatalf("accepted result = %+v, want authoritative close", accepted)
	}
	if got := competing.Skipped[issueID]; !strings.Contains(got, "claim-review") || !strings.Contains(got, "accepted review") {
		t.Fatalf("competing result = %+v, want durable accepted-epoch claim fence", competing)
	}
	events, err := acceptClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("review outcomes = %+v, want internal artifact and daemon acceptance only", events)
	}
	task, err := returnClient.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusDone || coordinationLease(task, domain.CoordinationLeaseReview) != nil {
		t.Fatalf("final task = %+v, want done without review lease", task)
	}
}

func TestReviewAcceptResumesDurablyAcceptedIntentWithoutReacquiringLease(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "resume accepted internal review", Description: "Executable", Acceptance: "validated and reviewed", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "resume-accepted-internal-review", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: request.ActorID, OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "test", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "summary": "parent consumed findings"}},
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: string(protocol.OrchestrationIntentReviewAccept), Payload: map[string]any{"outcome": "accepted", "actor_id": request.ActorID, "intent_key": request.IntentKey, "request_fingerprint": reviewRequestFingerprint(request)}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingGitRunner{}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.git = git.NewClient(runner, slog.Default())
	d.worktreeAdapter = &worktreeServiceAdapter{manager: git.NewWorktreeManager(runner, repoDir, slog.Default()), logger: slog.Default()}

	result, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Closed) != 1 || result.Closed[0] != issueID || len(result.Failed) != 0 {
		t.Fatalf("resumed accept result = %+v, want terminal close", result)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusDone || coordinationLease(task, domain.CoordinationLeaseReview) != nil {
		t.Fatalf("resumed accepted task = %+v, want done without review lease", task)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 2 {
		t.Fatalf("resumed review events = %+v, want original artifact and daemon acceptance only", reviewEvents)
	}
}

func TestReviewAcceptResumesDurableEvidenceFenceAfterCloseCrash(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker")
	event, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	readiness, err := d.taskIntegrationReadiness(ctx, "project", issueID, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	evidencePin, err := reviewEvidencePinFromReadiness(readiness)
	if err != nil || evidencePin.EventID != event.ID {
		t.Fatalf("evidence pin = %+v err=%v", evidencePin, err)
	}
	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "resume-evidence-fence", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: string(protocol.OrchestrationIntentReviewAccept),
		Payload: map[string]any{
			"outcome": "accepted", "actor_id": request.ActorID, "intent_key": request.IntentKey,
			"request_fingerprint": reviewRequestFingerprint(request), "reviewed_source_oid": "reviewed-source-oid",
			"reviewed_evidence_source": evidencePin.Source, "reviewed_evidence_event_id": evidencePin.EventID,
			"reviewed_evidence_digest": evidencePin.Digest,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.BeginReviewEvidenceClose(ctx, issueID, evidencePin); err != nil {
		t.Fatal(err)
	}

	result, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	failure := result.Failed[issueID]
	if !strings.Contains(failure, "authoritative close") || strings.Contains(failure, "release review lease") {
		t.Fatalf("resumed fenced accept = %+v, want replay to enter authoritative close", result)
	}
}

func TestReviewAcceptConvergesStaleBusyHookAtIdlePromptAndReplaysIdempotently(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, "issues.db")
	client := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "stale busy internal review", Description: "review findings", Acceptance: "consumed by parent", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "orchestrator", OwnerKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []issues.IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "test", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "agent", Payload: map[string]any{"outcome": "accepted", "summary": "parent consumed findings"}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, event); err != nil {
			t.Fatal(err)
		}
	}
	sessionID := naming.CanonicalSessionID("project", issueID)
	staleAt := time.Now().UTC().Add(-time.Minute)
	if err := upsertSessionStateFixture(runtimeStore, ctx, "project", daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: staleAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtimeStore.UpsertSessionActivityEvidence(ctx, daemonstate.SessionActivityEvidence{
		ProjectID: "project", SessionID: sessionID, IssueID: issueID,
		Activity: "busy", ActivitySource: "hooks", SourceSessionID: sessionID,
		Agent: "codex", Hook: "user_prompt_submit", Event: "user_prompt_submit",
		ObservedAt: staleAt, UpdatedAt: staleAt,
	}); err != nil {
		t.Fatal(err)
	}
	captureCalls := 0
	runner := &recordingGitRunner{runFn: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return sessionID, nil
		case "capture-pane":
			captureCalls++
			return "Implementation and validation complete.\n› Continue", nil
		default:
			return "", nil
		}
	}}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{"project": runtimeStore}
	d.tmux = tmux.NewClient(runner, slog.Default())
	d.sessionStore = daemonstate.NewStore()
	if _, err := d.sessionStore.UpsertSession("project", sessionID, issueID, daemonstate.SessionStateRunning); err != nil {
		t.Fatal(err)
	}
	d.git = git.NewClient(runner, slog.Default())
	d.worktreeAdapter = &worktreeServiceAdapter{manager: git.NewWorktreeManager(runner, repoDir, slog.Default()), logger: slog.Default()}
	projected, err := runtimeStore.ListSessionStates(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.observeTerminalFailureProbes(ctx, "project", projected, "project", sessionDisplayActivityByIssueKeyFromSessions(projected, "project")); err != nil {
		t.Fatalf("seed asynchronous idle-prompt observation: %v", err)
	}
	request := protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept,
		IntentKey: "accept-stale-busy-idle-prompt", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir,
	}

	result, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Closed) != 1 || result.Closed[0] != issueID || len(result.Failed) != 0 {
		t.Fatalf("review accept result = %+v captures=%d, want stale busy activity converged and issue closed", result, captureCalls)
	}
	if captureCalls != 2 {
		t.Fatalf("terminal prompt captures = %d, want one sparse observation and one authoritative revalidation", captureCalls)
	}
	replayed, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Closed) != 1 || replayed.Closed[0] != issueID || len(replayed.Failed) != 0 {
		t.Fatalf("replayed review accept = %+v, want idempotent closed result", replayed)
	}
	reviewEvents, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 2 {
		t.Fatalf("review events = %+v, want one artifact and one durable daemon acceptance", reviewEvents)
	}
}

func createReviewTask(t *testing.T, ctx context.Context, client *issues.Client, priority domain.Priority, owner string) string {
	t.Helper()
	id, err := client.Create(ctx, issues.CreateTaskParams{Title: "review " + owner, Description: "Executable", Acceptance: "validated and reviewed", Type: domain.TypeTask, Priority: priority, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", id, issues.OwnershipClaimParams{OwnerID: owner, OwnerKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, id, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	return id
}

func latestReviewEpochEventID(t *testing.T, ctx context.Context, client *issues.Client, issueID string) int64 {
	t.Helper()
	events, err := client.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventIssueStatusChanged}, NewestIDFirst: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if domain.IsReviewRequestTransition(event) {
			return event.ID
		}
	}
	t.Fatal("review epoch event not found")
	return 0
}

func newOrchestrationReviewTestDaemon(repoDir string, client *issues.Client) *Daemon {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Daemon{
		cfg:                   Config{RepoDir: repoDir, Logger: logger},
		hub:                   publish.NewHub(16, 8, logger),
		issueClientsByProject: map[string]*issues.Client{"project": client},
		revision:              map[string]uint64{},
		// Review fixtures use explicit cancellation hooks; local correctness must
		// never depend on the production admission timeout or machine load.
		snapshotAdmissionContext: context.WithCancel,
		reviewAcceptedSourceOID: func(context.Context, string, string) (string, error) {
			return "reviewed-source-oid", nil
		},
	}
	d.reviewCandidateCheck = func(ctx context.Context, projectID string, inspection protocol.OrchestrationReview) (string, string, error) {
		oid, err := (daemonOrchestrationAuthority{daemon: d}).resolveAcceptedReviewSourceOID(ctx, projectID, inspection.IssueID)
		return strings.TrimSpace(inspection.WorktreePath), strings.TrimSpace(oid), err
	}
	return d
}

type scriptedAuthoritativeReceiver struct {
	deliver func(context.Context, authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error)
}

func (r scriptedAuthoritativeReceiver) DeliverAgentInput(ctx context.Context, request authoritativeAgentInputRequest) (authoritativeAgentInputAcknowledgement, error) {
	return r.deliver(ctx, request)
}

func mustWorkerEvidencePayload(t *testing.T) map[string]any {
	t.Helper()
	var payload map[string]any
	err := json.Unmarshal([]byte(`{"schema":"worker_evidence.v1","summary":"ready","commands_run":["go test ./..."],"key_assertions":["tests pass"],"files_changed":["internal/daemon/orchestration.go"],"review":{"status":"clean","findings":["none"]},"risks":["none"]}`), &payload)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
