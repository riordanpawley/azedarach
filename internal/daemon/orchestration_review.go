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
	actionableTasks := orchestrationReviewScopeTasks(tasks, request.Scope, request.ReviewIssueIDs)
	acceptedCloseCandidates := make([]string, 0)
	for _, task := range actionableTasks {
		if reviewOutcomeLookupCandidate(task) {
			acceptedCloseCandidates = append(acceptedCloseCandidates, task.ID.String())
		}
	}
	trustedOutcomes, err := a.latestTrustedReviewOutcomes(ctx, projectID, acceptedCloseCandidates)
	if err != nil {
		return nil, fmt.Errorf("load accepted review outcomes: %w", err)
	}
	reviewTasks := make([]domain.Task, 0)
	for _, task := range actionableTasks {
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
	if a.daemon.materializedReadsEnabled() {
		worktrees = a.daemon.projectReadWorktrees(projectID)
	} else if manager := a.daemon.worktreeManagerForProject(projectID); manager != nil {
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

func reviewWorktreeRefreshIssueIDs(reviewTasks, allTasks []domain.Task) []string {
	byID := make(map[string]domain.Task, len(allTasks))
	for _, task := range allTasks {
		byID[task.ID.String()] = task
	}
	ids := taskIDsFromTasks(reviewTasks)
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	for _, task := range reviewTasks {
		for parentID := domain.TaskParentIssueID(task); parentID != ""; {
			if _, ok := seen[parentID]; ok {
				break
			}
			seen[parentID] = struct{}{}
			ids = append(ids, parentID)
			parent, ok := byID[parentID]
			if !ok {
				break
			}
			parentID = domain.TaskParentIssueID(parent)
		}
	}
	return ids
}

func orchestrationReviewScopeTasks(tasks []domain.Task, scope domain.OrchestrationScope, requestedIssueIDs []string) []domain.Task {
	requested := make(map[string]struct{}, len(requestedIssueIDs))
	for _, issueID := range requestedIssueIDs {
		requested[naming.CanonicalIssueIDKey(issueID)] = struct{}{}
	}
	out := make([]domain.Task, 0)
	for _, task := range tasks {
		if len(requested) > 0 {
			if _, ok := requested[naming.CanonicalIssueIDKey(task.ID.String())]; !ok {
				continue
			}
		}
		if scope.Kind == domain.OrchestrationScopeRooted && (task.ParentID == nil || task.ParentID.IsZero() || !naming.IssueIDsEqual(task.ParentID.String(), scope.RootIssueID.String())) {
			continue
		}
		out = append(out, task)
	}
	return out
}

func stableRequestedReviewCandidates(requested, ordered []string) []string {
	wanted := make(map[string]struct{}, len(requested))
	for _, issueID := range requested {
		if key := naming.CanonicalIssueIDKey(issueID); key != "" {
			wanted[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(wanted))
	seen := make(map[string]struct{}, len(wanted))
	for _, issueID := range ordered {
		key := naming.CanonicalIssueIDKey(issueID)
		if _, requested := wanted[key]; !requested {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, issueID)
	}
	remainder := make([]string, 0, len(wanted)-len(seen))
	for _, issueID := range requested {
		key := naming.CanonicalIssueIDKey(issueID)
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		remainder = append(remainder, strings.TrimSpace(issueID))
	}
	sort.SliceStable(remainder, func(i, j int) bool {
		return naming.CanonicalIssueIDKey(remainder[i]) < naming.CanonicalIssueIDKey(remainder[j])
	})
	return append(out, remainder...)
}

func reviewOutcomeLookupCandidate(task domain.Task) bool {
	facts := task.IssueFacts()
	return task.Status != domain.StatusDone && task.Status != domain.StatusInReview && facts.ReviewState != domain.IssueReviewRequested && !facts.ReviewReadyVisible
}

func (a daemonOrchestrationAuthority) latestTrustedReviewOutcomes(ctx context.Context, projectID string, issueIDs []string) (map[string]string, error) {
	if projection, prepared := orchestrationSnapshotProjection(ctx); prepared {
		outcomes := make(map[string]string, len(issueIDs))
		for _, issueID := range issueIDs {
			if outcome := projection.TrustedReviewOutcomes[issueID]; outcome != "" {
				outcomes[issueID] = string(outcome)
			}
		}
		return outcomes, nil
	}
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
	inspection := reviewCoordinationInspection(task, actorID)
	candidatePath := ""
	if candidate, ok := worktrees[task.ID.String()]; ok {
		candidatePath = strings.TrimSpace(candidate.Path)
	} else {
		inspection.Reasons = append(inspection.Reasons, "inspect-candidate: candidate_projection_missing: issue has no durable worktree projection")
	}
	readiness, err := a.daemon.taskReviewAcceptanceReadiness(ctx, projectID, task.ID.String(), candidatePath)
	if err != nil {
		inspection.Reasons = append(inspection.Reasons, "inspect-evidence: "+err.Error())
	} else {
		inspection.Evidence = readiness.EvidencePacket
		inspection.EvidenceSource = readiness.EvidenceSource
		if inspection.EvidenceSource == "" && readiness.EvidenceEventSeq > 0 {
			inspection.EvidenceSource = "mailbox"
		}
		inspection.ContextRisk = readiness.ContextRisk
		inspection.PendingDecisions = readiness.PendingDecisions
		inspection.Reasons = append(inspection.Reasons, readiness.Reasons...)
	}
	if worktree, ok := worktrees[task.ID.String()]; ok {
		inspection.WorktreePath = strings.TrimSpace(worktree.Path)
		inspection.Branch = strings.TrimSpace(worktree.Branch)
		inspection.BaseBranch = a.daemon.runtimeDiffBaseBranchForIssue(task.ID.String(), a.daemon.baseBranchForProject(projectID), tasks, worktrees)
		if a.daemon.git != nil && inspection.WorktreePath != "" && inspection.BaseBranch != "" {
			headRevision, headErr := a.daemon.git.HeadRevision(ctx, inspection.WorktreePath)
			if headErr != nil {
				inspection.Reasons = append(inspection.Reasons, "inspect-head-revision: "+headErr.Error())
			} else {
				baseRevision, baseErr := a.daemon.git.MergeBaseForRevision(ctx, inspection.WorktreePath, inspection.BaseBranch, headRevision)
				if baseErr != nil {
					inspection.Reasons = append(inspection.Reasons, "inspect-diff-base-revision: "+baseErr.Error())
				} else {
					inspection.DiffBaseRevision = strings.TrimSpace(baseRevision)
					inspection.HeadRevision = strings.TrimSpace(headRevision)
					inspection.DiffScope = fmt.Sprintf("issue:%s:base:%s@%s", task.ID.String(), inspection.BaseBranch, inspection.DiffBaseRevision)
					inspection.DiffRange = inspection.DiffBaseRevision + ".." + inspection.HeadRevision
				}
			}
		}
		if a.daemon.materializedReadsEnabled() {
			if task.GitAdditions != 0 || task.GitDeletions != 0 {
				inspection.DiffStat = fmt.Sprintf("%d additions, %d deletions", task.GitAdditions, task.GitDeletions)
			}
		} else if a.daemon.git != nil && inspection.WorktreePath != "" {
			if stat, err := a.daemon.git.DiffStat(ctx, inspection.WorktreePath, inspection.BaseBranch); err != nil {
				inspection.Reasons = append(inspection.Reasons, "inspect-diff: "+err.Error())
			} else {
				inspection.DiffStat = strings.TrimSpace(stat)
			}
		}
	}
	if task.Status == domain.StatusInReview {
		issueClient := a.daemon.issueClientForProject(projectID)
		if issueClient == nil {
			inspection.Actionable = false
			inspection.Reasons = append(inspection.Reasons, "inspect-review-admission: issue store unavailable")
		} else if admission, err := issueClient.CaptureReviewAdmissionPin(ctx, task.ID.String()); err != nil {
			inspection.Actionable = false
			inspection.Reasons = append(inspection.Reasons, "inspect-review-admission: "+err.Error())
		} else {
			inspection.ReviewEpochEventID = admission.ReviewEpochEventID
			if admission.Evidence != nil {
				inspection.EvidenceSource = admission.Evidence.Source
				inspection.EvidenceEventID = admission.Evidence.EventID
				inspection.EvidenceSeq = admission.Evidence.Seq
				inspection.EvidenceDigest = admission.Evidence.Digest
			}
			if readiness.EvidencePacket != nil {
				readinessPin, pinErr := reviewEvidencePinFromReadiness(readiness)
				if pinErr != nil || admission.Evidence == nil || readinessPin != *admission.Evidence {
					inspection.Actionable = false
					inspection.Reasons = append(inspection.Reasons, "inspect-review-admission: exported evidence identity changed")
				}
			} else if admission.Evidence != nil {
				inspection.Actionable = false
				inspection.Reasons = append(inspection.Reasons, "inspect-review-admission: exported evidence is unavailable")
			}
			if inspection.WorktreePath != "" || inspection.Evidence != nil {
				oid, oidErr := a.resolveAcceptedReviewSourceOID(ctx, projectID, task.ID.String())
				if oidErr != nil || strings.TrimSpace(oid) == "" {
					inspection.Actionable = false
					reason := "empty object ID"
					if oidErr != nil {
						reason = oidErr.Error()
					}
					inspection.Reasons = append(inspection.Reasons, "inspect-review-admission: exact candidate unavailable: "+reason)
				} else {
					inspection.SourceOID = strings.TrimSpace(oid)
				}
			}
		}
	}
	if outcome, err := a.latestTrustedReviewOutcome(ctx, projectID, task.ID.String()); err != nil {
		inspection.Reasons = append(inspection.Reasons, "inspect-review-outcome: "+err.Error())
	} else if outcome == "accepted" {
		inspection.Actionable = false
		inspection.Reasons = append(inspection.Reasons, "accepted-close-pending")
	}
	contextPacket, contextErr := buildReviewWorkflowContext(task, inspection, domain.WorkflowRoleReviewer)
	if contextErr != nil {
		inspection.Reasons = append(inspection.Reasons, "inspect-bounded-context: "+contextErr.Error())
	} else {
		inspection.ReviewContext = &contextPacket
		if integrationPacket, integrationErr := buildReviewWorkflowContext(task, inspection, domain.WorkflowRoleIntegrator); integrationErr == nil {
			inspection.IntegrationContext = &integrationPacket
		} else {
			inspection.Reasons = append(inspection.Reasons, "inspect-bounded-integration-context: "+integrationErr.Error())
		}
	}
	inspection.Reasons = uniqueNonEmpty(inspection.Reasons)
	return inspection
}

func buildReviewWorkflowContext(task domain.Task, inspection protocol.OrchestrationReview, role domain.WorkflowRole) (domain.WorkflowContextPacket, error) {
	revision := strings.TrimSpace(inspection.HeadRevision)
	if revision == "" {
		revision = strings.TrimSpace(inspection.SourceOID)
	}
	if revision == "" && inspection.ReviewEpochEventID > 0 {
		revision = fmt.Sprintf("issue-event:%d", inspection.ReviewEpochEventID)
	}
	if revision == "" {
		revision = domain.WorkflowIssueContextRevision(task)
	}
	findings := []string(nil)
	invariants := []string(nil)
	artifacts := []domain.WorkflowArtifactReference(nil)
	if inspection.Evidence != nil {
		findings = append(findings, inspection.Evidence.Review.Findings...)
		invariants = append(invariants, inspection.Evidence.Review.ReusedLayers...)
		if inspection.Evidence.Review.Matrix != nil {
			invariants = append(invariants, inspection.Evidence.Review.Matrix.CoveredCells...)
		}
		for _, link := range inspection.Evidence.ArtifactLinks {
			artifacts = append(artifacts, domain.WorkflowArtifactReference{Label: link.Label, Reference: link.URL})
		}
	}
	return domain.BuildWorkflowContextPacket(domain.WorkflowContextInput{
		Role: role, IssueID: task.ID.String(), SourceRevision: revision, Summary: task.Title,
		Requirements: []string{task.Description, task.Design, task.Acceptance}, UnresolvedFindings: findings,
		AffectedInvariants: invariants, ArtifactLinks: artifacts,
	})
}

func reviewCoordinationInspection(task domain.Task, actorID string) protocol.OrchestrationReview {
	inspection := protocol.OrchestrationReview{IssueID: task.ID.String(), Actionable: true}
	if task.ParentID != nil {
		inspection.ParentIssueID = task.ParentID.String()
	}
	now := time.Now().UTC()
	if task.Ownership != nil && task.Ownership.IsActive(now) {
		inspection.ExecutionOwner = task.Ownership.OwnerID
	}
	if lease := coordinationLease(task, domain.CoordinationLeaseOrchestration); lease != nil && !lease.IsExpired(now) {
		inspection.OrchestrationOwner = lease.OwnerID
		if !strings.EqualFold(strings.TrimSpace(lease.OwnerID), strings.TrimSpace(actorID)) {
			inspection.Actionable = false
			inspection.Reasons = append(inspection.Reasons, "orchestration-owned-by-"+lease.OwnerID)
		}
	}
	if lease := coordinationLease(task, domain.CoordinationLeaseReview); lease != nil && !lease.IsExpired(now) {
		inspection.ReviewOwner = lease.OwnerID
		if !strings.EqualFold(strings.TrimSpace(lease.OwnerID), strings.TrimSpace(actorID)) {
			inspection.Actionable = false
			inspection.Reasons = append(inspection.Reasons, "review-owned-by-"+lease.OwnerID)
		}
	}
	return inspection
}

// reviewReturnQueue reads only the exact durable issues named by the return
// intent. Returning findings does not accept or integrate a Git candidate, so
// it must not depend on a whole-project snapshot or synchronous candidate and
// ancestor Git refreshes. The review lease claim below remains the atomic
// current-review-state gate before any return side effect is published.
func (a daemonOrchestrationAuthority) reviewReturnQueue(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest) ([]protocol.OrchestrationReview, error) {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return nil, fmt.Errorf("issue store unavailable")
	}
	queue := make([]protocol.OrchestrationReview, 0, len(request.IssueIDs))
	for _, issueID := range stableRequestedReviewCandidates(request.IssueIDs, nil) {
		task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("load review return issue %s: %w", issueID, err)
		}
		if request.Scope.Kind == domain.OrchestrationScopeRooted && (task.ParentID == nil || task.ParentID.IsZero() || !naming.IssueIDsEqual(task.ParentID.String(), request.Scope.RootIssueID.String())) {
			continue
		}
		reviewRequested, err := currentReviewRequested(ctx, issueClient, task)
		if err != nil {
			return nil, fmt.Errorf("load review return epoch %s: %w", issueID, err)
		}
		if !reviewRequested {
			continue
		}
		inspection := reviewCoordinationInspection(task, request.ActorID)
		if outcome, err := a.latestTrustedReviewOutcome(ctx, projectID, task.ID.String()); err != nil {
			inspection.Actionable = false
			inspection.Reasons = append(inspection.Reasons, "inspect-review-outcome: "+err.Error())
		} else if outcome == "accepted" {
			inspection.Actionable = false
			inspection.Reasons = append(inspection.Reasons, "accepted-close-pending")
		}
		inspection.Reasons = uniqueNonEmpty(inspection.Reasons)
		queue = append(queue, inspection)
	}
	return queue, nil
}

func currentReviewRequested(ctx context.Context, issueClient *issues.Client, task domain.Task) (bool, error) {
	events, err := issueClient.ListIssueObservationEvents(ctx, task.ID.String(), issues.IssueObservationEventListOptions{
		Types:         []domain.IssueObservationEventType{domain.IssueEventIssueStatusChanged},
		NewestIDFirst: true,
	})
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if strings.TrimSpace(event.Source) != "issue-store" {
			continue
		}
		return domain.IsReviewRequestTransition(event), nil
	}
	facts := task.IssueFacts()
	return task.Status == domain.StatusInReview || task.State.Review() == domain.IssueReviewRequested || facts.ReviewState == domain.IssueReviewRequested, nil
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

func reviewAdmissionPinFromInspection(inspection protocol.OrchestrationReview) (issues.ReviewAdmissionPin, error) {
	if inspection.ReviewEpochEventID <= 0 {
		return issues.ReviewAdmissionPin{}, fmt.Errorf("exported review admission has no epoch identity")
	}
	pin := issues.ReviewAdmissionPin{ReviewEpochEventID: inspection.ReviewEpochEventID}
	if inspection.EvidenceEventID != 0 || inspection.EvidenceSeq != 0 || strings.TrimSpace(inspection.EvidenceDigest) != "" {
		if inspection.EvidenceEventID <= 0 || strings.TrimSpace(inspection.EvidenceDigest) == "" {
			return issues.ReviewAdmissionPin{}, fmt.Errorf("exported review admission has incomplete evidence identity")
		}
		pin.Evidence = &issues.ReviewEvidencePin{
			Source: strings.TrimSpace(inspection.EvidenceSource), EventID: inspection.EvidenceEventID,
			Seq: inspection.EvidenceSeq, Digest: strings.TrimSpace(inspection.EvidenceDigest),
		}
	}
	return pin, nil
}

func (a daemonOrchestrationAuthority) validateReviewAdmissionInspection(ctx context.Context, projectID string, inspection protocol.OrchestrationReview) error {
	expected, err := reviewAdmissionPinFromInspection(inspection)
	if err != nil {
		return err
	}
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	current, err := issueClient.CaptureReviewAdmissionPin(ctx, inspection.IssueID)
	if err != nil {
		return fmt.Errorf("validate review admission: %w", err)
	}
	if current.ReviewEpochEventID != expected.ReviewEpochEventID || (current.Evidence == nil) != (expected.Evidence == nil) || (current.Evidence != nil && *current.Evidence != *expected.Evidence) {
		return fmt.Errorf("review admission epoch or evidence identity changed")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, inspection.IssueID)
	if err != nil {
		return fmt.Errorf("validate review parent: %w", err)
	}
	currentParent := ""
	if task.ParentID != nil {
		currentParent = task.ParentID.String()
	}
	if (currentParent == "") != (strings.TrimSpace(inspection.ParentIssueID) == "") || (currentParent != "" && !naming.IssueIDsEqual(currentParent, inspection.ParentIssueID)) {
		return fmt.Errorf("review parent changed from %q to %q", inspection.ParentIssueID, currentParent)
	}
	if strings.TrimSpace(inspection.SourceOID) == "" {
		if strings.TrimSpace(inspection.WorktreePath) != "" {
			return fmt.Errorf("exported review admission has no exact candidate pin")
		}
		return nil
	}
	if strings.TrimSpace(inspection.WorktreePath) != "" {
		path, err := a.daemon.exactReviewCandidateWorktree(ctx, projectID, inspection.IssueID)
		if err != nil {
			return fmt.Errorf("validate exact review candidate: %w", err)
		}
		if filepath.Clean(path) != filepath.Clean(strings.TrimSpace(inspection.WorktreePath)) {
			return fmt.Errorf("review candidate path changed from %q to %q", inspection.WorktreePath, path)
		}
	}
	oid, err := a.resolveAcceptedReviewSourceOID(ctx, projectID, inspection.IssueID)
	if err != nil {
		return fmt.Errorf("validate exact review candidate: %w", err)
	}
	if strings.TrimSpace(oid) != strings.TrimSpace(inspection.SourceOID) {
		return fmt.Errorf("review candidate changed from %s to %s", inspection.SourceOID, strings.TrimSpace(oid))
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
	var reviews []protocol.OrchestrationReview
	var err error
	if request.Kind == protocol.OrchestrationIntentReviewReturn {
		reviews, err = a.reviewReturnQueue(ctx, projectID, request)
	} else {
		var snapshot protocol.OrchestrationSnapshot
		snapshot, err = a.Snapshot(ctx, projectID, protocol.OrchestrationSnapshotRequest{Scope: request.Scope, ActorID: request.ActorID, RepoDir: request.RepoDir, ReviewIssueIDs: request.IssueIDs})
		reviews = snapshot.ReviewQueue
	}
	if err != nil {
		return protocol.OrchestrationIntentResult{}, err
	}
	if hook := a.daemon.reviewAdmissionSnapshotLoaded; hook != nil {
		hook()
	}
	queue := make(map[string]protocol.OrchestrationReview, len(reviews))
	ordered := make([]string, 0, len(reviews))
	for _, review := range reviews {
		queue[review.IssueID] = review
		ordered = append(ordered, review.IssueID)
	}
	requested := stableRequestedReviewCandidates(request.IssueIDs, ordered)
	if len(request.IssueIDs) == 0 {
		requested = ordered
	}
	result := protocol.OrchestrationIntentResult{Scope: request.Scope, Kind: request.Kind, IntentKey: request.IntentKey, Requested: requested, Skipped: map[string]string{}, Failed: map[string]string{}}
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return protocol.OrchestrationIntentResult{}, fmt.Errorf("issue store unavailable")
	}
	for _, issueID := range requested {
		if request.Scope.Kind == domain.OrchestrationScopeRooted {
			task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
			if err != nil {
				result.Failed[issueID] = "refresh rooted review scope: " + err.Error()
				continue
			}
			if task.ParentID == nil || task.ParentID.IsZero() || !naming.IssueIDsEqual(task.ParentID.String(), request.Scope.RootIssueID.String()) {
				result.Skipped[issueID] = "outside-root-direct-child-scope: delegate descendants to their direct parent orchestrator"
				continue
			}
		}
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
			currentPin := storedPin
			if integrateBeforeClose {
				currentPin.SourceOID, err = a.resolveAcceptedReviewSourceOID(ctx, projectID, issueID)
				currentPin.SourceOID = strings.TrimSpace(currentPin.SourceOID)
			}
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
			var target taskMergeBaseTargetResult
			queueAvailable := integrateBeforeClose && a.daemon.operationRuntime != nil && a.daemon.operationRuntime.store != nil && a.daemon.operationRuntime.manager != nil && a.daemon.worktreeAdapter != nil && a.daemon.git != nil
			if queueAvailable {
				target, err = a.daemon.taskMergeBaseTarget(ctx, projectID, issueID, a.daemon.baseBranchForProject(projectID), false)
				if err != nil {
					result.Failed[issueID] = err.Error()
					continue
				}
			}
			if queueAvailable && strings.EqualFold(strings.TrimSpace(target.TargetID), "base") {
				publication, enqueueErr := a.daemon.enqueueAcceptedReviewPublication(ctx, projectID, request, issueID, storedPin)
				if enqueueErr != nil {
					result.Failed[issueID] = enqueueErr.Error()
					continue
				}
				result.Publications = append(result.Publications, publication)
				continue
			}
			if _, err := a.releaseAndCloseAcceptedReview(ctx, projectID, request, issueID, integrateBeforeClose, storedPin, "", &result); err != nil {
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
		activeValidationReturn := false
		if !ok && request.Kind == protocol.OrchestrationIntentReviewReturn {
			inspection, activeValidationReturn, err = a.activeValidationReviewReturn(ctx, projectID, request, issueID)
			if err != nil {
				result.Failed[issueID] = "inspect active validation review: " + err.Error()
				continue
			}
		}
		if !ok {
			if !activeValidationReturn {
				result.Skipped[issueID] = "not-review-ready"
				continue
			}
		}
		revokingAcceptedReview := request.Kind == protocol.OrchestrationIntentReviewReturn && slices.Contains(inspection.Reasons, "accepted-close-pending")
		if !inspection.Actionable && !revokingAcceptedReview {
			result.Skipped[issueID] = strings.Join(inspection.Reasons, "; ")
			continue
		}
		if !activeValidationReturn {
			claim := issues.OwnershipClaimParams{OwnerID: request.ActorID, OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview}
			if request.Kind == protocol.OrchestrationIntentReviewAccept {
				admission, admissionErr := reviewAdmissionPinFromInspection(inspection)
				if admissionErr != nil {
					result.Skipped[issueID] = "claim-review: " + admissionErr.Error()
					continue
				}
				if err := a.validateReviewAdmissionInspection(ctx, projectID, inspection); err != nil {
					result.Skipped[issueID] = "claim-review: " + err.Error()
					continue
				}
				claim.ExpectedReviewAdmission = &admission
				claim.ExpectedParentIssueID = inspection.ParentIssueID
				claim.ReviewSourceOID = inspection.SourceOID
			}
			if _, err := issueClient.ClaimOwnershipWithRuntime(ctx, projectID, issueID, claim); err != nil {
				result.Skipped[issueID] = "claim-review: " + err.Error()
				continue
			}
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

// activeValidationReviewReturn preserves the formal review outcome when the
// canonical aggregate gate assigned during the current review-request epoch
// moves the worker back to active before reporting a failure. The durable gate
// request is the fence: gates from an earlier review epoch cannot authorize a
// return against a later implementation episode.
func (a daemonOrchestrationAuthority) activeValidationReviewReturn(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string) (protocol.OrchestrationReview, bool, error) {
	if a.daemon.operationRuntime == nil {
		return protocol.OrchestrationReview{}, false, nil
	}
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return protocol.OrchestrationReview{}, false, fmt.Errorf("issue store unavailable")
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return protocol.OrchestrationReview{}, false, err
	}
	if task.Status != domain.StatusInProgress {
		return protocol.OrchestrationReview{}, false, nil
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventIssueStatusChanged}})
	if err != nil {
		return protocol.OrchestrationReview{}, false, err
	}
	var reviewRequestedAt time.Time
	var reviewEpochEventID int64
	for _, event := range events {
		if domain.IsReviewRequestTransition(event) && event.ObservedAt.After(reviewRequestedAt) {
			reviewRequestedAt = event.ObservedAt
			reviewEpochEventID = event.ID
		}
	}
	if reviewRequestedAt.IsZero() {
		return protocol.OrchestrationReview{}, false, nil
	}
	validationStore, err := a.daemon.validationProjectionStore()
	if err != nil {
		return protocol.OrchestrationReview{}, false, err
	}
	gate, err := validationStore.LatestReviewValidation(ctx, projectID, issueID, time.Now().UTC(), defaultValidationLeaseTTL)
	if err != nil {
		return protocol.OrchestrationReview{}, false, err
	}
	// The production validation wrapper records successful commands as
	// completed/"exit 0" and unsuccessful commands as failed/"exit N".
	// Outcome is diagnostic text, not authority: only the typed failed state may
	// authorize returning an actively validating review candidate.
	failedGate := gate != nil && gate.State == domain.ValidationRequestFailed
	if gate == nil || !failedGate || gate.ReviewEpochEventID != reviewEpochEventID {
		return protocol.OrchestrationReview{}, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(gate.ReviewerID), strings.TrimSpace(request.ActorID)) {
		return protocol.OrchestrationReview{}, false, nil
	}
	if a.daemon.git == nil || strings.TrimSpace(request.RepoDir) == "" {
		return protocol.OrchestrationReview{}, false, nil
	}
	candidateRevision, err := a.daemon.git.HeadRevision(ctx, request.RepoDir)
	if err != nil || candidateRevision != strings.TrimSpace(gate.SourceRevision) {
		return protocol.OrchestrationReview{}, false, nil
	}

	tasks, _, err := a.daemon.projectReadSnapshot(projectID)
	if err != nil {
		return protocol.OrchestrationReview{}, false, err
	}
	byID := make(map[string]domain.Task, len(tasks))
	for _, candidate := range tasks {
		byID[candidate.ID.String()] = candidate
	}
	worktrees := a.daemon.projectReadWorktrees(projectID)
	inspection := a.reviewInspection(ctx, projectID, request.RepoDir, request.ActorID, task, byID, worktrees)
	if inspection.ReviewContext == nil {
		inspection.HeadRevision = strings.TrimSpace(gate.SourceRevision)
		if packet, packetErr := buildReviewWorkflowContext(task, inspection, domain.WorkflowRoleReviewer); packetErr == nil {
			inspection.ReviewContext = &packet
			if integrationPacket, integrationErr := buildReviewWorkflowContext(task, inspection, domain.WorkflowRoleIntegrator); integrationErr == nil {
				inspection.IntegrationContext = &integrationPacket
			}
		}
	}
	inspection.Reasons = uniqueNonEmpty(append(inspection.Reasons, "active-validation-return:"+gate.RequestID))
	return inspection, true, nil
}

func (a daemonOrchestrationAuthority) returnReviewFindings(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, inspection protocol.OrchestrationReview, result *protocol.OrchestrationIntentResult) error {
	if inspection.ReviewEpochEventID > 0 {
		if err := a.validateReviewAdmissionInspection(ctx, projectID, inspection); err != nil {
			return fmt.Errorf("return review admission changed: %w", err)
		}
	}
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
	message, err := formatReviewFindingMessage(inspection, parent, request.Findings)
	if err != nil {
		return fmt.Errorf("build bounded review finding handoff: %w", err)
	}
	if inspection.ReviewEpochEventID > 0 {
		if err := a.validateReviewAdmissionInspection(ctx, projectID, inspection); err != nil {
			return fmt.Errorf("return review admission changed before delivery: %w", err)
		}
	}
	deliveryTimeout := a.reviewDeliveryTimeout
	if deliveryTimeout <= 0 {
		deliveryTimeout = orchestrationReviewDeliveryTimeout
	}
	withTimeout := a.reviewDeliveryContext
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	deliveryCtx, cancelDelivery := withTimeout(ctx, deliveryTimeout)
	if a.daemon.cfg.Logger != nil {
		a.daemon.cfg.Logger.Info("orchestration review return stage started", "project_id", projectID, "issue_id", inspection.IssueID, "intent_key", request.IntentKey, "stage", "live_delivery", "target", inspection.IssueID, "timeout", deliveryTimeout)
	}
	delivered, deliveryErr := a.deliverReviewMessage(deliveryCtx, projectID, request, inspection.IssueID, message)
	cancelDelivery()
	if deliveryErr != nil {
		deliveryErr = fmt.Errorf("stage=live_delivery target=%s: %w", inspection.IssueID, deliveryErr)
		if a.daemon.cfg.Logger != nil {
			a.daemon.cfg.Logger.Warn("orchestration review return stage failed", "project_id", projectID, "issue_id", inspection.IssueID, "intent_key", request.IntentKey, "stage", "live_delivery", "target", inspection.IssueID, "error", deliveryErr)
		}
	} else if a.daemon.cfg.Logger != nil {
		a.daemon.cfg.Logger.Info("orchestration review return stage completed", "project_id", projectID, "issue_id", inspection.IssueID, "intent_key", request.IntentKey, "stage", "live_delivery", "target", inspection.IssueID)
	}
	if deliveryErr != nil && request.RestartWorker {
		if inspection.ReviewEpochEventID > 0 {
			if err := a.validateReviewAdmissionInspection(ctx, projectID, inspection); err != nil {
				return fmt.Errorf("return review admission changed before restart: %w", err)
			}
		}
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
			if err := a.recordReviewRestartSubmitted(ctx, projectID, inspection.IssueID, request, launch, inspection); err != nil {
				return err
			}
			return nil
		case protocol.OperationStateDone:
			delivered = true
		case protocol.OperationStateFailed, protocol.OperationStateCancelled:
			failure := fmt.Sprintf("restart operation %s reached terminal %s", launch.OperationID, launch.OperationState)
			if err := a.recordReviewOutcomePinned(ctx, projectID, inspection.IssueID, request, "delivery_failed", failure, inspection); err != nil {
				return fmt.Errorf("review findings recorded but delivery failed: %v; %s; record failure: %w", deliveryErr, failure, err)
			}
			return fmt.Errorf("review findings recorded but delivery failed: %v; %s", deliveryErr, failure)
		default:
			return fmt.Errorf("review findings recorded but delivery failed: %v; restart operation %s returned unknown state %q", deliveryErr, launch.OperationID, launch.OperationState)
		}
	}
	if !delivered {
		failure := deliveryFailureMessage(deliveryErr, inspection.IssueID)
		if inspection.ReviewEpochEventID > 0 {
			if err := a.validateReviewAdmissionInspection(ctx, projectID, inspection); err != nil {
				return fmt.Errorf("return review admission changed before failure outcome: %w", err)
			}
		}
		if err := a.recordReviewOutcomePinned(ctx, projectID, inspection.IssueID, request, "delivery_failed", failure, inspection); err != nil {
			return fmt.Errorf("review findings recorded but active delivery failed: %s; publish durable failure: %w", failure, err)
		}
		return fmt.Errorf("review findings recorded but active delivery failed: %s", failure)
	}
	if inspection.ReviewEpochEventID > 0 {
		if err := a.validateReviewAdmissionInspection(ctx, projectID, inspection); err != nil {
			return fmt.Errorf("return review admission changed before outcome: %w", err)
		}
	}
	if err := a.recordReviewOutcomePinned(ctx, projectID, inspection.IssueID, request, "returned", "", inspection); err != nil {
		return err
	}
	return nil
}

func deliveryFailureMessage(err error, target string) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("stage=live_delivery target=%s: delivery returned without success", target)
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
	if hook := a.daemon.reviewAcceptanceBeforeCandidateCheck; hook != nil {
		hook()
	}
	candidatePath := ""
	if inspection.Evidence != nil || strings.TrimSpace(inspection.WorktreePath) != "" {
		var candidateErr error
		candidatePath, _, candidateErr = a.daemon.exactReviewedCandidateRevision(ctx, projectID, inspection)
		if candidateErr != nil {
			return false, fmt.Errorf("revalidate accepted review candidate: %w", candidateErr)
		}
	}
	if err := a.validateReviewAdmissionInspection(ctx, projectID, inspection); err != nil {
		return false, fmt.Errorf("accept review admission changed: %w", err)
	}
	if len(inspection.PendingDecisions) > 0 {
		return false, fmt.Errorf("accepted review rejected by stale material decisions: %s", strings.Join(pendingDecisionReadinessReasons(inspection.PendingDecisions), "; "))
	}
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
			if slices.ContainsFunc(inspection.Reasons, func(reason string) bool {
				return strings.Contains(reason, "candidate worktree is required to bind aggregate validation")
			}) {
				return false, fmt.Errorf("accepted review candidate rejected: %s", strings.Join(inspection.Reasons, "; "))
			}
			if len(inspection.Reasons) > 0 {
				return false, fmt.Errorf("accepted review requires complete worker_evidence.v1 or a declared internal_review investigation with a durable accepted/ratified review artifact: %s", strings.Join(inspection.Reasons, "; "))
			}
			return false, fmt.Errorf("accepted review requires complete worker_evidence.v1 or a declared internal_review investigation with a durable accepted/ratified review artifact")
		}
	}
	if inspection.Evidence != nil {
		// Snapshot inspection can race with new evidence or decisions. Re-read the
		// immutable patch-review inputs immediately before acceptance, while leaving
		// exact aggregate validation to the durable publication continuation.
		refreshed, err := a.daemon.taskReviewAcceptanceReadiness(ctx, projectID, inspection.IssueID, candidatePath)
		if err != nil {
			return false, fmt.Errorf("revalidate accepted review candidate: %w", err)
		}
		if !refreshed.Ready {
			return false, fmt.Errorf("accepted review candidate is not patch-review-ready: %s", strings.Join(refreshed.Reasons, "; "))
		}
		refreshedPin, err := reviewEvidencePinFromReadiness(refreshed)
		if err != nil {
			return false, fmt.Errorf("revalidate accepted review evidence identity: %w", err)
		}
		expectedPin, err := reviewAdmissionPinFromInspection(inspection)
		if err != nil || expectedPin.Evidence == nil || refreshedPin != *expectedPin.Evidence {
			return false, fmt.Errorf("revalidate accepted review evidence identity: exported evidence changed")
		}
	}
	if inspection.ContextRisk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*inspection.ContextRisk) {
		return false, fmt.Errorf("accepted review requires structured high-context-risk closeout evidence")
	}
	integrateBeforeClose := inspection.Evidence != nil || strings.TrimSpace(inspection.WorktreePath) != ""
	pin, err := a.captureAcceptedReviewPin(ctx, projectID, candidatePath, inspection, integrateBeforeClose)
	if err != nil {
		return false, err
	}
	var target taskMergeBaseTargetResult
	queueAvailable := integrateBeforeClose && a.daemon.operationRuntime != nil && a.daemon.operationRuntime.store != nil && a.daemon.operationRuntime.manager != nil && a.daemon.worktreeAdapter != nil && a.daemon.git != nil
	if queueAvailable {
		target, err = a.daemon.taskMergeBaseTarget(ctx, projectID, inspection.IssueID, a.daemon.baseBranchForProject(projectID), false)
		if err != nil {
			return false, err
		}
	}
	if queueAvailable && strings.EqualFold(strings.TrimSpace(target.TargetID), "base") {
		publication, err := a.daemon.acceptAndEnqueueReviewPublication(ctx, projectID, request, inspection.IssueID, pin, inspection)
		if err != nil {
			return false, fmt.Errorf("enqueue accepted review publication: %w", err)
		}
		result.Publications = append(result.Publications, publication)
		// The durable review lease remains owned by the queue until its runner
		// enters authoritative close. Returning true prevents the outer intent
		// loop from releasing it while this accepted patch is queued.
		return true, nil
	}
	if err := a.validateReviewAdmissionInspection(ctx, projectID, inspection); err != nil {
		return false, fmt.Errorf("accept review admission changed before outcome: %w", err)
	}
	if err := a.recordAcceptedReviewOutcome(ctx, projectID, inspection.IssueID, request, pin, inspection); err != nil {
		return false, err
	}
	return a.releaseAndCloseAcceptedReview(ctx, projectID, request, inspection.IssueID, integrateBeforeClose, pin, "", result)
}

