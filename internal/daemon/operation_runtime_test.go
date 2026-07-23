package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	opstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type runtimeGitService struct{}

type unsuccessfulMergeRuntimeGitService struct{ runtimeGitService }

type revisionTypedMergeRuntimeGitService struct {
	runtimeGitService
	mu    sync.Mutex
	calls int
}

func (s *revisionTypedMergeRuntimeGitService) MergeTyped(_ context.Context, _ string, req daemonhandlers.GitMergeRequest) (*daemonhandlers.GitMergeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	oid := "source-revision-one"
	if s.calls > 1 {
		oid = "source-revision-two"
	}
	return &daemonhandlers.GitMergeResult{SourceID: req.SourceID, TargetID: req.TargetID, SourceOID: oid, TargetOID: "target-" + oid, ReceiptRecorded: true, Result: git.MergeResult{Success: true}}, nil
}

func (unsuccessfulMergeRuntimeGitService) Merge(context.Context, string, string, string) (*git.MergeResult, error) {
	return &git.MergeResult{Success: false, Message: "hook rejected merge"}, nil
}

func TestGlobalProjectionRebuildOperationRoutingAndRunner(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), globalProjectionRebuild: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: json.RawMessage(`{"schema_version":2}`)}, nil
	}})
	defer runtime.Close()
	request, err := runtime.buildSubmitRequest(protocol.CommandGlobalProjectionRebuild, "project-a", nil, operationSubmitOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := request.ResourceKeys, []string{"user-projection:rebuild"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resource keys=%v want %v", got, want)
	}
	if request.RecentDedupeWindow != 0 {
		t.Fatalf("dedupe window=%s want immediate retry", request.RecentDedupeWindow)
	}
	if _, err = runtime.directRunnerForKind(protocol.CommandGlobalProjectionRebuild); err != nil {
		t.Fatal(err)
	}
}

type runtimeWorktreeService struct{}

func (runtimeGitService) Fetch(context.Context, string, string, string) error { return nil }
func (runtimeGitService) PullBase(context.Context, string, string, string, string) error {
	return nil
}
func (runtimeGitService) Push(context.Context, string, string, string, string) error { return nil }
func (runtimeGitService) Merge(context.Context, string, string, string) (*git.MergeResult, error) {
	return &git.MergeResult{Success: true}, nil
}
func (runtimeGitService) Checkout(context.Context, string, string, string) error { return nil }
func (runtimeGitService) AbortMerge(context.Context, string, string) error       { return nil }
func (runtimeGitService) DiffStat(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (runtimeGitService) Status(context.Context, string, string) (*git.GitStatus, error) {
	return &git.GitStatus{}, nil
}
func (runtimeGitService) RuntimeSignals(context.Context, string, []daemonhandlers.GitRuntimeSignalsTarget, string, bool, string, bool) ([]daemonhandlers.GitRuntimeSignalsResult, int, error) {
	return nil, 0, nil
}
func (runtimeGitService) WorktreePathForBranch(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}

func (runtimeWorktreeService) List(context.Context, string) ([]git.Worktree, error) { return nil, nil }
func (runtimeWorktreeService) Create(context.Context, string, string, string) (*git.Worktree, string, error) {
	return &git.Worktree{Path: "/tmp/worktree", Branch: "az/test", IssueID: "AZ-1"}, "main", nil
}
func (runtimeWorktreeService) Delete(context.Context, string, string, daemonhandlers.WorktreeRemoveOptions) (*git.Worktree, bool, error) {
	return nil, false, nil
}
func (runtimeWorktreeService) CleanupOrphaned(context.Context, string) (*daemonhandlers.CleanupOrphanedResult, error) {
	return &daemonhandlers.CleanupOrphanedResult{}, nil
}

func TestOperationRuntimeSubmitGetListPublishesLifecycleEvents(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	runtime.sessionStart = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "session started"}), nil
	}

	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-1"})
	submitReq := testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      "session.start",
		IssueID:   naming.IssueID("AZ-1"),
		Payload:   payload,
	})
	ch, cancel := runtime.hub.Subscribe("proj-1", 0)
	defer cancel()

	resp := runtime.Handle(context.Background(), submitReq)
	if !resp.OK {
		t.Fatalf("submit response = %+v", resp)
	}
	var submitBody protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}
	if !submitBody.Created {
		t.Fatal("expected created operation")
	}
	if submitBody.Operation.OperationID == "" {
		t.Fatal("expected operation id")
	}

	record := waitForRuntimeState(t, runtime, submitBody.Operation.OperationID.String(), daemonops.StateDone)
	if got := string(record.ResultPayload); got == "" {
		t.Fatal("expected persisted result payload")
	}

	getResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationGet, protocol.OperationGetRequestBody{OperationID: submitBody.Operation.OperationID}))
	if !getResp.OK {
		t.Fatalf("get response = %+v", getResp)
	}
	var getBody protocol.OperationGetResponseBody
	if err := json.Unmarshal(getResp.Body, &getBody); err != nil {
		t.Fatalf("unmarshal get body: %v", err)
	}
	if getBody.Operation.State != protocol.OperationStateDone {
		t.Fatalf("get state = %s, want done", getBody.Operation.State)
	}

	listResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationList, protocol.OperationListRequestBody{ProjectID: "proj-1"}))
	if !listResp.OK {
		t.Fatalf("list response = %+v", listResp)
	}
	var listBody protocol.OperationListResponseBody
	if err := json.Unmarshal(listResp.Body, &listBody); err != nil {
		t.Fatalf("unmarshal list body: %v", err)
	}
	if len(listBody.Operations) != 1 {
		t.Fatalf("operations len = %d, want 1", len(listBody.Operations))
	}

	events := collectOperationEvents(t, ch, 6)
	want := []string{
		protocol.EventOperationQueued,
		protocol.EventOperationProgress,
		protocol.EventOperationRunning,
		protocol.EventOperationProgress,
		protocol.EventOperationDone,
		protocol.EventOperationProgress,
	}
	for i, event := range events {
		if event.Event != want[i] {
			t.Fatalf("event[%d] = %s, want %s", i, event.Event, want[i])
		}
		if event.Event == protocol.EventOperationProgress {
			var body protocol.OperationProgressEventBody
			if err := json.Unmarshal(event.Body, &body); err != nil {
				t.Fatalf("unmarshal progress event body: %v", err)
			}
			if body.OperationID != submitBody.Operation.OperationID {
				t.Fatalf("progress operation id = %s, want %s", body.OperationID, submitBody.Operation.OperationID)
			}
			continue
		}
		var body protocol.OperationEventBody
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("unmarshal event body: %v", err)
		}
		if body.Operation.OperationID != submitBody.Operation.OperationID {
			t.Fatalf("event operation id = %s, want %s", body.Operation.OperationID, submitBody.Operation.OperationID)
		}
	}
}

func TestOperationRuntimeReportsTerminalSessionStartFailure(t *testing.T) {
	terminal := make(chan daemonops.Record, 1)
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:      t.TempDir(),
		nextRevision: sequentialRevision(),
		onTerminal: func(_ context.Context, record daemonops.Record) {
			terminal <- record
		},
	})
	runtime.sessionStart = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: false, Error: &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInternal, Message: "tmux launch failed"}}, nil
	}

	resp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID:    "proj-1",
		Kind:         "session.start",
		IssueID:      naming.IssueID("AZ-1"),
		DedupeKey:    "session.start:AZ-1",
		ResourceKeys: []string{"session:AZ-1"},
		Payload:      mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-1"}),
	}))
	if !resp.OK {
		t.Fatalf("submit response = %+v", resp)
	}
	var submitted protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &submitted); err != nil {
		t.Fatal(err)
	}
	_ = waitForRuntimeState(t, runtime, submitted.Operation.OperationID.String(), daemonops.StateFailed)
	select {
	case record := <-terminal:
		if record.ID != submitted.Operation.OperationID.String() || record.State != daemonops.StateFailed || record.DedupeKey != "session.start:AZ-1" || !slices.Contains(record.ResourceKeys, "session:AZ-1") {
			t.Fatalf("terminal callback record = %+v", record)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal callback not invoked")
	}
}

type sessionStartOperationFixture struct {
	daemon         *Daemon
	runtime        *operationRuntime
	issueClient    *issues.Client
	issueID        string
	projectID      string
	tmuxRunner     *sessionStartTmuxRunner
	worktreeRunner *worktreeCreateRunner
	terminal       chan daemonops.Record
}

func newSessionStartOperationFixture(t *testing.T) *sessionStartOperationFixture {
	t.Helper()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatal(err)
	}
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{
		Title: "Terminalize failed session start",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID),
		branchName:   "testuser/" + issueID + "/terminalize-start",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	memoryStore := daemonstate.NewStore()
	d := &Daemon{
		cfg: Config{
			RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.Default(),
		},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()), issues: issueClient,
		session: daemonhandlers.NewSessionHandler(memoryStore), sessionStore: memoryStore,
		revision: map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}
	attachIsolatedRuntimeStore(t, d, projectID)
	terminal := make(chan daemonops.Record, 4)
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir: repoDir,
		onTerminal: func(_ context.Context, record daemonops.Record) {
			terminal <- record
		},
	})
	runtime.sessionStart = d.handleSessionStart
	t.Cleanup(func() { _ = runtime.Close() })
	return &sessionStartOperationFixture{
		daemon: d, runtime: runtime, issueClient: issueClient, issueID: issueID, projectID: projectID,
		tmuxRunner: tmuxRunner, worktreeRunner: worktreeRunner, terminal: terminal,
	}
}

