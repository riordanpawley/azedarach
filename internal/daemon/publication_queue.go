package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const (
	publicationApplyOperationKind = "publication.apply"
	defaultPublicationClaimTTL    = 5 * time.Minute
)

func publicationClaimToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate publication claim token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (d *Daemon) publicationClaimIdentity() (string, time.Time, time.Duration, error) {
	d.publicationClaimMu.Lock()
	defer d.publicationClaimMu.Unlock()
	if d.publicationClaimOwner == "" {
		token, err := publicationClaimToken()
		if err != nil {
			return "", time.Time{}, 0, err
		}
		d.publicationClaimOwner = "daemon-" + token
	}
	now := time.Now().UTC()
	if d.publicationClaimNow != nil {
		now = d.publicationClaimNow().UTC()
	}
	ttl := d.publicationClaimTTL
	if ttl <= 0 {
		ttl = defaultPublicationClaimTTL
	}
	return d.publicationClaimOwner, now, ttl, nil
}

type publicationRetryError struct {
	cause       error
	replacement domain.PublicationOperation
}

func (e *publicationRetryError) Error() string { return e.cause.Error() }
func (e *publicationRetryError) Unwrap() error { return e.cause }

type taskClosePublicationBinding struct {
	operationID string
	claimToken  string
}

type taskClosePublicationBindingContextKey struct{}

type taskCloseAppliedPublicationRecoveryContextKey struct{}

func withTaskClosePublicationBinding(ctx context.Context, operationID, claimToken string) context.Context {
	return context.WithValue(ctx, taskClosePublicationBindingContextKey{}, taskClosePublicationBinding{
		operationID: strings.TrimSpace(operationID),
		claimToken:  strings.TrimSpace(claimToken),
	})
}

func taskClosePublicationBindingFromContext(ctx context.Context) (taskClosePublicationBinding, bool) {
	if ctx == nil {
		return taskClosePublicationBinding{}, false
	}
	binding, ok := ctx.Value(taskClosePublicationBindingContextKey{}).(taskClosePublicationBinding)
	return binding, ok && binding.operationID != "" && binding.claimToken != ""
}

func withTaskCloseAppliedPublicationRecovery(ctx context.Context, operation domain.PublicationOperation) context.Context {
	return context.WithValue(ctx, taskCloseAppliedPublicationRecoveryContextKey{}, operation)
}

func taskCloseAppliedPublicationRecoveryFromContext(ctx context.Context) (domain.PublicationOperation, bool) {
	if ctx == nil {
		return domain.PublicationOperation{}, false
	}
	operation, ok := ctx.Value(taskCloseAppliedPublicationRecoveryContextKey{}).(domain.PublicationOperation)
	return operation, ok && strings.TrimSpace(operation.OperationID) != ""
}

func publicationOperationIdentity(projectID, issueID, intentKey string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{projectID, issueID, intentKey}, "\x00")))
	return fmt.Sprintf("publication-%x", digest[:12])
}

func publicationCoalesceKey(operation domain.PublicationOperation) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		operation.IssueID, operation.IntentKey, operation.SourceRevision, operation.BaseRevision, operation.TargetBranch,
		operation.PolicyVersion, operation.EnvironmentFingerprint, operation.EvidenceDigest,
		operation.ValidationCommand,
	}, "\x00")))
	return fmt.Sprintf("candidate-%x", digest[:16])
}

func publicationValidationRequestIdentity(operationID, candidateRevision, claimToken string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{operationID, candidateRevision, claimToken}, "\x00")))
	return fmt.Sprintf("publication-validation-%x", digest[:16])
}

func publicationValidationLeaseToken(operationID, candidateRevision, claimToken string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{"publication-lease", operationID, candidateRevision, claimToken}, "\x00")))
	return fmt.Sprintf("%x", digest[:])
}

