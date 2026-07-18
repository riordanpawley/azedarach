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
			if _, _, err := client.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, params, operation, "candidate-"+name, stale, parentID, "reviewer"); !errors.Is(err, domain.ErrConflict) {
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
	if _, _, err := client.appendAcceptedReviewAndPublication(ctx, task, params, operation, "candidate", nil, "", ""); err == nil {
		t.Fatal("injected queue failure committed accepted review")
	}
	events, err := client.ListIssueObservationEvents(ctx, task, IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil || len(events) != 0 {
		t.Fatalf("rolled back review events = (%+v,%v)", events, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_publication_intent`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.appendAcceptedReviewAndPublication(ctx, task, params, operation, "candidate", nil, "", ""); err != nil {
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
	if _, canonicalID, err := client.appendAcceptedReviewAndPublication(ctx, task, params(first), first, "candidate", nil, "", ""); err != nil || canonicalID != first.OperationID {
		t.Fatalf("first canonical publication = (%q,%v)", canonicalID, err)
	}
	second := first
	second.OperationID = "publication-second"
	second.IntentKey = "accept-2"
	event, canonicalID, err := client.appendAcceptedReviewAndPublication(ctx, task, params(second), second, "candidate", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if canonicalID != first.OperationID || event.Payload["publication_operation_id"] != first.OperationID {
		t.Fatalf("coalesced publication = (%q,%+v)", canonicalID, event.Payload)
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
			_, canonicalID, appendErr := client.appendAcceptedReviewAndPublication(ctx, task, params, op, "concurrent-candidate", nil, "", "")
			results <- result{id: canonicalID, err: appendErr}
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
