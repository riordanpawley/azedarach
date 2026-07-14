package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type acceptedReviewPin struct {
	SourceOID       string
	EvidenceSource  string
	EvidenceEventID int64
	EvidenceSeq     int64
	EvidenceDigest  string
}

func validOrchestrationIntentKind(kind protocol.OrchestrationIntentKind) bool {
	switch kind {
	case protocol.OrchestrationIntentStart, protocol.OrchestrationIntentReviewReturn, protocol.OrchestrationIntentReviewAccept:
		return true
	default:
		return false
	}
}

func firstActionableReview(queue []protocol.OrchestrationReview) string {
	for _, review := range queue {
		if review.Actionable {
			return review.IssueID
		}
	}
	return ""
}

func validateOrchestrationReviewIntent(request protocol.OrchestrationIntentRequest) error {
	switch request.Kind {
	case protocol.OrchestrationIntentStart:
		return nil
	case protocol.OrchestrationIntentReviewAccept:
		if len(request.Findings) > 0 {
			return fmt.Errorf("review-accept intent cannot include findings")
		}
		return nil
	case protocol.OrchestrationIntentReviewReturn:
		if len(request.Findings) == 0 {
			return fmt.Errorf("review-return intent requires at least one actionable finding")
		}
		if len(request.Findings) > 50 {
			return fmt.Errorf("review-return intent supports at most 50 findings")
		}
		total := 0
		for i, finding := range request.Findings {
			if strings.TrimSpace(finding.Finding) == "" {
				return fmt.Errorf("review finding %d requires finding text", i+1)
			}
			total += len([]rune(finding.Finding)) + len([]rune(finding.SuggestedFix))
		}
		if total > 3000 {
			return fmt.Errorf("review findings exceed the 3000 character delivery limit")
		}
		return nil
	default:
		return fmt.Errorf("unsupported orchestration intent %q", request.Kind)
	}
}

func (a daemonOrchestrationAuthority) reviewQueue(ctx context.Context, projectID string, request protocol.OrchestrationSnapshotRequest, tasks []domain.Task) ([]protocol.OrchestrationReview, error) {
	if !usesProjectionSource(sourceForInvariant(daemonInvariantOrchestrationReview)) {
		return nil, nil
	}
	acceptedCloseCandidates := make([]string, 0)
	for _, task := range tasks {
		if reviewOutcomeLookupCandidate(task) {
			acceptedCloseCandidates = append(acceptedCloseCandidates, task.ID.String())
		}
	}
	trustedOutcomes, err := a.latestTrustedReviewOutcomes(ctx, projectID, acceptedCloseCandidates)
	if err != nil {
		return nil, fmt.Errorf("load accepted review outcomes: %w", err)
	}
	reviewTasks := make([]domain.Task, 0)
	for _, task := range tasks {
		if !reviewOutcomeLookupCandidate(task) && task.Status != domain.StatusDone {
			reviewTasks = append(reviewTasks, task)
			continue
		}
		// A close may fail after the trusted review decision has been recorded.
		// Keep that non-terminal issue in the queue so the same intent can resume
		// after repair or a reviewer can explicitly return new findings.
		if task.Status != domain.StatusDone && trustedOutcomes[task.ID.String()] == "accepted" {
			reviewTasks = append(reviewTasks, task)
		}
	}
	sort.SliceStable(reviewTasks, func(i, j int) bool { return orchestrationTaskLess(reviewTasks[i], reviewTasks[j]) })
	if request.Limit > 0 && len(reviewTasks) > request.Limit {
		reviewTasks = reviewTasks[:request.Limit]
	}

	worktrees := map[string]git.Worktree{}
	if manager := a.daemon.worktreeManagerForProject(projectID); manager != nil {
		if listed, err := manager.List(ctx); err == nil {
			for _, worktree := range listed {
				worktrees[strings.TrimSpace(worktree.IssueID)] = worktree
			}
		}
	}
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	repoDir := strings.TrimSpace(request.RepoDir)
	if repoDir == "" {
		repoDir = strings.TrimSpace(a.daemon.cfg.RepoDir)
	}
	out := make([]protocol.OrchestrationReview, 0, len(reviewTasks))
	for _, task := range reviewTasks {
		out = append(out, a.reviewInspection(ctx, projectID, repoDir, request.ActorID, task, byID, worktrees))
	}
	return out, nil
}

func reviewOutcomeLookupCandidate(task domain.Task) bool {
	facts := task.IssueFacts()
	return task.Status != domain.StatusDone && task.Status != domain.StatusInReview && facts.ReviewState != domain.IssueReviewRequested && !facts.ReviewReadyVisible
}

func (a daemonOrchestrationAuthority) latestTrustedReviewOutcomes(ctx context.Context, projectID string, issueIDs []string) (map[string]string, error) {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	events, err := issueClient.ListLatestIssueObservationEventsByIssue(ctx, issues.LatestIssueObservationEventOptions{
		IssueIDs:       issueIDs,
		Type:           domain.IssueEventReviewCompleted,
		Source:         "daemon-orchestration",
		SourceCommands: []string{string(protocol.OrchestrationIntentReviewAccept), string(protocol.OrchestrationIntentReviewReturn)},
		CommandOutcomePairs: []issues.IssueObservationCommandOutcomePair{
			{SourceCommand: string(protocol.OrchestrationIntentReviewAccept), Outcomes: []string{string(domain.ReviewOutcomeAccepted), string(domain.ReviewOutcomeIntegrationFailed)}},
			{SourceCommand: string(protocol.OrchestrationIntentReviewReturn), Outcomes: []string{string(domain.ReviewOutcomeReturned)}},
		},
		RequiredPayloadTextKeys: []string{"actor_id"},
		CurrentReviewEpoch:      true,
	})
	if err != nil {
		return nil, err
	}
	outcomes := make(map[string]string, len(events))
	for issueID, event := range events {
		if outcome, ok := domain.TrustedReviewOutcome(event); ok {
			outcomes[issueID] = string(outcome)
		}
	}
	return outcomes, nil
}

