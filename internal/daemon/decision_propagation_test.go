package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestParallelWorkerMaterialDecisionPropagationAndAcknowledgement(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })

	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "protocol root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Create(ctx, issues.CreateTaskParams{Title: "protocol v47", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Create(ctx, issues.CreateTaskParams{Title: "protocol v48", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := client.Create(ctx, issues.CreateTaskParams{Title: "unrelated docs", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, second, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}

	d := newOrchestrationReviewTestDaemon(repoDir, client)
	tmuxRunner := newSessionStartTmuxRunner()
	secondSessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope("project"), naming.IssueID(second)).String()
	tmuxRunner.sessions[secondSessionID] = true
	d.tmux = tmux.NewClient(tmuxRunner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service := issueDecisionService{daemon: d}
	serviceCtx := withDaemonProjectIDContext(ctx, "project")
	benign, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Naming note", Rationale: "nonmaterial"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddDecisionLink(serviceCtx, protocol.DecisionLinkAddRequestBody{DecisionID: benign.LocalID, TargetKind: protocol.DecisionTargetIssue, TargetID: first, Relation: protocol.DecisionRelationAppliesTo}); err != nil {
		t.Fatal(err)
	}
	if len(tmuxRunner.inputPayloads) != 0 {
		t.Fatalf("benign decision interrupted active worker: %#v", tmuxRunner.inputPayloads)
	}
	decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Protocol v47 is authoritative", Rationale: "Sibling integration selected v47"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddDecisionLink(serviceCtx, protocol.DecisionLinkAddRequestBody{DecisionID: decision.LocalID, TargetKind: protocol.DecisionTargetIssue, TargetID: first, Relation: protocol.DecisionRelationGoverns}); err != nil {
		t.Fatal(err)
	}

	events, err := client.ListIssueDecisionObservationEvents(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	pending := domain.ReducePendingDecisionChanges(events)
	if len(pending) != 1 || pending[0].DecisionID != decision.LocalID || pending[0].Revision <= 0 {
		t.Fatalf("pending = %+v, want revision-aware sibling decision", pending)
	}
	if len(tmuxRunner.inputPayloads) != 1 || !strings.Contains(tmuxRunner.inputPayloads[0], "revision "+fmt.Sprint(pending[0].Revision)) || !strings.Contains(tmuxRunner.inputPayloads[0], "az decision acknowledge") {
		t.Fatalf("active worker wake payloads = %#v commands=%#v session=%q, want exact revision and acknowledgement command", tmuxRunner.inputPayloads, tmuxRunner.commands, secondSessionID)
	}
	if unrelatedEvents, err := client.ListIssueDecisionObservationEvents(ctx, unrelated); err != nil || len(unrelatedEvents) != 0 {
		t.Fatalf("unrelated events = %+v err=%v, want no interruption", unrelatedEvents, err)
	}
	if _, _, err := d.updateTaskStatusExcludingClose(ctx, "project", second, domain.StatusInReview, taskStatusUpdateOptions{}); err == nil || !strings.Contains(err.Error(), "stale material decision") {
		t.Fatalf("review handoff err=%v, want stale material decision rejection", err)
	}

	readiness, err := d.taskIntegrationReadiness(ctx, "project", second, "")
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Ready || len(readiness.PendingDecisions) != 1 || !strings.Contains(strings.Join(readiness.Reasons, " "), "stale material decision") {
		t.Fatalf("readiness = %+v, want stale decision rejection", readiness)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, second, issues.IssueObservationEventParams{
		Type: domain.IssueEventDecisionAcknowledged, Source: "worker", SourceCommand: "task.event_append",
		Payload: map[string]any{"decision_id": decision.LocalID, "revision": pending[0].Revision, "disposition": domain.DecisionAcknowledgementReconciled},
	}); err != nil {
		t.Fatal(err)
	}
	readiness, err = d.taskIntegrationReadiness(ctx, "project", second, "")
	if err != nil || readiness.Ready || len(readiness.PendingDecisions) != 1 {
		t.Fatalf("forged acknowledgement readiness=%+v err=%v, want authoritative gate preserved", readiness, err)
	}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: mustRootedDecisionScope(t, root), ActorID: "orchestrator", RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PendingDecisions[second]) != 1 || !strings.Contains(snapshot.Blocked[second], "acknowledged") {
		t.Fatalf("snapshot pending=%+v blocked=%+v", snapshot.PendingDecisions, snapshot.Blocked)
	}

	ack, err := service.AcknowledgeDecision(withDaemonProjectIDContext(ctx, "project"), protocol.DecisionAcknowledgeRequestBody{
		IssueID: naming.IssueID(second), DecisionID: decision.LocalID, Revision: pending[0].Revision,
		Disposition: domain.DecisionAcknowledgementCompatible, Note: "v48 code is wire-compatible with authoritative v47",
	})
	if err != nil || ack.EventID == 0 {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	if _, _, err := d.updateTaskStatusExcludingClose(ctx, "project", second, domain.StatusInReview, taskStatusUpdateOptions{}); err != nil {
		t.Fatalf("review handoff after acknowledgement: %v", err)
	}
	// Exact duplicate acknowledgement is replay-safe and returns the same event.
	replayed, err := service.AcknowledgeDecision(withDaemonProjectIDContext(ctx, "project"), protocol.DecisionAcknowledgeRequestBody{
		IssueID: naming.IssueID(second), DecisionID: decision.LocalID, Revision: pending[0].Revision,
		Disposition: domain.DecisionAcknowledgementCompatible, Note: "same evidence",
	})
	if err != nil || replayed.EventID != ack.EventID {
		t.Fatalf("replayed=%+v err=%v, want event %d", replayed, err, ack.EventID)
	}

	// A fresh daemon derives the acknowledgement from SQLite and does not
	// redeliver or re-block the already acknowledged revision.
	restarted := newOrchestrationReviewTestDaemon(repoDir, client)
	readiness, err = restarted.taskIntegrationReadiness(ctx, "project", second, "")
	if err != nil || !readiness.Ready || len(readiness.PendingDecisions) != 0 {
		t.Fatalf("restarted readiness=%+v err=%v", readiness, err)
	}

	newRationale := "Sibling integration amended the protocol contract"
	restartedService := issueDecisionService{daemon: restarted}
	if _, err := restartedService.UpdateDecision(serviceCtx, protocol.DecisionUpdateRequestBody{ID: decision.LocalID, Rationale: &newRationale}); err != nil {
		t.Fatal(err)
	}
	readiness, err = restarted.taskIntegrationReadiness(ctx, "project", second, "")
	if err != nil || readiness.Ready || len(readiness.PendingDecisions) != 1 || readiness.PendingDecisions[0].Revision == pending[0].Revision {
		t.Fatalf("new revision readiness=%+v err=%v", readiness, err)
	}
	result, err := restarted.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{
		Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept,
		IntentKey: "reject-stale-material-decision", ActorID: "orchestrator", IssueIDs: []string{second}, RepoDir: repoDir,
	})
	if err != nil || !strings.Contains(result.Failed[second], "stale material decisions") {
		t.Fatalf("review accept result=%+v err=%v, want stale decision rejection", result, err)
	}
}

