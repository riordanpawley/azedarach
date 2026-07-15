package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const (
	defaultOrchestrationInspectLimit   = 50
	defaultOrchestrationStartLimit     = 3
	defaultOrchestrationAgentCapacity  = 12
	defaultOrchestrationOpenIssueLimit = 100
	orchestrationSnapshotCacheTTL      = 10 * time.Second
)

// orchestrationAuthority is the deliberately small daemon boundary for all
// rooted and project-wide orchestration clients.
type orchestrationAuthority interface {
	Snapshot(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error)
	Apply(context.Context, string, protocol.OrchestrationIntentRequest) (protocol.OrchestrationIntentResult, error)
}

type daemonOrchestrationAuthority struct {
	daemon             *Daemon
	submitStart        func(context.Context, protocol.RequestEnvelope) protocol.ResponseEnvelope
	lookupOperation    func(context.Context, string) (protocol.OperationRecord, error)
	releaseReviewLease func(context.Context, string, string, string) error
}

type invalidOrchestrationLaunchError struct {
	Field string
}

func (e *invalidOrchestrationLaunchError) Error() string {
	return fmt.Sprintf("invalid orchestration launch result: missing %s", e.Field)
}

type orchestrationSnapshotBuilder func(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error)

type orchestrationSnapshotCacheEntry struct {
	revision          uint64
	cachedAt          time.Time
	semanticExpiresAt time.Time
	snapshot          protocol.OrchestrationSnapshot
	runtimeIssueIDs   []string
}

type orchestrationSnapshotLoad struct {
	done     chan struct{}
	revision uint64
	stable   bool
	snapshot protocol.OrchestrationSnapshot
	err      error
}

func (d *Daemon) orchestrationAuthority() orchestrationAuthority {
	return daemonOrchestrationAuthority{daemon: d}
}

func (d *Daemon) handleOrchestrationSnapshot(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.OrchestrationSnapshotRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	requestedScope := body.Scope
	lease, found, err := d.resolveOrchestratorSession(ctx, d.projectID(req.Meta), body.SessionID)
	leaseResolvedFromSession := found
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if found {
		body.Scope = lease.Identity.Scope
	} else if strings.TrimSpace(body.SessionID) != "" && body.Scope.Kind == "" {
		// A normal worker session deliberately receives no orchestration context.
		encoded, _ := json.Marshal(protocol.OrchestrationSnapshot{Role: "worker", SessionID: body.SessionID})
		resp := d.successResponse(req)
		resp.Body = encoded
		return resp, nil
	}
	if _, err := domain.NewOrchestratorIdentity(d.projectID(req.Meta), body.Scope); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if !found && strings.TrimSpace(body.SessionID) == "" && d.sessionRuntimeStateStoreIfConfigured(d.projectID(req.Meta)) != nil {
		identity, _ := domain.NewOrchestratorIdentity(d.projectID(req.Meta), body.Scope)
		lease, found, err = daemonstate.NewOrchestratorLeaseAuthority(d.sessionRuntimeStateStoreIfConfigured(d.projectID(req.Meta))).Get(ctx, identity)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("load orchestration scope lease: %v", err)), nil
		}
	}
	if leaseResolvedFromSession && body.ObservedCursor > lease.Cursor && requestedScope == lease.Identity.Scope {
		lease, err = daemonstate.NewOrchestratorLeaseAuthority(d.sessionRuntimeStateStoreIfConfigured(d.projectID(req.Meta))).AdvanceCursor(ctx, lease.Identity, body.ObservedCursor)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("advance orchestration cursor: %v", err)), nil
		}
	}
	if strings.TrimSpace(body.ActorID) == "" {
		body.ActorID = strings.TrimSpace(req.Meta.ClientActor)
	}
	projectID := d.projectID(req.Meta)
	build := d.orchestrationAuthority().Snapshot
	if d.orchestrationSnapshotBuild != nil {
		build = d.orchestrationSnapshotBuild
	}
	snapshot, snapshotRevision, stable, err := d.loadOrchestrationSnapshot(ctx, projectID, body, build)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if !stable {
		return d.errorResponse(req, protocol.ErrorCodeConflict, "orchestration projection changed while building snapshot; retry"), nil
	}
	snapshot.Role = "orchestrator"
	snapshot.SessionID = strings.TrimSpace(body.SessionID)
	if found {
		snapshot.SessionID = lease.SessionID
		snapshot.Lifecycle = lease.Lifecycle
		snapshot.Completion.State = lease.Lifecycle
		snapshot.Cursor = lease.Cursor
		applyOrchestratorContinuationProjection(&snapshot, lease)
	}
	snapshot.Revision = snapshotRevision
	if snapshot.Cursor == 0 && !found {
		snapshot.Cursor = int64(snapshotRevision)
	}
	finalizeOrchestrationSnapshotSource(&snapshot)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body, resp.Revision = encoded, snapshot.Revision
	return resp, nil
}

func (d *Daemon) loadOrchestrationSnapshot(
	ctx context.Context,
	projectID string,
	request protocol.OrchestrationSnapshotRequest,
	build orchestrationSnapshotBuilder,
) (protocol.OrchestrationSnapshot, uint64, bool, error) {
	projectID = d.canonicalProjectID(projectID)
	request = d.normalizeOrchestrationSnapshotRequest(projectID, request)
	cacheKey := orchestrationSnapshotCacheKey(projectID, request)
	revision := d.currentRevision(projectID)
	loadKey := fmt.Sprintf("%s\x00%d", cacheKey, revision)

	d.orchestrationSnapshotMu.Lock()
	if d.orchestrationSnapshotCache == nil {
		d.orchestrationSnapshotCache = map[string]orchestrationSnapshotCacheEntry{}
	}
	if cached, ok := d.orchestrationSnapshotCache[cacheKey]; ok && cached.revision == revision && time.Since(cached.cachedAt) <= orchestrationSnapshotCacheTTL && (cached.semanticExpiresAt.IsZero() || time.Now().Before(cached.semanticExpiresAt)) {
		d.orchestrationSnapshotMu.Unlock()
		clone, err := cloneOrchestrationSnapshot(cached.snapshot)
		return clone, revision, err == nil, err
	}
	if d.orchestrationSnapshotLoads == nil {
		d.orchestrationSnapshotLoads = map[string]*orchestrationSnapshotLoad{}
	}
	if load := d.orchestrationSnapshotLoads[loadKey]; load != nil {
		d.orchestrationSnapshotMu.Unlock()
		select {
		case <-ctx.Done():
			return protocol.OrchestrationSnapshot{}, revision, false, ctx.Err()
		case <-load.done:
			clone, cloneErr := cloneOrchestrationSnapshot(load.snapshot)
			if load.err != nil {
				return protocol.OrchestrationSnapshot{}, load.revision, load.stable, load.err
			}
			stable := load.stable && d.currentRevision(projectID) == load.revision
			return clone, load.revision, stable && cloneErr == nil, cloneErr
		}
	}
	load := &orchestrationSnapshotLoad{done: make(chan struct{}), revision: revision}
	d.orchestrationSnapshotLoads[loadKey] = load
	d.orchestrationSnapshotMu.Unlock()

	snapshot, err := build(ctx, projectID, request)
	finishedRevision := d.currentRevision(projectID)
	stable := err == nil && finishedRevision == revision
	load.snapshot = snapshot
	load.revision = finishedRevision
	load.stable = stable
	load.err = err

	var semanticExpiresAt time.Time
	if stable && request.Scope.Kind == domain.OrchestrationScopeRooted {
		semanticExpiresAt = d.taskGraphReadinessCacheExpiry(projectID, request.Scope.RootIssueID.String(), request.ActorID, revision)
	}
	d.orchestrationSnapshotMu.Lock()
	delete(d.orchestrationSnapshotLoads, loadKey)
	if stable {
		d.orchestrationSnapshotCache[cacheKey] = orchestrationSnapshotCacheEntry{
			revision:          revision,
			cachedAt:          time.Now(),
			semanticExpiresAt: semanticExpiresAt,
			snapshot:          snapshot,
			runtimeIssueIDs:   orchestrationSnapshotRuntimeIssueIDs(snapshot),
		}
	}
	close(load.done)
	d.orchestrationSnapshotMu.Unlock()

	clone, cloneErr := cloneOrchestrationSnapshot(snapshot)
	if err != nil {
		return protocol.OrchestrationSnapshot{}, finishedRevision, false, err
	}
	stable = stable && d.currentRevision(projectID) == finishedRevision
	return clone, finishedRevision, stable && cloneErr == nil, cloneErr
}

