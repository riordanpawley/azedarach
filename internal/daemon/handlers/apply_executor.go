package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

// ApplyTaskService captures the task mutation operations needed by the bulk apply executor.
type ApplyTaskService interface {
	Create(context.Context, issues.CreateTaskParams) (string, error)
	Update(context.Context, string, domain.Status) error
	UpdateDetails(context.Context, string, issues.UpdateTaskParams) error
	AddDependency(context.Context, string, string, string) error
	RemoveDependency(context.Context, string, string, string) error
	Delete(context.Context, string) error
	Archive(context.Context, string) error
}

// ApplyRevisionManager coordinates deterministic revision sequencing and event publication.
type ApplyRevisionManager interface {
	CurrentRevision(projectID string) uint64
	NextRevision(projectID string) uint64
	PublishTaskEvent(req protocol.RequestEnvelope, eventName string, rev uint64)
}

// ApplyExecutionOperation captures one committed operation in deterministic input order.
type ApplyExecutionOperation struct {
	Index    int    `json:"index"`
	Command  string `json:"command"`
	TaskID   string `json:"task_id,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
}

const (
	applyExecutionOutcomeStatusSuccess = "success"
	applyExecutionOutcomeStatusFailure = "failure"
)

// ApplyExecutionOutcome captures the reported outcome for one requested operation.
type ApplyExecutionOutcome struct {
	Index    int    `json:"index"`
	Command  string `json:"command"`
	Status   string `json:"status"`
	TaskID   string `json:"task_id,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ApplyExecutionSummary captures the aggregate execution result across all requested operations.
type ApplyExecutionSummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

// ApplyExecutionResult captures the ordered outcome of a non-dry-run apply request.
type ApplyExecutionResult struct {
	ProjectID        string                    `json:"project_id"`
	SnapshotRevision uint64                    `json:"snapshot_revision"`
	Revision         uint64                    `json:"revision"`
	DryRun           bool                      `json:"dry_run"`
	Operations       []ApplyExecutionOperation `json:"operations"`
	Outcomes         []ApplyExecutionOutcome   `json:"outcomes"`
	Summary          ApplyExecutionSummary     `json:"summary"`
}

// ApplyHandler executes bulk apply requests against the authoritative task service.
type ApplyHandler struct {
	service   ApplyTaskService
	revisions ApplyRevisionManager
}

// NewApplyHandler returns a bulk apply handler.
func NewApplyHandler(service ApplyTaskService, revisions ApplyRevisionManager) *ApplyHandler {
	return &ApplyHandler{
		service:   service,
		revisions: revisions,
	}
}

// Handle validates, plans, and executes a bulk apply request.
func (h *ApplyHandler) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	resp := protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     nowUTC(),
	}

	applyReq, err := ValidateApplyRequestBody(req.Body)
	if err != nil {
		resp.Error = mapApplyValidationError(err)
		return resp
	}

	if applyReq.DryRun {
		preview := BuildApplyDryRunPreview(*applyReq)
		body, err := json.Marshal(preview)
		if err != nil {
			resp.Error = &protocol.ErrorEnvelope{
				Code:      protocol.ErrorCodeInternal,
				Message:   fmt.Sprintf("marshal dry-run response body: %v", err),
				Retryable: false,
			}
			return resp
		}

		resp.OK = true
		resp.Body = body
		return resp
	}

	projectID := projectIDFromMeta(req.Meta)
	if h.revisions != nil {
		gate := EvaluateApplyRevisionGate(*applyReq, h.revisions.CurrentRevision(projectID))
		if !gate.Allowed {
			resp.Error = gate.Error
			return resp
		}
	}

	result, err := h.execute(ctx, req, projectID, *applyReq)
	if err != nil {
		resp.Error = mapApplyExecutionError(err)
		return resp
	}

	body, err := json.Marshal(result)
	if err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   fmt.Sprintf("marshal apply response body: %v", err),
			Retryable: false,
		}
		return resp
	}

	resp.OK = true
	resp.Revision = result.Revision
	resp.Body = body
	return resp
}

