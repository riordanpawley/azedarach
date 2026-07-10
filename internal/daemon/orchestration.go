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
)

const (
	defaultOrchestrationInspectLimit   = 50
	defaultOrchestrationStartLimit     = 3
	defaultOrchestrationAgentCapacity  = 12
	defaultOrchestrationOpenIssueLimit = 100
)

// orchestrationAuthority is the deliberately small daemon boundary for all
// rooted and project-wide orchestration clients.
type orchestrationAuthority interface {
	Snapshot(context.Context, string, protocol.OrchestrationSnapshotRequest) (protocol.OrchestrationSnapshot, error)
	Apply(context.Context, string, protocol.OrchestrationIntentRequest) (protocol.OrchestrationIntentResult, error)
}

type daemonOrchestrationAuthority struct{ daemon *Daemon }

func (d *Daemon) orchestrationAuthority() orchestrationAuthority {
	return daemonOrchestrationAuthority{daemon: d}
}

func (d *Daemon) handleOrchestrationSnapshot(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var body protocol.OrchestrationSnapshotRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	lease, found, err := d.resolveOrchestratorSession(ctx, d.projectID(req.Meta), body.SessionID)
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
	if found && body.ObservedCursor > lease.Cursor {
		lease, err = daemonstate.NewOrchestratorLeaseAuthority(d.sessionRuntimeStateStoreIfConfigured(d.projectID(req.Meta))).AdvanceCursor(ctx, lease.Identity, body.ObservedCursor)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("advance orchestration cursor: %v", err)), nil
		}
	}
	if strings.TrimSpace(body.ActorID) == "" {
		body.ActorID = strings.TrimSpace(req.Meta.ClientActor)
	}
	projectID := d.projectID(req.Meta)
	var snapshot protocol.OrchestrationSnapshot
	var snapshotRevision uint64
	stable := false
	for attempt := 0; attempt < 3; attempt++ {
		before := d.currentRevision(projectID)
		snapshot, err = d.orchestrationAuthority().Snapshot(ctx, projectID, body)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		after := d.currentRevision(projectID)
		if before == after {
			snapshotRevision = after
			stable = true
			break
		}
	}
	if !stable {
		return d.errorResponse(req, protocol.ErrorCodeConflict, "orchestration projection changed while building snapshot; retry"), nil
	}
	snapshot.Role = "orchestrator"
	snapshot.SessionID = strings.TrimSpace(body.SessionID)
	if found {
		snapshot.Lifecycle = lease.Lifecycle
		snapshot.Cursor = lease.Cursor
		applyOrchestratorContinuationProjection(&snapshot, lease)
	}
	snapshot.Revision = snapshotRevision
	if snapshot.Cursor == 0 && !found {
		snapshot.Cursor = int64(snapshotRevision)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body, resp.Revision = encoded, snapshot.Revision
	return resp, nil
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
	return !completeCheckPassed && len(snapshot.Interactions) == 0 && len(snapshot.NestedRoots) > 0
}

