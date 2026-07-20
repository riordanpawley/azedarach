package domain

import (
	"fmt"
	"strings"
	"time"
)

// PublicationOperationState is the durable state of an accepted patch as it
// progresses from review acceptance to exact-base publication.
type PublicationOperationState string

const (
	PublicationOperationQueued     PublicationOperationState = "queued"
	PublicationOperationPreparing  PublicationOperationState = "preparing"
	PublicationOperationValidating PublicationOperationState = "validating"
	PublicationOperationPassed     PublicationOperationState = "passed"
	PublicationOperationFailed     PublicationOperationState = "failed"
	PublicationOperationConflicted PublicationOperationState = "conflicted"
	PublicationOperationStale      PublicationOperationState = "stale"
	PublicationOperationMerged     PublicationOperationState = "merged"
	PublicationOperationCanceled   PublicationOperationState = "canceled"
)

func (s PublicationOperationState) Terminal() bool {
	switch s {
	case PublicationOperationFailed, PublicationOperationConflicted, PublicationOperationStale, PublicationOperationMerged, PublicationOperationCanceled:
		return true
	default:
		return false
	}
}

func (s PublicationOperationState) Valid() bool {
	switch s {
	case PublicationOperationQueued, PublicationOperationPreparing, PublicationOperationValidating, PublicationOperationPassed,
		PublicationOperationFailed, PublicationOperationConflicted, PublicationOperationStale, PublicationOperationMerged, PublicationOperationCanceled:
		return true
	default:
		return false
	}
}

// PublicationOperation is the daemon-owned durable intent for publishing one
// accepted patch onto the configured project base. Review evidence is pinned
// independently from merge-result evidence so unrelated base movement does not
// erase the accepted review decision.
type PublicationOperation struct {
	OperationID                    string                    `json:"operation_id"`
	ProjectID                      string                    `json:"project_id"`
	IssueID                        string                    `json:"issue_id"`
	IntentKey                      string                    `json:"intent_key"`
	RequestFingerprint             string                    `json:"request_fingerprint"`
	ActorID                        string                    `json:"actor_id"`
	ReviewEpochEventID             int64                     `json:"review_epoch_event_id"`
	AcceptedReviewEventID          int64                     `json:"accepted_review_event_id"`
	ReviewerKind                   string                    `json:"reviewer_kind"`
	PatchEvidenceID                string                    `json:"patch_evidence_id"`
	AcceptedPublicationOperationID string                    `json:"accepted_publication_operation_id"`
	TargetID                       string                    `json:"target_id"`
	TargetBranch                   string                    `json:"target_branch"`
	SourceRevision                 string                    `json:"source_revision"`
	BaseRevision                   string                    `json:"base_revision"`
	CandidateRevision              string                    `json:"candidate_revision,omitempty"`
	PolicyVersion                  string                    `json:"policy_version,omitempty"`
	EnvironmentFingerprint         string                    `json:"environment_fingerprint,omitempty"`
	ValidationCommand              string                    `json:"validation_command"`
	EvidenceSource                 string                    `json:"evidence_source,omitempty"`
	EvidenceEventID                int64                     `json:"evidence_event_id,omitempty"`
	EvidenceSeq                    int64                     `json:"evidence_seq,omitempty"`
	EvidenceDigest                 string                    `json:"evidence_digest,omitempty"`
	State                          PublicationOperationState `json:"state"`
	LeaseOwner                     string                    `json:"lease_owner,omitempty"`
	ClaimToken                     string                    `json:"-"`
	ClaimExpiresAt                 *time.Time                `json:"claim_expires_at,omitempty"`
	ValidationRequestID            string                    `json:"validation_request_id,omitempty"`
	ReusedEvidenceID               string                    `json:"reused_evidence_id,omitempty"`
	FailureKind                    string                    `json:"failure_kind,omitempty"`
	FailureDetail                  string                    `json:"failure_detail,omitempty"`
	FailureArtifact                string                    `json:"failure_artifact,omitempty"`
	QueuePosition                  int                       `json:"queue_position,omitempty"`
	CreatedAt                      time.Time                 `json:"created_at"`
	UpdatedAt                      time.Time                 `json:"updated_at"`
	StartedAt                      *time.Time                `json:"started_at,omitempty"`
	FinishedAt                     *time.Time                `json:"finished_at,omitempty"`
}

func (o PublicationOperation) ValidateIntent() error {
	for name, value := range map[string]string{
		"operation_id": o.OperationID, "project_id": o.ProjectID, "issue_id": o.IssueID,
		"intent_key": o.IntentKey, "request_fingerprint": o.RequestFingerprint, "actor_id": o.ActorID,
		"target_id": o.TargetID, "target_branch": o.TargetBranch, "source_revision": o.SourceRevision,
		"base_revision":      o.BaseRevision,
		"validation_command": o.ValidationCommand,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("publication operation requires %s", name)
		}
	}
	if _, err := CanonicalReviewerIdentity(o.ActorID, o.ReviewerKind); err != nil {
		return fmt.Errorf("publication operation reviewer identity: %w", err)
	}
	if o.ReviewEpochEventID <= 0 {
		return fmt.Errorf("publication operation requires positive review_epoch_event_id")
	}
	if o.AcceptedReviewEventID <= 0 {
		return fmt.Errorf("publication operation requires positive accepted_review_event_id")
	}
	if strings.TrimSpace(o.PatchEvidenceID) == "" {
		return fmt.Errorf("publication operation requires patch_evidence_id")
	}
	if strings.TrimSpace(o.AcceptedPublicationOperationID) == "" {
		return fmt.Errorf("publication operation requires accepted_publication_operation_id")
	}
	if !strings.EqualFold(strings.TrimSpace(o.TargetID), "base") {
		return fmt.Errorf("publication operation target must be configured base")
	}
	if o.State == "" {
		o.State = PublicationOperationQueued
	}
	if !o.State.Valid() {
		return fmt.Errorf("invalid publication operation state %q", o.State)
	}
	return nil
}

func (o PublicationOperation) ValidateReviewAuthority() error {
	if strings.TrimSpace(o.ActorID) == "" || !strings.EqualFold(strings.TrimSpace(o.ReviewerKind), "orchestrator") || o.ReviewEpochEventID <= 0 || o.AcceptedReviewEventID <= 0 || strings.TrimSpace(o.PatchEvidenceID) == "" || strings.TrimSpace(o.AcceptedPublicationOperationID) == "" {
		return fmt.Errorf("publication operation requires exact orchestrator, review epoch, accepted event, and patch evidence authority")
	}
	return nil
}

// ValidatePreparedIntent permits only the one pre-commit state where the
// accepted event ID has not yet been allocated. The acceptance transaction
// must bind it before commit; ordinary queue insertion uses ValidateIntent.
func (o PublicationOperation) ValidatePreparedIntent() error {
	if o.AcceptedReviewEventID > 0 {
		return o.ValidateIntent()
	}
	o.AcceptedReviewEventID = 1
	return o.ValidateIntent()
}
