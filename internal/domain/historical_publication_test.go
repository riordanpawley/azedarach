package domain

import "testing"

func TestHistoricalPublicationEvidenceRequiresExactCandidate(t *testing.T) {
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

func TestHistoricalPublicationAuthorizationRequiresRepositoryPushGateAuthority(t *testing.T) {
	valid := HistoricalPublicationAuthorization{
		ReviewEventID: 10, ValidationEventID: 11, ReceiptEventID: 12,
		ReviewerID: "reviewer", AuthoritativeEvidenceID: "legacy-gate-report-1",
		Class: ValidationClassAggregate, Scope: ValidationScopeRepository, Purpose: ValidationPurposePushGate,
		Execution: ValidationExecutionExecuted, Override: ValidationOverrideNone, EvidencePresent: true, AttestsMissingLegacySemantics: true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*HistoricalPublicationAuthorization){
		"missing review":                 func(pin *HistoricalPublicationAuthorization) { pin.ReviewEventID = 0 },
		"missing validation":             func(pin *HistoricalPublicationAuthorization) { pin.ValidationEventID = 0 },
		"missing receipt":                func(pin *HistoricalPublicationAuthorization) { pin.ReceiptEventID = 0 },
		"missing reviewer":               func(pin *HistoricalPublicationAuthorization) { pin.ReviewerID = "" },
		"missing authoritative evidence": func(pin *HistoricalPublicationAuthorization) { pin.AuthoritativeEvidenceID = "" },
		"focused validation":             func(pin *HistoricalPublicationAuthorization) { pin.Class = ValidationClassSafe },
		"development scope":              func(pin *HistoricalPublicationAuthorization) { pin.Purpose = ValidationPurposeDevelopment },
		"non repository":                 func(pin *HistoricalPublicationAuthorization) { pin.Scope = ValidationScopeTicket },
		"non push gate":                  func(pin *HistoricalPublicationAuthorization) { pin.Purpose = ValidationPurposeReviewEvidence },
		"joined execution":               func(pin *HistoricalPublicationAuthorization) { pin.Execution = ValidationExecutionJoined },
		"skipped execution":              func(pin *HistoricalPublicationAuthorization) { pin.Execution = ValidationExecutionSkipped },
		"emergency":                      func(pin *HistoricalPublicationAuthorization) { pin.Override = ValidationOverrideEmergency },
		"missing evidence":               func(pin *HistoricalPublicationAuthorization) { pin.EvidencePresent = false },
		"missing explicit attestation":   func(pin *HistoricalPublicationAuthorization) { pin.AttestsMissingLegacySemantics = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid historical authorization was accepted")
			}
		})
	}
	reused := valid
	reused.Execution = ValidationExecutionReused
	if err := reused.Validate(); err != nil {
		t.Fatalf("authoritative reused validation rejected: %v", err)
	}
}