// exactReviewCandidateWorktree implements the hybrid project-review invariant:
// first read the durable issue-worktree projection, then compare that identity
// with Git's live worktree registry. The primary checkout is never a fallback.
func (d *Daemon) exactReviewCandidateWorktree(ctx context.Context, projectID, issueID string) (string, error) {
	if d == nil || d.worktreeAdapter == nil {
		return "", fmt.Errorf("candidate_worktree_unavailable: worktree projection authority is unavailable")
	}
	if err := d.refreshExactReviewWorktreeGitFacts(ctx, projectID, []string{issueID}); err != nil {
		return "", fmt.Errorf("candidate_git_facts_refresh_failed: %w", err)
	}
	projected, found, err := d.worktreeAdapter.projectedWorktreeForIssue(ctx, projectID, issueID)
	if err != nil {
		return "", fmt.Errorf("candidate_projection_read_failed: %w", err)
	}
	if !found || strings.TrimSpace(projected.Path) == "" {
		return "", fmt.Errorf("candidate_projection_missing: issue %s has no durable worktree projection", issueID)
	}
	manager := d.worktreeAdapter.managerFor(projectID)
	if manager == nil {
		return "", fmt.Errorf("candidate_live_registry_unavailable: worktree manager is unavailable")
	}
	live, err := manager.List(ctx)
	if err != nil {
		return "", fmt.Errorf("candidate_live_registry_failed: %w", err)
	}
	projectedPath := filepath.Clean(strings.TrimSpace(projected.Path))
	projectedBranch := strings.TrimSpace(projected.Branch)
	for _, worktree := range live {
		livePath := filepath.Clean(strings.TrimSpace(worktree.Path))
		if livePath == projectedPath && !naming.IssueIDsEqual(worktree.IssueID, issueID) {
			return "", fmt.Errorf("candidate_path_reused: projected path %s now belongs to issue %s", projectedPath, worktree.IssueID)
		}
		if !naming.IssueIDsEqual(worktree.IssueID, issueID) {
			continue
		}
		if livePath != projectedPath {
			return "", fmt.Errorf("candidate_path_mismatch: projected path %s does not match live path %s", projectedPath, livePath)
		}
		if projectedBranch == "" || strings.TrimSpace(worktree.Branch) != projectedBranch {
			return "", fmt.Errorf("candidate_branch_mismatch: projected branch %q does not match live branch %q", projectedBranch, strings.TrimSpace(worktree.Branch))
		}
		return projectedPath, nil
	}
	return "", fmt.Errorf("candidate_projection_stale: projected worktree %s for issue %s is absent from the live Git worktree registry", projectedPath, issueID)
}

