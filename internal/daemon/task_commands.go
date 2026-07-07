package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
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
	taskListRuntimeRefreshTTL       = 5 * time.Second
	taskListSnapshotRuntimeCacheTTL = taskListRuntimeRefreshTTL
	taskListSnapshotLoadTimeout     = 10 * time.Second
)

const (
	taskDeferredWorktreeCleanupOperationKind = "task.worktree_cleanup"
	taskDeferredWorktreeCleanupTimeout       = 15 * time.Second
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

type taskListRuntimeRefresh struct {
	done      chan struct{}
	runtimeAt time.Time
	err       error
}

type taskListSnapshotLoadResult struct {
	Revision      uint64
	LastCheckedAt time.Time
	Freshness     protocol.TaskListFreshness
	RuntimeAt     time.Time
	SummariesOnly bool
	Tasks         []domain.Task
}

type taskClosePreflightOptions struct {
	AllowTargetSession  bool `json:"allow_target_session,omitempty"`
	AllowTargetWorktree bool `json:"allow_target_worktree,omitempty"`
	ForceWorktree       bool `json:"force_worktree,omitempty"`
	IgnoreAhead         bool `json:"ignore_ahead,omitempty"`
	CloseCleanChildren  bool `json:"close_clean_children,omitempty"`
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
	CloseCleanChildren   bool   `json:"close_clean_children,omitempty"`
}

type taskDeleteRequest struct {
	TaskID         string `json:"task_id"`
	Cleanup        bool   `json:"cleanup,omitempty"`
	StopSession    bool   `json:"stop_session,omitempty"`
	RemoveWorktree bool   `json:"remove_worktree,omitempty"`
	ForceWorktree  bool   `json:"force_worktree,omitempty"`
}

type taskCloseResult struct {
	TaskID                     string                         `json:"task_id"`
	Status                     string                         `json:"status"`
	ContextRisk                *domain.IssueContextRiskPacket `json:"context_risk,omitempty"`
	IntegrationRequested       bool                           `json:"integration_requested,omitempty"`
	Integrated                 bool                           `json:"integrated,omitempty"`
	IntegratedSourceBranch     string                         `json:"integrated_source_branch,omitempty"`
	IntegratedTargetBranch     string                         `json:"integrated_target_branch,omitempty"`
	SessionStopped             bool                           `json:"session_stopped,omitempty"`
	WorktreeRemoved            bool                           `json:"worktree_removed,omitempty"`
	WorktreeForced             bool                           `json:"worktree_forced,omitempty"`
	Revision                   uint64                         `json:"revision,omitempty"`
	Phases                     []taskClosePhaseTiming         `json:"phases,omitempty"`
	AutoClosedChildren         []string                       `json:"auto_closed_children,omitempty"`
	WorktreeCleanupDeferred    bool                           `json:"worktree_cleanup_deferred,omitempty"`
	WorktreeCleanupOperationID string                         `json:"worktree_cleanup_operation_id,omitempty"`
}

type deferredTaskWorktreeCleanupResult struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	Skipped   bool   `json:"skipped,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type deferredTaskWorktreeCleanupPlan struct {
	Path   string
	Branch string
}

type taskClosePhaseTiming struct {
	Name      string `json:"name"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Skipped   bool   `json:"skipped,omitempty"`
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
	RootIssueID            string                          `json:"root_issue_id"`
	Runnable               []string                        `json:"runnable"`
	NestedRoots            []taskGraphNestedRoot           `json:"nested_roots,omitempty"`
	Pending                []taskGraphPendingStart         `json:"pending,omitempty"`
	Active                 []string                        `json:"active,omitempty"`
	ActiveSessions         []taskGraphActiveSession        `json:"active_sessions,omitempty"`
	SessionStartProgress   []taskGraphSessionStartProgress `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []taskStaleCloseableCandidate   `json:"stale_closeable_children,omitempty"`
	WorkerObservations     []domain.WorkerObservation      `json:"worker_observations,omitempty"`
	Blocked                map[string]string               `json:"blocked"`
}

type taskGraphNestedRoot struct {
	IssueID       string                  `json:"issue_id"`
	Status        string                  `json:"status"`
	Type          string                  `json:"type"`
	ChildCount    int                     `json:"child_count"`
	ActiveSession *taskGraphActiveSession `json:"active_session,omitempty"`
	Advice        string                  `json:"advice,omitempty"`
}

type taskGraphPendingStart struct {
	IssueID        string `json:"issue_id"`
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
}

type taskGraphActiveSession struct {
	IssueID           string                         `json:"issue_id"`
	Activity          string                         `json:"activity"`
	ActivitySource    string                         `json:"activity_source"`
	State             string                         `json:"state,omitempty"`
	Status            string                         `json:"status,omitempty"`
	TmuxAttachedCount int                            `json:"tmux_attached_count,omitempty"`
	StartProgress     *taskGraphSessionStartProgress `json:"start_progress,omitempty"`
	Advice            string                         `json:"advice,omitempty"`
}

type taskGraphSessionStartProgress struct {
	IssueID        string     `json:"issue_id"`
	OperationID    string     `json:"operation_id,omitempty"`
	OperationState string     `json:"operation_state"`
	Phase          string     `json:"phase,omitempty"`
	Message        string     `json:"message,omitempty"`
	Percent        int        `json:"percent,omitempty"`
	ElapsedMS      int64      `json:"elapsed_ms,omitempty"`
	EnqueuedAt     time.Time  `json:"enqueued_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

type taskCompleteCheckResult struct {
	RootIssueID            string                        `json:"root_issue_id"`
	Pass                   bool                          `json:"pass"`
	Reasons                []string                      `json:"reasons,omitempty"`
	Advice                 []string                      `json:"advice,omitempty"`
	StaleCloseableChildren []taskStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
}

type taskStaleCloseableCandidate struct {
	IssueID          string   `json:"issue_id"`
	Status           string   `json:"status"`
	Evidence         []string `json:"evidence"`
	SuggestedCommand string   `json:"suggested_command"`
}

type taskIntegrationReadinessResult struct {
	IssueID                string                         `json:"issue_id"`
	ParentIssueID          string                         `json:"parent_issue_id,omitempty"`
	Ready                  bool                           `json:"ready"`
	ContextRisk            *domain.IssueContextRiskPacket `json:"context_risk,omitempty"`
	Reasons                []string                       `json:"reasons,omitempty"`
	EvidenceEventSeq       int64                          `json:"evidence_event_seq,omitempty"`
	EvidencePacket         *domain.WorkerEvidencePacket   `json:"evidence_packet,omitempty"`
	EvidenceIncomplete     bool                           `json:"evidence_incomplete,omitempty"`
	EvidenceMissingFields  []string                       `json:"evidence_missing_fields,omitempty"`
	EvidenceInvalidReasons []string                       `json:"evidence_invalid_reasons,omitempty"`
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
	listReq, err := decodeTaskListRequest(req.Body)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	query := strings.TrimSpace(listReq.Query)
	startedAt := time.Now()
	result, shared, err := d.loadTaskListSnapshot(ctx, req, projectID, query, listReq.IncludeDependencies)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	payload := buildTaskListSnapshotPayload(projectID, result.Revision, result.LastCheckedAt, result.Freshness, result.Tasks, result.SummariesOnly)
	marshalStartedAt := time.Now()
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(result.Tasks), "cache_hit", false, "shared_load", shared, "query", query != "")
	body, err := json.Marshal(payload)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = payload.SnapshotRevision
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list completed", "project_id", projectID, "task_count", len(result.Tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds(), "shared_load", shared, "query", query != "")
	}
	return resp, nil
}

func (d *Daemon) handleBoardFetch(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	cacheStartedAt := time.Now()
	if cached, ok := d.readFreshTaskListSnapshotCache(projectID); ok {
		hydrated, hydrateErr := d.hydrateTaskListSnapshotCache(ctx, projectID, cached.Tasks)
		if hydrateErr != nil {
			latencytrace.LogPhase(d.cfg.Logger, "daemon", "board.fetch.snapshot_cache_hydrate", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", false, "error", hydrateErr)
		} else {
			cached.Tasks = hydrated
			cached.LastCheckedAt, cached.Freshness = d.taskListSnapshotFreshness(ctx, projectID)
			latencytrace.LogPhase(d.cfg.Logger, "daemon", "board.fetch.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", true)
			payload := buildBoardSnapshotPayload(projectID, cached.Revision, cached.LastCheckedAt, cached.Freshness, cached.Tasks)
			marshalStartedAt := time.Now()
			body, err := json.Marshal(payload)
			latencytrace.LogPhase(d.cfg.Logger, "daemon", "board.fetch.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(cached.Tasks), "cache_hit", true)
			if err != nil {
				return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
			}
			resp.Body = body
			resp.Revision = payload.SnapshotRevision
			if d.cfg.Logger != nil {
				d.cfg.Logger.Info("daemon board fetch completed", "project_id", projectID, "task_count", len(cached.Tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds(), "cache_hit", true)
			}
			return resp, nil
		}
	}
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "board.fetch.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", false)
	result, shared, err := d.loadTaskListSnapshot(ctx, req, projectID, "", false)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	payload := buildBoardSnapshotPayload(projectID, result.Revision, result.LastCheckedAt, result.Freshness, result.Tasks)
	marshalStartedAt := time.Now()
	body, err := json.Marshal(payload)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "board.fetch.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(result.Tasks), "cache_hit", false, "shared_load", shared)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = payload.SnapshotRevision
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon board fetch completed", "project_id", projectID, "task_count", len(result.Tasks), "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds(), "shared_load", shared)
	}
	return resp, nil
}

func decodeTaskListRequest(body []byte) (protocol.TaskListRequestBody, error) {
	if len(body) == 0 || strings.TrimSpace(string(body)) == "null" {
		return protocol.TaskListRequestBody{}, nil
	}
	var req protocol.TaskListRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		return protocol.TaskListRequestBody{}, err
	}
	req.Query = strings.TrimSpace(req.Query)
	return req, nil
}

func (d *Daemon) loadTaskListSnapshot(ctx context.Context, req protocol.RequestEnvelope, projectID string, query string, includeDependencies bool) (taskListSnapshotLoadResult, bool, error) {
	projectID = d.canonicalProjectID(projectID)
	query = strings.TrimSpace(query)
	loadKey := projectID
	if query != "" {
		loadKey = projectID + "\x00query:" + strings.ToLower(query)
	}
	if includeDependencies {
		loadKey += "\x00deps"
	}

	d.taskListSnapshotLoadMu.Lock()
	if d.taskListSnapshotLoads == nil {
		d.taskListSnapshotLoads = map[string]*taskListSnapshotLoad{}
	}
	if load := d.taskListSnapshotLoads[loadKey]; load != nil {
		d.taskListSnapshotLoadMu.Unlock()
		select {
		case <-ctx.Done():
			return taskListSnapshotLoadResult{}, true, ctx.Err()
		case <-load.done:
			return cloneTaskListSnapshotLoadResult(load.result), true, load.err
		}
	}
	load := &taskListSnapshotLoad{done: make(chan struct{})}
	d.taskListSnapshotLoads[loadKey] = load
	d.taskListSnapshotLoadMu.Unlock()

	buildCtx, cancel := context.WithTimeout(context.Background(), taskListSnapshotLoadTimeout)
	defer cancel()
	result, err := d.buildTaskListSnapshot(buildCtx, req, projectID, query, includeDependencies)
	load.result = cloneTaskListSnapshotLoadResult(result)
	load.err = err

	d.taskListSnapshotLoadMu.Lock()
	delete(d.taskListSnapshotLoads, loadKey)
	close(load.done)
	d.taskListSnapshotLoadMu.Unlock()

	return result, false, err
}

func (d *Daemon) buildTaskListSnapshot(ctx context.Context, req protocol.RequestEnvelope, projectID string, query string, includeDependencies bool) (taskListSnapshotLoadResult, error) {
	query = strings.TrimSpace(query)
	refreshStartedAt := time.Now()
	runtimeAt, runtimeRefreshed, refreshErr := d.refreshTaskListSessionRuntimeState(ctx, projectID)
	if refreshErr != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task list session runtime projection refresh failed", "project_id", projectID, "error", refreshErr)
	}
	d.triggerWorktreeStateRefresh(projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.worktree_refresh_trigger", refreshStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "session_runtime_refreshed", runtimeRefreshed)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task list requested", "project_id", projectID, "query", query != "")
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return taskListSnapshotLoadResult{}, errors.New("issue store unavailable")
	}
	queryStartedAt := time.Now()
	var (
		tasks         []domain.Task
		summariesOnly bool
		err           error
	)
	if query == "" {
		if includeDependencies {
			tasks, err = issueClient.ListSummariesWithRuntimeDependencies(ctx, projectID)
		} else {
			tasks, err = issueClient.ListSummariesWithRuntime(ctx, projectID)
		}
		summariesOnly = true
		latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.issue_store_list_summaries_with_runtime", queryStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "include_dependencies", includeDependencies)
	} else {
		tasks, err = issueClient.SearchWithRuntime(ctx, projectID, query)
	}
	if err != nil {
		return taskListSnapshotLoadResult{}, err
	}
	if query != "" {
		latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.issue_store_search_with_runtime", queryStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(tasks))
	}
	tasks = d.enrichTasksWithSessionState(ctx, projectID, tasks)
	freshnessStartedAt := time.Now()
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.list.snapshot_freshness", freshnessStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "freshness", freshness)
	revision := d.currentRevision(projectID)
	if query == "" && !includeDependencies {
		d.storeTaskListSnapshotCacheWithRuntimeAt(projectID, revision, lastCheckedAt, freshness, runtimeAt, tasks, summariesOnly)
	}
	return taskListSnapshotLoadResult{
		Revision:      revision,
		LastCheckedAt: lastCheckedAt,
		Freshness:     freshness,
		RuntimeAt:     runtimeAt,
		SummariesOnly: summariesOnly,
		Tasks:         tasks,
	}, nil
}

