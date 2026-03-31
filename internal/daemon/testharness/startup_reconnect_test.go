package testharness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	autoclient "github.com/riordanpawley/azedarach/internal/client"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type scenarioDaemon struct {
	harness     *Harness
	startCalls  atomic.Int32
	handshakeOK atomic.Int32
	snapshotRev atomic.Uint64
	startErr    error
	startedCh   chan struct{}
	releaseCh   chan struct{}
	once        sync.Once
}

func newScenarioDaemon(h *Harness) *scenarioDaemon {
	return &scenarioDaemon{
		harness:   h,
		startedCh: make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
}

func (s *scenarioDaemon) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	_ = ctx

	s.harness.mu.Lock()
	running := s.harness.running
	s.harness.mu.Unlock()

	if !running {
		return protocol.HelloAck{}, errors.New("daemon unavailable")
	}

	ack := protocol.NegotiateHello(hello, "daemon-test")
	s.handshakeOK.Add(1)
	if err := s.harness.appendEvent("daemon.handshake.compatible", map[string]any{
		"client_name":      hello.ClientName,
		"client_version":   hello.ClientVersion,
		"protocol_version": hello.ProtocolVersion,
		"daemon_version":   ack.DaemonVersion,
	}); err != nil {
		return protocol.HelloAck{}, err
	}
	return ack, nil
}

func (s *scenarioDaemon) Start(ctx context.Context) error {
	_ = ctx

	s.startCalls.Add(1)
	if s.startErr != nil {
		_ = s.harness.appendEvent("daemon.start.failed", map[string]any{
			"error": s.startErr.Error(),
		})
		return s.startErr
	}
	if err := s.harness.Boot(); err != nil {
		return err
	}

	s.once.Do(func() {
		close(s.startedCh)
	})

	<-s.releaseCh
	return nil
}

func (s *scenarioDaemon) attachClient(ctx context.Context, orch *autoclient.AutostartOrchestrator, hello protocol.Hello) (protocol.HelloAck, error) {
	ack, err := orch.EnsureAttached(ctx, hello)
	if err != nil {
		return protocol.HelloAck{}, err
	}
	if !ack.Accepted {
		return protocol.HelloAck{}, errors.New("expected accepted handshake")
	}
	if err := s.harness.appendEvent("daemon.snapshot.attach.success", map[string]any{
		"client_name":      hello.ClientName,
		"protocol_version": ack.DaemonProtocolVersion,
		"daemon_version":   ack.DaemonVersion,
		"revision":         s.snapshotRev.Load(),
	}); err != nil {
		return protocol.HelloAck{}, err
	}
	return ack, nil
}

type harnessEvent struct {
	Event    string `json:"event"`
	Revision uint64 `json:"revision"`
}

func readHarnessEvents(t *testing.T, path string) []harnessEvent {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read harness log: %v", err)
	}

	var events []harnessEvent
	for _, line := range splitLines(string(b)) {
		if line == "" {
			continue
		}
		var evt harnessEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("unmarshal event %q: %v", line, err)
		}
		events = append(events, evt)
	}
	return events
}

