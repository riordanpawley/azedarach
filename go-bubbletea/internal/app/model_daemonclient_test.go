package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

type recordingDaemonTransport struct {
	calls            []string
	requests         []string
	lastHello        protocol.Hello
	subscribeProject string
	subscribeFrom    uint64
	replyFn          func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	subscribeFn      func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error)
}

func (r *recordingDaemonTransport) Handshake(_ context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	r.calls = append(r.calls, "handshake")
	r.lastHello = hello
	return protocol.HelloAck{Accepted: true}, nil
}

func (r *recordingDaemonTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	_ = ctx
	r.calls = append(r.calls, req.Command)
	r.requests = append(r.requests, req.Command)
	if r.replyFn != nil {
		return r.replyFn(req)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
	}, nil
}

func (r *recordingDaemonTransport) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	_ = ctx
	r.calls = append(r.calls, "subscribe")
	r.subscribeProject = projectID
	r.subscribeFrom = fromRevision
	if r.subscribeFn != nil {
		return r.subscribeFn(ctx, projectID, fromRevision)
	}
	return make(chan protocol.EventEnvelope), nil
}

func newDaemonTestModel(transport *recordingDaemonTransport) Model {
	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	return m
}

func TestTaskCommandsUseDaemonClient(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskList)
				}
				body, err := json.Marshal([]domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}})
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
		m := newDaemonTestModel(transport)

		msg := m.loadBeadsCmd()()
		loaded, ok := msg.(beadsLoadedMsg)
		if !ok {
			t.Fatalf("message type = %T, want beadsLoadedMsg", msg)
		}
		if len(loaded.tasks) != 1 || loaded.tasks[0].ID != "az-1" {
			t.Fatalf("loaded tasks = %+v", loaded.tasks)
		}
		if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandTaskList {
			t.Fatalf("requests = %v", transport.requests)
		}
	})

	t.Run("create and update", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskCreate:
					var body daemonclient.TaskCreateParams
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal create request: %v", err)
					}
					if body.Title != "New task" {
						t.Fatalf("create body = %+v", body)
					}
					respBody, _ := json.Marshal(daemonclient.TaskIDResponse{TaskID: "az-new"})
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandTaskUpdate:
					var body struct {
						TaskID string `json:"task_id"`
						daemonclient.TaskUpdateParams
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal update request: %v", err)
					}
					if body.TaskID != "az-1" || body.Title != "Edited" {
						t.Fatalf("update body = %+v", body)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}
		m := newDaemonTestModel(transport)

		createMsg := m.saveTaskCmd(overlay.TaskCreatedMsg{
			Title:    "New task",
			Type:     domain.TypeTask,
			Priority: domain.P1,
		})()
		created, ok := createMsg.(taskCreatedResultMsg)
		if !ok || created.taskID != "az-new" || created.err != nil {
			t.Fatalf("create result = %#v", createMsg)
		}

		updateMsg := m.saveTaskCmd(overlay.TaskCreatedMsg{
			ID:       "az-1",
			Title:    "Edited",
			Type:     domain.TypeBug,
			Priority: domain.P0,
		})()
		updated, ok := updateMsg.(taskCreatedResultMsg)
		if !ok || !updated.isUpdate || updated.err != nil {
			t.Fatalf("update result = %#v", updateMsg)
		}

		if len(transport.requests) != 2 || transport.requests[0] != daemonclient.CommandTaskCreate || transport.requests[1] != daemonclient.CommandTaskUpdate {
			t.Fatalf("requests = %v", transport.requests)
		}
	})

	t.Run("status and delete", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskUpdateStatus:
					var body daemonclient.TaskStatusRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal status request: %v", err)
					}
					if body.TaskID != "az-1" || body.Status != domain.StatusInProgress {
						t.Fatalf("status body = %+v", body)
					}
				case daemonclient.CommandTaskDelete:
					var body daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal delete request: %v", err)
					}
					if body.TaskID != "az-2" {
						t.Fatalf("delete body = %+v", body)
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
		m := newDaemonTestModel(transport)

		statusMsg := m.moveTaskStatusCmd("az-1", 1)()
		status, ok := statusMsg.(taskStatusResultMsg)
		if !ok || status.newStatus != domain.StatusInProgress || status.err != nil {
			t.Fatalf("status result = %#v", statusMsg)
		}

		deleteMsg := m.deleteTaskCmd("az-2")()
		deleted, ok := deleteMsg.(taskDeletedResultMsg)
		if !ok || deleted.taskID != "az-2" || deleted.err != nil {
			t.Fatalf("delete result = %#v", deleteMsg)
		}

		if len(transport.requests) != 2 || transport.requests[0] != daemonclient.CommandTaskUpdateStatus || transport.requests[1] != daemonclient.CommandTaskDelete {
			t.Fatalf("requests = %v", transport.requests)
		}
	})
}