func orchestrationSnapshotCacheKey(projectID string, request protocol.OrchestrationSnapshotRequest) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%s", strings.TrimSpace(projectID), request.Scope.Kind, request.Scope.RootIssueID, strings.TrimSpace(request.ActorID), request.Limit, strings.TrimSpace(request.RepoDir))
}

func (d *Daemon) normalizeOrchestrationSnapshotRequest(projectID string, request protocol.OrchestrationSnapshotRequest) protocol.OrchestrationSnapshotRequest {
	repoDir := strings.TrimSpace(request.RepoDir)
	if repoDir == "" {
		repoDir = strings.TrimSpace(d.resolveRepoDirForProject(projectID))
	}
	if repoDir == "" {
		return request
	}
	if absolute, err := filepath.Abs(repoDir); err == nil {
		repoDir = absolute
	}
	repoDir = filepath.Clean(repoDir)
	if resolved, err := filepath.EvalSymlinks(repoDir); err == nil {
		repoDir = filepath.Clean(resolved)
	}
	request.RepoDir = repoDir
	return request
}

func cloneOrchestrationSnapshot(snapshot protocol.OrchestrationSnapshot) (protocol.OrchestrationSnapshot, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return protocol.OrchestrationSnapshot{}, fmt.Errorf("clone orchestration snapshot: %w", err)
	}
	var clone protocol.OrchestrationSnapshot
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return protocol.OrchestrationSnapshot{}, fmt.Errorf("clone orchestration snapshot: %w", err)
	}
	return clone, nil
}

func orchestrationSnapshotRuntimeIssueIDs(snapshot protocol.OrchestrationSnapshot) []string {
	seen := make(map[string]struct{})
	add := func(issueID string) {
		issueID = strings.TrimSpace(issueID)
		if issueID != "" {
			seen[issueID] = struct{}{}
		}
	}
	for _, issueID := range snapshot.Active {
		add(issueID)
	}
	for _, session := range snapshot.ActiveSessions {
		add(session.IssueID)
	}
	for _, pending := range snapshot.Pending {
		add(pending.IssueID)
	}
	for _, nested := range snapshot.NestedRoots {
		if nested.ActiveSession != nil {
			add(nested.IssueID)
		}
	}
	out := make([]string, 0, len(seen))
	for issueID := range seen {
		out = append(out, issueID)
	}
	sort.Strings(out)
	return out
}

func applyOrchestratorContinuationProjection(snapshot *protocol.OrchestrationSnapshot, lease daemonstate.OrchestratorScopeLease) {
	if snapshot == nil || lease.Identity.Scope.Kind != domain.OrchestrationScopeRooted || !rootedOrchestratorContinuationRequired(false, *snapshot) {
		return
	}
	snapshot.ContinuationRequired = true
	snapshot.ContinuationReason = "root complete-check has not passed while direct nested roots still require orchestration"
	snapshot.ContinuationContract = orchestratorContinuationPrompt(lease, snapshot.NestedRoots)
}

func rootedOrchestratorContinuationRequired(completeCheckPassed bool, snapshot protocol.OrchestrationSnapshot) bool {
	if completeCheckPassed || len(snapshot.Interactions) > 0 {
		return false
	}
	for _, nested := range snapshot.NestedRoots {
		if !slices.Contains(nested.ExclusionReasons, "lifecycle-backlog") {
			return true
		}
	}
	return false
}

func orchestratorContinuationPrompt(lease daemonstate.OrchestratorScopeLease, nested []protocol.OrchestrationNestedRoot) string {
	ids := make([]string, 0, len(nested))
	for _, item := range nested {
		if slices.Contains(item.ExclusionReasons, "lifecycle-backlog") {
			continue
		}
		if id := strings.TrimSpace(item.IssueID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return fmt.Sprintf("Persistent parent orchestration wake (root=%s cursor=%d). Continue now: consume `az orchestrate watch --root %s --since %d --jsonl`; coordinate only direct nested roots [%s] without flattening their descendants; review and integrate accepted epic results; advance cross-epic dependencies; repeat status/start/watch/review until `az orchestrate complete-check --root %s` passes, then validate and set the root in_review for human handoff. Do not emit a handoff response while this continuation remains required.", lease.Identity.Scope.RootIssueID, lease.Cursor, lease.Identity.Scope.RootIssueID, lease.Cursor, strings.Join(ids, ","), lease.Identity.Scope.RootIssueID)
}

func (d *Daemon) resolveOrchestratorSession(ctx context.Context, projectID, sessionID string) (daemonstate.OrchestratorScopeLease, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return daemonstate.OrchestratorScopeLease{}, false, nil
	}
	store := d.sessionRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		return daemonstate.OrchestratorScopeLease{}, false, nil
	}
	leases, err := store.ListOrchestratorScopeLeases(ctx, projectID)
	if err != nil {
		return daemonstate.OrchestratorScopeLease{}, false, fmt.Errorf("refresh orchestrator session projection: %w", err)
	}
	for _, lease := range leases {
		if lease.SessionID == sessionID {
			return lease, true, nil
		}
	}
	return daemonstate.OrchestratorScopeLease{}, false, nil
}

func (d *Daemon) handleOrchestrationIntent(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.OrchestrationIntentRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if _, err := domain.NewOrchestratorIdentity(d.projectID(req.Meta), body.Scope); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if !validOrchestrationIntentKind(body.Kind) || strings.TrimSpace(body.IntentKey) == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "supported orchestration intent with intent_key is required"), nil
	}
	if err := validateOrchestrationReviewIntent(body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if strings.TrimSpace(body.ActorID) == "" {
		body.ActorID = strings.TrimSpace(req.Meta.ClientActor)
	}
	if strings.TrimSpace(body.ActorID) == "" {
		body.ActorID = "orchestrate"
	}
	result, err := d.orchestrationAuthority().Apply(ctx, d.projectID(req.Meta), body)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	result.Revision = d.currentRevision(d.projectID(req.Meta))
	encoded, err := json.Marshal(result)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body, resp.Revision = encoded, result.Revision
	return resp, nil
}

