package domain

import (
	"fmt"
	"strings"
)

// ValidateHistoricalPublicationReviewEvidence recognizes the exact legacy
// review artifact written before daemon-owned review publication existed.
func ValidateHistoricalPublicationReviewEvidence(event IssueObservationEvent, baseRevision, candidateRevision string) error {
	if event.Type != IssueEventHistoricalReviewAccepted {
		return fmt.Errorf("historical review outcome is %s, not accepted", event.Type)
	}
	if err := validateHistoricalPublicationEvidenceProvenance(event); err != nil {
		return err
	}
	if historicalPayloadString(event.Payload, "review_result") != "accepted" {
		return fmt.Errorf("historical review result is not accepted")
	}
	return validateHistoricalPublicationEvidenceIdentity(event, baseRevision, candidateRevision)
}

// ValidateHistoricalPublicationValidationEvidence recognizes the exact
// legacy clean-candidate validation artifact paired with historical review.
func ValidateHistoricalPublicationValidationEvidence(event IssueObservationEvent, baseRevision, candidateRevision string) error {
	if event.Type != IssueEventHistoricalValidationCompleted {
		return fmt.Errorf("historical validation outcome is %s, not completed", event.Type)
	}
	if err := validateHistoricalPublicationEvidenceProvenance(event); err != nil {
		return err
	}
	if historicalPayloadString(event.Payload, "result") != "clean" {
		return fmt.Errorf("historical validation result is not clean")
	}
	return validateHistoricalPublicationEvidenceIdentity(event, baseRevision, candidateRevision)
}

func validateHistoricalPublicationEvidenceProvenance(event IssueObservationEvent) error {
	if strings.TrimSpace(event.Source) != "agent" || strings.TrimSpace(event.SourceCommand) != "az issue record" {
		return fmt.Errorf("historical evidence %d has untrusted provenance", event.ID)
	}
	return nil
}

func validateHistoricalPublicationEvidenceIdentity(event IssueObservationEvent, baseRevision, candidateRevision string) error {
	if historicalPayloadString(event.Payload, "base_revision") != strings.TrimSpace(baseRevision) || historicalPayloadString(event.Payload, "candidate_revision") != strings.TrimSpace(candidateRevision) {
		return fmt.Errorf("historical evidence %d does not match exact base and candidate revisions", event.ID)
	}
	return nil
}

func historicalPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}