func (d *Daemon) awaitPriorPublicationValidation(ctx context.Context, projectID, requestID string) error {
	for {
		if _, err := d.operationRuntime.store.ValidationSnapshot(ctx, projectID, time.Now().UTC(), defaultValidationLeaseTTL); err != nil {
			return err
		}
		validation, err := d.operationRuntime.store.ValidationRequest(ctx, projectID, requestID)
		if err != nil {
			return err
		}
		switch validation.State {
		case domain.ValidationRequestCompleted, domain.ValidationRequestFailed, domain.ValidationRequestCancelled, domain.ValidationRequestExpired:
			return nil
		}
		if d.publicationValidationWait != nil {
			if err := d.publicationValidationWait(ctx, requestID); err != nil {
				return err
			}
			continue
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *Daemon) publicationStoreForProject(projectID string) (*operationstore.SQLiteStore, error) {
	projectID = d.canonicalProjectID(projectID)
	if d.operationRuntime == nil || d.operationRuntime.store == nil {
		return nil, fmt.Errorf("publication store unavailable")
	}
	if projectID == d.operationRuntime.canonicalProject {
		return d.operationRuntime.store, nil
	}
	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if repoDir == "" {
		// Unit/in-process adapters may inject a project-scoped issue client
		// without a registry-backed root. They cannot exercise cross-store
		// publication, but may still read the empty runtime projection. Real
		// routed projects always resolve an exact root and use their own DB.
		d.issueClientsMu.Lock()
		_, injectedProject := d.issueClientsByProject[projectID]
		d.issueClientsMu.Unlock()
		if injectedProject {
			return d.operationRuntime.store, nil
		}
		return nil, fmt.Errorf("publication project root unavailable for %s", projectID)
	}
	d.publicationStoresMu.Lock()
	defer d.publicationStoresMu.Unlock()
	if d.publicationStores == nil {
		d.publicationStores = make(map[string]*operationstore.SQLiteStore)
	}
	storeKey := projectID + "\x00" + filepath.Clean(repoDir)
	if store := d.publicationStores[storeKey]; store != nil {
		return store, nil
	}
	store := operationstore.New(repoDir, d.cfg.Logger)
	d.publicationStores[storeKey] = store
	return store, nil
}

func (d *Daemon) closePublicationStores() {
	d.publicationContinuationMu.Lock()
	d.publicationContinuationStopping = true
	if d.publicationContinuationCancel != nil {
		d.publicationContinuationCancel()
	}
	d.publicationContinuationCtx = nil
	d.publicationContinuationCancel = nil
	d.publicationContinuationMu.Unlock()
	d.publicationContinuationWG.Wait()
	d.publicationContinuationMu.Lock()
	d.publicationContinuationRetrying = nil
	d.publicationContinuationMu.Unlock()
	d.publicationStoresMu.Lock()
	stores := d.publicationStores
	d.publicationStores = nil
	d.publicationStoresMu.Unlock()
	for storeKey, store := range stores {
		if store == nil {
			continue
		}
		if err := store.Close(); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("failed to close publication project store", "store_key", storeKey, "error", err)
		}
	}
}

func (d *Daemon) enqueueAcceptedReviewPublication(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string, pin acceptedReviewPin) (domain.PublicationOperation, error) {
	operation, err := d.prepareAcceptedReviewPublication(ctx, projectID, request, issueID, pin)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	store, err := d.publicationStoreForProject(projectID)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	stored, _, err := store.EnqueuePublication(ctx, operation, publicationCoalesceKey(operation))
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	return d.startAcceptedReviewPublication(ctx, stored)
}

func (d *Daemon) acceptAndEnqueueReviewPublication(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string, pin acceptedReviewPin, inspection protocol.OrchestrationReview) (domain.PublicationOperation, error) {
	reviewer, err := domain.CanonicalReviewerIdentity(request.ActorID, domain.ReviewerOwnerKindOrchestrator)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	pin.Reviewer = reviewer
	pin.ReviewEpochEventID = inspection.ReviewEpochEventID
	operation, err := d.prepareAcceptedReviewPublication(ctx, projectID, request, issueID, pin)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	patchEvidence, err := d.acceptedPatchReviewEvidence(ctx, projectID, request.ActorID, inspection)
	if err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("prepare accepted patch-review evidence: %w", err)
	}
	if strings.TrimSpace(patchEvidence.EvidenceID) == "" {
		return domain.PublicationOperation{}, fmt.Errorf("publication evidence capability absent: configure publicationEvidence.policyVersion for accepted patch authority")
	}
	operation.ReviewerKind = "orchestrator"
	operation.ReviewEpochEventID = inspection.ReviewEpochEventID
	operation.PatchEvidenceID = patchEvidence.EvidenceID
	admission, err := reviewAdmissionPinFromInspection(inspection)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return domain.PublicationOperation{}, fmt.Errorf("issue store unavailable")
	}
	store, err := d.publicationStoreForProject(projectID)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	// Establish the independently versioned queue schema before entering the
	// cross-authority acceptance transaction on this project's database.
	if _, err := store.PublicationOperations(ctx, projectID, "", false); err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("initialize publication queue projection: %w", err)
	}
	payload := map[string]any{
		"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": reviewer.OwnerID, "actor_kind": reviewer.OwnerKind,
		"intent_key": request.IntentKey, "request_fingerprint": reviewRequestFingerprint(request), "findings": request.Findings,
		"reviewed_source_oid": pin.SourceOID, "reviewed_evidence_source": pin.EvidenceSource,
		"reviewed_evidence_event_id": pin.EvidenceEventID, "reviewed_evidence_seq": pin.EvidenceSeq,
		"reviewed_evidence_digest": pin.EvidenceDigest, "publication_operation_id": operation.OperationID,
		"review_epoch_event_id": inspection.ReviewEpochEventID, "review_parent_issue_id": strings.TrimSpace(inspection.ParentIssueID),
		"review_source_oid": strings.TrimSpace(inspection.SourceOID), "review_evidence_source": strings.TrimSpace(inspection.EvidenceSource),
		"review_evidence_event_id": inspection.EvidenceEventID, "review_evidence_seq": inspection.EvidenceSeq,
		"review_evidence_digest": strings.TrimSpace(inspection.EvidenceDigest),
	}
	receipt, err := issueClient.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: string(request.Kind), Payload: payload,
	}, operation, patchEvidence, publicationCoalesceKey(operation), admission, inspection.ParentIssueID, request.ActorID)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	operation.OperationID = receipt.PublicationOperationID
	operation.AcceptedReviewEventID = receipt.EventID
	read := func(_ string, readCtx context.Context, projectID, operationID string) (domain.PublicationOperation, bool, error) {
		return store.PublicationOperation(readCtx, operationID)
	}
	if d.publicationCommittedQueueRead != nil {
		read = d.publicationCommittedQueueRead
	}
	stored, found, err := read("committed-queue-read", ctx, projectID, receipt.PublicationOperationID)
	if err != nil || !found {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("accepted review publication is durable but queue refresh failed; scheduling retry", "operation_id", receipt.PublicationOperationID, "found", found, "error", err)
		}
		d.schedulePublicationRecoveryRetry(d.publicationContinuationContext(), operation)
		return operation, nil
	}
	return d.startAcceptedReviewPublication(ctx, stored)
}

func (d *Daemon) prepareAcceptedReviewPublication(ctx context.Context, projectID string, request protocol.OrchestrationIntentRequest, issueID string, pin acceptedReviewPin) (domain.PublicationOperation, error) {
	if d.operationRuntime == nil || d.operationRuntime.store == nil || d.operationRuntime.manager == nil {
		return domain.PublicationOperation{}, fmt.Errorf("publication queue unavailable")
	}
	target, err := d.taskMergeBaseTarget(ctx, projectID, issueID, d.baseBranchForProject(projectID), false)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(target.TargetID), "base") {
		return domain.PublicationOperation{}, fmt.Errorf("publication queue requires configured base target, got %s", target.TargetID)
	}
	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(projectID))
	if repoDir == "" {
		return domain.PublicationOperation{}, fmt.Errorf("publication target repository is unavailable")
	}
	baseRevision, err := d.git.ResolveCommit(ctx, repoDir, target.Branch)
	if err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("resolve publication base revision: %w", err)
	}
	projectCfg, err := appconfig.LoadConfig(repoDir)
	if err != nil {
		return domain.PublicationOperation{}, fmt.Errorf("load publication capability: %w", err)
	}
	gateCommand := publicationValidationIdentity(projectCfg)
	if gateCommand == "" {
		return domain.PublicationOperation{}, fmt.Errorf("publication capability absent: configure gate.command or gate.stages for exact synthetic-candidate validation")
	}
	policyVersion := publicationPolicyVersion(projectCfg, gateCommand)
	reviewer := pin.Reviewer
	if reviewer.OwnerID == "" {
		reviewer, err = domain.CanonicalReviewerIdentity(request.ActorID, domain.ReviewerOwnerKindOrchestrator)
		if err != nil {
			return domain.PublicationOperation{}, err
		}
	}
	operation := domain.PublicationOperation{
		OperationID: publicationOperationIdentity(projectID, issueID, request.IntentKey), ProjectID: projectID,
		IssueID: issueID, IntentKey: request.IntentKey, RequestFingerprint: reviewRequestFingerprint(request),
		ActorID: reviewer.OwnerID, ReviewerKind: reviewer.OwnerKind, ReviewEpochEventID: pin.ReviewEpochEventID, AcceptedReviewEventID: pin.AcceptedReviewEventID,
		PatchEvidenceID: publicationOperationIdentity(projectID, issueID, request.IntentKey), TargetID: "base", TargetBranch: target.Branch,
		SourceRevision: pin.SourceOID, BaseRevision: strings.TrimSpace(baseRevision), PolicyVersion: policyVersion,
		EnvironmentFingerprint: publicationEnvironmentFingerprint(projectCfg), ValidationCommand: gateCommand, EvidenceSource: pin.EvidenceSource, EvidenceEventID: pin.EvidenceEventID,
		EvidenceSeq: pin.EvidenceSeq, EvidenceDigest: pin.EvidenceDigest, State: domain.PublicationOperationQueued,
		CreatedAt: time.Now().UTC(),
	}
	return operation, nil
}

