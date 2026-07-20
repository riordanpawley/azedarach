package domain

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ValidationClass string

type ValidationScope string
type ValidationPurpose string
type ValidationExecution string
type ValidationOverride string

const (
	ValidationScopeRepository       ValidationScope     = "repository"
	ValidationScopeTicket           ValidationScope     = "ticket"
	ValidationPurposeCapacity       ValidationPurpose   = "capacity"
	ValidationPurposeDevelopment    ValidationPurpose   = "development"
	ValidationPurposePushGate       ValidationPurpose   = "push_gate"
	ValidationPurposeReviewEvidence ValidationPurpose   = "review_evidence"
	ValidationPurposeLegacy         ValidationPurpose   = "legacy"
	ValidationExecutionExecuted     ValidationExecution = "executed"
	ValidationExecutionJoined       ValidationExecution = "joined"
	ValidationExecutionReused       ValidationExecution = "reused"
	ValidationExecutionSkipped      ValidationExecution = "skipped"
	ValidationOverrideNone          ValidationOverride  = "none"
	ValidationOverrideNoReuse       ValidationOverride  = "no_reuse"
	ValidationOverrideForceRerun    ValidationOverride  = "force_rerun"
	ValidationOverrideEmergency     ValidationOverride  = "emergency_skip"
)

