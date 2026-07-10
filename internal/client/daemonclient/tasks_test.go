package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type taskRecordingTransport struct {
	lastReq        protocol.RequestEnvelope
	commandCalls   int
	handshakeCalls int
	replyFn        func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	handshakeFn    func(protocol.Hello) (protocol.HelloAck, error)
}

func assertTaskProjectID(t *testing.T, req protocol.RequestEnvelope, want string) {
	t.Helper()
	if req.Meta.ProjectID.String() != want {
		t.Fatalf("project_id = %q, want %q", req.Meta.ProjectID.String(), want)
	}
}

func responseWithJSON(t *testing.T, req protocol.RequestEnvelope, body any) protocol.ResponseEnvelope {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal response body: %v", err)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
		Body:            data,
	}
}

func responseWithCommandError(req protocol.RequestEnvelope, message string) protocol.ResponseEnvelope {
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              false,
		Error: &protocol.ErrorEnvelope{
			Code:    protocol.ErrorCodeInternal,
			Message: message,
		},
	}
}

func (t *taskRecordingTransport) Handshake(_ context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	t.handshakeCalls++
	if t.handshakeFn != nil {
		return t.handshakeFn(hello)
	}
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *taskRecordingTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	_ = ctx
	t.commandCalls++
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
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    mustTaskSnapshotCheckedAt(),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            tasks,
	})
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}
	return body
}

func mustMarshalBoardSnapshotPayload(t *testing.T, protocolVersion protocol.Version, projectID string, revision uint64, tasks []domain.Task) []byte {
	t.Helper()
	body, err := json.Marshal(protocol.BoardSnapshotPayload{
		SchemaVersion:    protocol.BoardSnapshotSchemaVersion,
		ProtocolVersion:  protocolVersion,
		SnapshotRevision: revision,
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    mustTaskSnapshotCheckedAt(),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            protocol.BoardTaskSummariesFromDomain(tasks),
	})
	if err != nil {
		t.Fatalf("marshal board snapshot payload: %v", err)
	}
	return body
}

func mustTaskSnapshotCheckedAt() time.Time {
	return time.Date(2026, time.April, 2, 10, 31, 45, 0, time.UTC)
}

func mustTaskIssueState(t *testing.T, parts domain.IssueStateParts) domain.IssueState {
	t.Helper()
	state, err := domain.NewIssueState(parts)
	if err != nil {
		t.Fatalf("NewIssueState(%+v) error = %v", parts, err)
	}
	return state
}

func mustMarshalRawTaskListSnapshotBody(t *testing.T, body any) []byte {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal snapshot body: %v", err)
	}
	return data
}

func TestTaskSnapshotRequireFullDetails(t *testing.T) {
	if err := (TaskSnapshot{}).RequireFullDetails("issue update"); err != nil {
		t.Fatalf("full snapshot guard error = %v, want nil", err)
	}

	err := (TaskSnapshot{SummariesOnly: true}).RequireFullDetails("issue update")
	if err == nil {
		t.Fatal("summary snapshot guard error = nil, want error")
	}
	if got := err.Error(); !strings.Contains(got, "issue update requires full task details") {
		t.Fatalf("summary snapshot guard error = %q", got)
	}
}

