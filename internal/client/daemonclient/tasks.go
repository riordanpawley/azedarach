package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/buildinfo"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandBoardFetch           = protocol.CommandBoardFetch
	CommandBoardViewList        = protocol.CommandBoardViewList
	CommandBoardViewGet         = protocol.CommandBoardViewGet
	CommandBoardViewSave        = protocol.CommandBoardViewSave
	CommandBoardViewDelete      = protocol.CommandBoardViewDelete
	CommandBoardViewSelect      = protocol.CommandBoardViewSelect
	CommandTaskList             = "task.list"
	CommandTaskGet              = "task.get"
	CommandTaskGetMany          = "task.get_many"
	CommandTaskEvents           = "task.events"
	CommandTaskEventAppend      = "task.event.append"
	CommandTaskCreate           = "task.create"
	CommandTaskClose            = "task.close"
	CommandTaskBulkCleanup      = protocol.CommandTaskBulkCleanup
	CommandTaskGraphReadiness   = "task.graph_readiness"
	CommandTaskCompleteCheck    = "task.complete_check"
	CommandTaskIntegrationReady = "task.integration_readiness"
	CommandTaskContextRisk      = "task.context_risk"
	CommandTaskMergeBaseTarget  = "task.merge_base_target"
	CommandTaskFollowOnMerge    = "task.follow_on_merge_candidates"
	CommandTaskClaimOwnership   = "task.ownership.claim"
	CommandTaskReleaseOwnership = "task.ownership.release"
	CommandTaskUpdateStatus     = "task.update_status"
	CommandTaskUpdate           = "task.update_details"
	CommandTaskAppendNotes      = "task.append_notes"
	CommandTaskDelete           = "task.delete"
	CommandTaskArchive          = "task.archive"
	CommandTaskUnarchive        = "task.unarchive"
	CommandTaskDependencyAdd    = "task.dependency.add"
	CommandTaskDependencyRemove = "task.dependency.remove"
	CommandTaskSQLiteWAL        = protocol.CommandTaskSQLiteWAL
	CommandSyncRun              = "sync.run"
	CommandSyncConflicts        = "sync.conflicts"
)

// TaskCreateParams contains the payload used to create a task through the shared daemon client.
type TaskCreateParams struct {
	Title           string               `json:"title"`
	Description     string               `json:"description"`
	Type            domain.TaskType      `json:"type"`
	Priority        domain.Priority      `json:"priority"`
	Status          domain.Status        `json:"status,omitempty"`
	Lifecycle       domain.IssueWorkflow `json:"lifecycle_state,omitempty"`
	Assignee        string               `json:"assignee,omitempty"`
	Labels          []string             `json:"labels,omitempty"`
	Implementations []string             `json:"implementations,omitempty"`
	Design          string               `json:"design,omitempty"`
	Notes           string               `json:"notes,omitempty"`
	Acceptance      string               `json:"acceptance,omitempty"`
	Estimate        *int                 `json:"estimate,omitempty"`
	ParentID        *naming.IssueID      `json:"parent_id,omitempty"`
}

// TaskUpdateParams contains the payload used to update task details through the shared daemon client.
type TaskUpdateParams struct {
	Title           string                `json:"title"`
	Description     string                `json:"description"`
	Design          *string               `json:"design,omitempty"`
	Notes           *string               `json:"notes,omitempty"`
	Acceptance      *string               `json:"acceptance,omitempty"`
	Estimate        *int                  `json:"estimate,omitempty"`
	EstimateSet     bool                  `json:"estimate_set,omitempty"`
	Type            domain.TaskType       `json:"type"`
	Priority        domain.Priority       `json:"priority"`
	Lifecycle       *domain.IssueWorkflow `json:"lifecycle_state,omitempty"`
	Implementations []string              `json:"implementations"`
}

// TaskStatusRequest contains the payload used to update a task status.
type TaskStatusRequest struct {
	TaskID          naming.IssueID `json:"task_id"`
	Status          domain.Status  `json:"status"`
	CascadeChildren bool           `json:"cascade_children,omitempty"`
}

type TaskOwnershipRequest struct {
	TaskID    naming.IssueID                  `json:"task_id"`
	OwnerID   string                          `json:"owner_id,omitempty"`
	OwnerKind string                          `json:"owner_kind,omitempty"`
	TTL       string                          `json:"ttl,omitempty"`
	Force     bool                            `json:"force,omitempty"`
	Purpose   domain.CoordinationLeasePurpose `json:"purpose,omitempty"`
}

// TaskStatusOptions controls client-side status transition behavior.
type TaskStatusOptions struct {
	ForceWorktree        bool
	IgnoreAhead          bool
	IntegrateBeforeClose bool
	CloseCleanChildren   bool
	CascadeChildren      bool
	AllowActiveSession   bool
	CloseOutcome         domain.IssueCloseOutcome
}

type TaskDeleteOptions struct {
	Cleanup        bool
	StopSession    bool
	RemoveWorktree bool
	ForceWorktree  bool
}

type TaskUnarchiveOptions struct {
	WithParents     bool
	CascadeChildren bool
}

type taskCloseRequest struct {
	TaskID               naming.IssueID `json:"task_id"`
	ForceWorktree        bool           `json:"force_worktree,omitempty"`
	IgnoreAhead          bool           `json:"ignore_ahead,omitempty"`
	IntegrateBeforeClose bool           `json:"integrate_before_close,omitempty"`
	CloseCleanChildren   bool           `json:"close_clean_children,omitempty"`
	AllowActiveSession   bool           `json:"allow_active_session,omitempty"`
	CloseOutcome         string         `json:"closed_outcome,omitempty"`
}

// TaskBulkCleanupRequest selects and closes multiple issues in one daemon operation.
type TaskBulkCleanupRequest = protocol.TaskBulkCleanupRequest
type TaskBulkCleanupItem = protocol.TaskBulkCleanupItem
type TaskBulkCleanupResult = protocol.TaskBulkCleanupResult

type taskDeleteRequest struct {
	TaskID         naming.IssueID `json:"task_id"`
	Cleanup        bool           `json:"cleanup,omitempty"`
	StopSession    bool           `json:"stop_session,omitempty"`
	RemoveWorktree bool           `json:"remove_worktree,omitempty"`
	ForceWorktree  bool           `json:"force_worktree,omitempty"`
}

type TaskCloseResult = protocol.TaskCloseResult
type TaskClosePhaseTiming = protocol.TaskClosePhaseTiming

type TaskDeleteResult struct {
	TaskID          string `json:"task_id"`
	Deleted         bool   `json:"deleted"`
	SessionStopped  bool   `json:"session_stopped,omitempty"`
	WorktreeRemoved bool   `json:"worktree_removed,omitempty"`
	WorktreeForced  bool   `json:"worktree_forced,omitempty"`
	Revision        uint64 `json:"revision,omitempty"`
}

