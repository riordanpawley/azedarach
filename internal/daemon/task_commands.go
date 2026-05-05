package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type taskSnapshotExportBody struct {
	SchemaVersion    uint16                      `json:"schema_version"`
	ProtocolVersion  protocol.Version            `json:"protocol_version"`
	SnapshotRevision uint64                      `json:"snapshot_revision"`
	CapturedAtMs     int64                       `json:"captured_at_ms"`
	ProjectID        string                      `json:"project_id"`
	TaskCount        int                         `json:"task_count"`
	SessionCount     int                         `json:"session_count"`
	Tasks            []taskSnapshotExportTask    `json:"tasks"`
	Sessions         []taskSnapshotExportSession `json:"sessions"`
}

type taskSnapshotExportTask struct {
	ID              string          `json:"id"`
	Title           string          `json:"title"`
	Status          domain.Status   `json:"status"`
	Priority        domain.Priority `json:"priority"`
	Type            domain.TaskType `json:"issue_type"`
	ParentID        *string         `json:"parent_id,omitempty"`
	DependencyCount int             `json:"dependency_count"`
	SessionAttached bool            `json:"session_attached"`
	Critical        bool            `json:"critical"`
}

type taskSnapshotExportSession struct {
	Name string `json:"name"`
}

const (
	taskInvariantTaskListFreshness daemonInvariantID = daemonInvariantTaskListFreshness
)

func (d *Daemon) sourceForTaskInvariant(invariant daemonInvariantID) daemonInvariantSource {
	return sourceForInvariant(invariant)
}

func (d *Daemon) handleTaskList(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	d.triggerWorktreeStateRefresh(projectID)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list requested", "project_id", projectID)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.ListWithRuntime(ctx, projectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), lastCheckedAt, freshness, tasks)
	body, err := json.Marshal(payload)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = payload.SnapshotRevision
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list completed", "project_id", projectID, "task_count", len(tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds())
	}
	return resp, nil
}

func (d *Daemon) handleTaskGet(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	taskID := strings.TrimSpace(cmd.TaskID)
	if taskID == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "task_id is required"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task get requested", "project_id", projectID, "task_id", taskID)
	}
	d.refreshIssueWorktreeState(ctx, projectID, taskID)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.GetWithDependencyContextRuntime(ctx, projectID, taskID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || strings.Contains(err.Error(), domain.ErrNotFound.Error()) {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Info("daemon task get not found", "project_id", projectID, "task_id", taskID, "elapsed_ms", time.Since(startedAt).Milliseconds())
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue not found: %s", taskID)), nil
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("daemon task get failed", "project_id", projectID, "task_id", taskID, "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
		}
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), lastCheckedAt, freshness, tasks)
	body, err := json.Marshal(payload)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = payload.SnapshotRevision
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task get completed", "project_id", projectID, "task_id", taskID, "context_task_count", len(tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds())
	}
	return resp, nil
}

func buildTaskListSnapshotPayload(projectID string, revision uint64, lastCheckedAt time.Time, freshness protocol.TaskListFreshness, tasks []domain.Task) protocol.TaskListSnapshotPayload {
	if lastCheckedAt.IsZero() {
		lastCheckedAt = timeNow()
	}
	if !freshness.Valid() {
		freshness = protocol.TaskListFreshnessFresh
	}
	return protocol.TaskListSnapshotPayload{
		SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: revision,
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    lastCheckedAt.UTC(),
		Freshness:        freshness,
		Tasks:            tasks,
	}
}

func (d *Daemon) taskEventBody(ctx context.Context, projectID, taskID string) protocol.TaskEventBody {
	body := protocol.TaskEventBody{
		ProjectID: naming.ProjectID(projectID),
		TaskID:    naming.IssueID(taskID),
		UpdatedAt: timeNow().UTC(),
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return body
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load task event body failed", "project_id", projectID, "task_id", taskID, "error", err)
		}
		return body
	}
	body.Task = &task
	body.TaskID = naming.IssueID(task.ID)
	if !task.UpdatedAt.IsZero() {
		body.UpdatedAt = task.UpdatedAt.UTC()
	}
	return body
}

