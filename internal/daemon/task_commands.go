package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
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
	taskInvariantReviewHandoff     daemonInvariantID = daemonInvariantTaskReviewHandoff
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

type taskListSnapshotLoadResult struct {
	Revision      uint64
	LastCheckedAt time.Time
	Freshness     protocol.TaskListFreshness
	RuntimeAt     time.Time
	SummariesOnly bool
	Source        protocol.MaterializedSnapshotMetadata
	Tasks         []domain.Task
}

type taskGraphReadinessLoad struct {
	done   chan struct{}
	result taskGraphReadinessResult
	err    error
}

type taskGraphReadinessCacheEntry struct {
	revision  uint64
	expiresAt time.Time
	result    taskGraphReadinessResult
}

type taskGraphRuntimeValidationEntry struct {
	revision    uint64
	validatedAt time.Time
}

type taskGraphRuntimeValidationLoad struct {
	done chan struct{}
	err  error
}

type taskClosePreflightOptions struct {
	AllowTargetSession           bool `json:"allow_target_session,omitempty"`
	AllowActiveSession           bool `json:"allow_active_session,omitempty"`
	AllowTargetWorktree          bool `json:"allow_target_worktree,omitempty"`
	ForceWorktree                bool `json:"force_worktree,omitempty"`
	IgnoreAhead                  bool `json:"ignore_ahead,omitempty"`
	CloseCleanChildren           bool `json:"close_clean_children,omitempty"`
	SkipStatusRepairs            bool `json:"-"`
	AllowIntegratedWorktreeRetry bool `json:"-"`
}

type taskClosePreflightRequest struct {
	TaskID string `json:"task_id"`
	taskClosePreflightOptions
}

type taskCloseRequest struct {
	TaskID                    string                    `json:"task_id"`
	ForceWorktree             bool                      `json:"force_worktree,omitempty"`
	IgnoreAhead               bool                      `json:"ignore_ahead,omitempty"`
	IntegrateBeforeClose      bool                      `json:"integrate_before_close,omitempty"`
	CloseCleanChildren        bool                      `json:"close_clean_children,omitempty"`
	AllowActiveSession        bool                      `json:"allow_active_session,omitempty"`
	CloseOutcome              string                    `json:"closed_outcome,omitempty"`
	ExpectedSourceOID         string                    `json:"expected_source_oid,omitempty"`
	ExpectedBaseOID           string                    `json:"expected_base_oid,omitempty"`
	ExpectedReviewEvidence    *issues.ReviewEvidencePin `json:"expected_review_evidence,omitempty"`
	PromoteBacklogBeforeClose bool                      `json:"-"`
}

type taskCloseExpectedBaseStaleError struct {
	Expected string
	Actual   string
}

func (e *taskCloseExpectedBaseStaleError) Error() string {
	return fmt.Sprintf("configured base changed during authoritative publication: expected=%s actual=%s", e.Expected, e.Actual)
}

func taskCloseFenceExpectedBase(expected, actual string) error {
	expected, actual = strings.TrimSpace(expected), strings.TrimSpace(actual)
	if expected != "" && actual != expected {
		return &taskCloseExpectedBaseStaleError{Expected: expected, Actual: actual}
	}
	return nil
}

func (d *Daemon) fenceTaskCloseExpectedBase(ctx context.Context, targetWorktree, targetBranch, expectedBaseOID string) error {
	if strings.TrimSpace(expectedBaseOID) == "" {
		return nil
	}
	currentBaseOID, err := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
	if err != nil {
		return fmt.Errorf("resolve configured base for publication fence: %w", err)
	}
	return taskCloseFenceExpectedBase(expectedBaseOID, currentBaseOID)
}

type taskStatusUpdateOptions struct {
	CascadeChildren            bool
	AllowBusyReviewHandoffTask string
}

type taskDeleteRequest struct {
	TaskID         string `json:"task_id"`
	Cleanup        bool   `json:"cleanup,omitempty"`
	StopSession    bool   `json:"stop_session,omitempty"`
	RemoveWorktree bool   `json:"remove_worktree,omitempty"`
	ForceWorktree  bool   `json:"force_worktree,omitempty"`
}

type taskCloseResult = protocol.TaskCloseResult

type taskClosePhaseTiming = protocol.TaskClosePhaseTiming

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

type taskDeleteResult struct {
	TaskID          string `json:"task_id"`
	Deleted         bool   `json:"deleted"`
	SessionStopped  bool   `json:"session_stopped,omitempty"`
	WorktreeRemoved bool   `json:"worktree_removed,omitempty"`
	WorktreeForced  bool   `json:"worktree_forced,omitempty"`
	Revision        uint64 `json:"revision,omitempty"`
}

type taskClosePreflightResult struct {
	Task            domain.Task   `json:"task"`
	Worktree        string        `json:"worktree,omitempty"`
	Status          git.GitStatus `json:"status,omitempty"`
	MissingWorktree bool          `json:"-"`
}

type taskDeletePreflightResult struct {
	Task     domain.Task `json:"task"`
	Blockers []string    `json:"blockers,omitempty"`
}

type taskGraphReadinessResult struct {
	Revision               uint64                                `json:"revision,omitempty"`
	Source                 protocol.MaterializedSnapshotMetadata `json:"source,omitempty"`
	RootIssueID            string                                `json:"root_issue_id"`
	Capacity               taskGraphCapacitySummary              `json:"capacity"`
	Runnable               []string                              `json:"runnable"`
	NestedRoots            []taskGraphNestedRoot                 `json:"nested_roots,omitempty"`
	Pending                []taskGraphPendingStart               `json:"pending,omitempty"`
	PublicationQueue       []domain.PublicationOperation         `json:"publication_queue,omitempty"`
	Active                 []string                              `json:"active,omitempty"`
	ActiveSessions         []taskGraphActiveSession              `json:"active_sessions,omitempty"`
	SessionStartProgress   []taskGraphSessionStartProgress       `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []taskStaleCloseableCandidate         `json:"stale_closeable_children,omitempty"`
	ContainmentRisks       []taskContainmentRisk                 `json:"containment_risks,omitempty"`
	WorkerObservations     []domain.WorkerObservation            `json:"worker_observations,omitempty"`
	Blocked                map[string]string                     `json:"blocked"`
	scopeIssueIDs          []string
	cacheExpiresAt         time.Time
}

type taskGraphCapacitySummary struct {
	DirectRunnableCount        int `json:"direct_runnable_count"`
	DirectActiveCount          int `json:"direct_active_count"`
	NestedStartableCount       int `json:"nested_startable_count"`
	NestedActiveCount          int `json:"nested_active_count"`
	PendingStartsCount         int `json:"pending_starts_count"`
	BlockedNestedRootsCount    int `json:"blocked_nested_roots_count"`
	NotCountingCapacityCount   int `json:"not_counting_capacity_count"`
	TotalCountingCapacityCount int `json:"total_counting_capacity_count"`
}

type taskGraphNestedRoot struct {
	IssueID          string                  `json:"issue_id"`
	Status           string                  `json:"status"`
	IssueStatus      string                  `json:"issue_status,omitempty"`
	Classification   string                  `json:"classification,omitempty"`
	ExclusionReasons []string                `json:"exclusion_reasons,omitempty"`
	Type             string                  `json:"type"`
	ChildCount       int                     `json:"child_count"`
	ActiveSession    *taskGraphActiveSession `json:"active_session,omitempty"`
	StartFailure     *taskGraphStartFailure  `json:"start_failure,omitempty"`
	FallbackPolicy   string                  `json:"fallback_policy,omitempty"`
	Advice           string                  `json:"advice,omitempty"`
}

type taskGraphStartFailure struct {
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
	Message        string `json:"message,omitempty"`
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

type taskDurableCompletionEvidence struct {
	EventID int64
	Kind    string
}

type taskContainmentRisk struct {
	IssueID                string   `json:"issue_id"`
	ActiveBranch           string   `json:"active_branch,omitempty"`
	RootIssueID            string   `json:"root_issue_id"`
	RootBranch             string   `json:"root_branch,omitempty"`
	ClosedChildIssueID     string   `json:"closed_child_issue_id"`
	EvidenceCommit         string   `json:"evidence_commit"`
	EvidenceSubject        string   `json:"evidence_subject,omitempty"`
	RootContainsEvidence   bool     `json:"root_contains_evidence"`
	ActiveContainsEvidence bool     `json:"active_contains_evidence"`
	Classification         string   `json:"classification"`
	Message                string   `json:"message"`
	ChangedFiles           []string `json:"changed_files,omitempty"`
	OverlapFiles           []string `json:"overlap_files,omitempty"`
	SuggestedCommand       string   `json:"suggested_command,omitempty"`
}

type taskIntegrationReadinessResult struct {
	IssueID                string                         `json:"issue_id"`
	ParentIssueID          string                         `json:"parent_issue_id,omitempty"`
	Ready                  bool                           `json:"ready"`
	ContextRisk            *domain.IssueContextRiskPacket `json:"context_risk,omitempty"`
	Reasons                []string                       `json:"reasons,omitempty"`
	EvidenceEventSeq       int64                          `json:"evidence_event_seq,omitempty"`
	EvidenceEventID        int64                          `json:"evidence_event_id,omitempty"`
	EvidenceSource         string                         `json:"evidence_source,omitempty"`
	EvidencePacket         *domain.WorkerEvidencePacket   `json:"evidence_packet,omitempty"`
	EvidenceIncomplete     bool                           `json:"evidence_incomplete,omitempty"`
	EvidenceMissingFields  []string                       `json:"evidence_missing_fields,omitempty"`
	EvidenceInvalidReasons []string                       `json:"evidence_invalid_reasons,omitempty"`
	PendingDecisions       []domain.PendingDecisionChange `json:"pending_decisions,omitempty"`
	AggregateValidation    *domain.ValidationRequest      `json:"aggregate_validation,omitempty"`
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
	archiveMode := protocol.ArchiveMode(listReq.Archived)
	startedAt := time.Now()
	result, shared, err := d.loadTaskListSnapshot(ctx, req, projectID, query, listReq.IncludeDependencies, archiveMode)
	if err != nil {
		return d.errorResponse(req, taskReadErrorCode(err), err.Error()), nil
	}
	payload := buildTaskListSnapshotPayload(projectID, result.Revision, result.LastCheckedAt, result.Freshness, result.Tasks, result.SummariesOnly)
	payload.Source = result.Source
	marshalStartedAt := time.Now()
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.list.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(result.Tasks), "cache_hit", false, "shared_load", shared, "query", query != "")
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
	var boardReq protocol.BoardSnapshotRequestBody
	if err := decodeOptionalJSON(req.Body, &boardReq); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(boardReq.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(boardReq.ProjectID.String())
	}
	viewID := strings.TrimSpace(boardReq.ViewID)
	if viewID == "" {
		viewID = d.selectedBoardViewID(projectID)
	}
	if err, unhealthy := d.projectIssueStoreHealthError(projectID); unhealthy {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, err.Error()), nil
	}
	viewRecord, err := d.boardViewRecord(ctx, projectID, viewID)
	if err != nil {
		return d.boardViewErrorResponse(req, err), nil
	}
	if boardReq.ShowChildren != nil {
		viewRecord.View.Options.ShowChildren = *boardReq.ShowChildren
	}
	startedAt := time.Now()
	cacheStartedAt := time.Now()
	if !d.materializedReadsEnabled() {
		if cached, ok := d.readFreshTaskListSnapshotCache(projectID); ok {
			hydrated, hydrateErr := d.hydrateTaskListSnapshotCache(ctx, projectID, cached.Tasks)
			if hydrateErr != nil {
				latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "board.fetch.snapshot_cache_hydrate", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", false, "error", hydrateErr)
			} else {
				cached.Tasks = hydrated
				latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "board.fetch.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", true)
				payload, err := buildBoardSnapshotPayload(projectID, cached.Revision, cached.LastCheckedAt, cached.Freshness, cached.Tasks, viewRecord.View)
				if err != nil {
					return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
				}
				marshalStartedAt := time.Now()
				body, err := json.Marshal(payload)
				latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "board.fetch.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(cached.Tasks), "cache_hit", true)
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
	}
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "board.fetch.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "cache_hit", false)
	result, shared, err := d.loadTaskListSnapshot(ctx, req, projectID, "", false, protocol.ArchiveModeExclude)
	if err != nil {
		return d.errorResponse(req, projectIssueStoreHealthErrorCode(err), err.Error()), nil
	}
	payload, err := buildBoardSnapshotPayload(projectID, result.Revision, result.LastCheckedAt, result.Freshness, result.Tasks, viewRecord.View)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	marshalStartedAt := time.Now()
	body, err := json.Marshal(payload)
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "board.fetch.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_count", len(result.Tasks), "cache_hit", false, "shared_load", shared)
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
	archiveMode, err := protocol.NormalizeArchiveMode(req.Archived)
	if err != nil {
		return protocol.TaskListRequestBody{}, err
	}
	req.Archived = string(archiveMode)
	return req, nil
}

func (d *Daemon) loadTaskListSnapshot(ctx context.Context, req protocol.RequestEnvelope, projectID string, query string, includeDependencies bool, archiveMode protocol.ArchiveMode) (taskListSnapshotLoadResult, bool, error) {
	projectID = d.canonicalProjectID(projectID)
	query = strings.TrimSpace(query)
	if !archiveMode.Valid() {
		archiveMode = protocol.ArchiveModeExclude
	}
	loadKey := projectID
	if query != "" {
		loadKey = projectID + "\x00query:" + strings.ToLower(query)
	}
	if includeDependencies {
		loadKey += "\x00deps"
	}
	if archiveMode != protocol.ArchiveModeExclude {
		loadKey += "\x00archive:" + string(archiveMode)
	}

	d.taskListSnapshotLoadMu.Lock()
	if d.taskListSnapshotLoads == nil {
		d.taskListSnapshotLoads = map[string]*taskListSnapshotLoad{}
	}
	if load := d.taskListSnapshotLoads[loadKey]; load != nil {
		d.taskListSnapshotLoadMu.Unlock()
		waitStartedAt := time.Now()
		select {
		case <-ctx.Done():
			latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.list.singleflight_wait", waitStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "query", query != "", "include_dependencies", includeDependencies, "shared_load", true, "caller_deadline_remaining_ms", contextDeadlineRemainingMillis(ctx), "error", ctx.Err())
			return taskListSnapshotLoadResult{}, true, ctx.Err()
		case <-load.done:
			latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.list.singleflight_wait", waitStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "query", query != "", "include_dependencies", includeDependencies, "shared_load", true, "caller_deadline_remaining_ms", contextDeadlineRemainingMillis(ctx), "error", load.err)
			return cloneTaskListSnapshotLoadResult(load.result), true, load.err
		}
	}
	load := &taskListSnapshotLoad{done: make(chan struct{})}
	d.taskListSnapshotLoads[loadKey] = load
	d.taskListSnapshotLoadMu.Unlock()

	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskListSnapshotLoadTimeout)
	defer cancel()
	result, err := d.buildTaskListSnapshot(buildCtx, req, projectID, query, includeDependencies, archiveMode)
	load.result = cloneTaskListSnapshotLoadResult(result)
	load.err = err

	d.taskListSnapshotLoadMu.Lock()
	delete(d.taskListSnapshotLoads, loadKey)
	close(load.done)
	d.taskListSnapshotLoadMu.Unlock()

	return result, false, err
}

func contextDeadlineRemainingMillis(ctx context.Context) int64 {
	if ctx == nil {
		return -1
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return -1
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining.Milliseconds()
}

func (d *Daemon) buildTaskListSnapshot(ctx context.Context, req protocol.RequestEnvelope, projectID string, query string, includeDependencies bool, archiveMode protocol.ArchiveMode) (taskListSnapshotLoadResult, error) {
	if !d.materializedReadsEnabled() {
		return d.buildLegacyTaskListSnapshot(ctx, req, projectID, query, includeDependencies, archiveMode)
	}
	_ = req
	tasks, source, err := d.convergedProjectReadSnapshot(ctx, projectID)
	if err != nil {
		return taskListSnapshotLoadResult{}, err
	}
	query = strings.TrimSpace(query)
	filtered := tasks[:0]
	lastCheckedAt := time.Unix(0, 0).UTC()
	for _, task := range tasks {
		archived := task.State.IsArchived()
		if (archiveMode == protocol.ArchiveModeExclude && archived) || (archiveMode == protocol.ArchiveModeOnly && !archived) {
			continue
		}
		if query != "" && !domain.TaskMatchesContentQuery(task, query) {
			continue
		}
		lastCheckedAt = laterTime(lastCheckedAt, laterTime(task.UpdatedAt, task.RuntimeUpdatedAt))
		if query == "" {
			task.Description, task.Notes, task.Design, task.Acceptance = "", "", "", ""
			if !includeDependencies {
				task.Dependencies = nil
			}
		}
		filtered = append(filtered, task)
	}
	tasks = filtered
	summariesOnly := query == ""
	revision := d.currentRevision(projectID)
	return taskListSnapshotLoadResult{
		Revision:      revision,
		LastCheckedAt: lastCheckedAt,
		Freshness:     protocol.TaskListFreshnessFresh,
		SummariesOnly: summariesOnly,
		Source:        source,
		Tasks:         tasks,
	}, nil
}

func (d *Daemon) buildLegacyTaskListSnapshot(ctx context.Context, req protocol.RequestEnvelope, projectID, query string, includeDependencies bool, archiveMode protocol.ArchiveMode) (taskListSnapshotLoadResult, error) {
	_ = req
	if err, unhealthy := d.projectIssueStoreHealthError(projectID); unhealthy {
		return taskListSnapshotLoadResult{}, err
	}
	query = strings.TrimSpace(query)
	var runtimeAt time.Time
	var refreshErr error
	if query == "" {
		runtimeAt, _, refreshErr = d.refreshTaskListSessionRuntimeState(ctx, projectID)
		d.triggerWorktreeStateRefresh(projectID)
	} else {
		runtimeAt, _ = d.taskListSnapshotFreshness(ctx, projectID)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return taskListSnapshotLoadResult{}, errors.New("issue store unavailable")
	}
	var tasks []domain.Task
	var err error
	summariesOnly := query == ""
	if query == "" && includeDependencies {
		tasks, err = issueClient.ListSummariesWithRuntimeDependenciesArchiveMode(ctx, projectID, issues.ArchiveMode(archiveMode))
	} else if query == "" {
		tasks, err = issueClient.ListSummariesWithRuntimeArchiveMode(ctx, projectID, issues.ArchiveMode(archiveMode))
	} else {
		tasks, err = issueClient.SearchWithRuntimeArchiveMode(ctx, projectID, query, issues.ArchiveMode(archiveMode))
	}
	if err != nil {
		return taskListSnapshotLoadResult{}, d.recordProjectIssueStoreFailure(projectID, err)
	}
	d.clearProjectIssueStoreHealth(projectID)
	tasks = d.enrichTasksWithSessionState(ctx, projectID, tasks)
	lastCheckedAt, freshness := d.taskListSnapshotFreshness(ctx, projectID)
	if query == "" && refreshErr == nil && !runtimeAt.IsZero() {
		lastCheckedAt = laterTime(lastCheckedAt, runtimeAt)
		if timeNow().Sub(lastCheckedAt) <= taskListSnapshotStaleAfter {
			freshness = protocol.TaskListFreshnessFresh
		}
	}
	revision := d.currentRevision(projectID)
	if query == "" && !includeDependencies && archiveMode == protocol.ArchiveModeExclude {
		d.storeTaskListSnapshotCacheWithRuntimeAt(projectID, revision, lastCheckedAt, freshness, runtimeAt, tasks, summariesOnly)
	}
	return taskListSnapshotLoadResult{Revision: revision, LastCheckedAt: lastCheckedAt, Freshness: freshness, RuntimeAt: runtimeAt, SummariesOnly: summariesOnly, Tasks: tasks}, nil
}

func (d *Daemon) refreshTaskListSessionRuntimeState(_ context.Context, projectID string) (time.Time, bool, error) {
	if d == nil {
		return time.Time{}, false, nil
	}
	projectID = d.canonicalProjectID(projectID)
	d.taskListRuntimeRefreshMu.Lock()
	lastRefresh := d.taskListRuntimeLastRefresh[projectID]
	d.taskListRuntimeRefreshMu.Unlock()
	return lastRefresh, false, nil
}

func cloneTaskListSnapshotLoadResult(result taskListSnapshotLoadResult) taskListSnapshotLoadResult {
	result.Tasks = cloneTasks(result.Tasks)
	return result
}

func (d *Daemon) handleTaskGet(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	var cmd struct {
		TaskID   string `json:"task_id"`
		ActorID  string `json:"actor_id,omitempty"`
		Archived string `json:"archived,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	taskID := strings.TrimSpace(cmd.TaskID)
	if taskID == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "task_id is required"), nil
	}
	archiveMode, err := protocol.NormalizeArchiveMode(cmd.Archived)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if d.materializedReadsEnabled() {
		materialized, source, err := d.convergedProjectReadSnapshot(ctx, projectID)
		if err != nil {
			return d.errorResponse(req, taskReadErrorCode(err), err.Error()), nil
		}
		tasks := materializedTaskContext(materialized, []string{taskID}, true, false, true, false, archiveMode)
		found := false
		for _, task := range tasks {
			if task.ID.String() == taskID {
				found = true
				break
			}
		}
		if !found {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue not found: %s", taskID)), nil
		}
		tasks = d.attachPublicationEvidenceDiagnostic(ctx, projectID, taskID, tasks)
		lastCheckedAt := materializedLastCheckedAt(tasks)
		payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), lastCheckedAt, protocol.TaskListFreshnessFresh, tasks, false)
		payload.Source = source
		body, err := json.Marshal(payload)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		resp := d.successResponse(req)
		resp.Body, resp.Revision = body, payload.SnapshotRevision
		return resp, nil
	}
	cacheStartedAt := time.Now()
	if archiveMode == protocol.ArchiveModeExclude {
		if cached, ok := d.readFreshTaskListSnapshotCache(projectID); ok && !cached.SummariesOnly {
			if _, found := findCachedTaskByID(cached.Tasks, taskID); found {
				hydrated, hydrateErr := d.hydrateTaskListSnapshotCache(ctx, projectID, cached.Tasks)
				if hydrateErr != nil {
					latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.get.snapshot_cache_hydrate", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "cache_hit", false, "error", hydrateErr)
				} else {
					cached.Tasks = hydrated
					cached.Tasks = d.attachPublicationEvidenceDiagnostic(ctx, projectID, taskID, cached.Tasks)
					cached.LastCheckedAt, cached.Freshness = d.taskListSnapshotFreshness(ctx, projectID)
					latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.get.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "cache_hit", true)
					payload := buildTaskListSnapshotPayload(projectID, cached.Revision, cached.LastCheckedAt, cached.Freshness, cached.Tasks, cached.SummariesOnly)
					marshalStartedAt := time.Now()
					body, err := json.Marshal(payload)
					latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.get.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "context_task_count", len(cached.Tasks), "cache_hit", true)
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
	}
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.get.snapshot_cache_read", cacheStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "cache_hit", false)
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task get requested", "project_id", projectID, "task_id", taskID)
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	queryStartedAt := time.Now()
	tasks, err := issueClient.GetWithDependencyContextRuntimeArchiveMode(ctx, projectID, taskID, issues.ArchiveMode(archiveMode))
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.get.issue_store_get_dependency_context_runtime", queryStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID)
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
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.get.snapshot_freshness", freshnessStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "freshness", freshness)
	tasks = d.attachPublicationEvidenceDiagnostic(ctx, projectID, taskID, tasks)
	payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), lastCheckedAt, freshness, tasks, false)
	marshalStartedAt := time.Now()
	body, err := json.Marshal(payload)
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.get.marshal_snapshot", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "context_task_count", len(tasks), "cache_hit", false)
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

func (d *Daemon) attachPublicationEvidenceDiagnostic(ctx context.Context, projectID, taskID string, tasks []domain.Task) []domain.Task {
	out := append([]domain.Task(nil), tasks...)
	diagnostic := domain.PublicationEvidenceDiagnostic{State: "unavailable", Availability: "unavailable", Detail: "publication evidence projection is unavailable"}
	snapshot, err := d.publicationEvidenceSnapshot(ctx, projectID, taskID)
	if err == nil {
		diagnostic = domain.SummarizePublicationEvidence(snapshot, nil)
	} else {
		diagnostic.Detail = err.Error()
	}
	for i := range out {
		if out[i].ID.String() == taskID {
			copy := diagnostic
			out[i].PublicationEvidence = &copy
			break
		}
	}
	return out
}