// TaskGraphReadiness describes daemon-owned runnable-leaf policy for a root issue graph.
type TaskGraphReadiness struct {
	RootIssueID            string                        `json:"root_issue_id"`
	Capacity               TaskCapacitySummary           `json:"capacity"`
	Runnable               []string                      `json:"runnable"`
	NestedRoots            []TaskNestedRoot              `json:"nested_roots,omitempty"`
	Pending                []TaskPendingStart            `json:"pending,omitempty"`
	Active                 []string                      `json:"active,omitempty"`
	ActiveSessions         []TaskActiveSession           `json:"active_sessions,omitempty"`
	SessionStartProgress   []TaskSessionStartProgress    `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []TaskStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
	ContainmentRisks       []TaskContainmentRisk         `json:"containment_risks,omitempty"`
	WorkerObservations     []domain.WorkerObservation    `json:"worker_observations,omitempty"`
	Blocked                map[string]string             `json:"blocked"`
}

type TaskCapacitySummary struct {
	DirectRunnableCount        int `json:"direct_runnable_count"`
	DirectActiveCount          int `json:"direct_active_count"`
	NestedStartableCount       int `json:"nested_startable_count"`
	NestedActiveCount          int `json:"nested_active_count"`
	PendingStartsCount         int `json:"pending_starts_count"`
	BlockedNestedRootsCount    int `json:"blocked_nested_roots_count"`
	NotCountingCapacityCount   int `json:"not_counting_capacity_count"`
	TotalCountingCapacityCount int `json:"total_counting_capacity_count"`
}

// TaskNestedRoot describes a nested orchestration root that must be started
// from its own parent session instead of flattened into the current root.
type TaskNestedRoot struct {
	IssueID        string             `json:"issue_id"`
	Status         string             `json:"status"`
	IssueStatus    string             `json:"issue_status,omitempty"`
	Type           string             `json:"type"`
	ChildCount     int                `json:"child_count"`
	ActiveSession  *TaskActiveSession `json:"active_session,omitempty"`
	StartFailure   *TaskStartFailure  `json:"start_failure,omitempty"`
	FallbackPolicy string             `json:"fallback_policy,omitempty"`
	Advice         string             `json:"advice,omitempty"`
}

type TaskStartFailure struct {
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
	Message        string `json:"message,omitempty"`
}

// TaskPendingStart contains durable operation state for submitted session starts.
type TaskPendingStart struct {
	IssueID        string `json:"issue_id"`
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
}

// TaskActiveSession contains runtime activity details for active graph leaves.
type TaskActiveSession struct {
	IssueID           string                    `json:"issue_id"`
	Activity          string                    `json:"activity"`
	ActivitySource    string                    `json:"activity_source"`
	State             string                    `json:"state,omitempty"`
	Status            string                    `json:"status,omitempty"`
	TmuxAttachedCount int                       `json:"tmux_attached_count,omitempty"`
	StartProgress     *TaskSessionStartProgress `json:"start_progress,omitempty"`
	Advice            string                    `json:"advice,omitempty"`
}

type TaskSessionStartProgress struct {
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

type TaskStaleCloseableCandidate struct {
	IssueID          string   `json:"issue_id"`
	Status           string   `json:"status"`
	Evidence         []string `json:"evidence"`
	SuggestedCommand string   `json:"suggested_command"`
}

type TaskContainmentRisk struct {
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

// TaskCompleteCheckResult is the daemon-owned root close readiness gate.
type TaskCompleteCheckResult struct {
	RootIssueID            string                        `json:"root_issue_id"`
	Pass                   bool                          `json:"pass"`
	Reasons                []string                      `json:"reasons,omitempty"`
	Advice                 []string                      `json:"advice,omitempty"`
	StaleCloseableChildren []TaskStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
}

// TaskIntegrationReadiness is the daemon-owned worker integration evidence gate.
type TaskIntegrationReadiness struct {
	IssueID                string                         `json:"issue_id"`
	ParentIssueID          string                         `json:"parent_issue_id,omitempty"`
	Ready                  bool                           `json:"ready"`
	ContextRisk            *domain.IssueContextRiskPacket `json:"context_risk,omitempty"`
	Reasons                []string                       `json:"reasons,omitempty"`
	EvidenceEventSeq       int64                          `json:"evidence_event_seq,omitempty"`
	EvidenceEventID        int64                          `json:"evidence_event_id,omitempty"`
	EvidenceSource         string                         `json:"evidence_source,omitempty"`
	EvidencePacket         *domain.WorkerEvidencePacket   `json:"evidence_packet,omitempty"`
	EvidenceIncomplete     bool                           `json:"evidence_incomplete,omitempty"`
	EvidenceMissingFields  []string                       `json:"evidence_missing_fields,omitempty"`
	EvidenceInvalidReasons []string                       `json:"evidence_invalid_reasons,omitempty"`
}

type taskIntegrationReadinessRequest struct {
	TaskID  naming.IssueID `json:"task_id"`
	RepoDir string         `json:"repo_dir,omitempty"`
}

type taskGraphReadinessRequest struct {
	TaskID  naming.IssueID `json:"task_id"`
	ActorID string         `json:"actor_id,omitempty"`
}

type taskContextRiskRequest struct {
	TaskID  naming.IssueID `json:"task_id"`
	RepoDir string         `json:"repo_dir,omitempty"`
	Since   time.Time      `json:"since,omitempty,omitzero"`
	Compact bool           `json:"compact,omitempty"`
}

type TaskContextRiskOptions struct {
	Compact bool
}

// TaskMergeBaseTarget is the daemon-owned task graph/worktree merge target decision.
type TaskMergeBaseTarget struct {
	IssueID        string   `json:"issue_id"`
	TargetID       string   `json:"target_id"`
	Branch         string   `json:"branch"`
	WorktreePath   string   `json:"worktree_path,omitempty"`
	BranchAttached bool     `json:"branch_attached,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	AncestorChain  []string `json:"ancestor_chain,omitempty"`
}

type taskMergeBaseTargetRequest struct {
	TaskID            naming.IssueID `json:"task_id"`
	BaseBranch        string         `json:"base_branch,omitempty"`
	AllowBaseForChild bool           `json:"allow_base_for_child,omitempty"`
}

// TaskFollowOnMergeCandidate is a daemon-owned follow-on merge source decision.
type TaskFollowOnMergeCandidate struct {
	IssueID     string        `json:"issue_id"`
	Title       string        `json:"title"`
	Status      domain.Status `json:"status"`
	Relation    string        `json:"relation"`
	Order       int           `json:"order"`
	HasWorktree bool          `json:"has_worktree"`
}

type taskFollowOnMergeCandidatesRequest struct {
	TaskID naming.IssueID `json:"task_id"`
}

