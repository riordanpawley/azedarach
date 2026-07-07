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
			case protocol.CommandOperationQueue:
				var body protocol.OperationQueueRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal queue body: %v", err)
				}
				if body.ProjectID != naming.ProjectID("proj-a") || body.IssueID != naming.IssueID("az-1") || body.Kind != "session.start" || body.Limit != 5 {
					t.Fatalf("queue body = %+v", body)
				}
				respBody, _ := json.Marshal(protocol.OperationQueueResponseBody{
					ProjectID: naming.ProjectID("proj-a"),
					Queued: []protocol.OperationQueueEntry{{
						Operation: protocol.OperationRecord{
							OperationID: "op-2",
							ProjectID:   naming.ProjectID("proj-a"),
							Kind:        "session.start",
							State:       protocol.OperationStateQueued,
						},
						BlockingOperationIDs: []naming.OperationID{"op-1"},
						BlockedResourceKeys:  []string{"issue:proj-a:az-1"},
					}},
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

	queue, err := client.OperationQueue(context.Background(), OperationListOptions{
		IssueID: "az-1",
		Kind:    "session.start",
		States:  []protocol.OperationState{protocol.OperationStateQueued},
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("OperationQueue error: %v", err)
	}
	if len(queue.Queued) != 1 || queue.Queued[0].Operation.OperationID != "op-2" || len(queue.Queued[0].BlockingOperationIDs) != 1 {
		t.Fatalf("OperationQueue response = %+v", queue)
	}

	record, err = client.CancelOperation(context.Background(), "op-1", "user request")
	if err != nil {
		t.Fatalf("CancelOperation error: %v", err)
	}
	if record.State != protocol.OperationStateCancelled {
		t.Fatalf("CancelOperation state = %q, want cancelled", record.State)
	}
}

func TestSessionLifecycleOperationSubmitRoutesThroughDaemon(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandOperationSubmit {
				t.Fatalf("command = %q, want %q", req.Command, protocol.CommandOperationSubmit)
			}
			var body protocol.OperationSubmitRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal submit body: %v", err)
			}
			if body.ProjectID != naming.ProjectID("proj-a") || body.Kind != CommandSessionStop || body.IssueID != naming.IssueID("az-1") {
				t.Fatalf("submit body = %+v", body)
			}
			var payload sessionCommandBody
			if err := json.Unmarshal(body.Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if payload.ProjectID != naming.ProjectID("proj-a") || payload.SessionID != naming.SessionID("az-1") {
				t.Fatalf("payload = %+v", payload)
			}
			respBody, _ := json.Marshal(protocol.OperationSubmitResponseBody{
				Created: true,
				Operation: protocol.OperationRecord{
					OperationID: "op-stop",
					ProjectID:   naming.ProjectID("proj-a"),
					IssueID:     naming.IssueID("az-1"),
					Kind:        CommandSessionStop,
					State:       protocol.OperationStateQueued,
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
	record, err := client.StopSessionOperation(context.Background(), "az-1")
	if err != nil {
		t.Fatalf("StopSessionOperation error: %v", err)
	}
	if record.OperationID != "op-stop" || record.State != protocol.OperationStateQueued {
		t.Fatalf("record = %+v", record)
	}
}

func TestStartSessionOperationIncludesDedupeAndResourceKeys(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandOperationSubmit {
				t.Fatalf("command = %q, want %q", req.Command, protocol.CommandOperationSubmit)
			}
			var body protocol.OperationSubmitRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal submit body: %v", err)
			}
			if body.ProjectID != naming.ProjectID("proj-a") || body.Kind != CommandSessionStart || body.IssueID != naming.IssueID("az-1") {
				t.Fatalf("submit body = %+v", body)
			}
			if body.DedupeKey != "session.start:az-1" {
				t.Fatalf("dedupe key = %q", body.DedupeKey)
			}
			for _, want := range []string{
				"issue:proj-a:az-1",
				"worktree:az-1",
				"session:" + naming.CanonicalSessionID("/repo", "az-1"),
			} {
				if !containsString(body.ResourceKeys, want) {
					t.Fatalf("resource keys = %+v, want %s", body.ResourceKeys, want)
				}
			}
			var payload sessionCommandBody
			if err := json.Unmarshal(body.Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if payload.ProjectID != naming.ProjectID("proj-a") || payload.SessionID != naming.SessionID("az-1") || payload.BaseBranch != "main" {
				t.Fatalf("payload = %+v", payload)
			}
			if !payload.Yolo {
				t.Fatal("expected yolo=true in session.start operation payload")
			}
			if payload.StartWork == nil || *payload.StartWork {
				t.Fatalf("start_work = %v, want false", payload.StartWork)
			}
			if len(payload.ImagePaths) != 2 || payload.ImagePaths[0] != "/tmp/a.png" || payload.ImagePaths[1] != "/tmp/with space/image.png" {
				t.Fatalf("image_paths = %+v", payload.ImagePaths)
			}
			if payload.Prompt != "custom start prompt" {
				t.Fatalf("prompt = %q, want custom start prompt", payload.Prompt)
			}
			respBody, _ := json.Marshal(protocol.OperationSubmitResponseBody{
				Created: true,
				Operation: protocol.OperationRecord{
					OperationID: "op-start",
					ProjectID:   naming.ProjectID("proj-a"),
					IssueID:     naming.IssueID("az-1"),
					Kind:        CommandSessionStart,
					State:       protocol.OperationStateQueued,
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
	startWork := false
	record, err := client.StartSessionOperation(context.Background(), StartSessionParams{
		IssueID:    "az-1",
		RepoDir:    "/repo",
		BaseBranch: "main",
		Yolo:       true,
		StartWork:  &startWork,
		ImagePaths: []string{"/tmp/a.png", "/tmp/with space/image.png"},
		Prompt:     "custom start prompt",
	})
	if err != nil {
		t.Fatalf("StartSessionOperation error: %v", err)
	}
	if record.OperationID != "op-start" || record.State != protocol.OperationStateQueued {
		t.Fatalf("record = %+v", record)
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

func TestWaitForOperationReturnsLastRecordOnContextDeadline(t *testing.T) {
	transport := &lifecycleRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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
		},
	}

	client := New(transport).WithProjectID("proj-a")
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	record, err := client.WaitForOperation(ctx, "op-1", time.Hour)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForOperation error = %v, want context deadline", err)
	}
	if record.OperationID != "op-1" || record.State != protocol.OperationStateRunning {
		t.Fatalf("record = %+v, want last running op-1", record)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
