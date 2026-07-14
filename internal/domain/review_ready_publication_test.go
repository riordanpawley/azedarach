package domain

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestDeriveReviewReadyPublicationsCoalescesEvidenceAroundTransition(t *testing.T) {
	events := []IssueObservationEvent{
		{ID: 1, IssueID: naming.IssueID("az-1"), Type: "worker-integration-ready", Payload: reviewReadyEvidencePayload()},
		{ID: 2, IssueID: naming.IssueID("az-1"), Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
		{ID: 3, IssueID: naming.IssueID("az-1"), Type: IssueEventEvidenceSubmitted, Payload: reviewReadyEvidencePayload()},
	}

	got := DeriveReviewReadyPublications(events)
	if len(got) != 1 || got[0].SourceEvent.ID != 1 {
		t.Fatalf("publications = %+v, want one publication owned by evidence event 1", got)
	}
}

func TestDeriveReviewReadyPublicationsPublishesRepeatedResubmissions(t *testing.T) {
	events := []IssueObservationEvent{
		{ID: 1, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
		{ID: 2, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_progress"}},
		{ID: 3, Type: "worker.integration_ready", Payload: reviewReadyEvidencePayload()},
		{ID: 4, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
		{ID: 5, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_progress"}},
		{ID: 6, Type: "worker_ready", Payload: reviewReadyEvidencePayload()},
		{ID: 7, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
	}

	got := DeriveReviewReadyPublications(events)
	if len(got) != 2 || got[0].SourceEvent.ID != 3 || got[1].SourceEvent.ID != 6 {
		t.Fatalf("publications = %+v, want evidence-backed source events [3 6]", got)
	}
}

func TestIsReviewRequestTransitionRequiresIssueStoreProvenance(t *testing.T) {
	payload := map[string]any{"to_status": "in_review"}
	if IsReviewRequestTransition(IssueObservationEvent{Type: IssueEventIssueStatusChanged, Source: "az issue record", Payload: payload}) {
		t.Fatal("user-recorded status lookalike must not start a review-request epoch")
	}
	if !IsReviewRequestTransition(IssueObservationEvent{Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: payload}) {
		t.Fatal("issue-store status transition must start a review-request epoch")
	}
}

func TestDeriveReviewReadyPublicationsDoesNotPublishEvidenceUntilReviewReady(t *testing.T) {
	events := []IssueObservationEvent{{ID: 1, Type: IssueEventEvidenceSubmitted, Payload: reviewReadyEvidencePayload()}}
	if got := DeriveReviewReadyPublications(events); len(got) != 0 {
		t.Fatalf("publications = %+v, want none before in_review", got)
	}
}

func TestDeriveReviewReadyPublicationsRejectsStatusOnlyAndIncompleteEvidence(t *testing.T) {
	events := []IssueObservationEvent{
		{ID: 1, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
		{ID: 2, Type: "worker-integration-ready", Payload: map[string]any{"summary": "not a complete packet"}},
	}
	if got := DeriveReviewReadyPublications(events); len(got) != 0 {
		t.Fatalf("publications = %+v, want no readiness publication review accept would reject", got)
	}
}

func TestDeriveReviewReadyPublicationsNewestIncompleteEvidenceClearsPendingReadyPacket(t *testing.T) {
	events := []IssueObservationEvent{
		{ID: 1, Type: IssueEventEvidenceSubmitted, Payload: reviewReadyEvidencePayload()},
		{ID: 2, Type: "worker-integration-ready", Payload: map[string]any{"schema": WorkerEvidenceSchemaV1, "summary": "newer incomplete packet"}},
		{ID: 3, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
	}
	if got := DeriveReviewReadyPublications(events); len(got) != 0 {
		t.Fatalf("publications = %+v, want newer rejected evidence to prevent stale readiness", got)
	}
}

func reviewReadyEvidencePayload() map[string]any {
	return map[string]any{
		"schema": WorkerEvidenceSchemaV1, "summary": "Ready", "commands_run": []string{"just test"},
		"key_assertions": []string{"validation passed"}, "files_changed": []string{"internal/domain/review_ready_publication.go"},
		"review": map[string]any{"status": "clean", "findings": []string{}}, "risks": []string{"none"},
	}
}
