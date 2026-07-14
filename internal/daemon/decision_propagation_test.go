package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
