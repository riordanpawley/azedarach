package domain

import "testing"

func TestHistoricalPublicationEvidenceRequiresExactTrustedCandidate(t *testing.T) {
	review := IssueObservationEvent{
		ID: 10, Type: IssueEventHistoricalReviewAccepted, Source: "agent", SourceCommand: "az issue record",
		Payload: map[string]any{"review_result": "accepted", "base_revision": "base", "candidate_revision": "candidate"},
	}
	validation := IssueObservationEvent{
		ID: 11, Type: IssueEventHistoricalValidationCompleted, Source: "agent", SourceCommand: "az issue record",
		Payload: map[string]any{"result": "clean", "base_revision": "base", "candidate_revision": "candidate"},
	}
	if err := ValidateHistoricalPublicationReviewEvidence(review, "base", "candidate"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHistoricalPublicationValidationEvidence(validation, "base", "candidate"); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*IssueObservationEvent){
		"wrong candidate": func(event *IssueObservationEvent) { event.Payload["candidate_revision"] = "other" },
		"wrong base":      func(event *IssueObservationEvent) { event.Payload["base_revision"] = "other" },
		"untrusted source": func(event *IssueObservationEvent) {
			event.Source = "manual"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := review
			candidate.Payload = map[string]any{"review_result": "accepted", "base_revision": "base", "candidate_revision": "candidate"}
			mutate(&candidate)
			if err := ValidateHistoricalPublicationReviewEvidence(candidate, "base", "candidate"); err == nil {
				t.Fatal("invalid historical review evidence was accepted")
			}
		})
	}
}
