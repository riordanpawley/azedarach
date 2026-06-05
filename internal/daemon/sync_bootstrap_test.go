package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
)

type bootstrapRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *bootstrapRecorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *bootstrapRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type bootstrapRecordingServer struct {
	recorder *bootstrapRecorder
	started  chan struct{}
}

func (s *bootstrapRecordingServer) Serve(ctx context.Context) error {
	s.recorder.add("serve")
	if s.started != nil {
		close(s.started)
	}
	<-ctx.Done()
	return nil
}

type bootstrapRecordingLock struct{}

func (bootstrapRecordingLock) Acquire() (*lifecycle.Lease, error) {
	return &lifecycle.Lease{}, nil
}

func (bootstrapRecordingLock) Release() error {
	return nil
}

func TestRunStartsServingBeforeSyncBootstrapCompletes(t *testing.T) {
	recorder := &bootstrapRecorder{}
	serveStarted := make(chan struct{})
	bootstrapStarted := make(chan struct{})
	releaseBootstrap := make(chan struct{})
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		lock:  bootstrapRecordingLock{},
		serve: &bootstrapRecordingServer{recorder: recorder, started: serveStarted},
	}
	d.syncBootstrapFn = func(context.Context) error {
		recorder.add("bootstrap-start")
		close(bootstrapStarted)
		<-releaseBootstrap
		recorder.add("bootstrap-finish")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run(ctx)
	}()

	select {
	case <-serveStarted:
	case <-time.After(time.Second):
		t.Fatal("daemon server did not start before bootstrap completed")
	}
	select {
	case <-bootstrapStarted:
	case <-time.After(time.Second):
		t.Fatal("sync bootstrap did not start")
	}

	close(releaseBootstrap)
	deadline := time.Now().Add(time.Second)
	for {
		events := recorder.snapshot()
		if len(events) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("event order = %v, want serve before completed bootstrap", events)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop after context cancellation")
	}

	if got := recorder.snapshot(); len(got) < 3 || indexOfBootstrapEvent(got, "serve") > indexOfBootstrapEvent(got, "bootstrap-finish") {
		t.Fatalf("event order = %v, want serve before completed bootstrap", got)
	}
	if diag := d.syncBootstrapDiagnostic(); !diag.Ready || diag.State != "ready" {
		t.Fatalf("sync bootstrap diagnostic = %+v, want ready state", diag)
	}
}

func indexOfBootstrapEvent(events []string, want string) int {
	for i, event := range events {
		if event == want {
			return i
		}
	}
	return len(events)
}

func TestSyncBootstrapGuardBlocksDependentCommands(t *testing.T) {
	d := &Daemon{}

	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-1",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "task.list",
	})
	if err != nil {
		t.Fatalf("command() error = %v", err)
	}
	if resp.OK {
		t.Fatal("expected sync-dependent command to be rejected before bootstrap")
	}
	if resp.Error == nil {
		t.Fatal("expected error envelope")
	}
	if got, want := resp.Error.Code, protocol.ErrorCodeUnavailable; got != want {
		t.Fatalf("error code = %s, want %s", got, want)
	}
	if got, want := resp.Error.Message, "sync bootstrap not ready"; got != want {
		t.Fatalf("error message = %q, want %q", got, want)
	}

	body, err := json.Marshal(protocol.OperationSubmitRequestBody{
		ProjectID: "proj-a",
		Kind:      " session.start ",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	operationResp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-1b",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandOperationSubmit,
		Body:            body,
	})
	if err != nil {
		t.Fatalf("operation command() error = %v", err)
	}
	if operationResp.OK {
		t.Fatal("expected sync-dependent operation submit to be rejected before bootstrap")
	}
	if operationResp.Error == nil {
		t.Fatal("expected operation error envelope")
	}
	if got, want := operationResp.Error.Code, protocol.ErrorCodeUnavailable; got != want {
		t.Fatalf("operation error code = %s, want %s", got, want)
	}
	if got, want := operationResp.Error.Message, "sync bootstrap not ready"; got != want {
		t.Fatalf("operation error message = %q, want %q", got, want)
	}

	d.router = daemonhandlers.NewDispatcher(nil)
	nonSyncResp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-2",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "proj-a"},
		Command:         "git.status",
	})
	if err != nil {
		t.Fatalf("non-sync command() error = %v", err)
	}
	if nonSyncResp.Error == nil {
		t.Fatal("expected unsupported command response for unimplemented non-sync path")
	}
	if got, want := nonSyncResp.Error.Code, protocol.ErrorCodeUnsupportedCommand; got != want {
		t.Fatalf("non-sync error code = %s, want %s", got, want)
	}
}