func (f *sessionStartOperationFixture) assertIssueStatus(t *testing.T, want domain.Status) {
	t.Helper()
	task, err := f.issueClient.GetWithRuntime(context.Background(), f.projectID, f.issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != want {
		t.Fatalf("issue status = %s, want %s", task.Status, want)
	}
}

func (f *sessionStartOperationFixture) submit(t *testing.T) protocol.OperationSubmitResponseBody {
	t.Helper()
	sessionID := naming.CanonicalSessionID(f.daemon.sessionNamingScope(f.projectID), f.issueID)
	resp := f.runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: naming.ProjectID(f.projectID), Kind: daemonhandlers.CommandSessionStart,
		IssueID: naming.IssueID(f.issueID), DedupeKey: daemonhandlers.CommandSessionStart + ":" + f.issueID,
		ResourceKeys: []string{"issue:" + f.projectID + ":" + f.issueID, "session:" + sessionID, "worktree:" + f.issueID},
		Payload:      mustJSON(t, map[string]any{"project_id": f.projectID, "session_id": f.issueID, "start_work": true}),
	}))
	if !resp.OK {
		t.Fatalf("submit response = %+v", resp)
	}
	var body protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func (f *sessionStartOperationFixture) assertNoStartLeak(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	sessionID := naming.CanonicalSessionID(f.daemon.sessionNamingScope(f.projectID), f.issueID)
	if f.tmuxRunner.sessions[sessionID] {
		t.Fatalf("failed start leaked tmux session %s", sessionID)
	}
	store := f.daemon.sessionRuntimeStateStore(f.projectID)
	if worktree, found, err := store.GetWorktreeStateByIssueID(ctx, f.projectID, f.issueID); err != nil || found {
		t.Fatalf("failed start worktree projection = %+v found=%t err=%v", worktree, found, err)
	}
	if session, found, err := store.GetWorkerSessionStateByIssueID(ctx, f.projectID, f.issueID, sessionID); err != nil || (found && (session.State != daemonstate.SessionStateStopped || session.ObservedState != daemonstate.SessionStateStopped)) {
		t.Fatalf("failed start session projection = %+v found=%t err=%v", session, found, err)
	}
	scope, err := domain.RootedOrchestrationScope(f.issueID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(f.projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, identity); err != nil || found {
		t.Fatalf("failed worker start rooted lease = %+v found=%t err=%v", lease, found, err)
	}
}

func TestSessionStartOperationFailedLaunchTerminalizesAndRetriesAfterBoundedCompensation(t *testing.T) {
	f := newSessionStartOperationFixture(t)
	f.tmuxRunner.newSessionErr = errors.New("tmux new-session concrete failure")
	var cancelCompensation context.CancelFunc
	f.daemon.sessionStartCompensationContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
		cancelCompensation = cancel
		return ctx, cancel
	}
	f.worktreeRunner.onWorktreeRemove = func() {
		cancelCompensation()
	}

	first := f.submit(t)
	failed := <-f.terminal
	if failed.ID != first.Operation.OperationID.String() || failed.State != daemonops.StateFailed {
		t.Fatalf("terminal record = %+v, want failed %s", failed, first.Operation.OperationID)
	}
	if !strings.Contains(failed.ErrorMessage, "tmux new-session concrete failure") {
		t.Fatalf("terminal error = %q, want concrete tmux cause", failed.ErrorMessage)
	}
	if !f.worktreeRunner.worktreeRemoved {
		t.Fatal("failed start did not compensate the new worktree")
	}
	f.assertNoStartLeak(t)
	f.assertIssueStatus(t, domain.StatusOpen)

	f.daemon.sessionStartCompensationContext = nil
	f.worktreeRunner.onWorktreeRemove = nil
	f.tmuxRunner.newSessionErr = nil
	acknowledgeManagedAgentOnInitialLaunch(t, f.daemon, f.tmuxRunner, f.projectID)
	second := f.submit(t)
	done := <-f.terminal
	if !second.Created || second.Operation.OperationID == first.Operation.OperationID || done.ID != second.Operation.OperationID.String() || done.State != daemonops.StateDone {
		t.Fatalf("retry submit=%+v terminal=%+v first=%s", second, done, first.Operation.OperationID)
	}
}

func TestSessionStartOperationFailedLaunchCleansResourcesForInitiallyWorkingIssue(t *testing.T) {
	f := newSessionStartOperationFixture(t)
	if err := f.issueClient.Update(context.Background(), f.issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	f.tmuxRunner.newSessionErr = errors.New("tmux new-session concrete failure")

	started := f.submit(t)
	failed := <-f.terminal
	if failed.ID != started.Operation.OperationID.String() || failed.State != daemonops.StateFailed {
		t.Fatalf("terminal record = %+v, want failed %s", failed, started.Operation.OperationID)
	}
	f.assertNoStartLeak(t)
	f.assertIssueStatus(t, domain.StatusInProgress)
}

func TestSessionStartOperationCancellationTerminalizesAndAllowsRetry(t *testing.T) {
	f := newSessionStartOperationFixture(t)
	launchEntered := make(chan struct{})
	f.tmuxRunner.onNewSessionCommand = func(ctx context.Context, _ string) error {
		close(launchEntered)
		<-ctx.Done()
		return ctx.Err()
	}

	first := f.submit(t)
	<-launchEntered
	cancelResp := f.runtime.Handle(context.Background(), testRequest(protocol.CommandOperationCancel, protocol.OperationCancelRequestBody{
		OperationID: first.Operation.OperationID,
		Reason:      "cancel deterministic blocked launch",
	}))
	if !cancelResp.OK {
		t.Fatalf("cancel response = %+v", cancelResp)
	}
	cancelled := <-f.terminal
	if cancelled.ID != first.Operation.OperationID.String() || cancelled.State != daemonops.StateCancelled || cancelled.ErrorMessage != "cancel deterministic blocked launch" {
		t.Fatalf("cancelled record = %+v", cancelled)
	}
	if !f.worktreeRunner.worktreeRemoved {
		t.Fatal("cancelled start did not compensate the new worktree")
	}
	f.assertNoStartLeak(t)

	f.tmuxRunner.onNewSessionCommand = nil
	acknowledgeManagedAgentOnInitialLaunch(t, f.daemon, f.tmuxRunner, f.projectID)
	second := f.submit(t)
	done := <-f.terminal
	if second.Operation.OperationID == first.Operation.OperationID || done.ID != second.Operation.OperationID.String() || done.State != daemonops.StateDone {
		t.Fatalf("retry submit=%+v terminal=%+v first=%s", second, done, first.Operation.OperationID)
	}
}

func TestOperationRuntimeBulkCleanupPersistsStructuredResultAndAllowsCompletedRetry(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir()})
	runtime.taskBulkCleanup = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, taskBulkCleanupResult{
			Action: "cancelled",
			Items:  []taskBulkCleanupItem{{TaskID: "az-1", Action: "cancelled", Success: true}},
		}), nil
	}
	payload := mustJSON(t, taskBulkCleanupRequest{TaskIDs: []string{"az-1"}, CloseOutcome: "cancelled"})
	request := testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      protocol.CommandTaskBulkCleanup,
		Payload:   payload,
	})

	firstResp := runtime.Handle(context.Background(), request)
	if !firstResp.OK {
		t.Fatalf("first submit response = %+v", firstResp)
	}
	var first protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(firstResp.Body, &first); err != nil {
		t.Fatalf("unmarshal first submit: %v", err)
	}
	record := waitForRuntimeState(t, runtime, first.Operation.OperationID.String(), daemonops.StateDone)
	var result taskBulkCleanupResult
	if err := json.Unmarshal(record.ResultPayload, &result); err != nil {
		t.Fatalf("unmarshal persisted result: %v", err)
	}
	if len(result.Items) != 1 || !result.Items[0].Success {
		t.Fatalf("persisted result = %+v", result)
	}

	secondResp := runtime.Handle(context.Background(), request)
	if !secondResp.OK {
		t.Fatalf("retry submit response = %+v", secondResp)
	}
	var second protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(secondResp.Body, &second); err != nil {
		t.Fatalf("unmarshal retry submit: %v", err)
	}
	if !second.Created || second.Operation.OperationID == first.Operation.OperationID {
		t.Fatalf("retry operation = %+v, first = %+v", second, first)
	}
	waitForRuntimeState(t, runtime, second.Operation.OperationID.String(), daemonops.StateDone)
}

func TestOperationRuntimeGitMergePublishesLifecycleEvents(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	runtime.gitHandler = daemonhandlers.NewGitHandler(runtimeGitService{})

	payload := mustJSON(t, map[string]string{"worktree": "/tmp/wt", "branch": "main"})
	submitReq := testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      daemonhandlers.CommandGitMergeRef,
		Payload:   payload,
	})
	ch, cancel := runtime.hub.Subscribe("proj-1", 0)
	defer cancel()

	resp := runtime.Handle(context.Background(), submitReq)
	if !resp.OK {
		t.Fatalf("submit response = %+v", resp)
	}
	var submitBody protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}
	if submitBody.Operation.OperationID == "" {
		t.Fatal("expected operation id")
	}

	record := waitForRuntimeState(t, runtime, submitBody.Operation.OperationID.String(), daemonops.StateDone)
	if record.Kind != daemonhandlers.CommandGitMergeRef {
		t.Fatalf("operation kind = %s, want %s", record.Kind, daemonhandlers.CommandGitMergeRef)
	}

	events := collectOperationEvents(t, ch, 6)
	want := []string{
		protocol.EventOperationQueued,
		protocol.EventOperationProgress,
		protocol.EventOperationRunning,
		protocol.EventOperationProgress,
		protocol.EventOperationDone,
		protocol.EventOperationProgress,
	}
	for i, event := range events {
		if event.Event != want[i] {
			t.Fatalf("event[%d] = %s, want %s", i, event.Event, want[i])
		}
		if event.Event == protocol.EventOperationProgress {
			var progress protocol.OperationProgressEventBody
			if err := json.Unmarshal(event.Body, &progress); err != nil {
				t.Fatalf("unmarshal progress event body: %v", err)
			}
			if progress.OperationID != submitBody.Operation.OperationID {
				t.Fatalf("progress operation id = %s, want %s", progress.OperationID, submitBody.Operation.OperationID)
			}
			continue
		}
		var body protocol.OperationEventBody
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("unmarshal event body: %v", err)
		}
		if body.Operation.OperationID != submitBody.Operation.OperationID {
			t.Fatalf("event operation id = %s, want %s", body.Operation.OperationID, submitBody.Operation.OperationID)
		}
		if body.Operation.Kind != daemonhandlers.CommandGitMergeRef {
			t.Fatalf("event operation kind = %s, want %s", body.Operation.Kind, daemonhandlers.CommandGitMergeRef)
		}
	}
}

