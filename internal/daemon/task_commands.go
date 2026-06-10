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

const (
	taskListSnapshotCacheTTL        = 5 * time.Second
	taskListSnapshotRuntimeCacheTTL = 750 * time.Millisecond
	taskListSnapshotLoadTimeout     = 10 * time.Second
)

type taskListSnapshotCacheEntry struct {
	Revision      uint64
	LastCheckedAt time.Time
	Freshness     protocol.TaskListFreshness
	Tasks         []domain.Task
	CachedAt      time.Time
	RuntimeAt     time.Time
	SummariesOnly bool
}

type taskListSnapshotLoad struct {
	done   chan struct{}
	result taskListSnapshotLoadResult
	err    error
}

type taskListSnapshotLoadResult struct {
	Revision      uint64
	LastCheckedAt time.Time
	Freshness     protocol.TaskListFreshness
	SummariesOnly bool
	Tasks         []domain.Task
}

type taskClosePreflightOptions struct {
	AllowTargetSession  bool `json:"allow_target_session,omitempty"`
	AllowTargetWorktree bool `json:"allow_target_worktree,omitempty"`
	ForceWorktree       bool `json:"force_worktree,omitempty"`
	IgnoreAhead         bool `json:"ignore_ahead,omitempty"`
}

type taskClosePreflightRequest struct {
	TaskID string `json:"task_id"`
	taskClosePreflightOptions
}

type taskCloseRequest struct {
	TaskID               string `json:"task_id"`
	ForceWorktree        bool   `json:"force_worktree,omitempty"`
	IgnoreAhead          bool   `json:"ignore_ahead,omitempty"`
	IntegrateBeforeClose bool   `json:"integrate_before_close,omitempty"`
}

type taskDeleteRequest struct {
	TaskID         string `json:"task_id"`
	Cleanup        bool   `json:"cleanup,omitempty"`
	StopSession    bool   `json:"stop_session,omitempty"`
	RemoveWorktree bool   `json:"remove_worktree,omitempty"`
	ForceWorktree  bool   `json:"force_worktree,omitempty"`
}