func (d *Daemon) handleTaskGetMany(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	var cmd struct {
		TaskIDs           []string `json:"task_ids"`
		IncludeAncestors  bool     `json:"include_ancestors,omitempty"`
		ExcludeDependents bool     `json:"exclude_dependents,omitempty"`
		DirectDependents  bool     `json:"direct_dependents,omitempty"`
		MetadataOnly      bool     `json:"metadata_only,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	taskIDs := uniqueTrimmedTaskIDs(cmd.TaskIDs)
	if len(taskIDs) == 0 {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "task_ids is required"), nil
	}
	if d.materializedReadsEnabled() {
		projectionStartedAt := time.Now()
		materialized, source, err := d.convergedProjectReadSnapshot(ctx, projectID)
		if err != nil {
			return d.errorResponse(req, taskReadErrorCode(err), err.Error()), nil
		}
		tasks := materializedTaskContext(materialized, taskIDs, !cmd.MetadataOnly, cmd.IncludeAncestors, !cmd.ExcludeDependents, cmd.DirectDependents, protocol.ArchiveModeExclude)
		latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.get_many.projection_read", projectionStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "requested_task_count", len(taskIDs), "context_task_count", len(tasks))
		payload := buildTaskListSnapshotPayload(projectID, d.currentRevision(projectID), materializedLastCheckedAt(tasks), protocol.TaskListFreshnessFresh, tasks, false)
		payload.Source = source
		body, err := json.Marshal(payload)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		resp := d.successResponse(req)
		resp.Body, resp.Revision = body, payload.SnapshotRevision
		return resp, nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task get-many requested", "project_id", projectID, "task_count", len(taskIDs))
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
	if cmd.DirectDependents {
		contextOptions = append(contextOptions, issues.WithParentChildDependentContext())
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

func taskReadErrorCode(err error) protocol.ErrorCode {
	return projectIssueStoreHealthErrorCode(err)
}

func (d *Daemon) handleTaskEvents(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd protocol.TaskEventsRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	taskID := strings.TrimSpace(string(cmd.TaskID))
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
	payloadEquals := make([]issues.IssueObservationEventPayloadFilter, 0, len(cmd.PayloadEquals))
	for _, filter := range cmd.PayloadEquals {
		payloadEquals = append(payloadEquals, issues.IssueObservationEventPayloadFilter{Key: filter.Key, Value: filter.Value})
	}
	page, err := issueClient.QueryIssueObservationEvents(ctx, taskID, issues.IssueObservationEventQuery{
		Types:         eventTypes,
		Order:         issues.IssueObservationEventOrder(strings.TrimSpace(cmd.Order)),
		Limit:         cmd.Limit,
		AfterID:       cmd.AfterID,
		BeforeID:      cmd.BeforeID,
		Source:        cmd.Source,
		SourceCommand: cmd.SourceCommand,
		OperationID:   cmd.OperationID,
		SessionID:     cmd.SessionID,
		WorktreePath:  cmd.WorktreePath,
		ObservedSince: cmd.ObservedSince,
		ObservedUntil: cmd.ObservedUntil,
		Query:         cmd.Query,
		PayloadEquals: payloadEquals,
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue not found: %s", taskID)), nil
		}
		if errors.Is(err, issues.ErrInvalidIssueObservationEventQuery) {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(protocol.TaskEventsPage{
		Events:       page.Events,
		Order:        string(page.Order),
		Limit:        page.Limit,
		HasMore:      page.HasMore,
		FirstID:      page.FirstID,
		LastID:       page.LastID,
		NextAfterID:  page.NextAfterID,
		NextBeforeID: page.NextBeforeID,
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) handleTaskEventAppend(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID        string         `json:"task_id"`
		Type          string         `json:"event_type"`
		Source        string         `json:"source,omitempty"`
		SourceCommand string         `json:"source_command,omitempty"`
		OperationID   string         `json:"operation_id,omitempty"`
		SessionID     string         `json:"session_id,omitempty"`
		WorktreePath  string         `json:"worktree_path,omitempty"`
		Payload       map[string]any `json:"payload,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	taskID := strings.TrimSpace(cmd.TaskID)
	if taskID == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "task id is required"), nil
	}
	eventType := strings.TrimSpace(cmd.Type)
	if eventType == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "event type is required"), nil
	}
	parsedEventType := domain.IssueObservationEventType(eventType)
	if domain.IssueObservationEventTypeRequiresAuthority(parsedEventType) {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("event type %s is authority-only and cannot be appended through task.event_append", eventType)), nil
	}
	if domain.IsWorkerEvidenceEventType(parsedEventType) {
		packet, validation := domain.ParseWorkerEvidenceIssueEvent(domain.IssueObservationEvent{Type: parsedEventType, Payload: cmd.Payload})
		if !validation.Complete {
			problems := workerEvidenceProblemSummary(validation)
			if !validation.Found {
				problems = "event payload does not contain a structured worker_evidence.v1 packet"
			} else if strings.TrimSpace(problems) == "" {
				problems = "packet is incomplete"
			}
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "invalid worker_evidence.v1 packet: "+problems), nil
		}
		canonicalPayload, payloadErr := domain.WorkerEvidencePacketPayload(packet)
		if payloadErr != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, payloadErr.Error()), nil
		}
		cmd.Payload = canonicalPayload
	}
	source := strings.TrimSpace(cmd.Source)
	if source == "" {
		source = "az issue record"
	}
	event, err := issueClient.AppendIssueObservationEvent(ctx, taskID, issues.IssueObservationEventParams{
		Type:          parsedEventType,
		Source:        source,
		SourceCommand: strings.TrimSpace(cmd.SourceCommand),
		OperationID:   strings.TrimSpace(cmd.OperationID),
		SessionID:     strings.TrimSpace(cmd.SessionID),
		WorktreePath:  strings.TrimSpace(cmd.WorktreePath),
		Payload:       cmd.Payload,
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("issue not found: %s", taskID)), nil
		}
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	body, err := json.Marshal(struct {
		Event domain.IssueObservationEvent `json:"event"`
	}{Event: event})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.nextRevision(projectID)
	if task, err := issueClient.GetWithRuntime(ctx, projectID, taskID); err == nil {
		d.publishTaskEvent(req, protocol.EventTaskUpdated, resp.Revision, taskEventBodyFromTask(projectID, task))
	} else if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("daemon task event append publish task lookup failed", "project_id", projectID, "task_id", taskID, "error", err)
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

func buildBoardSnapshotPayload(projectID string, revision uint64, lastCheckedAt time.Time, freshness protocol.TaskListFreshness, tasks []domain.Task, view domain.BoardView) (protocol.BoardSnapshotPayload, error) {
	if lastCheckedAt.IsZero() {
		lastCheckedAt = timeNow()
	}
	if !freshness.Valid() {
		freshness = protocol.TaskListFreshnessFresh
	}
	view = view.Normalized()
	if view.ID == "" {
		view = domain.DefaultBoardView()
	}
	projection, err := domain.ProjectTasksByBoardView(view, tasks)
	if err != nil {
		return protocol.BoardSnapshotPayload{}, err
	}
	return protocol.BoardSnapshotPayload{
		SchemaVersion:    protocol.BoardSnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: revision,
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    lastCheckedAt.UTC(),
		Freshness:        freshness,
		Projection:       protocol.BoardViewProjectionFromDomain(projection),
	}, nil
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
		rev, err := d.runtimeProjectionStateWriter().PersistGitStatusProjectionAndPublish(ctx, projectID, issueID, worktreePath, status, true, false)
		if err != nil {
			return len(rows), fmt.Errorf("%s: persist refreshed git status projection: %w", issueID, err)
		}
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
	if task.IssueClosed() || task.State.IsArchived() {
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
		if parent.IssueClosed() || parent.State.IsArchived() {
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
			if _, err := d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID); err != nil {
				errs = append(errs, fmt.Errorf("%s: delete missing worktree projection: %w", issueID, err))
			}
			continue
		}
		if !runtimeWorktreeIssueEligible(issueID, taskByIssue) {
			if _, err := d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID); err != nil {
				errs = append(errs, fmt.Errorf("%s: delete ineligible worktree projection: %w", issueID, err))
			}
			continue
		}

		worktreePath := strings.TrimSpace(wt.Path)
		if d.suppressProjectedStaleWorktreeGitRefresh(ctx, projectID, issueID, worktreePath, nil) {
			continue
		}
		branch := strings.TrimSpace(wt.Branch)
		projection, found, projectionErr := d.worktreeRuntimeStateStore(projectID).GetWorktreeStateByIssueID(ctx, projectID, issueID)
		if projectionErr != nil {
			errs = append(errs, fmt.Errorf("%s: load worktree projection: %w", issueID, projectionErr))
			continue
		}
		if !found || strings.TrimSpace(projection.Path) != worktreePath || strings.TrimSpace(projection.Branch) != branch {
			if _, err := d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, issueID, worktreePath, branch); err != nil {
				errs = append(errs, fmt.Errorf("%s: persist worktree projection: %w", issueID, err))
				continue
			}
		}
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
		rev, persistErr := d.runtimeProjectionStateWriter().PersistGitStatusProjectionAndPublish(ctx, projectID, issueID, worktreePath, status, true, false)
		if persistErr != nil {
			errs = append(errs, fmt.Errorf("%s: persist git status projection: %w", issueID, persistErr))
			continue
		}
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

// refreshExactReviewWorktreeGitFacts is reserved for hybrid review acceptance
// and mutation preflight paths that must bind durable projection identity to
// live Git authority. Ordinary task and orchestration reads must never call it.
func (d *Daemon) refreshExactReviewWorktreeGitFacts(ctx context.Context, projectID string, issueIDs []string) error {
	issueIDs = normalizeRuntimeReconcileIssueIDs(issueIDs)
	if len(issueIDs) == 0 {
		return nil
	}
	store := d.worktreeRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return nil
	}
	projectedIssueIDs := make([]string, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		projection, found, err := store.GetWorktreeStateByIssueID(ctx, d.canonicalProjectID(projectID), issueID)
		if err != nil {
			return fmt.Errorf("%s: load finite worktree projection: %w", issueID, err)
		}
		if found && strings.TrimSpace(projection.Path) != "" {
			projectedIssueIDs = append(projectedIssueIDs, issueID)
		}
	}
	if len(projectedIssueIDs) == 0 {
		return nil
	}
	if _, err := d.refreshWorktreeRuntimeStateForIssues(ctx, projectID, projectedIssueIDs); err != nil {
		return err
	}
	return d.refreshProjectReadRuntimeForIssues(ctx, projectID, projectedIssueIDs)
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
		Title           string               `json:"title"`
		Description     string               `json:"description"`
		Type            domain.TaskType      `json:"type"`
		Priority        domain.Priority      `json:"priority"`
		Status          domain.Status        `json:"status,omitempty"`
		Lifecycle       domain.IssueWorkflow `json:"lifecycle_state,omitempty"`
		Assignee        string               `json:"assignee,omitempty"`
		Labels          []string             `json:"labels,omitempty"`
		Implementations []string             `json:"implementations,omitempty"`
		Design          string               `json:"design,omitempty"`
		Notes           string               `json:"notes,omitempty"`
		Acceptance      string               `json:"acceptance,omitempty"`
		Estimate        *int                 `json:"estimate,omitempty"`
		ParentID        *string              `json:"parent_id,omitempty"`
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
		Lifecycle:       cmd.Lifecycle,
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
	if cmd.Status == domain.StatusInReview {
		if risk := d.taskContextRiskForCloseout(ctx, projectID, cmd.TaskID, d.cfg.RepoDir); risk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*risk) {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("context risk is high for issue %s: record root_cause, invariant, regression_validation, or a structured risk note before marking in_review", cmd.TaskID)), nil
		}
	}
	var deferredCleanup deferredTaskWorktreeCleanupCancellation
	if cmd.Status != domain.StatusDone {
		var err error
		deferredCleanup, err = d.cancelDeferredTaskWorktreeCleanup(ctx, projectID, cmd.TaskID, "issue status changed before deferred worktree cleanup completed")
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
	}
	task, updatedTasks, err := d.updateTaskStatusExcludingClose(ctx, projectID, cmd.TaskID, cmd.Status, taskStatusUpdateOptions{
		CascadeChildren:            cmd.CascadeChildren,
		AllowBusyReviewHandoffTask: reviewHandoffActiveIssue(req.Meta, cmd.TaskID),
	})
	if err != nil {
		if compensationErr := d.compensateDeferredTaskWorktreeCleanup(ctx, projectID, cmd.TaskID, deferredCleanup); compensationErr != nil {
			err = errors.Join(err, compensationErr)
		}
		err = d.recordProjectIssueStoreFailure(projectID, err)
		return d.errorResponse(req, projectIssueStoreHealthErrorCode(err), err.Error()), nil
	}
	if deferredCleanup.Observed {
		if err := d.restoreDeferredCleanupWorktreeProjection(ctx, projectID, cmd.TaskID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
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

type taskOwnershipRequest struct {
	TaskID    string                          `json:"task_id"`
	OwnerID   string                          `json:"owner_id,omitempty"`
	OwnerKind string                          `json:"owner_kind,omitempty"`
	TTL       string                          `json:"ttl,omitempty"`
	Force     bool                            `json:"force,omitempty"`
	Purpose   domain.CoordinationLeasePurpose `json:"purpose,omitempty"`
}

func (d *Daemon) handleTaskOwnershipClaim(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd taskOwnershipRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	ttl, err := parseTaskOwnershipTTL(cmd.TTL)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	task, err := issueClient.ClaimOwnershipWithRuntime(ctx, projectID, cmd.TaskID, issues.OwnershipClaimParams{
		OwnerID:   cmd.OwnerID,
		OwnerKind: cmd.OwnerKind,
		TTL:       ttl,
		Force:     cmd.Force,
		Purpose:   cmd.Purpose,
	})
	if err != nil {
		return d.errorResponse(req, taskOwnershipErrorCode(err), err.Error()), nil
	}
	return d.taskOwnershipMutationResponse(req, projectID, task)
}

func (d *Daemon) handleTaskOwnershipRelease(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd taskOwnershipRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	task, err := issueClient.ReleaseOwnershipWithRuntime(ctx, projectID, cmd.TaskID, issues.OwnershipClaimParams{
		OwnerID: cmd.OwnerID,
		Force:   cmd.Force,
		Purpose: cmd.Purpose,
	})
	if err != nil {
		return d.errorResponse(req, taskOwnershipErrorCode(err), err.Error()), nil
	}
	return d.taskOwnershipMutationResponse(req, projectID, task)
}

func taskOwnershipErrorCode(err error) protocol.ErrorCode {
	switch {
	case errors.Is(err, domain.ErrConflict):
		return protocol.ErrorCodeConflict
	case errors.Is(err, domain.ErrNotFound):
		return protocol.ErrorCodeInvalidRequest
	default:
		return protocol.ErrorCodeInternal
	}
}

func (d *Daemon) taskOwnershipMutationResponse(req protocol.RequestEnvelope, projectID string, task domain.Task) (protocol.ResponseEnvelope, error) {
	resp := d.successResponse(req)
	body, err := json.Marshal(task)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp.Body = body
	resp.Revision = d.nextRevision(projectID)
	d.publishTaskEvent(req, protocol.EventTaskUpdated, resp.Revision, taskEventBodyFromTask(projectID, task))
	return resp, nil
}

func parseTaskOwnershipTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid ownership ttl: %w", err)
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("ownership ttl must be positive")
	}
	return ttl, nil
}

func (d *Daemon) handleTaskClose(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd taskCloseRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	closeOutcome, _, err := daemonTaskCloseOutcomeStatus(cmd.CloseOutcome)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	ctx, cancel := context.WithTimeout(ctx, taskCloseTimeout(closeOutcome))
	defer cancel()
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

func taskCloseTimeout(outcome domain.IssueCloseOutcome) time.Duration {
	if outcome == domain.IssueCloseCancelled {
		return domain.LifecycleCleanupTimeout
	}
	return domain.IntegrationCloseTimeout
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
	closeOutcome, closeStatus, err := daemonTaskCloseOutcomeStatus(cmd.CloseOutcome)
	if err != nil {
		return taskCloseResult{}, err
	}
	if closeOutcome == domain.IssueCloseCancelled {
		cmd.IntegrateBeforeClose = false
	}
	result := taskCloseResult{
		TaskID:         taskID,
		Status:         string(closeStatus),
		WorktreeForced: cmd.ForceWorktree,
	}
	result.ContextRisk = d.taskContextRiskForCloseout(ctx, projectID, taskID, d.cfg.RepoDir)
	if result.ContextRisk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*result.ContextRisk) {
		return result, fmt.Errorf("context risk is high for issue %s: record root_cause, invariant, regression_validation, or a structured risk note before closeout", taskID)
	}
	reviewEvidenceFence := ""
	if cmd.ExpectedReviewEvidence != nil {
		reviewEvidenceFence, err = issueClient.BeginReviewEvidenceClose(ctx, taskID, *cmd.ExpectedReviewEvidence)
		if err != nil {
			return result, fmt.Errorf("reviewed evidence close fence for issue %s: %w", taskID, err)
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if releaseErr := issueClient.ReleaseReviewEvidenceClose(releaseCtx, taskID, reviewEvidenceFence); releaseErr != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Error("release review evidence close fence", "project_id", projectID, "task_id", taskID, "error", releaseErr)
			}
		}()
	}
	recordPhase := func(name string, startedAt time.Time, skipped bool) {
		result.Phases = append(result.Phases, taskClosePhaseTiming{
			Name:      name,
			ElapsedMS: time.Since(startedAt).Milliseconds(),
			Skipped:   skipped,
		})
		latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.close."+name, startedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "task_id", taskID, "skipped", skipped)
	}
	var deferredCleanupPlan deferredTaskWorktreeCleanupPlan

	preflightOptions := taskClosePreflightOptions{
		AllowTargetSession:           true,
		AllowActiveSession:           cmd.AllowActiveSession,
		AllowTargetWorktree:          true,
		ForceWorktree:                cmd.ForceWorktree,
		IgnoreAhead:                  cmd.IgnoreAhead || cmd.IntegrateBeforeClose,
		CloseCleanChildren:           false,
		SkipStatusRepairs:            true,
		AllowIntegratedWorktreeRetry: cmd.IntegrateBeforeClose,
	}
	phaseStartedAt := time.Now()
	if err := d.refreshTaskCloseSessionRuntime(ctx, projectID, taskID); err != nil {
		recordPhase("preflight_runtime_projection_repair", phaseStartedAt, false)
		return result, fmt.Errorf("phase preflight_runtime_projection_repair for issue %s: %w", taskID, err)
	}
	recordPhase("preflight_runtime_projection_repair", phaseStartedAt, false)

	phaseStartedAt = time.Now()
	guard, err := d.validateTaskClosePreflight(ctx, projectID, taskID, preflightOptions, req)
	recordPhase("preflight", phaseStartedAt, false)
	if err != nil {
		return result, fmt.Errorf("phase preflight for issue %s: %w", taskID, err)
	}

	phaseStartedAt = time.Now()
	integrationCtx, cancelIntegration, integrationBudgetErr := taskCloseIntegrationContext(ctx)
	if integrationBudgetErr != nil && cmd.IntegrateBeforeClose {
		recordPhase("integrate_before_close", phaseStartedAt, false)
		return result, fmt.Errorf("phase integrate_before_close for issue %s: %w", taskID, integrationBudgetErr)
	}
	integration, err := d.integrateTaskBeforeClose(integrationCtx, projectID, taskID, cmd.IntegrateBeforeClose, guard.MissingWorktree, cmd.ExpectedSourceOID, cmd.ExpectedBaseOID)
	cancelIntegration()
	recordPhase("integrate_before_close", phaseStartedAt, !cmd.IntegrateBeforeClose)
	recordTaskCloseHookPhases(ctx, &result, d.cfg.Logger, req, projectID, taskID, integration.HookDiagnostics)
	if err != nil {
		return result, fmt.Errorf("phase integrate_before_close for issue %s: %w", taskID, err)
	}
	if integration.Integrated {
		if _, bound := taskClosePublicationBindingFromContext(ctx); bound && d.publicationAppliedBeforeTaskReceipt != nil {
			d.publicationAppliedBeforeTaskReceipt(ctx, integration)
		}
	}
	result.IntegrationRequested = integration.Requested
	result.Integrated = integration.Integrated
	result.IntegratedSourceBranch = integration.SourceBranch
	result.IntegratedTargetBranch = integration.TargetBranch
	result.IntegrationValidationAttempts = append([]domain.IntegrationCandidateValidationAttempt(nil), integration.ValidationAttempts...)

	phaseStartedAt = time.Now()
	if integration.Requested && (integration.Integrated || integration.NoChanges) {
		if err := d.persistTaskCloseIntegrationPublication(ctx, projectID, taskID, guard.Worktree, integration); err != nil {
			recordPhase("integration_receipt", phaseStartedAt, false)
			return result, fmt.Errorf("phase integration_receipt for issue %s: %w", taskID, err)
		}
	}
	recordPhase("integration_receipt", phaseStartedAt, !integration.Requested || (!integration.Integrated && !integration.NoChanges))

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
			if _, _, err := d.worktreeAdapter.Delete(ctx, projectID, taskID, daemonhandlers.WorktreeRemoveOptions{
				Force:        cmd.ForceWorktree,
				DeleteBranch: true,
			}); err != nil {
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
	var task domain.Task
	if cmd.ExpectedReviewEvidence != nil {
		task, err = issueClient.CloseWithRuntimeReviewEvidenceFence(ctx, projectID, taskID, closeStatus, *cmd.ExpectedReviewEvidence, reviewEvidenceFence)
	} else {
		task, err = issueClient.CloseWithRuntime(ctx, projectID, taskID, closeStatus)
	}
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

func (d *Daemon) validateTaskCloseReviewEvidence(ctx context.Context, projectID, taskID string, expected *issues.ReviewEvidencePin) error {
	if expected == nil {
		return nil
	}
	if strings.TrimSpace(expected.Source) != "issue_event" || expected.EventID <= 0 || expected.Seq != 0 || strings.TrimSpace(expected.Digest) == "" {
		return fmt.Errorf("accepted evidence is not an atomically revalidatable issue-event pin; fresh review required")
	}
	readiness, err := d.taskIntegrationReadiness(ctx, projectID, taskID, d.cfg.RepoDir)
	if err != nil {
		return err
	}
	current, err := reviewEvidencePinFromReadiness(readiness)
	if err != nil {
		return err
	}
	if current != *expected {
		return fmt.Errorf("reviewed evidence changed; fresh review required")
	}
	return nil
}

func taskCloseIntegrationContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	budget := domain.IntegrationMergeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		available := time.Until(deadline) - domain.IntegrationCloseReserve
		if available <= 0 {
			return ctx, func() {}, fmt.Errorf("close lifecycle budget exhausted before integration; %s cleanup reserve is required", domain.IntegrationCloseReserve)
		}
		if available < budget {
			budget = available
		}
	}
	integrationCtx, cancel := context.WithTimeout(ctx, budget)
	return integrationCtx, cancel, nil
}

func daemonTaskCloseOutcomeStatus(raw string) (domain.IssueCloseOutcome, domain.Status, error) {
	switch domain.IssueCloseOutcome(strings.ToLower(strings.TrimSpace(raw))) {
	case "", domain.IssueCloseCompleted, domain.IssueCloseOutcome(domain.StatusDone):
		return domain.IssueCloseCompleted, domain.StatusDone, nil
	case domain.IssueCloseCancelled:
		return domain.IssueCloseCancelled, domain.StatusCancelled, nil
	default:
		return "", "", fmt.Errorf("invalid close outcome: %s", raw)
	}
}

