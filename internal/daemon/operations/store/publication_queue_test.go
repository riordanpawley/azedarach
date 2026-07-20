package store

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestPublicationQueuePersistsOrdersAndCoalescesExactCandidate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "project.db")
	firstStore := NewAtPath(path, nil)
	t.Cleanup(func() { _ = firstStore.Close() })
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	first := testPublicationOperation("publication-1", "issue-1", "intent-1", "source-1", now)
	stored, created, err := firstStore.EnqueuePublication(ctx, first, "candidate-1")
	if err != nil || !created || stored.QueuePosition != 1 {
		t.Fatalf("first enqueue = (%+v,%t,%v)", stored, created, err)
	}
	second := testPublicationOperation("publication-2", "issue-2", "intent-2", "source-2", now.Add(time.Second))
	storedSecond, created, err := firstStore.EnqueuePublication(ctx, second, "candidate-2")
	if err != nil || !created || storedSecond.QueuePosition != 2 {
		t.Fatalf("second enqueue = (%+v,%t,%v)", storedSecond, created, err)
	}

	replay := first
	replay.OperationID = "publication-replay"
	replay.IntentKey = "intent-replay"
	coalesced, created, err := firstStore.EnqueuePublication(ctx, replay, "candidate-1")
	if err != nil || created || coalesced.OperationID != first.OperationID {
		t.Fatalf("coalesced enqueue = (%+v,%t,%v)", coalesced, created, err)
	}

	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := NewAtPath(path, nil)
	t.Cleanup(func() { _ = restarted.Close() })
	queued, err := restarted.PublicationOperations(ctx, "project", "", true)
	if err != nil || len(queued) != 2 || queued[0].OperationID != "publication-1" || queued[1].QueuePosition != 2 {
		t.Fatalf("restarted queue = (%+v,%v)", queued, err)
	}
}