func TestListTaskEventsCommand(t *testing.T) {
	const wantProjectID = "proj-task"
	observedAt := time.Date(2026, 7, 6, 2, 0, 0, 0, time.UTC)
	transport := &taskRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			assertTaskProjectID(t, req, wantProjectID)
			if req.Command != CommandTaskEvents {
				t.Fatalf("command = %q, want %q", req.Command, CommandTaskEvents)
			}
			var body TaskEventsRequest
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if body.TaskID != "az-1" || body.Limit != 10 || len(body.Types) != 1 || body.Types[0] != "issue.status_changed" {
				t.Fatalf("request body = %+v", body)
			}
			respBody, err := json.Marshal(struct {
				Events []domain.IssueObservationEvent `json:"events"`
			}{
				Events: []domain.IssueObservationEvent{{
					ID:         42,
					IssueID:    "az-1",
					Type:       domain.IssueEventIssueStatusChanged,
					ObservedAt: observedAt,
					Source:     "issue-store",
					Payload: map[string]any{
						"from_status": "open",
						"to_status":   "in_progress",
					},
				}},
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
	events, err := client.ListTaskEvents(context.Background(), "az-1", []string{"issue.status_changed"}, 10)
	if err != nil {
		t.Fatalf("ListTaskEvents error: %v", err)
	}
	if len(events) != 1 || events[0].ID != 42 || events[0].Type != domain.IssueEventIssueStatusChanged {
		t.Fatalf("events = %+v", events)
	}
}

func TestTaskGraphReadinessDecodesWorkerObservations(t *testing.T) {
	const wantProjectID = "proj-task"
	transport := &taskRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			assertTaskProjectID(t, req, wantProjectID)
			if req.Command != CommandTaskGraphReadiness {
				t.Fatalf("command = %q, want %q", req.Command, CommandTaskGraphReadiness)
			}
			var body taskGraphReadinessRequest
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if body.TaskID != "az-root" {
				t.Fatalf("request body = %+v", body)
			}
			if body.ActorID != "agent-a" {
				t.Fatalf("actor_id = %q, want agent-a", body.ActorID)
			}
			return responseWithJSON(t, req, TaskGraphReadiness{
				RootIssueID: "az-root",
				Runnable:    []string{"az-1"},
				NestedRoots: []TaskNestedRoot{{
					IssueID:    "az-nested",
					Status:     string(domain.StatusOpen),
					Type:       string(domain.TypeTask),
					ChildCount: 1,
					Advice:     "start its orchestrator session with `az session start az-nested`",
				}},
				Blocked: map[string]string{},
				WorkerObservations: []domain.WorkerObservation{{
					IssueID: "az-1",
					State:   domain.WorkerObservationRunnable,
					Reason:  "leaf worker has no unresolved blockers or active runtime",
					SourceTruthPolicy: domain.WorkerObservationSourcePolicy{
						IssueGraph:       "projection",
						SessionRuntime:   "hybrid",
						WorktreeGit:      "projection",
						MailboxEvidence:  "projection",
						ActiveOperations: "projection",
					},
				}},
			}), nil
		},
	}

	client := New(transport).WithProjectID(wantProjectID)
	ready, err := client.TaskGraphReadinessForActor(context.Background(), "az-root", "agent-a")
	if err != nil {
		t.Fatalf("TaskGraphReadiness error: %v", err)
	}
	if len(ready.WorkerObservations) != 1 {
		t.Fatalf("worker observations = %+v", ready.WorkerObservations)
	}
	if len(ready.NestedRoots) != 1 || ready.NestedRoots[0].IssueID != "az-nested" {
		t.Fatalf("nested roots = %+v", ready.NestedRoots)
	}
	observation := ready.WorkerObservations[0]
	if observation.IssueID != "az-1" || observation.State != domain.WorkerObservationRunnable || observation.SourceTruthPolicy.SessionRuntime != "hybrid" {
		t.Fatalf("worker observation = %+v", observation)
	}
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
					Body: mustMarshalRawTaskListSnapshotBody(t, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  req.ProtocolVersion,
						SnapshotRevision: 17,
						ProjectID:        naming.ProjectID(wantProjectID),
						LastCheckedAt:    mustTaskSnapshotCheckedAt(),
						Freshness:        protocol.TaskListFreshnessFresh,
						SummariesOnly:    true,
						Tasks:            []domain.Task{{ID: "az-9", Title: "Task 9", Status: domain.StatusInReview}},
					}),
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
		if !snapshot.LastCheckedAt.Equal(mustTaskSnapshotCheckedAt()) {
			t.Fatalf("last_checked_at = %v, want %v", snapshot.LastCheckedAt, mustTaskSnapshotCheckedAt())
		}
		if snapshot.Freshness != protocol.TaskListFreshnessFresh {
			t.Fatalf("freshness = %q, want %q", snapshot.Freshness, protocol.TaskListFreshnessFresh)
		}
		if !snapshot.SummariesOnly {
			t.Fatal("summaries_only = false, want true")
		}
		if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID != "az-9" {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("board snapshot", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandBoardFetch {
					t.Fatalf("command = %q, want %q", req.Command, CommandBoardFetch)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        18,
					OK:              true,
					Body: mustMarshalBoardSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 18, []domain.Task{{
						ID:          "az-board",
						Title:       "Board task",
						Description: "description must not cross board payload",
						Notes:       "notes must not cross board payload",
						Status:      domain.StatusInReview,
						State: mustTaskIssueState(t, domain.IssueStateParts{
							Workflow: domain.IssueWorkflowActive,
							Review:   domain.IssueReviewRequested,
						}),
						Session:  &domain.Session{IssueID: "az-board", Activity: string(domain.SessionBusy)},
						Priority: domain.P1,
						Type:     domain.TypeTask,
					}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.BoardSnapshot(context.Background())
		if err != nil {
			t.Fatalf("BoardSnapshot error: %v", err)
		}
		if snapshot.Revision != 18 {
			t.Fatalf("revision = %d, want 18", snapshot.Revision)
		}
		if !snapshot.SummariesOnly {
			t.Fatal("summaries_only = false, want true")
		}
		if len(snapshot.Tasks) != 1 {
			t.Fatalf("task count = %d, want 1", len(snapshot.Tasks))
		}
		task := snapshot.Tasks[0]
		if task.ID != "az-board" || task.Title != "Board task" || task.Status != domain.StatusInProgress {
			t.Fatalf("task = %+v", task)
		}
		if got, want := task.State.Review(), domain.IssueReviewRequested; got != want {
			t.Fatalf("task issue review state = %s, want %s", got, want)
		}
		if task.Description != "" || task.Notes != "" || task.Design != "" || task.Acceptance != "" {
			t.Fatalf("board snapshot decoded detail fields: description=%q notes=%q design=%q acceptance=%q", task.Description, task.Notes, task.Design, task.Acceptance)
		}
	})

	t.Run("list snapshot with query", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskList {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskList)
				}
				var body protocol.TaskListRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal task.list request body: %v", err)
				}
				if body.Query != "runtime cache" {
					t.Fatalf("query = %q, want runtime cache", body.Query)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        17,
					OK:              true,
					Body: mustMarshalRawTaskListSnapshotBody(t, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  req.ProtocolVersion,
						SnapshotRevision: 17,
						ProjectID:        naming.ProjectID(wantProjectID),
						LastCheckedAt:    mustTaskSnapshotCheckedAt(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-9", Title: "Task 9", Status: domain.StatusInReview}},
					}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.ListTasksSnapshotWithQuery(context.Background(), " runtime cache ")
		if err != nil {
			t.Fatalf("ListTasksSnapshotWithQuery error: %v", err)
		}
		if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID != "az-9" {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("list snapshot with dependencies", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskList {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskList)
				}
				var body protocol.TaskListRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal task.list request body: %v", err)
				}
				if !body.IncludeDependencies {
					t.Fatal("include_dependencies = false, want true")
				}
				if body.Query != "" {
					t.Fatalf("query = %q, want empty", body.Query)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        18,
					OK:              true,
					Body: mustMarshalRawTaskListSnapshotBody(t, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  req.ProtocolVersion,
						SnapshotRevision: 18,
						ProjectID:        naming.ProjectID(wantProjectID),
						LastCheckedAt:    mustTaskSnapshotCheckedAt(),
						Freshness:        protocol.TaskListFreshnessFresh,
						SummariesOnly:    true,
						Tasks:            []domain.Task{{ID: "az-10", Title: "Task 10", Status: domain.StatusOpen}},
					}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.ListTasksSnapshotWithDependencies(context.Background())
		if err != nil {
			t.Fatalf("ListTasksSnapshotWithDependencies error: %v", err)
		}
		if snapshot.Revision != 18 {
			t.Fatalf("revision = %d, want 18", snapshot.Revision)
		}
		if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID != "az-10" {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("get snapshot", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskGet {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskGet)
				}
				var body TaskIDRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if body.TaskID != "az-9" {
					t.Fatalf("task_id = %q, want az-9", body.TaskID)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        17,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 17, []domain.Task{{ID: "az-9", Title: "Task 9", Status: domain.StatusInReview}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.GetTaskSnapshot(context.Background(), "az-9")
		if err != nil {
			t.Fatalf("GetTaskSnapshot error: %v", err)
		}
		if snapshot.Revision != 17 {
			t.Fatalf("revision = %d, want 17", snapshot.Revision)
		}
		if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID != "az-9" {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("get many snapshot", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskGetMany {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskGetMany)
				}
				var body TaskIDsRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if got, want := len(body.TaskIDs), 2; got != want {
					t.Fatalf("len(task_ids) = %d, want %d", got, want)
				}
				if body.TaskIDs[0] != "az-2" || body.TaskIDs[1] != "az-1" {
					t.Fatalf("task_ids = %+v, want [az-2 az-1]", body.TaskIDs)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        18,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 18, []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusInReview}, {ID: "az-2", Title: "Task 2", Status: domain.StatusOpen}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.GetManyTaskSnapshot(context.Background(), []string{"az-2", "az-1"})
		if err != nil {
			t.Fatalf("GetManyTaskSnapshot error: %v", err)
		}
		if snapshot.Revision != 18 {
			t.Fatalf("revision = %d, want 18", snapshot.Revision)
		}
		if len(snapshot.Tasks) != 2 {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("get many snapshot with ancestors", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskGetMany {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskGetMany)
				}
				var body TaskIDsRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if !body.IncludeAncestors {
					t.Fatalf("include_ancestors = false, want true")
				}
				if len(body.TaskIDs) != 1 || body.TaskIDs[0] != "az-child" {
					t.Fatalf("task_ids = %+v, want [az-child]", body.TaskIDs)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        19,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 19, []domain.Task{{ID: "az-child", Title: "Child", Status: domain.StatusOpen}, {ID: "az-root", Title: "Root", Status: domain.StatusOpen}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.GetManyTaskSnapshotWithAncestors(context.Background(), []string{"az-child"})
		if err != nil {
			t.Fatalf("GetManyTaskSnapshotWithAncestors error: %v", err)
		}
		if snapshot.Revision != 19 {
			t.Fatalf("revision = %d, want 19", snapshot.Revision)
		}
		if len(snapshot.Tasks) != 2 {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("get many snapshot with ancestors no dependents", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskGetMany {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskGetMany)
				}
				var body TaskIDsRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if !body.IncludeAncestors {
					t.Fatalf("include_ancestors = false, want true")
				}
				if !body.ExcludeDependents {
					t.Fatalf("exclude_dependents = false, want true")
				}
				if len(body.TaskIDs) != 1 || body.TaskIDs[0] != "az-parent" {
					t.Fatalf("task_ids = %+v, want [az-parent]", body.TaskIDs)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        20,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 20, []domain.Task{{ID: "az-parent", Title: "Parent", Status: domain.StatusOpen}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.GetManyTaskSnapshotWithAncestorsNoDependents(context.Background(), []string{"az-parent"})
		if err != nil {
			t.Fatalf("GetManyTaskSnapshotWithAncestorsNoDependents error: %v", err)
		}
		if snapshot.Revision != 20 {
			t.Fatalf("revision = %d, want 20", snapshot.Revision)
		}
		if len(snapshot.Tasks) != 1 {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("child board snapshot requests direct dependents", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskGetMany {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskGetMany)
				}
				var body TaskIDsRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if !body.IncludeAncestors {
					t.Fatalf("include_ancestors = false, want true")
				}
				if !body.DirectDependents {
					t.Fatalf("direct_dependents = false, want true")
				}
				if body.ExcludeDependents {
					t.Fatalf("exclude_dependents = true, want false")
				}
				if len(body.TaskIDs) != 1 || body.TaskIDs[0] != "az-parent" {
					t.Fatalf("task_ids = %+v, want [az-parent]", body.TaskIDs)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        23,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 23, []domain.Task{{ID: "az-parent", Title: "Parent", Status: domain.StatusOpen}, {ID: "az-child", Title: "Child", Status: domain.StatusOpen}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.GetChildBoardSnapshotWithMode(context.Background(), "az-parent", ReadWaitModeExplicit)
		if err != nil {
			t.Fatalf("GetChildBoardSnapshotWithMode error: %v", err)
		}
		if snapshot.Revision != 23 {
			t.Fatalf("revision = %d, want 23", snapshot.Revision)
		}
		if len(snapshot.Tasks) != 2 {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("get many snapshot metadata only with ancestors no dependents", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskGetMany {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskGetMany)
				}
				var body TaskIDsRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if !body.IncludeAncestors || !body.ExcludeDependents || !body.MetadataOnly {
					t.Fatalf("request flags ancestors=%v exclude_dependents=%v metadata_only=%v, want all true", body.IncludeAncestors, body.ExcludeDependents, body.MetadataOnly)
				}
				if len(body.TaskIDs) != 1 || body.TaskIDs[0] != "az-parent" {
					t.Fatalf("task_ids = %+v, want [az-parent]", body.TaskIDs)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        21,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 21, []domain.Task{{ID: "az-parent", Title: "Parent", Status: domain.StatusOpen}}),
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly(context.Background(), []string{"az-parent"})
		if err != nil {
			t.Fatalf("GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly error: %v", err)
		}
		if snapshot.Revision != 21 {
			t.Fatalf("revision = %d, want 21", snapshot.Revision)
		}
		if len(snapshot.Tasks) != 1 {
			t.Fatalf("snapshot tasks = %+v", snapshot.Tasks)
		}
	})

	t.Run("list snapshot rejects legacy raw task array", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        23,
					OK:              true,
					Body:            mustMarshalRawTaskListSnapshotBody(t, []domain.Task{{ID: "az-legacy", Title: "Legacy Task", Status: domain.StatusOpen}}),
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

	t.Run("list snapshot rejects schema and protocol mismatches with expected and actual versions", func(t *testing.T) {
		cases := []struct {
			name        string
			body        protocol.TaskListSnapshotPayload
			wantSubstrs []string
		}{
			{
				name: "schema mismatch",
				body: protocol.TaskListSnapshotPayload{
					SchemaVersion:    protocol.TaskListSnapshotSchemaVersion + 1,
					ProtocolVersion:  protocol.CurrentVersion,
					SnapshotRevision: 3,
					ProjectID:        wantProjectID,
					LastCheckedAt:    mustTaskSnapshotCheckedAt(),
					Freshness:        protocol.TaskListFreshnessFresh,
				},
				wantSubstrs: []string{
					"decode task.list response",
					"schema_version mismatch: expected 3, actual 4",
				},
			},
			{
				name: "protocol mismatch",
				body: protocol.TaskListSnapshotPayload{
					SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
					ProtocolVersion:  protocol.CurrentVersion + 1,
					SnapshotRevision: 3,
					ProjectID:        wantProjectID,
					LastCheckedAt:    mustTaskSnapshotCheckedAt(),
					Freshness:        protocol.TaskListFreshnessFresh,
				},
				wantSubstrs: []string{
					"decode task.list response",
					fmt.Sprintf("protocol_version mismatch: expected %d, actual %d", protocol.CurrentVersion, protocol.CurrentVersion+1),
				},
			},
		}

		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				transport := &taskRecordingTransport{
					replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						assertTaskProjectID(t, req, wantProjectID)
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							Revision:        23,
							OK:              true,
							Body:            mustMarshalRawTaskListSnapshotBody(t, tt.body),
						}, nil
					},
				}

				client := New(transport).WithProjectID(wantProjectID)
				_, err := client.ListTasksSnapshot(context.Background())
				if err == nil {
					t.Fatal("expected version mismatch error")
				}
				for _, want := range tt.wantSubstrs {
					if got := err.Error(); !strings.Contains(got, want) {
						t.Fatalf("error = %q, want substring %q", got, want)
					}
				}
			})
		}
	})

	t.Run("list snapshot retries once after version mismatch and successful handshake", func(t *testing.T) {
		var transport *taskRecordingTransport
		transport = &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if transport.commandCalls == 1 {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Revision:        41,
						OK:              true,
						Body: mustMarshalRawTaskListSnapshotBody(t, protocol.TaskListSnapshotPayload{
							SchemaVersion:    protocol.TaskListSnapshotSchemaVersion + 1,
							ProtocolVersion:  protocol.CurrentVersion,
							SnapshotRevision: 41,
							ProjectID:        wantProjectID,
							LastCheckedAt:    mustTaskSnapshotCheckedAt(),
							Freshness:        protocol.TaskListFreshnessFresh,
						}),
					}, nil
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        42,
					OK:              true,
					Body:            mustMarshalTaskSnapshotPayload(t, req.ProtocolVersion, wantProjectID, 42, []domain.Task{{ID: "az-retry", Title: "Retry task", Status: domain.StatusOpen}}),
				}, nil
			},
		}
		client := New(transport).WithProjectID(wantProjectID)
		snapshot, err := client.ListTasksSnapshot(context.Background())
		if err != nil {
			t.Fatalf("ListTasksSnapshot error: %v", err)
		}
		if snapshot.Revision != 42 || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID != "az-retry" {
			t.Fatalf("snapshot = %+v, want retried payload", snapshot)
		}
		if transport.handshakeCalls != 1 {
			t.Fatalf("handshake calls = %d, want 1", transport.handshakeCalls)
		}
		if transport.commandCalls != 2 {
			t.Fatalf("command calls = %d, want 2", transport.commandCalls)
		}
	})

	t.Run("list snapshot version mismatch returns decode error when handshake fails", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Revision:        23,
					OK:              true,
					Body: mustMarshalRawTaskListSnapshotBody(t, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion + 1,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 23,
						ProjectID:        wantProjectID,
						LastCheckedAt:    mustTaskSnapshotCheckedAt(),
						Freshness:        protocol.TaskListFreshnessFresh,
					}),
				}, nil
			},
			handshakeFn: func(protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{}, errors.New("dial down")
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		_, err := client.ListTasksSnapshot(context.Background())
		if err == nil {
			t.Fatal("expected version mismatch error with failed handshake")
		}
		if got := err.Error(); !strings.Contains(got, "decode task.list response") || !strings.Contains(got, "handshake after mismatch failed") {
			t.Fatalf("error = %q, want decode+handshake failure context", got)
		}
		if transport.handshakeCalls != 1 {
			t.Fatalf("handshake calls = %d, want 1", transport.handshakeCalls)
		}
		if transport.commandCalls != 1 {
			t.Fatalf("command calls = %d, want 1", transport.commandCalls)
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
				if body.Title != "Task 2" || body.ParentID == nil || body.ParentID.String() != parentID {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(TaskIDResponse{TaskID: naming.IssueID("az-2")})
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
		typedParentID := naming.IssueID(parentID)
		id, err := client.CreateTask(context.Background(), TaskCreateParams{
			Title:    "Task 2",
			Type:     domain.TypeTask,
			Priority: domain.P1,
			ParentID: &typedParentID,
		})
		if err != nil {
			t.Fatalf("CreateTask error: %v", err)
		}
		if id != "az-2" {
			t.Fatalf("task id = %q, want az-2", id)
		}
	})

	t.Run("status update to done uses close command", func(t *testing.T) {
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				switch req.Command {
				case CommandTaskClose:
					var body taskCloseRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal request: %v", err)
					}
					if body.TaskID != "az-3" {
						t.Fatalf("request body = %+v", body)
					}
					return responseWithJSON(t, req, TaskCloseResult{
						TaskID: "az-3",
						Status: string(domain.StatusDone),
					}), nil
				default:
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskClose)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		if err := client.UpdateTaskStatus(context.Background(), "az-3", domain.StatusDone); err != nil {
			t.Fatalf("UpdateTaskStatus error: %v", err)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
		}
	})

	t.Run("status update to cancelled uses close command without integration", func(t *testing.T) {
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				if req.Command != CommandTaskClose {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskClose)
				}
				var body taskCloseRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.TaskID != "az-9" || body.IntegrateBeforeClose || body.CloseOutcome != string(domain.IssueCloseCancelled) {
					t.Fatalf("request body = %+v, want cancelled close without integration", body)
				}
				return responseWithJSON(t, req, TaskCloseResult{
					TaskID: "az-9",
					Status: string(domain.StatusCancelled),
				}), nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		if err := client.UpdateTaskStatus(context.Background(), "az-9", domain.StatusCancelled); err != nil {
			t.Fatalf("UpdateTaskStatus error: %v", err)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
		}
	})

	t.Run("status update to done propagates close pending operation", func(t *testing.T) {
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				if req.Command != CommandTaskClose {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskClose)
				}
				respBody, err := json.Marshal(map[string]any{
					"operation_id": "op-close",
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
		if pending.OperationID != "op-close" {
			t.Fatalf("operation id = %q, want op-close", pending.OperationID)
		}
		if pending.State != protocol.OperationStateQueued {
			t.Fatalf("state = %q, want queued", pending.State)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
		}
	})

	t.Run("status update cleans runtime before closing", func(t *testing.T) {
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				switch req.Command {
				case CommandTaskClose:
					var body taskCloseRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal close request: %v", err)
					}
					if body.TaskID != "az-3" {
						t.Fatalf("close body = %+v", body)
					}
					return responseWithJSON(t, req, TaskCloseResult{
						TaskID:          "az-3",
						Status:          string(domain.StatusDone),
						SessionStopped:  true,
						WorktreeRemoved: true,
					}), nil
				default:
					t.Fatalf("unexpected command = %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		err := client.UpdateTaskStatusWithOptions(context.Background(), "az-3", domain.StatusDone, TaskStatusOptions{})
		if err != nil {
			t.Fatalf("UpdateTaskStatusWithOptions error: %v", err)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
		}
	})

	t.Run("close task command", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskClose {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskClose)
				}
				var body taskCloseRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal close request: %v", err)
				}
				if body.TaskID != "az-3" || !body.ForceWorktree || !body.IgnoreAhead || !body.IntegrateBeforeClose || !body.AllowActiveSession {
					t.Fatalf("close body = %+v", body)
				}
				return responseWithJSON(t, req, TaskCloseResult{
					TaskID:                 "az-3",
					Status:                 string(domain.StatusDone),
					IntegrationRequested:   true,
					Integrated:             true,
					IntegratedSourceBranch: "feature/az-3",
					IntegratedTargetBranch: "main",
					SessionStopped:         true,
					WorktreeRemoved:        true,
					WorktreeForced:         true,
				}), nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		got, err := client.CloseTask(context.Background(), "az-3", TaskStatusOptions{ForceWorktree: true, IgnoreAhead: true, IntegrateBeforeClose: true, AllowActiveSession: true})
		if err != nil {
			t.Fatalf("CloseTask error: %v", err)
		}
		if got.TaskID != "az-3" || got.Status != string(domain.StatusDone) || !got.WorktreeForced || !got.IntegrationRequested || !got.Integrated {
			t.Fatalf("close result = %+v", got)
		}
	})

	t.Run("status update uses lightweight command for non-closed status", func(t *testing.T) {
		commands := []string{}
		var statusBody TaskStatusRequest
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				if req.Command == CommandTaskUpdateStatus {
					if err := json.Unmarshal(req.Body, &statusBody); err != nil {
						t.Fatalf("unmarshal status body: %v", err)
					}
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
		err := client.UpdateTaskStatusWithOptions(context.Background(), "az-3", domain.StatusInReview, TaskStatusOptions{CascadeChildren: true})
		if err != nil {
			t.Fatalf("UpdateTaskStatusWithOptions error: %v", err)
		}
		if strings.Join(commands, ",") != CommandTaskUpdateStatus {
			t.Fatalf("commands = %v, want only %s", commands, CommandTaskUpdateStatus)
		}
		if statusBody.TaskID.String() != "az-3" || statusBody.Status != domain.StatusInReview || !statusBody.CascadeChildren {
			t.Fatalf("status body = %+v, want cascade in_review for az-3", statusBody)
		}
	})

	t.Run("status update leaves issue unclosed when daemon close fails", func(t *testing.T) {
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				switch req.Command {
				case CommandTaskClose:
					return responseWithCommandError(req, "remove worktree before closing az-3: dirty worktree"), nil
				default:
					t.Fatalf("unexpected command after cleanup failure = %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		err := client.UpdateTaskStatusWithOptions(context.Background(), "az-3", domain.StatusDone, TaskStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), "remove worktree before closing az-3") {
			t.Fatalf("UpdateTaskStatusWithOptions error = %v, want worktree cleanup failure", err)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
		}
	})

	t.Run("status update passes force flag to daemon close", func(t *testing.T) {
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				switch req.Command {
				case CommandTaskClose:
					var body taskCloseRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal close request: %v", err)
					}
					if body.TaskID != "az-3" || !body.ForceWorktree {
						t.Fatalf("close body = %+v, want force close for az-3", body)
					}
					return responseWithJSON(t, req, TaskCloseResult{TaskID: "az-3", Status: string(domain.StatusDone), WorktreeForced: true}), nil
				default:
					t.Fatalf("unexpected command = %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		err := client.UpdateTaskStatusWithOptions(context.Background(), "az-3", domain.StatusDone, TaskStatusOptions{ForceWorktree: true})
		if err != nil {
			t.Fatalf("UpdateTaskStatusWithOptions error: %v", err)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
		}
	})

	t.Run("status update close guard blocks dirty or ahead worktree", func(t *testing.T) {
		cases := []struct {
			name    string
			status  GitStatus
			wantErr string
		}{
			{
				name:    "dirty",
				status:  GitStatus{HasChanges: true, Modified: []string{"main.go"}},
				wantErr: "worktree has local changes: main.go",
			},
			{
				name:    "untracked",
				status:  GitStatus{HasChanges: true, Untracked: []string{"scratch.txt"}},
				wantErr: "worktree has local changes: scratch.txt",
			},
			{
				name:    "ahead",
				status:  GitStatus{GitAheadCount: 2},
				wantErr: "branch is ahead by 2 commit(s)",
			},
		}

		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				commands := []string{}
				transport := &taskRecordingTransport{
					replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						assertTaskProjectID(t, req, wantProjectID)
						commands = append(commands, req.Command)
						switch req.Command {
						case CommandTaskClose:
							return responseWithCommandError(req, fmt.Sprintf("cannot close issue az-3: %s. Next: commit, discard, or merge the worktree changes first, then retry", tt.wantErr)), nil
						default:
							t.Fatalf("unexpected command after close guard failure = %q", req.Command)
							return protocol.ResponseEnvelope{}, nil
						}
					},
				}

				client := New(transport).WithProjectID(wantProjectID)
				err := client.UpdateTaskStatusWithOptions(context.Background(), "az-3", domain.StatusDone, TaskStatusOptions{})
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "Next:") || !strings.Contains(err.Error(), "commit, discard, or merge the worktree changes first") {
					t.Fatalf("UpdateTaskStatusWithOptions error = %v, want %q and recovery hint", err, tt.wantErr)
				}
				wantCommands := []string{CommandTaskClose}
				if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
					t.Fatalf("commands = %v, want %v", commands, wantCommands)
				}
			})
		}
	})

	t.Run("status update close can ignore ahead after integration", func(t *testing.T) {
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				switch req.Command {
				case CommandTaskClose:
					var body taskCloseRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal close request: %v", err)
					}
					if !body.IgnoreAhead {
						t.Fatalf("close body = %+v, want ignore_ahead", body)
					}
					return responseWithJSON(t, req, TaskCloseResult{TaskID: "az-3", Status: string(domain.StatusDone)}), nil
				default:
					t.Fatalf("unexpected command = %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		err := client.UpdateTaskStatusWithOptions(context.Background(), "az-3", domain.StatusDone, TaskStatusOptions{
			IgnoreAhead: true,
		})
		if err != nil {
			t.Fatalf("UpdateTaskStatusWithOptions error: %v", err)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
		}
	})

	t.Run("status update close guard blocks unresolved children", func(t *testing.T) {
		childID := naming.IssueID("az-4")
		grandchildID := naming.IssueID("az-5")
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				switch req.Command {
				case CommandTaskClose:
					message := fmt.Sprintf("cannot close issue az-3: unresolved child issues remain: %s (worktree); %s (open). Next: close or clean up the listed child issues first, then retry. Moved closed blockers back for cleanup: %s -> in_review", childID, grandchildID, childID)
					return responseWithCommandError(req, message), nil
				default:
					t.Fatalf("unexpected command after child close guard failure = %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		err := client.UpdateTaskStatusWithOptions(context.Background(), "az-3", domain.StatusDone, TaskStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), "unresolved child issues remain") || !strings.Contains(err.Error(), "az-4 (worktree)") || !strings.Contains(err.Error(), "az-5 (open)") || !strings.Contains(err.Error(), "close or clean up the listed child issues first") || !strings.Contains(err.Error(), "Moved closed blockers back for cleanup: az-4 -> in_review") || strings.Contains(err.Error(), "az issue close --id az-3 --cleanup") {
			t.Fatalf("UpdateTaskStatusWithOptions error = %v, want child close guard", err)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
		}
	})

	t.Run("status update close guard reopens closed dirty worktree", func(t *testing.T) {
		commands := []string{}
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				commands = append(commands, req.Command)
				switch req.Command {
				case CommandTaskClose:
					return responseWithCommandError(req, "cannot close issue az-3: worktree has local changes: main.go. Next: commit, discard, or merge the worktree changes first, then retry. Moved closed blockers back for cleanup: az-3 -> in_progress"), nil
				default:
					t.Fatalf("unexpected command after close guard failure = %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		err := client.UpdateTaskStatusWithOptions(context.Background(), "az-3", domain.StatusDone, TaskStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), "worktree has local changes: main.go") || !strings.Contains(err.Error(), "Moved closed blockers back for cleanup: az-3 -> in_progress") {
			t.Fatalf("UpdateTaskStatusWithOptions error = %v, want dirty guard and status repair", err)
		}
		wantCommands := []string{CommandTaskClose}
		if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
			t.Fatalf("commands = %v, want %v", commands, wantCommands)
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
				if body.TaskID != "az-4" ||
					body.Title != "Updated" ||
					body.Design == nil || *body.Design != "Replacement design" ||
					body.Notes == nil || *body.Notes != "Replacement notes" ||
					body.Acceptance == nil || *body.Acceptance != "Replacement acceptance" ||
					body.Estimate == nil || *body.Estimate != 3 ||
					!body.EstimateSet {
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
		design := "Replacement design"
		notes := "Replacement notes"
		acceptance := "Replacement acceptance"
		estimate := 3
		if err := client.UpdateTaskDetails(context.Background(), "az-4", TaskUpdateParams{
			Title:       "Updated",
			Design:      &design,
			Notes:       &notes,
			Acceptance:  &acceptance,
			Estimate:    &estimate,
			EstimateSet: true,
			Type:        domain.TypeBug,
			Priority:    domain.P0,
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
		if err := client.UnarchiveTask(context.Background(), "az-5"); err != nil {
			t.Fatalf("UnarchiveTask error: %v", err)
		}
		if len(commands) != 3 || commands[0] != CommandTaskDelete || commands[1] != CommandTaskArchive || commands[2] != CommandTaskUnarchive {
			t.Fatalf("commands = %v", commands)
		}
	})

	t.Run("unarchive with graph options", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskUnarchive {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskUnarchive)
				}
				var body struct {
					TaskID          naming.IssueID `json:"task_id"`
					WithParents     bool           `json:"with_parents"`
					CascadeChildren bool           `json:"cascade_children"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal unarchive request: %v", err)
				}
				if body.TaskID != "az-5" || !body.WithParents || !body.CascadeChildren {
					t.Fatalf("unarchive request = %+v, want graph options", body)
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
		if err := client.UnarchiveTaskWithOptions(context.Background(), "az-5", TaskUnarchiveOptions{WithParents: true, CascadeChildren: true}); err != nil {
			t.Fatalf("UnarchiveTaskWithOptions error: %v", err)
		}
	})

	t.Run("delete with cleanup options", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskDelete {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskDelete)
				}
				var body taskDeleteRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal delete request: %v", err)
				}
				if body.TaskID != "az-5" || !body.Cleanup || !body.StopSession || !body.RemoveWorktree || !body.ForceWorktree {
					t.Fatalf("delete request = %+v, want cleanup/stop/remove/force", body)
				}
				return responseWithJSON(t, req, TaskDeleteResult{
					TaskID:          "az-5",
					Deleted:         true,
					SessionStopped:  true,
					WorktreeRemoved: true,
					WorktreeForced:  true,
					Revision:        12,
				}), nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		got, err := client.DeleteTaskWithOptions(context.Background(), "az-5", TaskDeleteOptions{
			Cleanup:        true,
			StopSession:    true,
			RemoveWorktree: true,
			ForceWorktree:  true,
		})
		if err != nil {
			t.Fatalf("DeleteTaskWithOptions error: %v", err)
		}
		if !got.Deleted || !got.SessionStopped || !got.WorktreeRemoved || !got.WorktreeForced || got.Revision != 12 {
			t.Fatalf("delete result = %+v", got)
		}
	})

	t.Run("integration readiness", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskIntegrationReady {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskIntegrationReady)
				}
				var body taskIntegrationReadinessRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.TaskID != "az-7" || body.RepoDir != "/repo" {
					t.Fatalf("request body = %+v", body)
				}
				return responseWithJSON(t, req, TaskIntegrationReadiness{
					IssueID:       "az-7",
					ParentIssueID: "az-1",
					Ready:         true,
				}), nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		got, err := client.TaskIntegrationReadiness(context.Background(), "az-7", " /repo ")
		if err != nil {
			t.Fatalf("TaskIntegrationReadiness error: %v", err)
		}
		if !got.Ready || got.ParentIssueID != "az-1" {
			t.Fatalf("readiness = %+v", got)
		}
	})

	t.Run("merge base target", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskMergeBaseTarget {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskMergeBaseTarget)
				}
				var body taskMergeBaseTargetRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.TaskID != "az-8" || body.BaseBranch != "trunk" || !body.AllowBaseForChild {
					t.Fatalf("request body = %+v", body)
				}
				return responseWithJSON(t, req, TaskMergeBaseTarget{
					IssueID:        "az-8",
					TargetID:       "az-1",
					Branch:         "riordan/az-1/parent",
					WorktreePath:   "/repo-parent",
					BranchAttached: true,
					AncestorChain:  []string{"az-1"},
				}), nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		got, err := client.TaskMergeBaseTarget(context.Background(), "az-8", " trunk ", true)
		if err != nil {
			t.Fatalf("TaskMergeBaseTarget error: %v", err)
		}
		if got.TargetID != "az-1" || got.Branch != "riordan/az-1/parent" || !got.BranchAttached {
			t.Fatalf("merge target = %+v", got)
		}
	})

	t.Run("follow-on merge candidates", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != CommandTaskFollowOnMerge {
					t.Fatalf("command = %q, want %q", req.Command, CommandTaskFollowOnMerge)
				}
				var body taskFollowOnMergeCandidatesRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.TaskID != "az-9" {
					t.Fatalf("task id = %q, want az-9", body.TaskID)
				}
				return responseWithJSON(t, req, taskFollowOnMergeCandidatesResponse{
					TaskID:            "az-9",
					MergeTargetToBase: false,
					Candidates: []TaskFollowOnMergeCandidate{
						{
							IssueID:     "az-parent",
							Title:       "Parent",
							Status:      domain.StatusInProgress,
							Relation:    string(domain.DependencyParentChild),
							Order:       0,
							HasWorktree: true,
						},
					},
				}), nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		mergeTargetToBase, candidates, err := client.TaskFollowOnMergeCandidates(context.Background(), "az-9")
		if err != nil {
			t.Fatalf("TaskFollowOnMergeCandidates error: %v", err)
		}
		if mergeTargetToBase {
			t.Fatal("mergeTargetToBase = true, want false")
		}
		if len(candidates) != 1 || candidates[0].IssueID != "az-parent" || !candidates[0].HasWorktree {
			t.Fatalf("candidates = %+v", candidates)
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
			TaskID:      naming.IssueID("az-6"),
			DependsOnID: naming.IssueID("az-1"),
			Type:        "blocks",
		}); err != nil {
			t.Fatalf("AddTaskDependency error: %v", err)
		}
		if err := client.RemoveTaskDependency(context.Background(), TaskDependencyRemoveParams{
			TaskID:      naming.IssueID("az-6"),
			DependsOnID: naming.IssueID("az-1"),
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

func TestBulkCleanupTasksUsesDurableOperationAndReturnsStructuredResult(t *testing.T) {
	commandCount := 0
	transport := &taskRecordingTransport{replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		commandCount++
		switch req.Command {
		case protocol.CommandOperationSubmit:
			var submit protocol.OperationSubmitRequestBody
			if err := json.Unmarshal(req.Body, &submit); err != nil {
				t.Fatal(err)
			}
			if submit.Kind != CommandTaskBulkCleanup {
				t.Fatalf("operation kind = %q, want %q", submit.Kind, CommandTaskBulkCleanup)
			}
			var body TaskBulkCleanupRequest
			if err := json.Unmarshal(submit.Payload, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.TaskIDs) != 2 || body.CloseOutcome != "cancelled" || !body.DryRun {
				t.Fatalf("body = %+v", body)
			}
			return responseWithJSON(t, req, protocol.OperationSubmitResponseBody{Operation: protocol.OperationRecord{
				OperationID: "op-bulk-cleanup", State: protocol.OperationStateRunning,
			}}), nil
		case protocol.CommandOperationGet:
			result, err := json.Marshal(TaskBulkCleanupResult{DryRun: true, Action: "cancelled", Items: []TaskBulkCleanupItem{{TaskID: "az-1", Success: true}}})
			if err != nil {
				t.Fatal(err)
			}
			return responseWithJSON(t, req, protocol.OperationGetResponseBody{Operation: protocol.OperationRecord{
				OperationID: "op-bulk-cleanup", State: protocol.OperationStateDone, Result: result,
			}}), nil
		default:
			t.Fatalf("unexpected command %q", req.Command)
			return protocol.ResponseEnvelope{}, nil
		}
	}}
	client := New(transport).WithProjectID("project")
	result, err := client.BulkCleanupTasks(context.Background(), TaskBulkCleanupRequest{TaskIDs: []string{"az-1", "az-2"}, CloseOutcome: "cancelled", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if commandCount != 2 || len(result.Items) != 1 {
		t.Fatalf("commands = %d result = %+v", commandCount, result)
	}
}
