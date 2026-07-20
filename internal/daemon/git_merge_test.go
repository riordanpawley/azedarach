package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type typedMergeFixture struct {
	t            *testing.T
	ctx          context.Context
	projectID    string
	repo         string
	daemon       *Daemon
	issues       *issues.Client
	runtime      *daemonstate.RuntimeStateStore
	worktreeByID map[string]git.Worktree
	gateMarker   string
}

func newTypedMergeFixture(t *testing.T, withFailingGate bool) *typedMergeFixture {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	runDaemonTestGit(t, repo, "init", "-q", "-b", "main")
	runDaemonTestGit(t, repo, "config", "user.email", "test@example.com")
	runDaemonTestGit(t, repo, "config", "user.name", "Test User")
	gateMarker := filepath.Join(t.TempDir(), "publication-gate-ran")
	if withFailingGate {
		if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		gate := "#!/bin/sh\nprintf ran >\"$AZEDARACH_TEST_GATE_MARKER\"\nexit 91\n"
		if err := os.WriteFile(filepath.Join(repo, "scripts", "git-merge-rebase-gate.sh"), []byte(gate), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".azedarach/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDaemonTestGit(t, repo, "add", ".")
	runDaemonTestGit(t, repo, "commit", "-q", "-m", "base")
	t.Setenv("AZEDARACH_TEST_GATE_MARKER", gateMarker)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	issueClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), logger)
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), logger)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	projectID, err := appconfig.ProjectIDForRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	manager := git.NewWorktreeManager(git.NewExecRunner(repo), repo, logger)
	d := &Daemon{
		cfg:                       Config{RepoDir: repo, BaseBranch: "main", GitWorkflowMode: "local", Logger: logger},
		git:                       git.NewClient(git.NewExecRunner(repo), logger),
		issues:                    issueClient,
		issueClientsByProject:     map[string]*issues.Client{projectID: issueClient},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
		baseBranchByProject:       map[string]string{projectID: "main"},
		workflowModeByProject:     map[string]string{projectID: "local"},
		revision:                  map[string]uint64{projectID: 1},
	}
	d.worktreeAdapter = &worktreeServiceAdapter{
		managerForProject:           func(string) *git.WorktreeManager { return manager },
		runtimeStateStore:           runtimeStore,
		runtimeStateStoreForProject: func(string) *daemonstate.RuntimeStateStore { return runtimeStore },
		logger:                      logger,
		pollInterval:                time.Hour,
	}
	t.Cleanup(func() {
		d.worktreeAdapter.mu.Lock()
		defer d.worktreeAdapter.mu.Unlock()
		for _, cancel := range d.worktreeAdapter.pollers {
			cancel()
		}
	})
	return &typedMergeFixture{t: t, ctx: ctx, projectID: projectID, repo: repo, daemon: d, issues: issueClient, runtime: runtimeStore, worktreeByID: make(map[string]git.Worktree), gateMarker: gateMarker}
}