func (d *Daemon) startAcceptedReviewPublication(ctx context.Context, stored domain.PublicationOperation) (domain.PublicationOperation, error) {
	submit := d.submitPublicationOperation
	if d.publicationInitialSubmit != nil {
		submit = d.publicationInitialSubmit
	}
	if err := submit(ctx, stored); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("accepted review publication is durable but initial submit failed; scheduling retry", "operation_id", stored.OperationID, "error", err)
		}
		d.schedulePublicationRecoveryRetry(d.publicationContinuationContext(), stored)
		// Acceptance and queue ownership committed atomically before submission.
		// Report that durable state as success so the review loop retains its
		// lease and evidence/parent fences while immediate or restart recovery
		// retries the idempotent operation.
		return stored, nil
	}
	store, storeErr := d.publicationStoreForProject(stored.ProjectID)
	if storeErr != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("accepted review publication submit succeeded but queue store refresh failed; scheduling reconciliation", "operation_id", stored.OperationID, "error", storeErr)
		}
		d.schedulePublicationRecoveryRetry(d.publicationContinuationContext(), stored)
		return stored, nil
	}
	read := func(_ string, readCtx context.Context, _, operationID string) (domain.PublicationOperation, bool, error) {
		return store.PublicationOperation(readCtx, operationID)
	}
	if d.publicationCommittedQueueRead != nil {
		read = d.publicationCommittedQueueRead
	}
	fresh, found, err := read("post-submit-refresh", ctx, stored.ProjectID, stored.OperationID)
	if err == nil && found {
		stored = fresh
	} else {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("accepted review publication submit succeeded but queue refresh failed; scheduling reconciliation", "operation_id", stored.OperationID, "found", found, "error", err)
		}
		d.schedulePublicationRecoveryRetry(d.publicationContinuationContext(), stored)
	}
	return stored, nil
}

func (d *Daemon) submitPublicationOperation(ctx context.Context, operation domain.PublicationOperation) error {
	if d.operationRuntime == nil || d.operationRuntime.manager == nil {
		return fmt.Errorf("publication operation manager unavailable")
	}
	_, err := d.operationRuntime.manager.Submit(ctx, daemonops.SubmitRequest{
		ProjectID: operation.ProjectID, IssueID: operation.IssueID, Kind: publicationApplyOperationKind,
		DedupeKey:          "publication:" + operation.OperationID,
		ResourceKeys:       []string{"publication-target:" + operation.ProjectID + ":" + operation.TargetBranch},
		RecentDedupeWindow: 0,
	}, func(runCtx context.Context) ([]byte, error) {
		return d.runPublicationOperation(runCtx, operation.ProjectID, operation.OperationID)
	})
	if err != nil {
		return fmt.Errorf("submit publication operation: %w", err)
	}
	return nil
}

func (d *Daemon) claimPublicationOperation(ctx context.Context, store *operationstore.SQLiteStore, operationID string) (domain.PublicationOperation, bool, string, time.Duration, error) {
	owner, now, ttl, err := d.publicationClaimIdentity()
	if err != nil {
		return domain.PublicationOperation{}, false, "", 0, err
	}
	token, err := publicationClaimToken()
	if err != nil {
		return domain.PublicationOperation{}, false, "", 0, err
	}
	operation, acquired, err := store.ClaimPublicationOperation(ctx, operationID, operationstore.PublicationOperationClaim{
		Owner: owner, Token: token, Now: now, TTL: ttl,
	})
	if err != nil || !acquired {
		return operation, acquired, token, ttl, err
	}
	d.publishPublicationOperationEvent(operation)
	if d.publicationStateChanged != nil {
		d.publicationStateChanged(operation)
	}
	return operation, true, token, ttl, nil
}

func (d *Daemon) startPublicationClaimHeartbeat(store *operationstore.SQLiteStore, operationID, claimToken string, ttl time.Duration) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, now, _, err := d.publicationClaimIdentity()
				if err == nil {
					_, _ = store.RenewPublicationOperationClaim(context.Background(), operationID, claimToken, now, ttl)
				}
			}
		}
	}()
	return cancel
}

func (d *Daemon) renewPublicationClaim(ctx context.Context, store *operationstore.SQLiteStore, operationID, claimToken string, ttl time.Duration) (domain.PublicationOperation, error) {
	_, now, _, err := d.publicationClaimIdentity()
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	return store.RenewPublicationOperationClaim(ctx, operationID, claimToken, now, ttl)
}

func (d *Daemon) continuePublicationTarget(ctx context.Context, store *operationstore.SQLiteStore, terminal domain.PublicationOperation) error {
	operations, err := store.PublicationOperations(ctx, terminal.ProjectID, "", true)
	if err != nil {
		return fmt.Errorf("list publication target continuation: %w", err)
	}
	for _, operation := range operations {
		if operation.TargetBranch == terminal.TargetBranch {
			if d.publicationContinuationSubmit != nil {
				return d.publicationContinuationSubmit(ctx, operation)
			}
			return d.submitPublicationOperation(ctx, operation)
		}
	}
	return nil
}

func (d *Daemon) publicationContinuationContext() context.Context {
	d.publicationContinuationMu.Lock()
	defer d.publicationContinuationMu.Unlock()
	if d.publicationContinuationStopping {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	if d.publicationContinuationCtx == nil {
		d.publicationContinuationCtx, d.publicationContinuationCancel = context.WithCancel(context.Background())
	}
	return d.publicationContinuationCtx
}

func (d *Daemon) ensurePublicationTargetContinuation(store *operationstore.SQLiteStore, terminal domain.PublicationOperation) {
	ctx := d.publicationContinuationContext()
	if err := d.continuePublicationTarget(ctx, store, terminal); err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("publication target continuation submit failed; scheduling retry", "operation_id", terminal.OperationID, "error", err)
		}
		d.schedulePublicationTargetContinuationRetry(ctx, store, terminal)
	}
}

func (d *Daemon) schedulePublicationTargetContinuationRetry(ctx context.Context, store *operationstore.SQLiteStore, terminal domain.PublicationOperation) {
	key := "target\x00" + terminal.ProjectID + "\x00" + terminal.TargetBranch
	d.publicationContinuationMu.Lock()
	if d.publicationContinuationStopping {
		d.publicationContinuationMu.Unlock()
		return
	}
	if d.publicationContinuationRetrying == nil {
		d.publicationContinuationRetrying = make(map[string]bool)
	}
	if d.publicationContinuationRetrying[key] {
		d.publicationContinuationMu.Unlock()
		return
	}
	d.publicationContinuationRetrying[key] = true
	d.publicationContinuationWG.Add(1)
	d.publicationContinuationMu.Unlock()
	go func() {
		defer func() {
			d.publicationContinuationMu.Lock()
			delete(d.publicationContinuationRetrying, key)
			d.publicationContinuationMu.Unlock()
			d.publicationContinuationWG.Done()
		}()
		for {
			if d.publicationContinuationWait != nil {
				if err := d.publicationContinuationWait(ctx); err != nil {
					if ctx.Err() != nil {
						return
					}
					continue
				}
			} else {
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if err := d.continuePublicationTarget(ctx, store, terminal); err == nil {
				return
			} else if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("publication target continuation retry failed", "operation_id", terminal.OperationID, "error", err)
			}
		}
	}()
}