func splitLines(s string) []string {
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

func countEvent(events []harnessEvent, name string) int {
	count := 0
	for _, evt := range events {
		if evt.Event == name {
			count++
		}
	}
	return count
}

func TestStartupReconnectScenarioAC(t *testing.T) {
	base := t.TempDir()
	h := New(Config{
		BaseDir:      base,
		ProjectID:    "proj-afn",
		OTELExporter: "http://127.0.0.1:4318",
	})

	scenario := newScenarioDaemon(h)
	orch := autoclient.NewAutostartOrchestrator(scenario, scenario)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	hello1 := protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "client-1",
		ClientVersion:   "dev",
		Capabilities:    []string{"attach", "snapshot"},
	}
	hello2 := protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "client-2",
		ClientVersion:   "dev",
		Capabilities:    []string{"attach", "snapshot"},
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	ackCh := make(chan protocol.HelloAck, 2)

	attach := func(hello protocol.Hello) {
		defer wg.Done()
		ack, err := scenario.attachClient(ctx, orch, hello)
		if err != nil {
			errCh <- err
			return
		}
		ackCh <- ack
	}

	wg.Add(1)
	go attach(hello1)

	<-scenario.startedCh

	wg.Add(1)
	go attach(hello2)

	if got := scenario.startCalls.Load(); got != 1 {
		t.Fatalf("starter calls before release = %d, want 1", got)
	}

	close(scenario.releaseCh)
	wg.Wait()
	close(errCh)
	close(ackCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("initial attach error: %v", err)
		}
	}
	if len(ackCh) != 2 {
		t.Fatalf("initial attach count = %d, want 2", len(ackCh))
	}
	for ack := range ackCh {
		if !ack.Accepted || ack.DaemonProtocolVersion != protocol.CurrentVersion {
			t.Fatalf("unexpected ack: %+v", ack)
		}
	}

	if got := scenario.startCalls.Load(); got != 1 {
		t.Fatalf("starter calls after initial attach = %d, want 1", got)
	}

	scenario.snapshotRev.Store(1)

	if err := h.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	reconnectOrch := autoclient.NewAutostartOrchestrator(scenario, scenario)

	ack, err := scenario.attachClient(ctx, reconnectOrch, protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "client-reconnect",
		ClientVersion:   "dev",
		Capabilities:    []string{"attach", "snapshot"},
	})
	if err != nil {
		t.Fatalf("reconnect attach: %v", err)
	}
	if !ack.Accepted || ack.DaemonProtocolVersion != protocol.CurrentVersion {
		t.Fatalf("reconnect ack = %+v", ack)
	}

	if got := scenario.startCalls.Load(); got != 2 {
		t.Fatalf("starter calls after reconnect = %d, want 2", got)
	}

	if err := h.Shutdown(); err != nil {
		t.Fatalf("final Shutdown: %v", err)
	}

	events := readHarnessEvents(t, h.LogFilePath())
	if countEvent(events, "daemon.harness.boot") != 2 {
		t.Fatalf("boot events = %d, want 2", countEvent(events, "daemon.harness.boot"))
	}
	if countEvent(events, "daemon.harness.shutdown") != 2 {
		t.Fatalf("shutdown events = %d, want 2", countEvent(events, "daemon.harness.shutdown"))
	}
	if countEvent(events, "daemon.handshake.compatible") < 3 {
		t.Fatalf("compatible handshake events = %d, want at least 3", countEvent(events, "daemon.handshake.compatible"))
	}
	if countEvent(events, "daemon.snapshot.attach.success") < 3 {
		t.Fatalf("snapshot attach success events = %d, want at least 3", countEvent(events, "daemon.snapshot.attach.success"))
	}

	var attachRevisions []uint64
	for _, evt := range events {
		if evt.Event == "daemon.snapshot.attach.success" {
			attachRevisions = append(attachRevisions, evt.Revision)
		}
	}
	if len(attachRevisions) != 3 {
		t.Fatalf("snapshot attach revisions = %v, want 3 entries", attachRevisions)
	}
	if attachRevisions[0] != 0 || attachRevisions[1] != 0 || attachRevisions[2] != 1 {
		t.Fatalf("snapshot attach revisions = %v, want [0 0 1]", attachRevisions)
	}
}

func TestStartupBootstrapFailureDiagnosticsMatrix(t *testing.T) {
	base := t.TempDir()
	h := New(Config{
		BaseDir:      base,
		ProjectID:    "proj-bootstrap-failure",
		OTELExporter: "http://127.0.0.1:4318",
	})

	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if err := h.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	scenario := &scenarioDaemon{
		harness:   h,
		startErr:  errors.New("bootstrap rejected"),
		startedCh: make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
	orch := autoclient.NewAutostartOrchestrator(scenario, scenario)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := orch.EnsureAttached(ctx, protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      "client-bootstrap-failure",
		ClientVersion:   "dev",
		Capabilities:    []string{"attach", "snapshot"},
	})
	if err == nil {
		t.Fatal("EnsureAttached() error = nil, want bootstrap failure")
	}
	if got := err.Error(); !strings.Contains(got, "autostart daemon: bootstrap rejected") {
		t.Fatalf("EnsureAttached() error = %q, want autostart failure context", got)
	}

	if got := scenario.startCalls.Load(); got != 1 {
		t.Fatalf("start calls = %d, want 1", got)
	}

	events := readHarnessEvents(t, h.LogFilePath())
	if countEvent(events, "daemon.start.failed") != 1 {
		t.Fatalf("start failed events = %d, want 1", countEvent(events, "daemon.start.failed"))
	}
	if countEvent(events, "daemon.harness.boot") != 1 {
		t.Fatalf("boot events = %d, want 1", countEvent(events, "daemon.harness.boot"))
	}
	if countEvent(events, "daemon.harness.shutdown") != 1 {
		t.Fatalf("shutdown events = %d, want 1", countEvent(events, "daemon.harness.shutdown"))
	}
}
