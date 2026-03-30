package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"

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
}

func (s *bootstrapRecordingServer) Serve(context.Context) error {
	s.recorder.add("serve")
	return nil
}

type bootstrapRecordingLock struct{}

func (bootstrapRecordingLock) Acquire() (*lifecycle.Lease, error) {
	return &lifecycle.Lease{}, nil
}

func (bootstrapRecordingLock) Release() error {
	return nil
}

func TestRunInitializesSyncBootstrapBeforeServing(t *testing.T) {
	recorder := &bootstrapRecorder{}
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		lock:  bootstrapRecordingLock{},
		serve: &bootstrapRecordingServer{recorder: recorder},
	}
	d.syncBootstrapFn = func(context.Context) error {
		recorder.add("bootstrap")
		return nil
	}

	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := recorder.snapshot(), []string{"bootstrap", "serve"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("event order = %v, want %v", got, want)
	}
	if diag := d.syncBootstrapDiagnostic(); !diag.Ready || diag.State != "ready" {
		t.Fatalf("sync bootstrap diagnostic = %+v, want ready state", diag)
	}
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

	d.router = daemonhandlers.NewDispatcher(nil)
	nonSyncResp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-2",
		Kind:            protocol.EnvelopeKindCommand,
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