func taskEventBodyFromTask(projectID string, task domain.Task) protocol.TaskEventBody {
	updatedAt := timeNow().UTC()
	if !task.UpdatedAt.IsZero() {
		updatedAt = task.UpdatedAt.UTC()
	}
	return protocol.TaskEventBody{
		ProjectID: naming.ProjectID(projectID),
		TaskID:    naming.IssueID(task.ID),
		Task:      &task,
		UpdatedAt: updatedAt,
	}
}

const taskListSnapshotStaleAfter = 15 * time.Second

func (d *Daemon) taskListSnapshotFreshness(ctx context.Context, projectID string) (time.Time, protocol.TaskListFreshness) {
	lastCheckedAt := time.Time{}
	projectID = d.canonicalProjectID(projectID)

	sessionFreshnessSource := d.sourceForTaskInvariant(taskInvariantTaskListFreshness)
	if usesProjectionSource(sessionFreshnessSource) && d.sessionStore != nil {
		if err := d.refreshSessionInvariantCacheIfConfigured(ctx, projectID); err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("refresh session freshness cache failed", "project_id", projectID, "error", err)
			}
		} else {
			snapshot := d.sessionStore.ReadSnapshot(projectID)
			sessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
			for _, session := range snapshot.Sessions {
				sessions = append(sessions, session)
			}
			for _, session := range sessions {
				lastCheckedAt = laterTime(lastCheckedAt, session.UpdatedAt)
			}
		}
	}

	if d.worktreeRuntimeStateStoreIfConfigured(projectID) != nil {
		worktrees, err := d.worktreeRuntimeStateStoreIfConfigured(projectID).ListWorktreeStates(ctx, projectID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("load worktree freshness projections failed", "project_id", projectID, "error", err)
			}
		} else {
			for _, worktree := range worktrees {
				lastCheckedAt = laterTime(lastCheckedAt, worktree.UpdatedAt)
				if worktree.GitStatusUpdated != nil {
					lastCheckedAt = laterTime(lastCheckedAt, *worktree.GitStatusUpdated)
				}
			}
		}
	}

	if lastCheckedAt.IsZero() {
		lastCheckedAt = timeNow()
	}
	freshness := protocol.TaskListFreshnessFresh
	if timeNow().Sub(lastCheckedAt) > taskListSnapshotStaleAfter {
		freshness = protocol.TaskListFreshnessStale
	}
	return lastCheckedAt.UTC(), freshness
}

func laterTime(current, candidate time.Time) time.Time {
	if candidate.IsZero() {
		return current
	}
	candidate = candidate.UTC()
	if current.IsZero() || candidate.After(current) {
		return candidate
	}
	return current
}