func (a daemonOrchestrationAuthority) Snapshot(ctx context.Context, projectID string, request protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error) {
	identity, err := domain.NewOrchestratorIdentity(projectID, request.Scope)
	if err != nil {
		return protocol.OrchestrationSnapshot{}, err
	}
	limit := request.Limit
	if limit <= 0 {
		limit = a.inspectLimit()
	}
	request.Limit = limit
	snapshot := protocol.OrchestrationSnapshot{
		Scope: identity.Scope, GeneratedAt: time.Now().UTC(), Blocked: map[string]string{},
		Constraints: protocol.OrchestrationConstraints{
			InspectLimit: limit, StartLimit: a.startLimit(), AgentCapacity: a.agentCapacity(),
			Commands:       orchestrationScopeCommands(identity.Scope),
			RoleGuardrails: []string{"remain in the active orchestration loop", "do not implement worker issue scope", "delegate non-trivial review inspection to fresh read-only ephemeral subagents", "retain orchestrator-only durable review and integration authority", "preserve sessions during review handoff"},
		},
	}
	materializedTasks, source, err := a.daemon.projectReadSnapshot(projectID)
	if err != nil {
		return protocol.OrchestrationSnapshot{}, err
	}
	snapshot.Source = source
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return protocol.OrchestrationSnapshot{}, fmt.Errorf("issue store unavailable")
	}
	if a.daemon.operationRuntime != nil {
		validationStore, err := a.daemon.validationProjectionStore()
		if err != nil {
			return protocol.OrchestrationSnapshot{}, fmt.Errorf("load validation capacity projection: %w", err)
		}
		validation, err := validationStore.ValidationSnapshot(ctx, projectID, snapshot.GeneratedAt, defaultValidationLeaseTTL)
		if err != nil {
			return protocol.OrchestrationSnapshot{}, fmt.Errorf("load validation capacity projection: %w", err)
		}
		snapshot.ValidationCapacity = &validation
	}
	if identity.Scope.Kind == domain.OrchestrationScopeRooted {
		root := identity.Scope.RootIssueID.String()
		ready, err := a.daemon.taskGraphReadinessForActor(ctx, projectID, root, request.ActorID)
		if err != nil {
			return protocol.OrchestrationSnapshot{}, err
		}
		if err := orchestrationTranscode(ready, &snapshot); err != nil {
			return protocol.OrchestrationSnapshot{}, err
		}
		snapshot.Scope, snapshot.Roots, snapshot.GeneratedAt = identity.Scope, []string{root}, time.Now().UTC()
		a.enrichStewardshipContext(ctx, projectID, &snapshot)
		tasks := materializedParentChildClosure(materializedTasks, root)
		if err := a.enrichPendingDecisions(ctx, projectID, issueClient, &snapshot, tasks); err != nil {
			return protocol.OrchestrationSnapshot{}, err
		}
		snapshot.ReviewQueue, err = a.reviewQueue(ctx, projectID, request, tasks)
		if err != nil {
			return protocol.OrchestrationSnapshot{}, err
		}
		finalizeOrchestrationSnapshotSource(&snapshot)
		return snapshot, nil
	}

	// Project orchestration consumes the daemon's materialized projection only.
	// Its actionable window is live, unparented, canonical lifecycle Open roots.
	// LIMIT bounds runnable inspection, while roots with projected live sessions
	// remain visible independently. Dependencies remain readiness context only;
	// review and decision inventory are independently scoped to all live roots.
	projectTasks := materializedTasks
	projectRoots := projectOrchestrationRootTasks(projectTasks)
	candidateRoots := projectOrchestrationCandidateRoots(projectRoots, limit)
	tasks := materializedProjectOrchestrationContextForCandidates(projectTasks, candidateRoots)
	if err := a.enrichPendingDecisions(ctx, projectID, issueClient, &snapshot, projectRoots); err != nil {
		return protocol.OrchestrationSnapshot{}, err
	}
	snapshot.ReviewQueue, err = a.reviewQueue(ctx, projectID, request, projectTasks)
	if err != nil {
		return protocol.OrchestrationSnapshot{}, err
	}
	tasksByID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		tasksByID[task.ID.String()] = task
	}
	projectTasksByID := make(map[string]domain.Task, len(projectTasks))
	for _, task := range projectTasks {
		projectTasksByID[task.ID.String()] = task
	}
	openIssueCount := canonicalOpenIssueCount(projectTasks)
	snapshot.Health = orchestrationBoardHealth(projectTasks, projectTasksByID, openIssueCount, limit, a.openIssueLimit())
	for _, task := range candidateRoots {
		snapshot.Candidates = append(snapshot.Candidates, orchestrationCandidateForTask(task, request.ActorID, snapshot.GeneratedAt, snapshot.Health.Diagnostics))
	}
	sort.SliceStable(snapshot.Candidates, func(i, j int) bool {
		left, right := snapshot.Candidates[i], snapshot.Candidates[j]
		return orchestrationCandidateLess(left, right, tasksByID)
	})
	snapshot.Health.InspectedCount = len(snapshot.Candidates)
	for _, rootTask := range candidateRoots {
		root := rootTask.ID.String()
		snapshot.Roots = append(snapshot.Roots, root)
		ready, err := a.daemon.taskGraphReadinessForActor(ctx, projectID, root, request.ActorID)
		if err != nil {
			snapshot.Blocked[root] = err.Error()
			continue
		}
		var part protocol.OrchestrationSnapshot
		if err := orchestrationTranscode(ready, &part); err != nil {
			return protocol.OrchestrationSnapshot{}, err
		}
		mergeOrchestrationSnapshot(&snapshot, part)
	}
	constrainProjectOrchestrationSnapshotToRoots(&snapshot, candidateRoots)
	if globalActive := orchestrationGlobalActiveCount(projectTasks); globalActive > snapshot.Capacity.TotalCountingCapacityCount {
		snapshot.Capacity.TotalCountingCapacityCount = globalActive
	}
	explainOrchestrationCandidates(&snapshot)
	a.enrichStewardshipContext(ctx, projectID, &snapshot)
	sortOrchestrationSnapshot(&snapshot, tasksByID)
	snapshot.Completion = projectOrchestrationCompletion(snapshot)
	finalizeOrchestrationSnapshotSource(&snapshot)
	return snapshot, nil
}

func canonicalOpenIssueCount(tasks []domain.Task) int {
	count := 0
	for _, task := range tasks {
		if task.IssueFacts().LifecycleState == domain.IssueWorkflowOpen {
			count++
		}
	}
	return count
}

func materializedProjectOrchestrationContext(tasks []domain.Task, limit int) []domain.Task {
	return materializedProjectOrchestrationContextForCandidates(tasks, projectOrchestrationCandidateRoots(projectOrchestrationRootTasks(tasks), limit))
}

func projectOrchestrationCandidateRoots(roots []domain.Task, limit int) []domain.Task {
	open := make([]domain.Task, 0, len(roots))
	active := make([]domain.Task, 0, len(roots))
	for _, task := range roots {
		if task.IssueFacts().LifecycleState == domain.IssueWorkflowOpen {
			open = append(open, task)
		}
		if task.HasTmuxSession {
			active = append(active, task)
		}
	}
	sort.SliceStable(open, func(i, j int) bool { return orchestrationTaskLess(open[i], open[j]) })
	sort.SliceStable(active, func(i, j int) bool { return orchestrationTaskLess(active[i], active[j]) })
	if limit > 0 && len(open) > limit {
		open = open[:limit]
	}
	selected := make(map[string]struct{}, len(open)+len(active))
	for _, task := range open {
		selected[task.ID.String()] = struct{}{}
	}
	for _, task := range active {
		if _, ok := selected[task.ID.String()]; ok {
			continue
		}
		open = append(open, task)
	}
	return open
}