func (a daemonOrchestrationAuthority) reviewInspection(ctx context.Context, projectID, repoDir, actorID string, task domain.Task, tasks map[string]domain.Task, worktrees map[string]git.Worktree) protocol.OrchestrationReview {
	inspection := protocol.OrchestrationReview{IssueID: task.ID.String(), Actionable: true}
	if task.ParentID != nil {
		inspection.ParentIssueID = task.ParentID.String()
	}
	if task.Ownership != nil && task.Ownership.IsActive(time.Now().UTC()) {
		inspection.ExecutionOwner = task.Ownership.OwnerID
	}
	if lease := coordinationLease(task, domain.CoordinationLeaseOrchestration); lease != nil && !lease.IsExpired(time.Now().UTC()) {
		inspection.OrchestrationOwner = lease.OwnerID
		if !strings.EqualFold(strings.TrimSpace(lease.OwnerID), strings.TrimSpace(actorID)) {
			inspection.Actionable = false
			inspection.Reasons = append(inspection.Reasons, "orchestration-owned-by-"+lease.OwnerID)
		}
	}
	if lease := coordinationLease(task, domain.CoordinationLeaseReview); lease != nil && !lease.IsExpired(time.Now().UTC()) {
		inspection.ReviewOwner = lease.OwnerID
		if !strings.EqualFold(strings.TrimSpace(lease.OwnerID), strings.TrimSpace(actorID)) {
			inspection.Actionable = false
			inspection.Reasons = append(inspection.Reasons, "review-owned-by-"+lease.OwnerID)
		}
	}
	readiness, err := a.daemon.taskIntegrationReadiness(ctx, projectID, task.ID.String(), repoDir)
	if err != nil {
		inspection.Reasons = append(inspection.Reasons, "inspect-evidence: "+err.Error())
	} else {
		inspection.Evidence = readiness.EvidencePacket
		inspection.EvidenceSource = readiness.EvidenceSource
		if inspection.EvidenceSource == "" && readiness.EvidenceEventSeq > 0 {
			inspection.EvidenceSource = "mailbox"
		}
		inspection.ContextRisk = readiness.ContextRisk
		inspection.Reasons = append(inspection.Reasons, readiness.Reasons...)
	}
	if worktree, ok := worktrees[task.ID.String()]; ok {
		inspection.WorktreePath = strings.TrimSpace(worktree.Path)
		inspection.Branch = strings.TrimSpace(worktree.Branch)
		inspection.BaseBranch = a.daemon.runtimeDiffBaseBranchForIssue(task.ID.String(), a.daemon.baseBranchForProject(projectID), tasks, worktrees)
		if a.daemon.git != nil && inspection.WorktreePath != "" {
			if stat, err := a.daemon.git.DiffStat(ctx, inspection.WorktreePath, inspection.BaseBranch); err != nil {
				inspection.Reasons = append(inspection.Reasons, "inspect-diff: "+err.Error())
			} else {
				inspection.DiffStat = strings.TrimSpace(stat)
			}
		}
	}
	if outcome, err := a.latestTrustedReviewOutcome(ctx, projectID, task.ID.String()); err != nil {
		inspection.Reasons = append(inspection.Reasons, "inspect-review-outcome: "+err.Error())
	} else if outcome == "accepted" {
		inspection.Actionable = false
		inspection.Reasons = append(inspection.Reasons, "accepted-close-pending")
	}
	inspection.Reasons = uniqueNonEmpty(inspection.Reasons)
	return inspection
}

func (a daemonOrchestrationAuthority) latestTrustedReviewOutcome(ctx context.Context, projectID, issueID string) (string, error) {
	outcomes, err := a.latestTrustedReviewOutcomes(ctx, projectID, []string{issueID})
	if err != nil {
		return "", err
	}
	return outcomes[issueID], nil
}

func coordinationLease(task domain.Task, purpose domain.CoordinationLeasePurpose) *domain.CoordinationLease {
	for i := range task.CoordinationLeases {
		if task.CoordinationLeases[i].Purpose == purpose {
			return &task.CoordinationLeases[i]
		}
	}
	return nil
}

