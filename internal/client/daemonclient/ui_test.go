package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type uiRecordingTransport struct {
	lastReq protocol.RequestEnvelope
	replyFn func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
}

func (t *uiRecordingTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *uiRecordingTransport) Command(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	t.lastReq = req
	if t.replyFn != nil {
		return t.replyFn(req)
	}
	return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true}, nil
}

func (t *uiRecordingTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func TestOpenTaskWorkspaceRoutesThroughDaemon(t *testing.T) {
	createdAt := time.Date(2026, time.May, 5, 15, 40, 0, 0, time.UTC)
	transport := &uiRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandUIOpenTaskWorkspace {
				t.Fatalf("command = %q, want %q", req.Command, protocol.CommandUIOpenTaskWorkspace)
			}
			if req.Meta.ProjectID.String() != "proj-ui" {
				t.Fatalf("meta project_id = %q, want proj-ui", req.Meta.ProjectID.String())
			}
			var body protocol.UICommandRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			if body.Command != protocol.UICommandOpenTaskWorkspace || body.IssueID != "az-1" {
				t.Fatalf("request body = %+v", body)
			}
			respBody, err := json.Marshal(protocol.UICommandResponseBody{
				ProjectID: "proj-ui",
				IssueID:   "az-1",
				Command:   protocol.UICommandOpenTaskWorkspace,
				RequestID: "req-1",
				CreatedAt: createdAt,
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	client := New(transport).WithProjectID("proj-ui")

	resp, err := client.OpenTaskWorkspace(context.Background(), naming.IssueID("az-1"))
	if err != nil {
		t.Fatalf("OpenTaskWorkspace error: %v", err)
	}
	if resp.ProjectID != "proj-ui" || resp.IssueID != "az-1" || resp.Command != protocol.UICommandOpenTaskWorkspace {
		t.Fatalf("response = %+v", resp)
	}
	if resp.CreatedAt != createdAt {
		t.Fatalf("created_at = %s, want %s", resp.CreatedAt, createdAt)
	}
}
