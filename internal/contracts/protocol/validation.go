package protocol

import "github.com/riordanpawley/azedarach/internal/domain"

const ValidationArtifactReadMaxBytes = 1 << 20

const (
	CommandValidationAcquire           = "validation.acquire"
	CommandValidationHeartbeat         = "validation.heartbeat"
	CommandValidationNested            = "validation.authorize_nested"
	CommandValidationFinish            = "validation.finish"
	CommandValidationStatus            = "validation.status"
	CommandValidationArtifactRead      = "validation.artifact.read"
	CommandPublicationEvidenceRecord   = "validation.publication_evidence.record"
	CommandPublicationEvidenceStatus   = "validation.publication_evidence.status"
	CommandPublicationEvidenceEvaluate = "validation.publication_evidence.evaluate"
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

type ValidationArtifactReadRequest struct {
	Reference string `json:"reference"`
	Offset    int64  `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ValidationArtifactReadResponse struct {
	Reference  string `json:"reference"`
	Digest     string `json:"digest"`
	Content    []byte `json:"content"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	TotalSize  int64  `json:"total_size"`
	Complete   bool   `json:"complete"`
}

type ValidationRequestResponse struct {
	Request domain.ValidationRequest     `json:"request"`
	Context domain.WorkflowContextPacket `json:"context_packet"`
	Summary domain.WorkflowResultSummary `json:"summary"`
}

type ValidationStatusResponse struct {
	Snapshot domain.ValidationSnapshot `json:"snapshot"`
}

type PublicationEvidenceRecordRequest struct {
	EvidenceID           string                          `json:"evidence_id"`
	IssueID              string                          `json:"issue_id"`
	Layer                domain.PublicationEvidenceLayer `json:"layer"`
	ValidationRequestID  string                          `json:"validation_request_id,omitempty"`
	ReusedFromEvidenceID string                          `json:"reused_from_evidence_id,omitempty"`
}

type PublicationEvidenceRecordResponse struct {
	Evidence domain.PublicationEvidence `json:"evidence"`
}

type PublicationEvidenceStatusRequest struct {
	IssueID string `json:"issue_id,omitempty"`
}

type PublicationEvidenceStatusResponse struct {
	Snapshot    domain.PublicationEvidenceSnapshot     `json:"snapshot"`
	Assessments []domain.PublicationEvidenceAssessment `json:"assessments,omitempty"`
}

type PublicationEvidenceEvaluateRequest struct {
	IssueID string `json:"issue_id"`
}

type PublicationEvidenceEvaluateResponse struct {
	Snapshot    domain.PublicationEvidenceSnapshot     `json:"snapshot"`
	Assessments []domain.PublicationEvidenceAssessment `json:"assessments"`
}
