package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

type mutationFailureContext struct {
	TaskID        string
	Action        string
	TargetStatus  domain.Status
	CurrentStatus domain.Status
	Err           error
	ErrorCode     protocol.ErrorCode
	RawMessage    string
	Recovery      string
}

type mutationFailureDetails struct {
	TaskID         string
	Action         string
	PreviousStatus domain.Status
	TargetStatus   domain.Status
	CurrentStatus  domain.Status
	Reason         string
	Recovery       string
	Message        string
}

func buildMutationFailureDetails(ctx mutationFailureContext) mutationFailureDetails {
	taskID := strings.TrimSpace(ctx.TaskID)
	action := strings.TrimSpace(ctx.Action)
	if action == "" {
		action = "update"
	}
	raw := mutationFailureRawMessage(ctx)
	reason, recovery := splitMutationRecovery(raw)
	if override := strings.TrimSpace(ctx.Recovery); override != "" {
		recovery = override
	}
	code := ctx.ErrorCode
	if code == "" {
		if cmdErr := commandError(ctx.Err); cmdErr != nil {
			code = cmdErr.Code
		}
	}
	reason = normalizeMutationFailureReason(code, action, reason)
	recovery = normalizeMutationRecovery(code, action, reason, recovery)

	parts := []string{formatMutationAttemptSentence(taskID, action, ctx.TargetStatus)}
	if current := statusDisplayName(ctx.CurrentStatus); current != "" {
		parts = append(parts, fmt.Sprintf("It stayed %s", current))
	} else {
		parts = append(parts, "No local state changed")
	}
	if reason != "" {
		parts = append(parts, "Reason: "+reason)
	}
	if recovery != "" {
		parts = append(parts, "Next: "+recovery)
	}
	return mutationFailureDetails{
		TaskID:        taskID,
		Action:        action,
		TargetStatus:  ctx.TargetStatus,
		CurrentStatus: ctx.CurrentStatus,
		Reason:        reason,
		Recovery:      recovery,
		Message:       strings.Join(parts, ". "),
	}
}

func formatMutationFailure(ctx mutationFailureContext) string {
	return buildMutationFailureDetails(ctx).Message
}

func mutationFailureRawMessage(ctx mutationFailureContext) string {
	if raw := strings.TrimSpace(ctx.RawMessage); raw != "" {
		return raw
	}
	if cmdErr := commandError(ctx.Err); cmdErr != nil {
		return strings.TrimSpace(cmdErr.Message)
	}
	if ctx.Err != nil {
		return strings.TrimSpace(ctx.Err.Error())
	}
	return ""
}

func formatMutationAttemptSentence(taskID, action string, target domain.Status) string {
	if target != "" {
		targetName := statusDisplayName(target)
		if taskID != "" {
			return fmt.Sprintf("Could not move %s to %s", taskID, targetName)
		}
		return fmt.Sprintf("Could not move task to %s", targetName)
	}
	if taskID != "" {
		return fmt.Sprintf("Could not %s %s", action, taskID)
	}
	return "Could not " + action
}

func splitMutationRecovery(raw string) (string, string) {
	raw = compactSummaryText(raw)
	if raw == "" {
		return "", ""
	}
	lower := strings.ToLower(raw)
	idx := strings.Index(lower, "next:")
	if idx == -1 {
		return strings.Trim(strings.TrimSpace(raw), "."), ""
	}
	reason := strings.TrimSpace(raw[:idx])
	recovery := strings.TrimSpace(raw[idx+len("next:"):])
	return strings.Trim(reason, ". "), strings.Trim(recovery, ". ")
}

func normalizeMutationFailureReason(code protocol.ErrorCode, action, reason string) string {
	reason = strings.Trim(strings.TrimSpace(reason), ".")
	lower := strings.ToLower(reason)
	switch {
	case reason == "" && code == protocol.ErrorCodeUnavailable:
		return "the daemon is unavailable"
	case reason == "" && code == protocol.ErrorCodeTimeout:
		return "the daemon did not finish before the timeout"
	case reason == "" && code == protocol.ErrorCodeInvalidRequest:
		return "the request was invalid"
	case reason == "" && code == protocol.ErrorCodeConflict:
		return "the daemon blocked the change"
	case strings.Contains(lower, "daemon client unavailable"):
		return "the daemon client is unavailable"
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "timed out") || code == protocol.ErrorCodeTimeout:
		return "the daemon did not finish before the timeout"
	case strings.Contains(lower, "unresolved child") || strings.Contains(lower, "child issues remain unresolved") || strings.Contains(lower, "target-only close is blocked"):
		return "Done is blocked by unresolved child issues"
	case strings.Contains(lower, "dirty") || strings.Contains(lower, "uncommitted") || strings.Contains(lower, "modified or untracked"):
		if strings.Contains(strings.ToLower(action), "cleanup") {
			return "worktree cleanup is blocked by local changes"
		}
		return "close cleanup is blocked by local worktree changes"
	case strings.Contains(lower, "conflict"):
		return "the change conflicts with current daemon state"
	case code == protocol.ErrorCodeInvalidRequest:
		return "the request was invalid: " + reason
	case code == protocol.ErrorCodeUnavailable:
		return "the daemon is unavailable: " + reason
	default:
		return reason
	}
}