func (d *Daemon) exactReviewedCandidateRevision(ctx context.Context, projectID string, inspection protocol.OrchestrationReview) (string, string, error) {
	if d.reviewCandidateCheck != nil {
		return d.reviewCandidateCheck(ctx, projectID, inspection)
	}
	candidatePath, err := d.exactReviewCandidateWorktree(ctx, projectID, inspection.IssueID)
	if err != nil {
		return "", "", err
	}
	inspectedPath := strings.TrimSpace(inspection.WorktreePath)
	if inspectedPath == "" || filepath.Clean(inspectedPath) != candidatePath {
		return "", "", fmt.Errorf("candidate_path_changed: inspected path %q does not match current exact path %q", inspectedPath, candidatePath)
	}
	inspectedHead := strings.TrimSpace(inspection.HeadRevision)
	if inspectedHead == "" {
		return "", "", fmt.Errorf("reviewed_patch_identity_missing: inspection has no immutable HEAD revision")
	}
	if d.git == nil {
		return "", "", fmt.Errorf("candidate_git_unavailable: Git authority is unavailable")
	}
	var currentHead string
	if err := d.git.WithWorktreeLock(ctx, candidatePath, func(lockCtx context.Context) error {
		status, statusErr := d.git.Status(lockCtx, candidatePath)
		if statusErr != nil {
			return fmt.Errorf("inspect reviewed candidate tree: %w", statusErr)
		}
		if status.HasChanges {
			return fmt.Errorf("reviewed candidate worktree is dirty and no longer matches immutable revision %s", inspectedHead)
		}
		currentHead, statusErr = d.git.HeadRevision(lockCtx, candidatePath)
		if statusErr != nil {
			return fmt.Errorf("resolve reviewed candidate revision: %w", statusErr)
		}
		return nil
	}); err != nil {
		return "", "", err
	}
	currentHead = strings.TrimSpace(currentHead)
	if currentHead != inspectedHead {
		return "", "", fmt.Errorf("reviewed candidate revision changed from %s to %s after inspection", inspectedHead, currentHead)
	}
	return candidatePath, currentHead, nil
}

