package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestEvaluatePublicationEvidencePreservesPatchAcrossUnrelatedBaseMovement(t *testing.T) {
	evidence := testPublicationEvidence(PublicationEvidencePatchReview)
	evidence.BaseRevision = "base-a"
	candidate := testPublicationCandidate()
	candidate.BaseRevision = "base-b"
	candidate.ChangedPaths = []string{"web/package.json"}
	policy := testPublicationPolicy()

	got := EvaluatePublicationEvidence(evidence, candidate, policy)
	if !got.Retained || !got.BaseMovementOnly || len(got.Reasons) != 0 {
		t.Fatalf("assessment = %+v, want retained base-movement-only patch review", got)
	}
}

func TestEvaluatePublicationEvidenceSelectivelyInvalidatesOverlapAndHighRisk(t *testing.T) {
	evidence := testPublicationEvidence(PublicationEvidenceActivePath)
	evidence.BaseRevision = "base-a"
	evidence.Coverage = PublicationEvidenceCoverage{Paths: []string{"src/api.ts"}, Dependencies: []string{"core"}}
	candidate := testPublicationCandidate()
	candidate.BaseRevision = "base-b"
	candidate.ChangedPaths = []string{"src/api.ts"}
	candidate.ChangedDependencies = []string{"core"}
	candidate.ChangedSurfaces = []string{"protocol"}

	got := EvaluatePublicationEvidence(evidence, candidate, testPublicationPolicy())
	want := []PublicationInvalidationReason{PublicationInvalidPathOverlap, PublicationInvalidDependencyOverlap, PublicationInvalidHighRiskBaseChange}
	if got.Retained || !reflect.DeepEqual(got.Reasons, want) {
		t.Fatalf("reasons = %v retained=%t, want %v false", got.Reasons, got.Retained, want)
	}
}

func TestEvaluatePublicationEvidenceDetectsPortableDirectoryOverlap(t *testing.T) {
	evidence := testPublicationEvidence(PublicationEvidenceActivePath)
	evidence.Coverage.Paths = []string{"packages/api"}
	candidate := testPublicationCandidate()
	candidate.ChangedPaths = []string{"packages/api/src/handler.ts"}
	got := EvaluatePublicationEvidence(evidence, candidate, testPublicationPolicy())
	if got.Retained || !containsPublicationReason(got.Reasons, PublicationInvalidPathOverlap) {
		t.Fatalf("assessment = %+v, want directory path overlap", got)
	}
}

func TestEvaluatePublicationEvidenceMergeResultNeverSurvivesInputMovement(t *testing.T) {
	evidence := testPublicationEvidence(PublicationEvidenceMergeResult)
	candidate := testPublicationCandidate()
	candidate.BaseRevision = "base-b"
	candidate.ResultRevision = "merge-b"

	got := EvaluatePublicationEvidence(evidence, candidate, testPublicationPolicy())
	if got.Retained || !containsPublicationReason(got.Reasons, PublicationInvalidMergeInputChange) {
		t.Fatalf("assessment = %+v, want merge input invalidation", got)
	}
}

func TestEvaluatePublicationEvidenceFailsClosedWithoutConsumerCapability(t *testing.T) {
	evidence := testPublicationEvidence(PublicationEvidencePatchReview)
	candidate := testPublicationCandidate()
	candidate.CapabilityAvailable = false
	candidate.ImpactKnown = false

	got := EvaluatePublicationEvidence(evidence, candidate, testPublicationPolicy())
	if got.Retained || !containsPublicationReason(got.Reasons, PublicationInvalidCapabilityAbsent) || !containsPublicationReason(got.Reasons, PublicationInvalidImpactUnknown) {
		t.Fatalf("assessment = %+v, want absent-capability and unknown-impact invalidation", got)
	}
}

func TestPublicationEvidenceValidationRequiresLayerIdentity(t *testing.T) {
	patch := testPublicationEvidence(PublicationEvidencePatchReview)
	patch.PatchDigest = ""
	if err := patch.Validate(); err == nil {
		t.Fatal("patch review without patch digest validated")
	}
	merge := testPublicationEvidence(PublicationEvidenceMergeResult)
	merge.ResultRevision = ""
	if err := merge.Validate(); err == nil {
		t.Fatal("merge result without result revision validated")
	}
}

func TestCanonicalPublicationPathIsPortableAndRepoRelative(t *testing.T) {
	for _, valid := range []string{"src/api.ts", "packages/windows/portable.go", "README.md"} {
		got, err := CanonicalPublicationPath(valid)
		if err != nil || got != valid {
			t.Fatalf("CanonicalPublicationPath(%q) = %q, %v", valid, got, err)
		}
	}
	for _, invalid := range []string{"", ".", "../secret", "src/../secret", "/etc/passwd", `C:/Windows/system.ini`, `src\\api.ts`, "./src/api.ts", "src//api.ts"} {
		if got, err := CanonicalPublicationPath(invalid); err == nil {
			t.Fatalf("CanonicalPublicationPath(%q) = %q, want portable rejection", invalid, got)
		}
	}
}

