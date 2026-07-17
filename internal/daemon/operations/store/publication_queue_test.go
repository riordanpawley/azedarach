package store

import (
	"context"
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
	preparing, err := store.UpdatePublicationOperation(ctx, stored.OperationID, PublicationOperationUpdate{
		ExpectedStates: []domain.PublicationOperationState{domain.PublicationOperationQueued},
		State:          domain.PublicationOperationPreparing, LeaseOwner: "daemon", StartedAt: &started,
	})
	if err != nil || preparing.State != domain.PublicationOperationPreparing || preparing.StartedAt == nil {
		t.Fatalf("preparing = (%+v,%v)", preparing, err)
	}
	if _, err := store.UpdatePublicationOperation(ctx, stored.OperationID, PublicationOperationUpdate{ExpectedStates: []domain.PublicationOperationState{domain.PublicationOperationQueued}, State: domain.PublicationOperationFailed}); err == nil {
		t.Fatal("stale state transition succeeded")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE daemon_publication_operations SET source_revision='mutated' WHERE operation_id=?`, stored.OperationID); err == nil {
		t.Fatal("immutable publication identity update succeeded")
	}
}

func testPublicationOperation(id, issueID, intent, source string, created time.Time) domain.PublicationOperation {
	return domain.PublicationOperation{
		OperationID: id, ProjectID: "project", IssueID: issueID, IntentKey: intent,
		RequestFingerprint: "fingerprint", ActorID: "reviewer", TargetID: "base", TargetBranch: "main",
		SourceRevision: source, BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain",
		ValidationCommand: "npm test",
		EvidenceSource:    "mailbox", EvidenceEventID: 1, EvidenceSeq: 2, EvidenceDigest: "digest",
		State: domain.PublicationOperationQueued, CreatedAt: created,
	}
}