func (d *Daemon) refreshTaskListSessionRuntimeState(ctx context.Context, projectID string) (time.Time, bool, error) {
	if d == nil || d.tmux == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil {
		return time.Time{}, false, nil
	}
	projectID = d.canonicalProjectID(projectID)
	now := timeNow().UTC()

	d.taskListRuntimeRefreshMu.Lock()
	if d.taskListRuntimeLastRefresh == nil {
		d.taskListRuntimeLastRefresh = map[string]time.Time{}
	}
	if d.taskListRuntimeRefreshes == nil {
		d.taskListRuntimeRefreshes = map[string]*taskListRuntimeRefresh{}
	}
	lastRefresh := d.taskListRuntimeLastRefresh[projectID]
	if !lastRefresh.IsZero() && now.Sub(lastRefresh) < taskListRuntimeRefreshTTL {
		d.taskListRuntimeRefreshMu.Unlock()
		return lastRefresh, false, nil
	}
	if refresh := d.taskListRuntimeRefreshes[projectID]; refresh != nil {
		d.taskListRuntimeRefreshMu.Unlock()
		select {
		case <-ctx.Done():
			return time.Time{}, false, ctx.Err()
		case <-refresh.done:
			return refresh.runtimeAt, false, refresh.err
		}
	}
	refresh := &taskListRuntimeRefresh{done: make(chan struct{})}
	d.taskListRuntimeRefreshes[projectID] = refresh
	d.taskListRuntimeRefreshMu.Unlock()

	refresh.runtimeAt = now
	refresh.err = d.refreshExistingSessionRuntimeState(ctx, projectID)

	d.taskListRuntimeRefreshMu.Lock()
	d.taskListRuntimeLastRefresh[projectID] = now
	delete(d.taskListRuntimeRefreshes, projectID)
	close(refresh.done)
	d.taskListRuntimeRefreshMu.Unlock()

	if refresh.err != nil {
		return now, true, refresh.err
	}
	return now, true, nil
}

func cloneTaskListSnapshotLoadResult(result taskListSnapshotLoadResult) taskListSnapshotLoadResult {
	result.Tasks = cloneTasks(result.Tasks)
	return result
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
			hydrated, hydrateErr := d.hydrateTaskListSnapshotCache(ctx, projectID, cached.Tasks)
			if hydrateErr != nil {
				latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.snapshot_cache_hydrate", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "cache_hit", false, "error", hydrateErr)
			} else {
				cached.Tasks = hydrated
				cached.LastCheckedAt, cached.Freshness = d.taskListSnapshotFreshness(ctx, projectID)
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
	}
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "cache_hit", false)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task get requested", "project_id", projectID, "task_id", taskID)
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
	contextTaskIDs := taskIDsFromTasks(tasks)
	refreshSessionStartedAt := time.Now()
	if err := d.refreshIssueSessionRuntimeState(ctx, projectID, contextTaskIDs); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task get session runtime refresh failed", "project_id", projectID, "task_id", taskID, "context_task_count", len(contextTaskIDs), "error", err)
	}
	latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.issue_session_refresh", refreshSessionStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "context_task_count", len(contextTaskIDs))
	if len(contextTaskIDs) > 0 {
		queryStartedAt = time.Now()
		tasks, err = issueClient.GetWithDependencyContextRuntime(ctx, projectID, taskID)
		latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get.issue_store_get_dependency_context_runtime_after_session_refresh", queryStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon task get reload after session refresh failed", "project_id", projectID, "task_id", taskID, "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
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
		TaskIDs           []string `json:"task_ids"`
		IncludeAncestors  bool     `json:"include_ancestors,omitempty"`
		ExcludeDependents bool     `json:"exclude_dependents,omitempty"`
		MetadataOnly      bool     `json:"metadata_only,omitempty"`
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
	if !cmd.MetadataOnly {
		for _, taskID := range taskIDs {
			d.refreshIssueWorktreeState(ctx, projectID, taskID)
		}
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	contextOptions := []issues.DependencyContextOption(nil)
	if cmd.IncludeAncestors {
		contextOptions = append(contextOptions, issues.WithAncestorContext())
	}
	if cmd.ExcludeDependents {
		contextOptions = append(contextOptions, issues.WithoutDependentContext())
	}
	var (
		tasks []domain.Task
		err   error
	)
	if cmd.MetadataOnly {
		if cmd.IncludeAncestors {
			tasks, err = issueClient.GetManyMetadataWithAncestorContextRuntime(ctx, projectID, taskIDs)
		} else {
			tasks, err = issueClient.GetManyMetadataWithRuntime(ctx, projectID, taskIDs)
		}
	} else {
		tasks, err = issueClient.GetManyWithDependencyContextRuntime(ctx, projectID, taskIDs, contextOptions...)
	}
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("daemon task get-many failed", "project_id", projectID, "task_count", len(taskIDs), "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
		}
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	contextTaskIDs := taskIDsFromTasks(tasks)
	if !cmd.MetadataOnly {
		refreshSessionStartedAt := time.Now()
		if err := d.refreshIssueSessionRuntimeState(ctx, projectID, contextTaskIDs); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("task get-many session runtime refresh failed", "project_id", projectID, "requested_task_count", len(taskIDs), "context_task_count", len(contextTaskIDs), "error", err)
		}
		latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.get_many.issue_session_refresh", refreshSessionStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "requested_task_count", len(taskIDs), "context_task_count", len(contextTaskIDs))
	}
	if !cmd.MetadataOnly && len(contextTaskIDs) > 0 {
		tasks, err = issueClient.GetManyWithDependencyContextRuntime(ctx, projectID, taskIDs, contextOptions...)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("daemon task get-many reload after session refresh failed", "project_id", projectID, "task_count", len(taskIDs), "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
			}
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
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
		d.cfg.Logger.Info("daemon task get-many completed", "project_id", projectID, "requested_task_count", len(taskIDs), "context_task_count", len(tasks), "metadata_only", cmd.MetadataOnly, "revision", resp.Revision, "elapsed_ms", time.Since(startedAt).Milliseconds())
	}
	return resp, nil
}