func (f *typedMergeFixture) createIssue(title string, parentID *string) string {
	f.t.Helper()
	id, err := f.issues.Create(f.ctx, issues.CreateTaskParams{Title: title, Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: parentID})
	if err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(f.repo), filepath.Base(f.repo)+"-"+id)
	branch := "test/" + id
	runDaemonTestGit(f.t, f.repo, "worktree", "add", "-q", "-b", branch, path, "main")
	worktree := git.Worktree{IssueID: id, Path: path, Branch: branch}
	f.worktreeByID[id] = worktree
	if err := f.runtime.UpsertWorktreeState(f.ctx, daemonstate.WorktreeState{ProjectID: f.projectID, IssueID: id, Path: path, Branch: branch, UpdatedAt: time.Now().UTC()}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *typedMergeFixture) commit(issueID, filename string) string {
	f.t.Helper()
	worktree := f.worktreeByID[issueID]
	if err := os.WriteFile(filepath.Join(worktree.Path, filename), []byte(issueID+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	runDaemonTestGit(f.t, worktree.Path, "add", filename)
	runDaemonTestGit(f.t, worktree.Path, "commit", "-q", "-m", "change "+issueID)
	return runDaemonTestGitOutput(f.t, worktree.Path, "rev-parse", "HEAD")
}

func TestTypedGitMergeChildToParentComposesNoFFWithoutPublicationAndReplaysReceipt(t *testing.T) {
	f := newTypedMergeFixture(t, true)
	parentID := f.createIssue("parent", nil)
	childID := f.createIssue("child", &parentID)
	sourceOID := f.commit(childID, "child.txt")

	result, err := f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: childID, TargetID: parentID})
	if err != nil {
		t.Fatalf("child-to-parent typed merge: %v", err)
	}
	if result.ConfiguredBaseTarget || result.SourceID != childID || result.TargetID != parentID || result.SourceOID != sourceOID || !result.ReceiptRecorded || len(result.Result.ValidationAttempts) != 0 {
		t.Fatalf("composition result = %+v", result)
	}
	if _, err := os.Stat(f.gateMarker); !os.IsNotExist(err) {
		t.Fatalf("composition invoked publication gate: %v", err)
	}
	parents := strings.Fields(runDaemonTestGitOutput(t, f.worktreeByID[parentID].Path, "rev-list", "--parents", "-n", "1", result.TargetOID))
	if len(parents) != 3 {
		t.Fatalf("composition commit parents = %v, want no-ff two-parent merge", parents)
	}

	replayed, err := f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: childID, TargetID: parentID})
	if err != nil {
		t.Fatalf("replay typed merge: %v", err)
	}
	if replayed.TargetOID != result.TargetOID || !strings.Contains(replayed.Result.Message, "receipt already applied") {
		t.Fatalf("replay = %+v, want exact receipt reuse for %s", replayed, result.TargetOID)
	}
	events, err := f.issues.ListIssueObservationEvents(f.ctx, childID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10})
	if err != nil || len(events) != 1 {
		t.Fatalf("integration receipts = %+v err=%v, want exactly one", events, err)
	}
}

func TestTypedGitMergeDoesNotDependOnUnrelatedRuntimeReconciliation(t *testing.T) {
	f := newTypedMergeFixture(t, false)
	parentID := f.createIssue("parent", nil)
	childID := f.createIssue("child", &parentID)
	f.commit(childID, "child.txt")
	f.daemon.runtimeReconciler = &runtimeReconcileRecorder{err: errors.New("unrelated runtime inventory unavailable")}

	if _, err := f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: childID, TargetID: parentID}); err != nil {
		t.Fatalf("projection-authoritative typed merge inherited runtime failure: %v", err)
	}
	calls, _ := f.daemon.runtimeReconciler.(*runtimeReconcileRecorder).snapshot()
	if calls != 0 {
		t.Fatalf("typed merge runtime reconcile calls = %d, want projection-only freshness", calls)
	}
}

func TestTypedGitMergeAncestorToDescendantUsesCompositionAndRejectsUnrelatedTarget(t *testing.T) {
	f := newTypedMergeFixture(t, true)
	ancestorID := f.createIssue("ancestor", nil)
	descendantID := f.createIssue("descendant", &ancestorID)
	unrelatedID := f.createIssue("unrelated", nil)
	provenanceID := f.createIssue("created from ancestor", nil)
	if err := f.issues.AddDependency(f.ctx, provenanceID, ancestorID, string(domain.DependencyCreatedIn)); err != nil {
		t.Fatal(err)
	}
	f.commit(ancestorID, "ancestor.txt")

	result, err := f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: ancestorID, TargetID: descendantID})
	if err != nil || result.ConfiguredBaseTarget || len(result.Result.ValidationAttempts) != 0 {
		t.Fatalf("ancestor-to-descendant composition = %+v err=%v", result, err)
	}
	if _, err := os.Stat(f.gateMarker); !os.IsNotExist(err) {
		t.Fatalf("ancestor-to-descendant composition invoked publication gate: %v", err)
	}
	before := runDaemonTestGitOutput(t, f.worktreeByID[unrelatedID].Path, "rev-parse", "HEAD")
	stopCalls := 0
	f.daemon.typedMergeStopTarget = func(context.Context, string, string) error {
		stopCalls++
		return nil
	}
	_, err = f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: ancestorID, TargetID: unrelatedID, StopTargetSession: true})
	if err == nil || !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("unrelated target error = %v", err)
	}
	if stopCalls != 0 {
		t.Fatalf("stale relationship stopped target session %d time(s)", stopCalls)
	}
	after := runDaemonTestGitOutput(t, f.worktreeByID[unrelatedID].Path, "rev-parse", "HEAD")
	if after != before {
		t.Fatalf("unrelated target mutated: before=%s after=%s", before, after)
	}
	_, err = f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: ancestorID, TargetID: provenanceID})
	if err == nil || !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("provenance-only target error = %v", err)
	}
}

