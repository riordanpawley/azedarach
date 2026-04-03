package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type runtimeReconcileRecorder struct {
	mu            sync.Mutex
	calls         int
	projectIDs    []string
	started       chan struct{}
	waitForCancel bool
	result        protocol.RuntimeReconcileResponseBody
	err           error
}

func (r *runtimeReconcileRecorder) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	r.mu.Lock()
	r.calls++
	r.projectIDs = append(r.projectIDs, projectID)
	started := r.started
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

	return result, err
}

func (r *runtimeReconcileRecorder) snapshot() (calls int, projectIDs []string) {
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
	result := protocol.RuntimeReconcileResponseBody{ProjectID: projectID}
	if stored, ok := r.resultsByID[projectID]; ok {
		result = stored
		if result.ProjectID == "" {
			result.ProjectID = projectID
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
				return protocol.RuntimeReconcileResponseBody{ProjectID: projectID}, ctx.Err()
			}
		}
	}

	r.mu.Lock()
	r.current--
	r.mu.Unlock()
	return result, nil
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

type runtimeReconcileTestServer struct {
	served chan struct{}
}

func (s *runtimeReconcileTestServer) Serve(context.Context) error {
	if s.served != nil {
		select {
		case s.served <- struct{}{}:
		default:
		}
	}
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
	if len(args) > 0 && args[0] == "list-sessions" {
		return "", errors.New("no tmux sessions")
	}
	return "", nil
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

	calls, projectIDs := recorder.snapshot()
	if calls != 1 {
		t.Fatalf("runtime reconcile calls = %d, want 1", calls)
	}
	if len(projectIDs) != 1 || projectIDs[0] != "proj-runtime" {
		t.Fatalf("runtime reconcile project ids = %v, want [proj-runtime]", projectIDs)
	}
}

func TestRunInvokesStartupRuntimeReconcileWithBoundedTimeout(t *testing.T) {
	recorder := &runtimeReconcileRecorder{
		started:       make(chan struct{}, 1),
		waitForCancel: true,
	}
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
		runDone <- d.Run(context.Background())
	}()

	select {
	case <-recorder.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for startup reconcile to begin")
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Run to finish")
	}

	select {
	case <-serveDone:
	case <-time.After(time.Second):
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

func TestRunStartupRuntimeReconcileUsesRepoScopedProjectID(t *testing.T) {
	repoDir := t.TempDir()
	wantProjectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	recorder := &runtimeReconcileRecorder{
		result: protocol.RuntimeReconcileResponseBody{ProjectID: wantProjectID},
	}
	d := &Daemon{
		cfg: Config{
			Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
			RepoDir:                 repoDir,
			RuntimeReconcileTimeout: 25 * time.Millisecond,
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
			RuntimeReconcileInterval: 10 * time.Millisecond,
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
	time.Sleep(30 * time.Millisecond)

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
		result: protocol.RuntimeReconcileResponseBody{ProjectID: wantProjectID},
	}
	d := &Daemon{
		cfg: Config{
			Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
			RepoDir:                 repoDir,
			RuntimeReconcileTimeout: 25 * time.Millisecond,
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
	d := &Daemon{}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	if got, want := d.runtimeReconcileTimeout(), defaultRuntimeReconcileTimeout; got != want {
		t.Fatalf("runtimeReconcileTimeout() non-scoped = %s, want %s", got, want)
	}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	if got, want := d.runtimeReconcileTimeout(), scopedRuntimeReconcileTimeout; got != want {
		t.Fatalf("runtimeReconcileTimeout() scoped = %s, want %s", got, want)
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
		cfg:                  Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		sessionStore:         sessionStore,
		sessionRuntimeStore:  runtimeStateStore,
		worktreeRuntimeStore: runtimeStateStore,
		revision:             map[string]uint64{"proj-revision": 3},
	}

	got, err := d.runtimeReconcileKnownProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("runtimeReconcileKnownProjectIDs: %v", err)
	}
	want := []string{repoProjectID, "proj-projection", "proj-revision", "proj-session"}
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
		cfg:                  Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		sessionStore:         sessionStore,
		sessionRuntimeStore:  runtimeStateStore,
		worktreeRuntimeStore: runtimeStateStore,
		revision:             map[string]uint64{"proj-beta": 1},
	}

	got, err := d.runtimeReconcileKnownProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("runtimeReconcileKnownProjectIDs: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("project ids len = %d, want 4 (%v)", len(got), got)
	}
	if got[0] != repoProjectID {
		t.Fatalf("first project id = %q, want repo-scoped %q", got[0], repoProjectID)
	}
	wantSet := map[string]struct{}{
		repoProjectID: {},
		"proj-alpha":  {},
		"proj-beta":   {},
		"proj-zeta":   {},
	}
	for _, projectID := range got {
		if _, ok := wantSet[projectID]; !ok {
			t.Fatalf("unexpected project id %q in %v", projectID, got)
		}
		delete(wantSet, projectID)
	}
	if len(wantSet) != 0 {
		t.Fatalf("missing project ids: %v (got %v)", wantSet, got)
	}
}

func TestRuntimeReconcileKnownProjectIDsScopedModePrioritizesRepoNameProjectID(t *testing.T) {
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
	if len(got) < 2 {
		t.Fatalf("project ids len = %d, want >=2 (%v)", len(got), got)
	}
	if got[0] != "azedarach" {
		t.Fatalf("first project id = %q, want %q", got[0], "azedarach")
	}
	if got[1] != repoProjectID {
		t.Fatalf("second project id = %q, want repo-scoped id %q", got[1], repoProjectID)
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