func (d *Daemon) handleTaskEvents(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID string   `json:"task_id"`
		Types  []string `json:"event_types,omitempty"`
		Limit  int      `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	taskID := strings.TrimSpace(cmd.TaskID)
	if taskID == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "task id is required"), nil
	}
	eventTypes := make([]domain.IssueObservationEventType, 0, len(cmd.Types))
	for _, rawType := range cmd.Types {
		rawType = strings.TrimSpace(rawType)
		if rawType != "" {
			eventTypes = append(eventTypes, domain.IssueObservationEventType(rawType))
		}
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, taskID, issues.IssueObservationEventListOptions{
		Types: eventTypes,
		Limit: cmd.Limit,
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue not found: %s", taskID)), nil
		}
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(struct {
		Events []domain.IssueObservationEvent `json:"events"`
	}{Events: events})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
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

func taskIDsFromTasks(tasks []domain.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID.String())
	}
	return uniqueTrimmedTaskIDs(ids)
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
	d.storeTaskListSnapshotCacheWithRuntimeAt(projectID, revision, lastCheckedAt, freshness, timeNow(), tasks, summariesOnly)
}

func (d *Daemon) storeTaskListSnapshotCacheWithRuntimeAt(projectID string, revision uint64, lastCheckedAt time.Time, freshness protocol.TaskListFreshness, runtimeAt time.Time, tasks []domain.Task, summariesOnly bool) {
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
	if runtimeAt.IsZero() {
		runtimeAt = now
	}
	d.taskListSnapshotCache[projectID] = taskListSnapshotCacheEntry{
		Revision:      revision,
		LastCheckedAt: lastCheckedAt.UTC(),
		Freshness:     freshness,
		Tasks:         stripRuntimeFromTasks(tasks),
		CachedAt:      now,
		RuntimeAt:     runtimeAt.UTC(),
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
	for i := range tasks {
		out[i] = cloneTask(tasks[i])
	}
	return out
}

func cloneTask(task domain.Task) domain.Task {
	if task.Session != nil {
		session := *task.Session
		task.Session = &session
	}
	task.Dependencies = append([]domain.Dependency(nil), task.Dependencies...)
	task.Implementations = append([]string(nil), task.Implementations...)
	task.Labels = append([]string(nil), task.Labels...)
	task.ConflictFiles = append([]string(nil), task.ConflictFiles...)
	return task
}

func stripRuntimeFromTasks(tasks []domain.Task) []domain.Task {
	out := cloneTasks(tasks)
	for i := range out {
		out[i] = stripRuntimeFromTask(out[i])
	}
	return out
}

func stripRuntimeFromTask(task domain.Task) domain.Task {
	task.Session = nil
	task.HasTmuxSession = false
	task.HasWorktree = false
	task.GitAheadCount = 0
	task.GitBehindCount = 0
	task.HasUncommittedChanges = false
	task.HasConflicts = false
	task.ConflictFiles = nil
	task.GitAdditions = 0
	task.GitDeletions = 0
	task.RuntimeUpdatedAt = time.Time{}
	return task
}

func (d *Daemon) hydrateTaskListSnapshotCache(ctx context.Context, projectID string, tasks []domain.Task) ([]domain.Task, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, errors.New("issue store unavailable")
	}
	hydrated, err := issueClient.HydrateRuntime(ctx, projectID, tasks)
	if err != nil {
		return nil, err
	}
	return d.enrichTasksWithSessionState(ctx, projectID, hydrated), nil
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

func buildBoardSnapshotPayload(projectID string, revision uint64, lastCheckedAt time.Time, freshness protocol.TaskListFreshness, tasks []domain.Task) protocol.BoardSnapshotPayload {
	if lastCheckedAt.IsZero() {
		lastCheckedAt = timeNow()
	}
	if !freshness.Valid() {
		freshness = protocol.TaskListFreshnessFresh
	}
	return protocol.BoardSnapshotPayload{
		SchemaVersion:    protocol.BoardSnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: revision,
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    lastCheckedAt.UTC(),
		Freshness:        freshness,
		Tasks:            protocol.BoardTaskSummariesFromDomain(tasks),
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
	projectID = d.canonicalProjectID(projectID)

	sessionFreshnessSource := d.sourceForTaskInvariant(taskInvariantTaskListFreshness)
	if usesProjectionSource(sessionFreshnessSource) && d.sessionStore != nil {
		if err := d.refreshSessionInvariantCacheIfConfigured(ctx, projectID); err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("refresh session freshness cache failed", "project_id", projectID, "error", err)
			}
		}
	}
	return d.taskListSnapshotLocalProjectionFreshness(ctx, projectID)
}

func (d *Daemon) taskListSnapshotLocalProjectionFreshness(ctx context.Context, projectID string) (time.Time, protocol.TaskListFreshness) {
	lastCheckedAt := time.Time{}
	projectID = d.canonicalProjectID(projectID)

	if d.sessionStore != nil {
		snapshot := d.sessionStore.ReadSnapshot(projectID)
		sessions := make([]daemonstate.Session, 0, len(snapshot.Sessions))
		for _, session := range snapshot.Sessions {
			sessions = append(sessions, session)
		}
		for _, session := range sessions {
			lastCheckedAt = laterTime(lastCheckedAt, session.UpdatedAt)
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
	taskByIssue := d.runtimeWorktreeIssueTaskContext(ctx, projectID, worktreeIssueIDsFromGitWorktrees(worktrees))
	throttle := d.ensureWorktreeGitProbeThrottle()
	trigger := runtimeReconcileRequestFromContext(ctx)
	forceProbe := trigger.Priority >= reconcilePriorityManual && strings.TrimSpace(trigger.Reason) == "manual"
	processedProbes := 0
	skippedProbes := 0
	deferredProbes := 0
	failedProbes := 0
	suppressedProbes := 0
	processedIssueIDs := make([]string, 0, 10)
	failedIssueIDs := make([]string, 0, 10)
	skippedIssueIDs := make([]string, 0, 10)
	deferredIssueIDs := make([]string, 0, 10)
	suppressedIssueIDs := make([]string, 0, 10)
	now := time.Now().UTC()
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		if issueID == "" {
			continue
		}
		if !runtimeWorktreeIssueEligible(issueID, taskByIssue) {
			continue
		}
		worktreePath := strings.TrimSpace(wt.Path)
		if d.suppressProjectedStaleWorktreeGitRefresh(ctx, projectID, issueID, worktreePath, nil) {
			suppressedProbes++
			if len(suppressedIssueIDs) < cap(suppressedIssueIDs) {
				suppressedIssueIDs = append(suppressedIssueIDs, issueID)
			}
			continue
		}
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
					if d.suppressStaleWorktreeGitRefresh(ctx, projectID, issueID, worktreePath, err) {
						suppressedProbes++
						if len(suppressedIssueIDs) < cap(suppressedIssueIDs) {
							suppressedIssueIDs = append(suppressedIssueIDs, issueID)
						}
						continue
					}
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
		if failedProbes > 0 || skippedProbes > 0 || deferredProbes > 0 || suppressedProbes > 0 || trigger.Priority >= reconcilePriorityManual {
			logFn = d.cfg.Logger.Info
		}
		logFn("refresh worktree runtime state completed",
			"project_id", projectID,
			"reason", strings.TrimSpace(trigger.Reason),
			"processed_tasks", processedProbes,
			"skipped_tasks", skippedProbes,
			"deferred_tasks", deferredProbes,
			"failed_tasks", failedProbes,
			"suppressed_stale_tasks", suppressedProbes,
			"sample_processed_issue_ids", strings.Join(processedIssueIDs, ","),
			"sample_skipped_issue_ids", strings.Join(skippedIssueIDs, ","),
			"sample_deferred_issue_ids", strings.Join(deferredIssueIDs, ","),
			"sample_failed_issue_ids", strings.Join(failedIssueIDs, ","),
			"sample_suppressed_stale_issue_ids", strings.Join(suppressedIssueIDs, ","),
			"throttle_processed", counters.Processed,
			"throttle_skipped", counters.Skipped,
			"throttle_deferred", counters.Deferred,
		)
	}
	return len(rows), nil
}

func runtimeWorktreeIssueEligible(issueID string, taskByIssue map[string]domain.Task) bool {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return false
	}
	if len(taskByIssue) == 0 {
		return true
	}
	task, ok := runtimeTaskByIssueID(taskByIssue, issueID)
	if !ok {
		return true
	}
	if task.Status == domain.StatusDone {
		return false
	}
	seen := map[string]struct{}{strings.ToLower(issueID): {}}
	for parentID := domain.TaskParentIssueID(task); parentID != ""; {
		key := strings.ToLower(parentID)
		if _, ok := seen[key]; ok {
			return true
		}
		seen[key] = struct{}{}
		parent, ok := runtimeTaskByIssueID(taskByIssue, parentID)
		if !ok {
			return true
		}
		if parent.Status == domain.StatusDone {
			return false
		}
		parentID = domain.TaskParentIssueID(parent)
	}
	return true
}

func runtimeTaskByIssueID(taskByIssue map[string]domain.Task, issueID string) (domain.Task, bool) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return domain.Task{}, false
	}
	if task, ok := taskByIssue[issueID]; ok {
		return task, true
	}
	for id, task := range taskByIssue {
		if naming.IssueIDsEqual(id, issueID) {
			return task, true
		}
	}
	return domain.Task{}, false
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
	taskByIssue := d.runtimeWorktreeIssueTaskContext(ctx, projectID, issueIDs)

	refreshed := 0
	var errs []error
	now := time.Now().UTC()
	for _, issueID := range issueIDs {
		wt, ok := worktreeByIssue[issueID]
		if !ok || strings.TrimSpace(wt.Path) == "" {
			d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID)
			continue
		}
		if !runtimeWorktreeIssueEligible(issueID, taskByIssue) {
			d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID)
			continue
		}

		worktreePath := strings.TrimSpace(wt.Path)
		if d.suppressProjectedStaleWorktreeGitRefresh(ctx, projectID, issueID, worktreePath, nil) {
			continue
		}
		branch := strings.TrimSpace(wt.Branch)
		d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, issueID, worktreePath, branch)
		refreshed++

		if d.git == nil {
			continue
		}
		issueBaseBranch := d.runtimeDiffBaseBranchForIssue(issueID, baseBranch, taskByIssue, worktreeByIssue)
		status, statusErr := d.git.RuntimeStatus(ctx, worktreePath, issueBaseBranch)
		if statusErr != nil {
			if d.suppressStaleWorktreeGitRefresh(ctx, projectID, issueID, worktreePath, statusErr) {
				refreshed--
				continue
			}
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

	taskByIssue := d.runtimeWorktreeIssueTaskContext(ctx, projectID, []string{projection.IssueID})

	return d.runtimeDiffBaseBranchForIssue(projection.IssueID, baseBranch, taskByIssue, worktreeByIssue)
}

func (d *Daemon) runtimeWorktreeIssueTaskContext(ctx context.Context, projectID string, issueIDs []string) map[string]domain.Task {
	if d == nil {
		return nil
	}
	issueIDs = worktreeIssueIDsFromStrings(issueIDs)
	if len(issueIDs) == 0 {
		return nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil
	}
	tasks, err := issueClient.GetRuntimeWorktreeIssueContext(ctx, projectID, issueIDs)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("runtime worktree issue context failed", "project_id", projectID, "error", err)
		}
		return nil
	}
	taskByIssue := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		issueID := strings.TrimSpace(task.ID.String())
		if issueID == "" {
			continue
		}
		taskByIssue[issueID] = task
	}
	return taskByIssue
}

func worktreeIssueIDsFromStrings(issueIDs []string) []string {
	if len(issueIDs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(issueIDs))
	seen := map[string]struct{}{}
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		key := strings.ToLower(issueID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, issueID)
	}
	return ids
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
		if issues.IsSQLiteBusy(err) {
			return d.errorResponse(req, protocol.ErrorCodeUnavailable, err.Error()), nil
		}
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
		TaskID          string        `json:"task_id"`
		Status          domain.Status `json:"status"`
		CascadeChildren bool          `json:"cascade_children,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task status update requested", "project_id", projectID, "task_id", cmd.TaskID, "status", cmd.Status)
	}
	restoreDeferredWorktree := false
	if cmd.Status != domain.StatusDone {
		restoreDeferredWorktree = d.cancelDeferredTaskWorktreeCleanup(ctx, projectID, cmd.TaskID, "issue status changed before deferred worktree cleanup completed")
	}
	if cmd.Status == domain.StatusInReview {
		if risk := d.taskContextRiskForCloseout(ctx, projectID, cmd.TaskID, d.cfg.RepoDir); risk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*risk) {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("context risk is high for issue %s: record root_cause, invariant, regression_validation, or a structured risk note before marking in_review", cmd.TaskID)), nil
		}
	}
	task, updatedTasks, err := d.updateTaskStatusExcludingClose(ctx, projectID, cmd.TaskID, cmd.Status, cmd.CascadeChildren)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if restoreDeferredWorktree {
		d.restoreDeferredCleanupWorktreeProjection(ctx, projectID, cmd.TaskID)
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	for _, updatedTask := range updatedTasks {
		d.publishTaskEvent(req, protocol.EventTaskUpdated, resp.Revision, taskEventBodyFromTask(projectID, updatedTask))
	}
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
	result.ContextRisk = d.taskContextRiskForCloseout(ctx, projectID, taskID, d.cfg.RepoDir)
	if result.ContextRisk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*result.ContextRisk) {
		return result, fmt.Errorf("context risk is high for issue %s: record root_cause, invariant, regression_validation, or a structured risk note before closeout", taskID)
	}
	recordPhase := func(name string, startedAt time.Time, skipped bool) {
		result.Phases = append(result.Phases, taskClosePhaseTiming{
			Name:      name,
			ElapsedMS: time.Since(startedAt).Milliseconds(),
			Skipped:   skipped,
		})
		latencytrace.LogPhase(d.cfg.Logger, "daemon", "task.close."+name, startedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "skipped", skipped)
	}
	var deferredCleanupPlan deferredTaskWorktreeCleanupPlan

	phaseStartedAt := time.Now()
	autoClosedChildren, err := d.closeCleanDescendantsBeforeParent(ctx, projectID, taskID, cmd, req)
	recordPhase("close_clean_children", phaseStartedAt, !cmd.CloseCleanChildren)
	if err != nil {
		return result, fmt.Errorf("phase close_clean_children for issue %s: %w", taskID, err)
	}
	result.AutoClosedChildren = autoClosedChildren

	phaseStartedAt = time.Now()
	integration, err := d.integrateTaskBeforeClose(ctx, projectID, taskID, cmd.IntegrateBeforeClose)
	recordPhase("integrate_before_close", phaseStartedAt, !cmd.IntegrateBeforeClose)
	if err != nil {
		return result, fmt.Errorf("phase integrate_before_close for issue %s: %w", taskID, err)
	}
	result.IntegrationRequested = integration.Requested
	result.Integrated = integration.Integrated
	result.IntegratedSourceBranch = integration.SourceBranch
	result.IntegratedTargetBranch = integration.TargetBranch

	phaseStartedAt = time.Now()
	guard, err := d.validateTaskClosePreflight(ctx, projectID, taskID, taskClosePreflightOptions{
		AllowTargetSession:  true,
		AllowTargetWorktree: true,
		ForceWorktree:       cmd.ForceWorktree,
		IgnoreAhead:         cmd.IgnoreAhead || integration.Integrated || integration.NoChanges,
		CloseCleanChildren:  cmd.CloseCleanChildren,
	}, req)
	recordPhase("preflight", phaseStartedAt, false)
	if err != nil {
		return result, fmt.Errorf("phase preflight for issue %s: %w", taskID, err)
	}
	deferWorktreeCleanup := daemonShouldDeferWorktreeCleanupForClose(cmd, integration, guard)

	phaseStartedAt = time.Now()
	if !deferWorktreeCleanup {
		if err := d.cleanupTaskIssueResourcesForClose(ctx, projectID, taskID, guard.Worktree); err != nil {
			recordPhase("issue_resource_cleanup", phaseStartedAt, false)
			return result, taskClosePostIntegrationPhaseError(taskID, "issue_resource_cleanup", integration, err)
		}
	}
	recordPhase("issue_resource_cleanup", phaseStartedAt, deferWorktreeCleanup)

	phaseStartedAt = time.Now()
	if daemonCloseGuardTaskHasSession(guard.Task) {
		if err := d.stopTaskSessionForClose(ctx, req, projectID, taskID); err != nil {
			recordPhase("session_cleanup", phaseStartedAt, false)
			return result, taskClosePostIntegrationPhaseError(taskID, "session_cleanup", integration, err)
		}
		result.SessionStopped = true
	}
	recordPhase("session_cleanup", phaseStartedAt, !daemonCloseGuardTaskHasSession(guard.Task))

	phaseStartedAt = time.Now()
	if daemonCloseGuardTaskHasWorktree(guard.Task) {
		if d.worktreeAdapter == nil {
			recordPhase("worktree_cleanup", phaseStartedAt, false)
			return result, taskClosePostIntegrationPhaseError(taskID, "worktree_cleanup", integration, fmt.Errorf("worktree cleanup unavailable"))
		}
		if deferWorktreeCleanup {
			plan, err := d.deferTaskWorktreeCleanupForClose(ctx, projectID, taskID)
			if err != nil {
				recordPhase("worktree_cleanup", phaseStartedAt, false)
				return result, taskClosePostIntegrationPhaseError(taskID, "worktree_cleanup", integration, err)
			}
			result.WorktreeCleanupDeferred = true
			deferredCleanupPlan = plan
		} else {
			if err := d.worktreeAdapter.Delete(ctx, projectID, taskID, cmd.ForceWorktree); err != nil {
				recordPhase("worktree_cleanup", phaseStartedAt, false)
				return result, taskClosePostIntegrationPhaseError(taskID, "worktree_cleanup", integration, err)
			}
			result.WorktreeRemoved = true
		}
	}
	recordPhase("worktree_cleanup", phaseStartedAt, !daemonCloseGuardTaskHasWorktree(guard.Task))

	phaseStartedAt = time.Now()
	if err := d.repairStaleRuntimeProjections(ctx, projectID, taskID); err != nil {
		recordPhase("runtime_projection_repair", phaseStartedAt, false)
		return result, taskClosePostIntegrationPhaseError(taskID, "runtime_projection_repair", integration, err)
	}
	recordPhase("runtime_projection_repair", phaseStartedAt, false)

	phaseStartedAt = time.Now()
	task, err := issueClient.UpdateWithRuntime(ctx, projectID, taskID, domain.StatusDone)
	if err != nil {
		recordPhase("status_write", phaseStartedAt, false)
		return result, taskClosePostIntegrationPhaseError(taskID, "status_write", integration, err)
	}
	recordPhase("status_write", phaseStartedAt, false)
	rev := d.nextRevision(projectID)
	result.Revision = rev
	if deferWorktreeCleanup {
		operationID, err := d.submitDeferredTaskWorktreeCleanup(ctx, projectID, taskID, deferredCleanupPlan.Path, deferredCleanupPlan.Branch)
		if err != nil {
			return result, fmt.Errorf("phase worktree_cleanup_enqueue for issue %s: %w", taskID, err)
		}
		result.WorktreeCleanupOperationID = operationID
	}
	d.publishTaskEvent(req, protocol.EventTaskUpdated, rev, taskEventBodyFromTask(projectID, task))
	return result, nil
}

func (d *Daemon) repairStaleRuntimeProjections(ctx context.Context, projectID, taskID string) error {
	store := d.runtimeStateStoreForProject(projectID)
	if store == nil {
		return nil
	}
	projectIDs, err := store.ListProjectIDs(ctx)
	if err != nil {
		return err
	}
	liveSessions, sessionsLoaded, err := d.liveTmuxSessionSet(ctx)
	if err != nil {
		return err
	}
	blocked := make([]string, 0)
	var liveWorktreePaths map[string]struct{}
	worktreePathsLoaded := false
	worktreePathsLoadAttempted := false
	for _, projectionProjectID := range projectIDs {
		worktrees, err := store.ListWorktreeStates(ctx, projectionProjectID)
		if err != nil {
			return err
		}
		for _, worktree := range worktrees {
			if !naming.IssueIDsEqual(worktree.IssueID, taskID) || strings.TrimSpace(worktree.Path) == "" {
				continue
			}
			stale, needsLivePaths, err := worktreeProjectionStaleState(worktree, liveWorktreePaths, worktreePathsLoaded)
			if err != nil {
				return err
			}
			if needsLivePaths && !worktreePathsLoadAttempted {
				worktreePathsLoadAttempted = true
				loadedPaths, loaded, loadErr := d.liveWorktreePathSet(ctx, projectID)
				if loadErr != nil {
					if d.cfg.Logger != nil {
						d.cfg.Logger.Debug("load live worktree paths for stale projection repair failed", "project_id", projectID, "error", loadErr)
					}
				} else {
					liveWorktreePaths = loadedPaths
					worktreePathsLoaded = loaded
				}
				stale, _, err = worktreeProjectionStaleState(worktree, liveWorktreePaths, worktreePathsLoaded)
				if err != nil {
					return err
				}
			}
			if stale {
				if err := store.DeleteWorktreeState(ctx, projectionProjectID, taskID); err != nil {
					return err
				}
				continue
			}
			blocked = append(blocked, fmt.Sprintf("worktree %s:%s", projectionProjectID, worktree.Path))
		}

		sessions, err := store.ListSessionStates(ctx, projectionProjectID)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if !naming.IssueIDsEqual(session.IssueID, taskID) || closeSessionProjectionStopped(session) {
				continue
			}
			if sessionsLoaded {
				if _, live := liveSessions[session.ID]; !live {
					session.State = daemonstate.SessionStateStopped
					session.ObservedState = daemonstate.SessionStateStopped
					session.TmuxAttachedCount = 0
					session.UpdatedAt = time.Now().UTC()
					if err := store.UpsertSessionState(ctx, projectionProjectID, session); err != nil {
						return err
					}
					continue
				}
			}
			blocked = append(blocked, fmt.Sprintf("session %s:%s", projectionProjectID, session.ID))
		}
	}
	if len(blocked) > 0 {
		return fmt.Errorf("active runtime projection aliases remain: %s", strings.Join(blocked, ", "))
	}
	return nil
}