func (a daemonOrchestrationAuthority) captureAcceptedReviewPin(ctx context.Context, projectID, repoDir string, inspection protocol.OrchestrationReview, integrateBeforeClose bool) (acceptedReviewPin, error) {
	pin := acceptedReviewPin{}
	if inspection.Evidence != nil {
		pin.EvidenceSource = strings.TrimSpace(inspection.EvidenceSource)
		pin.EvidenceEventID = inspection.EvidenceEventID
		pin.EvidenceSeq = inspection.EvidenceSeq
		pin.EvidenceDigest = strings.TrimSpace(inspection.EvidenceDigest)
		if pin.EvidenceEventID <= 0 || pin.EvidenceDigest == "" {
			return pin, fmt.Errorf("capture reviewed evidence identity: exported evidence pin is incomplete")
		}
	}
	if integrateBeforeClose {
		pin.SourceOID = strings.TrimSpace(inspection.SourceOID)
		if pin.SourceOID == "" {
			return pin, fmt.Errorf("capture reviewed source commit: empty object ID")
		}
	}
	return pin, nil
}

func reviewEvidencePinFromReadiness(readiness taskIntegrationReadinessResult) (issues.ReviewEvidencePin, error) {
	if readiness.EvidencePacket == nil {
		return issues.ReviewEvidencePin{}, fmt.Errorf("current evidence is unavailable")
	}
	body, err := json.Marshal(readiness.EvidencePacket)
	if err != nil {
		return issues.ReviewEvidencePin{}, fmt.Errorf("encode evidence identity: %w", err)
	}
	source := strings.TrimSpace(readiness.EvidenceSource)
	if source == "" && readiness.EvidenceEventSeq > 0 {
		source = "mailbox"
	}
	return issues.ReviewEvidencePin{
		Source: source, EventID: readiness.EvidenceEventID, Seq: readiness.EvidenceEventSeq,
		Digest: fmt.Sprintf("%x", sha256.Sum256(body)),
	}, nil
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

func (a daemonOrchestrationAuthority) releaseAndCloseAcceptedReview(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string, integrateBeforeClose bool, pin acceptedReviewPin, expectedBaseOID string, result *protocol.OrchestrationIntentResult) (bool, error) {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return false, fmt.Errorf("issue store unavailable")
	}
	var evidencePin *issues.ReviewEvidencePin
	if strings.TrimSpace(pin.EvidenceDigest) != "" {
		evidencePin = &issues.ReviewEvidencePin{Source: pin.EvidenceSource, EventID: pin.EvidenceEventID, Seq: pin.EvidenceSeq, Digest: pin.EvidenceDigest}
	}
	task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		return false, fmt.Errorf("inspect review lease before authoritative close: %w", err)
	}
	reviewLease := coordinationLease(task, domain.CoordinationLeaseReview)
	resumingEvidenceFence := evidencePin != nil && issues.ReviewEvidenceCloseFenceMatches(reviewLease, *evidencePin)
	if reviewLease != nil && !resumingEvidenceFence {
		if _, err := issueClient.ReleaseOwnershipWithRuntime(ctx, projectID, issueID, issues.OwnershipClaimParams{OwnerID: request.ActorID, Purpose: domain.CoordinationLeaseReview}); err != nil {
			return false, fmt.Errorf("release review lease before authoritative close: %w", err)
		}
	}
	if hook := a.daemon.reviewLeaseReleasedBeforeClose; hook != nil {
		if err := hook(ctx, projectID, issueID); err != nil {
			return true, fmt.Errorf("after review lease release: %w", err)
		}
	}
	return true, a.closeAcceptedReview(ctx, projectID, request, issueID, integrateBeforeClose, pin, expectedBaseOID, result)
}