func normalizeMutationRecovery(code protocol.ErrorCode, action, reason, recovery string) string {
	recovery = strings.Trim(strings.TrimSpace(recovery), ".")
	if recovery != "" {
		return recovery
	}
	lowerReason := strings.ToLower(reason)
	switch {
	case strings.Contains(lowerReason, "unresolved child"):
		return "press C to close clean children too, or resolve child issues and retry"
	case strings.Contains(lowerReason, "worktree changes"):
		return "commit, discard, or merge the worktree changes, then retry"
	case strings.Contains(strings.ToLower(action), "cleanup") && strings.Contains(lowerReason, "local changes"):
		return "review the worktree and retry with force only if discarding changes is intended"
	case code == protocol.ErrorCodeTimeout || strings.Contains(lowerReason, "timeout"):
		return "refresh the board and retry if the task still needs the change"
	case code == protocol.ErrorCodeConflict || strings.Contains(lowerReason, "conflict"):
		return "refresh the task, resolve the reported blocker, then retry"
	case code == protocol.ErrorCodeUnavailable || strings.Contains(lowerReason, "daemon"):
		return "wait for daemon reconnect or run az daemon start, then retry"
	case code == protocol.ErrorCodeInvalidRequest:
		return "refresh the task and try the action again"
	default:
		return ""
	}
}

func commandError(err error) *daemonclient.CommandError {
	var cmdErr *daemonclient.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr
	}
	return nil
}

func mutationErrorCode(err error) protocol.ErrorCode {
	if cmdErr := commandError(err); cmdErr != nil {
		return cmdErr.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return protocol.ErrorCodeTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return protocol.ErrorCodeTimeout
	}
	return ""
}

func formatStatusMutationFailure(taskID string, previousStatus, targetStatus domain.Status, err error) string {
	return statusMutationFailureDetails(taskID, previousStatus, targetStatus, err).Message
}

func statusMutationFailureDetails(taskID string, previousStatus, targetStatus domain.Status, err error) mutationFailureDetails {
	details := buildMutationFailureDetails(mutationFailureContext{
		TaskID:        taskID,
		Action:        "update status",
		TargetStatus:  targetStatus,
		CurrentStatus: previousStatus,
		Err:           err,
		ErrorCode:     mutationErrorCode(err),
	})
	details.PreviousStatus = previousStatus
	return details
}

func formatOperationMutationFailure(taskID string, currentStatus domain.Status, record protocol.OperationRecord) string {
	return operationMutationFailureDetails(taskID, currentStatus, record).Message
}

func operationMutationFailureDetails(taskID string, currentStatus domain.Status, record protocol.OperationRecord) mutationFailureDetails {
	target := domain.Status("")
	action := mutationActionForOperationKind(record.Kind)
	if strings.TrimSpace(record.Kind) == daemonclient.CommandTaskClose {
		target = domain.StatusDone
	}
	raw := ""
	code := protocol.ErrorCode("")
	if record.Error != nil {
		raw = record.Error.Message
		code = record.Error.Code
	}
	if raw == "" && record.Progress != nil {
		raw = record.Progress.Message
	}
	details := buildMutationFailureDetails(mutationFailureContext{
		TaskID:        taskID,
		Action:        action,
		TargetStatus:  target,
		CurrentStatus: currentStatus,
		ErrorCode:     code,
		RawMessage:    raw,
	})
	details.PreviousStatus = currentStatus
	return details
}

func (m Model) operationFailureUserMessage(evt protocol.EventEnvelope) string {
	var body protocol.OperationEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		return ""
	}
	record := body.Operation
	taskID := m.resolveOperationTaskID(record.IssueID, record.ResourceKeys)
	if taskID == "" {
		taskID = strings.TrimSpace(record.IssueID.String())
	}
	if taskID == "" {
		return ""
	}
	status, _ := m.taskStatusByID(taskID)
	message := formatOperationMutationFailure(taskID, status, record)
	if m.logger != nil {
		raw := ""
		if record.Error != nil {
			raw = record.Error.Message
		}
		m.logger.Warn("daemon operation mutation failed",
			"task_id", taskID,
			"operation_id", record.OperationID,
			"kind", record.Kind,
			"state", record.State,
			"raw_error", raw,
			"user_message", message,
		)
	}
	return message
}

func mutationActionForOperationKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case daemonclient.CommandTaskClose:
		return "close"
	case daemonclient.CommandWorktreeRemove, "worktree_cleanup":
		return "cleanup worktree"
	case daemonclient.CommandSessionStart:
		return "start session"
	case daemonclient.CommandSessionStop:
		return "stop session"
	case daemonclient.CommandSessionResolveConflict:
		return "resolve conflict"
	case "git.merge":
		return "merge"
	default:
		return "update"
	}
}