func materializedProjectOrchestrationContextForCandidates(tasks, candidates []domain.Task) []domain.Task {
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		id := strings.TrimSpace(task.ID.String())
		if id == "" {
			continue
		}
		byID[id] = task
	}
	selected := make(map[string]struct{}, len(candidates)*2)
	for _, candidate := range candidates {
		candidateID := candidate.ID.String()
		selected[candidateID] = struct{}{}
		for _, dependency := range candidate.Dependencies {
			if dependencyID := strings.TrimSpace(dependency.ID.String()); dependencyID != "" {
				if _, ok := byID[dependencyID]; ok {
					selected[dependencyID] = struct{}{}
				}
			}
		}
	}
	context := make([]domain.Task, 0, len(selected))
	for _, task := range tasks {
		if _, ok := selected[task.ID.String()]; ok {
			context = append(context, task)
		}
	}
	return context
}

func constrainProjectOrchestrationSnapshotToRoots(snapshot *protocol.OrchestrationSnapshot, roots []domain.Task) {
	rootIDs := make(map[string]bool, len(roots))
	for _, root := range roots {
		rootIDs[root.ID.String()] = true
	}
	filterIDs := func(ids []string) []string {
		out := ids[:0]
		for _, id := range ids {
			if rootIDs[strings.TrimSpace(id)] {
				out = append(out, id)
			}
		}
		return out
	}
	snapshot.Runnable = filterIDs(snapshot.Runnable)
	snapshot.Active = filterIDs(snapshot.Active)
	snapshot.Capacity.DirectRunnableCount = len(snapshot.Runnable)
	snapshot.Capacity.DirectActiveCount = len(snapshot.Active)

	activeSessions := snapshot.ActiveSessions[:0]
	for _, session := range snapshot.ActiveSessions {
		if rootIDs[strings.TrimSpace(session.IssueID)] {
			activeSessions = append(activeSessions, session)
		}
	}
	snapshot.ActiveSessions = activeSessions

	pending := snapshot.Pending[:0]
	for _, start := range snapshot.Pending {
		if rootIDs[strings.TrimSpace(start.IssueID)] {
			pending = append(pending, start)
		}
	}
	snapshot.Pending = pending

	nestedRoots := snapshot.NestedRoots[:0]
	for _, nested := range snapshot.NestedRoots {
		if rootIDs[strings.TrimSpace(nested.IssueID)] {
			nestedRoots = append(nestedRoots, nested)
		}
	}
	snapshot.NestedRoots = nestedRoots

	for issueID := range snapshot.Blocked {
		if !rootIDs[strings.TrimSpace(issueID)] {
			delete(snapshot.Blocked, issueID)
		}
	}
}

func projectOrchestrationRootTasks(tasks []domain.Task) []domain.Task {
	roots := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.ParentID != nil && !task.ParentID.IsZero() {
			continue
		}
		state, err := task.IssueState()
		if err != nil || state.IsClosed() || state.IsArchived() {
			continue
		}
		roots = append(roots, task)
	}
	return roots
}

func finalizeOrchestrationSnapshotSource(snapshot *protocol.OrchestrationSnapshot) {
	if snapshot == nil {
		return
	}
	normalized := *snapshot
	normalized.GeneratedAt = time.Time{}
	normalized.Revision = 0
	normalized.Source.SemanticChecksum = ""
	snapshot.Source.SemanticChecksum = checksumJSON(normalized)
}

func (a daemonOrchestrationAuthority) enrichPendingDecisions(ctx context.Context, projectID string, issueClient *issues.Client, snapshot *protocol.OrchestrationSnapshot, tasks []domain.Task) error {
	if err := a.daemon.reconcileDecisionPropagationOutbox(ctx, projectID); err != nil {
		return fmt.Errorf("reconcile pending decisions: %w", err)
	}
	issueIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task.Status != domain.StatusDone {
			issueIDs = append(issueIDs, task.ID.String())
		}
	}
	eventsByIssue, err := issueClient.ListIssueDecisionObservationEventsByIssue(ctx, issueIDs)
	if err != nil {
		return fmt.Errorf("load pending decisions: %w", err)
	}
	for _, issueID := range issueIDs {
		pending := domain.ReducePendingDecisionChanges(eventsByIssue[issueID])
		if len(pending) == 0 {
			continue
		}
		if snapshot.PendingDecisions == nil {
			snapshot.PendingDecisions = make(map[string][]domain.PendingDecisionChange)
		}
		snapshot.PendingDecisions[issueID] = pending
		reasons := pendingDecisionReadinessReasons(pending)
		if snapshot.Blocked == nil {
			snapshot.Blocked = make(map[string]string)
		}
		snapshot.Blocked[issueID] = mergeOrchestrationBlockerReasons(snapshot.Blocked[issueID], reasons...)
	}
	return nil
}

func mergeOrchestrationBlockerReasons(existing string, additions ...string) string {
	ordered := make([]string, 0, len(additions)+1)
	seen := make(map[string]struct{}, len(additions)+1)
	add := func(raw string) {
		for _, part := range strings.Split(raw, ";") {
			reason := strings.TrimSpace(part)
			if reason == "" {
				continue
			}
			if _, duplicate := seen[reason]; duplicate {
				continue
			}
			seen[reason] = struct{}{}
			ordered = append(ordered, reason)
		}
	}
	add(existing)
	for _, addition := range additions {
		add(addition)
	}
	return strings.Join(ordered, "; ")
}

func projectOrchestrationCompletion(snapshot protocol.OrchestrationSnapshot) protocol.OrchestrationCompletion {
	reasons := make([]string, 0, 6)
	checks := []struct {
		count int
		label string
	}{
		{snapshot.Health.OpenIssueCount, "open issues remain"},
		{len(snapshot.ActiveSessions), "active worker sessions remain"},
		{len(snapshot.Pending), "session starts remain pending"},
		{len(snapshot.Reviews), "review requests remain"},
		{len(snapshot.Interactions), "human interactions remain unresolved"},
	}
	for _, check := range checks {
		if check.count > 0 {
			reasons = append(reasons, fmt.Sprintf("%d %s", check.count, check.label))
		}
	}
	if !snapshot.Health.Healthy {
		reasons = append(reasons, "board health is not healthy: "+strings.Join(snapshot.Health.Diagnostics, "; "))
	}
	return protocol.OrchestrationCompletion{Scope: snapshot.Scope, State: snapshot.Lifecycle, Pass: len(reasons) == 0, Reasons: reasons}
}

func orchestrationScopeCommands(scope domain.OrchestrationScope) []string {
	rootFlag := ""
	if scope.Kind == domain.OrchestrationScopeRooted {
		rootFlag = " --root " + scope.RootIssueID.String()
	}
	return []string{
		"az orchestrate status" + rootFlag,
		"az orchestrate start" + rootFlag,
		"az orchestrate watch" + rootFlag + " --since <cursor> --jsonl",
		"az orchestrate complete-check" + rootFlag,
	}
}

