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
	CommandTaskList             = "task.list"
	CommandTaskGet              = "task.get"
	CommandTaskGetMany          = "task.get_many"
	CommandTaskCreate           = "task.create"
	CommandTaskClose            = "task.close"
	CommandTaskGraphReadiness   = "task.graph_readiness"
	CommandTaskCompleteCheck    = "task.complete_check"
	CommandTaskIntegrationReady = "task.integration_readiness"
	CommandTaskMergeBaseTarget  = "task.merge_base_target"
	CommandTaskFollowOnMerge    = "task.follow_on_merge_candidates"
	CommandTaskUpdateStatus     = "task.update_status"
	CommandTaskUpdate           = "task.update_details"
	CommandTaskAppendNotes      = "task.append_notes"
	CommandTaskDelete           = "task.delete"
	CommandTaskArchive          = "task.archive"
	CommandTaskDependencyAdd    = "task.dependency.add"
	CommandTaskDependencyRemove = "task.dependency.remove"
	CommandSyncRun              = "sync.run"
	CommandSyncConflicts        = "sync.conflicts"
)

// TaskCreateParams contains the payload used to create a task through the shared daemon client.
type TaskCreateParams struct {
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	Type            domain.TaskType `json:"type"`
	Priority        domain.Priority `json:"priority"`
	Status          domain.Status   `json:"status,omitempty"`
	Assignee        string          `json:"assignee,omitempty"`
	Labels          []string        `json:"labels,omitempty"`
	Implementations []string        `json:"implementations,omitempty"`
	Design          string          `json:"design,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	Acceptance      string          `json:"acceptance,omitempty"`
	Estimate        *int            `json:"estimate,omitempty"`
	ParentID        *naming.IssueID `json:"parent_id,omitempty"`
}

// TaskUpdateParams contains the payload used to update task details through the shared daemon client.
type TaskUpdateParams struct {
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	Notes           *string         `json:"notes,omitempty"`
	Type            domain.TaskType `json:"type"`
	Priority        domain.Priority `json:"priority"`
	Implementations []string        `json:"implementations"`
}

// TaskStatusRequest contains the payload used to update a task status.
type TaskStatusRequest struct {
	TaskID naming.IssueID `json:"task_id"`
	Status domain.Status  `json:"status"`
}

// TaskStatusOptions controls client-side status transition behavior.
type TaskStatusOptions struct {
	ForceWorktree        bool
	IgnoreAhead          bool
	IntegrateBeforeClose bool
}

type TaskDeleteOptions struct {
	Cleanup        bool
	StopSession    bool
	RemoveWorktree bool
	ForceWorktree  bool
}

type taskCloseRequest struct {
	TaskID               naming.IssueID `json:"task_id"`
	ForceWorktree        bool           `json:"force_worktree,omitempty"`
	IgnoreAhead          bool           `json:"ignore_ahead,omitempty"`
	IntegrateBeforeClose bool           `json:"integrate_before_close,omitempty"`
}

type taskDeleteRequest struct {
	TaskID         naming.IssueID `json:"task_id"`
	Cleanup        bool           `json:"cleanup,omitempty"`
	StopSession    bool           `json:"stop_session,omitempty"`
	RemoveWorktree bool           `json:"remove_worktree,omitempty"`
	ForceWorktree  bool           `json:"force_worktree,omitempty"`
}

type TaskCloseResult struct {
	TaskID                 string `json:"task_id"`
	Status                 string `json:"status"`
	IntegrationRequested   bool   `json:"integration_requested,omitempty"`
	Integrated             bool   `json:"integrated,omitempty"`
	IntegratedSourceBranch string `json:"integrated_source_branch,omitempty"`
	IntegratedTargetBranch string `json:"integrated_target_branch,omitempty"`
	SessionStopped         bool   `json:"session_stopped,omitempty"`
	WorktreeRemoved        bool   `json:"worktree_removed,omitempty"`
	WorktreeForced         bool   `json:"worktree_forced,omitempty"`
	Revision               uint64 `json:"revision,omitempty"`
}

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
	RootIssueID          string                     `json:"root_issue_id"`
	Runnable             []string                   `json:"runnable"`
	Pending              []TaskPendingStart         `json:"pending,omitempty"`
	Active               []string                   `json:"active,omitempty"`
	ActiveSessions       []TaskActiveSession        `json:"active_sessions,omitempty"`
	SessionStartProgress []TaskSessionStartProgress `json:"session_start_progress,omitempty"`
	Blocked              map[string]string          `json:"blocked"`
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

// TaskCompleteCheckResult is the daemon-owned root close readiness gate.
type TaskCompleteCheckResult struct {
	RootIssueID string   `json:"root_issue_id"`
	Pass        bool     `json:"pass"`
	Reasons     []string `json:"reasons,omitempty"`
	Advice      []string `json:"advice,omitempty"`
}

// TaskIntegrationReadiness is the daemon-owned worker integration evidence gate.
type TaskIntegrationReadiness struct {
	IssueID       string   `json:"issue_id"`
	ParentIssueID string   `json:"parent_issue_id,omitempty"`
	Ready         bool     `json:"ready"`
	Reasons       []string `json:"reasons,omitempty"`
}

type taskIntegrationReadinessRequest struct {
	TaskID  naming.IssueID `json:"task_id"`
	RepoDir string         `json:"repo_dir,omitempty"`
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
	TaskID naming.IssueID `json:"task_id"`
}

// TaskIDsRequest contains the payload used for batch task reads.
type TaskIDsRequest struct {
	TaskIDs           []naming.IssueID `json:"task_ids"`
	IncludeAncestors  bool             `json:"include_ancestors,omitempty"`
	ExcludeDependents bool             `json:"exclude_dependents,omitempty"`
	MetadataOnly      bool             `json:"metadata_only,omitempty"`
}

// TaskDependencyParams contains the payload used for dependency operations.
type TaskDependencyParams struct {
	TaskID            naming.IssueID `json:"task_id"`
	DependsOnID       naming.IssueID `json:"depends_on_id"`
	Type              string         `json:"dependency_type"`
	ForceParentChange bool           `json:"force_parent_change,omitempty"`
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
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return TaskSnapshot{}, fmt.Errorf("invalid task id: %w", err)
	}

	waitCtx, cancel, budget := c.readWait.contextWithBudget(ctx, mode)
	defer cancel()

	resp, err := c.commandJSONResponse(waitCtx, CommandTaskGet, TaskIDRequest{TaskID: parsedTaskID})
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

type getManyTaskSnapshotOptions struct {
	includeAncestors  bool
	excludeDependents bool
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

	resp, err := c.commandJSONResponse(waitCtx, CommandTaskGetMany, TaskIDsRequest{TaskIDs: parsedTaskIDs, IncludeAncestors: opts.includeAncestors, ExcludeDependents: opts.excludeDependents, MetadataOnly: opts.metadataOnly})
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

// ListTasksSnapshot fetches the current task set and revision through the daemon client boundary.
func (c *Client) ListTasksSnapshot(ctx context.Context) (TaskSnapshot, error) {
	return c.ListTasksSnapshotWithMode(ctx, ReadWaitModeDefault)
}

// ListTasksSnapshotWithMode fetches a task snapshot with the requested bounded read budget.
func (c *Client) ListTasksSnapshotWithMode(ctx context.Context, mode ReadWaitMode) (TaskSnapshot, error) {
	waitCtx, cancel, budget := c.readWait.contextWithBudget(ctx, mode)
	defer cancel()

	resp, err := c.commandJSONResponse(waitCtx, CommandTaskList, nil)
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
		retryResp, retryErr := c.commandJSONResponse(waitCtx, CommandTaskList, nil)
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
// Done transitions are always routed through task.close so durable close
// invariants stay daemon-owned even when callers omit close options.
func (c *Client) UpdateTaskStatus(ctx context.Context, taskID string, status domain.Status) error {
	return c.UpdateTaskStatusWithOptions(ctx, taskID, status, TaskStatusOptions{})
}

// UpdateTaskStatusWithOptions updates a task's status. Done transitions are
// represented by the daemon-owned close command; other status moves use the
// lightweight status mutation command.
func (c *Client) UpdateTaskStatusWithOptions(ctx context.Context, taskID string, status domain.Status, opts TaskStatusOptions) error {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	if status == domain.StatusDone {
		_, err := c.CloseTask(ctx, parsedTaskID.String(), opts)
		return err
	}
	return c.commandJSON(ctx, CommandTaskUpdateStatus, TaskStatusRequest{
		TaskID: parsedTaskID,
		Status: status,
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
	}, &out); err != nil {
		return TaskCloseResult{}, err
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

// TaskGraphReadiness returns daemon-owned runnable-leaf policy for a root issue.
func (c *Client) TaskGraphReadiness(ctx context.Context, rootIssueID string) (TaskGraphReadiness, error) {
	parsedRootID, err := naming.ParseIssueID(rootIssueID)
	if err != nil {
		return TaskGraphReadiness{}, fmt.Errorf("invalid root issue id: %w", err)
	}
	var out TaskGraphReadiness
	if err := c.commandJSON(ctx, CommandTaskGraphReadiness, TaskIDRequest{TaskID: parsedRootID}, &out); err != nil {
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