func (d *Daemon) repairStaleSessionRuntimeProjections(ctx context.Context, projectID, taskID string) error {
	store := d.runtimeStateStoreForProject(projectID)
	if store == nil {
		return nil
	}
	liveSessions, sessionsLoaded, err := d.liveTmuxSessionSet(ctx)
	if err != nil {
		return err
	}
	if !sessionsLoaded {
		return nil
	}
	projectIDs, err := store.ListProjectIDs(ctx)
	if err != nil {
		return err
	}
	for _, projectionProjectID := range projectIDs {
		sessions, err := store.ListSessionStates(ctx, projectionProjectID)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if !naming.IssueIDsEqual(session.IssueID, taskID) || daemonSessionProjectionStopped(session) {
				continue
			}
			if _, live := liveSessions[session.ID]; live {
				continue
			}
			markSessionProjectionStopped(&session)
			if err := store.UpsertSessionState(ctx, projectionProjectID, session); err != nil {
				return err
			}
			if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
				ProjectID: projectionProjectID, SessionID: session.ID,
				ObservedState: daemonstate.SessionStateStopped, UpdatedAt: session.UpdatedAt,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Daemon) refreshTaskCloseSessionRuntime(ctx context.Context, projectID, taskID string) error {
	if _, err := d.reconcileStaleBusySessionActivity(ctx, projectID, []string{taskID}); err != nil {
		return fmt.Errorf("converge stale session activity: %w", err)
	}
	if err := d.repairStaleSessionRuntimeProjections(ctx, projectID, taskID); err != nil {
		return fmt.Errorf("repair stale session projections: %w", err)
	}
	return nil
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
			if !naming.IssueIDsEqual(session.IssueID, taskID) || daemonSessionProjectionStopped(session) {
				continue
			}
			if sessionsLoaded {
				if _, live := liveSessions[session.ID]; !live {
					markSessionProjectionStopped(&session)
					if err := store.UpsertSessionState(ctx, projectionProjectID, session); err != nil {
						return err
					}
					if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
						ProjectID: projectionProjectID, SessionID: session.ID,
						ObservedState: daemonstate.SessionStateStopped, UpdatedAt: session.UpdatedAt,
					}); err != nil {
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

func markSessionProjectionStopped(session *daemonstate.Session) {
	if session == nil {
		return
	}
	session.State = daemonstate.SessionStateStopped
	session.ObservedState = daemonstate.SessionStateStopped
	session.TmuxAttachedCount = 0
	session.UpdatedAt = time.Now().UTC()
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
	if _, err := d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, projectID, taskID); err != nil {
		return deferredTaskWorktreeCleanupPlan{}, fmt.Errorf("publish cleared worktree projection: %w", err)
	}
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
		skip, err := d.deferredTaskWorktreeCleanupShouldSkip(runCtx, projectID, taskID, fallbackPath, fallbackBranch)
		if err != nil {
			return nil, err
		}
		if skip {
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
				if cleanupErr := finalizeDeletedWorktree(runCtx, projectID, taskID, manager, nil, d.runtimeProjectionStateWriter()); cleanupErr != nil {
					return nil, cleanupErr
				}
				return json.Marshal(deferredTaskWorktreeCleanupResult{
					ProjectID: projectID,
					TaskID:    taskID,
				})
			}
			return nil, err
		}
		if cleanupErr := finalizeDeletedWorktree(runCtx, projectID, taskID, manager, removedWorktree, d.runtimeProjectionStateWriter()); cleanupErr != nil {
			return nil, cleanupErr
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

func (d *Daemon) deferredTaskWorktreeCleanupShouldSkip(ctx context.Context, projectID, taskID, fallbackPath, fallbackBranch string) (bool, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return false, nil
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, taskID)
	if err != nil || task.IssueClosed() {
		return false, nil
	}
	if fallbackPath != "" {
		if _, err := d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, taskID, fallbackPath, fallbackBranch); err != nil {
			return false, fmt.Errorf("restore deferred worktree projection: %w", err)
		}
	} else {
		if err := d.restoreDeferredCleanupWorktreeProjection(ctx, projectID, taskID); err != nil {
			return false, err
		}
	}
	return true, nil
}

type deferredCleanupOperationManager interface {
	List(context.Context, daemonops.Query) ([]daemonops.Record, error)
	Get(context.Context, string) (daemonops.Record, error)
	Cancel(context.Context, string, string) (daemonops.Record, error)
}

type deferredTaskWorktreeCleanupCancellation struct {
	Observed       bool
	NeedsRequeue   bool
	FallbackPath   string
	FallbackBranch string
}

func (d *Daemon) deferredTaskCleanupOperationManager() deferredCleanupOperationManager {
	if d == nil {
		return nil
	}
	if d.deferredCleanupOperationManager != nil {
		return d.deferredCleanupOperationManager
	}
	if d.operationRuntime == nil {
		return nil
	}
	return d.operationRuntime.manager
}

func (d *Daemon) cancelDeferredTaskWorktreeCleanup(ctx context.Context, projectID, taskID, reason string) (deferredTaskWorktreeCleanupCancellation, error) {
	manager := d.deferredTaskCleanupOperationManager()
	if manager == nil {
		return deferredTaskWorktreeCleanupCancellation{}, nil
	}
	projectID = normalizedProjectID(projectID)
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return deferredTaskWorktreeCleanupCancellation{}, nil
	}
	records, err := manager.List(ctx, daemonops.Query{
		ProjectID: projectID,
		IssueID:   taskID,
		Kind:      taskDeferredWorktreeCleanupOperationKind,
		States:    []daemonops.State{daemonops.StateQueued, daemonops.StateRunning},
	})
	if err != nil {
		return deferredTaskWorktreeCleanupCancellation{}, fmt.Errorf("list deferred worktree cleanup before lifecycle change: %w", err)
	}
	cancellation := deferredTaskWorktreeCleanupCancellation{}
	for _, record := range records {
		cancellation.Observed = true
		cancellation.captureResourceKeys(record.ResourceKeys)
		cancelled, err := manager.Cancel(ctx, record.ID, reason)
		if err != nil {
			cancelErr := fmt.Errorf("cancel deferred worktree cleanup %s before lifecycle change: %w", record.ID, err)
			return cancellation, errors.Join(cancelErr, d.compensateDeferredTaskWorktreeCleanup(ctx, projectID, taskID, cancellation))
		}
		terminal := cancelled
		if !isOperationTerminal(terminal.State) {
			terminal, err = waitForDeferredTaskWorktreeCleanupTerminal(ctx, manager, record.ID)
			if err != nil {
				waitErr := fmt.Errorf("wait for deferred worktree cleanup %s cancellation: %w", record.ID, err)
				return cancellation, errors.Join(waitErr, d.compensateDeferredTaskWorktreeCleanup(ctx, projectID, taskID, cancellation))
			}
		}
		if terminal.State == daemonops.StateCancelled {
			cancellation.NeedsRequeue = true
		}
	}
	return cancellation, nil
}

func (c *deferredTaskWorktreeCleanupCancellation) captureResourceKeys(keys []string) {
	for _, key := range keys {
		switch {
		case c.FallbackPath == "" && strings.HasPrefix(key, "worktree:"):
			c.FallbackPath = strings.TrimPrefix(key, "worktree:")
		case c.FallbackBranch == "" && strings.HasPrefix(key, "branch:"):
			c.FallbackBranch = strings.TrimPrefix(key, "branch:")
		}
	}
}

func waitForDeferredTaskWorktreeCleanupTerminal(ctx context.Context, manager deferredCleanupOperationManager, operationID string) (daemonops.Record, error) {
	ticker := time.NewTicker(defaultOperationPollDelay)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return daemonops.Record{}, ctx.Err()
		case <-ticker.C:
			record, err := manager.Get(ctx, operationID)
			if err != nil {
				return daemonops.Record{}, err
			}
			if isOperationTerminal(record.State) {
				return record, nil
			}
		}
	}
}

func (d *Daemon) compensateDeferredTaskWorktreeCleanup(ctx context.Context, projectID, taskID string, cancellation deferredTaskWorktreeCleanupCancellation) error {
	if !cancellation.NeedsRequeue {
		return nil
	}
	compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := d.submitDeferredTaskWorktreeCleanup(compensationCtx, projectID, taskID, cancellation.FallbackPath, cancellation.FallbackBranch); err != nil {
		restoreErr := d.restoreDeferredCleanupWorktreeProjection(compensationCtx, projectID, taskID)
		return errors.Join(fmt.Errorf("requeue deferred worktree cleanup after failed lifecycle change: %w", err), restoreErr)
	}
	return nil
}

func (d *Daemon) restoreDeferredCleanupWorktreeProjection(ctx context.Context, projectID, taskID string) error {
	manager := d.worktreeManagerForProject(projectID)
	if manager == nil {
		return nil
	}
	worktree, err := manager.Get(ctx, taskID)
	if err != nil || worktree == nil {
		return nil
	}
	_, err = d.runtimeProjectionStateWriter().PersistWorktreeProjectionAndPublish(ctx, projectID, taskID, worktree.Path, worktree.Branch)
	return err
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

func daemonSessionProjectionStopped(session daemonstate.Session) bool {
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
	Requested              bool
	Integrated             bool
	NoChanges              bool
	ReceiptRecovered       bool
	ConfiguredBaseTarget   bool
	TargetID               string
	SourceBranch           string
	TargetBranch           string
	BaseOID                string
	SourceOID              string
	TargetOID              string
	PublicationOperationID string
	HookDiagnostics        []git.GitHookDiagnostic
	ValidationAttempts     []domain.IntegrationCandidateValidationAttempt
}

type taskCloseIntegrationReceipt struct {
	ProjectID              string
	SourceBranch           string
	TargetBranch           string
	Integrated             bool
	ConfiguredBaseTarget   bool
	TargetID               string
	BaseOID                string
	SourceOID              string
	TargetOID              string
	PublicationOperationID string
}

const taskCloseSlowGitHookThreshold = 1 * time.Second

func verifyTaskCloseIntegrationReceipt(ctx context.Context, gitClient *git.Client, targetWorktree string, receipt taskCloseIntegrationReceipt, projectID, sourceBranch, targetBranch string) error {
	return verifyTaskCloseIntegrationReceiptAtRef(ctx, gitClient, targetWorktree, receipt, projectID, sourceBranch, targetBranch, targetBranch)
}

func verifyTaskCloseIntegrationReceiptAtRef(ctx context.Context, gitClient *git.Client, targetWorktree string, receipt taskCloseIntegrationReceipt, projectID, sourceBranch, targetBranch, targetRef string) error {
	if gitClient == nil {
		return fmt.Errorf("git adapter unavailable")
	}
	if protocol.NormalizeProjectID(receipt.ProjectID) != protocol.NormalizeProjectID(projectID) {
		return fmt.Errorf("integration receipt project %s does not match %s", receipt.ProjectID, projectID)
	}
	if strings.TrimSpace(receipt.SourceBranch) != strings.TrimSpace(sourceBranch) {
		return fmt.Errorf("integration receipt source branch %s does not match %s", receipt.SourceBranch, sourceBranch)
	}
	if strings.TrimSpace(receipt.TargetBranch) != strings.TrimSpace(targetBranch) {
		return fmt.Errorf("integration receipt target branch %s does not match %s", receipt.TargetBranch, targetBranch)
	}
	if strings.TrimSpace(receipt.SourceOID) == "" || strings.TrimSpace(receipt.TargetOID) == "" {
		return fmt.Errorf("integration receipt is missing exact source or target OID")
	}
	targetRef = strings.TrimSpace(targetRef)
	if targetRef == "" {
		return fmt.Errorf("current integration target ref is unavailable")
	}
	sourceReachable, err := gitClient.CommitContainedInRef(ctx, targetWorktree, receipt.SourceOID, targetRef)
	if err != nil {
		return fmt.Errorf("verify recorded source OID %s against %s: %w", receipt.SourceOID, targetRef, err)
	}
	if !sourceReachable {
		return fmt.Errorf("recorded source OID is not reachable from %s: %s", targetRef, receipt.SourceOID)
	}
	targetReachable, err := gitClient.CommitContainedInRef(ctx, targetWorktree, receipt.TargetOID, targetRef)
	if err != nil {
		return fmt.Errorf("verify recorded target OID %s against %s: %w", receipt.TargetOID, targetRef, err)
	}
	if !targetReachable {
		return fmt.Errorf("recorded target OID is not reachable from %s: %s", targetRef, receipt.TargetOID)
	}
	return nil
}

func (d *Daemon) persistTaskCloseIntegrationReceipt(ctx context.Context, projectID, taskID, worktreePath string, integration taskCloseIntegrationResult) error {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	receipt := taskCloseIntegrationReceipt{
		ProjectID:              protocol.NormalizeProjectID(projectID),
		SourceBranch:           strings.TrimSpace(integration.SourceBranch),
		TargetBranch:           strings.TrimSpace(integration.TargetBranch),
		Integrated:             integration.Integrated,
		ConfiguredBaseTarget:   integration.ConfiguredBaseTarget,
		TargetID:               strings.TrimSpace(integration.TargetID),
		BaseOID:                strings.TrimSpace(integration.BaseOID),
		SourceOID:              strings.TrimSpace(integration.SourceOID),
		TargetOID:              strings.TrimSpace(integration.TargetOID),
		PublicationOperationID: strings.TrimSpace(integration.PublicationOperationID),
	}
	if binding, ok := taskClosePublicationBindingFromContext(ctx); ok {
		receipt.PublicationOperationID = binding.operationID
	}
	if receipt.TargetID == "" || receipt.SourceBranch == "" || receipt.TargetBranch == "" || receipt.BaseOID == "" || receipt.SourceOID == "" || receipt.TargetOID == "" {
		return fmt.Errorf("integration result is missing exact typed target, base/source/target branch, or OID")
	}
	_, err := issueClient.AppendIssueObservationEvent(ctx, taskID, issues.IssueObservationEventParams{
		Type:          domain.IssueEventTaskIntegrationCompleted,
		Source:        "daemon-task-close",
		SourceCommand: "integrate-before-close",
		WorktreePath:  strings.TrimSpace(worktreePath),
		Payload: map[string]any{
			"project_id":               receipt.ProjectID,
			"source_branch":            receipt.SourceBranch,
			"target_branch":            receipt.TargetBranch,
			"integrated":               receipt.Integrated,
			"configured_base_target":   receipt.ConfiguredBaseTarget,
			"target_id":                receipt.TargetID,
			"base_oid":                 receipt.BaseOID,
			"source_oid":               receipt.SourceOID,
			"target_oid":               receipt.TargetOID,
			"publication_operation_id": receipt.PublicationOperationID,
		},
	})
	if err != nil {
		return fmt.Errorf("persist exact integration receipt: %w", err)
	}
	return nil
}

func (d *Daemon) persistTaskCloseIntegrationPublication(ctx context.Context, projectID, taskID, worktreePath string, integration taskCloseIntegrationResult) error {
	if !integration.ReceiptRecovered {
		if err := d.persistTaskCloseIntegrationReceipt(ctx, projectID, taskID, worktreePath, integration); err != nil {
			return err
		}
	}
	if !integration.ConfiguredBaseTarget || (!integration.Integrated && !integration.ReceiptRecovered) {
		return nil
	}
	configured, err := d.publicationEvidenceConfigured(projectID)
	if err != nil {
		return fmt.Errorf("resolve merge-result evidence capability: %w", err)
	}
	if !configured {
		return nil
	}
	if integration.ReceiptRecovered && strings.TrimSpace(integration.PublicationOperationID) == "" {
		targetWorktree := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
		if targetWorktree == "" {
			return fmt.Errorf("recover merge-result publication binding: exact project routing unavailable")
		}
		projectCfg, configErr := appconfig.LoadConfig(targetWorktree)
		if configErr != nil {
			return fmt.Errorf("recover merge-result publication binding: load publication capability: %w", configErr)
		}
		gateCommand := strings.TrimSpace(projectCfg.Gate.Command)
		operation, _, provenanceErr := d.taskClosePublicationProvenance(ctx, projectID, taskID, integration, publicationPolicyVersion(projectCfg, gateCommand), gateCommand, publicationEnvironmentFingerprint(projectCfg))
		if provenanceErr != nil {
			return fmt.Errorf("recover merge-result publication binding: %w", provenanceErr)
		}
		integration.PublicationOperationID = strings.TrimSpace(operation.OperationID)
		issueClient := d.issueClientForProject(projectID)
		if issueClient == nil {
			return fmt.Errorf("recover merge-result publication binding: issue store unavailable")
		}
		if _, bindErr := issueClient.BindTaskIntegrationPublicationOperation(ctx, taskID, issues.TaskIntegrationPublicationBinding{
			ProjectID: protocol.NormalizeProjectID(projectID), SourceBranch: integration.SourceBranch, TargetBranch: integration.TargetBranch,
			TargetID: integration.TargetID, BaseOID: integration.BaseOID, SourceOID: integration.SourceOID, TargetOID: integration.TargetOID,
			PublicationOperationID: integration.PublicationOperationID, WorktreePath: worktreePath,
		}); bindErr != nil {
			return fmt.Errorf("recover merge-result publication binding: %w", bindErr)
		}
	}
	if integration.ReceiptRecovered {
		targetWorktree := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
		if targetWorktree == "" {
			return fmt.Errorf("verify recovered merge-result publication: exact project routing unavailable")
		}
		currentTarget, resolveErr := d.git.ResolveCommit(ctx, targetWorktree, integration.TargetBranch)
		if resolveErr != nil {
			return fmt.Errorf("verify recovered merge-result publication target: %w", resolveErr)
		}
		if strings.TrimSpace(currentTarget) != strings.TrimSpace(integration.TargetOID) {
			receipt := taskCloseIntegrationReceipt{
				ProjectID: projectID, SourceBranch: integration.SourceBranch, TargetBranch: integration.TargetBranch,
				Integrated: true, ConfiguredBaseTarget: true, TargetID: integration.TargetID,
				BaseOID: integration.BaseOID, SourceOID: integration.SourceOID, TargetOID: integration.TargetOID,
				PublicationOperationID: integration.PublicationOperationID,
			}
			if err := verifyTaskCloseIntegrationReceiptAtRef(ctx, d.git, targetWorktree, receipt, projectID, integration.SourceBranch, integration.TargetBranch, currentTarget); err != nil {
				return fmt.Errorf("verify recovered merge-result publication target ancestry: %w", err)
			}
			if err := d.verifyRecoveredTaskClosePublication(ctx, projectID, taskID, integration); err != nil {
				return fmt.Errorf("verify recovered merge-result publication: %w", err)
			}
			return nil
		}
	}
	if err := d.recordTaskCloseMergeResultEvidence(ctx, projectID, taskID, integration); err != nil {
		return fmt.Errorf("record merge-result evidence: %w", err)
	}
	return nil
}

func (d *Daemon) latestTaskCloseIntegrationReceipt(ctx context.Context, projectID, taskID, sourceBranch string) (taskCloseIntegrationReceipt, bool, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return taskCloseIntegrationReceipt{}, false, fmt.Errorf("issue store unavailable")
	}
	events, err := issueClient.ListLatestIssueObservationEventsByIssue(ctx, issues.LatestIssueObservationEventOptions{
		IssueIDs:          []string{taskID},
		Type:              domain.IssueEventTaskIntegrationCompleted,
		PayloadTextEquals: map[string]string{"source_branch": strings.TrimSpace(sourceBranch)},
	})
	if err != nil {
		return taskCloseIntegrationReceipt{}, false, fmt.Errorf("read exact integration receipts: %w", err)
	}
	event, found := events[strings.TrimSpace(taskID)]
	if !found {
		return taskCloseIntegrationReceipt{}, false, nil
	}
	receipt := taskCloseIntegrationReceipt{
		ProjectID:              observationPayloadString(event.Payload, "project_id"),
		SourceBranch:           observationPayloadString(event.Payload, "source_branch"),
		TargetBranch:           observationPayloadString(event.Payload, "target_branch"),
		Integrated:             observationPayloadBool(event.Payload, "integrated"),
		ConfiguredBaseTarget:   observationPayloadBool(event.Payload, "configured_base_target"),
		TargetID:               observationPayloadString(event.Payload, "target_id"),
		BaseOID:                observationPayloadString(event.Payload, "base_oid"),
		SourceOID:              observationPayloadString(event.Payload, "source_oid"),
		TargetOID:              observationPayloadString(event.Payload, "target_oid"),
		PublicationOperationID: observationPayloadString(event.Payload, "publication_operation_id"),
	}
	if receipt.SourceOID == "" || receipt.TargetOID == "" {
		return taskCloseIntegrationReceipt{}, false, fmt.Errorf("exact integration receipt %d is missing source_oid or target_oid", event.ID)
	}
	return receipt, true, nil
}

func (d *Daemon) recoverPublishedTaskCloseIntegration(ctx context.Context, projectID, taskID, targetWorktree, targetID, sourceBranch, targetBranch, sourceOID, targetOID string) (taskCloseIntegrationResult, bool, error) {
	receipt, found, err := d.latestTaskCloseIntegrationReceipt(ctx, projectID, taskID, sourceBranch)
	if err != nil || !found {
		return taskCloseIntegrationResult{}, false, err
	}
	if err := validateTaskCloseIntegrationReceiptIdentity(receipt, projectID, targetID, targetBranch, true); err != nil {
		return taskCloseIntegrationResult{}, false, err
	}
	if !receipt.Integrated {
		return taskCloseIntegrationResult{}, false, nil
	}
	if receipt.BaseOID == "" {
		return taskCloseIntegrationResult{}, false, fmt.Errorf("exact integrated receipt is missing pre-integration base OID")
	}
	if receipt.SourceOID != strings.TrimSpace(sourceOID) {
		return taskCloseIntegrationResult{}, false, fmt.Errorf("exact integrated receipt source changed: recorded=%s current=%s", receipt.SourceOID, sourceOID)
	}
	if err := verifyTaskCloseIntegrationReceiptAtRef(ctx, d.git, targetWorktree, receipt, projectID, sourceBranch, targetBranch, targetOID); err != nil {
		return taskCloseIntegrationResult{}, false, fmt.Errorf("exact integrated receipt is not valid: %w", err)
	}
	validationAttempts, err := d.canonicalIntegrationValidationAttempts(ctx, targetWorktree, receipt.TargetOID)
	if err != nil {
		return taskCloseIntegrationResult{}, false, err
	}
	return taskCloseIntegrationResult{
		Requested:              true,
		NoChanges:              true,
		ReceiptRecovered:       true,
		ConfiguredBaseTarget:   true,
		TargetID:               receipt.TargetID,
		SourceBranch:           receipt.SourceBranch,
		TargetBranch:           receipt.TargetBranch,
		BaseOID:                receipt.BaseOID,
		SourceOID:              receipt.SourceOID,
		TargetOID:              receipt.TargetOID,
		PublicationOperationID: receipt.PublicationOperationID,
		ValidationAttempts:     validationAttempts,
	}, true, nil
}

func validateTaskCloseIntegrationReceiptIdentity(receipt taskCloseIntegrationReceipt, projectID, targetID, targetBranch string, configuredBaseTarget bool) error {
	projectID = protocol.NormalizeProjectID(projectID)
	if projectID == "" {
		return fmt.Errorf("fresh integration project identity is unavailable")
	}
	if protocol.NormalizeProjectID(receipt.ProjectID) != projectID {
		return fmt.Errorf("exact integration receipt project identity changed: recorded=%s current=%s", receipt.ProjectID, projectID)
	}
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return fmt.Errorf("fresh typed integration target identity is unavailable")
	}
	if strings.TrimSpace(receipt.TargetID) == "" {
		return fmt.Errorf("exact integration receipt is missing authoritative typed target identity")
	}
	if receipt.TargetID != targetID {
		return fmt.Errorf("exact integration receipt target identity changed: recorded=%s current=%s", receipt.TargetID, targetID)
	}
	if receipt.ConfiguredBaseTarget != configuredBaseTarget {
		return fmt.Errorf("exact integration receipt configured-base identity does not match typed target %s", targetID)
	}
	targetBranch = strings.TrimSpace(targetBranch)
	if targetBranch == "" {
		return fmt.Errorf("fresh integration target branch identity is unavailable")
	}
	if strings.TrimSpace(receipt.TargetBranch) != targetBranch {
		return fmt.Errorf("exact integration receipt target branch changed: recorded=%s current=%s", receipt.TargetBranch, targetBranch)
	}
	return nil
}

func observationPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func observationPayloadBool(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func (d *Daemon) integrateTaskBeforeClose(ctx context.Context, projectID, taskID string, requested, allowMissingSource bool, expectedSourceOID, expectedBaseOID string) (taskCloseIntegrationResult, error) {
	if !requested {
		return taskCloseIntegrationResult{}, nil
	}
	expectedSourceOID = strings.TrimSpace(expectedSourceOID)
	expectedBaseOID = strings.TrimSpace(expectedBaseOID)
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
	if !ok {
		if expectedSourceOID != "" {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("reviewed source commit %s cannot be verified because the source worktree projection is unavailable", expectedSourceOID)
		}
		return taskCloseIntegrationResult{}, nil
	}
	if strings.TrimSpace(source.Branch) == "" {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("source branch unavailable for %s", taskID)
	}
	if strings.TrimSpace(source.Path) == "" && !(allowMissingSource && expectedSourceOID != "") {
		return taskCloseIntegrationResult{}, nil
	}

	target, err := d.taskMergeBaseTarget(ctx, projectID, source.IssueID, d.baseBranchForProject(projectID), false)
	if err != nil {
		return taskCloseIntegrationResult{Requested: true}, err
	}
	targetBranch := strings.TrimSpace(target.Branch)
	configuredBaseTarget := strings.EqualFold(strings.TrimSpace(target.TargetID), "base")
	targetWorktree := strings.TrimSpace(target.WorktreePath)
	branchAttached := target.BranchAttached
	if targetWorktree == "" {
		targetWorktree = strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
		if targetWorktree == "" {
			targetWorktree = strings.TrimSpace(d.resolveRepoDirForProject(projectID))
		}
		if targetWorktree == "" {
			targetWorktree = "."
		}
	}
	if recovery, ok := taskCloseAppliedPublicationRecoveryFromContext(ctx); ok {
		return d.recoverAppliedPublicationIntegration(ctx, projectID, taskID, source, target.TargetID, targetWorktree, targetBranch, configuredBaseTarget, expectedSourceOID, expectedBaseOID, recovery)
	}

	var integration taskCloseIntegrationResult
	if daemonCloseIntegrationShouldUseOriginBase(d.workflowModeForProject(projectID), target) {
		return d.integrateTaskBeforeCloseOriginBase(ctx, projectID, taskID, source, targetWorktree, targetBranch, allowMissingSource, expectedSourceOID, expectedBaseOID)
	}
	if expectedBaseOID != "" {
		if fenceErr := d.fenceTaskCloseExpectedBase(ctx, targetWorktree, targetBranch, expectedBaseOID); fenceErr != nil {
			return taskCloseIntegrationResult{Requested: true}, fenceErr
		}
	}
	sourceOID, sourceOIDErr := d.git.ResolveCommit(ctx, targetWorktree, source.Branch)
	if expectedSourceOID != "" && sourceOIDErr == nil && sourceOID != expectedSourceOID {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("reviewed source commit changed: current=%s reviewed=%s", strings.TrimSpace(sourceOID), expectedSourceOID)
	}
	sourceRef := source.Branch
	if expectedSourceOID != "" {
		sourceRef = expectedSourceOID
	}
	sourceUniqueCommits, containmentErr := d.git.RevListCount(ctx, targetWorktree, targetBranch+".."+sourceRef)
	if containmentErr == nil && sourceUniqueCommits == 0 && sourceOIDErr == nil {
		targetOID, targetOIDErr := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
		if targetOIDErr != nil {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("resolve target commit before no-op close integration: %w", targetOIDErr)
		}
		if fenceErr := taskCloseFenceExpectedBase(expectedBaseOID, targetOID); fenceErr != nil {
			return taskCloseIntegrationResult{Requested: true}, fenceErr
		}
		if configuredBaseTarget {
			if recovered, found, recoverErr := d.recoverPublishedTaskCloseIntegration(ctx, projectID, taskID, targetWorktree, target.TargetID, source.Branch, targetBranch, sourceOID, targetOID); recoverErr != nil {
				return taskCloseIntegrationResult{Requested: true}, recoverErr
			} else if found {
				return recovered, nil
			}
		}
		return d.taskCloseNoChangesIntegrationResult(ctx, targetWorktree, target.TargetID, source.Branch, targetBranch, sourceOID, targetOID, configuredBaseTarget)
	}
	sourcePathMissing, statErr := taskCloseWorktreePathMissing(source.Path)
	if statErr != nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("inspect source worktree path %s before close integration: %w", source.Path, statErr)
	}
	if sourcePathMissing && allowMissingSource {
		if sourceOIDErr == nil {
			if containmentErr == nil {
				return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("source worktree for %s is already removed, but branch %s at %s still has %d commit(s) not reachable from %s; restore the worktree or integrate the branch into %s before retrying close", taskID, source.Branch, sourceOID, sourceUniqueCommits, targetBranch, targetBranch)
			}
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("source worktree for %s is already removed and integration of current branch %s at %s into %s could not be verified: %w", taskID, source.Branch, sourceOID, targetBranch, containmentErr)
		}
		receipt, found, receiptErr := d.latestTaskCloseIntegrationReceipt(ctx, projectID, taskID, source.Branch)
		if receiptErr != nil {
			return taskCloseIntegrationResult{Requested: true}, receiptErr
		}
		if !found {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("source worktree and branch for %s are already removed, but no exact integration receipt exists for %s into %s; restore the source ref or verify and integrate its exact commit before retrying close", taskID, source.Branch, targetBranch)
		}
		if expectedSourceOID != "" && receipt.SourceOID != expectedSourceOID {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("exact integration receipt source does not match reviewed commit: recorded=%s reviewed=%s", receipt.SourceOID, expectedSourceOID)
		}
		if err := validateTaskCloseIntegrationReceiptIdentity(receipt, projectID, target.TargetID, targetBranch, configuredBaseTarget); err != nil {
			return taskCloseIntegrationResult{Requested: true}, err
		}
		if err := verifyTaskCloseIntegrationReceipt(ctx, d.git, targetWorktree, receipt, projectID, source.Branch, targetBranch); err != nil {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("source worktree and branch for %s are already removed, but the exact integration receipt is not valid: %w", taskID, err)
		}
		var validationAttempts []domain.IntegrationCandidateValidationAttempt
		if receipt.Integrated && configuredBaseTarget {
			if receipt.BaseOID == "" {
				return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("exact integrated receipt is missing pre-integration base OID")
			}
			validationAttempts, err = d.canonicalIntegrationValidationAttempts(ctx, targetWorktree, receipt.TargetOID)
			if err != nil {
				return taskCloseIntegrationResult{Requested: true}, err
			}
		}
		if receipt.BaseOID == "" {
			receipt.BaseOID = receipt.TargetOID
		}
		return taskCloseIntegrationResult{
			Requested:              true,
			NoChanges:              true,
			ReceiptRecovered:       receipt.Integrated,
			ConfiguredBaseTarget:   configuredBaseTarget,
			TargetID:               receipt.TargetID,
			SourceBranch:           source.Branch,
			TargetBranch:           targetBranch,
			BaseOID:                receipt.BaseOID,
			SourceOID:              receipt.SourceOID,
			TargetOID:              receipt.TargetOID,
			PublicationOperationID: receipt.PublicationOperationID,
			ValidationAttempts:     validationAttempts,
		}, nil
	}
	if sourceOIDErr != nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("resolve source commit before close integration: %w", sourceOIDErr)
	}
	if containmentErr != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Debug("target-side source containment check failed before close integration", "project_id", projectID, "task_id", taskID, "target_worktree", targetWorktree, "target_branch", targetBranch, "source_branch", source.Branch, "error", containmentErr)
	}
	hasChangesToIntegrate, err := d.ensureMergeToBaseClean(ctx, source, targetWorktree, targetBranch)
	if err != nil {
		return taskCloseIntegrationResult{Requested: true}, err
	}
	if !hasChangesToIntegrate {
		targetOID, targetOIDErr := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
		if targetOIDErr != nil {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("resolve target commit before no-op close integration: %w", targetOIDErr)
		}
		if fenceErr := taskCloseFenceExpectedBase(expectedBaseOID, targetOID); fenceErr != nil {
			return taskCloseIntegrationResult{Requested: true}, fenceErr
		}
		return d.taskCloseNoChangesIntegrationResult(ctx, targetWorktree, target.TargetID, source.Branch, targetBranch, sourceOID, targetOID, configuredBaseTarget)
	}
	preflight, err := d.git.MergePreflight(ctx, source.Path, targetWorktree, targetBranch, sourceRef)
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
	if configuredBaseTarget {
		publicationConfigured, publicationErr := d.publicationEvidenceConfigured(projectID)
		if publicationErr != nil {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("resolve configured-base publication authority before integration: %w", publicationErr)
		}
		if _, bound := taskClosePublicationBindingFromContext(ctx); publicationConfigured && !bound {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("configured-base integration requires an accepted publication operation before synthetic merge or apply")
		}
	}
	if !branchAttached {
		if err := d.git.WithWorktreeLock(ctx, targetWorktree, func(ctx context.Context) error {
			return d.git.Checkout(ctx, targetWorktree, targetBranch)
		}); err != nil {
			return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("checkout target branch before close integration: %w", err)
		}
	}
	baseOID, baseOIDErr := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
	if baseOIDErr != nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("resolve target commit before close integration: %w", baseOIDErr)
	}
	if fenceErr := taskCloseFenceExpectedBase(expectedBaseOID, baseOID); fenceErr != nil {
		return taskCloseIntegrationResult{Requested: true}, fenceErr
	}
	merge, err := d.mergeTaskBranchBeforeClose(ctx, projectID, taskID, targetWorktree, targetBranch, sourceRef, configuredBaseTarget, expectedBaseOID)
	if err != nil {
		return taskCloseIntegrationResult{Requested: true}, err
	}
	if merge == nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("merge %s into %s returned no result", source.Branch, targetBranch)
	}
	if !merge.Success {
		if message, ok := candidateValidationFailureMessage(merge.ValidationAttempts); ok {
			return taskCloseIntegrationResult{Requested: true, ValidationAttempts: append([]domain.IntegrationCandidateValidationAttempt(nil), merge.ValidationAttempts...)}, errors.New(message)
		}
		details := strings.TrimSpace(merge.Message)
		if len(merge.ConflictFiles) > 0 {
			details = strings.TrimSpace(details + "\nconflicts: " + strings.Join(merge.ConflictFiles, ", "))
		}
		if details == "" {
			details = "merge did not complete successfully"
		}
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("merge %s into %s failed: %s", source.Branch, targetBranch, details)
	}
	targetOID, targetOIDErr := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
	if targetOIDErr != nil {
		return taskCloseIntegrationResult{Requested: true}, fmt.Errorf("resolve resulting target commit after close integration: %w", targetOIDErr)
	}
	integration = taskCloseIntegrationResult{
		Requested:            true,
		Integrated:           true,
		ConfiguredBaseTarget: configuredBaseTarget,
		TargetID:             target.TargetID,
		SourceBranch:         source.Branch,
		TargetBranch:         targetBranch,
		BaseOID:              baseOID,
		SourceOID:            sourceOID,
		TargetOID:            targetOID,
		HookDiagnostics:      append([]git.GitHookDiagnostic(nil), merge.HookDiagnostics...),
		ValidationAttempts:   append([]domain.IntegrationCandidateValidationAttempt(nil), merge.ValidationAttempts...),
	}
	return integration, nil
}

func (d *Daemon) recoverAppliedPublicationIntegration(ctx context.Context, projectID, taskID string, source git.Worktree, targetID, targetWorktree, targetBranch string, configuredBaseTarget bool, expectedSourceOID, expectedBaseOID string, operation domain.PublicationOperation) (taskCloseIntegrationResult, error) {
	result := taskCloseIntegrationResult{Requested: true}
	if protocol.NormalizeProjectID(operation.ProjectID) != protocol.NormalizeProjectID(projectID) || !naming.IssueIDsEqual(operation.IssueID, taskID) {
		return result, fmt.Errorf("exact applied publication operation %s does not match task %s in project %s", operation.OperationID, taskID, projectID)
	}
	if !configuredBaseTarget || !strings.EqualFold(strings.TrimSpace(targetID), strings.TrimSpace(operation.TargetID)) || strings.TrimSpace(targetBranch) != strings.TrimSpace(operation.TargetBranch) {
		return result, fmt.Errorf("exact applied publication operation %s target identity changed", operation.OperationID)
	}
	if strings.TrimSpace(expectedBaseOID) != strings.TrimSpace(operation.BaseRevision) || strings.TrimSpace(expectedSourceOID) != strings.TrimSpace(operation.SourceRevision) {
		return result, fmt.Errorf("exact applied publication operation %s reviewed base or source identity changed", operation.OperationID)
	}
	sourceOID, err := d.git.ResolveCommit(ctx, targetWorktree, source.Branch)
	if err != nil {
		return result, fmt.Errorf("resolve exact applied publication source %s: %w", source.Branch, err)
	}
	if strings.TrimSpace(sourceOID) != strings.TrimSpace(operation.SourceRevision) {
		return result, fmt.Errorf("exact applied publication source changed: current=%s reviewed=%s", strings.TrimSpace(sourceOID), strings.TrimSpace(operation.SourceRevision))
	}
	targetOID, err := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
	if err != nil {
		return result, fmt.Errorf("resolve exact applied publication target %s: %w", targetBranch, err)
	}
	if strings.TrimSpace(targetOID) != strings.TrimSpace(operation.CandidateRevision) {
		return result, fmt.Errorf("exact applied publication target changed: current=%s candidate=%s", strings.TrimSpace(targetOID), strings.TrimSpace(operation.CandidateRevision))
	}
	contained, err := d.git.CommitContainedInRef(ctx, targetWorktree, sourceOID, targetBranch)
	if err != nil || !contained {
		if err == nil {
			err = fmt.Errorf("source is not reachable from target")
		}
		return result, fmt.Errorf("verify exact applied publication containment: %w", err)
	}
	validationAttempts, err := d.canonicalIntegrationValidationAttempts(ctx, targetWorktree, targetOID)
	if err != nil {
		return result, err
	}
	if len(validationAttempts) != 1 || validationAttempts[0].Status != domain.IntegrationCandidateValidationPassed || !validationAttempts[0].Canonical || strings.TrimSpace(validationAttempts[0].CandidateHead) != strings.TrimSpace(targetOID) {
		return result, fmt.Errorf("exact applied publication %s has no canonical validation receipt for %s", operation.OperationID, targetOID)
	}
	return taskCloseIntegrationResult{
		Requested: true, Integrated: true, ConfiguredBaseTarget: true,
		TargetID: strings.TrimSpace(targetID), SourceBranch: strings.TrimSpace(source.Branch), TargetBranch: strings.TrimSpace(targetBranch),
		BaseOID: strings.TrimSpace(operation.BaseRevision), SourceOID: strings.TrimSpace(sourceOID), TargetOID: strings.TrimSpace(targetOID),
		PublicationOperationID: strings.TrimSpace(operation.OperationID), ValidationAttempts: validationAttempts,
	}, nil
}

func failedCandidateValidationAttempt(attempts []domain.IntegrationCandidateValidationAttempt) (domain.IntegrationCandidateValidationAttempt, bool) {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Status == domain.IntegrationCandidateValidationFailed {
			return attempts[i], true
		}
	}
	return domain.IntegrationCandidateValidationAttempt{}, false
}

func candidateValidationFailureMessage(attempts []domain.IntegrationCandidateValidationAttempt) (string, bool) {
	attempt, ok := failedCandidateValidationAttempt(attempts)
	if !ok {
		return "", false
	}
	details := strings.TrimSpace(attempt.Message)
	if details == "" {
		details = "candidate validation gate failed without diagnostic output"
	}
	return fmt.Sprintf("candidate validation for revision %s failed: %s", attempt.CandidateHead, details), true
}

func (d *Daemon) canonicalIntegrationValidationAttempts(ctx context.Context, targetWorktree, targetOID string) ([]domain.IntegrationCandidateValidationAttempt, error) {
	attempt, found, err := d.git.CanonicalIntegrationValidation(ctx, targetWorktree, targetOID)
	if err != nil {
		return nil, fmt.Errorf("read canonical integration validation for target %s: %w", targetOID, err)
	}
	if !found {
		return nil, nil
	}
	return []domain.IntegrationCandidateValidationAttempt{attempt}, nil
}

func (d *Daemon) taskCloseNoChangesIntegrationResult(ctx context.Context, targetWorktree, targetID, sourceBranch, targetBranch, sourceOID, targetOID string, configuredBaseTarget bool) (taskCloseIntegrationResult, error) {
	var validationAttempts []domain.IntegrationCandidateValidationAttempt
	if configuredBaseTarget {
		var err error
		validationAttempts, err = d.canonicalIntegrationValidationAttempts(ctx, targetWorktree, targetOID)
		if err != nil {
			return taskCloseIntegrationResult{Requested: true}, err
		}
	}
	return taskCloseIntegrationResult{
		Requested:            true,
		NoChanges:            true,
		ConfiguredBaseTarget: configuredBaseTarget,
		TargetID:             strings.TrimSpace(targetID),
		SourceBranch:         sourceBranch,
		TargetBranch:         targetBranch,
		BaseOID:              targetOID,
		SourceOID:            sourceOID,
		TargetOID:            targetOID,
		ValidationAttempts:   validationAttempts,
	}, nil
}

func daemonCloseIntegrationShouldUseOriginBase(workflowMode string, target taskMergeBaseTargetResult) bool {
	return strings.EqualFold(strings.TrimSpace(workflowMode), "origin") &&
		strings.EqualFold(strings.TrimSpace(target.TargetID), "base") &&
		strings.TrimSpace(target.WorktreePath) == ""
}

func (d *Daemon) integrateTaskBeforeCloseOriginBase(ctx context.Context, projectID, taskID string, source git.Worktree, targetWorktree, targetBranch string, allowMissingSource bool, expectedSourceOID, expectedBaseOID string) (taskCloseIntegrationResult, error) {
	remoteBaseRef := daemonRemoteTrackingBaseRef(targetBranch)
	result := taskCloseIntegrationResult{
		Requested:            true,
		ConfiguredBaseTarget: true,
		TargetID:             "base",
		SourceBranch:         source.Branch,
		TargetBranch:         remoteBaseRef,
	}
	sourcePathMissing, statErr := taskCloseWorktreePathMissing(source.Path)
	if statErr != nil {
		return result, fmt.Errorf("inspect source worktree path %s before origin-mode close integration: %w", source.Path, statErr)
	}
	if sourcePathMissing && allowMissingSource {
		if err := d.git.Fetch(ctx, targetWorktree, "origin"); err != nil {
			return result, fmt.Errorf("fetch origin before origin-mode close integration retry for %s: %w", source.IssueID, err)
		}
		sourceOID, sourceErr := d.git.ResolveCommit(ctx, targetWorktree, source.Branch)
		expectedSourceOID = strings.TrimSpace(expectedSourceOID)
		if expectedSourceOID != "" && sourceErr == nil && sourceOID != expectedSourceOID {
			return result, fmt.Errorf("reviewed source commit changed: current=%s reviewed=%s", strings.TrimSpace(sourceOID), expectedSourceOID)
		}
		if sourceErr == nil {
			contained, err := d.git.CommitContainedInRef(ctx, targetWorktree, sourceOID, remoteBaseRef)
			if err != nil {
				return result, fmt.Errorf("verify origin-mode source commit %s against %s: %w", sourceOID, remoteBaseRef, err)
			}
			if !contained {
				return result, fmt.Errorf("source worktree for %s is already removed, but branch %s at %s is not reachable from %s", taskID, source.Branch, sourceOID, remoteBaseRef)
			}
			targetOID, err := d.git.ResolveCommit(ctx, targetWorktree, remoteBaseRef)
			if err != nil {
				return result, fmt.Errorf("resolve %s during origin-mode close integration retry for %s: %w", remoteBaseRef, source.IssueID, err)
			}
			if err := taskCloseFenceExpectedBase(expectedBaseOID, targetOID); err != nil {
				return result, err
			}
			result.NoChanges = true
			result.BaseOID = targetOID
			result.SourceOID = sourceOID
			result.TargetOID = targetOID
			return result, nil
		}
		receipt, found, err := d.latestTaskCloseIntegrationReceipt(ctx, projectID, taskID, source.Branch)
		if err != nil {
			return result, err
		}
		if !found {
			return result, fmt.Errorf("source worktree and branch for %s are already removed, but no exact integration receipt exists for %s into %s", taskID, source.Branch, remoteBaseRef)
		}
		if err := validateTaskCloseIntegrationReceiptIdentity(receipt, projectID, "base", remoteBaseRef, true); err != nil {
			return result, err
		}
		if expectedSourceOID != "" && receipt.SourceOID != expectedSourceOID {
			return result, fmt.Errorf("exact integration receipt source does not match reviewed commit: recorded=%s reviewed=%s", receipt.SourceOID, expectedSourceOID)
		}
		if err := verifyTaskCloseIntegrationReceipt(ctx, d.git, targetWorktree, receipt, projectID, source.Branch, remoteBaseRef); err != nil {
			return result, fmt.Errorf("source worktree and branch for %s are already removed, but the exact origin-mode integration receipt is not valid: %w", taskID, err)
		}
		if receipt.BaseOID == "" {
			receipt.BaseOID = receipt.TargetOID
		}
		result.NoChanges = true
		result.BaseOID = receipt.BaseOID
		result.SourceOID = receipt.SourceOID
		result.TargetOID = receipt.TargetOID
		return result, nil
	}
	sourceStatus, err := d.git.Status(ctx, source.Path)
	if err != nil {
		return result, fmt.Errorf("read source status for %s: %w", source.IssueID, err)
	}
	if gitStatusHasDirtyFiles(sourceStatus) {
		return result, fmt.Errorf("source worker %s is not clean: %s", source.IssueID, gitStatusSummaryWithDetails(sourceStatus))
	}
	if err := d.git.Fetch(ctx, targetWorktree, "origin"); err != nil {
		return result, fmt.Errorf("fetch origin before origin-mode close integration for %s: %w", source.IssueID, err)
	}
	sourceOID, err := d.git.ResolveCommit(ctx, targetWorktree, source.Branch)
	if err != nil {
		return result, fmt.Errorf("resolve source commit for origin-mode close integration %s: %w", source.IssueID, err)
	}
	if expectedSourceOID = strings.TrimSpace(expectedSourceOID); expectedSourceOID != "" && sourceOID != expectedSourceOID {
		return result, fmt.Errorf("reviewed source commit changed: current=%s reviewed=%s", sourceOID, expectedSourceOID)
	}
	sourceRef := source.Branch
	if expectedSourceOID != "" {
		sourceRef = expectedSourceOID
	}
	targetOID, err := d.git.ResolveCommit(ctx, targetWorktree, remoteBaseRef)
	if err != nil {
		return result, fmt.Errorf("resolve %s before origin-mode close integration for %s: %w", remoteBaseRef, source.IssueID, err)
	}
	if err := taskCloseFenceExpectedBase(expectedBaseOID, targetOID); err != nil {
		return result, err
	}
	result.SourceOID = sourceOID
	result.BaseOID = targetOID
	result.TargetOID = targetOID
	changedFiles, err := d.git.ChangedFilesBetweenRefTrees(ctx, targetWorktree, remoteBaseRef, sourceRef)
	if err != nil {
		return result, fmt.Errorf("inspect origin-mode close diff for %s against %s: %w", source.IssueID, remoteBaseRef, err)
	}
	if len(changedFiles) == 0 {
		result.NoChanges = true
		return result, nil
	}
	contained, err := d.git.CommitContainedInRef(ctx, targetWorktree, sourceRef, remoteBaseRef)
	if err != nil {
		return result, fmt.Errorf("inspect origin-mode close ancestry for %s against %s: %w", source.IssueID, remoteBaseRef, err)
	}
	if contained {
		result.NoChanges = true
		return result, nil
	}
	return result, fmt.Errorf(
		"origin workflow close will not merge %s into the local %s checkout; %s still differs from %s (%d file(s): %s). Next: %s",
		source.Branch,
		targetBranch,
		source.IssueID,
		remoteBaseRef,
		len(changedFiles),
		strings.Join(daemonLimitStrings(changedFiles, 8), ", "),
		daemonOriginBaseIntegrationGuidance(source.IssueID, targetBranch),
	)
}

func daemonOriginBaseIntegrationGuidance(issueID, targetBranch string) string {
	issueID = strings.TrimSpace(issueID)
	remoteBaseRef := daemonRemoteTrackingBaseRef(targetBranch)
	return fmt.Sprintf(
		"push the issue branch with `git push -u origin HEAD`, run `az pr create --issue %s --draft=false`, inspect it with `az pr status --issue %s`, merge it with `az pr merge --issue %s --confirm`, fetch %s, then retry `az ticket close --id %s`",
		issueID, issueID, issueID, remoteBaseRef, issueID,
	)
}

func daemonRemoteTrackingBaseRef(targetBranch string) string {
	targetBranch = strings.TrimSpace(targetBranch)
	if targetBranch == "" {
		targetBranch = "main"
	}
	if strings.HasPrefix(targetBranch, "refs/remotes/") || strings.HasPrefix(targetBranch, "origin/") {
		return targetBranch
	}
	return "origin/" + targetBranch
}

func daemonLimitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	out := append([]string(nil), values[:limit]...)
	out = append(out, fmt.Sprintf("%d more omitted", len(values)-limit))
	return out
}

func recordTaskCloseHookPhases(ctx context.Context, result *taskCloseResult, logger *slog.Logger, req protocol.RequestEnvelope, projectID, taskID string, hooks []git.GitHookDiagnostic) {
	if result == nil || len(hooks) == 0 {
		return
	}
	for _, hook := range hooks {
		hookName := strings.TrimSpace(hook.Hook)
		name := "githook"
		if hookName != "" {
			name += "." + hookName
		}
		exitStatus := hook.ExitStatus
		blocking := hook.Blocking
		timedOut := hook.TimedOut
		phase := taskClosePhaseTiming{
			Name:       name,
			Hook:       hookName,
			Command:    strings.TrimSpace(hook.Command),
			ElapsedMS:  hook.ElapsedMS,
			ExitStatus: &exitStatus,
			Blocking:   &blocking,
			TimedOut:   &timedOut,
		}
		result.Phases = append(result.Phases, phase)
		startedAt := time.Now().Add(-time.Duration(hook.ElapsedMS) * time.Millisecond)
		latencytrace.LogPhaseContext(ctx, logger, "daemon", "task.close."+name, startedAt,
			"command", req.Command,
			"request_id", req.RequestID,
			"project_id", projectID,
			"task_id", taskID,
			"hook", hookName,
			"hook_command_shape", phase.Command,
			"command_shape", phase.Command,
			"exit_status", exitStatus,
			"blocking", blocking,
			"timed_out", timedOut,
		)
		if logger != nil && time.Duration(hook.ElapsedMS)*time.Millisecond >= taskCloseSlowGitHookThreshold {
			logger.WarnContext(ctx, "slow task close git hook",
				"event", "task.close.githook.slow",
				"operation", "task.close",
				"request_id", req.RequestID,
				"project_id", projectID,
				"task_id", taskID,
				"hook", hookName,
				"hook_command_shape", phase.Command,
				"elapsed_ms", hook.ElapsedMS,
				"exit_status", exitStatus,
				"blocking", blocking,
				"timed_out", timedOut,
			)
		}
	}
}