func TestDecisionPropagationRetriesCrossDaemonScopeChangesBeforeUpdateReadiness(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, "issues.db")
	clientA := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = clientA.CloseDB() })
	clientB := issues.NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = clientB.CloseDB() })
	issueA, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "scope a", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueB, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "scope b", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := clientA.RecordDecision(ctx, issues.RecordDecisionParams{Title: "shared protocol", Rationale: "initial"})
	if err != nil {
		t.Fatal(err)
	}

	daemonA := newOrchestrationReviewTestDaemon(repoDir, clientA)
	daemonB := newOrchestrationReviewTestDaemon(repoDir, clientB)
	serviceA := issueDecisionService{daemon: daemonA}
	serviceB := issueDecisionService{daemon: daemonB}
	serviceCtx := withDaemonProjectIDContext(ctx, "project")
	governs := func(issueID string) protocol.DecisionLinkAddRequestBody {
		return protocol.DecisionLinkAddRequestBody{DecisionID: decision.LocalID, TargetKind: protocol.DecisionTargetIssue, TargetID: issueID, Relation: protocol.DecisionRelationGoverns}
	}
	if _, err := serviceA.AddDecisionLink(serviceCtx, governs(issueA)); err != nil {
		t.Fatal(err)
	}

	ackPending := func(service issueDecisionService, client *issues.Client, issueID string) int64 {
		t.Helper()
		events, err := client.ListIssueDecisionObservationEvents(ctx, issueID)
		if err != nil {
			t.Fatal(err)
		}
		pending := domain.ReducePendingDecisionChanges(events)
		if len(pending) != 1 {
			t.Fatalf("pending %s=%+v, want one decision", issueID, pending)
		}
		if _, err := service.AcknowledgeDecision(serviceCtx, protocol.DecisionAcknowledgeRequestBody{
			IssueID: naming.IssueID(issueID), DecisionID: decision.LocalID, Revision: pending[0].Revision,
			Disposition: domain.DecisionAcknowledgementReconciled,
		}); err != nil {
			t.Fatal(err)
		}
		return pending[0].Revision
	}
	ackPending(serviceA, clientA, issueA)

	runPausedUpdate := func(rationale string, concurrent func()) error {
		t.Helper()
		captured := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		serviceA.beforeDecisionPropagationMutation = func(command string) {
			if command != protocol.CommandDecisionUpdate {
				return
			}
			once.Do(func() {
				close(captured)
				<-release
			})
		}
		result := make(chan error, 1)
		go func() {
			_, err := serviceA.UpdateDecision(serviceCtx, protocol.DecisionUpdateRequestBody{ID: decision.LocalID, Rationale: &rationale})
			result <- err
		}()
		<-captured
		concurrent()
		close(release)
		return <-result
	}

	if err := runPausedUpdate("update after adding b", func() {
		if _, err := serviceB.AddDecisionLink(serviceCtx, governs(issueB)); err != nil {
			t.Fatal(err)
		}
		ackPending(serviceB, clientB, issueB)
	}); err != nil {
		t.Fatal(err)
	}
	if err := daemonB.validateTaskDecisionAcknowledgementsForReview(ctx, "project", issueB); err == nil || !strings.Contains(err.Error(), "stale material decision") {
		t.Fatalf("issue b review readiness err=%v, want retried update revision pending", err)
	}
	ackPending(serviceA, clientA, issueA)
	ackPending(serviceB, clientB, issueB)

	if err := runPausedUpdate("update after removing b", func() {
		if _, err := serviceB.RemoveDecisionLink(serviceCtx, protocol.DecisionLinkRemoveRequestBody{DecisionID: decision.LocalID, TargetKind: protocol.DecisionTargetIssue, TargetID: issueB}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := daemonB.validateTaskDecisionAcknowledgementsForReview(ctx, "project", issueB); err != nil {
		t.Fatalf("removed issue b remained blocked after retried update: %v", err)
	}
	if err := daemonA.validateTaskDecisionAcknowledgementsForReview(ctx, "project", issueA); err == nil || !strings.Contains(err.Error(), "stale material decision") {
		t.Fatalf("issue a review readiness err=%v, want latest update pending", err)
	}
}

func TestDecisionPropagationRetriesCrossDaemonSpecLinkAddRemoveRaces(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, "issues.db")
	clientA := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = clientA.CloseDB() })
	clientB := issues.NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = clientB.CloseDB() })
	issueA, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "spec scope a", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueB, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "spec scope b", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := clientA.CreateRequirement(ctx, issues.CreateRequirementParams{LocalID: "REQ-SPEC-RACE", Title: "shared requirement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clientA.AddSpecLink(ctx, issues.AddSpecLinkParams{IssueID: issueA, RequirementID: requirement.LocalID, Role: issues.LinkRoleImplements}); err != nil {
		t.Fatal(err)
	}
	decision, err := clientA.RecordDecision(ctx, issues.RecordDecisionParams{Title: "requirement contract", Rationale: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	daemonA := newOrchestrationReviewTestDaemon(repoDir, clientA)
	serviceA := issueDecisionService{daemon: daemonA}
	serviceCtx := withDaemonProjectIDContext(ctx, "project")
	governsRequirement := protocol.DecisionLinkAddRequestBody{
		DecisionID: decision.LocalID, TargetKind: protocol.DecisionTargetRequirement,
		TargetID: requirement.LocalID, Relation: protocol.DecisionRelationGoverns,
	}
	if _, err := serviceA.AddDecisionLink(serviceCtx, governsRequirement); err != nil {
		t.Fatal(err)
	}
	ackPendingDecision(t, ctx, serviceA, clientA, serviceCtx, issueA, decision.LocalID)

	runPausedDecisionMutation(t, &serviceA, protocol.CommandDecisionUpdate, func() error {
		rationale := "update after spec-link add"
		_, err := serviceA.UpdateDecision(serviceCtx, protocol.DecisionUpdateRequestBody{ID: decision.LocalID, Rationale: &rationale})
		return err
	}, func() {
		if _, err := clientB.AddSpecLink(ctx, issues.AddSpecLinkParams{IssueID: issueB, RequirementID: requirement.LocalID, Role: issues.LinkRoleVerifies}); err != nil {
			t.Fatal(err)
		}
	})
	assertPendingDecisionCount(t, ctx, clientA, issueA, 1)
	assertPendingDecisionCount(t, ctx, clientB, issueB, 1)
	ackPendingDecision(t, ctx, serviceA, clientA, serviceCtx, issueA, decision.LocalID)
	ackPendingDecision(t, ctx, serviceA, clientB, serviceCtx, issueB, decision.LocalID)

	runPausedDecisionMutation(t, &serviceA, protocol.CommandDecisionLinkAdd, func() error {
		request := governsRequirement
		request.Note = "update after spec-link remove"
		_, err := serviceA.AddDecisionLink(serviceCtx, request)
		return err
	}, func() {
		if err := clientB.RemoveSpecLink(ctx, issueB, requirement.LocalID); err != nil {
			t.Fatal(err)
		}
	})
	assertPendingDecisionCount(t, ctx, clientA, issueA, 1)
	assertPendingDecisionCount(t, ctx, clientB, issueB, 0)
}

func TestDecisionPropagationRetriesCrossDaemonRequirementAssignmentRaces(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, "issues.db")
	clientA := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = clientA.CloseDB() })
	clientB := issues.NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = clientB.CloseDB() })
	issueA, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "requirement owner a", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueB, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "requirement owner b", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := clientA.CreateRequirement(ctx, issues.CreateRequirementParams{LocalID: "REQ-OWNER-RACE", Title: "owned requirement"})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := clientA.RecordDecision(ctx, issues.RecordDecisionParams{Title: "ownership contract", Rationale: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	daemonA := newOrchestrationReviewTestDaemon(repoDir, clientA)
	serviceA := issueDecisionService{daemon: daemonA}
	serviceCtx := withDaemonProjectIDContext(ctx, "project")
	governsRequirement := protocol.DecisionLinkAddRequestBody{
		DecisionID: decision.LocalID, TargetKind: protocol.DecisionTargetRequirement,
		TargetID: requirement.LocalID, Relation: protocol.DecisionRelationGoverns,
	}
	if _, err := serviceA.AddDecisionLink(serviceCtx, governsRequirement); err != nil {
		t.Fatal(err)
	}

	runPausedDecisionMutation(t, &serviceA, protocol.CommandDecisionUpdate, func() error {
		rationale := "update after requirement assignment"
		_, err := serviceA.UpdateDecision(serviceCtx, protocol.DecisionUpdateRequestBody{ID: decision.LocalID, Rationale: &rationale})
		return err
	}, func() {
		if _, err := clientB.UpdateRequirement(ctx, requirement.LocalID, issues.UpdateRequirementParams{IssueID: &issueA}); err != nil {
			t.Fatal(err)
		}
	})
	assertPendingDecisionCount(t, ctx, clientA, issueA, 1)
	ackPendingDecision(t, ctx, serviceA, clientA, serviceCtx, issueA, decision.LocalID)

	runPausedDecisionMutation(t, &serviceA, protocol.CommandDecisionLinkAdd, func() error {
		request := governsRequirement
		request.Note = "update after requirement assignment removal"
		_, err := serviceA.AddDecisionLink(serviceCtx, request)
		return err
	}, func() {
		empty := ""
		if _, err := clientB.UpdateRequirement(ctx, requirement.LocalID, issues.UpdateRequirementParams{IssueID: &empty}); err != nil {
			t.Fatal(err)
		}
	})
	assertPendingDecisionCount(t, ctx, clientA, issueA, 0)

	if _, err := clientB.UpdateRequirement(ctx, requirement.LocalID, issues.UpdateRequirementParams{IssueID: &issueA}); err != nil {
		t.Fatal(err)
	}
	runPausedDecisionMutation(t, &serviceA, protocol.CommandDecisionLinkRemove, func() error {
		_, err := serviceA.RemoveDecisionLink(serviceCtx, protocol.DecisionLinkRemoveRequestBody{
			DecisionID: decision.LocalID, TargetKind: protocol.DecisionTargetRequirement, TargetID: requirement.LocalID,
		})
		return err
	}, func() {
		if _, err := clientB.UpdateRequirement(ctx, requirement.LocalID, issues.UpdateRequirementParams{IssueID: &issueB}); err != nil {
			t.Fatal(err)
		}
	})
	assertDecisionWithdrawnByCommand(t, ctx, clientA, issueA, decision.LocalID, protocol.CommandDecisionLinkRemove)
	assertDecisionWithdrawnByCommand(t, ctx, clientB, issueB, decision.LocalID, protocol.CommandDecisionLinkRemove)
}

func TestReconcileRetriedDecisionPropagationIntentPreservesFinalChangedScope(t *testing.T) {
	intent := reconcileRetriedDecisionPropagationIntent(issues.DecisionPropagationIntent{
		ChangedIssueIDs:   []string{"issue-a"},
		WithdrawnIssueIDs: []string{"issue-c"},
	}, []issues.DecisionPropagationIntent{
		{ChangedIssueIDs: []string{"issue-b"}, WithdrawnIssueIDs: []string{"issue-a"}},
	})
	if got, want := strings.Join(intent.ChangedIssueIDs, ","), "issue-a"; got != want {
		t.Fatalf("changed = %q, want %q", got, want)
	}
	if got, want := strings.Join(intent.WithdrawnIssueIDs, ","), "issue-c,issue-b"; got != want {
		t.Fatalf("withdrawn = %q, want %q", got, want)
	}
}

func runPausedDecisionMutation(t *testing.T, service *issueDecisionService, command string, mutation func() error, concurrent func()) {
	t.Helper()
	captured := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	service.beforeDecisionPropagationMutation = func(got string) {
		if got != command {
			return
		}
		once.Do(func() {
			close(captured)
			<-release
		})
	}
	t.Cleanup(func() { service.beforeDecisionPropagationMutation = nil })
	result := make(chan error, 1)
	go func() { result <- mutation() }()
	<-captured
	concurrent()
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	service.beforeDecisionPropagationMutation = nil
}

func ackPendingDecision(t *testing.T, ctx context.Context, service issueDecisionService, client *issues.Client, serviceCtx context.Context, issueID, decisionID string) {
	t.Helper()
	events, err := client.ListIssueDecisionObservationEvents(ctx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	pending := domain.ReducePendingDecisionChanges(events)
	if len(pending) != 1 || pending[0].DecisionID != decisionID {
		t.Fatalf("pending for %s = %+v, want decision %s", issueID, pending, decisionID)
	}
	if _, err := service.AcknowledgeDecision(serviceCtx, protocol.DecisionAcknowledgeRequestBody{
		IssueID: naming.IssueID(issueID), DecisionID: decisionID, Revision: pending[0].Revision,
		Disposition: domain.DecisionAcknowledgementReconciled,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDecisionWithdrawnByCommand(t *testing.T, ctx context.Context, client *issues.Client, issueID, decisionID, command string) {
	t.Helper()
	events, err := client.ListIssueDecisionObservationEvents(ctx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type == domain.IssueEventDecisionChanged && event.SourceCommand == command && strings.TrimSpace(fmt.Sprint(event.Payload["decision_id"])) == decisionID && event.Payload["withdrawn"] == true {
			return
		}
	}
	t.Fatalf("events for %s = %+v, want withdrawn %s from %s", issueID, events, decisionID, command)
}

func mustRootedDecisionScope(t *testing.T, root string) domain.OrchestrationScope {
	t.Helper()
	scope, err := domain.RootedOrchestrationScope(root)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestDecisionScopeRemovalAndDeletionWithdrawPendingRevision(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Create(ctx, issues.CreateTaskParams{Title: "first", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Create(ctx, issues.CreateTaskParams{Title: "second", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "govern", Rationale: "material"})
	if err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	service := issueDecisionService{daemon: d}
	serviceCtx := withDaemonProjectIDContext(ctx, "project")
	linkReq := protocol.DecisionLinkAddRequestBody{DecisionID: decision.LocalID, TargetKind: protocol.DecisionTargetIssue, TargetID: first, Relation: protocol.DecisionRelationGoverns}
	if _, err := service.AddDecisionLink(serviceCtx, linkReq); err != nil {
		t.Fatal(err)
	}
	assertPendingDecisionCount(t, ctx, client, second, 1)
	if _, err := service.RemoveDecisionLink(serviceCtx, protocol.DecisionLinkRemoveRequestBody{DecisionID: decision.LocalID, TargetKind: protocol.DecisionTargetIssue, TargetID: first}); err != nil {
		t.Fatal(err)
	}
	assertPendingDecisionCount(t, ctx, client, second, 0)
	if _, err := service.AddDecisionLink(serviceCtx, linkReq); err != nil {
		t.Fatal(err)
	}
	assertPendingDecisionCount(t, ctx, client, second, 1)
	if _, err := service.DeleteDecision(serviceCtx, protocol.DecisionDeleteRequestBody{ID: decision.LocalID, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	assertPendingDecisionCount(t, ctx, client, second, 0)
}

func TestDecisionPropagationOutboxRecoversCrashPartialFanoutAndDeliveryFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Create(ctx, issues.CreateTaskParams{Title: "first", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Create(ctx, issues.CreateTaskParams{Title: "second", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "wire contract", Rationale: "material"})
	if err != nil {
		t.Fatal(err)
	}
	beforeCrash := newOrchestrationReviewTestDaemon(repoDir, client)
	override := &decisionLinkOverride{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: first, Relation: issues.DecisionRelationGoverns}
	affected, err := beforeCrash.decisionAffectedIssuesWithLinkOverride(ctx, "project", decision.LocalID, override)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := beforeCrash.newDecisionPropagationIntent(ctx, "project", decision.LocalID, affected, nil, protocol.CommandDecisionLinkAdd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AddDecisionLinkWithPropagation(ctx, issues.AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: first, Relation: issues.DecisionRelationGoverns}, intent); err != nil {
		t.Fatal(err)
	}
	entries, err := client.ListActiveDecisionPropagationOutbox(ctx, 100)
	if err != nil || len(entries) != len(affected) {
		t.Fatalf("atomic outbox entries=%+v err=%v, want %d", entries, err, len(affected))
	}
	revision, err := client.DecisionRevision(ctx, decision.LocalID)
	if err != nil || entries[0].Revision != revision {
		t.Fatalf("outbox revision=%d durable revision=%d err=%v", entries[0].Revision, revision, err)
	}
	if events, err := client.ListIssueDecisionObservationEvents(ctx, second); err != nil || len(events) != 0 {
		t.Fatalf("pre-restart events=%+v err=%v, mutation must be recoverable before fanout", events, err)
	}
	// Simulate an N-of-M crash by checkpointing one target before the daemon dies.
	partial := entries[0]
	if _, err := client.MaterializeDecisionPropagationOutbox(ctx, partial); err != nil {
		t.Fatal(err)
	}

	failing := newOrchestrationReviewTestDaemon(repoDir, client)
	failingRunner := newSessionStartTmuxRunner()
	secondSessionID := naming.CanonicalSessionIDForIssue(failing.sessionNamingScope("project"), naming.IssueID(second)).String()
	failingRunner.sessions[secondSessionID] = true
	failingRunner.sendKeysErr = fmt.Errorf("injected delivery failure")
	failing.tmux = tmux.NewClient(failingRunner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := failing.reconcileDecisionPropagationOutbox(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	for _, issueID := range affected {
		if events, err := client.ListIssueDecisionObservationEvents(ctx, issueID); err != nil || len(domain.ReducePendingDecisionChanges(events)) != 1 {
			t.Fatalf("reconciled issue %s events=%+v err=%v", issueID, events, err)
		}
	}

	restarted := newOrchestrationReviewTestDaemon(repoDir, client)
	retryRunner := newSessionStartTmuxRunner()
	retryRunner.sessions[secondSessionID] = true
	restarted.tmux = tmux.NewClient(retryRunner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted.reconcileAllDecisionPropagationOutboxes(ctx)
	if len(retryRunner.inputPayloads) != 1 || !strings.Contains(retryRunner.inputPayloads[0], "az decision acknowledge") {
		t.Fatalf("restart retry payloads=%#v, want pending exact-revision wake", retryRunner.inputPayloads)
	}
	events, err := client.ListIssueDecisionObservationEvents(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	pending := domain.ReducePendingDecisionChanges(events)
	service := issueDecisionService{daemon: restarted}
	if _, err := service.AcknowledgeDecision(withDaemonProjectIDContext(ctx, "project"), protocol.DecisionAcknowledgeRequestBody{IssueID: naming.IssueID(second), DecisionID: decision.LocalID, Revision: pending[0].Revision, Disposition: domain.DecisionAcknowledgementReconciled}); err != nil {
		t.Fatal(err)
	}
	retryRunner.inputPayloads = nil
	if err := restarted.reconcileDecisionPropagationOutbox(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if len(retryRunner.inputPayloads) != 0 {
		t.Fatalf("acked revision redelivered after restart: %#v", retryRunner.inputPayloads)
	}
	active, err := client.ListActiveDecisionPropagationOutbox(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range active {
		if entry.IssueID == second && entry.DecisionID == decision.LocalID {
			t.Fatalf("acked worker outbox entry remains active: %+v", entry)
		}
	}
}

func TestDecisionPropagationRestartDiscoversUnopenedRegisteredProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	baseRepo := filepath.Join(t.TempDir(), "base")
	projectRepo := filepath.Join(t.TempDir(), "registered")
	if err := os.MkdirAll(baseRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	const projectID = "registered-project"
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: projectID, Name: "Registered", Path: projectRepo}}}); err != nil {
		t.Fatal(err)
	}
	seed := newMigratedIssueClientAtPath(t, filepath.Join(projectRepo, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = seed.CloseDB() })
	worker, err := seed.Create(ctx, issues.CreateTaskParams{Title: "live worker", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := seed.RecordDecision(ctx, issues.RecordDecisionParams{Title: "restart contract", Rationale: "material"})
	if err != nil {
		t.Fatal(err)
	}
	rationale := "revised before daemon crash"
	if _, err := seed.UpdateDecisionWithPropagation(ctx, decision.LocalID, issues.UpdateDecisionParams{Rationale: &rationale}, issues.DecisionPropagationIntent{
		ChangedIssueIDs: []string{worker}, SourceCommand: protocol.CommandDecisionUpdate,
	}); err != nil {
		t.Fatal(err)
	}

	// New has opened only baseRepo. Reconciliation must discover projectRepo
	// from the durable registry instead of waiting for a project command.
	restarted := New(Config{RepoDir: baseRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(restarted.closeIssueClients)
	if restarted.userStore != nil {
		t.Cleanup(func() { _ = restarted.userStore.Close() })
	}
	if _, alreadyOpened := restarted.issueClientsByProject[projectID]; alreadyOpened {
		t.Fatal("registered project unexpectedly opened before restart reconciliation")
	}
	runner := newSessionStartTmuxRunner()
	sessionID := naming.CanonicalSessionIDForIssue(restarted.sessionNamingScope(projectID), naming.IssueID(worker)).String()
	runner.sessions[sessionID] = true
	restarted.tmux = tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted.reconcileAllDecisionPropagationOutboxes(ctx)
	if len(runner.inputPayloads) != 1 || !strings.Contains(runner.inputPayloads[0], decision.LocalID) {
		t.Fatalf("restart discovery payloads=%#v, want registered project decision wake", runner.inputPayloads)
	}
	events, err := seed.ListIssueDecisionObservationEvents(ctx, worker)
	if err != nil || len(domain.ReducePendingDecisionChanges(events)) != 1 {
		t.Fatalf("restart materialized events=%+v err=%v", events, err)
	}
}

func TestDecisionPropagationRestartLeavesMissingRegisteredProjectAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AZEDARACH_DISABLE_USER_DB", "1")
	baseRepo := t.TempDir()
	missingRepo := filepath.Join(t.TempDir(), "deleted-project")
	const projectID = "missing-registered-project"
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: projectID, Name: "Missing", Path: missingRepo}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missingRepo); !os.IsNotExist(err) {
		t.Fatalf("missing project unexpectedly exists before reconcile: %v", err)
	}

	restarted := New(Config{RepoDir: baseRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(restarted.closeIssueClients)
	restarted.reconcileAllDecisionPropagationOutboxes(context.Background())
	if _, err := os.Stat(missingRepo); !os.IsNotExist(err) {
		t.Fatalf("background discovery recreated missing project path: %v", err)
	}
	if client := restarted.issueClientsByProject[projectID]; client != nil {
		t.Fatal("background discovery registered a creating client for missing project")
	}
	if err, unavailable := restarted.projectIssueStoreHealthError(projectID); !unavailable || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing project health err=%v unavailable=%v", err, unavailable)
	}
}

func TestDecisionPropagationRestartLeavesMissingRegisteredDatabaseAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AZEDARACH_DISABLE_USER_DB", "1")
	baseRepo := t.TempDir()
	missingStoreRepo := t.TempDir()
	const projectID = "missing-registered-store"
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: projectID, Name: "Missing Store", Path: missingStoreRepo}}}); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(missingStoreRepo, ".azedarach", "azedarach.db")

	restarted := New(Config{RepoDir: baseRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(restarted.closeIssueClients)
	restarted.reconcileAllDecisionPropagationOutboxes(context.Background())
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("background discovery recreated missing project database: %v", err)
	}
	if client := restarted.issueClientsByProject[projectID]; client != nil {
		t.Fatal("background discovery registered a creating client for missing database")
	}
	if err, unavailable := restarted.projectIssueStoreHealthError(projectID); !unavailable || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing project store health err=%v unavailable=%v", err, unavailable)
	}
}

func TestPendingDecisionEnrichmentComposesAndDeduplicatesExistingBlockers(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	worker, err := client.Create(ctx, issues.CreateTaskParams{Title: "blocked worker", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, worker, issues.IssueObservationEventParams{
		Type: domain.IssueEventDecisionChanged, Source: "daemon-decision", SourceCommand: protocol.CommandDecisionUpdate,
		Payload: map[string]any{"decision_id": "dec-compose", "revision": int64(48), "material": true},
	}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	snapshot := protocol.OrchestrationSnapshot{Blocked: map[string]string{worker: "dependency dep-1 is unresolved; waiting for interaction req-1"}}
	tasks := []domain.Task{{ID: naming.IssueID(worker), Status: domain.StatusInProgress}}
	authority := daemonOrchestrationAuthority{daemon: d}
	for range 2 {
		if err := authority.enrichPendingDecisions(ctx, "project", client, &snapshot, tasks); err != nil {
			t.Fatal(err)
		}
	}
	got := snapshot.Blocked[worker]
	for _, want := range []string{"dependency dep-1 is unresolved", "waiting for interaction req-1", "stale material decision dec-compose revision 48"} {
		if !strings.Contains(got, want) {
			t.Fatalf("composed blocker=%q, want %q", got, want)
		}
		if strings.Count(got, want) != 1 {
			t.Fatalf("composed blocker duplicated %q: %q", want, got)
		}
	}
}

func TestDecisionPropagationReconcileSuppressesSupersededAndReactivatesPredecessorAfterWithdrawal(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := client.Create(ctx, issues.CreateTaskParams{Title: "worker", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	runner := newSessionStartTmuxRunner()
	runner.sessions[naming.CanonicalSessionIDForIssue(d.sessionNamingScope("project"), naming.IssueID(worker)).String()] = true
	d.tmux = tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service := issueDecisionService{daemon: d}
	serviceCtx := withDaemonProjectIDContext(ctx, "project")
	first, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "v1", Rationale: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddDecisionLink(serviceCtx, protocol.DecisionLinkAddRequestBody{DecisionID: first.LocalID, TargetKind: protocol.DecisionTargetIssue, TargetID: worker, Relation: protocol.DecisionRelationGoverns}); err != nil {
		t.Fatal(err)
	}
	runner.inputPayloads = nil
	second, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "v2", Rationale: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddDecisionLink(serviceCtx, protocol.DecisionLinkAddRequestBody{DecisionID: second.LocalID, TargetKind: protocol.DecisionTargetDecision, TargetID: first.LocalID, Relation: protocol.DecisionRelationRevises}); err != nil {
		t.Fatal(err)
	}
	if len(runner.inputPayloads) != 1 || !strings.Contains(runner.inputPayloads[0], second.LocalID) || strings.Contains(runner.inputPayloads[0], "("+first.LocalID+")") {
		t.Fatalf("replacement delivery=%#v, want only active replacement", runner.inputPayloads)
	}
	runner.inputPayloads = nil
	if _, err := service.RemoveDecisionLink(serviceCtx, protocol.DecisionLinkRemoveRequestBody{DecisionID: second.LocalID, TargetKind: protocol.DecisionTargetDecision, TargetID: first.LocalID}); err != nil {
		t.Fatal(err)
	}
	if len(runner.inputPayloads) != 1 || !strings.Contains(runner.inputPayloads[0], first.LocalID) {
		t.Fatalf("withdrawal delivery=%#v, want predecessor reactivated without withdrawn replacement delivery", runner.inputPayloads)
	}
}

func assertPendingDecisionCount(t *testing.T, ctx context.Context, client *issues.Client, issueID string, want int) {
	t.Helper()
	events, err := client.ListIssueDecisionObservationEvents(ctx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if pending := domain.ReducePendingDecisionChanges(events); len(pending) != want {
		t.Fatalf("pending for %s = %+v, want count %d", issueID, pending, want)
	}
}