func (a daemonOrchestrationAuthority) applyReviewIntent(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest) (protocol.OrchestrationIntentResult, error) {
	if strings.TrimSpace(request.ActorID) == "" {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("review orchestration intent requires actor_id")
	}
	if !usesProjectionSource(sourceForInvariant(daemonInvariantOrchestrationReview)) {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("unsupported project review invariant source: %s", sourceForInvariant(daemonInvariantOrchestrationReview))
	}
	a.daemon.orchestrationMu.Lock()
	defer a.daemon.orchestrationMu.Unlock()
	snapshot, err := a.Snapshot(ctx, projectID, protocol.OrchestrationSnapshotRequest{Scope: request.Scope, ActorID: request.ActorID, RepoDir: request.RepoDir})
	if err != nil {
		return protocol.OrchestrationIntentResult{}, err
	}
	queue := make(map[string]protocol.OrchestrationReview, len(snapshot.ReviewQueue))
	ordered := make([]string, 0, len(snapshot.ReviewQueue))
	for _, review := range snapshot.ReviewQueue {
		queue[review.IssueID] = review
		ordered = append(ordered, review.IssueID)
	}
	requested := stableRequestedCandidates(request.IssueIDs, ordered)
	if len(request.IssueIDs) == 0 {
		requested = ordered
	}
	result := protocol.OrchestrationIntentResult{Scope: request.Scope, Kind: request.Kind, IntentKey: request.IntentKey, Requested: requested, Skipped: map[string]string{}, Failed: map[string]string{}}
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("issue store unavailable")
	}
	for _, issueID := range requested {
		terminal, err := a.reviewIntentTerminalOutcome(ctx, projectID, issueID, request)
		if err != nil {
			result.Failed[issueID] = "inspect prior review intent: " + err.Error()
			continue
		}
		switch terminal {
		case "returned":
			if err := a.convergeReturnedReview(ctx, projectID, issueID, request.ActorID); err != nil {
				result.Failed[issueID] = "converge returned review status: " + err.Error()
				continue
			}
			result.Returned = append(result.Returned, issueID)
			continue
		case "closed":
			result.Closed = append(result.Closed, issueID)
			continue
		case "superseded":
			result.Skipped[issueID] = "review-intent-superseded"
			continue
		case "accepted_pending":
			inspection, ok := queue[issueID]
			if !ok {
				result.Skipped[issueID] = "not-review-ready"
				continue
			}
			integrateBeforeClose := inspection.Evidence != nil || strings.TrimSpace(inspection.WorktreePath) != ""
			storedPin, err := a.acceptedReviewPinForIntent(ctx, projectID, issueID, request, integrateBeforeClose)
			if err != nil {
				result.Failed[issueID] = err.Error()
				continue
			}
			currentPin, err := a.captureAcceptedReviewPin(ctx, projectID, request.RepoDir, inspection, integrateBeforeClose)
			if err != nil || currentPin != storedPin {
				failure := "reviewed source or evidence changed; fresh review required"
				if err != nil {
					failure += ": " + err.Error()
				}
				if recordErr := a.recordReviewOutcome(ctx, projectID, issueID, request, string(domain.ReviewOutcomeIntegrationFailed), failure); recordErr != nil {
					failure += "; record integration failure: " + recordErr.Error()
				}
				result.Failed[issueID] = failure
				continue
			}
			if _, err := a.releaseAndCloseAcceptedReview(ctx, projectID, request, issueID, integrateBeforeClose, storedPin.SourceOID, &result); err != nil {
				result.Failed[issueID] = err.Error()
			}
			continue
		}
		restart, found, err := a.reviewRestartSubmission(ctx, issueClient, issueID, request)
		if err != nil {
			result.Failed[issueID] = "inspect prior review restart: " + err.Error()
			continue
		}
		if found {
			if err := a.reconcileReviewRestart(ctx, projectID, issueID, request, restart, &result); err != nil {
				result.Failed[issueID] = err.Error()
			}
			continue
		}
		inspection, ok := queue[issueID]
		if !ok {
			result.Skipped[issueID] = "not-review-ready"
			continue
		}
		revokingAcceptedReview := request.Kind == protocol.OrchestrationIntentReviewReturn && slices.Contains(inspection.Reasons, "accepted-close-pending")
		if !inspection.Actionable && !revokingAcceptedReview {
			result.Skipped[issueID] = strings.Join(inspection.Reasons, "; ")
			continue
		}
		if _, err := issueClient.ClaimOwnershipWithRuntime(ctx, projectID, issueID, issues.OwnershipClaimParams{OwnerID: request.ActorID, OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview}); err != nil {
			result.Skipped[issueID] = "claim-review: " + err.Error()
			continue
		}
		var actionErr error
		reviewLeaseReleased := false
		switch request.Kind {
		case protocol.OrchestrationIntentReviewReturn:
			actionErr = a.returnReviewFindings(ctx, projectID, request, inspection, &result)
		case protocol.OrchestrationIntentReviewAccept:
			reviewLeaseReleased, actionErr = a.acceptReview(ctx, projectID, request, inspection, &result)
		}
		restartPending := false
		for _, pending := range result.Pending {
			if pending.IssueID == issueID {
				restartPending = true
				break
			}
		}
		returnedConvergenceAttempted := false
		if actionErr == nil && request.Kind == protocol.OrchestrationIntentReviewReturn && !restartPending {
			returnedConvergenceAttempted = true
			if err := a.convergeReturnedReview(ctx, projectID, issueID, request.ActorID); err != nil {
				actionErr = fmt.Errorf("converge returned review: %w", err)
			} else {
				result.Returned = append(result.Returned, issueID)
			}
		}
		if !reviewLeaseReleased && !returnedConvergenceAttempted {
			releaseErr := a.releaseMatchingReviewLease(ctx, projectID, issueID, request.ActorID)
			if releaseErr != nil {
				if actionErr == nil {
					actionErr = fmt.Errorf("release review lease: %w", releaseErr)
				} else {
					actionErr = fmt.Errorf("%v; release review lease: %w", actionErr, releaseErr)
				}
			}
		}
		if actionErr != nil {
			result.Failed[issueID] = actionErr.Error()
		}
	}
	return result, nil
}

