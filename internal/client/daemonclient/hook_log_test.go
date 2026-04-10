package daemonclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestHookLogCommands(t *testing.T) {
	const wantProjectID = "proj-hooks"
	now := time.Date(2026, time.April, 4, 9, 15, 0, 0, time.UTC)
	transport := &taskRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			assertTaskProjectID(t, req, wantProjectID)
			switch req.Command {
			case protocol.CommandHookLogAppend:
				var body protocol.HookLogAppendCommandBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal append body: %v", err)
				}
				body.Event.ProjectID = wantProjectID
				respBody, err := json.Marshal(body.Event)
				if err != nil {
					t.Fatalf("marshal append response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case protocol.CommandHookLogList:
				respBody, err := json.Marshal([]protocol.HookLogEvent{{
					ProjectID: wantProjectID,
					IssueID:   "az-123",
					Hook:      "post-commit",
					Worktree:  "/tmp/wt",
					Source:    "githooks.hook",
					Level:     "info",
					Message:   "refreshed daemon git state",
					CreatedAt: now,
				}})
				if err != nil {
					t.Fatalf("marshal list response: %v", err)
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
	appended, err := client.AppendHookLogEvent(context.Background(), protocol.HookLogEvent{
		IssueID:   "az-123",
		Hook:      "post-commit",
		Worktree:  "/tmp/wt",
		Source:    "githooks.hook",
		Level:     "info",
		Message:   "refreshed daemon git state",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("AppendHookLogEvent error: %v", err)
	}
	if appended.ProjectID != wantProjectID {
		t.Fatalf("append project_id = %q, want %q", appended.ProjectID, wantProjectID)
	}
	if appended.IssueID != "az-123" {
		t.Fatalf("append issue_id = %q, want az-123", appended.IssueID)
	}

	events, err := client.ListHookLogEvents(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListHookLogEvents error: %v", err)
	}
	if len(events) != 1 || events[0].Message != "refreshed daemon git state" {
		t.Fatalf("list events = %+v", events)
	}
	if events[0].IssueID != "az-123" {
		t.Fatalf("list issue_id = %q, want az-123", events[0].IssueID)
	}
}
