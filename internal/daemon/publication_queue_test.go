package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestPublicationReviewAuthorityRejectsSupersededAcceptance(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	client := newMigratedIssueClient(t, repo, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "superseded publication", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-old", issueID, "accept-old", "source", time.Now().UTC())
	operation.ActorID = "reviewer"
	operation.ReviewerKind = domain.ReviewerOwnerKindOrchestrator
	operation.ReviewEpochEventID = 17
	operation.PatchEvidenceID = "patch-old"
	operation.AcceptedPublicationOperationID = operation.OperationID
	if _, err := runtime.store.RecordPublicationEvidence(ctx, domain.PublicationEvidence{
		EvidenceID: operation.PatchEvidenceID, ProjectID: operation.ProjectID, IssueID: issueID,
		Layer: domain.PublicationEvidencePatchReview, PatchDigest: "patch", SourceRevision: operation.SourceRevision,
		BaseRevision: operation.BaseRevision, Producer: "reviewer:" + operation.ActorID,
		PolicyVersion: operation.PolicyVersion, EnvironmentFingerprint: operation.EnvironmentFingerprint, CreatedAt: operation.CreatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	appendAcceptance := func(publicationID string) int64 {
		event, appendErr := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept",
			Payload: map[string]any{"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": operation.ActorID, "actor_kind": operation.ReviewerKind, "publication_operation_id": publicationID, "review_epoch_event_id": operation.ReviewEpochEventID},
		})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		return event.ID
	}
	operation.AcceptedReviewEventID = appendAcceptance(operation.OperationID)
	for range 501 {
		if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
			Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept",
			Payload: map[string]any{"outcome": string(domain.ReviewOutcomeIntegrationFailed), "actor_id": operation.ActorID, "actor_kind": operation.ReviewerKind},
		}); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{cfg: Config{RepoDir: repo, Logger: slog.Default()}, operationRuntime: runtime, issues: client, issueClientsByProject: map[string]*issues.Client{runtime.canonicalProject: client}}
	if err := d.validatePublicationReviewAuthority(ctx, operation); err != nil {
		t.Fatalf("authoritative acceptance hidden by ignored diagnostics: %v", err)
	}
	newestID := appendAcceptance("publication-new")
	err = d.validatePublicationReviewAuthority(ctx, operation)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("superseded by authoritative event %d", newestID)) {
		t.Fatalf("superseded publication authority error = %v", err)
	}
}

func TestReviewAcceptRetainsLeaseAcrossCommittedPublicationContinuationFailures(t *testing.T) {
	for _, failureStage := range []string{"post-commit-cancellation", "initial-submit", "committed-queue-read", "post-submit-refresh"} {
		t.Run(failureStage, func(t *testing.T) {
			ctx := context.Background()
			repo := t.TempDir()
			runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
			runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
			runDaemonTestGit(t, repo, "config", "user.name", "Test User")
			canonicalRepo, err := appconfig.ResolveProjectRoot(repo)
			if err != nil {
				t.Fatal(err)
			}
			repo = canonicalRepo
			if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runDaemonTestGit(t, repo, "add", "README.md")
			runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
			if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, ".azedarach", "config.json"), []byte(`{"gate":{"command":"go test ./consumer/...","environmentFingerprint":"consumer-go"},"publicationEvidence":{"policyVersion":"consumer-v1"}}`), 0o644); err != nil {
				t.Fatal(err)
			}

			issueClient := newMigratedIssueClient(t, repo, slog.Default())
			t.Cleanup(func() { _ = issueClient.CloseDB() })
			runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
			t.Cleanup(func() { _ = runtime.Close() })
			projectID := runtime.canonicalProject
			issueID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "publish reviewed patch", Description: "consumer patch", Acceptance: "validated on configured base", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := issueClient.ClaimOwnershipWithRuntime(ctx, projectID, issueID, issues.OwnershipClaimParams{OwnerID: "worker", OwnerKind: "agent"}); err != nil {
				t.Fatal(err)
			}
			if err := issueClient.Update(ctx, issueID, domain.StatusInReview); err != nil {
				t.Fatal(err)
			}
			if _, err := issueClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t)}); err != nil {
				t.Fatal(err)
			}
			worktreeManager := gitservice.NewWorktreeManager(gitservice.NewExecRunner(repo), repo, slog.Default())
			reviewedWorktree, err := worktreeManager.Create(ctx, issueID, "main")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(reviewedWorktree.Path, "change.txt"), []byte("reviewed patch\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runDaemonTestGit(t, reviewedWorktree.Path, "add", "change.txt")
			runDaemonTestGit(t, reviewedWorktree.Path, "commit", "-q", "-m", "reviewed patch")

			gitClient := gitservice.NewClient(gitservice.NewExecRunner(repo), slog.Default())
			d := newOrchestrationReviewTestDaemon(repo, issueClient)
			d.snapshotAdmissionContext = func(parent context.Context) (context.Context, context.CancelFunc) {
				return context.WithCancel(parent)
			}
			d.cfg.BaseBranch = "main"
			d.issueClientsByProject = map[string]*issues.Client{projectID: issueClient}
			runtimeStateStore := daemonstate.NewRuntimeStateStore(repo, slog.Default())
			t.Cleanup(func() { _ = runtimeStateStore.Close() })
			d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStateStore}
			d.operationRuntime = runtime
			d.git = gitClient
			d.worktreeAdapter = &worktreeServiceAdapter{manager: worktreeManager, runtimeStateStore: runtimeStateStore}
			if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: projectID, IssueID: issueID, Path: reviewedWorktree.Path, Branch: reviewedWorktree.Branch}); err != nil {
				t.Fatalf("seed reviewed worktree projection: %v", err)
			}
			sourceOID := runDaemonTestGitOutput(t, reviewedWorktree.Path, "rev-parse", "HEAD")
			d.reviewAcceptedSourceOID = func(context.Context, string, string) (string, error) { return sourceOID, nil }
			if resolved := d.resolveRepoDirForProjectExact(projectID); resolved != repo {
				t.Fatalf("project %q resolved repo %q, want %q", projectID, resolved, repo)
			}
			applyEntered := make(chan domain.PublicationOperation, 1)
			applyRelease := make(chan struct{})
			merged := make(chan struct{})
			retryWaiting := make(chan struct{})
			retryRelease := make(chan struct{})
			var retryWaitOnce sync.Once
			var mergedOnce sync.Once
			var applyCount atomic.Int32
			switch failureStage {
			case "initial-submit":
				d.publicationInitialSubmit = func(context.Context, domain.PublicationOperation) error {
					return errors.New("injected initial publication submit failure")
				}
			case "committed-queue-read", "post-submit-refresh":
				d.publicationCommittedQueueRead = func(stage string, readCtx context.Context, _ string, operationID string) (domain.PublicationOperation, bool, error) {
					if stage == failureStage {
						return domain.PublicationOperation{}, false, fmt.Errorf("injected %s failure", failureStage)
					}
					return runtime.store.PublicationOperation(readCtx, operationID)
				}
			}
			d.publicationRecoveryWait = func(ctx context.Context) error {
				retryWaitOnce.Do(func() { close(retryWaiting) })
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-retryRelease:
					return nil
				}
			}
			d.publicationIdentityCheck = func(context.Context, domain.PublicationOperation) error { return nil }
			d.publicationClose = func(_ context.Context, operation domain.PublicationOperation) error {
				applyCount.Add(1)
				applyEntered <- operation
				<-applyRelease
				return nil
			}
			d.publicationStateChanged = func(operation domain.PublicationOperation) {
				if operation.State == domain.PublicationOperationMerged {
					mergedOnce.Do(func() { close(merged) })
				}
			}
			integrationReadiness, err := d.taskIntegrationReadiness(ctx, projectID, issueID, repo)
			if err != nil {
				t.Fatal(err)
			}
			if integrationReadiness.Ready || !strings.Contains(strings.Join(integrationReadiness.Reasons, "; "), "no aggregate validation") {
				t.Fatalf("pre-accept integration readiness = %+v, want aggregate gate preserved", integrationReadiness)
			}

			request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-and-auto-publish", ActorID: "reviewer", IssueIDs: []string{issueID}, RepoDir: repo}
			applyCtx := ctx
			if failureStage == "post-commit-cancellation" {
				var cancel context.CancelFunc
				applyCtx, cancel = context.WithCancel(ctx)
				applyCtx = issues.WithAcceptedReviewPublicationCommitHookForTest(applyCtx, cancel)
			}
			result, err := d.orchestrationAuthority().Apply(applyCtx, projectID, request)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Failed) != 0 || len(result.Publications) != 1 {
				t.Fatalf("review acceptance = %+v, want one automatic publication", result)
			}
			<-retryWaiting
			queued, found, err := runtime.store.PublicationOperation(ctx, result.Publications[0].OperationID)
			if err != nil || !found || queued.State != domain.PublicationOperationQueued {
				t.Fatalf("durable publication after submit failure = (%+v,%t,%v)", queued, found, err)
			}
			task, err := issueClient.GetWithRuntime(ctx, projectID, issueID)
			if err != nil {
				t.Fatal(err)
			}
			lease := coordinationLease(task, domain.CoordinationLeaseReview)
			if lease == nil || lease.OwnerID != "reviewer" {
				t.Fatalf("review lease during publication = %+v, want reviewer-owned durable lease", lease)
			}
			replacementEvidence := mustWorkerEvidencePayload(t)
			replacementEvidence["summary"] = "must remain fenced after committed enqueue"
			if _, err := issueClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: replacementEvidence}); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("evidence mutation after committed enqueue error = %v, want conflict", err)
			}
			if err := issueClient.Update(ctx, issueID, domain.StatusInProgress); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("epoch mutation after committed enqueue error = %v, want conflict", err)
			}
			events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
			if err != nil || len(events) != 1 {
				t.Fatalf("accepted review events after submit failure = (%+v,%v), want exactly one", events, err)
			}
			// A restart recovery scan may race the immediate retry. Operation-manager
			// dedupe and the durable publication claim must still admit one apply only.
			d.recoverPublicationOperations(ctx)
			close(retryRelease)
			operation := <-applyEntered
			if operation.SourceRevision != sourceOID || operation.TargetBranch != "main" || operation.ValidationCommand != "go test ./consumer/..." {
				t.Fatalf("continued publication = %+v", operation)
			}
			validation, err := runtime.store.LatestReviewValidation(ctx, projectID, issueID, time.Now().UTC(), defaultValidationLeaseTTL)
			if err != nil {
				t.Fatal(err)
			}
			if validation != nil {
				t.Fatalf("pre-accept aggregate validation = %+v, want none", validation)
			}
			close(applyRelease)
			<-merged
			if err := runtime.manager.Drain(ctx); err != nil {
				t.Fatalf("drain accepted publication operation: %v", err)
			}
			if got := applyCount.Load(); got != 1 {
				t.Fatalf("publication apply count = %d, want exactly one", got)
			}
		})
	}
}