func TestTypedGitMergeRootToBaseRequiresAcceptanceAndPublicationBindingBeforeMutation(t *testing.T) {
	f := newTypedMergeFixture(t, true)
	rootID := f.createIssue("root", nil)
	f.commit(rootID, "root.txt")
	baseBefore := runDaemonTestGitOutput(t, f.repo, "rev-parse", "main")

	_, err := f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: rootID, TargetID: "base"})
	if err == nil || !strings.Contains(err.Error(), "without durable human acceptance") {
		t.Fatalf("unaccepted base publication error = %v", err)
	}
	if _, err := f.issues.AppendIssueObservationEvent(f.ctx, rootID, issues.IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Payload: map[string]any{"base_integration_accepted": true}}); err != nil {
		t.Fatal(err)
	}
	_, err = f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: rootID, TargetID: "base"})
	if err == nil || !strings.Contains(err.Error(), "accepted publication binding") {
		t.Fatalf("accepted base publication error = %v, want missing publication binding", err)
	}
	if got := runDaemonTestGitOutput(t, f.repo, "rev-parse", "main"); got != baseBefore {
		t.Fatalf("missing publication binding mutated base: before=%s after=%s", baseBefore, got)
	}
	if _, err := os.Stat(f.gateMarker); !os.IsNotExist(err) {
		t.Fatalf("missing publication binding invoked gate: %v", err)
	}
}

func TestTypedGitMergeRootToBaseWithoutConfiguredCapabilityFailsHonestly(t *testing.T) {
	f := newTypedMergeFixture(t, false)
	rootID := f.createIssue("root", nil)
	f.commit(rootID, "root.txt")
	baseBefore := runDaemonTestGitOutput(t, f.repo, "rev-parse", "main")
	if _, err := f.issues.AppendIssueObservationEvent(f.ctx, rootID, issues.IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Payload: map[string]any{"base_integration_accepted": true}}); err != nil {
		t.Fatal(err)
	}

	_, err := f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: rootID, TargetID: "base"})
	if err == nil || !strings.Contains(err.Error(), "accepted publication binding") {
		t.Fatalf("absent publication capability error = %v", err)
	}
	if got := runDaemonTestGitOutput(t, f.repo, "rev-parse", "main"); got != baseBefore {
		t.Fatalf("absent publication capability mutated base: before=%s after=%s", baseBefore, got)
	}
}