func TestDaemonAttachFlowUsesHandshakeSnapshotSubscribe(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskList {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskList)
			}
			body, err := json.Marshal([]domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Revision:        8,
				OK:              true,
				Body:            body,
			}, nil
		},
		subscribeFn: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
			ch := make(chan protocol.EventEnvelope, 1)
			ch <- protocol.EventEnvelope{Revision: 9, Event: "task.updated"}
			return ch, nil
		},
	}
	m := newDaemonTestModel(transport)
	m.currentProject = "proj"

	msg := m.attachDaemonCmd()()
	loaded, ok := msg.(beadsLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want beadsLoadedMsg", msg)
	}
	if len(loaded.tasks) != 1 || loaded.tasks[0].ID != "az-1" {
		t.Fatalf("loaded tasks = %+v", loaded.tasks)
	}
	if loaded.revision != 8 {
		t.Fatalf("loaded revision = %d, want 8", loaded.revision)
	}
	if loaded.events == nil {
		t.Fatal("expected daemon event subscription channel")
	}
	if got := transport.calls; len(got) != 3 || got[0] != "handshake" || got[1] != daemonclient.CommandTaskList || got[2] != "subscribe" {
		t.Fatalf("calls = %v", got)
	}
	if transport.lastHello.ClientName != "tui" || transport.lastHello.ClientVersion != "dev" || transport.lastHello.ProtocolVersion != protocol.CurrentVersion {
		t.Fatalf("hello = %+v", transport.lastHello)
	}
	if transport.subscribeProject != "proj" {
		t.Fatalf("subscribe project = %q, want proj", transport.subscribeProject)
	}
	if transport.subscribeFrom != 8 {
		t.Fatalf("subscribe from revision = %d, want 8", transport.subscribeFrom)
	}

	m.daemonEvents = loaded.events
	eventMsg := m.waitForDaemonEventCmd()()
	evt, ok := eventMsg.(daemonStreamEventMsg)
	if !ok {
		t.Fatalf("event message type = %T, want daemonStreamEventMsg", eventMsg)
	}
	if evt.event.Revision != 9 || evt.event.Event != "task.updated" {
		t.Fatalf("event = %+v", evt.event)
	}
}

