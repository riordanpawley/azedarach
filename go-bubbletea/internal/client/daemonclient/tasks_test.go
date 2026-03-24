package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

type taskRecordingTransport struct {
	lastReq protocol.RequestEnvelope
	replyFn func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
}

func (t *taskRecordingTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *taskRecordingTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	_ = ctx
	t.lastReq = req
	if t.replyFn != nil {
		return t.replyFn(req)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
	}, nil
}

func (t *taskRecordingTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func TestTaskListCreateAndMutationCommands(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandTaskList {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskList)
				}
				tasks := []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}}
				body, err := json.Marshal(tasks)
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			},
		}

		client := New(transport)
		tasks, err := client.ListTasks(context.Background())
		if err != nil {
			t.Fatalf("ListTasks error: %v", err)
		}
		if len(tasks) != 1 || tasks[0].ID != "az-1" {
			t.Fatalf("tasks = %+v", tasks)
		}
	})

	t.Run("create", func(t *testing.T) {
		parentID := "epic-1"
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandTaskCreate {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskCreate)
				}
				var body TaskCreateParams
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Title != "Task 2" || body.ParentID == nil || *body.ParentID != parentID {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(TaskIDResponse{TaskID: "az-2"})
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

		client := New(transport)
		id, err := client.CreateTask(context.Background(), TaskCreateParams{
			Title:    "Task 2",
			Type:     domain.TypeTask,
			Priority: domain.P1,
			ParentID: &parentID,
		})
		if err != nil {
			t.Fatalf("CreateTask error: %v", err)
		}
		if id != "az-2" {
			t.Fatalf("task id = %q, want az-2", id)
		}
	})

	t.Run("status update", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandTaskUpdateStatus {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskUpdateStatus)
				}
				var body TaskStatusRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.TaskID != "az-3" || body.Status != domain.StatusDone {
					t.Fatalf("request body = %+v", body)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
				}, nil
			},
		}

		client := New(transport)
		if err := client.UpdateTaskStatus(context.Background(), "az-3", domain.StatusDone); err != nil {
			t.Fatalf("UpdateTaskStatus error: %v", err)
		}
	})

	t.Run("details update", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandTaskUpdate {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskUpdate)
				}
				var body struct {
					TaskID string `json:"task_id"`
					TaskUpdateParams
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.TaskID != "az-4" || body.Title != "Updated" {
					t.Fatalf("request body = %+v", body)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
				}, nil
			},
		}

		client := New(transport)
		if err := client.UpdateTaskDetails(context.Background(), "az-4", TaskUpdateParams{
			Title:    "Updated",
			Type:     domain.TypeBug,
			Priority: domain.P0,
		}); err != nil {
			t.Fatalf("UpdateTaskDetails error: %v", err)
		}
	})

	t.Run("delete and archive", func(t *testing.T) {
		commands := make([]string, 0, 2)
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				var body TaskIDRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.TaskID != "az-5" {
					t.Fatalf("request body = %+v", body)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
				}, nil
			},
		}

		client := New(transport)
		if err := client.DeleteTask(context.Background(), "az-5"); err != nil {
			t.Fatalf("DeleteTask error: %v", err)
		}
		if err := client.ArchiveTask(context.Background(), "az-5"); err != nil {
			t.Fatalf("ArchiveTask error: %v", err)
		}
		if len(commands) != 2 || commands[0] != CommandTaskDelete || commands[1] != CommandTaskArchive {
			t.Fatalf("commands = %v", commands)
		}
	})
}

func TestTaskCommandErrors(t *testing.T) {
	transport := &taskRecordingTransport{
		replyFn: func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{
				OK: false,
				Error: &protocol.ErrorEnvelope{
					Code:      protocol.ErrorCodeConflict,
					Message:   "busy",
					Retryable: false,
				},
			}, nil
		},
	}

	client := New(transport)
	err := client.DeleteTask(context.Background(), "az-1")
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected CommandError, got %T: %v", err, err)
	}
	if cmdErr.Code != protocol.ErrorCodeConflict {
		t.Fatalf("command error code = %q", cmdErr.Code)
	}
}
