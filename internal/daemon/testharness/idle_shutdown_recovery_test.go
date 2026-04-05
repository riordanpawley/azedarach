package testharness

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	autoclient "github.com/riordanpawley/azedarach/internal/client"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonruntime "github.com/riordanpawley/azedarach/internal/daemon/runtime"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type idleRecoveryDaemon struct {
	harness     *Harness
	projectID   string
	idleTimeout time.Duration

	store *daemonstate.Store
	hub   *publish.Hub

	idleMu sync.Mutex
	idle   *daemonruntime.IdleSupervisor

	startCalls   atomic.Int32
	closeCalls   atomic.Int32
	handshakeOK  atomic.Int32
	requestCount atomic.Int32
}

func newIdleRecoveryDaemon(h *Harness, projectID string, idleTimeout time.Duration) *idleRecoveryDaemon {
	return &idleRecoveryDaemon{
		harness:     h,
		projectID:   projectID,
		idleTimeout: idleTimeout,
		store:       daemonstate.NewStore(),
		hub:         publish.NewHub(32, 8, slog.Default()),
	}
}

func (d *idleRecoveryDaemon) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	_ = ctx

	d.harness.mu.Lock()
	running := d.harness.running
	d.harness.mu.Unlock()

	if !running {
		return protocol.HelloAck{}, errors.New("daemon unavailable")
	}

	ack := protocol.NegotiateHello(hello, "daemon-test")
	d.handshakeOK.Add(1)

	if err := d.harness.appendEvent("daemon.handshake.compatible", map[string]any{
		"client_name":      hello.ClientName,
		"client_version":   hello.ClientVersion,
		"protocol_version": hello.ProtocolVersion,
		"daemon_version":   ack.DaemonVersion,
	}); err != nil {
		return protocol.HelloAck{}, err
	}

	return ack, nil
}

func (d *idleRecoveryDaemon) Start(ctx context.Context) error {
	_ = ctx

	d.startCalls.Add(1)
	if err := d.harness.Boot(); err != nil {
		return err
	}

	idle := daemonruntime.NewIdleSupervisor(d.idleTimeout, daemonruntime.ShutdownHooks{
		StopIntake: func() error {
			return d.harness.appendEvent("daemon.idle.stop_intake", map[string]any{
				"project_id": d.projectID,
				"starts":     d.startCalls.Load(),
			})
		},
		DrainInFlight: func(context.Context) error {
			return d.harness.appendEvent("daemon.idle.drain", map[string]any{
				"project_id": d.projectID,
				"starts":     d.startCalls.Load(),
			})
		},
		CloseTransport: func() error {
			d.closeCalls.Add(1)
			if err := d.harness.appendEvent("daemon.idle.close_transport", map[string]any{
				"project_id": d.projectID,
				"starts":     d.startCalls.Load(),
			}); err != nil {
				return err
			}
			return d.harness.Shutdown()
		},
	})

	d.idleMu.Lock()
	d.idle = idle
	d.idleMu.Unlock()

	idle.Start()
	return nil
}

func (d *idleRecoveryDaemon) currentIdle() *daemonruntime.IdleSupervisor {
	d.idleMu.Lock()
	defer d.idleMu.Unlock()
	return d.idle
}

func (d *idleRecoveryDaemon) attachAndSnapshot(ctx context.Context, orch *autoclient.AutostartOrchestrator, hello protocol.Hello) (protocol.HelloAck, daemonstate.Snapshot, error) {
	ack, err := orch.EnsureAttached(ctx, hello)
	if err != nil {
		return protocol.HelloAck{}, daemonstate.Snapshot{}, err
	}
	if !ack.Accepted {
		return protocol.HelloAck{}, daemonstate.Snapshot{}, errors.New("expected accepted handshake")
	}

	snap := d.store.ReadSnapshot(d.projectID)
	if err := d.harness.appendEvent("daemon.snapshot.attach.success", map[string]any{
		"client_name":      hello.ClientName,
		"protocol_version": ack.DaemonProtocolVersion,
		"daemon_version":   ack.DaemonVersion,
		"revision":         snap.Revision,
		"sessions":         len(snap.Sessions),
	}); err != nil {
		return protocol.HelloAck{}, daemonstate.Snapshot{}, err
	}

	return ack, snap, nil
}