func TestPublicationQueueTransitionsAreCASAndIdentityIsImmutable(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	operation := testPublicationOperation("publication-1", "issue-1", "intent-1", "source-1", time.Now().UTC())
	stored, _, err := store.EnqueuePublication(ctx, operation, "candidate-1")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	preparing, acquired, err := store.ClaimPublicationOperation(ctx, stored.OperationID, PublicationOperationClaim{
		Owner: "daemon-1", Token: "claim-1", Now: started, TTL: time.Minute,
	})
	if !acquired {
		t.Fatal("publication claim was not acquired")
	}
	if err != nil || preparing.State != domain.PublicationOperationPreparing || preparing.StartedAt == nil {
		t.Fatalf("preparing = (%+v,%v)", preparing, err)
	}
	if _, err := store.UpdatePublicationOperation(ctx, stored.OperationID, PublicationOperationUpdate{ExpectedStates: []domain.PublicationOperationState{domain.PublicationOperationQueued}, ExpectedClaimToken: "claim-1", State: domain.PublicationOperationFailed, UpdatedAt: started}); err == nil {
		t.Fatal("stale state transition succeeded")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE daemon_publication_operations SET source_revision='mutated' WHERE operation_id=?`, stored.OperationID); err == nil {
		t.Fatal("immutable publication identity update succeeded")
	}
}

func TestPublicationQueueClaimFencesLiveAndExpiredAttempts(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	operation := testPublicationOperation("publication-claim", "issue", "intent", "source", now)
	stored, _, err := store.EnqueuePublication(ctx, operation, "candidate-claim")
	if err != nil {
		t.Fatal(err)
	}
	first, acquired, err := store.ClaimPublicationOperation(ctx, stored.OperationID, PublicationOperationClaim{Owner: "daemon-1", Token: "claim-1", Now: now, TTL: time.Minute})
	if err != nil || !acquired || first.ClaimToken != "claim-1" {
		t.Fatalf("first claim = (%+v,%t,%v)", first, acquired, err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("claim-1")) || bytes.Contains(encoded, []byte("claim_token")) {
		t.Fatalf("publication projection exposed fencing token: %s", encoded)
	}
	second, acquired, err := store.ClaimPublicationOperation(ctx, stored.OperationID, PublicationOperationClaim{Owner: "daemon-2", Token: "claim-2", Now: now.Add(30 * time.Second), TTL: time.Minute})
	if err != nil || acquired || second.ClaimToken != "claim-1" {
		t.Fatalf("live claim contention = (%+v,%t,%v)", second, acquired, err)
	}
	second, acquired, err = store.ClaimPublicationOperation(ctx, stored.OperationID, PublicationOperationClaim{Owner: "daemon-2", Token: "claim-2", Now: now.Add(time.Minute), TTL: time.Minute})
	if err != nil || !acquired || second.ClaimToken != "claim-2" {
		t.Fatalf("expired claim recovery = (%+v,%t,%v)", second, acquired, err)
	}
	if _, err := store.UpdatePublicationOperation(ctx, stored.OperationID, PublicationOperationUpdate{ExpectedStates: []domain.PublicationOperationState{second.State}, ExpectedClaimToken: "claim-1", State: domain.PublicationOperationMerged, ReleaseClaim: true, UpdatedAt: now.Add(time.Minute)}); err == nil {
		t.Fatal("expired owner finished after claim was reassigned")
	}
	merged, err := store.UpdatePublicationOperation(ctx, stored.OperationID, PublicationOperationUpdate{ExpectedStates: []domain.PublicationOperationState{second.State}, ExpectedClaimToken: "claim-2", State: domain.PublicationOperationMerged, ReleaseClaim: true, UpdatedAt: now.Add(time.Minute)})
	if err != nil || merged.State != domain.PublicationOperationMerged || merged.ClaimToken != "" || merged.ClaimExpiresAt != nil {
		t.Fatalf("claimed finish = (%+v,%v)", merged, err)
	}
}

func TestTerminalizePublicationWithSuccessorRollsBackPredecessorWhenInsertFails(t *testing.T) {
	store := NewAtPath(filepath.Join(t.TempDir(), "project.db"), nil)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now().UTC()
	predecessor := testPublicationOperation("publication-predecessor", "issue", "intent", "source", now)
	stored, _, err := store.EnqueuePublication(ctx, predecessor, "predecessor-key")
	if err != nil {
		t.Fatal(err)
	}
	claimed, acquired, err := store.ClaimPublicationOperation(ctx, stored.OperationID, PublicationOperationClaim{Owner: "daemon", Token: "claim", Now: now, TTL: time.Minute})
	if err != nil || !acquired {
		t.Fatalf("claim predecessor = (%+v,%t,%v)", claimed, acquired, err)
	}
	conflict := testPublicationOperation("publication-successor", "other-issue", "other-intent", "other-source", now.Add(time.Second))
	conflict.TargetBranch = "other-target"
	if _, _, err := store.EnqueuePublication(ctx, conflict, "conflict-key"); err != nil {
		t.Fatal(err)
	}
	successor := testPublicationOperation(conflict.OperationID, predecessor.IssueID, "intent:publication-retry:next", predecessor.SourceRevision, now.Add(2*time.Second))
	successor.BaseRevision = "base-next"
	finished := now.Add(3 * time.Second)
	_, _, err = store.TerminalizePublicationWithSuccessor(ctx, predecessor.OperationID, PublicationOperationUpdate{
		ExpectedStates: []domain.PublicationOperationState{claimed.State}, ExpectedClaimToken: "claim",
		State: domain.PublicationOperationStale, ReleaseClaim: true, FinishedAt: &finished, UpdatedAt: finished,
	}, successor, "successor-key")
	if err == nil {
		t.Fatal("conflicting successor insert unexpectedly committed")
	}
	after, found, readErr := store.PublicationOperation(ctx, predecessor.OperationID)
	if readErr != nil || !found || after.State != domain.PublicationOperationPreparing || after.ClaimToken != "claim" {
		t.Fatalf("predecessor after atomic rollback = (%+v,%t,%v)", after, found, readErr)
	}
}

func testPublicationOperation(id, issueID, intent, source string, created time.Time) domain.PublicationOperation {
	return domain.PublicationOperation{
		OperationID: id, ProjectID: "project", IssueID: issueID, IntentKey: intent,
		RequestFingerprint: "fingerprint", ActorID: "reviewer", ActorKind: domain.ReviewerOwnerKindOrchestrator,
		ReviewEpochEventID: 41, AcceptedReviewEventID: 42, AcceptedPublicationOperationID: id, TargetID: "base", TargetBranch: "main",
		SourceRevision: source, BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain",
		ValidationCommand: "npm test",
		EvidenceSource:    "mailbox", EvidenceEventID: 1, EvidenceSeq: 2, EvidenceDigest: "digest",
		State: domain.PublicationOperationQueued, CreatedAt: created,
	}
}
