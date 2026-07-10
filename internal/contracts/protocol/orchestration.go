package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	CommandOrchestrationSnapshot = "orchestration.snapshot"
	CommandOrchestrationIntent   = "orchestration.intent"
)

type OrchestrationSnapshotRequest struct {
	Scope domain.OrchestrationScope `json:"scope"`
	// SessionID lets presentation clients recover the daemon-persisted role and
	// scope without reconstructing authority from environment variables.
	SessionID      string `json:"session_id,omitempty"`
	ActorID        string `json:"actor_id,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	ObservedCursor int64  `json:"observed_cursor,omitempty"`
}

type OrchestrationSnapshot struct {
	Role                   string                       `json:"role,omitempty"`
	SessionID              string                       `json:"session_id,omitempty"`
	Lifecycle              domain.OrchestratorLifecycle `json:"lifecycle,omitempty"`
	Scope                  domain.OrchestrationScope    `json:"scope"`
	Revision               uint64                       `json:"revision"`
	GeneratedAt            time.Time                    `json:"generated_at"`
	Roots                  []string                     `json:"roots,omitempty"`
	Capacity               OrchestrationCapacity        `json:"capacity"`
	Runnable               []string                     `json:"runnable"`
	NestedRoots            []OrchestrationNestedRoot    `json:"nested_roots,omitempty"`
	Pending                []OrchestrationPending       `json:"pending,omitempty"`
	Active                 []string                     `json:"active,omitempty"`
	ActiveSessions         []OrchestrationSession       `json:"active_sessions,omitempty"`
	SessionStartProgress   []OrchestrationProgress      `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []OrchestrationCloseable     `json:"stale_closeable_children,omitempty"`
	ContainmentRisks       []OrchestrationRisk          `json:"containment_risks,omitempty"`
	WorkerObservations     []domain.WorkerObservation   `json:"worker_observations,omitempty"`
	Blocked                map[string]string            `json:"blocked"`
	Candidates             []OrchestrationCandidate     `json:"candidates,omitempty"`
	Reviews                []OrchestrationCandidate     `json:"reviews,omitempty"`
	OwnershipConflicts     []OrchestrationCandidate     `json:"ownership_conflicts,omitempty"`
	Interactions           []domain.InteractionRequest  `json:"interactions,omitempty"`
	RecentEvents           []MailEvent                  `json:"recent_events,omitempty"`
	Cursor                 int64                        `json:"cursor,omitempty"`
	ContinuationRequired   bool                         `json:"continuation_required,omitempty"`
	ContinuationReason     string                       `json:"continuation_reason,omitempty"`
	ContinuationContract   string                       `json:"continuation_contract,omitempty"`
	Constraints            OrchestrationConstraints     `json:"constraints"`
	Health                 OrchestrationHealth          `json:"health"`
}

type OrchestrationConstraints struct {
	InspectLimit   int      `json:"inspect_limit"`
	StartLimit     int      `json:"start_limit"`
	AgentCapacity  int      `json:"agent_capacity"`
	Commands       []string `json:"commands"`
	RoleGuardrails []string `json:"role_guardrails"`
}

type OrchestrationCandidate struct {
	IssueID          string   `json:"issue_id"`
	Included         bool     `json:"included"`
	Eligible         bool     `json:"eligible"`
	Sufficient       bool     `json:"sufficient"`
	Classification   string   `json:"classification"`
	Reason           string   `json:"reason"`
	Sufficiency      []string `json:"sufficiency_signals"`
	ExclusionReasons []string `json:"exclusion_reasons,omitempty"`
}

type OrchestrationHealth struct {
	Healthy        bool     `json:"healthy"`
	OpenIssueCount int      `json:"open_issue_count"`
	InspectedCount int      `json:"inspected_count"`
	InspectLimit   int      `json:"inspect_limit"`
	OpenIssueLimit int      `json:"open_issue_limit"`
	Diagnostics    []string `json:"diagnostics,omitempty"`
}

type OrchestrationCapacity struct {
	DirectRunnableCount        int `json:"direct_runnable_count"`
	DirectActiveCount          int `json:"direct_active_count"`
	NestedStartableCount       int `json:"nested_startable_count"`
	NestedActiveCount          int `json:"nested_active_count"`
	PendingStartsCount         int `json:"pending_starts_count"`
	BlockedNestedRootsCount    int `json:"blocked_nested_roots_count"`
	NotCountingCapacityCount   int `json:"not_counting_capacity_count"`
	TotalCountingCapacityCount int `json:"total_counting_capacity_count"`
}

