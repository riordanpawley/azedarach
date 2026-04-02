package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
			Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
			RepoDir:                repoDir,
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
			Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
			RepoDir:                repoDir,
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
	projectionStore := daemonstate.NewProjectionStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() { _ = projectionStore.Close() })
	if err := projectionStore.UpsertWorktree(context.Background(), daemonstate.WorktreeProjection{
		ProjectID: "proj-projection",
		IssueID:   "az-2",
		Path:      "/tmp/repo-az-2",
		Branch:    "riordan/az-2/task",
		UpdatedAt: time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertWorktree: %v", err)
	}

	d := &Daemon{
		cfg:             Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		sessionStore:    sessionStore,
		projectionStore: projectionStore,
		revision:        map[string]uint64{"proj-revision": 3},
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