func (h *ApplyHandler) execute(ctx context.Context, req protocol.RequestEnvelope, projectID string, applyReq protocol.ApplyRequestBody) (ApplyExecutionResult, error) {
	result := ApplyExecutionResult{
		ProjectID:        projectID,
		SnapshotRevision: applyReq.SnapshotRevision,
		DryRun:           false,
		Operations:       make([]ApplyExecutionOperation, 0, len(applyReq.Operations)),
		Outcomes:         make([]ApplyExecutionOutcome, 0, len(applyReq.Operations)),
	}

	for i, op := range applyReq.Operations {
		executed, err := h.executeOperation(ctx, i, op)
		outcome := ApplyExecutionOutcome{
			Index:   i,
			Command: op.Command,
		}
		if err != nil {
			outcome.Status = applyExecutionOutcomeStatusFailure
			outcome.Error = err.Error()
			result.Outcomes = append(result.Outcomes, outcome)
			continue
		}

		if h.revisions != nil {
			rev := h.revisions.NextRevision(projectID)
			h.revisions.PublishTaskEvent(req, applyEventName(op.Command), rev)
			executed.Revision = rev
			result.Revision = rev
			outcome.Revision = rev
		}

		result.Operations = append(result.Operations, executed)
		outcome.Status = applyExecutionOutcomeStatusSuccess
		outcome.TaskID = executed.TaskID
		result.Outcomes = append(result.Outcomes, outcome)
	}

	sort.SliceStable(result.Outcomes, func(i, j int) bool {
		return result.Outcomes[i].Index < result.Outcomes[j].Index
	})
	result.Summary = ApplyExecutionSummary{
		Total:     len(applyReq.Operations),
		Succeeded: len(result.Operations),
		Failed:    len(result.Outcomes) - len(result.Operations),
	}

	return result, nil
}

func (h *ApplyHandler) executeOperation(ctx context.Context, index int, op protocol.ApplyOperationBody) (ApplyExecutionOperation, error) {
	operation := ApplyExecutionOperation{
		Index:   index,
		Command: op.Command,
	}

	switch op.Command {
	case applyCommandTaskCreate:
		var payload applyTaskCreateBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return ApplyExecutionOperation{}, fmt.Errorf("decode apply create payload: %w", err)
		}

		taskType, err := parseApplyTaskType(payload.Type)
		if err != nil {
			return ApplyExecutionOperation{}, err
		}
		priority, err := parseApplyPriority(payload.Priority)
		if err != nil {
			return ApplyExecutionOperation{}, err
		}

		taskID, err := h.service.Create(ctx, issues.CreateTaskParams{
			Title:       payload.Title,
			Description: payload.Description,
			Type:        taskType,
			Priority:    priority,
			ParentID:    payload.ParentID,
		})
		if err != nil {
			return ApplyExecutionOperation{}, err
		}

		operation.TaskID = taskID
		return operation, nil

	case applyCommandTaskUpdate:
		var payload applyTaskUpdateBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return ApplyExecutionOperation{}, fmt.Errorf("decode apply update payload: %w", err)
		}

		taskType, err := parseApplyTaskType(payload.Type)
		if err != nil {
			return ApplyExecutionOperation{}, err
		}
		priority, err := parseApplyPriority(payload.Priority)
		if err != nil {
			return ApplyExecutionOperation{}, err
		}

		if err := h.service.UpdateDetails(ctx, payload.TaskID, issues.UpdateTaskParams{
			Title:       payload.Title,
			Description: payload.Description,
			Type:        taskType,
			Priority:    priority,
		}); err != nil {
			return ApplyExecutionOperation{}, err
		}

		operation.TaskID = payload.TaskID
		return operation, nil

	case applyCommandTaskUpdateStatus:
		var payload applyTaskStatusBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return ApplyExecutionOperation{}, fmt.Errorf("decode apply status payload: %w", err)
		}

		status, err := parseApplyStatus(payload.Status)
		if err != nil {
			return ApplyExecutionOperation{}, err
		}

		if err := h.service.Update(ctx, payload.TaskID, status); err != nil {
			return ApplyExecutionOperation{}, err
		}

		operation.TaskID = payload.TaskID
		return operation, nil

	case applyCommandTaskDelete:
		var payload applyTaskIDBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return ApplyExecutionOperation{}, fmt.Errorf("decode apply delete payload: %w", err)
		}

		if err := h.service.Delete(ctx, payload.TaskID); err != nil {
			return ApplyExecutionOperation{}, err
		}

		operation.TaskID = payload.TaskID
		return operation, nil

	case applyCommandTaskArchive:
		var payload applyTaskIDBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return ApplyExecutionOperation{}, fmt.Errorf("decode apply archive payload: %w", err)
		}

		if err := h.service.Archive(ctx, payload.TaskID); err != nil {
			return ApplyExecutionOperation{}, err
		}

		operation.TaskID = payload.TaskID
		return operation, nil

	case applyCommandDependencyAdd:
		var payload applyDependencyBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return ApplyExecutionOperation{}, fmt.Errorf("decode apply dependency add payload: %w", err)
		}
		if err := h.service.AddDependency(ctx, payload.TaskID, payload.DependsOnID, payload.Type); err != nil {
			return ApplyExecutionOperation{}, err
		}
		operation.TaskID = payload.TaskID
		return operation, nil

	case applyCommandDependencyRemove:
		var payload applyDependencyRemoveBody
		if err := json.Unmarshal(op.Body, &payload); err != nil {
			return ApplyExecutionOperation{}, fmt.Errorf("decode apply dependency remove payload: %w", err)
		}
		if err := h.service.RemoveDependency(issues.WithDependencyRemovalConfirmation(ctx), payload.TaskID, payload.DependsOnID, payload.Type); err != nil {
			return ApplyExecutionOperation{}, err
		}
		operation.TaskID = payload.TaskID
		return operation, nil

	default:
		return ApplyExecutionOperation{}, fmt.Errorf("unsupported apply command: %s", op.Command)
	}
}

