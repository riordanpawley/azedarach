package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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
	submit := func() error { submissions++; return nil }
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
	response, active := acceptNativeCodexDelivery(delivery.envelope, nil, false, statePath, &state, func() error { return nil })
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
	threadID, err := nativeCodexThread(ctx, rpc, t.TempDir(), false)
	if err != nil || threadID != "thread-1" {
		t.Fatalf("thread=%q err=%v", threadID, err)
	}
	if err := nativeCodexStartTurn(ctx, rpc, threadID, t.TempDir(), "hello", true); err != nil {
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