func TestPublicationRecoversCrashAfterExactApplyBeforeTaskReceipt(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repo, "config", "user.name", "Test User")
	requireNoError(t, os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755))
	requireNoError(t, os.WriteFile(filepath.Join(repo, ".azedarach", "config.json"), []byte(`{"gate":{"command":"go test ./...","environmentFingerprint":"go-consumer"},"publicationEvidence":{"policyVersion":"consumer-v1"}}`), 0o644))
	requireNoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("issues.db*\n.azedarach/*.db*\n"), 0o644))
	requireNoError(t, os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.test/consumer\n\ngo 1.24\n"), 0o644))
	requireNoError(t, os.WriteFile(filepath.Join(repo, "consumer.go"), []byte("package consumer\n\nconst Value = 1\n"), 0o644))
	runDaemonTestGit(t, repo, "add", ".")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
	canonicalRepo, err := appconfig.ResolveProjectRoot(repo)
	requireNoError(t, err)
	repo = canonicalRepo
	baseOID := runDaemonTestGitOutput(t, repo, "rev-parse", "HEAD")

	issuePath := filepath.Join(repo, ".azedarach", "azedarach.db")
	issueClient := newMigratedIssueClient(t, repo, slog.Default())
	issueID := createReviewTask(t, ctx, issueClient, domain.P1, "worker")
	_, err = issueClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t)})
	requireNoError(t, err)
	sourcePath := filepath.Join(t.TempDir(), "source")
	sourceBranch := "riordan/" + issueID + "/publication-crash"
	runDaemonTestGit(t, repo, "worktree", "add", "-q", "-b", sourceBranch, sourcePath, "main")
	sourcePath, err = filepath.EvalSymlinks(sourcePath)
	requireNoError(t, err)
	requireNoError(t, os.WriteFile(filepath.Join(sourcePath, "consumer.go"), []byte("package consumer\n\nconst Value = 2\n"), 0o644))
	runDaemonTestGit(t, sourcePath, "add", "consumer.go")
	runDaemonTestGit(t, sourcePath, "commit", "-q", "-m", "reviewed patch")
	sourceOID := runDaemonTestGitOutput(t, sourcePath, "rev-parse", "HEAD")

	statePath := filepath.Join(t.TempDir(), "runtime.db")
	firstState := daemonstate.NewRuntimeStateStoreAtPath(statePath, slog.Default())
	firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	projectID := firstRuntime.canonicalProject
	requireNoError(t, firstState.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: projectID, IssueID: issueID, Path: sourcePath, Branch: sourceBranch, UpdatedAt: time.Now().UTC()}))
	first := newOrchestrationReviewTestDaemon(repo, issueClient)
	first.reviewCandidateCheck = nil
	first.snapshotAdmissionContext = context.WithCancel
	first.cfg.BaseBranch = "main"
	first.issueClientsByProject = map[string]*issues.Client{projectID: issueClient}
	first.operationRuntime = firstRuntime
	first.git = gitservice.NewClient(gitservice.NewExecRunner(repo), slog.Default())
	first.worktreeAdapter = &worktreeServiceAdapter{manager: gitservice.NewWorktreeManager(gitservice.NewExecRunner(repo), repo, slog.Default()), runtimeStateStore: firstState}
	first.reviewAcceptedSourceOID = func(context.Context, string, string) (string, error) { return sourceOID, nil }
	first.publicationClaimTTL = time.Minute
	claimNow := time.Now().UTC()
	first.publicationClaimNow = func() time.Time { return claimNow }
	crashed := make(chan struct{})
	failed := make(chan domain.PublicationOperation, 1)
	first.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.State.Terminal() && operation.State != domain.PublicationOperationMerged {
			failed <- operation
		}
	}
	first.publicationAppliedBeforeTaskReceipt = func(context.Context, taskCloseIntegrationResult) {
		close(crashed)
		goruntime.Goexit()
	}
	accepted, err := first.orchestrationAuthority().Apply(ctx, projectID, protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-crash-after-apply", ActorID: "reviewer", IssueIDs: []string{issueID}, RepoDir: repo})
	requireNoError(t, err)
	if len(accepted.Publications) != 1 || len(accepted.Failed) != 0 {
		t.Fatalf("acceptance = %+v, want one publication", accepted)
	}
	select {
	case <-crashed:
	case operation := <-failed:
		t.Fatalf("publication failed before deterministic crash barrier: state=%s kind=%s detail=%s", operation.State, operation.FailureKind, operation.FailureDetail)
	}
	requireNoError(t, firstRuntime.manager.Drain(ctx))
	operations, err := firstRuntime.store.PublicationOperations(ctx, projectID, issueID, false)
	requireNoError(t, err)
	if len(operations) != 1 || operations[0].State != domain.PublicationOperationPassed || operations[0].CandidateRevision == "" {
		t.Fatalf("post-crash operations = %+v, want one passed original", operations)
	}
	original := operations[0]
	if original.BaseRevision != baseOID || original.SourceRevision != sourceOID || runDaemonTestGitOutput(t, repo, "rev-parse", "main") != original.CandidateRevision {
		t.Fatalf("post-crash operation = %+v, want exact candidate applied", original)
	}
	before, err := firstRuntime.store.ValidationSnapshot(ctx, projectID, claimNow, defaultValidationLeaseTTL)
	requireNoError(t, err)
	beforeCount := len(before.Active) + len(before.Queued) + len(before.Recent)
	receipts, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}})
	requireNoError(t, err)
	if len(receipts) != 0 {
		t.Fatalf("post-crash task receipts = %+v, want none", receipts)
	}
	first.closePublicationStores()
	requireNoError(t, firstRuntime.Close())
	requireNoError(t, firstState.Close())
	requireNoError(t, issueClient.CloseDB())

	restartedClient := newMigratedIssueClientAtPath(t, issuePath, slog.Default())
	t.Cleanup(func() { _ = restartedClient.CloseDB() })
	restartedState := daemonstate.NewRuntimeStateStoreAtPath(statePath, slog.Default())
	t.Cleanup(func() { _ = restartedState.Close() })
	restartedRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = restartedRuntime.Close() })
	restarted := newOrchestrationReviewTestDaemon(repo, restartedClient)
	restarted.reviewCandidateCheck = nil
	restarted.cfg.BaseBranch = "main"
	restarted.issueClientsByProject = map[string]*issues.Client{projectID: restartedClient}
	restarted.operationRuntime = restartedRuntime
	restarted.git = gitservice.NewClient(gitservice.NewExecRunner(repo), slog.Default())
	restarted.worktreeAdapter = &worktreeServiceAdapter{manager: gitservice.NewWorktreeManager(gitservice.NewExecRunner(repo), repo, slog.Default()), runtimeStateStore: restartedState}
	restarted.publicationClaimTTL = time.Minute
	restarted.publicationClaimNow = func() time.Time { return claimNow.Add(2 * time.Minute) }
	merged := make(chan domain.PublicationOperation, 1)
	restarted.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.State == domain.PublicationOperationMerged {
			merged <- operation
		}
	}
	restarted.recoverPublicationOperations(ctx)
	recovered := <-merged
	requireNoError(t, restartedRuntime.manager.Drain(ctx))
	if recovered.OperationID != original.OperationID {
		t.Fatalf("recovered operation = %+v, want original %s", recovered, original.OperationID)
	}
	all, err := restartedRuntime.store.PublicationOperations(ctx, projectID, issueID, false)
	requireNoError(t, err)
	if len(all) != 1 || all[0].State != domain.PublicationOperationMerged {
		t.Fatalf("recovered operations = %+v, want one merged original and no successor", all)
	}
	after, err := restartedRuntime.store.ValidationSnapshot(ctx, projectID, claimNow.Add(2*time.Minute), defaultValidationLeaseTTL)
	requireNoError(t, err)
	afterCount := len(after.Active) + len(after.Queued) + len(after.Recent)
	if afterCount != beforeCount {
		t.Fatalf("validation request count = before %d after %d, want no revalidation", beforeCount, afterCount)
	}
	task, err := restartedClient.GetWithRuntime(ctx, projectID, issueID)
	requireNoError(t, err)
	if task.Status != domain.StatusDone {
		t.Fatalf("recovered issue status = %s, want done", task.Status)
	}
	receipts, err = restartedClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}})
	requireNoError(t, err)
	if len(receipts) != 1 {
		t.Fatalf("recovered task receipts = %+v, want one exact integration receipt", receipts)
	}
	receipt := receipts[0]
	if operationID := observationPayloadString(receipt.Payload, "publication_operation_id"); operationID != original.OperationID {
		t.Fatalf("recovered receipt publication operation = %q, want %q", operationID, original.OperationID)
	}
	if got := observationPayloadString(receipt.Payload, "base_oid"); got != original.BaseRevision {
		t.Fatalf("recovered receipt base OID = %q, want %q", got, original.BaseRevision)
	}
	if got := observationPayloadString(receipt.Payload, "source_oid"); got != original.SourceRevision {
		t.Fatalf("recovered receipt source OID = %q, want %q", got, original.SourceRevision)
	}
	if got := observationPayloadString(receipt.Payload, "target_oid"); got != original.CandidateRevision {
		t.Fatalf("recovered receipt target OID = %q, want %q", got, original.CandidateRevision)
	}
	evidence, err := restartedRuntime.store.PublicationEvidenceSnapshot(ctx, projectID, issueID)
	requireNoError(t, err)
	var mergeEvidence domain.PublicationEvidence
	found := false
	for _, candidate := range evidence.Evidence {
		if candidate.Layer == domain.PublicationEvidenceMergeResult {
			mergeEvidence, found = candidate, true
			break
		}
	}
	if !found || mergeEvidence.BaseRevision != original.BaseRevision || mergeEvidence.SourceRevision != original.SourceRevision || mergeEvidence.ResultRevision != original.CandidateRevision {
		t.Fatalf("recovered merge-result evidence = %+v, want exact publication identity", evidence.Evidence)
	}
}

