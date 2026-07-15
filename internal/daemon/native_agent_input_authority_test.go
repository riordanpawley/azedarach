package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestNativeAgentInputAuthorityExactAcceptance(t *testing.T) {
	authority := newNativeAgentInputAuthority()
	authority.timeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tmp, err := os.MkdirTemp("/tmp", "az-input-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	socket := filepath.Join(tmp, "input.sock")
	serveErr := make(chan error, 1)
	go func() { serveErr <- authority.Serve(ctx, socket) }()

	var conn net.Conn
	err = nil
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		conn, err = net.Dial("unix", socket)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("dial authority: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	registration := nativeAgentInputRegistration{ProtocolVersion: nativeAgentInputProtocolVersion, ProjectID: "p", SessionID: "s",
		LogicalPaneID: "agent", TmuxPaneID: "%1", PanePID: 42, AgentIncarnation: "inc", Tool: "codex"}
	if err := json.NewEncoder(conn).Encode(registration); err != nil {
		t.Fatal(err)
	}

	received := make(chan nativeAgentInputEnvelope, 1)
	go func() {
		reader := bufio.NewReader(conn)
		var envelope nativeAgentInputEnvelope
		if readErr := readNativeAgentInputJSON(reader, &envelope); readErr != nil {
			return
		}
		received <- envelope
		_ = json.NewEncoder(conn).Encode(nativeAgentInputResponse{ProjectID: envelope.ProjectID, IntentKey: envelope.IntentKey,
			AgentIncarnation: envelope.AgentIncarnation, LeaseToken: envelope.LeaseToken, Outcome: "accepted", AcknowledgementToken: "ack-1"})
	}()

	request := authoritativeAgentInputRequest{Delivery: domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s",
		Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "%1", PanePID: 42, AgentIncarnation: "inc"},
		Tool:   "codex", Kind: domain.AgentInputMessageSessionMessage, Payload: "hello", IntentKey: "intent"}, LeaseToken: "lease"}
	var acknowledgement authoritativeAgentInputAcknowledgement
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		acknowledgement, err = authority.DeliverAgentInput(context.Background(), request)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if acknowledgement.AcknowledgementToken != "ack-1" {
		t.Fatalf("acknowledgement = %#v", acknowledgement)
	}
	select {
	case envelope := <-received:
		if envelope.Payload != "hello" || envelope.AgentIncarnation != "inc" || envelope.LeaseToken != "lease" {
			t.Fatalf("envelope = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("native client did not receive delivery")
	}
	cancel()
	if err := <-serveErr; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestNativeAgentInputAuthorityFailsClosedForStaleRegistration(t *testing.T) {
	authority := newNativeAgentInputAuthority()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	authority.clients[nativeAgentInputClientKey("p", "s", "agent")] = &nativeAgentInputClient{registration: nativeAgentInputRegistration{
		ProtocolVersion: nativeAgentInputProtocolVersion, ProjectID: "p", SessionID: "s", LogicalPaneID: "agent",
		TmuxPaneID: "%1", PanePID: 41, AgentIncarnation: "old", Tool: "codex"}, conn: server, reader: bufio.NewReader(server)}
	request := authoritativeAgentInputRequest{Delivery: domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s",
		Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "%1", PanePID: 42, AgentIncarnation: "new"}, Tool: "codex"}}
	if _, err := authority.DeliverAgentInput(context.Background(), request); err != errAuthoritativeAgentInputUnavailable {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestNativeAgentInputAuthorityHonorsDeliveryCancellation(t *testing.T) {
	authority := newNativeAgentInputAuthority()
	authority.timeout = time.Minute
	server, clientConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	registration := nativeAgentInputRegistration{ProtocolVersion: nativeAgentInputProtocolVersion, ProjectID: "p", SessionID: "s",
		LogicalPaneID: "agent", TmuxPaneID: "%1", PanePID: 42, AgentIncarnation: "inc", Tool: "codex"}
	client := &nativeAgentInputClient{registration: registration, conn: server, reader: bufio.NewReader(server), done: make(chan struct{})}
	authority.clients[nativeAgentInputClientKey("p", "s", "agent")] = client
	go func() {
		_, _ = bufio.NewReader(clientConn).ReadBytes('\n')
	}()
	request := authoritativeAgentInputRequest{Delivery: domain.AgentInputDeliveryRequest{ProjectID: "p", SessionID: "s",
		Target: domain.ManagedAgentRuntimeIdentity{LogicalPaneID: "agent", TmuxPaneID: "%1", PanePID: 42, AgentIncarnation: "inc"}, Tool: "codex"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := authority.DeliverAgentInput(ctx, request)
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != errAuthoritativeAgentInputUnavailable {
			t.Fatalf("error = %v, want unavailable after cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery did not honor context cancellation")
	}
}

func TestSessionLaunchExportsNativeAgentInputSocket(t *testing.T) {
	d := &Daemon{cfg: Config{SocketPath: "/tmp/azedarach-daemon.sock"}}
	commands := d.sessionLaunchStartupExportCommands(daemonProjectRuntimeConfig{}, issueResourceLifecycleContext{})
	if len(commands) == 0 || !strings.Contains(commands[0], "AZEDARACH_AGENT_INPUT_SOCKET='/tmp/azedarach-daemon-input.sock'") {
		t.Fatalf("startup exports = %#v", commands)
	}
}
