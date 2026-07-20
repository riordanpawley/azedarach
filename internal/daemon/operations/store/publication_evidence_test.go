package store

import (
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
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

func TestPatchReviewEvidenceReplayRetainsFirstBaseAcrossConcurrentStores(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	storeA := NewAtPath(dbPath, slog.Default())
	defer storeA.Close()
	storeB := NewAtPath(dbPath, slog.Default())
	defer storeB.Close()
	original := storedPublicationEvidence()
	if _, err := storeA.RecordPublicationEvidence(ctx, original); err != nil {
		t.Fatal(err)
	}
	retry := original
	retry.BaseRevision = "advanced-base"
	retry.CreatedAt = original.CreatedAt.Add(time.Hour)
	got, err := storeB.RecordPublicationEvidence(ctx, retry)
	if err != nil {
		t.Fatalf("base-advanced retry: %v", err)
	}
	if got.BaseRevision != original.BaseRevision || !got.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("retry replaced first immutable record: got=%+v original=%+v", got, original)
	}
	for name, mutate := range map[string]func(*domain.PublicationEvidence){
		"review_epoch_identity": func(e *domain.PublicationEvidence) { e.EvidenceID = "different-review-epoch" },
		"source":                func(e *domain.PublicationEvidence) { e.SourceRevision = "different-source" },
		"reviewer":              func(e *domain.PublicationEvidence) { e.Producer = "reviewer:different" },
		"digest":                func(e *domain.PublicationEvidence) { e.PatchDigest = "different-digest" },
		"policy":                func(e *domain.PublicationEvidence) { e.PolicyVersion = "different-policy" },
		"environment":           func(e *domain.PublicationEvidence) { e.EnvironmentFingerprint = "different-environment" },
		"coverage":              func(e *domain.PublicationEvidence) { e.Coverage.Paths = []string{"different.go"} },
		"cost":                  func(e *domain.PublicationEvidence) { e.Cost.Tokens++ },
	} {
		t.Run(name, func(t *testing.T) {
			conflict := retry
			mutate(&conflict)
			if name == "review_epoch_identity" {
				// A different epoch has a different deterministic evidence ID and
				// therefore cannot reuse or overwrite the accepted proof.
				if _, err := storeB.RecordPublicationEvidence(ctx, conflict); err != nil {
					t.Fatalf("independent epoch record: %v", err)
				}
				return
			}
			if _, err := storeB.RecordPublicationEvidence(ctx, conflict); err == nil {
				t.Fatal("semantic identity mismatch replay succeeded")
			}
		})
	}
}

func TestAcceptedPatchReviewEvidenceConcurrentCrossBaseInsertRetainsOneProof(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	storeA := NewAtPath(dbPath, slog.Default())
	defer storeA.Close()
	storeB := NewAtPath(dbPath, slog.Default())
	defer storeB.Close()
	// Complete lazy open/migration sequentially so this test isolates evidence
	// insertion concurrency instead of racing database initialization.
	if _, err := storeA.dbHandle(); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.dbHandle(); err != nil {
		t.Fatal(err)
	}

	first := storedPublicationEvidence()
	second := first
	second.BaseRevision = "advanced-base"
	second.PatchDigest = "advanced-base-relative-digest"
	second.Coverage.Paths = []string{"advanced-base-relative.go"}
	second.CreatedAt = first.CreatedAt.Add(time.Hour)

	start := make(chan struct{})
	results := make(chan domain.PublicationEvidence, 2)
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, attempt := range []struct {
		store    *SQLiteStore
		evidence domain.PublicationEvidence
	}{{storeA, first}, {storeB, second}} {
		go func() {
			ready.Done()
			<-start
			got, err := attempt.store.RecordAcceptedPatchReviewEvidence(ctx, attempt.evidence)
			results <- got
			errs <- err
		}()
	}
	ready.Wait()
	close(start)

	gotA, gotB := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatalf("first concurrent record: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("second concurrent record: %v", err)
	}
	if !reflect.DeepEqual(gotA, gotB) {
		t.Fatalf("concurrent callers observed different authority: first=%+v second=%+v", gotA, gotB)
	}
	snapshot, err := storeA.PublicationEvidenceSnapshot(ctx, "project", "issue")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Evidence) != 1 || !reflect.DeepEqual(snapshot.Evidence[0], gotA) {
		t.Fatalf("expected one coherent immutable proof: snapshot=%+v result=%+v", snapshot.Evidence, gotA)
	}

	sameBaseDigestMismatch := gotA
	sameBaseDigestMismatch.PatchDigest = "fabricated-digest"
	if _, err := storeB.RecordAcceptedPatchReviewEvidence(ctx, sameBaseDigestMismatch); err == nil {
		t.Fatal("same-base digest mismatch succeeded")
	}
	identityMismatch := second
	identityMismatch.SourceRevision = "different-source"
	if _, err := storeB.RecordAcceptedPatchReviewEvidence(ctx, identityMismatch); err == nil {
		t.Fatal("cross-base accepted-review identity mismatch succeeded")
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