type taskFollowOnMergeCandidatesResponse struct {
	TaskID            string                       `json:"task_id"`
	MergeTargetToBase bool                         `json:"merge_target_to_base,omitempty"`
	Candidates        []TaskFollowOnMergeCandidate `json:"candidates"`
}

// TaskAppendNotesRequest appends a single line to task notes.
type TaskAppendNotesRequest struct {
	TaskID naming.IssueID `json:"task_id"`
	Line   string         `json:"line"`
}

// TaskIDRequest contains the payload used for delete/archive task operations.
type TaskIDRequest struct {
	TaskID   naming.IssueID `json:"task_id"`
	Archived string         `json:"archived,omitempty"`
}

// TaskIDsRequest contains the payload used for batch task reads.
type TaskIDsRequest struct {
	TaskIDs           []naming.IssueID `json:"task_ids"`
	IncludeAncestors  bool             `json:"include_ancestors,omitempty"`
	ExcludeDependents bool             `json:"exclude_dependents,omitempty"`
	DirectDependents  bool             `json:"direct_dependents,omitempty"`
	MetadataOnly      bool             `json:"metadata_only,omitempty"`
}

// TaskEventsRequest contains the payload used to list issue observation events.
type TaskEventsRequest struct {
	TaskID naming.IssueID `json:"task_id"`
	Types  []string       `json:"event_types,omitempty"`
	Limit  int            `json:"limit,omitempty"`
}

// TaskEventAppendRequest contains the payload used to append one issue observation event.
type TaskEventAppendRequest struct {
	TaskID        naming.IssueID `json:"task_id"`
	Type          string         `json:"event_type"`
	Source        string         `json:"source,omitempty"`
	SourceCommand string         `json:"source_command,omitempty"`
	OperationID   string         `json:"operation_id,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
	WorktreePath  string         `json:"worktree_path,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

// TaskDependencyParams contains the payload used for dependency operations.
type TaskDependencyParams struct {
	TaskID             naming.IssueID `json:"task_id"`
	DependsOnID        naming.IssueID `json:"depends_on_id"`
	Type               string         `json:"dependency_type"`
	ForceParentChange  bool           `json:"force_parent_change,omitempty"`
	IssueProjectID     string         `json:"issue_project_id,omitempty"`
	DependsOnProjectID string         `json:"depends_on_project_id,omitempty"`
}

// TaskDependencyRemoveParams extends dependency params with explicit confirmation.
type TaskDependencyRemoveParams struct {
	TaskID              naming.IssueID `json:"task_id"`
	DependsOnID         naming.IssueID `json:"depends_on_id"`
	Type                string         `json:"dependency_type"`
	Confirm             bool           `json:"confirm"`
	ConfirmParentOrphan bool           `json:"confirm_parent_orphan,omitempty"`
}

// TaskIDResponse is returned by commands that allocate a new task identifier.
type TaskIDResponse struct {
	TaskID naming.IssueID `json:"task_id"`
}

// TaskSnapshot captures a task list snapshot and the revision it was read at.
type TaskSnapshot struct {
	Tasks         []domain.Task
	View          domain.BoardView
	Columns       []domain.BoardViewColumnSnapshot
	Revision      uint64
	LastCheckedAt time.Time
	Freshness     protocol.TaskListFreshness
	SummariesOnly bool
}

// RequireFullDetails fails when a caller that needs mutation-safe task fields
// was accidentally given a list snapshot optimized for summaries.
func (s TaskSnapshot) RequireFullDetails(caller string) error {
	if !s.SummariesOnly {
		return nil
	}
	caller = strings.TrimSpace(caller)
	if caller == "" {
		caller = "task snapshot"
	}
	return fmt.Errorf("%s requires full task details but received a summary-only snapshot", caller)
}

type IssueSyncSummary struct {
	Provider              string `json:"provider"`
	Enabled               bool   `json:"enabled"`
	Skipped               bool   `json:"skipped"`
	Reason                string `json:"reason,omitempty"`
	Imported              int    `json:"imported"`
	UpdatedLocal          int    `json:"updated_local"`
	PushedRemote          int    `json:"pushed_remote"`
	Conflicts             int    `json:"conflicts"`
	RemoteIssues          int    `json:"remote_issues"`
	LocalIssues           int    `json:"local_issues"`
	SkippedPushOutOfScope int    `json:"skipped_push_out_of_scope"`
	OutOfScopeRefs        int    `json:"out_of_scope_refs"`
	SkippedUnchanged      int    `json:"skipped_unchanged"`
	PendingPushes         int    `json:"pending_pushes"`
	PushBudgetExhausted   bool   `json:"push_budget_exhausted"`
	RetriedRequests       int    `json:"retried_requests"`
	APIRequests           int    `json:"api_requests"`
	RateLimitLimit        int    `json:"rate_limit_limit,omitempty"`
	RateLimitRemaining    int    `json:"rate_limit_remaining,omitempty"`
	RateLimitReset        string `json:"rate_limit_reset,omitempty"`
	Incremental           bool   `json:"incremental"`
	Cursor                string `json:"cursor,omitempty"`
	RemoteScopeIssues     int    `json:"remote_scope_issues,omitempty"`
}

type IssueSyncConflict struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	ProjectID   string `json:"project_id"`
	IssueID     string `json:"issue_id"`
	Field       string `json:"field"`
	LocalValue  string `json:"local_value,omitempty"`
	RemoteValue string `json:"remote_value,omitempty"`
	DetectedAt  string `json:"detected_at"`
}

type IssueSyncConflictsResponse struct {
	Provider  string              `json:"provider"`
	ProjectID string              `json:"project_id"`
	Conflicts []IssueSyncConflict `json:"conflicts"`
}

// CommandError wraps typed daemon command failures.
type CommandError struct {
	Code      protocol.ErrorCode
	Retryable bool
	Message   string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ReadWaitPolicy returns the normalized bounded read wait policy configured on the client.
func (c *Client) ReadWaitPolicy() ReadWaitPolicy {
	return c.readWait.Normalize()
}

func (c *Client) commandJSONResponse(ctx context.Context, command string, body any) (protocol.ResponseEnvelope, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return protocol.ResponseEnvelope{}, fmt.Errorf("marshal %s request: %w", command, err)
		}
	}

	requestID, _ := naming.ParseRequestID(fmt.Sprintf("%s-%d", command, time.Now().UTC().UnixNano()))
	resp, err := c.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       requestID,
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: c.projectID,
		},
		Command: command,
		SentAt:  time.Now().UTC(),
		Body:    payload,
	})
	if err != nil {
		return protocol.ResponseEnvelope{}, err
	}
	if !resp.OK {
		return protocol.ResponseEnvelope{}, commandResponseError(command, resp.Error)
	}
	return resp, nil
}