func (a daemonOrchestrationAuthority) returnReviewFindings(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, inspection protocol.OrchestrationReview, result *protocol.OrchestrationIntentResult) error {
	body, err := json.Marshal(map[string]any{"type": "review-finding", "intent_key": request.IntentKey, "request_fingerprint": reviewRequestFingerprint(request), "findings": request.Findings})
	if err != nil {
		return err
	}
	parent := inspection.ParentIssueID
	if parent == "" {
		parent = inspection.IssueID
	}
	repoDir := strings.TrimSpace(request.RepoDir)
	if repoDir == "" {
		repoDir = strings.TrimSpace(a.daemon.cfg.RepoDir)
	}
	persisted, err := reviewFindingMailExists(repoDir, parent, inspection.IssueID, request)
	if err != nil {
		return fmt.Errorf("inspect durable review findings: %w", err)
	}
	if !persisted {
		mailBody, err := json.Marshal(protocol.MailSendCommandBody{RepoDir: repoDir, ParentIssue: parent, IssueID: naming.IssueID(inspection.IssueID), Type: "review-finding", From: "orchestrator", To: inspection.IssueID, Body: string(body)})
		if err != nil {
			return err
		}
		mailReq := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("orchestration-review-mail-" + request.IntentKey + "-" + inspection.IssueID), Kind: protocol.EnvelopeKindCommand, Command: "mail.send", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID), ClientActor: request.ActorID}, Body: mailBody}
		mailResp, err := a.daemon.handleMailSend(ctx, mailReq)
		if err != nil {
			return fmt.Errorf("persist review findings: %w", err)
		}
		if !mailResp.OK {
			return fmt.Errorf("persist review findings: %s", responseErrorMessage(mailResp))
		}
	}
	message := formatReviewFindingMessage(inspection.IssueID, parent, request.Findings)
	delivered, deliveryErr := a.deliverReviewMessage(ctx, projectID, request, inspection.IssueID, message)
	if deliveryErr != nil && request.RestartWorker {
		allowed, ownershipErr := a.reviewWorkerRestartAllowed(ctx, projectID, inspection.IssueID, request.ActorID)
		if ownershipErr != nil {
			return fmt.Errorf("review findings recorded but delivery failed: %v; inspect restart ownership: %w", deliveryErr, ownershipErr)
		}
		if !allowed {
			return fmt.Errorf("review findings recorded but delivery failed: %v; worker restart refused because the acting orchestrator does not own execution or orchestration scope", deliveryErr)
		}
		launch, restartErr := a.claimAndSubmitStartWithPrompt(ctx, projectID, request, inspection.IssueID, message)
		if restartErr != nil {
			return fmt.Errorf("review findings recorded but delivery failed: %v; restart failed: %w", deliveryErr, restartErr)
		}
		result.Launched = append(result.Launched, launch)
		switch protocol.OperationState(launch.OperationState) {
		case protocol.OperationStateQueued, protocol.OperationStateRunning:
			result.Pending = append(result.Pending, protocol.OrchestrationPending{IssueID: inspection.IssueID, OperationID: launch.OperationID, OperationState: launch.OperationState})
			if err := a.recordReviewRestartSubmitted(ctx, projectID, inspection.IssueID, request, launch); err != nil {
				return err
			}
			return nil
		case protocol.OperationStateDone:
			delivered = true
		case protocol.OperationStateFailed, protocol.OperationStateCancelled:
			failure := fmt.Sprintf("restart operation %s reached terminal %s", launch.OperationID, launch.OperationState)
			if err := a.recordReviewOutcome(ctx, projectID, inspection.IssueID, request, "delivery_failed", failure); err != nil {
				return fmt.Errorf("review findings recorded but delivery failed: %v; %s; record failure: %w", deliveryErr, failure, err)
			}
			return fmt.Errorf("review findings recorded but delivery failed: %v; %s", deliveryErr, failure)
		default:
			return fmt.Errorf("review findings recorded but delivery failed: %v; restart operation %s returned unknown state %q", deliveryErr, launch.OperationID, launch.OperationState)
		}
	}
	if !delivered {
		_ = a.recordReviewOutcome(ctx, projectID, inspection.IssueID, request, "delivery_failed", deliveryErr.Error())
		return fmt.Errorf("review findings recorded but active delivery failed: %w", deliveryErr)
	}
	if err := a.recordReviewOutcome(ctx, projectID, inspection.IssueID, request, "returned", ""); err != nil {
		return err
	}
	return nil
}

func (a daemonOrchestrationAuthority) convergeReturnedReview(ctx context.Context, projectID, issueID, actorID string) error {
	if err := a.releaseMatchingReviewLease(ctx, projectID, issueID, actorID); err != nil {
		return fmt.Errorf("release review lease: %w", err)
	}
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return err
	}
	switch task.Status {
	case domain.StatusInReview, domain.StatusOpen:
		return issueClient.Update(ctx, issueID, domain.StatusInProgress)
	default:
		return nil
	}
}

func (a daemonOrchestrationAuthority) releaseMatchingReviewLease(ctx context.Context, projectID, issueID, actorID string) error {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return err
	}
	lease := coordinationLease(task, domain.CoordinationLeaseReview)
	if lease == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(lease.OwnerID), strings.TrimSpace(actorID)) {
		return fmt.Errorf("review lease owned by %s, not %s", lease.OwnerID, actorID)
	}
	if a.releaseReviewLease != nil {
		return a.releaseReviewLease(ctx, projectID, issueID, actorID)
	}
	_, err = issueClient.ReleaseOwnershipWithRuntime(ctx, projectID, issueID, issues.OwnershipClaimParams{OwnerID: actorID, Purpose: domain.CoordinationLeaseReview})
	return err
}

func reviewFindingMailExists(repoDir, parentIssueID, issueID string, request protocol.OrchestrationIntentRequest) (bool, error) {
	events, err := readMailboxEvents(repoDir, parentIssueID)
	if err != nil {
		return false, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != "review-finding" || !naming.IssueIDsEqual(event.IssueID, issueID) {
			continue
		}
		var payload struct {
			IntentKey          string `json:"intent_key"`
			RequestFingerprint string `json:"request_fingerprint"`
		}
		if json.Unmarshal([]byte(event.Body), &payload) == nil && payload.IntentKey == request.IntentKey {
			if payload.RequestFingerprint != "" && payload.RequestFingerprint != reviewRequestFingerprint(request) {
				return false, fmt.Errorf("intent_key %s was already used with different review findings", request.IntentKey)
			}
			return true, nil
		}
	}
	return false, nil
}

func (a daemonOrchestrationAuthority) reviewRestartSubmission(ctx context.Context, issueClient *issues.Client, issueID string, request protocol.OrchestrationIntentRequest) (domain.ReviewRestartSubmission, bool, error) {
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted, domain.IssueEventIssueStatusChanged}, NewestIDFirst: true})
	if err != nil {
		return domain.ReviewRestartSubmission{}, false, err
	}
	fingerprint := reviewRequestFingerprint(request)
	for _, event := range events {
		if domain.IsReviewRequestTransition(event) {
			break
		}
		submission, trusted, err := domain.TrustedReviewRestartSubmission(event)
		if err != nil {
			return domain.ReviewRestartSubmission{}, false, err
		}
		if !trusted {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(event.Payload["intent_key"])) != strings.TrimSpace(request.IntentKey) {
			continue
		}
		if stored := strings.TrimSpace(fmt.Sprint(event.Payload["request_fingerprint"])); stored != "" && stored != fingerprint {
			return domain.ReviewRestartSubmission{}, false, fmt.Errorf("intent_key %s was already used with a different review request", request.IntentKey)
		}
		if !strings.EqualFold(strings.TrimSpace(submission.ActorID), strings.TrimSpace(request.ActorID)) {
			return domain.ReviewRestartSubmission{}, false, fmt.Errorf("restart_submitted intent actor is %s, not %s", submission.ActorID, request.ActorID)
		}
		return submission, true, nil
	}
	return domain.ReviewRestartSubmission{}, false, nil
}