type OrchestrationNestedRoot struct {
	IssueID        string                     `json:"issue_id"`
	Status         string                     `json:"status"`
	IssueStatus    string                     `json:"issue_status,omitempty"`
	Type           string                     `json:"type"`
	ChildCount     int                        `json:"child_count"`
	ActiveSession  *OrchestrationSession      `json:"active_session,omitempty"`
	StartFailure   *OrchestrationStartFailure `json:"start_failure,omitempty"`
	FallbackPolicy string                     `json:"fallback_policy,omitempty"`
	Advice         string                     `json:"advice,omitempty"`
}

type OrchestrationStartFailure struct {
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
	Message        string `json:"message,omitempty"`
}
type OrchestrationPending struct {
	IssueID        string `json:"issue_id"`
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
}
type OrchestrationSession struct {
	IssueID           string                 `json:"issue_id"`
	Activity          string                 `json:"activity"`
	ActivitySource    string                 `json:"activity_source"`
	State             string                 `json:"state,omitempty"`
	Status            string                 `json:"status,omitempty"`
	TmuxAttachedCount int                    `json:"tmux_attached_count,omitempty"`
	StartProgress     *OrchestrationProgress `json:"start_progress,omitempty"`
	Advice            string                 `json:"advice,omitempty"`
}
type OrchestrationProgress struct {
	IssueID        string     `json:"issue_id"`
	OperationID    string     `json:"operation_id,omitempty"`
	OperationState string     `json:"operation_state"`
	Phase          string     `json:"phase,omitempty"`
	Message        string     `json:"message,omitempty"`
	Percent        int        `json:"percent,omitempty"`
	ElapsedMS      int64      `json:"elapsed_ms,omitempty"`
	EnqueuedAt     time.Time  `json:"enqueued_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}
type OrchestrationCloseable struct {
	IssueID          string   `json:"issue_id"`
	Status           string   `json:"status"`
	Evidence         []string `json:"evidence"`
	SuggestedCommand string   `json:"suggested_command"`
}
type OrchestrationRisk struct {
	IssueID                string   `json:"issue_id"`
	ActiveBranch           string   `json:"active_branch,omitempty"`
	RootIssueID            string   `json:"root_issue_id"`
	RootBranch             string   `json:"root_branch,omitempty"`
	ClosedChildIssueID     string   `json:"closed_child_issue_id"`
	EvidenceCommit         string   `json:"evidence_commit"`
	EvidenceSubject        string   `json:"evidence_subject,omitempty"`
	RootContainsEvidence   bool     `json:"root_contains_evidence"`
	ActiveContainsEvidence bool     `json:"active_contains_evidence"`
	Classification         string   `json:"classification"`
	Message                string   `json:"message"`
	ChangedFiles           []string `json:"changed_files,omitempty"`
	OverlapFiles           []string `json:"overlap_files,omitempty"`
	SuggestedCommand       string   `json:"suggested_command,omitempty"`
}

type OrchestrationIntentKind string

const OrchestrationIntentStart OrchestrationIntentKind = "start"

type OrchestrationIntentRequest struct {
	Scope               domain.OrchestrationScope `json:"scope"`
	Kind                OrchestrationIntentKind   `json:"kind"`
	IntentKey           string                    `json:"intent_key"`
	ActorID             string                    `json:"actor_id,omitempty"`
	IssueIDs            []string                  `json:"issue_ids,omitempty"`
	Limit               int                       `json:"limit,omitempty"`
	RepoDir             string                    `json:"repo_dir,omitempty"`
	BaseBranch          string                    `json:"base_branch,omitempty"`
	OverrideBoardHealth bool                      `json:"override_board_health,omitempty"`
}

type OrchestrationIntentResult struct {
	Scope     domain.OrchestrationScope `json:"scope"`
	Kind      OrchestrationIntentKind   `json:"kind"`
	IntentKey string                    `json:"intent_key"`
	Revision  uint64                    `json:"revision"`
	Requested []string                  `json:"requested"`
	Started   []string                  `json:"started,omitempty"`
	Launched  []OrchestrationLaunch     `json:"launched,omitempty"`
	Pending   []OrchestrationPending    `json:"pending,omitempty"`
	Skipped   map[string]string         `json:"skipped,omitempty"`
	Failed    map[string]string         `json:"failed,omitempty"`
}

type OrchestrationLaunch struct {
	IssueID        string `json:"issue_id"`
	SessionID      string `json:"session_id,omitempty"`
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
}
