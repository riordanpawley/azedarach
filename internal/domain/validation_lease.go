package domain

import (
	"fmt"
	"strings"
	"time"
)

type ValidationClass string

type ValidationScope string
type ValidationPurpose string

const (
	ValidationScopeRepository       ValidationScope   = "repository"
	ValidationScopeTicket           ValidationScope   = "ticket"
	ValidationPurposeCapacity       ValidationPurpose = "capacity"
	ValidationPurposeDevelopment    ValidationPurpose = "development"
	ValidationPurposePushGate       ValidationPurpose = "push_gate"
	ValidationPurposeReviewEvidence ValidationPurpose = "review_evidence"
	ValidationPurposeLegacy         ValidationPurpose = "legacy"
)

func (s ValidationScope) Valid() bool {
	return s == ValidationScopeRepository || s == ValidationScopeTicket
}
func (p ValidationPurpose) Valid() bool {
	return p == ValidationPurposeCapacity || p == ValidationPurposeDevelopment || p == ValidationPurposePushGate || p == ValidationPurposeReviewEvidence
}

const (
	ValidationClassAggregate ValidationClass = "aggregate"
	ValidationClassShared    ValidationClass = "shared"
	ValidationClassSafe      ValidationClass = "safe"
)

func (c ValidationClass) Valid() bool {
	return c == ValidationClassAggregate || c == ValidationClassShared || c == ValidationClassSafe
}

type ValidationRequestState string

const (
	ValidationRequestQueued    ValidationRequestState = "queued"
	ValidationRequestActive    ValidationRequestState = "active"
	ValidationRequestCompleted ValidationRequestState = "completed"
	ValidationRequestCancelled ValidationRequestState = "cancelled"
	ValidationRequestExpired   ValidationRequestState = "expired"
	ValidationRequestFailed    ValidationRequestState = "failed"
)

type ValidationRequest struct {
	Sequence           int64                  `json:"sequence"`
	RequestID          string                 `json:"request_id"`
	ProjectID          string                 `json:"project_id"`
	IssueID            string                 `json:"issue_id"`
	Class              ValidationClass        `json:"class"`
	Scope              ValidationScope        `json:"scope"`
	Purpose            ValidationPurpose      `json:"purpose"`
	Profile            string                 `json:"profile"`
	Command            string                 `json:"command"`
	SourceRevision     string                 `json:"source_revision"`
	ReviewerID         string                 `json:"reviewer_id,omitempty"`
	ReviewEpochEventID int64                  `json:"review_epoch_event_id,omitempty"`
	State              ValidationRequestState `json:"state"`
	QueuedAt           time.Time              `json:"queued_at"`
	StartedAt          *time.Time             `json:"started_at,omitempty"`
	HeartbeatAt        *time.Time             `json:"heartbeat_at,omitempty"`
	ExpiresAt          *time.Time             `json:"expires_at,omitempty"`
	FinishedAt         *time.Time             `json:"finished_at,omitempty"`
	Outcome            string                 `json:"outcome,omitempty"`
	Evidence           ValidationEvidence     `json:"evidence"`
}

type ValidationEvidence struct {
	Held                bool              `json:"held"`
	RequestID           string            `json:"request_id,omitempty"`
	Class               ValidationClass   `json:"class,omitempty"`
	Scope               ValidationScope   `json:"scope,omitempty"`
	Purpose             ValidationPurpose `json:"purpose,omitempty"`
	Profile             string            `json:"profile,omitempty"`
	SourceRevision      string            `json:"source_revision,omitempty"`
	Present             bool              `json:"present"`
	ReportPath          string            `json:"report_path,omitempty"`
	ReportPaths         []string          `json:"report_paths,omitempty"`
	OverlapDetected     bool              `json:"overlap_detected"`
	ExternalGoProcesses int               `json:"external_go_processes"`
}

