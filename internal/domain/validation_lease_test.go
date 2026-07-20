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
	a.ReviewerID, a.ReviewerKind, a.ReviewEpochEventID = "reviewer", ReviewerOwnerKindOrchestrator, 42
	a.PublicationOperationID, a.AcceptedReviewEventID = "publication", 43
	a.AcceptedPublicationOperationID = "publication"
	assert.NoError(t, a.Validate())
}

func TestValidationReviewEvidenceCompatibilityFailsClosedWithoutExactAuthority(t *testing.T) {
	target := validValidationAcquire()
	source := ValidationRequest{
		ProjectID: target.ProjectID, Class: target.Class, Scope: ValidationScopeTicket, Purpose: ValidationPurposeReviewEvidence,
		Profile: target.Profile, Command: target.Command, SourceRevision: target.SourceRevision, IsolationMode: target.IsolationMode, EnvironmentFingerprint: target.EnvironmentFingerprint,
		ReviewerID: "reviewer", ReviewerKind: ReviewerOwnerKindOrchestrator, ReviewEpochEventID: 42, PublicationOperationID: "publication", AcceptedReviewEventID: 43, AcceptedPublicationOperationID: "publication",
	}
	assert.True(t, ValidationRequestCanSatisfy(source, target))

	for name, edit := range map[string]func(*ValidationRequest){
		"legacy untyped reviewer": func(request *ValidationRequest) { request.ReviewerKind = "" },
		"wrong reviewer kind":     func(request *ValidationRequest) { request.ReviewerKind = "agent" },
		"missing current operation": func(request *ValidationRequest) {
			request.PublicationOperationID = ""
		},
		"missing accepted event": func(request *ValidationRequest) { request.AcceptedReviewEventID = 0 },
		"missing accepted operation": func(request *ValidationRequest) {
			request.AcceptedPublicationOperationID = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := source
			edit(&candidate)
			assert.False(t, ValidationRequestCanSatisfy(candidate, target))
		})
	}
}

func TestValidationDevelopmentDoesNotUseDaemonAdmission(t *testing.T) {
	a := validValidationAcquire()
	a.Scope, a.Purpose, a.IssueID = ValidationScopeTicket, ValidationPurposeDevelopment, "dnb"
	require.ErrorContains(t, a.Validate(), "development validation does not use daemon admission")
}

func TestOrderValidationQueueUsesPriorityFIFOAndBoundedFairness(t *testing.T) {
	requests := []ValidationRequest{
		{Sequence: 1, RequestID: "protected-p4", IssuePriority: P4, PriorityBypassCount: ValidationPriorityBypassLimit},
		{Sequence: 2, RequestID: "p1-first", IssuePriority: P1},
		{Sequence: 3, RequestID: "p0-first", IssuePriority: P0},
		{Sequence: 4, RequestID: "p0-second", IssuePriority: P0},
	}

	ordered := OrderValidationQueue(requests)
	assert.Equal(t, []string{"protected-p4", "p0-first", "p0-second", "p1-first"}, []string{ordered[0].RequestID, ordered[1].RequestID, ordered[2].RequestID, ordered[3].RequestID})
	assert.Equal(t, ValidationOrderingBoundedFairness, ordered[0].OrderingReason)
	assert.Equal(t, ValidationOrderingPriorityFIFO, ordered[1].OrderingReason)
	assert.Equal(t, []int{1, 2, 3, 4}, []int{ordered[0].QueuePosition, ordered[1].QueuePosition, ordered[2].QueuePosition, ordered[3].QueuePosition})
}
