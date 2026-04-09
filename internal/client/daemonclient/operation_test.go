package daemonclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestOperationCommandsRouteThroughDaemon(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case protocol.CommandOperationGet:
				var body protocol.OperationGetRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal get body: %v", err)
				}
				if body.ProjectID != "proj-a" || body.OperationID != "op-1" {
					t.Fatalf("get body = %+v", body)
				}
				respBody, _ := json.Marshal(protocol.OperationGetResponseBody{
					Operation: protocol.OperationRecord{
						OperationID: "op-1",
							ProjectID:   naming.ProjectID("proj-a"),
						Kind:        "session.start",
						State:       protocol.OperationStateRunning,
					},
				})
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case protocol.CommandOperationList:
				var body protocol.OperationListRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal list body: %v", err)
				}
					if body.ProjectID != naming.ProjectID("proj-a") || body.IssueID != naming.IssueID("az-1") || body.Kind != "session.start" || body.Limit != 5 {
						t.Fatalf("list body = %+v", body)
					}
					respBody, _ := json.Marshal(protocol.OperationListResponseBody{
						ProjectID: naming.ProjectID("proj-a"),
						Operations: []protocol.OperationRecord{
							{
								OperationID: "op-1",
								ProjectID:   naming.ProjectID("proj-a"),
								Kind:        "session.start",
								State:       protocol.OperationStateQueued,
							},
						},
				})
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case protocol.CommandOperationCancel:
				var body protocol.OperationCancelRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal cancel body: %v", err)
				}
				if body.ProjectID != "proj-a" || body.OperationID != "op-1" || body.Reason != "user request" {
					t.Fatalf("cancel body = %+v", body)
				}
					respBody, _ := json.Marshal(protocol.OperationCancelResponseBody{
						Cancelled: true,
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   naming.ProjectID("proj-a"),
							Kind:        "session.start",
							State:       protocol.OperationStateCancelled,
						},
					})
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	client := New(transport).WithProjectID("proj-a")

	record, err := client.GetOperation(context.Background(), "op-1")
	if err != nil {
		t.Fatalf("GetOperation error: %v", err)
	}
	if record.OperationID != "op-1" || record.State != protocol.OperationStateRunning {
		t.Fatalf("GetOperation record = %+v", record)
	}

	records, err := client.ListOperations(context.Background(), OperationListOptions{
		IssueID: "az-1",
		Kind:    "session.start",
		States:  []protocol.OperationState{protocol.OperationStateQueued},
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("ListOperations error: %v", err)
	}
	if len(records) != 1 || records[0].OperationID != "op-1" {
		t.Fatalf("ListOperations records = %+v", records)
	}

	record, err = client.CancelOperation(context.Background(), "op-1", "user request")
	if err != nil {
		t.Fatalf("CancelOperation error: %v", err)
	}
	if record.State != protocol.OperationStateCancelled {
		t.Fatalf("CancelOperation state = %q, want cancelled", record.State)
	}
}

func TestWaitForOperationPollsUntilTerminalState(t *testing.T) {
	var calls int
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			calls++
			state := protocol.OperationStateQueued
			if calls > 1 {
				state = protocol.OperationStateDone
			}
			respBody, _ := json.Marshal(protocol.OperationGetResponseBody{
				Operation: protocol.OperationRecord{
					OperationID: "op-1",
						ProjectID:   naming.ProjectID("proj-a"),
					Kind:        "session.start",
					State:       state,
				},
			})
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}

	client := New(transport).WithProjectID("proj-a")
	record, err := client.WaitForOperation(context.Background(), "op-1", time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForOperation error: %v", err)
	}
	if record.State != protocol.OperationStateDone {
		t.Fatalf("final state = %q, want done", record.State)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want at least 2", calls)
	}
}