func TestReviewAcceptWithoutConfiguredGateFailsWithoutPublicationReadinessEvidence(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repo, "config", "user.name", "Test User")
	canonicalRepo, err := appconfig.ResolveProjectRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	repo = canonicalRepo
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "add", "README.md")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
	if err := os.MkdirAll(filepath.Join(repo, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".azedarach", "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	issueClient := newMigratedIssueClient(t, repo, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	projectID := runtime.canonicalProject
	issueID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "publish without gate", Description: "consumer patch", Acceptance: "fail closed without configured validation", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueClient.ClaimOwnershipWithRuntime(ctx, projectID, issueID, issues.OwnershipClaimParams{OwnerID: "worker", OwnerKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := issueClient.Update(ctx, issueID, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	if _, err := issueClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}

	d := newOrchestrationReviewTestDaemon(repo, issueClient)
	d.snapshotAdmissionContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}
	d.cfg.BaseBranch = "main"
	d.issueClientsByProject = map[string]*issues.Client{projectID: issueClient}
	d.operationRuntime = runtime
	d.git = gitservice.NewClient(gitservice.NewExecRunner(repo), slog.Default())
	d.worktreeAdapter = &worktreeServiceAdapter{manager: gitservice.NewWorktreeManager(gitservice.NewExecRunner(repo), repo, slog.Default())}
	sourceOID := runDaemonTestGitOutput(t, repo, "rev-parse", "HEAD")
	d.reviewAcceptedSourceOID = func(context.Context, string, string) (string, error) { return sourceOID, nil }

	request := protocol.OrchestrationIntentRequest{Scope: domain.ProjectOrchestrationScope(), Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: "accept-without-gate", ActorID: "reviewer", IssueIDs: []string{issueID}, RepoDir: repo}
	result, err := d.orchestrationAuthority().Apply(ctx, projectID, request)
	if err != nil {
		t.Fatal(err)
	}
	if failure := result.Failed[issueID]; !strings.Contains(failure, "publication capability absent: configure gate.command") {
		t.Fatalf("review acceptance = %+v, want explicit absent publication capability failure", result)
	}
	if len(result.Publications) != 0 || len(result.Closed) != 0 {
		t.Fatalf("review acceptance = %+v, want no publication or accepted close", result)
	}
	operations, err := runtime.store.PublicationOperations(ctx, projectID, issueID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 {
		t.Fatalf("publication operations = %+v, want none", operations)
	}
	reviewEvents, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventReviewCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewEvents) != 0 {
		t.Fatalf("review completion evidence = %+v, want none", reviewEvents)
	}
	validation, err := runtime.store.LatestReviewValidation(ctx, projectID, issueID, time.Now().UTC(), defaultValidationLeaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if validation != nil {
		t.Fatalf("aggregate validation evidence = %+v, want none", validation)
	}
	readiness, err := d.taskIntegrationReadiness(ctx, projectID, issueID, repo)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Ready || !strings.Contains(strings.Join(readiness.Reasons, "; "), "no aggregate validation") {
		t.Fatalf("integration readiness = %+v, want fail-closed absence of publication evidence", readiness)
	}
}

func TestPublicationQueueRecoveryClaimsOnceAcrossDaemons(t *testing.T) {
	repo := t.TempDir()
	firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	secondRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = firstRuntime.Close() })
	t.Cleanup(func() { _ = secondRuntime.Close() })

	operation := daemonTestPublicationOperation(firstRuntime.canonicalProject, "publication-multi-daemon", "issue", "intent", "source", time.Now().UTC())
	stored, _, err := firstRuntime.store.EnqueuePublication(context.Background(), operation, "candidate-multi-daemon")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	stored, acquired, err := firstRuntime.store.ClaimPublicationOperation(context.Background(), stored.OperationID, operationstore.PublicationOperationClaim{Owner: "crashed-daemon", Token: "crashed-claim", Now: started, TTL: time.Minute})
	if err != nil || !acquired {
		t.Fatal(err)
	}

	enteredApply := make(chan struct{}, 2)
	releaseApply := make(chan struct{})
	var applyCount atomic.Int32
	var validationCount atomic.Int32
	identityCheck := func(context.Context, domain.PublicationOperation) error {
		validationCount.Add(1)
		return nil
	}
	closeFn := func(context.Context, domain.PublicationOperation) error {
		applyCount.Add(1)
		enteredApply <- struct{}{}
		<-releaseApply
		return nil
	}
	first := &Daemon{operationRuntime: firstRuntime, cfg: Config{RepoDir: repo}, publicationClose: closeFn, publicationIdentityCheck: identityCheck}
	second := &Daemon{operationRuntime: secondRuntime, cfg: Config{RepoDir: repo}, publicationClose: closeFn, publicationIdentityCheck: identityCheck}
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, daemon := range []*Daemon{first, second} {
		go func(d *Daemon) {
			<-start
			_, runErr := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID)
			results <- runErr
		}(daemon)
	}
	close(start)
	<-enteredApply
	select {
	case <-enteredApply:
		close(releaseApply)
		<-results
		<-results
		t.Fatalf("two daemons reached publication apply; count=%d", applyCount.Load())
	case err := <-results:
		if err != nil {
			close(releaseApply)
			<-results
			t.Fatalf("non-owning daemon returned error: %v", err)
		}
		close(releaseApply)
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := applyCount.Load(); got != 1 {
		t.Fatalf("publication apply count = %d, want 1", got)
	}
	if got := validationCount.Load(); got != 1 {
		t.Fatalf("publication validation count = %d, want 1", got)
	}
}

func TestPublicationQueueSerializesDistinctTargetOperationsAcrossDaemons(t *testing.T) {
	repo := t.TempDir()
	firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	secondRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = firstRuntime.Close() })
	t.Cleanup(func() { _ = secondRuntime.Close() })
	now := time.Now().UTC()
	firstOperation := daemonTestPublicationOperation(firstRuntime.canonicalProject, "publication-target-first", "issue-first", "intent-first", "source-first", now)
	secondOperation := daemonTestPublicationOperation(firstRuntime.canonicalProject, "publication-target-second", "issue-second", "intent-second", "source-second", now.Add(time.Second))
	for _, operation := range []domain.PublicationOperation{firstOperation, secondOperation} {
		if _, _, err := firstRuntime.store.EnqueuePublication(context.Background(), operation, publicationCoalesceKey(operation)); err != nil {
			t.Fatal(err)
		}
	}
	firstApplyEntered := make(chan struct{})
	secondApplyEntered := make(chan struct{})
	secondMerged := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	var validationCount atomic.Int32
	var applyCount atomic.Int32
	identityCheck := func(context.Context, domain.PublicationOperation) error {
		validationCount.Add(1)
		return nil
	}
	closeFn := func(_ context.Context, operation domain.PublicationOperation) error {
		applyCount.Add(1)
		switch operation.OperationID {
		case firstOperation.OperationID:
			close(firstApplyEntered)
			<-releaseFirst
		case secondOperation.OperationID:
			close(secondApplyEntered)
			<-releaseSecond
		}
		return nil
	}
	first := &Daemon{operationRuntime: firstRuntime, cfg: Config{RepoDir: repo}, publicationClose: closeFn, publicationIdentityCheck: identityCheck}
	first.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.OperationID == secondOperation.OperationID && operation.State == domain.PublicationOperationMerged {
			close(secondMerged)
		}
	}
	second := &Daemon{operationRuntime: secondRuntime, cfg: Config{RepoDir: repo}, publicationClose: closeFn, publicationIdentityCheck: identityCheck}
	firstResult := make(chan error, 1)
	go func() {
		_, runErr := first.runPublicationOperation(context.Background(), firstOperation.ProjectID, firstOperation.OperationID)
		firstResult <- runErr
	}()
	<-firstApplyEntered
	if _, err := second.runPublicationOperation(context.Background(), secondOperation.ProjectID, secondOperation.OperationID); err != nil {
		t.Fatal(err)
	}
	if got := validationCount.Load(); got != 1 {
		t.Fatalf("validation count before first terminal = %d, want 1", got)
	}
	if got := applyCount.Load(); got != 1 {
		t.Fatalf("apply count before first terminal = %d, want 1", got)
	}
	queued, found, err := secondRuntime.store.PublicationOperation(context.Background(), secondOperation.OperationID)
	if err != nil || !found || queued.State != domain.PublicationOperationQueued {
		t.Fatalf("second operation while first active = (%+v,%t,%v), want queued", queued, found, err)
	}
	close(releaseFirst)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	<-secondApplyEntered
	if got := validationCount.Load(); got != 2 {
		t.Fatalf("validation count after first terminal = %d, want 2", got)
	}
	if got := applyCount.Load(); got != 2 {
		t.Fatalf("apply count after first terminal = %d, want 2", got)
	}
	close(releaseSecond)
	<-secondMerged
	if err := firstRuntime.manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationQueueSerializesTargetAndCoalescesManagerWork(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	merged := make(chan string, 2)
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationClose = func(_ context.Context, operation domain.PublicationOperation) error {
		if operation.OperationID == "publication-1" {
			close(firstStarted)
			<-releaseFirst
		} else {
			close(secondStarted)
		}
		return nil
	}
	d.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.State == domain.PublicationOperationMerged {
			merged <- operation.OperationID
		}
	}
	projectID := runtime.canonicalProject
	first := daemonTestPublicationOperation(projectID, "publication-1", "issue-1", "intent-1", "source-1", time.Now().UTC())
	second := daemonTestPublicationOperation(projectID, "publication-2", "issue-2", "intent-2", "source-2", time.Now().UTC().Add(time.Second))
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), first, "candidate-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), second, "candidate-2"); err != nil {
		t.Fatal(err)
	}
	if err := d.submitPublicationOperation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := d.submitPublicationOperation(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("second publication started while target resource was occupied")
	default:
	}
	close(releaseFirst)
	<-secondStarted
	seen := map[string]bool{<-merged: true, <-merged: true}
	if err := runtime.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !seen[first.OperationID] || !seen[second.OperationID] {
		t.Fatalf("merged operations = %v", seen)
	}
	if err := runtime.manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationQueueFailureIsTypedAndRetainsArtifact(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationClose = func(context.Context, domain.PublicationOperation) error {
		return errors.New("candidate validation failed: npm test: unit suite failed")
	}
	issueClient := issues.NewClient(repo, nil)
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "publish", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-failed", issueID, "intent", "source", time.Now().UTC())
	request := protocol.OrchestrationIntentRequest{Kind: protocol.OrchestrationIntentReviewAccept, IntentKey: operation.IntentKey, ActorID: operation.ActorID, IssueIDs: []string{issueID}}
	operation.RequestFingerprint = reviewRequestFingerprint(request)
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err == nil {
		t.Fatal("failed publication returned success")
	}
	failed, found, err := runtime.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found {
		t.Fatalf("failed operation = (%+v,%t,%v)", failed, found, err)
	}
	if failed.State != domain.PublicationOperationFailed || failed.FailureKind != "validation_or_apply_failed" || failed.FailureArtifact == "" {
		t.Fatalf("failed operation = %+v", failed)
	}
	if _, err := os.Stat(failed.FailureArtifact); err != nil {
		t.Fatalf("retained failure artifact: %v", err)
	}
	if outcome, err := (daemonOrchestrationAuthority{daemon: d}).reviewIntentTerminalOutcome(context.Background(), operation.ProjectID, issueID, request); err != nil || outcome != "superseded" {
		t.Fatalf("terminal review outcome=(%q,%v), want superseded", outcome, err)
	}
}

