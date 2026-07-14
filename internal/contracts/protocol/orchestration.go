package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	CommandOrchestrationSnapshot     = "orchestration.snapshot"
	CommandOrchestrationIntent       = "orchestration.intent"
	CommandOrchestratorSessionStart  = "orchestration.session.start"
	CommandOrchestratorSessionAttach = "orchestration.session.attach"
	CommandOrchestratorSessionStop   = "orchestration.session.stop"
	CommandOrchestratorSessionStatus = "orchestration.session.status"
	EventOrchestrationLoopUpdated    = "orchestration.loop.updated"
)

type OrchestrationLoopEventBody struct {
	Scope        domain.OrchestrationScope `json:"scope" msgpack:"scope"`
	WatchCursor  int64                     `json:"watch_cursor" msgpack:"watch_cursor"`
	ActionKey    string                    `json:"action_key" msgpack:"action_key"`
	ActionKind   string                    `json:"action_kind" msgpack:"action_kind"`
	ActionStatus string                    `json:"action_status" msgpack:"action_status"`
	UpdatedAt    time.Time                 `json:"updated_at" msgpack:"updated_at"`
}

type OrchestratorSessionRequest struct {
	Scope             domain.OrchestrationScope `json:"scope"`
	ExpectedSessionID string                    `json:"expected_session_id,omitempty"`
}

type OrchestratorSessionResult struct {
	Scope       domain.OrchestrationScope    `json:"scope"`
	SessionID   string                       `json:"session_id,omitempty"`
	Disposition string                       `json:"disposition,omitempty"`
	Lifecycle   domain.OrchestratorLifecycle `json:"lifecycle,omitempty"`
	Live        bool                         `json:"live"`
	Forced      bool                         `json:"forced,omitempty"`
}

type OrchestrationSnapshotRequest struct {
	Scope domain.OrchestrationScope `json:"scope"`
	// SessionID lets presentation clients recover the daemon-persisted role and
	// scope without reconstructing authority from environment variables.
	SessionID      string `json:"session_id,omitempty"`
	ActorID        string `json:"actor_id,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	ObservedCursor int64  `json:"observed_cursor,omitempty"`
	RepoDir        string `json:"repo_dir,omitempty"`
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
	ReviewQueue            []OrchestrationReview        `json:"review_queue,omitempty"`
	Health                 OrchestrationHealth          `json:"health"`
	Completion             OrchestrationCompletion      `json:"completion"`
}

type OrchestrationCompletion struct {
	Scope   domain.OrchestrationScope    `json:"scope"`
	State   domain.OrchestratorLifecycle `json:"state,omitempty"`
	Pass    bool                         `json:"pass"`
	Reasons []string                     `json:"reasons,omitempty"`
}

type OrchestrationConstraints struct {
	InspectLimit   int      `json:"inspect_limit"`
	StartLimit     int      `json:"start_limit"`
	AgentCapacity  int      `json:"agent_capacity"`
	Commands       []string `json:"commands"`
	RoleGuardrails []string `json:"role_guardrails"`
}

// OrchestrationReview is the bounded daemon-owned inspection packet for one
// review-ready issue. It carries the worker's structured closeout evidence and
// enough branch metadata for the orchestrator to inspect the actual diff.
type OrchestrationReview struct {
	IssueID            string                         `json:"issue_id"`
	ParentIssueID      string                         `json:"parent_issue_id,omitempty"`
	Actionable         bool                           `json:"actionable"`
	Reasons            []string                       `json:"reasons,omitempty"`
	Evidence           *domain.WorkerEvidencePacket   `json:"evidence,omitempty"`
	ContextRisk        *domain.IssueContextRiskPacket `json:"context_risk,omitempty"`
	EvidenceSource     string                         `json:"evidence_source,omitempty"`
	WorktreePath       string                         `json:"worktree_path,omitempty"`
	Branch             string                         `json:"branch,omitempty"`
	BaseBranch         string                         `json:"base_branch,omitempty"`
	DiffStat           string                         `json:"diff_stat,omitempty"`
	ExecutionOwner     string                         `json:"execution_owner,omitempty"`
	OrchestrationOwner string                         `json:"orchestration_owner,omitempty"`
	ReviewOwner        string                         `json:"review_owner,omitempty"`
}

type OrchestrationCandidate struct {
	IssueID          string                              `json:"issue_id"`
	Included         bool                                `json:"included"`
	Eligible         bool                                `json:"eligible"`
	Sufficient       bool                                `json:"sufficient"`
	Classification   string                              `json:"classification"`
	Reason           string                              `json:"reason"`
	Sufficiency      []string                            `json:"sufficiency_signals"`
	ExclusionReasons []string                            `json:"exclusion_reasons,omitempty"`
	Executability    domain.IssueExecutabilityAssessment `json:"executability"`
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

const (
	OrchestrationIntentStart        OrchestrationIntentKind = "start"
	OrchestrationIntentReviewReturn OrchestrationIntentKind = "review-return"
	OrchestrationIntentReviewAccept OrchestrationIntentKind = "review-accept"
)

type OrchestrationReviewFinding struct {
	Severity     string   `json:"severity"`
	File         string   `json:"file,omitempty"`
	Line         int      `json:"line,omitempty"`
	Finding      string   `json:"finding"`
	SuggestedFix string   `json:"suggested_fix,omitempty"`
	Validation   []string `json:"validation,omitempty"`
}

type OrchestrationIntentRequest struct {
	Scope               domain.OrchestrationScope            `json:"scope"`
	Kind                OrchestrationIntentKind              `json:"kind"`
	IntentKey           string                               `json:"intent_key"`
	ActorID             string                               `json:"actor_id,omitempty"`
	IssueIDs            []string                             `json:"issue_ids,omitempty"`
	Limit               int                                  `json:"limit,omitempty"`
	RepoDir             string                               `json:"repo_dir,omitempty"`
	BaseBranch          string                               `json:"base_branch,omitempty"`
	OverrideBoardHealth bool                                 `json:"override_board_health,omitempty"`
	Findings            []OrchestrationReviewFinding         `json:"findings,omitempty"`
	RestartWorker       bool                                 `json:"restart_worker,omitempty"`
	Routes              []domain.OrchestrationCandidateRoute `json:"routes,omitempty"`
}

type OrchestrationIntentResult struct {
	Scope     domain.OrchestrationScope  `json:"scope"`
	Kind      OrchestrationIntentKind    `json:"kind"`
	IntentKey string                     `json:"intent_key"`
	Revision  uint64                     `json:"revision"`
	Requested []string                   `json:"requested"`
	Started   []string                   `json:"started,omitempty"`
	Returned  []string                   `json:"returned,omitempty"`
	Closed    []string                   `json:"closed,omitempty"`
	Launched  []OrchestrationLaunch      `json:"launched,omitempty"`
	Pending   []OrchestrationPending     `json:"pending,omitempty"`
	Routed    []OrchestrationRouteResult `json:"routed,omitempty"`
	Skipped   map[string]string          `json:"skipped,omitempty"`
	Failed    map[string]string          `json:"failed,omitempty"`
}

type OrchestrationRouteResult struct {
	IssueID            string                        `json:"issue_id"`
	Kind               domain.OrchestrationRouteKind `json:"kind"`
	Reason             string                        `json:"reason"`
	MissingDetails     []string                      `json:"missing_details,omitempty"`
	InteractionID      string                        `json:"interaction_id,omitempty"`
	InteractionCreated bool                          `json:"interaction_created,omitempty"`
}

type OrchestrationLaunch struct {
	IssueID        string `json:"issue_id"`
	SessionID      string `json:"session_id,omitempty"`
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
}