func (c *Client) commandJSON(ctx context.Context, command string, body any, out any) error {
	resp, err := c.commandJSONResponse(ctx, command, body)
	if err != nil {
		return err
	}
	if len(resp.Body) == 0 {
		return nil
	}
	if out != nil {
		if err := decodeLongRunningJSON(command, resp.Body, out); err != nil {
			return err
		}
		return nil
	}

	var envelope longRunningResultEnvelope
	if err := json.Unmarshal(resp.Body, &envelope); err == nil {
		if pending := pendingOperationError(command, envelope); pending != nil {
			return pending
		}
	}
	return nil
}

func commandResponseError(command string, env *protocol.ErrorEnvelope) error {
	if env == nil {
		return fmt.Errorf("%s failed", command)
	}
	return &CommandError{
		Code:      env.Code,
		Retryable: env.Retryable,
		Message:   env.Message,
	}
}

// ListTasks fetches the current task set through the daemon client boundary.
func (c *Client) ListTasks(ctx context.Context) ([]domain.Task, error) {
	snapshot, err := c.ListTasksSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.Tasks, nil
}

func (c *Client) RunIssueSync(ctx context.Context) (IssueSyncSummary, error) {
	var out IssueSyncSummary
	if err := c.commandJSON(ctx, CommandSyncRun, map[string]any{}, &out); err != nil {
		return IssueSyncSummary{}, err
	}
	return out, nil
}

func (c *Client) ListIssueSyncConflicts(ctx context.Context, includeResolved bool) (IssueSyncConflictsResponse, error) {
	var out IssueSyncConflictsResponse
	if err := c.commandJSON(ctx, CommandSyncConflicts, map[string]any{"include_resolved": includeResolved}, &out); err != nil {
		return IssueSyncConflictsResponse{}, err
	}
	return out, nil
}

// GetTaskSnapshot fetches one task and its direct dependency context.
func (c *Client) GetTaskSnapshot(ctx context.Context, taskID string) (TaskSnapshot, error) {
	return c.GetTaskSnapshotWithMode(ctx, taskID, ReadWaitModeDefault)
}

// GetTaskSnapshotWithMode fetches one task and its direct dependency context with the requested bounded read budget.
func (c *Client) GetTaskSnapshotWithMode(ctx context.Context, taskID string, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.GetTaskSnapshotWithArchiveMode(ctx, taskID, protocol.ArchiveModeExclude, mode)
}

// GetTaskSnapshotWithArchiveMode fetches one task with explicit archived issue visibility.
func (c *Client) GetTaskSnapshotWithArchiveMode(ctx context.Context, taskID string, archiveMode protocol.ArchiveMode, mode ReadWaitMode) (TaskSnapshot, error) {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return TaskSnapshot{}, fmt.Errorf("invalid task id: %w", err)
	}
	if !archiveMode.Valid() {
		return TaskSnapshot{}, fmt.Errorf("invalid archive mode: %s", archiveMode)
	}

	waitCtx, cancel, budget := c.readWait.contextWithBudget(ctx, mode)
	defer cancel()

	resp, err := c.commandJSONResponse(waitCtx, CommandTaskGet, TaskIDRequest{TaskID: parsedTaskID, Archived: string(archiveMode)})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return TaskSnapshot{}, c.readWait.timeoutError(mode, budget, err)
		}
		return TaskSnapshot{}, err
	}
	snapshot, decodeErr := c.decodeTaskSnapshotResponse(resp)
	if decodeErr != nil {
		return TaskSnapshot{}, fmt.Errorf("decode %s response: %w (expected %s payload; daemon likely outdated)", CommandTaskGet, decodeErr, "TaskListSnapshotPayload")
	}
	return snapshot, nil
}

// GetManyTaskSnapshot fetches multiple tasks and their direct dependency context in one daemon command.
func (c *Client) GetManyTaskSnapshot(ctx context.Context, taskIDs []string) (TaskSnapshot, error) {
	return c.GetManyTaskSnapshotWithMode(ctx, taskIDs, ReadWaitModeDefault)
}

// GetManyTaskSnapshotWithMode fetches multiple tasks and their direct dependency context with the requested bounded read budget.
func (c *Client) GetManyTaskSnapshotWithMode(ctx context.Context, taskIDs []string, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.getManyTaskSnapshot(ctx, taskIDs, mode, getManyTaskSnapshotOptions{})
}

// GetManyTaskSnapshotWithAncestors fetches multiple tasks, their direct dependency context, and full parent-child ancestor chains.
func (c *Client) GetManyTaskSnapshotWithAncestors(ctx context.Context, taskIDs []string) (TaskSnapshot, error) {
	return c.GetManyTaskSnapshotWithAncestorsMode(ctx, taskIDs, ReadWaitModeDefault)
}

// GetManyTaskSnapshotWithAncestorsMode fetches multiple tasks with ancestor context using the requested bounded read budget.
func (c *Client) GetManyTaskSnapshotWithAncestorsMode(ctx context.Context, taskIDs []string, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.getManyTaskSnapshot(ctx, taskIDs, mode, getManyTaskSnapshotOptions{includeAncestors: true})
}

// GetManyTaskSnapshotWithAncestorsNoDependents fetches tasks and ancestor chains without expanding direct dependents.
func (c *Client) GetManyTaskSnapshotWithAncestorsNoDependents(ctx context.Context, taskIDs []string) (TaskSnapshot, error) {
	return c.GetManyTaskSnapshotWithAncestorsNoDependentsMode(ctx, taskIDs, ReadWaitModeDefault)
}

// GetManyTaskSnapshotWithAncestorsNoDependentsMode fetches tasks and ancestor chains without dependent context.
func (c *Client) GetManyTaskSnapshotWithAncestorsNoDependentsMode(ctx context.Context, taskIDs []string, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.getManyTaskSnapshot(ctx, taskIDs, mode, getManyTaskSnapshotOptions{includeAncestors: true, excludeDependents: true})
}

// GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly fetches stored task metadata without runtime refresh work.
func (c *Client) GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly(ctx context.Context, taskIDs []string) (TaskSnapshot, error) {
	return c.GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnlyMode(ctx, taskIDs, ReadWaitModeDefault)
}

// GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnlyMode fetches stored task metadata using the requested bounded read budget.
func (c *Client) GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnlyMode(ctx context.Context, taskIDs []string, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.getManyTaskSnapshot(ctx, taskIDs, mode, getManyTaskSnapshotOptions{includeAncestors: true, excludeDependents: true, metadataOnly: true})
}

// GetChildBoardSnapshotWithMode fetches a parent plus its direct child board context.
func (c *Client) GetChildBoardSnapshotWithMode(ctx context.Context, parentID string, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.getManyTaskSnapshot(ctx, []string{parentID}, mode, getManyTaskSnapshotOptions{includeAncestors: true, directDependents: true})
}

