package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcceptNativeCodexDeliveryPreservesDraftAndDeduplicatesAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := loadNativeCodexState(path, false)
	if err != nil {
		t.Fatal(err)
	}
	envelope := nativeCodexEnvelope{ProjectID: "p", IntentKey: "intent", AgentIncarnation: state.Incarnation, LeaseToken: "lease", Payload: "automated"}
	submissions := 0
	submit := func(string) error { submissions++; return nil }
	response, active := acceptNativeCodexDelivery(envelope, []byte("human draft"), false, path, &state, submit)
	if response.Outcome != "composer_nonempty" || active || submissions != 0 {
		t.Fatalf("draft response=%+v active=%t submissions=%d", response, active, submissions)
	}
	response, active = acceptNativeCodexDelivery(envelope, nil, false, path, &state, submit)
	if response.Outcome != "accepted" || response.AcknowledgementToken == "" || !active || submissions != 1 {
		t.Fatalf("accept response=%+v active=%t submissions=%d", response, active, submissions)
	}
	wantAck := response.AcknowledgementToken
	restarted, err := loadNativeCodexState(path, true)
	if err != nil {
		t.Fatal(err)
	}
	response, active = acceptNativeCodexDelivery(envelope, nil, false, path, &restarted, submit)
	if response.Outcome != "accepted" || response.AcknowledgementToken != wantAck || active || submissions != 1 {
		t.Fatalf("retry response=%+v active=%t submissions=%d", response, active, submissions)
	}
	if len(state.Pending) != 0 {
		t.Fatalf("pending after accepted delivery = %#v", state.Pending)
	}
}

func TestAcceptNativeCodexDeliveryPersistsPendingBeforeSubmitFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := loadNativeCodexState(path, false)
	if err != nil {
		t.Fatal(err)
	}
	envelope := nativeCodexEnvelope{ProjectID: "p", IntentKey: "intent", AgentIncarnation: state.Incarnation, LeaseToken: "lease", Payload: "automated"}
	response, active := acceptNativeCodexDelivery(envelope, nil, false, path, &state, func(string) error { return errors.New("before reply crash") })
	if response.Outcome != "not_ready" || active || state.Pending["intent\x00"+state.Incarnation] == "" {
		t.Fatalf("response=%+v pending=%#v", response, state.Pending)
	}
	restarted, err := loadNativeCodexState(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Pending["intent\x00"+state.Incarnation] == "" {
		t.Fatalf("pending intent lost on restart: %#v", restarted)
	}
}

func TestAcceptNativeCodexDeliveryActiveDoesNotPoisonRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, _ := loadNativeCodexState(path, false)
	env := nativeCodexEnvelope{IntentKey: "active", AgentIncarnation: state.Incarnation}
	calls := 0
	r, _ := acceptNativeCodexDelivery(env, nil, true, path, &state, func(string) error { calls++; return nil })
	if r.Outcome != "not_ready" || len(state.Pending) != 0 || calls != 0 {
		t.Fatalf("response=%+v pending=%v calls=%d", r, state.Pending, calls)
	}
	r, active := acceptNativeCodexDelivery(env, nil, false, path, &state, func(string) error { calls++; return nil })
	if r.Outcome != "accepted" || !active || calls != 1 {
		t.Fatalf("response=%+v active=%v calls=%d", r, active, calls)
	}
}

func TestAcceptNativeCodexDeliveryFinalSaveFailureRetainsPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, _ := loadNativeCodexState(path, false)
	env := nativeCodexEnvelope{IntentKey: "fault", AgentIncarnation: state.Incarnation}
	calls := 0
	original := saveNativeCodexStateFn
	defer func() { saveNativeCodexStateFn = original }()
	saves := 0
	saveNativeCodexStateFn = func(string, nativeCodexState) error {
		saves++
		if saves == 2 {
			return errors.New("injected final save failure")
		}
		return nil
	}
	r, _ := acceptNativeCodexDelivery(env, nil, false, path, &state, func(string) error { calls++; return nil })
	if r.Outcome != "not_ready" || calls != 1 || state.Pending["fault\x00"+state.Incarnation] == "" {
		t.Fatalf("response=%+v calls=%d pending=%v", r, calls, state.Pending)
	}
	r, _ = acceptNativeCodexDelivery(env, nil, false, path, &state, func(string) error { calls++; return nil })
	if calls != 1 || r.Outcome != "not_ready" {
		t.Fatalf("retry response=%+v submit count=%d", r, calls)
	}
}