func (d *Daemon) mergeTaskBranchBeforeClose(ctx context.Context, projectID, taskID, targetWorktree, targetBranch, sourceBranch string, configuredBaseTarget bool, expectedBaseOID string) (*git.MergeResult, error) {
	ctx = git.WithIntegrationFailureArtifactPaths(ctx, d.runtimeConfigForProject(projectID).GateFailureArtifactPaths)
	var validationAttempts []git.CandidateValidationAttempt
	for attempt := 1; ; attempt++ {
		if err := d.fenceTaskCloseExpectedBase(ctx, targetWorktree, targetBranch, expectedBaseOID); err != nil {
			return nil, err
		}
		var result *git.MergeResult
		var err error
		if configuredBaseTarget {
			result, err = d.git.MergeCleanlyTransactionalAtTarget(ctx, targetWorktree, sourceBranch, expectedBaseOID, targetBranch)
		} else {
			result, err = d.git.MergeCleanlyTransactionalComposition(ctx, targetWorktree, sourceBranch)
		}
		if err != nil {
			var stale *git.IntegrationTargetStaleError
			if errors.As(err, &stale) {
				return nil, &taskCloseExpectedBaseStaleError{Expected: stale.ExpectedHead, Actual: stale.ActualHead}
			}
			return nil, fmt.Errorf("merge %s into %s: %w", sourceBranch, targetBranch, err)
		}
		if result == nil {
			return nil, fmt.Errorf("merge %s into %s returned no result", sourceBranch, targetBranch)
		}
		currentAttempts := append([]git.CandidateValidationAttempt(nil), result.ValidationAttempts...)
		validationAttempts = append(validationAttempts, currentAttempts...)
		if result.Success || !git.IsTransactionalMergeStaleTarget(result) {
			result.ValidationAttempts = validationAttempts
			return result, nil
		}
		if strings.TrimSpace(expectedBaseOID) != "" {
			actualBaseOID, resolveErr := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolve configured base after stale transactional apply: %w", resolveErr)
			}
			return nil, &taskCloseExpectedBaseStaleError{Expected: strings.TrimSpace(expectedBaseOID), Actual: strings.TrimSpace(actualBaseOID)}
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
		reasons = append(reasons, fmt.Sprintf("source worker %s is not clean: %s", source.IssueID, gitStatusSummaryWithDetails(sourceStatus)))
	}
	if gitStatusHasDirtyFiles(targetStatus) && hasChangesToIntegrate {
		reasons = append(reasons, formatDirtyTargetPreflightReason(source.IssueID, targetWorktree, targetStatus))
	}
	if len(reasons) > 0 {
		return false, errors.New(strings.Join(reasons, "; "))
	}
	return hasChangesToIntegrate, nil
}

const gitStatusDetailPathLimit = 12

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

func gitStatusSummaryWithDetails(status *git.GitStatus) string {
	groups := gitStatusDetailGroups(status)
	summaryParts := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group.files) == 0 {
			continue
		}
		summaryParts = append(summaryParts, fmt.Sprintf("%d %s", len(group.files), group.label))
	}
	summary := strings.Join(summaryParts, ", ")
	if summary == "" {
		summary = gitStatusSummary(status)
	}
	details := gitStatusDetails(status, gitStatusDetailPathLimit)
	if details == "" {
		return summary
	}
	return fmt.Sprintf("%s; %s", summary, details)
}

type gitStatusDetailGroup struct {
	label string
	files []string
}

func gitStatusDetailGroups(status *git.GitStatus) []gitStatusDetailGroup {
	if status == nil {
		return nil
	}
	staged := uniqueNonEmpty(status.Staged)
	stagedSet := make(map[string]struct{}, len(staged))
	for _, file := range staged {
		stagedSet[file] = struct{}{}
	}
	unstagedOnly := func(files []string) []string {
		out := uniqueNonEmpty(files)
		if len(out) == 0 || len(stagedSet) == 0 {
			return out
		}
		filtered := out[:0]
		for _, file := range out {
			if _, ok := stagedSet[file]; ok {
				continue
			}
			filtered = append(filtered, file)
		}
		return filtered
	}
	return []gitStatusDetailGroup{
		{label: "staged", files: staged},
		{label: "modified", files: unstagedOnly(status.Modified)},
		{label: "added", files: unstagedOnly(status.Added)},
		{label: "deleted", files: unstagedOnly(status.Deleted)},
		{label: "untracked", files: uniqueNonEmpty(status.Untracked)},
		{label: "conflicted", files: uniqueNonEmpty(status.Conflicted)},
	}
}

func gitStatusDetails(status *git.GitStatus, limit int) string {
	if status == nil {
		return ""
	}
	if limit <= 0 {
		limit = gitStatusDetailPathLimit
	}

	remaining := limit
	omitted := 0
	groups := gitStatusDetailGroups(status)
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		files := group.files
		if len(files) == 0 {
			continue
		}
		if remaining <= 0 {
			omitted += len(files)
			continue
		}
		shown := files
		if len(shown) > remaining {
			shown = shown[:remaining]
			omitted += len(files) - len(shown)
		}
		parts = append(parts, fmt.Sprintf("%s: %s", group.label, strings.Join(shown, ", ")))
		remaining -= len(shown)
	}
	if omitted > 0 {
		parts = append(parts, fmt.Sprintf("%d more omitted", omitted))
	}
	return strings.Join(parts, "; ")
}

