package domain

import (
	"fmt"
	"strings"
)

const ReviewerOwnerKindOrchestrator = "orchestrator"

// ReviewerIdentity is the canonical typed identity permitted to authorize an
// orchestration review. Reviewer IDs are case-insensitive by contract and are
// persisted in lower case; owner kinds are exact and never inferred.
type ReviewerIdentity struct {
	OwnerID   string `json:"owner_id"`
	OwnerKind string `json:"owner_kind"`
}

func CanonicalReviewerIdentity(ownerID, ownerKind string) (ReviewerIdentity, error) {
	identity := ReviewerIdentity{
		OwnerID:   strings.ToLower(strings.TrimSpace(ownerID)),
		OwnerKind: strings.TrimSpace(ownerKind),
	}
	if identity.OwnerID == "" {
		return ReviewerIdentity{}, fmt.Errorf("reviewer owner_id is required")
	}
	if identity.OwnerKind != ReviewerOwnerKindOrchestrator {
		return ReviewerIdentity{}, fmt.Errorf("reviewer owner_kind must be %q", ReviewerOwnerKindOrchestrator)
	}
	return identity, nil
}

func (i ReviewerIdentity) Matches(ownerID, ownerKind string) bool {
	expected, err := CanonicalReviewerIdentity(i.OwnerID, i.OwnerKind)
	if err != nil {
		return false
	}
	actual, err := CanonicalReviewerIdentity(ownerID, ownerKind)
	return err == nil && actual == expected
}