func TestTypedGitMergeRootToBaseRejectsStalePublicationBindingBeforeMutation(t *testing.T) {
	f := newTypedMergeFixture(t, true)
	rootID := f.createIssue("root", nil)
	sourceOID := f.commit(rootID, "root.txt")
	baseBefore := runDaemonTestGitOutput(t, f.repo, "rev-parse", "main")
	if _, err := f.issues.AppendIssueObservationEvent(f.ctx, rootID, issues.IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Payload: map[string]any{"base_integration_accepted": true}}); err != nil {
		t.Fatal(err)
	}
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: f.repo})
	t.Cleanup(func() { _ = runtime.Close() })
	f.daemon.operationRuntime = runtime
	operation := domain.PublicationOperation{
		OperationID: "publication-stale-binding", ProjectID: f.projectID, IssueID: rootID, IntentKey: "accepted-root",
		RequestFingerprint: "fingerprint", ActorID: "reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator,
		ReviewEpochEventID: 1, AcceptedReviewEventID: 2, PatchEvidenceID: "publication-stale-binding", TargetID: "base", TargetBranch: "main",
		SourceRevision: sourceOID, BaseRevision: "stale-base", ValidationCommand: "consumer verify", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC(),
	}
	store, err := f.daemon.publicationStoreForProject(f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnqueuePublication(f.ctx, operation, publicationCoalesceKey(operation)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.issues.AppendIssueObservationEvent(f.ctx, rootID, issues.IssueObservationEventParams{
		Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: string(protocol.OrchestrationIntentReviewAccept),
		Payload: map[string]any{"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": "reviewer", "actor_kind": domain.ReviewerOwnerKindOrchestrator, "review_epoch_event_id": operation.ReviewEpochEventID, "intent_key": operation.IntentKey, "request_fingerprint": operation.RequestFingerprint, "reviewed_source_oid": sourceOID, "publication_operation_id": operation.OperationID},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: rootID, TargetID: "base"})
	if err == nil || !strings.Contains(err.Error(), "base is stale") {
		t.Fatalf("stale publication binding error = %v", err)
	}
	if got := runDaemonTestGitOutput(t, f.repo, "rev-parse", "main"); got != baseBefore {
		t.Fatalf("stale publication binding mutated base: before=%s after=%s", baseBefore, got)
	}
	if _, err := os.Stat(f.gateMarker); !os.IsNotExist(err) {
		t.Fatalf("stale publication binding invoked gate: %v", err)
	}
}

func TestTypedGitMergeRejectsDirtyAndConflictingTargetsBeforeMutation(t *testing.T) {
	t.Run("dirty target", func(t *testing.T) {
		f := newTypedMergeFixture(t, true)
		parentID := f.createIssue("parent", nil)
		childID := f.createIssue("child", &parentID)
		f.commit(childID, "child.txt")
		target := f.worktreeByID[parentID]
		before := runDaemonTestGitOutput(t, target.Path, "rev-parse", "HEAD")
		if err := os.WriteFile(filepath.Join(target.Path, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: childID, TargetID: parentID})
		if err == nil || !strings.Contains(err.Error(), "target worktree") || !strings.Contains(err.Error(), "dirty") {
			t.Fatalf("dirty target error = %v", err)
		}
		if after := runDaemonTestGitOutput(t, target.Path, "rev-parse", "HEAD"); after != before {
			t.Fatalf("dirty target mutated: before=%s after=%s", before, after)
		}
	})

	t.Run("conflicting target", func(t *testing.T) {
		f := newTypedMergeFixture(t, true)
		parentID := f.createIssue("parent", nil)
		childID := f.createIssue("child", &parentID)
		parent := f.worktreeByID[parentID]
		child := f.worktreeByID[childID]
		if err := os.WriteFile(filepath.Join(parent.Path, "base.txt"), []byte("parent\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runDaemonTestGit(t, parent.Path, "add", "base.txt")
		runDaemonTestGit(t, parent.Path, "commit", "-q", "-m", "parent conflict")
		if err := os.WriteFile(filepath.Join(child.Path, "base.txt"), []byte("child\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runDaemonTestGit(t, child.Path, "add", "base.txt")
		runDaemonTestGit(t, child.Path, "commit", "-q", "-m", "child conflict")
		before := runDaemonTestGitOutput(t, parent.Path, "rev-parse", "HEAD")
		_, err := f.daemon.mergeTypedGitBranches(f.ctx, f.projectID, daemonhandlers.GitMergeRequest{SourceID: childID, TargetID: parentID})
		if err == nil || !strings.Contains(err.Error(), "predicted conflicts") {
			t.Fatalf("conflicting target error = %v", err)
		}
		if after := runDaemonTestGitOutput(t, parent.Path, "rev-parse", "HEAD"); after != before {
			t.Fatalf("conflicting target mutated: before=%s after=%s", before, after)
		}
	})
}