func (d *Daemon) refreshWorktreeRuntimeState(ctx context.Context, projectID string) (int, error) {
	if d == nil || d.worktreeRuntimeStateStore(projectID) == nil {
		return 0, nil
	}
	projectID = d.canonicalProjectID(projectID)
	baseBranch := d.baseBranchForProject(projectID)
	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return 0, nil
	}

	worktrees, err := manager.List(ctx)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("refresh worktree projection cache failed", "project_id", projectID, "error", err)
		}
		return 0, err
	}

	rows := make([]daemonstate.WorktreeState, 0, len(worktrees))
	statusByIssue := make(map[string]*git.GitStatus, len(worktrees))
	worktreePathByIssue := make(map[string]string, len(worktrees))
	throttle := d.ensureWorktreeGitProbeThrottle()
	trigger := runtimeReconcileRequestFromContext(ctx)
	forceProbe := trigger.Priority >= reconcilePriorityManual
	processedProbes := 0
	skippedProbes := 0
	deferredProbes := 0
	failedProbes := 0
	processedIssueIDs := make([]string, 0, 10)
	failedIssueIDs := make([]string, 0, 10)
	skippedIssueIDs := make([]string, 0, 10)
	deferredIssueIDs := make([]string, 0, 10)
	now := time.Now().UTC()
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		if issueID == "" {
			continue
		}
		worktreePath := strings.TrimSpace(wt.Path)
		if d.git != nil && worktreePath != "" {
			probeKey := gitStatusRefreshQueueKey(projectID, worktreePath)
			decision := throttle.Admit(probeKey, forceProbe)
			switch decision.Action {
			case reconcileThrottleSkip:
				skippedProbes++
				if len(skippedIssueIDs) < cap(skippedIssueIDs) {
					skippedIssueIDs = append(skippedIssueIDs, issueID)
				}
			case reconcileThrottleDefer:
				deferredProbes++
				if len(deferredIssueIDs) < cap(deferredIssueIDs) {
					deferredIssueIDs = append(deferredIssueIDs, issueID)
				}
			default:
				processedProbes++
				if len(processedIssueIDs) < cap(processedIssueIDs) {
					processedIssueIDs = append(processedIssueIDs, issueID)
				}
				status, err := d.git.RuntimeStatus(ctx, worktreePath, baseBranch)
				outcome := throttle.Record(probeKey, gitStatusSignature(status), err)
				if err != nil {
					failedProbes++
					if len(failedIssueIDs) < cap(failedIssueIDs) {
						failedIssueIDs = append(failedIssueIDs, issueID)
					}
					if d.cfg.Logger != nil {
						d.cfg.Logger.Debug("refresh worktree runtime git status failed",
							"project_id", projectID,
							"issue_id", issueID,
							"worktree", worktreePath,
							"outcome", string(outcome),
							"error", err,
						)
					}
				} else {
					statusByIssue[issueID] = status
					worktreePathByIssue[issueID] = worktreePath
				}
			}
		}
		rows = append(rows, daemonstate.WorktreeState{
			ProjectID: projectID,
			IssueID:   issueID,
			Path:      worktreePath,
			Branch:    strings.TrimSpace(wt.Branch),
			UpdatedAt: now,
		})
	}
	if err := d.runtimeProjectionStateWriter().ReplaceWorktreeProjectionSnapshot(ctx, projectID, rows); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("replace worktree projections failed", "project_id", projectID, "error", err)
		}
		return len(rows), err
	}

	for issueID, status := range statusByIssue {
		worktreePath := worktreePathByIssue[issueID]
		rev := d.runtimeProjectionStateWriter().PersistGitStatusProjectionAndPublish(ctx, projectID, issueID, worktreePath, status, true, false)
		if rev == 0 && d.worktreeRuntimeStateStore(projectID) != nil {
			rawStatus, err := json.Marshal(status)
			if err != nil {
				if d.cfg.Logger != nil {
					d.cfg.Logger.Debug("marshal worktree runtime git status failed", "project_id", projectID, "issue_id", issueID, "error", err)
				}
				continue
			}
			if err := d.worktreeRuntimeStateStore(projectID).UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, now); err != nil {
				if d.cfg.Logger != nil {
					d.cfg.Logger.Debug("persist refreshed worktree runtime git status failed", "project_id", projectID, "issue_id", issueID, "error", err)
				}
			}
			continue
		}
		if d.cfg.Logger != nil && rev > 0 {
			d.cfg.Logger.Debug("published refreshed worktree runtime git status", "project_id", projectID, "issue_id", issueID, "revision", rev)
		}
		if rev == 0 && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("persist refreshed worktree runtime git status without publish", "project_id", projectID, "issue_id", issueID)
		}
	}
	if d.cfg.Logger != nil && d.git != nil {
		counters := throttle.snapshotCounters()
		logFn := d.cfg.Logger.Debug
		if failedProbes > 0 || skippedProbes > 0 || deferredProbes > 0 || trigger.Priority >= reconcilePriorityManual {
			logFn = d.cfg.Logger.Info
		}
		logFn("refresh worktree runtime state completed",
			"project_id", projectID,
			"reason", strings.TrimSpace(trigger.Reason),
			"processed_tasks", processedProbes,
			"skipped_tasks", skippedProbes,
			"deferred_tasks", deferredProbes,
			"failed_tasks", failedProbes,
			"sample_processed_issue_ids", strings.Join(processedIssueIDs, ","),
			"sample_skipped_issue_ids", strings.Join(skippedIssueIDs, ","),
			"sample_deferred_issue_ids", strings.Join(deferredIssueIDs, ","),
			"sample_failed_issue_ids", strings.Join(failedIssueIDs, ","),
			"throttle_processed", counters.Processed,
			"throttle_skipped", counters.Skipped,
			"throttle_deferred", counters.Deferred,
		)
	}
	return len(rows), nil
}

