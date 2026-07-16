package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type fakeCodexInputTmux struct {
	mu                           sync.Mutex
	clients                      []tmux.AttachedClientInfo
	panes                        []tmux.PaneInfo
	paneEnabled                  bool
	hooksEnabled                 bool
	recordPath                   string
	setReadOnlyCalls             []string
	maxGates                     int
	activeGates                  int
	failDisableHooks             bool
	failDisablePane              bool
	disablePaneErrorsAfterEffect int
}

func (f *fakeCodexInputTmux) ListAttachedClients(_ context.Context, session string) ([]tmux.AttachedClientInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	clients := make([]tmux.AttachedClientInfo, 0, len(f.clients))
	for _, client := range f.clients {
		if session == "" || client.SessionName == session {
			clients = append(clients, client)
		}
	}
	return clients, nil
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
	if !enabled && f.failDisablePane {
		return errors.New("disable pane input failed")
	}
	f.paneEnabled = enabled
	if !enabled && f.disablePaneErrorsAfterEffect > 0 {
		f.disablePaneErrorsAfterEffect--
		return errors.New("disable pane input failed after side effect")
	}
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
	if !enabled && f.failDisableHooks {
		return errors.New("disable hooks failed")
	}
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

func (f *fakeCodexRPC) Call(ctx context.Context, method string, params, result any) error {
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
			select {
			case <-f.releaseTurn:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
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
	}, LeaseToken: "lease", SessionLeaseOwner: "daemon", SessionLeaseToken: "fence", BeginSubmission: func(context.Context) (time.Time, error) { return time.Now().Add(time.Minute), nil }, RevalidateSubmissionFence: func(context.Context) (time.Time, error) { return time.Now().Add(time.Minute), nil }, RenewRestoreFence: func(context.Context) (bool, error) { return true, nil }}
}

func newFakeCodexAuthority(t *testing.T, adapter *fakeCodexInputTmux, rpc *fakeCodexRPC) *codexAppServerInputAuthority {
	t.Helper()
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	authority.startRPC = func(context.Context, string) (codexAppServerRPC, error) { return rpc, nil }
	return authority
}

func TestCodexAppServerDeliveryFailsClosedUnlessProjectCapabilityIsEnabled(t *testing.T) {
	tests := []struct {
		name         string
		config       daemonProjectRuntimeConfig
		deliveryTool string
	}{
		{name: "disabled app server", config: daemonProjectRuntimeConfig{CLITool: "codex"}, deliveryTool: "codex"},
		{name: "unsupported configured tool", config: daemonProjectRuntimeConfig{CLITool: "claude", CodexAppServer: true}, deliveryTool: "codex"},
		{name: "unsupported delivery tool", config: daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}, deliveryTool: "claude"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &fakeCodexInputTmux{}
			authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig { return test.config })
			started := false
			authority.startRPC = func(context.Context, string) (codexAppServerRPC, error) {
				started = true
				return &fakeCodexRPC{}, nil
			}
			request := codexAuthorityRequest()
			request.Delivery.Tool = test.deliveryTool
			if _, err := authority.DeliverAgentInput(context.Background(), request); !errors.Is(err, errAuthoritativeAgentInputUnavailable) {
				t.Fatalf("err=%v, want unavailable", err)
			}
			if started {
				t.Fatal("app-server proxy started without exact enabled capability")
			}
		})
	}
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
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		if adapter.paneEnabled {
			return errors.New("turn/start ran without the pane input fence")
		}
		return nil
	}}
	request := codexAuthorityRequest()
	request.BeginSubmission = func(context.Context) (time.Time, error) {
		boundaryBegun = true
		return time.Now().Add(time.Minute), nil
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

func TestCodexAppServerDeliveryKeepsGenericMethodErrorsAmbiguous(t *testing.T) {
	tests := []codexRPCMethodError{
		{Method: "turn/start", Code: -32603, Message: "internal error"},
		{Method: "turn/start", Code: -32600, Message: "unknown request"},
	}
	for _, methodErr := range tests {
		adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
		_, err := newFakeCodexAuthority(t, adapter, &fakeCodexRPC{turnErr: methodErr}).DeliverAgentInput(context.Background(), codexAuthorityRequest())
		var refusal agentInputRefusalError
		if err == nil || errors.As(err, &refusal) {
			t.Fatalf("method error %+v became retryable refusal: %v", methodErr, err)
		}
	}
}

func TestCodexAppServerDeliveryKeepsActiveTurnMethodErrorAmbiguous(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	methodErr := codexRPCMethodError{Method: "turn/start", Code: -32600, Message: "turn already active"}
	_, err := newFakeCodexAuthority(t, adapter, &fakeCodexRPC{turnErr: methodErr}).DeliverAgentInput(context.Background(), codexAuthorityRequest())
	var refusal agentInputRefusalError
	if err == nil || errors.As(err, &refusal) {
		t.Fatalf("active-turn method error became retryable refusal: %v", err)
	}
}

func TestCodexAppServerDeliveryBoundsAcceptanceBySessionFenceExpiry(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	rpc := &fakeCodexRPC{releaseTurn: make(chan struct{})}
	request := codexAuthorityRequest()
	now := time.Date(2026, time.July, 17, 5, 0, 0, 0, time.UTC)
	request.RevalidateSubmissionFence = func(context.Context) (time.Time, error) { return now.Add(time.Minute), nil }
	authority := newFakeCodexAuthority(t, adapter, rpc)
	authority.now = func() time.Time { return now }
	authority.safetyMargin = 5 * time.Second
	wantDeadline := now.Add(55 * time.Second)
	authority.acceptContext = func(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
		if !deadline.Equal(wantDeadline) {
			t.Fatalf("acceptance deadline=%s, want %s", deadline, wantDeadline)
		}
		acceptCtx, cancelCause := context.WithCancelCause(parent)
		cancelCause(context.DeadlineExceeded)
		return acceptCtx, func() {}
	}
	if _, err := authority.DeliverAgentInput(context.Background(), request); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline ambiguity", err)
	}
}