func (d *Daemon) runPublicationOperation(ctx context.Context, projectID, operationID string) ([]byte, error) {
	store, err := d.publicationStoreForProject(projectID)
	if err != nil {
		return nil, err
	}
	operation, acquired, claimToken, claimTTL, err := d.claimPublicationOperation(context.Background(), store, operationID)
	if err != nil {
		return nil, fmt.Errorf("claim publication operation %s: %w", operationID, err)
	}
	if !acquired {
		return json.Marshal(operation)
	}
	stopClaimHeartbeat := d.startPublicationClaimHeartbeat(store, operationID, claimToken, claimTTL)
	defer stopClaimHeartbeat()
	var reviewAuthorityErr error
	// Test-only execution hooks replace the durable close/identity boundaries;
	// production and active-path tests always validate the persisted authority.
	if d.publicationClose == nil && d.publicationIdentityCheck == nil {
		reviewAuthorityErr = d.validatePublicationReviewAuthority(ctx, operation)
	}
	ticketID, ticketIdentityErr := naming.ParseTicketID(operation.IssueID)
	identityCheck := d.publicationIdentityCheck
	appliedRecovery := false
	if identityCheck == nil && d.publicationClose == nil {
		appliedRecovery, err = d.publicationOperationAlreadyApplied(ctx, operation)
		if err != nil {
			return nil, fmt.Errorf("inspect exact applied publication recovery: %w", err)
		}
		if !appliedRecovery {
			identityCheck = d.validatePublicationOperationIdentity
		}
	}
	if reviewAuthorityErr != nil || identityCheck != nil || ticketIdentityErr != nil {
		identityErr := reviewAuthorityErr
		if identityErr != nil {
			identityErr = fmt.Errorf("bind accepted-review publication authority: %w", identityErr)
		} else {
			identityErr = ticketIdentityErr
		}
		if ticketIdentityErr != nil {
			identityErr = fmt.Errorf("bind accepted-review publication ticket identity: %w", identityErr)
		} else if identityErr == nil {
			identityErr = identityCheck(ctx, operation)
		}
		if identityErr != nil {
			finished := time.Now().UTC()
			state, kind := classifyPublicationFailure(identityErr)
			artifact := d.writePublicationFailureArtifact(operation, state, identityErr)
			mutate := func(update *operationstore.PublicationOperationUpdate) {
				update.FailureKind, update.FailureDetail, update.FailureArtifact = kind, identityErr.Error(), artifact
				update.ReleaseClaim = true
				update.FinishedAt = &finished
			}
			var retry *publicationRetryError
			var terminal domain.PublicationOperation
			var transitionErr error
			if errors.As(identityErr, &retry) {
				terminal, _, transitionErr = d.transitionPublicationWithRetry(context.Background(), store, operation, claimToken, state, mutate, retry)
			} else {
				terminal, transitionErr = d.terminalizeAcceptedReviewPublication(context.Background(), operation, claimToken, state, kind, identityErr.Error(), artifact, finished)
			}
			if transitionErr != nil {
				return nil, fmt.Errorf("%w; persist terminal publication continuation: %v", identityErr, transitionErr)
			}
			d.ensurePublicationTargetContinuation(store, terminal)
			return nil, identityErr
		}
	}
	_ = daemonops.ReportProgress(ctx, daemonops.Progress{Phase: "preparing", Message: "preparing exact synthetic merge candidate", Current: 20, Total: 100, Unit: "percent", Percent: 20})

	ctx = git.WithCandidateValidationObserver(ctx, func(attempt git.CandidateValidationAttempt) {
		state := domain.PublicationOperationValidating
		if attempt.Status == git.CandidateValidationPassed {
			state = domain.PublicationOperationPassed
		}
		current, found, readErr := store.PublicationOperation(context.Background(), operationID)
		if readErr == nil && found && !current.State.Terminal() {
			_, _ = d.transitionPublicationOperation(context.Background(), current, claimToken, state, func(update *operationstore.PublicationOperationUpdate) {
				update.CandidateRevision = attempt.CandidateHead
			})
		}
		phase := "validating"
		percent := int64(65)
		if state == domain.PublicationOperationPassed {
			phase, percent = "passed", 85
		}
		_ = daemonops.ReportProgress(ctx, daemonops.Progress{Phase: phase, Message: strings.TrimSpace(attempt.Message), Current: percent, Total: 100, Unit: "percent", Percent: int(percent)})
	})
	ctx = git.WithCandidateValidationCommand(ctx, operation.ValidationCommand)
	if projectCfg, configErr := appconfig.LoadConfig(d.resolveRepoDirForProjectExact(operation.ProjectID)); configErr != nil {
		return nil, fmt.Errorf("load publication validation stages: %w", configErr)
	} else if len(projectCfg.Gate.Stages) > 0 {
		stages := publicationValidationStages(projectCfg)
		ctx = git.WithCandidateValidationDAG(ctx, stages)
		artifactPaths := append([]string(nil), d.runtimeConfigForProject(operation.ProjectID).GateFailureArtifactPaths...)
		for _, stage := range stages {
			artifactPaths = append(artifactPaths, stage.ArtifactPaths...)
		}
		ctx = git.WithIntegrationFailureArtifactPaths(ctx, artifactPaths)
	}
	ctx = git.WithCandidateValidationTicket(ctx, ticketID)
	ctx = git.WithCandidateValidationReviewAuthority(ctx, git.CandidateValidationReviewAuthority{
		ReviewerID: operation.ActorID, ReviewerKind: operation.ReviewerKind,
		ReviewEpochEventID: operation.ReviewEpochEventID, PublicationOperationID: operation.OperationID,
		AcceptedReviewEventID: operation.AcceptedReviewEventID, AcceptedPublicationOperationID: operation.OperationID,
	})
	ctx = git.WithCandidateValidationAdmission(ctx, d.publicationCandidateAdmission(operation.ProjectID, operationID, claimToken, claimTTL))

	request := protocol.OrchestrationIntentRequest{
		Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: operation.IntentKey, ActorID: operation.ActorID,
		IssueIDs: []string{operation.IssueID}, RepoDir: d.resolveRepoDirForProjectExact(operation.ProjectID),
	}
	pin := acceptedReviewPin{Reviewer: domain.ReviewerIdentity{OwnerID: operation.ActorID, OwnerKind: operation.ReviewerKind}, ReviewEpochEventID: operation.ReviewEpochEventID, AcceptedReviewEventID: operation.AcceptedReviewEventID, AcceptedPublicationOperationID: operation.OperationID, SourceOID: operation.SourceRevision, EvidenceSource: operation.EvidenceSource, EvidenceEventID: operation.EvidenceEventID, EvidenceSeq: operation.EvidenceSeq, EvidenceDigest: operation.EvidenceDigest}
	result := protocol.OrchestrationIntentResult{Scope: request.Scope, Kind: request.Kind, IntentKey: request.IntentKey, Requested: []string{operation.IssueID}}
	operation, err = d.renewPublicationClaim(context.Background(), store, operationID, claimToken, claimTTL)
	if err != nil {
		return nil, fmt.Errorf("verify publication claim before apply: %w", err)
	}
	var runErr error
	ctx = withTaskClosePublicationBinding(ctx, operation.OperationID, claimToken)
	if appliedRecovery {
		ctx = withTaskCloseAppliedPublicationRecovery(ctx, operation)
	}
	if d.publicationClose != nil {
		runErr = d.publicationClose(ctx, operation)
	} else {
		_, runErr = (daemonOrchestrationAuthority{daemon: d}).releaseAndCloseAcceptedReview(ctx, operation.ProjectID, request, operation.IssueID, true, pin, operation.BaseRevision, &result)
	}
	finished := time.Now().UTC()
	current, found, readErr := store.PublicationOperation(context.Background(), operationID)
	if readErr != nil || !found {
		return nil, fmt.Errorf("reload publication operation after apply: %w", readErr)
	}
	if runErr != nil {
		if applied, appliedErr := d.publicationOperationAlreadyApplied(context.Background(), current); appliedErr == nil && applied {
			recoverable, transitionErr := d.transitionPublicationOperation(context.Background(), current, claimToken, domain.PublicationOperationPassed, func(update *operationstore.PublicationOperationUpdate) {
				update.FailureKind = "post_apply_persistence_failed"
				update.FailureDetail = runErr.Error()
				update.ReleaseClaim = true
			})
			if transitionErr != nil {
				return nil, fmt.Errorf("%w; preserve recoverable applied publication: %v", runErr, transitionErr)
			}
			d.schedulePublicationRecoveryRetry(context.Background(), recoverable)
			return nil, runErr
		}
		var baseStale *taskCloseExpectedBaseStaleError
		if errors.As(runErr, &baseStale) {
			replacement := refreshedPublicationOperationAttempt(operation, baseStale.Actual, operation.ValidationCommand, operation.PolicyVersion, operation.EnvironmentFingerprint)
			runErr = &publicationRetryError{cause: runErr, replacement: replacement}
		}
		state, kind := classifyPublicationFailure(runErr)
		artifact := d.writePublicationFailureArtifact(operation, state, runErr)
		mutate := func(update *operationstore.PublicationOperationUpdate) {
			update.FailureKind, update.FailureDetail, update.FailureArtifact = kind, runErr.Error(), artifact
			update.ReleaseClaim = true
			update.FinishedAt = &finished
		}
		var retry *publicationRetryError
		var terminal domain.PublicationOperation
		var transitionErr error
		if errors.As(runErr, &retry) {
			terminal, _, transitionErr = d.transitionPublicationWithRetry(context.Background(), store, current, claimToken, state, mutate, retry)
		} else {
			terminal, transitionErr = d.terminalizeAcceptedReviewPublication(context.Background(), current, claimToken, state, kind, runErr.Error(), artifact, finished)
		}
		if transitionErr != nil {
			return nil, fmt.Errorf("%w; persist terminal publication continuation: %v", runErr, transitionErr)
		}
		d.ensurePublicationTargetContinuation(store, terminal)
		return nil, runErr
	}
	current, err = d.transitionPublicationOperation(context.Background(), current, claimToken, domain.PublicationOperationMerged, func(update *operationstore.PublicationOperationUpdate) {
		update.ReleaseClaim = true
		update.FinishedAt = &finished
	})
	if err != nil {
		return nil, err
	}
	d.ensurePublicationTargetContinuation(store, current)
	_ = daemonops.ReportProgress(ctx, daemonops.Progress{Phase: "merged", Message: "exact candidate merged and accepted issue closed", Current: 100, Total: 100, Unit: "percent", Percent: 100})
	return json.Marshal(current)
}