type getManyTaskSnapshotOptions struct {
	includeAncestors  bool
	excludeDependents bool
	directDependents  bool
	metadataOnly      bool
}

func (c *Client) getManyTaskSnapshot(ctx context.Context, taskIDs []string, mode ReadWaitMode, opts getManyTaskSnapshotOptions) (TaskSnapshot, error) {
	parsedTaskIDs := make([]naming.IssueID, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		trimmed := strings.TrimSpace(taskID)
		if trimmed == "" {
			continue
		}
		parsedTaskID, err := naming.ParseIssueID(trimmed)
		if err != nil {
			return TaskSnapshot{}, fmt.Errorf("invalid task id %q: %w", taskID, err)
		}
		parsedTaskIDs = append(parsedTaskIDs, parsedTaskID)
	}
	if len(parsedTaskIDs) == 0 {
		return TaskSnapshot{}, fmt.Errorf("task_ids is required")
	}

	waitCtx, cancel, budget := c.readWait.contextWithBudget(ctx, mode)
	defer cancel()

	resp, err := c.commandJSONResponse(waitCtx, CommandTaskGetMany, TaskIDsRequest{TaskIDs: parsedTaskIDs, IncludeAncestors: opts.includeAncestors, ExcludeDependents: opts.excludeDependents, DirectDependents: opts.directDependents, MetadataOnly: opts.metadataOnly})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return TaskSnapshot{}, c.readWait.timeoutError(mode, budget, err)
		}
		return TaskSnapshot{}, err
	}
	snapshot, decodeErr := c.decodeTaskSnapshotResponse(resp)
	if decodeErr != nil {
		return TaskSnapshot{}, fmt.Errorf("decode %s response: %w (expected %s payload; daemon likely outdated)", CommandTaskGetMany, decodeErr, "TaskListSnapshotPayload")
	}
	return snapshot, nil
}

// ListTaskEvents fetches the durable observation event stream for one task.
func (c *Client) ListTaskEvents(ctx context.Context, taskID string, eventTypes []string, limit int) ([]domain.IssueObservationEvent, error) {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return nil, fmt.Errorf("invalid task id: %w", err)
	}
	types := make([]string, 0, len(eventTypes))
	for _, eventType := range eventTypes {
		trimmed := strings.TrimSpace(eventType)
		if trimmed != "" {
			types = append(types, trimmed)
		}
	}
	var out struct {
		Events []domain.IssueObservationEvent `json:"events"`
	}
	if err := c.commandJSON(ctx, CommandTaskEvents, TaskEventsRequest{
		TaskID: parsedTaskID,
		Types:  types,
		Limit:  limit,
	}, &out); err != nil {
		return nil, err
	}
	return append([]domain.IssueObservationEvent(nil), out.Events...), nil
}

// AppendTaskEvent records a durable issue-scoped observation event.
func (c *Client) AppendTaskEvent(ctx context.Context, taskID string, req TaskEventAppendRequest) (domain.IssueObservationEvent, error) {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return domain.IssueObservationEvent{}, fmt.Errorf("invalid task id: %w", err)
	}
	req.TaskID = parsedTaskID
	req.Type = strings.TrimSpace(req.Type)
	req.Source = strings.TrimSpace(req.Source)
	req.SourceCommand = strings.TrimSpace(req.SourceCommand)
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.WorktreePath = strings.TrimSpace(req.WorktreePath)
	if req.Payload == nil {
		req.Payload = map[string]any{}
	}
	var out struct {
		Event domain.IssueObservationEvent `json:"event"`
	}
	if err := c.commandJSON(ctx, CommandTaskEventAppend, req, &out); err != nil {
		return domain.IssueObservationEvent{}, err
	}
	return out.Event, nil
}

func (c *Client) TaskSQLiteWAL(ctx context.Context, checkpointMode string) (protocol.TaskSQLiteWALResponse, error) {
	var out protocol.TaskSQLiteWALResponse
	if err := c.commandJSON(ctx, CommandTaskSQLiteWAL, protocol.TaskSQLiteWALRequest{
		CheckpointMode: strings.TrimSpace(checkpointMode),
	}, &out); err != nil {
		return protocol.TaskSQLiteWALResponse{}, err
	}
	return out, nil
}

// ListTasksSnapshot fetches the current task set and revision through the daemon client boundary.
func (c *Client) ListTasksSnapshot(ctx context.Context) (TaskSnapshot, error) {
	return c.ListTasksSnapshotWithMode(ctx, ReadWaitModeDefault)
}

// ListTasksSnapshotWithMode fetches a task snapshot with the requested bounded read budget.
func (c *Client) ListTasksSnapshotWithMode(ctx context.Context, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.ListTasksSnapshotWithQueryMode(ctx, "", mode)
}

// ListTasksSnapshotWithDependencies fetches the current task set including non-parent dependency edges.
func (c *Client) ListTasksSnapshotWithDependencies(ctx context.Context) (TaskSnapshot, error) {
	return c.listTasksSnapshotWithQueryMode(ctx, "", ReadWaitModeDefault, true)
}

// ListTasksSnapshotWithQuery fetches a daemon-filtered task snapshot for an issue content query.
func (c *Client) ListTasksSnapshotWithQuery(ctx context.Context, query string) (TaskSnapshot, error) {
	return c.ListTasksSnapshotWithQueryMode(ctx, query, ReadWaitModeDefault)
}

// ListTasksSnapshotWithQueryMode fetches a task snapshot with an optional daemon-side content query.
func (c *Client) ListTasksSnapshotWithQueryMode(ctx context.Context, query string, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.listTasksSnapshotWithQueryArchiveMode(ctx, query, mode, false, protocol.ArchiveModeExclude)
}

func (c *Client) ListTasksSnapshotWithArchiveMode(ctx context.Context, archiveMode protocol.ArchiveMode) (TaskSnapshot, error) {
	return c.listTasksSnapshotWithQueryArchiveMode(ctx, "", ReadWaitModeDefault, false, archiveMode)
}

func (c *Client) ListTasksSnapshotWithDependenciesArchiveMode(ctx context.Context, archiveMode protocol.ArchiveMode) (TaskSnapshot, error) {
	return c.listTasksSnapshotWithQueryArchiveMode(ctx, "", ReadWaitModeDefault, true, archiveMode)
}

func (c *Client) ListTasksSnapshotWithQueryArchiveMode(ctx context.Context, query string, archiveMode protocol.ArchiveMode) (TaskSnapshot, error) {
	return c.listTasksSnapshotWithQueryArchiveMode(ctx, query, ReadWaitModeDefault, false, archiveMode)
}