func TestOperationRuntimeTypedGitMergeReexecutesSameIDsAfterCompletion(t *testing.T) {
	service := &revisionTypedMergeRuntimeGitService{}
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.gitHandler = daemonhandlers.NewGitHandler(service)
	payload := mustJSON(t, map[string]string{"source_id": "az-child", "target_id": "az-parent"})
	submit := func() (protocol.OperationSubmitResponseBody, daemonops.Record) {
		t.Helper()
		resp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{ProjectID: "proj-1", Kind: daemonhandlers.CommandGitMerge, Payload: payload}))
		if !resp.OK {
			t.Fatalf("submit response = %+v", resp)
		}
		var body protocol.OperationSubmitResponseBody
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			t.Fatal(err)
		}
		return body, waitForRuntimeState(t, runtime, body.Operation.OperationID.String(), daemonops.StateDone)
	}

	first, firstRecord := submit()
	second, secondRecord := submit()
	if first.Operation.OperationID == second.Operation.OperationID || !second.Created {
		t.Fatalf("completed typed merge was deduped: first=%+v second=%+v", first.Operation, second.Operation)
	}
	service.mu.Lock()
	calls := service.calls
	service.mu.Unlock()
	if calls != 2 {
		t.Fatalf("typed merge executions = %d, want 2", calls)
	}
	if !strings.Contains(string(firstRecord.ResultPayload), "source-revision-one") || !strings.Contains(string(secondRecord.ResultPayload), "source-revision-two") {
		t.Fatalf("typed merge results did not bind refreshed source revisions: first=%s second=%s", firstRecord.ResultPayload, secondRecord.ResultPayload)
	}
}

func TestOperationRuntimeMarksUnsuccessfulGitMergeFailed(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	runtime.gitHandler = daemonhandlers.NewGitHandler(unsuccessfulMergeRuntimeGitService{})
	resp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      daemonhandlers.CommandGitMergeRef,
		Payload:   mustJSON(t, map[string]string{"worktree": "/tmp/wt", "branch": "source"}),
	}))
	if !resp.OK {
		t.Fatalf("submit response = %+v", resp)
	}
	var body protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}
	record := waitForRuntimeState(t, runtime, body.Operation.OperationID.String(), daemonops.StateFailed)
	if record.ErrorMessage != "hook rejected merge" {
		t.Fatalf("failed operation = %+v, want unsuccessful merge error", record)
	}
}

func TestOperationRuntimeQueueReportsBlockedDependencies(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir()})
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := runtime.manager.Submit(context.Background(), daemonops.SubmitRequest{
		ID:           "op-running",
		ProjectID:    "proj-1",
		IssueID:      "az-1",
		Kind:         "git.merge",
		ResourceKeys: []string{"worktree:/tmp/wt"},
	}, func(context.Context) ([]byte, error) {
		close(started)
		<-release
		return nil, nil
	}); err != nil {
		t.Fatalf("submit running operation: %v", err)
	}
	<-started
	queuedStarted := make(chan struct{}, 1)
	if _, err := runtime.manager.Submit(context.Background(), daemonops.SubmitRequest{
		ID:           "op-queued",
		ProjectID:    "proj-1",
		IssueID:      "az-2",
		Kind:         "worktree.cleanup",
		ResourceKeys: []string{"worktree:/tmp/wt"},
	}, func(context.Context) ([]byte, error) {
		queuedStarted <- struct{}{}
		return nil, nil
	}); err != nil {
		t.Fatalf("submit queued operation: %v", err)
	}
	select {
	case <-queuedStarted:
		t.Fatal("queued operation started before snapshot")
	default:
	}

	resp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationQueue, protocol.OperationQueueRequestBody{ProjectID: "proj-1"}))
	if !resp.OK {
		t.Fatalf("queue response = %+v", resp)
	}
	var body protocol.OperationQueueResponseBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal queue body: %v", err)
	}
	if len(body.Running) != 1 || body.Running[0].Operation.OperationID != "op-running" {
		t.Fatalf("running entries = %+v, want op-running", body.Running)
	}
	if len(body.Queued) != 1 || body.Queued[0].Operation.OperationID != "op-queued" {
		t.Fatalf("queued entries = %+v, want op-queued", body.Queued)
	}
	if len(body.Queued[0].BlockingOperationIDs) != 1 || body.Queued[0].BlockingOperationIDs[0] != "op-running" {
		t.Fatalf("blocking ids = %+v, want op-running", body.Queued[0].BlockingOperationIDs)
	}
	if len(body.Queued[0].BlockedResourceKeys) != 1 || body.Queued[0].BlockedResourceKeys[0] != "worktree:/tmp/wt" {
		t.Fatalf("blocked resources = %+v, want worktree", body.Queued[0].BlockedResourceKeys)
	}
	select {
	case <-queuedStarted:
		t.Fatal("queued operation started before running operation released")
	default:
	}

	close(release)
	if err := runtime.manager.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestOperationRuntimeSessionStartPersistsRunningProgress(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	runtime.sessionStart = func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		if err := daemonops.ReportProgress(ctx, daemonops.Progress{
			Phase:             "worktree_preflight",
			Message:           "creating or reusing worktree",
			Current:           25,
			Total:             100,
			Unit:              "percent",
			Percent:           25,
			AgentIncarnation:  "planned-incarnation",
			PromptHandoffPath: "/runtime/launch.prompt",
		}); err != nil {
			t.Fatalf("ReportProgress error: %v", err)
		}
		close(started)
		<-release
		return testResponse(req, map[string]string{"output": "session started"}), nil
	}

	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-1"})
	ch, cancel := runtime.hub.Subscribe("proj-1", 0)
	defer cancel()
	resp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      "session.start",
		IssueID:   naming.IssueID("AZ-1"),
		Payload:   payload,
	}))
	if !resp.OK {
		t.Fatalf("submit response = %+v", resp)
	}
	var submitBody protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}
	<-started

	record := waitForRuntimeProgress(t, runtime, submitBody.Operation.OperationID.String(), "worktree_preflight")
	if record.State != daemonops.StateRunning {
		t.Fatalf("record state = %s, want running", record.State)
	}
	if record.Progress == nil || record.Progress.PromptHandoffPath != "/runtime/launch.prompt" {
		t.Fatalf("durable internal progress = %+v, want prompt handoff path", record.Progress)
	}

	getResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationGet, protocol.OperationGetRequestBody{OperationID: submitBody.Operation.OperationID}))
	if !getResp.OK {
		t.Fatalf("get response = %+v", getResp)
	}
	var getBody protocol.OperationGetResponseBody
	if err := json.Unmarshal(getResp.Body, &getBody); err != nil {
		t.Fatalf("unmarshal get body: %v", err)
	}
	if getBody.Operation.Progress == nil || getBody.Operation.Progress.Phase != "worktree_preflight" || getBody.Operation.Progress.Percent != 25 || getBody.Operation.Progress.AgentIncarnation != "planned-incarnation" {
		t.Fatalf("operation progress = %+v, want worktree_preflight 25%%", getBody.Operation.Progress)
	}

	events := collectOperationEvents(t, ch, 5)
	if events[4].Event != protocol.EventOperationProgress {
		t.Fatalf("event[4] = %s, want progress-only update without duplicate lifecycle event", events[4].Event)
	}
	var progress protocol.OperationProgressEventBody
	if err := json.Unmarshal(events[4].Body, &progress); err != nil {
		t.Fatalf("unmarshal progress event: %v", err)
	}
	if progress.Progress.Phase != "worktree_preflight" {
		t.Fatalf("progress event phase = %q, want worktree_preflight", progress.Progress.Phase)
	}
}

func TestStoredOperationProgressPreservesExplicitTmuxOnlyLaunchPlan(t *testing.T) {
	required := false
	payload := marshalOperationProgressJSON(&daemonops.Progress{
		Phase: "tmux_launch", Percent: 70, AgentLaunchRequired: &required,
	})
	progress := unmarshalOperationProgress(payload)
	if progress == nil || progress.AgentLaunchRequired == nil || *progress.AgentLaunchRequired {
		t.Fatalf("round-trip progress = %+v, want explicit tmux-only launch plan", progress)
	}
}

func TestOperationRuntimePersistsFinalTmuxOnlyFenceForRecovery(t *testing.T) {
	projectID, issueID := "proj-tmux-final", "AZ-2"
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	progressed := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	runtime.sessionStart = func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		if err := reportTmuxOnlySessionStartProgress(ctx, "tmux_launch", "creating tmux session without agent launch", 70); err != nil {
			return protocol.ResponseEnvelope{}, err
		}
		if err := reportTmuxOnlySessionStartProgress(ctx, "tmux_launch", "tmux session created without agent launch", 90); err != nil {
			return protocol.ResponseEnvelope{}, err
		}
		close(progressed)
		<-release
		return testResponse(req, map[string]string{"output": "session started"}), nil
	}
	payload := mustJSON(t, map[string]any{"project_id": projectID, "session_id": issueID, "start_work": false})
	response := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: naming.ProjectID(projectID), Kind: daemonhandlers.CommandSessionStart, IssueID: naming.IssueID(issueID), Payload: payload,
	}))
	if !response.OK {
		t.Fatalf("submit tmux-only start: %+v", response)
	}
	var submitted protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(response.Body, &submitted); err != nil {
		t.Fatal(err)
	}
	<-progressed
	record := waitForRuntimeProgress(t, runtime, submitted.Operation.OperationID.String(), "tmux_launch")
	if record.Progress == nil || record.Progress.Percent != 90 || record.Progress.AgentLaunchRequired == nil || *record.Progress.AgentLaunchRequired {
		t.Fatalf("persisted final tmux-only progress=%+v", record.Progress)
	}

	store := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = store.Close() })
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	runner.currentCommand = "zsh"
	d := &Daemon{
		cfg:  Config{RepoDir: t.TempDir(), SessionShell: "zsh", Logger: slog.Default()},
		tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{}, revision: map[string]uint64{},
	}
	recovery, ok := d.recoverInterruptedOperation(context.Background(), record)
	if !ok || recovery.State != daemonops.StateDone || !runner.sessions[sessionID] {
		t.Fatalf("persisted final tmux-only recovery=%+v ok=%t live=%t", recovery, ok, runner.sessions[sessionID])
	}
}

func TestOperationRuntimeDirectGitMergeWaitTimeoutReturnsPendingEnvelope(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.gitHandler = daemonhandlers.NewGitHandler(runtimeGitService{})
	runtime.pollInterval = time.Millisecond
	executor := operationCommandExecutor{runtime: runtime}
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	req := testRequest(daemonhandlers.CommandGitMergeRef, map[string]string{"worktree": "/tmp/wt", "branch": "main"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	defer cancel()
	resp := executor.Execute(ctx, req, daemonhandlers.CommandGitMergeRef, func(_ context.Context) protocol.ResponseEnvelope {
		close(started)
		<-release
		return testResponse(req, map[string]string{"worktree": "/tmp/wt", "branch": "main"})
	})
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("response error = %+v", *resp.Error)
		}
		t.Fatalf("response = %+v", resp)
	}
	var body operationResultEnvelope
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.OperationID == "" {
		t.Fatal("expected pending operation id")
	}
	if body.State != string(daemonops.StateRunning) && body.State != string(daemonops.StateQueued) {
		t.Fatalf("state = %q, want running or queued", body.State)
	}
	if len(body.Result) != 0 {
		t.Fatalf("result = %s, want empty pending result", string(body.Result))
	}
}