func (d *idleRecoveryDaemon) runSessionMutation(ctx context.Context, sessionID string, sessionState daemonstate.SessionState, issueID string, started chan<- struct{}, release <-chan struct{}) error {
	idle := d.currentIdle()
	if idle == nil {
		return errors.New("idle supervisor unavailable")
	}

	if err := idle.BeginOperation(); err != nil {
		return err
	}
	defer idle.EndOperation()

	idle.RecordActivity()

	evt, err := d.store.UpsertSession(d.projectID, sessionID, issueID, sessionState)
	if err != nil {
		return err
	}
	d.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(d.projectID),
		Revision:        evt.Revision,
		Event:           evt.Type,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body: mustIdleJSON(map[string]any{
			"project_id": d.projectID,
			"revision":   evt.Revision,
			"session_id": sessionID,
			"issue_id":   issueID,
			"state":      string(sessionState),
		}),
	})

	d.requestCount.Add(1)
	if started != nil {
		close(started)
	}

	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type idleHarnessEvent struct {
	Event string `json:"event"`
}

func mustIdleJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func readIdleHarnessEventNames(t *testing.T, path string) []string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read harness log: %v", err)
	}

	var names []string
	for _, line := range idleSplitLines(string(b)) {
		if line == "" {
			continue
		}
		var evt idleHarnessEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("unmarshal event %q: %v", line, err)
		}
		names = append(names, evt.Event)
	}
	return names
}

func idleSplitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		out = append(out, s[start:])
	}
	return out
}

func idleCountEvent(names []string, event string) int {
	count := 0
	for _, name := range names {
		if name == event {
			count++
		}
	}
	return count
}

func waitForStatus(t *testing.T, idle *daemonruntime.IdleSupervisor, want string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if idle.Status() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("idle status = %s, want %s", idle.Status(), want)
}