func (a daemonOrchestrationAuthority) closeAcceptedReview(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string, integrateBeforeClose bool, pin acceptedReviewPin, expectedBaseOID string, result *protocol.OrchestrationIntentResult) error {
	var evidencePin *issues.ReviewEvidencePin
	if strings.TrimSpace(pin.EvidenceDigest) != "" {
		evidencePin = &issues.ReviewEvidencePin{Source: pin.EvidenceSource, EventID: pin.EvidenceEventID, Seq: pin.EvidenceSeq, Digest: pin.EvidenceDigest}
	}
	body, err := json.Marshal(taskCloseRequest{
		TaskID: issueID, IntegrateBeforeClose: integrateBeforeClose, ExpectedSourceOID: strings.TrimSpace(pin.SourceOID), ExpectedBaseOID: strings.TrimSpace(expectedBaseOID), ExpectedReviewEvidence: evidencePin,
	})
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
	return a.recordReviewOutcomeWithRestart(ctx, projectID, issueID, request, outcome, failure, nil, nil, nil)
}

func (a daemonOrchestrationAuthority) recordReviewOutcomePinned(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, outcome, failure string, inspection protocol.OrchestrationReview) error {
	if inspection.ReviewEpochEventID <= 0 {
		return a.recordReviewOutcome(ctx, projectID, issueID, request, outcome, failure)
	}
	return a.recordReviewOutcomeWithRestart(ctx, projectID, issueID, request, outcome, failure, nil, nil, &inspection)
}