func TestOperationRuntimeWorktreeCleanupPublishesProgressEvents(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	runtime.worktreeHandler = daemonhandlers.NewWorktreeHandler(runtimeWorktreeService{})

	payload := mustJSON(t, map[string]string{"project_id": "proj-1"})
	submitReq := testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      daemonhandlers.CommandWorktreeCleanupOrphaned,
		Payload:   payload,
	})
	ch, cancel := runtime.hub.Subscribe("proj-1", 0)
	defer cancel()

	resp := runtime.Handle(context.Background(), submitReq)
	if !resp.OK {
		t.Fatalf("submit response = %+v", resp)
	}
	var submitBody protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}

	_ = waitForRuntimeState(t, runtime, submitBody.Operation.OperationID.String(), daemonops.StateDone)
	events := collectOperationEvents(t, ch, 6)
	if events[1].Event != protocol.EventOperationProgress || events[3].Event != protocol.EventOperationProgress || events[5].Event != protocol.EventOperationProgress {
		t.Fatalf("expected progress events at queued/running/done slots, got %q %q %q", events[1].Event, events[3].Event, events[5].Event)
	}
	var queued protocol.OperationProgressEventBody
	if err := json.Unmarshal(events[1].Body, &queued); err != nil {
		t.Fatalf("unmarshal queued progress body: %v", err)
	}
	var running protocol.OperationProgressEventBody
	if err := json.Unmarshal(events[3].Body, &running); err != nil {
		t.Fatalf("unmarshal running progress body: %v", err)
	}
	var done protocol.OperationProgressEventBody
	if err := json.Unmarshal(events[5].Body, &done); err != nil {
		t.Fatalf("unmarshal done progress body: %v", err)
	}
	if queued.Progress.Percent != 0 || running.Progress.Percent != 50 || done.Progress.Percent != 100 {
		t.Fatalf("progress percents = %d/%d/%d, want 0/50/100", queued.Progress.Percent, running.Progress.Percent, done.Progress.Percent)
	}
}

func TestOperationProgressForState_UsesTerminalStateMessage(t *testing.T) {
	failed := operationProgressForState(daemonops.StateFailed, daemonhandlers.CommandWorktreeRemove)
	if failed.Message != "failed "+daemonhandlers.CommandWorktreeRemove {
		t.Fatalf("failed message = %q", failed.Message)
	}
	if failed.Percent != 100 {
		t.Fatalf("failed percent = %d, want 100", failed.Percent)
	}

	cancelled := operationProgressForState(daemonops.StateCancelled, "session.stop")
	if cancelled.Message != "cancelled session.stop" {
		t.Fatalf("cancelled message = %q", cancelled.Message)
	}
	if cancelled.Percent != 100 {
		t.Fatalf("cancelled percent = %d, want 100", cancelled.Percent)
	}
}

func TestOperationRuntimeCancelMarksRunningOperationCancelled(t *testing.T) {
	blocked := make(chan struct{})
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	runtime.sessionStop = func(ctx context.Context, _ protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		close(blocked)
		<-ctx.Done()
		return protocol.ResponseEnvelope{}, ctx.Err()
	}

	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-2"})
	submitResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      "session.stop",
		IssueID:   naming.IssueID("AZ-2"),
		Payload:   payload,
	}))
	if !submitResp.OK {
		t.Fatalf("submit response = %+v", submitResp)
	}
	var submitBody protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(submitResp.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}

	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("operation did not start running")
	}

	cancelResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationCancel, protocol.OperationCancelRequestBody{
		ProjectID:   "proj-1",
		OperationID: submitBody.Operation.OperationID,
		Reason:      "user requested cancel",
	}))
	if !cancelResp.OK {
		t.Fatalf("cancel response = %+v", cancelResp)
	}

	record := waitForRuntimeState(t, runtime, submitBody.Operation.OperationID.String(), daemonops.StateCancelled)
	if record.ErrorMessage != "user requested cancel" {
		t.Fatalf("cancel error message = %q, want user requested cancel", record.ErrorMessage)
	}
}

func TestOperationRuntimeCancelMapsCancelledErrorResponseToCancelled(t *testing.T) {
	blocked := make(chan struct{})
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.sessionStart = func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		close(blocked)
		<-ctx.Done()
		return runtime.errorResponse(req, protocol.ErrorCodeInternal, ctx.Err().Error()), nil
	}

	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-2"})
	submitResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      "session.start",
		IssueID:   naming.IssueID("AZ-2"),
		Payload:   payload,
	}))
	if !submitResp.OK {
		t.Fatalf("submit response = %+v", submitResp)
	}
	var submitBody protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(submitResp.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}

	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("operation did not start running")
	}

	cancelResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationCancel, protocol.OperationCancelRequestBody{
		ProjectID:   "proj-1",
		OperationID: submitBody.Operation.OperationID,
		Reason:      "user requested cancel",
	}))
	if !cancelResp.OK {
		t.Fatalf("cancel response = %+v", cancelResp)
	}

	record := waitForRuntimeState(t, runtime, submitBody.Operation.OperationID.String(), daemonops.StateCancelled)
	if record.ErrorMessage != "user requested cancel" {
		t.Fatalf("cancel error message = %q, want user requested cancel", record.ErrorMessage)
	}
}

func TestOperationRuntimeCancelAfterRestartDispatchPersistsTerminalAggregate(t *testing.T) {
	d, runtimeStore, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
	if err := upsertSessionStateFixture(runtimeStore, context.Background(), target.ProjectID, daemonstate.Session{
		ID: target.SessionID, IssueID: target.IssueID, Role: daemonstate.SessionRoleWorker,
		ScopeKind: daemonstate.SessionScopeIssue, ScopeID: target.IssueID, State: daemonstate.SessionStateRunning,
	}); err != nil {
		t.Fatal(err)
	}
	dispatched := make(chan struct{})
	releaseDispatch := make(chan struct{})
	d.sessionRestartRespawn = func(_ context.Context, paneTarget, worktree, command string) (error, bool) {
		if _, err := runner.Run(context.Background(), "respawn-pane", "-k", "-c", worktree, "-t", paneTarget, command); err != nil {
			return err, false
		}
		close(dispatched)
		<-releaseDispatch
		return nil, false
	}

	resultTemplate := protocol.SessionRestartAllResponseBody{
		ProjectID: naming.ProjectID("project"), ProjectIDs: []naming.ProjectID{"project"},
		Sessions: make([]protocol.SessionRestartAllItem, 0, 1),
	}
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	defer runtime.Close()
	runtime.sessionRestartAll = func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		var body protocol.SessionRestartAllRequestBody
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return protocol.ResponseEnvelope{}, err
		}
		return d.executeSessionRestartBatch(ctx, req, "project", []string{"project"}, body, []sessionRestartAllTarget{target}, resultTemplate)
	}

	payload := mustJSON(t, protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID("project")})
	submitResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "project", Kind: protocol.CommandSessionRestartAll, Payload: payload,
	}))
	if !submitResp.OK {
		t.Fatalf("submit response = %+v", submitResp)
	}
	var submitted protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(submitResp.Body, &submitted); err != nil {
		t.Fatal(err)
	}
	<-dispatched
	cancelResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationCancel, protocol.OperationCancelRequestBody{
		ProjectID: "project", OperationID: submitted.Operation.OperationID, Reason: "cancel after respawn dispatch",
	}))
	if !cancelResp.OK {
		t.Fatalf("cancel response = %+v", cancelResp)
	}
	close(releaseDispatch)
	if err := runtime.manager.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := runtime.manager.Get(context.Background(), submitted.Operation.OperationID.String())
	if err != nil {
		t.Fatal(err)
	}
	if record.State != daemonops.StateDone {
		t.Fatalf("operation state = %s, want done; error=%q progress=%+v", record.State, record.ErrorMessage, record.Progress)
	}
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(record.ResultPayload, &result); err != nil {
		t.Fatalf("decode aggregate result: %v", err)
	}
	respawns, _, _ := runner.snapshot()
	if respawns != 1 || result.Restarted != 1 || result.Skipped != 0 || result.Failed != 0 || len(result.Sessions) != 1 || !result.Sessions[0].Restarted {
		t.Fatalf("respawns=%d result=%+v, want one truthfully acknowledged restart", respawns, result)
	}
	batch, ok := decodeSessionRestartBatchPlan(record)
	if !ok || batch.Cursor != 1 || batch.Current != nil || len(batch.Results) != 1 || !batch.Results[0].Restarted {
		t.Fatalf("terminal aggregate checkpoint = %+v ok=%t", batch, ok)
	}
}

func TestOperationRuntimeStartupFailsInterruptedOperations(t *testing.T) {
	repoDir := t.TempDir()
	first := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, nextRevision: sequentialRevision()})

	if _, err := first.store.Create(context.Background(), opstore.CreateParams{
		OperationID:  "op-running",
		ProjectID:    "proj-1",
		IssueID:      "AZ-2",
		Kind:         "session.start",
		DedupeKey:    "session.start:AZ-2",
		ResourceKeys: []string{"issue:proj-1:AZ-2"},
	}); err != nil {
		t.Fatalf("create running operation: %v", err)
	}
	if _, err := first.store.Transition(context.Background(), opstore.TransitionParams{
		OperationID: "op-running",
		ToState:     opstore.StateRunning,
	}); err != nil {
		t.Fatalf("transition running operation: %v", err)
	}
	if _, err := first.store.Create(context.Background(), opstore.CreateParams{
		OperationID:  "op-queued",
		ProjectID:    "proj-1",
		IssueID:      "AZ-2",
		Kind:         "session.stop",
		DedupeKey:    "session.stop:AZ-2",
		ResourceKeys: []string{"issue:proj-1:AZ-2"},
	}); err != nil {
		t.Fatalf("create queued operation: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}

	restarted := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, nextRevision: sequentialRevision()})
	t.Cleanup(func() { _ = restarted.Close() })

	runningRecord := waitForRuntimeState(t, restarted, "op-running", daemonops.StateFailed)
	if runningRecord.FinishedAt == nil {
		t.Fatal("running operation finished_at was not set after restart reconciliation")
	}
	if runningRecord.ErrorMessage != interruptedOperationMessage {
		t.Fatalf("running operation error = %q, want %q", runningRecord.ErrorMessage, interruptedOperationMessage)
	}
	queuedRecord := waitForRuntimeState(t, restarted, "op-queued", daemonops.StateFailed)
	if queuedRecord.FinishedAt == nil {
		t.Fatal("queued operation finished_at was not set after restart reconciliation")
	}
	if queuedRecord.ErrorMessage != interruptedOperationMessage {
		t.Fatalf("queued operation error = %q, want %q", queuedRecord.ErrorMessage, interruptedOperationMessage)
	}
}