func (c *Client) listTasksSnapshotWithQueryMode(ctx context.Context, query string, mode ReadWaitMode, includeDependencies bool) (TaskSnapshot, error) {
	return c.listTasksSnapshotWithQueryArchiveMode(ctx, query, mode, includeDependencies, protocol.ArchiveModeExclude)
}

func (c *Client) listTasksSnapshotWithQueryArchiveMode(ctx context.Context, query string, mode ReadWaitMode, includeDependencies bool, archiveMode protocol.ArchiveMode) (TaskSnapshot, error) {
	waitCtx, cancel, budget := c.readWait.contextWithBudget(ctx, mode)
	defer cancel()
	if !archiveMode.Valid() {
		return TaskSnapshot{}, fmt.Errorf("invalid archive mode: %s", archiveMode)
	}

	var body any
	query = strings.TrimSpace(query)
	if query != "" || includeDependencies || archiveMode != protocol.ArchiveModeExclude {
		body = protocol.TaskListRequestBody{Query: query, IncludeDependencies: includeDependencies, Archived: string(archiveMode)}
	}

	resp, err := c.commandJSONResponse(waitCtx, CommandTaskList, body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return TaskSnapshot{}, c.readWait.timeoutError(mode, budget, err)
		}
		return TaskSnapshot{}, err
	}
	snapshot, decodeErr := c.decodeTaskSnapshotResponse(resp)
	if decodeErr == nil {
		return snapshot, nil
	}
	if protocol.IsTaskListSnapshotVersionMismatch(decodeErr) {
		ack, diag := c.Handshake(waitCtx, protocol.Hello{
			ProtocolVersion: protocol.CurrentVersion,
			ClientName:      "client",
			ClientVersion:   buildinfo.VersionString(),
			Capabilities:    []string{"snapshot", "subscribe"},
		})
		if diag != nil {
			if diag.Message != "" {
				return TaskSnapshot{}, fmt.Errorf("decode %s response: %w (handshake after mismatch failed: %s)", CommandTaskList, decodeErr, diag.Message)
			}
			return TaskSnapshot{}, fmt.Errorf("decode %s response: %w (handshake after mismatch failed)", CommandTaskList, decodeErr)
		}
		if !ack.Accepted {
			return TaskSnapshot{}, fmt.Errorf("decode %s response: %w (handshake rejected after mismatch: %s)", CommandTaskList, decodeErr, ack.Reason)
		}
		retryResp, retryErr := c.commandJSONResponse(waitCtx, CommandTaskList, body)
		if retryErr != nil {
			return TaskSnapshot{}, retryErr
		}
		retrySnapshot, retryDecodeErr := c.decodeTaskSnapshotResponse(retryResp)
		if retryDecodeErr != nil {
			return TaskSnapshot{}, fmt.Errorf("decode %s response: %w (expected %s payload; daemon likely outdated)", CommandTaskList, retryDecodeErr, "TaskListSnapshotPayload")
		}
		return retrySnapshot, nil
	}
	return TaskSnapshot{}, fmt.Errorf("decode %s response: %w (expected %s payload; daemon likely outdated)", CommandTaskList, decodeErr, "TaskListSnapshotPayload")
}

// BoardSnapshot fetches the board view's summary-only task set and revision.
func (c *Client) BoardSnapshot(ctx context.Context) (TaskSnapshot, error) {
	return c.BoardSnapshotWithMode(ctx, ReadWaitModeDefault)
}

// BoardSnapshotWithMode fetches a board snapshot with the requested bounded read budget.
func (c *Client) BoardSnapshotWithMode(ctx context.Context, mode ReadWaitMode) (TaskSnapshot, error) {
	return c.BoardSnapshotForViewWithMode(ctx, "", mode)
}

// BoardSnapshotForViewWithMode fetches a board snapshot grouped by the requested view.
func (c *Client) BoardSnapshotForViewWithMode(ctx context.Context, viewID string, mode ReadWaitMode) (TaskSnapshot, error) {
	waitCtx, cancel, budget := c.readWait.contextWithBudget(ctx, mode)
	defer cancel()

	var body any
	if strings.TrimSpace(viewID) != "" {
		body = protocol.BoardSnapshotRequestBody{ViewID: strings.TrimSpace(viewID)}
	}
	resp, err := c.commandJSONResponse(waitCtx, CommandBoardFetch, body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return TaskSnapshot{}, c.readWait.timeoutError(mode, budget, err)
		}
		return TaskSnapshot{}, err
	}
	snapshot, decodeErr := c.decodeBoardSnapshotResponse(resp)
	if decodeErr != nil {
		return TaskSnapshot{}, fmt.Errorf("decode %s response: %w (expected %s payload; daemon likely outdated)", CommandBoardFetch, decodeErr, "BoardSnapshotPayload")
	}
	return snapshot, nil
}

func (c *Client) decodeTaskSnapshotResponse(resp protocol.ResponseEnvelope) (TaskSnapshot, error) {
	if len(resp.Body) == 0 {
		return TaskSnapshot{Revision: resp.Revision}, nil
	}

	payload, err := protocol.DecodeTaskListSnapshotPayload(resp.Body)
	if err != nil {
		return TaskSnapshot{}, err
	}
	revision := payload.SnapshotRevision
	if revision == 0 {
		revision = resp.Revision
	}
	return TaskSnapshot{
		Tasks:         payload.Tasks,
		Revision:      revision,
		LastCheckedAt: payload.LastCheckedAt,
		Freshness:     payload.Freshness,
		SummariesOnly: payload.SummariesOnly,
	}, nil
}

func (c *Client) decodeBoardSnapshotResponse(resp protocol.ResponseEnvelope) (TaskSnapshot, error) {
	if len(resp.Body) == 0 {
		return TaskSnapshot{Revision: resp.Revision, SummariesOnly: true}, nil
	}

	payload, err := protocol.DecodeBoardSnapshotPayload(resp.Body)
	if err != nil {
		return TaskSnapshot{}, err
	}
	revision := payload.SnapshotRevision
	if revision == 0 {
		revision = resp.Revision
	}
	return TaskSnapshot{
		Tasks:         protocol.DomainTasksFromBoardSummaries(payload.Tasks),
		View:          payload.View,
		Columns:       boardSnapshotColumnsToDomain(payload.Columns),
		Revision:      revision,
		LastCheckedAt: payload.LastCheckedAt,
		Freshness:     payload.Freshness,
		SummariesOnly: true,
	}, nil
}

func boardSnapshotColumnsToDomain(columns []protocol.BoardSnapshotColumn) []domain.BoardViewColumnSnapshot {
	if len(columns) == 0 {
		return nil
	}
	out := make([]domain.BoardViewColumnSnapshot, 0, len(columns))
	for _, column := range columns {
		out = append(out, domain.BoardViewColumnSnapshot{
			Definition: column.Definition,
			Tasks:      protocol.DomainTasksFromBoardSummaries(column.Tasks),
		})
	}
	return out
}

