package issues

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestListProjectIssueObservationEventsProvidesDurableCursor(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	writer := newTestClientAtPath(t, path, slog.Default())
	reader := newTestClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = writer.CloseDB(); _ = reader.CloseDB() })
	first, err := writer.Create(ctx, CreateTaskParams{Title: "first", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	second, err := writer.Create(ctx, CreateTaskParams{Title: "second", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	events, err := reader.ListProjectIssueObservationEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0].IssueID.String() != first || events[len(events)-1].IssueID.String() != second {
		t.Fatalf("events = %+v, want project ordered creation events", events)
	}
	cursor := events[0].ID
	after, err := reader.ListProjectIssueObservationEvents(ctx, cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(events)-1 || after[0].ID <= cursor {
		t.Fatalf("after cursor %d = %+v", cursor, after)
	}
}

func TestBindTaskIntegrationPublicationOperationConvergesAcrossClients(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	clients := []*Client{newTestClientAtPath(t, path, slog.Default()), newTestClientAtPath(t, path, slog.Default())}
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.CloseDB()
		}
	})
	issueID, err := clients[0].Create(ctx, CreateTaskParams{Title: "publication recovery", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	binding := TaskIntegrationPublicationBinding{
		ProjectID: "project", SourceBranch: "feature", TargetBranch: "main", TargetID: "base",
		BaseOID: "base-a", SourceOID: "source-a", TargetOID: "result-a", PublicationOperationID: "publication-a",
	}
	if _, err := clients[0].AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close",
		Payload: map[string]any{
			"project_id": binding.ProjectID, "source_branch": binding.SourceBranch, "target_branch": binding.TargetBranch,
			"target_id": binding.TargetID, "configured_base_target": true, "integrated": true,
			"base_oid": binding.BaseOID, "source_oid": binding.SourceOID, "target_oid": binding.TargetOID,
			"publication_operation_id": "",
		},
	}); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, len(clients))
	start := make(chan struct{})
	errs := make(chan error, len(clients))
	var appended atomic.Int32
	for _, client := range clients {
		client := client
		go func() {
			ready <- struct{}{}
			<-start
			created, bindErr := client.BindTaskIntegrationPublicationOperation(ctx, issueID, binding)
			if created {
				appended.Add(1)
			}
			errs <- bindErr
		}()
	}
	for range clients {
		<-ready
	}
	close(start)
	for range clients {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := appended.Load(); got != 1 {
		t.Fatalf("corrected receipt appends = %d, want 1", got)
	}
	events, err := clients[0].ListIssueObservationEvents(ctx, issueID, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Payload["publication_operation_id"] != binding.PublicationOperationID {
		t.Fatalf("integration receipts = %+v, want original plus exact corrected binding", events)
	}
	mismatch := binding
	mismatch.TargetOID = "result-other"
	if _, err := clients[0].BindTaskIntegrationPublicationOperation(ctx, issueID, mismatch); err == nil {
		t.Fatal("exact revision mismatch unexpectedly rebound integration receipt")
	}
	mixedPayload := map[string]any{
		"project_id": binding.ProjectID, "source_branch": binding.SourceBranch, "target_branch": binding.TargetBranch,
		"target_id": binding.TargetID, "configured_base_target": true, "integrated": true,
		"base_oid": binding.BaseOID, "source_oid": binding.SourceOID, "target_oid": binding.TargetOID,
		"publication_operation_id": binding.PublicationOperationID, "historical_recovery_binding_id": "historical-a",
	}
	if _, err := clients[0].AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close", Payload: mixedPayload}); err != nil {
		t.Fatal(err)
	}
	if _, err := clients[0].BindTaskIntegrationPublicationOperation(ctx, issueID, binding); err == nil || !strings.Contains(err.Error(), "mixed modern and historical authority") {
		t.Fatalf("mixed authority error = %v, want rejection", err)
	}
}