func formatDirtyTargetPreflightReason(sourceIssueID, targetWorktree string, status *git.GitStatus) string {
	return fmt.Sprintf(
		"target branch worktree is not clean: %s. This is target-side dirt, not source worker dirt; child %s evidence may still be valid, but clean the target branch separately before retrying. Next: inspect with `git -C %q status --short`; stash target drift with `git -C %q stash push -u -m \"az issue close target drift before %s\"`; commit it intentionally; or abort and leave the child open/in_review",
		gitStatusSummaryWithDetails(status),
		sourceIssueID,
		targetWorktree,
		targetWorktree,
		sourceIssueID,
	)
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

func (d *Daemon) updateTaskStatusExcludingClose(ctx context.Context, projectID, taskID string, status domain.Status, opts taskStatusUpdateOptions) (domain.Task, []domain.Task, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.Task{}, nil, fmt.Errorf("issue store unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	if status == domain.StatusDone || status == domain.StatusCancelled {
		return domain.Task{}, nil, fmt.Errorf("status %s must be applied with task.close", status)
	}
	if opts.CascadeChildren && status != domain.StatusInReview {
		return domain.Task{}, nil, fmt.Errorf("cascade_children is only supported with status %s", domain.StatusInReview)
	}
	var cascaded []domain.Task
	if status == domain.StatusInReview {
		if err := d.validateTaskDecisionAcknowledgementsForReview(ctx, projectID, taskID); err != nil {
			return domain.Task{}, nil, err
		}
		if err := d.validateTaskActivityForReview(ctx, projectID, taskID, reviewHandoffAllowsBusy(opts, taskID)); err != nil {
			return domain.Task{}, nil, err
		}
		updated, err := d.validateOrCascadeChildrenForReview(ctx, projectID, taskID, opts)
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

func (d *Daemon) validateTaskDecisionAcknowledgementsForReview(ctx context.Context, projectID, taskID string) error {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	if !usesProjectionSource(d.sourceForTaskInvariant(taskInvariantReviewHandoff)) {
		return fmt.Errorf("unsupported review handoff invariant source: %s", d.sourceForTaskInvariant(taskInvariantReviewHandoff))
	}
	if err := d.reconcileDecisionPropagationOutbox(ctx, projectID); err != nil {
		return fmt.Errorf("reconcile material decisions before moving %s to in_review: %w", taskID, err)
	}
	events, err := issueClient.ListIssueDecisionObservationEvents(ctx, taskID)
	if err != nil {
		return fmt.Errorf("inspect material decisions before moving %s to in_review: %w", taskID, err)
	}
	pending := domain.ReducePendingDecisionChanges(events)
	if len(pending) == 0 {
		return nil
	}
	return fmt.Errorf("cannot move issue %s to in_review: %s", taskID, strings.Join(pendingDecisionReadinessReasons(pending), "; "))
}

func reviewHandoffActiveIssue(meta protocol.Metadata, taskID string) string {
	activeIssue := strings.TrimSpace(meta.ClientActiveIssue)
	if activeIssue == "" || !naming.IssueIDsEqual(activeIssue, taskID) {
		return ""
	}
	return activeIssue
}

func reviewHandoffAllowsBusy(opts taskStatusUpdateOptions, taskID string) bool {
	return strings.TrimSpace(opts.AllowBusyReviewHandoffTask) != "" && naming.IssueIDsEqual(opts.AllowBusyReviewHandoffTask, taskID)
}

func (d *Daemon) validateTaskActivityForReview(ctx context.Context, projectID, taskID string, allowBusy bool) error {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	source := d.sourceForTaskInvariant(taskInvariantReviewHandoff)
	task, err := taskForReviewActivityInvariant(ctx, issueClient, projectID, taskID, source)
	if err != nil {
		return fmt.Errorf("inspect session activity before moving %s to in_review: %w", taskID, err)
	}
	if task.Session == nil {
		return nil
	}
	if !task.Session.BlocksReviewHandoff() {
		return nil
	}
	if allowBusy {
		return nil
	}
	activity := strings.TrimSpace(task.Session.DisplayActivity())
	if activity == "" {
		activity = strings.TrimSpace(string(task.Session.State))
	}
	if activity == "" {
		activity = string(domain.SessionBusy)
	}
	activitySource := strings.TrimSpace(task.Session.ActivitySource)
	if activitySource != "" {
		activitySource = " (source: " + activitySource + ")"
	}
	return fmt.Errorf("cannot move issue %s to in_review: session activity is %s%s. Next: leave it in_progress until the session reports idle/done/no-agent activity, or intentionally stop the session before handoff", taskID, activity, activitySource)
}

func taskForReviewActivityInvariant(ctx context.Context, issueClient *issues.Client, projectID, taskID string, source daemonInvariantSource) (domain.Task, error) {
	if issueClient == nil {
		return domain.Task{}, fmt.Errorf("issue store unavailable")
	}
	if !usesProjectionSource(source) {
		return domain.Task{}, fmt.Errorf("unsupported review handoff invariant source: %s", source)
	}
	return issueClient.GetWithRuntime(ctx, projectID, taskID)
}

func (d *Daemon) validateOrCascadeChildrenForReview(ctx context.Context, projectID, taskID string, _ taskStatusUpdateOptions) ([]domain.Task, error) {
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
	return nil, fmt.Errorf("cannot move issue %s to in_review: all live descendants must be terminal: %s", taskID, strings.Join(blocked, "; "))
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
		if !ok || daemonReviewGuardChildReady(child) {
			continue
		}
		blocked = append(blocked, fmt.Sprintf("%s (%s)", child.ID.String(), child.IssueDisplayStatusText()))
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
		if !ok || daemonReviewGuardChildReady(child) {
			continue
		}
		out = append(out, childID)
	}
	return out
}

func daemonReviewGuardChildReady(child domain.Task) bool {
	switch child.IssueDisplayPhase() {
	case domain.IssueDisplayDone, domain.IssueDisplayCancelled:
		return true
	default:
		return false
	}
}

func (d *Daemon) handleTaskClosePreflight(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd taskClosePreflightRequest
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if taskID := strings.TrimSpace(cmd.TaskID); taskID != "" {
		if err := d.refreshTaskCloseSessionRuntime(ctx, projectID, taskID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeUnavailable, fmt.Sprintf("refresh session runtime before close preflight: %v", err)), nil
		}
	}
	result, err := d.validateTaskClosePreflight(ctx, projectID, cmd.TaskID, cmd.taskClosePreflightOptions, req)
	if err != nil {
		return d.errorResponse(req, taskReadErrorCode(err), err.Error()), nil
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
	acceptance, err := d.investigationAcceptance(ctx, projectID, task)
	if err != nil {
		return taskClosePreflightResult{}, err
	}
	if !acceptance.Accepted {
		reasons = append(reasons, acceptance.Reason)
	}
	reasons = append(reasons, daemonCloseGuardRuntimeBlockers(task, opts)...)
	reasons = append(reasons, daemonCloseGuardChildBlockers(task.ID, tasks, opts)...)
	if len(reasons) > 0 {
		if opts.SkipStatusRepairs {
			return taskClosePreflightResult{}, fmt.Errorf("%s", daemonCloseGuardFailureMessage(taskID, reasons, nil))
		}
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
	if opts.AllowIntegratedWorktreeRetry && d.git != nil {
		missing, statErr := taskCloseWorktreePathMissing(worktreePath)
		if statErr != nil {
			return taskClosePreflightResult{}, fmt.Errorf("inspect worktree path before closing %s: %w", taskID, statErr)
		}
		if missing {
			if _, probeErr := d.git.Status(ctx, worktreePath); probeErr != nil {
				return taskClosePreflightResult{Task: task, Worktree: worktreePath, MissingWorktree: true}, nil
			}
		}
	}
	status, err := d.refreshTaskCloseGitStatus(ctx, projectID, worktreePath)
	if err != nil {
		missing, statErr := taskCloseWorktreePathMissing(worktreePath)
		if opts.AllowIntegratedWorktreeRetry && statErr == nil && missing {
			return taskClosePreflightResult{Task: task, Worktree: worktreePath, MissingWorktree: true}, nil
		}
		return taskClosePreflightResult{}, fmt.Errorf("inspect git status before closing %s: %w", taskID, err)
	}
	if reasons := daemonCloseGuardGitBlockers(*status, opts); len(reasons) > 0 {
		if opts.SkipStatusRepairs {
			return taskClosePreflightResult{}, fmt.Errorf("%s", daemonCloseGuardFailureMessage(taskID, reasons, nil))
		}
		repairs, repairErr := d.reopenClosedCloseGuardBlockers(ctx, issueClient, projectID, req, task, tasks, reasons)
		if repairErr != nil {
			return taskClosePreflightResult{}, fmt.Errorf("%s. Failed to move closed blockers back for cleanup: %w", daemonCloseGuardFailureMessage(taskID, reasons, repairs), repairErr)
		}
		return taskClosePreflightResult{}, fmt.Errorf("%s", daemonCloseGuardFailureMessage(taskID, reasons, repairs))
	}
	return taskClosePreflightResult{Task: task, Worktree: worktreePath, Status: *status}, nil
}

func taskCloseWorktreePathMissing(path string) (bool, error) {
	_, err := os.Stat(strings.TrimSpace(path))
	if err == nil {
		return false, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
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
		if !ok || child.Status == domain.StatusDone || child.Status == domain.StatusCancelled {
			continue
		}
		if !daemonCloseGuardCleanChildAutoCloseEligible(child) {
			continue
		}
		childResult, err := d.closeTask(ctx, projectID, taskCloseRequest{
			TaskID:                    child.ID.String(),
			ForceWorktree:             false,
			IgnoreAhead:               false,
			IntegrateBeforeClose:      false,
			CloseCleanChildren:        true,
			CloseOutcome:              cmd.CloseOutcome,
			PromoteBacklogBeforeClose: cmd.CloseOutcome != string(domain.IssueCloseCancelled),
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
	tasks, _, err := d.convergedProjectReadSnapshotForInvariant(ctx, projectID)
	if err != nil {
		return nil, err
	}
	closure := materializedParentChildClosure(tasks, taskID)
	if err := d.refreshProjectReadRuntimeForIssues(ctx, projectID, taskIDsFromTasks(closure)); err != nil {
		return nil, newProjectReadUnavailableError("refresh close-preflight runtime facts: %w", err)
	}
	tasks, _, err = d.convergedProjectReadSnapshotForInvariant(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return materializedParentChildClosure(tasks, taskID), nil
}

func (d *Daemon) investigationAcceptance(ctx context.Context, projectID string, task domain.Task) (domain.InvestigationAcceptance, error) {
	if task.Type != domain.TypeInvestigation {
		return domain.InvestigationAcceptance{Accepted: true}, nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.InvestigationAcceptance{}, fmt.Errorf("issue store unavailable")
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, task.ID.String(), issues.IssueObservationEventListOptions{
		Types: []domain.IssueObservationEventType{
			domain.IssueEventInvestigationDisposition,
			domain.IssueEventReviewCompleted,
			domain.IssueEventHumanInputProvided,
			domain.IssueEventIssueStatusChanged,
		},
		Limit:         5000,
		NewestIDFirst: true,
	})
	if err != nil {
		return domain.InvestigationAcceptance{}, fmt.Errorf("read investigation acceptance for %s: %w", task.ID, err)
	}
	return domain.EvaluateInvestigationAcceptance(task, events), nil
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
		if !task.IssueClosed() {
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
	hasActiveSession := false
	hasChildren := false
	for _, reason := range reasons {
		if strings.Contains(reason, "local changes") || strings.Contains(reason, "conflicts") || strings.Contains(reason, "ahead") {
			hasGitState = true
		}
		if strings.HasPrefix(reason, "session activity") {
			hasActiveSession = true
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
	if hasActiveSession {
		steps = append(steps, "wait for the session projection to report idle/done/terminal activity or intentionally stop the session")
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
	if daemonCloseGuardTaskHasSession(task) {
		if !opts.AllowTargetSession {
			reasons = append(reasons, "issue still has a session")
		} else if !opts.AllowActiveSession {
			if reason := daemonCloseGuardActiveSessionActivityReason(task); reason != "" {
				reasons = append(reasons, reason)
			}
		}
	}
	if !opts.AllowTargetWorktree && daemonCloseGuardTaskHasWorktree(task) {
		reasons = append(reasons, "issue still has a worktree")
	}
	return reasons
}

func daemonCloseGuardActiveSessionActivityReason(task domain.Task) string {
	if task.Session == nil {
		if task.HasTmuxSession {
			return "session activity is unknown"
		}
		return ""
	}
	activity := task.Session.DisplayActivity()
	if activity == "" {
		activity = strings.ToLower(strings.TrimSpace(string(task.Session.State)))
	}
	if daemonCloseGuardSessionActivityAllowsClose(activity) {
		return ""
	}
	if activity == "" {
		activity = "unknown"
	}
	if source := strings.TrimSpace(task.Session.ActivitySource); source != "" {
		return fmt.Sprintf("session activity is %s (source: %s)", activity, source)
	}
	return fmt.Sprintf("session activity is %s", activity)
}

func daemonCloseGuardSessionActivityAllowsClose(activity string) bool {
	switch strings.ToLower(strings.TrimSpace(activity)) {
	case string(domain.SessionIdle), string(domain.SessionDone), string(domain.SessionError), string(domain.SessionPaused), "ended":
		return true
	default:
		return false
	}
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
			if opts.AllowIntegratedWorktreeRetry && d.worktreeAdapter != nil {
				projected, found, projectionErr := d.worktreeAdapter.projectedWorktreeForIssue(ctx, projectID, taskID)
				if projectionErr != nil {
					return "", fmt.Errorf("inspect projected worktree before closing %s: %w", taskID, projectionErr)
				}
				if found && strings.TrimSpace(projected.Path) != "" {
					return strings.TrimSpace(projected.Path), nil
				}
			}
			return "", fmt.Errorf("cannot close issue %s: worktree is projected but path is unavailable. Next: run `az issue close --id %s --force-worktree` after confirming the worktree is gone, then retry", taskID, taskID)
		}
		return "", fmt.Errorf("inspect worktree before closing %s: %w", taskID, err)
	}
	return strings.TrimSpace(worktree.Path), nil
}

func (d *Daemon) refreshTaskCloseGitStatus(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	if d.gitStatusAdapter == nil {
		if d.git == nil {
			return nil, fmt.Errorf("git status service unavailable")
		}
		return d.git.Status(ctx, worktree)
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
	if task.IssueClosed() {
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
	if !task.IssueClosed() {
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
		TaskID  string `json:"task_id"`
		ActorID string `json:"actor_id,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.validateTaskDeletePreflight(ctx, projectID, cmd.TaskID)
	if err != nil {
		return d.errorResponse(req, taskReadErrorCode(err), err.Error()), nil
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
	tasks, err := d.loadTaskDeletePreflightDomainTasks(ctx, projectID, taskID)
	if err != nil {
		return taskDeletePreflightResult{}, fmt.Errorf("inspect runtime attachments before deleting %s: %w", taskID, err)
	}
	task, ok := findDaemonTaskByID(tasks, taskID)
	if !ok {
		return taskDeletePreflightResult{}, fmt.Errorf("issue not found: %s", taskID)
	}
	return taskDeletePreflightResult{Task: task, Blockers: daemonTaskDeleteRuntimeBlockers(task)}, nil
}

func (d *Daemon) loadTaskDeletePreflightDomainTasks(ctx context.Context, projectID, taskID string) ([]domain.Task, error) {
	tasks, _, err := d.convergedProjectReadSnapshotForInvariant(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := d.refreshProjectReadRuntimeForIssues(ctx, projectID, []string{taskID}); err != nil {
		return nil, newProjectReadUnavailableError("refresh delete-preflight runtime facts: %w", err)
	}
	tasks, _, err = d.convergedProjectReadSnapshotForInvariant(ctx, projectID)
	return tasks, err
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

func (d *Daemon) handleTaskSQLiteWAL(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var cmd protocol.TaskSQLiteWALRequest
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
		}
	}
	mode := strings.ToUpper(strings.TrimSpace(cmd.CheckpointMode))
	switch mode {
	case "", string(issues.SQLiteWALCheckpointPassive), string(issues.SQLiteWALCheckpointTruncate):
	default:
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "checkpoint_mode must be PASSIVE or TRUNCATE"), nil
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	diag, err := client.SQLiteWALDiagnostics(ctx)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	out := protocol.TaskSQLiteWALResponse{
		DBPath:              diag.DBPath,
		WALPath:             diag.WALPath,
		WALBytes:            diag.WALBytes,
		CheckpointThreshold: diag.CheckpointThreshold,
		LargeThreshold:      diag.LargeThreshold,
		Large:               diag.Large,
		OpenConnections:     diag.DBStats.OpenConnections,
		InUse:               diag.DBStats.InUse,
		Idle:                diag.DBStats.Idle,
		Stores:              d.sqliteStoreDiagnostics(),
	}
	switch mode {
	case "":
	case string(issues.SQLiteWALCheckpointPassive), string(issues.SQLiteWALCheckpointTruncate):
		stats, err := client.CheckpointSQLiteWAL(ctx, issues.SQLiteWALCheckpointMode(mode))
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		out.WALBytes = stats.WALBytesAfter
		out.Large = stats.WALBytesAfter >= out.LargeThreshold
		out.Checkpoint = &protocol.TaskSQLiteWALCheckpointInfo{
			Mode:                string(stats.Mode),
			Busy:                stats.Busy,
			LogFrames:           stats.LogFrames,
			CheckpointedFrames:  stats.CheckpointedFrame,
			WALBytesBefore:      stats.WALBytesBefore,
			WALBytesAfter:       stats.WALBytesAfter,
			DurationMillisecond: stats.Duration.Milliseconds(),
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.currentRevision(projectID)
	return resp, nil
}

func (d *Daemon) handleTaskGraphReadiness(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	startedAt := time.Now()
	var cmd struct {
		TaskID  string `json:"task_id"`
		ActorID string `json:"actor_id,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	evalStartedAt := time.Now()
	actorID := strings.TrimSpace(cmd.ActorID)
	if actorID == "" {
		actorID = strings.TrimSpace(req.Meta.ClientActor)
	}
	result, err := d.taskGraphReadinessForActor(ctx, projectID, cmd.TaskID, actorID)
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.graph_readiness.evaluate", evalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "root_issue_id", cmd.TaskID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	result.Revision = d.currentRevision(projectID)
	finalizeTaskGraphReadinessSource(&result)
	marshalStartedAt := time.Now()
	body, err := json.Marshal(result)
	latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.graph_readiness.marshal_result", marshalStartedAt, "command", req.Command, "request_id", req.RequestID, "project_id", projectID, "root_issue_id", cmd.TaskID, "runnable_count", len(result.Runnable), "active_count", len(result.Active))
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = result.Revision
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task graph readiness completed",
			"project_id", projectID,
			"root_issue_id", strings.TrimSpace(cmd.TaskID),
			"runnable_count", len(result.Runnable),
			"active_count", len(result.Active),
			"nested_root_count", len(result.NestedRoots),
			"elapsed_ms", time.Since(startedAt).Milliseconds(),
		)
	}
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
	return d.taskReadiness(ctx, projectID, issueID, repoDir, true)
}

// taskReviewAcceptanceReadiness evaluates the immutable patch-review inputs.
// Aggregate validation is deliberately excluded: for configured-base targets
// it is daemon-owned continuation work after the reviewer accepts this patch.
func (d *Daemon) taskReviewAcceptanceReadiness(ctx context.Context, projectID, issueID, repoDir string) (taskIntegrationReadinessResult, error) {
	return d.taskReadiness(ctx, projectID, issueID, repoDir, false)
}

func (d *Daemon) taskReadiness(ctx context.Context, projectID, issueID, repoDir string, requireAggregate bool) (taskIntegrationReadinessResult, error) {
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

	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return taskIntegrationReadinessResult{}, fmt.Errorf("inspect issue integration readiness: issue store unavailable")
	}
	var latestAggregate *domain.ValidationRequest
	if requireAggregate && d.operationRuntime != nil {
		validationStore, storeErr := d.validationProjectionStore()
		if storeErr != nil {
			return taskIntegrationReadinessResult{}, fmt.Errorf("inspect aggregate validation projection: %w", storeErr)
		}
		aggregate, aggregateErr := validationStore.LatestReviewValidation(ctx, projectID, task.ID.String(), time.Now().UTC(), defaultValidationLeaseTTL)
		if aggregateErr != nil {
			return taskIntegrationReadinessResult{}, fmt.Errorf("inspect aggregate validation evidence: %w", aggregateErr)
		}
		if aggregate == nil {
			return taskIntegrationReadinessResult{IssueID: task.ID.String(), ParentIssueID: parentIssueID, Ready: false, ContextRisk: contextRisk, Reasons: []string{"no aggregate validation is present in the daemon validation projection"}}, nil
		}
		if aggregate.State != domain.ValidationRequestCompleted || !aggregate.Evidence.Present || aggregate.Evidence.SourceRevision != aggregate.SourceRevision {
			reasons := []string{fmt.Sprintf("aggregate validation %s is not valid integration evidence", aggregate.RequestID)}
			if aggregate.State != domain.ValidationRequestCompleted {
				reasons = append(reasons, fmt.Sprintf("aggregate validation outcome is %s", aggregate.State))
			}
			if !aggregate.Evidence.Present {
				reasons = append(reasons, "aggregate validation did not record machine-load evidence")
			}
			if aggregate.Evidence.SourceRevision != aggregate.SourceRevision {
				reasons = append(reasons, fmt.Sprintf("aggregate validation evidence revision %q does not match candidate revision %q", aggregate.Evidence.SourceRevision, aggregate.SourceRevision))
			}
			return taskIntegrationReadinessResult{IssueID: task.ID.String(), ParentIssueID: parentIssueID, Ready: false, ContextRisk: contextRisk, Reasons: reasons, AggregateValidation: aggregate}, nil
		}
		if strings.TrimSpace(repoDir) == "" {
			return taskIntegrationReadinessResult{IssueID: task.ID.String(), ParentIssueID: parentIssueID, Ready: false, ContextRisk: contextRisk, Reasons: []string{"candidate worktree is required to bind aggregate validation to the exact revision"}, AggregateValidation: aggregate}, nil
		}
		if d.git == nil {
			return taskIntegrationReadinessResult{IssueID: task.ID.String(), ParentIssueID: parentIssueID, Ready: false, ContextRisk: contextRisk, Reasons: []string{"Git authority is unavailable for exact aggregate validation revision binding"}, AggregateValidation: aggregate}, nil
		}
		candidateRevision, revisionErr := d.git.HeadRevision(ctx, repoDir)
		if revisionErr != nil {
			return taskIntegrationReadinessResult{IssueID: task.ID.String(), ParentIssueID: parentIssueID, Ready: false, ContextRisk: contextRisk, Reasons: []string{fmt.Sprintf("resolve exact candidate revision: %v", revisionErr)}, AggregateValidation: aggregate}, nil
		}
		if candidateRevision != aggregate.SourceRevision {
			return taskIntegrationReadinessResult{IssueID: task.ID.String(), ParentIssueID: parentIssueID, Ready: false, ContextRisk: contextRisk, Reasons: []string{fmt.Sprintf("aggregate validation revision %s does not match exact candidate revision %s", aggregate.SourceRevision, candidateRevision)}, AggregateValidation: aggregate}, nil
		}
		candidateStatus, statusErr := d.git.Status(ctx, repoDir)
		if statusErr != nil {
			return taskIntegrationReadinessResult{IssueID: task.ID.String(), ParentIssueID: parentIssueID, Ready: false, ContextRisk: contextRisk, Reasons: []string{fmt.Sprintf("inspect exact candidate tree: %v", statusErr)}, AggregateValidation: aggregate}, nil
		}
		if candidateStatus.HasChanges {
			return taskIntegrationReadinessResult{IssueID: task.ID.String(), ParentIssueID: parentIssueID, Ready: false, ContextRisk: contextRisk, Reasons: []string{"aggregate validation does not bind the current dirty candidate tree to an exact revision"}, AggregateValidation: aggregate}, nil
		}
		latestAggregate = aggregate
	}
	if !orchestrationSnapshotPrepared(ctx) {
		if err := d.reconcileDecisionPropagationOutbox(ctx, projectID); err != nil {
			return taskIntegrationReadinessResult{}, fmt.Errorf("reconcile issue decision propagation: %w", err)
		}
	}
	decisionEvents, err := issueClient.ListIssueDecisionObservationEvents(ctx, task.ID.String())
	if err != nil {
		return taskIntegrationReadinessResult{}, fmt.Errorf("inspect issue decision acknowledgements: %w", err)
	}
	pendingDecisions := domain.ReducePendingDecisionChanges(decisionEvents)
	decisionReasons := pendingDecisionReadinessReasons(pendingDecisions)
	reviewEvents, err := issueClient.ListIssueReviewReadyObservationEvents(ctx, task.ID.String())
	if err != nil {
		return taskIntegrationReadinessResult{}, fmt.Errorf("inspect issue integration evidence: %w", err)
	}
	if evidence := domain.ReduceReviewReadyEvidence(reviewEvents).LatestEvidence; evidence != nil {
		evt := evidence.SourceEvent
		packet, validation := evidence.Evidence, evidence.Validation
		if requireAggregate {
			validateWorkerAggregateRequest(&validation, packet, latestAggregate)
		}
		if validation.Complete {
			return taskIntegrationReadinessResult{
				IssueID:          task.ID.String(),
				ParentIssueID:    parentIssueID,
				Ready:            len(pendingDecisions) == 0,
				ContextRisk:      contextRisk,
				Reasons:          decisionReasons,
				PendingDecisions: pendingDecisions,
				EvidenceEventID:  evt.ID,
				EvidenceSource:   "issue_event",
				EvidencePacket:   &packet,
			}, nil
		}
		reasons := append([]string{fmt.Sprintf("issue %s is not closed", task.ID.String())}, decisionReasons...)
		if validation.Found {
			reasons = append(reasons, fmt.Sprintf("worker evidence packet in issue event %d is incomplete", evt.ID))
		} else {
			reasons = append(reasons, fmt.Sprintf("issue evidence event %d does not contain a structured worker_evidence.v1 packet", evt.ID))
		}
		reasons = append(reasons, validation.Problems()...)
		return taskIntegrationReadinessResult{
			IssueID:                task.ID.String(),
			ParentIssueID:          parentIssueID,
			Ready:                  false,
			ContextRisk:            contextRisk,
			Reasons:                reasons,
			EvidenceEventID:        evt.ID,
			EvidenceSource:         "issue_event",
			EvidenceIncomplete:     true,
			EvidenceMissingFields:  validation.Missing,
			EvidenceInvalidReasons: validation.Invalid,
			PendingDecisions:       pendingDecisions,
		}, nil
	}

	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return taskIntegrationReadinessResult{
			IssueID:       task.ID.String(),
			ParentIssueID: parentIssueID,
			Ready:         false,
			ContextRisk:   contextRisk,
			Reasons: append([]string{
				fmt.Sprintf("issue %s is not closed", task.ID.String()),
				"repo_dir is required to inspect worker-integration-ready mailbox evidence when no issue evidence.submitted record exists",
			}, decisionReasons...),
			PendingDecisions: pendingDecisions,
		}, nil
	}
	mailboxRepoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if mailboxRepoDir == "" {
		return taskIntegrationReadinessResult{}, fmt.Errorf("resolve authoritative project mailbox root for %s", projectID)
	}
	events, err := readMailboxEvents(mailboxRepoDir, parentIssueID)
	if err != nil {
		return taskIntegrationReadinessResult{}, fmt.Errorf("list mailbox events for %s: %w", parentIssueID, err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		evt := events[i]
		if naming.IssueIDsEqual(evt.IssueID, task.ID.String()) && daemonWorkerIntegrationReadyMailType(evt.Type) {
			if evt.Payload != nil && evt.Payload["publication"] == reviewReadyReplayPublication {
				continue
			}
			packet, validation := domain.ParseWorkerEvidencePacketBody(evt.Body)
			if requireAggregate {
				validateWorkerAggregateRequest(&validation, packet, latestAggregate)
			}
			if validation.Complete {
				return taskIntegrationReadinessResult{
					IssueID:          task.ID.String(),
					ParentIssueID:    parentIssueID,
					Ready:            len(pendingDecisions) == 0,
					ContextRisk:      contextRisk,
					Reasons:          decisionReasons,
					PendingDecisions: pendingDecisions,
					EvidenceEventSeq: evt.Seq,
					EvidencePacket:   &packet,
				}, nil
			}
			reasons := append([]string{fmt.Sprintf("issue %s is not closed", task.ID.String())}, decisionReasons...)
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
				EvidenceSource:         "mailbox",
				EvidenceIncomplete:     true,
				EvidenceMissingFields:  validation.Missing,
				EvidenceInvalidReasons: validation.Invalid,
				PendingDecisions:       pendingDecisions,
			}, nil
		}
	}
	return taskIntegrationReadinessResult{
		IssueID:       task.ID.String(),
		ParentIssueID: parentIssueID,
		Ready:         false,
		ContextRisk:   contextRisk,
		Reasons: append([]string{
			fmt.Sprintf("issue %s is not closed", task.ID.String()),
			fmt.Sprintf("no worker-integration-ready mailbox event found under parent %s for %s", parentIssueID, task.ID.String()),
		}, decisionReasons...),
		PendingDecisions: pendingDecisions,
	}, nil
}

func pendingDecisionReadinessReasons(pending []domain.PendingDecisionChange) []string {
	reasons := make([]string, 0, len(pending))
	for _, change := range pending {
		reasons = append(reasons, fmt.Sprintf("stale material decision %s revision %d is unacknowledged; %s", change.DecisionID, change.Revision, change.RequiredAction))
	}
	return reasons
}

func validateWorkerAggregateRequest(validation *domain.WorkerEvidenceParseResult, packet domain.WorkerEvidencePacket, latest *domain.ValidationRequest) {
	if validation == nil {
		return
	}
	var problem string
	if packet.AggregateValidation == nil {
		// The daemon projection is the authority for aggregate validation. Older
		// and issue-recorded worker packets do not have to duplicate that proof;
		// when they do, the identity and revision are still checked below.
		return
	} else if latest == nil {
		problem = fmt.Sprintf("aggregate_validation request %s is not present in the daemon validation projection", packet.AggregateValidation.RequestID)
	} else if packet.AggregateValidation.RequestID != latest.RequestID {
		problem = fmt.Sprintf("aggregate_validation request %s does not match latest daemon request %s", packet.AggregateValidation.RequestID, latest.RequestID)
	} else if packet.AggregateValidation.SourceRevision != latest.SourceRevision {
		problem = fmt.Sprintf("aggregate_validation source revision %q does not match candidate revision %q", packet.AggregateValidation.SourceRevision, latest.SourceRevision)
	}
	if problem != "" {
		validation.Invalid = append(validation.Invalid, problem)
		validation.Complete = false
	}
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
		TaskID                 string `json:"task_id"`
		BaseBranch             string `json:"base_branch,omitempty"`
		AllowBaseForChild      bool   `json:"allow_base_for_child,omitempty"`
		RequireHumanAcceptance bool   `json:"require_human_acceptance,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	result, err := d.taskMergeBaseTarget(ctx, projectID, cmd.TaskID, cmd.BaseBranch, cmd.AllowBaseForChild)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if cmd.RequireHumanAcceptance && result.TargetID == "base" {
		if strings.EqualFold(strings.TrimSpace(d.workflowModeForProject(projectID)), "origin") {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf(
				"refusing direct base integration for %s because git workflow mode is origin; %s",
				cmd.TaskID, daemonOriginBaseIntegrationGuidance(cmd.TaskID, result.Branch),
			)), nil
		}
		accepted, acceptErr := d.hasDurableBaseIntegrationAcceptance(ctx, projectID, cmd.TaskID)
		if acceptErr != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, acceptErr.Error()), nil
		}
		if !accepted {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("refusing root issue %s integration into base without durable human acceptance; record `human.input_provided` with data {\"base_integration_accepted\":true}", cmd.TaskID)), nil
		}
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

func (d *Daemon) hasDurableBaseIntegrationAcceptance(ctx context.Context, projectID, issueID string) (bool, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return false, fmt.Errorf("issue store unavailable")
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventHumanInputProvided}, Limit: 100})
	if err != nil {
		return false, fmt.Errorf("read durable human acceptance for %s: %w", issueID, err)
	}
	for _, event := range events {
		if accepted, ok := event.Payload["base_integration_accepted"].(bool); ok && accepted {
			return true, nil
		}
	}
	return false, nil
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
	tasks, _, err := d.convergedProjectReadSnapshotForInvariant(ctx, projectID)
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
	completionEvidence, err := d.taskGraphDurableCompletionEvidence(ctx, projectID, daemonTaskGraphDirectWorkerLeafIDs(rootID, byID, children))
	if err != nil {
		return taskCompleteCheckResult{}, err
	}
	ready, err := daemonTaskGraphReadinessFromIndexesWithCompletionEvidence(rootID, byID, children, completionEvidence)
	if err != nil {
		return taskCompleteCheckResult{}, err
	}

	desc := daemonTaskGraphDescendants(rootID, children)
	staleCloseable := daemonTaskGraphStaleCloseableCandidatesWithEvidence(rootID, byID, children, completionEvidence)
	acceptanceByIssue := make(map[string]domain.InvestigationAcceptance)
	for _, id := range desc {
		task := byID[id]
		if task.Type != domain.TypeInvestigation || task.IssueClosed() {
			continue
		}
		acceptance, err := d.investigationAcceptance(ctx, projectID, task)
		if err != nil {
			return taskCompleteCheckResult{}, err
		}
		acceptanceByIssue[id.String()] = acceptance
	}
	filteredStale := staleCloseable[:0]
	for _, candidate := range staleCloseable {
		acceptance, investigation := acceptanceByIssue[candidate.IssueID]
		if investigation && !acceptance.Accepted {
			continue
		}
		if investigation {
			candidate.Evidence = append(candidate.Evidence, "investigation disposition="+string(acceptance.Disposition), "durable acceptance satisfied")
		}
		filteredStale = append(filteredStale, candidate)
	}
	staleCloseable = filteredStale
	staleCloseableSet := make(map[string]struct{}, len(staleCloseable))
	for _, candidate := range staleCloseable {
		staleCloseableSet[candidate.IssueID] = struct{}{}
	}
	openDescendants := make([]string, 0, len(desc))
	activeSessions := make([]string, 0, len(desc))
	for _, id := range desc {
		task := byID[id]
		if !task.IssueClosed() {
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
	for issueID, acceptance := range acceptanceByIssue {
		if !acceptance.Accepted {
			reasons = append(reasons, fmt.Sprintf("investigation %s acceptance blocked: %s", issueID, acceptance.Reason))
		}
	}
	sort.Strings(reasons)
	return taskCompleteCheckResult{
		RootIssueID:            rootID.String(),
		Pass:                   len(reasons) == 0,
		Reasons:                reasons,
		Advice:                 daemonTaskCompletionAdvice(rootID.String(), ready.Runnable, ready.NestedRoots, openDescendants, activeSessions, staleCloseable),
		StaleCloseableChildren: staleCloseable,
	}, nil
}

func (d *Daemon) taskGraphReadiness(ctx context.Context, projectID, rootIssueID string) (taskGraphReadinessResult, error) {
	return d.taskGraphReadinessForActor(ctx, projectID, rootIssueID, "")
}

func (d *Daemon) taskGraphReadinessForActor(ctx context.Context, projectID, rootIssueID, actorID string) (taskGraphReadinessResult, error) {
	projectID = d.canonicalProjectID(projectID)
	rootIssueID = strings.TrimSpace(rootIssueID)
	actorID = strings.TrimSpace(actorID)
	cacheKey := taskGraphReadinessLoadKey(projectID, rootIssueID, actorID)

	for {
		revision := d.currentRevision(projectID)
		loadKey := fmt.Sprintf("%s\x00%d", cacheKey, revision)

		d.taskGraphReadinessMu.Lock()
		if d.taskGraphReadinessCache == nil {
			d.taskGraphReadinessCache = map[string]taskGraphReadinessCacheEntry{}
		}
		if cached, ok := d.taskGraphReadinessCache[cacheKey]; ok && cached.revision == revision && (cached.expiresAt.IsZero() || time.Now().Before(cached.expiresAt)) {
			d.taskGraphReadinessMu.Unlock()
			if !d.materializedReadsEnabled() {
				if err := d.validateTaskGraphRuntime(ctx, projectID, cached.result.scopeIssueIDs, revision); err != nil && d.cfg.Logger != nil {
					d.cfg.Logger.Debug("validate cached graph readiness runtime", "project_id", projectID, "root_issue_id", rootIssueID, "error", err)
				}
			}
			if d.currentRevision(projectID) != revision {
				continue
			}
			return cloneTaskGraphReadinessResult(cached.result), nil
		}
		if d.taskGraphReadinessLoads == nil {
			d.taskGraphReadinessLoads = map[string]*taskGraphReadinessLoad{}
		}
		if load := d.taskGraphReadinessLoads[loadKey]; load != nil {
			d.taskGraphReadinessMu.Unlock()
			waitStartedAt := time.Now()
			select {
			case <-ctx.Done():
				latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.graph_readiness.singleflight_wait", waitStartedAt, "project_id", projectID, "root_issue_id", rootIssueID, "actor_id", actorID, "shared_load", true, "error", ctx.Err())
				return taskGraphReadinessResult{}, ctx.Err()
			case <-load.done:
				latencytrace.LogPhaseContext(ctx, d.cfg.Logger, "daemon", "task.graph_readiness.singleflight_wait", waitStartedAt, "project_id", projectID, "root_issue_id", rootIssueID, "actor_id", actorID, "shared_load", true, "error", load.err)
				if load.err != nil {
					return taskGraphReadinessResult{}, load.err
				}
				if d.currentRevision(projectID) != revision {
					continue
				}
				return cloneTaskGraphReadinessResult(load.result), nil
			}
		}
		load := &taskGraphReadinessLoad{done: make(chan struct{})}
		d.taskGraphReadinessLoads[loadKey] = load
		d.taskGraphReadinessMu.Unlock()

		result, err := d.buildTaskGraphReadinessForActor(ctx, projectID, rootIssueID, actorID)
		load.result = cloneTaskGraphReadinessResult(result)
		load.err = err
		finishedRevision := d.currentRevision(projectID)

		d.taskGraphReadinessMu.Lock()
		delete(d.taskGraphReadinessLoads, loadKey)
		if err == nil && finishedRevision == revision {
			d.taskGraphReadinessCache[cacheKey] = taskGraphReadinessCacheEntry{
				revision:  revision,
				expiresAt: result.cacheExpiresAt,
				result:    cloneTaskGraphReadinessResult(result),
			}
		}
		close(load.done)
		d.taskGraphReadinessMu.Unlock()

		return result, err
	}
}

const taskGraphRuntimeValidationTTL = time.Second

// validateTaskGraphRuntime bounds the hybrid projection/tmux observation work
// shared by rooted watches, project watches, and finite snapshot readers. The
// projection revision remains part of the authority key, while the short TTL
// ensures out-of-band tmux changes are still observed without making every
// poll independently query SQLite and tmux.
func (d *Daemon) validateTaskGraphRuntime(ctx context.Context, projectID string, issueIDs []string, revision uint64) error {
	if d == nil || d.tmux == nil || d.sessionRuntimeStateStoreIfConfigured(projectID) == nil || len(issueIDs) == 0 {
		return nil
	}
	projectID = d.canonicalProjectID(projectID)
	ids := uniqueStrings(append([]string(nil), issueIDs...))
	sort.Strings(ids)
	cacheKey := projectID + "\x00" + strings.Join(ids, "\x00")
	loadKey := fmt.Sprintf("%s\x00%d", cacheKey, revision)

	d.taskGraphRuntimeValidationMu.Lock()
	if d.taskGraphRuntimeValidations == nil {
		d.taskGraphRuntimeValidations = map[string]taskGraphRuntimeValidationEntry{}
	}
	if cached, ok := d.taskGraphRuntimeValidations[cacheKey]; ok && cached.revision == revision && time.Since(cached.validatedAt) <= taskGraphRuntimeValidationTTL {
		d.taskGraphRuntimeValidationMu.Unlock()
		return nil
	}
	if d.taskGraphRuntimeValidationLoads == nil {
		d.taskGraphRuntimeValidationLoads = map[string]*taskGraphRuntimeValidationLoad{}
	}
	if load := d.taskGraphRuntimeValidationLoads[loadKey]; load != nil {
		d.taskGraphRuntimeValidationMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-load.done:
			return load.err
		}
	}
	load := &taskGraphRuntimeValidationLoad{done: make(chan struct{})}
	d.taskGraphRuntimeValidationLoads[loadKey] = load
	d.taskGraphRuntimeValidationMu.Unlock()

	err := d.refreshIssueSessionRuntimeState(ctx, projectID, ids)
	load.err = err
	d.taskGraphRuntimeValidationMu.Lock()
	delete(d.taskGraphRuntimeValidationLoads, loadKey)
	if err == nil && d.currentRevision(projectID) == revision {
		d.taskGraphRuntimeValidations[cacheKey] = taskGraphRuntimeValidationEntry{revision: revision, validatedAt: time.Now()}
	}
	close(load.done)
	d.taskGraphRuntimeValidationMu.Unlock()
	return err
}

func (d *Daemon) buildTaskGraphReadinessForActor(ctx context.Context, projectID, rootIssueID, actorID string) (taskGraphReadinessResult, error) {
	var (
		tasks  []domain.Task
		source protocol.MaterializedSnapshotMetadata
		err    error
	)
	if d.materializedReadsEnabled() {
		var materialized []domain.Task
		materialized, source, err = d.projectReadSnapshot(projectID)
		tasks = materializedParentChildClosure(materialized, rootIssueID)
	} else {
		tasks, err = d.loadTaskGraphReadinessDomainTasks(ctx, projectID, rootIssueID)
	}
	if err != nil {
		return taskGraphReadinessResult{}, fmt.Errorf("inspect issue graph readiness: %w", err)
	}
	waitingIssues, err := d.taskGraphUnresolvedInteractions(ctx, projectID)
	if err != nil {
		return taskGraphReadinessResult{}, fmt.Errorf("refresh interaction readiness projection: %w", err)
	}
	readinessContext, err := d.captureTaskGraphReadinessContext(ctx, projectID, tasks, []string{rootIssueID}, waitingIssues, true)
	if err != nil {
		return taskGraphReadinessResult{}, err
	}
	ready, err := deriveTaskGraphReadinessFromTasksForActor(rootIssueID, actorID, tasks, readinessContext)
	if err != nil {
		return taskGraphReadinessResult{}, err
	}
	ready.scopeIssueIDs = taskIDsFromTasks(tasks)
	if d.operationRuntime != nil && d.operationRuntime.store != nil {
		publicationStore, publicationErr := d.publicationStoreForProject(projectID)
		if publicationErr != nil {
			return taskGraphReadinessResult{}, fmt.Errorf("resolve publication queue projection: %w", publicationErr)
		}
		publications, publicationErr := publicationStore.PublicationOperations(ctx, projectID, "", true)
		if publicationErr != nil {
			return taskGraphReadinessResult{}, fmt.Errorf("load publication queue projection: %w", publicationErr)
		}
		allowed := make(map[string]struct{}, len(ready.scopeIssueIDs))
		for _, issueID := range ready.scopeIssueIDs {
			allowed[issueID] = struct{}{}
		}
		for _, publication := range publications {
			if _, ok := allowed[publication.IssueID]; ok {
				ready.PublicationQueue = append(ready.PublicationQueue, publication)
			}
		}
	}
	ready.Source = source
	ready.cacheExpiresAt = taskGraphReadinessOwnershipExpiry(tasks, readinessContext.capturedAt)
	return ready, nil
}

func (d *Daemon) taskGraphUnresolvedInteractions(ctx context.Context, projectID string) (map[string]struct{}, error) {
	if d.taskGraphUnresolvedInteractionIDs != nil {
		return d.taskGraphUnresolvedInteractionIDs(ctx, d.canonicalProjectID(projectID))
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	return issueClient.UnresolvedInteractionIssueIDs(ctx)
}

type taskGraphReadinessContext struct {
	capturedAt             time.Time
	waitingIssues          map[string]struct{}
	pendingStarts          map[string]taskGraphPendingStart
	startProgressByIssue   map[string]taskGraphSessionStartProgress
	failedStartsByIssue    map[string]taskGraphStartFailure
	containmentRisksByRoot map[string][]taskContainmentRisk
	completionByIssue      map[string]taskDurableCompletionEvidence
	issueEventsByIssue     map[string][]domain.IssueObservationEvent
	stewardshipByIssue     map[string][]domain.IssueObservationEvent
	mailEventsByRoot       map[string]map[string][]daemonMailEvent
	initCommandsConfigured bool
}

func (d *Daemon) captureTaskGraphReadinessContext(ctx context.Context, projectID string, tasks []domain.Task, roots []string, waitingIssues map[string]struct{}, includeMailbox bool) (taskGraphReadinessContext, error) {
	var err error
	captured := taskGraphReadinessContext{
		capturedAt:             time.Now().UTC(),
		waitingIssues:          cloneStringStructMap(waitingIssues),
		containmentRisksByRoot: make(map[string][]taskContainmentRisk, len(roots)),
		issueEventsByIssue:     make(map[string][]domain.IssueObservationEvent, len(tasks)),
		stewardshipByIssue:     make(map[string][]domain.IssueObservationEvent, len(tasks)),
		mailEventsByRoot:       make(map[string]map[string][]daemonMailEvent, len(roots)),
		initCommandsConfigured: len(d.runtimeConfigForProject(projectID).SessionSyncInitCommands) > 0,
	}
	captured.pendingStarts, err = d.taskGraphPendingSessionStarts(ctx, projectID)
	if err != nil {
		return taskGraphReadinessContext{}, orchestrationAdmissionBoundaryError(protocol.OrchestrationAdmissionOperationsStore, fmt.Errorf("list pending session-start operations: %w", err))
	}
	captured.startProgressByIssue = d.sessionStartProgressByIssueAt(ctx, projectID, captured.capturedAt)
	captured.failedStartsByIssue = d.failedSessionStartByIssue(ctx, projectID)
	var worktrees []git.Worktree
	if d != nil && d.materializedReadsEnabled() {
		projected := d.projectReadWorktrees(projectID)
		worktrees = make([]git.Worktree, 0, len(projected))
		for _, worktree := range projected {
			worktrees = append(worktrees, worktree)
		}
	} else if d != nil && d.taskGraphWorktrees != nil {
		worktrees, err = d.taskGraphWorktrees(ctx, projectID)
	}
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("readiness context worktree list failed", "project_id", projectID, "error", err)
		}
	}
	captured.containmentRisksByRoot = captureProjectedTaskGraphContainmentRisks(tasks, roots, worktrees, err)
	completionIssueIDs := make([]naming.IssueID, 0, len(tasks))
	for _, task := range tasks {
		if !task.ID.IsZero() {
			completionIssueIDs = append(completionIssueIDs, task.ID)
		}
	}
	captured.completionByIssue, err = d.taskGraphDurableCompletionEvidence(ctx, projectID, completionIssueIDs)
	if err != nil {
		return taskGraphReadinessContext{}, orchestrationAdmissionBoundaryError(protocol.OrchestrationAdmissionObservationProjection, fmt.Errorf("load durable completion evidence: %w", err))
	}
	if includeMailbox {
		for _, rootIssueID := range uniqueNonEmpty(roots) {
			captured.mailEventsByRoot[rootIssueID] = d.workerObservationMailboxEvents(rootIssueID)
		}
	} else if d.taskGraphObservationEvents == nil && !orchestrationSnapshotPrepared(ctx) {
		repoDir := strings.TrimSpace(d.resolveRepoDirForProject(projectID))
		if err := d.ensureLegacyMailboxObservationProjection(ctx, projectID, repoDir); err != nil {
			return taskGraphReadinessContext{}, orchestrationAdmissionBoundaryError(protocol.OrchestrationAdmissionObservationProjection, fmt.Errorf("project legacy mailbox observation projection: %w", err))
		}
	}
	taskIDs := taskIDsFromTasks(tasks)
	if d.taskGraphObservationEvents != nil {
		observationCapture := d.taskGraphObservationEvents(ctx, projectID, taskIDs)
		captured.issueEventsByIssue = observationCapture.RecentByIssue
		captured.stewardshipByIssue = observationCapture.StewardshipByIssue
	} else if issueClient := d.issueClientForProject(projectID); issueClient != nil {
		observationCapture, captureErr := issueClient.CaptureProjectIssueObservationEvents(ctx, taskIDs, 50, 20)
		if captureErr != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Debug("readiness context observation capture failed", "project_id", projectID, "issue_count", len(taskIDs), "error", captureErr)
			}
			return taskGraphReadinessContext{}, orchestrationAdmissionBoundaryError(protocol.OrchestrationAdmissionObservationProjection, fmt.Errorf("capture project observation projection: %w", captureErr))
		}
		captured.issueEventsByIssue = observationCapture.RecentByIssue
		captured.stewardshipByIssue = observationCapture.StewardshipByIssue
	}
	return captured, nil
}

func (d *Daemon) listTaskGraphOperations(ctx context.Context, query daemonops.Query) ([]daemonops.Record, error) {
	query.ProjectID = d.canonicalProjectID(query.ProjectID)
	if d.taskGraphOperationList != nil {
		return d.taskGraphOperationList(ctx, query)
	}
	if d == nil || d.operationRuntime == nil || d.operationRuntime.manager == nil {
		return nil, nil
	}
	return d.operationRuntime.manager.List(ctx, query)
}

func cloneStringStructMap(in map[string]struct{}) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func deriveTaskGraphReadinessFromTasksForActor(rootIssueID, actorID string, tasks []domain.Task, captured taskGraphReadinessContext) (taskGraphReadinessResult, error) {
	rootID, byID, children, err := daemonTaskGraphIndexes(rootIssueID, tasks)
	if err != nil {
		return taskGraphReadinessResult{}, err
	}
	ready, err := daemonTaskGraphReadinessFromIndexesForActorWithCompletionEvidence(rootID, byID, children, actorID, captured.capturedAt, captured.completionByIssue)
	if err != nil {
		return taskGraphReadinessResult{}, err
	}
	if len(captured.waitingIssues) > 0 {
		runnable := ready.Runnable[:0]
		for _, issueID := range ready.Runnable {
			if _, waiting := captured.waitingIssues[issueID]; waiting {
				ready.Blocked[issueID] = "unresolved interaction request requires human decision"
				continue
			}
			runnable = append(runnable, issueID)
		}
		ready.Runnable = runnable
	}
	if len(captured.pendingStarts) > 0 {
		ready = daemonTaskGraphApplyPendingStarts(ready, captured.pendingStarts)
	}
	ready.applySessionStartProgress(captured.startProgressByIssue)
	ready.NestedRoots = daemonTaskGraphNestedRoots(ready.NestedRoots, byID, captured.startProgressByIssue, captured.failedStartsByIssue, captured)
	ready.ActiveSessions = daemonTaskGraphActiveSessions(ready.Active, byID, captured.startProgressByIssue, captured)
	ready.ActiveSessions = append(ready.ActiveSessions, daemonTaskGraphCleanupPendingSessions(rootID, byID, children)...)
	ready.SessionStartProgress = daemonTaskGraphSessionStartProgressList(rootID, children, captured.startProgressByIssue)
	ready.ContainmentRisks = append([]taskContainmentRisk(nil), captured.containmentRisksByRoot[rootIssueID]...)
	ready.WorkerObservations = daemonTaskGraphWorkerObservations(rootID, byID, children, ready, captured)
	ready.Capacity = daemonTaskGraphCapacitySummary(ready)
	return ready, nil
}

func finalizeTaskGraphReadinessSource(result *taskGraphReadinessResult) {
	if result == nil || result.Source.IssueChecksum == "" {
		return
	}
	normalized := *result
	normalized.Revision = 0
	normalized.Source.SemanticChecksum = ""
	result.Source.SemanticChecksum = checksumJSON(normalized)
}

func taskGraphReadinessOwnershipExpiry(tasks []domain.Task, now time.Time) time.Time {
	var earliest time.Time
	for _, task := range tasks {
		if task.Ownership == nil || task.Ownership.ExpiresAt == nil || !task.Ownership.ExpiresAt.After(now) {
			continue
		}
		if earliest.IsZero() || task.Ownership.ExpiresAt.Before(earliest) {
			earliest = task.Ownership.ExpiresAt.UTC()
		}
	}
	return earliest
}

func taskGraphReadinessLoadKey(projectID, rootIssueID, actorID string) string {
	return strings.TrimSpace(projectID) + "\x00" + strings.TrimSpace(rootIssueID) + "\x00" + strings.TrimSpace(actorID)
}

func (d *Daemon) taskGraphReadinessCacheExpiry(projectID, rootIssueID, actorID string, revision uint64) time.Time {
	cacheKey := taskGraphReadinessLoadKey(d.canonicalProjectID(projectID), rootIssueID, actorID)
	d.taskGraphReadinessMu.Lock()
	defer d.taskGraphReadinessMu.Unlock()
	cached, ok := d.taskGraphReadinessCache[cacheKey]
	if !ok || cached.revision != revision {
		return time.Time{}
	}
	return cached.expiresAt
}

func (d *Daemon) loadTaskGraphReadinessDomainTasks(ctx context.Context, projectID, rootIssueID string) ([]domain.Task, error) {
	if d.materializedReadsEnabled() {
		tasks, _, err := d.projectReadSnapshot(projectID)
		if err != nil {
			return nil, err
		}
		return materializedParentChildClosure(tasks, rootIssueID), nil
	}
	// Explicit compatibility exception: production starts project materializers
	// before serving commands. This direct indexed read exists only for embedded
	// and migration-isolation tests that deliberately disable that startup path.
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	tasks, err := issueClient.ListGraphReadinessWithRuntime(ctx, projectID, rootIssueID)
	if err != nil {
		return nil, err
	}
	contextTaskIDs := taskIDsFromTasks(tasks)
	if d.tmux != nil && d.sessionRuntimeStateStoreIfConfigured(projectID) != nil && len(contextTaskIDs) > 0 {
		if err := d.validateTaskGraphRuntime(ctx, projectID, contextTaskIDs, d.currentRevision(projectID)); err == nil {
			tasks, err = issueClient.ListGraphReadinessWithRuntime(ctx, projectID, rootIssueID)
			if err != nil {
				return nil, err
			}
		}
	}
	return d.enrichTasksWithSessionState(ctx, projectID, tasks), nil
}

func (result *taskGraphReadinessResult) applySessionStartProgress(progressByIssue map[string]taskGraphSessionStartProgress) {
	if result == nil || len(progressByIssue) == 0 {
		return
	}
	if len(result.Runnable) > 0 {
		runnable := result.Runnable[:0]
		for _, issueID := range result.Runnable {
			if _, launching := progressByIssue[sessionKey(issueID)]; launching {
				continue
			}
			runnable = append(runnable, issueID)
		}
		result.Runnable = runnable
	}
	if len(result.StaleCloseableChildren) > 0 {
		stale := result.StaleCloseableChildren[:0]
		for _, candidate := range result.StaleCloseableChildren {
			if _, launching := progressByIssue[sessionKey(candidate.IssueID)]; launching {
				continue
			}
			stale = append(stale, candidate)
		}
		result.StaleCloseableChildren = stale
	}
}

func (d *Daemon) loadTaskGraphDomainTasks(ctx context.Context, projectID string) ([]domain.Task, error) {
	tasks, _, err := d.projectReadSnapshot(projectID)
	if err != nil {
		return nil, err
	}
	return tasks, nil
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
	return daemonTaskGraphReadinessFromIndexesWithCompletionEvidence(rootID, byID, children, nil)
}

func daemonTaskGraphReadinessFromIndexesForActor(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID, actorID string, now time.Time) (taskGraphReadinessResult, error) {
	return daemonTaskGraphReadinessFromIndexesForActorWithCompletionEvidence(rootID, byID, children, actorID, now, nil)
}

func daemonTaskGraphReadinessFromIndexesWithCompletionEvidence(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID, completionEvidence map[string]taskDurableCompletionEvidence) (taskGraphReadinessResult, error) {
	return daemonTaskGraphReadinessFromIndexesForActorWithCompletionEvidence(rootID, byID, children, "", time.Now().UTC(), completionEvidence)
}

func daemonTaskGraphReadinessFromIndexesForActorWithCompletionEvidence(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID, actorID string, now time.Time, completionEvidence map[string]taskDurableCompletionEvidence) (taskGraphReadinessResult, error) {
	rootBacklog := byID[rootID].IssueFacts().LifecycleState == domain.IssueWorkflowBacklog
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
		NestedRoots: daemonTaskGraphNestedRootSummaries(rootID, byID, children, actorID, now),
		Active:      make([]string, 0),
		Blocked:     make(map[string]string),
	}
	if rootBacklog {
		for i := range result.NestedRoots {
			result.Blocked[result.NestedRoots[i].IssueID] = "lifecycle-backlog"
			result.NestedRoots[i].Status = "not_counting_capacity"
			result.NestedRoots[i].Classification = string(domain.OrchestrationCandidateBacklog)
			result.NestedRoots[i].ExclusionReasons = uniqueNonEmpty(append(result.NestedRoots[i].ExclusionReasons, "lifecycle-backlog"))
			result.NestedRoots[i].FallbackPolicy = "preserve_issue_lifecycle"
			result.NestedRoots[i].Advice = fmt.Sprintf("nested root %s is contained by backlog root %s", result.NestedRoots[i].IssueID, rootID.String())
		}
	}
	result.StaleCloseableChildren = daemonTaskGraphStaleCloseableCandidatesWithEvidence(rootID, byID, children, completionEvidence)
	for _, idRaw := range leaves {
		id, parseErr := naming.ParseIssueID(idRaw)
		if parseErr != nil {
			continue
		}
		task := byID[id]
		if task.IssueClosed() {
			continue
		}
		if daemonCloseGuardTaskHasSession(task) {
			result.Active = append(result.Active, idRaw)
			continue
		}
		if rootBacklog && id != rootID {
			result.Blocked[idRaw] = "lifecycle-backlog"
			continue
		}
		blockers := daemonTaskGraphUnresolvedBlockers(task, byID)
		if daemonTaskStaleCloseableCandidate(task, completionEvidence[task.ID.String()]) {
			continue
		}
		assessment := domain.AssessOrchestrationCandidate(task, actorID, now, blockers)
		if !assessment.Eligible {
			switch assessment.Classification {
			case domain.OrchestrationCandidateActive:
				result.Blocked[idRaw] = strings.Join(assessment.ExclusionReasons, ",")
			case domain.OrchestrationCandidateBlocked:
				result.Blocked[idRaw] = "waiting on " + strings.Join(blockers, ",")
			case domain.OrchestrationCandidateOwnedElsewhere:
				result.Blocked[idRaw] = daemonTaskOwnershipBlockReason(task, actorID, now)
			default:
				result.Blocked[idRaw] = strings.Join(assessment.ExclusionReasons, ",")
			}
			continue
		}
		result.Runnable = append(result.Runnable, idRaw)
	}
	result.Capacity = daemonTaskGraphCapacitySummary(result)
	return result, nil
}

func daemonTaskOwnershipBlockReason(task domain.Task, actorID string, now time.Time) string {
	if task.Ownership == nil || !task.Ownership.IsActive(now) {
		return ""
	}
	if strings.TrimSpace(actorID) != "" && task.Ownership.OwnedBy(actorID, now) {
		return ""
	}
	owner := strings.TrimSpace(task.Ownership.OwnerID)
	if owner == "" {
		return ""
	}
	if task.Ownership.ExpiresAt != nil && !task.Ownership.ExpiresAt.IsZero() {
		return fmt.Sprintf("owned by %s until %s", owner, task.Ownership.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return "owned by " + owner
}

func daemonTaskGraphRunnableCandidates(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID) ([]string, []string) {
	directChildren := children[rootID]
	if len(directChildren) == 0 {
		if task := byID[rootID]; !task.ID.IsZero() && task.Type != domain.TypeEpic {
			return []string{rootID.String()}, nil
		}
		return nil, nil
	}
	runnable := make([]string, 0, len(directChildren))
	nestedRoots := make([]string, 0)
	for _, id := range directChildren {
		task := byID[id]
		if task.ID.IsZero() {
			continue
		}
		if task.Type == domain.TypeEpic || len(children[id]) > 0 {
			nestedRoots = append(nestedRoots, id.String())
			continue
		}
		runnable = append(runnable, id.String())
	}
	return runnable, nestedRoots
}

func daemonTaskGraphStaleCloseableCandidates(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID) []taskStaleCloseableCandidate {
	return daemonTaskGraphStaleCloseableCandidatesWithEvidence(rootID, byID, children, nil)
}

func daemonTaskGraphStaleCloseableCandidatesWithEvidence(rootID naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID, completionEvidence map[string]taskDurableCompletionEvidence) []taskStaleCloseableCandidate {
	out := make([]taskStaleCloseableCandidate, 0)
	for _, id := range daemonTaskGraphDirectWorkerLeafIDs(rootID, byID, children) {
		task := byID[id]
		evidence := completionEvidence[task.ID.String()]
		if !daemonTaskStaleCloseableCandidate(task, evidence) {
			continue
		}
		if len(daemonTaskGraphUnresolvedBlockers(task, byID)) > 0 {
			continue
		}
		out = append(out, taskStaleCloseableCandidate{
			IssueID:          task.ID.String(),
			Status:           string(task.Status),
			Evidence:         daemonTaskStaleCloseableEvidence(task, evidence),
			SuggestedCommand: fmt.Sprintf("az issue close --id %s --close-clean-children", rootID.String()),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].IssueID < out[j].IssueID
	})
	return out
}

func daemonTaskStaleCloseableCandidate(task domain.Task, evidence taskDurableCompletionEvidence) bool {
	if task.IssueClosed() {
		return false
	}
	if !daemonCloseGuardCleanChildAutoCloseEligible(task) {
		return false
	}
	return task.Status == domain.StatusInReview || evidence.EventID > 0
}

func daemonTaskStaleCloseableEvidence(task domain.Task, completion taskDurableCompletionEvidence) []string {
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
		evidence = append(evidence, "durable completion handoff: status=in_review")
	}
	if completion.EventID > 0 {
		evidence = append(evidence, fmt.Sprintf("durable %s event=%d", completion.Kind, completion.EventID))
	}
	return evidence
}

func (d *Daemon) taskGraphDurableCompletionEvidence(ctx context.Context, projectID string, issueIDs []naming.IssueID) (map[string]taskDurableCompletionEvidence, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("inspect durable task completion evidence: issue store unavailable")
	}
	issueIDStrings := make([]string, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		issueIDStrings = append(issueIDStrings, issueID.String())
	}
	events, err := issueClient.ListLatestIssueObservationEventsByIssue(ctx, issues.LatestIssueObservationEventOptions{
		IssueIDs:                issueIDStrings,
		Type:                    domain.IssueEventTaskIntegrationCompleted,
		Source:                  "daemon-task-close",
		SourceCommands:          []string{"integrate-before-close"},
		RequiredPayloadTextKeys: []string{"project_id", "source_branch", "target_branch", "source_oid", "target_oid"},
		InvalidatedByStatuses:   []domain.Status{domain.StatusOpen, domain.StatusInProgress},
	})
	if err != nil {
		return nil, fmt.Errorf("inspect durable task completion evidence: %w", err)
	}
	out := make(map[string]taskDurableCompletionEvidence)
	for _, issueID := range issueIDs {
		event, found := events[issueID.String()]
		if !found {
			continue
		}
		receipt := taskCloseIntegrationReceipt{
			ProjectID:    observationPayloadString(event.Payload, "project_id"),
			SourceBranch: observationPayloadString(event.Payload, "source_branch"),
			TargetBranch: observationPayloadString(event.Payload, "target_branch"),
			SourceOID:    observationPayloadString(event.Payload, "source_oid"),
			TargetOID:    observationPayloadString(event.Payload, "target_oid"),
		}
		if protocol.NormalizeProjectID(receipt.ProjectID) != protocol.NormalizeProjectID(projectID) ||
			receipt.SourceBranch == "" || receipt.TargetBranch == "" || receipt.SourceOID == "" || receipt.TargetOID == "" {
			continue
		}
		out[issueID.String()] = taskDurableCompletionEvidence{EventID: event.ID, Kind: string(event.Type)}
	}
	return out, nil
}

// captureProjectedTaskGraphContainmentRisks derives readability diagnostics
// exclusively from materialized issue, worktree, and Git status projections.
// Exact containment remains mutation-preflight authority; ordinary readiness
// and orchestration snapshots must never execute Git while building a response.
func captureProjectedTaskGraphContainmentRisks(tasks []domain.Task, roots []string, worktrees []git.Worktree, worktreeListErr error) map[string][]taskContainmentRisk {
	out := make(map[string][]taskContainmentRisk, len(roots))
	type activeInput struct {
		issueID  naming.IssueID
		task     domain.Task
		worktree git.Worktree
	}
	type rootInputs struct {
		rootID       naming.IssueID
		rootWorktree git.Worktree
		active       []activeInput
		closedCount  int
	}
	appendIncomplete := func(input rootInputs, active activeInput, incompleteRefs []string, detail string) {
		refDescription := strings.Join(uniqueNonEmpty(incompleteRefs), ", ")
		if refDescription == "" {
			refDescription = "the expected root or active worktree projection"
		}
		message := fmt.Sprintf("containment evidence is incomplete for %s; absence of closed-child evidence is unknown", refDescription)
		if strings.TrimSpace(detail) != "" {
			message += ": " + detail
		}
		rootBranch := strings.TrimSpace(input.rootWorktree.Branch)
		activeBranch := strings.TrimSpace(active.worktree.Branch)
		targets := uniqueNonEmpty([]string{rootBranch, activeBranch})
		suggestedTarget := strings.Join(targets, " and ")
		if suggestedTarget == "" {
			suggestedTarget = input.rootID.String() + " and " + active.issueID.String()
		}
		out[input.rootID.String()] = append(out[input.rootID.String()], taskContainmentRisk{
			IssueID: active.issueID.String(), ActiveBranch: activeBranch, RootIssueID: input.rootID.String(), RootBranch: rootBranch,
			Classification:   "containment_evidence_incomplete",
			Message:          message,
			SuggestedCommand: fmt.Sprintf("inspect or refresh containment for %s before relying on a negative result", suggestedTarget),
		})
	}
	tasksByID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		tasksByID[task.ID.String()] = task
	}
	worktreeRefs := daemonIssueWorktreeRefs(worktrees)
	for _, rootIssueID := range uniqueNonEmpty(roots) {
		rootID, byID, children, err := daemonTaskGraphIndexes(rootIssueID, tasks)
		if err != nil {
			continue
		}
		rootTask := byID[rootID]
		rootWorktree, rootFound := daemonWorktreeForIssue(worktrees, rootIssueID)
		input := rootInputs{rootID: rootID, rootWorktree: rootWorktree}
		for _, id := range daemonTaskGraphDescendants(rootID, children) {
			task := byID[id]
			if task.ID.IsZero() || task.Type == domain.TypeEpic {
				continue
			}
			if task.IssueClosed() {
				input.closedCount++
			} else if worktree, found := daemonWorktreeForIssue(worktrees, id.String()); found {
				input.active = append(input.active, activeInput{issueID: id, task: task, worktree: worktree})
			} else if task.HasWorktree {
				input.active = append(input.active, activeInput{issueID: id, task: task})
			}
		}
		if len(input.active) == 0 || input.closedCount == 0 {
			continue
		}
		if worktreeListErr != nil {
			for _, active := range input.active {
				appendIncomplete(input, active, nil, "worktree projection capture failed")
			}
			continue
		}
		if !rootFound || strings.TrimSpace(rootWorktree.Path) == "" || strings.TrimSpace(rootWorktree.Branch) == "" {
			if rootTask.HasWorktree || len(input.active) > 0 {
				for _, active := range input.active {
					appendIncomplete(input, active, []string{active.worktree.Branch}, "expected root worktree projection is missing")
				}
			}
			continue
		}
		for _, active := range input.active {
			if strings.TrimSpace(active.worktree.Path) == "" || strings.TrimSpace(active.worktree.Branch) == "" {
				appendIncomplete(input, active, []string{rootWorktree.Branch}, "expected active worktree projection is missing")
				continue
			}
			if active.task.GitBehindCount <= 0 {
				continue
			}
			baseBranch := strings.TrimSpace(rootWorktree.Branch)
			if target, ok := domain.ClosestAncestorWithWorktree(active.issueID.String(), tasksByID, worktreeRefs); ok {
				baseBranch = target.Branch
			}
			out[input.rootID.String()] = append(out[input.rootID.String()], taskContainmentRisk{
				IssueID: active.issueID.String(), ActiveBranch: active.worktree.Branch, RootIssueID: input.rootID.String(), RootBranch: baseBranch,
				Classification:   "stale_child_branch",
				Message:          fmt.Sprintf("stale child branch: projected active branch %s for %s is behind ancestor branch %s by %d commit(s); exact closed-child containment is verified only by mutation preflight", active.worktree.Branch, active.issueID.String(), baseBranch, active.task.GitBehindCount),
				SuggestedCommand: fmt.Sprintf("merge or rebase %s into %s before continuing, or record explicit supersession evidence", baseBranch, active.worktree.Branch),
			})
		}
		sort.SliceStable(out[input.rootID.String()], func(i, j int) bool {
			left, right := out[input.rootID.String()][i], out[input.rootID.String()][j]
			return left.IssueID < right.IssueID
		})
	}
	return out
}

func daemonTaskGraphNestedRoots(
	nested []taskGraphNestedRoot,
	byID map[naming.IssueID]domain.Task,
	startProgressByIssue map[string]taskGraphSessionStartProgress,
	failedStartsByIssue map[string]taskGraphStartFailure,
	captured taskGraphReadinessContext,
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
	activeByIssue := daemonTaskGraphActiveSessionsByIssue(daemonTaskGraphActiveSessions(activeIDs, byID, startProgressByIssue, captured))
	out := make([]taskGraphNestedRoot, 0, len(nested))
	for _, item := range nested {
		taskID, parseErr := naming.ParseIssueID(item.IssueID)
		task := byID[taskID]
		if parseErr == nil && !task.ID.IsZero() {
			item.IssueStatus = string(task.Status)
		}
		if active := activeByIssue[item.IssueID]; active != nil {
			copyActive := *active
			item.ActiveSession = &copyActive
			item.Status = "active"
			item.FallbackPolicy = "watch_nested_root"
			item.Advice = fmt.Sprintf("watch nested root orchestrator: az orchestrate status --root %s --json", item.IssueID)
		}
		if item.ActiveSession == nil && len(item.ExclusionReasons) > 0 {
			item.Status = "not_counting_capacity"
			item.StartFailure = nil
			item.FallbackPolicy = "preserve_issue_lifecycle"
			item.Advice = fmt.Sprintf("nested root %s is excluded from orchestration start candidates: %s", item.IssueID, strings.Join(item.ExclusionReasons, ","))
			out = append(out, item)
			continue
		}
		if item.ActiveSession == nil {
			if failure, failed := failedStartsByIssue[item.IssueID]; failed {
				copyFailure := failure
				item.StartFailure = &copyFailure
				item.Status = "blocked_start_failed"
				item.FallbackPolicy = "keep_children_blocked_or_create_replacement_direct_work"
				item.Advice = fmt.Sprintf("nested root session start failed; inspect operation %s, retry `az orchestrator-session start --root %s`, or create replacement direct work under the parent without flattening %s descendants", failure.OperationID, item.IssueID, item.IssueID)
			}
		}
		if item.Status == "" || item.Status == "startable" || item.Status == string(domain.StatusOpen) || item.Status == string(domain.StatusInProgress) || item.Status == string(domain.StatusInReview) {
			if (domain.Task{Status: domain.Status(item.IssueStatus)}).IssueClosed() {
				item.Status = "not_counting_capacity"
				item.FallbackPolicy = "repair_or_close_remaining_descendants"
				item.Advice = fmt.Sprintf("nested root %s is closed; repair or close remaining descendants before parent completion", item.IssueID)
			} else if item.Status == "" || item.Status == "startable" || item.Status == item.IssueStatus {
				item.Status = "startable"
				item.FallbackPolicy = "start_nested_root"
			}
		}
		if strings.TrimSpace(item.Advice) == "" {
			item.Advice = fmt.Sprintf("start nested root orchestrator: az orchestrator-session start --root %s", item.IssueID)
		}
		out = append(out, item)
	}
	return out
}

func (d *Daemon) failedSessionStartByIssue(ctx context.Context, projectID string) map[string]taskGraphStartFailure {
	if d == nil || d.operationRuntime == nil || d.operationRuntime.manager == nil {
		if d == nil || d.taskGraphOperationList == nil {
			return nil
		}
	}
	projectID = d.canonicalProjectID(projectID)
	records, err := d.listTaskGraphOperations(ctx, daemonops.Query{
		ProjectID: projectID,
		Kind:      daemonhandlers.CommandSessionStart,
		States:    []daemonops.State{daemonops.StateDone, daemonops.StateFailed, daemonops.StateCancelled},
		Limit:     500,
	})
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Debug("list failed session start operations for graph readiness failed", "project_id", projectID, "error", err)
		}
		return nil
	}
	out := make(map[string]taskGraphStartFailure, len(records))
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
		if record.State != daemonops.StateFailed {
			delete(out, issueID)
			continue
		}
		out[issueID] = taskGraphStartFailure{
			OperationID:    strings.TrimSpace(record.ID),
			OperationState: string(record.State),
			Message:        strings.TrimSpace(record.ErrorMessage),
		}
	}
	return out
}

func daemonTaskGraphCapacitySummary(ready taskGraphReadinessResult) taskGraphCapacitySummary {
	summary := taskGraphCapacitySummary{
		DirectRunnableCount: len(ready.Runnable),
		DirectActiveCount:   len(ready.Active),
		PendingStartsCount:  len(ready.SessionStartProgress),
	}
	countingCapacity := make(map[string]struct{}, len(ready.Active)+len(ready.SessionStartProgress)+len(ready.Pending)+len(ready.NestedRoots))
	for _, issueID := range ready.Active {
		issueID = strings.TrimSpace(issueID)
		if issueID != "" {
			countingCapacity[issueID] = struct{}{}
		}
	}
	pendingSeen := make(map[string]struct{}, len(ready.SessionStartProgress)+len(ready.Pending))
	for _, progress := range ready.SessionStartProgress {
		issueID := strings.TrimSpace(progress.IssueID)
		if issueID != "" {
			pendingSeen[issueID] = struct{}{}
			countingCapacity[issueID] = struct{}{}
		}
	}
	for _, pending := range ready.Pending {
		issueID := strings.TrimSpace(pending.IssueID)
		if issueID == "" {
			continue
		}
		countingCapacity[issueID] = struct{}{}
		if _, seen := pendingSeen[issueID]; seen {
			continue
		}
		pendingSeen[issueID] = struct{}{}
		summary.PendingStartsCount++
	}
	for _, nested := range ready.NestedRoots {
		switch strings.TrimSpace(nested.Status) {
		case "active":
			summary.NestedActiveCount++
			issueID := strings.TrimSpace(nested.IssueID)
			if issueID != "" {
				countingCapacity[issueID] = struct{}{}
			}
		case "blocked_start_failed":
			summary.BlockedNestedRootsCount++
		case "not_counting_capacity":
			summary.NotCountingCapacityCount++
		default:
			summary.NestedStartableCount++
		}
	}
	summary.TotalCountingCapacityCount = len(countingCapacity)
	return summary
}

func cloneTaskGraphReadinessResult(result taskGraphReadinessResult) taskGraphReadinessResult {
	result.Runnable = append([]string(nil), result.Runnable...)
	result.Pending = append([]taskGraphPendingStart(nil), result.Pending...)
	result.Active = append([]string(nil), result.Active...)
	result.SessionStartProgress = cloneTaskGraphSessionStartProgressList(result.SessionStartProgress)
	result.StaleCloseableChildren = append([]taskStaleCloseableCandidate(nil), result.StaleCloseableChildren...)
	for i := range result.StaleCloseableChildren {
		result.StaleCloseableChildren[i].Evidence = append([]string(nil), result.StaleCloseableChildren[i].Evidence...)
	}
	result.ContainmentRisks = append([]taskContainmentRisk(nil), result.ContainmentRisks...)
	for i := range result.ContainmentRisks {
		result.ContainmentRisks[i].ChangedFiles = append([]string(nil), result.ContainmentRisks[i].ChangedFiles...)
	}
	result.WorkerObservations = append([]domain.WorkerObservation(nil), result.WorkerObservations...)
	for i := range result.WorkerObservations {
		observation := &result.WorkerObservations[i]
		if observation.LastEvent != nil {
			lastEvent := *observation.LastEvent
			observation.LastEvent = &lastEvent
		}
		observation.EvidenceSummary = append([]string(nil), observation.EvidenceSummary...)
		observation.Risks = append([]string(nil), observation.Risks...)
		observation.NextActions = append([]string(nil), observation.NextActions...)
	}
	result.PublicationQueue = append([]domain.PublicationOperation(nil), result.PublicationQueue...)
	result.scopeIssueIDs = append([]string(nil), result.scopeIssueIDs...)
	if result.Blocked != nil {
		blocked := make(map[string]string, len(result.Blocked))
		for key, value := range result.Blocked {
			blocked[key] = value
		}
		result.Blocked = blocked
	}
	result.NestedRoots = cloneTaskGraphNestedRoots(result.NestedRoots)
	result.ActiveSessions = cloneTaskGraphActiveSessions(result.ActiveSessions)
	return result
}

func cloneTaskGraphNestedRoots(in []taskGraphNestedRoot) []taskGraphNestedRoot {
	if len(in) == 0 {
		return nil
	}
	out := make([]taskGraphNestedRoot, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].ExclusionReasons = append([]string(nil), in[i].ExclusionReasons...)
		if in[i].ActiveSession != nil {
			active := *in[i].ActiveSession
			if active.StartProgress != nil {
				active.StartProgress = cloneTaskGraphSessionStartProgress(active.StartProgress)
			}
			out[i].ActiveSession = &active
		}
		if in[i].StartFailure != nil {
			failure := *in[i].StartFailure
			out[i].StartFailure = &failure
		}
	}
	return out
}

func cloneTaskGraphActiveSessions(in []taskGraphActiveSession) []taskGraphActiveSession {
	if len(in) == 0 {
		return nil
	}
	out := make([]taskGraphActiveSession, len(in))
	for i := range in {
		out[i] = in[i]
		if in[i].StartProgress != nil {
			out[i].StartProgress = cloneTaskGraphSessionStartProgress(in[i].StartProgress)
		}
	}
	return out
}

func cloneTaskGraphSessionStartProgressList(in []taskGraphSessionStartProgress) []taskGraphSessionStartProgress {
	out := append([]taskGraphSessionStartProgress(nil), in...)
	for i := range out {
		cloned := cloneTaskGraphSessionStartProgress(&out[i])
		out[i] = *cloned
	}
	return out
}

func cloneTaskGraphSessionStartProgress(in *taskGraphSessionStartProgress) *taskGraphSessionStartProgress {
	if in == nil {
		return nil
	}
	out := *in
	if in.StartedAt != nil {
		startedAt := *in.StartedAt
		out.StartedAt = &startedAt
	}
	if in.FinishedAt != nil {
		finishedAt := *in.FinishedAt
		out.FinishedAt = &finishedAt
	}
	return &out
}

func hasTaskGraphSessionStartProgress(issueID string, progressByIssue map[string]taskGraphSessionStartProgress) bool {
	if len(progressByIssue) == 0 {
		return false
	}
	_, ok := progressByIssue[sessionKey(issueID)]
	return ok
}

func daemonTaskGraphWorkerObservations(
	rootID naming.IssueID,
	byID map[naming.IssueID]domain.Task,
	children map[naming.IssueID][]naming.IssueID,
	ready taskGraphReadinessResult,
	captured taskGraphReadinessContext,
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
	mailByIssue := captured.mailEventsByRoot[rootID.String()]

	out := make([]domain.WorkerObservation, 0, len(leafIDs))
	for _, id := range leafIDs {
		task := byID[id]
		if task.ID.IsZero() || task.Type == domain.TypeEpic {
			continue
		}
		issueID := task.ID.String()
		issueEvents := captured.issueEventsByIssue[issueID]
		observation := daemonWorkerObservationFromInputs(workerObservationInputs{
			RootIssueID:     rootID.String(),
			Task:            task,
			BlockedReason:   ready.Blocked[issueID],
			Runnable:        runnable[issueID],
			Active:          activeByIssue[issueID],
			Pending:         pendingByIssue[issueID],
			StartProgress:   startProgressByIssue[issueID],
			Stale:           staleByIssue[issueID],
			IssueEvents:     issueEvents,
			MailEvents:      mailByIssue[issueID],
			DecisionWaiting: strings.HasPrefix(ready.Blocked[issueID], "unresolved interaction request"),
		})
		out = append(out, observation)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].IssueID < out[j].IssueID
	})
	return out
}

type workerObservationInputs struct {
	RootIssueID     string
	Task            domain.Task
	BlockedReason   string
	Runnable        bool
	Active          *taskGraphActiveSession
	Pending         *taskGraphPendingStart
	StartProgress   *taskGraphSessionStartProgress
	Stale           *taskStaleCloseableCandidate
	IssueEvents     []domain.IssueObservationEvent
	MailEvents      []daemonMailEvent
	DecisionWaiting bool
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
	case in.Task.IssueClosed() && (daemonCloseGuardTaskHasSession(in.Task) || daemonCloseGuardTaskHasWorktree(in.Task) || in.Active != nil):
		observation.State = domain.WorkerObservationCleanupPending
		observation.Reason = "issue is closed but runtime projection remains"
	case in.Task.IssueClosed():
		observation.State = domain.WorkerObservationDone
		observation.Reason = "issue is closed"
	case in.Active != nil && daemonWorkerObservationActiveFailed(*in.Active):
		observation.State = domain.WorkerObservationFailed
		observation.Reason = "active session reports failed runtime state"
	case in.DecisionWaiting:
		observation.State = domain.WorkerObservationWaitingHuman
		observation.Reason = "unresolved interaction request blocks issue pickup"
		observation.WaitingHumanSource = domain.WaitingHumanSourceInteractionRequest
		observation.WaitingHumanReason = observation.Reason
	case in.Task.Status == domain.StatusInReview && daemonWorkerObservationReviewReadyPhaseAllowed(in):
		observation.State = domain.WorkerObservationReviewReady
		observation.Reason = "review-ready handoff is idle"
	case in.Active != nil && daemonWorkerObservationActiveWaiting(*in.Active):
		observation.State = domain.WorkerObservationWaitingHuman
		observation.Reason = "active session is waiting for human input"
		observation.WaitingHumanSource = domain.WaitingHumanSourceRuntimePrompt
		observation.WaitingHumanReason = observation.Reason
	case in.Active != nil:
		observation.State = domain.WorkerObservationWorking
		observation.Reason = "active worker session is present"
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
	switch activity {
	case "waiting", "waiting_human", "waiting_ai", "waiting_tool", "waiting-for-human", "waiting-for-ai", "waiting-for-tool":
		return true
	default:
		return false
	}
}

func daemonWorkerObservationReviewReadyPhaseAllowed(in workerObservationInputs) bool {
	if in.Task.Status != domain.StatusInReview {
		return false
	}
	if in.Active == nil {
		return in.Task.Session.AllowsReviewReadyPhase(in.Task.HasTmuxSession)
	}
	session := &domain.Session{
		State:          domain.SessionState(strings.TrimSpace(in.Active.State)),
		Activity:       strings.TrimSpace(in.Active.Activity),
		ActivitySource: strings.TrimSpace(in.Active.ActivitySource),
	}
	return session.AllowsReviewReadyPhase(true)
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
	if evt := latestWorkerObservationIssueEvent(in.IssueEvents); evt != nil {
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
		return []string{fmt.Sprintf("validate evidence, then accept and close review: az orchestrate review accept --root %s --issue %s", rootIssueID, issueID)}
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
	if evt := latestWorkerObservationIssueEvent(issueEvents); evt != nil {
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

func latestWorkerObservationIssueEvent(events []domain.IssueObservationEvent) *domain.IssueObservationEvent {
	var latest *domain.IssueObservationEvent
	for i := range events {
		if !workerObservationIssueEventMeaningful(events[i]) {
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

func workerObservationIssueEventMeaningful(evt domain.IssueObservationEvent) bool {
	switch evt.Type {
	case domain.IssueEventIssueStatusChanged,
		domain.IssueEventProgressRecorded,
		domain.IssueEventFollowupCreated,
		domain.IssueEventSessionLifecycleChanged,
		domain.IssueEventAgentActivityChanged,
		domain.IssueEventWorktreeGitChanged,
		domain.IssueEventCommandStarted,
		domain.IssueEventCommandFinished,
		domain.IssueEventValidationPassed,
		domain.IssueEventValidationFailed,
		domain.IssueEventEvidenceSubmitted,
		domain.IssueEventReviewCompleted,
		domain.IssueEventReviewCloseFailed,
		domain.IssueEventRiskRecorded,
		domain.IssueEventBlockerReported,
		domain.IssueEventHumanInputRequested,
		domain.IssueEventHumanInputProvided,
		domain.IssueEventInvestigationDisposition:
		return true
	case domain.IssueEventIssueDependencyAdded, domain.IssueEventIssueDependencyRemoved:
		return workerObservationDependencyEventMeaningful(evt)
	default:
		return false
	}
}

func workerObservationDependencyEventMeaningful(evt domain.IssueObservationEvent) bool {
	dependencyType := strings.TrimSpace(fmt.Sprint(evt.Payload["dependency_type"]))
	switch domain.DependencyType(dependencyType) {
	case domain.DependencyBlocks, domain.DependencyBlockedBy:
		return true
	default:
		return false
	}
}

func issueObservationEventSummary(evt domain.IssueObservationEvent) string {
	if len(evt.Payload) == 0 {
		return ""
	}
	for _, key := range []string{"summary", "message", "body", "reason", "status", "failure"} {
		if value, ok := evt.Payload[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				return truncateObservationSummary(text)
			}
		}
	}
	return ""
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
		case domain.IssueEventProgressRecorded,
			domain.IssueEventFollowupCreated,
			domain.IssueEventEvidenceSubmitted,
			domain.IssueEventValidationPassed,
			domain.IssueEventValidationFailed,
			domain.IssueEventReviewCompleted,
			domain.IssueEventReviewCloseFailed,
			domain.IssueEventRiskRecorded,
			domain.IssueEventBlockerReported,
			domain.IssueEventHumanInputRequested,
			domain.IssueEventHumanInputProvided,
			domain.IssueEventInvestigationDisposition:
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
	events, err := d.readTaskGraphMailbox(d.cfg.RepoDir, rootIssueID)
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

func (d *Daemon) readTaskGraphMailbox(repoDir, rootIssueID string) ([]daemonMailEvent, error) {
	if d != nil && d.taskGraphMailboxRead != nil {
		return d.taskGraphMailboxRead(repoDir, rootIssueID)
	}
	return readMailboxEvents(repoDir, rootIssueID)
}

func (d *Daemon) taskGraphPendingSessionStarts(ctx context.Context, projectID string) (map[string]taskGraphPendingStart, error) {
	if d == nil || d.operationRuntime == nil || d.operationRuntime.manager == nil {
		if d == nil || d.taskGraphOperationList == nil {
			return nil, nil
		}
	}
	projectID = d.canonicalProjectID(projectID)
	records, err := d.listTaskGraphOperations(ctx, daemonops.Query{
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
	addPending := func(start taskGraphPendingStart) {
		for _, existing := range result.Pending {
			if strings.EqualFold(existing.IssueID, start.IssueID) {
				return
			}
		}
		result.Pending = append(result.Pending, start)
	}
	runnable := make([]string, 0, len(result.Runnable))
	for _, issueID := range result.Runnable {
		if start, ok := pending[issueID]; ok {
			addPending(start)
			continue
		}
		runnable = append(runnable, issueID)
	}
	result.Runnable = runnable
	if len(result.StaleCloseableChildren) > 0 {
		stale := result.StaleCloseableChildren[:0]
		for _, candidate := range result.StaleCloseableChildren {
			if start, ok := pending[candidate.IssueID]; ok {
				addPending(start)
				continue
			}
			stale = append(stale, candidate)
		}
		result.StaleCloseableChildren = stale
	}
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

func daemonTaskGraphNestedRootSummaries(root naming.IssueID, byID map[naming.IssueID]domain.Task, children map[naming.IssueID][]naming.IssueID, actorID string, now time.Time) []taskGraphNestedRoot {
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
			assessment := domain.AssessOrchestrationCandidate(task, actorID, now, nil)
			status := "startable"
			fallbackPolicy := "start_nested_root"
			advice := fmt.Sprintf("start nested root orchestrator: az orchestrator-session start --root %s", id.String())
			if !assessment.Eligible {
				status = "not_counting_capacity"
				fallbackPolicy = "preserve_issue_lifecycle"
				advice = fmt.Sprintf("nested root %s is excluded from orchestration start candidates: %s", id.String(), strings.Join(assessment.ExclusionReasons, ","))
			}
			out = append(out, taskGraphNestedRoot{
				IssueID:          id.String(),
				Status:           status,
				IssueStatus:      string(task.Status),
				Classification:   string(assessment.Classification),
				ExclusionReasons: append([]string(nil), assessment.ExclusionReasons...),
				Type:             string(task.Type),
				ChildCount:       len(daemonTaskGraphDescendants(id, children)),
				FallbackPolicy:   fallbackPolicy,
				Advice:           advice,
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
	if !task.IssueClosed() {
		return true
	}
	for _, descID := range daemonTaskGraphDescendants(id, children) {
		if !byID[descID].IssueClosed() {
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
		if !depTask.IssueClosed() {
			out = append(out, dep.ID.String())
		}
	}
	sort.Strings(out)
	return out
}

func daemonTaskGraphActiveSessions(activeIDs []string, byID map[naming.IssueID]domain.Task, progressByIssue map[string]taskGraphSessionStartProgress, captured taskGraphReadinessContext) []taskGraphActiveSession {
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
			} else if progress, found := sessionInitCommandProgress(task, captured.initCommandsConfigured, captured.capturedAt); found {
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
		if !task.IssueClosed() || !daemonCloseGuardTaskHasSession(task) {
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
	return d.sessionStartProgressByIssueAt(ctx, projectID, time.Now().UTC())
}

func (d *Daemon) sessionStartProgressByIssueAt(ctx context.Context, projectID string, now time.Time) map[string]taskGraphSessionStartProgress {
	if d == nil || d.operationRuntime == nil || d.operationRuntime.manager == nil {
		if d == nil || d.taskGraphOperationList == nil {
			return nil
		}
	}
	projectID = d.canonicalProjectID(projectID)
	records, err := d.listTaskGraphOperations(ctx, daemonops.Query{
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
	return sessionInitCommandProgress(task, len(d.runtimeConfigForProject(projectID).SessionSyncInitCommands) > 0, time.Now().UTC())
}

func sessionInitCommandProgress(task domain.Task, initCommandsConfigured bool, now time.Time) (taskGraphSessionStartProgress, bool) {
	if task.Session == nil || !initCommandsConfigured {
		return taskGraphSessionStartProgress{}, false
	}
	if strings.TrimSpace(task.Session.Activity) != "busy" || strings.TrimSpace(task.Session.ActivitySource) != "session" {
		return taskGraphSessionStartProgress{}, false
	}
	elapsedMS := int64(0)
	if task.Session.StartedAt != nil && !task.Session.StartedAt.IsZero() {
		elapsedMS = now.Sub(task.Session.StartedAt.UTC()).Milliseconds()
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
			advice = append(advice, fmt.Sprintf("start nested root orchestrator: az orchestrator-session start --root %s", nested.IssueID))
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
		TaskID          string                `json:"task_id"`
		Title           string                `json:"title"`
		Description     string                `json:"description"`
		Design          *string               `json:"design,omitempty"`
		Notes           *string               `json:"notes,omitempty"`
		Acceptance      *string               `json:"acceptance,omitempty"`
		Estimate        *int                  `json:"estimate,omitempty"`
		EstimateSet     bool                  `json:"estimate_set,omitempty"`
		Type            domain.TaskType       `json:"type"`
		Priority        domain.Priority       `json:"priority"`
		Lifecycle       *domain.IssueWorkflow `json:"lifecycle_state,omitempty"`
		Implementations []string              `json:"implementations,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task details update requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	var deferredCleanup deferredTaskWorktreeCleanupCancellation
	if cmd.Lifecycle != nil {
		var err error
		deferredCleanup, err = d.cancelDeferredTaskWorktreeCleanup(ctx, projectID, cmd.TaskID, "issue lifecycle changed before deferred worktree cleanup completed")
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
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
		Lifecycle:       cmd.Lifecycle,
		Implementations: cmd.Implementations,
	})
	if err != nil {
		if compensationErr := d.compensateDeferredTaskWorktreeCleanup(ctx, projectID, cmd.TaskID, deferredCleanup); compensationErr != nil {
			err = errors.Join(err, compensationErr)
		}
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if deferredCleanup.Observed {
		if err := d.restoreDeferredCleanupWorktreeProjection(ctx, projectID, cmd.TaskID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		task, err = issueClient.GetWithRuntime(ctx, projectID, cmd.TaskID)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
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
			if _, _, err := d.worktreeAdapter.Delete(ctx, projectID, taskID, daemonhandlers.WorktreeRemoveOptions{
				Force:        cmd.ForceWorktree,
				DeleteBranch: true,
			}); err != nil {
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

func (d *Daemon) handleTaskUnarchive(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, "issue store unavailable"), nil
	}
	var cmd struct {
		TaskID          string `json:"task_id"`
		WithParents     bool   `json:"with_parents,omitempty"`
		CascadeChildren bool   `json:"cascade_children,omitempty"`
	}
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task unarchive requested", "project_id", projectID, "task_id", cmd.TaskID)
	}
	result, err := issueClient.UnarchiveWithOptions(ctx, cmd.TaskID, issues.UnarchiveOptions{
		WithParents:     cmd.WithParents,
		CascadeChildren: cmd.CascadeChildren,
	})
	if err != nil {
		return d.errorResponse(req, daemonTaskMutationErrorCode(err), err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	for _, restoredID := range result.UnarchivedIDs {
		d.publishTaskEvent(req, protocol.EventTaskRestored, resp.Revision, d.taskEventBody(ctx, projectID, restoredID))
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon task unarchive completed", "project_id", projectID, "task_id", cmd.TaskID, "revision", resp.Revision)
	}
	return resp, nil
}

func daemonTaskMutationErrorCode(err error) protocol.ErrorCode {
	if isProjectReadUnavailableError(err) {
		return protocol.ErrorCodeUnavailable
	}
	if errors.Is(err, issues.ErrIssueHasLiveChildren) || errors.Is(err, issues.ErrIssueHasArchivedParents) {
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