// CreateTask creates a task through the daemon client boundary.
func (c *Client) CreateTask(ctx context.Context, params TaskCreateParams) (string, error) {
	var resp TaskIDResponse
	if err := c.commandJSON(ctx, CommandTaskCreate, params, &resp); err != nil {
		return "", err
	}
	if resp.TaskID == "" {
		return "", fmt.Errorf("%s returned empty task id", CommandTaskCreate)
	}
	return resp.TaskID.String(), nil
}

// UpdateTaskStatus updates a task's status through the daemon client boundary.
// Done and cancelled transitions are always routed through task.close so
// durable close invariants stay daemon-owned even when callers omit close
// options.
func (c *Client) UpdateTaskStatus(ctx context.Context, taskID string, status domain.Status) error {
	return c.UpdateTaskStatusWithOptions(ctx, taskID, status, TaskStatusOptions{})
}

// UpdateTaskStatusWithOptions updates a task's status. Terminal close
// transitions are represented by the daemon-owned close command; other status
// moves use the lightweight status mutation command.
func (c *Client) UpdateTaskStatusWithOptions(ctx context.Context, taskID string, status domain.Status, opts TaskStatusOptions) error {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	if status == domain.StatusDone {
		opts.CloseOutcome = domain.IssueCloseCompleted
		_, err := c.CloseTask(ctx, parsedTaskID.String(), opts)
		return err
	}
	if status == domain.StatusCancelled {
		opts.CloseOutcome = domain.IssueCloseCancelled
		opts.IntegrateBeforeClose = false
		_, err := c.CloseTask(ctx, parsedTaskID.String(), opts)
		return err
	}
	return c.commandJSON(ctx, CommandTaskUpdateStatus, TaskStatusRequest{
		TaskID:          parsedTaskID,
		Status:          status,
		CascadeChildren: opts.CascadeChildren,
	}, nil)
}

func (c *Client) CloseTask(ctx context.Context, taskID string, opts TaskStatusOptions) (TaskCloseResult, error) {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return TaskCloseResult{}, fmt.Errorf("invalid task id: %w", err)
	}
	var out TaskCloseResult
	if err := c.commandJSON(ctx, CommandTaskClose, taskCloseRequest{
		TaskID:               parsedTaskID,
		ForceWorktree:        opts.ForceWorktree,
		IgnoreAhead:          opts.IgnoreAhead,
		IntegrateBeforeClose: opts.IntegrateBeforeClose,
		CloseCleanChildren:   opts.CloseCleanChildren,
		AllowActiveSession:   opts.AllowActiveSession,
		CloseOutcome:         string(opts.CloseOutcome),
	}, &out); err != nil {
		return TaskCloseResult{}, err
	}
	return out, nil
}

func (c *Client) BulkCleanupTasks(ctx context.Context, req TaskBulkCleanupRequest) (TaskBulkCleanupResult, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return TaskBulkCleanupResult{}, fmt.Errorf("marshal %s request: %w", CommandTaskBulkCleanup, err)
	}
	var submitted protocol.OperationSubmitResponseBody
	if err := c.commandJSON(ctx, protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: c.projectID,
		Kind:      CommandTaskBulkCleanup,
		Payload:   payload,
	}, &submitted); err != nil {
		return TaskBulkCleanupResult{}, err
	}
	record := submitted.Operation
	if !isTerminalOperationState(record.State) {
		record, err = c.WaitForOperation(ctx, record.OperationID.String(), 0)
		if err != nil {
			return TaskBulkCleanupResult{}, fmt.Errorf("wait for bulk cleanup operation %s: %w", record.OperationID, err)
		}
	}
	if record.State != protocol.OperationStateDone {
		if record.Error != nil && strings.TrimSpace(record.Error.Message) != "" {
			return TaskBulkCleanupResult{}, fmt.Errorf("bulk cleanup operation %s %s: %s", record.OperationID, record.State, record.Error.Message)
		}
		return TaskBulkCleanupResult{}, fmt.Errorf("bulk cleanup operation %s %s", record.OperationID, record.State)
	}
	var out TaskBulkCleanupResult
	if err := json.Unmarshal(record.Result, &out); err != nil {
		return TaskBulkCleanupResult{}, fmt.Errorf("decode bulk cleanup operation %s result: %w", record.OperationID, err)
	}
	return out, nil
}

func (c *Client) ClaimTaskOwnership(ctx context.Context, taskID string, params TaskOwnershipRequest) (domain.Task, error) {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return domain.Task{}, fmt.Errorf("invalid task id: %w", err)
	}
	params.TaskID = parsedTaskID
	var out domain.Task
	if err := c.commandJSON(ctx, CommandTaskClaimOwnership, params, &out); err != nil {
		return domain.Task{}, err
	}
	return out, nil
}

func (c *Client) ReleaseTaskOwnership(ctx context.Context, taskID string, params TaskOwnershipRequest) (domain.Task, error) {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return domain.Task{}, fmt.Errorf("invalid task id: %w", err)
	}
	params.TaskID = parsedTaskID
	var out domain.Task
	if err := c.commandJSON(ctx, CommandTaskReleaseOwnership, params, &out); err != nil {
		return domain.Task{}, err
	}
	return out, nil
}

// UpdateTaskDetails updates a task's details through the daemon client boundary.
func (c *Client) UpdateTaskDetails(ctx context.Context, taskID string, params TaskUpdateParams) error {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	body := struct {
		TaskID naming.IssueID `json:"task_id"`
		TaskUpdateParams
	}{
		TaskID:           parsedTaskID,
		TaskUpdateParams: params,
	}
	return c.commandJSON(ctx, CommandTaskUpdate, body, nil)
}

// AppendTaskNotes appends a note line to task notes through the daemon boundary.
func (c *Client) AppendTaskNotes(ctx context.Context, taskID, line string) error {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	return c.commandJSON(ctx, CommandTaskAppendNotes, TaskAppendNotesRequest{
		TaskID: parsedTaskID,
		Line:   line,
	}, nil)
}

// DeleteTask deletes a task through the daemon client boundary.
func (c *Client) DeleteTask(ctx context.Context, taskID string) error {
	_, err := c.DeleteTaskWithOptions(ctx, taskID, TaskDeleteOptions{})
	return err
}

func (c *Client) DeleteTaskWithOptions(ctx context.Context, taskID string, opts TaskDeleteOptions) (TaskDeleteResult, error) {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return TaskDeleteResult{}, fmt.Errorf("invalid task id: %w", err)
	}
	var out TaskDeleteResult
	if err := c.commandJSON(ctx, CommandTaskDelete, taskDeleteRequest{
		TaskID:         parsedTaskID,
		Cleanup:        opts.Cleanup,
		StopSession:    opts.StopSession,
		RemoveWorktree: opts.RemoveWorktree,
		ForceWorktree:  opts.ForceWorktree,
	}, &out); err != nil {
		return TaskDeleteResult{}, err
	}
	return out, nil
}