func (d *Daemon) terminalizeAcceptedReviewPublication(ctx context.Context, current domain.PublicationOperation, claimToken string, state domain.PublicationOperationState, failureKind, failureDetail, failureArtifact string, finished time.Time) (domain.PublicationOperation, error) {
	issueClient := d.issueClientForProject(current.ProjectID)
	if issueClient == nil {
		return domain.PublicationOperation{}, fmt.Errorf("issue store unavailable for terminal publication disposition")
	}
	terminal, err := issueClient.TerminalizeAcceptedReviewPublication(ctx, issues.TerminalReviewPublicationDisposition{Operation: current, ExpectedClaimToken: claimToken, State: state, FailureKind: failureKind, FailureDetail: failureDetail, FailureArtifact: failureArtifact, FinishedAt: finished})
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	d.publishPublicationOperationEvent(terminal)
	if d.publicationStateChanged != nil {
		d.publicationStateChanged(terminal)
	}
	return terminal, nil
}

func (d *Daemon) validatePublicationReviewAuthority(ctx context.Context, operation domain.PublicationOperation) error {
	if err := operation.ValidateReviewAuthority(); err != nil {
		return err
	}
	store, err := d.publicationStoreForProject(operation.ProjectID)
	if err != nil {
		return err
	}
	snapshot, err := store.PublicationEvidenceSnapshot(ctx, operation.ProjectID, operation.IssueID)
	if err != nil {
		return fmt.Errorf("read patch-review prerequisite: %w", err)
	}
	foundEvidence := false
	for _, evidence := range snapshot.Evidence {
		if evidence.EvidenceID == operation.PatchEvidenceID && evidence.Layer == domain.PublicationEvidencePatchReview && evidence.SourceRevision == operation.SourceRevision && evidence.PolicyVersion == operation.PolicyVersion && evidence.EnvironmentFingerprint == operation.EnvironmentFingerprint && strings.EqualFold(strings.TrimPrefix(evidence.Producer, "reviewer:"), operation.ActorID) {
			foundEvidence = true
			break
		}
	}
	if !foundEvidence {
		return fmt.Errorf("publication patch-review prerequisite %s is missing or incompatible", operation.PatchEvidenceID)
	}
	issueClient := d.issueClientForProject(operation.ProjectID)
	if issueClient == nil {
		return fmt.Errorf("issue store unavailable for accepted-review authority")
	}
	events, err := issueClient.ListIssueObservationEvents(ctx, operation.IssueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}, NewestIDFirst: true})
	if err != nil {
		return fmt.Errorf("read accepted-review authority: %w", err)
	}
	for _, event := range events {
		outcome, trusted := domain.TrustedReviewOutcome(event)
		if event.ID == operation.AcceptedReviewEventID && trusted && outcome == domain.ReviewOutcomeAccepted && strings.EqualFold(observationPayloadString(event.Payload, "actor_id"), operation.ActorID) && observationPayloadString(event.Payload, "publication_operation_id") == operation.OperationID && reviewPayloadInt64(event.Payload["review_epoch_event_id"]) == operation.ReviewEpochEventID {
			return nil
		}
	}
	return fmt.Errorf("publication accepted-review event %d is missing or incompatible", operation.AcceptedReviewEventID)
}

func (d *Daemon) publicationOperationAlreadyApplied(ctx context.Context, operation domain.PublicationOperation) (bool, error) {
	candidate := strings.TrimSpace(operation.CandidateRevision)
	if operation.State != domain.PublicationOperationPassed || candidate == "" || strings.TrimSpace(operation.ValidationRequestID) == "" || d.git == nil {
		return false, nil
	}
	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(operation.ProjectID))
	if repoDir == "" {
		return false, nil
	}
	currentBase, err := d.git.ResolveCommit(ctx, repoDir, operation.TargetBranch)
	if err != nil {
		return false, fmt.Errorf("resolve target %s: %w", operation.TargetBranch, err)
	}
	if strings.TrimSpace(currentBase) != candidate {
		return false, nil
	}
	currentHead, err := d.git.HeadRevision(ctx, repoDir)
	if err != nil {
		return false, fmt.Errorf("resolve exact applied target HEAD: %w", err)
	}
	if strings.TrimSpace(currentHead) != candidate {
		return false, nil
	}
	status, err := d.git.Status(ctx, repoDir)
	if err != nil {
		return false, fmt.Errorf("inspect exact applied target: %w", err)
	}
	if status.HasChanges {
		return false, fmt.Errorf("exact applied target %s is dirty", candidate)
	}
	cfg, err := appconfig.LoadConfig(repoDir)
	if err != nil {
		return false, fmt.Errorf("reload project capability: %w", err)
	}
	gateCommand := publicationValidationIdentity(cfg)
	if gateCommand != operation.ValidationCommand || publicationPolicyVersion(cfg, gateCommand) != operation.PolicyVersion || publicationEnvironmentFingerprint(cfg) != operation.EnvironmentFingerprint {
		return false, nil
	}
	attempt, found, err := d.git.CanonicalIntegrationValidation(ctx, repoDir, candidate)
	if err != nil {
		return false, fmt.Errorf("read canonical validation receipt for %s: %w", candidate, err)
	}
	return found && attempt.Canonical && attempt.Status == domain.IntegrationCandidateValidationPassed && strings.TrimSpace(attempt.CandidateHead) == candidate, nil
}

