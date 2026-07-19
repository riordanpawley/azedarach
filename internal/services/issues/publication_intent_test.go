package issues

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/domain"
	_ "modernc.org/sqlite"
)

func TestAcceptedReviewAndPublicationRejectsReplacedEpochBeforeAnySideEffect(t *testing.T) {
	for _, rooted := range []bool{false, true} {
		name := "project"
		if rooted {
			name = "rooted-child"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo := t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
				t.Fatal(err)
			}
			client := NewClient(repo, nil)
			t.Cleanup(func() { _ = client.CloseDB() })
			queueStore := operationstore.New(repo, nil)
			t.Cleanup(func() { _ = queueStore.Close() })
			if _, err := queueStore.PublicationOperations(ctx, "project", "", false); err != nil {
				t.Fatal(err)
			}
			issueID, err := client.Create(ctx, CreateTaskParams{Title: "publish", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview})
			if err != nil {
				t.Fatal(err)
			}
			parentID := ""
			if rooted {
				parentID, err = client.Create(ctx, CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
				if err != nil {
					t.Fatal(err)
				}
				if err := client.AddDependency(ctx, issueID, parentID, string(domain.DependencyParentChild)); err != nil {
					t.Fatal(err)
				}
			}
			stale, err := client.CaptureReviewAdmissionPin(ctx, issueID)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
				t.Fatal(err)
			}
			if err := client.Update(ctx, issueID, domain.StatusInReview); err != nil {
				t.Fatal(err)
			}
			current, err := client.CaptureReviewAdmissionPin(ctx, issueID)
			if err != nil {
				t.Fatal(err)
			}
			if current.ReviewEpochEventID == stale.ReviewEpochEventID {
				t.Fatalf("review epoch remained %d, want replacement", stale.ReviewEpochEventID)
			}
			if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{
				OwnerID: "reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview,
				ExpectedReviewAdmission: &current, ExpectedParentIssueID: parentID, ReviewSourceOID: "source",
			}); err != nil {
				t.Fatal(err)
			}
			operation := domain.PublicationOperation{
				OperationID: "publication-replaced-" + name, ProjectID: "project", IssueID: issueID, IntentKey: "accept-replaced",
				RequestFingerprint: "fingerprint", ActorID: "reviewer", TargetID: "base", TargetBranch: "main",
				SourceRevision: "source", BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain",
				ValidationCommand: "npm test", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC(),
			}
			params := IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
				"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint,
			}}
			if _, err := client.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, params, operation, "candidate-"+name, stale, parentID, "reviewer"); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("replaced review epoch error = %v, want conflict", err)
			}
			events, err := client.ListIssueObservationEvents(ctx, issueID, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
			if err != nil || len(events) != 0 {
				t.Fatalf("review side effects = (%+v,%v), want none", events, err)
			}
			operations, err := queueStore.PublicationOperations(ctx, "project", issueID, false)
			if err != nil || len(operations) != 0 {
				t.Fatalf("publication side effects = (%+v,%v), want none", operations, err)
			}
		})
	}
}