func daemonShouldDeferWorktreeCleanupForClose(cmd taskCloseRequest, integration taskCloseIntegrationResult, guard taskClosePreflightResult) bool {
	return cmd.ForceWorktree &&
		integration.Requested &&
		integration.NoChanges &&
		daemonCloseGuardTaskHasWorktree(guard.Task) &&
		!daemonCloseGuardTaskHasSession(guard.Task)
}

func taskClosePostIntegrationPhaseError(taskID, phase string, integration taskCloseIntegrationResult, err error) error {
	base := fmt.Errorf("phase %s for issue %s: %w", phase, taskID, err)
	if !integration.Integrated && !(integration.Requested && integration.NoChanges) {
		return base
	}
	source := strings.TrimSpace(integration.SourceBranch)
	target := strings.TrimSpace(integration.TargetBranch)
	switch {
	case source != "" && target != "":
		return fmt.Errorf("%w. Integration already completed: branch %s landed on %s; cleanup/status remains. Next: repair the reported cleanup blocker, then retry close; retry will skip merge when the source is already reachable", base, source, target)
	case source != "":
		return fmt.Errorf("%w. Integration already completed: branch %s landed; cleanup/status remains. Next: repair the reported cleanup blocker, then retry close; retry will skip merge when the source is already reachable", base, source)
	default:
		return fmt.Errorf("%w. Integration already completed; cleanup/status remains. Next: repair the reported cleanup blocker, then retry close; retry will skip merge when the source is already reachable", base)
	}
}

func (d *Daemon) deferTaskWorktreeCleanupForClose(ctx context.Context, projectID, taskID string) (deferredTaskWorktreeCleanupPlan, error) {
	if d.worktreeAdapter == nil {
		return deferredTaskWorktreeCleanupPlan{}, fmt.Errorf("worktree cleanup unavailable")
	}
	var fallbackBranch, fallbackPath string
	if projected, found, err := d.worktreeAdapter.projectedWorktreeForIssue(ctx, projectID, taskID); err != nil {
		return deferredTaskWorktreeCleanupPlan{}, fmt.Errorf("read worktree projection before deferred cleanup: %w", err)
	} else if found {
		fallbackBranch = strings.TrimSpace(projected.Branch)
		fallbackPath = strings.TrimSpace(projected.Path)
	}
	if store := d.worktreeRuntimeStateStore(projectID); store != nil {
		if err := store.DeleteWorktreeState(ctx, projectID, taskID); err != nil {
			return deferredTaskWorktreeCleanupPlan{}, fmt.Errorf("clear worktree projection before close: %w", err)
		}
	}
	d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, projectID, taskID)
	return deferredTaskWorktreeCleanupPlan{Path: fallbackPath, Branch: fallbackBranch}, nil
}

func (d *Daemon) submitDeferredTaskWorktreeCleanup(ctx context.Context, projectID, taskID, fallbackPath, fallbackBranch string) (string, error) {
	if d.operationRuntime == nil || d.operationRuntime.manager == nil {
		return "", fmt.Errorf("operation runtime unavailable")
	}
	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return "", fmt.Errorf("worktree manager unavailable")
	}
	projectID = normalizedProjectID(projectID)
	taskID = strings.TrimSpace(taskID)
	fallbackPath = strings.TrimSpace(fallbackPath)
	fallbackBranch = strings.TrimSpace(fallbackBranch)
	resourceKeys := []string{"issue:" + projectID + ":" + taskID}
	if fallbackPath != "" {
		resourceKeys = append(resourceKeys, "worktree:"+filepath.Clean(fallbackPath))
	}
	if fallbackBranch != "" {
		resourceKeys = append(resourceKeys, "branch:"+fallbackBranch)
	}
	submitResult, err := d.operationRuntime.manager.Submit(ctx, daemonops.SubmitRequest{
		ProjectID:    projectID,
		IssueID:      taskID,
		Kind:         taskDeferredWorktreeCleanupOperationKind,
		DedupeKey:    taskDeferredWorktreeCleanupOperationKind + ":" + taskID,
		ResourceKeys: resourceKeys,
	}, func(runCtx context.Context) ([]byte, error) {
		runCtx, cancel := context.WithTimeout(runCtx, taskDeferredWorktreeCleanupTimeout)
		defer cancel()
		if d.deferredTaskWorktreeCleanupShouldSkip(runCtx, projectID, taskID, fallbackPath, fallbackBranch) {
			return json.Marshal(deferredTaskWorktreeCleanupResult{
				ProjectID: projectID,
				TaskID:    taskID,
				Skipped:   true,
				Reason:    "issue no longer closed",
			})
		}
		removedWorktree, err := manager.DeleteWithOptions(runCtx, taskID, git.WorktreeDeleteOptions{
			Force:          true,
			BranchCleanup:  git.WorktreeBranchCleanupRequired,
			FallbackBranch: fallbackBranch,
		})
		if err != nil {
			if errors.Is(err, git.ErrWorktreeNotFound) {
				return json.Marshal(deferredTaskWorktreeCleanupResult{
					ProjectID: projectID,
					TaskID:    taskID,
				})
			}
			return nil, err
		}
		if removedWorktree != nil {
			_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(removedWorktree.Path))
		}
		return json.Marshal(deferredTaskWorktreeCleanupResult{
			ProjectID: projectID,
			TaskID:    taskID,
		})
	})
	if err != nil {
		return "", err
	}
	return submitResult.Record.ID, nil
}

func (d *Daemon) deferredTaskWorktreeCleanupShouldSkip(ctx context.Context, projectID, taskID, fallbackPath, fallbackBranch string) bool {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return false
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil || task.Status == domain.StatusDone {
		return false
	}
	if fallbackPath != "" {
		d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, taskID, fallbackPath, fallbackBranch)
	} else {
		d.restoreDeferredCleanupWorktreeProjection(ctx, projectID, taskID)
	}
	return true
}

func (d *Daemon) cancelDeferredTaskWorktreeCleanup(ctx context.Context, projectID, taskID, reason string) bool {
	if d == nil || d.operationRuntime == nil || d.operationRuntime.manager == nil {
		return false
	}
	projectID = normalizedProjectID(projectID)
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false
	}
	records, err := d.operationRuntime.manager.List(ctx, daemonops.Query{
		ProjectID: projectID,
		IssueID:   taskID,
		Kind:      taskDeferredWorktreeCleanupOperationKind,
		States:    []daemonops.State{daemonops.StateQueued, daemonops.StateRunning},
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("deferred worktree cleanup cancellation lookup failed", "project_id", projectID, "task_id", taskID, "error", err)
		}
		return false
	}
	cancelled := false
	for _, record := range records {
		if _, err := d.operationRuntime.manager.Cancel(ctx, record.ID, reason); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("deferred worktree cleanup cancellation failed", "project_id", projectID, "task_id", taskID, "operation_id", record.ID, "error", err)
			continue
		}
		cancelled = true
	}
	return cancelled
}

func (d *Daemon) restoreDeferredCleanupWorktreeProjection(ctx context.Context, projectID, taskID string) {
	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return
	}
	worktree, err := manager.Get(ctx, taskID)
	if err != nil || worktree == nil {
		return
	}
	d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, taskID, worktree.Path, worktree.Branch)
}

func (d *Daemon) liveWorktreePathSet(ctx context.Context, projectID string) (map[string]struct{}, bool, error) {
	manager := d.cachedWorktreeManagerForProject(projectID)
	if manager == nil {
		return nil, false, nil
	}
	paths, err := manager.ListPaths(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make(map[string]struct{}, len(paths))
	for _, worktreePath := range paths {
		path := normalizeWorktreeProjectionPath(worktreePath)
		if path != "" {
			out[path] = struct{}{}
		}
	}
	return out, true, nil
}

func (d *Daemon) cachedWorktreeManagerForProject(projectID string) *git.WorktreeManager {
	if d == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	d.worktreeManagersMu.Lock()
	defer d.worktreeManagersMu.Unlock()
	if manager, ok := d.worktreeManagersByProject[projectID]; ok && manager != nil {
		return manager
	}
	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectLocked(projectID))
	if repoDir == "" {
		return nil
	}
	if manager, ok := d.worktreeManagersByRoot[repoDir]; ok && manager != nil {
		if d.worktreeManagersByProject == nil {
			d.worktreeManagersByProject = make(map[string]*git.WorktreeManager)
		}
		d.worktreeManagersByProject[projectID] = manager
		return manager
	}
	return nil
}

func worktreeProjectionStaleState(worktree daemonstate.WorktreeState, liveWorktreePaths map[string]struct{}, liveWorktreePathsLoaded bool) (stale bool, needsLivePaths bool, err error) {
	path := strings.TrimSpace(worktree.Path)
	if path == "" {
		return true, false, nil
	}
	if _, err := os.Stat(path); err == nil {
		if liveWorktreePathsLoaded {
			_, live := liveWorktreePaths[normalizeWorktreeProjectionPath(path)]
			return !live, false, nil
		}
		return false, true, nil
	} else if os.IsNotExist(err) {
		return true, false, nil
	} else {
		return false, false, fmt.Errorf("inspect worktree projection %s/%s: %w", worktree.ProjectID, path, err)
	}
}

func normalizeWorktreeProjectionPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func closeSessionProjectionStopped(session daemonstate.Session) bool {
	state := daemonstate.NormalizeSessionState(session.State)
	observed := daemonstate.NormalizeSessionState(session.ObservedState)
	return state == daemonstate.SessionStateStopped && (observed == "" || observed == daemonstate.SessionStateStopped)
}

func (d *Daemon) liveTmuxSessionSet(ctx context.Context) (map[string]struct{}, bool, error) {
	if d.tmux == nil {
		return nil, false, nil
	}
	sessions, err := d.tmux.ListSessions(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		session = strings.TrimSpace(session)
		if session != "" {
			out[session] = struct{}{}
		}
	}
	return out, true, nil
}

