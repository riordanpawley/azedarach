package domain

import (
	"fmt"
	"strings"
	"time"
)

type ValidationClass string

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
	Sequence       int64                  `json:"sequence"`
	RequestID      string                 `json:"request_id"`
	ProjectID      string                 `json:"project_id"`
	IssueID        string                 `json:"issue_id"`
	Class          ValidationClass        `json:"class"`
	Profile        string                 `json:"profile"`
	Command        string                 `json:"command"`
	SourceRevision string                 `json:"source_revision"`
	State          ValidationRequestState `json:"state"`
	QueuedAt       time.Time              `json:"queued_at"`
	StartedAt      *time.Time             `json:"started_at,omitempty"`
	HeartbeatAt    *time.Time             `json:"heartbeat_at,omitempty"`
	ExpiresAt      *time.Time             `json:"expires_at,omitempty"`
	FinishedAt     *time.Time             `json:"finished_at,omitempty"`
	Outcome        string                 `json:"outcome,omitempty"`
	Evidence       ValidationEvidence     `json:"evidence"`
}

type ValidationEvidence struct {
	Held                bool            `json:"held"`
	RequestID           string          `json:"request_id,omitempty"`
	Class               ValidationClass `json:"class,omitempty"`
	Profile             string          `json:"profile,omitempty"`
	Present             bool            `json:"present"`
	ReportPath          string          `json:"report_path,omitempty"`
	ReportPaths         []string        `json:"report_paths,omitempty"`
	OverlapDetected     bool            `json:"overlap_detected"`
	ExternalGoProcesses int             `json:"external_go_processes"`
}

type ValidationSnapshot struct {
	Schema   string              `json:"schema"`
	Active   []ValidationRequest `json:"active"`
	Queued   []ValidationRequest `json:"queued"`
	Recent   []ValidationRequest `json:"recent,omitempty"`
	Revision int64               `json:"revision"`
}

type ValidationAcquire struct {
	RequestID      string
	LeaseToken     string
	ProjectID      string
	IssueID        string
	Class          ValidationClass
	Profile        string
	Command        string
	SourceRevision string
	TTL            time.Duration
}

func (a ValidationAcquire) Validate() error {
	if strings.TrimSpace(a.RequestID) == "" || strings.TrimSpace(a.LeaseToken) == "" || strings.TrimSpace(a.ProjectID) == "" || strings.TrimSpace(a.IssueID) == "" {
		return fmt.Errorf("validation request requires request id, lease token, project id, and issue id")
	}
	if !a.Class.Valid() {
		return fmt.Errorf("unsupported validation class %q", a.Class)
	}
	if strings.TrimSpace(a.Profile) == "" || strings.TrimSpace(a.Command) == "" || strings.TrimSpace(a.SourceRevision) == "" {
		return fmt.Errorf("validation request requires profile, command, and source revision")
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
