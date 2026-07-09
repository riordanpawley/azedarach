package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestDaemonWatchClientsTracksWatchOriginatedRequests(t *testing.T) {
	d := &Daemon{}
	now := time.Now().UTC().Add(-time.Second)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-watch"),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandMailWatch,
		Meta: protocol.Metadata{
			ClientInvocationID: "inv-1",
			ClientCommandShape: "orchestrate watch --root az-root --jsonl",
			ClientPID:          35559,
			ClientPPID:         1,
			ClientCWD:          "/repo",
			ClientActiveIssue:  "az-root",
		},
	}

	d.recordWatchClientRequest("chefy", req, now)
	resp := d.handleDaemonWatchClients(protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-list"),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandDaemonWatchClients,
	})
	if !resp.OK {
		t.Fatalf("handleDaemonWatchClients response error: %+v", resp.Error)
	}
	var result protocol.DaemonWatchClientsResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Clients) != 1 {
		t.Fatalf("clients = %d, want 1", len(result.Clients))
	}
	client := result.Clients[0]
	if client.ClientPID != 35559 || client.ClientPPID != 1 || client.ProjectID != "chefy" || !client.OrphanCandidate {
		t.Fatalf("client = %+v, want tracked orphan candidate", client)
	}
}

func TestDaemonWatchClientsIgnoresNonWatchReadinessRequests(t *testing.T) {
	d := &Daemon{}
	d.recordWatchClientRequest("proj", protocol.RequestEnvelope{
		Command: "task.graph_readiness",
		Meta: protocol.Metadata{
			ClientCommandShape: "orchestrate status --root az-root",
			ClientPID:          123,
			ClientPPID:         99,
		},
	}, time.Now().UTC())

	resp := d.handleDaemonWatchClients(protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID("req-list"),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandDaemonWatchClients,
	})
	if !resp.OK {
		t.Fatalf("handleDaemonWatchClients response error: %+v", resp.Error)
	}
	var result protocol.DaemonWatchClientsResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Clients) != 0 {
		t.Fatalf("clients = %+v, want none", result.Clients)
	}
}