func TestBindTaskIntegrationHistoricalRecoveryOriginalReceiptRejectsCompetingAuthorizationAcrossClients(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	clients := []*Client{newTestClientAtPath(t, path, slog.Default()), newTestClientAtPath(t, path, slog.Default())}
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.CloseDB()
		}
	})
	issueID, err := clients[0].Create(ctx, CreateTaskParams{Title: "historical recovery", Type: domain.TypeBug, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := clients[0].AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventHistoricalValidationCompleted, Source: "agent", SourceCommand: "az issue record", Payload: map[string]any{"base_revision": "base-a", "candidate_revision": "result-a", "result": "clean"}})
	if err != nil {
		t.Fatal(err)
	}
	review, err := clients[0].AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventHistoricalReviewAccepted, Source: "agent", SourceCommand: "az issue record", Payload: map[string]any{"base_revision": "base-a", "candidate_revision": "result-a", "review_result": "accepted"}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := clients[0].AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close", Payload: map[string]any{"project_id": "project", "source_branch": "feature", "target_branch": "main", "target_id": "base", "configured_base_target": true, "integrated": true, "base_oid": "base-a", "source_oid": "source-a", "target_oid": "result-a", "publication_operation_id": ""}})
	if err != nil {
		t.Fatal(err)
	}
	baseAuthorization := domain.HistoricalPublicationAuthorization{ValidationEventID: validation.ID, ReviewEventID: review.ID, ReceiptEventID: receipt.ID, ReviewerID: "reviewer-a", AuthoritativeEvidenceID: "evidence-a", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate, Execution: domain.ValidationExecutionExecuted, Override: domain.ValidationOverrideNone, EvidencePresent: true, AttestsMissingLegacySemantics: true}
	bindings := []TaskIntegrationHistoricalBinding{
		{ProjectID: "project", SourceBranch: "feature", TargetBranch: "main", TargetID: "base", BaseOID: "base-a", SourceOID: "source-a", TargetOID: "result-a", BindingID: "binding-a", Authorization: baseAuthorization},
		{ProjectID: "project", SourceBranch: "feature", TargetBranch: "main", TargetID: "base", BaseOID: "base-a", SourceOID: "source-a", TargetOID: "result-a", BindingID: "binding-b", Authorization: baseAuthorization},
	}
	bindings[1].Authorization.ReviewerID = "reviewer-b"
	bindings[1].Authorization.AuthoritativeEvidenceID = "evidence-b"
	ready := make(chan struct{}, len(clients))
	start := make(chan struct{})
	results := make(chan error, len(clients))
	for i, client := range clients {
		i, client := i, client
		go func() {
			ready <- struct{}{}
			<-start
			_, bindErr := client.BindTaskIntegrationHistoricalRecovery(ctx, issueID, bindings[i])
			results <- bindErr
		}()
	}
	for range clients {
		<-ready
	}
	close(start)
	var succeeded, rejected int
	for range clients {
		if bindErr := <-results; bindErr == nil {
			succeeded++
		} else if strings.Contains(bindErr.Error(), "competing historical authorization") {
			rejected++
		} else {
			t.Fatalf("unexpected bind error: %v", bindErr)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("results succeeded=%d rejected=%d, want one authoritative winner and one competing rejection", succeeded, rejected)
	}
	authorizations, err := clients[0].ListIssueObservationEvents(ctx, issueID, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationHistoricalAuthorized}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := clients[0].ListIssueObservationEvents(ctx, issueID, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizations) != 1 || len(receipts) != 2 {
		t.Fatalf("authorizations=%d receipts=%d, want one authorization and original plus one correction", len(authorizations), len(receipts))
	}
}