func TestOperationRuntimeStartupRecoversInterruptedSessionStartWhenCompleted(t *testing.T) {
	repoDir := t.TempDir()
	first := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, nextRevision: sequentialRevision()})
	recoveryCalls := 0

	if _, err := first.store.Create(context.Background(), opstore.CreateParams{
		OperationID:  "op-start",
		ProjectID:    "proj-1",
		IssueID:      "AZ-2",
		Kind:         "session.start",
		DedupeKey:    "session.start:AZ-2",
		ResourceKeys: []string{"issue:proj-1:AZ-2"},
	}); err != nil {
		t.Fatalf("create session.start operation: %v", err)
	}
	if _, err := first.store.Transition(context.Background(), opstore.TransitionParams{
		OperationID: "op-start",
		ToState:     opstore.StateRunning,
	}); err != nil {
		t.Fatalf("transition session.start operation: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}

	restarted := newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		nextRevision: sequentialRevision(),
		recoverInterrupted: func(_ context.Context, record daemonops.Record) (interruptedOperationRecovery, bool) {
			recoveryCalls++
			if record.Kind != "session.start" || record.IssueID != "AZ-2" {
				return interruptedOperationRecovery{}, false
			}
			return interruptedOperationRecovery{
				State:         daemonops.StateDone,
				ResultPayload: mustJSON(t, map[string]string{"output": "session recovered"}),
			}, true
		},
	})

	record := waitForRuntimeState(t, restarted, "op-start", daemonops.StateDone)
	if record.FinishedAt == nil {
		t.Fatal("recovered operation finished_at was not set")
	}
	if record.ErrorMessage != "" {
		t.Fatalf("recovered operation error = %q, want empty", record.ErrorMessage)
	}
	if string(record.ResultPayload) == "" {
		t.Fatal("recovered operation result payload was not preserved")
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery calls = %d, want exactly one", recoveryCalls)
	}
	if err := restarted.Close(); err != nil {
		t.Fatalf("close recovered runtime: %v", err)
	}
	restartedAgain := newOperationRuntime(operationRuntimeConfig{
		repoDir:      repoDir,
		nextRevision: sequentialRevision(),
		recoverInterrupted: func(context.Context, daemonops.Record) (interruptedOperationRecovery, bool) {
			recoveryCalls++
			return interruptedOperationRecovery{State: daemonops.StateDone}, true
		},
	})
	t.Cleanup(func() { _ = restartedAgain.Close() })
	if got := waitForRuntimeState(t, restartedAgain, "op-start", daemonops.StateDone); got.State != daemonops.StateDone {
		t.Fatalf("second restart state = %s, want done", got.State)
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery calls after second startup = %d, want exactly one total", recoveryCalls)
	}
}

func TestDaemonRecoverInterruptedSessionStartUsesActiveProjection(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-1"
	managedSessionID := naming.CanonicalSessionID(projectID, "AZ-2")
	store := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = store.Close() })
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID:            "AZ-2",
		IssueID:       "AZ-2",
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert active session projection: %v", err)
	}
	if err := store.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{
		ProjectID: projectID, SessionID: managedSessionID, LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "planned", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed exact managed agent identity: %v", err)
	}
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID:            "AZ-3",
		IssueID:       "AZ-3",
		State:         daemonstate.SessionStateStarting,
		ObservedState: daemonstate.SessionStateStarting,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert starting session projection: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[managedSessionID] = true
	tmuxRunner.panes[managedSessionID] = []string{"%7"}
	tmuxRunner.panePIDs[managedSessionID] = 123
	tmuxRunner.currentCommand = "codex"
	daemon := &Daemon{
		cfg:                    Config{RepoDir: t.TempDir()},
		tmux:                   tmux.NewClient(tmuxRunner, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
	}

	recovery, ok := daemon.recoverInterruptedOperation(ctx, daemonops.Record{
		ID:        "op-start",
		ProjectID: projectID,
		IssueID:   "AZ-2",
		Kind:      "session.start",
		Progress:  &daemonops.Progress{Phase: "tmux_launch", AgentIncarnation: "planned"},
	})
	if !ok || recovery.State != daemonops.StateDone {
		t.Fatalf("active session recovery = %+v, ok=%t; want done", recovery, ok)
	}

	startingRecovery, ok := daemon.recoverInterruptedOperation(ctx, daemonops.Record{
		ID:        "op-starting",
		ProjectID: projectID,
		IssueID:   "AZ-3",
		Kind:      "session.start",
	})
	if !ok || startingRecovery.State != daemonops.StateFailed {
		t.Fatalf("starting-only recovery = %+v, ok=%t; want terminal failure", startingRecovery, ok)
	}
}

func TestDaemonRecoverInterruptedTmuxOnlySessionStartWithoutAgentAcknowledgement(t *testing.T) {
	ctx := context.Background()
	projectID, issueID := "proj-tmux-only", "AZ-2"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	runner.panes[sessionID] = []string{"%7"}
	runner.panePIDs[sessionID] = 123
	runner.currentCommand = "zsh"
	d := &Daemon{
		cfg:  Config{RepoDir: t.TempDir(), SessionShell: "zsh", Logger: slog.Default()},
		tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{}, revision: map[string]uint64{},
	}
	required := false
	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{
		ID: "tmux-only-op", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart,
		Progress: &daemonops.Progress{Phase: "tmux_launch", AgentLaunchRequired: &required},
	})
	if !ok || recovery.State != daemonops.StateDone {
		t.Fatalf("tmux-only recovery = %+v, ok=%t; want done", recovery, ok)
	}
	if !runner.sessions[sessionID] {
		t.Fatalf("tmux-only session %s was compensated despite live runtime", sessionID)
	}
	projection, found, err := store.GetWorkerSessionStateByIssueID(ctx, projectID, issueID, sessionID)
	if err != nil || !found || projection.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("tmux-only projection = %+v, found=%t err=%v", projection, found, err)
	}
}

func TestDaemonRecoverInterruptedLegacyV58TmuxOnlyFinalProgress(t *testing.T) {
	ctx := context.Background()
	projectID, issueID := "proj-v58-tmux-only", "AZ-2"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	runner.currentCommand = "zsh"
	d := &Daemon{
		cfg:  Config{RepoDir: t.TempDir(), SessionShell: "zsh", Logger: slog.Default()},
		tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{}, revision: map[string]uint64{},
	}
	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{
		ID: "legacy-v58-tmux-only", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart,
		Progress: &daemonops.Progress{Phase: "tmux_launch", Message: "tmux session created without agent launch", Percent: 90},
	})
	if !ok || recovery.State != daemonops.StateDone || !runner.sessions[sessionID] {
		t.Fatalf("legacy v58 tmux-only recovery=%+v ok=%t live=%t", recovery, ok, runner.sessions[sessionID])
	}
}

func TestRootedOrchestratorIntentSupersedesWorkerLifecycleRecovery(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, issueID := "proj-1", "AZ-2"
	workerID := naming.CanonicalSessionID(repoDir, issueID)
	store := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	seeds := []daemonstate.Session{
		{ID: workerID, IssueID: issueID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, UpdatedAt: now},
		{ID: issueID, IssueID: issueID, Role: daemonstate.SessionRoleAdvisor, ScopeKind: daemonstate.SessionScopeInteraction, ScopeID: "request-1", State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, UpdatedAt: now.Add(time.Minute)},
		{ID: workerID, IssueID: issueID, Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: issueID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, UpdatedAt: now.Add(2 * time.Minute)},
	}
	for _, seed := range seeds {
		if err := upsertSessionStateFixture(store, ctx, projectID, seed); err != nil {
			t.Fatalf("seed %s: %v", seed.ID, err)
		}
	}
	runner := newSessionStartTmuxRunner()
	d := &Daemon{
		cfg: Config{RepoDir: repoDir}, tmux: tmux.NewClient(runner, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
	}
	worker, found, err := store.GetWorkerSessionStateByIssueID(ctx, projectID, issueID, workerID)
	if err != nil || found {
		t.Fatalf("retired worker intent = %+v found=%t err=%v", worker, found, err)
	}
	intents, err := store.ListSessionIntentStates(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	preserved := map[daemonstate.SessionRole]bool{}
	for _, row := range intents {
		if row.IssueID == issueID && row.Role != daemonstate.SessionRoleWorker && row.State == daemonstate.SessionStateRunning {
			preserved[row.Role] = true
		}
	}
	if !preserved[daemonstate.SessionRoleAdvisor] || !preserved[daemonstate.SessionRoleOrchestrator] {
		t.Fatalf("non-worker intents not preserved: %+v", intents)
	}
	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{ID: "op", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart})
	if !ok || recovery.State != daemonops.StateFailed {
		t.Fatalf("unplanned rooted recovery=%+v ok=%t, want terminal failure", recovery, ok)
	}
}

func TestInterruptedSessionStartFailsClosedBeforeManagedIncarnationPlanning(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, issueID := "proj-1", "AZ-2"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = store.Close() })
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worker intent: %v", err)
	}
	scope, err := domain.RootedOrchestrationScope(issueID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireOrchestratorScopeLease(ctx, identity, sessionID, func(context.Context, string) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("seed pre-atomic rooted lease: %v", err)
	}
	runner := newSessionStartTmuxRunner()
	d := &Daemon{
		cfg: Config{RepoDir: repoDir}, tmux: tmux.NewClient(runner, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
	}
	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{ID: "op", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart})
	if !ok || recovery.State != daemonops.StateFailed || !strings.Contains(recovery.ErrorMessage, "before durable managed-agent incarnation planning") {
		t.Fatalf("rooted crash-state recovery=%+v ok=%t, want typed pre-planning failure", recovery, ok)
	}
	if _, found, err := store.GetWorkerSessionStateByIssueID(ctx, projectID, issueID, sessionID); err != nil || found {
		t.Fatalf("worker intent after rooted compensation found=%t err=%v", found, err)
	}
	rooted, found, err := store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, issueID)
	if err != nil || !found || rooted.State != daemonstate.SessionStateStopped || rooted.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("rooted intent after fail-closed recovery=%+v found=%t err=%v", rooted, found, err)
	}
	if lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, identity); err != nil || found {
		t.Fatalf("rooted lease after fail-closed recovery=%+v found=%t err=%v", lease, found, err)
	}

	second, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{ID: "op-retry", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart})
	if !ok || second.State != daemonops.StateFailed || !strings.Contains(second.ErrorMessage, "before durable managed-agent incarnation planning") {
		t.Fatalf("second rooted crash-state recovery=%+v ok=%t, want identical typed failure", second, ok)
	}
	rooted, found, err = store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, issueID)
	if err != nil || !found || rooted.State != daemonstate.SessionStateStopped || rooted.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("rooted intent after repeated compensation=%+v found=%t err=%v", rooted, found, err)
	}
	rooted.State, rooted.ObservedState, rooted.UpdatedAt = daemonstate.SessionStateStarting, "", time.Now().UTC()
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).AcquireRooted(ctx, identity, rooted, func(context.Context, string) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("retry rooted acquisition after idempotent compensation: %v", err)
	}
}

