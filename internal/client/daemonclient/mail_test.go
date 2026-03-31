package daemonclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestMailCommands(t *testing.T) {
	const wantProjectID = "proj-mail"
	transport := &taskRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			assertTaskProjectID(t, req, wantProjectID)
			switch req.Command {
			case protocol.CommandMailSend:
				var body protocol.MailSendCommandBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal send body: %v", err)
				}
				respBody, err := json.Marshal(protocol.MailEvent{
					Seq:         1,
					ParentIssue: body.ParentIssue,
					Type:        body.Type,
					Body:        body.Body,
					CreatedAt:   "2026-01-01T00:00:00Z",
				})
				if err != nil {
					t.Fatalf("marshal send response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case protocol.CommandMailList, protocol.CommandMailWatch:
				respBody, err := json.Marshal([]protocol.MailEvent{{
					Seq:         1,
					ParentIssue: "az-parent",
					Type:        "handoff",
					Body:        "ready",
					CreatedAt:   "2026-01-01T00:00:00Z",
				}})
				if err != nil {
					t.Fatalf("marshal list/watch response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
				return protocol.ResponseEnvelope{}, nil
			}
		},
	}

	client := New(transport).WithProjectID(wantProjectID)
	event, err := client.MailSend(context.Background(), protocol.MailSendCommandBody{
		RepoDir:     "/tmp/repo",
		ParentIssue: "az-parent",
		Type:        "handoff",
		Body:        "ready",
	})
	if err != nil {
		t.Fatalf("MailSend error: %v", err)
	}
	if event.Seq != 1 || event.ParentIssue != "az-parent" {
		t.Fatalf("mail send event = %+v", event)
	}

	listed, err := client.MailList(context.Background(), protocol.MailListCommandBody{
		RepoDir:     "/tmp/repo",
		ParentIssue: "az-parent",
	})
	if err != nil {
		t.Fatalf("MailList error: %v", err)
	}
	if len(listed) != 1 || listed[0].Seq != 1 {
		t.Fatalf("mail list = %+v", listed)
	}

	watched, err := client.MailWatch(context.Background(), protocol.MailWatchCommandBody{
		RepoDir:     "/tmp/repo",
		ParentIssue: "az-parent",
		SinceSeq:    1,
	})
	if err != nil {
		t.Fatalf("MailWatch error: %v", err)
	}
	if len(watched) != 1 || watched[0].Type != "handoff" {
		t.Fatalf("mail watch = %+v", watched)
	}
}