type taskCloseIntegrationResult struct {
	Requested    bool
	Integrated   bool
	NoChanges    bool
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
	if source, ok := daemonWorktreeForIssue(worktrees, taskID); !ok || strings.TrimSpace(source.Branch) == "" {
		d.worktreeAdapter.pollAndPersistWorktrees(ctx, projectID)
		worktrees, err = d.worktreeAdapter.List(ctx, projectID)
		if err != nil {
			return taskCloseIntegrationResult{}, fmt.Errorf("refresh worktrees before close integration: %w", err)
		}
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
		targetWorktree = strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
		if targetWorktree == "" {
			targetWorktree = strings.TrimSpace(d.resolveRepoDirForProject(projectID))
		}
		if targetWorktree == "" {
			targetWorktree = "."
		}
	}

	var integration taskCloseIntegrationResult
	sourceUniqueCommits, err := d.git.RevListCount(ctx, targetWorktree, targetBranch+".."+source.Branch)
	if err == nil && sourceUniqueCommits == 0 {
		return taskCloseIntegrationResult{
			Requested:    true,
			NoChanges:    true,
			SourceBranch: source.Branch,
			TargetBranch: targetBranch,
		}, nil
	}
	if err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("target-side source containment check failed before close integration", "project_id", projectID, "task_id", taskID, "target_worktree", targetWorktree, "target_branch", targetBranch, "source_branch", source.Branch, "error", err)
	}
	hasChangesToIntegrate, err := d.ensureMergeToBaseClean(ctx, source, targetWorktree, targetBranch)
	if err != nil {
		return taskCloseIntegrationResult{Requested: true}, err
	}
	if !hasChangesToIntegrate {
		return taskCloseIntegrationResult{
			Requested:    true,
			NoChanges:    true,
			SourceBranch: source.Branch,
			TargetBranch: targetBranch,
		}, nil
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
	if !branchAttached {
		if err := d.git.WithWorktreeLock(ctx, targetWorktree, func(ctx context.Context) error {
			return d.git.Checkout(ctx, targetWorktree, targetBranch)
		}); err != nil {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("checkout target branch before close integration: %w", err)
		}
	}
	merge, err := d.mergeTaskBranchBeforeClose(ctx, projectID, taskID, targetWorktree, targetBranch, source.Branch)
	if err != nil {
		return taskCloseIntegrationResult{Requested: true}, err
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
	integration = taskCloseIntegrationResult{
		Requested:    true,
		Integrated:   true,
		SourceBranch: source.Branch,
		TargetBranch: targetBranch,
	}
	return integration, nil
}

func (d *Daemon) mergeTaskBranchBeforeClose(ctx context.Context, projectID, taskID, targetWorktree, targetBranch, sourceBranch string) (*git.MergeResult, error) {
	for attempt := 1; ; attempt++ {
		result, err := d.git.MergeCleanlyTransactional(ctx, targetWorktree, sourceBranch)
		if err != nil {
			return nil, fmt.Errorf("merge %s into %s: %w", sourceBranch, targetBranch, err)
		}
		if result == nil {
			return nil, fmt.Errorf("merge %s into %s returned no result", sourceBranch, targetBranch)
		}
		if result.Success || !git.IsTransactionalMergeStaleTarget(result) {
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("merge %s into %s retry stopped after target HEAD moved during scratch validation: %w", sourceBranch, targetBranch, err)
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("retrying close integration after target HEAD moved during scratch validation",
				"project_id", projectID,
				"task_id", taskID,
				"target_worktree", targetWorktree,
				"target_branch", targetBranch,
				"source_branch", sourceBranch,
				"attempt", attempt,
			)
		}
	}
}

func daemonWorktreeForIssue(worktrees []git.Worktree, issueID string) (git.Worktree, bool) {
	for _, wt := range worktrees {
		if naming.IssueIDsEqual(wt.IssueID, issueID) {
			return wt, true
		}
	}
	return git.Worktree{}, false
}

func (d *Daemon) ensureMergeToBaseClean(ctx context.Context, source git.Worktree, targetWorktree, targetBranch string) (bool, error) {
	sourceStatus, err := d.git.Status(ctx, source.Path)
	if err != nil {
		return false, fmt.Errorf("read source status for %s: %w", source.IssueID, err)
	}
	targetStatus, err := d.git.Status(ctx, targetWorktree)
	if err != nil {
		return false, fmt.Errorf("read target branch status: %w", err)
	}
	changedFiles, err := d.git.ChangedFilesLocalBase(ctx, source.Path, targetBranch)
	if err != nil {
		return false, fmt.Errorf("inspect source branch changes for %s against %s: %w", source.IssueID, targetBranch, err)
	}
	hasChangesToIntegrate := len(changedFiles) > 0

	reasons := make([]string, 0, 2)
	if gitStatusHasDirtyFiles(sourceStatus) {
		reasons = append(reasons, fmt.Sprintf("source %s is not clean: %s", source.IssueID, gitStatusSummary(sourceStatus)))
	}
	if gitStatusHasDirtyFiles(targetStatus) && hasChangesToIntegrate {
		reasons = append(reasons, fmt.Sprintf("target branch is not clean: %s", gitStatusSummary(targetStatus)))
	}
	if len(reasons) > 0 {
		return false, errors.New(strings.Join(reasons, "; "))
	}
	return hasChangesToIntegrate, nil
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
	resp, err := d.handleSessionStopDirectWithOptions(ctx, stopReq, sessionStopOptions{
		skipIssueResourceCleanup: true,
	})
	return cleanupCommandError(resp, err)
}

func (d *Daemon) cleanupTaskIssueResourcesForClose(ctx context.Context, projectID, taskID, worktreePath string) error {
	resources := d.runtimeConfigForProject(projectID).IssueResources
	if len(resources.CleanupCommands) == 0 && strings.TrimSpace(resources.ReconcileCommand) == "" {
		return nil
	}
	lookupPath, branch := d.issueWorktreeContext(ctx, projectID, taskID)
	if strings.TrimSpace(worktreePath) == "" {
		worktreePath = lookupPath
	}
	resourceCtx := d.issueResourceLifecycleContext(projectID, taskID, naming.CanonicalSessionID(projectID, taskID), worktreePath, branch)
	if _, err := d.runIssueResourceReconcileCommand(ctx, projectID, resourceCtx, "absent"); err != nil {
		return err
	}
	_, err := d.runIssueResourceCleanupCommands(ctx, projectID, resourceCtx)
	return err
}

func (d *Daemon) updateTaskStatusExcludingClose(ctx context.Context, projectID, taskID string, status domain.Status, cascadeChildren bool) (domain.Task, []domain.Task, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.Task{}, nil, fmt.Errorf("issue store unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	if status == domain.StatusDone {
		return domain.Task{}, nil, fmt.Errorf("status %s must be applied with task.close", status)
	}
	if cascadeChildren && status != domain.StatusInReview {
		return domain.Task{}, nil, fmt.Errorf("cascade_children is only supported with status %s", domain.StatusInReview)
	}
	var cascaded []domain.Task
	if status == domain.StatusInReview {
		updated, err := d.validateOrCascadeChildrenForReview(ctx, projectID, taskID, cascadeChildren)
		if err != nil {
			return domain.Task{}, nil, err
		}
		cascaded = updated
	}
	task, err := issueClient.UpdateWithRuntime(ctx, projectID, taskID, status)
	if err != nil {
		return domain.Task{}, nil, err
	}
	return task, cascaded, nil
}

func (d *Daemon) validateOrCascadeChildrenForReview(ctx context.Context, projectID, taskID string, cascadeChildren bool) ([]domain.Task, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	tasks, err := d.loadTaskClosePreflightDomainTasks(ctx, projectID, taskID)
	if err != nil {
		return nil, fmt.Errorf("inspect child issues before moving %s to in_review: %w", taskID, err)
	}
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
		return nil, fmt.Errorf("issue not found: %s", taskID)
	}
	blocked := daemonReviewGuardChildBlockers(task.ID, tasks)
	if len(blocked) == 0 {
		return nil, nil
	}
	if !cascadeChildren {
		return nil, fmt.Errorf("cannot move issue %s to in_review: child issues are not review-ready: %s. Next: move or finish the listed child issues first, or retry with --cascade-children to move them to in_review first", taskID, strings.Join(blocked, "; "))
	}
	updated := make([]domain.Task, 0)
	for _, childID := range daemonReviewGuardChildIDsToCascade(task.ID, tasks) {
		child, err := issueClient.UpdateWithRuntime(ctx, projectID, childID.String(), domain.StatusInReview)
		if err != nil {
			return nil, fmt.Errorf("cascade child %s to in_review before moving %s: %w", childID.String(), taskID, err)
		}
		updated = append(updated, child)
	}
	return updated, nil
}

func daemonReviewGuardChildBlockers(parentID naming.IssueID, tasks []domain.Task) []string {
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
		if !ok || child.Status == domain.StatusInReview || child.Status == domain.StatusDone {
			continue
		}
		blocked = append(blocked, fmt.Sprintf("%s (%s)", child.ID.String(), child.Status))
	}
	return blocked
}