func (a daemonOrchestrationAuthority) enrichStewardshipContext(ctx context.Context, projectID string, snapshot *protocol.OrchestrationSnapshot) {
	for _, candidate := range snapshot.Candidates {
		switch candidate.Classification {
		case string(domain.OrchestrationCandidateReviewReady):
			snapshot.Reviews = append(snapshot.Reviews, candidate)
		case string(domain.OrchestrationCandidateOwnedElsewhere):
			snapshot.OwnershipConflicts = append(snapshot.OwnershipConflicts, candidate)
		}
	}
	if issueClient := a.daemon.issueClientForProject(projectID); issueClient != nil {
		if interactions, err := issueClient.ListInteractions(ctx); err == nil {
			for _, interaction := range interactions {
				if interaction.Unresolved() && orchestrationInteractionInScope(interaction, snapshot.Scope) {
					snapshot.Interactions = append(snapshot.Interactions, interaction)
				}
			}
		}
	}
	repoDir := strings.TrimSpace(a.daemon.resolveRepoDirForProject(projectID))
	if repoDir != "" {
		parents := append([]string(nil), snapshot.Roots...)
		if snapshot.Scope.Kind == domain.OrchestrationScopeRooted {
			parents = []string{snapshot.Scope.RootIssueID.String()}
		}
		var recent []daemonMailEvent
		for _, parent := range parents {
			if events, err := readMailboxEvents(repoDir, parent); err == nil {
				recent = append(recent, events...)
			}
		}
		sort.SliceStable(recent, func(i, j int) bool {
			if recent[i].CreatedAt.Equal(recent[j].CreatedAt) {
				return recent[i].ParentIssue < recent[j].ParentIssue
			}
			return recent[i].CreatedAt.Before(recent[j].CreatedAt)
		})
		const recentLimit = 20
		if len(recent) > recentLimit {
			recent = recent[len(recent)-recentLimit:]
		}
		for _, event := range recent {
			snapshot.RecentEvents = append(snapshot.RecentEvents, mailEventToProtocol(event))
			if event.Seq > snapshot.Cursor {
				snapshot.Cursor = event.Seq
			}
		}
	}
}

func orchestrationInteractionInScope(interaction domain.InteractionRequest, scope domain.OrchestrationScope) bool {
	if scope.Kind == domain.OrchestrationScopeProject {
		return true
	}
	return interaction.OrchestrationScope == scope.RootIssueID.String() || interaction.IssueID == scope.RootIssueID.String()
}

func explainOrchestrationCandidates(snapshot *protocol.OrchestrationSnapshot) {
	runnable := make(map[string]bool, len(snapshot.Runnable))
	active := make(map[string]bool, len(snapshot.Active))
	for _, id := range snapshot.Runnable {
		runnable[id] = true
	}
	for _, id := range snapshot.Active {
		active[id] = true
	}
	for i := range snapshot.Candidates {
		candidate := &snapshot.Candidates[i]
		if candidate.Classification == "malformed" || candidate.Classification == string(domain.OrchestrationCandidateOwnedElsewhere) {
			continue
		}
		switch {
		case candidate.Classification == string(domain.OrchestrationCandidateDecisionWaiting):
			// A durable whole-issue interaction is the specific source of the
			// block and must remain visible as Waiting Human.
			continue
		case candidate.Classification != string(domain.OrchestrationCandidateOpen):
			// Canonical lifecycle/ownership classification precedes rooted graph
			// refinement. In particular, a backlog exclusion reported by graph
			// readiness must not collapse the project candidate into generic blocked.
			continue
		case snapshot.Blocked[candidate.IssueID] != "":
			candidate.Included, candidate.Eligible, candidate.Classification, candidate.Reason = false, false, string(domain.OrchestrationCandidateBlocked), "excluded: "+snapshot.Blocked[candidate.IssueID]
			candidate.ExclusionReasons = append(candidate.ExclusionReasons, snapshot.Blocked[candidate.IssueID])
		case !candidate.Sufficient && candidate.Executability.Disposition != "":
			candidate.Included, candidate.Eligible, candidate.Classification = false, false, string(candidate.Executability.Disposition)
			candidate.Reason = "excluded: " + strings.Join(candidate.Executability.Reasons, "; ")
			candidate.ExclusionReasons = append(candidate.ExclusionReasons, candidate.Executability.Reasons...)
		case active[candidate.IssueID]:
			candidate.Included, candidate.Eligible, candidate.Classification, candidate.Reason = false, false, string(domain.OrchestrationCandidateActive), "excluded: session already active"
			candidate.ExclusionReasons = append(candidate.ExclusionReasons, "session-already-active")
		case runnable[candidate.IssueID]:
			candidate.Included, candidate.Eligible, candidate.Classification, candidate.Reason = true, true, "runnable", "included: ready for worker start"
		default:
			candidate.Included, candidate.Eligible, candidate.Classification, candidate.Reason = false, false, "not-runnable", "excluded: not ready in current graph projection"
			candidate.ExclusionReasons = append(candidate.ExclusionReasons, "not-ready-in-current-graph")
		}
	}
}

