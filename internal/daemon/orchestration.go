package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const defaultOrchestrationInspectLimit = 50

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
	if _, err := domain.NewOrchestratorIdentity(d.projectID(req.Meta), body.Scope); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if strings.TrimSpace(body.ActorID) == "" {
		body.ActorID = strings.TrimSpace(req.Meta.ClientActor)
	}
	snapshot, err := d.orchestrationAuthority().Snapshot(ctx, d.projectID(req.Meta), body)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	snapshot.Revision = d.currentRevision(d.projectID(req.Meta))
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body, resp.Revision = encoded, snapshot.Revision
	return resp, nil
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
		limit = defaultOrchestrationInspectLimit
	}
	snapshot := protocol.OrchestrationSnapshot{Scope: identity.Scope, GeneratedAt: time.Now().UTC(), Blocked: map[string]string{}}
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
		return snapshot, nil
	}

	tasks, err := a.daemon.loadTaskGraphDomainTasks(ctx, projectID)
	if err != nil {
		return protocol.OrchestrationSnapshot{}, fmt.Errorf("load project orchestration projection: %w", err)
	}
	roots := make([]domain.Task, 0)
	tasksByID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		tasksByID[task.ID.String()] = task
		if (task.ParentID == nil || task.ParentID.IsZero()) && task.Status != domain.StatusDone {
			roots = append(roots, task)
		}
	}
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
	sortOrchestrationSnapshot(&snapshot, tasksByID)
	return snapshot, nil
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
	runnable := make(map[string]struct{}, len(snapshot.Runnable))
	active := make(map[string]struct{}, len(snapshot.Active))
	for _, id := range snapshot.Runnable {
		runnable[id] = struct{}{}
	}
	for _, id := range snapshot.Active {
		active[id] = struct{}{}
	}
	requested := append([]string(nil), request.IssueIDs...)
	if len(requested) == 0 {
		requested = append(requested, snapshot.Runnable...)
	}
	result := protocol.OrchestrationIntentResult{Scope: request.Scope, Kind: request.Kind, IntentKey: request.IntentKey, Requested: requested, Skipped: map[string]string{}, Failed: map[string]string{}}
	limit := request.Limit
	if limit <= 0 {
		limit = 3
	}
	started := 0
	for _, issueID := range requested {
		if _, ok := runnable[issueID]; !ok {
			if _, ok := active[issueID]; ok {
				result.Skipped[issueID] = "session-already-running"
			} else if reason := snapshot.Blocked[issueID]; reason != "" {
				result.Skipped[issueID] = reason
			} else {
				result.Skipped[issueID] = "not-runnable"
			}
			continue
		}
		if started >= limit {
			result.Skipped[issueID] = "limit-reached"
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