func TestPublicationQueueRejectsStaleIdentityBeforeValidation(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	closeCalled := false
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationIdentityCheck = func(context.Context, domain.PublicationOperation) error {
		return errors.New("publication identity stale: base revision changed from base-a to base-b")
	}
	d.publicationClose = func(context.Context, domain.PublicationOperation) error {
		closeCalled = true
		return nil
	}
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-stale", "issue", "intent", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-stale"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err == nil {
		t.Fatal("stale publication returned success")
	}
	stale, found, err := runtime.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found || stale.State != domain.PublicationOperationStale || stale.FailureKind != "identity_changed" || stale.FailureArtifact == "" {
		t.Fatalf("stale publication = (%+v,%t,%v)", stale, found, err)
	}
	if closeCalled {
		t.Fatal("stale publication reached validation/apply")
	}
}

func TestPublicationQueueAutomaticallyRecomputesChangedBaseAttempt(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	merged := make(chan domain.PublicationOperation, 1)
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationIdentityCheck = func(_ context.Context, operation domain.PublicationOperation) error {
		if operation.OperationID != "publication-refresh-base" {
			return nil
		}
		replacement := refreshedPublicationOperationAttempt(operation, "base-b", operation.ValidationCommand, operation.PolicyVersion, operation.EnvironmentFingerprint)
		return &publicationRetryError{cause: errors.New("publication identity stale: base revision changed from base to base-b"), replacement: replacement}
	}
	d.publicationClose = func(context.Context, domain.PublicationOperation) error { return nil }
	d.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.State == domain.PublicationOperationMerged {
			merged <- operation
		}
	}
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-refresh-base", "issue", "intent", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-refresh-base"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err == nil {
		t.Fatal("changed-base attempt returned success instead of recording stale predecessor")
	}
	replacement := <-merged
	if replacement.OperationID == operation.OperationID || replacement.BaseRevision != "base-b" || !strings.Contains(replacement.IntentKey, ":publication-retry:") {
		t.Fatalf("replacement publication = %+v", replacement)
	}
	stale, found, err := runtime.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found || stale.State != domain.PublicationOperationStale {
		t.Fatalf("stale predecessor = (%+v,%t,%v)", stale, found, err)
	}
}