func (a daemonOrchestrationAuthority) recordAcceptedReviewOutcome(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, pin acceptedReviewPin, inspection protocol.OrchestrationReview) error {
	metadata := map[string]any{
		"reviewed_source_oid":        pin.SourceOID,
		"reviewed_evidence_source":   pin.EvidenceSource,
		"reviewed_evidence_event_id": pin.EvidenceEventID,
		"reviewed_evidence_seq":      pin.EvidenceSeq,
		"reviewed_evidence_digest":   pin.EvidenceDigest,
	}
	return a.recordReviewOutcomeWithRestart(ctx, projectID, issueID, request, string(domain.ReviewOutcomeAccepted), "", nil, metadata, &inspection)
}

func (a daemonOrchestrationAuthority) recordReviewRestartSubmitted(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, launch protocol.OrchestrationLaunch, inspection protocol.OrchestrationReview) error {
	operationID, err := naming.ParseOperationID(launch.OperationID)
	if err != nil {
		return fmt.Errorf("record restart submission operation: %w", err)
	}
	state := protocol.OperationState(launch.OperationState)
	if state != protocol.OperationStateQueued && state != protocol.OperationStateRunning {
		return fmt.Errorf("record restart submission operation %s with non-pending state %q", operationID, state)
	}
	restart := &domain.ReviewRestartSubmission{OperationID: operationID, State: domain.ReviewRestartOperationState(state), SessionID: strings.TrimSpace(launch.SessionID), ActorID: strings.TrimSpace(request.ActorID)}
	var admission *protocol.OrchestrationReview
	if inspection.ReviewEpochEventID > 0 {
		admission = &inspection
	}
	return a.recordReviewOutcomeWithRestart(ctx, projectID, issueID, request, "restart_submitted", "", restart, nil, admission)
}

