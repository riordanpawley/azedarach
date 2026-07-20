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

func acceptedPublicationTestEvidence(operation domain.PublicationOperation) domain.PublicationEvidence {
	return domain.PublicationEvidence{
		EvidenceID: operation.PatchEvidenceID, ProjectID: operation.ProjectID, IssueID: operation.IssueID,
		Layer: domain.PublicationEvidencePatchReview, PatchDigest: "patch-digest", SourceRevision: operation.SourceRevision,
		BaseRevision: operation.BaseRevision, Producer: "reviewer:" + operation.ActorID, PolicyVersion: operation.PolicyVersion,
		EnvironmentFingerprint: operation.EnvironmentFingerprint, CreatedAt: operation.CreatedAt,
	}
}

func TestTerminalizeAcceptedReviewPublicationAtomicallySupersedesExactEpoch(t *testing.T) {
	for _, state := range []domain.PublicationOperationState{domain.PublicationOperationFailed, domain.PublicationOperationConflicted, domain.PublicationOperationStale, domain.PublicationOperationCanceled} {
		t.Run(string(state), func(t *testing.T) {
			ctx, repo := context.Background(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
				t.Fatal(err)
			}
			client := NewClient(repo, nil)
			t.Cleanup(func() { _ = client.CloseDB() })
			reader := NewClient(repo, nil)
			t.Cleanup(func() { _ = reader.CloseDB() })
			if err := reader.OpenProjectionDeltaStore(); err != nil {
				t.Fatal(err)
			}
			queue := operationstore.New(repo, nil)
			t.Cleanup(func() { _ = queue.Close() })
			issueID, err := client.Create(ctx, CreateTaskParams{Title: "publish", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview})
			if err != nil {
				t.Fatal(err)
			}
			op := domain.PublicationOperation{OperationID: "publication-" + string(state), ProjectID: "project", IssueID: issueID, IntentKey: "intent", RequestFingerprint: "fingerprint", ActorID: "reviewer", ReviewerKind: "orchestrator", ReviewEpochEventID: 17, AcceptedReviewEventID: 19, PatchEvidenceID: "patch-evidence", TargetID: "base", TargetBranch: "main", SourceRevision: "source", BaseRevision: "base", ValidationCommand: "npm test", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC()}
			stored, _, err := queue.EnqueuePublication(ctx, op, "candidate-"+string(state))
			if err != nil {
				t.Fatal(err)
			}
			expired, acquired, err := queue.ClaimPublicationOperation(ctx, stored.OperationID, operationstore.PublicationOperationClaim{Owner: "old-daemon", Token: "expired-claim", Now: time.Now().UTC().Add(-2 * time.Minute), TTL: time.Minute})
			if err != nil || !acquired {
				t.Fatalf("expired claim=(%+v,%t,%v)", expired, acquired, err)
			}
			if _, err := client.TerminalizeAcceptedReviewPublication(ctx, TerminalReviewPublicationDisposition{Operation: expired, ExpectedClaimToken: "expired-claim", State: state, FinishedAt: time.Now().UTC().Add(-90 * time.Second)}); err == nil {
				t.Fatal("expired claim passed using stale failure timestamp")
			}
			claimed, acquired, err := queue.ClaimPublicationOperation(ctx, stored.OperationID, operationstore.PublicationOperationClaim{Owner: "daemon-a", Token: "claim", Now: time.Now().UTC(), TTL: time.Minute})
			if err != nil || !acquired {
				t.Fatalf("claim=(%+v,%t,%v)", claimed, acquired, err)
			}
			if _, err := client.TerminalizeAcceptedReviewPublication(ctx, TerminalReviewPublicationDisposition{Operation: claimed, ExpectedClaimToken: "wrong", State: state, FinishedAt: time.Now().UTC()}); err == nil {
				t.Fatal("stale daemon terminalized active operation")
			}
			for name, mutate := range map[string]func(*domain.PublicationOperation){
				"actor":           func(candidate *domain.PublicationOperation) { candidate.ActorID = "other-reviewer" },
				"reviewer-kind":   func(candidate *domain.PublicationOperation) { candidate.ReviewerKind = "agent" },
				"intent":          func(candidate *domain.PublicationOperation) { candidate.IntentKey = "other-intent" },
				"fingerprint":     func(candidate *domain.PublicationOperation) { candidate.RequestFingerprint = "other-fingerprint" },
				"epoch":           func(candidate *domain.PublicationOperation) { candidate.ReviewEpochEventID++ },
				"accepted-review": func(candidate *domain.PublicationOperation) { candidate.AcceptedReviewEventID++ },
				"patch-evidence":  func(candidate *domain.PublicationOperation) { candidate.PatchEvidenceID = "other-patch" },
			} {
				t.Run("rejects-"+name+"-mismatch", func(t *testing.T) {
					mismatched := claimed
					mutate(&mismatched)
					if _, err := client.TerminalizeAcceptedReviewPublication(ctx, TerminalReviewPublicationDisposition{Operation: mismatched, ExpectedClaimToken: "claim", State: state, FinishedAt: time.Now().UTC()}); err == nil {
						t.Fatal("mismatched immutable authority terminalized operation")
					}
				})
			}
			_, projectionHead, err := reader.ListProjectionDeltas(ctx, "default", 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := client.TerminalizeAcceptedReviewPublication(ctx, TerminalReviewPublicationDisposition{Operation: claimed, ExpectedClaimToken: "claim", State: state, FailureKind: "terminal", FailureDetail: "failed", FinishedAt: time.Now().UTC()})
			if err != nil {
				t.Fatal(err)
			}
			if terminal.State != state {
				t.Fatalf("state=%s want %s", terminal.State, state)
			}
			events, err := client.ListIssueObservationEvents(ctx, issueID, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
			if err != nil || len(events) != 1 {
				t.Fatalf("events=(%+v,%v)", events, err)
			}
			if got := events[0].Payload["publication_operation_id"]; got != op.OperationID {
				t.Fatalf("operation binding=%v", got)
			}
			if got := events[0].Payload["reviewer_kind"]; got != op.ReviewerKind {
				t.Fatalf("reviewer kind binding=%v", got)
			}
			if got := events[0].Payload["patch_evidence_id"]; got != op.PatchEvidenceID {
				t.Fatalf("patch evidence binding=%v", got)
			}
			deltas, nextHead, err := reader.WatchProjectionDeltas(ctx, "default", projectionHead, 1)
			if err != nil || len(deltas) != 1 || deltas[0].Kind != domain.ProjectionKindSourceAdvance || nextHead != projectionHead+1 {
				t.Fatalf("cross-client terminal observation advance=(%+v,%d,%v), want source advance at %d", deltas, nextHead, err, projectionHead+1)
			}
			if replay, err := client.TerminalizeAcceptedReviewPublication(ctx, TerminalReviewPublicationDisposition{Operation: claimed, ExpectedClaimToken: "wrong", State: state, FinishedAt: time.Now().UTC()}); err != nil || replay.State != state {
				t.Fatalf("idempotent exact replay=(%+v,%v)", replay, err)
			}
			events, _ = client.ListIssueObservationEvents(ctx, issueID, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
			if len(events) != 1 {
				t.Fatalf("duplicate disposition events=%d", len(events))
			}
		})
	}
}

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
			operation.ReviewerKind, operation.ReviewEpochEventID, operation.PatchEvidenceID = "orchestrator", stale.ReviewEpochEventID, "patch-replaced-"+name
			patchEvidence := acceptedPublicationTestEvidence(operation)
			params := IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
				"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint, "actor_id": operation.ActorID, "review_epoch_event_id": operation.ReviewEpochEventID,
			}}
			if _, err := client.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, params, operation, patchEvidence, "candidate-"+name, stale, parentID, "reviewer"); !errors.Is(err, domain.ErrConflict) {
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
	operation.ReviewerKind, operation.ReviewEpochEventID, operation.PatchEvidenceID = "orchestrator", admission.ReviewEpochEventID, "patch-cancelled-receipt"
	patchEvidence := acceptedPublicationTestEvidence(operation)
	params := IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", Payload: map[string]any{
		"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint, "actor_id": operation.ActorID, "review_epoch_event_id": operation.ReviewEpochEventID,
	}}
	ctx, cancel := context.WithCancel(baseCtx)
	ctx = WithAcceptedReviewPublicationCommitHookForTest(ctx, cancel)
	receipt, err := client.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, params, operation, patchEvidence, "candidate", admission, parentID, "reviewer")
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
	if err != nil || !found || queued.State != domain.PublicationOperationQueued || queued.AcceptedReviewEventID != receipt.EventID || queued.ReviewEpochEventID != admission.ReviewEpochEventID || queued.ReviewerKind != "orchestrator" || queued.PatchEvidenceID != patchEvidence.EvidenceID {
		t.Fatalf("durable queue after cancellation = (%+v,%t,%v)", queued, found, err)
	}
	evidenceSnapshot, err := queueStore.PublicationEvidenceSnapshot(baseCtx, "project", issueID)
	if err != nil || len(evidenceSnapshot.Evidence) != 1 || evidenceSnapshot.Evidence[0].EvidenceID != queued.PatchEvidenceID {
		t.Fatalf("atomic patch evidence after cancellation = (%+v,%v)", evidenceSnapshot, err)
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
	if _, err := client.appendAcceptedReviewAndPublication(ctx, task, params, operation, nil, "candidate", nil, "", ""); err == nil {
		t.Fatal("injected queue failure committed accepted review")
	}
	events, err := client.ListIssueObservationEvents(ctx, task, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil || len(events) != 0 {
		t.Fatalf("rolled back review events = (%+v,%v)", events, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_publication_intent`); err != nil {
		t.Fatal(err)
	}
	if _, err := client.appendAcceptedReviewAndPublication(ctx, task, params, operation, nil, "candidate", nil, "", ""); err != nil {
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
	firstReceipt, err := client.appendAcceptedReviewAndPublication(ctx, task, params(first), first, nil, "candidate", nil, "", "")
	if err != nil || firstReceipt.PublicationOperationID != first.OperationID {
		t.Fatalf("first canonical publication = (%q,%v)", firstReceipt.PublicationOperationID, err)
	}
	second := first
	second.OperationID = "publication-second"
	second.IntentKey = "accept-2"
	receipt, err := client.appendAcceptedReviewAndPublication(ctx, task, params(second), second, nil, "candidate", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PublicationOperationID != first.OperationID || receipt.EventID == 0 {
		t.Fatalf("coalesced publication receipt = %+v", receipt)
	}
	events, err := client.ListIssueObservationEvents(ctx, task, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil || len(events) != 1 || events[0].ID != receipt.EventID || events[0].Payload["publication_operation_id"] != first.OperationID {
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
		id      string
		eventID int64
		err     error
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
			receipt, appendErr := client.appendAcceptedReviewAndPublication(ctx, task, params, op, nil, "concurrent-candidate", nil, "", "")
			results <- result{id: receipt.PublicationOperationID, eventID: receipt.EventID, err: appendErr}
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
	if err != nil || len(events) != 1 || events[0].ID != firstResult.eventID || events[0].ID != secondResult.eventID {
		t.Fatalf("concurrent accepted review events = (%+v,%v)", events, err)
	}
}
