package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

// mergeTypedGitBranches resolves untrusted transport identifiers against the
// refreshed daemon issue/worktree projection before choosing composition or
// configured-base publication.
func (d *Daemon) mergeTypedGitBranches(ctx context.Context, projectID string, req daemonhandlers.GitMergeRequest) (*daemonhandlers.GitMergeResult, error) {
	projectID = d.canonicalProjectID(projectID)
	sourceID := strings.TrimSpace(req.SourceID)
	targetID := strings.TrimSpace(req.TargetID)
	if sourceID == "" || targetID == "" {
		return nil, fmt.Errorf("git.merge requires source_id and target_id")
	}
	if naming.IssueIDsEqual(sourceID, targetID) {
		return nil, fmt.Errorf("source and target issue must be different: %s", sourceID)
	}
	if d.git == nil || d.worktreeAdapter == nil {
		return nil, fmt.Errorf("typed git merge authority unavailable")
	}
	// Merge target authority is the durable issue graph plus the exact live Git
	// worktree identities read below. A whole runtime reconcile also probes tmux,
	// interactions, activity, and resource hooks that are unrelated to branch
	// composition and can make Git unavailable under repository-family load.
	tasks, _, err := d.convergedCanonicalProjectReadSnapshot(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("resolve authoritative git.merge issue graph: %w", err)
	}
	tasksByID := tasksByDaemonIssueID(tasks)
	sourceTask, ok := taskByCanonicalIssueID(tasksByID, sourceID)
	if !ok {
		return nil, fmt.Errorf("git.merge source issue %s is absent from authoritative issue projection", sourceID)
	}
	sourceID = sourceTask.ID.String()

	worktrees, err := d.worktreeAdapter.List(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("resolve authoritative git.merge worktrees: %w", err)
	}
	source, ok := daemonWorktreeForIssue(worktrees, sourceID)
	if !ok || strings.TrimSpace(source.Path) == "" || strings.TrimSpace(source.Branch) == "" {
		return nil, fmt.Errorf("git.merge source issue %s has no authoritative worktree identity", sourceID)
	}

	configuredBaseTarget := strings.EqualFold(targetID, "base")
	targetBranch := ""
	targetWorktree := ""
	branchAttached := false
	if configuredBaseTarget {
		resolved, err := d.taskMergeBaseTarget(ctx, projectID, sourceID, d.baseBranchForProject(projectID), false)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSpace(resolved.TargetID), "base") {
			return nil, fmt.Errorf("refusing configured-base publication for %s: authoritative target is issue %s", sourceID, resolved.TargetID)
		}
		if strings.EqualFold(strings.TrimSpace(d.workflowModeForProject(projectID)), "origin") {
			return nil, fmt.Errorf("direct configured-base git.merge is unavailable in origin workflow mode")
		}
		accepted, err := d.hasDurableBaseIntegrationAcceptance(ctx, projectID, sourceID)
		if err != nil {
			return nil, fmt.Errorf("verify configured-base integration acceptance: %w", err)
		}
		if !accepted {
			return nil, fmt.Errorf("refusing root issue %s integration into base without durable human acceptance", sourceID)
		}
		targetID = "base"
		targetBranch = strings.TrimSpace(resolved.Branch)
		targetWorktree = strings.TrimSpace(resolved.WorktreePath)
		branchAttached = resolved.BranchAttached
		if targetWorktree == "" {
			for _, worktree := range worktrees {
				if strings.TrimSpace(worktree.Branch) == targetBranch && strings.TrimSpace(worktree.Path) != "" {
					targetWorktree = strings.TrimSpace(worktree.Path)
					branchAttached = true
					break
				}
			}
		}
		if targetWorktree == "" {
			targetWorktree = strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
		}
		if targetWorktree == "" {
			return nil, fmt.Errorf("configured-base target worktree is unavailable for project %s", projectID)
		}
	} else {
		targetTask, ok := taskByCanonicalIssueID(tasksByID, targetID)
		if !ok {
			return nil, fmt.Errorf("git.merge target issue %s is absent from authoritative issue projection", targetID)
		}
		targetID = targetTask.ID.String()
		if !typedMergeIssuesAreAncestorRelated(sourceID, targetID, tasksByID) {
			return nil, fmt.Errorf("git.merge target %s is unrelated to source %s in the authoritative parent-child graph", targetID, sourceID)
		}
		target, ok := daemonWorktreeForIssue(worktrees, targetID)
		if !ok || strings.TrimSpace(target.Path) == "" || strings.TrimSpace(target.Branch) == "" {
			return nil, fmt.Errorf("git.merge target issue %s has no authoritative worktree identity", targetID)
		}
		targetWorktree = strings.TrimSpace(target.Path)
		targetBranch = strings.TrimSpace(target.Branch)
		branchAttached = true
	}

	if err := requireTypedMergeCleanWorktree(ctx, d.git, source.Path, "source", sourceID); err != nil {
		return nil, err
	}
	if err := requireTypedMergeCleanWorktree(ctx, d.git, targetWorktree, "target", targetID); err != nil {
		return nil, err
	}
	if !branchAttached && !configuredBaseTarget {
		if err := d.git.WithWorktreeLock(ctx, targetWorktree, func(ctx context.Context) error {
			return d.git.Checkout(ctx, targetWorktree, targetBranch)
		}); err != nil {
			return nil, fmt.Errorf("checkout authoritative git.merge target %s: %w", targetBranch, err)
		}
	}

	sourceOID, err := d.git.ResolveCommit(ctx, targetWorktree, source.Branch)
	if err != nil {
		return nil, fmt.Errorf("resolve exact git.merge source %s: %w", source.Branch, err)
	}
	baseOID, err := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
	if err != nil {
		return nil, fmt.Errorf("resolve exact git.merge target %s: %w", targetBranch, err)
	}
	if configuredBaseTarget {
		return d.mergeTypedConfiguredBaseThroughPublication(ctx, projectID, sourceID, source, targetWorktree, targetBranch, sourceOID, baseOID)
	}

	if receipt, found, err := d.latestTaskCloseIntegrationReceipt(ctx, projectID, sourceID, source.Branch); err != nil {
		return nil, err
	} else if found {
		if err := validateTaskCloseIntegrationReceiptIdentity(receipt, projectID, targetID, targetBranch, configuredBaseTarget); err != nil {
			return nil, err
		}
		if receipt.SourceOID == sourceOID && receipt.TargetOID == baseOID {
			if err := verifyTaskCloseIntegrationReceipt(ctx, d.git, targetWorktree, receipt, projectID, source.Branch, targetBranch); err != nil {
				return nil, fmt.Errorf("replay exact git.merge receipt: %w", err)
			}
			return typedMergeResponse(sourceID, targetID, targetWorktree, source.Branch, configuredBaseTarget, receipt.BaseOID, sourceOID, baseOID, true, git.MergeResult{Success: true, Message: "exact integration receipt already applied"}), nil
		}
	}

	contained, err := d.git.CommitContainedInRef(ctx, targetWorktree, sourceOID, targetBranch)
	if err != nil {
		return nil, fmt.Errorf("inspect exact git.merge containment: %w", err)
	}
	if contained {
		integration := taskCloseIntegrationResult{Requested: true, NoChanges: true, ConfiguredBaseTarget: configuredBaseTarget, TargetID: targetID, SourceBranch: source.Branch, TargetBranch: targetBranch, BaseOID: baseOID, SourceOID: sourceOID, TargetOID: baseOID}
		if err := d.persistTaskCloseIntegrationPublication(ctx, projectID, sourceID, source.Path, integration); err != nil {
			return nil, err
		}
		return typedMergeResponse(sourceID, targetID, targetWorktree, source.Branch, configuredBaseTarget, baseOID, sourceOID, baseOID, true, git.MergeResult{Success: true, Message: "source already contained in target"}), nil
	}

	preflight, err := d.git.MergePreflight(ctx, source.Path, targetWorktree, targetBranch, sourceOID)
	if err != nil {
		return nil, fmt.Errorf("git.merge preflight failed: %w", err)
	}
	if preflight != nil && preflight.HasConflicts {
		conflicts := uniqueNonEmpty(preflight.ConflictFiles)
		if len(conflicts) == 0 {
			conflicts = []string{"unknown"}
		}
		return nil, fmt.Errorf("git.merge preflight predicted conflicts: %s", strings.Join(conflicts, ", "))
	}
	if req.StopTargetSession {
		if err := d.stopTypedMergeTargetSession(ctx, projectID, targetID); err != nil {
			return nil, err
		}
	}

	merged, err := d.git.MergeCleanlyTransactionalCompositionAtTarget(ctx, targetWorktree, sourceOID, baseOID, targetBranch)
	if err != nil {
		return nil, fmt.Errorf("merge exact source %s into %s: %w", sourceOID, targetBranch, err)
	}
	if merged == nil || !merged.Success {
		detail := "merge did not complete successfully"
		if merged != nil && strings.TrimSpace(merged.Message) != "" {
			detail = strings.TrimSpace(merged.Message)
		}
		return nil, fmt.Errorf("merge exact source %s into %s failed: %s", sourceOID, targetBranch, detail)
	}
	targetOID, err := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
	if err != nil {
		return nil, fmt.Errorf("resolve exact resulting git.merge target: %w", err)
	}
	integration := taskCloseIntegrationResult{Requested: true, Integrated: true, ConfiguredBaseTarget: configuredBaseTarget, TargetID: targetID, SourceBranch: source.Branch, TargetBranch: targetBranch, BaseOID: baseOID, SourceOID: sourceOID, TargetOID: targetOID, HookDiagnostics: append([]git.GitHookDiagnostic(nil), merged.HookDiagnostics...), ValidationAttempts: append([]domain.IntegrationCandidateValidationAttempt(nil), merged.ValidationAttempts...)}
	if err := d.persistTaskCloseIntegrationPublication(ctx, projectID, sourceID, source.Path, integration); err != nil {
		return nil, err
	}
	return typedMergeResponse(sourceID, targetID, targetWorktree, source.Branch, configuredBaseTarget, baseOID, sourceOID, targetOID, true, *merged), nil
}