func (a daemonOrchestrationAuthority) reconcileReviewRestart(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, submission domain.ReviewRestartSubmission, result *protocol.OrchestrationIntentResult) error {
	operation, err := a.lookupReviewRestartOperation(ctx, submission.OperationID.String())
	if err != nil {
		return fmt.Errorf("inspect restart operation %s: %w", submission.OperationID, err)
	}
	if operation.OperationID != submission.OperationID || operation.Kind != "session.start" || !naming.IssueIDsEqual(operation.IssueID.String(), issueID) {
		return fmt.Errorf("restart operation relation mismatch: operation=%s kind=%s issue=%s", operation.OperationID, operation.Kind, operation.IssueID)
	}
	if operation.ProjectID.String() != "" && a.daemon.canonicalProjectID(operation.ProjectID.String()) != a.daemon.canonicalProjectID(projectID) {
		return fmt.Errorf("restart operation %s belongs to project %s, not %s", operation.OperationID, operation.ProjectID, projectID)
	}
	launch := protocol.OrchestrationLaunch{IssueID: issueID, SessionID: submission.SessionID, OperationID: operation.OperationID.String(), OperationState: string(operation.State)}
	result.Launched = append(result.Launched, launch)
	switch operation.State {
	case protocol.OperationStateQueued, protocol.OperationStateRunning:
		result.Pending = append(result.Pending, protocol.OrchestrationPending{IssueID: issueID, OperationID: operation.OperationID.String(), OperationState: string(operation.State)})
		return nil
	case protocol.OperationStateDone:
		if err := a.recordReviewOutcome(ctx, projectID, issueID, request, "returned", ""); err != nil {
			return err
		}
		if err := a.convergeReturnedReview(ctx, projectID, issueID, request.ActorID); err != nil {
			return fmt.Errorf("converge returned review: %w", err)
		}
		result.Returned = append(result.Returned, issueID)
		return nil
	case protocol.OperationStateFailed, protocol.OperationStateCancelled:
		failure := fmt.Sprintf("restart operation %s reached terminal %s", operation.OperationID, operation.State)
		if operation.Error != nil && strings.TrimSpace(operation.Error.Message) != "" {
			failure += ": " + strings.TrimSpace(operation.Error.Message)
		}
		if err := a.recordReviewOutcome(ctx, projectID, issueID, request, "delivery_failed", failure); err != nil {
			return fmt.Errorf("%s; record failure: %w", failure, err)
		}
		return fmt.Errorf("review findings recorded but %s", failure)
	default:
		return fmt.Errorf("restart operation %s returned invalid state %q", operation.OperationID, operation.State)
	}
}

func (a daemonOrchestrationAuthority) lookupReviewRestartOperation(ctx context.Context, operationID string) (protocol.OperationRecord, error) {
	if a.lookupOperation != nil {
		return a.lookupOperation(ctx, operationID)
	}
	if a.daemon == nil || a.daemon.operationRuntime == nil || a.daemon.operationRuntime.manager == nil {
		return protocol.OperationRecord{}, fmt.Errorf("operation runtime unavailable")
	}
	record, err := a.daemon.operationRuntime.manager.Get(ctx, operationID)
	if err != nil {
		return protocol.OperationRecord{}, err
	}
	return a.daemon.operationRuntime.toProtocolRecord(record), nil
}

func (a daemonOrchestrationAuthority) reviewIntentTerminalOutcome(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest) (string, error) {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return "", fmt.Errorf("issue store unavailable")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return "", err
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted, domain.IssueEventIssueStatusChanged}, NewestIDFirst: true})
	if err != nil {
		return "", err
	}
	latestSemanticDecisionSeen := false
	for _, event := range events {
		if domain.IsReviewRequestTransition(event) {
			break
		}
		outcome, trusted := domain.TrustedReviewOutcome(event)
		if !trusted {
			continue
		}
		isLatestSemanticDecision := false
		switch outcome {
		case domain.ReviewOutcomeAccepted, domain.ReviewOutcomeReturned, domain.ReviewOutcomeIntegrationFailed:
			isLatestSemanticDecision = !latestSemanticDecisionSeen
			latestSemanticDecisionSeen = true
		}
		if strings.TrimSpace(fmt.Sprint(event.Payload["intent_key"])) != strings.TrimSpace(request.IntentKey) {
			continue
		}
		if stored := strings.TrimSpace(fmt.Sprint(event.Payload["request_fingerprint"])); stored != "" && stored != reviewRequestFingerprint(request) {
			return "", fmt.Errorf("intent_key %s was already used with a different review request", request.IntentKey)
		}
		if outcome == domain.ReviewOutcomeReturned {
			return "returned", nil
		}
		if outcome == domain.ReviewOutcomeIntegrationFailed {
			return "superseded", nil
		}
		if outcome == domain.ReviewOutcomeAccepted {
			if task.Status == domain.StatusDone {
				return "closed", nil
			}
			if !isLatestSemanticDecision {
				return "superseded", nil
			}
			return "accepted_pending", nil
		}
	}
	return "", nil
}

func (a daemonOrchestrationAuthority) reviewWorkerRestartAllowed(ctx context.Context, projectID, issueID, actorID string) (bool, error) {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return false, fmt.Errorf("issue store unavailable")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if task.Ownership != nil && task.Ownership.OwnedBy(actorID, now) {
		return true, nil
	}
	lease := coordinationLease(task, domain.CoordinationLeaseOrchestration)
	return lease != nil && !lease.IsExpired(now) && strings.EqualFold(strings.TrimSpace(lease.OwnerID), strings.TrimSpace(actorID)), nil
}

