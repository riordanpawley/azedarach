package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

type taskRecordingTransport struct {
	lastReq protocol.RequestEnvelope
	replyFn func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
}

func assertTaskProjectID(t *testing.T, req protocol.RequestEnvelope, want string) {
	t.Helper()
	if req.Meta.ProjectID != want {
		t.Fatalf("project_id = %q, want %q", req.Meta.ProjectID, want)
	}
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

func mustMarshalTaskSnapshotPayload(t *testing.T, protocolVersion protocol.Version, projectID string, revision uint64, tasks []domain.Task) []byte {
	t.Helper()
	body, err := json.Marshal(protocol.TaskListSnapshotPayload{
		SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
		ProtocolVersion:  protocolVersion,
		SnapshotRevision: revision,
		ProjectID:        projectID,
		Tasks:            tasks,
	})
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}
	return body
}

func TestTaskListCreateAndMutationCommands(t *testing.T) {
	const wantProjectID = "proj-task"

	t.Run("list", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskList {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskList)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 0, []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		tasks, err := client.ListTasks(context.Background())
		if err != nil {
			t.Fatalf("ListTasks error: %v", err)
		}
		if len(tasks) != 1 || tasks[0].ID != "az-1" {
			t.Fatalf("tasks = %+v", tasks)
		}
	})

	t.Run("list snapshot", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskList {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskList)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        17,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 17, []domain.Task{{ID: "az-9", Title: "Task 9", Status: domain.StatusBlocked}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.ListTasksSnapshot(context.Background())
		if err != nil {
			t.Fatalf("ListTasksSnapshot error: %v", err)
		}
		if snapshot.Revision != 17 {
			t.Fatalf("revision = %d, want 17", snapshot.Revision)
		}
		if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID != "az-9" {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("list snapshot rejects legacy raw task array", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				body, err := json.Marshal([]domain.Task{{ID: "az-legacy", Title: "Legacy Task", Status: domain.StatusOpen}})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        23,
					OK:              true,
					Body:            body,
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		_, err := client.ListTasksSnapshot(context.Background())
		if err == nil {
			t.Fatal("expected decode error for legacy raw task array response")
		}
		if got := err.Error(); !strings.Contains(got, "decode task.list response") {
			t.Fatalf("error = %q, want decode task.list response", got)
		}
	})

	t.Run("create", func(t *testing.T) {
		parentID := "epic-1"
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
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

		client := New(transport).WithProjectID(wantProjectID)
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
				assertTaskProjectID(t, req, wantProjectID)
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

		client := New(transport).WithProjectID(wantProjectID)
		if err := client.UpdateTaskStatus(context.Background(), "az-3", domain.StatusDone); err != nil {
			t.Fatalf("UpdateTaskStatus error: %v", err)
		}
	})

	t.Run("status update pending operation", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskUpdateStatus {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskUpdateStatus)
				}
				respBody, err := json.Marshal(map[string]any{
					"operation_id": "op-status",
					"state":        string(protocol.OperationStateQueued),
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

		client := New(transport).WithProjectID(wantProjectID)
		err := client.UpdateTaskStatus(context.Background(), "az-3", domain.StatusDone)
		var pending *OperationPendingError
		if !errors.As(err, &pending) {
			t.Fatalf("UpdateTaskStatus error = %v, want OperationPendingError", err)
		}
		if pending.OperationID != "op-status" {
			t.Fatalf("operation id = %q, want op-status", pending.OperationID)
		}
		if pending.State != protocol.OperationStateQueued {
			t.Fatalf("state = %q, want queued", pending.State)
		}
	})

	t.Run("details update", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
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

		client := New(transport).WithProjectID(wantProjectID)
		if err := client.UpdateTaskDetails(context.Background(), "az-4", TaskUpdateParams{
			Title:    "Updated",
			Type:     domain.TypeBug,
			Priority: domain.P0,
		}); err != nil {
			t.Fatalf("UpdateTaskDetails error: %v", err)
		}
	})

	t.Run("append notes", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskAppendNotes {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskAppendNotes)
				}
				var body TaskAppendNotesRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.TaskID != "az-4" || body.Line != "📎 [img.png](.azedarach/images/az-4/img.png)" {
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

		client := New(transport).WithProjectID(wantProjectID)
		if err := client.AppendTaskNotes(context.Background(), "az-4", "📎 [img.png](.azedarach/images/az-4/img.png)"); err != nil {
			t.Fatalf("AppendTaskNotes error: %v", err)
		}
	})

	t.Run("delete and archive", func(t *testing.T) {
		commands := make([]string, 0, 2)
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
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

		client := New(transport).WithProjectID(wantProjectID)
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

	t.Run("dependency add and remove", func(t *testing.T) {
		commands := make([]string, 0, 2)
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				switch req.Command {
				case CommandTaskDependencyAdd:
					var body TaskDependencyParams
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal add request: %v", err)
					}
					if body.TaskID != "az-6" || body.DependsOnID != "az-1" || body.Type != "blocks" {
						t.Fatalf("add request body = %+v", body)
					}
				case CommandTaskDependencyRemove:
					var body TaskDependencyRemoveParams
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal remove request: %v", err)
					}
					if body.TaskID != "az-6" || body.DependsOnID != "az-1" || body.Type != "blocks" || !body.Confirm {
						t.Fatalf("remove request body = %+v", body)
					}
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		if err := client.AddTaskDependency(context.Background(), TaskDependencyParams{
			TaskID:      "az-6",
			DependsOnID: "az-1",
			Type:        "blocks",
		}); err != nil {
			t.Fatalf("AddTaskDependency error: %v", err)
		}
		if err := client.RemoveTaskDependency(context.Background(), TaskDependencyRemoveParams{
			TaskID:      "az-6",
			DependsOnID: "az-1",
			Type:        "blocks",
			Confirm:     true,
		}); err != nil {
			t.Fatalf("RemoveTaskDependency error: %v", err)
		}
		if len(commands) != 2 || commands[0] != CommandTaskDependencyAdd || commands[1] != CommandTaskDependencyRemove {
			t.Fatalf("commands = %v", commands)
		}
	})
}

func TestTaskCommandErrors(t *testing.T) {
	transport := &taskRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			assertTaskProjectID(t, req, "proj-task")
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

	client := New(transport).WithProjectID("proj-task")
	err := client.DeleteTask(context.Background(), "az-1")
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected CommandError, got %T: %v", err, err)
	}
	if cmdErr.Code != protocol.ErrorCodeConflict {
		t.Fatalf("command error code = %q", cmdErr.Code)
	}
}