func (d *Daemon) stopTypedMergeTargetSession(ctx context.Context, projectID, targetID string) error {
	if d.typedMergeStopTarget != nil {
		return d.typedMergeStopTarget(ctx, projectID, targetID)
	}
	body, err := json.Marshal(sessionCommandBody{ProjectID: projectID, SessionID: targetID})
	if err != nil {
		return fmt.Errorf("encode authoritative target session stop: %w", err)
	}
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("git-merge-stop-target-" + targetID),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	}
	resp, err := d.handleSessionStopDirect(ctx, req)
	if cleanupErr := cleanupCommandError(resp, err); cleanupErr != nil {
		return fmt.Errorf("stop authoritative target session %s before merge: %w", targetID, cleanupErr)
	}
	return nil
}

func (d *Daemon) mergeTypedConfiguredBaseThroughPublication(ctx context.Context, projectID, sourceID string, source git.Worktree, targetWorktree, targetBranch, sourceOID, currentTargetOID string) (*daemonhandlers.GitMergeResult, error) {
	operation, err := d.typedMergeAcceptedPublicationBinding(ctx, projectID, sourceID, targetBranch, sourceOID, currentTargetOID)
	if err != nil {
		return nil, err
	}
	if !operation.State.Terminal() {
		if _, err := d.startAcceptedReviewPublication(ctx, operation); err != nil {
			return nil, fmt.Errorf("start accepted typed merge publication %s: %w", operation.OperationID, err)
		}
	}
	operation, err = d.awaitTypedMergePublication(ctx, projectID, operation.OperationID)
	if err != nil {
		return nil, err
	}
	if operation.State != domain.PublicationOperationMerged {
		detail := strings.TrimSpace(operation.FailureDetail)
		if detail == "" {
			detail = string(operation.State)
		}
		return nil, fmt.Errorf("accepted typed merge publication %s did not merge: %s", operation.OperationID, detail)
	}
	targetOID, err := d.git.ResolveCommit(ctx, targetWorktree, targetBranch)
	if err != nil {
		return nil, fmt.Errorf("resolve completed publication target: %w", err)
	}
	if strings.TrimSpace(operation.CandidateRevision) != strings.TrimSpace(targetOID) {
		return nil, fmt.Errorf("completed publication target changed: operation=%s current=%s", operation.CandidateRevision, targetOID)
	}
	integration, found, err := d.recoverPublishedTaskCloseIntegration(ctx, projectID, sourceID, targetWorktree, "base", source.Branch, targetBranch, sourceOID, targetOID)
	if err != nil {
		return nil, err
	}
	if !found || strings.TrimSpace(integration.PublicationOperationID) != strings.TrimSpace(operation.OperationID) {
		return nil, fmt.Errorf("completed publication %s is missing its exact integration receipt", operation.OperationID)
	}
	if err := d.persistTaskCloseIntegrationPublication(ctx, projectID, sourceID, source.Path, integration); err != nil {
		return nil, fmt.Errorf("converge completed publication evidence: %w", err)
	}
	result := git.MergeResult{Success: true, Message: "accepted publication queue applied exact candidate", ValidationAttempts: append([]domain.IntegrationCandidateValidationAttempt(nil), integration.ValidationAttempts...)}
	return typedMergeResponse(sourceID, "base", targetWorktree, source.Branch, true, integration.BaseOID, integration.SourceOID, integration.TargetOID, true, result), nil
}