func TestRecoverInterruptedLegacySessionStartCompensatesLiveShellRuntime(t *testing.T) {
	ctx := context.Background()
	projectID, issueID := "proj-legacy-start", "AZ-2"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStore := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	runner.currentCommand = "zsh"
	d := &Daemon{
		cfg:  Config{RepoDir: t.TempDir(), SessionShell: "zsh", Logger: slog.Default()},
		tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
	}
	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{
		ID: "legacy-op", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart,
	})
	if !ok || recovery.State != daemonops.StateFailed || !strings.Contains(recovery.ErrorMessage, "before durable managed-agent incarnation planning") {
		t.Fatalf("recovery = %+v, ok=%t", recovery, ok)
	}
	if runner.sessions[sessionID] {
		t.Fatalf("legacy shell runtime %s survived compensation", sessionID)
	}
	projection, found, err := runtimeStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || projection.State != daemonstate.SessionStateStopped || projection.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("compensated projection = %+v, found=%t err=%v", projection, found, err)
	}
}

func TestRecoverInterruptedPlannedSessionStartRemovesUnconsumedPrompt(t *testing.T) {
	ctx := context.Background()
	projectID, issueID := "proj-planned-start", "AZ-3"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStore := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	runner.sessionsWithoutPanes[sessionID] = true
	d := &Daemon{
		cfg:  Config{RepoDir: t.TempDir(), Logger: slog.Default()},
		tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
	}
	if err := ensureSessionLaunchArtifactDir(d.sessionLaunchArtifactDir()); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(d.sessionLaunchArtifactDir(), sessionLaunchArtifactPrefix+"planned.prompt")
	if err := os.WriteFile(promptPath, []byte("owner-only worker instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{
		ID: "planned-op", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart,
		Progress: &daemonops.Progress{Phase: "tmux_launch", AgentIncarnation: "planned", PromptHandoffPath: promptPath},
	})
	if !ok || recovery.State != daemonops.StateFailed {
		t.Fatalf("recovery = %+v, ok=%t", recovery, ok)
	}
	if _, err := os.Stat(promptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prompt artifact survived failed recovery: %v", err)
	}
	if runner.sessions[sessionID] {
		t.Fatalf("failed planned runtime %s survived compensation", sessionID)
	}
}

func TestRecoverInterruptedSessionStartRejectsUnsafePromptPathWithoutRemovingIt(t *testing.T) {
	ctx := context.Background()
	projectID, issueID := "proj-unsafe-start", "AZ-3"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStore := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = runtimeStore.Close() })
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	runner.sessionsWithoutPanes[sessionID] = true
	outsidePath := filepath.Join(t.TempDir(), "must-survive.prompt")
	if err := os.WriteFile(outsidePath, []byte("unrelated owner data"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:  Config{RepoDir: t.TempDir(), Logger: slog.Default()},
		tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
	}
	if err := ensureSessionLaunchArtifactDir(d.sessionLaunchArtifactDir()); err != nil {
		t.Fatal(err)
	}
	recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{
		ID: "unsafe-op", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart,
		Progress: &daemonops.Progress{Phase: "tmux_launch", AgentIncarnation: "planned", PromptHandoffPath: outsidePath},
	})
	if !ok || recovery.State != daemonops.StateFailed || !strings.Contains(recovery.ErrorMessage, "outside session launch artifact directory") {
		t.Fatalf("recovery = %+v, ok=%t", recovery, ok)
	}
	if got, err := os.ReadFile(outsidePath); err != nil || string(got) != "unrelated owner data" {
		t.Fatalf("unsafe target changed: contents=%q err=%v", got, err)
	}
	if runner.sessions[sessionID] {
		t.Fatalf("runtime %s survived unsafe recovery compensation", sessionID)
	}
}

func TestRecoverInterruptedAcknowledgedSessionStartReconstructsMissingProjection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selector sessionIntentSelector
	}{
		{name: "worker", selector: normalizeSessionIntentSelector(sessionIntentSelector{}, "AZ-4")},
		{name: "rooted orchestrator", selector: sessionIntentSelector{Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: "AZ-4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			projectID, issueID := "proj-ack-recovery", "AZ-4"
			sessionID := naming.CanonicalSessionID(projectID, issueID)
			runtimeStore := daemonstate.NewRuntimeStateStore(t.TempDir(), nil)
			t.Cleanup(func() { _ = runtimeStore.Close() })
			if tc.selector.Role == daemonstate.SessionRoleOrchestrator {
				if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
					ID: sessionID, IssueID: issueID, Role: tc.selector.Role, ScopeKind: tc.selector.ScopeKind,
					ScopeID: tc.selector.ScopeID, State: daemonstate.SessionStateStarting, ObservedState: daemonstate.SessionStateStarting,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := runtimeStore.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{
				ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: "7",
				PanePID: 123, AgentIncarnation: "planned", ObservedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			runner := newSessionStartTmuxRunner()
			runner.sessions[sessionID] = true
			runner.panes[sessionID] = []string{"%7"}
			runner.panePIDs[sessionID] = 123
			runner.currentCommand = "codex"
			d := &Daemon{
				cfg:  Config{RepoDir: t.TempDir(), Logger: slog.Default()},
				tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(),
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
				runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{}, revision: map[string]uint64{},
			}
			recovery, ok := d.recoverInterruptedOperation(ctx, daemonops.Record{
				ID: "ack-op", ProjectID: projectID, IssueID: issueID, Kind: daemonhandlers.CommandSessionStart,
				Progress: &daemonops.Progress{Phase: "agent_launch", AgentIncarnation: "planned"},
			})
			if !ok || recovery.State != daemonops.StateDone {
				t.Fatalf("recovery = %+v, ok=%t", recovery, ok)
			}
			projection, found, err := runtimeStore.GetSessionIntent(ctx, projectID, tc.selector.Role, tc.selector.ScopeKind, tc.selector.ScopeID)
			if err != nil || !found || projection.State != daemonstate.SessionStateStarting || projection.ObservedState != daemonstate.SessionStateRunning {
				t.Fatalf("reconstructed projection = %+v, found=%t err=%v", projection, found, err)
			}
			if tc.selector.Role == daemonstate.SessionRoleOrchestrator {
				scope, _ := domain.RootedOrchestrationScope(issueID)
				identity, _ := domain.NewOrchestratorIdentity(projectID, scope)
				lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(runtimeStore).Get(ctx, identity)
				if err != nil || !found || lease.SessionID != sessionID {
					t.Fatalf("reconstructed rooted lease = %+v, found=%t err=%v", lease, found, err)
				}
			}
		})
	}
}

func TestSessionOperationExecutorWrapsNestedResultEnvelope(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.sessionStart = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "started"}), nil
	}
	executor := sessionOperationExecutor{runtime: runtime}
	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-3"})
	req := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("req-session"), Kind: protocol.EnvelopeKindCommand, Command: "session.start", Meta: protocol.Metadata{ProjectID: "proj-1"}, Body: payload}

	resp, err := executor.Execute(context.Background(), req, req.Command, func(_ context.Context) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "started"}), nil
	})
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response = %+v", resp)
	}
	wrapped := decodeWrappedResult(t, resp.Body)
	if wrapped.OperationID == "" || wrapped.State != string(daemonops.StateDone) {
		t.Fatalf("wrapped response = %+v", wrapped)
	}
	var nested struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(wrapped.Result, &nested); err != nil {
		t.Fatalf("unmarshal nested result: %v", err)
	}
	if nested.Output != "started" {
		t.Fatalf("nested output = %q, want started", nested.Output)
	}
}

func TestCommandExecutorWrapsGitAndWorktreeResults(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.gitHandler = daemonhandlers.NewGitHandler(runtimeGitService{})
	runtime.worktreeHandler = daemonhandlers.NewWorktreeHandler(runtimeWorktreeService{})
	executor := operationCommandExecutor{runtime: runtime}

	cases := []struct {
		name    string
		command string
		body    any
		result  any
	}{
		{
			name:    "git merge",
			command: daemonhandlers.CommandGitMergeRef,
			body:    map[string]string{"worktree": "/tmp/wt", "branch": "main"},
			result:  map[string]string{"worktree": "/tmp/wt", "branch": "main"},
		},
		{
			name:    "worktree cleanup",
			command: daemonhandlers.CommandWorktreeCleanupOrphaned,
			body:    map[string]string{"project_id": "proj-1"},
			result:  protocol.CleanupOrphanedResponseBody{ProjectID: "proj-1", WorktreesRemoved: 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("req-" + tc.name), Kind: protocol.EnvelopeKindCommand, Command: tc.command, Meta: protocol.Metadata{ProjectID: "proj-1"}, Body: mustJSON(t, tc.body)}
			resp := executor.Execute(context.Background(), req, tc.command, func(_ context.Context) protocol.ResponseEnvelope {
				return testResponse(req, tc.result)
			})
			if !resp.OK {
				t.Fatalf("response = %+v", resp)
			}
			wrapped := decodeWrappedResult(t, resp.Body)
			if wrapped.OperationID == "" || wrapped.State != string(daemonops.StateDone) {
				t.Fatalf("wrapped response = %+v", wrapped)
			}
		})
	}
}