type taskCloseResult struct {
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

type taskDeleteResult struct {
	TaskID          string `json:"task_id"`
	Deleted         bool   `json:"deleted"`
	SessionStopped  bool   `json:"session_stopped,omitempty"`
	WorktreeRemoved bool   `json:"worktree_removed,omitempty"`
	WorktreeForced  bool   `json:"worktree_forced,omitempty"`
	Revision        uint64 `json:"revision,omitempty"`
}

type taskClosePreflightResult struct {
	Task     domain.Task   `json:"task"`
	Worktree string        `json:"worktree,omitempty"`
	Status   git.GitStatus `json:"status,omitempty"`
}

type taskDeletePreflightResult struct {
	Task     domain.Task `json:"task"`
	Blockers []string    `json:"blockers,omitempty"`
}

type taskGraphReadinessResult struct {
	RootIssueID    string                   `json:"root_issue_id"`
	Runnable       []string                 `json:"runnable"`
	Active         []string                 `json:"active,omitempty"`
	ActiveSessions []taskGraphActiveSession `json:"active_sessions,omitempty"`
	Blocked        map[string]string        `json:"blocked"`
}

type taskGraphActiveSession struct {
	IssueID           string `json:"issue_id"`
	Activity          string `json:"activity"`
	ActivitySource    string `json:"activity_source"`
	State             string `json:"state,omitempty"`
	TmuxAttachedCount int    `json:"tmux_attached_count,omitempty"`
	Advice            string `json:"advice,omitempty"`
}

type taskCompleteCheckResult struct {
	RootIssueID string   `json:"root_issue_id"`
	Pass        bool     `json:"pass"`
	Reasons     []string `json:"reasons,omitempty"`
	Advice      []string `json:"advice,omitempty"`
}

type taskIntegrationReadinessResult struct {
	IssueID       string   `json:"issue_id"`
	ParentIssueID string   `json:"parent_issue_id,omitempty"`
	Ready         bool     `json:"ready"`
	Reasons       []string `json:"reasons,omitempty"`
}

type taskMergeBaseTargetResult struct {
	IssueID        string   `json:"issue_id"`
	TargetID       string   `json:"target_id"`
	Branch         string   `json:"branch"`
	WorktreePath   string   `json:"worktree_path,omitempty"`
	BranchAttached bool     `json:"branch_attached,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	AncestorChain  []string `json:"ancestor_chain,omitempty"`
}

type taskFollowOnMergeCandidatesResult struct {
	TaskID            string                           `json:"task_id"`
	MergeTargetToBase bool                             `json:"merge_target_to_base,omitempty"`
	Candidates        []taskFollowOnMergeCandidateItem `json:"candidates"`
}

type taskFollowOnMergeCandidateItem struct {
	IssueID     string        `json:"issue_id"`
	Title       string        `json:"title"`
	Status      domain.Status `json:"status"`
	Relation    string        `json:"relation"`
	Order       int           `json:"order"`
	HasWorktree bool          `json:"has_worktree"`
}

func (d *Daemon) sourceForTaskInvariant(invariant daemonInvariantID) daemonInvariantSource {
	return sourceForInvariant(invariant)
}

func (d *Daemon) handleTaskList(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	cacheStartedAt := time.Now()
	if cached, ok := d.readFreshTaskListSnapshotCache(projectID); ok {
		latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", true)
		payload := buildTaskListSnapshotPayload(projectID, cached.Revision, cached.LastCheckedAt, cached.Freshness, cached.Tasks, cached.SummariesOnly)
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
	result, shared, err := d.loadTaskListSnapshot(ctx, req, projectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	payload := buildTaskListSnapshotPayload(projectID, result.Revision, result.LastCheckedAt, result.Freshness, result.Tasks, result.SummariesOnly)
	marshalStartedAt := time.Now()
	body, err := json.Marshal(payload)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(result.Tasks), "cache_hit", false, "shared_load", shared)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = payload.SnapshotRevision
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list completed", "project_id", projectID, "task_count", len(result.Tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds(), "shared_load", shared)
	}
	return resp, nil
}

func (d *Daemon) loadTaskListSnapshot(ctx context.Context, req protocol.RequestEnvelope, projectID string) (taskListSnapshotLoadResult, bool, error) {
	projectID = d.canonicalProjectID(projectID)

	d.taskListSnapshotLoadMu.Lock()
	if d.taskListSnapshotLoads == nil {
		d.taskListSnapshotLoads = map[string]*taskListSnapshotLoad{}
	}
	if load := d.taskListSnapshotLoads[projectID]; load != nil {
		d.taskListSnapshotLoadMu.Unlock()
		select {
		case <-ctx.Done():
			return taskListSnapshotLoadResult{}, true, ctx.Err()
		case <-load.done:
			return cloneTaskListSnapshotLoadResult(load.result), true, load.err
		}
	}
	load := &taskListSnapshotLoad{done: make(chan struct{})}
	d.taskListSnapshotLoads[projectID] = load
	d.taskListSnapshotLoadMu.Unlock()

	buildCtx, cancel := context.WithTimeout(context.Background(), taskListSnapshotLoadTimeout)
	defer cancel()
	result, err := d.buildTaskListSnapshot(buildCtx, req, projectID)
	load.result = cloneTaskListSnapshotLoadResult(result)
	load.err = err

	d.taskListSnapshotLoadMu.Lock()
	delete(d.taskListSnapshotLoads, projectID)
	close(load.done)
	d.taskListSnapshotLoadMu.Unlock()

	return result, false, err
}

func cloneTaskListSnapshotLoadResult(result taskListSnapshotLoadResult) taskListSnapshotLoadResult {
	result.Tasks = cloneTasks(result.Tasks)
	return result
}

func (d *Daemon) buildTaskListSnapshot(ctx context.Context, req protocol.RequestEnvelope, projectID string) (taskListSnapshotLoadResult, error) {
	refreshStartedAt := time.Now()
	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task list session runtime refresh failed", "project_id", projectID, "error", err)
	}
	d.triggerWorktreeStateRefresh(projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.worktree_refresh_trigger", refreshStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list requested", "project_id", projectID)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return taskListSnapshotLoadResult{}, errors.New("issue store unavailable")
	}
	queryStartedAt := time.Now()
	tasks, err := issueClient.ListSummariesWithRuntime(ctx, projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.issue_store_list_summaries_with_runtime", queryStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID)
	if err != nil {
		return taskListSnapshotLoadResult{}, err
	}
	tasks = d.enrichTasksWithSessionState(ctx, projectID, tasks)
	freshnessStartedAt := time.Now()
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.snapshot_freshness", freshnessStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "freshness", freshness)
	revision := d.currentRevision(projectID)
	d.storeTaskListSnapshotCache(projectID, revision, lastCheckedAt, freshness, tasks, true)
	return taskListSnapshotLoadResult{
		Revision:      revision,
		LastCheckedAt: lastCheckedAt,
		Freshness:     freshness,
		SummariesOnly: true,
		Tasks:         tasks,
	}, nil
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
	cacheStartedAt := time.Now()
	if cached, ok := d.readFreshTaskListSnapshotCache(projectID); ok && !cached.SummariesOnly {
		if _, found := findCachedTaskByID(cached.Tasks, taskID); found {
			latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "cache_hit", true)
			payload := buildTaskListSnapshotPayload(projectID, cached.Revision, cached.LastCheckedAt, cached.Freshness, cached.Tasks, cached.SummariesOnly)
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
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task get requested", "project_id", projectID, "task_id", taskID)
	}
	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task get session runtime refresh failed", "project_id", projectID, "task_id", taskID, "error", err)
	}
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
	payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), lastCheckedAt, freshness, tasks, false)
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
	payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), lastCheckedAt, freshness, tasks, false)
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
	runtimeAt := cached.RuntimeAt
	if runtimeAt.IsZero() {
		runtimeAt = cached.CachedAt
	}
	if timeNow().Sub(runtimeAt) > taskListSnapshotRuntimeCacheTTL {
		return taskListSnapshotCacheEntry{}, false
	}
	cached.Tasks = cloneTasks(cached.Tasks)
	return cached, true
}

func (d *Daemon) storeTaskListSnapshotCache(projectID string, revision uint64, lastCheckedAt time.Time, freshness protocol.TaskListFreshness, tasks []domain.Task, summariesOnly bool) {
	if freshness != protocol.TaskListFreshnessFresh {
		return
	}
	projectID = d.canonicalProjectID(projectID)
	d.taskListSnapshotCacheMu.Lock()
	defer d.taskListSnapshotCacheMu.Unlock()
	if d.taskListSnapshotCache == nil {
		d.taskListSnapshotCache = map[string]taskListSnapshotCacheEntry{}
	}
	now := timeNow()
	d.taskListSnapshotCache[projectID] = taskListSnapshotCacheEntry{
		Revision:      revision,
		LastCheckedAt: lastCheckedAt.UTC(),
		Freshness:     freshness,
		Tasks:         cloneTasks(tasks),
		CachedAt:      now,
		RuntimeAt:     now,
		SummariesOnly: summariesOnly,
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

func buildTaskListSnapshotPayload(projectID string, revision uint64, lastCheckedAt time.Time, freshness protocol.TaskListFreshness, tasks []domain.Task, summariesOnly bool) protocol.TaskListSnapshotPayload {
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
		SummariesOnly:    summariesOnly,
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
			issueBaseBranch := d.runtimeDiffBaseBranchForIssue(issueID, baseBranch, taskByIssue, worktreeByIssue)
			probeKey := worktreeGitProbeThrottleKey(projectID, worktreePath, issueBaseBranch)
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

	if target, ok := domain.ClosestAncestorWithWorktree(issueID, taskByIssue, issueWorktreeRefsFromGitWorktrees(worktreeByIssue)); ok {
		return target.Branch
	}

	return baseBranch
}

func (d *Daemon) runtimeDiffBaseBranchForWorktree(ctx context.Context, projectID, worktree string) string {
	if d == nil || strings.TrimSpace(worktree) == "" {
		return "main"
	}
	baseBranch := d.baseBranchForProject(projectID)
	if strings.TrimSpace(baseBranch) == "" {
		baseBranch = "main"
	}
	projectID = d.canonicalProjectID(projectID)
	store := d.worktreeRuntimeStateStore(projectID)
	if store == nil {
		return baseBranch
	}
	projection, found, err := store.GetWorktreeStateByPath(ctx, projectID, worktree)
	if err != nil || !found || strings.TrimSpace(projection.IssueID) == "" {
		return baseBranch
	}

	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return baseBranch
	}
	worktrees, err := manager.List(ctx)
	if err != nil {
		return baseBranch
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
	if issueClient == nil {
		return baseBranch
	}
	tasks, err := issueClient.ListWithRuntime(ctx, projectID)
	if err != nil {
		return baseBranch
	}
	taskByIssue := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		issueID := strings.TrimSpace(task.ID.String())
		if issueID == "" {
			continue
		}
		taskByIssue[issueID] = task
	}

	return d.runtimeDiffBaseBranchForIssue(projection.IssueID, baseBranch, taskByIssue, worktreeByIssue)
}

func issueWorktreeRefsFromGitWorktrees(worktreesByIssue map[string]git.Worktree) map[string]domain.IssueWorktreeRef {
	if len(worktreesByIssue) == 0 {
		return nil
	}
	refs := make(map[string]domain.IssueWorktreeRef, len(worktreesByIssue))
	for issueID, wt := range worktreesByIssue {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		refs[issueID] = domain.IssueWorktreeRef{
			Branch: strings.TrimSpace(wt.Branch),
			Path:   strings.TrimSpace(wt.Path),
		}
	}
	return refs
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

func worktreeGitProbeThrottleKey(projectID, worktree, baseBranch string) string {
	key := gitStatusRefreshQueueKey(projectID, worktree)
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	return key + "|base=" + baseBranch
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
	task, err := d.updateTaskStatusExcludingClose(ctx, projectID, cmd.TaskID, cmd.Status)
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

func (d *Daemon) handleTaskClose(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd taskCloseRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.closeTask(ctx, projectID, cmd, req)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = result.Revision
	return resp, nil
}

func (d *Daemon) closeTask(ctx context.Context, projectID string, cmd taskCloseRequest, req protocol.RequestEnvelope) (taskCloseResult, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return taskCloseResult{}, fmt.Errorf("issue store unavailable")
	}
	taskID := strings.TrimSpace(cmd.TaskID)
	if taskID == "" {
		return taskCloseResult{}, fmt.Errorf("task id is required")
	}
	result := taskCloseResult{
		TaskID:         taskID,
		Status:         string(domain.StatusDone),
		WorktreeForced: cmd.ForceWorktree,
	}
	integration, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, cmd.IntegrateBeforeClose)
	if err != nil {
		return result, fmt.Errorf("integrate before closing %s: %w", taskID, err)
	}
	result.IntegrationRequested = integration.Requested
	result.Integrated = integration.Integrated
	result.IntegratedSourceBranch = integration.SourceBranch
	result.IntegratedTargetBranch = integration.TargetBranch

	guard, err := d.validateTaskClosePreflight(ctx, projectID, taskID, taskClosePreflightOptions{
		AllowTargetSession:  true,
		AllowTargetWorktree: true,
		ForceWorktree:       cmd.ForceWorktree,
		IgnoreAhead:         cmd.IgnoreAhead || integration.Integrated,
	}, req)
	if err != nil {
		return result, err
	}
	if daemonCloseGuardTaskHasSession(guard.Task) {
		if err := d.stopTaskSessionForClose(ctx, req, projectID, taskID); err != nil {
			return result, fmt.Errorf("stop session before closing %s: %w", taskID, err)
		}
		result.SessionStopped = true
	}
	if !result.SessionStopped {
		if err := d.cleanupTaskIssueResourcesForClose(ctx, projectID, taskID, guard.Worktree); err != nil {
			return result, fmt.Errorf("cleanup issue resources before closing %s: %w", taskID, err)
		}
	}
	if daemonCloseGuardTaskHasWorktree(guard.Task) {
		if d.worktreeAdapter == nil {
			return result, fmt.Errorf("remove worktree before closing %s: worktree cleanup unavailable", taskID)
		}
		if err := d.worktreeAdapter.Delete(ctx, projectID, taskID, cmd.ForceWorktree); err != nil {
			return result, fmt.Errorf("remove worktree before closing %s: %w", taskID, err)
		}
		result.WorktreeRemoved = true
	}

	task, err := issueClient.UpdateWithRuntime(ctx, projectID, taskID, domain.StatusDone)
	if err != nil {
		return result, err
	}
	rev := d.nextRevision(projectID)
	result.Revision = rev
	d.publishTaskEvent(req, protocol.EventTaskUpdated, rev, taskEventBodyFromTask(projectID, task))
	return result, nil
}

type taskCloseIntegrationResult struct {
	Requested    bool
	Integrated   bool
	SourceBranch string
	TargetBranch string
}

func (d *Daemon) integrateTaskBeforeClose(ctx context.Context, projectID, taskID string, requested bool) (taskCloseIntegrationResult, error) {
	if !requested {
		return taskCloseIntegrationResult{}, nil
	}
	if d.worktreeAdapter == nil {
		return taskCloseIntegrationResult{}, fmt.Errorf("worktree adapter unavailable")
	}
	if d.git == nil {
		return taskCloseIntegrationResult{}, fmt.Errorf("git adapter unavailable")
	}
	worktrees, err := d.worktreeAdapter.List(ctx, projectID)
	if err != nil {
		return taskCloseIntegrationResult{}, fmt.Errorf("list worktrees before close integration: %w", err)
	}
	source, ok := daemonWorktreeForIssue(worktrees, taskID)
	if !ok || strings.TrimSpace(source.Path) == "" {
		return taskCloseIntegrationResult{}, nil
	}
	if strings.TrimSpace(source.Branch) == "" {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("source branch unavailable for %s", taskID)
	}

	target, err := d.taskMergeBaseTarget(ctx, projectID, source.IssueID, d.baseBranchForProject(projectID), false)
	if err != nil {
		return taskCloseIntegrationResult{Requested: true}, err
	}
	targetBranch := strings.TrimSpace(target.Branch)
	targetWorktree := strings.TrimSpace(target.WorktreePath)
	branchAttached := target.BranchAttached
	if targetWorktree == "" {
		if attached, found, err := d.git.WorktreePathForBranch(ctx, targetBranch); err == nil && found && strings.TrimSpace(attached) != "" {
			targetWorktree = strings.TrimSpace(attached)
			branchAttached = true
		}
	}
	if targetWorktree == "" {
		targetWorktree = strings.TrimSpace(d.cfg.RepoDir)
		if targetWorktree == "" {
			targetWorktree = "."
		}
	}

	if err := d.ensureMergeToBaseClean(ctx, source, targetWorktree); err != nil {
		return taskCloseIntegrationResult{Requested: true}, err
	}
	preflight, err := d.git.MergePreflight(ctx, source.Path, targetWorktree, targetBranch, source.Branch)
	if err != nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("merge preflight failed: %w", err)
	}
	if preflight != nil && preflight.HasConflicts {
		reasons := uniqueNonEmpty(preflight.ConflictFiles)
		if len(reasons) == 0 {
			reasons = append(reasons, "unknown")
		}
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("merge preflight failed: predicted conflicts %s", strings.Join(reasons, ", "))
	}
	if err := d.git.Fetch(ctx, targetWorktree, "origin"); err != nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("fetch target branch before close integration: %w", err)
	}
	if !branchAttached {
		if err := d.git.Checkout(ctx, targetWorktree, targetBranch); err != nil {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("checkout target branch before close integration: %w", err)
		}
	}
	merge, err := d.git.Merge(ctx, targetWorktree, source.Branch)
	if err != nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("merge %s into %s: %w", source.Branch, targetBranch, err)
	}
	if merge == nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("merge %s into %s returned no result", source.Branch, targetBranch)
	}
	if !merge.Success {
		details := strings.TrimSpace(merge.Message)
		if len(merge.ConflictFiles) > 0 {
			details = strings.TrimSpace(details + "\nconflicts: " + strings.Join(merge.ConflictFiles, ", "))
		}
		if details == "" {
			details = "merge did not complete successfully"
		}
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("merge %s into %s failed: %s", source.Branch, targetBranch, details)
	}
	return taskCloseIntegrationResult{
		Requested:    true,
		Integrated:   true,
		SourceBranch: source.Branch,
		TargetBranch: targetBranch,
	}, nil
}

func daemonWorktreeForIssue(worktrees []git.Worktree, issueID string) (git.Worktree, bool) {
	for _, wt := range worktrees {
		if naming.IssueIDsEqual(wt.IssueID, issueID) {
			return wt, true
		}
	}
	return git.Worktree{}, false
}

func (d *Daemon) ensureMergeToBaseClean(ctx context.Context, source git.Worktree, targetWorktree string) error {
	sourceStatus, err := d.git.Status(ctx, source.Path)
	if err != nil {
		return fmt.Errorf("read source status for %s: %w", source.IssueID, err)
	}
	targetStatus, err := d.git.Status(ctx, targetWorktree)
	if err != nil {
		return fmt.Errorf("read target branch status: %w", err)
	}
	reasons := make([]string, 0, 2)
	if gitStatusHasDirtyFiles(sourceStatus) {
		reasons = append(reasons, fmt.Sprintf("source %s is not clean: %s", source.IssueID, gitStatusSummary(sourceStatus)))
	}
	if gitStatusHasDirtyFiles(targetStatus) {
		reasons = append(reasons, fmt.Sprintf("target branch is not clean: %s", gitStatusSummary(targetStatus)))
	}
	if len(reasons) > 0 {
		return errors.New(strings.Join(reasons, "; "))
	}
	return nil
}

func gitStatusHasDirtyFiles(status *git.GitStatus) bool {
	if status == nil {
		return false
	}
	return status.HasChanges ||
		status.HasConflicts ||
		len(status.Modified) > 0 ||
		len(status.Added) > 0 ||
		len(status.Deleted) > 0 ||
		len(status.Untracked) > 0 ||
		len(status.Staged) > 0 ||
		len(status.Conflicted) > 0
}

func gitStatusSummary(status *git.GitStatus) string {
	if status == nil {
		return "unknown"
	}
	parts := make([]string, 0, 6)
	if len(status.Modified) > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", len(status.Modified)))
	}
	if len(status.Added) > 0 {
		parts = append(parts, fmt.Sprintf("%d added", len(status.Added)))
	}
	if len(status.Deleted) > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", len(status.Deleted)))
	}
	if len(status.Untracked) > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", len(status.Untracked)))
	}
	if len(status.Staged) > 0 {
		parts = append(parts, fmt.Sprintf("%d staged", len(status.Staged)))
	}
	if len(status.Conflicted) > 0 {
		parts = append(parts, fmt.Sprintf("%d conflicted", len(status.Conflicted)))
	}
	if len(parts) == 0 {
		return "dirty"
	}
	return strings.Join(parts, ", ")
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (d *Daemon) stopTaskSessionForClose(ctx context.Context, req protocol.RequestEnvelope, projectID, taskID string) error {
	body, err := json.Marshal(sessionCommandBody{
		ProjectID: projectID,
		SessionID: taskID,
	})
	if err != nil {
		return err
	}
	stopReq := req
	stopReq.Command = "session.stop"
	stopReq.Body = body
	stopReq.Meta.ProjectID = naming.ProjectID(projectID)
	resp, err := d.handleSessionStopDirect(ctx, stopReq)
	return cleanupCommandError(resp, err)
}

func (d *Daemon) cleanupTaskIssueResourcesForClose(ctx context.Context, projectID, taskID, worktreePath string) error {
	if len(d.runtimeConfigForProject(projectID).IssueResources.CleanupCommands) == 0 {
		return nil
	}
	lookupPath, branch := d.issueWorktreeContext(ctx, projectID, taskID)
	if strings.TrimSpace(worktreePath) == "" {
		worktreePath = lookupPath
	}
	resourceCtx := d.issueResourceLifecycleContext(projectID, taskID, naming.CanonicalSessionID(projectID, taskID), worktreePath, branch)
	_, err := d.runIssueResourceCleanupCommands(ctx, projectID, resourceCtx)
	return err
}

func (d *Daemon) updateTaskStatusExcludingClose(ctx context.Context, projectID, taskID string, status domain.Status) (domain.Task, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.Task{}, fmt.Errorf("issue store unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	if status == domain.StatusDone {
		return domain.Task{}, fmt.Errorf("status %s must be applied with task.close", status)
	}
	return issueClient.UpdateWithRuntime(ctx, projectID, taskID, status)
}

func (d *Daemon) handleTaskClosePreflight(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd taskClosePreflightRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.validateTaskClosePreflight(ctx, projectID, cmd.TaskID, cmd.taskClosePreflightOptions, req)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) validateTaskClosePreflight(ctx context.Context, projectID, taskID string, opts taskClosePreflightOptions, req protocol.RequestEnvelope) (taskClosePreflightResult, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return taskClosePreflightResult{}, fmt.Errorf("issue store unavailable")
	}
	tasks, err := issueClient.ListWithRuntime(ctx, projectID)
	if err != nil {
		return taskClosePreflightResult{}, fmt.Errorf("inspect runtime attachments before closing %s: %w", taskID, err)
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
		return taskClosePreflightResult{}, fmt.Errorf("issue not found: %s", taskID)
	}

	reasons := make([]string, 0, 3)
	reasons = append(reasons, daemonCloseGuardRuntimeBlockers(task, opts)...)
	reasons = append(reasons, daemonCloseGuardChildBlockers(task.ID, tasks)...)
	if len(reasons) > 0 {
		repairs, repairErr := d.reopenClosedCloseGuardBlockers(ctx, issueClient, projectID, req, task, tasks, reasons)
		if repairErr != nil {
			return taskClosePreflightResult{}, fmt.Errorf("%s. Failed to move closed blockers back for cleanup: %w", daemonCloseGuardFailureMessage(taskID, reasons, repairs), repairErr)
		}
		return taskClosePreflightResult{}, fmt.Errorf("%s", daemonCloseGuardFailureMessage(taskID, reasons, repairs))
	}

	worktreePath, err := d.resolveTaskCloseWorktreePath(ctx, projectID, taskID, task, opts)
	if err != nil {
		return taskClosePreflightResult{}, err
	}
	if strings.TrimSpace(worktreePath) == "" {
		return taskClosePreflightResult{Task: task}, nil
	}
	if opts.ForceWorktree {
		return taskClosePreflightResult{Task: task, Worktree: worktreePath}, nil
	}
	status, err := d.refreshTaskCloseGitStatus(ctx, projectID, worktreePath)
	if err != nil {
		return taskClosePreflightResult{}, fmt.Errorf("inspect git status before closing %s: %w", taskID, err)
	}
	if reasons := daemonCloseGuardGitBlockers(*status, opts); len(reasons) > 0 {
		repairs, repairErr := d.reopenClosedCloseGuardBlockers(ctx, issueClient, projectID, req, task, tasks, reasons)
		if repairErr != nil {
			return taskClosePreflightResult{}, fmt.Errorf("%s. Failed to move closed blockers back for cleanup: %w", daemonCloseGuardFailureMessage(taskID, reasons, repairs), repairErr)
		}
		return taskClosePreflightResult{}, fmt.Errorf("%s", daemonCloseGuardFailureMessage(taskID, reasons, repairs))
	}
	return taskClosePreflightResult{Task: task, Worktree: worktreePath, Status: *status}, nil
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
	hasGitState := false
	hasRuntime := false
	hasChildren := false
	for _, reason := range reasons {
		if strings.Contains(reason, "local changes") || strings.Contains(reason, "conflicts") || strings.Contains(reason, "ahead") {
			hasGitState = true
		}
		if strings.HasPrefix(reason, "issue still has a ") || strings.HasPrefix(reason, "worktree is projected") {
			hasRuntime = true
		}
		if strings.Contains(reason, "child issues") {
			hasChildren = true
		}
	}

	steps := make([]string, 0, 3)
	if hasGitState {
		steps = append(steps, "commit, discard, or merge the worktree changes first")
	}
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

func daemonCloseGuardRuntimeBlockers(task domain.Task, opts taskClosePreflightOptions) []string {
	reasons := make([]string, 0, 2)
	if !opts.AllowTargetSession && daemonCloseGuardTaskHasSession(task) {
		reasons = append(reasons, "issue still has a session")
	}
	if !opts.AllowTargetWorktree && daemonCloseGuardTaskHasWorktree(task) {
		reasons = append(reasons, "issue still has a worktree")
	}
	return reasons
}

func (d *Daemon) resolveTaskCloseWorktreePath(ctx context.Context, projectID, taskID string, task domain.Task, opts taskClosePreflightOptions) (string, error) {
	worktreePath := daemonCloseGuardTaskWorktree(task)
	if worktreePath != "" {
		return worktreePath, nil
	}
	if !task.HasWorktree {
		return "", nil
	}
	if opts.ForceWorktree {
		return "", nil
	}
	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return "", fmt.Errorf("inspect worktree before closing %s: worktree manager unavailable", taskID)
	}
	worktree, err := manager.Get(ctx, taskID)
	if err != nil {
		if errors.Is(err, git.ErrWorktreeNotFound) {
			return "", fmt.Errorf("cannot close issue %s: worktree is projected but path is unavailable. Next: run `az issue close --id %s --force-worktree` after confirming the worktree is gone, then retry", taskID, taskID)
		}
		return "", fmt.Errorf("inspect worktree before closing %s: %w", taskID, err)
	}
	return strings.TrimSpace(worktree.Path), nil
}

func (d *Daemon) refreshTaskCloseGitStatus(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	if d.gitStatusAdapter == nil {
		return nil, fmt.Errorf("git status service unavailable")
	}
	status, err := d.gitStatusAdapter.RefreshStatus(ctx, projectID, worktree)
	if err != nil {
		return nil, err
	}
	if status == nil {
		return &git.GitStatus{}, nil
	}
	return status, nil
}

func daemonCloseGuardGitBlockers(status git.GitStatus, opts taskClosePreflightOptions) []string {
	dirty := daemonCloseGuardDirtyFiles(status)
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
	if status.GitAheadCount > 0 && !opts.IgnoreAhead {
		reasons = append(reasons, fmt.Sprintf("branch is ahead by %d commit(s)", status.GitAheadCount))
	}
	return reasons
}

func daemonCloseGuardDirtyFiles(status git.GitStatus) []string {
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
	sort.Strings(out)
	return out
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
	return daemonCloseGuardTaskWorktree(task) != ""
}

func daemonCloseGuardTaskWorktree(task domain.Task) string {
	if task.Session == nil {
		return ""
	}
	return strings.TrimSpace(task.Session.Worktree)
}

func (d *Daemon) handleTaskDeletePreflight(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.validateTaskDeletePreflight(ctx, projectID, cmd.TaskID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) validateTaskDeletePreflight(ctx context.Context, projectID, taskID string) (taskDeletePreflightResult, error) {
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return taskDeletePreflightResult{}, fmt.Errorf("inspect runtime attachments before deleting %s: %w", taskID, err)
	}
	task, ok := findDaemonTaskByID(tasks, taskID)
	if !ok {
		return taskDeletePreflightResult{}, fmt.Errorf("issue not found: %s", taskID)
	}
	return taskDeletePreflightResult{Task: task, Blockers: daemonTaskDeleteRuntimeBlockers(task)}, nil
}

func daemonTaskDeleteRuntimeBlockers(task domain.Task) []string {
	blockers := make([]string, 0, 2)
	if daemonCloseGuardTaskHasSession(task) {
		blockers = append(blockers, "session")
	}
	if daemonCloseGuardTaskHasWorktree(task) {
		blockers = append(blockers, "worktree")
	}
	return blockers
}

func (d *Daemon) handleTaskGraphReadiness(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.taskGraphReadiness(ctx, projectID, cmd.TaskID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) handleTaskCompleteCheck(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.taskCompleteCheck(ctx, projectID, cmd.TaskID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) handleTaskIntegrationReadiness(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID  string `json:"task_id"`
		RepoDir string `json:"repo_dir,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.taskIntegrationReadiness(ctx, projectID, cmd.TaskID, cmd.RepoDir)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) taskIntegrationReadiness(ctx context.Context, projectID, issueID, repoDir string) (taskIntegrationReadinessResult, error) {
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return taskIntegrationReadinessResult{}, fmt.Errorf("inspect issue integration readiness: %w", err)
	}
	task, ok := findDaemonTaskByID(tasks, issueID)
	if !ok {
		return taskIntegrationReadinessResult{
			IssueID: strings.TrimSpace(issueID),
			Ready:   false,
			Reasons: []string{fmt.Sprintf("issue %s not found in daemon task projection", strings.TrimSpace(issueID))},
		}, nil
	}
	parentIssueID := task.ID.String()
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		parentIssueID = strings.TrimSpace(task.ParentID.String())
	}
	if task.Status == domain.StatusDone {
		return taskIntegrationReadinessResult{
			IssueID:       task.ID.String(),
			ParentIssueID: parentIssueID,
			Ready:         true,
		}, nil
	}

	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return taskIntegrationReadinessResult{
			IssueID:       task.ID.String(),
			ParentIssueID: parentIssueID,
			Ready:         false,
			Reasons: []string{
				fmt.Sprintf("issue %s is not closed", task.ID.String()),
				"repo_dir is required to inspect worker-integration-ready mailbox evidence",
			},
		}, nil
	}
	events, err := readMailboxEvents(repoDir, parentIssueID)
	if err != nil {
		return taskIntegrationReadinessResult{}, fmt.Errorf("list mailbox events for %s: %w", parentIssueID, err)
	}
	for _, evt := range events {
		if naming.IssueIDsEqual(evt.IssueID, task.ID.String()) && daemonWorkerIntegrationReadyMailType(evt.Type) {
			return taskIntegrationReadinessResult{
				IssueID:       task.ID.String(),
				ParentIssueID: parentIssueID,
				Ready:         true,
			}, nil
		}
	}
	return taskIntegrationReadinessResult{
		IssueID:       task.ID.String(),
		ParentIssueID: parentIssueID,
		Ready:         false,
		Reasons: []string{
			fmt.Sprintf("issue %s is not closed", task.ID.String()),
			fmt.Sprintf("no worker-integration-ready mailbox event found under parent %s for %s", parentIssueID, task.ID.String()),
		},
	}, nil
}