type controlledDeadlineWriter struct {
	deadlineObserved bool
	deadline         time.Time
}

func (w *controlledDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	if !deadline.IsZero() {
		w.deadlineObserved = true
	}
	return nil
}

func (w *controlledDeadlineWriter) Write([]byte) (int, error) {
	if w.deadline.IsZero() {
		return 0, errors.New("write began without a deadline")
	}
	return 0, os.ErrDeadlineExceeded
}

func (*controlledDeadlineWriter) Close() error { return nil }

func TestProcessCodexAppServerRPCBoundsBlockedSubmissionWrite(t *testing.T) {
	writer := &controlledDeadlineWriter{}
	rpc := &processCodexAppServerRPC{stdin: writer, writeGate: make(chan struct{}, 1), waits: map[string]chan codexRPCResponse{}, done: make(chan struct{})}
	rpc.writeGate <- struct{}{}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
	defer cancel()
	if err := rpc.Call(ctx, "turn/start", map[string]any{"threadId": "thread"}, nil); err == nil {
		t.Fatal("blocked submission write unexpectedly succeeded")
	}
	if !writer.deadlineObserved {
		t.Fatal("submission writer did not receive the context deadline")
	}
}

func TestCodexAppServerGateRestoresAfterPaneDisableSideEffectThenError(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, disablePaneErrorsAfterEffect: 1}
	authority := newFakeCodexAuthority(t, adapter, &fakeCodexRPC{})
	_, err := authority.acquireGate(context.Background(), codexAuthorityRequest())
	if err == nil || !strings.Contains(err.Error(), "after side effect") {
		t.Fatalf("err=%v, want side-effect failure", err)
	}
	adapter.mu.Lock()
	paneEnabled := adapter.paneEnabled
	adapter.mu.Unlock()
	if !paneEnabled {
		t.Fatal("pane input remained disabled after acquisition failure")
	}
	markers, globErr := filepath.Glob(filepath.Join(authority.gateDir, "gate-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(markers) != 0 {
		t.Fatalf("fully restored acquisition retained gate artifacts: %v", markers)
	}
}