func (a daemonOrchestrationAuthority) Apply(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest) (protocol.OrchestrationIntentResult, error) {
	if !validOrchestrationIntentKind(request.Kind) {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("unsupported orchestration intent %q", request.Kind)
	}
	if strings.TrimSpace(request.IntentKey) == "" {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("orchestration intent_key is required")
	}
	if err := validateOrchestrationReviewIntent(request); err != nil {
		return protocol.OrchestrationIntentResult{}, err
	}
	if request.Kind != protocol.OrchestrationIntentStart {
		return a.applyReviewIntent(ctx, projectID, request)
	}
	if request.Scope.Kind != domain.OrchestrationScopeProject && len(request.Routes) > 0 {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("candidate routing is available only for project orchestration scope")
	}
	// Keep readiness, capacity selection, claims, and operation submission in
	// one daemon-authoritative critical section. Ownership claims remain the
	// durable cross-process conflict gate for individual issues.
	a.daemon.orchestrationMu.Lock()
	defer a.daemon.orchestrationMu.Unlock()
	snapshot, err := a.Snapshot(ctx, projectID, protocol.OrchestrationSnapshotRequest{Scope: request.Scope, ActorID: request.ActorID})
	if err != nil {
		return protocol.OrchestrationIntentResult{}, err
	}
	if request.Scope.Kind == domain.OrchestrationScopeProject && !snapshot.Health.Healthy {
		if !request.OverrideBoardHealth || !orchestrationHealthOverrideAllowed(snapshot.Health) {
			return protocol.OrchestrationIntentResult{}, fmt.Errorf("project board health refused start: %s (only the open-issue threshold may be explicitly overridden)", strings.Join(snapshot.Health.Diagnostics, "; "))
		}
	}
	runnable := make(map[string]struct{}, len(snapshot.Runnable))
	candidates := make(map[string]struct{}, len(snapshot.Candidates))
	for _, candidate := range snapshot.Candidates {
		candidates[candidate.IssueID] = struct{}{}
	}
	active := make(map[string]struct{}, len(snapshot.Active))
	nestedRoots := make(map[string]struct{}, len(snapshot.NestedRoots))
	for _, id := range snapshot.Runnable {
		runnable[id] = struct{}{}
	}
	for _, id := range snapshot.Active {
		active[id] = struct{}{}
	}
	for _, nested := range snapshot.NestedRoots {
		nestedRoots[nested.IssueID] = struct{}{}
	}
	requested := append([]string(nil), request.IssueIDs...)
	if len(requested) == 0 {
		requested = append(requested, snapshot.Runnable...)
	} else {
		requested = stableRequestedCandidates(requested, snapshot.Runnable)
	}
	result := protocol.OrchestrationIntentResult{Scope: request.Scope, Kind: request.Kind, IntentKey: request.IntentKey, Requested: requested, Skipped: map[string]string{}, Failed: map[string]string{}}
	routedIssues := map[string]struct{}{}
	if request.Scope.Kind == domain.OrchestrationScopeProject {
		issueClient := a.daemon.issueClientForProject(projectID)
		if issueClient == nil {
			return protocol.OrchestrationIntentResult{}, fmt.Errorf("issue store unavailable")
		}
		candidateIDs := make(map[string]bool, len(snapshot.Candidates))
		for _, candidate := range snapshot.Candidates {
			candidateIDs[candidate.IssueID] = true
		}
		explicitlyRequested := make(map[string]bool, len(request.IssueIDs))
		for _, issueID := range request.IssueIDs {
			explicitlyRequested[strings.TrimSpace(issueID)] = true
		}
		for _, route := range request.Routes {
			if issueID := strings.TrimSpace(route.IssueID); !candidateIDs[issueID] {
				result.Failed[issueID] = "route candidate: issue is outside the bounded project candidate snapshot"
			} else if len(explicitlyRequested) > 0 && !explicitlyRequested[issueID] {
				result.Failed[issueID] = "route candidate: issue is outside the explicit issue selection"
			}
		}
		for _, route := range projectCandidateRoutes(snapshot, request.Routes, request.IssueIDs) {
			issueID := strings.TrimSpace(route.IssueID)
			routedIssues[issueID] = struct{}{}
			routed, err := issueClient.RouteOrchestrationCandidate(ctx, projectID, request.ActorID, route)
			if err != nil {
				result.Failed[issueID] = "route candidate: " + err.Error()
				continue
			}
			entry := protocol.OrchestrationRouteResult{IssueID: issueID, Kind: routed.Route.Kind, Reason: routed.Route.Reason, MissingDetails: append([]string(nil), routed.Route.MissingDetails...), InteractionCreated: routed.InteractionCreated}
			if routed.Interaction != nil {
				entry.InteractionID = routed.Interaction.ID
			}
			result.Routed = append(result.Routed, entry)
			a.publishOrchestrationRoute(ctx, projectID, routed.Task)
		}
		if len(result.Routed) > 0 && a.daemon.noticeService != nil {
			_ = a.daemon.reconcileInteractionNotices(ctx, projectID)
		}
	}
	limit := request.Limit
	if limit <= 0 {
		limit = a.startLimit()
	}
	if configured := a.startLimit(); limit > configured {
		limit = configured
	}
	remainingCapacity := a.agentCapacity() - snapshot.Capacity.TotalCountingCapacityCount
	capacityLimit := remainingCapacity
	if remainingCapacity < limit {
		limit = remainingCapacity
	}
	if limit < 0 {
		limit = 0
	}
	started := 0
	for _, issueID := range requested {
		if request.Scope.Kind == domain.OrchestrationScopeProject {
			if _, ok := candidates[issueID]; !ok {
				result.Skipped[issueID] = "outside-project-root-candidate-scope"
				continue
			}
		}
		if _, routed := routedIssues[issueID]; routed {
			if _, failed := result.Failed[issueID]; failed {
				result.Skipped[issueID] = "candidate-route-failed"
			} else {
				result.Skipped[issueID] = "candidate-routed-" + routeKindForIssue(result.Routed, issueID)
			}
			continue
		}
		if _, ok := runnable[issueID]; !ok {
			result.Skipped[issueID] = orchestrationSkipReason(issueID, nestedRoots, active, snapshot.Blocked)
			continue
		}
		if started >= limit {
			if started >= capacityLimit {
				result.Skipped[issueID] = "global-agent-capacity-reached"
			} else {
				result.Skipped[issueID] = "wave-limit-reached"
			}
			continue
		}
		launch, err := a.claimAndSubmitStart(ctx, projectID, request, issueID)
		if err != nil {
			result.Failed[issueID] = err.Error()
			continue
		}
		result.Started = append(result.Started, issueID)
		result.Launched = append(result.Launched, launch)
		if launch.OperationState != string(protocol.OperationStateDone) {
			result.Pending = append(result.Pending, protocol.OrchestrationPending{IssueID: issueID, OperationID: launch.OperationID, OperationState: launch.OperationState})
		}
		started++
	}
	return result, nil
}

func projectCandidateRoutes(snapshot protocol.OrchestrationSnapshot, explicit []domain.OrchestrationCandidateRoute, requested []string) []domain.OrchestrationCandidateRoute {
	allowed := make(map[string]bool, len(requested))
	for _, issueID := range requested {
		allowed[strings.TrimSpace(issueID)] = true
	}
	byIssue := make(map[string]domain.OrchestrationCandidateRoute, len(explicit))
	for _, route := range explicit {
		issueID := strings.TrimSpace(route.IssueID)
		if len(allowed) == 0 || allowed[issueID] {
			byIssue[issueID] = route
		}
	}
	ordered := make([]domain.OrchestrationCandidateRoute, 0, len(snapshot.Candidates)+len(explicit))
	seen := make(map[string]bool)
	for _, candidate := range snapshot.Candidates {
		issueID := strings.TrimSpace(candidate.IssueID)
		if len(allowed) > 0 && !allowed[issueID] {
			continue
		}
		if route, ok := byIssue[issueID]; ok {
			ordered, seen[issueID] = append(ordered, route), true
			continue
		}
		if candidate.Classification != string(domain.IssuePremature) {
			continue
		}
		guidance, ok := domain.PrematureRouteGuidance(candidate.Executability)
		if !ok {
			continue
		}
		ordered, seen[issueID] = append(ordered, domain.OrchestrationCandidateRoute{IssueID: issueID, Kind: domain.OrchestrationRouteBacklog, Reason: "project orchestration found an incomplete execution contract", MissingDetails: guidance}), true
	}
	return ordered
}

func (a daemonOrchestrationAuthority) publishOrchestrationRoute(ctx context.Context, projectID string, task domain.Task) {
	if a.daemon.hub == nil {
		return
	}
	if enriched := a.daemon.enrichTasksWithSessionState(ctx, projectID, []domain.Task{task}); len(enriched) == 1 {
		task = enriched[0]
	}
	revision := a.daemon.nextRevision(projectID)
	body, _ := json.Marshal(taskEventBodyFromTask(projectID, task))
	a.daemon.hub.Publish(protocol.EventEnvelope{ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(projectID), Revision: revision, Event: protocol.EventTaskUpdated, Kind: protocol.EnvelopeKindEvent, EmittedAt: time.Now().UTC(), Body: body})
}

func routeKindForIssue(routes []protocol.OrchestrationRouteResult, issueID string) string {
	for _, route := range routes {
		if route.IssueID == issueID {
			return string(route.Kind)
		}
	}
	return "unknown"
}

func orchestrationGlobalActiveCount(tasks []domain.Task) int {
	count := 0
	for _, task := range tasks {
		if task.HasTmuxSession {
			count++
		}
	}
	return count
}

func orchestrationHealthOverrideAllowed(health protocol.OrchestrationHealth) bool {
	if len(health.Diagnostics) == 0 {
		return true
	}
	for _, diagnostic := range health.Diagnostics {
		if !strings.HasPrefix(diagnostic, "open issue count ") {
			return false
		}
	}
	return true
}

func stableRequestedCandidates(requested, orderedRunnable []string) []string {
	wanted := make(map[string]bool, len(requested))
	for _, id := range requested {
		wanted[id] = true
	}
	out := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, id := range orderedRunnable {
		if wanted[id] && !seen[id] {
			out, seen[id] = append(out, id), true
		}
	}
	remainder := make([]string, 0)
	for _, id := range requested {
		if !seen[id] {
			remainder, seen[id] = append(remainder, id), true
		}
	}
	sort.Strings(remainder)
	return append(out, remainder...)
}

