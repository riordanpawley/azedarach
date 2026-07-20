package store

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestPublicationEvidencePersistsAcrossRestartAndMultiStoreReads(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	storeA := NewAtPath(dbPath, slog.Default())
	storeB := NewAtPath(dbPath, slog.Default())
	defer storeA.Close()
	defer storeB.Close()

	evidence := storedPublicationEvidence()
	if _, err := storeA.RecordPublicationEvidence(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	first, err := storeB.PublicationEvidenceSnapshot(ctx, "project", "issue")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || len(first.Evidence) != 1 || first.Evidence[0].EvidenceID != evidence.EvidenceID {
		t.Fatalf("first snapshot = %+v", first)
	}
	invalidation := domain.PublicationEvidenceInvalidation{InvalidationID: "i-1", EvidenceID: evidence.EvidenceID, Reason: domain.PublicationInvalidPathOverlap, Details: "src/api.ts changed", CreatedAt: time.Unix(2, 0).UTC()}
	if _, err := storeB.RecordPublicationEvidenceInvalidation(ctx, invalidation); err != nil {
		t.Fatal(err)
	}
	second, err := storeA.PublicationEvidenceSnapshot(ctx, "project", "issue")
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || len(second.Invalidations) != 1 || second.Invalidations[0].Reason != domain.PublicationInvalidPathOverlap {
		t.Fatalf("second snapshot = %+v", second)
	}
	if err := storeA.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := NewAtPath(dbPath, slog.Default())
	defer restarted.Close()
	afterRestart, err := restarted.PublicationEvidenceSnapshot(ctx, "project", "issue")
	if err != nil {
		t.Fatal(err)
	}
	if afterRestart.Revision != 2 || len(afterRestart.Evidence) != 1 || len(afterRestart.Invalidations) != 1 {
		t.Fatalf("restart snapshot = %+v", afterRestart)
	}
}

func TestPublicationEvidenceWritesAreImmutableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	defer store.Close()
	evidence := storedPublicationEvidence()
	if _, err := store.RecordPublicationEvidence(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordPublicationEvidence(ctx, evidence); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	evidence.Coverage.Dependencies = []string{}
	evidence.Coverage.Surfaces = []string{}
	if _, err := store.RecordPublicationEvidence(ctx, evidence); err != nil {
		t.Fatalf("idempotent replay with canonical empty coverage: %v", err)
	}
	conflict := evidence
	conflict.Producer = "different-reviewer"
	if _, err := store.RecordPublicationEvidence(ctx, conflict); err == nil {
		t.Fatal("conflicting immutable evidence replay succeeded")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE daemon_publication_evidence SET producer='mutated' WHERE evidence_id=?`, evidence.EvidenceID); err == nil {
		t.Fatal("immutable evidence update succeeded")
	}
}

func TestPublicationEvidenceReuseRequiresMatchingStoredProof(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	defer store.Close()
	original := storedPublicationEvidence()
	if _, err := store.RecordPublicationEvidence(ctx, original); err != nil {
		t.Fatal(err)
	}
	reused := original
	reused.EvidenceID = "e-2"
	reused.BaseRevision = "base-b"
	reused.ReusedFromEvidenceID = original.EvidenceID
	reused.CreatedAt = time.Unix(2, 0).UTC()
	if _, err := store.RecordPublicationEvidence(ctx, reused); err != nil {
		t.Fatalf("matching patch reuse: %v", err)
	}
	mismatch := reused
	mismatch.EvidenceID = "e-3"
	mismatch.PatchDigest = "different"
	if _, err := store.RecordPublicationEvidence(ctx, mismatch); err == nil {
		t.Fatal("mismatched reusable proof recorded")
	}
	crossIssue := reused
	crossIssue.EvidenceID = "e-cross-issue"
	crossIssue.IssueID = "other-issue"
	if _, err := store.RecordPublicationEvidence(ctx, crossIssue); err == nil {
		t.Fatal("cross-issue reusable proof recorded")
	}
	narrowedCoverage := reused
	narrowedCoverage.EvidenceID = "e-narrowed"
	narrowedCoverage.Coverage.Paths = nil
	if _, err := store.RecordPublicationEvidence(ctx, narrowedCoverage); err == nil {
		t.Fatal("narrowed-coverage reusable proof recorded")
	}
}

func TestPublicationEvidenceLostResponseRetriesPreserveServerTimestamp(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	defer store.Close()
	evidence := storedPublicationEvidence()
	evidence.CreatedAt = time.Time{}
	first, err := store.RecordPublicationEvidence(ctx, evidence)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.RecordPublicationEvidence(ctx, evidence)
	if err != nil {
		t.Fatalf("lost-response evidence retry: %v", err)
	}
	if retried.CreatedAt != first.CreatedAt || retried.CreatedAt.IsZero() {
		t.Fatalf("retry timestamps first=%s retried=%s", first.CreatedAt, retried.CreatedAt)
	}
	invalidation := domain.PublicationEvidenceInvalidation{InvalidationID: "retry-i", EvidenceID: first.EvidenceID, Reason: domain.PublicationInvalidPathOverlap, Details: "same semantic invalidation"}
	firstInvalidation, err := store.RecordPublicationEvidenceInvalidation(ctx, invalidation)
	if err != nil {
		t.Fatal(err)
	}
	retriedInvalidation, err := store.RecordPublicationEvidenceInvalidation(ctx, invalidation)
	if err != nil {
		t.Fatalf("lost-response invalidation retry: %v", err)
	}
	if retriedInvalidation.CreatedAt != firstInvalidation.CreatedAt || retriedInvalidation.CreatedAt.IsZero() {
		t.Fatalf("invalidation retry timestamps first=%s retried=%s", firstInvalidation.CreatedAt, retriedInvalidation.CreatedAt)
	}
}

func storedPublicationEvidence() domain.PublicationEvidence {
	return domain.PublicationEvidence{
		EvidenceID: "e-1", ProjectID: "project", IssueID: "issue", Layer: domain.PublicationEvidencePatchReview,
		PatchDigest: "patch-a", SourceRevision: "source-a", BaseRevision: "base-a", Producer: "reviewer",
		PolicyVersion: "policy-v1", EnvironmentFingerprint: "env-a", Coverage: domain.PublicationEvidenceCoverage{Paths: []string{"src/api.ts"}},
		Cost: domain.PublicationEvidenceCost{WallMilliseconds: 10, Tokens: 20}, CreatedAt: time.Unix(1, 0).UTC(),
	}
}
