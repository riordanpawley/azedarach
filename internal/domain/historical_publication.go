package domain

import (
	"fmt"
	"strings"
)

// HistoricalPublicationAuthorization is an explicit operator attestation for
// one integration that predates daemon-owned publication operations. Legacy
// observations remain evidence inputs; this typed authorization is authority.
type HistoricalPublicationAuthorization struct {
	ReviewEventID                 int64               `json:"review_event_id"`
	ValidationEventID             int64               `json:"validation_event_id"`
	ReceiptEventID                int64               `json:"receipt_event_id"`
	ReviewerID                    string              `json:"reviewer_id"`
	AuthoritativeEvidenceID       string              `json:"authoritative_evidence_id"`
	Class                         ValidationClass     `json:"validation_class"`
	Scope                         ValidationScope     `json:"validation_scope"`
	Purpose                       ValidationPurpose   `json:"validation_purpose"`
	Execution                     ValidationExecution `json:"validation_execution"`
	Override                      ValidationOverride  `json:"validation_override"`
	EvidencePresent               bool                `json:"evidence_present"`
	AttestsMissingLegacySemantics bool                `json:"attests_missing_legacy_semantics"`
}

func (a HistoricalPublicationAuthorization) Validate() error {
	if a.ReviewEventID <= 0 || a.ValidationEventID <= 0 || a.ReceiptEventID <= 0 {
		return fmt.Errorf("historical authorization requires review, validation, and receipt event IDs")
	}
	if strings.TrimSpace(a.ReviewerID) == "" || strings.TrimSpace(a.AuthoritativeEvidenceID) == "" {
		return fmt.Errorf("historical authorization requires reviewer and authoritative evidence identity")
	}
	if a.Class != ValidationClassAggregate || a.Scope != ValidationScopeRepository || a.Purpose != ValidationPurposePushGate {
		return fmt.Errorf("historical authorization requires aggregate repository push-gate validation")
	}
	if a.Execution != ValidationExecutionExecuted && a.Execution != ValidationExecutionReused {
		return fmt.Errorf("historical authorization requires executed or authoritative reused validation")
	}
	if a.Override != ValidationOverrideNone {
		return fmt.Errorf("historical authorization rejects emergency or other validation overrides")
	}
	if !a.EvidencePresent {
		return fmt.Errorf("historical authorization requires present authoritative evidence")
	}
	if !a.AttestsMissingLegacySemantics {
		return fmt.Errorf("historical authorization must explicitly attest the legacy publication semantics absent from stored evidence")
	}
	return nil
}

// ValidateHistoricalPublicationReviewEvidence recognizes the exact legacy
// review artifact written before daemon-owned review publication existed.
func ValidateHistoricalPublicationReviewEvidence(event IssueObservationEvent, baseRevision, candidateRevision string) error {
	if event.Type != IssueEventHistoricalReviewAccepted {
		return fmt.Errorf("historical review outcome is %s, not accepted", event.Type)
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
	if historicalPayloadString(event.Payload, "result") != "clean" {
		return fmt.Errorf("historical validation result is not clean")
	}
	return validateHistoricalPublicationEvidenceIdentity(event, baseRevision, candidateRevision)
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