func (d *Daemon) typedMergeAcceptedPublicationBinding(ctx context.Context, projectID, sourceID, targetBranch, sourceOID, currentTargetOID string) (domain.PublicationOperation, error) {
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.PublicationOperation{}, fmt.Errorf("issue store unavailable")
	}
	store, err := d.publicationStoreForProject(projectID)
	if err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("typed configured-base merge requires accepted publication binding: %w", err)
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, sourceID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}, NewestFirst: true})
	if err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("read accepted publication binding: %w", err)
	}
	for _, event := range events {
		outcome, trusted := domain.TrustedReviewOutcome(event)
		if !trusted {
			continue
		}
		if outcome != domain.ReviewOutcomeAccepted {
			return domain.PublicationOperation{}, fmt.Errorf("latest authoritative review outcome for %s is %s, not accepted", sourceID, outcome)
		}
		operationID := observationPayloadString(event.Payload, "publication_operation_id")
		if operationID == "" {
			return domain.PublicationOperation{}, fmt.Errorf("latest accepted review for %s has no publication operation binding", sourceID)
		}
		if observationPayloadString(event.Payload, "reviewed_source_oid") != strings.TrimSpace(sourceOID) {
			return domain.PublicationOperation{}, fmt.Errorf("latest accepted review source changed for %s", sourceID)
		}
		operation, found, readErr := store.PublicationOperation(ctx, operationID)
		if readErr != nil {
			return domain.PublicationOperation{}, fmt.Errorf("read accepted publication operation %s: %w", operationID, readErr)
		}
		if !found {
			return domain.PublicationOperation{}, fmt.Errorf("accepted publication operation %s is absent", operationID)
		}
		exactIdentity := protocol.NormalizeProjectID(operation.ProjectID) == protocol.NormalizeProjectID(projectID) && naming.IssueIDsEqual(operation.IssueID, sourceID) && strings.EqualFold(strings.TrimSpace(operation.TargetID), "base") && strings.TrimSpace(operation.TargetBranch) == strings.TrimSpace(targetBranch) && strings.TrimSpace(operation.SourceRevision) == strings.TrimSpace(sourceOID)
		if !exactIdentity {
			return domain.PublicationOperation{}, fmt.Errorf("accepted publication operation %s has stale source or target identity", operationID)
		}
		if operation.State == domain.PublicationOperationMerged {
			if strings.TrimSpace(operation.CandidateRevision) != strings.TrimSpace(currentTargetOID) {
				return domain.PublicationOperation{}, fmt.Errorf("completed publication operation %s target changed", operationID)
			}
		} else if strings.TrimSpace(operation.BaseRevision) != strings.TrimSpace(currentTargetOID) {
			return domain.PublicationOperation{}, fmt.Errorf("accepted publication operation %s base is stale: accepted=%s current=%s", operationID, operation.BaseRevision, currentTargetOID)
		}
		return operation, nil
	}
	return domain.PublicationOperation{}, fmt.Errorf("typed configured-base merge requires an exact accepted-review publication binding for %s at %s", sourceID, sourceOID)
}