func (d *Daemon) refreshWorktreeRuntimeStateForIssues(ctx context.Context, projectID string, issueIDs []string) (int, error) {
	if d == nil || d.worktreeRuntimeStateStore(projectID) == nil {
		return 0, nil
	}
	projectID = d.canonicalProjectID(projectID)
	baseBranch := d.baseBranchForProject(projectID)
	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return 0, nil
	}

	issueIDs = normalizeRuntimeReconcileIssueIDs(issueIDs)
	if len(issueIDs) == 0 {
		return 0, nil
	}

	worktrees, err := manager.List(ctx)
	if err != nil {
		return 0, err
	}
	worktreeByIssue := make(map[string]git.Worktree, len(worktrees))
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		if issueID == "" {
			continue
		}
		worktreeByIssue[issueID] = wt
	}

	refreshed := 0
	var errs []error
	now := time.Now().UTC()
	for _, issueID := range issueIDs {
		wt, ok := worktreeByIssue[issueID]
		if !ok || strings.TrimSpace(wt.Path) == "" {
			d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID)
			continue
		}

		worktreePath := strings.TrimSpace(wt.Path)
		branch := strings.TrimSpace(wt.Branch)
		d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, issueID, worktreePath, branch)
		refreshed++

		if d.git == nil {
			continue
		}
		status, statusErr := d.git.RuntimeStatus(ctx, worktreePath, baseBranch)
		if statusErr != nil {
			errs = append(errs, fmt.Errorf("%s: refresh git status: %w", issueID, statusErr))
			continue
		}
		rev := d.runtimeProjectionStateWriter().PersistGitStatusProjectionAndPublish(ctx, projectID, issueID, worktreePath, status, true, true)
		if rev == 0 && d.worktreeRuntimeStateStore(projectID) != nil {
			rawStatus, marshalErr := json.Marshal(status)
			if marshalErr != nil {
				errs = append(errs, fmt.Errorf("%s: marshal git status: %w", issueID, marshalErr))
				continue
			}
			if upsertErr := d.worktreeRuntimeStateStore(projectID).UpsertWorktreeStateGitStatus(ctx, projectID, issueID, rawStatus, now); upsertErr != nil {
				errs = append(errs, fmt.Errorf("%s: persist git status: %w", issueID, upsertErr))
			}
		}
	}
	return refreshed, errors.Join(errs...)
}

func (d *Daemon) ensureWorktreeGitProbeThrottle() *reconcileThrottle {
	if d == nil {
		return newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "worktree_git_probe",
			Budget:               defaultWorktreeGitProbeBudget,
			Cadence:              defaultRuntimeReconcileInterval,
			UnchangedBackoffBase: defaultWorktreeGitProbeUnchangedBackoff,
			UnchangedBackoffMax:  maxWorktreeGitProbeUnchangedBackoff,
			FailureBackoffBase:   defaultWorktreeGitProbeFailureBackoff,
			FailureBackoffMax:    maxWorktreeGitProbeFailureBackoff,
		})
	}

	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	if d.worktreeGitProbeThrottle == nil {
		d.worktreeGitProbeThrottle = newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "worktree_git_probe",
			Budget:               defaultWorktreeGitProbeBudget,
			Cadence:              d.runtimeReconcileInterval(),
			UnchangedBackoffBase: defaultWorktreeGitProbeUnchangedBackoff,
			UnchangedBackoffMax:  maxWorktreeGitProbeUnchangedBackoff,
			FailureBackoffBase:   defaultWorktreeGitProbeFailureBackoff,
			FailureBackoffMax:    maxWorktreeGitProbeFailureBackoff,
			Logger:               d.cfg.Logger,
		})
	}
	return d.worktreeGitProbeThrottle
}

