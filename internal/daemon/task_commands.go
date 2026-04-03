package daemon

import (
	"context"
	"encoding/json"
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

type taskListReader interface {
	List(context.Context) ([]domain.Task, error)
}

type tmuxSessionReader interface {
	ListSessions(context.Context) ([]string, error)
}

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

func (d *Daemon) handleTaskList(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	projectID := d.projectID(req.Meta)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list requested", "project_id", projectID)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.List(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	tasks = d.enrichTasksWithSessionStateReadOnly(ctx, projectID, tasks)
	tasks = d.enrichTasksWithRuntimeProjectionCache(ctx, projectID, tasks)
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), lastCheckedAt, freshness, tasks)
	body, err := json.Marshal(payload)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = payload.SnapshotRevision
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list completed", "project_id", projectID, "task_count", len(tasks), "revision", resp.Revision)
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
		ProjectID:        projectID,
		LastCheckedAt:    lastCheckedAt.UTC(),
		Freshness:        freshness,
		Tasks:            tasks,
	}
}

const taskListSnapshotStaleAfter = 15 * time.Second

func (d *Daemon) taskListSnapshotFreshness(ctx context.Context, projectID string) (time.Time, protocol.TaskListFreshness) {
	lastCheckedAt := time.Time{}
	projectID = protocol.NormalizeProjectID(projectID)

	if d.sessionStore != nil {
		snapshot := d.sessionStore.ReadSnapshot(projectID)
		for _, session := range snapshot.Sessions {
			lastCheckedAt = laterTime(lastCheckedAt, session.UpdatedAt)
		}
	}

	if d.sessionRuntimeStateStore(projectID) != nil {
		sessions, err := d.sessionRuntimeStateStore(projectID).ListSessionStates(ctx, projectID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("load session freshness projections failed", "project_id", projectID, "error", err)
			}
		} else {
			for _, session := range sessions {
				lastCheckedAt = laterTime(lastCheckedAt, session.UpdatedAt)
			}
		}
	}

	if d.worktreeRuntimeStateStore(projectID) != nil {
		worktrees, err := d.worktreeRuntimeStateStore(projectID).ListWorktreeStates(ctx, projectID)
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

func (d *Daemon) enrichTasksWithSessionStateReadOnly(ctx context.Context, projectID string, tasks []domain.Task) []domain.Task {
	if len(tasks) == 0 {
		return tasks
	}

	namingScope := d.sessionNamingScope(projectID)
	snapshotByKey := map[string]daemonstate.Session{}
	if d.sessionStore != nil {
		snapshot := d.sessionStore.ReadSnapshot(projectID)
		snapshotSessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
		for _, session := range snapshot.Sessions {
			snapshotSessions = append(snapshotSessions, session)
		}
		snapshotByKey = sessionProjectionByIssueKey(snapshotSessions, namingScope)
	}

	projectionByKey := map[string]daemonstate.Session{}
	if d.sessionRuntimeStateStore(projectID) != nil {
		cachedSessions, err := d.sessionRuntimeStateStore(projectID).ListSessionStates(ctx, projectID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("failed to load cached session projections while enriching read-only tasks", "project_id", projectID, "error", err)
			}
		} else {
			projectionByKey = sessionProjectionByIssueKey(cachedSessions, namingScope)
		}
	}

	for i := range tasks {
		taskKey := sessionKey(tasks[i].ID)
		session, ok := snapshotByKey[taskKey]
		if !ok {
			session, ok = projectionByKey[taskKey]
		}
		if !ok || session.State == daemonstate.SessionStateStopped {
			continue
		}

		state := domain.SessionBusy
		if session.State == daemonstate.SessionStatePaused {
			state = domain.SessionPaused
		}
		var startedAt *time.Time
		if !session.UpdatedAt.IsZero() {
			started := session.UpdatedAt.UTC()
			startedAt = &started
		}
		tasks[i].Session = &domain.Session{
			IssueID:   tasks[i].ID,
			State:     state,
			StartedAt: startedAt,
		}
	}

	return tasks
}

func (d *Daemon) refreshWorktreeRuntimeState(ctx context.Context, projectID string) (int, error) {
	if d == nil || d.worktree == nil || d.worktreeRuntimeStateStore(projectID) == nil {
		return 0, nil
	}
	projectID = protocol.NormalizeProjectID(projectID)

	worktrees, err := d.worktree.List(ctx)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("refresh worktree projection cache failed", "project_id", projectID, "error", err)
		}
		return 0, err
	}

	rows := make([]daemonstate.WorktreeState, 0, len(worktrees))
	statusByIssue := make(map[string]*git.GitStatus, len(worktrees))
	throttle := d.ensureWorktreeGitProbeThrottle()
	trigger := runtimeReconcileRequestFromContext(ctx)
	forceProbe := trigger.Priority >= reconcilePriorityManual
	processedProbes := 0
	skippedProbes := 0
	deferredProbes := 0
	failedProbes := 0
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
			case reconcileThrottleDefer:
				deferredProbes++
			default:
				processedProbes++
				status, err := d.git.RuntimeStatus(ctx, worktreePath, d.cfg.BaseBranch)
				outcome := throttle.Record(probeKey, gitStatusSignature(status), err)
				if err != nil {
					failedProbes++
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
	}
	if d.cfg.Logger != nil && d.git != nil {
		counters := throttle.snapshotCounters()
		d.cfg.Logger.Debug("refresh worktree runtime state completed",
			"project_id", projectID,
			"reason", strings.TrimSpace(trigger.Reason),
			"processed_tasks", processedProbes,
			"skipped_tasks", skippedProbes,
			"deferred_tasks", deferredProbes,
			"failed_tasks", failedProbes,
			"throttle_processed", counters.Processed,
			"throttle_skipped", counters.Skipped,
			"throttle_deferred", counters.Deferred,
		)
	}
	return len(rows), nil
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