func (d *Daemon) awaitTypedMergePublication(ctx context.Context, projectID, operationID string) (domain.PublicationOperation, error) {
	store, err := d.publicationStoreForProject(projectID)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	for {
		operation, found, err := store.PublicationOperation(ctx, operationID)
		if err != nil {
			return domain.PublicationOperation{}, err
		}
		if !found {
			return domain.PublicationOperation{}, fmt.Errorf("publication operation %s disappeared", operationID)
		}
		if operation.State.Terminal() {
			return operation, nil
		}
		if d.typedMergePublicationWait != nil {
			if err := d.typedMergePublicationWait(ctx, operationID); err != nil {
				return domain.PublicationOperation{}, err
			}
			continue
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.PublicationOperation{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func typedMergeResponse(sourceID, targetID, worktree, branch string, configuredBase bool, baseOID, sourceOID, targetOID string, receipt bool, result git.MergeResult) *daemonhandlers.GitMergeResult {
	return &daemonhandlers.GitMergeResult{Worktree: worktree, Branch: branch, SourceID: sourceID, TargetID: targetID, ConfiguredBaseTarget: configuredBase, BaseOID: baseOID, SourceOID: sourceOID, TargetOID: targetOID, ReceiptRecorded: receipt, Result: result}
}

func requireTypedMergeCleanWorktree(ctx context.Context, client *git.Client, worktree, role, issueID string) error {
	status, err := client.Status(ctx, worktree)
	if err != nil {
		return fmt.Errorf("read authoritative %s worktree status for %s: %w", role, issueID, err)
	}
	if gitStatusHasDirtyFiles(status) {
		return fmt.Errorf("%s worktree for %s is dirty: %s", role, issueID, gitStatusSummaryWithDetails(status))
	}
	return nil
}

func taskByCanonicalIssueID(tasks map[string]domain.Task, issueID string) (domain.Task, bool) {
	for id, task := range tasks {
		if naming.IssueIDsEqual(id, issueID) {
			return task, true
		}
	}
	return domain.Task{}, false
}

func typedMergeIssuesAreAncestorRelated(sourceID, targetID string, tasks map[string]domain.Task) bool {
	return typedMergeIssueIsAncestor(sourceID, targetID, tasks) || typedMergeIssueIsAncestor(targetID, sourceID, tasks)
}

func typedMergeIssueIsAncestor(ancestorID, descendantID string, tasks map[string]domain.Task) bool {
	seen := make(map[string]struct{}, len(tasks))
	current, ok := taskByCanonicalIssueID(tasks, descendantID)
	for ok {
		parentID := domain.TaskParentIssueID(current)
		if parentID == "" {
			return false
		}
		if naming.IssueIDsEqual(parentID, ancestorID) {
			return true
		}
		if _, duplicate := seen[parentID]; duplicate {
			return false
		}
		seen[parentID] = struct{}{}
		current, ok = taskByCanonicalIssueID(tasks, parentID)
	}
	return false
}