func (d *Daemon) handleTaskCreate(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
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
		ParentID        *string         `json:"parent_id,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task create requested",
			"project_id", projectID,
			"title", cmd.Title,
			"type", cmd.Type,
			"priority", cmd.Priority,
			"parent_id", cmd.ParentID,
		)
	}
	task, err := issueClient.CreateWithRuntime(ctx, projectID, issues.CreateTaskParams{
		Title:           cmd.Title,
		Description:     cmd.Description,
		Type:            cmd.Type,
		Priority:        cmd.Priority,
		Status:          cmd.Status,
		Assignee:        cmd.Assignee,
		Labels:          cmd.Labels,
		Implementations: cmd.Implementations,
		Design:          cmd.Design,
		Notes:           cmd.Notes,
		Acceptance:      cmd.Acceptance,
		Estimate:        cmd.Estimate,
		ParentID:        cmd.ParentID,
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	taskID := task.ID.String()
	body, _ := json.Marshal(struct {
		TaskID string `json:"task_id"`
	}{TaskID: taskID})
	resp.Body = body
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskCreated, resp.Revision, taskEventBodyFromTask(projectID, task))
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task create completed", "project_id", projectID, "task_id", taskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskUpdateStatus(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID string        `json:"task_id"`
		Status domain.Status `json:"status"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task status update requested", "project_id", projectID, "task_id", cmd.TaskID, "status", cmd.Status)
	}
	task, err := issueClient.UpdateWithRuntime(ctx, projectID, cmd.TaskID, cmd.Status)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskUpdated, resp.Revision, taskEventBodyFromTask(projectID, task))
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task status update completed", "project_id", projectID, "task_id", cmd.TaskID, "status", cmd.Status, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskUpdateDetails(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID          string          `json:"task_id"`
		Title           string          `json:"title"`
		Description     string          `json:"description"`
		Notes           *string         `json:"notes,omitempty"`
		Type            domain.TaskType `json:"type"`
		Priority        domain.Priority `json:"priority"`
		Implementations []string        `json:"implementations,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task details update requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	task, err := issueClient.UpdateDetailsWithRuntime(ctx, projectID, cmd.TaskID, issues.UpdateTaskParams{
		Title:           cmd.Title,
		Description:     cmd.Description,
		Notes:           cmd.Notes,
		Type:            cmd.Type,
		Priority:        cmd.Priority,
		Implementations: cmd.Implementations,
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskUpdated, resp.Revision, taskEventBodyFromTask(projectID, task))
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task details update completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskAppendNotes(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID string `json:"task_id"`
		Line   string `json:"line"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task append notes requested", "project_id", projectID, "task_id", cmd.TaskID, "line_bytes", len(cmd.Line))
	}
	task, err := issueClient.AppendNotesWithRuntime(ctx, projectID, cmd.TaskID, cmd.Line)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskUpdated, resp.Revision, taskEventBodyFromTask(projectID, task))
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task append notes completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskDelete(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task delete requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	if err := issueClient.Delete(ctx, cmd.TaskID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskDeleted, resp.Revision, protocol.TaskEventBody{
		ProjectID: naming.ProjectID(projectID),
		TaskID:    naming.IssueID(cmd.TaskID),
		UpdatedAt: timeNow().UTC(),
	})
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task delete completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskArchive(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task archive requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	if err := issueClient.Archive(ctx, cmd.TaskID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskArchived, resp.Revision, protocol.TaskEventBody{
		ProjectID: naming.ProjectID(projectID),
		TaskID:    naming.IssueID(cmd.TaskID),
		UpdatedAt: timeNow().UTC(),
	})
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task archive completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) handleTaskDependencyAdd(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID         string `json:"task_id"`
		DependsOnID    string `json:"depends_on_id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency add requested",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
		)
	}
	task, err := issueClient.AddDependencyWithRuntime(ctx, projectID, cmd.TaskID, cmd.DependsOnID, cmd.DependencyType)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskUpdated, resp.Revision, taskEventBodyFromTask(projectID, task))
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency add completed",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
			"revision", resp.Revision,
		)
	}
	return resp, nil
}

func (d *Daemon) handleTaskDependencyRemove(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID         string `json:"task_id"`
		DependsOnID    string `json:"depends_on_id"`
		DependencyType string `json:"dependency_type"`
		Confirm        bool   `json:"confirm"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency remove requested",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
			"confirm", cmd.Confirm,
		)
	}
	callCtx := ctx
	if cmd.Confirm {
		callCtx = issues.WithDependencyRemovalConfirmation(callCtx)
	}
	task, err := issueClient.RemoveDependencyWithRuntime(callCtx, projectID, cmd.TaskID, cmd.DependsOnID, cmd.DependencyType)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskUpdated, resp.Revision, taskEventBodyFromTask(projectID, task))
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency remove completed",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
			"revision", resp.Revision,
		)
	}
	return resp, nil
}

