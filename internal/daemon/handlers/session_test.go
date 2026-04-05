package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestSessionHandlerStartPauseStopFlow(t *testing.T) {
	store := daemonstate.NewStore()
	h := NewSessionHandler(store)

	req := func(command string) protocol.RequestEnvelope {
		body, _ := json.Marshal(map[string]string{
			"project_id": "proj",
			"session_id": "s1",
			"issue_id":   "aey",
		})
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID("req-" + command),
			Kind:            protocol.EnvelopeKindCommand,
			Command:         command,
			Body:            body,
		}
	}

	r1 := h.Handle(context.Background(), req(CommandSessionStart))
	if !r1.OK || r1.Revision != 1 {
		t.Fatalf("start response = %+v", r1)
	}
	if got := store.ReadSnapshot("proj"); got.Sessions["s1"].State != daemonstate.SessionStateStarting {
		t.Fatalf("store state after start = %+v", got.Sessions["s1"])
	}
	r2 := h.Handle(context.Background(), req(CommandSessionAttach))
	if !r2.OK || r2.Revision != 2 {
		t.Fatalf("attach response = %+v", r2)
	}
	if got := store.ReadSnapshot("proj"); got.Sessions["s1"].State != daemonstate.SessionStateAttached {
		t.Fatalf("store state after attach = %+v", got.Sessions["s1"])
	}
	r3 := h.Handle(context.Background(), req(CommandSessionPause))
	if !r3.OK || r3.Revision != 3 {
		t.Fatalf("pause response = %+v", r3)
	}
	if got := store.ReadSnapshot("proj"); got.Sessions["s1"].State != daemonstate.SessionStatePaused {
		t.Fatalf("store state after pause = %+v", got.Sessions["s1"])
	}
	r4 := h.Handle(context.Background(), req(CommandSessionStop))
	if !r4.OK || r4.Revision != 4 {
		t.Fatalf("stop response = %+v", r4)
	}
	if got := store.ReadSnapshot("proj"); got.Sessions["s1"].State != daemonstate.SessionStateStopped {
		t.Fatalf("store state after stop = %+v", got.Sessions["s1"])
	}
}

func TestSessionHandlerInvalidTransitionMappedToConflict(t *testing.T) {
	store := daemonstate.NewStore()
	h := NewSessionHandler(store)
	body, _ := json.Marshal(map[string]string{
		"project_id": "proj",
		"session_id": "s1",
		"issue_id":   "aey",
	})

	_ = h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandSessionStart,
		Body:            body,
	})

	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-bad",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandSessionResume, // starting -> attached allowed; then attached -> attached no-op
		Body:            body,
	})
	if !resp.OK {
		t.Fatalf("expected resume from start to succeed, got %+v", resp.Error)
	}

	// starting/attached -> start is invalid, should map to conflict.
	resp2 := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-invalid",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         CommandSessionStart,
		Body:            body,
	})
	if resp2.OK {
		t.Fatalf("expected invalid transition error")
	}
	if resp2.Error == nil || resp2.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("expected conflict mapping, got %+v", resp2.Error)
	}
}

func TestSessionHandlerUnsupportedCommand(t *testing.T) {
	store := daemonstate.NewStore()
	h := NewSessionHandler(store)
	body, _ := json.Marshal(map[string]string{
		"project_id": "proj",
		"session_id": "s1",
	})
	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-x",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.recover",
		Body:            body,
	})
	if resp.OK {
		t.Fatalf("expected unsupported command error")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnsupportedCommand {
		t.Fatalf("error mapping = %+v", resp.Error)
	}
}

func TestSessionHandlerProjectIDFallbacks(t *testing.T) {
	t.Run("trims body project id", func(t *testing.T) {
		store := daemonstate.NewStore()
		h := NewSessionHandler(store)
		body, _ := json.Marshal(map[string]string{
			"project_id": "  proj-body  ",
			"session_id": "s-body",
			"issue_id":   "aey",
		})

		resp := h.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-body",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandSessionStart,
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("response = %+v", resp.Error)
		}
		if got := store.ReadSnapshot("proj-body"); got.Sessions["s-body"].State != daemonstate.SessionStateStarting {
			t.Fatalf("store state = %+v, want started in proj-body", got.Sessions["s-body"])
		}
	})

	t.Run("uses metadata project id when body is empty", func(t *testing.T) {
		store := daemonstate.NewStore()
		h := NewSessionHandler(store)
		body, _ := json.Marshal(map[string]string{
			"session_id": "s-meta",
			"issue_id":   "aey",
		})

		resp := h.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-meta",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandSessionStart,
			Meta:            protocol.Metadata{ProjectID: "  proj-meta  "},
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("response = %+v", resp.Error)
		}
		if got := store.ReadSnapshot("proj-meta"); got.Sessions["s-meta"].State != daemonstate.SessionStateStarting {
			t.Fatalf("store state = %+v, want started in proj-meta", got.Sessions["s-meta"])
		}
	})

	t.Run("defaults when project id is missing from body and metadata", func(t *testing.T) {
		store := daemonstate.NewStore()
		h := NewSessionHandler(store)
		body, _ := json.Marshal(map[string]string{
			"session_id": "s-default",
			"issue_id":   "aey",
		})

		resp := h.Handle(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-default",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         CommandSessionStart,
			Body:            body,
		})
		if !resp.OK {
			t.Fatalf("response = %+v", resp.Error)
		}
		if got := store.ReadSnapshot(protocol.DefaultProjectID); got.Sessions["s-default"].State != daemonstate.SessionStateStarting {
			t.Fatalf("store state = %+v, want started in default project", got.Sessions["s-default"])
		}
	})
}
