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
	AutoFinalizeOnClose bool
	SkipClosePreflight  bool
}

// CloseGuardOptions controls which target runtime assets may remain because
// the caller is about to clean them before writing the closed status.
type CloseGuardOptions struct {
	AllowTargetSession  bool
	AllowTargetWorktree bool
	ForceWorktree       bool
}

// CloseGuardResult records the preflight inputs used for a close transition.
type CloseGuardResult struct {
	Task     domain.Task
	Worktree string
	Status   GitStatus
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
	TaskIDs []naming.IssueID `json:"task_ids"`
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
	TaskID      naming.IssueID `json:"task_id"`
	DependsOnID naming.IssueID `json:"depends_on_id"`
	Type        string         `json:"dependency_type"`
	Confirm     bool           `json:"confirm"`
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

	resp, err := c.commandJSONResponse(waitCtx, CommandTaskGetMany, TaskIDsRequest{TaskIDs: parsedTaskIDs})
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
func (c *Client) UpdateTaskStatus(ctx context.Context, taskID string, status domain.Status) error {
	return c.UpdateTaskStatusWithOptions(ctx, taskID, status, TaskStatusOptions{})
}

// UpdateTaskStatusWithOptions updates a task's status and can finalize runtime
// attachments before committing a closed/done transition.
func (c *Client) UpdateTaskStatusWithOptions(ctx context.Context, taskID string, status domain.Status, opts TaskStatusOptions) error {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	if status == domain.StatusDone && !opts.SkipClosePreflight {
		if opts.AutoFinalizeOnClose {
			if err := c.finalizeTaskRuntimeAttachments(ctx, parsedTaskID.String()); err != nil {
				return err
			}
		} else if _, err := c.ValidateTaskClose(ctx, parsedTaskID.String()); err != nil {
			return err
		}
	}
	return c.commandJSON(ctx, CommandTaskUpdateStatus, TaskStatusRequest{
		TaskID: parsedTaskID,
		Status: status,
	}, nil)
}

func (c *Client) finalizeTaskRuntimeAttachments(ctx context.Context, taskID string) error {
	guard, err := c.ValidateTaskCloseWithOptions(ctx, taskID, CloseGuardOptions{
		AllowTargetSession:  true,
		AllowTargetWorktree: true,
	})
	if err != nil {
		return err
	}

	task := guard.Task
	if task.HasTmuxSession || task.Session != nil {
		if _, err := c.StopSession(ctx, taskID); err != nil {
			return fmt.Errorf("stop session before closing %s: %w", taskID, err)
		}
	}
	if closeGuardTaskHasWorktree(task) {
		if err := c.RemoveWorktreeWithOptions(ctx, taskID, false); err != nil {
			return fmt.Errorf("remove worktree before closing %s: %w", taskID, err)
		}
	}
	return nil
}

// ValidateTaskClose rejects close/done transitions for worktrees that still
// have tracked changes, conflicts, or commits ahead of their integration base.
func (c *Client) ValidateTaskClose(ctx context.Context, taskID string) (CloseGuardResult, error) {
	return c.ValidateTaskCloseWithOptions(ctx, taskID, CloseGuardOptions{})
}

// ValidateTaskCloseWithOptions rejects close/done transitions unless the target
// issue and its children are in a closeable state.
func (c *Client) ValidateTaskCloseWithOptions(ctx context.Context, taskID string, opts CloseGuardOptions) (CloseGuardResult, error) {
	snapshot, err := c.ListTasksSnapshot(ctx)
	if err != nil {
		return CloseGuardResult{}, fmt.Errorf("inspect runtime attachments before closing %s: %w", taskID, err)
	}
	var task domain.Task
	found := false
	for _, candidate := range snapshot.Tasks {
		if strings.EqualFold(strings.TrimSpace(candidate.ID.String()), taskID) {
			task = candidate
			found = true
			break
		}
	}
	if !found {
		return CloseGuardResult{}, fmt.Errorf("issue not found: %s", taskID)
	}

	if reasons := closeGuardRuntimeBlockers(task, opts); len(reasons) > 0 {
		return CloseGuardResult{}, fmt.Errorf("cannot close issue %s: %s", taskID, strings.Join(reasons, "; "))
	}
	if reasons := closeGuardChildBlockers(task.ID, snapshot.Tasks); len(reasons) > 0 {
		return CloseGuardResult{}, fmt.Errorf("cannot close issue %s: %s", taskID, strings.Join(reasons, "; "))
	}

	worktree := closeGuardTaskWorktree(task)
	if worktree == "" && task.HasWorktree {
		resolved, ok, err := c.worktreePathForIssue(ctx, taskID)
		if err != nil {
			return CloseGuardResult{}, fmt.Errorf("inspect worktree before closing %s: %w", taskID, err)
		}
		if !ok || strings.TrimSpace(resolved) == "" {
			if opts.ForceWorktree {
				return CloseGuardResult{Task: task}, nil
			}
			return CloseGuardResult{}, fmt.Errorf("cannot close issue %s: worktree is projected but path is unavailable", taskID)
		}
		worktree = resolved
	}
	if strings.TrimSpace(worktree) == "" {
		return CloseGuardResult{Task: task}, nil
	}
	if opts.ForceWorktree {
		return CloseGuardResult{
			Task:     task,
			Worktree: worktree,
		}, nil
	}

	status, err := c.GitStatusRefresh(ctx, worktree)
	if err != nil {
		return CloseGuardResult{}, fmt.Errorf("inspect git status before closing %s: %w", taskID, err)
	}
	if reasons := closeGuardBlockers(status); len(reasons) > 0 {
		return CloseGuardResult{}, fmt.Errorf("cannot close issue %s: %s", taskID, strings.Join(reasons, "; "))
	}
	return CloseGuardResult{
		Task:     task,
		Worktree: worktree,
		Status:   status,
	}, nil
}

func (c *Client) worktreePathForIssue(ctx context.Context, taskID string) (string, bool, error) {
	worktrees, err := c.ListWorktrees(ctx)
	if err != nil {
		return "", false, err
	}
	for _, worktree := range worktrees {
		if strings.EqualFold(strings.TrimSpace(worktree.IssueID), taskID) {
			return strings.TrimSpace(worktree.Path), true, nil
		}
	}
	return "", false, nil
}

func closeGuardRuntimeBlockers(task domain.Task, opts CloseGuardOptions) []string {
	reasons := make([]string, 0, 2)
	if !opts.AllowTargetSession && closeGuardTaskHasSession(task) {
		reasons = append(reasons, "issue still has a session")
	}
	if !opts.AllowTargetWorktree && closeGuardTaskHasWorktree(task) {
		reasons = append(reasons, "issue still has a worktree")
	}
	return reasons
}

func closeGuardChildBlockers(parentID naming.IssueID, tasks []domain.Task) []string {
	childrenByParent := closeGuardChildrenByParent(tasks)
	descendants := closeGuardDescendants(parentID, childrenByParent)
	if len(descendants) == 0 {
		return nil
	}
	byID := make(map[naming.IssueID]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}
	blocked := make([]string, 0, len(descendants))
	for _, childID := range descendants {
		child, ok := byID[childID]
		if !ok {
			continue
		}
		reasons := closeGuardChildReasons(child)
		if len(reasons) == 0 {
			continue
		}
		blocked = append(blocked, fmt.Sprintf("%s (%s)", child.ID.String(), strings.Join(reasons, ", ")))
	}
	if len(blocked) == 0 {
		return nil
	}
	return []string{"unresolved child issues remain: " + strings.Join(blocked, "; ")}
}

func closeGuardChildrenByParent(tasks []domain.Task) map[naming.IssueID][]naming.IssueID {
	children := make(map[naming.IssueID][]naming.IssueID)
	seen := make(map[naming.IssueID]map[naming.IssueID]struct{})
	add := func(parentID, childID naming.IssueID) {
		if parentID.IsZero() || childID.IsZero() {
			return
		}
		if seen[parentID] == nil {
			seen[parentID] = make(map[naming.IssueID]struct{})
		}
		if _, ok := seen[parentID][childID]; ok {
			return
		}
		seen[parentID][childID] = struct{}{}
		children[parentID] = append(children[parentID], childID)
	}
	for _, task := range tasks {
		if task.ParentID != nil {
			add(*task.ParentID, task.ID)
		}
		for _, dep := range task.Dependencies {
			if dep.Type == domain.DependencyParentChild || string(dep.Type) == "parent_child" {
				add(dep.ID, task.ID)
			}
		}
	}
	return children
}

func closeGuardDescendants(rootID naming.IssueID, children map[naming.IssueID][]naming.IssueID) []naming.IssueID {
	out := make([]naming.IssueID, 0)
	seen := make(map[naming.IssueID]struct{})
	queue := append([]naming.IssueID(nil), children[rootID]...)
	for len(queue) > 0 {
		childID := queue[0]
		queue = queue[1:]
		if _, ok := seen[childID]; ok {
			continue
		}
		seen[childID] = struct{}{}
		out = append(out, childID)
		queue = append(queue, children[childID]...)
	}
	return out
}

func closeGuardChildReasons(task domain.Task) []string {
	reasons := make([]string, 0, 3)
	if task.Status != domain.StatusDone {
		reasons = append(reasons, string(task.Status))
	}
	if closeGuardTaskHasSession(task) {
		reasons = append(reasons, "session")
	}
	if closeGuardTaskHasWorktree(task) {
		reasons = append(reasons, "worktree")
	}
	return reasons
}

func closeGuardTaskHasSession(task domain.Task) bool {
	return task.HasTmuxSession || task.Session != nil
}

func closeGuardTaskHasWorktree(task domain.Task) bool {
	return task.HasWorktree || closeGuardTaskWorktree(task) != ""
}

func closeGuardTaskWorktree(task domain.Task) string {
	if task.Session == nil {
		return ""
	}
	return strings.TrimSpace(task.Session.Worktree)
}

func closeGuardBlockers(status GitStatus) []string {
	dirty := closeGuardDirtyFiles(status)
	reasons := make([]string, 0, 3)
	if len(dirty) > 0 {
		reasons = append(reasons, "worktree has local changes: "+strings.Join(dirty, ", "))
	}
	if status.HasConflicts || len(status.Conflicted) > 0 {
		conflicts := append([]string(nil), status.Conflicted...)
		if len(conflicts) == 0 {
			reasons = append(reasons, "worktree has conflicts")
		} else {
			reasons = append(reasons, "worktree has conflicts: "+strings.Join(conflicts, ", "))
		}
	}
	if status.GitAheadCount > 0 {
		reasons = append(reasons, fmt.Sprintf("branch is ahead by %d commit(s)", status.GitAheadCount))
	}
	return reasons
}

func closeGuardDirtyFiles(status GitStatus) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(status.Staged)+len(status.Modified)+len(status.Added)+len(status.Deleted)+len(status.Untracked)+len(status.Conflicted))
	appendUnique := func(files []string) {
		for _, file := range files {
			file = strings.TrimSpace(file)
			if file == "" {
				continue
			}
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			out = append(out, file)
		}
	}
	appendUnique(status.Staged)
	appendUnique(status.Modified)
	appendUnique(status.Added)
	appendUnique(status.Deleted)
	appendUnique(status.Untracked)
	appendUnique(status.Conflicted)
	return out
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
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	return c.commandJSON(ctx, CommandTaskDelete, TaskIDRequest{TaskID: parsedTaskID}, nil)
}

// ArchiveTask archives a task through the daemon client boundary.
func (c *Client) ArchiveTask(ctx context.Context, taskID string) error {
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	return c.commandJSON(ctx, CommandTaskArchive, TaskIDRequest{TaskID: parsedTaskID}, nil)
}

// AddTaskDependency creates or restores a dependency between two tasks.
func (c *Client) AddTaskDependency(ctx context.Context, params TaskDependencyParams) error {
	return c.commandJSON(ctx, CommandTaskDependencyAdd, params, nil)
}

// RemoveTaskDependency tombstones a dependency between two tasks.
func (c *Client) RemoveTaskDependency(ctx context.Context, params TaskDependencyRemoveParams) error {
	return c.commandJSON(ctx, CommandTaskDependencyRemove, params, nil)
}