func (d *Daemon) enrichTasksWithRuntimeProjectionCache(ctx context.Context, projectID string, tasks []domain.Task) []domain.Task {
	if len(tasks) == 0 || d.worktreeRuntimeStateStore(projectID) == nil {
		return tasks
	}

	projectID = protocol.NormalizeProjectID(projectID)

	worktreeRows, err := d.worktreeRuntimeStateStore(projectID).ListWorktreeStates(ctx, projectID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("load worktree projections for task enrichment failed", "project_id", projectID, "error", err)
		}
		return tasks
	}
	if len(worktreeRows) == 0 {
		return tasks
	}

	worktreeByIssue := make(map[string]daemonstate.WorktreeState, len(worktreeRows))
	for _, row := range worktreeRows {
		issueID := strings.TrimSpace(row.IssueID)
		if issueID == "" {
			continue
		}
		worktreeByIssue[issueID] = row
	}

	for i := range tasks {
		row, ok := worktreeByIssue[strings.TrimSpace(tasks[i].ID)]
		if !ok {
			continue
		}

		worktreePath := strings.TrimSpace(row.Path)
		tasks[i].HasWorktree = worktreePath != ""
		if tasks[i].Session != nil && tasks[i].Session.Worktree == "" && worktreePath != "" {
			tasks[i].Session.Worktree = worktreePath
		}

		statusRaw := row.GitStatusRaw
		if len(statusRaw) == 0 {
			continue
		}

		var status git.GitStatus
		if err := json.Unmarshal(statusRaw, &status); err != nil {
			continue
		}
		tasks[i].HasUncommittedChanges = status.HasChanges
		tasks[i].GitAdditions = status.GitAdditions
		tasks[i].GitDeletions = status.GitDeletions
		tasks[i].GitAheadCount = status.GitAheadCount
		tasks[i].GitBehindCount = status.GitBehindCount
		if tasks[i].GitAdditions == 0 {
			tasks[i].GitAdditions = len(status.Added) + len(status.Modified) + len(status.Staged)
		}
		if tasks[i].GitDeletions == 0 {
			tasks[i].GitDeletions = len(status.Deleted)
		}
	}

	return tasks
}

func (d *Daemon) hydrateGitStatusProjection(ctx context.Context, projectID, issueID, worktree string) *git.GitStatus {
	if d == nil || d.git == nil || d.worktreeRuntimeStateStore(projectID) == nil {
		return nil
	}
	projectID = protocol.NormalizeProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	worktree = strings.TrimSpace(worktree)
	if issueID == "" || worktree == "" {
		return nil
	}

	timeoutCtx := ctx
	cancel := func() {}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		timeoutCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	}
	defer cancel()

	status, err := d.git.RuntimeStatus(timeoutCtx, worktree, d.cfg.BaseBranch)
	if err != nil || status == nil {
		return nil
	}
	if rev := d.runtimeProjectionStateWriter().PersistGitStatusProjectionAndPublish(timeoutCtx, projectID, issueID, worktree, status, true, true); rev == 0 {
		return status
	}
	return status
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
	taskID, err := issueClient.Create(ctx, issues.CreateTaskParams{
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
	body, _ := json.Marshal(struct {
		TaskID string `json:"task_id"`
	}{TaskID: taskID})
	resp.Body = body
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.created", resp.Revision)
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
	if err := issueClient.Update(ctx, cmd.TaskID, cmd.Status); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
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
	if err := issueClient.UpdateDetails(ctx, cmd.TaskID, issues.UpdateTaskParams{
		Title:           cmd.Title,
		Description:     cmd.Description,
		Type:            cmd.Type,
		Priority:        cmd.Priority,
		Implementations: cmd.Implementations,
	}); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
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
	if err := issueClient.AppendNotes(ctx, cmd.TaskID, cmd.Line); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
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
	d.publishTaskEvent(req, "task.deleted", resp.Revision)
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
	d.publishTaskEvent(req, "task.archived", resp.Revision)
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
	if err := issueClient.AddDependency(ctx, cmd.TaskID, cmd.DependsOnID, cmd.DependencyType); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
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
	if err := issueClient.RemoveDependency(callCtx, cmd.TaskID, cmd.DependsOnID, cmd.DependencyType); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, "task.updated", resp.Revision)
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
	sessions, err := d.tmux.ListSessions(ctx)
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
		_, hasSession := sessionSet[sessionKey(task.ID)]
		out.Tasks = append(out.Tasks, taskSnapshotExportTask{
			ID:              task.ID,
			Title:           task.Title,
			Status:          task.Status,
			Priority:        task.Priority,
			Type:            task.Type,
			ParentID:        task.ParentID,
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
