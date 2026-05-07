package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/buildinfo"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	CommandTaskList             = "task.list"
	CommandTaskCreate           = "task.create"
	CommandTaskUpdateStatus     = "task.update_status"
	CommandTaskUpdate           = "task.update_details"
	CommandTaskAppendNotes      = "task.append_notes"
	CommandTaskDelete           = "task.delete"
	CommandTaskArchive          = "task.archive"
	CommandTaskDependencyAdd    = "task.dependency.add"
	CommandTaskDependencyRemove = "task.dependency.remove"
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

// TaskAppendNotesRequest appends a single line to task notes.
type TaskAppendNotesRequest struct {
	TaskID naming.IssueID `json:"task_id"`
	Line   string         `json:"line"`
}

// TaskIDRequest contains the payload used for delete/archive task operations.
type TaskIDRequest struct {
	TaskID naming.IssueID `json:"task_id"`
}

// TaskDependencyParams contains the payload used for dependency operations.
type TaskDependencyParams struct {
	TaskID      naming.IssueID `json:"task_id"`
	DependsOnID naming.IssueID `json:"depends_on_id"`
	Type        string         `json:"dependency_type"`
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
	parsedTaskID, err := naming.ParseIssueID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task id: %w", err)
	}
	return c.commandJSON(ctx, CommandTaskUpdateStatus, TaskStatusRequest{
		TaskID: parsedTaskID,
		Status: status,
	}, nil)
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