func TestPerformCleanupRoutesDaemonCleanupAndPreservesCounts(t *testing.T) {
	base := time.Date(2026, time.March, 24, 12, 0, 0, 0, time.UTC)
	oldSessionStart := base.Add(-48 * time.Hour)

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandTaskDelete:
				var body daemonclient.TaskIDRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal delete request: %v", err)
				}
				if body.TaskID != "az-old" {
					t.Fatalf("delete body = %+v", body)
				}
			case daemonclient.CommandTaskArchive:
				var body daemonclient.TaskIDRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal archive request: %v", err)
				}
				if body.TaskID != "az-old" && body.TaskID != "az-recent" {
					t.Fatalf("archive body = %+v", body)
				}
			case protocol.CommandWorktreeCleanupOrphaned:
				var body protocol.CleanupOrphanedRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal cleanup request: %v", err)
				}
				if body.ProjectID != "proj-1" {
					t.Fatalf("cleanup body = %+v", body)
				}
				respBody, err := json.Marshal(protocol.CleanupOrphanedResponseBody{
					ProjectID:        body.ProjectID,
					WorktreesRemoved: 2,
				})
				if err != nil {
					t.Fatalf("marshal cleanup response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandSessionStop:
				var body struct {
					ProjectID string `json:"project_id"`
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal session stop request: %v", err)
				}
				if body.SessionID != "bead-1" {
					t.Fatalf("session stop body = %+v", body)
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

	m := newDaemonTestModel(transport)
	m.currentProject = "proj-1"
	m.daemonClient.WithProjectID(m.daemonProjectID())
	m.tasks = []domain.Task{
		{ID: "az-old", Status: domain.StatusDone, UpdatedAt: base.AddDate(0, 0, -31)},
		{ID: "az-recent", Status: domain.StatusDone, UpdatedAt: base},
		{ID: "az-open", Status: domain.StatusOpen, UpdatedAt: base},
	}
	m.sessions = map[string]*domain.Session{
		"bead-1": &domain.Session{BeadID: "bead-1", State: domain.SessionPaused, StartedAt: &oldSessionStart},
		"bead-2": &domain.Session{BeadID: "bead-2", State: domain.SessionBusy, StartedAt: &oldSessionStart},
	}

	result, err := m.performCleanup(context.Background(), []string{
		"delete_old_done",
		"archive_done",
		"remove_orphaned_worktrees",
		"clean_stale_sessions",
	})
	if err != nil {
		t.Fatalf("performCleanup error: %v", err)
	}

	if result.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.Deleted)
	}
	if result.Archived != 2 {
		t.Fatalf("archived = %d, want 2", result.Archived)
	}
	if result.WorktreesRemoved != 2 {
		t.Fatalf("worktrees removed = %d, want 2", result.WorktreesRemoved)
	}
	if result.SessionsCleaned != 1 {
		t.Fatalf("sessions cleaned = %d, want 1", result.SessionsCleaned)
	}

	if got := transport.requests; len(got) != 5 {
		t.Fatalf("requests = %v", got)
	}
	if transport.requests[0] != daemonclient.CommandTaskDelete ||
		transport.requests[1] != daemonclient.CommandTaskArchive ||
		transport.requests[2] != daemonclient.CommandTaskArchive ||
		transport.requests[3] != protocol.CommandWorktreeCleanupOrphaned ||
		transport.requests[4] != daemonclient.CommandSessionStop {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestDaemonEventRevisionReducer(t *testing.T) {
	tests := []struct {
		name         string
		current      uint64
		revision     uint64
		wantAction   daemonEventDecision
		wantRevision uint64
	}{
		{
			name:         "duplicate",
			current:      4,
			revision:     4,
			wantAction:   daemonEventIgnore,
			wantRevision: 4,
		},
		{
			name:         "out_of_order",
			current:      4,
			revision:     3,
			wantAction:   daemonEventIgnore,
			wantRevision: 4,
		},
		{
			name:         "sequential",
			current:      4,
			revision:     5,
			wantAction:   daemonEventRefreshSnapshot,
			wantRevision: 5,
		},
		{
			name:         "gap",
			current:      4,
			revision:     7,
			wantAction:   daemonEventRehydrate,
			wantRevision: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m.daemonRevision = tt.current
			m.daemonEvents = make(chan protocol.EventEnvelope)

			gotAction := m.reduceDaemonEvent(protocol.EventEnvelope{Revision: tt.revision})
			if gotAction != tt.wantAction {
				t.Fatalf("action = %v, want %v", gotAction, tt.wantAction)
			}

			updated, _ := m.Update(daemonStreamEventMsg{event: protocol.EventEnvelope{Revision: tt.revision}})
			next, ok := updated.(Model)
			if !ok {
				t.Fatalf("updated model type = %T, want Model", updated)
			}
			if next.daemonRevision != tt.wantRevision {
				t.Fatalf("model revision = %d, want %d", next.daemonRevision, tt.wantRevision)
			}
		})
	}
}

func TestBulkTaskCommandsUseDaemonClient(t *testing.T) {
	t.Run("bulk status delete archive", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskUpdateStatus:
					var body daemonclient.TaskStatusRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal status request: %v", err)
					}
				case daemonclient.CommandTaskDelete, daemonclient.CommandTaskArchive:
					var body daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task id request: %v", err)
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
		m := newDaemonTestModel(transport)
		m.tasks = []domain.Task{
			{ID: "az-1", Status: domain.StatusOpen},
			{ID: "az-2", Status: domain.StatusOpen},
		}

		statusMsg := m.bulkSetStatusCmd([]string{"az-1", "az-2"}, domain.StatusDone)()
		status, ok := statusMsg.(bulkStatusResultMsg)
		if !ok || status.updated != 2 || status.failed != 0 {
			t.Fatalf("bulk status result = %#v", statusMsg)
		}

		deleteMsg := m.bulkDeleteCmd([]string{"az-1", "az-2"})()
		deleted, ok := deleteMsg.(bulkStatusResultMsg)
		if !ok || deleted.updated != 2 || deleted.failed != 0 {
			t.Fatalf("bulk delete result = %#v", deleteMsg)
		}

		archiveMsg := m.bulkArchiveCmd([]string{"az-1"})()
		archived, ok := archiveMsg.(bulkStatusResultMsg)
		if !ok || archived.updated != 1 || archived.failed != 0 {
			t.Fatalf("bulk archive result = %#v", archiveMsg)
		}

		if got := transport.requests; len(got) != 5 {
			t.Fatalf("requests = %v", got)
		}
		if transport.requests[0] != daemonclient.CommandTaskUpdateStatus ||
			transport.requests[1] != daemonclient.CommandTaskUpdateStatus ||
			transport.requests[2] != daemonclient.CommandTaskDelete ||
			transport.requests[3] != daemonclient.CommandTaskDelete ||
			transport.requests[4] != daemonclient.CommandTaskArchive {
			t.Fatalf("requests = %v", transport.requests)
		}
	})
}