func (a daemonOrchestrationAuthority) acceptReview(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, inspection protocol.OrchestrationReview, result *protocol.OrchestrationIntentResult) (bool, error) {
	if inspection.Evidence == nil {
		issueClient := a.daemon.issueClientForProject(projectID)
		if issueClient == nil {
			return false, fmt.Errorf("issue store unavailable")
		}
		task, err := issueClient.GetWithRuntime(ctx, projectID, inspection.IssueID)
		if err != nil {
			return false, fmt.Errorf("inspect review issue: %w", err)
		}
		events, err := issueClient.ListIssueObservationEvents(ctx, inspection.IssueID, issues.IssueObservationEventListOptions{})
		if err != nil {
			return false, fmt.Errorf("inspect review evidence: %w", err)
		}
		if !domain.HasInternalReviewArtifact(task, events) {
			return false, fmt.Errorf("accepted review requires complete worker_evidence.v1 or a declared internal_review investigation with a durable accepted/ratified review artifact")
		}
	}
	if inspection.ContextRisk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*inspection.ContextRisk) {
		return false, fmt.Errorf("accepted review requires structured high-context-risk closeout evidence")
	}
	integrateBeforeClose := inspection.Evidence != nil || strings.TrimSpace(inspection.WorktreePath) != ""
	pin, err := a.captureAcceptedReviewPin(ctx, projectID, request.RepoDir, inspection, integrateBeforeClose)
	if err != nil {
		return false, err
	}
	if err := a.recordAcceptedReviewOutcome(ctx, projectID, inspection.IssueID, request, pin); err != nil {
		return false, err
	}
	return a.releaseAndCloseAcceptedReview(ctx, projectID, request, inspection.IssueID, integrateBeforeClose, pin.SourceOID, result)
}

func (a daemonOrchestrationAuthority) captureAcceptedReviewPin(ctx context.Context, projectID, repoDir string, inspection protocol.OrchestrationReview, integrateBeforeClose bool) (acceptedReviewPin, error) {
	pin := acceptedReviewPin{}
	if inspection.Evidence != nil {
		repoDir = strings.TrimSpace(repoDir)
		if repoDir == "" {
			repoDir = strings.TrimSpace(a.daemon.cfg.RepoDir)
		}
		readiness, err := a.daemon.taskIntegrationReadiness(ctx, projectID, inspection.IssueID, repoDir)
		if err != nil {
			return pin, fmt.Errorf("capture reviewed evidence identity: %w", err)
		}
		if readiness.EvidencePacket == nil {
			return pin, fmt.Errorf("capture reviewed evidence identity: current evidence is unavailable")
		}
		body, err := json.Marshal(readiness.EvidencePacket)
		if err != nil {
			return pin, fmt.Errorf("encode reviewed evidence identity: %w", err)
		}
		pin.EvidenceSource = strings.TrimSpace(readiness.EvidenceSource)
		if pin.EvidenceSource == "" && readiness.EvidenceEventSeq > 0 {
			pin.EvidenceSource = "mailbox"
		}
		pin.EvidenceEventID = readiness.EvidenceEventID
		pin.EvidenceSeq = readiness.EvidenceEventSeq
		pin.EvidenceDigest = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	if integrateBeforeClose {
		oid, err := a.resolveAcceptedReviewSourceOID(ctx, projectID, inspection.IssueID)
		if err != nil {
			return pin, fmt.Errorf("capture reviewed source commit: %w", err)
		}
		pin.SourceOID = strings.TrimSpace(oid)
		if pin.SourceOID == "" {
			return pin, fmt.Errorf("capture reviewed source commit: empty object ID")
		}
	}
	return pin, nil
}

func (a daemonOrchestrationAuthority) resolveAcceptedReviewSourceOID(ctx context.Context, projectID, issueID string) (string, error) {
	if hook := a.daemon.reviewAcceptedSourceOID; hook != nil {
		return hook(ctx, projectID, issueID)
	}
	if a.daemon.worktreeAdapter == nil || a.daemon.git == nil {
		return "", fmt.Errorf("worktree or git adapter unavailable")
	}
	worktrees, err := a.daemon.worktreeAdapter.List(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("list worktrees: %w", err)
	}
	source, ok := daemonWorktreeForIssue(worktrees, issueID)
	if !ok || strings.TrimSpace(source.Branch) == "" {
		a.daemon.worktreeAdapter.pollAndPersistWorktrees(ctx, projectID)
		worktrees, err = a.daemon.worktreeAdapter.List(ctx, projectID)
		if err != nil {
			return "", fmt.Errorf("refresh worktrees: %w", err)
		}
		source, ok = daemonWorktreeForIssue(worktrees, issueID)
	}
	if !ok || strings.TrimSpace(source.Branch) == "" {
		return "", fmt.Errorf("source branch unavailable for %s", issueID)
	}
	worktree := strings.TrimSpace(source.Path)
	ref := "HEAD"
	if missing, statErr := taskCloseWorktreePathMissing(worktree); statErr != nil {
		return "", statErr
	} else if missing {
		worktree = strings.TrimSpace(a.daemon.resolveRepoDirForProjectExact(projectID))
		if worktree == "" {
			worktree = strings.TrimSpace(a.daemon.resolveRepoDirForProject(projectID))
		}
		ref = source.Branch
	}
	return a.daemon.git.ResolveCommit(ctx, worktree, ref)
}

func (a daemonOrchestrationAuthority) acceptedReviewPinForIntent(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, requireSourceOID bool) (acceptedReviewPin, error) {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return acceptedReviewPin{}, fmt.Errorf("issue store unavailable")
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}, NewestFirst: true})
	if err != nil {
		return acceptedReviewPin{}, err
	}
	for _, event := range events {
		outcome, trusted := domain.TrustedReviewOutcome(event)
		if !trusted || outcome != domain.ReviewOutcomeAccepted || strings.TrimSpace(fmt.Sprint(event.Payload["intent_key"])) != strings.TrimSpace(request.IntentKey) {
			continue
		}
		pin := acceptedReviewPin{
			SourceOID:       observationPayloadString(event.Payload, "reviewed_source_oid"),
			EvidenceSource:  observationPayloadString(event.Payload, "reviewed_evidence_source"),
			EvidenceEventID: reviewPayloadInt64(event.Payload["reviewed_evidence_event_id"]),
			EvidenceSeq:     reviewPayloadInt64(event.Payload["reviewed_evidence_seq"]),
			EvidenceDigest:  observationPayloadString(event.Payload, "reviewed_evidence_digest"),
		}
		if requireSourceOID && pin.SourceOID == "" {
			return acceptedReviewPin{}, fmt.Errorf("accepted review is missing an exact reviewed source commit; fresh review required")
		}
		return pin, nil
	}
	return acceptedReviewPin{}, fmt.Errorf("accepted review pin not found; fresh review required")
}

func reviewPayloadInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		var parsed int64
		_, _ = fmt.Sscan(strings.TrimSpace(typed), &parsed)
		return parsed
	default:
		return 0
	}
}

func (a daemonOrchestrationAuthority) releaseAndCloseAcceptedReview(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string, integrateBeforeClose bool, expectedSourceOID string, result *protocol.OrchestrationIntentResult) (bool, error) {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return false, fmt.Errorf("issue store unavailable")
	}
	if _, err := issueClient.ReleaseOwnershipWithRuntime(ctx, projectID, issueID, issues.OwnershipClaimParams{OwnerID: request.ActorID, Purpose: domain.CoordinationLeaseReview}); err != nil {
		return false, fmt.Errorf("release review lease before authoritative close: %w", err)
	}
	if hook := a.daemon.reviewLeaseReleasedBeforeClose; hook != nil {
		if err := hook(ctx, projectID, issueID); err != nil {
			return true, fmt.Errorf("after review lease release: %w", err)
		}
	}
	return true, a.closeAcceptedReview(ctx, projectID, request, issueID, integrateBeforeClose, expectedSourceOID, result)
}

func (a daemonOrchestrationAuthority) closeAcceptedReview(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string, integrateBeforeClose bool, expectedSourceOID string, result *protocol.OrchestrationIntentResult) error {
	body, err := json.Marshal(taskCloseRequest{TaskID: issueID, IntegrateBeforeClose: integrateBeforeClose, ExpectedSourceOID: strings.TrimSpace(expectedSourceOID)})
	if err != nil {
		return err
	}
	closeReq := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("orchestration-review-close-" + request.IntentKey + "-" + issueID), Kind: protocol.EnvelopeKindCommand, Command: "task.close", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID), ClientActor: request.ActorID}, Body: body}
	closeResp, err := a.daemon.handleTaskClose(ctx, closeReq)
	if err != nil {
		_ = a.recordReviewCloseFailure(ctx, projectID, issueID, request, err.Error())
		return fmt.Errorf("authoritative close: %w", err)
	}
	if !closeResp.OK {
		failure := responseErrorMessage(closeResp)
		_ = a.recordReviewCloseFailure(ctx, projectID, issueID, request, failure)
		return fmt.Errorf("authoritative close: %s", failure)
	}
	result.Closed = append(result.Closed, issueID)
	return nil
}

func (a daemonOrchestrationAuthority) recordReviewCloseFailure(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, failure string) error {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	fingerprint := reviewRequestFingerprint(request)
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCloseFailed}, NewestFirst: true})
	if err != nil {
		return fmt.Errorf("inspect prior review close failure: %w", err)
	}
	for _, event := range events {
		if strings.TrimSpace(fmt.Sprint(event.Payload["intent_key"])) == strings.TrimSpace(request.IntentKey) &&
			strings.TrimSpace(fmt.Sprint(event.Payload["request_fingerprint"])) == fingerprint &&
			strings.TrimSpace(fmt.Sprint(event.Payload["failure"])) == strings.TrimSpace(failure) {
			return nil
		}
	}
	parsed, err := naming.ParseIssueID(issueID)
	if err != nil {
		return err
	}
	payload := map[string]any{"actor_id": request.ActorID, "intent_key": request.IntentKey, "request_fingerprint": fingerprint, "failure": strings.TrimSpace(failure)}
	if _, err := issueClient.AppendIssueObservationEvent(ctx, parsed.String(), issues.IssueObservationEventParams{Type: domain.IssueEventReviewCloseFailed, Source: "daemon-orchestration", SourceCommand: string(request.Kind), Payload: payload}); err != nil {
		return fmt.Errorf("record review close failure: %w", err)
	}
	return nil
}

func (a daemonOrchestrationAuthority) recordReviewOutcome(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, outcome, failure string) error {
	return a.recordReviewOutcomeWithRestart(ctx, projectID, issueID, request, outcome, failure, nil, nil)
}

func (a daemonOrchestrationAuthority) recordAcceptedReviewOutcome(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, pin acceptedReviewPin) error {
	metadata := map[string]any{
		"reviewed_source_oid":        pin.SourceOID,
		"reviewed_evidence_source":   pin.EvidenceSource,
		"reviewed_evidence_event_id": pin.EvidenceEventID,
		"reviewed_evidence_seq":      pin.EvidenceSeq,
		"reviewed_evidence_digest":   pin.EvidenceDigest,
	}
	return a.recordReviewOutcomeWithRestart(ctx, projectID, issueID, request, string(domain.ReviewOutcomeAccepted), "", nil, metadata)
}

