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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestRuntimeReconcileRefreshesCrossProjectProjectionAfterSourceChanges(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	const projectID = "runtime-refresh-project"
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: projectID, Name: "Runtime refresh", Path: root}}}); err != nil {
		t.Fatal(err)
	}
	issueClient := issues.NewClientAtPath(filepath.Join(root, ".azedarach", "azedarach.db"), slog.Default())
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "Created before reconcile", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	store, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg:                   Config{RepoDir: root, Logger: slog.Default()},
		userStore:             store,
		issueClientsByProject: map[string]*issues.Client{projectID: issueClient},
	}

	result, err := newRuntimeReconcileService(d).Reconcile(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if result.CrossProjectProjection == nil || result.CrossProjectProjection.Freshness != protocol.GlobalProjectionFreshnessFresh {
		t.Fatalf("cross-project projection health = %+v", result.CrossProjectProjection)
	}
	snapshot, err := store.Snapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 1 || len(snapshot.Projects[0].Tasks) != 1 || snapshot.Projects[0].Tasks[0].ID.String() != issueID {
		t.Fatalf("post-reconcile projection = %+v", snapshot.Projects)
	}
}

type runtimeReconcileRecorder struct {
	mu            sync.Mutex
	calls         int
	projectIDs    []string
	issueIDs      [][]string
	started       chan struct{}
	finished      chan struct{}
	waitForCancel bool
	result        protocol.RuntimeReconcileResponseBody
	err           error
}

func (r *runtimeReconcileRecorder) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	r.mu.Lock()
	r.calls++
	r.projectIDs = append(r.projectIDs, projectID)
	started := r.started
	finished := r.finished
	waitForCancel := r.waitForCancel
	result := r.result
	err := r.err
	r.mu.Unlock()

	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}

	if waitForCancel {
		<-ctx.Done()
		if err == nil {
			err = ctx.Err()
		}
	}
	if finished != nil {
		select {
		case finished <- struct{}{}:
		default:
		}
	}

	return result, err
}

func (r *runtimeReconcileRecorder) ReconcileIssues(ctx context.Context, projectID string, issueIDs []string) (protocol.RuntimeReconcileResponseBody, error) {
	r.mu.Lock()
	r.issueIDs = append(r.issueIDs, append([]string(nil), issueIDs...))
	r.mu.Unlock()
	return r.Reconcile(ctx, projectID)
}

func (r *runtimeReconcileRecorder) snapshot() (calls int, projectIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]string(nil), r.projectIDs...)
}

func (r *runtimeReconcileRecorder) issueSnapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, 0, len(r.issueIDs))
	for _, issueIDs := range r.issueIDs {
		out = append(out, append([]string(nil), issueIDs...))
	}
	return out
}

