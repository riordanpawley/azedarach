package protocol

import "github.com/riordanpawley/azedarach/internal/domain"

const (
	CommandValidationAcquire   = "validation.acquire"
	CommandValidationHeartbeat = "validation.heartbeat"
	CommandValidationNested    = "validation.authorize_nested"
	CommandValidationFinish    = "validation.finish"
	CommandValidationStatus    = "validation.status"
)

type ValidationAcquireRequest struct {
	RequestID      string                   `json:"request_id"`
	LeaseToken     string                   `json:"lease_token"`
	IssueID        string                   `json:"issue_id"`
	Class          domain.ValidationClass   `json:"class"`
	Scope          domain.ValidationScope   `json:"scope"`
	Purpose        domain.ValidationPurpose `json:"purpose"`
	Profile        string                   `json:"profile"`
	Command        string                   `json:"command"`
	SourceRevision string                   `json:"source_revision"`
	ReviewerID     string                   `json:"reviewer_id,omitempty"`
	TTLSeconds     int                      `json:"ttl_seconds,omitempty"`
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
