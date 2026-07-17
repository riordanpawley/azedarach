package protocol

import "github.com/riordanpawley/azedarach/internal/domain"

const (
	CommandValidationAcquire             = "validation.acquire"
	CommandValidationHeartbeat           = "validation.heartbeat"
	CommandValidationNested              = "validation.authorize_nested"
	CommandValidationFinish              = "validation.finish"
	CommandValidationStatus              = "validation.status"
	CommandPublicationEvidenceRecord     = "validation.publication_evidence.record"
	CommandPublicationEvidenceInvalidate = "validation.publication_evidence.invalidate"
	CommandPublicationEvidenceStatus     = "validation.publication_evidence.status"
	CommandPublicationEvidenceEvaluate   = "validation.publication_evidence.evaluate"
)

type ValidationAcquireRequest struct {
	RequestID              string                    `json:"request_id"`
	LeaseToken             string                    `json:"lease_token"`
	IssueID                string                    `json:"issue_id"`
	Class                  domain.ValidationClass    `json:"class"`
	Scope                  domain.ValidationScope    `json:"scope"`
	Purpose                domain.ValidationPurpose  `json:"purpose"`
	IsolationMode          string                    `json:"isolation_mode"`
	EnvironmentFingerprint string                    `json:"environment_fingerprint"`
	Override               domain.ValidationOverride `json:"override"`
	OverrideActor          string                    `json:"override_actor,omitempty"`
	OverrideReason         string                    `json:"override_reason,omitempty"`
	Profile                string                    `json:"profile"`
	Command                string                    `json:"command"`
	SourceRevision         string                    `json:"source_revision"`
	ReviewerID             string                    `json:"reviewer_id,omitempty"`
	TTLSeconds             int                       `json:"ttl_seconds,omitempty"`
}

type ValidationHeartbeatRequest struct {
	RequestID  string `json:"request_id"`
	LeaseToken string `json:"lease_token"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type ValidationAuthorizeNestedRequest struct {
	RequestID  string                   `json:"request_id"`
	LeaseToken string                   `json:"lease_token"`
	Class      domain.ValidationClass   `json:"class"`
	Scope      domain.ValidationScope   `json:"scope"`
	Purpose    domain.ValidationPurpose `json:"purpose"`
}

type ValidationFinishRequest struct {
	RequestID  string                        `json:"request_id"`
	LeaseToken string                        `json:"lease_token"`
	State      domain.ValidationRequestState `json:"state"`
	Outcome    string                        `json:"outcome,omitempty"`
	Evidence   domain.ValidationEvidence     `json:"evidence"`
}

type ValidationStatusRequest struct{}

type ValidationRequestResponse struct {
	Request domain.ValidationRequest `json:"request"`
}

type ValidationStatusResponse struct {
	Snapshot domain.ValidationSnapshot `json:"snapshot"`
}

type PublicationEvidenceRecordRequest struct {
	Evidence domain.PublicationEvidence `json:"evidence"`
}

type PublicationEvidenceRecordResponse struct {
	Evidence domain.PublicationEvidence `json:"evidence"`
}

type PublicationEvidenceInvalidateRequest struct {
	Invalidation domain.PublicationEvidenceInvalidation `json:"invalidation"`
}

type PublicationEvidenceInvalidateResponse struct {
	Invalidation domain.PublicationEvidenceInvalidation `json:"invalidation"`
}

type PublicationEvidenceStatusRequest struct {
	IssueID string `json:"issue_id,omitempty"`
}

type PublicationEvidenceStatusResponse struct {
	Snapshot domain.PublicationEvidenceSnapshot `json:"snapshot"`
}

type PublicationEvidenceEvaluateRequest struct {
	IssueID   string                              `json:"issue_id"`
	Candidate domain.PublicationEvidenceCandidate `json:"candidate"`
	Policy    domain.PublicationEvidencePolicy    `json:"policy"`
}

type PublicationEvidenceEvaluateResponse struct {
	Snapshot    domain.PublicationEvidenceSnapshot     `json:"snapshot"`
	Assessments []domain.PublicationEvidenceAssessment `json:"assessments"`
}
