package daemonclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestNoticeCommandsRouteThroughDaemon(t *testing.T) {
	now := time.Now().UTC()
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case protocol.CommandNoticeList:
				var body protocol.NoticeListRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal list body: %v", err)
				}
				if body.ProjectID != naming.ProjectID("proj-a") || body.ScopeType != "issue" || body.ScopeID != "az-1" || body.Limit != 5 {
					t.Fatalf("list body = %+v", body)
				}
				respBody, _ := json.Marshal(protocol.NoticeListResponseBody{
					ProjectID: naming.ProjectID("proj-a"),
					Notices: []protocol.NoticeRecord{{
						NoticeID:         "notice-1",
						ProjectID:        naming.ProjectID("proj-a"),
						Scope:            protocol.NoticeScope{Type: "issue", ID: "az-1"},
						Severity:         protocol.NoticeSeverityError,
						Category:         "operation_failed",
						State:            protocol.NoticeStateActive,
						Title:            "Operation failed",
						Summary:          "Close failed",
						OccurrenceCount:  1,
						CreatedAt:        now,
						UpdatedAt:        now,
						RetentionClass:   protocol.NoticeRetentionError,
						LastOccurrenceAt: now,
					}},
				})
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case protocol.CommandNoticeGet:
				var body protocol.NoticeGetRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal get body: %v", err)
				}
				if body.ProjectID != naming.ProjectID("proj-a") || body.NoticeID != "notice-1" {
					t.Fatalf("get body = %+v", body)
				}
				respBody, _ := json.Marshal(protocol.NoticeGetResponseBody{Notice: protocol.NoticeRecord{NoticeID: "notice-1", ProjectID: naming.ProjectID("proj-a")}})
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case protocol.CommandNoticeUpdate:
				var body protocol.NoticeUpdateRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal update body: %v", err)
				}
				if body.ProjectID != naming.ProjectID("proj-a") || body.NoticeID != "notice-1" || body.Read == nil || !*body.Read || body.State != protocol.NoticeStateDismissed {
					t.Fatalf("update body = %+v", body)
				}
				respBody, _ := json.Marshal(protocol.NoticeUpdateResponseBody{Notice: protocol.NoticeRecord{NoticeID: "notice-1", ProjectID: naming.ProjectID("proj-a"), Read: true, State: protocol.NoticeStateDismissed}})
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case protocol.CommandNoticeAction:
				var body protocol.NoticeActionRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal action body: %v", err)
				}
				if body.ProjectID != naming.ProjectID("proj-a") || body.NoticeID != "notice-1" || body.ActionID != "open-task" || body.Input["task_id"] != "az-1" {
					t.Fatalf("action body = %+v", body)
				}
				respBody, _ := json.Marshal(protocol.NoticeActionResponseBody{Notice: protocol.NoticeRecord{NoticeID: "notice-1", ProjectID: naming.ProjectID("proj-a")}})
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	client := New(transport).WithProjectID("proj-a")
	notices, err := client.ListNotices(context.Background(), NoticeListOptions{
		States:    []protocol.NoticeState{protocol.NoticeStateActive},
		ScopeType: "issue",
		ScopeID:   "az-1",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("ListNotices error: %v", err)
	}
	if len(notices) != 1 || notices[0].NoticeID != "notice-1" {
		t.Fatalf("ListNotices notices = %+v", notices)
	}

	notice, err := client.GetNotice(context.Background(), "notice-1")
	if err != nil {
		t.Fatalf("GetNotice error: %v", err)
	}
	if notice.NoticeID != "notice-1" {
		t.Fatalf("GetNotice notice = %+v", notice)
	}

	read := true
	notice, err = client.UpdateNotice(context.Background(), "notice-1", &read, protocol.NoticeStateDismissed)
	if err != nil {
		t.Fatalf("UpdateNotice error: %v", err)
	}
	if !notice.Read || notice.State != protocol.NoticeStateDismissed {
		t.Fatalf("UpdateNotice notice = %+v", notice)
	}

	notice, err = client.RunNoticeAction(context.Background(), "notice-1", "open-task", map[string]string{"task_id": "az-1"})
	if err != nil {
		t.Fatalf("RunNoticeAction error: %v", err)
	}
	if notice.NoticeID != "notice-1" {
		t.Fatalf("RunNoticeAction notice = %+v", notice)
	}
}
