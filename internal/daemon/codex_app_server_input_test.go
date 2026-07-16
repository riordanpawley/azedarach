package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type fakeCodexInputTmux struct {
	mu               sync.Mutex
	clients          []tmux.AttachedClientInfo
	panes            []tmux.PaneInfo
	paneEnabled      bool
	hooksEnabled     bool
	recordPath       string
	setReadOnlyCalls []string
	maxGates         int
	activeGates      int
}

func (f *fakeCodexInputTmux) ListAttachedClients(context.Context, string) ([]tmux.AttachedClientInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tmux.AttachedClientInfo(nil), f.clients...), nil
}

func (f *fakeCodexInputTmux) SetClientReadOnly(_ context.Context, name string, readOnly bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.clients {
		if f.clients[i].ClientName == name {
			f.clients[i].ReadOnly = readOnly
		}
	}
	f.setReadOnlyCalls = append(f.setReadOnlyCalls, name+"="+map[bool]string{true: "read-only", false: "writable"}[readOnly])
	return nil
}

func (f *fakeCodexInputTmux) SetPaneInputEnabled(_ context.Context, _ string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paneEnabled = enabled
	return nil
}

func (f *fakeCodexInputTmux) PaneInputEnabled(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paneEnabled, nil
}

func (f *fakeCodexInputTmux) SetSessionReadOnlyAttachHooks(_ context.Context, _, _, recordPath string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooksEnabled = enabled
	f.recordPath = recordPath
	if enabled {
		f.activeGates++
		if f.activeGates > f.maxGates {
			f.maxGates = f.activeGates
		}
	} else if f.activeGates > 0 {
		f.activeGates--
	}
	return nil
}

func (f *fakeCodexInputTmux) ListPaneInfos(context.Context) ([]tmux.PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tmux.PaneInfo(nil), f.panes...), nil
}

type fakeCodexRPC struct {
	mu           sync.Mutex
	methods      []string
	turnParams   map[string]any
	resumeThread string
	turnErr      error
	turnStarted  chan struct{}
	releaseTurn  chan struct{}
	turnCheck    func() error
}

func (f *fakeCodexRPC) Call(_ context.Context, method string, params, result any) error {
	f.mu.Lock()
	f.methods = append(f.methods, method)
	f.mu.Unlock()
	switch method {
	case "initialize":
		return nil
	case "thread/resume":
		raw, _ := json.Marshal(params)
		var decoded struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(raw, &decoded)
		f.resumeThread = decoded.ThreadID
		raw, _ = json.Marshal(map[string]any{"thread": map[string]any{"id": decoded.ThreadID}})
		return json.Unmarshal(raw, result)
	case "turn/start":
		if f.turnCheck != nil {
			if err := f.turnCheck(); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(params)
		_ = json.Unmarshal(raw, &f.turnParams)
		if f.turnStarted != nil {
			close(f.turnStarted)
		}
		if f.releaseTurn != nil {
			<-f.releaseTurn
		}
		if f.turnErr != nil {
			return f.turnErr
		}
		raw, _ = json.Marshal(map[string]any{"turn": map[string]any{"id": "turn-accepted"}})
		return json.Unmarshal(raw, result)
	default:
		return errors.New("unexpected RPC method: " + method)
	}
}

func (f *fakeCodexRPC) Notify(method string, _ any) error {
	if method != "initialized" {
		return errors.New("unexpected notification: " + method)
	}
	f.mu.Lock()
	f.methods = append(f.methods, method)
	f.mu.Unlock()
	return nil
}

func (*fakeCodexRPC) Close() error { return nil }

func codexAuthorityRequest() authoritativeAgentInputRequest {
	return authoritativeAgentInputRequest{Delivery: domain.AgentInputDeliveryRequest{
		ProjectID: "p", SessionID: "az-dlb", Tool: "codex", IntentKey: "intent", Payload: "automated body",
		Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 42, AgentIncarnation: "thread-exact"},
	}, LeaseToken: "lease", BeginSubmission: func(context.Context) error { return nil }}
}

func newFakeCodexAuthority(t *testing.T, adapter *fakeCodexInputTmux, rpc *fakeCodexRPC) *codexAppServerInputAuthority {
	t.Helper()
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) string { return "codex" })
	authority.startRPC = func(context.Context, string) (codexAppServerRPC, error) { return rpc, nil }
	return authority
}