func TestRecoverAcceptedReviewPublicationCreatesCurrentBaseSuccessorFromTerminalRoot(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	root := daemonTestPublicationOperation(runtime.canonicalProject, "publication-root", "issue", "accepted-intent", "source", time.Now().UTC())
	stored, _, err := runtime.store.EnqueuePublication(context.Background(), root, publicationCoalesceKey(root))
	if err != nil {
		t.Fatal(err)
	}
	claimed, acquired, err := runtime.store.ClaimPublicationOperation(context.Background(), stored.OperationID, operationstore.PublicationOperationClaim{Owner: "daemon", Token: "claim", Now: time.Now().UTC(), TTL: time.Minute})
	if err != nil || !acquired {
		t.Fatalf("claim root = (%+v,%t,%v)", claimed, acquired, err)
	}
	if _, err = runtime.store.UpdatePublicationOperation(context.Background(), stored.OperationID, operationstore.PublicationOperationUpdate{
		ExpectedStates:     []domain.PublicationOperationState{claimed.State},
		ExpectedClaimToken: "claim",
		State:              domain.PublicationOperationFailed,
		ReleaseClaim:       true,
		UpdatedAt:          time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	prepared := root
	prepared.OperationID = "prepared-current-base"
	prepared.BaseRevision = "current-base"
	pin := acceptedReviewPin{
		Reviewer:                       domain.ReviewerIdentity{OwnerID: root.ActorID, OwnerKind: root.ReviewerKind},
		ReviewEpochEventID:             root.ReviewEpochEventID,
		AcceptedReviewEventID:          root.AcceptedReviewEventID,
		AcceptedPublicationOperationID: root.OperationID,
		SourceOID:                      root.SourceRevision,
	}
	successor, err := (&Daemon{}).recoverAcceptedReviewPublication(context.Background(), runtime.store, prepared, pin)
	if err != nil {
		t.Fatal(err)
	}
	if successor.OperationID == root.OperationID || successor.BaseRevision != "current-base" || successor.AcceptedPublicationOperationID != root.OperationID || successor.PatchEvidenceID != root.PatchEvidenceID || !strings.HasPrefix(successor.IntentKey, root.IntentKey+":publication-retry:") {
		t.Fatalf("recovered successor = %+v", successor)
	}
}

func TestPublicationQueueRefreshesExpectedBaseStaleAtAuthoritativeApply(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	merged := make(chan domain.PublicationOperation, 1)
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	t.Cleanup(d.closePublicationStores)
	d.publicationClose = func(_ context.Context, operation domain.PublicationOperation) error {
		if operation.OperationID == "publication-authoritative-base-fence" {
			return &taskCloseExpectedBaseStaleError{Expected: operation.BaseRevision, Actual: "base-b"}
		}
		return nil
	}
	d.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.State == domain.PublicationOperationMerged {
			merged <- operation
		}
	}
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-authoritative-base-fence", "issue", "intent", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-authoritative-base-fence"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err == nil {
		t.Fatal("authoritative expected-base movement returned success")
	}
	replacement := <-merged
	if replacement.OperationID == operation.OperationID || replacement.BaseRevision != "base-b" {
		t.Fatalf("replacement publication = %+v", replacement)
	}
	stale, found, err := runtime.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found || stale.State != domain.PublicationOperationStale || stale.FailureKind != "identity_changed" {
		t.Fatalf("stale authoritative predecessor = (%+v,%t,%v)", stale, found, err)
	}
}

func TestPublicationRetryAttemptGenerationSurvivesBaseIdentityCycles(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	current := daemonTestPublicationOperation(runtime.canonicalProject, "publication-base-cycle", "issue", "intent", "source", time.Now().UTC())
	current.BaseRevision = "A"
	stored, created, err := runtime.store.EnqueuePublication(context.Background(), current, publicationCoalesceKey(current))
	if err != nil || !created {
		t.Fatalf("enqueue initial A attempt = (%+v,%t,%v)", stored, created, err)
	}
	current = stored
	seen := map[string]struct{}{current.OperationID: {}}
	for index, nextBase := range []string{"B", "A", "C", "A"} {
		claimToken := fmt.Sprintf("cycle-claim-%d", index)
		claimed, acquired, claimErr := runtime.store.ClaimPublicationOperation(context.Background(), current.OperationID, operationstore.PublicationOperationClaim{Owner: "cycle-daemon", Token: claimToken, Now: time.Now().UTC(), TTL: time.Minute})
		if claimErr != nil || !acquired {
			t.Fatalf("claim %s attempt = (%+v,%t,%v)", current.BaseRevision, claimed, acquired, claimErr)
		}
		terminal, transitionErr := d.transitionPublicationOperation(context.Background(), claimed, claimToken, domain.PublicationOperationStale, func(update *operationstore.PublicationOperationUpdate) {
			update.ReleaseClaim = true
		})
		if transitionErr != nil {
			t.Fatal(transitionErr)
		}
		next := refreshedPublicationOperationAttempt(terminal, nextBase, terminal.ValidationCommand, terminal.PolicyVersion, terminal.EnvironmentFingerprint)
		if _, duplicate := seen[next.OperationID]; duplicate {
			t.Fatalf("base cycle generated duplicate operation %s for %s -> %s", next.OperationID, terminal.BaseRevision, nextBase)
		}
		seen[next.OperationID] = struct{}{}
		stored, created, err = runtime.store.EnqueuePublication(context.Background(), next, publicationCoalesceKey(next))
		if err != nil || !created || stored.State != domain.PublicationOperationQueued || stored.BaseRevision != nextBase {
			t.Fatalf("enqueue cycle successor %s = (%+v,%t,%v)", nextBase, stored, created, err)
		}
		current = stored
	}
	if len(seen) != 5 {
		t.Fatalf("attempt generations = %d, want A-B-A-C-A five distinct rows", len(seen))
	}
}

func TestPublicationStaleSuccessorSurvivesAtomicCommitCrashAndReopen(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*Daemon, domain.PublicationOperation)
	}{
		{name: "identity-check", configure: func(d *Daemon, operation domain.PublicationOperation) {
			d.publicationIdentityCheck = func(context.Context, domain.PublicationOperation) error {
				replacement := refreshedPublicationOperationAttempt(operation, "base-b", operation.ValidationCommand, operation.PolicyVersion, operation.EnvironmentFingerprint)
				return &publicationRetryError{cause: errors.New("publication identity stale: base moved"), replacement: replacement}
			}
		}},
		{name: "apply-time-expected-base", configure: func(d *Daemon, _ domain.PublicationOperation) {
			d.publicationClose = func(_ context.Context, operation domain.PublicationOperation) error {
				return &taskCloseExpectedBaseStaleError{Expected: operation.BaseRevision, Actual: "base-b"}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
			operation := daemonTestPublicationOperation(firstRuntime.canonicalProject, "publication-atomic-crash-"+test.name, "issue", "intent", "source", time.Now().UTC())
			if _, _, err := firstRuntime.store.EnqueuePublication(context.Background(), operation, publicationCoalesceKey(operation)); err != nil {
				t.Fatal(err)
			}
			retryWaiting := make(chan struct{})
			var waitOnce sync.Once
			first := &Daemon{operationRuntime: firstRuntime, cfg: Config{RepoDir: repo}}
			test.configure(first, operation)
			first.publicationContinuationSubmit = func(context.Context, domain.PublicationOperation) error {
				return errors.New("simulated crash after atomic stale successor commit")
			}
			first.publicationContinuationWait = func(ctx context.Context) error {
				waitOnce.Do(func() { close(retryWaiting) })
				<-ctx.Done()
				return ctx.Err()
			}
			if _, err := first.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err == nil {
				t.Fatal("stale predecessor returned success")
			}
			<-retryWaiting
			predecessor, found, err := firstRuntime.store.PublicationOperation(context.Background(), operation.OperationID)
			if err != nil || !found || predecessor.State != domain.PublicationOperationStale {
				t.Fatalf("atomic predecessor = (%+v,%t,%v)", predecessor, found, err)
			}
			nonterminal, err := firstRuntime.store.PublicationOperations(context.Background(), operation.ProjectID, operation.IssueID, true)
			if err != nil || len(nonterminal) != 1 || nonterminal[0].BaseRevision != "base-b" {
				t.Fatalf("atomic successor before crash = (%+v,%v)", nonterminal, err)
			}
			first.closePublicationStores()
			if err := firstRuntime.Close(); err != nil {
				t.Fatal(err)
			}

			restartedRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
			t.Cleanup(func() { _ = restartedRuntime.Close() })
			merged := make(chan domain.PublicationOperation, 1)
			restarted := &Daemon{operationRuntime: restartedRuntime, cfg: Config{RepoDir: repo}, publicationClose: func(context.Context, domain.PublicationOperation) error { return nil }}
			restarted.publicationStateChanged = func(operation domain.PublicationOperation) {
				if operation.State == domain.PublicationOperationMerged {
					merged <- operation
				}
			}
			restarted.recoverPublicationOperations(context.Background())
			got := <-merged
			if err := restartedRuntime.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got.BaseRevision != "base-b" || got.OperationID == operation.OperationID {
				t.Fatalf("recovered atomic successor = %+v", got)
			}
			if err := restartedRuntime.manager.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicationTargetContinuationRetriesTransientSubmitWithoutRestart(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	now := time.Now().UTC()
	firstOperation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-retry-submit-first", "issue-first", "intent-first", "source-first", now)
	secondOperation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-retry-submit-second", "issue-second", "intent-second", "source-second", now.Add(time.Second))
	for _, operation := range []domain.PublicationOperation{firstOperation, secondOperation} {
		if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, publicationCoalesceKey(operation)); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}, publicationClose: func(context.Context, domain.PublicationOperation) error { return nil }}
	retryWaiting := make(chan struct{})
	releaseRetry := make(chan struct{})
	secondMerged := make(chan struct{})
	var waitOnce sync.Once
	var submitAttempts atomic.Int32
	d.publicationContinuationSubmit = func(ctx context.Context, operation domain.PublicationOperation) error {
		if submitAttempts.Add(1) == 1 {
			return errors.New("injected transient continuation submit failure")
		}
		return d.submitPublicationOperation(ctx, operation)
	}
	d.publicationContinuationWait = func(ctx context.Context) error {
		waitOnce.Do(func() { close(retryWaiting) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseRetry:
			return nil
		}
	}
	d.publicationStateChanged = func(operation domain.PublicationOperation) {
		if operation.OperationID == secondOperation.OperationID && operation.State == domain.PublicationOperationMerged {
			close(secondMerged)
		}
	}
	if _, err := d.runPublicationOperation(context.Background(), firstOperation.ProjectID, firstOperation.OperationID); err != nil {
		t.Fatal(err)
	}
	<-retryWaiting
	queued, found, err := runtime.store.PublicationOperation(context.Background(), secondOperation.OperationID)
	if err != nil || !found || queued.State != domain.PublicationOperationQueued {
		t.Fatalf("queued successor during transient failure = (%+v,%t,%v)", queued, found, err)
	}
	close(releaseRetry)
	<-secondMerged
	if got := submitAttempts.Load(); got != 2 {
		t.Fatalf("continuation submit attempts = %d, want one failure and one daemon retry", got)
	}
	if err := runtime.manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationRecoveryRetriesTransientSubmitOnceWithoutRestart(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-recovery-retry", "issue", "intent", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, publicationCoalesceKey(operation)); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}, publicationClose: func(context.Context, domain.PublicationOperation) error { return nil }}
	retryWaiting := make(chan struct{})
	releaseRetry := make(chan struct{})
	merged := make(chan struct{})
	var waitOnce sync.Once
	var mergeOnce sync.Once
	var attempts atomic.Int32
	d.publicationRecoverySubmit = func(ctx context.Context, recovered domain.PublicationOperation) error {
		if attempts.Add(1) <= 2 {
			return errors.New("injected transient startup submit failure")
		}
		return d.submitPublicationOperation(ctx, recovered)
	}
	d.publicationRecoveryWait = func(ctx context.Context) error {
		waitOnce.Do(func() { close(retryWaiting) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseRetry:
			return nil
		}
	}
	d.publicationStateChanged = func(current domain.PublicationOperation) {
		if current.OperationID == operation.OperationID && current.State == domain.PublicationOperationMerged {
			mergeOnce.Do(func() { close(merged) })
		}
	}
	d.recoverPublicationOperations(context.Background())
	<-retryWaiting
	d.recoverPublicationOperations(context.Background())
	if got := attempts.Load(); got != 2 {
		// The second recovery scan may submit through the operation manager, but
		// the durable claim and manager dedupe still fence execution to one runner.
		t.Fatalf("recovery submit attempts before retry release = %d, want 2 scans with one fenced retry runner", got)
	}
	close(releaseRetry)
	<-merged
	if got := attempts.Load(); got != 3 {
		t.Fatalf("recovery submit attempts = %d, want two startup scans and one retry", got)
	}
	if err := runtime.manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.closePublicationStores()
}

func TestPublicationRecoveryRetryStopsOnShutdown(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-recovery-shutdown", "issue", "intent", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), operation, publicationCoalesceKey(operation)); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	retryWaiting := make(chan struct{})
	var waitOnce sync.Once
	var attempts atomic.Int32
	d.publicationRecoverySubmit = func(context.Context, domain.PublicationOperation) error {
		attempts.Add(1)
		return errors.New("injected persistent startup submit failure")
	}
	d.publicationRecoveryWait = func(ctx context.Context) error {
		waitOnce.Do(func() { close(retryWaiting) })
		<-ctx.Done()
		return ctx.Err()
	}
	d.recoverPublicationOperations(context.Background())
	<-retryWaiting
	d.closePublicationStores()
	if got := attempts.Load(); got != 1 {
		t.Fatalf("recovery submit attempts after shutdown = %d, want initial attempt only", got)
	}
}

func TestPublicationQueueRecoveryResubmitsNonterminalIntent(t *testing.T) {
	for _, crashState := range []domain.PublicationOperationState{
		domain.PublicationOperationQueued,
		domain.PublicationOperationPreparing,
		domain.PublicationOperationValidating,
		domain.PublicationOperationPassed,
	} {
		t.Run(string(crashState), func(t *testing.T) {
			repo := t.TempDir()
			firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
			operation := daemonTestPublicationOperation(firstRuntime.canonicalProject, "publication-recover", "issue", "intent", "source", time.Now().UTC())
			stored, _, err := firstRuntime.store.EnqueuePublication(context.Background(), operation, "candidate-recover")
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now().UTC().Add(-10 * time.Minute)
			if crashState != domain.PublicationOperationQueued {
				stored, _, err = firstRuntime.store.ClaimPublicationOperation(context.Background(), stored.OperationID, operationstore.PublicationOperationClaim{Owner: "crashed-daemon", Token: "crashed-claim", Now: started, TTL: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				for _, next := range []domain.PublicationOperationState{domain.PublicationOperationValidating, domain.PublicationOperationPassed} {
					if stored.State == crashState {
						break
					}
					stored, err = firstRuntime.store.UpdatePublicationOperation(context.Background(), stored.OperationID, operationstore.PublicationOperationUpdate{ExpectedStates: []domain.PublicationOperationState{stored.State}, ExpectedClaimToken: "crashed-claim", State: next, StartedAt: &started, UpdatedAt: started.Add(time.Second)})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := firstRuntime.Close(); err != nil {
				t.Fatal(err)
			}

			restarted := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
			t.Cleanup(func() { _ = restarted.Close() })
			merged := make(chan struct{})
			d := &Daemon{operationRuntime: restarted, cfg: Config{RepoDir: repo, ScopedRuntime: true}}
			d.publicationClose = func(context.Context, domain.PublicationOperation) error { return nil }
			d.publicationStateChanged = func(operation domain.PublicationOperation) {
				if operation.State == domain.PublicationOperationMerged {
					close(merged)
				}
			}
			d.recoverPublicationOperations(context.Background())
			<-merged
			recovered, found, err := restarted.store.PublicationOperation(context.Background(), operation.OperationID)
			if err != nil || !found || recovered.State != domain.PublicationOperationMerged {
				t.Fatalf("recovered operation = (%+v,%t,%v)", recovered, found, err)
			}
			if err := restarted.manager.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicationQueueRestartRecoveryPreservesTicketIdentity(t *testing.T) {
	repo := t.TempDir()
	runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".azedarach/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "add", "base.txt", ".gitignore")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
	runDaemonTestGit(t, repo, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "add", "feature.txt")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "feature")
	runDaemonTestGit(t, repo, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "add", "main.txt")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "main")

	firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	operation := daemonTestPublicationOperation(firstRuntime.canonicalProject, "publication-ticket-recovery", "issue-recovery", "intent", "source", time.Now().UTC())
	operation.ValidationCommand = `test "$AZEDARACH_TICKET_ID" = issue-recovery && test "$AZEDARACH_ISSUE_ID" = issue-recovery`
	if _, _, err := firstRuntime.store.EnqueuePublication(context.Background(), operation, "candidate-ticket-recovery"); err != nil {
		t.Fatal(err)
	}
	if err := firstRuntime.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = restarted.Close() })
	recovered, found, err := restarted.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found || recovered.IssueID != "issue-recovery" {
		t.Fatalf("reopened ticket publication = (%+v,%t,%v)", recovered, found, err)
	}
	gitClient := gitservice.NewClient(gitservice.NewExecRunner(repo), slog.Default())
	d := &Daemon{
		operationRuntime: restarted,
		cfg:              Config{RepoDir: repo},
		git:              gitClient,
		publicationIdentityCheck: func(context.Context, domain.PublicationOperation) error {
			return nil
		},
	}
	d.publicationClose = func(ctx context.Context, _ domain.PublicationOperation) error {
		result, err := gitClient.MergeCleanlyTransactional(ctx, repo, "feature")
		if err != nil {
			return err
		}
		if result == nil || !result.Success || len(result.ValidationAttempts) != 1 {
			return fmt.Errorf("recovered candidate validation = %+v", result)
		}
		return nil
	}
	if _, err := d.runPublicationOperation(context.Background(), operation.ProjectID, operation.OperationID); err != nil {
		t.Fatal(err)
	}
	recovered, found, err = restarted.store.PublicationOperation(context.Background(), operation.OperationID)
	if err != nil || !found || recovered.State != domain.PublicationOperationMerged || recovered.ValidationRequestID == "" {
		t.Fatalf("recovered ticket publication = (%+v,%t,%v)", recovered, found, err)
	}
}

func TestPublicationCandidateAdmissionRecordsAndReusesExactEvidence(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	first := daemonTestPublicationOperation(runtime.canonicalProject, "publication-admit-1", "issue-1", "intent-1", "source", time.Now().UTC())
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), first, "candidate-admit-1"); err != nil {
		t.Fatal(err)
	}
	firstStoredClaim, acquired, err := runtime.store.ClaimPublicationOperation(context.Background(), first.OperationID, operationstore.PublicationOperationClaim{Owner: "daemon", Token: "claim-first", Now: time.Now().UTC(), TTL: time.Minute})
	if err != nil || !acquired {
		t.Fatalf("claim first publication = (%+v,%t,%v)", firstStoredClaim, acquired, err)
	}
	reused, finish, err := d.publicationCandidateAdmission(first.ProjectID, first.OperationID, "claim-first", time.Minute)(context.Background(), "candidate-head")
	if err != nil || reused || finish == nil {
		t.Fatalf("first admission = (reused=%t, finish=%t, err=%v)", reused, finish != nil, err)
	}
	if err := finish(domain.IntegrationCandidateValidationAttempt{CandidateHead: "candidate-head", Status: domain.IntegrationCandidateValidationPassed}); err != nil {
		t.Fatal(err)
	}
	firstStored, found, err := runtime.store.PublicationOperation(context.Background(), first.OperationID)
	if err != nil || !found || firstStored.ValidationRequestID == "" {
		t.Fatalf("first publication validation identity = (%+v,%t,%v)", firstStored, found, err)
	}
	if _, err := d.transitionPublicationOperation(context.Background(), firstStored, "claim-first", domain.PublicationOperationMerged, func(update *operationstore.PublicationOperationUpdate) {
		update.ReleaseClaim = true
	}); err != nil {
		t.Fatal(err)
	}

	second := daemonTestPublicationOperation(runtime.canonicalProject, "publication-admit-2", "issue-2", "intent-2", "source", time.Now().UTC().Add(time.Second))
	if _, _, err := runtime.store.EnqueuePublication(context.Background(), second, "candidate-admit-2"); err != nil {
		t.Fatal(err)
	}
	secondStoredClaim, acquired, err := runtime.store.ClaimPublicationOperation(context.Background(), second.OperationID, operationstore.PublicationOperationClaim{Owner: "daemon", Token: "claim-second", Now: time.Now().UTC(), TTL: time.Minute})
	if err != nil || !acquired {
		t.Fatalf("claim second publication = (%+v,%t,%v)", secondStoredClaim, acquired, err)
	}
	reused, finish, err = d.publicationCandidateAdmission(second.ProjectID, second.OperationID, "claim-second", time.Minute)(context.Background(), "candidate-head")
	if err != nil || !reused || finish != nil {
		t.Fatalf("reused admission = (reused=%t, finish=%t, err=%v)", reused, finish != nil, err)
	}
	secondStored, found, err := runtime.store.PublicationOperation(context.Background(), second.OperationID)
	if err != nil || !found || secondStored.ReusedEvidenceID != firstStored.ValidationRequestID {
		t.Fatalf("reused publication validation identity = (%+v,%t,%v), want %s", secondStored, found, err, firstStored.ValidationRequestID)
	}
}

func TestPublicationValidationClaimGenerationFencesExpiryOverlap(t *testing.T) {
	repo := t.TempDir()
	firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	secondRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = firstRuntime.Close() })
	t.Cleanup(func() { _ = secondRuntime.Close() })

	now := time.Now().UTC()
	claimTTL := time.Minute
	claimNow := now
	operation := daemonTestPublicationOperation(firstRuntime.canonicalProject, "publication-validation-generation", "issue", "intent", "source", now)
	stored, _, err := firstRuntime.store.EnqueuePublication(context.Background(), operation, "candidate-validation-generation")
	if err != nil {
		t.Fatal(err)
	}
	claimedA, acquired, err := firstRuntime.store.ClaimPublicationOperation(context.Background(), stored.OperationID, operationstore.PublicationOperationClaim{Owner: "daemon-a", Token: "claim-a", Now: claimNow, TTL: claimTTL})
	if err != nil || !acquired {
		t.Fatalf("claim generation A = (%+v,%t,%v)", claimedA, acquired, err)
	}

	first := &Daemon{operationRuntime: firstRuntime, cfg: Config{RepoDir: repo}, publicationClaimNow: func() time.Time { return claimNow }}
	second := &Daemon{operationRuntime: secondRuntime, cfg: Config{RepoDir: repo}, publicationClaimNow: func() time.Time { return claimNow }}
	var commandCount atomic.Int32
	var applyCount atomic.Int32
	reusedA, finishA, err := first.publicationCandidateAdmission(operation.ProjectID, operation.OperationID, "claim-a", claimTTL)(context.Background(), "candidate-head")
	if err != nil || reusedA || finishA == nil {
		t.Fatalf("generation A admission = (reused=%t, finish=%t, err=%v)", reusedA, finishA != nil, err)
	}
	commandCount.Add(1) // Generation A is now inside the configured validator.

	claimNow = now.Add(claimTTL)
	claimedB, acquired, err := secondRuntime.store.ClaimPublicationOperation(context.Background(), stored.OperationID, operationstore.PublicationOperationClaim{Owner: "daemon-b", Token: "claim-b", Now: claimNow, TTL: claimTTL})
	if err != nil || !acquired {
		t.Fatalf("reclaim generation B = (%+v,%t,%v)", claimedB, acquired, err)
	}
	waiting := make(chan struct{})
	releaseWait := make(chan struct{})
	var waitOnce sync.Once
	second.publicationValidationWait = func(ctx context.Context, _ string) error {
		waitOnce.Do(func() { close(waiting) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseWait:
			return nil
		}
	}
	type admissionResult struct {
		reused bool
		finish func(gitservice.CandidateValidationAttempt) error
		err    error
	}
	admittedB := make(chan admissionResult, 1)
	go func() {
		reused, finish, admissionErr := second.publicationCandidateAdmission(operation.ProjectID, operation.OperationID, "claim-b", claimTTL)(context.Background(), "candidate-head")
		admittedB <- admissionResult{reused: reused, finish: finish, err: admissionErr}
	}()
	<-waiting // Deterministic barrier: B observed A's active generation and cannot enter its command.
	if got := commandCount.Load(); got != 1 {
		t.Fatalf("validator command count while B awaits A = %d, want 1", got)
	}

	if err := finishA(domain.IntegrationCandidateValidationAttempt{CandidateHead: "candidate-head", Status: domain.IntegrationCandidateValidationPassed}); err == nil {
		t.Fatal("generation A was not fenced after publishing terminal validation evidence")
	}
	close(releaseWait)
	resultB := <-admittedB
	if resultB.err != nil || !resultB.reused || resultB.finish != nil {
		t.Fatalf("generation B admission after A terminal = (reused=%t, finish=%t, err=%v)", resultB.reused, resultB.finish != nil, resultB.err)
	}
	if got := commandCount.Load(); got != 1 {
		t.Fatalf("validator command count = %d, want 1", got)
	}
	if _, err := first.renewPublicationClaim(context.Background(), firstRuntime.store, operation.OperationID, "claim-a", claimTTL); err == nil {
		applyCount.Add(1)
		t.Fatal("expired generation A retained publication apply authority")
	}
	current, err := second.renewPublicationClaim(context.Background(), secondRuntime.store, operation.OperationID, "claim-b", claimTTL)
	if err != nil {
		t.Fatal(err)
	}
	applyCount.Add(1)
	if _, err := second.transitionPublicationOperation(context.Background(), current, "claim-b", domain.PublicationOperationMerged, func(update *operationstore.PublicationOperationUpdate) {
		update.ReleaseClaim = true
	}); err != nil {
		t.Fatal(err)
	}
	if got := applyCount.Load(); got != 1 {
		t.Fatalf("publication apply count = %d, want 1", got)
	}
}

func TestPublicationValidationWaitReconcilesExpiredPriorExecutor(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	ttl := time.Minute
	request, err := runtime.store.AcquireValidation(context.Background(), domain.ValidationAcquire{
		RequestID: "expired-prior-publication", LeaseToken: "expired-prior-token", ProjectID: runtime.canonicalProject,
		Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate,
		IsolationMode: "synthetic-worktree", EnvironmentFingerprint: "test", Override: domain.ValidationOverrideNone,
		Profile: "publication:test", Command: "go test ./consumer/...", SourceRevision: "candidate", TTL: ttl,
	}, time.Now().UTC().Add(-2*ttl))
	if err != nil || request.State != domain.ValidationRequestActive {
		t.Fatalf("seed expired prior validation = (%+v,%v)", request, err)
	}
	d := &Daemon{operationRuntime: runtime, cfg: Config{RepoDir: repo}}
	d.publicationValidationWait = func(context.Context, string) error {
		t.Fatal("expired prior executor remained active after reconciliation")
		return nil
	}
	if err := d.awaitPriorPublicationValidation(context.Background(), runtime.canonicalProject, request.RequestID); err != nil {
		t.Fatal(err)
	}
	reconciled, err := runtime.store.ValidationRequest(context.Background(), runtime.canonicalProject, request.RequestID)
	if err != nil || reconciled.State != domain.ValidationRequestExpired {
		t.Fatalf("reconciled prior validation = (%+v,%v), want expired", reconciled, err)
	}
}

func TestPublicationValidationProfileReusesCompatibleExactReviewEvidence(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	now := time.Now().UTC()
	request, err := runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{
		RequestID: "review-exact", LeaseToken: "review-token", ProjectID: runtime.canonicalProject, IssueID: "issue",
		IssuePriority: domain.P1, IssuePriorityResolved: true, Class: domain.ValidationClassAggregate,
		Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence,
		IsolationMode: "synthetic-worktree", EnvironmentFingerprint: "portable-env", Override: domain.ValidationOverrideNone,
		Profile: "portable-review", Command: "npm run verify", SourceRevision: "candidate", ReviewerID: "reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator, ReviewEpochEventID: 42, PublicationOperationID: "publication", AcceptedReviewEventID: 43, AcceptedPublicationOperationID: "patch", TTL: time.Minute,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	evidence := domain.ValidationEvidence{Held: true, Present: true, RequestID: request.RequestID, Class: request.Class, Scope: request.Scope, Purpose: request.Purpose, Execution: request.Execution, Profile: request.Profile, SourceRevision: request.SourceRevision}
	if _, err := runtime.store.FinishValidation(ctx, request.RequestID, "review-token", domain.ValidationRequestCompleted, "clean", evidence, now.Add(time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{operationRuntime: runtime}
	operation := domain.PublicationOperation{OperationID: "publication", ProjectID: runtime.canonicalProject, IssueID: "issue", ActorID: "reviewer", ReviewerKind: "orchestrator", ReviewEpochEventID: 42, AcceptedReviewEventID: 43, PatchEvidenceID: "patch", AcceptedPublicationOperationID: "patch", PolicyVersion: "v1", ValidationCommand: "npm run verify", EnvironmentFingerprint: "portable-env"}
	if got := d.publicationValidationProfile(ctx, operation, "candidate"); got != "portable-review" {
		t.Fatalf("compatible profile = %q, want portable-review", got)
	}
	if got := d.publicationValidationProfile(ctx, operation, "different"); got != "publication:v1" {
		t.Fatalf("incompatible revision profile = %q, want publication:v1", got)
	}
}

func TestPublicationTransitionInvalidatesSnapshotsAndPublishesWatchEvent(t *testing.T) {
	repo := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repo})
	t.Cleanup(func() { _ = runtime.Close() })
	hub := publish.NewHub(8, 8, nil)
	d := &Daemon{
		operationRuntime: runtime, cfg: Config{RepoDir: repo}, hub: hub,
		orchestrationSnapshotCache: map[string]orchestrationSnapshotCacheEntry{
			runtime.canonicalProject + "\x00scope": {},
		},
	}
	events, unsubscribe := hub.Subscribe(runtime.canonicalProject, 0)
	t.Cleanup(unsubscribe)
	operation := daemonTestPublicationOperation(runtime.canonicalProject, "publication-event", "issue", "intent", "source", time.Now().UTC())
	stored, _, err := runtime.store.EnqueuePublication(context.Background(), operation, "candidate-event")
	if err != nil {
		t.Fatal(err)
	}
	claimed, acquired, err := runtime.store.ClaimPublicationOperation(context.Background(), stored.OperationID, operationstore.PublicationOperationClaim{Owner: "daemon", Token: "claim-event", Now: time.Now().UTC(), TTL: time.Minute})
	if err != nil || !acquired {
		t.Fatalf("claim event publication = (%+v,%t,%v)", claimed, acquired, err)
	}
	updated, err := d.transitionPublicationOperation(context.Background(), claimed, "claim-event", domain.PublicationOperationPreparing, nil)
	if err != nil {
		t.Fatal(err)
	}
	event := <-events
	if event.Event != protocol.EventPublicationOperationUpdated || event.Revision != 1 {
		t.Fatalf("publication event = %+v", event)
	}
	var projected domain.PublicationOperation
	if err := json.Unmarshal(event.Body, &projected); err != nil || projected.OperationID != updated.OperationID || projected.State != updated.State {
		t.Fatalf("publication event body = (%+v,%v)", projected, err)
	}
	if len(d.orchestrationSnapshotCache) != 0 {
		t.Fatalf("orchestration snapshot cache retained publication state: %+v", d.orchestrationSnapshotCache)
	}
}

func TestPublicationStoreRoutesRegisteredProjectIntentAtomically(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AZEDARACH_DISABLE_USER_DB", "1")
	defaultRepo := filepath.Join(home, "default")
	registeredRepo := filepath.Join(home, "consumer")
	for _, repo := range []string{defaultRepo, registeredRepo} {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defaultID, err := appconfig.ProjectIDForRoot(defaultRepo)
	if err != nil {
		t.Fatal(err)
	}
	registeredID, err := appconfig.ProjectIDForRoot(registeredRepo)
	if err != nil {
		t.Fatal(err)
	}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{DefaultProject: "Default", Projects: []appconfig.Project{
		{ID: defaultID, Name: "Default", Path: defaultRepo},
		{ID: registeredID, Name: "Consumer", Path: registeredRepo},
	}}); err != nil {
		t.Fatal(err)
	}
	d := New(Config{RepoDir: defaultRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(func() {
		d.closePublicationStores()
		d.closeIssueClients()
		_ = d.operationRuntime.Close()
	})
	issueClient := d.issueClientForProject(registeredID)
	if issueClient == nil {
		t.Fatal("registered issue client unavailable")
	}
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "consumer publish", Description: "consumer", Acceptance: "published", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	operation := daemonTestPublicationOperation(registeredID, "publication-registered", issueID, "intent", "source", time.Now().UTC())
	registeredStore, err := d.publicationStoreForProject(registeredID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registeredStore.PublicationOperations(context.Background(), registeredID, "", false); err != nil {
		t.Fatal(err)
	}
	params := issues.IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
		"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint,
	}}
	admission, err := issueClient.CaptureReviewAdmissionPin(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	operation.ReviewEpochEventID = admission.ReviewEpochEventID
	operation.AcceptedReviewEventID = 0
	if _, err := issueClient.ClaimOwnershipWithRuntime(context.Background(), registeredID, issueID, issues.OwnershipClaimParams{
		OwnerID: "reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview,
		ExpectedReviewAdmission: &admission, ReviewSourceOID: operation.SourceRevision,
	}); err != nil {
		t.Fatal(err)
	}
	operation.ReviewerKind, operation.ReviewEpochEventID, operation.PatchEvidenceID = "orchestrator", admission.ReviewEpochEventID, "registered-patch-evidence"
	params.Payload["actor_id"], params.Payload["review_epoch_event_id"] = operation.ActorID, operation.ReviewEpochEventID
	patchEvidence := domain.PublicationEvidence{EvidenceID: operation.PatchEvidenceID, ProjectID: operation.ProjectID, IssueID: operation.IssueID, Layer: domain.PublicationEvidencePatchReview, PatchDigest: "patch", SourceRevision: operation.SourceRevision, BaseRevision: operation.BaseRevision, Producer: "reviewer:reviewer", PolicyVersion: operation.PolicyVersion, EnvironmentFingerprint: operation.EnvironmentFingerprint, CreatedAt: operation.CreatedAt}
	receipt, err := issueClient.AppendAcceptedReviewAndPublicationWithReviewAdmission(context.Background(), issueID, params, operation, patchEvidence, "registered-candidate", admission, "", "reviewer")
	if err != nil || receipt.PublicationOperationID != operation.OperationID {
		t.Fatalf("registered atomic enqueue = (%q,%v)", receipt.PublicationOperationID, err)
	}
	if _, found, err := registeredStore.PublicationOperation(context.Background(), operation.OperationID); err != nil || !found {
		t.Fatalf("registered publication = (found=%t, err=%v)", found, err)
	}
	if _, found, err := d.operationRuntime.store.PublicationOperation(context.Background(), operation.OperationID); err != nil || found {
		t.Fatalf("default store publication = (found=%t, err=%v), want isolated", found, err)
	}
}

func TestPublicationValidationIdentityIsOrderStableAndConsumerNeutral(t *testing.T) {
	left := &appconfig.Config{Gate: appconfig.GateConfig{Stages: []appconfig.GateStageConfig{
		{ID: "package", Command: "acme package", DependsOn: []string{"inspect"}, Resources: []string{"disk", "cpu"}},
		{ID: "inspect", Command: "acme inspect"},
	}}}
	right := &appconfig.Config{Gate: appconfig.GateConfig{Stages: []appconfig.GateStageConfig{
		{ID: "inspect", Command: "acme inspect"},
		{ID: "package", Command: "acme package", DependsOn: []string{"inspect"}, Resources: []string{"cpu", "disk"}},
	}}}
	if got, want := publicationValidationIdentity(left), publicationValidationIdentity(right); got != want || !strings.HasPrefix(got, "stage-dag:") {
		t.Fatalf("stage DAG identities = (%q, %q), want equal typed identities", got, want)
	}
	right.Gate.Stages[1].Command = "acme package --strict"
	if publicationValidationIdentity(left) == publicationValidationIdentity(right) {
		t.Fatal("stage command change retained reusable validation identity")
	}
}

func TestPublicationValidationStagesRequireExactPinnedDAGCapability(t *testing.T) {
	cfg := &appconfig.Config{Gate: appconfig.GateConfig{Stages: []appconfig.GateStageConfig{{ID: "dogfood-only", Command: "consumer-tool verify"}}}}
	if stages := publicationValidationStagesForOperation(cfg, "true"); len(stages) != 0 {
		t.Fatalf("foreign consumer command inherited ambient stage DAG: %+v", stages)
	}
	if stages := publicationValidationStagesForOperation(cfg, "stage-dag:stale"); len(stages) != 0 {
		t.Fatalf("stale stage identity inherited changed DAG: %+v", stages)
	}
	identity := publicationValidationIdentity(cfg)
	if stages := publicationValidationStagesForOperation(cfg, identity); len(stages) != 1 || stages[0].ID != "dogfood-only" {
		t.Fatalf("exact pinned stage capability = %+v, want configured DAG", stages)
	}
}

func daemonTestPublicationOperation(projectID, operationID, issueID, intent, source string, created time.Time) domain.PublicationOperation {
	return domain.PublicationOperation{
		OperationID: operationID, ProjectID: projectID, IssueID: issueID, IntentKey: intent,
		RequestFingerprint: "fingerprint", ActorID: "reviewer", ReviewerKind: "orchestrator", ReviewEpochEventID: 42, AcceptedReviewEventID: 43, PatchEvidenceID: "patch-evidence", AcceptedPublicationOperationID: operationID, TargetID: "base", TargetBranch: "main",
		SourceRevision: source, BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "go:test",
		ValidationCommand: "npm test", EvidenceDigest: "evidence", State: domain.PublicationOperationQueued, CreatedAt: created,
	}
}
