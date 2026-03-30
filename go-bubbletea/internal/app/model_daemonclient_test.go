package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/pr"
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

type recordingCommandRunner struct {
	calls  [][]string
	output string
	err    error
}

type blockingSnapshotTransport struct{}

func (blockingSnapshotTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (blockingSnapshotTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	<-ctx.Done()
	return protocol.ResponseEnvelope{}, ctx.Err()
}

func (blockingSnapshotTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func (r *recordingCommandRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return r.output, r.err
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

		msg := m.loadIssuesCmd()()
		loaded, ok := msg.(issuesLoadedMsg)
		if !ok {
			t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
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

	t.Run("status and archive", func(t *testing.T) {
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
				case daemonclient.CommandTaskArchive:
					var body daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal archive request: %v", err)
					}
					if body.TaskID != "az-2" {
						t.Fatalf("archive body = %+v", body)
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

		statusMsg := m.moveTaskStatusCmd("az-1", domain.StatusOpen, domain.StatusInProgress)()
		status, ok := statusMsg.(taskStatusResultMsg)
		if !ok || status.newStatus != domain.StatusInProgress || status.err != nil {
			t.Fatalf("status result = %#v", statusMsg)
		}

		deleteMsg := m.deleteTaskCmd("az-2")()
		deleted, ok := deleteMsg.(taskDeletedResultMsg)
		if !ok || deleted.taskID != "az-2" || deleted.err != nil {
			t.Fatalf("archive result = %#v", deleteMsg)
		}

		if len(transport.requests) != 2 || transport.requests[0] != daemonclient.CommandTaskUpdateStatus || transport.requests[1] != daemonclient.CommandTaskArchive {
			t.Fatalf("requests = %v", transport.requests)
		}
	})
}

func TestTaskStatusMovePendingKeepsOptimisticOverlayAcrossHydration(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskUpdateStatus {
				t.Fatalf("unexpected command: %s", req.Command)
			}
			respBody, _ := json.Marshal(map[string]any{
				"operation_id": "op-status",
				"state":        string(protocol.OperationStateQueued),
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

	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{ID: "az-1", Status: domain.StatusOpen}}
	m.nav.SelectTask("az-1", 0)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "l"})
	if cmd == nil {
		t.Fatal("expected task move command")
	}
	optimistic := updated.(Model)
	if optimistic.tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("optimistic status = %s, want %s", optimistic.tasks[0].Status, domain.StatusInProgress)
	}

	result := cmd()
	updatedAfterResult, refreshCmd := optimistic.Update(result)
	if refreshCmd == nil {
		t.Fatal("expected refresh command for pending status move")
	}
	pendingModel := updatedAfterResult.(Model)
	if pendingModel.tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("pending status = %s, want %s", pendingModel.tasks[0].Status, domain.StatusInProgress)
	}
	if len(pendingModel.toasts) == 0 {
		t.Fatal("expected pending toast")
	}
	pendingToast := pendingModel.toasts[len(pendingModel.toasts)-1].Message
	if !strings.Contains(pendingToast, "Task move queued for az-1 (operation op-status)") {
		t.Fatalf("toast = %q, want pending task move message", pendingToast)
	}

	staleHydration, _ := pendingModel.Update(issuesLoadedMsg{
		tasks: []domain.Task{{ID: "az-1", Status: domain.StatusOpen}},
	})
	staleModel := staleHydration.(Model)
	if staleModel.tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("stale hydration status = %s, want optimistic %s", staleModel.tasks[0].Status, domain.StatusInProgress)
	}

	confirmedHydration, _ := staleModel.Update(issuesLoadedMsg{
		tasks: []domain.Task{{ID: "az-1", Status: domain.StatusInProgress}},
	})
	confirmedModel := confirmedHydration.(Model)
	if confirmedModel.tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("confirmed hydration status = %s, want %s", confirmedModel.tasks[0].Status, domain.StatusInProgress)
	}

	postConfirmHydration, _ := confirmedModel.Update(issuesLoadedMsg{
		tasks: []domain.Task{{ID: "az-1", Status: domain.StatusOpen}},
	})
	postConfirmModel := postConfirmHydration.(Model)
	if postConfirmModel.tasks[0].Status != domain.StatusOpen {
		t.Fatalf("post-confirm hydration status = %s, want %s after clearing overlay", postConfirmModel.tasks[0].Status, domain.StatusOpen)
	}
}

func TestTaskStatusMoveFailureRollsBackOptimisticState(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskUpdateStatus {
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, io.ErrUnexpectedEOF
		},
	}

	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{ID: "az-1", Status: domain.StatusOpen}}
	m.nav.SelectTask("az-1", 0)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "l"})
	if cmd == nil {
		t.Fatal("expected task move command")
	}
	optimistic := updated.(Model)
	if optimistic.tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("optimistic status = %s, want %s", optimistic.tasks[0].Status, domain.StatusInProgress)
	}

	result := cmd()
	rolledBack, refreshCmd := optimistic.Update(result)
	if refreshCmd != nil {
		t.Fatal("unexpected refresh command for terminal task move failure")
	}
	rolledBackModel := rolledBack.(Model)
	if rolledBackModel.tasks[0].Status != domain.StatusOpen {
		t.Fatalf("rolled back status = %s, want %s", rolledBackModel.tasks[0].Status, domain.StatusOpen)
	}
	if len(rolledBackModel.toasts) == 0 {
		t.Fatal("expected error toast")
	}
	lastToast := rolledBackModel.toasts[len(rolledBackModel.toasts)-1].Message
	if !strings.Contains(lastToast, "Failed to update task") {
		t.Fatalf("toast = %q, want update failure message", lastToast)
	}
}

func TestDaemonCommandsReportMissingDaemonClient(t *testing.T) {
	m := newTestModel()
	m.daemonClient = nil

	if msg := m.loadIssuesCmd()(); msg == nil {
		t.Fatal("loadIssuesCmd returned nil message")
	} else if errMsg, ok := msg.(issuesErrorMsg); !ok {
		t.Fatalf("loadIssuesCmd message type = %T, want issuesErrorMsg", msg)
	} else if errMsg.err == nil || errMsg.err.Error() != "daemon client unavailable" {
		t.Fatalf("loadIssuesCmd error = %v, want daemon client unavailable", errMsg.err)
	}

	if msg := m.attachDaemonCmd()(); msg == nil {
		t.Fatal("attachDaemonCmd returned nil message")
	} else if errMsg, ok := msg.(issuesErrorMsg); !ok {
		t.Fatalf("attachDaemonCmd message type = %T, want issuesErrorMsg", msg)
	} else if errMsg.err == nil || errMsg.err.Error() != "daemon client unavailable" {
		t.Fatalf("attachDaemonCmd error = %v, want daemon client unavailable", errMsg.err)
	}

	if msg := m.abortMergeCmd("/tmp/az-1")(); msg == nil {
		t.Fatal("abortMergeCmd returned nil message")
	} else if abortMsg, ok := msg.(abortMergeResultMsg); !ok {
		t.Fatalf("abortMergeCmd message type = %T, want abortMergeResultMsg", msg)
	} else if abortMsg.err == nil || abortMsg.err.Error() != "daemon client unavailable" {
		t.Fatalf("abortMergeCmd error = %v, want daemon client unavailable", abortMsg.err)
	}
}

func TestLoadIssuesCmdTimeoutReturnsStaleIssuesMsg(t *testing.T) {
	transport := blockingSnapshotTransport{}
	m := newTestModel()
	m.currentProject = "proj-read"
	m.daemonClient = daemonclient.New(&transport).WithProjectID("proj-read").WithReadWaitPolicy(daemonclient.ReadWaitPolicy{
		Default:  1 * time.Nanosecond,
		Explicit: 2 * time.Nanosecond,
	})
	m.tasks = []domain.Task{{ID: "az-1", Title: "Existing", Status: domain.StatusOpen}}

	msg := m.loadIssuesCmd()()
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
	}
	if !loaded.stale {
		t.Fatal("expected stale issuesLoadedMsg on read timeout")
	}
	if loaded.freshnessHint == "" {
		t.Fatal("expected freshness hint on read timeout")
	}

	updated, _ := m.Update(loaded)
	newModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if len(newModel.tasks) != 1 || newModel.tasks[0].ID != "az-1" {
		t.Fatalf("tasks = %+v, want existing board state preserved", newModel.tasks)
	}
	if !newModel.hasRefreshLoop {
		t.Fatal("expected refresh loop to start after stale read timeout")
	}
	if len(newModel.toasts) == 0 || !strings.Contains(newModel.toasts[len(newModel.toasts)-1].Message, "local-first data") {
		t.Fatalf("toasts = %+v, want freshness warning", newModel.toasts)
	}
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
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
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

