package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validValidationAcquire() ValidationAcquire {
	return ValidationAcquire{RequestID: "request", LeaseToken: "secret", ProjectID: "project", Class: ValidationClassAggregate, Scope: ValidationScopeRepository, Purpose: ValidationPurposePushGate, IsolationMode: "repository-family", EnvironmentFingerprint: "toolchain-a", Override: ValidationOverrideNone, Profile: "merge-gate", Command: "just merge-gate", SourceRevision: "abc", TTL: time.Minute}
}

func TestValidationAcquireSeparatesScopeAndPurpose(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ValidationAcquire)
		want string
	}{
		{name: "repository ticket", edit: func(a *ValidationAcquire) { a.IssueID = "dmm" }, want: "must not identify a ticket"},
		{name: "ticket missing identity", edit: func(a *ValidationAcquire) { a.Scope = ValidationScopeTicket; a.Purpose = ValidationPurposeDevelopment }, want: "requires an existing ticket"},
		{name: "repository review", edit: func(a *ValidationAcquire) { a.Purpose = ValidationPurposeReviewEvidence }, want: "requires ticket scope"},
		{name: "ticket push", edit: func(a *ValidationAcquire) { a.Scope = ValidationScopeTicket; a.IssueID = "dmm" }, want: "requires repository scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := validValidationAcquire()
			tt.edit(&a)
			require.ErrorContains(t, a.Validate(), tt.want)
		})
	}
}

func TestValidationReviewEvidenceRequiresFullAuthorityIdentity(t *testing.T) {
	a := validValidationAcquire()
	a.Scope, a.Purpose, a.IssueID = ValidationScopeTicket, ValidationPurposeReviewEvidence, "dmm"
	require.ErrorContains(t, a.Validate(), "reviewer identity")
	a.ReviewerID, a.ReviewEpochEventID = "reviewer", 42
	assert.NoError(t, a.Validate())
}