func startBlockingHeavySessionStart(t *testing.T, projectID, issueID string) *operationRuntime {
	t.Helper()

	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir()})
	release := make(chan struct{})
	var releaseOnce sync.Once
	cleanup := func() {
		releaseOnce.Do(func() {
			close(release)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = runtime.Drain(ctx)
			_ = runtime.Close()
		})
	}
	t.Cleanup(cleanup)

	submitResult, err := runtime.manager.Submit(context.Background(), daemonops.SubmitRequest{
		ProjectID:    projectID,
		IssueID:      issueID,
		Kind:         daemonhandlers.CommandSessionStart,
		DedupeKey:    daemonhandlers.CommandSessionStart + ":" + issueID,
		ResourceKeys: []string{"issue:" + projectID + ":" + issueID, "session:ch-" + issueID},
	}, func(ctx context.Context) ([]byte, error) {
		select {
		case <-release:
			return []byte(`{}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("submit heavy session-start operation: %v", err)
	}
	waitForRuntimeState(t, runtime, submitResult.Record.ID, daemonops.StateRunning)
	return runtime
}

type sequentialRuntimeReconciler struct {
	mu         sync.Mutex
	calls      int
	projectIDs []string
}

func (r *sequentialRuntimeReconciler) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.projectIDs = append(r.projectIDs, projectID)
	r.mu.Unlock()

	if call == 1 {
		<-ctx.Done()
		return protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}, ctx.Err()
	}
	return protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}, nil
}

func (r *sequentialRuntimeReconciler) ReconcileIssues(ctx context.Context, projectID string, _ []string) (protocol.RuntimeReconcileResponseBody, error) {
	return r.Reconcile(ctx, projectID)
}

type blockingIssueProjectionReconciler struct {
	store     *daemonstate.RuntimeStateStore
	started   chan struct{}
	release   chan struct{}
	issueID   string
	sessionID string
}

func (r *blockingIssueProjectionReconciler) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	return r.ReconcileIssues(ctx, projectID, []string{r.issueID})
}

func (r *blockingIssueProjectionReconciler) ReconcileIssues(ctx context.Context, projectID string, issueIDs []string) (protocol.RuntimeReconcileResponseBody, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}, ctx.Err()
	}
	if len(issueIDs) > 0 && r.store != nil {
		issueID := issueIDs[0]
		sessionID := r.sessionID
		if sessionID == "" {
			sessionID = naming.CanonicalSessionID(projectID, issueID)
		}
		if err := r.store.UpsertSessionState(ctx, projectID, daemonstate.Session{
			ID:            sessionID,
			IssueID:       issueID,
			State:         daemonstate.SessionStateAttached,
			ObservedState: daemonstate.SessionStateAttached,
			UpdatedAt:     time.Now().UTC(),
		}); err != nil {
			return protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}, err
		}
	}
	return protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}, nil
}

func (r *sequentialRuntimeReconciler) snapshot() (calls int, projectIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]string(nil), r.projectIDs...)
}

type scriptedRuntimeReconciler struct {
	mu            sync.Mutex
	started       chan string
	releaseByID   map[string]chan struct{}
	startOrder    []string
	callCountByID map[string]int
	current       int
	maxConcurrent int
	resultsByID   map[string]protocol.RuntimeReconcileResponseBody
}

func (r *scriptedRuntimeReconciler) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	r.mu.Lock()
	if r.callCountByID == nil {
		r.callCountByID = map[string]int{}
	}
	r.callCountByID[projectID]++
	r.startOrder = append(r.startOrder, projectID)
	r.current++
	if r.current > r.maxConcurrent {
		r.maxConcurrent = r.current
	}
	started := r.started
	release := map[string]chan struct{}(nil)
	if r.releaseByID != nil {
		release = r.releaseByID
	}
	result := protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}
	if stored, ok := r.resultsByID[projectID]; ok {
		result = stored
		if result.ProjectID == "" {
			result.ProjectID = naming.ProjectID(projectID)
		}
	}
	r.mu.Unlock()

	if started != nil {
		select {
		case started <- projectID:
		default:
		}
	}

	if release != nil {
		if gate := release[projectID]; gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				r.mu.Lock()
				r.current--
				r.mu.Unlock()
				return protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}, ctx.Err()
			}
		}
	}

	r.mu.Lock()
	r.current--
	r.mu.Unlock()
	return result, nil
}

func (r *scriptedRuntimeReconciler) ReconcileIssues(ctx context.Context, projectID string, _ []string) (protocol.RuntimeReconcileResponseBody, error) {
	return r.Reconcile(ctx, projectID)
}

func (r *scriptedRuntimeReconciler) snapshot() (order []string, callCount map[string]int, maxConcurrent int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	outCounts := make(map[string]int, len(r.callCountByID))
	for key, value := range r.callCountByID {
		outCounts[key] = value
	}
	return append([]string(nil), r.startOrder...), outCounts, r.maxConcurrent
}

type dedupedTimeoutRuntimeReconciler struct {
	mu      sync.Mutex
	started chan struct{}
	calls   int
	result  protocol.RuntimeReconcileResponseBody
}

func (r *dedupedTimeoutRuntimeReconciler) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	started := r.started
	result := r.result
	if result.ProjectID == "" {
		result.ProjectID = naming.ProjectID(projectID)
	}
	r.mu.Unlock()

	if call == 1 {
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}, ctx.Err()
	}
	return result, nil
}

func (r *dedupedTimeoutRuntimeReconciler) ReconcileIssues(ctx context.Context, projectID string, _ []string) (protocol.RuntimeReconcileResponseBody, error) {
	return r.Reconcile(ctx, projectID)
}

func (r *dedupedTimeoutRuntimeReconciler) snapshot() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type runtimeReconcileTestServer struct {
	served        chan struct{}
	waitForCancel bool
	release       <-chan struct{}
}

func (s *runtimeReconcileTestServer) Serve(ctx context.Context) error {
	if s.served != nil {
		select {
		case s.served <- struct{}{}:
		default:
		}
	}
	if s.waitForCancel {
		<-ctx.Done()
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	}
	<-ctx.Done()
	return nil
}

type runtimeReconcileTestLock struct{}

func (runtimeReconcileTestLock) Acquire() (*lifecycle.Lease, error) {
	return &lifecycle.Lease{}, nil
}

func (runtimeReconcileTestLock) Release() error {
	return nil
}

type emptyTmuxRunner struct{}

func (emptyTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) > 0 && (args[0] == "list-sessions" || args[0] == "list-panes") {
		return "", errors.New("no tmux sessions")
	}
	return "", nil
}

type emptyGitRunner struct{}

func (emptyGitRunner) Run(context.Context, ...string) (string, error) {
	return "", nil
}

type signalingTmuxRunner struct {
	started chan struct{}
	once    sync.Once
}

func (r *signalingTmuxRunner) Run(ctx context.Context, args ...string) (string, error) {
	r.once.Do(func() { close(r.started) })
	return emptyTmuxRunner{}.Run(ctx, args...)
}

func TestCommandRuntimeReconcileRoutesToManualRepair(t *testing.T) {
	recorder := &runtimeReconcileRecorder{
		result: protocol.RuntimeReconcileResponseBody{
			ProjectID:             "proj-runtime",
			WorktreesRefreshed:    2,
			RecreatedTmuxSessions: 1,
			AlignedDaemonSessions: 1,
		},
	}
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		runtimeReconciler: recorder,
	}

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-runtime-reconcile",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandRuntimeReconcile,
		Meta:            protocol.Metadata{ProjectID: " proj-runtime "},
		Body: mustJSONBody(t, protocol.RuntimeReconcileRequestBody{
			ProjectID: " proj-runtime ",
		}),
	})
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("runtime.reconcile response not OK: %+v", resp.Error)
	}

	var out protocol.RuntimeReconcileResponseBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("unmarshal runtime.reconcile body: %v", err)
	}
	if out.ProjectID != "proj-runtime" {
		t.Fatalf("project id = %q, want proj-runtime", out.ProjectID)
	}
	if out.WorktreesRefreshed != 2 || out.RecreatedTmuxSessions != 1 || out.AlignedDaemonSessions != 1 {
		t.Fatalf("runtime reconcile body = %+v", out)
	}
	if got := out.InvariantSources[string(daemonInvariantSessionStartConflict)]; got != string(daemonInvariantSourceTmux) {
		t.Fatalf("invariant_sources[%q] = %q, want %q", daemonInvariantSessionStartConflict, got, daemonInvariantSourceTmux)
	}
	if got := out.InvariantSources[string(daemonInvariantTaskListFreshness)]; got != string(daemonInvariantSourceProjection) {
		t.Fatalf("invariant_sources[%q] = %q, want %q", daemonInvariantTaskListFreshness, got, daemonInvariantSourceProjection)
	}
	if got := out.InvariantSources[string(daemonInvariantOrchestrationScope)]; got != string(daemonInvariantSourceProjection) {
		t.Fatalf("invariant_sources[%q] = %q, want %q", daemonInvariantOrchestrationScope, got, daemonInvariantSourceProjection)
	}
	if got := out.InvariantSources[string(daemonInvariantOrchestrationSingleton)]; got != string(daemonInvariantSourceHybrid) {
		t.Fatalf("invariant_sources[%q] = %q, want %q", daemonInvariantOrchestrationSingleton, got, daemonInvariantSourceHybrid)
	}
	if got := out.InvariantSources[string(daemonInvariantOrchestrationCompletion)]; got != string(daemonInvariantSourceHybrid) {
		t.Fatalf("invariant_sources[%q] = %q, want %q", daemonInvariantOrchestrationCompletion, got, daemonInvariantSourceHybrid)
	}
	if got := out.InvariantSources[string(daemonInvariantOrchestrationCandidates)]; got != string(daemonInvariantSourceProjection) {
		t.Fatalf("invariant_sources[%q] = %q, want %q", daemonInvariantOrchestrationCandidates, got, daemonInvariantSourceProjection)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("runtime reconcile calls = %d, want 1", calls)
	}
	if len(projectIDs) != 1 || projectIDs[0] != "proj-runtime" {
		t.Fatalf("runtime reconcile project ids = %v, want [proj-runtime]", projectIDs)
	}
}

func TestRunInvokesStartupRuntimeReconcileWithBoundedTimeout(t *testing.T) {
	const waitForStartupReconcile = 5 * time.Second

	recorder := &runtimeReconcileRecorder{
		started:       make(chan struct{}, 1),
		finished:      make(chan struct{}, 1),
		waitForCancel: true,
	}
	t.Chdir(t.TempDir())
	runCtx, cancelRun := context.WithCancel(context.Background())
	t.Cleanup(cancelRun)
	runDone := make(chan error, 1)
	serveDone := make(chan struct{}, 1)
	d := &Daemon{
		cfg: Config{
			Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
			RuntimeReconcileTimeout: 20 * time.Millisecond,
		},
		lock:  runtimeReconcileTestLock{},
		serve: &runtimeReconcileTestServer{served: serveDone},
	}
	d.syncBootstrapFn = func(context.Context) error { return nil }
	d.runtimeReconciler = recorder

	go func() {
		runDone <- d.Run(runCtx)
	}()

	select {
	case <-recorder.started:
	case <-time.After(waitForStartupReconcile):
		t.Fatal("timed out waiting for startup reconcile to begin")
	}
	select {
	case <-recorder.finished:
	case <-time.After(waitForStartupReconcile):
		t.Fatal("timed out waiting for bounded startup reconcile to finish")
	}
	cancelRun()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(waitForStartupReconcile):
		t.Fatal("timed out waiting for Run to finish")
	}

	select {
	case <-serveDone:
	case <-time.After(waitForStartupReconcile):
		t.Fatal("timed out waiting for serve to begin")
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("startup reconcile calls = %d, want 1", calls)
	}
	if len(projectIDs) != 1 || projectIDs[0] != protocol.DefaultProjectID {
		t.Fatalf("startup reconcile project ids = %v, want [%s]", projectIDs, protocol.DefaultProjectID)
	}
}

func TestStartupRuntimeReconcileBeginsSessionRecoveryBeforeInteractionStaleness(t *testing.T) {
	const waitForSessionRecovery = 5 * time.Second

	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("project id for root: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	interactionStarted := make(chan struct{})
	interactionBeforeSessionRecovery := make(chan struct{}, 1)
	releaseInteraction := make(chan struct{})
	var releaseInteractionOnce sync.Once
	release := func() { releaseInteractionOnce.Do(func() { close(releaseInteraction) }) }
	t.Cleanup(release)
	sessionRecoveryStarted := make(chan struct{})
	runCtx, cancelRun := context.WithCancel(context.Background())
	t.Cleanup(cancelRun)
	worktreeManager := git.NewWorktreeManager(emptyGitRunner{}, repoDir, slog.Default())

	d := &Daemon{
		cfg: Config{
			RepoDir:                 repoDir,
			Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
			RuntimeReconcileTimeout: 20 * time.Second,
		},
		issues:       issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default()),
		tmux:         tmux.NewClient(&signalingTmuxRunner{started: sessionRecoveryStarted}, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		lock:         runtimeReconcileTestLock{},
		serve:        &runtimeReconcileTestServer{served: make(chan struct{}, 1), waitForCancel: true, release: releaseInteraction},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: worktreeManager,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: worktreeManager,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}
	t.Cleanup(func() { _ = d.issues.CloseDB() })
	d.reconcileInteractionStalenessFn = func(ctx context.Context, _ string) error {
		select {
		case <-sessionRecoveryStarted:
		default:
			interactionBeforeSessionRecovery <- struct{}{}
		}
		close(interactionStarted)
		select {
		case <-releaseInteraction:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.syncBootstrapFn = func(context.Context) error { return nil }
	d.runtimeReconciler = newRuntimeReconcileService(d)

	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(runCtx)
	}()

	select {
	case <-sessionRecoveryStarted:
	case <-time.After(waitForSessionRecovery):
		t.Fatal("timed out waiting for startup session recovery to begin")
	}
	select {
	case <-interactionStarted:
	case <-time.After(waitForSessionRecovery):
		t.Fatal("timed out waiting for interaction reconciliation to begin")
	}
	select {
	case <-interactionBeforeSessionRecovery:
		t.Fatal("interaction reconciliation began before startup session recovery")
	default:
	}

	release()
	cancelRun()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(waitForSessionRecovery):
		t.Fatal("timed out waiting for Run to finish")
	}
}

func TestRunStartupRuntimeReconcileUsesRepoScopedProjectID(t *testing.T) {
	repoDir := t.TempDir()
	wantProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	recorder := &runtimeReconcileRecorder{
		result: protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(wantProjectID)},
	}
	d := &Daemon{
		cfg: Config{
			Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
			RepoDir:                 repoDir,
			RuntimeReconcileTimeout: 250 * time.Millisecond,
		},
		runtimeReconciler: recorder,
	}

	if _, err := d.runStartupRuntimeReconcile(context.Background()); err != nil {
		t.Fatalf("runStartupRuntimeReconcile: %v", err)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("startup reconcile calls = %d, want 1", calls)
	}
	if len(projectIDs) != 1 || projectIDs[0] != wantProjectID {
		t.Fatalf("startup reconcile project ids = %v, want [%s]", projectIDs, wantProjectID)
	}
}

func TestRunRuntimeReconcileSweepUsesBoundedQueueConcurrency(t *testing.T) {
	release := make(chan struct{})
	recorder := &scriptedRuntimeReconciler{
		started: make(chan string, 8),
		releaseByID: map[string]chan struct{}{
			"proj-a": release,
			"proj-b": release,
			"proj-c": release,
		},
	}
	queue := newReconcileQueue[protocol.RuntimeReconcileResponseBody](reconcileQueueConfig{
		Name:    "runtime_reconcile_test",
		Workers: 2,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() {
		_ = queue.Close()
	})

	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeReconciler:     recorder,
		runtimeReconcileQueue: queue,
		revision: map[string]uint64{
			"proj-a": 1,
			"proj-b": 1,
			"proj-c": 1,
		},
	}

	type sweepResult struct {
		results []protocol.RuntimeReconcileResponseBody
		err     error
	}
	done := make(chan sweepResult, 1)
	go func() {
		results, err := d.runRuntimeReconcileSweep(context.Background())
		done <- sweepResult{results: results, err: err}
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-recorder.started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for queued reconciles to start")
		}
	}
	time.Sleep(25 * time.Millisecond)

	_, _, maxConcurrent := recorder.snapshot()
	if maxConcurrent != 2 {
		t.Fatalf("max concurrent reconciles = %d, want 2", maxConcurrent)
	}

	close(release)

	out := <-done
	if out.err != nil {
		t.Fatalf("runRuntimeReconcileSweep error: %v", out.err)
	}
	if len(out.results) != 3 {
		t.Fatalf("reconcile results = %d, want 3", len(out.results))
	}
}

func TestRuntimeReconcileWorkerInvokesUntilCanceled(t *testing.T) {
	recorder := &runtimeReconcileRecorder{
		started: make(chan struct{}, 4),
	}
	d := &Daemon{
		cfg: Config{
			Logger:                   slog.New(slog.NewTextHandler(io.Discard, nil)),
			RuntimeReconcileInterval: 100 * time.Millisecond,
			RuntimeReconcileTimeout:  25 * time.Millisecond,
		},
		runtimeReconciler: recorder,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startRuntimeReconcileWorker(ctx)

	select {
	case <-recorder.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for periodic reconcile to run")
	}

	cancel()
	select {
	case <-recorder.started:
		t.Fatal("periodic reconcile ran after cancellation")
	case <-time.After(50 * time.Millisecond):
	}

	calls, _ := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("periodic reconcile calls = %d, want 1 after cancellation", calls)
	}
}

func TestCommandRuntimeReconcileReprioritizesPendingQueuedProject(t *testing.T) {
	releaseBusy := make(chan struct{})
	recorder := &scriptedRuntimeReconciler{
		started: make(chan string, 8),
		releaseByID: map[string]chan struct{}{
			"busy": releaseBusy,
		},
		resultsByID: map[string]protocol.RuntimeReconcileResponseBody{
			"proj-runtime": {
				ProjectID:             "proj-runtime",
				WorktreesRefreshed:    1,
				RecreatedTmuxSessions: 2,
				AlignedDaemonSessions: 3,
			},
		},
	}
	queue := newReconcileQueue[protocol.RuntimeReconcileResponseBody](reconcileQueueConfig{
		Name:    "runtime_reconcile_test",
		Workers: 1,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() {
		_ = queue.Close()
	})

	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeReconciler:     recorder,
		runtimeReconcileQueue: queue,
		revision: map[string]uint64{
			"busy":         1,
			"later":        1,
			"proj-runtime": 1,
		},
	}

	sweepDone := make(chan error, 1)
	go func() {
		_, err := d.runRuntimeReconcileSweep(context.Background())
		sweepDone <- err
	}()

	select {
	case started := <-recorder.started:
		if started != "busy" {
			t.Fatalf("first started project = %q, want busy", started)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for busy reconcile to start")
	}

	waitForPending := func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			snapshot := queue.snapshot()
			if len(snapshot.Pending) == 2 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("timed out waiting for pending reconcile jobs")
	}
	waitForPending()

	type commandResult struct {
		resp protocol.ResponseEnvelope
		err  error
	}
	commandDone := make(chan commandResult, 1)
	go func() {
		resp, err := d.command(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-runtime-reconcile-manual",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         protocol.CommandRuntimeReconcile,
			Meta:            protocol.Metadata{ProjectID: "proj-runtime"},
			Body:            mustJSONBody(t, protocol.RuntimeReconcileRequestBody{ProjectID: "proj-runtime"}),
		})
		commandDone <- commandResult{resp: resp, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := queue.snapshot()
		if len(snapshot.Pending) == 2 &&
			snapshot.Pending[0] == "proj-runtime" &&
			snapshot.Pending[1] == "later" &&
			snapshot.Counters.Deduped == 1 &&
			snapshot.Counters.Reprioritized == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := queue.snapshot()
	if len(snapshot.Pending) != 2 || snapshot.Pending[0] != "proj-runtime" || snapshot.Pending[1] != "later" {
		t.Fatalf("pending order after manual bump = %v, want [proj-runtime later]", snapshot.Pending)
	}
	if snapshot.Counters.Deduped != 1 || snapshot.Counters.Reprioritized != 1 {
		t.Fatalf("queue counters after manual bump = %+v", snapshot.Counters)
	}

	close(releaseBusy)

	commandOut := <-commandDone
	if commandOut.err != nil {
		t.Fatalf("command returned error: %v", commandOut.err)
	}
	if !commandOut.resp.OK {
		t.Fatalf("runtime.reconcile response not OK: %+v", commandOut.resp.Error)
	}
	var out protocol.RuntimeReconcileResponseBody
	if err := json.Unmarshal(commandOut.resp.Body, &out); err != nil {
		t.Fatalf("unmarshal command response: %v", err)
	}
	if out.ProjectID != "proj-runtime" || out.WorktreesRefreshed != 1 || out.RecreatedTmuxSessions != 2 || out.AlignedDaemonSessions != 3 {
		t.Fatalf("runtime.reconcile body = %+v", out)
	}
	if got := out.InvariantSources[string(daemonInvariantSessionReconcile)]; got != string(daemonInvariantSourceHybrid) {
		t.Fatalf("invariant_sources[%q] = %q, want %q", daemonInvariantSessionReconcile, got, daemonInvariantSourceHybrid)
	}

	if err := <-sweepDone; err != nil {
		t.Fatalf("background sweep error: %v", err)
	}

	order, callCounts, _ := recorder.snapshot()
	wantOrder := []string{"busy", "proj-runtime", "later"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("reconcile start order = %v, want %v", order, wantOrder)
	}
	for _, projectID := range []string{"busy", "later", "proj-runtime"} {
		if got := callCounts[projectID]; got != 1 {
			t.Fatalf("reconcile calls for %s = %d, want 1", projectID, got)
		}
	}
}

func TestCommandRuntimeReconcileRetriesQueuedWhenDedupedBackgroundTimesOut(t *testing.T) {
	recorder := &dedupedTimeoutRuntimeReconciler{
		started: make(chan struct{}, 1),
		result: protocol.RuntimeReconcileResponseBody{
			ProjectID:             "proj-runtime",
			WorktreesRefreshed:    4,
			RecreatedTmuxSessions: 1,
			AlignedDaemonSessions: 2,
		},
	}
	queue := newReconcileQueue[protocol.RuntimeReconcileResponseBody](reconcileQueueConfig{
		Name:    "runtime_reconcile_test",
		Workers: 1,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() {
		_ = queue.Close()
	})

	d := &Daemon{
		cfg:                   Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeReconciler:     recorder,
		runtimeReconcileQueue: queue,
	}

	backgroundCtx, backgroundCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer backgroundCancel()
	background, err := d.queueRuntimeReconcile(backgroundCtx, "proj-runtime", reconcilePriorityBackground, "periodic")
	if err != nil {
		t.Fatalf("queue background reconcile: %v", err)
	}
	select {
	case <-recorder.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background reconcile to start")
	}
	if snapshot := queue.snapshot(); len(snapshot.Running) != 1 || snapshot.Running[0] != "proj-runtime" {
		t.Fatalf("queue running = %v, want [proj-runtime]", snapshot.Running)
	}

	commandCtx, commandCancel := context.WithTimeout(context.Background(), time.Second)
	defer commandCancel()
	resp, err := d.command(commandCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-runtime-reconcile-manual",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandRuntimeReconcile,
		Meta:            protocol.Metadata{ProjectID: "proj-runtime"},
		Body:            mustJSONBody(t, protocol.RuntimeReconcileRequestBody{ProjectID: "proj-runtime"}),
	})
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("runtime.reconcile response not OK: %+v", resp.Error)
	}
	var out protocol.RuntimeReconcileResponseBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("unmarshal command response: %v", err)
	}
	if out.ProjectID != "proj-runtime" || out.WorktreesRefreshed != 4 || out.RecreatedTmuxSessions != 1 || out.AlignedDaemonSessions != 2 {
		t.Fatalf("runtime reconcile body = %+v", out)
	}
	if got := recorder.snapshot(); got != 2 {
		t.Fatalf("runtime reconcile calls = %d, want 2", got)
	}
	if counters := queue.snapshot().Counters; counters.Deduped != 1 {
		t.Fatalf("queue counters = %+v, want one deduped manual waiter", counters)
	} else if counters.Enqueued != 2 {
		t.Fatalf("queue counters = %+v, want background and retry jobs enqueued", counters)
	}
	outcome, waitErr := background.Wait(context.Background())
	if waitErr != nil {
		t.Fatalf("background wait: %v", waitErr)
	}
	if !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("background outcome error = %v, want context deadline exceeded", outcome.Err)
	}
}

func TestRunRuntimeReconcileSweepDefersProjectsWhenBudgetExhausted(t *testing.T) {
	now := time.Date(2026, time.April, 3, 14, 0, 0, 0, time.UTC)
	recorder := &runtimeReconcileRecorder{
		result: protocol.RuntimeReconcileResponseBody{
			ProjectID:             "proj-a",
			WorktreesRefreshed:    1,
			RecreatedTmuxSessions: 1,
			AlignedDaemonSessions: 1,
		},
	}
	d := &Daemon{
		cfg:               Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeReconciler: recorder,
		runtimeReconcileThrottle: newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "runtime_reconcile_budget_test",
			Budget:               1,
			Cadence:              time.Hour,
			UnchangedBackoffBase: time.Hour,
			UnchangedBackoffMax:  time.Hour,
			FailureBackoffBase:   time.Hour,
			FailureBackoffMax:    time.Hour,
			Now:                  func() time.Time { return now },
		}),
		revision: map[string]uint64{
			"proj-a": 1,
			"proj-b": 1,
			"proj-c": 1,
		},
	}

	results, metrics, err := d.runRuntimeReconcileSweepWithPriority(context.Background(), reconcilePriorityBackground, "periodic")
	if err != nil {
		t.Fatalf("runRuntimeReconcileSweepWithPriority error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if metrics.Processed != 1 || metrics.Deferred != 2 || metrics.Skipped != 0 || metrics.Failed != 0 {
		t.Fatalf("metrics = %+v, want processed=1 deferred=2 skipped=0 failed=0", metrics)
	}
	calls, _ := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", calls)
	}
}

func TestRunRuntimeReconcileSweepDefersBackgroundDuringHeavySessionStart(t *testing.T) {
	projectID := "proj-burst"
	recorder := &runtimeReconcileRecorder{
		result: protocol.RuntimeReconcileResponseBody{
			ProjectID:             naming.ProjectID(projectID),
			WorktreesRefreshed:    1,
			RecreatedTmuxSessions: 1,
			AlignedDaemonSessions: 1,
		},
	}
	d := &Daemon{
		cfg:               Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		operationRuntime:  startBlockingHeavySessionStart(t, projectID, "az-starting"),
		runtimeReconciler: recorder,
		revision: map[string]uint64{
			projectID: 1,
		},
	}
	t.Cleanup(func() {
		if d.runtimeReconcileQueue != nil {
			_ = d.runtimeReconcileQueue.Close()
		}
	})

	results, metrics, err := d.runRuntimeReconcileSweepWithPriority(context.Background(), reconcilePriorityBackground, "periodic")
	if err != nil {
		t.Fatalf("background sweep error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("background results len = %d, want 0", len(results))
	}
	if metrics.Processed != 0 || metrics.Deferred != 1 || metrics.Skipped != 0 || metrics.Failed != 0 {
		t.Fatalf("background metrics = %+v, want processed=0 deferred=1 skipped=0 failed=0", metrics)
	}
	calls, projectIDs := recorder.snapshot()
	if calls != 0 || len(projectIDs) != 0 {
		t.Fatalf("background reconcile calls = %d projectIDs=%v, want none", calls, projectIDs)
	}

	results, metrics, err = d.runRuntimeReconcileSweepWithPriority(context.Background(), reconcilePriorityManual, "manual")
	if err != nil {
		t.Fatalf("manual sweep error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("manual results len = %d, want 1", len(results))
	}
	if metrics.Processed != 1 || metrics.Deferred != 0 || metrics.Skipped != 0 || metrics.Failed != 0 {
		t.Fatalf("manual metrics = %+v, want processed=1 deferred=0 skipped=0 failed=0", metrics)
	}
	calls, projectIDs = recorder.snapshot()
	if calls != 1 || len(projectIDs) != 1 || projectIDs[0] != projectID {
		t.Fatalf("manual reconcile calls = %d projectIDs=%v, want [%s]", calls, projectIDs, projectID)
	}
}

func TestRunRuntimeReconcileSweepSkipsProjectDuringBackoffAfterUnchangedResult(t *testing.T) {
	now := time.Date(2026, time.April, 3, 15, 0, 0, 0, time.UTC)
	recorder := &runtimeReconcileRecorder{
		result: protocol.RuntimeReconcileResponseBody{
			ProjectID:             "proj-runtime",
			WorktreesRefreshed:    1,
			RecreatedTmuxSessions: 1,
			AlignedDaemonSessions: 1,
		},
	}
	d := &Daemon{
		cfg:               Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeReconciler: recorder,
		runtimeReconcileThrottle: newReconcileThrottle(reconcileThrottleConfig{
			Name:                 "runtime_reconcile_backoff_test",
			Budget:               4,
			Cadence:              time.Second,
			UnchangedBackoffBase: time.Hour,
			UnchangedBackoffMax:  time.Hour,
			FailureBackoffBase:   time.Hour,
			FailureBackoffMax:    time.Hour,
			Now:                  func() time.Time { return now },
		}),
		revision: map[string]uint64{
			"proj-runtime": 1,
		},
	}

	firstResults, firstMetrics, err := d.runRuntimeReconcileSweepWithPriority(context.Background(), reconcilePriorityBackground, "periodic")
	if err != nil {
		t.Fatalf("first sweep error: %v", err)
	}
	if len(firstResults) != 1 {
		t.Fatalf("first results len = %d, want 1", len(firstResults))
	}
	if firstMetrics.Processed != 1 || firstMetrics.Skipped != 0 || firstMetrics.Deferred != 0 || firstMetrics.Failed != 0 {
		t.Fatalf("first metrics = %+v, want processed=1 skipped=0 deferred=0 failed=0", firstMetrics)
	}

	secondResults, secondMetrics, err := d.runRuntimeReconcileSweepWithPriority(context.Background(), reconcilePriorityBackground, "periodic")
	if err != nil {
		t.Fatalf("second sweep error: %v", err)
	}
	if len(secondResults) != 0 {
		t.Fatalf("second results len = %d, want 0", len(secondResults))
	}
	if secondMetrics.Processed != 0 || secondMetrics.Skipped != 1 || secondMetrics.Deferred != 0 || secondMetrics.Failed != 0 {
		t.Fatalf("second metrics = %+v, want processed=0 skipped=1 deferred=0 failed=0", secondMetrics)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", calls)
	}
	if len(projectIDs) != 1 || projectIDs[0] != "proj-runtime" {
		t.Fatalf("reconcile project ids = %v, want [proj-runtime]", projectIDs)
	}
}

func TestRuntimeReconcileCycleUsesRepoScopedProjectID(t *testing.T) {
	repoDir := t.TempDir()
	wantProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	recorder := &runtimeReconcileRecorder{
		result: protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(wantProjectID)},
	}
	d := &Daemon{
		cfg: Config{
			Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
			RepoDir:                 repoDir,
			RuntimeReconcileTimeout: 250 * time.Millisecond,
		},
		runtimeReconciler: recorder,
	}

	d.runRuntimeReconcileCycle(context.Background())

	calls, projectIDs := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", calls)
	}
	if len(projectIDs) != 1 || projectIDs[0] != wantProjectID {
		t.Fatalf("reconcile project ids = %v, want [%s]", projectIDs, wantProjectID)
	}
}

func TestRuntimeReconcileTimeoutDefaultsByScopeMode(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git): %v", err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir}}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	if got, want := d.runtimeReconcileTimeout(), defaultRuntimeReconcileTimeout; got != want {
		t.Fatalf("runtimeReconcileTimeout() non-scoped = %s, want %s", got, want)
	}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	if got, want := d.runtimeReconcileTimeout(), defaultRuntimeReconcileTimeout; got != want {
		t.Fatalf("runtimeReconcileTimeout() forced scoped outside azedarach worktree = %s, want %s", got, want)
	}

	base := t.TempDir()
	baseRepo := filepath.Join(base, "base")
	worktree := filepath.Join(base, "wt")
	if err := os.MkdirAll(filepath.Join(baseRepo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(base repo worktrees): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(baseRepo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	d = &Daemon{cfg: Config{RepoDir: worktree}}
	if got, want := d.runtimeReconcileTimeout(), defaultRuntimeReconcileTimeout; got != want {
		t.Fatalf("runtimeReconcileTimeout() non-azedarach linked worktree = %s, want %s", got, want)
	}
	if err := os.WriteFile(filepath.Join(baseRepo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if got, want := d.runtimeReconcileTimeout(), defaultRuntimeReconcileTimeout; got != want {
		t.Fatalf("runtimeReconcileTimeout() azedarach linked worktree default = %s, want %s", got, want)
	}
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	if got, want := d.runtimeReconcileTimeout(), scopedRuntimeReconcileTimeout; got != want {
		t.Fatalf("runtimeReconcileTimeout() explicit azedarach linked worktree scope = %s, want %s", got, want)
	}
}

func TestRuntimeReconcileTimeoutHonorsExplicitScopedRuntime(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git): %v", err)
	}
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	d := New(Config{
		RepoDir:       repoDir,
		ScopedRuntime: true,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() {
		d.closeIssueClients()
		d.closeRuntimeStateStores()
		_ = d.runtimeReconcileQueue.Close()
		_ = d.gitStatusRefreshQueue.Close()
	})

	if got, want := d.runtimeReconcileTimeout(), scopedRuntimeReconcileTimeout; got != want {
		t.Fatalf("runtimeReconcileTimeout() explicit scoped = %s, want %s", got, want)
	}
}

func TestRuntimeReconcileKnownProjectIDsIncludesAllKnownSources(t *testing.T) {
	repoDir := t.TempDir()
	repoProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.UpsertSession("proj-session", "sess-1", "az-1", daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := runtimeStateStore.UpsertWorktreeState(context.Background(), daemonstate.WorktreeState{
		ProjectID: "proj-projection",
		IssueID:   "az-2",
		Path:      "/tmp/repo-az-2",
		Branch:    "riordan/az-2/task",
		UpdatedAt: time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		revision: map[string]uint64{"proj-revision": 3},
	}

	got, err := d.runtimeReconcileKnownProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("runtimeReconcileKnownProjectIDs: %v", err)
	}
	want := []string{repoProjectID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("project ids = %v, want %v", got, want)
	}
}

func TestRuntimeReconcileKnownProjectIDsScopedModePrioritizesRepoProject(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	repoDir := t.TempDir()
	repoProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.UpsertSession("proj-zeta", "sess-1", "az-1", daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := runtimeStateStore.UpsertWorktreeState(context.Background(), daemonstate.WorktreeState{
		ProjectID: "proj-alpha",
		IssueID:   "az-2",
		Path:      "/tmp/repo-az-2",
		Branch:    "riordan/az-2/task",
		UpdatedAt: time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		revision: map[string]uint64{"proj-beta": 1},
	}

	got, err := d.runtimeReconcileKnownProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("runtimeReconcileKnownProjectIDs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("project ids len = %d, want 1 (%v)", len(got), got)
	}
	if got[0] != repoProjectID {
		t.Fatalf("first project id = %q, want repo-scoped %q", got[0], repoProjectID)
	}
}

func TestRuntimeReconcileKnownProjectIDsCanonicalizesRepoAliases(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	base := t.TempDir()
	repoDir := filepath.Join(base, "azedarach")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(repoDir): %v", err)
	}
	repoProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}

	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.UpsertSession("azedarach", "sess-1", "az-1", daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession(azedarach): %v", err)
	}
	if _, err := sessionStore.UpsertSession("proj-zeta", "sess-2", "az-2", daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession(proj-zeta): %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		sessionStore: sessionStore,
		revision: map[string]uint64{
			repoProjectID: 1,
		},
	}

	got, err := d.runtimeReconcileKnownProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("runtimeReconcileKnownProjectIDs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("project ids len = %d, want 1 (%v)", len(got), got)
	}
	if got[0] != repoProjectID {
		t.Fatalf("first project id = %q, want repo-scoped id %q", got[0], repoProjectID)
	}
}

func TestRuntimeReconcileKnownProjectIDsDoesNotCreateRuntimeStoresWhenUnconfigured(t *testing.T) {
	repoDir := t.TempDir()
	wantProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{},
	}

	got, err := d.runtimeReconcileKnownProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("runtimeReconcileKnownProjectIDs: %v", err)
	}
	if len(got) != 1 || got[0] != wantProjectID {
		t.Fatalf("project ids = %v, want [%s]", got, wantProjectID)
	}
	if len(d.runtimeStoresByRoot) != 0 {
		t.Fatalf("runtimeStoresByRoot mutated: len=%d, want 0", len(d.runtimeStoresByRoot))
	}
	if len(d.runtimeStoresByProject) != 0 {
		t.Fatalf("runtimeStoresByProject mutated: len=%d, want 0", len(d.runtimeStoresByProject))
	}
}

func TestEnsureFreshRuntimeForMutationFallsBackToDirectReconcileAfterTimeout(t *testing.T) {
	recorder := &sequentialRuntimeReconciler{}
	d := &Daemon{
		cfg: Config{
			Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
			RuntimeReconcileTimeout: 20 * time.Millisecond,
		},
		runtimeReconciler: recorder,
	}
	t.Cleanup(func() {
		if d.runtimeReconcileQueue != nil {
			_ = d.runtimeReconcileQueue.Close()
		}
	})

	if err := d.ensureFreshRuntimeForMutation(context.Background(), "proj-fresh", "session.stop"); err != nil {
		t.Fatalf("ensureFreshRuntimeForMutation returned error: %v", err)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 2 {
		t.Fatalf("reconcile calls = %d, want 2 (queued then direct fallback)", calls)
	}
	if len(projectIDs) != 2 || projectIDs[0] != "proj-fresh" || projectIDs[1] != "proj-fresh" {
		t.Fatalf("reconcile project ids = %v, want [proj-fresh proj-fresh]", projectIDs)
	}
}

func TestEnsureFreshRuntimeForIssueMutationUsesIssueScopedReconcile(t *testing.T) {
	recorder := &runtimeReconcileRecorder{}
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		runtimeReconciler: recorder,
	}

	if err := d.ensureFreshRuntimeForIssueMutation(context.Background(), "proj-fresh", " az-1 ", "session.pause"); err != nil {
		t.Fatalf("ensureFreshRuntimeForIssueMutation returned error: %v", err)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", calls)
	}
	if len(projectIDs) != 1 || projectIDs[0] != "proj-fresh" {
		t.Fatalf("reconcile project ids = %v, want [proj-fresh]", projectIDs)
	}
	issueCalls := recorder.issueSnapshot()
	if len(issueCalls) != 1 || !reflect.DeepEqual(issueCalls[0], []string{"az-1"}) {
		t.Fatalf("reconcile issue ids = %v, want [[az-1]]", issueCalls)
	}
}

func TestRuntimeReconcileIssuesUsesBatchedSessionRefreshForLargeIssueSets(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-large-issue-reconcile"
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	issueIDs := make([]string, 0, runtimeReconcileIssueRepairLimit+1)
	for i := 0; i <= runtimeReconcileIssueRepairLimit; i++ {
		issueID := fmt.Sprintf("az-%d", i+1)
		issueIDs = append(issueIDs, issueID)
		if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
			ID:            naming.CanonicalSessionID(projectID, issueID),
			IssueID:       issueID,
			State:         daemonstate.SessionStateRunning,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     time.Now().UTC().Add(-time.Minute),
		}); err != nil {
			t.Fatalf("seed session projection %s: %v", issueID, err)
		}
	}

	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		panes:       map[string][]string{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if _, err := newRuntimeReconcileService(d).ReconcileIssues(ctx, projectID, issueIDs); err != nil {
		t.Fatalf("ReconcileIssues returned error: %v", err)
	}

	if got := tmuxRunner.listSessionCallCount(); got != 1 {
		t.Fatalf("tmux list-sessions calls = %d, want one batched liveness refresh", got)
	}
	row, found, err := runtimeStateStore.GetSessionState(ctx, projectID, naming.CanonicalSessionID(projectID, issueIDs[0]))
	if err != nil {
		t.Fatalf("get refreshed session projection: %v", err)
	}
	if !found {
		t.Fatal("refreshed session projection not found")
	}
	if row.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("observed state = %s, want %s", row.ObservedState, daemonstate.SessionStateStopped)
	}
}

func TestRefreshRuntimeForIssueMutationAsyncReturnsBeforeReconcileCompletes(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	recorder := &blockingIssueProjectionReconciler{
		started: started,
		release: release,
		issueID: "az-1",
	}
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		runtimeReconciler: recorder,
	}
	t.Cleanup(func() {
		close(release)
		if d.runtimeReconcileQueue != nil {
			_ = d.runtimeReconcileQueue.Close()
		}
	})

	returned := make(chan struct{})
	go func() {
		d.refreshRuntimeForIssueMutationAsync("proj-fresh", " az-1 ", "session.pause")
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("async issue reconcile enqueue blocked on reconcile completion")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async issue reconcile to start")
	}
}

func TestRefreshRuntimeForIssueMutationAsyncDefersDuringHeavySessionStart(t *testing.T) {
	projectID := "proj-burst"
	recorder := &runtimeReconcileRecorder{}
	d := &Daemon{
		cfg:               Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		operationRuntime:  startBlockingHeavySessionStart(t, projectID, "az-starting"),
		runtimeReconciler: recorder,
	}
	t.Cleanup(func() {
		if d.runtimeReconcileQueue != nil {
			_ = d.runtimeReconcileQueue.Close()
		}
	})

	d.refreshRuntimeForIssueMutationAsync(projectID, "az-paused", "session.pause")
	if d.runtimeReconcileQueue != nil {
		snapshot := d.runtimeReconcileQueue.snapshot()
		if len(snapshot.Pending) != 0 || len(snapshot.Running) != 0 {
			t.Fatalf("runtime reconcile queue = %+v, want no async issue job", snapshot)
		}
	}
	calls, projectIDs := recorder.snapshot()
	if calls != 0 || len(projectIDs) != 0 {
		t.Fatalf("async reconcile calls = %d projectIDs=%v, want none", calls, projectIDs)
	}
	if issueCalls := recorder.issueSnapshot(); len(issueCalls) != 0 {
		t.Fatalf("async issue reconcile calls = %v, want none", issueCalls)
	}
}

func TestRefreshRuntimeForIssueMutationAsyncEventuallyUpdatesProjection(t *testing.T) {
	projectID := "proj-fresh"
	issueID := "az-1"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	recorder := &blockingIssueProjectionReconciler{
		store:     store,
		started:   started,
		release:   release,
		issueID:   issueID,
		sessionID: sessionID,
	}
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		runtimeReconciler: recorder,
	}
	t.Cleanup(func() {
		if d.runtimeReconcileQueue != nil {
			_ = d.runtimeReconcileQueue.Close()
		}
	})

	d.refreshRuntimeForIssueMutationAsync(projectID, issueID, "session.resume")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async issue reconcile to start")
	}
	if _, found, err := store.GetSessionState(context.Background(), projectID, sessionID); err != nil {
		t.Fatalf("get session projection before release: %v", err)
	} else if found {
		t.Fatal("session projection updated before async reconcile was released")
	}

	close(release)
	deadline := time.After(time.Second)
	for {
		session, found, err := store.GetSessionState(context.Background(), projectID, sessionID)
		if err != nil {
			t.Fatalf("get session projection: %v", err)
		}
		if found && session.ObservedState == daemonstate.SessionStateAttached {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for async projection update; found=%v session=%+v", found, session)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSessionStatusDoesNotInvokeRuntimeReconcile(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuesDBPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(issuesDBPath, logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	recorder := &runtimeReconcileRecorder{
		started: make(chan struct{}, 1),
	}
	d := &Daemon{
		cfg: Config{
			Logger: logger,
		},
		issues:            issuesClient,
		tmux:              tmux.NewClient(emptyTmuxRunner{}, logger),
		runtimeReconciler: recorder,
	}
	d.syncBootstrapFn = func(context.Context) error { return nil }
	if err := d.bootstrapSyncOrchestrator(context.Background()); err != nil {
		t.Fatalf("bootstrap sync orchestrator: %v", err)
	}

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-session-status",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: "proj-status"},
		Body: mustJSONBody(t, map[string]string{
			"project_id": "proj-status",
		}),
	})
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session.status response not OK: %+v", resp.Error)
	}

	select {
	case <-recorder.started:
		t.Fatal("session.status should not invoke runtime reconciliation")
	default:
	}

	calls, _ := recorder.snapshot()
	if calls != 0 {
		t.Fatalf("runtime reconcile calls = %d, want 0", calls)
	}
}

func TestDefaultRuntimeReconcilePathIsNilSafe(t *testing.T) {
	var d Daemon
	result, err := d.ensureRuntimeReconciler().Reconcile(context.Background(), "")
	if err != nil {
		t.Fatalf("nil-safe default reconcile returned error: %v", err)
	}
	if result.ProjectID != protocol.DefaultProjectID {
		t.Fatalf("project id = %q, want %q", result.ProjectID, protocol.DefaultProjectID)
	}
	if result.WorktreesRefreshed != 0 || result.RecreatedTmuxSessions != 0 || result.AlignedDaemonSessions != 0 {
		t.Fatalf("default reconcile result = %+v, want zero counts", result)
	}
}

func TestReconcileIssueResourcesPresentRunsForActiveRuntimeAttachmentsOnly(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-resource-reconcile"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	activeID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Active resources",
		Type:   domain.TypeTask,
		Status: domain.StatusInReview,
	})
	if err != nil {
		t.Fatalf("create active issue: %v", err)
	}
	if _, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Open without runtime resources",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	}); err != nil {
		t.Fatalf("create open issue: %v", err)
	}
	activeWorktree := filepath.Join(repoDir, "wt-"+activeID)
	if err := os.MkdirAll(activeWorktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree %s: %v", activeWorktree, err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   activeID,
		Path:      activeWorktree,
		Branch:    "riordan/" + activeID + "/resources",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree state: %v", err)
	}

	marker := filepath.Join(repoDir, "resource-reconcile")
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				ReconcileCommand: fmt.Sprintf("printf '%%s|%%s|%%s\\n' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_RESOURCE_DESIRED_STATE\" \"$AZEDARACH_WORKTREE_PATH\" >> %q", marker),
			},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	if err := d.reconcileIssueResourcesPresent(ctx, projectID, nil); err != nil {
		t.Fatalf("reconcileIssueResourcesPresent error: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := activeID + "|present|" + activeWorktree
	if got != want {
		t.Fatalf("resource reconcile marker = %q, want %q", got, want)
	}
}

func TestRuntimeReconcileIssuesSkipsIssueResourceHookForSessionStartFreshness(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-resource-start-freshness"
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	activeID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Active resources",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create active issue: %v", err)
	}
	activeWorktree := filepath.Join(repoDir, "wt-"+activeID)
	if err := os.MkdirAll(activeWorktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree %s: %v", activeWorktree, err)
	}
	if err := runtimeStateStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{
		ProjectID: projectID,
		IssueID:   activeID,
		Path:      activeWorktree,
		Branch:    "riordan/" + activeID + "/resources",
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed worktree state: %v", err)
	}

	marker := filepath.Join(repoDir, "resource-reconcile")
	d := &Daemon{
		cfg: Config{
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				ReconcileCommand: fmt.Sprintf("printf 'ran' > %q", marker),
			},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	startCtx := context.WithValue(ctx, runtimeReconcileRequestContextKey{}, runtimeReconcileRequestContext{
		Priority: reconcilePriorityManual,
		Reason:   "mutation-issue:" + daemonhandlers.CommandSessionStart,
	})
	if _, err := newRuntimeReconcileService(d).ReconcileIssues(startCtx, projectID, []string{activeID}); err != nil {
		t.Fatalf("ReconcileIssues session.start freshness error: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session.start freshness marker stat error = %v, want not exist", err)
	}

	pauseCtx := context.WithValue(ctx, runtimeReconcileRequestContextKey{}, runtimeReconcileRequestContext{
		Priority: reconcilePriorityManual,
		Reason:   "mutation-issue:" + daemonhandlers.CommandSessionPause,
	})
	if _, err := newRuntimeReconcileService(d).ReconcileIssues(pauseCtx, projectID, []string{activeID}); err != nil {
		t.Fatalf("ReconcileIssues session.pause freshness error: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(data)) != "ran" {
		t.Fatalf("marker = %q, want ran", strings.TrimSpace(string(data)))
	}
}

func TestRuntimeReconcileRefreshesSessionProjectionWithoutWorktreeManager(t *testing.T) {
	const projectID = "proj-runtime"
	const issueID = "az-1"

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	sessionID := projectID + "-" + issueID
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		tmux:         tmux.NewClient(emptyTmuxRunner{}, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	result, err := d.ensureRuntimeReconciler().Reconcile(context.Background(), projectID)
	if err != nil {
		t.Fatalf("runtime reconcile returned error: %v", err)
	}
	if result.ProjectID != projectID {
		t.Fatalf("project id = %q, want %q", result.ProjectID, projectID)
	}

	rows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list projection sessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("projection rows = %d, want 1 after session runtime refresh", len(rows))
	}
	if rows[0].State != daemonstate.SessionStateAttached {
		t.Fatalf("desired session state = %s, want %s", rows[0].State, daemonstate.SessionStateAttached)
	}
	if rows[0].ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("observed session state = %s, want %s", rows[0].ObservedState, daemonstate.SessionStateStopped)
	}
}