func TestStrictNativeCodexStateDoesNotMutate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	if _, err := strictNativeCodexState(path); err == nil {
		t.Fatal("missing state accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state mutated: %v", err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := strictNativeCodexState(path); err == nil {
		t.Fatal("corrupt state accepted")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "corrupt" {
		t.Fatalf("state overwritten: %q", raw)
	}
}

func TestRecoverNativeCodexIntentRequiresValidExactState(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".azedarach", "native-agent-input")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "s.json"), []byte("bad"), 0600); err == nil {
		if RecoverNativeCodexIntent(dir, "s", "i", "discard", "t") == nil {
			t.Fatal("corrupt recovery state accepted")
		}
	}
	path := filepath.Join(stateDir, "s.json")
	state := nativeCodexState{Incarnation: "inc", ThreadID: "thread", Pending: map[string]string{"i\x00inc": "msg"}, Accepted: map[string]string{}, Resolved: map[string]string{}}
	if err := saveNativeCodexState(path, state); err != nil {
		t.Fatal(err)
	}
	if err := RecoverNativeCodexIntent(dir, "s", "i", "discard", "wrong"); err == nil {
		t.Fatal("thread mismatch accepted")
	}
	if err := RecoverNativeCodexIntent(dir, "s", "i", "discard", "thread"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadNativeCodexState(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reloaded.Resolved["i\x00inc"], "discarded:") {
		t.Fatalf("resolution=%q", reloaded.Resolved["i\x00inc"])
	}
	response, _ := acceptNativeCodexDelivery(nativeCodexEnvelope{IntentKey: "i", AgentIncarnation: "inc"}, nil, false, path, &reloaded, func(string) error { t.Fatal("discard resubmitted"); return nil })
	if response.Outcome != "accepted" || response.AcknowledgementToken == "" {
		t.Fatalf("response=%+v", response)
	}
}

func TestRecoverNativeCodexDeliveredStableReplay(t *testing.T) {
	dir := t.TempDir()
	sd := filepath.Join(dir, ".azedarach", "native-agent-input")
	_ = os.MkdirAll(sd, 0700)
	path := filepath.Join(sd, "s.json")
	state := nativeCodexState{Incarnation: "inc", ThreadID: "thread", Pending: map[string]string{"i\x00inc": "msg"}, Accepted: map[string]string{}, Resolved: map[string]string{}}
	if err := saveNativeCodexState(path, state); err != nil {
		t.Fatal(err)
	}
	if err := RecoverNativeCodexIntent(dir, "s", "i", "delivered", "thread"); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := loadNativeCodexState(path, true)
	calls := 0
	var token string
	for n := 0; n < 2; n++ {
		r, _ := acceptNativeCodexDelivery(nativeCodexEnvelope{IntentKey: "i", AgentIncarnation: "inc"}, nil, false, path, &reloaded, func(string) error { calls++; return nil })
		if r.Outcome != "accepted" || r.AcknowledgementToken == "" {
			t.Fatalf("response=%+v", r)
		}
		if n == 0 {
			token = r.AcknowledgementToken
		} else if r.AcknowledgementToken != token {
			t.Fatalf("ack changed: %q != %q", r.AcknowledgementToken, token)
		}
	}
	if calls != 0 {
		t.Fatalf("submission calls=%d", calls)
	}
}

func TestNativeCodexRecoveryShellQuote(t *testing.T) {
	if got := shellQuote("intent with 'quote'"); got != `'intent with '\''quote'\'''` {
		t.Fatalf("quote=%q", got)
	}
}

func TestCodexRPCDisconnectBroadcastsToCallAndObservers(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go io.Copy(io.Discard, server)
	c := &codexRPCClient{stdin: client, waits: map[string]chan codexRPCMessage{}, done: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- c.call(context.Background(), "turn/start", nil, nil) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.waitMu.Lock()
		waiting := len(c.waits) > 0
		c.waitMu.Unlock()
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	sentinel := errors.New("connection lost")
	c.terminalDisconnect(sentinel)
	if err := <-result; !errors.Is(err, sentinel) {
		t.Fatal("in-flight call did not observe disconnect")
	}
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("observer did not wake")
	}
	if err := c.call(context.Background(), "turn/start", nil, nil); !errors.Is(err, sentinel) {
		t.Fatal("subsequent call did not fail")
	}
	c.waitMu.Lock()
	pending := len(c.waits)
	c.waitMu.Unlock()
	if pending != 0 {
		t.Fatalf("waiters leaked: %d", pending)
	}
}

