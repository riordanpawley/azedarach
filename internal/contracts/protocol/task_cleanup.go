package protocol

import (
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// CommandTaskBulkCleanup performs a potentially long-running sequence of
// issue lifecycle cleanup operations in one daemon request.
const CommandTaskBulkCleanup = "task.bulk_cleanup"

type TaskBulkCleanupRequest struct {
	TaskIDs         []string      `json:"task_ids,omitempty"`
	Statuses        []string      `json:"statuses,omitempty"`
	Query           string        `json:"query,omitempty"`
	UpdatedBefore   *time.Time    `json:"updated_before,omitempty"`
	Limit           int           `json:"limit,omitempty"`
	DryRun          bool          `json:"dry_run,omitempty"`
	CloseOutcome    string        `json:"closed_outcome,omitempty"`
	PerIssueTimeout time.Duration `json:"per_issue_timeout,omitempty"`
}

type TaskBulkCleanupItem struct {
	TaskID  string           `json:"task_id"`
	Action  string           `json:"action"`
	Status  string           `json:"status,omitempty"`
	Success bool             `json:"success"`
	Skipped bool             `json:"skipped,omitempty"`
	Error   string           `json:"error,omitempty"`
	Result  *TaskCloseResult `json:"result,omitempty"`
}

type TaskBulkCleanupResult struct {
	DryRun bool                  `json:"dry_run"`
	Action string                `json:"action"`
	Items  []TaskBulkCleanupItem `json:"items"`
}

type TaskCloseResult struct {
	TaskID                     string                         `json:"task_id"`
	Status                     string                         `json:"status"`
	ContextRisk                *domain.IssueContextRiskPacket `json:"context_risk,omitempty"`
	IntegrationRequested       bool                           `json:"integration_requested,omitempty"`
	Integrated                 bool                           `json:"integrated,omitempty"`
	IntegratedSourceBranch     string                         `json:"integrated_source_branch,omitempty"`
	IntegratedTargetBranch     string                         `json:"integrated_target_branch,omitempty"`
	SessionStopped             bool                           `json:"session_stopped,omitempty"`
	WorktreeRemoved            bool                           `json:"worktree_removed,omitempty"`
	WorktreeCleanupDeferred    bool                           `json:"worktree_cleanup_deferred,omitempty"`
	WorktreeCleanupOperationID string                         `json:"worktree_cleanup_operation_id,omitempty"`
	WorktreeForced             bool                           `json:"worktree_forced,omitempty"`
	Revision                   uint64                         `json:"revision,omitempty"`
	Phases                     []TaskClosePhaseTiming         `json:"phases,omitempty"`
	AutoClosedChildren         []string                       `json:"auto_closed_children,omitempty"`
}

type TaskClosePhaseTiming struct {
	Name       string `json:"name"`
	ElapsedMS  int64  `json:"elapsed_ms"`
	Skipped    bool   `json:"skipped,omitempty"`
	Hook       string `json:"hook,omitempty"`
	Command    string `json:"command,omitempty"`
	ExitStatus *int   `json:"exit_status,omitempty"`
	Blocking   *bool  `json:"blocking,omitempty"`
	TimedOut   *bool  `json:"timed_out,omitempty"`
}

func (p TaskClosePhaseTiming) Elapsed() time.Duration {
	if p.ElapsedMS <= 0 {
		return 0
	}
	return time.Duration(p.ElapsedMS) * time.Millisecond
}