func daemonReviewGuardChildIDsToCascade(parentID naming.IssueID, tasks []domain.Task) []naming.IssueID {
	childrenByParent := daemonCloseGuardChildrenByParent(tasks)
	descendants := daemonCloseGuardDescendants(parentID, childrenByParent)
	if len(descendants) == 0 {
		return nil
	}
	byID := make(map[naming.IssueID]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}
	out := make([]naming.IssueID, 0, len(descendants))
	for _, childID := range descendants {
		child, ok := byID[childID]
		if !ok || child.Status == domain.StatusInReview || child.Status == domain.StatusDone {
			continue
		}
		out = append(out, childID)
	}
	return out
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
	tasks, err := d.loadTaskClosePreflightDomainTasks(ctx, projectID, taskID)
	if err != nil {
		return taskClosePreflightResult{}, fmt.Errorf("inspect runtime attachments before closing %s: %w", taskID, err)
	}

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
	reasons = append(reasons, daemonCloseGuardChildBlockers(task.ID, tasks, opts)...)
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

func (d *Daemon) closeCleanDescendantsBeforeParent(ctx context.Context, projectID, taskID string, cmd taskCloseRequest, req protocol.RequestEnvelope) ([]string, error) {
	if !cmd.CloseCleanChildren {
		return nil, nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	tasks, err := d.loadTaskClosePreflightDomainTasks(ctx, projectID, taskID)
	if err != nil {
		return nil, fmt.Errorf("inspect child issues before closing %s: %w", taskID, err)
	}
	childrenByParent := daemonCloseGuardChildrenByParent(tasks)
	root, err := naming.ParseIssueID(taskID)
	if err != nil {
		return nil, fmt.Errorf("invalid task id: %w", err)
	}
	descendants := daemonCloseGuardDescendants(root, childrenByParent)
	if len(descendants) == 0 {
		return nil, nil
	}
	byID := make(map[naming.IssueID]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}
	closed := make([]string, 0, len(descendants))
	for i := len(descendants) - 1; i >= 0; i-- {
		childID := descendants[i]
		child, ok := byID[childID]
		if !ok || child.Status == domain.StatusDone {
			continue
		}
		if !daemonCloseGuardCleanChildAutoCloseEligible(child) {
			continue
		}
		childResult, err := d.closeTask(ctx, projectID, taskCloseRequest{
			TaskID:               child.ID.String(),
			ForceWorktree:        false,
			IgnoreAhead:          false,
			IntegrateBeforeClose: false,
			CloseCleanChildren:   true,
		}, req)
		if err != nil {
			return closed, fmt.Errorf("close clean child %s: %w", child.ID.String(), err)
		}
		closed = append(closed, child.ID.String())
		closed = append(closed, childResult.AutoClosedChildren...)
	}
	return closed, nil
}

func (d *Daemon) loadTaskClosePreflightDomainTasks(ctx context.Context, projectID, taskID string) ([]domain.Task, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	tasks, err := issueClient.ListParentChildSubtreeWithRuntime(ctx, projectID, taskID)
	if err != nil {
		return nil, err
	}
	return d.enrichTasksWithSessionState(ctx, projectID, tasks), nil
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

func daemonCloseGuardChildBlockers(parentID naming.IssueID, tasks []domain.Task, opts taskClosePreflightOptions) []string {
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
		if opts.CloseCleanChildren && daemonCloseGuardCleanChildAutoCloseEligible(child) {
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

func daemonCloseGuardCleanChildAutoCloseEligible(task domain.Task) bool {
	if task.Status == domain.StatusDone {
		return true
	}
	if daemonCloseGuardTaskHasSession(task) {
		return false
	}
	if task.HasUncommittedChanges || task.HasConflicts {
		return false
	}
	if task.GitAheadCount > 0 || task.GitAdditions > 0 || task.GitDeletions > 0 {
		return false
	}
	return true
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
	contextRisk := d.taskContextRiskForCloseout(ctx, projectID, task.ID.String(), repoDir)
	parentIssueID := task.ID.String()
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		parentIssueID = strings.TrimSpace(task.ParentID.String())
	}
	if task.Status == domain.StatusDone {
		return taskIntegrationReadinessResult{
			IssueID:       task.ID.String(),
			ParentIssueID: parentIssueID,
			Ready:         true,
			ContextRisk:   contextRisk,
		}, nil
	}

	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return taskIntegrationReadinessResult{
			IssueID:       task.ID.String(),
			ParentIssueID: parentIssueID,
			Ready:         false,
			ContextRisk:   contextRisk,
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
	for i := len(events) - 1; i >= 0; i-- {
		evt := events[i]
		if naming.IssueIDsEqual(evt.IssueID, task.ID.String()) && daemonWorkerIntegrationReadyMailType(evt.Type) {
			packet, validation := domain.ParseWorkerEvidencePacketBody(evt.Body)
			if validation.Complete {
				return taskIntegrationReadinessResult{
					IssueID:          task.ID.String(),
					ParentIssueID:    parentIssueID,
					Ready:            true,
					ContextRisk:      contextRisk,
					EvidenceEventSeq: evt.Seq,
					EvidencePacket:   &packet,
				}, nil
			}
			reasons := []string{fmt.Sprintf("issue %s is not closed", task.ID.String())}
			if validation.Found {
				reasons = append(reasons, fmt.Sprintf("worker evidence packet in mailbox event seq %d is incomplete", evt.Seq))
			} else {
				reasons = append(reasons, fmt.Sprintf("worker-integration-ready mailbox event seq %d does not contain a structured worker_evidence.v1 packet", evt.Seq))
			}
			reasons = append(reasons, validation.Problems()...)
			return taskIntegrationReadinessResult{
				IssueID:                task.ID.String(),
				ParentIssueID:          parentIssueID,
				Ready:                  false,
				ContextRisk:            contextRisk,
				Reasons:                reasons,
				EvidenceEventSeq:       evt.Seq,
				EvidenceIncomplete:     true,
				EvidenceMissingFields:  validation.Missing,
				EvidenceInvalidReasons: validation.Invalid,
			}, nil
		}
	}
	return taskIntegrationReadinessResult{
		IssueID:       task.ID.String(),
		ParentIssueID: parentIssueID,
		Ready:         false,
		ContextRisk:   contextRisk,
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
		baseBranch = d.baseBranchForProject(projectID)
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
		attachIssueID := domain.TaskParentIssueID(sourceTask)
		if len(defaultTarget.AncestorChain) > 0 && strings.TrimSpace(defaultTarget.AncestorChain[0]) != "" {
			attachIssueID = defaultTarget.AncestorChain[0]
		}
		return taskMergeBaseTargetResult{}, fmt.Errorf("refusing to merge child issue %s directly into base: no active ancestor worktree branch was found; run `az worktree create %s`, then close the child into that target", issueID, attachIssueID)
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
	tasks, err := d.loadTaskGraphReadinessDomainTasks(ctx, projectID, rootIssueID)
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
	staleCloseable := daemonTaskGraphStaleCloseableCandidates(rootID, byID, children)
	staleCloseableSet := make(map[string]struct{}, len(staleCloseable))
	for _, candidate := range staleCloseable {
		staleCloseableSet[candidate.IssueID] = struct{}{}
	}
	openDescendants := make([]string, 0, len(desc))
	activeSessions := make([]string, 0, len(desc))
	for _, id := range desc {
		task := byID[id]
		if task.Status != domain.StatusDone {
			if _, closeable := staleCloseableSet[id.String()]; closeable {
				continue
			}
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
	if len(ready.NestedRoots) > 0 {
		ids := make([]string, 0, len(ready.NestedRoots))
		for _, nested := range ready.NestedRoots {
			ids = append(ids, nested.IssueID)
		}
		reasons = append(reasons, fmt.Sprintf("nested roots require orchestration: %s", strings.Join(ids, ",")))
	}
	if len(openDescendants) > 0 {
		reasons = append(reasons, fmt.Sprintf("required descendants not closed: %s", strings.Join(openDescendants, ",")))
	}
	if len(staleCloseable) > 0 {
		ids := make([]string, 0, len(staleCloseable))
		for _, candidate := range staleCloseable {
			ids = append(ids, candidate.IssueID)
		}
		reasons = append(reasons, fmt.Sprintf("stale-closeable child candidates remain: %s", strings.Join(ids, ",")))
	}
	if len(activeSessions) > 0 {
		reasons = append(reasons, fmt.Sprintf("active child sessions remain: %s", strings.Join(activeSessions, ",")))
	}
	return taskCompleteCheckResult{
		RootIssueID:            rootID.String(),
		Pass:                   len(reasons) == 0,
		Reasons:                reasons,
		Advice:                 daemonTaskCompletionAdvice(rootID.String(), ready.Runnable, ready.NestedRoots, openDescendants, activeSessions, staleCloseable),
		StaleCloseableChildren: staleCloseable,
	}, nil
}

func (d *Daemon) taskGraphReadiness(ctx context.Context, projectID, rootIssueID string) (taskGraphReadinessResult, error) {
	tasks, err := d.loadTaskGraphReadinessDomainTasks(ctx, projectID, rootIssueID)
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
	pendingStarts, err := d.taskGraphPendingSessionStarts(ctx, projectID)
	if err != nil {
		return taskGraphReadinessResult{}, err
	}
	if len(pendingStarts) > 0 {
		ready = daemonTaskGraphApplyPendingStarts(ready, pendingStarts)
	}
	startProgressByIssue := d.sessionStartProgressByIssue(ctx, projectID)
	ready.removeRunnableSessionStarts(startProgressByIssue)
	ready.NestedRoots = d.daemonTaskGraphNestedRoots(ctx, projectID, ready.NestedRoots, byID, startProgressByIssue)
	ready.ActiveSessions = d.daemonTaskGraphActiveSessions(ctx, projectID, ready.Active, byID, startProgressByIssue)
	ready.ActiveSessions = append(ready.ActiveSessions, daemonTaskGraphCleanupPendingSessions(rootID, byID, children)...)
	ready.SessionStartProgress = daemonTaskGraphSessionStartProgressList(rootID, children, startProgressByIssue)
	ready.WorkerObservations = d.daemonTaskGraphWorkerObservations(ctx, projectID, rootID, byID, children, ready)
	return ready, nil
}

func (d *Daemon) loadTaskGraphReadinessDomainTasks(ctx context.Context, projectID, rootIssueID string) ([]domain.Task, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	tasks, err := issueClient.ListGraphReadinessWithRuntime(ctx, projectID, rootIssueID)
	if err != nil {
		return nil, err
	}
	contextTaskIDs := taskIDsFromTasks(tasks)
	canRefreshSessionRuntime := d != nil && d.tmux != nil && d.sessionRuntimeStateStoreIfConfigured(projectID) != nil
	if canRefreshSessionRuntime && len(contextTaskIDs) > 0 {
		if err := d.refreshIssueSessionRuntimeState(ctx, projectID, contextTaskIDs); err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("task graph scoped session runtime refresh failed", "project_id", projectID, "root_issue_id", rootIssueID, "context_task_count", len(contextTaskIDs), "error", err)
			}
		} else {
			tasks, err = issueClient.ListGraphReadinessWithRuntime(ctx, projectID, rootIssueID)
			if err != nil {
				return nil, err
			}
		}
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task graph readiness loaded root-scoped tasks",
			"project_id", projectID,
			"root_issue_id", rootIssueID,
			"task_count", len(tasks),
		)
	}
	return d.enrichTasksWithSessionState(ctx, projectID, tasks), nil
}

func (result *taskGraphReadinessResult) removeRunnableSessionStarts(progressByIssue map[string]taskGraphSessionStartProgress) {
	if result == nil || len(result.Runnable) == 0 || len(progressByIssue) == 0 {
		return
	}
	runnable := result.Runnable[:0]
	for _, issueID := range result.Runnable {
		if _, launching := progressByIssue[sessionKey(issueID)]; launching {
			continue
		}
		runnable = append(runnable, issueID)
	}
	result.Runnable = runnable
}

func (d *Daemon) loadTaskGraphDomainTasks(ctx context.Context, projectID string) ([]domain.Task, error) {
	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("task graph session runtime refresh failed", "project_id", projectID, "error", err)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	tasks, err := issueClient.ListSummariesWithRuntimeDependencies(ctx, projectID)
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
	leafIDs := daemonTaskGraphDirectWorkerLeafIDs(rootID, byID, children)
	leaves := make([]string, 0, len(leafIDs))
	for _, id := range leafIDs {
		task := byID[id]
		if task.Type == domain.TypeEpic {
			continue
		}
		leaves = append(leaves, id.String())
	}
	sort.Strings(leaves)
	result := taskGraphReadinessResult{
		RootIssueID: rootID.String(),
		Runnable:    make([]string, 0, len(leaves)),
		NestedRoots: daemonTaskGraphNestedRootSummaries(rootID, byID, children),
		Active:      make([]string, 0),
		Blocked:     make(map[string]string),
	}
	result.StaleCloseableChildren = daemonTaskGraphStaleCloseableCandidates(rootID, byID, children)
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
		if daemonTaskStaleCloseableCandidate(task) {
			continue
		}
		result.Runnable = append(result.Runnable, idRaw)
	}
	return result, nil
}

func daemonTaskGraphStaleCloseableCandidates(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID) []taskStaleCloseableCandidate {
	out := make([]taskStaleCloseableCandidate, 0)
	for _, id := range daemonTaskGraphDirectWorkerLeafIDs(rootID, byID, children) {
		task := byID[id]
		if !daemonTaskStaleCloseableCandidate(task) {
			continue
		}
		if len(daemonTaskGraphUnresolvedBlockers(task, byID)) > 0 {
			continue
		}
		out = append(out, taskStaleCloseableCandidate{
			IssueID:          task.ID.String(),
			Status:           string(task.Status),
			Evidence:         daemonTaskStaleCloseableEvidence(task),
			SuggestedCommand: fmt.Sprintf("az issue close --id %s --close-clean-children", rootID.String()),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].IssueID < out[j].IssueID
	})
	return out
}

func daemonTaskStaleCloseableCandidate(task domain.Task) bool {
	if task.Status == domain.StatusDone {
		return false
	}
	if !daemonCloseGuardCleanChildAutoCloseEligible(task) {
		return false
	}
	return task.Status == domain.StatusInReview || daemonCloseGuardTaskHasWorktree(task)
}

func daemonTaskStaleCloseableEvidence(task domain.Task) []string {
	evidence := []string{
		"no active session",
		fmt.Sprintf("status=%s", task.Status),
	}
	if daemonCloseGuardTaskHasWorktree(task) {
		evidence = append(evidence, "clean worktree")
		evidence = append(evidence, "branch not ahead")
		if task.GitBehindCount > 0 {
			evidence = append(evidence, fmt.Sprintf("branch behind by %d commit(s)", task.GitBehindCount))
		}
	} else {
		evidence = append(evidence, "no active worktree projection")
	}
	if task.Status == domain.StatusInReview {
		evidence = append(evidence, "in_review status is completion evidence")
	}
	return evidence
}

func (d *Daemon) daemonTaskGraphNestedRoots(
	ctx context.Context,
	projectID string,
	nested []taskGraphNestedRoot,
	byID map[naming.IssueID]domain.Task,
	startProgressByIssue map[string]taskGraphSessionStartProgress,
) []taskGraphNestedRoot {
	if len(nested) == 0 {
		return nil
	}
	activeIDs := make([]string, 0, len(nested))
	for _, item := range nested {
		taskID, parseErr := naming.ParseIssueID(item.IssueID)
		task := byID[taskID]
		if parseErr == nil && (daemonCloseGuardTaskHasSession(task) || hasTaskGraphSessionStartProgress(item.IssueID, startProgressByIssue)) {
			activeIDs = append(activeIDs, item.IssueID)
		}
	}
	activeByIssue := daemonTaskGraphActiveSessionsByIssue(d.daemonTaskGraphActiveSessions(ctx, projectID, activeIDs, byID, startProgressByIssue))
	out := make([]taskGraphNestedRoot, 0, len(nested))
	for _, item := range nested {
		if active := activeByIssue[item.IssueID]; active != nil {
			copyActive := *active
			item.ActiveSession = &copyActive
			item.Advice = fmt.Sprintf("watch nested root orchestrator: az orchestrate status --root %s --json", item.IssueID)
		}
		if strings.TrimSpace(item.Advice) == "" {
			item.Advice = fmt.Sprintf("start nested root orchestrator: az session start %s", item.IssueID)
		}
		out = append(out, item)
	}
	return out
}

func hasTaskGraphSessionStartProgress(issueID string, progressByIssue map[string]taskGraphSessionStartProgress) bool {
	if len(progressByIssue) == 0 {
		return false
	}
	_, ok := progressByIssue[sessionKey(issueID)]
	return ok
}

func (d *Daemon) daemonTaskGraphWorkerObservations(
	ctx context.Context,
	projectID string,
	rootID naming.IssueID,
	byID map[naming.IssueID]domain.Task,
	children map[naming.IssueID][]naming.IssueID,
	ready taskGraphReadinessResult,
) []domain.WorkerObservation {
	leafIDs := daemonTaskGraphDirectWorkerLeafIDs(rootID, byID, children)
	if len(leafIDs) == 0 {
		return nil
	}
	activeByIssue := daemonTaskGraphActiveSessionsByIssue(ready.ActiveSessions)
	pendingByIssue := daemonTaskGraphPendingByIssue(ready.Pending)
	startProgressByIssue := daemonTaskGraphStartProgressByIssue(ready.SessionStartProgress)
	staleByIssue := daemonTaskGraphStaleCloseableByIssue(ready.StaleCloseableChildren)
	runnable := stringSet(ready.Runnable)
	mailByIssue := d.workerObservationMailboxEvents(rootID.String())

	out := make([]domain.WorkerObservation, 0, len(leafIDs))
	for _, id := range leafIDs {
		task := byID[id]
		if task.ID.IsZero() || task.Type == domain.TypeEpic {
			continue
		}
		issueID := task.ID.String()
		issueEvents := d.workerObservationIssueEvents(ctx, projectID, issueID)
		observation := daemonWorkerObservationFromInputs(workerObservationInputs{
			RootIssueID:   rootID.String(),
			Task:          task,
			BlockedReason: ready.Blocked[issueID],
			Runnable:      runnable[issueID],
			Active:        activeByIssue[issueID],
			Pending:       pendingByIssue[issueID],
			StartProgress: startProgressByIssue[issueID],
			Stale:         staleByIssue[issueID],
			IssueEvents:   issueEvents,
			MailEvents:    mailByIssue[issueID],
		})
		out = append(out, observation)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].IssueID < out[j].IssueID
	})
	return out
}

type workerObservationInputs struct {
	RootIssueID   string
	Task          domain.Task
	BlockedReason string
	Runnable      bool
	Active        *taskGraphActiveSession
	Pending       *taskGraphPendingStart
	StartProgress *taskGraphSessionStartProgress
	Stale         *taskStaleCloseableCandidate
	IssueEvents   []domain.IssueObservationEvent
	MailEvents    []daemonMailEvent
}

func daemonWorkerObservationFromInputs(in workerObservationInputs) domain.WorkerObservation {
	issueID := in.Task.ID.String()
	observation := domain.WorkerObservation{
		IssueID:           issueID,
		SourceTruthPolicy: daemonWorkerObservationSourcePolicy(),
		LastEvent:         daemonWorkerObservationLastEvent(in.IssueEvents, in.MailEvents),
		EvidenceSummary:   daemonWorkerObservationEvidenceSummary(in),
		Risks:             daemonWorkerObservationRisks(in),
	}

	switch {
	case in.Task.Status == domain.StatusDone && (daemonCloseGuardTaskHasSession(in.Task) || daemonCloseGuardTaskHasWorktree(in.Task) || in.Active != nil):
		observation.State = domain.WorkerObservationCleanupPending
		observation.Reason = "issue is closed but runtime projection remains"
	case in.Task.Status == domain.StatusDone:
		observation.State = domain.WorkerObservationDone
		observation.Reason = "issue is closed"
	case in.Active != nil && daemonWorkerObservationActiveFailed(*in.Active):
		observation.State = domain.WorkerObservationFailed
		observation.Reason = "active session reports failed runtime state"
	case in.Active != nil && daemonWorkerObservationActiveWaiting(*in.Active):
		observation.State = domain.WorkerObservationWaitingHuman
		observation.Reason = "active session is waiting for human input"
	case in.Active != nil:
		observation.State = domain.WorkerObservationWorking
		observation.Reason = "active worker session is present"
	case in.Task.Status == domain.StatusInReview:
		observation.State = domain.WorkerObservationReviewReady
		observation.Reason = "issue is in_review"
	case strings.TrimSpace(in.BlockedReason) != "":
		observation.State = domain.WorkerObservationBlocked
		observation.Reason = in.BlockedReason
	case in.Stale != nil:
		observation.State = domain.WorkerObservationStale
		observation.Reason = "runtime projection indicates stale closeable work"
	case in.Pending != nil || in.StartProgress != nil:
		observation.State = domain.WorkerObservationWorking
		observation.Reason = "session start operation is queued or running"
	case in.Runnable:
		observation.State = domain.WorkerObservationRunnable
		observation.Reason = "leaf worker has no unresolved blockers or active runtime"
	default:
		observation.State = domain.WorkerObservationStale
		observation.Reason = "issue is not runnable and has no active session projection"
	}
	observation.NextActions = daemonWorkerObservationNextActions(in.RootIssueID, observation, in)
	return observation
}

func daemonWorkerObservationSourcePolicy() domain.WorkerObservationSourcePolicy {
	return domain.WorkerObservationSourcePolicy{
		IssueGraph:       string(daemonInvariantSourceProjection),
		SessionRuntime:   string(daemonInvariantSourceHybrid),
		WorktreeGit:      string(daemonInvariantSourceProjection),
		MailboxEvidence:  string(daemonInvariantSourceProjection),
		ActiveOperations: string(daemonInvariantSourceProjection),
	}
}

func daemonWorkerObservationActiveFailed(active taskGraphActiveSession) bool {
	for _, value := range []string{active.Activity, active.State, active.Status} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "failed", "failure", "error":
			return true
		}
	}
	return false
}

