package daemonclient

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	CommandTaskList         = "task.list"
	CommandTaskCreate       = "task.create"
	CommandTaskUpdateStatus = "task.update_status"
	CommandTaskUpdate       = "task.update_details"
	CommandTaskDelete       = "task.delete"
	CommandTaskArchive      = "task.archive"
)

// TaskCreateParams contains the payload used to create a task through the shared daemon client.
type TaskCreateParams struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Type        domain.TaskType `json:"type"`
	Priority    domain.Priority `json:"priority"`
	ParentID    *string         `json:"parent_id,omitempty"`
}

// TaskUpdateParams contains the payload used to update task details through the shared daemon client.
type TaskUpdateParams struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Type        domain.TaskType `json:"type"`
	Priority    domain.Priority `json:"priority"`
}

// TaskStatusRequest contains the payload used to update a task status.
type TaskStatusRequest struct {
	TaskID string        `json:"task_id"`
	Status domain.Status `json:"status"`
}

// TaskIDRequest contains the payload used for delete/archive task operations.
type TaskIDRequest struct {
	TaskID string `json:"task_id"`
}

// TaskIDResponse is returned by commands that allocate a new task identifier.
type TaskIDResponse struct {
	TaskID string `json:"task_id"`
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

func (c *Client) commandJSON(ctx context.Context, command string, body any, out any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s request: %w", command, err)
		}
	}

	resp, err := c.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       fmt.Sprintf("%s-%d", command, time.Now().UTC().UnixNano()),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         command,
		SentAt:          time.Now().UTC(),
		Body:            payload,
	})
	if err != nil {
		return err
	}
	if !resp.OK {
		return commandResponseError(command, resp.Error)
	}

	if out != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return fmt.Errorf("decode %s response: %w", command, err)
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
	var tasks []domain.Task
	if err := c.commandJSON(ctx, CommandTaskList, nil, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
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
	return resp.TaskID, nil
}

// UpdateTaskStatus updates a task's status through the daemon client boundary.
func (c *Client) UpdateTaskStatus(ctx context.Context, taskID string, status domain.Status) error {
	return c.commandJSON(ctx, CommandTaskUpdateStatus, TaskStatusRequest{
		TaskID: taskID,
		Status: status,
	}, nil)
}

// UpdateTaskDetails updates a task's details through the daemon client boundary.
func (c *Client) UpdateTaskDetails(ctx context.Context, taskID string, params TaskUpdateParams) error {
	body := struct {
		TaskID string `json:"task_id"`
		TaskUpdateParams
	}{
		TaskID:           taskID,
		TaskUpdateParams: params,
	}
	return c.commandJSON(ctx, CommandTaskUpdate, body, nil)
}

// DeleteTask deletes a task through the daemon client boundary.
func (c *Client) DeleteTask(ctx context.Context, taskID string) error {
	return c.commandJSON(ctx, CommandTaskDelete, TaskIDRequest{TaskID: taskID}, nil)
}

// ArchiveTask archives a task through the daemon client boundary.
func (c *Client) ArchiveTask(ctx context.Context, taskID string) error {
	return c.commandJSON(ctx, CommandTaskArchive, TaskIDRequest{TaskID: taskID}, nil)
}
