package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

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
	decision, err := client.RecordDecision(ctx, issues.RecordDecisionParams{Title: "Protocol v47 is authoritative", Rationale: "Sibling integration selected v47"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AddDecisionLink(ctx, issues.AddDecisionLinkParams{DecisionID: decision.LocalID, TargetKind: issues.DecisionTargetIssue, TargetID: first, Relation: issues.DecisionRelationGoverns}); err != nil {
		t.Fatal(err)
	}
	if err := d.propagateDecisionChange(ctx, "project", decision.LocalID, protocol.CommandDecisionLinkAdd); err != nil {
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
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: mustRootedDecisionScope(t, root), ActorID: "orchestrator", RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PendingDecisions[second]) != 1 || !strings.Contains(snapshot.Blocked[second], "acknowledged") {
		t.Fatalf("snapshot pending=%+v blocked=%+v", snapshot.PendingDecisions, snapshot.Blocked)
	}

	service := issueDecisionService{daemon: d}
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
	if _, err := client.UpdateDecision(ctx, decision.LocalID, issues.UpdateDecisionParams{Rationale: &newRationale}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.propagateDecisionChange(ctx, "project", decision.LocalID, protocol.CommandDecisionUpdate); err != nil {
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
