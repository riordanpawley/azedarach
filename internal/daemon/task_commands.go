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
	"github.com/riordanpawley/azedarach/internal/latencytrace"
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

const taskListSnapshotCacheTTL = time.Second

type taskListSnapshotCacheEntry struct {
	Revision      uint64
	LastCheckedAt time.Time
	Freshness     protocol.TaskListFreshness
	Tasks         []domain.Task
	CachedAt      time.Time
}

func (d *Daemon) sourceForTaskInvariant(invariant daemonInvariantID) daemonInvariantSource {
	return sourceForInvariant(invariant)
}

func (d *Daemon) handleTaskList(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	refreshStartedAt := time.Now()
	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task list session runtime refresh failed", "project_id", projectID, "error", err)
	}
	d.triggerWorktreeStateRefresh(projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.worktree_refresh_trigger", refreshStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID)
	cacheStartedAt := time.Now()
	if cached, ok := d.readFreshTaskListSnapshotCache(projectID); ok {
		latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", true)
		payload := buildTaskListSnapshotPayload(projectID, cached.Revision, cached.LastCheckedAt, cached.Freshness, cached.Tasks)
		marshalStartedAt := time.Now()
		body, err := json.Marshal(payload)
		latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(cached.Tasks), "cache_hit", true)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		resp.Body = body
		resp.Revision = payload.SnapshotRevision
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("daemon task list completed", "project_id", projectID, "task_count", len(cached.Tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds(), "cache_hit", true)
		}
		return resp, nil
	}
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", false)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list requested", "project_id", projectID)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	queryStartedAt := time.Now()
	tasks, err := issueClient.ListWithRuntime(ctx, projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.issue_store_list_with_runtime", queryStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	tasks = d.enrichTasksWithSessionState(ctx, projectID, tasks)
	freshnessStartedAt := time.Now()
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.snapshot_freshness", freshnessStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "freshness", freshness)
	revision := d.currentRevision(projectID)
	payload := buildTaskListSnapshotPayload(projectID, revision, lastCheckedAt, freshness, tasks)
	marshalStartedAt := time.Now()
	body, err := json.Marshal(payload)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(tasks), "cache_hit", false)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = payload.SnapshotRevision
	d.storeTaskListSnapshotCache(projectID, revision, lastCheckedAt, freshness, tasks)
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
	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task get session runtime refresh failed", "project_id", projectID, "task_id", taskID, "error", err)
	}
	cacheStartedAt := time.Now()
	if cached, ok := d.readFreshTaskListSnapshotCache(projectID); ok {
		if _, found := findCachedTaskByID(cached.Tasks, taskID); found {
			latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "cache_hit", true)
			payload := buildTaskListSnapshotPayload(projectID, cached.Revision, cached.LastCheckedAt, cached.Freshness, cached.Tasks)
			marshalStartedAt := time.Now()
			body, err := json.Marshal(payload)
			latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "context_task_count", len(cached.Tasks), "cache_hit", true)
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
			resp := d.successResponse(req)
			resp.Body = body
			resp.Revision = payload.SnapshotRevision
			if d.cfg.Logger != nil {
				d.cfg.Logger.Info("daemon task get completed", "project_id", projectID, "task_id", taskID, "context_task_count", len(cached.Tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds(), "cache_hit", true)
			}
			return resp, nil
		}
	}
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "cache_hit", false)
	refreshIssueStartedAt := time.Now()
	d.refreshIssueWorktreeState(ctx, projectID, taskID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.issue_worktree_refresh", refreshIssueStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	queryStartedAt := time.Now()
	tasks, err := issueClient.GetWithDependencyContextRuntime(ctx, projectID, taskID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.issue_store_get_dependency_context_runtime", queryStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID)
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
	freshnessStartedAt := time.Now()
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.snapshot_freshness", freshnessStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "freshness", freshness)
	payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), lastCheckedAt, freshness, tasks)
	marshalStartedAt := time.Now()
	body, err := json.Marshal(payload)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "context_task_count", len(tasks), "cache_hit", false)
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