func TestBranchBehindMsgAttachesWhenCaughtUp(t *testing.T) {
	t.Setenv("TMUX", "")
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionAttach {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionAttach)
			}
			var body struct {
				ProjectID string `json:"project_id"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal attach request: %v", err)
			}
			if body.SessionID != "az-1" {
				t.Fatalf("attach session = %q, want az-1", body.SessionID)
			}
			respBody, err := json.Marshal(struct {
				Output string `json:"output"`
			}{Output: "attached"})
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
	m := newDaemonTestModel(transport)

	updated, cmd := m.Update(branchBehindMsg{
		issueID:       "az-1",
		worktree:      "/tmp/az-1",
		commitsBehind: 0,
	})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected attach command")
	}

	msg := cmd()
	attached, ok := msg.(sessionAttachedMsg)
	if !ok {
		t.Fatalf("message type = %T, want sessionAttachedMsg", msg)
	}
	if attached.issueID != "az-1" {
		t.Fatalf("attached issue = %q, want az-1", attached.issueID)
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionAttach {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestMergeAttachSelectionAttachesAfterMerge(t *testing.T) {
	t.Setenv("TMUX", "")
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandGitFetch:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal fetch request: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Remote != "origin" {
					t.Fatalf("fetch body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{Worktree: body.Worktree, Remote: body.Remote})
				if err != nil {
					t.Fatalf("marshal fetch response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMerge:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal merge request: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Branch != "origin/main" {
					t.Fatalf("merge body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitMergeCommandResponse{
					Worktree: body.Worktree,
					Branch:   body.Branch,
					Result:   git.MergeResult{Success: true},
				})
				if err != nil {
					t.Fatalf("marshal merge response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandSessionAttach:
				var body struct {
					ProjectID string `json:"project_id"`
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal attach request: %v", err)
				}
				if body.SessionID != "az-1" {
					t.Fatalf("attach body = %+v", body)
				}
				respBody, err := json.Marshal(struct {
					Output string `json:"output"`
				}{Output: "attached"})
				if err != nil {
					t.Fatalf("marshal attach response: %v", err)
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
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.sessions["az-1"] = &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionBusy,
		Worktree: "/tmp/az-1",
	}
	m.config.Git.BaseBranch = "main"

	updated, cmd := m.Update(overlay.SelectionMsg{
		Key:   "merge_attach",
		Value: "az-1",
	})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected merge command")
	}

	msg := cmd()
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	nextModel, cmd2 := next.Update(msg)
	if _, ok := nextModel.(Model); !ok {
		t.Fatalf("next model type = %T, want Model", nextModel)
	}
	if cmd2 == nil {
		t.Fatal("expected attach command after merge")
	}

	attached := cmd2()
	attachedMsg, ok := attached.(sessionAttachedMsg)
	if !ok {
		t.Fatalf("attached message type = %T, want sessionAttachedMsg", attached)
	}
	if attachedMsg.issueID != "az-1" {
		t.Fatalf("attached issue = %q, want az-1", attachedMsg.issueID)
	}
	if got := transport.requests; len(got) != 3 || got[0] != daemonclient.CommandGitFetch || got[1] != daemonclient.CommandGitMerge || got[2] != daemonclient.CommandSessionAttach {
		t.Fatalf("requests = %v", got)
	}
}

func TestFollowOnMergeCandidateOrderingAndEligibility(t *testing.T) {
	parentID := "az-parent"
	blockerID := "az-blocker"
	nonReadyID := "az-open"

	m := newTestModel()
	m.tasks = []domain.Task{
		{
			ID:       "az-child",
			Title:    "Child task",
			Status:   domain.StatusInProgress,
			Type:     domain.TypeTask,
			ParentID: &parentID,
			Dependencies: []domain.Dependency{
				{ID: blockerID, Type: domain.DependencyBlocks},
				{ID: nonReadyID, Type: domain.DependencyBlocks},
			},
		},
		{ID: parentID, Title: "Parent epic", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: blockerID, Title: "Ready blocker", Status: domain.StatusDone, Type: domain.TypeTask},
		{ID: nonReadyID, Title: "Non-ready blocker", Status: domain.StatusOpen, Type: domain.TypeTask},
	}
	m.sessions[parentID] = &domain.Session{IssueID: parentID, State: domain.SessionBusy, Worktree: "/tmp/parent"}
	m.sessions[blockerID] = &domain.Session{IssueID: blockerID, State: domain.SessionBusy, Worktree: "/tmp/blocker"}
	m.sessions[nonReadyID] = &domain.Session{IssueID: nonReadyID, State: domain.SessionBusy, Worktree: "/tmp/open"}

	candidates := m.getFollowOnMergeCandidates(&m.tasks[0])
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(candidates))
	}
	if candidates[0].target.ID != parentID {
		t.Fatalf("first candidate = %s, want %s", candidates[0].target.ID, parentID)
	}
	if candidates[1].target.ID != blockerID {
		t.Fatalf("second candidate = %s, want %s", candidates[1].target.ID, blockerID)
	}
	for _, candidate := range candidates {
		if candidate.target.ID == nonReadyID {
			t.Fatalf("non-ready candidate %s should have been excluded", nonReadyID)
		}
	}
}

func TestFollowOnMergeSelectionDirectMergeFromPausedTarget(t *testing.T) {
	parentID := "az-parent"
	childID := "az-child"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				respBody, err := json.Marshal(struct {
					ProjectID string `json:"project_id"`
					Worktrees []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					} `json:"worktrees"`
				}{
					ProjectID: "default",
					Worktrees: []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					}{
						{Path: "/tmp/parent", Branch: "az/az-parent", IssueID: parentID},
					},
				})
				if err != nil {
					t.Fatalf("marshal worktree response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitStatus:
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{Status: git.GitStatus{HasChanges: false}})
				if err != nil {
					t.Fatalf("marshal status response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMerge:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal merge request: %v", err)
				}
				if body.Worktree != "/tmp/child" || body.Branch != "az/az-parent" {
					t.Fatalf("merge body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitMergeCommandResponse{
					Worktree: body.Worktree,
					Branch:   body.Branch,
					Result:   git.MergeResult{Success: true},
				})
				if err != nil {
					t.Fatalf("marshal merge response: %v", err)
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
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}
	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)

	m.tasks = []domain.Task{
		{
			ID:       childID,
			Title:    "Child task",
			Status:   domain.StatusInProgress,
			Type:     domain.TypeTask,
			ParentID: &parentID,
		},
		{
			ID:     parentID,
			Title:  "Parent epic",
			Status: domain.StatusInProgress,
			Type:   domain.TypeEpic,
		},
	}
	m.sessions[childID] = &domain.Session{IssueID: childID, State: domain.SessionPaused, Worktree: "/tmp/child"}
	m.sessions[parentID] = &domain.Session{IssueID: parentID, State: domain.SessionBusy, Worktree: "/tmp/parent"}
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "m"})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected direct follow-on merge command")
	}

	msg := cmd()
	mergeMsg, ok := msg.(mergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergeResultMsg", msg)
	}
	if mergeMsg.sourceID != parentID || mergeMsg.targetID != childID {
		t.Fatalf("merge result = %+v, want source=%s target=%s", mergeMsg, parentID, childID)
	}
	if mergeMsg.err != nil {
		t.Fatalf("merge err = %v", mergeMsg.err)
	}
	if got := transport.requests; len(got) != 4 || got[0] != daemonclient.CommandWorktreeList || got[1] != daemonclient.CommandGitStatus || got[2] != daemonclient.CommandGitStatus || got[3] != daemonclient.CommandGitMerge {
		t.Fatalf("requests = %v", got)
	}
}

func TestFollowOnMergeSelectionBusyOrWaitingStopsBeforeMerge(t *testing.T) {
	tests := []struct {
		name  string
		state domain.SessionState
	}{
		{name: "busy session", state: domain.SessionBusy},
		{name: "waiting session", state: domain.SessionWaiting},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parentID := "az-parent"
			childID := "az-child"

			transport := &recordingDaemonTransport{
				replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					switch req.Command {
					case daemonclient.CommandWorktreeList:
						respBody, err := json.Marshal(struct {
							ProjectID string `json:"project_id"`
							Worktrees []struct {
								Path    string `json:"path"`
								Branch  string `json:"branch"`
								IssueID string `json:"issue_id"`
							} `json:"worktrees"`
						}{
							ProjectID: "default",
							Worktrees: []struct {
								Path    string `json:"path"`
								Branch  string `json:"branch"`
								IssueID string `json:"issue_id"`
							}{
								{Path: "/tmp/parent", Branch: "az/az-parent", IssueID: parentID},
							},
						})
						if err != nil {
							t.Fatalf("marshal worktree response: %v", err)
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
						if body.SessionID != childID {
							t.Fatalf("session stop body = %+v, want session_id=%s", body, childID)
						}
						respBody, err := json.Marshal(struct {
							Output string `json:"output"`
						}{Output: "stopped"})
						if err != nil {
							t.Fatalf("marshal session stop response: %v", err)
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							OK:              true,
							Body:            respBody,
						}, nil
					case daemonclient.CommandGitStatus:
						respBody, err := json.Marshal(struct {
							Status git.GitStatus `json:"status"`
						}{Status: git.GitStatus{HasChanges: false}})
						if err != nil {
							t.Fatalf("marshal status response: %v", err)
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							OK:              true,
							Body:            respBody,
						}, nil
					case daemonclient.CommandGitMerge:
						var body daemonclient.GitCommandRequest
						if err := json.Unmarshal(req.Body, &body); err != nil {
							t.Fatalf("unmarshal merge request: %v", err)
						}
						if body.Worktree != "/tmp/child" || body.Branch != "az/az-parent" {
							t.Fatalf("merge body = %+v", body)
						}
						respBody, err := json.Marshal(daemonclient.GitMergeCommandResponse{
							Worktree: body.Worktree,
							Branch:   body.Branch,
							Result:   git.MergeResult{Success: true},
						})
						if err != nil {
							t.Fatalf("marshal merge response: %v", err)
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
					}
					return protocol.ResponseEnvelope{}, nil
				},
			}

			m := newTestModel()
			m.daemonClient = daemonclient.New(transport)
			m.tasks = []domain.Task{
				{
					ID:       childID,
					Title:    "Child task",
					Status:   domain.StatusInProgress,
					Type:     domain.TypeTask,
					ParentID: &parentID,
				},
				{
					ID:     parentID,
					Title:  "Parent epic",
					Status: domain.StatusInProgress,
					Type:   domain.TypeEpic,
				},
			}
			m.sessions[childID] = &domain.Session{IssueID: childID, State: tt.state, Worktree: "/tmp/child"}
			m.sessions[parentID] = &domain.Session{IssueID: parentID, State: domain.SessionBusy, Worktree: "/tmp/parent"}
			m.nav.SelectTask(childID, 1)

			updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "m"})
			if _, ok := updated.(Model); !ok {
				t.Fatalf("updated model type = %T, want Model", updated)
			}
			if cmd == nil {
				t.Fatal("expected follow-on merge command")
			}

			msg := cmd()
			mergeMsg, ok := msg.(mergeResultMsg)
			if !ok {
				t.Fatalf("message type = %T, want mergeResultMsg", msg)
			}
			if mergeMsg.sourceID != parentID || mergeMsg.targetID != childID {
				t.Fatalf("merge result = %+v, want source=%s target=%s", mergeMsg, parentID, childID)
			}
			if mergeMsg.err != nil {
				t.Fatalf("merge err = %v", mergeMsg.err)
			}
			if got := transport.requests; len(got) != 5 || got[0] != daemonclient.CommandSessionStop || got[1] != daemonclient.CommandWorktreeList || got[2] != daemonclient.CommandGitStatus || got[3] != daemonclient.CommandGitStatus || got[4] != daemonclient.CommandGitMerge {
				t.Fatalf("requests = %v", got)
			}
		})
	}
}

func TestFollowOnMergeSelectionUsesDaemonSnapshotStateWhenProjectionMissing(t *testing.T) {
	parentID := "az-parent"
	childID := "az-child"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				respBody, err := json.Marshal(struct {
					ProjectID string `json:"project_id"`
					Worktrees []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					} `json:"worktrees"`
				}{
					ProjectID: "default",
					Worktrees: []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					}{
						{Path: "/tmp/parent", Branch: "az/az-parent", IssueID: parentID},
						{Path: "/tmp/child", Branch: "az/az-child", IssueID: childID},
					},
				})
				if err != nil {
					t.Fatalf("marshal worktree response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandTaskList:
				tasks := []domain.Task{
					{
						ID:      childID,
						Title:   "Child task",
						Status:  domain.StatusInProgress,
						Type:    domain.TypeTask,
						Session: &domain.Session{IssueID: childID, State: domain.SessionBusy, Worktree: "/tmp/child"},
					},
					{
						ID:      parentID,
						Title:   "Parent epic",
						Status:  domain.StatusInProgress,
						Type:    domain.TypeEpic,
						Session: &domain.Session{IssueID: parentID, State: domain.SessionBusy, Worktree: "/tmp/parent"},
					},
				}
				respBody, err := json.Marshal(tasks)
				if err != nil {
					t.Fatalf("marshal task list response: %v", err)
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
				if body.SessionID != childID {
					t.Fatalf("session stop body = %+v, want session_id=%s", body, childID)
				}
				respBody, err := json.Marshal(struct {
					Output string `json:"output"`
				}{Output: "stopped"})
				if err != nil {
					t.Fatalf("marshal session stop response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitStatus:
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{Status: git.GitStatus{HasChanges: false}})
				if err != nil {
					t.Fatalf("marshal status response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMerge:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal merge request: %v", err)
				}
				if body.Worktree != "/tmp/child" || body.Branch != "az/az-parent" {
					t.Fatalf("merge body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitMergeCommandResponse{
					Worktree: body.Worktree,
					Branch:   body.Branch,
					Result:   git.MergeResult{Success: true},
				})
				if err != nil {
					t.Fatalf("marshal merge response: %v", err)
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
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.tasks = []domain.Task{
		{
			ID:       childID,
			Title:    "Child task",
			Status:   domain.StatusInProgress,
			Type:     domain.TypeTask,
			ParentID: &parentID,
		},
		{
			ID:     parentID,
			Title:  "Parent epic",
			Status: domain.StatusInProgress,
			Type:   domain.TypeEpic,
		},
	}
	// Simulate stale projection: m.sessions map does not yet contain hydrated sessions.
	m.sessions = map[string]*domain.Session{}
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "m"})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected follow-on merge command")
	}

	msg := cmd()
	mergeMsg, ok := msg.(mergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergeResultMsg", msg)
	}
	if mergeMsg.sourceID != parentID || mergeMsg.targetID != childID {
		t.Fatalf("merge result = %+v, want source=%s target=%s", mergeMsg, parentID, childID)
	}
	if mergeMsg.err != nil {
		t.Fatalf("merge err = %v", mergeMsg.err)
	}
	if got := transport.requests; len(got) != 8 || got[0] != daemonclient.CommandWorktreeList || got[1] != daemonclient.CommandWorktreeList || got[2] != daemonclient.CommandTaskList || got[3] != daemonclient.CommandSessionStop || got[4] != daemonclient.CommandWorktreeList || got[5] != daemonclient.CommandGitStatus || got[6] != daemonclient.CommandGitStatus || got[7] != daemonclient.CommandGitMerge {
		t.Fatalf("requests = %v", got)
	}
}

func TestHandleMergeResultPendingOperationShowsInfoToast(t *testing.T) {
	m := newTestModel()

	updated, cmd := m.Update(mergeResultMsg{
		sourceID:    "az-1",
		targetID:    "main",
		stage:       "merge",
		operationID: "op-merge",
		state:       protocol.OperationStateQueued,
	})
	if cmd == nil {
		t.Fatal("expected refresh command for pending merge")
	}

	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if len(updatedModel.toasts) == 0 {
		t.Fatal("expected pending-operation toast")
	}
	gotToast := updatedModel.toasts[len(updatedModel.toasts)-1].Message
	if !strings.Contains(gotToast, "Merge queued for az-1 (operation op-merge)") {
		t.Fatalf("toast = %q, want queued merge message", gotToast)
	}
}

func TestHandleMergeTargetSelectionToMainUsesWorktreeLookupFallback(t *testing.T) {
	sourceID := "az-source"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				respBody, err := json.Marshal(struct {
					ProjectID string `json:"project_id"`
					Worktrees []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					} `json:"worktrees"`
				}{
					ProjectID: "default",
					Worktrees: []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					}{
						{Path: "/tmp/az-source", Branch: "az/az-source", IssueID: sourceID},
					},
				})
				if err != nil {
					t.Fatalf("marshal worktree response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitStatus:
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{Status: git.GitStatus{HasChanges: false}})
				if err != nil {
					t.Fatalf("marshal status response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitFetch:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal fetch request: %v", err)
				}
				if body.Worktree != "." || body.Remote != "origin" {
					t.Fatalf("fetch body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{Worktree: body.Worktree, Remote: body.Remote})
				if err != nil {
					t.Fatalf("marshal fetch response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitCheckout:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal checkout request: %v", err)
				}
				if body.Worktree != "." || body.Branch != "main" {
					t.Fatalf("checkout body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{Worktree: body.Worktree, Branch: body.Branch})
				if err != nil {
					t.Fatalf("marshal checkout response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMerge:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal merge request: %v", err)
				}
				if body.Worktree != "." || body.Branch != "az/az-source" {
					t.Fatalf("merge body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitMergeCommandResponse{
					Worktree: body.Worktree,
					Branch:   body.Branch,
					Result:   git.MergeResult{Success: true},
				})
				if err != nil {
					t.Fatalf("marshal merge response: %v", err)
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
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)

	updated, cmd := m.handleMergeTargetSelection(overlay.MergeTargetSelectedMsg{
		SourceID: sourceID,
		TargetID: "main",
	})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected resolve command")
	}

	msg := cmd()
	resolvedMsg, ok := msg.(mergeTargetSelectionResolvedMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergeTargetSelectionResolvedMsg", msg)
	}
	next, nextCmd := updated.(Model).Update(resolvedMsg)
	if _, ok := next.(Model); !ok {
		t.Fatalf("next model type = %T, want Model", next)
	}
	if nextCmd == nil {
		t.Fatal("expected merge command after resolution")
	}
	mergeMsg, ok := nextCmd().(mergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergeResultMsg", nextCmd())
	}
	if mergeMsg.err != nil {
		t.Fatalf("merge err = %v", mergeMsg.err)
	}
	if got := transport.requests; len(got) != 7 || got[0] != daemonclient.CommandWorktreeList || got[1] != daemonclient.CommandWorktreeList || got[2] != daemonclient.CommandGitStatus || got[3] != daemonclient.CommandGitStatus || got[4] != daemonclient.CommandGitFetch || got[5] != daemonclient.CommandGitCheckout || got[6] != daemonclient.CommandGitMerge {
		t.Fatalf("requests = %v", got)
	}
}

func TestActionModeMergeKeyTriggersFollowOnMergeFlow(t *testing.T) {
	parentID := "az-parent"
	childID := "az-child"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				respBody, err := json.Marshal(struct {
					ProjectID string `json:"project_id"`
					Worktrees []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					} `json:"worktrees"`
				}{
					ProjectID: "default",
					Worktrees: []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					}{
						{Path: "/tmp/parent", Branch: "az/az-parent", IssueID: parentID},
						{Path: "/tmp/child", Branch: "az/az-child", IssueID: childID},
					},
				})
				if err != nil {
					t.Fatalf("marshal worktree response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitStatus:
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{Status: git.GitStatus{HasChanges: false}})
				if err != nil {
					t.Fatalf("marshal status response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMerge:
				respBody, err := json.Marshal(daemonclient.GitMergeCommandResponse{
					Worktree: "/tmp/child",
					Branch:   "az/az-parent",
					Result:   git.MergeResult{Success: true},
				})
				if err != nil {
					t.Fatalf("marshal merge response: %v", err)
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
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.editor.EnterAction()
	m.tasks = []domain.Task{
		{ID: childID, Title: "Child task", Status: domain.StatusInProgress, Type: domain.TypeTask, ParentID: &parentID},
		{ID: parentID, Title: "Parent task", Status: domain.StatusDone, Type: domain.TypeTask},
	}
	m.sessions[childID] = &domain.Session{IssueID: childID, State: domain.SessionPaused, Worktree: "/tmp/child"}
	m.sessions[parentID] = &domain.Session{IssueID: parentID, State: domain.SessionBusy, Worktree: "/tmp/parent"}
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected follow-on merge command from action-mode m")
	}
	msg := cmd()
	if _, ok := msg.(mergeResultMsg); !ok {
		t.Fatalf("msg type = %T, want mergeResultMsg", msg)
	}
}

func TestFollowOnMergeSelectionTopLevelFallsBackToMergeMain(t *testing.T) {
	issueID := "az-top"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				respBody, err := json.Marshal(struct {
					ProjectID string `json:"project_id"`
					Worktrees []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					} `json:"worktrees"`
				}{
					ProjectID: "default",
					Worktrees: []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					}{
						{Path: "/tmp/az-top", Branch: "az/az-top", IssueID: issueID},
					},
				})
				if err != nil {
					t.Fatalf("marshal worktree response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitStatus:
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{Status: git.GitStatus{HasChanges: false}})
				if err != nil {
					t.Fatalf("marshal status response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitFetch:
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{Worktree: ".", Remote: "origin"})
				if err != nil {
					t.Fatalf("marshal fetch response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitCheckout:
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{Worktree: ".", Branch: "main"})
				if err != nil {
					t.Fatalf("marshal checkout response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMerge:
				respBody, err := json.Marshal(daemonclient.GitMergeCommandResponse{
					Worktree: ".",
					Branch:   "az/az-top",
					Result:   git.MergeResult{Success: true},
				})
				if err != nil {
					t.Fatalf("marshal merge response: %v", err)
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
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.tasks = []domain.Task{
		{
			ID:     issueID,
			Title:  "Top-level task",
			Status: domain.StatusInProgress,
			Type:   domain.TypeTask,
		},
	}
	m.sessions[issueID] = &domain.Session{IssueID: issueID, State: domain.SessionPaused, Worktree: "/tmp/az-top"}
	m.nav.SelectTask(issueID, 1)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "m"})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected merge-to-main command for top-level issue")
	}
	msg := cmd()
	mergeMsg, ok := msg.(mergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergeResultMsg", msg)
	}
	if mergeMsg.targetID != "main" || mergeMsg.err != nil {
		t.Fatalf("merge message = %+v", mergeMsg)
	}
}

func TestMergeToMainPreflightBlocksDirtySourceOrTarget(t *testing.T) {
	sourceID := "az-source"
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				respBody, err := json.Marshal(struct {
					ProjectID string `json:"project_id"`
					Worktrees []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					} `json:"worktrees"`
				}{
					ProjectID: "default",
					Worktrees: []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					}{
						{Path: "/tmp/az-source", Branch: "az/az-source", IssueID: sourceID},
					},
				})
				if err != nil {
					t.Fatalf("marshal worktree response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitStatus:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal status request: %v", err)
				}
				status := git.GitStatus{HasChanges: false}
				if body.Worktree == "/tmp/az-source" {
					status = git.GitStatus{HasChanges: true, Modified: []string{"main.go"}}
				}
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{Status: status})
				if err != nil {
					t.Fatalf("marshal status response: %v", err)
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
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	msg := m.mergeToMainCmd("/tmp/az-source", sourceID)()

	preflight, ok := msg.(mergePreflightFailureMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergePreflightFailureMsg", msg)
	}
	if preflight.sourceID != sourceID || preflight.targetID != "main" {
		t.Fatalf("preflight msg = %+v", preflight)
	}
	if preflight.targetWorktree != m.activeProjectPath() {
		t.Fatalf("target worktree = %q, want %q", preflight.targetWorktree, m.activeProjectPath())
	}
	if len(preflight.reasons) == 0 || !strings.Contains(preflight.reasons[0], "not clean") {
		t.Fatalf("preflight reasons = %+v, want dirty-worktree reason", preflight.reasons)
	}
	for _, command := range transport.requests {
		if command == daemonclient.CommandGitFetch || command == daemonclient.CommandGitCheckout || command == daemonclient.CommandGitMerge {
			t.Fatalf("unexpected git merge command during preflight failure: %v", transport.requests)
		}
	}
}

func TestDiscardChangesCmdRunsRestoreThenClean(t *testing.T) {
	var calls [][]string
	original := runGitCommandFunc
	runGitCommandFunc = func(_ context.Context, worktree string, args ...string) (string, error) {
		call := append([]string{worktree}, args...)
		calls = append(calls, call)
		return "", nil
	}
	defer func() {
		runGitCommandFunc = original
	}()

	m := newTestModel()
	msg := m.discardChangesCmd("source", "/tmp/az-1")()

	result, ok := msg.(mergePreflightActionResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergePreflightActionResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("discard err = %v", result.err)
	}
	if len(calls) != 2 {
		t.Fatalf("git calls = %v, want 2 calls", calls)
	}
	wantFirst := []string{"/tmp/az-1", "restore", "--staged", "--worktree", "."}
	if !reflect.DeepEqual(calls[0], wantFirst) {
		t.Fatalf("first git call = %v, want %v", calls[0], wantFirst)
	}
	wantSecond := []string{"/tmp/az-1", "clean", "-fd"}
	if !reflect.DeepEqual(calls[1], wantSecond) {
		t.Fatalf("second git call = %v, want %v", calls[1], wantSecond)
	}
}

func TestDiscardChangesCmdReturnsCleanError(t *testing.T) {
	original := runGitCommandFunc
	runGitCommandFunc = func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "clean" && args[1] == "-fd" {
			return "", errors.New("clean failed")
		}
		return "", nil
	}
	defer func() {
		runGitCommandFunc = original
	}()

	m := newTestModel()
	msg := m.discardChangesCmd("target", "/tmp/az-2")()

	result, ok := msg.(mergePreflightActionResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergePreflightActionResultMsg", msg)
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "clean failed") {
		t.Fatalf("discard err = %v, want clean failure", result.err)
	}
}

func TestGitWorkflowCommandsUseDaemonClient(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandGitAbortMerge:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal abort request: %v", err)
				}
				if body.Worktree != "/tmp/az-1" {
					t.Fatalf("abort body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{Worktree: body.Worktree})
				if err != nil {
					t.Fatalf("marshal abort response: %v", err)
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
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)

	abortMsg := m.abortMergeCmd("/tmp/az-1")()
	abortResult, ok := abortMsg.(abortMergeResultMsg)
	if !ok {
		t.Fatalf("abort message type = %T, want abortMergeResultMsg", abortMsg)
	}
	if abortResult.err != nil {
		t.Fatalf("abort err = %v", abortResult.err)
	}

	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandGitAbortMerge {
		t.Fatalf("requests = %v", got)
	}
}

func TestHandleSelectionWorktreeCleanupActions(t *testing.T) {
	t.Run("cleanup worktree only", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandSessionStop:
					var body struct {
						ProjectID string `json:"project_id"`
						SessionID string `json:"session_id"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal session stop request: %v", err)
					}
					if body.SessionID != "az-1" {
						t.Fatalf("session stop body = %+v, want session_id=az-1", body)
					}
				case daemonclient.CommandWorktreeRemove:
					var body struct {
						ProjectID string `json:"project_id"`
						IssueID   string `json:"issue_id"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal worktree remove request: %v", err)
					}
					if body.IssueID != "az-1" {
						t.Fatalf("worktree remove body = %+v, want issue_id=az-1", body)
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
			{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress},
		}
		m.sessions["az-1"] = &domain.Session{
			IssueID:  "az-1",
			State:    domain.SessionBusy,
			Worktree: "/tmp/az-1",
		}
		m.nav.SelectTask("az-1", 1)

		updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "w"})
		if _, ok := updated.(Model); !ok {
			t.Fatalf("updated model type = %T, want Model", updated)
		}
		if cmd == nil {
			t.Fatal("expected worktree cleanup command")
		}

		msg := cmd()
		result, ok := msg.(worktreeCleanupResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupResultMsg", msg)
		}
		if result.err != nil {
			t.Fatalf("cleanup result err = %v", result.err)
		}
		if result.deletedTask {
			t.Fatalf("deletedTask = true, want false")
		}
		if got := transport.requests; len(got) != 2 ||
			got[0] != daemonclient.CommandSessionStop ||
			got[1] != daemonclient.CommandWorktreeRemove {
			t.Fatalf("requests = %v", got)
		}
	})

	t.Run("delete task and cleanup worktree", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskDelete:
					// expected commands
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
			{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress},
		}
		m.sessions["az-1"] = &domain.Session{
			IssueID:  "az-1",
			State:    domain.SessionBusy,
			Worktree: "/tmp/az-1",
		}
		m.nav.SelectTask("az-1", 1)

		updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "W"})
		if _, ok := updated.(Model); !ok {
			t.Fatalf("updated model type = %T, want Model", updated)
		}
		if cmd == nil {
			t.Fatal("expected full cleanup command")
		}

		msg := cmd()
		result, ok := msg.(worktreeCleanupResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupResultMsg", msg)
		}
		if result.err != nil {
			t.Fatalf("cleanup result err = %v", result.err)
		}
		if !result.deletedTask {
			t.Fatalf("deletedTask = false, want true")
		}
		if got := transport.requests; len(got) != 3 ||
			got[0] != daemonclient.CommandSessionStop ||
			got[1] != daemonclient.CommandWorktreeRemove ||
			got[2] != daemonclient.CommandTaskDelete {
			t.Fatalf("requests = %v", got)
		}
	})
}

func TestAbortMergeCmdUsesDaemonClient(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandGitAbortMerge {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandGitAbortMerge)
			}
			var body daemonclient.GitCommandRequest
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal abort request: %v", err)
			}
			if body.Worktree != "/tmp/az-1" {
				t.Fatalf("abort body = %+v", body)
			}
			respBody, err := json.Marshal(daemonclient.GitCommandResponse{Worktree: body.Worktree})
			if err != nil {
				t.Fatalf("marshal abort response: %v", err)
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

	m := newDaemonTestModel(transport)
	msg := m.abortMergeCmd("/tmp/az-1")()
	result, ok := msg.(abortMergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want abortMergeResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("abort err = %v", result.err)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandGitAbortMerge {
		t.Fatalf("requests = %v", got)
	}
}

func TestHandleSelectionOpenPRAndHelixPaths(t *testing.T) {
	t.Run("open PR without session warns", func(t *testing.T) {
		m := newDaemonTestModel(&recordingDaemonTransport{})
		m.tasks = []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, Type: domain.TypeTask}}
		m.nav.SelectTask("az-1", 1)

		updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "O"})
		if cmd != nil {
			t.Fatal("expected nil command when session is missing")
		}
		updatedModel, ok := updated.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updated)
		}
		if len(updatedModel.toasts) == 0 {
			t.Fatal("expected warning toast")
		}
		got := updatedModel.toasts[len(updatedModel.toasts)-1].Message
		if !strings.Contains(got, "No active session - start session first") {
			t.Fatalf("warning toast = %q", got)
		}
	})

	t.Run("open helix without tmux returns hint", func(t *testing.T) {
		t.Setenv("TMUX", "")

		m := newDaemonTestModel(&recordingDaemonTransport{})
		msg := m.openHelixCmd("/tmp/az-1", "az-1")()
		result, ok := msg.(helixOpenResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want helixOpenResultMsg", msg)
		}
		if result.opened {
			t.Fatalf("opened = true, want false when tmux missing")
		}
		if !strings.Contains(result.commandHint, "cd /tmp/az-1 && hx") {
			t.Fatalf("command hint = %q", result.commandHint)
		}
	})
}

func TestHandleSelectionTombstoneActionDeletesTask(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskArchive {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskArchive)
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
	m.tasks = []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, Type: domain.TypeTask}}
	m.nav.SelectTask("az-1", 1)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "T"})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected delete command")
	}
	msg := cmd()
	deleted, ok := msg.(taskDeletedResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want taskDeletedMsg", msg)
	}
	if deleted.taskID != "az-1" || deleted.err != nil {
		t.Fatalf("deleted msg = %+v", deleted)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandTaskArchive {
		t.Fatalf("requests = %v", got)
	}
}

func TestOpenPROverlayUsesDaemonWorktreeBranch(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandWorktreeList {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandWorktreeList)
			}
			respBody, err := json.Marshal(struct {
				ProjectID string `json:"project_id"`
				Worktrees []struct {
					Path    string `json:"path"`
					Branch  string `json:"branch"`
					IssueID string `json:"issue_id"`
				} `json:"worktrees"`
			}{
				ProjectID: "default",
				Worktrees: []struct {
					Path    string `json:"path"`
					Branch  string `json:"branch"`
					IssueID string `json:"issue_id"`
				}{
					{Path: "/tmp/az-1", Branch: "az/az-1", IssueID: "az-1"},
				},
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

	m := newDaemonTestModel(transport)
	msg := m.openPROverlayCmd("/tmp/az-1", "az-1")()
	result, ok := msg.(openPROverlayResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want openPROverlayResultMsg", msg)
	}
	if result.branch != "az/az-1" || result.issueID != "az-1" {
		t.Fatalf("result = %+v", result)
	}

	updated, cmd := m.Update(result)
	if cmd == nil {
		t.Fatal("expected overlay push command")
	}
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	current := updatedModel.overlayStack.Current()
	prOverlay, ok := current.(*overlay.PRCreateOverlay)
	if !ok {
		t.Fatalf("overlay type = %T, want *overlay.PRCreateOverlay", current)
	}
	view := prOverlay.View()
	if !strings.Contains(view, "az/az-1") || !strings.Contains(view, "az-1") {
		t.Fatalf("overlay view = %q, want branch and issue", view)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandWorktreeList {
		t.Fatalf("requests = %v", got)
	}
}

func TestCreatePROverlayUsesDaemonPRSurface(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandPRCreate {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandPRCreate)
			}
			var body daemonclient.CreatePullRequestParams
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if body.Branch != "az/az-1" || body.BaseBranch != "main" || body.IssueID != "az-1" {
				t.Fatalf("request body = %+v", body)
			}
			respBody, err := json.Marshal(daemonclient.CreatePullRequestResult{
				IssueID: "az-1",
				PullRequest: pr.PRInfo{
					Number:  7,
					Title:   body.Title,
					URL:     "https://example.com/pr/7",
					State:   "open",
					Draft:   body.Draft,
					Branch:  body.Branch,
					BaseRef: body.BaseBranch,
				},
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

	m := newDaemonTestModel(transport)
	msg := m.createPRWithOverlayCmd(overlay.PRCreatedMsg{
		Title:      "Add feature",
		Body:       "Body",
		Branch:     "az/az-1",
		BaseBranch: "main",
		Draft:      true,
		IssueID:    "az-1",
	})()
	result, ok := msg.(prCreatedResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want prCreatedResultMsg", msg)
	}
	if result.url != "https://example.com/pr/7" || result.err != nil {
		t.Fatalf("result = %+v", result)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandPRCreate {
		t.Fatalf("requests = %v", got)
	}
}

func TestCheckBranchBehindCmdUsesDaemonSurface(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandGitBranchBehind {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandGitBranchBehind)
			}
			var body daemonclient.BranchBehindCheckParams
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if body.Worktree != "/tmp/az-1" || body.BaseBranch != "main" || body.Remote != "origin" {
				t.Fatalf("request body = %+v", body)
			}
			respBody, err := json.Marshal(daemonclient.BranchBehindCheckResult{
				Worktree:      body.Worktree,
				BaseBranch:    body.BaseBranch,
				Remote:        body.Remote,
				RevRange:      "main..origin/main",
				CommitsBehind: 2,
				Behind:        true,
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

	m := newDaemonTestModel(transport)
	m.config.Git.BaseBranch = "main"
	msg := m.checkBranchBehindCmd("/tmp/az-1", "az-1")()
	result, ok := msg.(branchBehindMsg)
	if !ok {
		t.Fatalf("message type = %T, want branchBehindMsg", msg)
	}
	if result.commitsBehind != 2 || result.err != nil || result.issueID != "az-1" {
		t.Fatalf("result = %+v", result)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandGitBranchBehind {
		t.Fatalf("requests = %v", got)
	}
}

func TestMergeSourceOverlaySelectsUpstreamSource(t *testing.T) {
	target := domain.Task{
		ID:     "az-child",
		Title:  "Child task",
		Status: domain.StatusInProgress,
		Type:   domain.TypeTask,
	}
	candidates := []overlay.MergeTarget{
		{ID: "az-parent", Label: "Parent epic", Status: domain.StatusInProgress, HasWorktree: true},
	}

	menu := overlay.NewMergeSourceSelectOverlay(&target, candidates, nil, nil)
	if got := menu.Title(); got != "Select Upstream Source" {
		t.Fatalf("title = %q, want Select Upstream Source", got)
	}

	view := menu.View()
	if !strings.Contains(view, "Merge into") || !strings.Contains(view, target.ID) {
		t.Fatalf("view = %q, want upstream header", view)
	}

	_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected selection command")
	}
	msg := cmd()
	selMsg, ok := msg.(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("selection type = %T, want SelectionMsg", msg)
	}
	result, ok := selMsg.Value.(overlay.MergeTargetSelectedMsg)
	if !ok {
		t.Fatalf("selection value type = %T, want MergeTargetSelectedMsg", selMsg.Value)
	}
	if result.SourceID != "az-parent" || result.TargetID != "az-child" {
		t.Fatalf("selection = %+v, want source az-parent target az-child", result)
	}
}

func TestStartSessionShiftSStartsDirectlyFromBaseBranch(t *testing.T) {
	baseBranch := "develop"
	childID := "az-child"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandSessionStart:
				var body struct {
					ProjectID  string `json:"project_id"`
					SessionID  string `json:"session_id"`
					BaseBranch string `json:"base_branch,omitempty"`
					Yolo       bool   `json:"yolo,omitempty"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal session start request: %v", err)
				}
				if body.SessionID != childID {
					t.Fatalf("session ID = %q, want %q", body.SessionID, childID)
				}
				if body.BaseBranch != baseBranch {
					t.Fatalf("base branch = %q, want %q", body.BaseBranch, baseBranch)
				}
				if body.Yolo {
					t.Fatal("expected yolo=false for Shift+S start")
				}
				respBody, _ := json.Marshal(struct {
					Output string `json:"output"`
				}{Output: "started"})
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

	m := newDaemonTestModel(transport)
	m.config.Git.BaseBranch = baseBranch
	m.tasks = []domain.Task{
		{
			ID:     childID,
			Title:  "Child task",
			Status: domain.StatusInProgress,
			Type:   domain.TypeTask,
		},
	}
	m.nav.SelectTask(childID, 0)

	_, startCmd := m.handleSelection(overlay.SelectionMsg{Key: "S"})
	if startCmd == nil {
		t.Fatal("expected session start command")
	}
	startMsg := startCmd()
	started, ok := startMsg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("start message type = %T, want sessionStartedMsg", startMsg)
	}
	if started.issueID != childID {
		t.Fatalf("started issue = %q, want %q", started.issueID, childID)
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionStart {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestStartSessionCommandReturnsPendingOperationToast(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionStart {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionStart)
			}
			respBody, _ := json.Marshal(map[string]any{
				"operation_id": "op-start",
				"state":        string(protocol.OperationStateQueued),
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

	m := newDaemonTestModel(transport)
	startMsg := m.startSessionCmd("az-child", "main", false)()
	started, ok := startMsg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("message type = %T, want sessionStartedMsg", startMsg)
	}
	if started.operationID != "op-start" || started.state != protocol.OperationStateQueued {
		t.Fatalf("started msg = %+v", started)
	}

	updated, cmd := m.Update(startMsg)
	if cmd == nil {
		t.Fatal("expected refresh command after pending operation")
	}
	updatedModel := updated.(Model)
	if len(updatedModel.toasts) == 0 {
		t.Fatal("expected queued operation toast")
	}
	gotToast := updatedModel.toasts[len(updatedModel.toasts)-1].Message
	if !strings.Contains(gotToast, "Session start queued for az-child (operation op-start)") {
		t.Fatalf("toast = %q, want queued operation message", gotToast)
	}
}

func TestSessionOriginCandidatesIncludeBaseBranchAndUpstreamSource(t *testing.T) {
	baseBranch := "develop"
	parentID := "az-parent"
	childID := "az-child"

	m := newDaemonTestModel(&recordingDaemonTransport{})
	m.config.Git.BaseBranch = baseBranch
	m.tasks = []domain.Task{
		{
			ID:       childID,
			Title:    "Child task",
			Status:   domain.StatusInProgress,
			Type:     domain.TypeTask,
			ParentID: &parentID,
		},
		{
			ID:     parentID,
			Title:  "Parent task",
			Status: domain.StatusDone,
			Type:   domain.TypeTask,
		},
	}
	m.sessions[parentID] = &domain.Session{
		IssueID:  parentID,
		State:    domain.SessionBusy,
		Worktree: "/tmp/parent",
	}

	candidates, upstreamCount := m.sessionOriginCandidates(&m.tasks[0])
	if upstreamCount != 1 {
		t.Fatalf("upstreamCount = %d, want 1", upstreamCount)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v, want base branch plus one upstream source", candidates)
	}
	if candidates[0].ID != baseBranch || candidates[0].Label != baseBranch || !candidates[0].IsMain {
		t.Fatalf("base candidate = %+v, want main branch %q", candidates[0], baseBranch)
	}
	if candidates[1].ID != parentID || candidates[1].Status != domain.StatusDone || !candidates[1].HasWorktree {
		t.Fatalf("upstream candidate = %+v, want upstream issue %q with worktree", candidates[1], parentID)
	}
	if got := m.originBranchForSelection(""); got != baseBranch {
		t.Fatalf("originBranchForSelection(\"\") = %q, want %q", got, baseBranch)
	}
	if got := m.originBranchForSelection(parentID); got != "az/"+parentID {
		t.Fatalf("originBranchForSelection(%q) = %q, want %q", parentID, got, "az/"+parentID)
	}
	if got := m.originBranchForSelection("az/custom"); got != "az/custom" {
		t.Fatalf("originBranchForSelection(%q) = %q, want %q", "az/custom", got, "az/custom")
	}
}

func TestStartSessionShiftSIgnoresUpstreamChoices(t *testing.T) {
	baseBranch := "develop"
	parentA := "az-parent-a"
	parentB := "az-parent-b"
	childID := "az-child"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandSessionStart:
				var body struct {
					ProjectID  string `json:"project_id"`
					SessionID  string `json:"session_id"`
					BaseBranch string `json:"base_branch,omitempty"`
					Yolo       bool   `json:"yolo,omitempty"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal session start request: %v", err)
				}
				if body.SessionID != childID {
					t.Fatalf("session ID = %q, want %q", body.SessionID, childID)
				}
				if body.BaseBranch != baseBranch {
					t.Fatalf("base branch = %q, want %q", body.BaseBranch, baseBranch)
				}
				if body.Yolo {
					t.Fatal("expected yolo=false for Shift+S start")
				}
				respBody, _ := json.Marshal(struct {
					Output string `json:"output"`
				}{Output: "started"})
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

	m := newDaemonTestModel(transport)
	m.config.Git.BaseBranch = baseBranch
	m.tasks = []domain.Task{
		{
			ID:           childID,
			Title:        "Child task",
			Status:       domain.StatusInProgress,
			Type:         domain.TypeTask,
			ParentID:     &parentA,
			Dependencies: []domain.Dependency{{ID: parentB, Type: domain.DependencyBlocks}},
		},
		{
			ID:     parentA,
			Title:  "Parent A",
			Status: domain.StatusDone,
			Type:   domain.TypeTask,
		},
		{
			ID:     parentB,
			Title:  "Parent B",
			Status: domain.StatusInProgress,
			Type:   domain.TypeTask,
		},
	}
	m.sessions[parentA] = &domain.Session{
		IssueID:  parentA,
		State:    domain.SessionBusy,
		Worktree: "/tmp/parent-a",
	}
	m.sessions[parentB] = &domain.Session{
		IssueID:  parentB,
		State:    domain.SessionBusy,
		Worktree: "/tmp/parent-b",
	}
	m.nav.SelectTask(childID, 0)

	candidates, upstreamCount := m.sessionOriginCandidates(&m.tasks[0])
	if upstreamCount != 2 {
		t.Fatalf("upstreamCount = %d, want 2", upstreamCount)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates = %+v, want base branch plus two upstream sources", candidates)
	}

	_, startCmd := m.handleSelection(overlay.SelectionMsg{Key: "S"})
	if startCmd == nil {
		t.Fatal("expected session start command")
	}
	startMsg := startCmd()
	started, ok := startMsg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("start message type = %T, want sessionStartedMsg", startMsg)
	}
	if started.issueID != childID {
		t.Fatalf("started issue = %q, want %q", started.issueID, childID)
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionStart {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestStartSessionBangStartsYoloFromBaseBranch(t *testing.T) {
	baseBranch := "develop"
	childID := "az-child"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandSessionStart:
				var body struct {
					ProjectID  string `json:"project_id"`
					SessionID  string `json:"session_id"`
					BaseBranch string `json:"base_branch,omitempty"`
					Yolo       bool   `json:"yolo,omitempty"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal session start request: %v", err)
				}
				if body.SessionID != childID {
					t.Fatalf("session ID = %q, want %q", body.SessionID, childID)
				}
				if body.BaseBranch != baseBranch {
					t.Fatalf("base branch = %q, want %q", body.BaseBranch, baseBranch)
				}
				if !body.Yolo {
					t.Fatal("expected yolo=true for ! start")
				}
				respBody, _ := json.Marshal(struct {
					Output string `json:"output"`
				}{Output: "started"})
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

	m := newDaemonTestModel(transport)
	m.config.Git.BaseBranch = baseBranch
	m.tasks = []domain.Task{
		{
			ID:     childID,
			Title:  "Child task",
			Status: domain.StatusInProgress,
			Type:   domain.TypeTask,
		},
	}
	m.nav.SelectTask(childID, 0)

	_, startCmd := m.handleSelection(overlay.SelectionMsg{Key: "!"})
	if startCmd == nil {
		t.Fatal("expected session start command")
	}
	startMsg := startCmd()
	started, ok := startMsg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("start message type = %T, want sessionStartedMsg", startMsg)
	}
	if started.issueID != childID {
		t.Fatalf("started issue = %q, want %q", started.issueID, childID)
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionStart {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestStopSessionCommandPreservesDaemonProjection(t *testing.T) {
	startedAt := time.Date(2026, time.March, 25, 11, 0, 0, 0, time.UTC)
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandSessionStop {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionStop)
			}
			var body struct {
				ProjectID string `json:"project_id"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal stop request: %v", err)
			}
			if body.SessionID != "az-child" {
				t.Fatalf("stop body = %+v, want az-child", body)
			}
			respBody, _ := json.Marshal(struct {
				Output string `json:"output"`
			}{Output: "stopped"})
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.sessions["az-child"] = &domain.Session{
		IssueID:   "az-child",
		State:     domain.SessionBusy,
		StartedAt: &startedAt,
		Worktree:  "/tmp/az-child",
	}

	msg := m.stopSessionCmd("az-child")()
	stopped, ok := msg.(sessionStoppedMsg)
	if !ok {
		t.Fatalf("message type = %T, want sessionStoppedMsg", msg)
	}
	if stopped.issueID != "az-child" {
		t.Fatalf("stopped issue = %q, want az-child", stopped.issueID)
	}

	updated, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("update command = %T, want nil", cmd)
	}
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if session, ok := updatedModel.sessions["az-child"]; !ok || session == nil || session.Worktree != "/tmp/az-child" {
		t.Fatalf("session projection = %+v, want preserved worktree /tmp/az-child", session)
	}
	if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandSessionStop {
		t.Fatalf("requests = %v", transport.requests)
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
				if body.SessionID != "issue-1" {
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
		"issue-1": &domain.Session{IssueID: "issue-1", State: domain.SessionPaused, StartedAt: &oldSessionStart},
		"issue-2": &domain.Session{IssueID: "issue-2", State: domain.SessionBusy, StartedAt: &oldSessionStart},
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
	if _, ok := m.sessions["issue-1"]; !ok {
		t.Fatal("expected stale session issue-1 to remain in projection until daemon refresh")
	}
	if _, ok := m.sessions["issue-2"]; !ok {
		t.Fatal("expected stale session issue-2 to remain in projection until daemon refresh")
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

func TestFetchAndMergeCommandReturnsPendingOperationToast(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandGitFetch:
				respBody, _ := json.Marshal(daemonclient.GitCommandResponse{
					Worktree: "/tmp/az-child",
					Remote:   "origin",
				})
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMerge:
				respBody, _ := json.Marshal(map[string]any{
					"operation_id": "op-merge",
					"state":        string(protocol.OperationStateRunning),
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

	m := newDaemonTestModel(transport)
	msg := m.fetchAndMergeCmd("/tmp/az-child", "main", "az-child", false)()
	result, ok := msg.(fetchAndMergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want fetchAndMergeResultMsg", msg)
	}
	if result.operationID != "op-merge" || result.state != protocol.OperationStateRunning || result.stage != "merge" {
		t.Fatalf("result = %+v", result)
	}

	updated, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("expected refresh command after pending merge")
	}
	updatedModel := updated.(Model)
	if len(updatedModel.toasts) == 0 {
		t.Fatal("expected pending merge toast")
	}
	gotToast := updatedModel.toasts[len(updatedModel.toasts)-1].Message
	if !strings.Contains(gotToast, "Merge running for az-child (operation op-merge)") {
		t.Fatalf("toast = %q, want merge running message", gotToast)
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

	t.Run("bulk delete reports per-item failures", func(t *testing.T) {
		deleteCount := 0
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskDelete {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskDelete)
				}
				var body daemonclient.TaskIDRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal delete request: %v", err)
				}
				deleteCount++
				if deleteCount == 2 {
					return protocol.ResponseEnvelope{}, io.ErrUnexpectedEOF
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

		msg := m.bulkDeleteCmd([]string{"az-1", "az-2"})()
		result, ok := msg.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want bulkStatusResultMsg", msg)
		}
		if result.updated != 1 || result.failed != 1 || len(result.issues) != 1 {
			t.Fatalf("bulk delete result = %+v", result)
		}
		if result.issues[0].taskID != "az-2" || !strings.Contains(result.issues[0].reason, "unexpected EOF") {
			t.Fatalf("issues = %+v", result.issues)
		}
		if got := transport.requests; len(got) != 2 || got[0] != daemonclient.CommandTaskDelete || got[1] != daemonclient.CommandTaskDelete {
			t.Fatalf("requests = %v", got)
		}

		updated, _ := m.Update(result)
		updatedModel, ok := updated.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updated)
		}
		if len(updatedModel.toasts) == 0 {
			t.Fatal("expected bulk action toast")
		}
		gotToast := updatedModel.toasts[len(updatedModel.toasts)-1].Message
		if !strings.Contains(gotToast, "az-2:") || !strings.Contains(gotToast, "unexpected EOF") {
			t.Fatalf("toast = %q, want wrapped failure reason", gotToast)
		}
	})

	t.Run("bulk move right applies to selected set", func(t *testing.T) {
		statusBodies := make([]daemonclient.TaskStatusRequest, 0, 2)
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskUpdateStatus:
					var body daemonclient.TaskStatusRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal status request: %v", err)
					}
					statusBodies = append(statusBodies, body)
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
			{ID: "az-2", Status: domain.StatusInProgress},
			{ID: "az-3", Status: domain.StatusBlocked},
		}

		updated, cmd := m.handleBulkAction(overlay.BulkActionMsg{
			Action:      "l",
			SelectedIDs: []string{"az-1", "az-2"},
		})
		if cmd == nil {
			t.Fatal("expected bulk move command")
		}
		if _, ok := updated.(Model); !ok {
			t.Fatalf("updated model type = %T, want Model", updated)
		}

		msg := cmd()
		result, ok := msg.(bulkStatusResultMsg)
		if !ok || result.updated != 2 || result.failed != 0 {
			t.Fatalf("bulk move result = %#v", msg)
		}
		if len(statusBodies) != 2 {
			t.Fatalf("status bodies = %+v, want 2 updates", statusBodies)
		}
		if statusBodies[0].TaskID != "az-1" || statusBodies[0].Status != domain.StatusInProgress {
			t.Fatalf("first update = %+v, want az-1 -> in_progress", statusBodies[0])
		}
		if statusBodies[1].TaskID != "az-2" || statusBodies[1].Status != domain.StatusBlocked {
			t.Fatalf("second update = %+v, want az-2 -> blocked", statusBodies[1])
		}
		if got := transport.requests; len(got) != 2 ||
			got[0] != daemonclient.CommandTaskUpdateStatus ||
			got[1] != daemonclient.CommandTaskUpdateStatus {
			t.Fatalf("requests = %v", got)
		}
	})
}

func TestBulkActionMenuPreviewAndFrozenSelection(t *testing.T) {
	selected := []string{"az-1", "az-2"}
	menu := overlay.NewBulkActionMenu(selected, len(selected))

	selected[0] = "az-mutated"

	view := menu.View()
	if !strings.Contains(view, "Selected:") {
		t.Fatalf("view = %q, want selected preview", view)
	}
	if !strings.Contains(view, "Scope: 2 frozen selected task(s)") {
		t.Fatalf("view = %q, want frozen scope preview", view)
	}

	_, cmd := menu.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("expected bulk action command")
	}

	msg := cmd()
	result, ok := msg.(overlay.BulkActionMsg)
	if !ok {
		t.Fatalf("message type = %T, want BulkActionMsg", msg)
	}

	if !reflect.DeepEqual(result.SelectedIDs, []string{"az-1", "az-2"}) {
		t.Fatalf("selected ids = %+v, want frozen original ids", result.SelectedIDs)
	}
}

func TestBulkDeleteReportsSkippedDriftedIDs(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskDelete {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskDelete)
			}
			var body daemonclient.TaskIDRequest
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal delete request: %v", err)
			}
			if body.TaskID != "az-1" {
				t.Fatalf("delete body = %+v, want az-1", body)
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
	}

	msg := m.bulkDeleteCmd([]string{"az-1", "az-2"})()
	result, ok := msg.(bulkStatusResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want bulkStatusResultMsg", msg)
	}
	if result.updated != 1 || result.failed != 0 || len(result.issues) != 1 {
		t.Fatalf("bulk delete result = %+v", result)
	}
	if result.issues[0].taskID != "az-2" || result.issues[0].reason != "task not found" {
		t.Fatalf("issue details = %+v", result.issues)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandTaskDelete {
		t.Fatalf("requests = %v", got)
	}

	updated, _ := m.Update(result)
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if len(updatedModel.toasts) == 0 {
		t.Fatal("expected bulk action toast")
	}
	gotToast := updatedModel.toasts[len(updatedModel.toasts)-1].Message
	if !strings.Contains(gotToast, "az-2: task not found") {
		t.Fatalf("toast = %q, want issue reason", gotToast)
	}
}