func (a daemonOrchestrationAuthority) inspectLimit() int {
	if n := a.daemon.cfg.Orchestration.InspectLimit; n > 0 {
		return n
	}
	return defaultOrchestrationInspectLimit
}
func (a daemonOrchestrationAuthority) startLimit() int {
	if n := a.daemon.cfg.Orchestration.StartLimit; n > 0 {
		return n
	}
	return defaultOrchestrationStartLimit
}
func (a daemonOrchestrationAuthority) agentCapacity() int {
	if n := a.daemon.cfg.Orchestration.AgentCapacity; n > 0 {
		return n
	}
	return defaultOrchestrationAgentCapacity
}
func (a daemonOrchestrationAuthority) openIssueLimit() int {
	if n := a.daemon.cfg.Orchestration.OpenIssueLimit; n > 0 {
		return n
	}
	return defaultOrchestrationOpenIssueLimit
}

func orchestrationBoardHealth(tasks []domain.Task, byID map[string]domain.Task, canonicalOpenCount, inspectLimit, openLimit int) protocol.OrchestrationHealth {
	health := protocol.OrchestrationHealth{Healthy: true, OpenIssueCount: canonicalOpenCount, InspectLimit: inspectLimit, OpenIssueLimit: openLimit}
	for _, task := range tasks {
		if task.Status == domain.StatusDone {
			continue
		}
		id := task.ID.String()
		if task.ParentID != nil && !task.ParentID.IsZero() {
			parent := task.ParentID.String()
			if parent == id {
				health.Diagnostics = append(health.Diagnostics, "malformed graph: "+id+" is its own parent")
			} else if _, ok := byID[parent]; !ok {
				health.Diagnostics = append(health.Diagnostics, "malformed graph: "+id+" has missing parent "+parent)
			}
		}
		if task.Ownership != nil && (strings.TrimSpace(task.Ownership.OwnerID) == "" || strings.TrimSpace(task.Ownership.OwnerKind) == "") {
			health.Diagnostics = append(health.Diagnostics, "malformed ownership: "+id+" has incomplete owner identity")
		}
	}
	for id := range byID {
		seen := map[string]bool{}
		for current := id; current != ""; {
			if seen[current] {
				health.Diagnostics = append(health.Diagnostics, "malformed graph: parent cycle contains "+current)
				break
			}
			seen[current] = true
			task, ok := byID[current]
			if !ok || task.ParentID == nil || task.ParentID.IsZero() {
				break
			}
			current = task.ParentID.String()
		}
	}
	if health.OpenIssueCount > openLimit {
		health.Diagnostics = append(health.Diagnostics, fmt.Sprintf("open issue count %d exceeds refusal threshold %d", health.OpenIssueCount, openLimit))
	}
	sort.Strings(health.Diagnostics)
	health.Diagnostics = uniqueStrings(health.Diagnostics)
	health.Healthy = len(health.Diagnostics) == 0
	return health
}

func orchestrationCandidateForTask(task domain.Task, actorID string, now time.Time, diagnostics []string) protocol.OrchestrationCandidate {
	assessment := domain.AssessOrchestrationCandidate(task, actorID, now, nil)
	c := protocol.OrchestrationCandidate{IssueID: task.ID.String(), Included: assessment.Eligible, Eligible: assessment.Eligible, Sufficient: assessment.Sufficient, Classification: string(assessment.Classification), Sufficiency: assessment.Sufficiency, ExclusionReasons: assessment.ExclusionReasons, Executability: assessment.Executability}
	if assessment.Eligible {
		c.Reason = "included: eligible for bounded readiness inspection"
	} else {
		c.Reason = "excluded: " + strings.Join(assessment.ExclusionReasons, "; ")
	}
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, ": "+task.ID.String()+" ") {
			c.Included, c.Eligible, c.Classification, c.Reason = false, false, "malformed", diagnostic
			c.ExclusionReasons = append(c.ExclusionReasons, diagnostic)
			break
		}
	}
	return c
}

func orchestrationCandidateLess(left, right protocol.OrchestrationCandidate, byID map[string]domain.Task) bool {
	l, r := byID[left.IssueID], byID[right.IssueID]
	if l.Priority != r.Priority {
		return l.Priority < r.Priority
	}
	if left.Included != right.Included {
		return left.Included
	}
	if !l.UpdatedAt.Equal(r.UpdatedAt) {
		return l.UpdatedAt.Before(r.UpdatedAt)
	}
	return left.IssueID < right.IssueID
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func orchestrationSkipReason(issueID string, nestedRoots, active map[string]struct{}, blocked map[string]string) string {
	if reason := blocked[issueID]; reason != "" {
		return reason
	}
	if _, ok := nestedRoots[issueID]; ok {
		return fmt.Sprintf("nested-root-start-orchestrator-session: az orchestrator-session start --root %s", issueID)
	}
	if _, ok := active[issueID]; ok {
		return "session-already-running"
	}
	return "not-runnable"
}

func (a daemonOrchestrationAuthority) claimAndSubmitStart(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string) (protocol.OrchestrationLaunch, error) {
	return a.claimAndSubmitStartWithPrompt(ctx, projectID, request, issueID, "")
}

func (a daemonOrchestrationAuthority) claimAndSubmitStartWithPrompt(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID, prompt string) (_ protocol.OrchestrationLaunch, retErr error) {
	parsed, err := naming.ParseIssueID(issueID)
	if err != nil {
		return protocol.OrchestrationLaunch{}, err
	}
	baseReq := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("orchestration-" + request.IntentKey + "-" + issueID), Kind: protocol.EnvelopeKindCommand, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID), ClientActor: request.ActorID}}
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return protocol.OrchestrationLaunch{}, fmt.Errorf("issue store unavailable")
	}
	dedupeKey := orchestrationStartDedupeKey(issueID, request.IntentKey)
	attempt, err := issueClient.BeginOrchestrationStart(ctx, projectID, issueID, request.IntentKey, request.ActorID, dedupeKey)
	if err != nil {
		return protocol.OrchestrationLaunch{}, fmt.Errorf("claim orchestration start: %w", err)
	}
	completed := false
	defer func() {
		if retErr == nil || completed {
			return
		}
		compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		retErr = a.compensateStartFailure(compensationCtx, issueClient, attempt, retErr)
	}()
	sessionID := parsed.String()
	if repoDir := strings.TrimSpace(request.RepoDir); repoDir != "" {
		sessionID = naming.CanonicalSessionIDForIssue(repoDir, parsed).String()
	}
	payload, err := json.Marshal(sessionCommandBody{ProjectID: projectID, SessionID: sessionID, BaseBranch: request.BaseBranch, Prompt: strings.TrimSpace(prompt)})
	if err != nil {
		return protocol.OrchestrationLaunch{}, fmt.Errorf("marshal session start: %w", err)
	}
	resources := []string{"issue:" + projectID + ":" + issueID, "worktree:" + issueID, "session:" + sessionID}
	submitBody, err := json.Marshal(protocol.OperationSubmitRequestBody{ProjectID: naming.ProjectID(projectID), Kind: "session.start", IssueID: parsed, DedupeKey: dedupeKey, ResourceKeys: resources, Payload: payload})
	if err != nil {
		return protocol.OrchestrationLaunch{}, fmt.Errorf("marshal operation submit: %w", err)
	}
	submitReq := baseReq
	submitReq.Command, submitReq.Body = protocol.CommandOperationSubmit, submitBody
	var submitResp protocol.ResponseEnvelope
	if a.submitStart != nil {
		submitResp = a.submitStart(ctx, submitReq)
	} else {
		submitResp = a.daemon.operationRuntime.handleOperationSubmit(ctx, submitReq)
	}
	if !submitResp.OK {
		failure := errors.New("submit session start failed")
		if submitResp.Error != nil {
			failure = fmt.Errorf("submit session start: %s", submitResp.Error.Message)
		}
		return protocol.OrchestrationLaunch{}, failure
	}
	var launchShape struct {
		Operation *struct {
			OperationID json.RawMessage `json:"operation_id"`
		} `json:"operation"`
	}
	if err := json.Unmarshal(submitResp.Body, &launchShape); err != nil {
		return protocol.OrchestrationLaunch{}, fmt.Errorf("decode session start submission shape: %w", err)
	}
	if launchShape.Operation == nil {
		return protocol.OrchestrationLaunch{}, &invalidOrchestrationLaunchError{Field: "operation_id"}
	}
	operationIDShape := strings.TrimSpace(string(launchShape.Operation.OperationID))
	if operationIDShape == "" || operationIDShape == "null" || operationIDShape == `""` {
		return protocol.OrchestrationLaunch{}, &invalidOrchestrationLaunchError{Field: "operation_id"}
	}
	var submitted protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(submitResp.Body, &submitted); err != nil {
		return protocol.OrchestrationLaunch{}, fmt.Errorf("decode session start submission: %w", err)
	}
	if strings.TrimSpace(submitted.Operation.OperationID.String()) == "" {
		return protocol.OrchestrationLaunch{}, &invalidOrchestrationLaunchError{Field: "operation_id"}
	}
	if err := issueClient.CompleteOrchestrationStart(ctx, attempt, submitted.Operation.OperationID.String()); err != nil {
		return protocol.OrchestrationLaunch{}, fmt.Errorf("record submitted orchestration start: %w", err)
	}
	completed = true
	return protocol.OrchestrationLaunch{IssueID: issueID, SessionID: sessionID, OperationID: submitted.Operation.OperationID.String(), OperationState: string(submitted.Operation.State)}, nil
}