func TestCodexAppServerDeliveryGatesClientsSubmitsExactThreadAndRestores(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{
		{ClientName: "/dev/tty1", SessionName: "az-dlb"},
		{ClientName: "/dev/tty2", SessionName: "az-dlb", ReadOnly: true},
	}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "%12", PanePID: 42}}}
	boundaryBegun := false
	rpc := &fakeCodexRPC{turnCheck: func() error {
		if !boundaryBegun {
			return errors.New("turn/start preceded durable boundary")
		}
		return nil
	}}
	request := codexAuthorityRequest()
	request.BeginSubmission = func(context.Context) error {
		boundaryBegun = true
		return nil
	}
	ack, err := newFakeCodexAuthority(t, adapter, rpc).DeliverAgentInput(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if ack.AcknowledgementToken != "turn-accepted" || rpc.resumeThread != "thread-exact" {
		t.Fatalf("ack=%+v resumed=%q", ack, rpc.resumeThread)
	}
	if rpc.turnParams["threadId"] != "thread-exact" || rpc.turnParams["clientUserMessageId"] != codexDeliveryMessageID("intent", "thread-exact") {
		t.Fatalf("turn params = %#v", rpc.turnParams)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if !adapter.paneEnabled || adapter.hooksEnabled || adapter.clients[0].ReadOnly || !adapter.clients[1].ReadOnly {
		t.Fatalf("gate was not restored exactly: pane=%v hooks=%v clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.clients)
	}
}

func TestCodexAppServerDeliveryRestoresGateWhenTurnRejected(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	rpc := &fakeCodexRPC{turnErr: errors.New("turn active")}
	if _, err := newFakeCodexAuthority(t, adapter, rpc).DeliverAgentInput(context.Background(), codexAuthorityRequest()); err == nil {
		t.Fatal("expected turn rejection")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if !adapter.paneEnabled || adapter.hooksEnabled || adapter.clients[0].ReadOnly {
		t.Fatalf("failed delivery left gate active: pane=%v hooks=%v clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.clients)
	}
}

func TestCodexAppServerDeliveryFailsClosedForLivePaneMismatch(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 99}}}
	rpc := &fakeCodexRPC{}
	request := codexAuthorityRequest()
	boundaryBegun := false
	request.BeginSubmission = func(context.Context) error { boundaryBegun = true; return nil }
	if _, err := newFakeCodexAuthority(t, adapter, rpc).DeliverAgentInput(context.Background(), request); err == nil {
		t.Fatal("expected stale pane refusal")
	}
	if boundaryBegun {
		t.Fatal("stale pane crossed durable submission boundary")
	}
	if rpc.turnParams != nil {
		t.Fatalf("turn submitted for stale pane: %#v", rpc.turnParams)
	}
}

func TestCodexAppServerDeliveryRevalidatesClientsAfterDurableBoundary(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	rpc := &fakeCodexRPC{}
	request := codexAuthorityRequest()
	request.BeginSubmission = func(context.Context) error {
		adapter.mu.Lock()
		adapter.clients[0].ReadOnly = false
		adapter.mu.Unlock()
		return nil
	}
	_, err := newFakeCodexAuthority(t, adapter, rpc).DeliverAgentInput(context.Background(), request)
	var refusal agentInputRefusalError
	if !errors.As(err, &refusal) || refusal.outcome != "human_attached" || !refusal.safeToRetry {
		t.Fatalf("err=%v refusal=%+v", err, refusal)
	}
	if rpc.turnParams != nil {
		t.Fatalf("turn submitted after client became writable: %#v", rpc.turnParams)
	}
}

func TestCodexAppServerDeliverySerializesOneSessionGate(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	firstRPC := &fakeCodexRPC{turnStarted: make(chan struct{}), releaseTurn: make(chan struct{})}
	secondRPC := &fakeCodexRPC{}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) string { return "codex" })
	var starts int
	var startsMu sync.Mutex
	authority.startRPC = func(context.Context, string) (codexAppServerRPC, error) {
		startsMu.Lock()
		defer startsMu.Unlock()
		starts++
		if starts == 1 {
			return firstRPC, nil
		}
		return secondRPC, nil
	}
	errCh := make(chan error, 2)
	go func() {
		_, err := authority.DeliverAgentInput(context.Background(), codexAuthorityRequest())
		errCh <- err
	}()
	<-firstRPC.turnStarted
	second := codexAuthorityRequest()
	second.Delivery.IntentKey = "intent-2"
	go func() { _, err := authority.DeliverAgentInput(context.Background(), second); errCh <- err }()
	close(firstRPC.releaseTurn)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.maxGates != 1 {
		t.Fatalf("max concurrent gates = %d, want 1", adapter.maxGates)
	}
}

func TestCodexInputGateRestoresNewReadOnlyAttachFromHookLedger(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty-old", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	authority := newFakeCodexAuthority(t, adapter, &fakeCodexRPC{})
	gate, err := authority.acquireGate(context.Background(), codexAuthorityRequest())
	if err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	adapter.clients = append(adapter.clients, tmux.AttachedClientInfo{ClientName: "tty-new", SessionName: "az-dlb", ReadOnly: true})
	recordPath := adapter.recordPath
	adapter.mu.Unlock()
	if err := os.WriteFile(recordPath, []byte("tty-new\t0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := gate.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.clients[1].ReadOnly {
		t.Fatalf("new writable attach was not restored: %+v", adapter.clients)
	}
}

func TestCodexInputGateStartupRecoveryRestoresPersistedFlags(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) string { return "codex" })
	if err := os.MkdirAll(authority.gateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(authority.gateDir, "gate-dead.events")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := codexInputGateState{Version: codexInputGateStateVersion, SessionID: "az-dlb", PaneID: "12", PaneInputEnabled: true,
		HookID: "9137", EventsPath: eventsPath, OriginalReadOnly: map[string]bool{"tty": false}}
	raw, _ := json.Marshal(state)
	statePath := filepath.Join(authority.gateDir, "gate-dead.json")
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authority.RecoverStaleGates(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.clients[0].ReadOnly || !adapter.paneEnabled || adapter.hooksEnabled {
		t.Fatalf("stale gate was not restored: pane=%v hooks=%v clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.clients)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered state remains: %v", err)
	}
}