func TestCodexAppServerDeliveryFailsClosedForLivePaneMismatch(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 99}}}
	rpc := &fakeCodexRPC{}
	request := codexAuthorityRequest()
	boundaryBegun := false
	request.BeginSubmission = func(context.Context) (time.Time, error) {
		boundaryBegun = true
		return time.Now().Add(time.Minute), nil
	}
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
	request.BeginSubmission = func(context.Context) (time.Time, error) {
		adapter.mu.Lock()
		adapter.clients[0].ReadOnly = false
		adapter.mu.Unlock()
		return time.Now().Add(time.Minute), nil
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

func TestCodexAppServerDeliveryRevalidatesThreadAfterDurableBoundary(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	rpc := &fakeCodexRPC{}
	request := codexAuthorityRequest()
	request.RevalidateSubmissionFence = func(context.Context) (time.Time, error) {
		return time.Time{}, agentInputRefusalError{outcome: "stale_incarnation", safeToRetry: true}
	}
	_, err := newFakeCodexAuthority(t, adapter, rpc).DeliverAgentInput(context.Background(), request)
	var refusal agentInputRefusalError
	if !errors.As(err, &refusal) || refusal.outcome != "stale_incarnation" || !refusal.safeToRetry {
		t.Fatalf("err=%v refusal=%+v", err, refusal)
	}
	if rpc.turnParams != nil {
		t.Fatalf("turn submitted after thread incarnation changed: %#v", rpc.turnParams)
	}
}

func TestCodexAppServerDeliveryRevalidatesPaneFenceAfterDurableFence(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	rpc := &fakeCodexRPC{}
	request := codexAuthorityRequest()
	request.RevalidateSubmissionFence = func(context.Context) (time.Time, error) {
		adapter.mu.Lock()
		adapter.paneEnabled = true
		adapter.mu.Unlock()
		return time.Now().Add(time.Minute), nil
	}
	_, err := newFakeCodexAuthority(t, adapter, rpc).DeliverAgentInput(context.Background(), request)
	var refusal agentInputRefusalError
	if !errors.As(err, &refusal) || refusal.outcome != "human_attached" || !refusal.safeToRetry {
		t.Fatalf("err=%v refusal=%+v", err, refusal)
	}
	if rpc.turnParams != nil {
		t.Fatalf("turn submitted after pane input fence was lost: %#v", rpc.turnParams)
	}
}

func TestCodexInputGateRestoreRetainsGateWhenPaneFenceFails(t *testing.T) {
	adapter := &fakeCodexInputTmux{
		paneEnabled:     true,
		hooksEnabled:    true,
		failDisablePane: true,
		clients:         []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}},
	}
	gate := &codexInputGate{
		tmux: adapter,
		state: codexInputGateState{
			SessionID:        "az-dlb",
			PaneID:           "12",
			PaneInputEnabled: true,
			OriginalReadOnly: map[string]bool{"tty": false},
		},
		renewRestoreFence: func(context.Context) (bool, error) { return true, nil },
	}
	if err := gate.Restore(context.Background()); err == nil {
		t.Fatal("restore unexpectedly proceeded without the pane input fence")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if !adapter.hooksEnabled || !adapter.clients[0].ReadOnly || gate.restored {
		t.Fatalf("failed pane fence ungated session: hooks=%v clients=%+v restored=%v", adapter.hooksEnabled, adapter.clients, gate.restored)
	}
}