func TestDaemonDrainInFlightCommandsStopsIntakeCancelsQueuedAndDrainsRunning(t *testing.T) {
	release := make(chan struct{})
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.sessionStart = func(ctx context.Context, _ protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		select {
		case <-release:
			return protocol.ResponseEnvelope{OK: true, Body: mustJSON(t, map[string]string{"output": "done"})}, nil
		case <-ctx.Done():
			return protocol.ResponseEnvelope{}, ctx.Err()
		}
	}
	runtime.sessionStop = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "queued cancel"}), nil
	}

	runningReq := runtime.buildSubmitRequestForTest(t, "session.start", "proj-1", mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-10"}))
	queuedReq := runtime.buildSubmitRequestForTest(t, "session.stop", "proj-1", mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-10"}))
	if _, err := runtime.manager.Submit(context.Background(), runningReq, func(ctx context.Context) ([]byte, error) {
		resp, err := runtime.sessionStart(ctx, protocol.RequestEnvelope{})
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}); err != nil {
		t.Fatalf("submit running operation: %v", err)
	}
	queuedResult, err := runtime.manager.Submit(context.Background(), queuedReq, func(ctx context.Context) ([]byte, error) {
		resp, err := runtime.sessionStop(ctx, protocol.RequestEnvelope{})
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	})
	if err != nil {
		t.Fatalf("submit queued operation: %v", err)
	}

	waitForRuntimeState(t, runtime, queuedResult.Record.ID, daemonops.StateQueued)

	d := &Daemon{cfg: Config{IdleTimeout: 250 * time.Millisecond}, operationRuntime: runtime}
	drained := make(chan struct{})
	go func() {
		d.drainInFlightCommands()
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("drain returned before running operation completed")
	case <-time.After(50 * time.Millisecond):
	}

	if err := d.beginCommand(); err == nil {
		t.Fatal("expected new intake rejection while draining")
	}

	close(release)

	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish")
	}

	waitForRuntimeState(t, runtime, queuedResult.Record.ID, daemonops.StateCancelled)
}

func TestBuildSubmitRequestRoutesSessionStartByIssueAndSession(t *testing.T) {
	runtime := newSessionStartOperationRuntime(t)

	startReq := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandSessionStart,
		"proj-1",
		mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-10"}),
	)
	if !hasResourceKey(startReq.ResourceKeys, "issue:proj-1:AZ-10") {
		t.Fatalf("session.start resource keys = %v, want issue key", startReq.ResourceKeys)
	}
	if !hasResourceKey(startReq.ResourceKeys, "session:"+naming.CanonicalSessionID("proj-1", "AZ-10")) {
		t.Fatalf("session.start resource keys = %v, want session key", startReq.ResourceKeys)
	}
	if hasResourceKey(startReq.ResourceKeys, heavySessionStartResourceKey("proj-1")) {
		t.Fatalf("session.start resource keys = %v, did not want project heavy-start key", startReq.ResourceKeys)
	}

	stopReq := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandSessionStop,
		"proj-1",
		mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-10"}),
	)
	if hasResourceKey(stopReq.ResourceKeys, heavySessionStartResourceKey("proj-1")) {
		t.Fatalf("session.stop resource keys = %v, did not want project heavy-start key", stopReq.ResourceKeys)
	}
}

func TestBuildSubmitRequestUsesPayloadProjectForSessionStartIssueResource(t *testing.T) {
	runtime := newSessionStartOperationRuntime(t)

	req := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandSessionStart,
		protocol.DefaultProjectID,
		mustJSON(t, map[string]string{"project_id": "payload-proj", "session_id": "AZ-10"}),
	)

	if !hasResourceKey(req.ResourceKeys, "issue:payload-proj:AZ-10") {
		t.Fatalf("session.start resource keys = %v, want payload project issue key", req.ResourceKeys)
	}
	if !hasResourceKey(req.ResourceKeys, "session:"+naming.CanonicalSessionID("payload-proj", "AZ-10")) {
		t.Fatalf("session.start resource keys = %v, want payload project session key", req.ResourceKeys)
	}
	if hasResourceKey(req.ResourceKeys, heavySessionStartResourceKey("payload-proj")) {
		t.Fatalf("session.start resource keys = %v, did not want payload project heavy-start key", req.ResourceKeys)
	}
	if hasResourceKey(req.ResourceKeys, heavySessionStartResourceKey(protocol.DefaultProjectID)) {
		t.Fatalf("session.start resource keys = %v, did not want meta project heavy-start key", req.ResourceKeys)
	}
}

func TestBuildSubmitRequestPreservesExplicitSessionStartKeys(t *testing.T) {
	runtime := newSessionStartOperationRuntime(t)
	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-11"})

	req, err := runtime.buildSubmitRequest(daemonhandlers.CommandSessionStart, "proj-1", payload, operationSubmitOverrides{
		IssueID:      naming.IssueID("AZ-11"),
		DedupeKey:    "session.start:AZ-11",
		ResourceKeys: []string{"session:AZ-11", "worktree:AZ-11"},
	})
	if err != nil {
		t.Fatalf("build submit request: %v", err)
	}

	for _, key := range []string{"issue:proj-1:AZ-11", "session:" + naming.CanonicalSessionID("proj-1", "AZ-11"), "session:AZ-11", "worktree:AZ-11"} {
		if !hasResourceKey(req.ResourceKeys, key) {
			t.Fatalf("resource keys = %v, want %s", req.ResourceKeys, key)
		}
	}
	if hasResourceKey(req.ResourceKeys, heavySessionStartResourceKey("proj-1")) {
		t.Fatalf("resource keys = %v, did not want project heavy-start key", req.ResourceKeys)
	}
}

func TestBuildSubmitRequestRoutesBulkCleanupAsProjectOperation(t *testing.T) {
	runtime := &operationRuntime{
		taskBulkCleanup: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{OK: true}, nil
		},
	}
	payload := mustJSON(t, map[string]any{"task_ids": []string{"az-1", "az-2"}, "closed_outcome": "cancelled"})

	first, err := runtime.buildSubmitRequest(protocol.CommandTaskBulkCleanup, "proj-1", payload, operationSubmitOverrides{})
	if err != nil {
		t.Fatalf("build submit request: %v", err)
	}
	second, err := runtime.buildSubmitRequest(protocol.CommandTaskBulkCleanup, "proj-1", payload, operationSubmitOverrides{})
	if err != nil {
		t.Fatalf("build duplicate submit request: %v", err)
	}

	if !hasResourceKey(first.ResourceKeys, "project:proj-1:issue-lifecycle-cleanup") {
		t.Fatalf("resource keys = %v", first.ResourceKeys)
	}
	if first.DedupeKey == "" || first.DedupeKey != second.DedupeKey {
		t.Fatalf("dedupe keys = %q, %q", first.DedupeKey, second.DedupeKey)
	}
	if first.RecentDedupeWindow != 0 {
		t.Fatalf("recent dedupe window = %s, want immediate completed retry", first.RecentDedupeWindow)
	}
}

func TestBuildSubmitRequestAddsSessionStartMinimumKeysToExplicitKeys(t *testing.T) {
	runtime := newSessionStartOperationRuntime(t)
	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-13"})

	req, err := runtime.buildSubmitRequest(daemonhandlers.CommandSessionStart, "proj-1", payload, operationSubmitOverrides{
		IssueID:      naming.IssueID("AZ-13"),
		ResourceKeys: []string{"issue:proj-1:AZ-13", "worktree:AZ-13"},
	})
	if err != nil {
		t.Fatalf("build submit request: %v", err)
	}

	for _, key := range []string{"issue:proj-1:AZ-13", "session:" + naming.CanonicalSessionID("proj-1", "AZ-13"), "worktree:AZ-13"} {
		if !hasResourceKey(req.ResourceKeys, key) {
			t.Fatalf("resource keys = %v, want %s", req.ResourceKeys, key)
		}
	}
}

func TestBuildSubmitRequestPreservesCustomSessionStartID(t *testing.T) {
	runtime := newSessionStartOperationRuntime(t)
	payload := mustJSON(t, map[string]string{
		"project_id": "proj-1",
		"issue_id":   "AZ-12",
		"session_id": "custom-AZ-12",
	})

	req := runtime.buildSubmitRequestForTest(t, daemonhandlers.CommandSessionStart, "proj-1", payload)

	if req.IssueID != "AZ-12" {
		t.Fatalf("issue id = %q, want AZ-12", req.IssueID)
	}
	if req.DedupeKey != daemonhandlers.CommandSessionStart+":AZ-12" {
		t.Fatalf("dedupe key = %q, want custom session start issue dedupe", req.DedupeKey)
	}
	for _, key := range []string{"issue:proj-1:AZ-12", "session:custom-AZ-12"} {
		if !hasResourceKey(req.ResourceKeys, key) {
			t.Fatalf("resource keys = %v, want %s", req.ResourceKeys, key)
		}
	}
}

func TestOperationRuntimeAllowsDifferentIssueSessionStartsConcurrently(t *testing.T) {
	runtime := newSessionStartOperationRuntime(t)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{}, 1)

	firstReq := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandSessionStart,
		"proj-1",
		mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-10"}),
	)
	secondReq := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandSessionStart,
		"proj-1",
		mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-11"}),
	)

	if _, err := runtime.manager.Submit(context.Background(), firstReq, func(context.Context) ([]byte, error) {
		close(firstStarted)
		<-firstRelease
		return nil, nil
	}); err != nil {
		t.Fatalf("submit first session.start: %v", err)
	}
	<-firstStarted

	if _, err := runtime.manager.Submit(context.Background(), secondReq, func(context.Context) ([]byte, error) {
		secondStarted <- struct{}{}
		return nil, nil
	}); err != nil {
		t.Fatalf("submit second session.start: %v", err)
	}

	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second different-issue session.start did not run concurrently")
	}

	close(firstRelease)
	if err := runtime.manager.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestOperationRuntimeAllowsDifferentProjectSessionStartsConcurrently(t *testing.T) {
	runtime := newSessionStartOperationRuntime(t)
	started := make(chan string, 2)
	release := make(chan struct{})

	runner := func(name string) daemonops.Runner {
		return func(context.Context) ([]byte, error) {
			started <- name
			<-release
			return nil, nil
		}
	}
	firstReq := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandSessionStart,
		"proj-1",
		mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-10"}),
	)
	secondReq := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandSessionStart,
		"proj-2",
		mustJSON(t, map[string]string{"project_id": "proj-2", "session_id": "AZ-11"}),
	)

	if _, err := runtime.manager.Submit(context.Background(), firstReq, runner("first")); err != nil {
		t.Fatalf("submit first session.start: %v", err)
	}
	if _, err := runtime.manager.Submit(context.Background(), secondReq, runner("second")); err != nil {
		t.Fatalf("submit second session.start: %v", err)
	}

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatalf("expected both different-project starts to run concurrently, saw %v", seen)
		}
	}

	close(release)
	if err := runtime.manager.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func TestBuildSubmitRequestDefaultsGitFetchRemote(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:      t.TempDir(),
		nextRevision: sequentialRevision(),
		gitHandler:   daemonhandlers.NewGitHandler(runtimeGitService{}),
	})

	req := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandGitFetch,
		"proj-1",
		mustJSON(t, map[string]string{
			"worktree": "/tmp/az-1",
		}),
	)

	if req.IssueID != "" {
		t.Fatalf("issue id = %q, want empty for non-issue operation", req.IssueID)
	}
	if req.DedupeKey != "git.fetch:/tmp/az-1:origin" {
		t.Fatalf("dedupe key = %q, want default origin remote", req.DedupeKey)
	}
	if len(req.ResourceKeys) != 1 || req.ResourceKeys[0] != "worktree:/tmp/az-1" {
		t.Fatalf("resource keys = %v, want worktree routing key", req.ResourceKeys)
	}
}