func orchestratorContinuationPrompt(lease daemonstate.OrchestratorScopeLease, nested []protocol.OrchestrationNestedRoot) string {
	ids := make([]string, 0, len(nested))
	for _, item := range nested {
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
	if body.Kind != protocol.OrchestrationIntentStart || strings.TrimSpace(body.IntentKey) == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "start orchestration intent with intent_key is required"), nil
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
	snapshot := protocol.OrchestrationSnapshot{
		Scope: identity.Scope, GeneratedAt: time.Now().UTC(), Blocked: map[string]string{},
		Constraints: protocol.OrchestrationConstraints{
			InspectLimit: limit, StartLimit: a.startLimit(), AgentCapacity: a.agentCapacity(),
			Commands:       orchestrationScopeCommands(identity.Scope),
			RoleGuardrails: []string{"remain in the active orchestration loop", "do not implement worker issue scope", "preserve sessions during review handoff"},
		},
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
		return snapshot, nil
	}

	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return protocol.OrchestrationSnapshot{}, fmt.Errorf("issue store unavailable")
	}
	tasks, err := issueClient.ListGraphReadinessWithRuntime(ctx, projectID, "", limit)
	if err != nil {
		return protocol.OrchestrationSnapshot{}, fmt.Errorf("load project orchestration projection: %w", err)
	}
	tasks = a.daemon.enrichTasksWithSessionState(ctx, projectID, tasks)
	roots := make([]domain.Task, 0)
	tasksByID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		tasksByID[task.ID.String()] = task
		if (task.ParentID == nil || task.ParentID.IsZero()) && task.Status != domain.StatusDone {
			roots = append(roots, task)
		}
	}
	snapshot.Health = orchestrationBoardHealth(tasks, tasksByID, limit, a.openIssueLimit())
	openIssueCount, err := issueClient.CountOpenOrchestrationIssues(ctx)
	if err != nil {
		return protocol.OrchestrationSnapshot{}, fmt.Errorf("count project orchestration issues: %w", err)
	}
	snapshot.Health.OpenIssueCount = openIssueCount
	if openIssueCount > snapshot.Health.OpenIssueLimit {
		snapshot.Health.Diagnostics = append(snapshot.Health.Diagnostics, fmt.Sprintf("open issue count %d exceeds refusal threshold %d", openIssueCount, snapshot.Health.OpenIssueLimit))
		sort.Strings(snapshot.Health.Diagnostics)
		snapshot.Health.Diagnostics = uniqueStrings(snapshot.Health.Diagnostics)
		snapshot.Health.Healthy = false
	}
	for _, task := range tasks {
		if task.Status == domain.StatusDone {
			continue
		}
		snapshot.Candidates = append(snapshot.Candidates, orchestrationCandidateForTask(task, request.ActorID, snapshot.GeneratedAt, snapshot.Health.Diagnostics))
	}
	sort.SliceStable(snapshot.Candidates, func(i, j int) bool {
		left, right := snapshot.Candidates[i], snapshot.Candidates[j]
		return orchestrationCandidateLess(left, right, tasksByID)
	})
	if len(snapshot.Candidates) > limit {
		snapshot.Candidates = snapshot.Candidates[:limit]
	}
	snapshot.Health.InspectedCount = len(snapshot.Candidates)
	sort.SliceStable(roots, func(i, j int) bool { return orchestrationTaskLess(roots[i], roots[j]) })
	if len(roots) > limit {
		roots = roots[:limit]
	}
	for _, rootTask := range roots {
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
	if globalActive := orchestrationGlobalActiveCount(tasks); globalActive > snapshot.Capacity.TotalCountingCapacityCount {
		snapshot.Capacity.TotalCountingCapacityCount = globalActive
	}
	explainOrchestrationCandidates(&snapshot)
	a.enrichStewardshipContext(ctx, projectID, &snapshot)
	sortOrchestrationSnapshot(&snapshot, tasksByID)
	return snapshot, nil
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
		case snapshot.Blocked[candidate.IssueID] != "":
			candidate.Included, candidate.Eligible, candidate.Classification, candidate.Reason = false, false, string(domain.OrchestrationCandidateBlocked), "excluded: "+snapshot.Blocked[candidate.IssueID]
			candidate.ExclusionReasons = append(candidate.ExclusionReasons, snapshot.Blocked[candidate.IssueID])
		case candidate.Classification != string(domain.OrchestrationCandidateOpen):
			continue
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
	if request.Kind != protocol.OrchestrationIntentStart {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("unsupported orchestration intent %q", request.Kind)
	}
	if strings.TrimSpace(request.IntentKey) == "" {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("orchestration intent_key is required")
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

func orchestrationBoardHealth(tasks []domain.Task, byID map[string]domain.Task, inspectLimit, openLimit int) protocol.OrchestrationHealth {
	health := protocol.OrchestrationHealth{Healthy: true, InspectLimit: inspectLimit, OpenIssueLimit: openLimit}
	for _, task := range tasks {
		if task.Status == domain.StatusDone {
			continue
		}
		health.OpenIssueCount++
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
	c := protocol.OrchestrationCandidate{IssueID: task.ID.String(), Included: assessment.Eligible, Eligible: assessment.Eligible, Sufficient: assessment.Sufficient, Classification: string(assessment.Classification), Sufficiency: assessment.Sufficiency, ExclusionReasons: assessment.ExclusionReasons}
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
	if _, ok := nestedRoots[issueID]; ok {
		return fmt.Sprintf("nested-root-start-orchestrator-session: az session start %s", issueID)
	}
	if _, ok := active[issueID]; ok {
		return "session-already-running"
	}
	if reason := blocked[issueID]; reason != "" {
		return reason
	}
	return "not-runnable"
}

func (a daemonOrchestrationAuthority) claimAndSubmitStart(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string) (protocol.OrchestrationLaunch, error) {
	parsed, err := naming.ParseIssueID(issueID)
	if err != nil {
		return protocol.OrchestrationLaunch{}, err
	}
	claimBody, err := json.Marshal(taskOwnershipRequest{TaskID: issueID, OwnerID: request.ActorID, OwnerKind: "agent"})
	if err != nil {
		return protocol.OrchestrationLaunch{}, fmt.Errorf("marshal ownership claim: %w", err)
	}
	baseReq := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("orchestration-" + request.IntentKey + "-" + issueID), Kind: protocol.EnvelopeKindCommand, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID), ClientActor: request.ActorID}}
	claimReq := baseReq
	claimReq.Command, claimReq.Body = "task.ownership.claim", claimBody
	claimResp, err := a.daemon.handleTaskOwnershipClaim(ctx, claimReq)
	if err != nil {
		return protocol.OrchestrationLaunch{}, err
	}
	if !claimResp.OK {
		if claimResp.Error != nil {
			return protocol.OrchestrationLaunch{}, fmt.Errorf("claim ownership: %s", claimResp.Error.Message)
		}
		return protocol.OrchestrationLaunch{}, fmt.Errorf("claim ownership failed")
	}
	sessionID := parsed.String()
	if repoDir := strings.TrimSpace(request.RepoDir); repoDir != "" {
		sessionID = naming.CanonicalSessionIDForIssue(repoDir, parsed).String()
	}
	payload, err := json.Marshal(sessionCommandBody{ProjectID: projectID, SessionID: sessionID, BaseBranch: request.BaseBranch})
	if err != nil {
		a.releaseStartClaim(ctx, baseReq, issueID, request.ActorID)
		return protocol.OrchestrationLaunch{}, fmt.Errorf("marshal session start: %w", err)
	}
	resources := []string{"issue:" + projectID + ":" + issueID, "worktree:" + issueID, "session:" + sessionID}
	submitBody, err := json.Marshal(protocol.OperationSubmitRequestBody{ProjectID: naming.ProjectID(projectID), Kind: "session.start", IssueID: parsed, DedupeKey: "session.start:" + issueID, ResourceKeys: resources, Payload: payload})
	if err != nil {
		a.releaseStartClaim(ctx, baseReq, issueID, request.ActorID)
		return protocol.OrchestrationLaunch{}, fmt.Errorf("marshal operation submit: %w", err)
	}
	submitReq := baseReq
	submitReq.Command, submitReq.Body = protocol.CommandOperationSubmit, submitBody
	submitResp := a.daemon.operationRuntime.handleOperationSubmit(ctx, submitReq)
	if !submitResp.OK {
		a.releaseStartClaim(ctx, baseReq, issueID, request.ActorID)
		if submitResp.Error != nil {
			return protocol.OrchestrationLaunch{}, fmt.Errorf("submit session start: %s", submitResp.Error.Message)
		}
		return protocol.OrchestrationLaunch{}, fmt.Errorf("submit session start failed")
	}
	var submitted protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(submitResp.Body, &submitted); err != nil {
		return protocol.OrchestrationLaunch{}, err
	}
	return protocol.OrchestrationLaunch{IssueID: issueID, SessionID: sessionID, OperationID: submitted.Operation.OperationID.String(), OperationState: string(submitted.Operation.State)}, nil
}

func (a daemonOrchestrationAuthority) releaseStartClaim(ctx context.Context, baseReq protocol.RequestEnvelope, issueID, actorID string) {
	releaseBody, err := json.Marshal(taskOwnershipRequest{TaskID: issueID, OwnerID: actorID})
	if err != nil {
		return
	}
	releaseReq := baseReq
	releaseReq.Command, releaseReq.Body = "task.ownership.release", releaseBody
	_, _ = a.daemon.handleTaskOwnershipRelease(ctx, releaseReq)
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