type ValidationNestedAuthorization struct {
	RequestID  string
	LeaseToken string
	Class      ValidationClass
	Scope      ValidationScope
	Purpose    ValidationPurpose
}

func (a ValidationNestedAuthorization) Validate() error {
	if strings.TrimSpace(a.RequestID) == "" || strings.TrimSpace(a.LeaseToken) == "" {
		return fmt.Errorf("nested validation authorization requires request id and lease token")
	}
	if !a.Class.Valid() {
		return fmt.Errorf("unsupported nested validation class %q", a.Class)
	}
	if !a.Scope.Valid() || !a.Purpose.Valid() {
		return fmt.Errorf("nested validation authorization requires inherited scope and purpose")
	}
	return nil
}

type ValidationSnapshot struct {
	Schema   string              `json:"schema"`
	Active   []ValidationRequest `json:"active"`
	Queued   []ValidationRequest `json:"queued"`
	Recent   []ValidationRequest `json:"recent,omitempty"`
	Revision int64               `json:"revision"`
}

type ValidationAcquire struct {
	RequestID          string
	LeaseToken         string
	ProjectID          string
	IssueID            string
	Class              ValidationClass
	Scope              ValidationScope
	Purpose            ValidationPurpose
	Profile            string
	Command            string
	SourceRevision     string
	ReviewerID         string
	ReviewEpochEventID int64
	TTL                time.Duration
}

func (a ValidationAcquire) Validate() error {
	if strings.TrimSpace(a.RequestID) == "" || strings.TrimSpace(a.LeaseToken) == "" || strings.TrimSpace(a.ProjectID) == "" {
		return fmt.Errorf("validation request requires request id, lease token, and project id")
	}
	if !a.Class.Valid() {
		return fmt.Errorf("unsupported validation class %q", a.Class)
	}
	if !a.Scope.Valid() || !a.Purpose.Valid() {
		return fmt.Errorf("validation request requires supported scope and purpose")
	}
	issueID := strings.TrimSpace(a.IssueID)
	if a.Scope == ValidationScopeRepository && issueID != "" {
		return fmt.Errorf("repository-scoped validation must not identify a ticket")
	}
	if a.Scope == ValidationScopeTicket && issueID == "" {
		return fmt.Errorf("ticket-scoped validation requires an existing ticket identity")
	}
	if a.Purpose == ValidationPurposeReviewEvidence && a.Scope != ValidationScopeTicket {
		return fmt.Errorf("review evidence requires ticket scope")
	}
	if a.Purpose == ValidationPurposePushGate && a.Scope != ValidationScopeRepository {
		return fmt.Errorf("push gate validation requires repository scope")
	}
	if strings.TrimSpace(a.Profile) == "" || strings.TrimSpace(a.Command) == "" || strings.TrimSpace(a.SourceRevision) == "" {
		return fmt.Errorf("validation request requires profile, command, and source revision")
	}
	if (strings.TrimSpace(a.ReviewerID) == "") != (a.ReviewEpochEventID == 0) || a.ReviewEpochEventID < 0 {
		return fmt.Errorf("validation review assignment requires reviewer id and positive review epoch event id together")
	}
	if a.Purpose == ValidationPurposeReviewEvidence && (strings.TrimSpace(a.ReviewerID) == "" || a.ReviewEpochEventID <= 0) {
		return fmt.Errorf("review evidence requires reviewer identity and current review epoch")
	}
	if a.Class == ValidationClassSafe && !validSafeValidation(a.Profile, a.Command) {
		return fmt.Errorf("safe validation requires a bounded non-compiling profile and command")
	}
	if a.TTL <= 0 {
		return fmt.Errorf("validation request TTL must be positive")
	}
	return nil
}

func validSafeValidation(profile, command string) bool {
	profile = strings.TrimSpace(profile)
	command = strings.Join(strings.Fields(command), " ")
	switch profile {
	case "safe-git-diff":
		return command == "git diff --check"
	case "safe-noop":
		return command == "true"
	default:
		return false
	}
}