func (a daemonOrchestrationAuthority) recordReviewRestartSubmitted(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, launch protocol.OrchestrationLaunch) error {
	operationID, err := naming.ParseOperationID(launch.OperationID)
	if err != nil {
		return fmt.Errorf("record restart submission operation: %w", err)
	}
	state := protocol.OperationState(launch.OperationState)
	if state != protocol.OperationStateQueued && state != protocol.OperationStateRunning {
		return fmt.Errorf("record restart submission operation %s with non-pending state %q", operationID, state)
	}
	restart := &domain.ReviewRestartSubmission{OperationID: operationID, State: domain.ReviewRestartOperationState(state), SessionID: strings.TrimSpace(launch.SessionID), ActorID: strings.TrimSpace(request.ActorID)}
	return a.recordReviewOutcomeWithRestart(ctx, projectID, issueID, request, "restart_submitted", "", restart, nil)
}

func (a daemonOrchestrationAuthority) recordReviewOutcomeWithRestart(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, outcome, failure string, restart *domain.ReviewRestartSubmission, metadata map[string]any) error {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	fingerprint := reviewRequestFingerprint(request)
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}, NewestFirst: true})
	if err != nil {
		return fmt.Errorf("inspect prior review outcome: %w", err)
	}
	for _, event := range events {
		eventFailure := ""
		if value, ok := event.Payload["failure"]; ok {
			eventFailure = strings.TrimSpace(fmt.Sprint(value))
		}
		matches := strings.TrimSpace(fmt.Sprint(event.Payload["intent_key"])) == strings.TrimSpace(request.IntentKey) &&
			strings.TrimSpace(fmt.Sprint(event.Payload["request_fingerprint"])) == fingerprint &&
			strings.TrimSpace(fmt.Sprint(event.Payload["outcome"])) == strings.TrimSpace(outcome) &&
			eventFailure == strings.TrimSpace(failure)
		if restart != nil {
			matches = matches && event.OperationID == restart.OperationID.String() && strings.TrimSpace(fmt.Sprint(event.Payload["operation_state"])) == string(restart.State)
		}
		for key, value := range metadata {
			matches = matches && strings.TrimSpace(fmt.Sprint(event.Payload[key])) == strings.TrimSpace(fmt.Sprint(value))
		}
		if matches {
			return nil
		}
	}
	parsed, err := naming.ParseIssueID(issueID)
	if err != nil {
		return err
	}
	payload := map[string]any{"outcome": outcome, "actor_id": request.ActorID, "intent_key": request.IntentKey, "request_fingerprint": fingerprint, "findings": request.Findings}
	for key, value := range metadata {
		payload[key] = value
	}
	if strings.TrimSpace(failure) != "" {
		payload["failure"] = failure
	}
	params := issues.IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: string(request.Kind), Payload: payload}
	if restart != nil {
		params.OperationID = restart.OperationID.String()
		params.SessionID = restart.SessionID
		payload["operation_id"] = restart.OperationID.String()
		payload["operation_state"] = string(restart.State)
	}
	_, err = issueClient.AppendIssueObservationEvent(ctx, parsed.String(), params)
	if err != nil {
		return fmt.Errorf("record review outcome: %w", err)
	}
	return nil
}

func reviewRequestFingerprint(request protocol.OrchestrationIntentRequest) string {
	body, _ := json.Marshal(struct {
		Scope         domain.OrchestrationScope             `json:"scope"`
		Kind          protocol.OrchestrationIntentKind      `json:"kind"`
		ActorID       string                                `json:"actor_id"`
		RepoDir       string                                `json:"repo_dir,omitempty"`
		Findings      []protocol.OrchestrationReviewFinding `json:"findings,omitempty"`
		RestartWorker bool                                  `json:"restart_worker,omitempty"`
	}{Scope: request.Scope, Kind: request.Kind, ActorID: strings.TrimSpace(request.ActorID), RepoDir: strings.TrimSpace(request.RepoDir), Findings: request.Findings, RestartWorker: request.RestartWorker})
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func (a daemonOrchestrationAuthority) deliverReviewMessage(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID, message string) (bool, error) {
	// Leave IssueID empty so decodeSessionRequest resolves the bare issue-shaped
	// session input through the project naming scope. Supplying both fields makes
	// the decoder treat SessionID as an explicit tmux name, which misses the
	// canonical project-scoped session used by session.start.
	body, err := json.Marshal(sessionCommandBody{ProjectID: projectID, SessionID: issueID, Message: message})
	if err != nil {
		return false, err
	}
	req := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("orchestration-review-message-" + request.IntentKey + "-" + issueID), Kind: protocol.EnvelopeKindCommand, Command: "session.message", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID), ClientActor: request.ActorID}, Body: body}
	resp, err := a.daemon.handleSessionMessage(ctx, req)
	if err != nil {
		return false, err
	}
	if !resp.OK {
		return false, fmt.Errorf("%s", responseErrorMessage(resp))
	}
	return true, nil
}

func formatReviewFindingMessage(issueID, parent string, findings []protocol.OrchestrationReviewFinding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Orchestrator review findings for issue %s under root %s:\n", issueID, parent)
	for i, finding := range findings {
		fmt.Fprintf(&b, "\n%d. [%s]", i+1, strings.TrimSpace(finding.Severity))
		if finding.File != "" {
			fmt.Fprintf(&b, " %s", finding.File)
			if finding.Line > 0 {
				fmt.Fprintf(&b, ":%d", finding.Line)
			}
		}
		fmt.Fprintf(&b, " — %s", strings.TrimSpace(finding.Finding))
		if finding.SuggestedFix != "" {
			fmt.Fprintf(&b, "\n   Fix: %s", strings.TrimSpace(finding.SuggestedFix))
		}
	}
	b.WriteString("\n\nAddress the findings, rerun validation and the required review loop, then report updated worker-integration-ready evidence without stopping this session.")
	return b.String()
}

func responseErrorMessage(resp protocol.ResponseEnvelope) string {
	if resp.Error != nil && strings.TrimSpace(resp.Error.Message) != "" {
		return strings.TrimSpace(resp.Error.Message)
	}
	return "daemon command failed"
}