func daemonWorkerObservationActiveWaiting(active taskGraphActiveSession) bool {
	activity := strings.ToLower(strings.TrimSpace(active.Activity))
	return activity == "waiting" || activity == "waiting_human"
}

func daemonWorkerObservationEvidenceSummary(in workerObservationInputs) []string {
	evidence := make([]string, 0, 8)
	evidence = append(evidence, fmt.Sprintf("status=%s", in.Task.Status))
	if in.Active != nil {
		evidence = append(evidence, fmt.Sprintf("session activity=%s source=%s", in.Active.Activity, in.Active.ActivitySource))
		if strings.TrimSpace(in.Active.State) != "" {
			evidence = append(evidence, fmt.Sprintf("session state=%s", in.Active.State))
		}
	}
	if in.Pending != nil {
		evidence = append(evidence, fmt.Sprintf("session start %s operation=%s", in.Pending.OperationState, in.Pending.OperationID))
	}
	if in.StartProgress != nil && strings.TrimSpace(in.StartProgress.Phase) != "" {
		evidence = append(evidence, fmt.Sprintf("session start phase=%s percent=%d", in.StartProgress.Phase, in.StartProgress.Percent))
	}
	if in.Stale != nil {
		evidence = append(evidence, in.Stale.Evidence...)
	}
	if in.Task.HasWorktree {
		evidence = append(evidence, "worktree projection present")
	}
	if in.Task.HasConflicts {
		evidence = append(evidence, fmt.Sprintf("conflicts=%d", len(in.Task.ConflictFiles)))
	}
	if in.Task.HasUncommittedChanges {
		evidence = append(evidence, fmt.Sprintf("git changes +%d -%d", in.Task.GitAdditions, in.Task.GitDeletions))
	}
	if in.Task.GitAheadCount > 0 || in.Task.GitBehindCount > 0 {
		evidence = append(evidence, fmt.Sprintf("git ahead=%d behind=%d", in.Task.GitAheadCount, in.Task.GitBehindCount))
	}
	if evt := latestWorkerMailEvent(in.MailEvents); evt != nil {
		evidence = append(evidence, fmt.Sprintf("mailbox %s: %s", strings.TrimSpace(evt.Type), truncateObservationSummary(evt.Body)))
	}
	if evt := latestIssueObservationEvent(in.IssueEvents); evt != nil {
		evidence = append(evidence, fmt.Sprintf("event %s from %s", evt.Type, strings.TrimSpace(evt.Source)))
	}
	for _, evt := range workerObservationEvidenceEvents(in.IssueEvents) {
		summary := issueObservationEventSummary(evt)
		if summary == "" {
			summary = strings.TrimSpace(evt.Source)
		}
		if summary != "" {
			evidence = append(evidence, fmt.Sprintf("evidence %s: %s", evt.Type, summary))
		} else {
			evidence = append(evidence, fmt.Sprintf("evidence %s", evt.Type))
		}
	}
	return uniqueDaemonTaskAdvice(evidence)
}

func daemonWorkerObservationRisks(in workerObservationInputs) []string {
	risks := make([]string, 0, 4)
	if strings.TrimSpace(in.BlockedReason) != "" {
		risks = append(risks, in.BlockedReason)
	}
	if in.Task.HasConflicts {
		risks = append(risks, "worktree has merge conflicts")
	}
	if in.Task.HasUncommittedChanges {
		risks = append(risks, "worktree has uncommitted changes")
	}
	if in.Task.GitBehindCount > 0 {
		risks = append(risks, fmt.Sprintf("branch is behind by %d commit(s)", in.Task.GitBehindCount))
	}
	if in.Active != nil && strings.TrimSpace(in.Active.Activity) == "unknown" {
		risks = append(risks, "session activity is unknown")
	}
	if in.Stale != nil {
		risks = append(risks, "runtime projection may be stale")
	}
	return uniqueDaemonTaskAdvice(risks)
}

func daemonWorkerObservationNextActions(rootIssueID string, observation domain.WorkerObservation, in workerObservationInputs) []string {
	issueID := observation.IssueID
	switch observation.State {
	case domain.WorkerObservationRunnable:
		return []string{fmt.Sprintf("az orchestrate start --root %s --issue %s --json", rootIssueID, issueID)}
	case domain.WorkerObservationWorking:
		if in.Active != nil && strings.TrimSpace(in.Active.Advice) != "" {
			return []string{in.Active.Advice}
		}
		return []string{fmt.Sprintf("watch worker activity for %s", issueID)}
	case domain.WorkerObservationWaitingHuman:
		return []string{fmt.Sprintf("inspect worker %s and answer the pending prompt", issueID)}
	case domain.WorkerObservationBlocked:
		return []string{fmt.Sprintf("resolve blockers for %s", issueID)}
	case domain.WorkerObservationReviewReady:
		return []string{fmt.Sprintf("validate evidence, then close accepted worker: az issue close --id %s", issueID)}
	case domain.WorkerObservationStale:
		if in.Stale != nil && strings.TrimSpace(in.Stale.SuggestedCommand) != "" {
			return []string{in.Stale.SuggestedCommand}
		}
		return []string{fmt.Sprintf("refresh or repair runtime projection for %s", issueID)}
	case domain.WorkerObservationFailed:
		return []string{fmt.Sprintf("inspect failed worker session for %s", issueID)}
	case domain.WorkerObservationCleanupPending:
		return []string{fmt.Sprintf("cleanup runtime for closed worker: az orchestrate close-session --issue %s", issueID)}
	case domain.WorkerObservationDone:
		return nil
	default:
		return nil
	}
}

func daemonWorkerObservationLastEvent(issueEvents []domain.IssueObservationEvent, mailEvents []daemonMailEvent) *domain.WorkerObservationEventSummary {
	var last *domain.WorkerObservationEventSummary
	if evt := latestIssueObservationEvent(issueEvents); evt != nil {
		last = &domain.WorkerObservationEventSummary{
			Kind:      "issue_event",
			Type:      string(evt.Type),
			At:        evt.ObservedAt,
			Source:    strings.TrimSpace(evt.Source),
			Summary:   issueObservationEventSummary(*evt),
			SessionID: strings.TrimSpace(evt.SessionID),
			Worktree:  strings.TrimSpace(evt.WorktreePath),
		}
	}
	if evt := latestWorkerMailEvent(mailEvents); evt != nil {
		summary := &domain.WorkerObservationEventSummary{
			Kind:    "mailbox",
			Type:    strings.TrimSpace(evt.Type),
			At:      evt.CreatedAt,
			Source:  strings.TrimSpace(evt.From),
			Summary: truncateObservationSummary(evt.Body),
			Seq:     evt.Seq,
		}
		if last == nil || (!summary.At.IsZero() && summary.At.After(last.At)) {
			last = summary
		}
	}
	return last
}

func issueObservationEventSummary(evt domain.IssueObservationEvent) string {
	if len(evt.Payload) == 0 {
		return ""
	}
	for _, key := range []string{"summary", "message", "body", "reason", "status"} {
		if value, ok := evt.Payload[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				return truncateObservationSummary(text)
			}
		}
	}
	return ""
}

func latestIssueObservationEvent(events []domain.IssueObservationEvent) *domain.IssueObservationEvent {
	var latest *domain.IssueObservationEvent
	for i := range events {
		if strings.TrimSpace(string(events[i].Type)) == "" {
			continue
		}
		if latest == nil ||
			(!events[i].ObservedAt.IsZero() && events[i].ObservedAt.After(latest.ObservedAt)) ||
			(events[i].ObservedAt.Equal(latest.ObservedAt) && events[i].ID > latest.ID) {
			latest = &events[i]
		}
	}
	return latest
}

func latestWorkerMailEvent(events []daemonMailEvent) *daemonMailEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if strings.TrimSpace(events[i].Type) == "" {
			continue
		}
		return &events[i]
	}
	return nil
}

func workerObservationEvidenceEvents(events []domain.IssueObservationEvent) []domain.IssueObservationEvent {
	out := make([]domain.IssueObservationEvent, 0, 4)
	for _, evt := range events {
		switch evt.Type {
		case domain.IssueEventEvidenceSubmitted,
			domain.IssueEventValidationPassed,
			domain.IssueEventValidationFailed,
			domain.IssueEventReviewCompleted,
			domain.IssueEventRiskRecorded,
			domain.IssueEventBlockerReported,
			domain.IssueEventHumanInputRequested,
			domain.IssueEventHumanInputProvided:
			out = append(out, evt)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].ObservedAt.After(out[j].ObservedAt)
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func truncateObservationSummary(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const max = 160
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max-1]) + "..."
}

func daemonTaskGraphActiveSessionsByIssue(active []taskGraphActiveSession) map[string]*taskGraphActiveSession {
	out := make(map[string]*taskGraphActiveSession, len(active))
	for i := range active {
		issueID := strings.TrimSpace(active[i].IssueID)
		if issueID == "" {
			continue
		}
		out[issueID] = &active[i]
	}
	return out
}

func daemonTaskGraphPendingByIssue(pending []taskGraphPendingStart) map[string]*taskGraphPendingStart {
	out := make(map[string]*taskGraphPendingStart, len(pending))
	for i := range pending {
		issueID := strings.TrimSpace(pending[i].IssueID)
		if issueID == "" {
			continue
		}
		out[issueID] = &pending[i]
	}
	return out
}

func daemonTaskGraphStartProgressByIssue(progress []taskGraphSessionStartProgress) map[string]*taskGraphSessionStartProgress {
	out := make(map[string]*taskGraphSessionStartProgress, len(progress))
	for i := range progress {
		issueID := strings.TrimSpace(progress[i].IssueID)
		if issueID == "" {
			continue
		}
		out[issueID] = &progress[i]
	}
	return out
}

func daemonTaskGraphStaleCloseableByIssue(stale []taskStaleCloseableCandidate) map[string]*taskStaleCloseableCandidate {
	out := make(map[string]*taskStaleCloseableCandidate, len(stale))
	for i := range stale {
		issueID := strings.TrimSpace(stale[i].IssueID)
		if issueID == "" {
			continue
		}
		out[issueID] = &stale[i]
	}
	return out
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func (d *Daemon) workerObservationMailboxEvents(rootIssueID string) map[string][]daemonMailEvent {
	if d == nil || strings.TrimSpace(d.cfg.RepoDir) == "" || strings.TrimSpace(rootIssueID) == "" {
		return nil
	}
	events, err := readMailboxEvents(d.cfg.RepoDir, rootIssueID)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("worker observation mailbox read failed", "root_issue_id", rootIssueID, "error", err)
		}
		return nil
	}
	out := make(map[string][]daemonMailEvent)
	for _, evt := range events {
		issueID := strings.TrimSpace(evt.IssueID)
		if issueID == "" {
			continue
		}
		out[issueID] = append(out[issueID], evt)
	}
	return out
}

func (d *Daemon) workerObservationIssueEvents(ctx context.Context, projectID, issueID string) []domain.IssueObservationEvent {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{
		Limit:       50,
		NewestFirst: true,
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("worker observation issue events read failed", "project_id", projectID, "issue_id", issueID, "error", err)
		}
		return nil
	}
	return events
}

func (d *Daemon) taskGraphPendingSessionStarts(ctx context.Context, projectID string) (map[string]taskGraphPendingStart, error) {
	if d == nil || d.operationRuntime == nil || d.operationRuntime.manager == nil {
		return nil, nil
	}
	projectID = d.canonicalProjectID(projectID)
	records, err := d.operationRuntime.manager.List(ctx, daemonops.Query{
		ProjectID: projectID,
		Kind:      daemonhandlers.CommandSessionStart,
		States:    []daemonops.State{daemonops.StateQueued, daemonops.StateRunning},
	})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	out := make(map[string]taskGraphPendingStart, len(records))
	newest := make(map[string]time.Time, len(records))
	for _, record := range records {
		issueID := strings.TrimSpace(record.IssueID)
		if issueID == "" {
			continue
		}
		if latest, ok := newest[issueID]; ok && record.UpdatedAt.Before(latest) {
			continue
		}
		newest[issueID] = record.UpdatedAt
		out[issueID] = taskGraphPendingStart{
			IssueID:        issueID,
			OperationID:    strings.TrimSpace(record.ID),
			OperationState: string(record.State),
		}
	}
	return out, nil
}