func TestCodexRPCPreservesStringAndNumericServerRequestIDs(t *testing.T) {
	c := &codexRPCClient{waits: map[string]chan codexRPCMessage{}, requests: make(chan codexRPCMessage, 2), events: make(chan codexRPCMessage, 2), done: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		c.read(strings.NewReader("{\"id\":\"request-string\",\"method\":\"item/permissions/requestApproval\",\"params\":{}}\n{\"id\":7,\"method\":\"mcpServer/elicitation/request\",\"params\":{}}\n"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("read blocked")
	}
	var first, second codexRPCMessage
	select {
	case first = <-c.requests:
	case <-time.After(time.Second):
		t.Fatal("missing first request")
	}
	select {
	case second = <-c.requests:
	case <-time.After(time.Second):
		t.Fatal("missing second request")
	}
	if string(first.ID) != `"request-string"` || string(second.ID) != "7" {
		t.Fatalf("request IDs = %s, %s", first.ID, second.ID)
	}
}

func TestNativeCodexAuthorityLoopRegistersAndReturnsExactAcknowledgement(t *testing.T) {
	tmp, err := os.MkdirTemp("/tmp", "az-native-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	socket := filepath.Join(tmp, "authority.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	deliveries := make(chan nativeCodexDelivery)
	registration := nativeCodexRegistration{ProtocolVersion: 1, ProjectID: "p", SessionID: "s", LogicalPaneID: "agent", TmuxPaneID: "%1", PanePID: 42, AgentIncarnation: "inc", Tool: "codex"}
	go nativeCodexAuthorityLoop(ctx, socket, registration, deliveries)
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var gotRegistration nativeCodexRegistration
	if err := json.Unmarshal(line, &gotRegistration); err != nil || gotRegistration != registration {
		t.Fatalf("registration=%+v err=%v", gotRegistration, err)
	}
	envelope := nativeCodexEnvelope{ProjectID: "p", IntentKey: "intent", AgentIncarnation: "inc", LeaseToken: "lease", Payload: "wake"}
	if err := json.NewEncoder(conn).Encode(envelope); err != nil {
		t.Fatal(err)
	}
	delivery := <-deliveries
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := nativeCodexState{Incarnation: "inc", Accepted: map[string]string{}}
	response, active := acceptNativeCodexDelivery(delivery.envelope, nil, false, statePath, &state, func(string) error { return nil })
	if !active || response.Outcome != "accepted" {
		t.Fatalf("response=%+v active=%t", response, active)
	}
	delivery.reply <- response
	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var wire nativeCodexResponse
	if err := json.Unmarshal(line, &wire); err != nil || wire.IntentKey != "intent" || wire.LeaseToken != "lease" || wire.AcknowledgementToken == "" {
		t.Fatalf("wire=%+v err=%v", wire, err)
	}
}

func TestCodexRPCClientUsesNativeAppServerTurns(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1 $2" != "app-server proxy" ]; then exit 64; fi
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '%s\n' '{"id":1,"result":{"userAgent":"test"}}' ;;
    *'"method":"thread/start"'*) printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-1"}}}' ;;
    *'"method":"turn/start"'*) printf '%s\n' '{"id":3,"result":{"turn":{"id":"turn-1"}}}' ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rpc, err := startCodexRPC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.command.Process.Kill() //nolint:errcheck
	var initialized map[string]any
	if err := rpc.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "test"}}, &initialized); err != nil {
		t.Fatal(err)
	}
	if err := rpc.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := nativeCodexState{Incarnation: "inc", Accepted: map[string]string{}, Pending: map[string]string{}}
	threadID, err := nativeCodexThread(ctx, rpc, t.TempDir(), false, &state, statePath)
	if err != nil || threadID != "thread-1" {
		t.Fatalf("thread=%q err=%v", threadID, err)
	}
	if err := nativeCodexStartTurn(ctx, rpc, threadID, t.TempDir(), "hello", true, "msg-1"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadNativeCodexStateStartsNewIncarnationAndResumesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := loadNativeCodexState(path, false)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := loadNativeCodexState(path, true)
	if err != nil || resumed.Incarnation != first.Incarnation {
		t.Fatalf("resumed=%+v err=%v first=%+v", resumed, err, first)
	}
	second, err := loadNativeCodexState(path, false)
	if err != nil || second.Incarnation == first.Incarnation {
		t.Fatalf("new state=%+v err=%v first=%+v", second, err, first)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode=%v", info.Mode())
	}
}