func parseApplyTaskType(value string) (domain.TaskType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "task":
		return domain.TypeTask, nil
	case "bug":
		return domain.TypeBug, nil
	case "feature":
		return domain.TypeFeature, nil
	case "epic":
		return domain.TypeEpic, nil
	case "chore":
		return domain.TypeChore, nil
	default:
		return "", fmt.Errorf("unsupported task type: %s", value)
	}
}

func parseApplyPriority(value string) (domain.Priority, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "p0", "0", "critical":
		return domain.P0, nil
	case "p1", "1", "high":
		return domain.P1, nil
	case "p2", "2", "medium":
		return domain.P2, nil
	case "p3", "3", "low":
		return domain.P3, nil
	case "p4", "4", "backlog":
		return domain.P4, nil
	default:
		return domain.P4, fmt.Errorf("unsupported priority: %s", value)
	}
}

func parseApplyStatus(value string) (domain.Status, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "open":
		return domain.StatusOpen, nil
	case "in_progress", "in progress", "progress":
		return domain.StatusInProgress, nil
	case "blocked":
		return domain.StatusBlocked, nil
	case "done", "closed", "complete", "completed":
		return domain.StatusDone, nil
	default:
		return "", fmt.Errorf("unsupported status: %s", value)
	}
}

func applyEventName(command string) string {
	switch command {
	case applyCommandTaskCreate:
		return "task.created"
	case applyCommandTaskUpdate, applyCommandTaskUpdateStatus:
		return "task.updated"
	case applyCommandTaskDelete:
		return "task.deleted"
	case applyCommandTaskArchive:
		return "task.archived"
	default:
		return "task.updated"
	}
}

func mapApplyValidationError(err error) *protocol.ErrorEnvelope {
	var vErr *protocol.ApplyValidationError
	if errors.As(err, &vErr) {
		return &protocol.ErrorEnvelope{
			Code:      vErr.Code,
			Message:   vErr.Message,
			Retryable: false,
		}
	}

	return &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInvalidRequest,
		Message:   err.Error(),
		Retryable: false,
	}
}

func mapApplyExecutionError(err error) *protocol.ErrorEnvelope {
	if err == nil {
		return nil
	}

	return &protocol.ErrorEnvelope{
		Code:      protocol.ErrorCodeInternal,
		Message:   err.Error(),
		Retryable: false,
	}
}

func projectIDFromMeta(meta protocol.Metadata) string {
	if meta.ProjectID != "" {
		return meta.ProjectID
	}
	return "default"
}

func nowUTC() (t time.Time) {
	return time.Now().UTC()
}