func (s ValidationScope) Valid() bool {
	return s == ValidationScopeRepository || s == ValidationScopeTicket
}
func (p ValidationPurpose) Valid() bool {
	return p == ValidationPurposeCapacity || p == ValidationPurposeDevelopment || p == ValidationPurposePushGate || p == ValidationPurposeReviewEvidence
}
func (o ValidationOverride) Valid() bool {
	return o == ValidationOverrideNone || o == ValidationOverrideNoReuse || o == ValidationOverrideForceRerun || o == ValidationOverrideEmergency
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
type ValidationOrderingReason string

const (
	ValidationRequestQueued    ValidationRequestState = "queued"
	ValidationRequestActive    ValidationRequestState = "active"
	ValidationRequestCompleted ValidationRequestState = "completed"
	ValidationRequestCancelled ValidationRequestState = "cancelled"
	ValidationRequestExpired   ValidationRequestState = "expired"
	ValidationRequestFailed    ValidationRequestState = "failed"
)

const (
	ValidationOrderingPriorityFIFO    ValidationOrderingReason = "priority_fifo"
	ValidationOrderingBoundedFairness ValidationOrderingReason = "bounded_fairness"
	ValidationOrderingPublication     ValidationOrderingReason = "publication_immediate"
	ValidationOrderingSafe            ValidationOrderingReason = "safe_parallel"
	ValidationOrderingAggregate       ValidationOrderingReason = "aggregate_exclusive"
	ValidationOrderingShared          ValidationOrderingReason = "shared_capacity"
	ValidationOrderingJoinedSource    ValidationOrderingReason = "joined_source"

	ValidationPriorityBypassLimit = 1
)

type ValidationRequest struct {
	Sequence                       int64                    `json:"sequence"`
	RequestID                      string                   `json:"request_id"`
	ProjectID                      string                   `json:"project_id"`
	IssueID                        string                   `json:"issue_id"`
	IssuePriority                  Priority                 `json:"issue_priority"`
	PriorityBypassCount            int                      `json:"priority_bypass_count"`
	Class                          ValidationClass          `json:"class"`
	Scope                          ValidationScope          `json:"scope"`
	Purpose                        ValidationPurpose        `json:"purpose"`
	Execution                      ValidationExecution      `json:"execution"`
	AuthoritativeRequestID         string                   `json:"authoritative_request_id,omitempty"`
	CompatibilityKey               string                   `json:"compatibility_key"`
	IsolationMode                  string                   `json:"isolation_mode"`
	EnvironmentFingerprint         string                   `json:"environment_fingerprint"`
	Override                       ValidationOverride       `json:"override"`
	OverrideActor                  string                   `json:"override_actor,omitempty"`
	OverrideReason                 string                   `json:"override_reason,omitempty"`
	Profile                        string                   `json:"profile"`
	Command                        string                   `json:"command"`
	SourceRevision                 string                   `json:"source_revision"`
	ReviewerID                     string                   `json:"reviewer_id,omitempty"`
	ReviewerKind                   string                   `json:"reviewer_kind,omitempty"`
	ReviewEpochEventID             int64                    `json:"review_epoch_event_id,omitempty"`
	PublicationOperationID         string                   `json:"publication_operation_id,omitempty"`
	AcceptedReviewEventID          int64                    `json:"accepted_review_event_id,omitempty"`
	AcceptedPublicationOperationID string                   `json:"accepted_publication_operation_id,omitempty"`
	State                          ValidationRequestState   `json:"state"`
	QueuedAt                       time.Time                `json:"queued_at"`
	StartedAt                      *time.Time               `json:"started_at,omitempty"`
	HeartbeatAt                    *time.Time               `json:"heartbeat_at,omitempty"`
	ExpiresAt                      *time.Time               `json:"expires_at,omitempty"`
	FinishedAt                     *time.Time               `json:"finished_at,omitempty"`
	Outcome                        string                   `json:"outcome,omitempty"`
	Evidence                       ValidationEvidence       `json:"evidence"`
	QueuePosition                  int                      `json:"queue_position,omitempty"`
	OrderingReason                 ValidationOrderingReason `json:"ordering_reason,omitempty"`
}

type ValidationEvidence struct {
	Held                   bool                      `json:"held"`
	RequestID              string                    `json:"request_id,omitempty"`
	Class                  ValidationClass           `json:"class,omitempty"`
	Scope                  ValidationScope           `json:"scope,omitempty"`
	Purpose                ValidationPurpose         `json:"purpose,omitempty"`
	Execution              ValidationExecution       `json:"execution,omitempty"`
	AuthoritativeRequestID string                    `json:"authoritative_request_id,omitempty"`
	Profile                string                    `json:"profile,omitempty"`
	SourceRevision         string                    `json:"source_revision,omitempty"`
	Present                bool                      `json:"present"`
	ReportPath             string                    `json:"report_path,omitempty"`
	ReportPaths            []string                  `json:"report_paths,omitempty"`
	FailureSummary         string                    `json:"failure_summary,omitempty"`
	OverlapDetected        bool                      `json:"overlap_detected"`
	ExternalGoProcesses    int                       `json:"external_go_processes"`
	Stages                 []ValidationStageEvidence `json:"stages,omitempty"`
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

type ValidationSnapshotFreshness string

const (
	ValidationSnapshotFresh       ValidationSnapshotFreshness = "fresh"
	ValidationSnapshotStale       ValidationSnapshotFreshness = "stale"
	ValidationSnapshotUnavailable ValidationSnapshotFreshness = "unavailable"
)

type ValidationSnapshot struct {
	Schema         string                      `json:"schema"`
	Active         []ValidationRequest         `json:"active"`
	Queued         []ValidationRequest         `json:"queued"`
	Recent         []ValidationRequest         `json:"recent,omitempty"`
	Revision       int64                       `json:"revision"`
	Freshness      ValidationSnapshotFreshness `json:"freshness"`
	ObservedAt     time.Time                   `json:"observed_at"`
	DegradedReason string                      `json:"degraded_reason,omitempty"`
}

type ValidationAcquire struct {
	RequestID                      string
	LeaseToken                     string
	ProjectID                      string
	IssueID                        string
	IssuePriority                  Priority
	IssuePriorityResolved          bool
	Class                          ValidationClass
	Scope                          ValidationScope
	Purpose                        ValidationPurpose
	IsolationMode                  string
	EnvironmentFingerprint         string
	Override                       ValidationOverride
	OverrideActor                  string
	OverrideReason                 string
	Profile                        string
	Command                        string
	SourceRevision                 string
	ReviewerID                     string
	ReviewerKind                   string
	ReviewEpochEventID             int64
	PublicationOperationID         string
	AcceptedReviewEventID          int64
	AcceptedPublicationOperationID string
	TTL                            time.Duration
}

func (a ValidationAcquire) Validate() error {
	if strings.TrimSpace(a.RequestID) == "" || strings.TrimSpace(a.LeaseToken) == "" || strings.TrimSpace(a.ProjectID) == "" {
		return fmt.Errorf("validation request requires request id, lease token, and project id")
	}
	if !a.Class.Valid() {
		return fmt.Errorf("unsupported validation class %q", a.Class)
	}
	if a.IssuePriority < P0 || a.IssuePriority > P4 {
		return fmt.Errorf("validation request requires issue priority P0 through P4")
	}
	if !a.Scope.Valid() || !a.Purpose.Valid() {
		return fmt.Errorf("validation request requires supported scope and purpose")
	}
	if strings.TrimSpace(a.IsolationMode) == "" || strings.TrimSpace(a.EnvironmentFingerprint) == "" {
		return fmt.Errorf("validation request requires isolation mode and environment fingerprint")
	}
	if !a.Override.Valid() {
		return fmt.Errorf("unsupported validation override %q", a.Override)
	}
	if a.Override == ValidationOverrideEmergency {
		if strings.TrimSpace(a.OverrideActor) == "" || strings.TrimSpace(a.OverrideReason) == "" {
			return fmt.Errorf("emergency validation skip requires explicit actor and reason")
		}
	} else if strings.TrimSpace(a.OverrideActor) != "" || strings.TrimSpace(a.OverrideReason) != "" {
		return fmt.Errorf("validation override actor and reason are reserved for emergency skip")
	}
	issueID := strings.TrimSpace(a.IssueID)
	if a.Scope == ValidationScopeRepository && issueID != "" {
		return fmt.Errorf("repository-scoped validation must not identify a ticket")
	}
	if a.Scope == ValidationScopeTicket && issueID == "" {
		return fmt.Errorf("ticket-scoped validation requires an existing ticket identity")
	}
	if a.Purpose == ValidationPurposeDevelopment {
		return fmt.Errorf("development validation does not use daemon admission")
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
	if a.Purpose == ValidationPurposeReviewEvidence {
		if _, err := CanonicalReviewerIdentity(a.ReviewerID, a.ReviewerKind); err != nil || strings.TrimSpace(a.PublicationOperationID) == "" || a.AcceptedReviewEventID <= 0 || strings.TrimSpace(a.AcceptedPublicationOperationID) == "" {
			return fmt.Errorf("review evidence requires typed reviewer and exact accepted publication operation binding")
		}
	}
	if a.Class == ValidationClassSafe && !validSafeValidation(a.Profile, a.Command) {
		return fmt.Errorf("safe validation requires a bounded non-compiling profile and command")
	}
	if a.TTL <= 0 {
		return fmt.Errorf("validation request TTL must be positive")
	}
	return nil
}

// OrderValidationQueue returns the effective priority-aware admission order.
// A request that has already been overtaken by the configured bound is chosen
// first by durable sequence; otherwise lower numeric priority wins with FIFO
// preserved inside each priority.
func OrderValidationQueue(requests []ValidationRequest) []ValidationRequest {
	ordered := append([]ValidationRequest(nil), requests...)
	sort.SliceStable(ordered, func(i, j int) bool {
		leftProtected := ordered[i].PriorityBypassCount >= ValidationPriorityBypassLimit
		rightProtected := ordered[j].PriorityBypassCount >= ValidationPriorityBypassLimit
		if leftProtected != rightProtected {
			return leftProtected
		}
		if leftProtected || ordered[i].IssuePriority == ordered[j].IssuePriority {
			return ordered[i].Sequence < ordered[j].Sequence
		}
		return ordered[i].IssuePriority < ordered[j].IssuePriority
	})
	for i := range ordered {
		ordered[i].QueuePosition = i + 1
		ordered[i].OrderingReason = ValidationOrderingPriorityFIFO
		if ordered[i].PriorityBypassCount >= ValidationPriorityBypassLimit {
			ordered[i].OrderingReason = ValidationOrderingBoundedFairness
		}
	}
	return ordered
}

func (a ValidationAcquire) CompatibilityKey() string {
	identity := strings.Join([]string{
		strings.TrimSpace(a.ProjectID), string(a.Class), strings.TrimSpace(a.Profile),
		strings.Join(strings.Fields(a.Command), " "), strings.TrimSpace(a.SourceRevision),
		strings.TrimSpace(a.IsolationMode), strings.TrimSpace(a.EnvironmentFingerprint),
	}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

func ValidationRequestCanSatisfy(source ValidationRequest, target ValidationAcquire) bool {
	if source.ProjectID != target.ProjectID || source.Class != target.Class || source.Profile != target.Profile || strings.Join(strings.Fields(source.Command), " ") != strings.Join(strings.Fields(target.Command), " ") || source.SourceRevision != target.SourceRevision || source.IsolationMode != target.IsolationMode || source.EnvironmentFingerprint != target.EnvironmentFingerprint {
		return false
	}
	if target.Scope == ValidationScopeRepository && target.Purpose == ValidationPurposePushGate {
		return (source.Scope == ValidationScopeRepository && source.Purpose == ValidationPurposePushGate) ||
			(source.Scope == ValidationScopeTicket && source.Purpose == ValidationPurposeReviewEvidence && source.HasCompleteReviewAuthority())
	}
	if source.Scope != target.Scope || source.Purpose != target.Purpose || source.IssueID != target.IssueID {
		return false
	}
	if target.Purpose != ValidationPurposeReviewEvidence {
		return source.ReviewerID == target.ReviewerID && source.ReviewerKind == target.ReviewerKind && source.ReviewEpochEventID == target.ReviewEpochEventID && source.PublicationOperationID == target.PublicationOperationID && source.AcceptedReviewEventID == target.AcceptedReviewEventID && source.AcceptedPublicationOperationID == target.AcceptedPublicationOperationID
	}
	sourceReviewer, sourceOK := source.reviewAuthority()
	targetReviewer, targetErr := CanonicalReviewerIdentity(target.ReviewerID, target.ReviewerKind)
	return sourceOK && targetErr == nil && sourceReviewer == targetReviewer && source.ReviewEpochEventID == target.ReviewEpochEventID && source.PublicationOperationID == target.PublicationOperationID && source.AcceptedReviewEventID == target.AcceptedReviewEventID && source.AcceptedPublicationOperationID == target.AcceptedPublicationOperationID
}

// HasCompleteReviewAuthority reports whether persisted validation evidence has
// every typed identity and immutable acceptance binding required to authorize
// publication. Legacy rows intentionally fail closed.
func (a ValidationRequest) HasCompleteReviewAuthority() bool {
	_, ok := a.reviewAuthority()
	return ok
}

func (a ValidationRequest) reviewAuthority() (ReviewerIdentity, bool) {
	reviewer, err := CanonicalReviewerIdentity(a.ReviewerID, a.ReviewerKind)
	return reviewer, err == nil && a.ReviewEpochEventID > 0 && strings.TrimSpace(a.PublicationOperationID) != "" && a.AcceptedReviewEventID > 0 && strings.TrimSpace(a.AcceptedPublicationOperationID) != ""
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