func publicationPolicyVersion(cfg *appconfig.Config, gateCommand string) string {
	if version := strings.TrimSpace(cfg.PublicationEvidence.PolicyVersion); version != "" {
		return version
	}
	return "gate-command:" + fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(gateCommand))))[:16]
}

func publicationValidationIdentity(cfg *appconfig.Config) string {
	if cfg == nil || len(cfg.Gate.Stages) == 0 {
		if cfg == nil {
			return ""
		}
		return strings.TrimSpace(cfg.Gate.Command)
	}
	type identityStage struct {
		ID            string   `json:"id"`
		Command       string   `json:"command"`
		DependsOn     []string `json:"depends_on,omitempty"`
		Resources     []string `json:"resources,omitempty"`
		ArtifactPaths []string `json:"artifact_paths,omitempty"`
		Required      bool     `json:"required"`
	}
	stages := make([]identityStage, 0, len(cfg.Gate.Stages))
	for _, stage := range cfg.Gate.Stages {
		required := stage.Required == nil || *stage.Required
		item := identityStage{ID: strings.TrimSpace(stage.ID), Command: strings.TrimSpace(stage.Command), DependsOn: append([]string(nil), stage.DependsOn...), Resources: append([]string(nil), stage.Resources...), ArtifactPaths: append([]string(nil), stage.ArtifactPaths...), Required: required}
		sort.Strings(item.DependsOn)
		sort.Strings(item.Resources)
		sort.Strings(item.ArtifactPaths)
		stages = append(stages, item)
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i].ID < stages[j].ID })
	encoded, _ := json.Marshal(stages)
	return "stage-dag:" + fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func publicationValidationStages(cfg *appconfig.Config) []git.CandidateValidationStage {
	stages := make([]git.CandidateValidationStage, 0, len(cfg.Gate.Stages))
	for _, stage := range cfg.Gate.Stages {
		required := stage.Required == nil || *stage.Required
		stages = append(stages, git.CandidateValidationStage{ID: stage.ID, Command: stage.Command, DependsOn: append([]string(nil), stage.DependsOn...), Resources: append([]string(nil), stage.Resources...), ArtifactPaths: append([]string(nil), stage.ArtifactPaths...), Required: true})
		stages[len(stages)-1].Required = required
	}
	return stages
}

func publicationEnvironmentFingerprint(cfg *appconfig.Config) string {
	if cfg != nil {
		if fingerprint := strings.TrimSpace(cfg.Gate.EnvironmentFingerprint); fingerprint != "" {
			return fingerprint
		}
	}
	return runtime.Version() + ":" + runtime.GOOS + ":" + runtime.GOARCH
}

func (d *Daemon) validatePublicationOperationIdentity(ctx context.Context, operation domain.PublicationOperation) error {
	repoDir := strings.TrimSpace(d.resolveRepoDirForProjectExact(operation.ProjectID))
	if repoDir == "" {
		return fmt.Errorf("inspect publication identity: target repository is unavailable")
	}
	currentBase, err := d.git.ResolveCommit(ctx, repoDir, operation.TargetBranch)
	if err != nil {
		return fmt.Errorf("inspect publication identity: resolve target %s: %w", operation.TargetBranch, err)
	}
	cfg, err := appconfig.LoadConfig(repoDir)
	if err != nil {
		return fmt.Errorf("inspect publication identity: reload project capability: %w", err)
	}
	gateCommand := publicationValidationIdentity(cfg)
	currentBase = strings.TrimSpace(currentBase)
	currentPolicy := publicationPolicyVersion(cfg, gateCommand)
	currentEnvironment := publicationEnvironmentFingerprint(cfg)
	var changes []string
	if currentBase != operation.BaseRevision {
		changes = append(changes, fmt.Sprintf("base revision changed from %s to %s", operation.BaseRevision, currentBase))
	}
	if gateCommand != operation.ValidationCommand {
		changes = append(changes, "configured validation command changed")
	}
	if currentPolicy != operation.PolicyVersion {
		changes = append(changes, fmt.Sprintf("policy version changed from %s to %s", operation.PolicyVersion, currentPolicy))
	}
	if currentEnvironment != operation.EnvironmentFingerprint {
		changes = append(changes, fmt.Sprintf("toolchain environment changed from %s to %s", operation.EnvironmentFingerprint, currentEnvironment))
	}
	if len(changes) > 0 {
		replacement := refreshedPublicationOperationAttempt(operation, currentBase, gateCommand, currentPolicy, currentEnvironment)
		return &publicationRetryError{cause: fmt.Errorf("publication identity stale: %s", strings.Join(changes, "; ")), replacement: replacement}
	}
	return nil
}

func refreshedPublicationOperationAttempt(operation domain.PublicationOperation, baseRevision, validationCommand, policyVersion, environmentFingerprint string) domain.PublicationOperation {
	rootIntent := strings.Split(operation.IntentKey, ":publication-retry:")[0]
	identity := sha256.Sum256([]byte(strings.Join([]string{operation.OperationID, baseRevision, validationCommand, policyVersion, environmentFingerprint}, "\x00")))
	operation.IntentKey = fmt.Sprintf("%s:publication-retry:%x", rootIntent, identity[:8])
	operation.OperationID = publicationOperationIdentity(operation.ProjectID, operation.IssueID, operation.IntentKey)
	operation.BaseRevision = baseRevision
	operation.ValidationCommand = validationCommand
	operation.PolicyVersion = policyVersion
	operation.EnvironmentFingerprint = environmentFingerprint
	operation.CandidateRevision = ""
	operation.ValidationRequestID = ""
	operation.ReusedEvidenceID = ""
	operation.FailureKind = ""
	operation.FailureDetail = ""
	operation.FailureArtifact = ""
	operation.LeaseOwner = ""
	operation.ClaimToken = ""
	operation.ClaimExpiresAt = nil
	operation.State = domain.PublicationOperationQueued
	operation.QueuePosition = 0
	operation.CreatedAt = time.Now().UTC()
	operation.UpdatedAt = time.Time{}
	operation.StartedAt = nil
	operation.FinishedAt = nil
	return operation
}

