package domain

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestDeriveReviewReadyPublicationsCoalescesEvidenceAroundTransition(t *testing.T) {
	events := []IssueObservationEvent{
		{ID: 1, IssueID: naming.IssueID("az-1"), Type: "worker-integration-ready"},
		{ID: 2, IssueID: naming.IssueID("az-1"), Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
		{ID: 3, IssueID: naming.IssueID("az-1"), Type: IssueEventEvidenceSubmitted, Payload: map[string]any{"schema": WorkerEvidenceSchemaV1}},
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
		{ID: 3, Type: "worker.integration_ready"},
		{ID: 4, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
		{ID: 5, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_progress"}},
		{ID: 6, Type: "worker_ready"},
		{ID: 7, Type: IssueEventIssueStatusChanged, Source: "issue-store", Payload: map[string]any{"to_status": "in_review"}},
	}

	got := DeriveReviewReadyPublications(events)
	if len(got) != 3 || got[0].SourceEvent.ID != 1 || got[1].SourceEvent.ID != 3 || got[2].SourceEvent.ID != 6 {
		t.Fatalf("publications = %+v, want source events [1 3 6]", got)
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
	events := []IssueObservationEvent{{ID: 1, Type: IssueEventEvidenceSubmitted, Payload: map[string]any{"schema": WorkerEvidenceSchemaV1}}}
	if got := DeriveReviewReadyPublications(events); len(got) != 0 {
		t.Fatalf("publications = %+v, want none before in_review", got)
	}
}