func (d *Daemon) handleTaskGetMany(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	var cmd struct {
		TaskIDs []string `json:"task_ids"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	taskIDs := uniqueTrimmedTaskIDs(cmd.TaskIDs)
	if len(taskIDs) == 0 {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "task_ids is required"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task get-many requested", "project_id", projectID, "task_count", len(taskIDs))
	}
	for _, taskID := range taskIDs {
		d.refreshIssueWorktreeState(ctx, projectID, taskID)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	tasks, err := issueClient.GetManyWithDependencyContextRuntime(ctx, projectID, taskIDs)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("daemon task get-many failed", "project_id", projectID, "task_count", len(taskIDs), "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
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
		d.cfg.Logger.Info("daemon task get-many completed", "project_id", projectID, "requested_task_count", len(taskIDs), "context_task_count", len(tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds())
	}
	return resp, nil
}

func uniqueTrimmedTaskIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (d *Daemon) readFreshTaskListSnapshotCache(projectID string) (taskListSnapshotCacheEntry, bool) {
	projectID = d.canonicalProjectID(projectID)
	currentRevision := d.currentRevision(projectID)

	d.taskListSnapshotCacheMu.Lock()
	defer d.taskListSnapshotCacheMu.Unlock()
	if d.taskListSnapshotCache == nil {
		return taskListSnapshotCacheEntry{}, false
	}
	cached, ok := d.taskListSnapshotCache[projectID]
	if !ok {
		return taskListSnapshotCacheEntry{}, false
	}
	if cached.Revision != currentRevision {
		return taskListSnapshotCacheEntry{}, false
	}
	if cached.Freshness != protocol.TaskListFreshnessFresh {
		return taskListSnapshotCacheEntry{}, false
	}
	if timeNow().Sub(cached.CachedAt) > taskListSnapshotCacheTTL {
		return taskListSnapshotCacheEntry{}, false
	}
	cached.Tasks = cloneTasks(cached.Tasks)
	return cached, true
}

func (d *Daemon) storeTaskListSnapshotCache(projectID string, revision uint64, lastCheckedAt time.Time, freshness protocol.TaskListFreshness, tasks []domain.Task) {
	if freshness != protocol.TaskListFreshnessFresh {
		return
	}
	projectID = d.canonicalProjectID(projectID)
	d.taskListSnapshotCacheMu.Lock()
	defer d.taskListSnapshotCacheMu.Unlock()
	if d.taskListSnapshotCache == nil {
		d.taskListSnapshotCache = map[string]taskListSnapshotCacheEntry{}
	}
	d.taskListSnapshotCache[projectID] = taskListSnapshotCacheEntry{
		Revision:      revision,
		LastCheckedAt: lastCheckedAt.UTC(),
		Freshness:     freshness,
		Tasks:         cloneTasks(tasks),
		CachedAt:      timeNow(),
	}
}

func (d *Daemon) invalidateTaskListSnapshotCache(projectID string) {
	projectID = d.canonicalProjectID(projectID)
	d.taskListSnapshotCacheMu.Lock()
	defer d.taskListSnapshotCacheMu.Unlock()
	if d.taskListSnapshotCache == nil {
		return
	}
	delete(d.taskListSnapshotCache, projectID)
}

func cloneTasks(tasks []domain.Task) []domain.Task {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]domain.Task, len(tasks))
	copy(out, tasks)
	return out
}

func findCachedTaskByID(tasks []domain.Task, taskID string) (domain.Task, bool) {
	for _, task := range tasks {
		if task.ID.String() == taskID {
			return task, true
		}
	}
	return domain.Task{}, false
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
	worktreeByIssue := make(map[string]git.Worktree, len(worktrees))
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		if issueID == "" {
			continue
		}
		worktreeByIssue[issueID] = wt
	}
	issueClient := d.issueClientForProject(projectID)
	taskByIssue := make(map[string]domain.Task)
	if issueClient != nil {
		if tasks, taskErr := issueClient.ListWithRuntime(ctx, projectID); taskErr == nil {
			taskByIssue = make(map[string]domain.Task, len(tasks))
			for _, task := range tasks {
				taskByIssue[strings.TrimSpace(task.ID.String())] = task
			}
		}
	}
	throttle := d.ensureWorktreeGitProbeThrottle()
	trigger := runtimeReconcileRequestFromContext(ctx)
	forceProbe := trigger.Priority >= reconcilePriorityManual && strings.TrimSpace(trigger.Reason) == "manual"
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
				issueBaseBranch := d.runtimeDiffBaseBranchForIssue(issueID, baseBranch, taskByIssue, worktreeByIssue)
				status, err := d.git.RuntimeStatus(ctx, worktreePath, issueBaseBranch)
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
	issueClient := d.issueClientForProject(projectID)
	taskByIssue := make(map[string]domain.Task)
	if issueClient != nil {
		if tasks, taskErr := issueClient.ListWithRuntime(ctx, projectID); taskErr == nil {
			taskByIssue = make(map[string]domain.Task, len(tasks))
			for _, task := range tasks {
				taskByIssue[strings.TrimSpace(task.ID.String())] = task
			}
		}
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
		issueBaseBranch := d.runtimeDiffBaseBranchForIssue(issueID, baseBranch, taskByIssue, worktreeByIssue)
		status, statusErr := d.git.RuntimeStatus(ctx, worktreePath, issueBaseBranch)
		if statusErr != nil {
			errs = append(errs, fmt.Errorf("%s: refresh git status: %w", issueID, statusErr))
			continue
		}
		rev := d.runtimeProjectionStateWriter().PersistGitStatusProjectionAndPublish(ctx, projectID, issueID, worktreePath, status, true, false)
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

func (d *Daemon) runtimeDiffBaseBranchForIssue(
	issueID string,
	defaultBaseBranch string,
	taskByIssue map[string]domain.Task,
	worktreeByIssue map[string]git.Worktree,
) string {
	baseBranch := strings.TrimSpace(defaultBaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	if len(taskByIssue) == 0 {
		return baseBranch
	}

	worktreeBranchForIssue := func(id string) string {
		id = strings.TrimSpace(id)
		if id == "" {
			return ""
		}
		if wt, ok := worktreeByIssue[id]; ok {
			if candidate := strings.TrimSpace(wt.Branch); candidate != "" {
				return candidate
			}
		}
		return ""
	}
	branchForIssue := func(id string) string {
		id = strings.TrimSpace(id)
		if id == "" {
			return ""
		}
		if candidate := worktreeBranchForIssue(id); candidate != "" {
			return candidate
		}
		return "az/" + id
	}

	task, ok := taskByIssue[strings.TrimSpace(issueID)]
	if !ok || task.ParentID == nil {
		return baseBranch
	}

	nextParentID := strings.TrimSpace(task.ParentID.String())
	visited := map[string]struct{}{}
	for nextParentID != "" {
		if _, seen := visited[nextParentID]; seen {
			break
		}
		visited[nextParentID] = struct{}{}

		if candidate := worktreeBranchForIssue(nextParentID); candidate != "" {
			return candidate
		}

		parentTask, parentOK := taskByIssue[nextParentID]
		if !parentOK {
			return branchForIssue(nextParentID)
		}
		if parentTask.Status != domain.StatusDone {
			return branchForIssue(nextParentID)
		}

		nextParentID = ""
		if parentTask.ParentID != nil {
			nextParentID = strings.TrimSpace(parentTask.ParentID.String())
		}
	}

	return baseBranch
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
	task, err := d.updateTaskStatusWithClosePreflight(ctx, projectID, cmd.TaskID, cmd.Status, req)
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

func (d *Daemon) updateTaskStatusWithClosePreflight(ctx context.Context, projectID, taskID string, status domain.Status, req protocol.RequestEnvelope) (domain.Task, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.Task{}, fmt.Errorf("issue store unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	if status == domain.StatusDone {
		if err := d.validateTaskClosePreflight(ctx, projectID, taskID, req); err != nil {
			return domain.Task{}, err
		}
	}
	return issueClient.UpdateWithRuntime(ctx, projectID, taskID, status)
}

func (d *Daemon) validateTaskClosePreflight(ctx context.Context, projectID, taskID string, req protocol.RequestEnvelope) error {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	tasks, err := issueClient.ListWithRuntime(ctx, projectID)
	if err != nil {
		return fmt.Errorf("inspect runtime attachments before closing %s: %w", taskID, err)
	}
	tasks = d.enrichTasksWithSessionState(ctx, projectID, tasks)

	var task domain.Task
	found := false
	for _, candidate := range tasks {
		if strings.EqualFold(strings.TrimSpace(candidate.ID.String()), taskID) {
			task = candidate
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("issue not found: %s", taskID)
	}

	reasons := make([]string, 0, 3)
	reasons = append(reasons, daemonCloseGuardRuntimeBlockers(task)...)
	reasons = append(reasons, daemonCloseGuardChildBlockers(task.ID, tasks)...)
	if len(reasons) > 0 {
		repairs, repairErr := d.reopenClosedCloseGuardBlockers(ctx, issueClient, projectID, req, task, tasks, reasons)
		if repairErr != nil {
			return fmt.Errorf("%s. Failed to move closed blockers back for cleanup: %w", daemonCloseGuardFailureMessage(taskID, reasons, repairs), repairErr)
		}
		return fmt.Errorf("%s", daemonCloseGuardFailureMessage(taskID, reasons, repairs))
	}
	return nil
}

type daemonCloseGuardStatusRepair struct {
	IssueID string
	Status  domain.Status
}

func (d *Daemon) reopenClosedCloseGuardBlockers(ctx context.Context, issueClient *issues.Client, projectID string, req protocol.RequestEnvelope, target domain.Task, tasks []domain.Task, reasons []string) ([]daemonCloseGuardStatusRepair, error) {
	repairs := daemonCloseGuardStatusRepairs(target, tasks, reasons)
	for _, repair := range repairs {
		task, err := issueClient.UpdateWithRuntime(ctx, projectID, repair.IssueID, repair.Status)
		if err != nil {
			return repairs, fmt.Errorf("move %s to %s: %w", repair.IssueID, repair.Status, err)
		}
		rev := d.nextRevision(projectID)
		d.publishTaskEvent(req, protocol.EventTaskUpdated, rev, taskEventBodyFromTask(projectID, task))
	}
	return repairs, nil
}

func daemonCloseGuardStatusRepairs(target domain.Task, tasks []domain.Task, reasons []string) []daemonCloseGuardStatusRepair {
	if len(reasons) == 0 {
		return nil
	}
	repairs := make([]daemonCloseGuardStatusRepair, 0, 2)
	seen := make(map[naming.IssueID]struct{})
	add := func(task domain.Task) {
		if task.Status != domain.StatusDone {
			return
		}
		if _, ok := seen[task.ID]; ok {
			return
		}
		seen[task.ID] = struct{}{}
		repairs = append(repairs, daemonCloseGuardStatusRepair{
			IssueID: task.ID.String(),
			Status:  daemonCloseGuardReopenStatus(task),
		})
	}

	add(target)
	childrenByParent := daemonCloseGuardChildrenByParent(tasks)
	descendants := daemonCloseGuardDescendants(target.ID, childrenByParent)
	byID := make(map[naming.IssueID]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}
	for _, childID := range descendants {
		child, ok := byID[childID]
		if !ok || len(daemonCloseGuardChildReasons(child)) == 0 {
			continue
		}
		add(child)
	}
	return repairs
}

func daemonCloseGuardReopenStatus(task domain.Task) domain.Status {
	if daemonCloseGuardTaskHasSession(task) {
		return domain.StatusInProgress
	}
	return domain.StatusInReview
}

func daemonCloseGuardFailureMessage(taskID string, reasons []string, repairs []daemonCloseGuardStatusRepair) string {
	message := fmt.Sprintf("cannot close issue %s: %s", taskID, strings.Join(reasons, "; "))
	hint := daemonCloseGuardRecoveryHint(taskID, reasons)
	if hint == "" {
		return daemonCloseGuardAppendRepairSummary(message, repairs)
	}
	return daemonCloseGuardAppendRepairSummary(message+". Next: "+hint, repairs)
}

func daemonCloseGuardAppendRepairSummary(message string, repairs []daemonCloseGuardStatusRepair) string {
	if len(repairs) == 0 {
		return message
	}
	parts := make([]string, 0, len(repairs))
	for _, repair := range repairs {
		parts = append(parts, fmt.Sprintf("%s -> %s", repair.IssueID, repair.Status))
	}
	return message + ". Moved closed blockers back for cleanup: " + strings.Join(parts, ", ")
}

func daemonCloseGuardRecoveryHint(taskID string, reasons []string) string {
	hasRuntime := false
	hasChildren := false
	for _, reason := range reasons {
		if strings.HasPrefix(reason, "issue still has a ") {
			hasRuntime = true
		}
		if strings.Contains(reason, "child issues") {
			hasChildren = true
		}
	}

	steps := make([]string, 0, 2)
	if hasChildren {
		steps = append(steps, "close or clean up the listed child issues first")
	}
	if hasRuntime {
		steps = append(steps, fmt.Sprintf("run `az issue close --id %s` or stop sessions/remove worktrees manually", taskID))
	}
	if len(steps) == 0 {
		return "fix the listed blockers, refresh, then retry"
	}
	return strings.Join(steps, "; ") + ", then retry"
}

func daemonCloseGuardRuntimeBlockers(task domain.Task) []string {
	reasons := make([]string, 0, 2)
	if daemonCloseGuardTaskHasSession(task) {
		reasons = append(reasons, "issue still has a session")
	}
	if daemonCloseGuardTaskHasWorktree(task) {
		reasons = append(reasons, "issue still has a worktree")
	}
	return reasons
}

func daemonCloseGuardChildBlockers(parentID naming.IssueID, tasks []domain.Task) []string {
	childrenByParent := daemonCloseGuardChildrenByParent(tasks)
	descendants := daemonCloseGuardDescendants(parentID, childrenByParent)
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
		reasons := daemonCloseGuardChildReasons(child)
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

func daemonCloseGuardChildrenByParent(tasks []domain.Task) map[naming.IssueID][]naming.IssueID {
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

func daemonCloseGuardDescendants(rootID naming.IssueID, children map[naming.IssueID][]naming.IssueID) []naming.IssueID {
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

func daemonCloseGuardChildReasons(task domain.Task) []string {
	reasons := make([]string, 0, 3)
	if task.Status != domain.StatusDone {
		reasons = append(reasons, string(task.Status))
	}
	if daemonCloseGuardTaskHasSession(task) {
		reasons = append(reasons, "session")
	}
	if daemonCloseGuardTaskHasWorktree(task) {
		reasons = append(reasons, "worktree")
	}
	return reasons
}

func daemonCloseGuardTaskHasSession(task domain.Task) bool {
	return task.HasTmuxSession || task.Session != nil
}

func daemonCloseGuardTaskHasWorktree(task domain.Task) bool {
	if task.HasWorktree {
		return true
	}
	return task.Session != nil && strings.TrimSpace(task.Session.Worktree) != ""
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
		TaskID            string `json:"task_id"`
		DependsOnID       string `json:"depends_on_id"`
		DependencyType    string `json:"dependency_type"`
		ForceParentChange bool   `json:"force_parent_change"`
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
			"force_parent_change", cmd.ForceParentChange,
		)
	}
	task, err := issueClient.AddDependencyWithRuntimeAndParentChange(ctx, projectID, cmd.TaskID, cmd.DependsOnID, cmd.DependencyType, cmd.ForceParentChange)
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
			"force_parent_change", cmd.ForceParentChange,
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
			Critical:        false,
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