func (d *Daemon) publicationCandidateAdmission(projectID, operationID, claimToken string, claimTTL time.Duration) git.CandidateValidationAdmission {
	return func(ctx context.Context, candidateRevision string) (bool, func(git.CandidateValidationAttempt) error, error) {
		store, err := d.publicationStoreForProject(projectID)
		if err != nil {
			return false, nil, err
		}
		operation, err := d.renewPublicationClaim(context.Background(), store, operationID, claimToken, claimTTL)
		if err != nil {
			return false, nil, err
		}
		requestID := publicationValidationRequestIdentity(operation.OperationID, candidateRevision, claimToken)
		leaseToken := publicationValidationLeaseToken(operation.OperationID, candidateRevision, claimToken)
		if operation.ValidationRequestID != "" && operation.ValidationRequestID != requestID {
			if err := d.awaitPriorPublicationValidation(ctx, operation.ProjectID, operation.ValidationRequestID); err != nil {
				return false, nil, fmt.Errorf("await prior publication validation %s: %w", operation.ValidationRequestID, err)
			}
			operation, err = d.renewPublicationClaim(context.Background(), store, operationID, claimToken, claimTTL)
			if err != nil {
				return false, nil, err
			}
		}
		acquire := func(id string) (domain.ValidationRequest, error) {
			profile := d.publicationValidationProfile(context.Background(), operation, candidateRevision)
			return d.operationRuntime.store.AcquireValidation(context.Background(), domain.ValidationAcquire{
				RequestID: id, LeaseToken: leaseToken, ProjectID: operation.ProjectID,
				Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate,
				IsolationMode: "synthetic-worktree", EnvironmentFingerprint: operation.EnvironmentFingerprint,
				Override: domain.ValidationOverrideNone, Profile: profile,
				Command: operation.ValidationCommand, SourceRevision: candidateRevision, TTL: defaultValidationLeaseTTL,
			}, time.Now().UTC())
		}
		validation, err := acquire(requestID)
		if err != nil {
			return false, nil, err
		}
		if validation.State == domain.ValidationRequestExpired || validation.State == domain.ValidationRequestFailed || validation.State == domain.ValidationRequestCancelled {
			retryToken, tokenErr := publicationClaimToken()
			if tokenErr != nil {
				return false, nil, tokenErr
			}
			requestID = fmt.Sprintf("%s-retry-%s", publicationValidationRequestIdentity(operation.OperationID, candidateRevision, claimToken), retryToken)
			leaseToken = publicationValidationLeaseToken(operation.OperationID, candidateRevision, claimToken+"\x00"+retryToken)
			validation, err = acquire(requestID)
			if err != nil {
				return false, nil, err
			}
		}
		current, found, err := store.PublicationOperation(context.Background(), operationID)
		if err != nil || !found {
			if err == nil {
				err = fmt.Errorf("publication operation disappeared during validation admission")
			}
			return false, nil, err
		}
		reusedID := ""
		reused := validation.Execution == domain.ValidationExecutionReused || (validation.State == domain.ValidationRequestCompleted && validation.Evidence.Present)
		if reused {
			reusedID = validation.AuthoritativeRequestID
			if reusedID == "" {
				reusedID = validation.RequestID
			}
		}
		if _, err = d.transitionPublicationOperation(context.Background(), current, claimToken, current.State, func(update *operationstore.PublicationOperationUpdate) {
			update.CandidateRevision = candidateRevision
			update.ValidationRequestID = validation.RequestID
			update.ReusedEvidenceID = reusedID
		}); err != nil {
			return false, nil, err
		}
		if reused {
			return true, nil, nil
		}
		if validation.State != domain.ValidationRequestActive {
			return false, nil, fmt.Errorf("publication validation request %s is %s", validation.RequestID, validation.State)
		}

		heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
		go func() {
			ticker := time.NewTicker(defaultValidationLeaseTTL / 3)
			defer ticker.Stop()
			for {
				select {
				case <-heartbeatCtx.Done():
					return
				case now := <-ticker.C:
					_, _ = d.operationRuntime.store.HeartbeatValidation(context.Background(), validation.RequestID, leaseToken, now.UTC(), defaultValidationLeaseTTL)
				}
			}
		}()
		var finishOnce sync.Once
		var finishErr error
		finish := func(attempt git.CandidateValidationAttempt) error {
			finishOnce.Do(func() {
				stopHeartbeat()
				state := domain.ValidationRequestCompleted
				if attempt.Status != git.CandidateValidationPassed {
					state = domain.ValidationRequestFailed
				}
				evidence := domain.ValidationEvidence{
					Held: true, Present: true, RequestID: validation.RequestID, Class: validation.Class,
					Scope: validation.Scope, Purpose: validation.Purpose, Execution: domain.ValidationExecutionExecuted,
					AuthoritativeRequestID: validation.RequestID, Profile: validation.Profile, SourceRevision: candidateRevision,
					Stages: append([]domain.ValidationStageEvidence(nil), attempt.Stages...),
				}
				if state == domain.ValidationRequestFailed {
					evidence.FailureSummary = attempt.Message
				}
				_, finishErr = d.operationRuntime.store.FinishValidation(context.Background(), validation.RequestID, leaseToken, state, attempt.Message, evidence, time.Now().UTC(), defaultValidationLeaseTTL)
				if finishErr == nil && state == domain.ValidationRequestCompleted {
					_, finishErr = d.renewPublicationClaim(context.Background(), store, operationID, claimToken, claimTTL)
				}
			})
			return finishErr
		}
		return false, finish, nil
	}
}

// publicationValidationProfile preserves the queue's policy-derived profile
// unless an exact completed reviewer-owned execution proves that the same
// command and environment already ran for this candidate. Selecting that
// execution's profile lets validation admission apply its existing typed
// ticket-review-to-repository-publication compatibility rule; no looser
// command, revision, environment, or authority match is introduced here.
func (d *Daemon) publicationValidationProfile(ctx context.Context, operation domain.PublicationOperation, candidateRevision string) string {
	fallback := "publication:" + operation.PolicyVersion
	if d == nil || d.operationRuntime == nil || d.operationRuntime.store == nil {
		return fallback
	}
	review, err := d.operationRuntime.store.LatestReviewValidation(ctx, operation.ProjectID, operation.IssueID, time.Now().UTC(), defaultValidationLeaseTTL)
	if err != nil || review == nil || review.State != domain.ValidationRequestCompleted || !review.Evidence.Present {
		return fallback
	}
	if operation.ValidateReviewAuthority() != nil || review.SourceRevision != strings.TrimSpace(candidateRevision) ||
		strings.Join(strings.Fields(review.Command), " ") != strings.Join(strings.Fields(operation.ValidationCommand), " ") ||
		review.EnvironmentFingerprint != operation.EnvironmentFingerprint || review.Override == domain.ValidationOverrideEmergency ||
		review.Scope != domain.ValidationScopeTicket || review.Purpose != domain.ValidationPurposeReviewEvidence ||
		!strings.EqualFold(strings.TrimSpace(review.ReviewerID), strings.TrimSpace(operation.ActorID)) ||
		review.ReviewEpochEventID != operation.ReviewEpochEventID {
		return fallback
	}
	return review.Profile
}

func (d *Daemon) transitionPublicationOperation(ctx context.Context, current domain.PublicationOperation, claimToken string, state domain.PublicationOperationState, mutate func(*operationstore.PublicationOperationUpdate)) (domain.PublicationOperation, error) {
	_, now, _, err := d.publicationClaimIdentity()
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	update := publicationOperationTransitionUpdate(current, claimToken, state, now, mutate)
	store, err := d.publicationStoreForProject(current.ProjectID)
	if err != nil {
		return domain.PublicationOperation{}, err
	}
	operation, err := store.UpdatePublicationOperation(ctx, current.OperationID, update)
	if err == nil {
		d.publishPublicationOperationEvent(operation)
		if d.publicationStateChanged != nil {
			d.publicationStateChanged(operation)
		}
	}
	return operation, err
}