func TestAcceptedReviewAndPublicationReturnsCommittedReceiptAfterCallerCancellation(t *testing.T) {
	baseCtx := context.Background()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(repo, nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	queueStore := operationstore.New(repo, nil)
	t.Cleanup(func() { _ = queueStore.Close() })
	if _, err := queueStore.PublicationOperations(baseCtx, "project", "", false); err != nil {
		t.Fatal(err)
	}
	issueID, err := client.Create(baseCtx, CreateTaskParams{Title: "publish", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	parentID, err := client.Create(baseCtx, CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddDependency(baseCtx, issueID, parentID, string(domain.DependencyParentChild)); err != nil {
		t.Fatal(err)
	}
	admission, err := client.CaptureReviewAdmissionPin(baseCtx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(baseCtx, "project", issueID, OwnershipClaimParams{
		OwnerID: "reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview,
		ExpectedReviewAdmission: &admission, ExpectedParentIssueID: parentID, ReviewSourceOID: "source",
	}); err != nil {
		t.Fatal(err)
	}
	operation := domain.PublicationOperation{
		OperationID: "publication-cancelled-receipt", ProjectID: "project", IssueID: issueID, IntentKey: "accept-cancelled-receipt",
		RequestFingerprint: "fingerprint", ActorID: "reviewer", TargetID: "base", TargetBranch: "main",
		SourceRevision: "source", BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain",
		ValidationCommand: "npm test", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC(),
	}
	params := IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
		"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": "reviewer", "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint,
	}}
	ctx, cancel := context.WithCancel(baseCtx)
	ctx = WithAcceptedReviewPublicationCommitHookForTest(ctx, cancel)
	receipt, err := client.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, params, operation, "candidate", admission, parentID, "reviewer")
	if err != nil || receipt.EventID == 0 || receipt.PublicationOperationID != operation.OperationID {
		t.Fatalf("committed receipt after cancellation = (%+v,%v)", receipt, err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("caller context = %v, want canceled at commit boundary", ctx.Err())
	}
	events, err := client.ListIssueObservationEvents(baseCtx, issueID, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil || len(events) != 1 || events[0].ID != receipt.EventID {
		t.Fatalf("durable accepted event = (%+v,%v), want receipt event %d", events, err, receipt.EventID)
	}
	queued, found, err := queueStore.PublicationOperation(baseCtx, receipt.PublicationOperationID)
	if err != nil || !found || queued.State != domain.PublicationOperationQueued {
		t.Fatalf("durable queue after cancellation = (%+v,%t,%v)", queued, found, err)
	}
	parentMutationCtx := WithParentChildOrphanConfirmation(WithDependencyRemovalConfirmation(baseCtx))
	if err := client.RemoveDependency(parentMutationCtx, issueID, parentID, string(domain.DependencyParentChild)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("parent mutation after committed cancellation error = %v, want conflict", err)
	}
	if err := client.Update(baseCtx, issueID, domain.StatusInProgress); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("epoch mutation after committed cancellation error = %v, want conflict", err)
	}
	// Before the receipt API this caller-context cancellation made the
	// post-commit event fetch fail, falsely reporting the durable transaction as
	// rejected. Returning transaction-derived IDs locks out that ambiguity.
}

func TestAcceptedReviewPublicationFencePreservesReviewerIdentity(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(repo, nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	queueStore := operationstore.New(repo, nil)
	t.Cleanup(func() { _ = queueStore.Close() })
	if _, err := queueStore.PublicationOperations(ctx, "project", "", false); err != nil {
		t.Fatal(err)
	}
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "publish", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: map[string]any{
			"schema": "worker_evidence.v1", "summary": "reviewed", "commands_run": []any{"go test ./..."},
			"key_assertions": []any{"publication is exact"}, "files_changed": []any{"consumer.go"},
			"review": map[string]any{"status": "clean", "findings": []any{"none"}}, "risks": []any{"none"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := client.CaptureReviewAdmissionPin(ctx, issueID)
	if err != nil || admission.Evidence == nil || admission.Evidence.EventID != evidence.ID {
		t.Fatalf("review admission = %+v err=%v", admission, err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{
		OwnerID: "reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview,
		ExpectedReviewAdmission: &admission, ReviewSourceOID: "source",
	}); err != nil {
		t.Fatal(err)
	}
	operation := domain.PublicationOperation{
		OperationID: "publication-reviewer-fence", ProjectID: "project", IssueID: issueID, IntentKey: "accept-reviewer-fence",
		RequestFingerprint: "fingerprint", ActorID: "reviewer", TargetID: "base", TargetBranch: "main",
		SourceRevision: "source", BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain",
		ValidationCommand: "make verify", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC(),
	}
	params := IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
		"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": "reviewer", "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint,
		"reviewed_evidence_source": admission.Evidence.Source, "reviewed_evidence_event_id": admission.Evidence.EventID,
		"reviewed_evidence_seq": admission.Evidence.Seq, "reviewed_evidence_digest": admission.Evidence.Digest,
	}}
	if _, err := client.BeginReviewEvidenceClose(ctx, issueID, *admission.Evidence, "reviewer"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("pre-accept reviewer fence error = %v, want conflict", err)
	}
	if _, err := client.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, params, operation, "candidate", admission, "", "reviewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.BeginReviewEvidenceClose(ctx, issueID, *admission.Evidence, "other-reviewer"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mismatched accepted reviewer error = %v, want conflict", err)
	}
	if _, err := client.BeginReviewEvidenceClose(ctx, issueID, *admission.Evidence, "reviewer"); err != nil {
		t.Fatal(err)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	var reviewLease *domain.CoordinationLease
	for i := range task.CoordinationLeases {
		if task.CoordinationLeases[i].Purpose == domain.CoordinationLeaseReview {
			reviewLease = &task.CoordinationLeases[i]
			break
		}
	}
	if reviewLease == nil || reviewLease.OwnerID != "reviewer" {
		t.Fatalf("accepted publication review lease = %+v, want authoritative reviewer identity", reviewLease)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE issue_coordination_leases SET owner_id=?,owner_kind=? WHERE issue_id=? AND purpose=?`,
		reviewEvidenceCloseFenceToken(*admission.Evidence), legacyReviewEvidenceCloseFenceOwnerKind, issueID, domain.CoordinationLeaseReview); err != nil {
		t.Fatal(err)
	}
	if _, err := client.BeginReviewEvidenceClose(ctx, issueID, *admission.Evidence, "reviewer"); err != nil {
		t.Fatalf("recover legacy synthetic fence: %v", err)
	}
	task, err = client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	reviewLease = nil
	for i := range task.CoordinationLeases {
		if task.CoordinationLeases[i].Purpose == domain.CoordinationLeaseReview {
			reviewLease = &task.CoordinationLeases[i]
			break
		}
	}
	if reviewLease == nil || reviewLease.OwnerID != "reviewer" || reviewLease.OwnerKind != "orchestrator" {
		t.Fatalf("recovered legacy publication review lease = %+v, want authoritative reviewer identity", reviewLease)
	}
}

func TestCloseWithRuntimeReviewLeaseRequiresTrustedAcceptedActor(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(repo, nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "internal review", Type: domain.TypeInvestigation, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := client.CaptureReviewAdmissionPin(ctx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{
		OwnerID: "reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview, ExpectedReviewAdmission: &admission,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CloseWithRuntimeReviewLease(ctx, "project", issueID, domain.StatusDone, "reviewer"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("close without accepted outcome error = %v, want conflict", err)
	}
	for _, actor := range []string{"other-reviewer", "reviewer"} {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
			Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept",
			Payload: map[string]any{"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": actor},
		}); err != nil {
			t.Fatal(err)
		}
		closed, closeErr := client.CloseWithRuntimeReviewLease(ctx, "project", issueID, domain.StatusDone, "reviewer")
		if actor == "other-reviewer" {
			if !errors.Is(closeErr, domain.ErrConflict) {
				t.Fatalf("mismatched accepted actor error = %v, want conflict", closeErr)
			}
			continue
		}
		if closeErr != nil || closed.Status != domain.StatusDone {
			t.Fatalf("matching accepted reviewer close = (%+v,%v)", closed, closeErr)
		}
	}
}

func TestAcceptedReviewAndPublicationIntentCommitAtomically(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(repo, nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	queueStore := operationstore.New(repo, nil)
	t.Cleanup(func() { _ = queueStore.Close() })
	// Opening the queue projection applies its independently versioned schema.
	if _, err := queueStore.PublicationOperations(ctx, "project", "", false); err != nil {
		t.Fatal(err)
	}
	task, err := client.Create(ctx, CreateTaskParams{Title: "publish", Description: "publish", Acceptance: "merged", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	operation := domain.PublicationOperation{
		OperationID: "publication-atomic", ProjectID: "project", IssueID: task, IntentKey: "accept-1",
		RequestFingerprint: "fingerprint", ActorID: "reviewer", TargetID: "base", TargetBranch: "main",
		SourceRevision: "source", BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain",
		ValidationCommand: "npm test", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC(),
	}
	params := IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
		"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint,
	}}
	db, err := sql.Open("sqlite", filepath.Join(repo, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER reject_publication_intent BEFORE INSERT ON daemon_publication_operations BEGIN SELECT RAISE(ABORT,'injected publication failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := client.appendAcceptedReviewAndPublication(ctx, task, params, operation, "candidate", nil, "", ""); err == nil {
		t.Fatal("injected queue failure committed accepted review")
	}
	events, err := client.ListIssueObservationEvents(ctx, task, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil || len(events) != 0 {
		t.Fatalf("rolled back review events = (%+v,%v)", events, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_publication_intent`); err != nil {
		t.Fatal(err)
	}
	if _, err := client.appendAcceptedReviewAndPublication(ctx, task, params, operation, "candidate", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	events, err = client.ListIssueObservationEvents(ctx, task, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil || len(events) != 1 {
		t.Fatalf("committed review events = (%+v,%v)", events, err)
	}
	queued, found, err := queueStore.PublicationOperation(ctx, operation.OperationID)
	if err != nil || !found || queued.State != domain.PublicationOperationQueued {
		t.Fatalf("committed publication = (%+v,%t,%v)", queued, found, err)
	}
}

func TestAcceptedReviewAndPublicationCoalescesCanonicalOperation(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(repo, nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	queueStore := operationstore.New(repo, nil)
	t.Cleanup(func() { _ = queueStore.Close() })
	if _, err := queueStore.PublicationOperations(ctx, "project", "", false); err != nil {
		t.Fatal(err)
	}
	task, err := client.Create(ctx, CreateTaskParams{Title: "publish", Description: "publish", Acceptance: "merged", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	first := domain.PublicationOperation{
		OperationID: "publication-first", ProjectID: "project", IssueID: task, IntentKey: "accept-1",
		RequestFingerprint: "fingerprint", ActorID: "reviewer", TargetID: "base", TargetBranch: "main",
		SourceRevision: "source", BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain",
		ValidationCommand: "npm test", EvidenceDigest: "evidence", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC(),
	}
	params := func(operation domain.PublicationOperation) IssueObservationEventParams {
		return IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
			"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint,
		}}
	}
	firstReceipt, err := client.appendAcceptedReviewAndPublication(ctx, task, params(first), first, "candidate", nil, "", "")
	if err != nil || firstReceipt.PublicationOperationID != first.OperationID {
		t.Fatalf("first canonical publication = (%q,%v)", firstReceipt.PublicationOperationID, err)
	}
	second := first
	second.OperationID = "publication-second"
	second.IntentKey = "accept-2"
	receipt, err := client.appendAcceptedReviewAndPublication(ctx, task, params(second), second, "candidate", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PublicationOperationID != first.OperationID || receipt.EventID == 0 {
		t.Fatalf("coalesced publication receipt = %+v", receipt)
	}
	events, err := client.ListIssueObservationEvents(ctx, task, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil || len(events) != 2 || events[0].Payload["publication_operation_id"] != first.OperationID || events[1].Payload["publication_operation_id"] != first.OperationID {
		t.Fatalf("coalesced publication events = (%+v,%v)", events, err)
	}
	operations, err := queueStore.PublicationOperations(ctx, "project", task, false)
	if err != nil || len(operations) != 1 {
		t.Fatalf("coalesced operations = (%+v,%v)", operations, err)
	}
}

func TestAcceptedReviewAndPublicationConcurrentClientsCoalesceOneExecution(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	firstClient := NewClient(repo, nil)
	secondClient := NewClient(repo, nil)
	t.Cleanup(func() { _ = firstClient.CloseDB(); _ = secondClient.CloseDB() })
	queueStore := operationstore.New(repo, nil)
	t.Cleanup(func() { _ = queueStore.Close() })
	if _, err := queueStore.PublicationOperations(ctx, "project", "", false); err != nil {
		t.Fatal(err)
	}
	task, err := firstClient.Create(ctx, CreateTaskParams{Title: "publish", Description: "publish", Acceptance: "merged", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	operation := func(id, intent string) domain.PublicationOperation {
		return domain.PublicationOperation{
			OperationID: id, ProjectID: "project", IssueID: task, IntentKey: intent,
			RequestFingerprint: "fingerprint", ActorID: "reviewer", TargetID: "base", TargetBranch: "main",
			SourceRevision: "source", BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain",
			ValidationCommand: "npm test", EvidenceDigest: "evidence", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC(),
		}
	}
	type result struct {
		id  string
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for index, client := range []*Client{firstClient, secondClient} {
		op := operation([]string{"publication-concurrent-1", "publication-concurrent-2"}[index], []string{"accept-concurrent-1", "accept-concurrent-2"}[index])
		go func(client *Client, op domain.PublicationOperation) {
			<-start
			params := IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "test", Payload: map[string]any{
				"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": op.IntentKey, "request_fingerprint": op.RequestFingerprint,
			}}
			receipt, appendErr := client.appendAcceptedReviewAndPublication(ctx, task, params, op, "concurrent-candidate", nil, "", "")
			results <- result{id: receipt.PublicationOperationID, err: appendErr}
		}(client, op)
	}
	close(start)
	firstResult, secondResult := <-results, <-results
	if firstResult.err != nil || secondResult.err != nil || firstResult.id == "" || firstResult.id != secondResult.id {
		t.Fatalf("concurrent coalesce = (%+v,%+v)", firstResult, secondResult)
	}
	operations, err := queueStore.PublicationOperations(ctx, "project", task, false)
	if err != nil || len(operations) != 1 {
		t.Fatalf("concurrent operations = (%+v,%v)", operations, err)
	}
	events, err := firstClient.ListIssueObservationEvents(ctx, task, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil || len(events) != 2 {
		t.Fatalf("concurrent accepted review events = (%+v,%v)", events, err)
	}
}