func (a daemonOrchestrationAuthority) recordReviewOutcomeWithRestart(ctx context.Context, projectID, issueID string, request protocol.OrchestrationIntentRequest, outcome, failure string, restart *domain.ReviewRestartSubmission, metadata map[string]any, inspection *protocol.OrchestrationReview) error {
	issueClient := a.daemon.issueClientForProject(projectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable")
	}
	if inspection != nil {
		if metadata == nil {
			metadata = make(map[string]any)
		}
		metadata["review_epoch_event_id"] = inspection.ReviewEpochEventID
		metadata["review_parent_issue_id"] = strings.TrimSpace(inspection.ParentIssueID)
		metadata["review_source_oid"] = strings.TrimSpace(inspection.SourceOID)
		metadata["review_evidence_source"] = strings.TrimSpace(inspection.EvidenceSource)
		metadata["review_evidence_event_id"] = inspection.EvidenceEventID
		metadata["review_evidence_seq"] = inspection.EvidenceSeq
		metadata["review_evidence_digest"] = strings.TrimSpace(inspection.EvidenceDigest)
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
	if inspection != nil {
		admission, admissionErr := reviewAdmissionPinFromInspection(*inspection)
		if admissionErr != nil {
			return admissionErr
		}
		_, err = issueClient.AppendIssueObservationEventWithReviewAdmission(ctx, parsed.String(), params, admission, inspection.ParentIssueID, request.ActorID)
	} else {
		_, err = issueClient.AppendIssueObservationEvent(ctx, parsed.String(), params)
	}
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

func formatReviewFindingMessage(inspection protocol.OrchestrationReview, parent string, findings []protocol.OrchestrationReviewFinding) (string, error) {
	revision := strings.TrimSpace(inspection.HeadRevision)
	if revision == "" {
		revision = strings.TrimSpace(inspection.SourceOID)
	}
	if revision == "" && inspection.ReviewEpochEventID > 0 {
		revision = fmt.Sprintf("issue-event:%d", inspection.ReviewEpochEventID)
	}
	unresolved := make([]string, 0, len(findings))
	invariants := make([]string, 0)
	for _, finding := range findings {
		parts := []string{strings.TrimSpace(finding.Severity)}
		if file := strings.TrimSpace(finding.File); file != "" && !filepath.IsAbs(file) {
			if finding.Line > 0 {
				file = fmt.Sprintf("%s:%d", file, finding.Line)
			}
			parts = append(parts, file)
		}
		parts = append(parts, strings.TrimSpace(finding.Finding))
		if finding.SuggestedFix != "" {
			parts = append(parts, "fix: "+strings.TrimSpace(finding.SuggestedFix))
		}
		unresolved = append(unresolved, strings.Join(parts, " — "))
		invariants = append(invariants, finding.Validation...)
	}
	if revision == "" {
		material, err := json.Marshal(struct {
			IssueID  string                                `json:"issue_id"`
			ParentID string                                `json:"parent_id"`
			Findings []protocol.OrchestrationReviewFinding `json:"findings"`
		}{IssueID: inspection.IssueID, ParentID: parent, Findings: findings})
		if err != nil {
			return "", fmt.Errorf("marshal review finding provenance: %w", err)
		}
		sum := sha256.Sum256(material)
		revision = fmt.Sprintf("review-findings-sha256:%x", sum)
	}
	packet, err := domain.BuildWorkflowContextPacket(domain.WorkflowContextInput{
		Role: domain.WorkflowRoleWorker, IssueID: inspection.IssueID, SourceRevision: revision,
		Summary:            "Address returned review findings under root " + parent,
		UnresolvedFindings: unresolved, AffectedInvariants: invariants,
	})
	if err != nil {
		return "", err
	}
	encoded, err := domain.MarshalWorkflowContextPacket(packet)
	if err != nil {
		return "", err
	}
	return "Orchestrator review return for issue " + inspection.IssueID + ". Consume only this bounded semantic packet for the repair phase; do not reconstruct context from workflow scrollback:\n" + string(encoded) + "\n\nAddress the findings, rerun validation and the required review loop, then report updated worker-integration-ready evidence without stopping this session.", nil
}

func responseErrorMessage(resp protocol.ResponseEnvelope) string {
	if resp.Error != nil && strings.TrimSpace(resp.Error.Message) != "" {
		return strings.TrimSpace(resp.Error.Message)
	}
	return "daemon command failed"
}