func TestListIssueObservationEventsForIssuesBoundsEachIssueInOneResult(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	ids := make([]string, 0, 2)
	for _, title := range []string{"first", "second"} {
		issueID, err := client.Create(ctx, CreateTaskParams{Title: title, Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, issueID)
		for sequence := 1; sequence <= 3; sequence++ {
			if _, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueObservationEventType("worker.progress"), Source: "test", Payload: map[string]any{"sequence": sequence}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	events, err := client.ListIssueObservationEventsForIssues(ctx, ids, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, issueID := range ids {
		if len(events[issueID]) != 2 || events[issueID][0].Payload["sequence"] != float64(3) || events[issueID][1].Payload["sequence"] != float64(2) {
			t.Fatalf("events[%s] = %+v, want newest two", issueID, events[issueID])
		}
	}
	manyIDs := make([]string, 1200)
	for i := range manyIDs {
		manyIDs[i] = fmt.Sprintf("missing-%d", i)
	}
	if _, err := client.ListIssueObservationEventsForIssues(ctx, manyIDs, 2); err != nil {
		t.Fatalf("large batch must remain one JSON-parameter query: %v", err)
	}
}

func TestGetProjectIssueObservationEventsByIDsUsesSparseExactPositions(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "sparse", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventProgressRecorded, Source: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sqlite_sequence SET seq=10000 WHERE name='issue_observation_events'`); err != nil {
		t.Fatal(err)
	}
	last, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventProgressRecorded, Source: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.GetProjectIssueObservationEventsByIDs(ctx, []int64{last.ID, first.ID, last.ID, -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != first.ID || events[1].ID != last.ID || last.ID-first.ID <= 5000 {
		t.Fatalf("sparse exact events = %+v, first=%d last=%d", events, first.ID, last.ID)
	}
}

func TestListLatestIssueObservationEventsByIssueScopesAndRanksCandidates(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	first, err := client.Create(ctx, CreateTaskParams{Title: "first", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Create(ctx, CreateTaskParams{Title: "second", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []IssueObservationEventParams{
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}},
		{Type: domain.IssueEventReviewCompleted, Source: " daemon-orchestration ", SourceCommand: " review-return ", Payload: map[string]any{"actor_id": "reviewer", "outcome": "returned"}},
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-return", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}},
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-return", Payload: map[string]any{"actor_id": 42, "outcome": "returned"}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, first, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.AppendIssueObservationEvent(ctx, second, IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}}); err != nil {
		t.Fatal(err)
	}
	events, err := client.ListLatestIssueObservationEventsByIssue(ctx, LatestIssueObservationEventOptions{
		IssueIDs:       []string{first},
		Type:           domain.IssueEventReviewCompleted,
		Source:         "daemon-orchestration",
		SourceCommands: []string{"review-accept", "review-return"},
		CommandOutcomePairs: []IssueObservationCommandOutcomePair{
			{SourceCommand: "review-accept", Outcomes: []string{"accepted", "integration_failed"}},
			{SourceCommand: "review-return", Outcomes: []string{"returned"}},
		},
		RequiredPayloadTextKeys: []string{"actor_id"},
		CurrentReviewEpoch:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[first].Payload["outcome"] != "returned" {
		t.Fatalf("latest scoped events = %+v, want newest valid first-issue return only", events)
	}
	events, err = client.ListLatestIssueObservationEventsByIssue(ctx, LatestIssueObservationEventOptions{
		IssueIDs:          []string{first},
		Type:              domain.IssueEventReviewCompleted,
		PayloadTextEquals: map[string]string{"actor_id": "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[first].Payload["outcome"] != "accepted" {
		t.Fatalf("latest payload-matched event = %+v, want newest reviewer event before newer nonmatching payload", events)
	}
	if _, err := client.ListLatestIssueObservationEventsByIssue(ctx, LatestIssueObservationEventOptions{
		IssueIDs: []string{first}, Type: domain.IssueEventReviewCompleted,
		PayloadTextEquals: map[string]string{"actor_id": "reviewer", " actor_id ": "other"},
	}); err == nil || !strings.Contains(err.Error(), "conflicting payload text equality filters") {
		t.Fatalf("conflicting normalized payload filters error = %v", err)
	}
}

func TestListLatestIssueObservationEventsByIssueStopsAtCurrentReviewEpoch(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "review", Description: "scope", Acceptance: "done", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}}); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusOpen); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	events, err := client.ListLatestIssueObservationEventsByIssue(ctx, LatestIssueObservationEventOptions{
		IssueIDs: []string{issueID}, Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration",
		CommandOutcomePairs:     []IssueObservationCommandOutcomePair{{SourceCommand: "review-accept", Outcomes: []string{"accepted"}}},
		RequiredPayloadTextKeys: []string{"actor_id"}, CurrentReviewEpoch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("current epoch outcomes = %+v, want none", events)
	}
}

func TestListLatestIssueObservationEventsByIssueInvalidatesCompletionAtTrustedLifecycleTransition(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "integrated", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close",
		Payload: map[string]any{"project_id": "project", "source_branch": "issue", "target_branch": "main", "source_oid": "source", "target_oid": "target"},
	}); err != nil {
		t.Fatal(err)
	}
	opts := LatestIssueObservationEventOptions{
		IssueIDs: []string{issueID}, Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close",
		SourceCommands:          []string{"integrate-before-close"},
		RequiredPayloadTextKeys: []string{"project_id", "source_branch", "target_branch", "source_oid", "target_oid"},
		InvalidatedByStatuses:   []domain.Status{domain.StatusOpen, domain.StatusInProgress},
	}
	events, err := client.ListLatestIssueObservationEventsByIssue(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("completion events = %+v, want receipt before any later lifecycle transition", events)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type: domain.IssueEventIssueStatusChanged, Source: "agent", Payload: map[string]any{"to_status": string(domain.StatusInProgress)},
	}); err != nil {
		t.Fatal(err)
	}
	events, err = client.ListLatestIssueObservationEventsByIssue(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("completion events after untrusted status claim = %+v, want receipt retained", events)
	}
	if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	events, err = client.ListLatestIssueObservationEventsByIssue(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("completion events after trusted new-work transition = %+v, want receipt revoked", events)
	}
}

func TestInvestigationAcceptancesScopeAuthorityToCurrentReviewEpoch(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	humanID, err := client.Create(ctx, CreateTaskParams{Title: "human findings", Type: domain.TypeInvestigation, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	internalID, err := client.Create(ctx, CreateTaskParams{Title: "internal review", Type: domain.TypeInvestigation, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, humanID, IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Source: "human", Payload: map[string]any{"investigation_findings_accepted": true}}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []IssueObservationEventParams{
		{Type: domain.IssueEventInvestigationDisposition, Source: "agent", Payload: map[string]any{"disposition": "internal_review"}},
		{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}},
	} {
		if _, err := client.AppendIssueObservationEvent(ctx, internalID, event); err != nil {
			t.Fatal(err)
		}
	}
	for _, issueID := range []string{humanID, internalID} {
		if err := client.Update(ctx, issueID, domain.StatusOpen); err != nil {
			t.Fatal(err)
		}
		if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
			t.Fatal(err)
		}
	}
	tasks := make([]domain.Task, 0, 2)
	for _, issueID := range []string{humanID, internalID} {
		task, err := client.GetWithRuntime(ctx, "project", issueID)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	acceptances, err := client.InvestigationAcceptances(ctx, tasks)
	if err != nil {
		t.Fatal(err)
	}
	if acceptances[humanID].Accepted || acceptances[internalID].Accepted {
		t.Fatalf("stale acceptances = %+v, want both rejected after new epoch", acceptances)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, humanID, IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Source: "human", Payload: map[string]any{"investigation_findings_accepted": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, internalID, IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}}); err != nil {
		t.Fatal(err)
	}
	acceptances, err = client.InvestigationAcceptances(ctx, tasks)
	if err != nil {
		t.Fatal(err)
	}
	if !acceptances[humanID].Accepted || !acceptances[internalID].Accepted {
		t.Fatalf("same-epoch acceptances = %+v, want both accepted", acceptances)
	}
}