func TestSyncBootstrapFailureDiagnosticContract(t *testing.T) {
	d := &Daemon{}
	wantErr := errors.New("open issue store: boom")
	d.syncBootstrapFn = func(context.Context) error {
		return wantErr
	}

	err := d.bootstrapSyncOrchestrator(context.Background())
	if err == nil {
		t.Fatal("expected bootstrap error")
	}
	if got, want := err.Error(), "sync bootstrap: open issue store: boom"; got != want {
		t.Fatalf("bootstrap error = %q, want %q", got, want)
	}

	diag := d.syncBootstrapDiagnostic()
	if diag.Ready {
		t.Fatal("expected failed diagnostic to report not ready")
	}
	if got, want := diag.State, "failed"; got != want {
		t.Fatalf("diagnostic state = %q, want %q", got, want)
	}
	if got, want := diag.Reason, wantErr.Error(); got != want {
		t.Fatalf("diagnostic reason = %q, want %q", got, want)
	}

	resp, handled := d.guardSyncDependentCommand(protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-3",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
	})
	if !handled {
		t.Fatal("expected sync-dependent command to be guarded after bootstrap failure")
	}
	if resp.Error == nil {
		t.Fatal("expected guarded response to include error envelope")
	}
	if got, want := resp.Error.Code, protocol.ErrorCodeUnavailable; got != want {
		t.Fatalf("guarded error code = %s, want %s", got, want)
	}
	if got, want := resp.Error.Message, "sync bootstrap failed: open issue store: boom"; got != want {
		t.Fatalf("guarded error message = %q, want %q", got, want)
	}
}

func TestDefaultSyncBootstrapSkipsRegisteredProjectStoresAtStartup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	baseRepo := newBootstrapTestRepo(t, "azedarach")
	chefyRepo := newBootstrapTestRepo(t, "Chefy")
	registry := &appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{
			{Name: "Chefy", Path: chefyRepo},
		},
		DefaultProject: "azedarach",
	}
	if err := appconfig.SaveProjectsRegistry(registry); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: baseRepo,
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
	t.Cleanup(d.closeIssueClients)

	if err := d.defaultSyncBootstrap(context.Background()); err != nil {
		t.Fatalf("default sync bootstrap: %v", err)
	}

	assertBootstrapDBExists(t, baseRepo)
	if got := d.resolveRepoDirForProject("Chefy"); got != chefyRepo {
		t.Fatalf("Chefy project repo = %q, want %q", got, chefyRepo)
	}
	if _, err := os.Stat(filepath.Join(chefyRepo, ".azedarach", "azedarach.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registered project db stat error = %v, want not exist", err)
	}
}

func TestDefaultSyncBootstrapScopedRuntimeSkipsRegisteredProjectStores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	baseRepo := newBootstrapTestRepo(t, "azedarach")
	chefyRepo := newBootstrapTestRepo(t, "Chefy")
	registry := &appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{
			{Name: "Chefy", Path: chefyRepo},
		},
		DefaultProject: "azedarach",
	}
	if err := appconfig.SaveProjectsRegistry(registry); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir:       baseRepo,
			ScopedRuntime: true,
			Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
	t.Cleanup(d.closeIssueClients)

	if err := d.defaultSyncBootstrap(context.Background()); err != nil {
		t.Fatalf("default sync bootstrap: %v", err)
	}

	assertBootstrapDBExists(t, baseRepo)
	if _, err := os.Stat(filepath.Join(chefyRepo, ".azedarach", "azedarach.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registered project db stat error = %v, want not exist", err)
	}
}

func TestDefaultSyncBootstrapIgnoresBrokenRegisteredProjectStoresAtStartup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	baseRepo := newBootstrapTestRepo(t, "azedarach")
	brokenProjectPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(brokenProjectPath, []byte("file, not repo dir"), 0o644); err != nil {
		t.Fatalf("write broken project path: %v", err)
	}
	registry := &appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{
			{Name: "Broken", Path: brokenProjectPath},
		},
		DefaultProject: "azedarach",
	}
	if err := appconfig.SaveProjectsRegistry(registry); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir: baseRepo,
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
	t.Cleanup(d.closeIssueClients)

	if err := d.defaultSyncBootstrap(context.Background()); err != nil {
		t.Fatalf("default sync bootstrap: %v", err)
	}

	assertBootstrapDBExists(t, baseRepo)
}

func newBootstrapTestRepo(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return root
}

func assertBootstrapDBExists(t *testing.T, repoDir string) {
	t.Helper()
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected bootstrap db at %s: %v", dbPath, err)
	}
}