func (d *Daemon) handleTaskSnapshotExport(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task snapshot export requested", "project_id", projectID)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	sessions, err := d.listProjectionSessionsOnly(ctx, projectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}

	body := buildTaskSnapshotExportBody(projectID, d.currentRevision(projectID), tasks, sessions, d.sessionNamingScope(projectID))
	payload, err := json.Marshal(body)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal snapshot export body: %v", err)), nil
	}

	resp := d.successResponse(req)
	resp.Revision = body.SnapshotRevision
	resp.Body = payload
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task snapshot export completed",
			"project_id", projectID,
			"task_count", body.TaskCount,
			"session_count", body.SessionCount,
			"snapshot_revision", body.SnapshotRevision,
		)
	}
	return resp, nil
}

func buildTaskSnapshotExportBody(projectID string, revision uint64, tasks []domain.Task, tmuxSessions []string, projectPath string) taskSnapshotExportBody {
	taskCopy := make([]domain.Task, len(tasks))
	copy(taskCopy, tasks)
	sort.SliceStable(taskCopy, func(i, j int) bool {
		return taskCopy[i].ID < taskCopy[j].ID
	})

	sessionCopy := make([]string, len(tmuxSessions))
	copy(sessionCopy, tmuxSessions)
	sort.Strings(sessionCopy)

	sessionSet := make(map[string]struct{}, len(sessionCopy))
	for _, session := range sessionCopy {
		sessionSet[sessionKey(session)] = struct{}{}
		if issueID, ok := naming.ParseIssueIDFromSessionName(session, projectPath); ok {
			sessionSet[sessionKey(issueID)] = struct{}{}
		}
	}

	out := taskSnapshotExportBody{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.SnapshotProtocolVersion,
		SnapshotRevision: revision,
		CapturedAtMs:     nowUnixMs(),
		ProjectID:        projectID,
		TaskCount:        len(taskCopy),
		SessionCount:     len(sessionCopy),
		Tasks:            make([]taskSnapshotExportTask, 0, len(taskCopy)),
		Sessions:         make([]taskSnapshotExportSession, 0, len(sessionCopy)),
	}

	for _, task := range taskCopy {
		_, hasSession := sessionSet[sessionKey(task.ID.String())]
		out.Tasks = append(out.Tasks, taskSnapshotExportTask{
			ID:       task.ID.String(),
			Title:    task.Title,
			Status:   task.Status,
			Priority: task.Priority,
			Type:     task.Type,
			ParentID: func() *string {
				if task.ParentID == nil {
					return nil
				}
				parentID := task.ParentID.String()
				return &parentID
			}(),
			DependencyCount: len(task.Dependencies),
			SessionAttached: hasSession,
			Critical:        task.Status == domain.StatusBlocked,
		})
	}

	for _, session := range sessionCopy {
		out.Sessions = append(out.Sessions, taskSnapshotExportSession{Name: session})
	}

	return out
}

var nowUnixMs = func() int64 {
	return timeNow().UnixMilli()
}

var timeNow = func() time.Time {
	return time.Now().UTC()
}