func TestSummarizePublicationEvidenceExplainsPartialInvalidation(t *testing.T) {
	snapshot := PublicationEvidenceSnapshot{Revision: 4, Evidence: []PublicationEvidence{
		{EvidenceID: "review", Layer: PublicationEvidencePatchReview},
		{EvidenceID: "path", Layer: PublicationEvidenceActivePath},
	}, Invalidations: []PublicationEvidenceInvalidation{{EvidenceID: "path", Reason: PublicationInvalidDependencyOverlap}}}
	got := SummarizePublicationEvidence(snapshot, []PublicationEvidenceAssessment{
		{EvidenceID: "review", Layer: PublicationEvidencePatchReview, Retained: true},
		{EvidenceID: "path", Layer: PublicationEvidenceActivePath, Reasons: []PublicationInvalidationReason{PublicationInvalidDependencyOverlap}},
	})
	if got.State != "partial" || got.PatchReview != 1 || got.ActivePath != 0 || got.Invalidated != 1 || !reflect.DeepEqual(got.Reasons, []PublicationInvalidationReason{PublicationInvalidDependencyOverlap}) {
		t.Fatalf("diagnostic = %+v", got)
	}
}

func TestApplyPublicationEvidenceInvalidationsOverridesRetainedAssessment(t *testing.T) {
	assessment := PublicationEvidenceAssessment{EvidenceID: "review", Layer: PublicationEvidencePatchReview, Retained: true, BaseMovementOnly: true}
	got := ApplyPublicationEvidenceInvalidations(assessment, []PublicationEvidenceInvalidation{{EvidenceID: "review", Reason: PublicationInvalidMaterialDecision, Details: "decision revision advanced"}})
	if got.Retained || got.BaseMovementOnly || !reflect.DeepEqual(got.Reasons, []PublicationInvalidationReason{PublicationInvalidMaterialDecision}) {
		t.Fatalf("assessment = %+v", got)
	}
}

func TestEvaluatePublicationEvidenceBuiltInHighRiskSurfaceFailsClosed(t *testing.T) {
	evidence := testPublicationEvidence(PublicationEvidencePatchReview)
	candidate := testPublicationCandidate()
	candidate.BaseRevision = "base-b"
	candidate.ChangedSurfaces = []string{"authority_boundary"}
	policy := testPublicationPolicy()
	policy.ExactBaseSurfaces = nil
	got := EvaluatePublicationEvidence(evidence, candidate, policy)
	if got.Retained || !containsPublicationReason(got.Reasons, PublicationInvalidHighRiskBaseChange) {
		t.Fatalf("assessment = %+v, want built-in high-risk invalidation", got)
	}
}

func TestPublicationEvidenceInvalidationRejectsUnknownReason(t *testing.T) {
	invalidation := PublicationEvidenceInvalidation{InvalidationID: "i", EvidenceID: "e", Reason: "consumer_typo", Details: "bad reason", CreatedAt: time.Unix(1, 0)}
	if err := invalidation.Validate(); err == nil {
		t.Fatal("unknown invalidation reason validated")
	}
}

func TestPublicationEvidenceCandidateRequiresCurrentBase(t *testing.T) {
	candidate := testPublicationCandidate()
	candidate.BaseRevision = ""
	if err := candidate.Validate(); err == nil {
		t.Fatal("candidate without current base validated")
	}
}

func TestEffectivePublicationEvidenceInvalidationsFollowReuseProvenance(t *testing.T) {
	snapshot := PublicationEvidenceSnapshot{Evidence: []PublicationEvidence{
		{EvidenceID: "original", Layer: PublicationEvidencePatchReview},
		{EvidenceID: "reused", Layer: PublicationEvidencePatchReview, ReusedFromEvidenceID: "original"},
	}, Invalidations: []PublicationEvidenceInvalidation{{InvalidationID: "invalid", EvidenceID: "original", Reason: PublicationInvalidPolicyChange, Details: "policy advanced", CreatedAt: time.Unix(2, 0)}}}
	effective := EffectivePublicationEvidenceInvalidations(snapshot)
	if len(effective) != 2 || effective[1].EvidenceID != "reused" || effective[1].Reason != PublicationInvalidPolicyChange {
		t.Fatalf("effective invalidations = %+v", effective)
	}
}

func testPublicationEvidence(layer PublicationEvidenceLayer) PublicationEvidence {
	return PublicationEvidence{
		EvidenceID: "e-1", ProjectID: "project", IssueID: "issue", Layer: layer,
		PatchDigest: "patch-a", SourceRevision: "source-a", BaseRevision: "base-a", ResultRevision: "merge-a",
		Producer: "reviewer", PolicyVersion: "policy-v1", EnvironmentFingerprint: "env-a", CreatedAt: time.Unix(1, 0).UTC(),
	}
}

func testPublicationCandidate() PublicationEvidenceCandidate {
	return PublicationEvidenceCandidate{PatchDigest: "patch-a", SourceRevision: "source-a", BaseRevision: "base-a", ResultRevision: "merge-a", PolicyVersion: "policy-v1", EnvironmentFingerprint: "env-a", ImpactKnown: true, CapabilityAvailable: true}
}

func testPublicationPolicy() PublicationEvidencePolicy {
	return PublicationEvidencePolicy{Version: "policy-v1", ExactBaseSurfaces: []string{"migration", "protocol", "schema"}, InvalidatePathOverlap: true, InvalidateDependencyOverlap: true, RequireEnvironmentMatch: true, FailClosedUnknownImpact: true, RequireCapability: true}
}

func containsPublicationReason(reasons []PublicationInvalidationReason, want PublicationInvalidationReason) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