func TestBuildSubmitRequestRoutesGitPullBaseByWorktreeAndBase(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:      t.TempDir(),
		nextRevision: sequentialRevision(),
		gitHandler:   daemonhandlers.NewGitHandler(runtimeGitService{}),
	})

	req := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandGitPullBase,
		"proj-1",
		mustJSON(t, map[string]string{
			"worktree":    "/tmp/az-1",
			"base_branch": "main",
		}),
	)

	if req.IssueID != "" {
		t.Fatalf("issue id = %q, want empty for non-issue operation", req.IssueID)
	}
	if req.DedupeKey != "git.pull_base:/tmp/az-1:origin:main" {
		t.Fatalf("dedupe key = %q, want default origin/base branch", req.DedupeKey)
	}
	if len(req.ResourceKeys) != 1 || req.ResourceKeys[0] != "worktree:/tmp/az-1" {
		t.Fatalf("resource keys = %v, want worktree routing key", req.ResourceKeys)
	}
}

func TestBuildSubmitRequestRoutesGitPushByWorktreeAndBranch(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision(), gitHandler: daemonhandlers.NewGitHandler(runtimeGitService{})})
	req := runtime.buildSubmitRequestForTest(t, daemonhandlers.CommandGitPush, "proj-1", mustJSON(t, map[string]string{"worktree": "/tmp/az-1", "branch": "main"}))
	if req.DedupeKey != "git.push:/tmp/az-1:origin:main" {
		t.Fatalf("dedupe key = %q", req.DedupeKey)
	}
	if len(req.ResourceKeys) != 1 || req.ResourceKeys[0] != "worktree:/tmp/az-1" {
		t.Fatalf("resource keys = %v", req.ResourceKeys)
	}
}

func TestBuildSubmitRequestRoutesSessionResolveConflictByIssueAndWorktree(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:      t.TempDir(),
		nextRevision: sequentialRevision(),
	})
	runtime.sessionResolveConflict = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, protocol.SessionResolveConflictResponseBody{}), nil
	}

	req := runtime.buildSubmitRequestForTest(
		t,
		protocol.CommandSessionResolveConflict,
		"proj-1",
		mustJSON(t, protocol.SessionResolveConflictRequestBody{
			ProjectID: naming.ProjectID("proj-1"),
			IssueID:   naming.IssueID("AZ-10"),
			Worktree:  "/tmp/project-az-10/",
		}),
	)

	if req.IssueID != "AZ-10" {
		t.Fatalf("issue id = %q, want AZ-10", req.IssueID)
	}
	if req.DedupeKey != protocol.CommandSessionResolveConflict+":AZ-10" {
		t.Fatalf("dedupe key = %q, want resolve conflict issue key", req.DedupeKey)
	}
	wantResources := []string{"issue:proj-1:AZ-10", "worktree:/tmp/project-az-10"}
	if len(req.ResourceKeys) != len(wantResources) {
		t.Fatalf("resource keys = %v, want %v", req.ResourceKeys, wantResources)
	}
	for i := range wantResources {
		if req.ResourceKeys[i] != wantResources[i] {
			t.Fatalf("resource key[%d] = %q, want %q", i, req.ResourceKeys[i], wantResources[i])
		}
	}
}

func TestBuildSubmitRequestRoutesSessionRestartAllDurablyByProjectAndPayload(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.sessionRestartAll = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, protocol.SessionRestartAllResponseBody{}), nil
	}
	body := mustJSON(t, protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID("proj-1"), ForceBusy: true})
	first := runtime.buildSubmitRequestForTest(t, protocol.CommandSessionRestartAll, "proj-1", body)
	second := runtime.buildSubmitRequestForTest(t, protocol.CommandSessionRestartAll, "proj-1", body)
	if first.DedupeKey == "" || first.DedupeKey != second.DedupeKey {
		t.Fatalf("dedupe keys = %q and %q, want stable payload identity", first.DedupeKey, second.DedupeKey)
	}
	if len(first.ResourceKeys) != 1 || first.ResourceKeys[0] != "project:proj-1:session-restart" {
		t.Fatalf("resource keys = %v", first.ResourceKeys)
	}
	if first.RecentDedupeWindow != 0 {
		t.Fatalf("restart retry dedupe window = %s, want active-operation dedupe only", first.RecentDedupeWindow)
	}
}

func TestBuildSubmitRequestNormalizesWorktreeForConflictSerialization(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:      t.TempDir(),
		nextRevision: sequentialRevision(),
		gitHandler:   daemonhandlers.NewGitHandler(runtimeGitService{}),
	})

	firstReq := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandGitMergeRef,
		"proj-1",
		mustJSON(t, map[string]string{
			"worktree": "/tmp/az-1/",
			"branch":   "main",
		}),
	)
	secondReq := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandGitMergeRef,
		"proj-1",
		mustJSON(t, map[string]string{
			"worktree": "/tmp/az-1",
			"branch":   "release",
		}),
	)

	if firstReq.IssueID != "" || secondReq.IssueID != "" {
		t.Fatalf("issue ids = %q and %q, want empty for worktree operations", firstReq.IssueID, secondReq.IssueID)
	}
	if len(firstReq.ResourceKeys) != 1 || firstReq.ResourceKeys[0] != "worktree:/tmp/az-1" {
		t.Fatalf("first resource keys = %v, want normalized worktree key", firstReq.ResourceKeys)
	}
	if len(secondReq.ResourceKeys) != 1 || secondReq.ResourceKeys[0] != "worktree:/tmp/az-1" {
		t.Fatalf("second resource keys = %v, want normalized worktree key", secondReq.ResourceKeys)
	}

	firstRunning := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{}, 1)

	if _, err := runtime.manager.Submit(context.Background(), firstReq, func(context.Context) ([]byte, error) {
		close(firstRunning)
		<-firstRelease
		return nil, nil
	}); err != nil {
		t.Fatalf("submit first merge: %v", err)
	}
	<-firstRunning

	if _, err := runtime.manager.Submit(context.Background(), secondReq, func(context.Context) ([]byte, error) {
		secondStarted <- struct{}{}
		return nil, nil
	}); err != nil {
		t.Fatalf("submit second merge: %v", err)
	}

	select {
	case <-secondStarted:
		t.Fatal("second merge started before conflicting normalized key was released")
	case <-time.After(50 * time.Millisecond):
	}

	close(firstRelease)

	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second merge did not start after first merge completed")
	}

	if err := runtime.manager.Drain(context.Background()); err != nil {
		t.Fatalf("drain error: %v", err)
	}
}

func (r *operationRuntime) buildSubmitRequestForTest(t *testing.T, kind, projectID string, payload []byte) daemonops.SubmitRequest {
	t.Helper()
	req, err := r.buildSubmitRequest(kind, projectID, payload, operationSubmitOverrides{})
	if err != nil {
		t.Fatalf("build submit request: %v", err)
	}
	return req
}

func newSessionStartOperationRuntime(t *testing.T) *operationRuntime {
	t.Helper()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.sessionStart = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "started"}), nil
	}
	runtime.sessionStop = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "stopped"}), nil
	}
	return runtime
}

func testRequest(command string, body any) protocol.RequestEnvelope {
	return protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("req-" + command), Kind: protocol.EnvelopeKindCommand, Command: command, Body: marshalJSON(body)}
}

func mustJSON(tb testing.TB, value any) []byte {
	tb.Helper()
	return marshalJSON(value)
}

func testResponse(req protocol.RequestEnvelope, body any) protocol.ResponseEnvelope {
	return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, CompletedAt: time.Now().UTC(), OK: true, Body: marshalJSON(body)}
}

func decodeWrappedResult(t *testing.T, body []byte) operationResultEnvelope {
	t.Helper()
	var wrapped operationResultEnvelope
	if err := json.Unmarshal(body, &wrapped); err != nil {
		t.Fatalf("unmarshal wrapped result: %v", err)
	}
	return wrapped
}

func waitForRuntimeState(t *testing.T, runtime *operationRuntime, operationID string, want daemonops.State) daemonops.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		record, err := runtime.manager.Get(context.Background(), operationID)
		if err == nil && record.State == want {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	record, err := runtime.manager.Get(context.Background(), operationID)
	if err != nil {
		t.Fatalf("get operation %s: %v", operationID, err)
	}
	t.Fatalf("operation %s state = %s, want %s", operationID, record.State, want)
	return daemonops.Record{}
}

func waitForRuntimeProgress(t *testing.T, runtime *operationRuntime, operationID, wantPhase string) daemonops.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		record, err := runtime.manager.Get(context.Background(), operationID)
		if err == nil && record.Progress != nil && record.Progress.Phase == wantPhase {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	record, err := runtime.manager.Get(context.Background(), operationID)
	if err != nil {
		t.Fatalf("get operation %s: %v", operationID, err)
	}
	t.Fatalf("operation %s progress = %+v, want phase %s", operationID, record.Progress, wantPhase)
	return daemonops.Record{}
}

func hasResourceKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}

func collectOperationEvents(t *testing.T, ch <-chan protocol.EventEnvelope, count int) []protocol.EventEnvelope {
	t.Helper()
	events := make([]protocol.EventEnvelope, 0, count)
	deadline := time.After(2 * time.Second)
	for len(events) < count {
		select {
		case evt := <-ch:
			events = append(events, evt)
		case <-deadline:
			t.Fatalf("timed out waiting for %d operation events, got %d", count, len(events))
		}
	}
	return events
}

func sequentialRevision() func(string) uint64 {
	var next uint64
	return func(string) uint64 {
		next++
		return next
	}
}

func marshalJSON(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}