func waitForRevision(t *testing.T, store *daemonstate.Store, projectID string, want uint64, timeout time.Duration) daemonstate.Snapshot {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap := store.ReadSnapshot(projectID)
		if snap.Revision == want {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap := store.ReadSnapshot(projectID)
	t.Fatalf("snapshot revision = %d, want %d", snap.Revision, want)
	return snap
}

func waitForEvent(t *testing.T, ch <-chan protocol.EventEnvelope, timeout time.Duration) protocol.EventEnvelope {
	t.Helper()

	select {
	case evt := <-ch:
		return evt
	case <-time.After(timeout):
		t.Fatal("timed out waiting for event")
		return protocol.EventEnvelope{}
	}
}

func TestIdleShutdownRecoveryScenarioAC(t *testing.T) {
	base := t.TempDir()
	h := New(Config{
		BaseDir:      base,
		ProjectID:    "proj-afo",
		OTELExporter: "http://127.0.0.1:4318",
	})

	daemon := newIdleRecoveryDaemon(h, "proj-afo", 25*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	firstOrch := autoclient.NewAutostartOrchestrator(daemon, daemon)
	hello := protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "client-1",
		ClientVersion:   "dev",
		Capabilities:    []string{"attach", "snapshot"},
	}

	ack, snap, err := daemon.attachAndSnapshot(ctx, firstOrch, hello)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if !ack.Accepted || ack.DaemonProtocolVersion != protocol.CurrentVersion {
		t.Fatalf("first ack = %+v", ack)
	}
	if snap.Revision != 0 {
		t.Fatalf("initial snapshot revision = %d, want 0", snap.Revision)
	}

	requestStarted := make(chan struct{})
	requestRelease := make(chan struct{})
	requestErr := make(chan error, 1)
	go func() {
		requestErr <- daemon.runSessionMutation(ctx, "sess-1", daemonstate.SessionStateStarting, "issue-1", requestStarted, requestRelease)
	}()

	select {
	case <-requestStarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for request to start")
	}

	firstRevision := waitForRevision(t, daemon.store, daemon.projectID, 1, 500*time.Millisecond)
	if firstRevision.Sessions["sess-1"].State != daemonstate.SessionStateStarting {
		t.Fatalf("snapshot session state = %s, want starting", firstRevision.Sessions["sess-1"].State)
	}

	firstIdle := daemon.currentIdle()
	if firstIdle == nil {
		t.Fatal("expected idle supervisor")
	}
	waitForStatus(t, firstIdle, "stopping", 500*time.Millisecond)
	if got := daemon.closeCalls.Load(); got != 0 {
		t.Fatalf("close transport before drain = %d, want 0", got)
	}

	close(requestRelease)
	if err := <-requestErr; err != nil {
		t.Fatalf("in-flight request: %v", err)
	}
	if err := firstIdle.WaitStopped(ctx); err != nil {
		t.Fatalf("first wait stopped: %v", err)
	}
	if err := firstIdle.BeginOperation(); err != daemonruntime.ErrShuttingDown {
		t.Fatalf("stopped idle supervisor BeginOperation err = %v, want %v", err, daemonruntime.ErrShuttingDown)
	}
	if got := daemon.closeCalls.Load(); got != 1 {
		t.Fatalf("close transport after drain = %d, want 1", got)
	}

	secondOrch := autoclient.NewAutostartOrchestrator(daemon, daemon)
	secondAck, secondSnap, err := daemon.attachAndSnapshot(ctx, secondOrch, protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "client-2",
		ClientVersion:   "dev",
		Capabilities:    []string{"attach", "snapshot"},
	})
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	if !secondAck.Accepted || secondAck.DaemonProtocolVersion != protocol.CurrentVersion {
		t.Fatalf("second ack = %+v", secondAck)
	}
	if secondSnap.Revision != 1 {
		t.Fatalf("restarted snapshot revision = %d, want 1", secondSnap.Revision)
	}
	if got := daemon.startCalls.Load(); got != 2 {
		t.Fatalf("start calls = %d, want 2", got)
	}

	backlogCh, cancelBacklog := daemon.hub.Subscribe(daemon.projectID, secondSnap.Revision)
	defer cancelBacklog()

	postRestartStarted := make(chan struct{})
	postRestartRelease := make(chan struct{})
	postRestartErr := make(chan error, 1)
	go func() {
		postRestartErr <- daemon.runSessionMutation(ctx, "sess-1", daemonstate.SessionStateAttached, "issue-1", postRestartStarted, postRestartRelease)
	}()

	select {
	case <-postRestartStarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for restarted request to start")
	}

	postRestartSnap := waitForRevision(t, daemon.store, daemon.projectID, 2, 500*time.Millisecond)
	if postRestartSnap.Sessions["sess-1"].State != daemonstate.SessionStateAttached {
		t.Fatalf("post-restart state = %s, want attached", postRestartSnap.Sessions["sess-1"].State)
	}

	evt := waitForEvent(t, backlogCh, 500*time.Millisecond)
	if evt.Revision != 2 {
		t.Fatalf("backlog event revision = %d, want 2", evt.Revision)
	}

	close(postRestartRelease)
	if err := <-postRestartErr; err != nil {
		t.Fatalf("post-restart request: %v", err)
	}

	secondIdle := daemon.currentIdle()
	if secondIdle == nil {
		t.Fatal("expected restarted idle supervisor")
	}
	if err := secondIdle.WaitStopped(ctx); err != nil {
		t.Fatalf("second wait stopped: %v", err)
	}

	if got := daemon.closeCalls.Load(); got != 2 {
		t.Fatalf("close calls = %d, want 2", got)
	}
	if got := daemon.handshakeOK.Load(); got != 2 {
		t.Fatalf("handshake compatibility count = %d, want 2", got)
	}

	events := readIdleHarnessEventNames(t, h.LogFilePath())
	if idleCountEvent(events, "daemon.harness.boot") != 2 {
		t.Fatalf("boot events = %d, want 2", idleCountEvent(events, "daemon.harness.boot"))
	}
	if idleCountEvent(events, "daemon.harness.shutdown") != 2 {
		t.Fatalf("shutdown events = %d, want 2", idleCountEvent(events, "daemon.harness.shutdown"))
	}
	if idleCountEvent(events, "daemon.handshake.compatible") != 2 {
		t.Fatalf("handshake events = %d, want 2", idleCountEvent(events, "daemon.handshake.compatible"))
	}
	if idleCountEvent(events, "daemon.snapshot.attach.success") != 2 {
		t.Fatalf("snapshot attach success events = %d, want 2", idleCountEvent(events, "daemon.snapshot.attach.success"))
	}
	if idleCountEvent(events, "daemon.idle.stop_intake") != 2 {
		t.Fatalf("stop intake events = %d, want 2", idleCountEvent(events, "daemon.idle.stop_intake"))
	}
	if idleCountEvent(events, "daemon.idle.drain") != 2 {
		t.Fatalf("drain events = %d, want 2", idleCountEvent(events, "daemon.idle.drain"))
	}
	if idleCountEvent(events, "daemon.idle.close_transport") != 2 {
		t.Fatalf("close transport events = %d, want 2", idleCountEvent(events, "daemon.idle.close_transport"))
	}
}