func daemonWorkerIntegrationReadyMailType(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "worker-integration-ready", "worker-ready", "worker-complete":
		return true
	default:
		return false
	}
}

func (d *Daemon) handleTaskMergeBaseTarget(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID            string `json:"task_id"`
		BaseBranch        string `json:"base_branch,omitempty"`
		AllowBaseForChild bool   `json:"allow_base_for_child,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.taskMergeBaseTarget(ctx, projectID, cmd.TaskID, cmd.BaseBranch, cmd.AllowBaseForChild)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) handleTaskFollowOnMergeCandidates(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.taskFollowOnMergeCandidates(ctx, projectID, cmd.TaskID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) taskMergeBaseTarget(ctx context.Context, projectID, issueID, baseBranch string, allowBaseForChild bool) (taskMergeBaseTargetResult, error) {
	issueID = strings.TrimSpace(issueID)
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		baseBranch = d.cfg.BaseBranch
	}
	if strings.TrimSpace(baseBranch) == "" {
		baseBranch = "main"
	}

	defaultTarget := taskMergeBaseTargetResult{
		IssueID:  issueID,
		TargetID: "base",
		Branch:   baseBranch,
	}
	if issueID == "" {
		defaultTarget.Reason = "empty issue id: default base target"
		return defaultTarget, nil
	}

	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return taskMergeBaseTargetResult{}, fmt.Errorf("resolve merge base branch task graph: %w", err)
	}
	tasksByID := tasksByDaemonIssueID(tasks)
	sourceTask, ok := tasksByID[issueID]
	if !ok {
		return taskMergeBaseTargetResult{}, fmt.Errorf("cannot resolve merge target for %s: issue not found in task projection; refusing fallback to base", issueID)
	}

	if d.worktreeAdapter == nil {
		return taskMergeBaseTargetResult{}, fmt.Errorf("resolve merge base branch worktrees: worktree adapter unavailable")
	}
	worktrees, err := d.worktreeAdapter.List(ctx, projectID)
	if err != nil {
		return taskMergeBaseTargetResult{}, fmt.Errorf("resolve merge base branch worktrees: %w", err)
	}

	if target, ok := domain.ClosestAncestorWithWorktree(issueID, tasksByID, daemonIssueWorktreeRefs(worktrees)); ok {
		return taskMergeBaseTargetResult{
			IssueID:        issueID,
			TargetID:       target.IssueID,
			Branch:         target.Branch,
			WorktreePath:   target.WorktreePath,
			BranchAttached: true,
			Reason:         "selected closest ancestor worktree branch",
			AncestorChain:  target.AncestorChain,
		}, nil
	} else {
		defaultTarget.AncestorChain = target.AncestorChain
	}
	for _, parentID := range defaultTarget.AncestorChain {
		if _, ok := tasksByID[parentID]; !ok {
			return taskMergeBaseTargetResult{}, fmt.Errorf("cannot resolve merge target for %s: parent issue %s missing from task projection; refusing fallback to base", issueID, parentID)
		}
	}
	if domain.TaskParentIssueID(sourceTask) != "" && !allowBaseForChild {
		return taskMergeBaseTargetResult{}, fmt.Errorf("refusing to merge child issue %s into base without explicit override; rerun with --allow-base-for-child", issueID)
	}
	if domain.TaskParentIssueID(sourceTask) != "" {
		defaultTarget.Reason = "no ancestor worktree branch found; explicit override allowed base target"
	} else {
		defaultTarget.Reason = "no ancestor chain; selected default base target"
	}
	return defaultTarget, nil
}

func (d *Daemon) taskFollowOnMergeCandidates(ctx context.Context, projectID, targetIssueID string) (taskFollowOnMergeCandidatesResult, error) {
	targetIssueID = strings.TrimSpace(targetIssueID)
	if targetIssueID == "" {
		return taskFollowOnMergeCandidatesResult{}, fmt.Errorf("target issue id is required")
	}
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return taskFollowOnMergeCandidatesResult{}, fmt.Errorf("resolve follow-on merge candidates task graph: %w", err)
	}
	tasksByID := tasksByDaemonIssueID(tasks)
	target, ok := tasksByID[targetIssueID]
	if !ok {
		return taskFollowOnMergeCandidatesResult{}, fmt.Errorf("cannot resolve follow-on merge candidates for %s: issue not found in task projection", targetIssueID)
	}

	candidates := make([]taskFollowOnMergeCandidateItem, 0, 4)
	seen := make(map[string]struct{}, 4)
	addCandidate := func(taskID, relation string, order int) {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" {
			return
		}
		if _, ok := seen[taskID]; ok {
			return
		}
		task, ok := tasksByID[taskID]
		if !ok {
			return
		}
		hasWorktree := daemonTaskHasWorktree(task)
		if !daemonEligibleFollowOnMergeSource(task, relation, hasWorktree) {
			return
		}
		candidates = append(candidates, taskFollowOnMergeCandidateItem{
			IssueID:     task.ID.String(),
			Title:       task.Title,
			Status:      task.Status,
			Relation:    relation,
			Order:       order,
			HasWorktree: hasWorktree,
		})
		seen[taskID] = struct{}{}
	}

	if target.ParentID != nil {
		addCandidate(target.ParentID.String(), string(domain.DependencyParentChild), 0)
	}
	for _, dep := range target.Dependencies {
		switch dep.Type {
		case domain.DependencyBlocks, domain.DependencyBlockedBy:
			addCandidate(dep.ID.String(), string(domain.DependencyBlocks), 1)
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Order != candidates[j].Order {
			return candidates[i].Order < candidates[j].Order
		}
		if daemonFollowOnMergeStatusPriority(candidates[i].Status) != daemonFollowOnMergeStatusPriority(candidates[j].Status) {
			return daemonFollowOnMergeStatusPriority(candidates[i].Status) < daemonFollowOnMergeStatusPriority(candidates[j].Status)
		}
		if candidates[i].Title != candidates[j].Title {
			return candidates[i].Title < candidates[j].Title
		}
		return candidates[i].IssueID < candidates[j].IssueID
	})

	return taskFollowOnMergeCandidatesResult{
		TaskID:            target.ID.String(),
		MergeTargetToBase: target.ParentID == nil || target.ParentID.IsZero(),
		Candidates:        candidates,
	}, nil
}

func daemonTaskHasWorktree(task domain.Task) bool {
	if task.Session != nil && strings.TrimSpace(task.Session.Worktree) != "" {
		return true
	}
	return task.HasWorktree
}

func daemonEligibleFollowOnMergeSource(task domain.Task, relation string, hasWorktree bool) bool {
	switch relation {
	case string(domain.DependencyParentChild), string(domain.DependencyBlocks):
		return hasWorktree && (task.Status == domain.StatusInProgress || task.Status == domain.StatusDone)
	default:
		return false
	}
}

func daemonFollowOnMergeStatusPriority(status domain.Status) int {
	switch status {
	case domain.StatusInProgress:
		return 0
	case domain.StatusDone:
		return 1
	default:
		return 2
	}
}

func tasksByDaemonIssueID(tasks []domain.Task) map[string]domain.Task {
	tasksByID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		id := strings.TrimSpace(task.ID.String())
		if id == "" {
			continue
		}
		tasksByID[id] = task
	}
	return tasksByID
}

func daemonIssueWorktreeRefs(worktrees []git.Worktree) map[string]domain.IssueWorktreeRef {
	refs := make(map[string]domain.IssueWorktreeRef, len(worktrees))
	for _, worktree := range worktrees {
		issueID := strings.TrimSpace(worktree.IssueID)
		if issueID == "" {
			continue
		}
		refs[issueID] = domain.IssueWorktreeRef{
			Branch: strings.TrimSpace(worktree.Branch),
			Path:   strings.TrimSpace(worktree.Path),
		}
	}
	return refs
}

func (d *Daemon) taskCompleteCheck(ctx context.Context, projectID, rootIssueID string) (taskCompleteCheckResult, error) {
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return taskCompleteCheckResult{}, fmt.Errorf("inspect issue graph before completion check: %w", err)
	}
	rootID, byID, children, err := daemonTaskGraphIndexes(rootIssueID, tasks)
	if err != nil {
		return taskCompleteCheckResult{}, err
	}
	ready, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		return taskCompleteCheckResult{}, err
	}

	desc := daemonTaskGraphDescendants(rootID, children)
	openDescendants := make([]string, 0, len(desc))
	activeSessions := make([]string, 0, len(desc))
	for _, id := range desc {
		task := byID[id]
		if task.Status != domain.StatusDone {
			openDescendants = append(openDescendants, id.String())
		}
		if daemonCloseGuardTaskHasSession(task) {
			activeSessions = append(activeSessions, id.String())
		}
	}
	sort.Strings(openDescendants)
	sort.Strings(activeSessions)

	reasons := make([]string, 0, 3)
	if len(ready.Runnable) > 0 {
		reasons = append(reasons, fmt.Sprintf("runnable leaves remain: %s", strings.Join(ready.Runnable, ",")))
	}
	if len(openDescendants) > 0 {
		reasons = append(reasons, fmt.Sprintf("required descendants not closed: %s", strings.Join(openDescendants, ",")))
	}
	if len(activeSessions) > 0 {
		reasons = append(reasons, fmt.Sprintf("active child sessions remain: %s", strings.Join(activeSessions, ",")))
	}
	return taskCompleteCheckResult{
		RootIssueID: rootID.String(),
		Pass:        len(reasons) == 0,
		Reasons:     reasons,
		Advice:      daemonTaskCompletionAdvice(rootID.String(), ready.Runnable, openDescendants, activeSessions),
	}, nil
}

func (d *Daemon) taskGraphReadiness(ctx context.Context, projectID, rootIssueID string) (taskGraphReadinessResult, error) {
	tasks, err := d.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return taskGraphReadinessResult{}, fmt.Errorf("inspect issue graph readiness: %w", err)
	}
	rootID, byID, children, err := daemonTaskGraphIndexes(rootIssueID, tasks)
	if err != nil {
		return taskGraphReadinessResult{}, err
	}
	ready, err := daemonTaskGraphReadinessFromIndexes(rootID, byID, children)
	if err != nil {
		return taskGraphReadinessResult{}, err
	}
	ready.ActiveSessions = daemonTaskGraphActiveSessions(ready.Active, byID)
	return ready, nil
}

func (d *Daemon) loadTaskGraphDomainTasks(ctx context.Context, projectID string) ([]domain.Task, error) {
	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task graph session runtime refresh failed", "project_id", projectID, "error", err)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	tasks, err := issueClient.ListWithRuntime(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return d.enrichTasksWithSessionState(ctx, projectID, tasks), nil
}

func daemonTaskGraphIndexes(rootIssueID string, tasks []domain.Task) (naming.IssueID, map[naming.IssueID]domain.Task, map[naming.IssueID][]naming.IssueID, error) {
	rootID, err := naming.ParseIssueID(strings.TrimSpace(rootIssueID))
	if err != nil {
		return "", nil, nil, fmt.Errorf("invalid root issue id %q: %w", rootIssueID, err)
	}
	byID := make(map[naming.IssueID]domain.Task, len(tasks))
	children := make(map[naming.IssueID][]naming.IssueID, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
		if task.ParentID != nil && !task.ParentID.IsZero() {
			children[*task.ParentID] = append(children[*task.ParentID], task.ID)
		}
	}
	if _, ok := byID[rootID]; !ok {
		return "", nil, nil, fmt.Errorf("root issue not found: %s", rootIssueID)
	}
	return rootID, byID, children, nil
}

func daemonTaskGraphReadinessFromIndexes(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID) (taskGraphReadinessResult, error) {
	desc := daemonTaskGraphDescendants(rootID, children)
	leaves := make([]string, 0, len(desc))
	for _, id := range desc {
		task := byID[id]
		if task.Type == domain.TypeEpic {
			continue
		}
		if len(children[id]) == 0 {
			leaves = append(leaves, id.String())
		}
	}
	sort.Strings(leaves)
	result := taskGraphReadinessResult{
		RootIssueID: rootID.String(),
		Runnable:    make([]string, 0, len(leaves)),
		Active:      make([]string, 0),
		Blocked:     make(map[string]string),
	}
	for _, idRaw := range leaves {
		id, parseErr := naming.ParseIssueID(idRaw)
		if parseErr != nil {
			continue
		}
		task := byID[id]
		if task.Status == domain.StatusDone {
			continue
		}
		if daemonCloseGuardTaskHasSession(task) {
			result.Active = append(result.Active, idRaw)
			continue
		}
		blockers := daemonTaskGraphUnresolvedBlockers(task, byID)
		if len(blockers) > 0 {
			result.Blocked[idRaw] = "waiting on " + strings.Join(blockers, ",")
			continue
		}
		result.Runnable = append(result.Runnable, idRaw)
	}
	return result, nil
}

func daemonTaskGraphDescendants(root naming.IssueID, children map[naming.IssueID][]naming.IssueID) []naming.IssueID {
	out := make([]naming.IssueID, 0, 16)
	stack := append([]naming.IssueID(nil), children[root]...)
	seen := map[naming.IssueID]struct{}{}
	for len(stack) > 0 {
		cur := stack[0]
		stack = stack[1:]
		if _, ok := seen[cur]; ok {
			continue
		}
		seen[cur] = struct{}{}
		out = append(out, cur)
		stack = append(stack, children[cur]...)
	}
	return out
}

func daemonTaskGraphUnresolvedBlockers(task domain.Task, byID map[naming.IssueID]domain.Task) []string {
	out := make([]string, 0, 4)
	for _, dep := range task.Dependencies {
		if dep.Type != domain.DependencyBlocks {
			continue
		}
		depTask, ok := byID[dep.ID]
		if !ok {
			out = append(out, dep.ID.String()+"(missing)")
			continue
		}
		if depTask.Status != domain.StatusDone {
			out = append(out, dep.ID.String())
		}
	}
	sort.Strings(out)
	return out
}

func daemonTaskGraphActiveSessions(activeIDs []string, byID map[naming.IssueID]domain.Task) []taskGraphActiveSession {
	if len(activeIDs) == 0 {
		return nil
	}
	out := make([]taskGraphActiveSession, 0, len(activeIDs))
	for _, issueID := range activeIDs {
		taskID, _ := naming.ParseIssueID(issueID)
		task, ok := byID[taskID]
		active := taskGraphActiveSession{
			IssueID:        issueID,
			Activity:       "unknown",
			ActivitySource: "none",
			Advice:         fmt.Sprintf("activity unknown: check hooks with az ai status --target=auto; install/update with az ai install --target=auto; use sparse pane capture only if status/watch looks stale, failed, or contradictory for %s", issueID),
		}
		if ok && task.Session != nil {
			active.State = string(task.Session.State)
			active.TmuxAttachedCount = task.Session.TmuxAttachedCount
			if activity := strings.TrimSpace(task.Session.Activity); activity != "" {
				active.Activity = activity
			}
			if source := strings.TrimSpace(task.Session.ActivitySource); source != "" {
				active.ActivitySource = source
			}
			if active.Activity != "unknown" {
				active.Advice = ""
			}
		}
		out = append(out, active)
	}
	return out
}

func daemonTaskCompletionAdvice(rootIssueID string, runnable, openDescendants, activeSessions []string) []string {
	advice := make([]string, 0, len(runnable)+len(openDescendants)+len(activeSessions))
	for _, id := range activeSessions {
		advice = append(advice, fmt.Sprintf("if intentionally abandoning active worker session, repair-stop it: az orchestrate close-session --issue %s", id))
	}
	for _, id := range openDescendants {
		advice = append(advice, fmt.Sprintf("after integration/evidence, close required child issue: az issue close --id %s", id))
	}
	for _, id := range runnable {
		advice = append(advice, fmt.Sprintf("start or resolve runnable leaf: az orchestrate start --root %s --issue %s --json", rootIssueID, id))
	}
	return uniqueDaemonTaskAdvice(advice)
}

func uniqueDaemonTaskAdvice(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func findDaemonTaskByID(tasks []domain.Task, taskID string) (domain.Task, bool) {
	taskID = strings.TrimSpace(taskID)
	for _, candidate := range tasks {
		if strings.EqualFold(strings.TrimSpace(candidate.ID.String()), taskID) {
			return candidate, true
		}
	}
	return domain.Task{}, false
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
	var cmd taskDeleteRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task delete requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	result, err := d.deleteTask(ctx, issueClient, projectID, cmd, req)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = result.Revision
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task delete completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func (d *Daemon) deleteTask(ctx context.Context, issueClient *issues.Client, projectID string, cmd taskDeleteRequest, req protocol.RequestEnvelope) (taskDeleteResult, error) {
	taskID := strings.TrimSpace(cmd.TaskID)
	if taskID == "" {
		return taskDeleteResult{}, fmt.Errorf("task id is required")
	}
	preflight, err := d.validateTaskDeletePreflight(ctx, projectID, taskID)
	if err != nil {
		return taskDeleteResult{}, err
	}
	result := taskDeleteResult{
		TaskID:         taskID,
		Deleted:        true,
		WorktreeForced: cmd.ForceWorktree,
	}
	resourceCleanupRan := false
	if len(preflight.Blockers) > 0 {
		missing := daemonTaskDeleteMissingCleanupOptions(preflight.Blockers, cmd)
		if len(missing) > 0 {
			return result, fmt.Errorf(
				"cannot delete issue %s: runtime metadata fields still present (%s); repair with az issue delete %s --confirm --cleanup --remove-worktree --force-worktree, or rerun with %s",
				taskID,
				strings.Join(preflight.Blockers, ", "),
				taskID,
				strings.Join(missing, " "),
			)
		}
		if daemonCloseGuardTaskHasSession(preflight.Task) {
			if err := d.stopTaskSessionForClose(ctx, req, projectID, taskID); err != nil {
				return result, fmt.Errorf("stop session before deleting %s: %w", taskID, err)
			}
			result.SessionStopped = true
		}
		if daemonCloseGuardTaskHasWorktree(preflight.Task) {
			if err := d.cleanupTaskIssueResourcesBeforeDelete(ctx, projectID, taskID, preflight.Task, result.SessionStopped); err != nil {
				return result, err
			}
			resourceCleanupRan = !result.SessionStopped
			if d.worktreeAdapter == nil {
				return result, fmt.Errorf("remove worktree before deleting %s: worktree cleanup unavailable", taskID)
			}
			if err := d.worktreeAdapter.Delete(ctx, projectID, taskID, cmd.ForceWorktree); err != nil {
				return result, fmt.Errorf("remove worktree before deleting %s: %w", taskID, err)
			}
			result.WorktreeRemoved = true
		}
	}
	if !resourceCleanupRan {
		if err := d.cleanupTaskIssueResourcesBeforeDelete(ctx, projectID, taskID, preflight.Task, result.SessionStopped); err != nil {
			return result, err
		}
	}
	if err := issueClient.Delete(ctx, taskID); err != nil {
		return result, err
	}
	result.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskDeleted, result.Revision, protocol.TaskEventBody{
		ProjectID: naming.ProjectID(projectID),
		TaskID:    naming.IssueID(taskID),
		UpdatedAt: timeNow().UTC(),
	})
	return result, nil
}

func (d *Daemon) cleanupTaskIssueResourcesBeforeDelete(ctx context.Context, projectID, taskID string, task domain.Task, sessionStopped bool) error {
	if sessionStopped {
		return nil
	}
	if err := d.cleanupTaskIssueResourcesForClose(ctx, projectID, taskID, daemonCloseGuardTaskWorktree(task)); err != nil {
		return fmt.Errorf("cleanup issue resources before deleting %s: %w", taskID, err)
	}
	return nil
}

func daemonTaskDeleteMissingCleanupOptions(blockers []string, cmd taskDeleteRequest) []string {
	cleanupAll := cmd.Cleanup
	missing := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		switch blocker {
		case "session":
			if !cleanupAll && !cmd.StopSession {
				missing = append(missing, "stop_session")
			}
		case "worktree":
			if !cleanupAll && !cmd.RemoveWorktree {
				missing = append(missing, "remove_worktree")
			}
		}
	}
	return missing
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
		TaskID              string `json:"task_id"`
		DependsOnID         string `json:"depends_on_id"`
		DependencyType      string `json:"dependency_type"`
		Confirm             bool   `json:"confirm"`
		ConfirmParentOrphan bool   `json:"confirm_parent_orphan"`
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
			"confirm_parent_orphan", cmd.ConfirmParentOrphan,
		)
	}
	callCtx := ctx
	if cmd.Confirm {
		callCtx = issues.WithDependencyRemovalConfirmation(callCtx)
	}
	if cmd.ConfirmParentOrphan {
		callCtx = issues.WithParentChildOrphanConfirmation(callCtx)
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