func TestCodexAppServerDeliverySerializesOneSessionGate(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	firstRPC := &fakeCodexRPC{turnStarted: make(chan struct{}), releaseTurn: make(chan struct{})}
	secondRPC := &fakeCodexRPC{}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
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

func TestCodexInputGateIncarnationTakeoverNeverOverlapsOrOldOwnerUngates(t *testing.T) {
	ctx := context.Background()
	adapter := &fakeCodexInputTmux{paneEnabled: true, hooksEnabled: true, activeGates: 1, maxGates: 1, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	oldLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-old", "daemon-old", time.Now().Add(-2*time.Minute), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("old lease=%+v acquired=%v err=%v", oldLease, acquired, err)
	}
	if err := os.MkdirAll(authority.gateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(authority.gateDir, "gate-old.events")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldState := codexInputGateState{Version: codexInputGateStateVersion, ProjectID: "p", SessionID: "az-dlb", AgentIncarnation: "thread-old", LeaseOwner: "daemon-old", FenceToken: oldLease.LeaseToken, PaneID: "12", PanePID: 42, PaneInputEnabled: true, HookID: "100", EventsPath: eventsPath, OriginalReadOnly: map[string]bool{"tty": false}}
	statePath := filepath.Join(authority.gateDir, "gate-old.json")
	raw, _ := json.Marshal(oldState)
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldGate := &codexInputGate{tmux: adapter, state: oldState, statePath: statePath, renewRestoreFence: func(restoreCtx context.Context) (bool, error) {
		_, renewed, err := client.RenewAgentInputDeliverySessionLease(restoreCtx, "p", "az-dlb", "thread-old", oldLease.LeaseOwner, oldLease.LeaseToken, time.Now(), time.Minute)
		return renewed, err
	}}
	newLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-new", "daemon-new", time.Now(), time.Minute)
	if err != nil || !acquired || newLease.PreviousLeaseToken != oldLease.LeaseToken || newLease.PreviousAgentIncarnation != "thread-old" {
		t.Fatalf("new lease=%+v acquired=%v err=%v", newLease, acquired, err)
	}
	rpc := &fakeCodexRPC{turnStarted: make(chan struct{}), releaseTurn: make(chan struct{})}
	authority.startRPC = func(context.Context, string) (codexAppServerRPC, error) { return rpc, nil }
	request := codexAuthorityRequest()
	request.Delivery.Target.AgentIncarnation = "thread-new"
	request.SessionLeaseOwner = "daemon-new"
	request.SessionLeaseToken = newLease.LeaseToken
	request.PreviousAgentIncarnation = newLease.PreviousAgentIncarnation
	request.PreviousSessionLeaseToken = newLease.PreviousLeaseToken
	currentLease := newLease
	request.CompleteSessionTakeover = func(takeoverCtx context.Context) (issues.AgentInputDeliverySessionLease, error) {
		completed, ok, err := client.CompleteAgentInputDeliverySessionLeaseTakeover(takeoverCtx, "p", "az-dlb", currentLease.AgentIncarnation, currentLease.LeaseToken, "thread-new", "daemon-new", time.Now(), time.Minute)
		if err != nil {
			return issues.AgentInputDeliverySessionLease{}, err
		}
		if !ok {
			return issues.AgentInputDeliverySessionLease{}, errors.New("takeover completion lost exact fence")
		}
		currentLease = completed
		return completed, nil
	}
	request.RenewRestoreFence = func(restoreCtx context.Context) (bool, error) {
		_, renewed, err := client.RenewAgentInputDeliverySessionLease(restoreCtx, "p", "az-dlb", currentLease.AgentIncarnation, currentLease.LeaseOwner, currentLease.LeaseToken, time.Now(), time.Minute)
		return renewed, err
	}
	done := make(chan error, 1)
	go func() {
		_, err := authority.DeliverAgentInput(ctx, request)
		done <- err
	}()
	<-rpc.turnStarted
	adapter.mu.Lock()
	if adapter.maxGates != 1 || adapter.activeGates != 1 || !adapter.hooksEnabled || !adapter.clients[0].ReadOnly {
		adapter.mu.Unlock()
		t.Fatalf("takeover gate overlap/state max=%d active=%d hooks=%v clients=%+v", adapter.maxGates, adapter.activeGates, adapter.hooksEnabled, adapter.clients)
	}
	adapter.mu.Unlock()
	if err := oldGate.Restore(ctx); !errors.Is(err, errCodexSessionFenceLost) {
		t.Fatalf("old owner restore err=%v, want fence loss", err)
	}
	adapter.mu.Lock()
	if adapter.activeGates != 1 || !adapter.hooksEnabled || !adapter.clients[0].ReadOnly {
		adapter.mu.Unlock()
		t.Fatalf("old owner ungated new owner: active=%d hooks=%v clients=%+v", adapter.activeGates, adapter.hooksEnabled, adapter.clients)
	}
	adapter.mu.Unlock()
	close(rpc.releaseTurn)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCodexInputGateTakeoverFailuresRetainExactRecoveryAuthority(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*fakeCodexInputTmux)
		wantMarker bool
	}{
		{name: "before restore", configure: func(adapter *fakeCodexInputTmux) { adapter.panes[0].PanePID = 99 }, wantMarker: true},
		{name: "during restore", configure: func(adapter *fakeCodexInputTmux) { adapter.failDisableHooks = true }, wantMarker: true},
		{name: "after restore before rotation", wantMarker: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			adapter := &fakeCodexInputTmux{paneEnabled: true, hooksEnabled: true, activeGates: 1, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
			if test.configure != nil {
				test.configure(adapter)
			}
			authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
				return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
			})
			client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
			t.Cleanup(func() { _ = client.CloseDB() })
			oldLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-old", "daemon-old", now.Add(-2*time.Minute), time.Minute)
			if err != nil || !acquired {
				t.Fatalf("old lease=%+v acquired=%v err=%v", oldLease, acquired, err)
			}
			takeover, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-new", "daemon-new", now, time.Minute)
			if err != nil || !acquired || !takeover.TakeoverPending || takeover.LeaseToken != oldLease.LeaseToken {
				t.Fatalf("takeover=%+v acquired=%v err=%v", takeover, acquired, err)
			}
			if err := os.MkdirAll(authority.gateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			eventsPath := filepath.Join(authority.gateDir, "gate-old.events")
			if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			state := codexInputGateState{Version: codexInputGateStateVersion, ProjectID: "p", SessionID: "az-dlb", AgentIncarnation: "thread-old", LeaseOwner: "daemon-old", FenceToken: oldLease.LeaseToken, PaneID: "12", PanePID: 42, PaneInputEnabled: true, HookID: "100", EventsPath: eventsPath, OriginalReadOnly: map[string]bool{"tty": false}}
			statePath := filepath.Join(authority.gateDir, "gate-old.json")
			raw, _ := json.Marshal(state)
			if err := os.WriteFile(statePath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			request := codexAuthorityRequest()
			request.Delivery.Target.AgentIncarnation = "thread-new"
			request.SessionLeaseOwner = takeover.LeaseOwner
			request.SessionLeaseToken = takeover.LeaseToken
			request.PreviousAgentIncarnation = takeover.PreviousAgentIncarnation
			request.PreviousSessionLeaseToken = takeover.PreviousLeaseToken
			request.RenewRestoreFence = func(restoreCtx context.Context) (bool, error) {
				_, renewed, err := client.RenewAgentInputDeliverySessionLease(restoreCtx, "p", "az-dlb", takeover.AgentIncarnation, takeover.LeaseOwner, takeover.LeaseToken, time.Now().UTC(), time.Minute)
				return renewed, err
			}
			request.CompleteSessionTakeover = func(context.Context) (issues.AgentInputDeliverySessionLease, error) {
				return issues.AgentInputDeliverySessionLease{}, errors.New("injected crash before fence rotation")
			}
			if err := authority.recoverSupersededGate(ctx, &request); err == nil {
				t.Fatal("takeover failure was accepted")
			}
			if _, renewed, err := client.RenewAgentInputDeliverySessionLease(ctx, "p", "az-dlb", takeover.AgentIncarnation, "daemon-old", takeover.LeaseToken, now.Add(time.Second), time.Minute); err != nil || renewed {
				t.Fatalf("old owner renewed transferred fence=%v err=%v", renewed, err)
			}
			if _, renewed, err := client.RenewAgentInputDeliverySessionLease(ctx, "p", "az-dlb", takeover.AgentIncarnation, takeover.LeaseOwner, takeover.LeaseToken, now.Add(time.Second), time.Minute); err != nil || !renewed {
				t.Fatalf("takeover owner lost recovery fence=%v err=%v", renewed, err)
			}
			_, statErr := os.Stat(statePath)
			if test.wantMarker && statErr != nil {
				t.Fatalf("recovery marker removed: %v", statErr)
			}
			if !test.wantMarker && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("completed restore marker remains: %v", statErr)
			}
		})
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

func TestCodexInputGateRestoresRecordedClientAfterSessionSwitch(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, clients: []tmux.AttachedClientInfo{{ClientName: "tty-old", SessionName: "az-dlb"}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	authority := newFakeCodexAuthority(t, adapter, &fakeCodexRPC{})
	gate, err := authority.acquireGate(context.Background(), codexAuthorityRequest())
	if err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	adapter.clients[0].SessionName = "az-other"
	adapter.mu.Unlock()
	if err := gate.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.clients[0].ReadOnly {
		t.Fatalf("recorded client remained globally read-only after switching sessions: %+v", adapter.clients)
	}
}

func TestCodexInputGateStartupRecoveryRefusesLiveOwner(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, hooksEnabled: true, activeGates: 1, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	lease, acquired, err := client.ClaimAgentInputDeliverySessionLease(context.Background(), "p", "az-dlb", "thread-exact", "live-daemon", time.Now(), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
	authority.issueClients = func(string) *issues.Client { return client }
	if err := os.MkdirAll(authority.gateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(authority.gateDir, "gate-dead.events")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := codexInputGateState{Version: codexInputGateStateVersion, ProjectID: "p", SessionID: "az-dlb", AgentIncarnation: "thread-exact", LeaseOwner: "live-daemon", FenceToken: lease.LeaseToken, PaneID: "12", PanePID: 42, PaneInputEnabled: true, HookID: "9137", EventsPath: eventsPath, OriginalReadOnly: map[string]bool{"tty": false}}
	raw, _ := json.Marshal(state)
	statePath := filepath.Join(authority.gateDir, "gate-dead.json")
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authority.RecoverStaleGates(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	if !adapter.clients[0].ReadOnly || !adapter.paneEnabled || !adapter.hooksEnabled {
		adapter.mu.Unlock()
		t.Fatalf("live-owner recovery mutated gate: pane=%v hooks=%v clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.clients)
	}
	adapter.mu.Unlock()
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("live-owner state was removed: %v", err)
	}
}

func TestCodexInputGateStartupRecoveryRefusesMissingDurableLease(t *testing.T) {
	ctx := context.Background()
	adapter := &fakeCodexInputTmux{paneEnabled: true, hooksEnabled: true, activeGates: 1, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	if _, err := client.List(ctx); err != nil {
		t.Fatal(err)
	}
	authority.issueClients = func(string) *issues.Client { return client }
	if err := os.MkdirAll(authority.gateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(authority.gateDir, "gate-missing.events")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := codexInputGateState{Version: codexInputGateStateVersion, ProjectID: "p", SessionID: "az-dlb", AgentIncarnation: "thread-old", LeaseOwner: "dead-daemon", FenceToken: "missing-token", PaneID: "12", PanePID: 42, PaneInputEnabled: true, HookID: "9137", EventsPath: eventsPath, OriginalReadOnly: map[string]bool{"tty": false}}
	raw, _ := json.Marshal(state)
	statePath := filepath.Join(authority.gateDir, "gate-missing.json")
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authority.RecoverStaleGates(ctx); !errors.Is(err, errCodexSessionFenceLost) {
		t.Fatalf("recovery error=%v, want missing-fence diagnostic", err)
	}
	adapter.mu.Lock()
	if !adapter.clients[0].ReadOnly || !adapter.paneEnabled || !adapter.hooksEnabled {
		adapter.mu.Unlock()
		t.Fatalf("missing-lease recovery mutated runtime: pane=%v hooks=%v clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.clients)
	}
	adapter.mu.Unlock()
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("missing-lease diagnostic marker was removed: %v", err)
	}
}

func TestCodexInputGateStartupRecoveryRetriesAtLiveOwnerExpiry(t *testing.T) {
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	adapter := &fakeCodexInputTmux{paneEnabled: true, hooksEnabled: true, activeGates: 1, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	lease, acquired, err := client.ClaimAgentInputDeliverySessionLease(context.Background(), "p", "az-dlb", "thread-exact", "dead-daemon", now, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
	authority.issueClients = func(string) *issues.Client { return client }
	authority.now = func() time.Time { return now }
	retryAt := make(chan time.Time)
	releaseRetry := make(chan struct{})
	authority.recoveryWait = func(ctx context.Context, at time.Time) bool {
		select {
		case <-ctx.Done():
			return false
		case retryAt <- at:
		}
		select {
		case <-ctx.Done():
			return false
		case <-releaseRetry:
			return true
		}
	}
	if err := os.MkdirAll(authority.gateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(authority.gateDir, "gate-dead.events")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := codexInputGateState{Version: codexInputGateStateVersion, ProjectID: "p", SessionID: "az-dlb", AgentIncarnation: "thread-exact", LeaseOwner: "dead-daemon", FenceToken: lease.LeaseToken, PaneID: "12", PanePID: 42, PaneInputEnabled: true, HookID: "9137", EventsPath: eventsPath, OriginalReadOnly: map[string]bool{"tty": false}}
	raw, _ := json.Marshal(state)
	statePath := filepath.Join(authority.gateDir, "gate-dead.json")
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	operations, err := authority.recoverStaleGates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].statePath != statePath {
		t.Fatalf("scheduled recovery operations=%+v, want exact gate %s", operations, statePath)
	}
	select {
	case scheduled := <-retryAt:
		if !scheduled.Equal(lease.LeaseExpires) {
			t.Fatalf("scheduled retry=%s want lease expiry=%s", scheduled, lease.LeaseExpires)
		}
	case <-ctx.Done():
		t.Fatalf("recovery context cancelled before retry scheduling: %v", context.Cause(ctx))
	}
	now = lease.LeaseExpires.Add(time.Nanosecond)
	close(releaseRetry)
	select {
	case err := <-operations[0].done:
		if err != nil {
			t.Fatalf("scheduled recovery: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("recovery context cancelled before completion: %v", context.Cause(ctx))
	}
	postRecoveryLease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-exact", "completion-observer", now, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("completion signalled before recovery lease release: lease=%+v acquired=%v err=%v", postRecoveryLease, acquired, err)
	}
	if err := client.ReleaseAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-exact", postRecoveryLease.LeaseOwner, postRecoveryLease.LeaseToken); err != nil {
		t.Fatalf("release completion-observer lease: %v", err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered state remains: %v", err)
	}
	cancel()
	<-ctx.Done()
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.clients[0].ReadOnly || !adapter.paneEnabled || adapter.hooksEnabled {
		t.Fatalf("scheduled recovery did not restore gate: pane=%v hooks=%v clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.clients)
	}
}

func TestCodexInputGateScheduledRecoveryCompletionReportsCancellation(t *testing.T) {
	authority := newCodexAppServerInputAuthority(&fakeCodexInputTmux{}, filepath.Join(t.TempDir(), "daemon.sock"), nil, nil)
	waiting := make(chan struct{})
	authority.recoveryWait = func(ctx context.Context, _ time.Time) bool {
		close(waiting)
		<-ctx.Done()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	operation, scheduled := authority.scheduleGateRecovery(ctx, "gate-cancelled.json", time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	if !scheduled {
		t.Fatal("recovery was not scheduled")
	}
	<-waiting
	cancel()
	if err := <-operation.done; !errors.Is(err, context.Canceled) {
		t.Fatalf("scheduled recovery completion error=%v, want context cancellation", err)
	}
	authority.recoveryMux.Lock()
	_, queued := authority.recoveryQueued[operation.statePath]
	authority.recoveryMux.Unlock()
	if queued {
		t.Fatalf("cancelled recovery remained queued: %s", operation.statePath)
	}
}

func TestCodexInputGateStartupRecoveryRestoresExpiredOwner(t *testing.T) {
	adapter := &fakeCodexInputTmux{paneEnabled: true, hooksEnabled: true, activeGates: 1, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 42}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	lease, acquired, err := client.ClaimAgentInputDeliverySessionLease(context.Background(), "p", "az-dlb", "thread-old", "dead-daemon", time.Now().Add(-2*time.Minute), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
	authority.issueClients = func(string) *issues.Client { return client }
	if err := os.MkdirAll(authority.gateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(authority.gateDir, "gate-dead.events")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := codexInputGateState{Version: codexInputGateStateVersion, ProjectID: "p", SessionID: "az-dlb", AgentIncarnation: "thread-old", LeaseOwner: "dead-daemon", FenceToken: lease.LeaseToken, PaneID: "12", PanePID: 42, PaneInputEnabled: true, HookID: "9137", EventsPath: eventsPath, OriginalReadOnly: map[string]bool{"tty": false}}
	raw, _ := json.Marshal(state)
	statePath := filepath.Join(authority.gateDir, "gate-dead.json")
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authority.RecoverStaleGates(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	if adapter.clients[0].ReadOnly || !adapter.paneEnabled || adapter.hooksEnabled {
		adapter.mu.Unlock()
		t.Fatalf("expired gate was not restored: pane=%v hooks=%v clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.clients)
	}
	adapter.mu.Unlock()
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered state remains: %v", err)
	}
}

func TestCodexInputGateStartupRecoveryRefusesReusedPanePID(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	adapter := &fakeCodexInputTmux{paneEnabled: true, hooksEnabled: true, activeGates: 1, clients: []tmux.AttachedClientInfo{{ClientName: "tty", SessionName: "az-dlb", ReadOnly: true}}, panes: []tmux.PaneInfo{{SessionName: "az-dlb", PaneID: "12", PanePID: 99}}}
	authority := newCodexAppServerInputAuthority(adapter, filepath.Join(t.TempDir(), "daemon.sock"), nil, func(string) daemonProjectRuntimeConfig {
		return daemonProjectRuntimeConfig{CLITool: "codex", CodexAppServer: true}
	})
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	lease, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-old", "dead-daemon", now.Add(-2*time.Minute), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease=%+v acquired=%v err=%v", lease, acquired, err)
	}
	authority.issueClients = func(string) *issues.Client { return client }
	authority.now = func() time.Time { return now }
	if err := os.MkdirAll(authority.gateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(authority.gateDir, "gate-reused.events")
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := codexInputGateState{Version: codexInputGateStateVersion, ProjectID: "p", SessionID: "az-dlb", AgentIncarnation: "thread-old", LeaseOwner: "dead-daemon", FenceToken: lease.LeaseToken, PaneID: "12", PanePID: 42, PaneInputEnabled: true, HookID: "9137", EventsPath: eventsPath, OriginalReadOnly: map[string]bool{"tty": false}}
	raw, _ := json.Marshal(state)
	statePath := filepath.Join(authority.gateDir, "gate-reused.json")
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authority.RecoverStaleGates(ctx); !errors.Is(err, errCodexPaneIdentityChanged) {
		t.Fatalf("recovery error=%v, want pane identity refusal", err)
	}
	adapter.mu.Lock()
	if !adapter.clients[0].ReadOnly || !adapter.paneEnabled || !adapter.hooksEnabled {
		adapter.mu.Unlock()
		t.Fatalf("reused-pane recovery mutated runtime: pane=%v hooks=%v clients=%+v", adapter.paneEnabled, adapter.hooksEnabled, adapter.clients)
	}
	adapter.mu.Unlock()
	persistedRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("diagnostic recovery marker was removed: %v", err)
	}
	var persisted codexInputGateState
	if err := json.Unmarshal(persistedRaw, &persisted); err != nil || persisted.FenceToken != lease.LeaseToken || persisted.LeaseOwner != authority.recoveryOwner {
		t.Fatalf("recovery ownership was not durably transferred without rotating the marker fence: state=%+v err=%v", persisted, err)
	}
	next, acquired, err := client.ClaimAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-new", "new-daemon", now, time.Minute)
	if err != nil || acquired || next.LeaseToken != "" {
		t.Fatalf("failed recovery released live durable authority: lease=%+v acquired=%v err=%v", next, acquired, err)
	}
	next, acquired, err = client.ClaimAgentInputDeliverySessionLease(ctx, "p", "az-dlb", "thread-new", "new-daemon", now.Add(authority.leaseDuration), time.Minute)
	if err != nil || !acquired || !next.TakeoverPending || next.LeaseToken != lease.LeaseToken {
		t.Fatalf("expired failed recovery could not transfer exact fence: lease=%+v acquired=%v err=%v", next, acquired, err)
	}
}