func daemonTaskGraphApplyPendingStarts(result taskGraphReadinessResult, pending map[string]taskGraphPendingStart) taskGraphReadinessResult {
	if len(pending) == 0 {
		return result
	}
	runnable := make([]string, 0, len(result.Runnable))
	for _, issueID := range result.Runnable {
		if start, ok := pending[issueID]; ok {
			result.Pending = append(result.Pending, start)
			continue
		}
		runnable = append(runnable, issueID)
	}
	result.Runnable = runnable
	sort.Slice(result.Pending, func(i, j int) bool {
		return result.Pending[i].IssueID < result.Pending[j].IssueID
	})
	return result
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

func daemonTaskGraphLeafIDs(root naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID) []naming.IssueID {
	desc := daemonTaskGraphDescendants(root, children)
	if len(desc) == 0 {
		if task := byID[root]; !task.ID.IsZero() && task.Type != domain.TypeEpic {
			return []naming.IssueID{root}
		}
		return nil
	}
	out := make([]naming.IssueID, 0, len(desc))
	for _, id := range desc {
		task := byID[id]
		if task.ID.IsZero() || task.Type == domain.TypeEpic || len(children[id]) > 0 {
			continue
		}
		out = append(out, id)
	}
	return out
}

func daemonTaskGraphDirectWorkerLeafIDs(root naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID) []naming.IssueID {
	if len(children[root]) == 0 {
		if task := byID[root]; !task.ID.IsZero() && task.Type != domain.TypeEpic {
			return []naming.IssueID{root}
		}
		return nil
	}
	out := make([]naming.IssueID, 0, len(children[root]))
	seen := map[naming.IssueID]struct{}{}
	var walk func(naming.IssueID)
	walk = func(id naming.IssueID) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		task := byID[id]
		if task.ID.IsZero() {
			return
		}
		if daemonTaskGraphRequiresNestedRootOrchestration(root, id, task, children, byID) {
			return
		}
		if len(children[id]) == 0 {
			if task.Type != domain.TypeEpic {
				out = append(out, id)
			}
			return
		}
		for _, childID := range children[id] {
			walk(childID)
		}
	}
	for _, childID := range children[root] {
		walk(childID)
	}
	return out
}

func daemonTaskGraphNestedRootSummaries(root naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID) []taskGraphNestedRoot {
	if len(children[root]) == 0 {
		return nil
	}
	out := make([]taskGraphNestedRoot, 0, len(children[root]))
	seenRoots := map[naming.IssueID]struct{}{}
	seenWalk := map[naming.IssueID]struct{}{}
	var walk func(naming.IssueID)
	walk = func(id naming.IssueID) {
		if _, ok := seenWalk[id]; ok {
			return
		}
		seenWalk[id] = struct{}{}
		task := byID[id]
		if task.ID.IsZero() {
			return
		}
		if daemonTaskGraphRequiresNestedRootOrchestration(root, id, task, children, byID) {
			if _, ok := seenRoots[id]; ok {
				return
			}
			seenRoots[id] = struct{}{}
			out = append(out, taskGraphNestedRoot{
				IssueID:    id.String(),
				Status:     string(task.Status),
				Type:       string(task.Type),
				ChildCount: len(daemonTaskGraphDescendants(id, children)),
				Advice:     fmt.Sprintf("start nested root orchestrator: az session start %s", id.String()),
			})
			return
		}
		for _, childID := range children[id] {
			walk(childID)
		}
	}
	for _, childID := range children[root] {
		walk(childID)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].IssueID < out[j].IssueID
	})
	return out
}

func daemonTaskGraphRequiresNestedRootOrchestration(root, id naming.IssueID, task domain.Task, children map[naming.IssueID][]naming.IssueID, byID map[naming.IssueID]domain.Task) bool {
	if id == root || task.ID.IsZero() {
		return false
	}
	if task.Type != domain.TypeEpic && len(children[id]) == 0 {
		return false
	}
	if task.Status != domain.StatusDone {
		return true
	}
	for _, descID := range daemonTaskGraphDescendants(id, children) {
		if byID[descID].Status != domain.StatusDone {
			return true
		}
	}
	return false
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

func (d *Daemon) daemonTaskGraphActiveSessions(ctx context.Context, projectID string, activeIDs []string, byID map[naming.IssueID]domain.Task, progressByIssue map[string]taskGraphSessionStartProgress) []taskGraphActiveSession {
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
			Status:         "active",
			Advice:         unknownActivityAdvice(issueID),
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
			if progress, found := progressByIssue[sessionKey(issueID)]; found {
				active.StartProgress = &progress
			} else if progress, found := d.sessionInitCommandProgress(ctx, projectID, task); found {
				active.StartProgress = &progress
			}
		}
		out = append(out, active)
	}
	return out
}

func daemonTaskGraphCleanupPendingSessions(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID) []taskGraphActiveSession {
	desc := daemonTaskGraphDescendants(rootID, children)
	out := make([]taskGraphActiveSession, 0)
	for _, id := range desc {
		task := byID[id]
		if task.Status != domain.StatusDone || !daemonCloseGuardTaskHasSession(task) {
			continue
		}
		active := taskGraphActiveSession{
			IssueID:        id.String(),
			Activity:       "unknown",
			ActivitySource: "none",
			Status:         "cleanup-pending",
			Advice:         fmt.Sprintf("closed issue %s still has session projection; cleanup is pending or stale", id.String()),
		}
		if task.Session != nil {
			active.State = string(task.Session.State)
			active.TmuxAttachedCount = task.Session.TmuxAttachedCount
			if activity := strings.TrimSpace(task.Session.Activity); activity != "" {
				active.Activity = activity
			}
			if source := strings.TrimSpace(task.Session.ActivitySource); source != "" {
				active.ActivitySource = source
			}
		}
		out = append(out, active)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].IssueID < out[j].IssueID
	})
	return out
}

func (d *Daemon) sessionStartProgressByIssue(ctx context.Context, projectID string) map[string]taskGraphSessionStartProgress {
	if d == nil || d.operationRuntime == nil || d.operationRuntime.manager == nil {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	records, err := d.operationRuntime.manager.List(ctx, daemonops.Query{
		ProjectID: projectID,
		Kind:      daemonhandlers.CommandSessionStart,
		States:    []daemonops.State{daemonops.StateQueued, daemonops.StateRunning},
		Limit:     500,
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("list session start operations for graph readiness failed", "project_id", projectID, "error", err)
		}
		return nil
	}
	now := time.Now().UTC()
	out := make(map[string]taskGraphSessionStartProgress, len(records))
	for _, record := range records {
		issueID := strings.TrimSpace(record.IssueID)
		if issueID == "" {
			continue
		}
		key := sessionKey(issueID)
		if _, exists := out[key]; exists {
			continue
		}
		out[key] = taskGraphSessionStartProgressFromOperation(record, now)
	}
	return out
}

func taskGraphSessionStartProgressFromOperation(record daemonops.Record, now time.Time) taskGraphSessionStartProgress {
	progress := protocolOperationProgress(record)
	elapsedFrom := record.CreatedAt
	if elapsedFrom.IsZero() {
		elapsedFrom = record.UpdatedAt
	}
	elapsedMS := int64(0)
	if !elapsedFrom.IsZero() && !now.Before(elapsedFrom) {
		elapsedMS = now.Sub(elapsedFrom).Milliseconds()
	}
	return taskGraphSessionStartProgress{
		IssueID:        strings.TrimSpace(record.IssueID),
		OperationID:    strings.TrimSpace(record.ID),
		OperationState: string(record.State),
		Phase:          progress.Phase,
		Message:        progress.Message,
		Percent:        progress.Percent,
		ElapsedMS:      elapsedMS,
		EnqueuedAt:     record.CreatedAt,
		StartedAt:      record.StartedAt,
		FinishedAt:     record.FinishedAt,
	}
}

func (d *Daemon) sessionInitCommandProgress(_ context.Context, projectID string, task domain.Task) (taskGraphSessionStartProgress, bool) {
	if task.Session == nil || len(d.runtimeConfigForProject(projectID).SessionSyncInitCommands) == 0 {
		return taskGraphSessionStartProgress{}, false
	}
	if strings.TrimSpace(task.Session.Activity) != "busy" || strings.TrimSpace(task.Session.ActivitySource) != "session" {
		return taskGraphSessionStartProgress{}, false
	}
	elapsedMS := int64(0)
	if task.Session.StartedAt != nil && !task.Session.StartedAt.IsZero() {
		elapsedMS = time.Since(task.Session.StartedAt.UTC()).Milliseconds()
		if elapsedMS < 0 {
			elapsedMS = 0
		}
	}
	return taskGraphSessionStartProgress{
		IssueID:        task.ID.String(),
		OperationState: string(protocol.OperationStateRunning),
		Phase:          "init_commands",
		Message:        "configured init commands likely running before agent hooks",
		Percent:        90,
		ElapsedMS:      elapsedMS,
		StartedAt:      task.Session.StartedAt,
	}, true
}

func daemonTaskGraphSessionStartProgressList(rootID naming.IssueID, children map[naming.IssueID][]naming.IssueID, progressByIssue map[string]taskGraphSessionStartProgress) []taskGraphSessionStartProgress {
	if len(progressByIssue) == 0 {
		return nil
	}
	desc := daemonTaskGraphDescendants(rootID, children)
	out := make([]taskGraphSessionStartProgress, 0, len(progressByIssue))
	for _, id := range desc {
		if progress, found := progressByIssue[sessionKey(id.String())]; found {
			out = append(out, progress)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IssueID != out[j].IssueID {
			return out[i].IssueID < out[j].IssueID
		}
		return out[i].OperationID < out[j].OperationID
	})
	return out
}

func daemonTaskCompletionAdvice(rootIssueID string, runnable []string, nestedRoots []taskGraphNestedRoot, openDescendants, activeSessions []string, staleCloseable []taskStaleCloseableCandidate) []string {
	advice := make([]string, 0, len(runnable)+len(nestedRoots)+len(openDescendants)+len(activeSessions)+len(staleCloseable))
	for _, id := range activeSessions {
		advice = append(advice, fmt.Sprintf("if intentionally abandoning active worker session, repair-stop it: az orchestrate close-session --issue %s", id))
	}
	for _, nested := range nestedRoots {
		if strings.TrimSpace(nested.Advice) != "" {
			advice = append(advice, nested.Advice)
		} else {
			advice = append(advice, fmt.Sprintf("start nested root orchestrator: az session start %s", nested.IssueID))
		}
	}
	if len(staleCloseable) > 0 {
		advice = append(advice, fmt.Sprintf("stale-closeable children can be handled by the parent close path: az issue close --id %s --close-clean-children", rootIssueID))
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
		Design          *string         `json:"design,omitempty"`
		Notes           *string         `json:"notes,omitempty"`
		Acceptance      *string         `json:"acceptance,omitempty"`
		Estimate        *int            `json:"estimate,omitempty"`
		EstimateSet     bool            `json:"estimate_set,omitempty"`
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
		Design:          cmd.Design,
		Notes:           cmd.Notes,
		Acceptance:      cmd.Acceptance,
		Estimate:        cmd.Estimate,
		EstimateSet:     cmd.EstimateSet,
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
	var result taskDeleteResult
	if err := issueClient.WithMutationLock(ctx, func(ctx context.Context) error {
		var err error
		result, err = d.deleteTask(ctx, issueClient, projectID, cmd, req)
		return err
	}); err != nil {
		return d.errorResponse(req, daemonTaskMutationErrorCode(err), err.Error()), nil
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
	if err := issueClient.EnsureNoUndeletedParentChildDescendants(ctx, "delete", taskID); err != nil {
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
	if err := d.repairStaleRuntimeProjections(ctx, projectID, taskID); err != nil {
		return result, fmt.Errorf("repair runtime projections before deleting %s: %w", taskID, err)
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
		return d.errorResponse(req, daemonTaskMutationErrorCode(err), err.Error()), nil
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

func daemonTaskMutationErrorCode(err error) protocol.ErrorCode {
	if errors.Is(err, issues.ErrIssueHasLiveChildren) {
		return protocol.ErrorCodeConflict
	}
	return protocol.ErrorCodeInternal
}

func (d *Daemon) handleTaskDependencyAdd(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID             string `json:"task_id"`
		DependsOnID        string `json:"depends_on_id"`
		DependencyType     string `json:"dependency_type"`
		ForceParentChange  bool   `json:"force_parent_change"`
		IssueProjectID     string `json:"issue_project_id,omitempty"`
		DependsOnProjectID string `json:"depends_on_project_id,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if err := validateDependencyEndpointProjects(projectID, cmd.IssueProjectID, cmd.DependsOnProjectID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task dependency add requested",
			"project_id", projectID,
			"task_id", cmd.TaskID,
			"depends_on_id", cmd.DependsOnID,
			"dependency_type", cmd.DependencyType,
			"force_parent_change", cmd.ForceParentChange,
			"issue_project_id", cmd.IssueProjectID,
			"depends_on_project_id", cmd.DependsOnProjectID,
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

func validateDependencyEndpointProjects(requestProjectID, issueProjectID, dependsOnProjectID string) error {
	requestProjectID = strings.TrimSpace(requestProjectID)
	issueProjectID = strings.TrimSpace(issueProjectID)
	dependsOnProjectID = strings.TrimSpace(dependsOnProjectID)
	if issueProjectID != "" && dependsOnProjectID != "" && protocol.NormalizeProjectID(issueProjectID) != protocol.NormalizeProjectID(dependsOnProjectID) {
		return fmt.Errorf("dependency endpoints must be in the same project: issue_project_id %q, depends_on_project_id %q", issueProjectID, dependsOnProjectID)
	}
	for _, endpoint := range []struct {
		field     string
		projectID string
	}{
		{field: "issue_project_id", projectID: issueProjectID},
		{field: "depends_on_project_id", projectID: dependsOnProjectID},
	} {
		if endpoint.projectID == "" {
			continue
		}
		if protocol.NormalizeProjectID(endpoint.projectID) != protocol.NormalizeProjectID(requestProjectID) {
			return fmt.Errorf("%s %q does not match request project %q", endpoint.field, endpoint.projectID, requestProjectID)
		}
	}
	return nil
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