// ArchiveTask archives a task through the daemon client boundary.
func (c *Client) ArchiveTask(ctx context.Context, taskID string) error {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	return c.commandJSON(ctx, CommandTaskArchive, TaskIDRequest{TaskID: parsedTaskID}, nil)
}

// UnarchiveTask restores an archived task through the daemon client boundary.
func (c *Client) UnarchiveTask(ctx context.Context, taskID string) error {
	return c.UnarchiveTaskWithOptions(ctx, taskID, TaskUnarchiveOptions{})
}

// UnarchiveTaskWithOptions restores archived task graph rows through the daemon client boundary.
func (c *Client) UnarchiveTaskWithOptions(ctx context.Context, taskID string, opts TaskUnarchiveOptions) error {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	return c.commandJSON(ctx, CommandTaskUnarchive, struct {
		TaskID          naming.IssueID `json:"task_id"`
		WithParents     bool           `json:"with_parents,omitempty"`
		CascadeChildren bool           `json:"cascade_children,omitempty"`
	}{
		TaskID:          parsedTaskID,
		WithParents:     opts.WithParents,
		CascadeChildren: opts.CascadeChildren,
	}, nil)
}

// TaskGraphReadiness returns daemon-owned runnable-leaf policy for a root issue.
func (c *Client) TaskGraphReadiness(ctx context.Context, rootIssueID string) (TaskGraphReadiness, error) {
	return c.TaskGraphReadinessForActor(ctx, rootIssueID, "")
}

// TaskGraphReadinessForActor returns daemon-owned runnable-leaf policy for a root issue
// from a specific actor's ownership perspective.
func (c *Client) TaskGraphReadinessForActor(ctx context.Context, rootIssueID string, actorID string) (TaskGraphReadiness, error) {
	parsedRootID, err := naming.ParseIssueID(rootIssueID)
	if err != nil {
		return TaskGraphReadiness{}, fmt.Errorf("invalid root issue id: %w", err)
	}
	var out TaskGraphReadiness
	if err := c.commandJSON(ctx, CommandTaskGraphReadiness, taskGraphReadinessRequest{
		TaskID:  parsedRootID,
		ActorID: strings.TrimSpace(actorID),
	}, &out); err != nil {
		return TaskGraphReadiness{}, err
	}
	return out, nil
}

// TaskCompleteCheck returns the daemon-owned completion gate for a root issue.
func (c *Client) TaskCompleteCheck(ctx context.Context, rootIssueID string) (TaskCompleteCheckResult, error) {
	parsedRootID, err := naming.ParseIssueID(rootIssueID)
	if err != nil {
		return TaskCompleteCheckResult{}, fmt.Errorf("invalid root issue id: %w", err)
	}
	var out TaskCompleteCheckResult
	if err := c.commandJSON(ctx, CommandTaskCompleteCheck, TaskIDRequest{TaskID: parsedRootID}, &out); err != nil {
		return TaskCompleteCheckResult{}, err
	}
	return out, nil
}

// TaskIntegrationReadiness returns the daemon-owned worker integration evidence gate.
func (c *Client) TaskIntegrationReadiness(ctx context.Context, issueID, repoDir string) (TaskIntegrationReadiness, error) {
	parsedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return TaskIntegrationReadiness{}, fmt.Errorf("invalid issue id: %w", err)
	}
	var out TaskIntegrationReadiness
	if err := c.commandJSON(ctx, CommandTaskIntegrationReady, taskIntegrationReadinessRequest{
		TaskID:  parsedIssueID,
		RepoDir: strings.TrimSpace(repoDir),
	}, &out); err != nil {
		return TaskIntegrationReadiness{}, err
	}
	return out, nil
}

// TaskContextRisk returns a daemon-owned risk packet for repeated local issue overlap.
func (c *Client) TaskContextRisk(ctx context.Context, issueID, repoDir string, since time.Time, opts ...TaskContextRiskOptions) (domain.IssueContextRiskPacket, error) {
	parsedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return domain.IssueContextRiskPacket{}, fmt.Errorf("invalid issue id: %w", err)
	}
	var opt TaskContextRiskOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	var out domain.IssueContextRiskPacket
	if err := c.commandJSON(ctx, CommandTaskContextRisk, taskContextRiskRequest{
		TaskID:  parsedIssueID,
		RepoDir: strings.TrimSpace(repoDir),
		Since:   since,
		Compact: opt.Compact,
	}, &out); err != nil {
		return domain.IssueContextRiskPacket{}, err
	}
	return out, nil
}

// TaskMergeBaseTarget resolves the daemon-owned merge target for an issue branch.
func (c *Client) TaskMergeBaseTarget(ctx context.Context, issueID, baseBranch string, allowBaseForChild bool) (TaskMergeBaseTarget, error) {
	parsedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return TaskMergeBaseTarget{}, fmt.Errorf("invalid issue id: %w", err)
	}
	var out TaskMergeBaseTarget
	if err := c.commandJSON(ctx, CommandTaskMergeBaseTarget, taskMergeBaseTargetRequest{
		TaskID:            parsedIssueID,
		BaseBranch:        strings.TrimSpace(baseBranch),
		AllowBaseForChild: allowBaseForChild,
	}, &out); err != nil {
		return TaskMergeBaseTarget{}, err
	}
	return out, nil
}

// TaskFollowOnMergeCandidates returns daemon-owned follow-on merge source candidates.
func (c *Client) TaskFollowOnMergeCandidates(ctx context.Context, issueID string) (bool, []TaskFollowOnMergeCandidate, error) {
	parsedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return false, nil, fmt.Errorf("invalid issue id: %w", err)
	}
	var out taskFollowOnMergeCandidatesResponse
	if err := c.commandJSON(ctx, CommandTaskFollowOnMerge, taskFollowOnMergeCandidatesRequest{
		TaskID: parsedIssueID,
	}, &out); err != nil {
		return false, nil, err
	}
	return out.MergeTargetToBase, append([]TaskFollowOnMergeCandidate(nil), out.Candidates...), nil
}

// AddTaskDependency creates or restores a dependency between two tasks.
func (c *Client) AddTaskDependency(ctx context.Context, params TaskDependencyParams) error {
	return c.commandJSON(ctx, CommandTaskDependencyAdd, params, nil)
}

// RemoveTaskDependency tombstones a dependency between two tasks.
func (c *Client) RemoveTaskDependency(ctx context.Context, params TaskDependencyRemoveParams) error {
	return c.commandJSON(ctx, CommandTaskDependencyRemove, params, nil)
}
