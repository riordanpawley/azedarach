package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/domain"
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

func TestProjectStartIntentCannotBypassActionableReview(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	reviewID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	openID, err := client.Create(ctx, issues.CreateTaskParams{Title: "new work", Description: "Executable", Acceptance: "done", Type: domain.TypeTask, Priority: domain.P0, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "start-blocked-by-review", ActorID: "orchestrator", IssueIDs: []string{openID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Skipped[openID]; got != "review-priority:"+reviewID {
		t.Fatalf("result = %+v, want review priority skip", result)
	}
	openTask, err := client.GetWithRuntime(ctx, "project", openID)
	if err != nil {
		t.Fatal(err)
	}
	if openTask.Ownership != nil || openTask.Status != domain.StatusOpen {
		t.Fatalf("open task mutated despite review priority = %+v", openTask)
	}
}

func TestProjectStartIntentRoutesPrematureWorkBeforePrioritizingReview(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	reviewID := createReviewTask(t, ctx, client, domain.P1, "orchestrator")
	openID, err := client.Create(ctx, issues.CreateTaskParams{Title: "new work", Description: "Executable", Acceptance: "done", Type: domain.TypeTask, Priority: domain.P0, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	prematureID, err := client.Create(ctx, issues.CreateTaskParams{Title: "thin work", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", prematureID, issues.OwnershipClaimParams{OwnerID: "orchestrator", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseExecution}); err != nil {
		t.Fatal(err)
	}
	d := newOrchestrationReviewTestDaemon(repoDir, client)

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentStart, IntentKey: "route-before-review", ActorID: "orchestrator", IssueIDs: []string{openID, prematureID}, RepoDir: repoDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Started) != 0 || result.Skipped[openID] != "review-priority:"+reviewID {
		t.Fatalf("result = %+v, want review to suppress runnable start", result)
	}
	if len(result.Routed) != 1 || result.Routed[0].IssueID != prematureID || result.Skipped[prematureID] != "candidate-routed-backlog" {
		t.Fatalf("result = %+v, want premature candidate routed before review return", result)
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
	tmuxRunner.sessions[issueID] = true
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	d.tmux = tmux.NewClient(tmuxRunner, slog.Default())

	result, err := d.orchestrationAuthority().Apply(ctx, "project", protocol.OrchestrationIntentRequest{
		Scope:     domain.ProjectOrchestrationScope(),
		Kind:      protocol.OrchestrationIntentReviewReturn,
		IntentKey: "review-return-1",
		ActorID:   "orchestrator",
		IssueIDs:  []string{issueID},
		RepoDir:   repoDir,
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
	if len(result.Returned) != 1 || result.Returned[0] != issueID || len(result.Failed) != 0 {
		t.Fatalf("return result = %+v", result)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Ownership == nil || task.Ownership.OwnerID != "worker-a" {
		t.Fatalf("execution ownership = %+v, want preserved", task.Ownership)
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
		Scope:     domain.ProjectOrchestrationScope(),
		Kind:      protocol.OrchestrationIntentReviewReturn,
		IntentKey: "review-return-1",
		ActorID:   "orchestrator",
		IssueIDs:  []string{issueID},
		RepoDir:   repoDir,
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

func TestReviewAcceptSameIntentRecoversAfterCloseFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
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
	d := newOrchestrationReviewTestDaemon(repoDir, client)
	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-owned-review", ActorID: "orchestrator", IssueIDs: []string{issueID}, RepoDir: repoDir}
	failed, err := d.orchestrationAuthority().Apply(ctx, "project", request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failed.Failed[issueID], "authoritative close") {
		t.Fatalf("first close = %+v, want ownership-backed close failure", failed)
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
	if _, err := client.ReleaseOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "worker"}); err != nil {
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
	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-review-batch", ActorID: "orchestrator", IssueIDs: ids, RepoDir: repoDir}
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

func newOrchestrationReviewTestDaemon(repoDir string, client *issues.Client) *Daemon {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Daemon{
		cfg:                   Config{RepoDir: repoDir, Logger: logger},
		hub:                   publish.NewHub(16, 8, logger),
		issueClientsByProject: map[string]*issues.Client{"project": client},
		revision:              map[string]uint64{},
	}
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