func publicationOperationTransitionUpdate(current domain.PublicationOperation, claimToken string, state domain.PublicationOperationState, now time.Time, mutate func(*operationstore.PublicationOperationUpdate)) operationstore.PublicationOperationUpdate {
	update := operationstore.PublicationOperationUpdate{
		ExpectedStates: []domain.PublicationOperationState{current.State}, ExpectedClaimToken: claimToken, State: state,
		CandidateRevision: current.CandidateRevision, ValidationRequestID: current.ValidationRequestID,
		ReusedEvidenceID: current.ReusedEvidenceID, FailureKind: current.FailureKind, FailureDetail: current.FailureDetail,
		FailureArtifact: current.FailureArtifact, StartedAt: current.StartedAt, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(&update)
	}
	return update
}

func (d *Daemon) transitionPublicationWithRetry(ctx context.Context, store *operationstore.SQLiteStore, current domain.PublicationOperation, claimToken string, state domain.PublicationOperationState, mutate func(*operationstore.PublicationOperationUpdate), retry *publicationRetryError) (domain.PublicationOperation, domain.PublicationOperation, error) {
	_, now, _, err := d.publicationClaimIdentity()
	if err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, err
	}
	update := publicationOperationTransitionUpdate(current, claimToken, state, now, mutate)
	terminal, successor, err := store.TerminalizePublicationWithSuccessor(ctx, current.OperationID, update, retry.replacement, publicationCoalesceKey(retry.replacement))
	if err != nil {
		return domain.PublicationOperation{}, domain.PublicationOperation{}, err
	}
	d.publishPublicationOperationEvent(terminal)
	d.publishPublicationOperationEvent(successor)
	if d.publicationStateChanged != nil {
		d.publicationStateChanged(terminal)
		d.publicationStateChanged(successor)
	}
	return terminal, successor, nil
}

func (d *Daemon) publishPublicationOperationEvent(operation domain.PublicationOperation) {
	projectID := d.canonicalProjectID(operation.ProjectID)
	revision := d.nextRevision(projectID)
	if d.hub == nil {
		return
	}
	body, err := json.Marshal(operation)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("marshal publication operation event failed", "project_id", projectID, "operation_id", operation.OperationID, "error", err)
		}
		return
	}
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(projectID),
		Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Revision: revision,
		Event: protocol.EventPublicationOperationUpdated, Kind: protocol.EnvelopeKindEvent,
		EmittedAt: time.Now().UTC(), Body: body,
	})
}

func classifyPublicationFailure(err error) (domain.PublicationOperationState, string) {
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.Canceled):
		return domain.PublicationOperationCanceled, "operation_canceled"
	case strings.Contains(message, "conflict"):
		return domain.PublicationOperationConflicted, "merge_conflict"
	case strings.Contains(message, "moved"), strings.Contains(message, "changed"), strings.Contains(message, "stale"), strings.Contains(message, "fresh review required"):
		return domain.PublicationOperationStale, "identity_changed"
	default:
		return domain.PublicationOperationFailed, "validation_or_apply_failed"
	}
}

func (d *Daemon) writePublicationFailureArtifact(operation domain.PublicationOperation, state domain.PublicationOperationState, cause error) string {
	root := strings.TrimSpace(d.resolveRepoDirForProjectExact(operation.ProjectID))
	if root == "" {
		return ""
	}
	dir := filepath.Join(root, ".azedarach", "artifacts", "publication")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(dir, operation.OperationID+".log")
	body := fmt.Sprintf("operation=%s\nissue=%s\nsource=%s\nbase=%s\nstate=%s\nerror=%s\n", operation.OperationID, operation.IssueID, operation.SourceRevision, operation.BaseRevision, state, cause)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return ""
	}
	return path
}

func (d *Daemon) recoverPublicationOperations(ctx context.Context) {
	if d.operationRuntime == nil || d.operationRuntime.store == nil {
		return
	}
	for _, projectID := range d.publicationProjectIDs() {
		store, err := d.publicationStoreForProject(projectID)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("publication store recovery failed", "project_id", projectID, "error", err)
			}
			continue
		}
		operations, err := store.PublicationOperations(ctx, projectID, "", true)
		if err != nil {
			if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("publication queue recovery failed", "project_id", projectID, "error", err)
			}
			continue
		}
		for _, operation := range operations {
			if err := d.submitRecoveredPublicationOperation(ctx, operation); err != nil {
				if d.cfg.Logger != nil {
					d.cfg.Logger.Warn("publication operation recovery submit failed; scheduling retry", "operation_id", operation.OperationID, "error", err)
				}
				d.schedulePublicationRecoveryRetry(d.publicationContinuationContext(), operation)
			}
		}
	}
}

func (d *Daemon) submitRecoveredPublicationOperation(ctx context.Context, operation domain.PublicationOperation) error {
	if d.publicationRecoverySubmit != nil {
		return d.publicationRecoverySubmit(ctx, operation)
	}
	return d.submitPublicationOperation(ctx, operation)
}

func (d *Daemon) schedulePublicationRecoveryRetry(ctx context.Context, operation domain.PublicationOperation) {
	key := "recovery\x00" + operation.ProjectID + "\x00" + operation.OperationID
	d.publicationContinuationMu.Lock()
	if d.publicationContinuationStopping {
		d.publicationContinuationMu.Unlock()
		return
	}
	if d.publicationContinuationRetrying == nil {
		d.publicationContinuationRetrying = make(map[string]bool)
	}
	if d.publicationContinuationRetrying[key] {
		d.publicationContinuationMu.Unlock()
		return
	}
	d.publicationContinuationRetrying[key] = true
	d.publicationContinuationWG.Add(1)
	d.publicationContinuationMu.Unlock()
	go func() {
		defer func() {
			d.publicationContinuationMu.Lock()
			delete(d.publicationContinuationRetrying, key)
			d.publicationContinuationMu.Unlock()
			d.publicationContinuationWG.Done()
		}()
		for {
			if d.publicationRecoveryWait != nil {
				if err := d.publicationRecoveryWait(ctx); err != nil {
					if ctx.Err() != nil {
						return
					}
					continue
				}
			} else {
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if err := d.submitRecoveredPublicationOperation(ctx, operation); err == nil {
				return
			} else if d.cfg.Logger != nil {
				d.cfg.Logger.Warn("publication operation recovery retry failed", "operation_id", operation.OperationID, "error", err)
			}
		}
	}()
}

func (d *Daemon) publicationProjectIDs() []string {
	seen := make(map[string]struct{})
	add := func(raw string, out *[]string) {
		projectID := d.canonicalProjectID(raw)
		if _, ok := seen[projectID]; ok {
			return
		}
		seen[projectID] = struct{}{}
		*out = append(*out, projectID)
	}
	var out []string
	add(protocol.DefaultProjectID, &out)
	if d.cfg.ScopedRuntime {
		return out
	}
	registry, err := appconfig.LoadProjectsRegistry()
	if err != nil || registry == nil {
		return out
	}
	for _, project := range registry.Projects {
		projectID := strings.TrimSpace(project.ID)
		if projectID == "" {
			projectID = strings.TrimSpace(project.Name)
		}
		if projectID != "" {
			add(projectID, &out)
		}
	}
	return out
}