func orchestrationStartDedupeKey(issueID, intentKey string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(intentKey)))
	return fmt.Sprintf("session.start:%s:%x", strings.TrimSpace(issueID), digest)
}

func (a daemonOrchestrationAuthority) compensateStartFailure(ctx context.Context, issueClient *issues.Client, attempt issues.OrchestrationStartAttempt, cause error) error {
	if err := issueClient.CompensateOrchestrationStart(ctx, attempt, cause); err != nil {
		return fmt.Errorf("%v; persist orchestration start compensation: %w", cause, err)
	}
	return cause
}

func (d *Daemon) reconcileOrchestrationStartOperation(ctx context.Context, record daemonops.Record) {
	if d == nil || record.Kind != "session.start" || (record.State != daemonops.StateFailed && record.State != daemonops.StateCancelled) {
		return
	}
	if d.tmux != nil {
		sessionID := ""
		for _, resourceKey := range record.ResourceKeys {
			if strings.HasPrefix(resourceKey, "session:") {
				sessionID = strings.TrimSpace(strings.TrimPrefix(resourceKey, "session:"))
				break
			}
		}
		if sessionID == "" {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("skip orchestration start compensation without session identity", "project_id", record.ProjectID, "issue_id", record.IssueID, "operation_id", record.ID)
			}
			return
		}
		live, err := d.tmux.HasSession(ctx, sessionID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("skip orchestration start compensation after tmux probe failure", "project_id", record.ProjectID, "issue_id", record.IssueID, "operation_id", record.ID, "session_id", sessionID, "error", err)
			}
			return
		}
		if live {
			return
		}
	}
	issueClient := d.issueClientForProject(record.ProjectID)
	if issueClient == nil {
		return
	}
	cause := errors.New(strings.TrimSpace(record.ErrorMessage))
	if strings.TrimSpace(record.ErrorMessage) == "" {
		cause = fmt.Errorf("session start operation ended in %s", record.State)
	}
	if _, err := issueClient.CompensateOrchestrationStartOperation(ctx, d.canonicalProjectID(record.ProjectID), record.DedupeKey, record.ID, cause); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("compensate terminal orchestration start failed", "project_id", record.ProjectID, "issue_id", record.IssueID, "operation_id", record.ID, "state", record.State, "error", err)
	}
}

func orchestrationTranscode(in, out any) error {
	encoded, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

func mergeOrchestrationSnapshot(dst *protocol.OrchestrationSnapshot, src protocol.OrchestrationSnapshot) {
	dst.Runnable = append(dst.Runnable, src.Runnable...)
	dst.NestedRoots = append(dst.NestedRoots, src.NestedRoots...)
	dst.Pending = append(dst.Pending, src.Pending...)
	dst.Active = append(dst.Active, src.Active...)
	dst.ActiveSessions = append(dst.ActiveSessions, src.ActiveSessions...)
	dst.SessionStartProgress = append(dst.SessionStartProgress, src.SessionStartProgress...)
	dst.StaleCloseableChildren = append(dst.StaleCloseableChildren, src.StaleCloseableChildren...)
	dst.ContainmentRisks = append(dst.ContainmentRisks, src.ContainmentRisks...)
	dst.WorkerObservations = append(dst.WorkerObservations, src.WorkerObservations...)
	for id, reason := range src.Blocked {
		dst.Blocked[id] = reason
	}
	dst.Capacity.DirectRunnableCount += src.Capacity.DirectRunnableCount
	dst.Capacity.DirectActiveCount += src.Capacity.DirectActiveCount
	dst.Capacity.NestedStartableCount += src.Capacity.NestedStartableCount
	dst.Capacity.NestedActiveCount += src.Capacity.NestedActiveCount
	dst.Capacity.PendingStartsCount += src.Capacity.PendingStartsCount
	dst.Capacity.BlockedNestedRootsCount += src.Capacity.BlockedNestedRootsCount
	dst.Capacity.NotCountingCapacityCount += src.Capacity.NotCountingCapacityCount
	dst.Capacity.TotalCountingCapacityCount += src.Capacity.TotalCountingCapacityCount
}

func sortOrchestrationSnapshot(s *protocol.OrchestrationSnapshot, tasksByID ...map[string]domain.Task) {
	if len(tasksByID) > 0 {
		byID := tasksByID[0]
		sort.SliceStable(s.Runnable, func(i, j int) bool { return orchestrationTaskLess(byID[s.Runnable[i]], byID[s.Runnable[j]]) })
	} else {
		sort.Strings(s.Runnable)
	}
	sort.Strings(s.Active)
	sort.Slice(s.NestedRoots, func(i, j int) bool { return s.NestedRoots[i].IssueID < s.NestedRoots[j].IssueID })
	sort.Slice(s.ActiveSessions, func(i, j int) bool { return s.ActiveSessions[i].IssueID < s.ActiveSessions[j].IssueID })
}

func orchestrationTaskLess(left, right domain.Task) bool {
	if left.Priority != right.Priority {
		return left.Priority < right.Priority
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.Before(right.UpdatedAt)
	}
	return left.ID.String() < right.ID.String()
}
