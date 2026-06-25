package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/pr"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

type recordingDaemonTransport struct {
	calls            []string
	requests         []string
	commandBudgets   []time.Duration
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

type refreshableDiffClient struct {
	paths       []string
	statusCalls int
}

type sessionOperationPayload struct {
	ProjectID  naming.ProjectID `json:"project_id"`
	SessionID  naming.SessionID `json:"session_id,omitempty"`
	BaseBranch string           `json:"base_branch,omitempty"`
	Yolo       bool             `json:"yolo,omitempty"`
	StartWork  *bool            `json:"start_work,omitempty"`
	ImagePaths []string         `json:"image_paths,omitempty"`
	Message    string           `json:"message,omitempty"`
}

func (r *refreshableDiffClient) Status(context.Context, string) (*git.GitStatus, error) {
	r.statusCalls++
	return &git.GitStatus{
		HasChanges: len(r.paths) > 0,
		Modified:   append([]string(nil), r.paths...),
	}, nil
}

func (r *refreshableDiffClient) ChangedFiles(context.Context, string, string) ([]git.ChangedFile, error) {
	files := make([]git.ChangedFile, 0, len(r.paths))
	for _, path := range r.paths {
		files = append(files, git.ChangedFile{Path: path, Status: git.DiffFileModified})
	}
	return files, nil
}

func (r *refreshableDiffClient) ChangedFilesLocalBase(ctx context.Context, worktree, baseBranch string) ([]git.ChangedFile, error) {
	return r.ChangedFiles(ctx, worktree, baseBranch)
}

func (*refreshableDiffClient) MergeBase(context.Context, string, string) (string, error) {
	return "abc123", nil
}

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

func emptyWorktreeListResponse(t *testing.T, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	t.Helper()
	respBody, err := json.Marshal(struct {
		ProjectID string `json:"project_id"`
		Worktrees []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		} `json:"worktrees"`
	}{
		ProjectID: req.Meta.ProjectID.String(),
		Worktrees: []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		}{},
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
	}
}

func decodeSessionOperationSubmit(t *testing.T, req protocol.RequestEnvelope, wantKind string) sessionOperationPayload {
	t.Helper()
	if req.Command != protocol.CommandOperationSubmit {
		t.Fatalf("command = %q, want %q", req.Command, protocol.CommandOperationSubmit)
	}
	var submit protocol.OperationSubmitRequestBody
	if err := json.Unmarshal(req.Body, &submit); err != nil {
		t.Fatalf("unmarshal operation submit request: %v", err)
	}
	if submit.Kind != wantKind {
		t.Fatalf("operation kind = %q, want %q", submit.Kind, wantKind)
	}
	var payload sessionOperationPayload
	if err := json.Unmarshal(submit.Payload, &payload); err != nil {
		t.Fatalf("unmarshal session operation payload: %v", err)
	}
	return payload
}

func sessionOperationSubmitResponse(t *testing.T, req protocol.RequestEnvelope, operationID, kind, issueID string, state protocol.OperationState) protocol.ResponseEnvelope {
	t.Helper()
	respBody, err := json.Marshal(protocol.OperationSubmitResponseBody{
		Created: true,
		Operation: protocol.OperationRecord{
			OperationID: naming.OperationID(operationID),
			ProjectID:   req.Meta.ProjectID,
			IssueID:     naming.IssueID(issueID),
			Kind:        kind,
			State:       state,
		},
	})
	if err != nil {
		t.Fatalf("marshal operation submit response: %v", err)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
		Body:            respBody,
	}
}

func followOnMergeCandidatesResponse(t *testing.T, req protocol.RequestEnvelope, taskID string, mergeTargetToBase bool, candidates []daemonclient.TaskFollowOnMergeCandidate) protocol.ResponseEnvelope {
	t.Helper()
	respBody, err := json.Marshal(struct {
		TaskID            string                                    `json:"task_id"`
		MergeTargetToBase bool                                      `json:"merge_target_to_base,omitempty"`
		Candidates        []daemonclient.TaskFollowOnMergeCandidate `json:"candidates"`
	}{
		TaskID:            taskID,
		MergeTargetToBase: mergeTargetToBase,
		Candidates:        candidates,
	})
	if err != nil {
		t.Fatalf("marshal follow-on merge candidates response: %v", err)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
		Body:            respBody,
	}
}

func (r *recordingDaemonTransport) Handshake(_ context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	r.calls = append(r.calls, "handshake")
	r.lastHello = hello
	return protocol.HelloAck{Accepted: true}, nil
}

func (r *recordingDaemonTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if deadline, ok := ctx.Deadline(); ok {
		r.commandBudgets = append(r.commandBudgets, time.Until(deadline))
	}
	r.calls = append(r.calls, req.Command)
	r.requests = append(r.requests, req.Command)
	if req.Command == daemonclient.CommandTaskFollowOnMerge {
		var body struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(req.Body, &body)
		taskID := strings.TrimSpace(body.TaskID)
		mergeTargetToBase := false
		candidates := []daemonclient.TaskFollowOnMergeCandidate{}
		switch taskID {
		case "az-child":
			candidates = []daemonclient.TaskFollowOnMergeCandidate{
				{IssueID: "az-parent", Title: "Parent epic", Status: domain.StatusInProgress, Relation: string(domain.DependencyParentChild), Order: 0, HasWorktree: true},
			}
		case "az-top":
			mergeTargetToBase = true
		}
		respBody, err := json.Marshal(struct {
			TaskID            string                                    `json:"task_id"`
			MergeTargetToBase bool                                      `json:"merge_target_to_base,omitempty"`
			Candidates        []daemonclient.TaskFollowOnMergeCandidate `json:"candidates"`
		}{
			TaskID:            taskID,
			MergeTargetToBase: mergeTargetToBase,
			Candidates:        candidates,
		})
		if err != nil {
			return protocol.ResponseEnvelope{}, err
		}
		return protocol.ResponseEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindResponse,
			OK:              true,
			Body:            respBody,
		}, nil
	}
	if r.replyFn != nil {
		return r.replyFn(req)
	}
	if req.Command == daemonclient.CommandTaskMergeBaseTarget {
		respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
			TargetID: mergeBaseTargetID,
			Branch:   "main",
		})
		if err != nil {
			return protocol.ResponseEnvelope{}, err
		}
		return protocol.ResponseEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindResponse,
			OK:              true,
			Body:            respBody,
		}, nil
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

func TestMoveTaskStatusCascadeChildrenCmdSendsDaemonCascadeOption(t *testing.T) {
	var statusBody daemonclient.TaskStatusRequest
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskUpdateStatus {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskUpdateStatus)
			}
			if err := json.Unmarshal(req.Body, &statusBody); err != nil {
				t.Fatalf("unmarshal status body: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Meta:            req.Meta,
				OK:              true,
				CompletedAt:     req.SentAt,
			}, nil
		},
	}
	m := newDaemonTestModel(transport)

	msg := m.moveTaskStatusCascadeChildrenCmd("az-1", domain.StatusInProgress, domain.StatusInReview)()
	result, ok := msg.(taskStatusResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("cascade status result = %#v", msg)
	}
	if statusBody.TaskID.String() != "az-1" || statusBody.Status != domain.StatusInReview || !statusBody.CascadeChildren {
		t.Fatalf("status body = %+v, want cascade in_review for az-1", statusBody)
	}
}

func TestSaveTaskCmdUpdatesEditedDescriptionThroughDaemon(t *testing.T) {
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskUpdate {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskUpdate)
			}
			if err := json.Unmarshal(req.Body, &updateBody); err != nil {
				t.Fatalf("unmarshal update body: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Meta:            req.Meta,
				OK:              true,
				CompletedAt:     req.SentAt,
			}, nil
		},
	}
	m := newDaemonTestModel(transport)

	cmd := m.saveTaskCmd(overlay.TaskCreatedMsg{
		ID:          "az-1",
		Title:       "Edited title",
		Description: "Edited description from TUI",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	if cmd == nil {
		t.Fatal("saveTaskCmd returned nil")
	}
	msg := cmd()
	result, ok := msg.(taskCreatedResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want taskCreatedResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("saveTaskCmd error = %v", result.err)
	}
	if !result.isUpdate || result.taskID != "az-1" {
		t.Fatalf("result = %+v, want update for az-1", result)
	}
	if updateBody.TaskID != "az-1" ||
		updateBody.Title != "Edited title" ||
		updateBody.Description != "Edited description from TUI" ||
		updateBody.Type != domain.TypeTask ||
		updateBody.Priority != domain.P2 {
		t.Fatalf("update body = %+v, want edited TUI fields", updateBody)
	}
}

func TestEditedTaskDetailsStayVisibleAcrossStaleHydration(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskUpdate {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskUpdate)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Meta:            req.Meta,
				OK:              true,
				CompletedAt:     req.SentAt,
			}, nil
		},
	}
	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{
		ID:          "az-1",
		Title:       "Original title",
		Description: "Original description",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
	}}

	submitted, saveCmd := m.Update(overlay.TaskCreatedMsg{
		ID:          "az-1",
		Title:       "Original title",
		Description: "Edited description from workspace form",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	if saveCmd == nil {
		t.Fatal("expected save command")
	}
	result := saveCmd()
	updatedAny, _ := submitted.Update(result)
	updated := updatedAny.(Model)
	if got := updated.tasks[0].Description; got != "Edited description from workspace form" {
		t.Fatalf("description after save result = %q, want edited description", got)
	}

	staleAny, _ := updated.Update(issuesLoadedMsg{
		tasks: []domain.Task{{
			ID:          "az-1",
			Title:       "Original title",
			Description: "Original description",
			Type:        domain.TypeTask,
			Priority:    domain.P2,
			Status:      domain.StatusOpen,
		}},
		revision: 42,
	})
	stale := staleAny.(Model)
	if got := stale.tasks[0].Description; got != "Edited description from workspace form" {
		t.Fatalf("description after stale hydration = %q, want edited description", got)
	}

	confirmedAny, _ := stale.Update(issuesLoadedMsg{
		tasks: []domain.Task{{
			ID:          "az-1",
			Title:       "Original title",
			Description: "Edited description from workspace form",
			Type:        domain.TypeTask,
			Priority:    domain.P2,
			Status:      domain.StatusOpen,
		}},
		revision: 43,
	})
	confirmed := confirmedAny.(Model)
	if got := confirmed.tasks[0].Description; got != "" {
		t.Fatalf("description after confirmed hydration = %q, want board summary without description", got)
	}
	if _, ok := confirmed.pendingDetails[taskIDKey("az-1")]; ok {
		t.Fatal("expected pending detail overlay to clear after confirmed hydration")
	}
}

func TestEditTaskSubmitSavesTitleAndClosesEditOverlay(t *testing.T) {
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskUpdate {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskUpdate)
			}
			if err := json.Unmarshal(req.Body, &updateBody); err != nil {
				t.Fatalf("unmarshal update body: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Meta:            req.Meta,
				OK:              true,
				CompletedAt:     req.SentAt,
			}, nil
		},
	}
	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{
		ID:       "az-1",
		Title:    "Original title",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	}}
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))
	m.overlayStack.Push(overlay.NewEditTaskOverlay(m.tasks[0]))

	modelAny, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model := modelAny.(Model)
	for _, r := range "Edited title" {
		modelAny, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = modelAny.(Model)
	}
	modelAny, submitCmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = modelAny.(Model)
	if submitCmd == nil {
		t.Fatal("expected submit command")
	}
	msgs := teaBatchMessages(submitCmd())
	if len(msgs) != 1 {
		t.Fatalf("submit messages = %d, want one save message: %#v", len(msgs), msgs)
	}

	modelAny, saveCmd := model.Update(msgs[0])
	model = modelAny.(Model)
	if saveCmd == nil {
		t.Fatal("expected save command")
	}
	result := saveCmd()
	modelAny, _ = model.Update(result)
	model = modelAny.(Model)

	if updateBody.TaskID != "az-1" || updateBody.Title != "Edited title" {
		t.Fatalf("update body = %+v, want edited title for az-1", updateBody)
	}
	if current := model.overlayStack.Current(); current != nil {
		t.Fatalf("current overlay = %T, want no stacked workspace after edit save", current)
	}
	if got := model.tasks[0].Title; got != "Edited title" {
		t.Fatalf("task title after save = %q, want edited title", got)
	}
}

func TestEditTaskActionLoadsFullDetailsBeforeSubmit(t *testing.T) {
	const issueID = "az-1"
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandTaskGet:
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalFullTaskSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
						{ID: naming.IssueID(issueID), Title: "Full title", Description: "stored description", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen},
					}),
				}, nil
			case daemonclient.CommandTaskUpdate:
				if err := json.Unmarshal(req.Body, &updateBody); err != nil {
					t.Fatalf("unmarshal update body: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}
	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{
		ID:       naming.IssueID(issueID),
		Title:    "Summary title",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	}}
	m.nav.SelectTask(issueID, 0)

	updatedAny, loadCmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	updated := updatedAny.(Model)
	if loadCmd == nil {
		t.Fatal("expected edit action to load full task detail")
	}
	loadedAny, openCmd := updated.Update(loadCmd())
	loaded := loadedAny.(Model)
	if openCmd == nil {
		t.Fatal("expected full detail load to open edit overlay")
	}
	_ = openCmd()
	if _, ok := loaded.overlayStack.Current().(*overlay.CreateTaskOverlay); !ok {
		t.Fatalf("current overlay = %T, want edit task overlay", loaded.overlayStack.Current())
	}

	submittedAny, submitCmd := loaded.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	submitted := submittedAny.(Model)
	if submitCmd == nil {
		t.Fatal("expected edit submit command")
	}
	msgs := teaBatchMessages(submitCmd())
	if len(msgs) != 1 {
		t.Fatalf("submit messages = %d, want one save message: %#v", len(msgs), msgs)
	}
	savingAny, saveCmd := submitted.Update(msgs[0])
	saving := savingAny.(Model)
	if saveCmd == nil {
		t.Fatal("expected save command")
	}
	savedAny, _ := saving.Update(saveCmd())
	saved := savedAny.(Model)

	if updateBody.TaskID != issueID || updateBody.Title != "Full title" || updateBody.Description != "stored description" {
		t.Fatalf("update body = %+v, want full detail fields preserved", updateBody)
	}
	if got := saved.tasks[0].Description; got != "stored description" {
		t.Fatalf("task description after save = %q, want stored description", got)
	}
	if got := transport.requests; len(got) != 2 || got[0] != daemonclient.CommandTaskGet || got[1] != daemonclient.CommandTaskUpdate {
		t.Fatalf("requests = %v", got)
	}
}

func TestEditTaskSubmitSavesTypedDescriptionAndClosesEditOverlay(t *testing.T) {
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskUpdate {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskUpdate)
			}
			if err := json.Unmarshal(req.Body, &updateBody); err != nil {
				t.Fatalf("unmarshal update body: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Meta:            req.Meta,
				OK:              true,
				CompletedAt:     req.SentAt,
			}, nil
		},
	}
	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{
		ID:       "az-1",
		Title:    "Original title",
		Type:     domain.TypeTask,
		Priority: domain.P2,
		Status:   domain.StatusOpen,
	}}
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))
	m.overlayStack.Push(overlay.NewEditTaskOverlay(m.tasks[0]))

	modelAny, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	model := modelAny.(Model)
	for _, r := range "Typed description" {
		modelAny, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = modelAny.(Model)
	}
	modelAny, submitCmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = modelAny.(Model)
	if submitCmd == nil {
		t.Fatal("expected submit command")
	}
	msgs := teaBatchMessages(submitCmd())
	if len(msgs) != 1 {
		t.Fatalf("submit messages = %d, want one save message: %#v", len(msgs), msgs)
	}

	modelAny, saveCmd := model.Update(msgs[0])
	model = modelAny.(Model)
	if saveCmd == nil {
		t.Fatal("expected save command")
	}
	result := saveCmd()
	modelAny, _ = model.Update(result)
	model = modelAny.(Model)

	if updateBody.TaskID != "az-1" || updateBody.Description != "Typed description" {
		t.Fatalf("update body = %+v, want typed description for az-1", updateBody)
	}
	if current := model.overlayStack.Current(); current != nil {
		t.Fatalf("current overlay = %T, want no stacked workspace after edit save", current)
	}
	if got := model.tasks[0].Description; got != "Typed description" {
		t.Fatalf("task description after save = %q, want typed description", got)
	}
}

func TestEditTaskTabOutThenEnterSavesTypedDescriptionAndClosesEditOverlay(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exitKey   tea.KeyMsg
		wantValue string
	}{
		{
			name:      "tab_to_type",
			exitKey:   tea.KeyMsg{Type: tea.KeyTab},
			wantValue: "Typed description via tab enter",
		},
		{
			name:      "shift_tab_to_title",
			exitKey:   tea.KeyMsg{Type: tea.KeyShiftTab},
			wantValue: "Typed description via shift tab enter",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var updateBody struct {
				TaskID string `json:"task_id"`
				daemonclient.TaskUpdateParams
			}
			transport := &recordingDaemonTransport{
				replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					if req.Command != daemonclient.CommandTaskUpdate {
						t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskUpdate)
					}
					if err := json.Unmarshal(req.Body, &updateBody); err != nil {
						t.Fatalf("unmarshal update body: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				},
			}
			m := newDaemonTestModel(transport)
			m.tasks = []domain.Task{{
				ID:       "az-1",
				Title:    "Original title",
				Type:     domain.TypeTask,
				Priority: domain.P2,
				Status:   domain.StatusOpen,
			}}
			m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))
			m.overlayStack.Push(overlay.NewEditTaskOverlay(m.tasks[0]))

			modelAny, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			model := modelAny.(Model)
			for _, r := range tc.wantValue {
				modelAny, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				model = modelAny.(Model)
			}
			modelAny, _ = model.Update(tc.exitKey)
			model = modelAny.(Model)
			modelAny, submitCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = modelAny.(Model)
			if submitCmd == nil {
				t.Fatal("expected submit command")
			}
			msgs := teaBatchMessages(submitCmd())
			if len(msgs) != 1 {
				t.Fatalf("submit messages = %d, want one save message: %#v", len(msgs), msgs)
			}

			modelAny, saveCmd := model.Update(msgs[0])
			model = modelAny.(Model)
			if saveCmd == nil {
				t.Fatal("expected save command")
			}
			result := saveCmd()
			modelAny, _ = model.Update(result)
			model = modelAny.(Model)

			if updateBody.TaskID != "az-1" || updateBody.Description != tc.wantValue {
				t.Fatalf("update body = %+v, want typed description for az-1", updateBody)
			}
			if current := model.overlayStack.Current(); current != nil {
				t.Fatalf("current overlay = %T, want no stacked workspace after edit save", current)
			}
			if got := model.tasks[0].Description; got != tc.wantValue {
				t.Fatalf("task description after save = %q, want %q", got, tc.wantValue)
			}
		})
	}
}

func teaBatchMessages(msg tea.Msg) []tea.Msg {
	if msg == nil {
		return nil
	}
	switch batch := msg.(type) {
	case tea.BatchMsg:
		msgs := make([]tea.Msg, 0, len(batch))
		for _, cmd := range batch {
			if cmd != nil {
				msgs = append(msgs, cmd())
			}
		}
		return msgs
	default:
		return []tea.Msg{msg}
	}
}

func mustMarshalBoardSnapshot(t *testing.T, protocolVersion protocol.Version, revision uint64, projectID string, tasks []domain.Task) []byte {
	t.Helper()
	body, err := json.Marshal(protocol.BoardSnapshotPayload{
		SchemaVersion:    protocol.BoardSnapshotSchemaVersion,
		ProtocolVersion:  protocolVersion,
		SnapshotRevision: revision,
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    daemonSnapshotCheckedAt(),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            protocol.BoardTaskSummariesFromDomain(tasks),
	})
	if err != nil {
		t.Fatalf("marshal board snapshot: %v", err)
	}
	return body
}

func mustMarshalFullTaskSnapshot(t *testing.T, protocolVersion protocol.Version, revision uint64, projectID string, tasks []domain.Task) []byte {
	t.Helper()
	body, err := json.Marshal(protocol.TaskListSnapshotPayload{
		SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
		ProtocolVersion:  protocolVersion,
		SnapshotRevision: revision,
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    daemonSnapshotCheckedAt(),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            tasks,
	})
	if err != nil {
		t.Fatalf("marshal full task snapshot: %v", err)
	}
	return body
}

func daemonSnapshotCheckedAt() time.Time {
	return time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
}

func setTaskSession(t *testing.T, m *Model, issueID string, session *domain.Session) {
	t.Helper()
	for i := range m.tasks {
		if m.tasks[i].ID.String() != issueID {
			continue
		}
		m.tasks[i].Session = cloneSession(session)
		m.tasks[i].HasTmuxSession = session != nil
		return
	}
	t.Fatalf("task %q not found", issueID)
}

func TestTaskCommandsUseDaemonClient(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandBoardFetch {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandBoardFetch)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            mustMarshalBoardSnapshot(t, req.ProtocolVersion, 0, req.Meta.ProjectID.String(), []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}}),
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
		if !loaded.lastCheckedAt.Equal(daemonSnapshotCheckedAt()) {
			t.Fatalf("last_checked_at = %v, want %v", loaded.lastCheckedAt, daemonSnapshotCheckedAt())
		}
		if loaded.freshness != protocol.TaskListFreshnessFresh {
			t.Fatalf("freshness = %q, want %q", loaded.freshness, protocol.TaskListFreshnessFresh)
		}
		if len(transport.requests) != 1 || transport.requests[0] != daemonclient.CommandBoardFetch {
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
				case daemonclient.CommandWorktreeList:
					return emptyWorktreeListResponse(t, req), nil
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

func TestTaskStatusExactKeyUsesDaemonClient(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskUpdateStatus {
				t.Fatalf("unexpected command: %s", req.Command)
			}
			var body daemonclient.TaskStatusRequest
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal status request: %v", err)
			}
			if body.TaskID != "az-1" || body.Status != domain.StatusInReview {
				t.Fatalf("status body = %+v, want az-1 -> blocked", body)
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
	m.tasks = []domain.Task{{ID: "az-1", Status: domain.StatusOpen}}
	m.nav.SelectTask("az-1", 0)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "3"})
	if cmd == nil {
		t.Fatal("expected exact status command")
	}
	optimistic := updated.(Model)
	if optimistic.tasks[0].Status != domain.StatusInReview {
		t.Fatalf("optimistic status = %s, want %s", optimistic.tasks[0].Status, domain.StatusInReview)
	}
	pending, ok := optimistic.pendingStatuses[taskIDKey("az-1")]
	if !ok {
		t.Fatal("expected pending status marker immediately after exact-key move")
	}
	if pending.previousStatus != domain.StatusOpen || pending.targetStatus != domain.StatusInReview || pending.state != protocol.OperationStateQueued {
		t.Fatalf("pending status = %+v, want open -> review queued", pending)
	}

	result := cmd()
	status, ok := result.(taskStatusResultMsg)
	if !ok {
		t.Fatalf("result = %T, want taskStatusResultMsg", result)
	}
	if status.previousStatus != domain.StatusOpen || status.newStatus != domain.StatusInReview || status.err != nil {
		t.Fatalf("status result = %#v", status)
	}
}

func TestTaskStatusDoneRequiresCloseCleanupConfirmation(t *testing.T) {
	var closeBody struct {
		TaskID               string `json:"task_id"`
		IntegrateBeforeClose bool   `json:"integrate_before_close"`
	}
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command == daemonclient.CommandBoardFetch {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
						{ID: "az-4", Status: domain.StatusInReview},
					}),
				}, nil
			}
			if req.Command == daemonclient.CommandWorktreeList {
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
					}{},
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
			}
			if req.Command != daemonclient.CommandTaskClose {
				t.Fatalf("unexpected command: %s", req.Command)
			}
			if err := json.Unmarshal(req.Body, &closeBody); err != nil {
				t.Fatalf("unmarshal close request: %v", err)
			}
			respBody, err := json.Marshal(daemonclient.TaskCloseResult{
				TaskID: "az-4",
				Status: string(domain.StatusDone),
			})
			if err != nil {
				t.Fatalf("marshal close response: %v", err)
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
	m.tasks = []domain.Task{{ID: "az-4", Status: domain.StatusInReview}}
	m.nav.SelectTask("az-4", domain.StatusInReview.Column())

	updatedAny, promptCmd := m.handleSelection(overlay.SelectionMsg{Key: "l"})
	if promptCmd == nil {
		t.Fatal("expected confirmation overlay command")
	}
	prompted := updatedAny.(Model)
	if prompted.pendingClose == nil {
		t.Fatal("expected pending close confirmation")
	}
	if prompted.tasks[0].Status != domain.StatusInReview {
		t.Fatalf("status before confirmation = %s, want %s", prompted.tasks[0].Status, domain.StatusInReview)
	}
	if len(transport.requests) != 0 {
		t.Fatalf("daemon requests before confirmation = %v, want none", transport.requests)
	}

	confirmedAny, statusCmd := prompted.handleSelection(overlay.SelectionMsg{Key: "yes"})
	if statusCmd == nil {
		t.Fatal("expected status update command after confirmation")
	}
	confirmed := confirmedAny.(Model)
	if confirmed.pendingClose != nil {
		t.Fatal("pending close confirmation was not cleared")
	}
	if confirmed.tasks[0].Status != domain.StatusDone {
		t.Fatalf("optimistic status after confirmation = %s, want %s", confirmed.tasks[0].Status, domain.StatusDone)
	}
	pending, ok := confirmed.pendingStatuses[taskIDKey("az-4")]
	if !ok {
		t.Fatal("expected pending status marker immediately after close confirmation")
	}
	if pending.previousStatus != domain.StatusInReview || pending.targetStatus != domain.StatusDone || pending.state != protocol.OperationStateQueued {
		t.Fatalf("pending status = %+v, want review -> done queued", pending)
	}

	msg := statusCmd()
	status, ok := msg.(taskStatusResultMsg)
	if !ok {
		t.Fatalf("result = %T, want taskStatusResultMsg", msg)
	}
	if status.previousStatus != domain.StatusInReview || status.newStatus != domain.StatusDone || status.err != nil {
		t.Fatalf("status result = %#v", status)
	}
	if closeBody.TaskID != "az-4" {
		t.Fatalf("close body = %+v, want az-4", closeBody)
	}
	if !closeBody.IntegrateBeforeClose {
		t.Fatalf("close body = %+v, want integrate_before_close", closeBody)
	}
	if got := transport.requests; len(got) != 1 ||
		got[0] != daemonclient.CommandTaskClose {
		t.Fatalf("daemon requests after confirmation = %v", got)
	}
}

func TestTaskStatusDoneUsesExtendedCloseTimeout(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandTaskClose {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskClose)
			}
			respBody, err := json.Marshal(daemonclient.TaskCloseResult{
				TaskID: "az-4",
				Status: string(domain.StatusDone),
			})
			if err != nil {
				t.Fatalf("marshal close response: %v", err)
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

	msg := m.moveTaskStatusCmd("az-4", domain.StatusInReview, domain.StatusDone)()
	status, ok := msg.(taskStatusResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want taskStatusResultMsg", msg)
	}
	if status.err != nil {
		t.Fatalf("status err = %v", status.err)
	}
	if len(transport.commandBudgets) != 1 {
		t.Fatalf("command budget count = %d, want 1", len(transport.commandBudgets))
	}
	if got := transport.commandBudgets[0]; got < taskCloseMutationTimeout-10*time.Second {
		t.Fatalf("close timeout budget = %s, want near %s", got, taskCloseMutationTimeout)
	}
}

func TestTaskStatusDoneSuccessKeepsOptimisticOverlayAcrossStaleHydration(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandBoardFetch:
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
						{ID: "az-4", Status: domain.StatusInReview},
					}),
				}, nil
			case daemonclient.CommandWorktreeList:
				respBody, err := json.Marshal(struct {
					ProjectID string `json:"project_id"`
					Worktrees []struct {
						Path    string `json:"path"`
						Branch  string `json:"branch"`
						IssueID string `json:"issue_id"`
					} `json:"worktrees"`
				}{ProjectID: "default"})
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
			case daemonclient.CommandTaskClose:
				respBody, err := json.Marshal(daemonclient.TaskCloseResult{
					TaskID: "az-4",
					Status: string(domain.StatusDone),
				})
				if err != nil {
					t.Fatalf("marshal close response: %v", err)
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

	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{ID: "az-4", Status: domain.StatusInReview}}
	m.nav.SelectTask("az-4", domain.StatusInReview.Column())

	promptedAny, promptCmd := m.handleSelection(overlay.SelectionMsg{Key: "4"})
	if promptCmd == nil {
		t.Fatal("expected close confirmation command")
	}
	prompted := promptedAny.(Model)
	confirmedAny, statusCmd := prompted.handleSelection(overlay.SelectionMsg{Key: "yes"})
	if statusCmd == nil {
		t.Fatal("expected status command after confirmation")
	}
	confirmed := confirmedAny.(Model)
	statusMsg := statusCmd()
	afterStatusAny, refreshCmd := confirmed.Update(statusMsg)
	if refreshCmd == nil {
		t.Fatal("expected refresh after successful close")
	}
	afterStatus := afterStatusAny.(Model)
	if afterStatus.tasks[0].Status != domain.StatusDone {
		t.Fatalf("status after close success = %s, want %s", afterStatus.tasks[0].Status, domain.StatusDone)
	}
	if _, ok := afterStatus.pendingStatuses[taskIDKey("az-4")]; !ok {
		t.Fatal("pending done overlay should remain until hydration confirms done")
	}

	staleAny, _ := afterStatus.Update(issuesLoadedMsg{
		tasks: []domain.Task{{ID: "az-4", Status: domain.StatusInReview}},
	})
	stale := staleAny.(Model)
	if stale.tasks[0].Status != domain.StatusDone {
		t.Fatalf("stale hydration status = %s, want optimistic %s", stale.tasks[0].Status, domain.StatusDone)
	}
	if _, ok := stale.pendingStatuses[taskIDKey("az-4")]; !ok {
		t.Fatal("pending done overlay should survive stale hydration")
	}

	confirmedHydrationAny, _ := stale.Update(issuesLoadedMsg{
		tasks: []domain.Task{{ID: "az-4", Status: domain.StatusDone}},
	})
	confirmedHydration := confirmedHydrationAny.(Model)
	if confirmedHydration.tasks[0].Status != domain.StatusDone {
		t.Fatalf("confirmed hydration status = %s, want %s", confirmedHydration.tasks[0].Status, domain.StatusDone)
	}
	if _, ok := confirmedHydration.pendingStatuses[taskIDKey("az-4")]; ok {
		t.Fatal("pending done overlay should clear after hydration confirms done")
	}
}

func TestTaskStatusDonePendingOverlaySurvivesCloseOperationDoneBeforeHydration(t *testing.T) {
	m := newTestModel()
	m.tasks = []domain.Task{{ID: "az-4", Status: domain.StatusDone}}
	m.pendingStatuses = map[string]pendingTaskStatus{
		taskIDKey("az-4"): {
			previousStatus: domain.StatusInReview,
			targetStatus:   domain.StatusDone,
			operationID:    "op-close",
			state:          protocol.OperationStateRunning,
			action:         "task_move",
			updatedAt:      time.Now(),
		},
	}

	body, err := json.Marshal(protocol.OperationEventBody{
		Operation: protocol.OperationRecord{
			OperationID: "op-close",
			IssueID:     naming.IssueID("az-4"),
			State:       protocol.OperationStateDone,
		},
	})
	if err != nil {
		t.Fatalf("marshal close done event: %v", err)
	}

	m.applyOperationProgressEvent(protocol.EventEnvelope{
		Event: protocol.EventOperationDone,
		Body:  body,
	})
	if _, ok := m.pendingStatuses[taskIDKey("az-4")]; !ok {
		t.Fatal("pending done overlay should survive task.close operation.done until task status confirms closed")
	}

	staleAny, _ := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{{ID: "az-4", Status: domain.StatusInReview}},
	})
	stale := staleAny.(Model)
	if stale.tasks[0].Status != domain.StatusDone {
		t.Fatalf("stale hydration status = %s, want optimistic %s", stale.tasks[0].Status, domain.StatusDone)
	}
	if _, ok := stale.pendingStatuses[taskIDKey("az-4")]; !ok {
		t.Fatal("pending done overlay should survive stale hydration after close operation.done")
	}

	confirmedAny, _ := stale.Update(issuesLoadedMsg{
		tasks: []domain.Task{{ID: "az-4", Status: domain.StatusDone}},
	})
	confirmed := confirmedAny.(Model)
	if _, ok := confirmed.pendingStatuses[taskIDKey("az-4")]; ok {
		t.Fatal("pending done overlay should clear once hydration confirms closed")
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
	if _, ok := optimistic.pendingStatuses[taskIDKey("az-1")]; !ok {
		t.Fatal("expected pending status marker before daemon result")
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
	if _, ok := rolledBackModel.pendingStatuses[taskIDKey("az-1")]; ok {
		t.Fatal("pending status marker should be cleared after rollback")
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

func TestIssuesLoadedHydratesFreshnessMetadataAndSyncsWorkspace(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))

	checkedAt := time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
	updatedAny, _ := m.Update(issuesLoadedMsg{
		tasks:         append([]domain.Task(nil), m.tasks...),
		lastCheckedAt: checkedAt,
		freshness:     protocol.TaskListFreshnessStale,
	})

	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if !updated.taskSnapshotCheckedAt.Equal(checkedAt) {
		t.Fatalf("taskSnapshotCheckedAt = %v, want %v", updated.taskSnapshotCheckedAt, checkedAt)
	}
	if updated.taskSnapshotFreshness != protocol.TaskListFreshnessStale {
		t.Fatalf("taskSnapshotFreshness = %q, want %q", updated.taskSnapshotFreshness, protocol.TaskListFreshnessStale)
	}

	current := updated.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay, got %T", current)
	}
	view := workspace.View()
	if !strings.Contains(view, "Freshness:") || !strings.Contains(view, "stale") {
		t.Fatalf("workspace view = %q, want freshness row", view)
	}
	if !strings.Contains(view, "Checked:") || !strings.Contains(view, "2026-04-02 11:02:00") {
		t.Fatalf("workspace view = %q, want checked timestamp", view)
	}
}

func TestTaskSnapshotReadPathsUseExplicitTimeoutBudget(t *testing.T) {
	const (
		defaultBudget  = 25 * time.Millisecond
		explicitBudget = 250 * time.Millisecond
	)

	replyWithSnapshot := func(t *testing.T, transport *recordingDaemonTransport) {
		t.Helper()
		transport.replyFn = func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Revision:        11,
				OK:              true,
				Body:            mustMarshalBoardSnapshot(t, req.ProtocolVersion, 11, req.Meta.ProjectID.String(), []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}}),
			}, nil
		}
	}

	assertExplicitBudget := func(t *testing.T, budgets []time.Duration) {
		t.Helper()
		if len(budgets) != 1 {
			t.Fatalf("command budget count = %d, want 1", len(budgets))
		}
		if budgets[0] < explicitBudget/2 {
			t.Fatalf("command budget = %s, want explicit budget near %s", budgets[0], explicitBudget)
		}
		if budgets[0] <= defaultBudget {
			t.Fatalf("command budget = %s, want greater than default budget %s", budgets[0], defaultBudget)
		}
	}

	t.Run("load issues uses explicit snapshot budget", func(t *testing.T) {
		transport := &recordingDaemonTransport{}
		replyWithSnapshot(t, transport)

		m := newTestModel()
		m.currentProject = "proj-read"
		m.daemonClient = daemonclient.New(transport).
			WithProjectID("proj-read").
			WithReadWaitPolicy(daemonclient.ReadWaitPolicy{
				Default:  defaultBudget,
				Explicit: explicitBudget,
			})

		msg := m.loadIssuesCmd()()
		if _, ok := msg.(issuesLoadedMsg); !ok {
			t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
		}
		assertExplicitBudget(t, transport.commandBudgets)
	})

	t.Run("scoped attach propagates explicit snapshot budget", func(t *testing.T) {
		transport := &recordingDaemonTransport{}
		replyWithSnapshot(t, transport)

		previousFactory := newScopedDaemonClient
		defer func() { newScopedDaemonClient = previousFactory }()

		var capturedPolicy daemonclient.ReadWaitPolicy
		newScopedDaemonClient = func(socketPath, projectID string, readWaitPolicy daemonclient.ReadWaitPolicy) *daemonclient.Client {
			capturedPolicy = readWaitPolicy
			return daemonclient.New(transport).
				WithProjectID(projectID).
				WithReadWaitPolicy(readWaitPolicy)
		}

		m := newTestModel()
		m.currentProject = "proj-read"
		m.repoDir = t.TempDir()
		m.daemonSocketPath = filepath.Join(t.TempDir(), "other.sock")
		m.daemonClient = daemonclient.New(&recordingDaemonTransport{}).
			WithProjectID("proj-read").
			WithReadWaitPolicy(daemonclient.ReadWaitPolicy{
				Default:  defaultBudget,
				Explicit: explicitBudget,
			})

		msg := m.attachDaemonCmd()()
		if _, ok := msg.(issuesLoadedMsg); !ok {
			t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
		}
		if capturedPolicy.Default != defaultBudget || capturedPolicy.Explicit != explicitBudget {
			t.Fatalf("captured policy = %+v, want default=%s explicit=%s", capturedPolicy, defaultBudget, explicitBudget)
		}
		assertExplicitBudget(t, transport.commandBudgets)
	})
}

func TestDaemonAttachFlowUsesHandshakeSnapshotSubscribe(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandBoardFetch {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandBoardFetch)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Revision:        8,
				OK:              true,
				Body:            mustMarshalBoardSnapshot(t, req.ProtocolVersion, 8, req.Meta.ProjectID.String(), []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}}),
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
	if got := transport.calls; len(got) != 3 || got[0] != "handshake" || got[1] != daemonclient.CommandBoardFetch || got[2] != "subscribe" {
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

func TestDaemonAttachFlowPropagatesRuntimeProjectionAcrossGitWorktreeSessionAndAgent(t *testing.T) {
	const projectID = "proj-runtime"

	initialStartedAt := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
	gitUpdatedAt := time.Date(2026, time.April, 1, 10, 5, 0, 0, time.UTC)
	worktreeUpdatedAt := time.Date(2026, time.April, 1, 10, 10, 0, 0, time.UTC)
	sessionUpdatedAt := time.Date(2026, time.April, 1, 10, 15, 0, 0, time.UTC)

	makeProjectionBody := func(revision uint64, issueID, worktreePath string, gitAdditions, gitDeletions, gitAhead, gitBehind int, sessionState protocol.SessionLifecycleState, updatedAt time.Time, activeOperation *protocol.RuntimeOperationProjection) []byte {
		body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
			ProjectID: naming.ProjectID(projectID),
			IssueID:   naming.IssueID(issueID),
			Worktree:  worktreePath,
			UpdatedAt: updatedAt,
			Runtime: &protocol.RuntimeProjectionEventBody{
				ProjectID: naming.ProjectID(projectID),
				Revision:  revision,
				Projection: protocol.RuntimeProjection{
					ProjectID: naming.ProjectID(projectID),
					IssueID:   naming.IssueID(issueID),
					Worktree: protocol.RuntimeWorktreeProjection{
						Exists:             worktreePath != "",
						Path:               worktreePath,
						Branch:             "riordan/az-1/task",
						Healthy:            worktreePath != "",
						GitStatusUpdatedAt: &updatedAt,
					},
					Git: protocol.RuntimeGitProjection{
						HasUncommittedChanges: gitAdditions > 0 || gitDeletions > 0,
						GitAdditions:          gitAdditions,
						GitDeletions:          gitDeletions,
						GitAheadCount:         gitAhead,
						GitBehindCount:        gitBehind,
						ActiveOperation:       activeOperation,
					},
					Session: protocol.RuntimeSessionProjection{
						HasSession: true,
						SessionID:  "sess-1",
						State:      sessionState,
						StartedAt:  &initialStartedAt,
						UpdatedAt:  &updatedAt,
						Worktree:   worktreePath,
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal runtime projection event: %v", err)
		}
		return body
	}

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandBoardFetch {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandBoardFetch)
			}
			tasks := []domain.Task{
				{
					ID:       "az-1",
					Title:    "RT",
					Status:   domain.StatusOpen,
					Priority: domain.P2,
					Type:     domain.TypeTask,
				},
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Revision:        7,
				OK:              true,
				Body:            mustMarshalBoardSnapshot(t, req.ProtocolVersion, 7, req.Meta.ProjectID.String(), tasks),
			}, nil
		},
		subscribeFn: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
			ch := make(chan protocol.EventEnvelope, 3)
			ch <- protocol.EventEnvelope{
				Revision: 8,
				Event:    protocol.EventGitStatusUpdated,
				Body: makeProjectionBody(8, "az-1", "/tmp/repo-az-1", 3, 1, 2, 1, protocol.SessionLifecycleStateAttached, gitUpdatedAt, &protocol.RuntimeOperationProjection{
					OperationID:     "op-git",
					State:           protocol.OperationStateRunning,
					ProgressPercent: 40,
					Message:         "git status sync",
				}),
			}
			ch <- protocol.EventEnvelope{
				Revision: 9,
				Event:    protocol.EventWorktreeProjectionUpdated,
				Body: makeProjectionBody(9, "az-1", "/tmp/repo-az-1-alt", 5, 2, 2, 1, protocol.SessionLifecycleStateAttached, worktreeUpdatedAt, &protocol.RuntimeOperationProjection{
					OperationID:     "op-worktree",
					State:           protocol.OperationStateRunning,
					ProgressPercent: 75,
					Message:         "worktree update",
				}),
			}
			sessionBody, err := json.Marshal(protocol.SessionProjectionEventBody{
				ProjectID: naming.ProjectID(projectID),
				Revision:  10,
				Session: protocol.SessionProjection{
					SessionID: "sess-1",
					IssueID:   "az-1",
					State:     protocol.SessionLifecycleStatePaused,
					UpdatedAt: sessionUpdatedAt,
				},
				Runtime: &protocol.RuntimeProjectionEventBody{
					ProjectID: naming.ProjectID(projectID),
					Revision:  10,
					Projection: protocol.RuntimeProjection{
						ProjectID: naming.ProjectID(projectID),
						IssueID:   "az-1",
						Worktree: protocol.RuntimeWorktreeProjection{
							Exists:             true,
							Path:               "/tmp/repo-az-1-alt",
							Branch:             "riordan/az-1/task",
							Healthy:            true,
							GitStatusUpdatedAt: &sessionUpdatedAt,
						},
						Git: protocol.RuntimeGitProjection{
							HasUncommittedChanges: false,
							GitAheadCount:         0,
							GitBehindCount:        0,
						},
						Session: protocol.RuntimeSessionProjection{
							HasSession: true,
							SessionID:  "sess-1",
							State:      protocol.SessionLifecycleStatePaused,
							StartedAt:  &initialStartedAt,
							UpdatedAt:  &sessionUpdatedAt,
							Worktree:   "/tmp/repo-az-1-alt",
						},
					},
				},
			})
			if err != nil {
				t.Fatalf("marshal session projection event: %v", err)
			}
			ch <- protocol.EventEnvelope{
				Revision: 10,
				Event:    protocol.EventSessionUpdated,
				Body:     sessionBody,
			}
			close(ch)
			return ch, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.currentProject = projectID

	loadedMsg := m.attachDaemonCmd()()
	loaded, ok := loadedMsg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", loadedMsg)
	}
	if loaded.revision != 7 {
		t.Fatalf("snapshot revision = %d, want 7", loaded.revision)
	}
	if loaded.events == nil {
		t.Fatal("expected daemon stream subscription")
	}
	if transport.subscribeProject != projectID {
		t.Fatalf("subscribe project = %q, want %q", transport.subscribeProject, projectID)
	}
	if transport.subscribeFrom != 7 {
		t.Fatalf("subscribe from revision = %d, want 7", transport.subscribeFrom)
	}

	updatedAny, _ := m.Update(loaded)
	model, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if model.daemonRevision != 7 {
		t.Fatalf("daemon revision after snapshot = %d, want 7", model.daemonRevision)
	}
	model.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(model.tasks[0], model.tasks, nil, 120, 30))

	workspace := func(model Model) *overlay.TaskWorkspaceOverlay {
		current := model.overlayStack.Current()
		got, ok := current.(*overlay.TaskWorkspaceOverlay)
		if !ok {
			t.Fatalf("expected TaskWorkspaceOverlay, got %T", current)
		}
		return got
	}
	boardView := func(model Model) string {
		task := model.tasks[0]
		columns := []board.Column{{Title: "Open", Tasks: []domain.Task{task}}}
		return board.Render(
			columns,
			board.Cursor{Column: 0, Task: 0},
			map[string]bool{},
			model.runtimeSignalsForBoard(),
			board.BuildChildProgress([]domain.Task{task}),
			nil,
			false,
			nil,
			0,
			styles.New(),
			120,
			20,
		)
	}

	eventChecks := []struct {
		name     string
		event    string
		assertFn func(t *testing.T, model Model, evt protocol.EventEnvelope)
	}{
		{
			name:  "git",
			event: protocol.EventGitStatusUpdated,
			assertFn: func(t *testing.T, model Model, evt protocol.EventEnvelope) {
				var body protocol.ProjectionUpdateEventBody
				if err := json.Unmarshal(evt.Body, &body); err != nil {
					t.Fatalf("unmarshal git projection body: %v", err)
				}
				if body.Runtime == nil || body.Runtime.Projection.Session.State != protocol.SessionLifecycleStateAttached {
					t.Fatalf("git runtime session = %+v, want attached", body.Runtime)
				}
				if model.daemonRevision != 8 {
					t.Fatalf("daemon revision after git event = %d, want 8", model.daemonRevision)
				}
				runtime := model.runtimeSignalsForBoard()["az-1"]
				if !runtime.HasTmuxSession || !runtime.HasWorktree || !runtime.HasUncommittedChanges {
					t.Fatalf("runtime signals after git event = %+v, want session/worktree/dirty", runtime)
				}
				if runtime.PendingOperationID != "op-git" || runtime.PendingOperationState != string(protocol.OperationStateRunning) || runtime.PendingOperationPercent != 40 {
					t.Fatalf("runtime pending op after git event = %+v, want running op-git 40%%", runtime)
				}
				view := boardView(model)
				if !strings.Contains(view, " T ") || !strings.Contains(view, " ✓ ") || !strings.Contains(view, " ↑2 ") || !strings.Contains(view, " ↓1 ") || !strings.Contains(view, " ✎ ") || !strings.Contains(view, "+3/-1") || !strings.Contains(view, "M:running(40%)") {
					t.Fatalf("board view after git event = %q, missing projected runtime tokens", view)
				}
				workspaceView := workspace(model).View()
				if !strings.Contains(workspaceView, "Runtime") || !strings.Contains(workspaceView, "dirty (+3/-1; ↑2/↓1)") {
					t.Fatalf("workspace view after git event = %q, missing projected git summary", workspaceView)
				}
			},
		},
		{
			name:  "worktree",
			event: protocol.EventWorktreeProjectionUpdated,
			assertFn: func(t *testing.T, model Model, evt protocol.EventEnvelope) {
				var body protocol.ProjectionUpdateEventBody
				if err := json.Unmarshal(evt.Body, &body); err != nil {
					t.Fatalf("unmarshal worktree projection body: %v", err)
				}
				if body.Runtime == nil || body.Runtime.Projection.Session.State != protocol.SessionLifecycleStateAttached {
					t.Fatalf("worktree runtime session = %+v, want attached", body.Runtime)
				}
				if model.daemonRevision != 9 {
					t.Fatalf("daemon revision after worktree event = %d, want 9", model.daemonRevision)
				}
				if got := strings.TrimSpace(model.runtimeSignalWorktreeByTask["az-1"]); got != "/tmp/repo-az-1-alt" {
					t.Fatalf("runtime worktree map = %q, want /tmp/repo-az-1-alt", got)
				}
				if got := model.resolveOperationTaskID("/tmp/repo-az-1-alt", nil); got != "az-1" {
					t.Fatalf("resolveOperationTaskID(worktree path) = %q, want az-1", got)
				}
				runtime := model.runtimeSignalsForBoard()["az-1"]
				if !runtime.HasWorktree || runtime.GitAdditions != 5 || runtime.GitDeletions != 2 {
					t.Fatalf("runtime signals after worktree event = %+v, want worktree and refreshed diff stat", runtime)
				}
				view := boardView(model)
				if !strings.Contains(view, " ✓ ") || !strings.Contains(view, "+5/-2") {
					t.Fatalf("board view after worktree event = %q, missing refreshed worktree/diff tokens", view)
				}
				workspaceView := workspace(model).View()
				if !strings.Contains(workspaceView, "dirty (+5/-2; ↑2/↓1)") {
					t.Fatalf("workspace view after worktree event = %q, missing refreshed git summary", workspaceView)
				}
			},
		},
		{
			name:  "session",
			event: protocol.EventSessionUpdated,
			assertFn: func(t *testing.T, model Model, evt protocol.EventEnvelope) {
				var body protocol.SessionProjectionEventBody
				if err := json.Unmarshal(evt.Body, &body); err != nil {
					t.Fatalf("unmarshal session projection body: %v", err)
				}
				if body.Runtime == nil || body.Runtime.Projection.Session.State != protocol.SessionLifecycleStatePaused {
					t.Fatalf("session runtime session = %+v, want paused", body.Runtime)
				}
				if model.daemonRevision != 10 {
					t.Fatalf("daemon revision after session event = %d, want 10", model.daemonRevision)
				}
				task := model.tasks[0]
				if task.Session == nil || task.Session.State != domain.SessionPaused {
					t.Fatalf("task session after session event = %+v, want paused", task.Session)
				}
				runtime := model.runtimeSignalsForBoard()["az-1"]
				if !runtime.HasTmuxSession {
					t.Fatalf("runtime signals after session event = %+v, want tmux session", runtime)
				}
				view := boardView(model)
				if !strings.Contains(view, "⏸ P") {
					t.Fatalf("board view after session event = %q, missing paused session state", view)
				}
				workspaceView := workspace(model).View()
				if !strings.Contains(workspaceView, "paused") {
					t.Fatalf("workspace view after session event = %q, missing paused session state", workspaceView)
				}
			},
		},
	}

	batchMsg := model.waitForDaemonEventCmd()()
	batch, ok := batchMsg.(daemonStreamEventMsg)
	if !ok {
		t.Fatalf("stream message type = %T, want daemonStreamEventMsg", batchMsg)
	}
	if len(batch.events) != len(eventChecks) {
		t.Fatalf("batched event count = %d, want %d", len(batch.events), len(eventChecks))
	}
	for i, tt := range eventChecks {
		t.Run(tt.name, func(t *testing.T) {
			evt := daemonStreamEventMsg{stream: batch.stream, event: batch.events[i]}
			if evt.event.Event != tt.event {
				t.Fatalf("event name = %q, want %q", evt.event.Event, tt.event)
			}
			updatedAny, nextCmd := model.Update(evt)
			nextModel, ok := updatedAny.(Model)
			if !ok {
				t.Fatalf("updated model type = %T, want Model", updatedAny)
			}
			tt.assertFn(t, nextModel, evt.event)
			model = nextModel
			if nextCmd == nil {
				t.Fatal("expected next wait command")
			}
		})
	}
}

func TestShouldAttemptDaemonReattach(t *testing.T) {
	t.Run("socket missing transport error", func(t *testing.T) {
		err := errors.New("daemon command transport: dial unix /Users/riordan/.azedarach/run/daemon.sock: connect: no such file or directory")
		if !reconnect.IsTransientTransportError(err) {
			t.Fatal("expected daemon reattach for missing socket transport error")
		}
	})

	t.Run("daemon socket unavailable", func(t *testing.T) {
		err := errors.New("daemon socket unavailable: stat /tmp/azedarach/daemon.sock: no such file or directory")
		if !reconnect.IsTransientTransportError(err) {
			t.Fatal("expected daemon reattach for unavailable socket error")
		}
	})

	t.Run("command validation failure", func(t *testing.T) {
		err := errors.New("failed to update task: invalid request: status transition blocked")
		if reconnect.IsTransientTransportError(err) {
			t.Fatal("did not expect daemon reattach for non-transport daemon error")
		}
	})
}

func TestShouldQueueDaemonReattach(t *testing.T) {
	now := time.Now()
	socketErr := errors.New("daemon command transport: dial unix /Users/riordan/.azedarach/run/daemon.sock: connect: no such file or directory")
	policy := reconnect.DefaultReconciliationPolicy()

	t.Run("first transport failure", func(t *testing.T) {
		if !policy.ShouldQueueReattach(time.Time{}, now, socketErr) {
			t.Fatal("expected first transport error to queue reattach")
		}
	})

	t.Run("within retry interval", func(t *testing.T) {
		last := now.Add(-policy.ReattachRetryInterval + time.Second)
		if policy.ShouldQueueReattach(last, now, socketErr) {
			t.Fatal("did not expect reattach within retry interval")
		}
	})

	t.Run("after retry interval", func(t *testing.T) {
		last := now.Add(-policy.ReattachRetryInterval - time.Second)
		if !policy.ShouldQueueReattach(last, now, socketErr) {
			t.Fatal("expected reattach after retry interval")
		}
	})

	t.Run("non transport error", func(t *testing.T) {
		err := errors.New("failed to update task: invalid request")
		if policy.ShouldQueueReattach(time.Time{}, now, err) {
			t.Fatal("did not expect reattach for non-transport error")
		}
	})
}

func TestDaemonStreamClosedTriggersReattachAndSnapshotRehydrate(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandBoardFetch {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandBoardFetch)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Revision:        8,
				OK:              true,
				Body:            mustMarshalBoardSnapshot(t, req.ProtocolVersion, 8, req.Meta.ProjectID.String(), []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}}),
			}, nil
		},
		subscribeFn: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
			return make(chan protocol.EventEnvelope), nil
		},
	}
	m := newDaemonTestModel(transport)
	m.currentProject = "proj"
	stream := make(chan protocol.EventEnvelope)
	m.daemonEvents = stream

	updated, cmd := m.Update(daemonStreamClosedMsg{stream: stream})
	if cmd == nil {
		t.Fatal("expected reattach command after active stream closure")
	}
	next := updated.(Model)
	if next.daemonEvents != nil {
		t.Fatal("expected active stream to clear before reattach")
	}

	msg := cmd()
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
	}
	if loaded.revision != 8 {
		t.Fatalf("loaded revision = %d, want 8", loaded.revision)
	}
	if transport.subscribeFrom != 8 {
		t.Fatalf("subscribe from revision = %d, want 8", transport.subscribeFrom)
	}
}

func TestDaemonStreamClosedIgnoresStaleStreamClose(t *testing.T) {
	m := newTestModel()
	active := make(chan protocol.EventEnvelope)
	stale := make(chan protocol.EventEnvelope)
	m.daemonEvents = active

	updated, cmd := m.Update(daemonStreamClosedMsg{stream: stale})
	if cmd != nil {
		t.Fatal("expected no command for stale stream close signal")
	}
	next := updated.(Model)
	if next.daemonEvents != active {
		t.Fatal("expected active stream to remain unchanged")
	}
}

func TestDaemonGapEventTriggersSnapshotRehydrate(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandBoardFetch {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandBoardFetch)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Revision:        12,
				OK:              true,
				Body:            mustMarshalBoardSnapshot(t, req.ProtocolVersion, 12, req.Meta.ProjectID.String(), []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}}),
			}, nil
		},
		subscribeFn: func(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
			ch := make(chan protocol.EventEnvelope, 1)
			ch <- protocol.EventEnvelope{Revision: 13, Event: "task.updated"}
			return ch, nil
		},
	}
	m := newDaemonTestModel(transport)
	m.currentProject = "proj"
	m.daemonRevision = 4
	m.daemonEvents = make(chan protocol.EventEnvelope)

	updated, cmd := m.Update(daemonStreamEventMsg{event: protocol.EventEnvelope{Revision: 7, Event: "task.updated"}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if next.daemonRevision != 4 {
		t.Fatalf("daemon revision after gap = %d, want 4", next.daemonRevision)
	}
	if cmd == nil {
		t.Fatal("expected gap event to trigger rehydrate command")
	}

	msg := cmd()
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("rehydrate message type = %T, want issuesLoadedMsg", msg)
	}
	if loaded.revision != 12 {
		t.Fatalf("loaded revision = %d, want 12", loaded.revision)
	}
	if loaded.events == nil {
		t.Fatal("expected rehydrate to resubscribe to daemon stream")
	}
	if transport.subscribeProject != "proj" {
		t.Fatalf("subscribe project = %q, want proj", transport.subscribeProject)
	}
	if transport.subscribeFrom != 12 {
		t.Fatalf("subscribe from revision = %d, want 12", transport.subscribeFrom)
	}

	reloaded, followCmd := next.Update(loaded)
	reloadedModel, ok := reloaded.(Model)
	if !ok {
		t.Fatalf("reloaded model type = %T, want Model", reloaded)
	}
	if reloadedModel.daemonRevision != 12 {
		t.Fatalf("daemon revision after rehydrate = %d, want 12", reloadedModel.daemonRevision)
	}
	if reloadedModel.daemonEvents == nil {
		t.Fatal("expected daemon stream to be restored after rehydrate")
	}
	if followCmd == nil {
		t.Fatal("expected restored stream to schedule the next wait")
	}
}

func TestDaemonStreamClosedRehydratesCurrentStreamAndIgnoresStaleClose(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandBoardFetch {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandBoardFetch)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				Revision:        8,
				OK:              true,
				Body:            mustMarshalBoardSnapshot(t, req.ProtocolVersion, 8, req.Meta.ProjectID.String(), []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen}}),
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

	loadedMsg := m.attachDaemonCmd()()
	loaded, ok := loadedMsg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", loadedMsg)
	}
	updated, cmd := m.Update(loaded)
	attachedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if attachedModel.daemonEvents == nil {
		t.Fatal("expected daemon stream to be active after attach")
	}
	if cmd == nil {
		t.Fatal("expected attach to schedule stream wait")
	}

	staleStream := make(chan protocol.EventEnvelope)
	ignored, staleCmd := attachedModel.Update(daemonStreamClosedMsg{stream: staleStream})
	ignoredModel, ok := ignored.(Model)
	if !ok {
		t.Fatalf("ignored model type = %T, want Model", ignored)
	}
	if ignoredModel.daemonEvents == nil {
		t.Fatal("stale close should not clear active stream")
	}
	if staleCmd != nil {
		t.Fatalf("stale close command = %v, want nil", staleCmd)
	}

	closed, closeCmd := attachedModel.Update(daemonStreamClosedMsg{stream: loaded.events})
	closedModel, ok := closed.(Model)
	if !ok {
		t.Fatalf("closed model type = %T, want Model", closed)
	}
	if closedModel.daemonEvents != nil {
		t.Fatal("expected current stream to be cleared on close")
	}
	if closeCmd == nil {
		t.Fatal("expected current stream close to trigger rehydrate")
	}

	reattachMsg := closeCmd()
	reattached, ok := reattachMsg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("reattach message type = %T, want issuesLoadedMsg", reattachMsg)
	}
	if reattached.revision != 8 {
		t.Fatalf("reattach revision = %d, want 8", reattached.revision)
	}
	if transport.subscribeFrom != 8 {
		t.Fatalf("subscribe from revision after reattach = %d, want 8", transport.subscribeFrom)
	}
	if got, want := len(transport.calls), 6; got != want {
		t.Fatalf("transport calls = %v, want %d calls", transport.calls, want)
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
	setTaskSession(t, &m, "az-1", &domain.Session{
		IssueID:  "az-1",
		State:    domain.SessionBusy,
		Worktree: "/tmp/az-1",
	})
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

func TestFollowOnMergeSelectionDirectMergeFromPausedTarget(t *testing.T) {
	parentID := "az-parent"
	childID := "az-child"
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)

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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
			case daemonclient.CommandGitMergePreflight:
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       childID,
					SourceWorktree: "/tmp/child",
					TargetID:       parentID,
					TargetWorktree: "/tmp/parent",
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
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
			ID:       childIssueID,
			Title:    "Child task",
			Status:   domain.StatusInProgress,
			Type:     domain.TypeTask,
			ParentID: &parentIssueID,
		},
		{
			ID:     parentIssueID,
			Title:  "Parent epic",
			Status: domain.StatusInProgress,
			Type:   domain.TypeEpic,
			// Candidate eligibility is based on task projection worktree signals.
			HasWorktree: true,
		},
	}
	setTaskSession(t, &m, childID, &domain.Session{IssueID: naming.IssueID(childID), State: domain.SessionPaused, Worktree: "/tmp/child"})
	setTaskSession(t, &m, parentID, &domain.Session{IssueID: naming.IssueID(parentID), State: domain.SessionPaused, Worktree: "/tmp/parent"})
	m.nav.SelectTask(childID, 1)

	task, session := m.getCurrentTaskAndSession()
	cmd := m.followOnMergeSelectionCmd(task, session)
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
	if got := transport.requests; len(got) != 7 || got[0] != daemonclient.CommandTaskFollowOnMerge || got[1] != daemonclient.CommandWorktreeList || got[2] != daemonclient.CommandRuntimeReconcileIssue || got[3] != daemonclient.CommandGitStatus || got[4] != daemonclient.CommandGitStatus || got[5] != daemonclient.CommandGitMergePreflight || got[6] != daemonclient.CommandGitMerge {
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
					case daemonclient.CommandRuntimeReconcileIssue:
						respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
						if err != nil {
							t.Fatalf("marshal reconcile response: %v", err)
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
					case daemonclient.CommandGitMergePreflight:
						respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
							SourceID:       childID,
							SourceWorktree: "/tmp/child",
							TargetID:       parentID,
							TargetWorktree: "/tmp/parent",
							Clean:          true,
						})
						if err != nil {
							t.Fatalf("marshal preflight response: %v", err)
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
			parentIssueID := naming.IssueID(parentID)
			childIssueID := naming.IssueID(childID)
			m.tasks = []domain.Task{
				{
					ID:       childIssueID,
					Title:    "Child task",
					Status:   domain.StatusInProgress,
					Type:     domain.TypeTask,
					ParentID: &parentIssueID,
				},
				{
					ID:     parentIssueID,
					Title:  "Parent epic",
					Status: domain.StatusInProgress,
					Type:   domain.TypeEpic,
				},
			}
			setTaskSession(t, &m, childID, &domain.Session{IssueID: naming.IssueID(childID), State: tt.state, Worktree: "/tmp/child"})
			setTaskSession(t, &m, parentID, &domain.Session{IssueID: naming.IssueID(parentID), State: domain.SessionBusy, Worktree: "/tmp/parent"})
			m.nav.SelectTask(childID, 1)

			task, session := m.getCurrentTaskAndSession()
			cmd := m.followOnMergeSelectionCmd(task, session)
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
			if got := transport.requests; len(got) != 8 || got[0] != daemonclient.CommandTaskFollowOnMerge || got[1] != daemonclient.CommandWorktreeList || got[2] != daemonclient.CommandRuntimeReconcileIssue || got[3] != daemonclient.CommandGitStatus || got[4] != daemonclient.CommandGitStatus || got[5] != daemonclient.CommandGitMergePreflight || got[6] != daemonclient.CommandSessionStop || got[7] != daemonclient.CommandGitMerge {
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
			case daemonclient.CommandBoardFetch:
				childIssueID := naming.IssueID(childID)
				parentIssueID := naming.IssueID(parentID)
				tasks := []domain.Task{
					{
						ID:      childIssueID,
						Title:   "Child task",
						Status:  domain.StatusInProgress,
						Type:    domain.TypeTask,
						Session: &domain.Session{IssueID: naming.IssueID(childID), State: domain.SessionBusy, Worktree: "/tmp/child"},
					},
					{
						ID:      parentIssueID,
						Title:   "Parent epic",
						Status:  domain.StatusInProgress,
						Type:    domain.TypeEpic,
						Session: &domain.Session{IssueID: naming.IssueID(parentID), State: domain.SessionBusy, Worktree: "/tmp/parent"},
					},
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            mustMarshalBoardSnapshot(t, req.ProtocolVersion, 0, req.Meta.ProjectID.String(), tasks),
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
			case daemonclient.CommandGitMergePreflight:
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       childID,
					SourceWorktree: "/tmp/child",
					TargetID:       parentID,
					TargetWorktree: "/tmp/parent",
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
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
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{
			ID:       childIssueID,
			Title:    "Child task",
			Status:   domain.StatusInProgress,
			Type:     domain.TypeTask,
			ParentID: &parentIssueID,
		},
		{
			ID:          parentIssueID,
			Title:       "Parent epic",
			Status:      domain.StatusInProgress,
			Type:        domain.TypeEpic,
			HasWorktree: true,
		},
	}
	// Simulate stale projection by leaving task sessions nil and forcing daemon snapshot fallback.
	m.nav.SelectTask(childID, 1)

	task, session := m.getCurrentTaskAndSession()
	cmd := m.followOnMergeSelectionCmd(task, session)
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
	if got := transport.requests; len(got) != 11 || got[0] != daemonclient.CommandTaskFollowOnMerge || got[1] != daemonclient.CommandWorktreeList || got[2] != daemonclient.CommandWorktreeList || got[3] != daemonclient.CommandBoardFetch || got[4] != daemonclient.CommandWorktreeList || got[5] != daemonclient.CommandRuntimeReconcileIssue || got[6] != daemonclient.CommandGitStatus || got[7] != daemonclient.CommandGitStatus || got[8] != daemonclient.CommandGitMergePreflight || got[9] != daemonclient.CommandSessionStop || got[10] != daemonclient.CommandGitMerge {
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
	signals := updatedModel.runtimeSignalsForBoard()["az-1"]
	if signals.PendingOperationID != "op-merge" || signals.PendingOperationState != string(protocol.OperationStateQueued) {
		t.Fatalf("pending merge signals = %+v", signals)
	}
	progress := updatedModel.pendingMutationForTask("az-1")
	if progress == nil || progress.OperationID != "op-merge" || progress.State != string(protocol.OperationStateQueued) {
		t.Fatalf("pending merge detail progress = %+v", progress)
	}
}

func TestHandleFollowOnMergePendingOperationMarksSourceAndTarget(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(mergeResultMsg{
		sourceID:    "az-1",
		targetID:    "az-3",
		stage:       "merge",
		operationID: "op-merge",
		state:       protocol.OperationStateRunning,
	})

	updatedModel := updated.(Model)
	for _, taskID := range []string{"az-1", "az-3"} {
		signals := updatedModel.runtimeSignalsForBoard()[taskID]
		if signals.PendingOperationID != "op-merge" || signals.PendingOperationState != string(protocol.OperationStateRunning) {
			t.Fatalf("pending merge signals for %s = %+v", taskID, signals)
		}
		progress := updatedModel.pendingMutationForTask(taskID)
		if progress == nil || progress.OperationID != "op-merge" || progress.State != string(protocol.OperationStateRunning) {
			t.Fatalf("pending merge detail progress for %s = %+v", taskID, progress)
		}
	}
}

func TestPendingMergeOperationSurvivesStaleIssueSnapshot(t *testing.T) {
	m := newTestModel()

	pendingAny, _ := m.Update(mergeResultMsg{
		sourceID:    "az-1",
		targetID:    "az-3",
		stage:       "merge",
		operationID: "op-merge",
		state:       protocol.OperationStateRunning,
	})
	pendingModel := pendingAny.(Model)

	refreshedAny, _ := pendingModel.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1 stale", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-3", Title: "Task 3 stale", Status: domain.StatusInProgress, Priority: domain.P0, Type: domain.TypeFeature},
		},
	})
	refreshed := refreshedAny.(Model)

	for _, taskID := range []string{"az-1", "az-3"} {
		signals := refreshed.runtimeSignalsForBoard()[taskID]
		if signals.PendingOperationID != "op-merge" || signals.PendingOperationState != string(protocol.OperationStateRunning) {
			t.Fatalf("pending merge signals after stale snapshot for %s = %+v", taskID, signals)
		}
		progress := refreshed.pendingMutationForTask(taskID)
		if progress == nil || progress.OperationID != "op-merge" || progress.State != string(protocol.OperationStateRunning) {
			t.Fatalf("pending merge detail progress after stale snapshot for %s = %+v", taskID, progress)
		}
	}
}

func TestLocalMergeActivityMarkerClearsOnMergeFailure(t *testing.T) {
	m := newTestModel()
	m.markMergeOperationPreparing("az-1", "az-3", "preparing merge")

	updatedAny, _ := m.Update(mergeResultMsg{
		sourceID: "az-1",
		targetID: "az-3",
		stage:    "merge",
		err:      fmt.Errorf("preflight failed"),
	})
	updated := updatedAny.(Model)

	for _, taskID := range []string{"az-1", "az-3"} {
		signals := updated.runtimeSignalsForBoard()[taskID]
		if signals.PendingOperationState != "" || signals.PendingOperationID != "" {
			t.Fatalf("pending merge marker for %s = %+v, want cleared", taskID, signals)
		}
		if progress := updated.pendingMutationForTask(taskID); progress != nil {
			t.Fatalf("detail progress for %s = %+v, want nil", taskID, progress)
		}
	}
}

func TestHandleMergeTargetSelectionToBaseUsesWorktreeLookupFallback(t *testing.T) {
	sourceID := "az-source"
	baseWorktree := ""

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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
			case daemonclient.CommandTaskMergeBaseTarget:
				respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
					IssueID:  sourceID,
					TargetID: mergeBaseTargetID,
					Branch:   "main",
				})
				if err != nil {
					t.Fatalf("marshal merge base target response: %v", err)
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
				if body.Worktree != baseWorktree || body.Remote != "origin" {
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
				if body.Worktree != baseWorktree || body.Branch != "main" {
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
				if body.Worktree != baseWorktree || body.Branch != "az/az-source" {
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
			case daemonclient.CommandGitMergePreflight:
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       sourceID,
					SourceWorktree: "/tmp/az-source",
					TargetID:       mergeBaseTargetID,
					TargetWorktree: baseWorktree,
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
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
	m.config.Git.BaseBranch = "trunk"
	baseWorktree = m.activeProjectPath()
	m.daemonClient = daemonclient.New(transport)

	updated, cmd := m.handleMergeTargetSelection(overlay.MergeTargetSelectedMsg{
		SourceID: sourceID,
		TargetID: mergeBaseTargetID,
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
	if got := transport.requests; len(got) != 11 || got[0] != daemonclient.CommandWorktreeList || got[1] != daemonclient.CommandTaskMergeBaseTarget || got[2] != daemonclient.CommandWorktreeList || got[3] != daemonclient.CommandRuntimeReconcileIssue || got[4] != daemonclient.CommandGitStatus || got[5] != daemonclient.CommandGitStatus || got[6] != daemonclient.CommandGitMergePreflight || got[7] != daemonclient.CommandGitFetch || got[8] != daemonclient.CommandGitCheckout || got[9] != daemonclient.CommandGitMerge || got[10] != daemonclient.CommandGitStatus {
		t.Fatalf("requests = %v", got)
	}
}

func TestActionModeMergeKeyMergesChildIntoParent(t *testing.T) {
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
			case daemonclient.CommandGitMergePreflight:
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       childID,
					SourceWorktree: "/tmp/child",
					TargetID:       parentID,
					TargetWorktree: "/tmp/parent",
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
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
					Worktree: "/tmp/parent",
					Branch:   "az/az-child",
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
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{ID: childIssueID, Title: "Child task", Status: domain.StatusInProgress, Type: domain.TypeTask, ParentID: &parentIssueID},
		{ID: parentIssueID, Title: "Parent task", Status: domain.StatusDone, Type: domain.TypeTask},
	}
	setTaskSession(t, &m, childID, &domain.Session{IssueID: naming.IssueID(childID), State: domain.SessionPaused, Worktree: "/tmp/child"})
	setTaskSession(t, &m, parentID, &domain.Session{IssueID: naming.IssueID(parentID), State: domain.SessionPaused, Worktree: "/tmp/parent"})
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected child-to-parent merge command from action-mode m")
	}
	msg := cmd()
	resolved, ok := msg.(mergeTargetSelectionResolvedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergeTargetSelectionResolvedMsg", msg)
	}
	next, nextCmd := updated.(Model).Update(resolved)
	if _, ok := next.(Model); !ok {
		t.Fatalf("next model type = %T, want Model", next)
	}
	if nextCmd == nil {
		t.Fatal("expected merge command after target resolution")
	}
	if _, ok := nextCmd().(mergeResultMsg); !ok {
		t.Fatalf("next msg type = %T, want mergeResultMsg", nextCmd())
	}
}

func TestActionModeMergeKeyDoesNotStopBusyParentSession(t *testing.T) {
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
			case daemonclient.CommandGitMergePreflight:
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       childID,
					SourceWorktree: "/tmp/child",
					TargetID:       parentID,
					TargetWorktree: "/tmp/parent",
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
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
				if body.Worktree != "/tmp/parent" || body.Branch != "az/az-child" {
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
			case daemonclient.CommandSessionStop:
				t.Fatalf("default child merge must not stop target session: %s", req.Command)
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.editor.EnterAction()
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{ID: childIssueID, Title: "Child task", Status: domain.StatusInProgress, Type: domain.TypeTask, ParentID: &parentIssueID},
		{ID: parentIssueID, Title: "Parent task", Status: domain.StatusInProgress, Type: domain.TypeTask},
	}
	setTaskSession(t, &m, childID, &domain.Session{IssueID: naming.IssueID(childID), State: domain.SessionPaused, Worktree: "/tmp/child"})
	setTaskSession(t, &m, parentID, &domain.Session{IssueID: naming.IssueID(parentID), State: domain.SessionBusy, Worktree: "/tmp/parent"})
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected child-to-parent merge command from action-mode m")
	}
	msg := cmd()
	resolved, ok := msg.(mergeTargetSelectionResolvedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergeTargetSelectionResolvedMsg", msg)
	}
	next, nextCmd := updated.(Model).Update(resolved)
	if _, ok := next.(Model); !ok {
		t.Fatalf("next model type = %T, want Model", next)
	}
	if nextCmd == nil {
		t.Fatal("expected merge command after target resolution")
	}
	mergeMsg, ok := nextCmd().(mergeResultMsg)
	if !ok {
		t.Fatalf("next msg type = %T, want mergeResultMsg", mergeMsg)
	}
	if mergeMsg.err != nil {
		t.Fatalf("merge err = %v", mergeMsg.err)
	}
	for _, command := range transport.requests {
		if command == daemonclient.CommandSessionStop {
			t.Fatalf("requests = %v, must not include %s", transport.requests, daemonclient.CommandSessionStop)
		}
	}
}

func TestActionModeMergeKeyBlocksPredictedChildIntoParentConflictsBeforeMerge(t *testing.T) {
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
			case daemonclient.CommandGitMergePreflight:
				var body daemonclient.GitMergePreflightRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal preflight request: %v", err)
				}
				if body.SourceID != childID || body.TargetID != parentID || body.SourceWorktree != "/tmp/child" || body.TargetWorktree != "/tmp/parent" {
					t.Fatalf("preflight body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       childID,
					SourceWorktree: "/tmp/child",
					TargetID:       parentID,
					TargetWorktree: "/tmp/parent",
					Clean:          false,
					ConflictFiles:  []string{"internal/tui/model.go"},
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandSessionStop, daemonclient.CommandGitMerge:
				t.Fatalf("unexpected command after failed preflight: %s", req.Command)
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.editor.EnterAction()
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{ID: childIssueID, Title: "Child task", Status: domain.StatusInProgress, Type: domain.TypeTask, ParentID: &parentIssueID},
		{ID: parentIssueID, Title: "Parent task", Status: domain.StatusDone, Type: domain.TypeTask},
	}
	setTaskSession(t, &m, childID, &domain.Session{IssueID: naming.IssueID(childID), State: domain.SessionBusy, Worktree: "/tmp/child"})
	setTaskSession(t, &m, parentID, &domain.Session{IssueID: naming.IssueID(parentID), State: domain.SessionBusy, Worktree: "/tmp/parent"})
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected child-to-parent merge preflight command from action-mode m")
	}

	msg := cmd()
	resolved, ok := msg.(mergeTargetSelectionResolvedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergeTargetSelectionResolvedMsg", msg)
	}
	next, nextCmd := updated.(Model).Update(resolved)
	if _, ok := next.(Model); !ok {
		t.Fatalf("next model type = %T, want Model", next)
	}
	if nextCmd == nil {
		t.Fatal("expected merge command after target resolution")
	}
	msg = nextCmd()
	preflight, ok := msg.(mergePreflightFailureMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergePreflightFailureMsg", msg)
	}
	if preflight.sourceID != childID || preflight.targetID != parentID {
		t.Fatalf("preflight = %+v, want source=%s target=%s", preflight, childID, parentID)
	}
	if len(preflight.conflictFiles) != 1 || preflight.conflictFiles[0] != "internal/tui/model.go" {
		t.Fatalf("conflict files = %+v", preflight.conflictFiles)
	}
	for _, command := range transport.requests {
		if command == daemonclient.CommandSessionStop || command == daemonclient.CommandGitMerge {
			t.Fatalf("requests = %v, want preflight block before stop/merge", transport.requests)
		}
	}
}

func TestActionModeMergeKeyIgnoreSourceDirtyMergesChildIntoBusyParent(t *testing.T) {
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
				if body.Worktree == "/tmp/child" {
					status = git.GitStatus{HasChanges: true, Modified: []string{"source.go"}}
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
			case daemonclient.CommandGitMergePreflight:
				var body daemonclient.GitMergePreflightRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal preflight request: %v", err)
				}
				if body.SourceID != childID || body.TargetID != parentID || !body.IgnoreSourceDirty {
					t.Fatalf("preflight body = %+v, want ignored source dirty child-to-parent preflight", body)
				}
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       childID,
					SourceWorktree: "/tmp/child",
					TargetID:       parentID,
					TargetWorktree: "/tmp/parent",
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandSessionStop:
				t.Fatalf("default child merge must not stop target session: %s", req.Command)
			case daemonclient.CommandGitMerge:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal merge request: %v", err)
				}
				if body.Worktree != "/tmp/parent" || body.Branch != "az/az-child" {
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
	m.editor.EnterAction()
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{ID: childIssueID, Title: "Child task", Status: domain.StatusInProgress, Type: domain.TypeTask, ParentID: &parentIssueID},
		{ID: parentIssueID, Title: "Parent task", Status: domain.StatusDone, Type: domain.TypeTask},
	}
	setTaskSession(t, &m, childID, &domain.Session{IssueID: naming.IssueID(childID), State: domain.SessionBusy, Worktree: "/tmp/child"})
	setTaskSession(t, &m, parentID, &domain.Session{IssueID: naming.IssueID(parentID), State: domain.SessionBusy, Worktree: "/tmp/parent"})
	m.nav.SelectTask(childID, 1)

	updated, cmd := m.handleActionMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if cmd == nil {
		t.Fatal("expected child-to-parent merge preflight command from action-mode m")
	}
	msg := cmd()
	resolved, ok := msg.(mergeTargetSelectionResolvedMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergeTargetSelectionResolvedMsg", msg)
	}
	next, nextCmd := updated.(Model).Update(resolved)
	if _, ok := next.(Model); !ok {
		t.Fatalf("next model type = %T, want Model", next)
	}
	if nextCmd == nil {
		t.Fatal("expected merge command after target resolution")
	}
	msg = nextCmd()
	preflight, ok := msg.(mergePreflightFailureMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergePreflightFailureMsg", msg)
	}
	if preflight.stopTargetBeforeMerge {
		t.Fatalf("stopTargetBeforeMerge = true, want false")
	}
	for _, command := range transport.requests {
		if command == daemonclient.CommandSessionStop || command == daemonclient.CommandGitMerge {
			t.Fatalf("requests = %v, want initial dirty preflight before stop/merge", transport.requests)
		}
	}

	retrySelection := overlay.SelectionMsg{
		Key: "merge_preflight_ignore_source_dirty",
		Value: overlay.MergePreflightRefreshSelection{
			SourceID:              preflight.sourceID,
			TargetID:              preflight.targetID,
			SourceWorktree:        preflight.sourceWorktree,
			TargetWorktree:        preflight.targetWorktree,
			TargetRef:             preflight.targetRef,
			SourceBranch:          preflight.sourceBranch,
			IgnoreSourceDirty:     true,
			StopTargetBeforeMerge: preflight.stopTargetBeforeMerge,
		},
	}
	retryModel, retryCmd := next.(Model).Update(retrySelection)
	if _, ok := retryModel.(Model); !ok {
		t.Fatalf("retry model type = %T, want Model", retryModel)
	}
	if retryCmd == nil {
		t.Fatal("expected retry merge command")
	}
	retryMsg := retryCmd()
	mergeMsg, ok := retryMsg.(mergeResultMsg)
	if !ok {
		t.Fatalf("retry msg type = %T, want mergeResultMsg", retryMsg)
	}
	if mergeMsg.err != nil || mergeMsg.result == nil || !mergeMsg.result.Success {
		t.Fatalf("merge message = %+v", mergeMsg)
	}

	got := transport.requests
	wantSuffix := []string{
		daemonclient.CommandWorktreeList,
		daemonclient.CommandRuntimeReconcileIssue,
		daemonclient.CommandGitStatus,
		daemonclient.CommandGitStatus,
		daemonclient.CommandGitMergePreflight,
		daemonclient.CommandGitMerge,
	}
	if len(got) < len(wantSuffix) {
		t.Fatalf("requests = %v, want retry suffix %v", got, wantSuffix)
	}
	gotSuffix := got[len(got)-len(wantSuffix):]
	for i, want := range wantSuffix {
		if gotSuffix[i] != want {
			t.Fatalf("requests suffix = %v, want %v", gotSuffix, wantSuffix)
		}
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
			case daemonclient.CommandTaskMergeBaseTarget:
				respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
					IssueID:  issueID,
					TargetID: mergeBaseTargetID,
					Branch:   "trunk",
				})
				if err != nil {
					t.Fatalf("marshal merge base target response: %v", err)
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
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{Worktree: ".", Branch: "trunk"})
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
			case daemonclient.CommandGitMergePreflight:
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       issueID,
					SourceWorktree: "/tmp/az-top",
					TargetID:       mergeBaseTargetID,
					TargetWorktree: ".",
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
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
	issueIDTyped := naming.IssueID(issueID)
	m.tasks = []domain.Task{
		{
			ID:     issueIDTyped,
			Title:  "Top-level task",
			Status: domain.StatusInProgress,
			Type:   domain.TypeTask,
		},
	}
	setTaskSession(t, &m, issueID, &domain.Session{IssueID: naming.IssueID(issueID), State: domain.SessionPaused, Worktree: "/tmp/az-top"})
	m.nav.SelectTask(issueID, 1)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "m"})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected merge-to-base command for top-level issue")
	}
	msg := cmd()
	mergeMsg, ok := msg.(mergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergeResultMsg", msg)
	}
	if mergeMsg.targetID != mergeBaseTargetID || mergeMsg.err != nil {
		t.Fatalf("merge message = %+v", mergeMsg)
	}
}

func TestMergeToBasePreflightBlocksDirtySourceOrTarget(t *testing.T) {
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandTaskMergeBaseTarget:
				respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
					IssueID:  sourceID,
					TargetID: mergeBaseTargetID,
					Branch:   "main",
				})
				if err != nil {
					t.Fatalf("marshal merge base target response: %v", err)
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
	msg := m.mergeToBaseCmd("/tmp/az-source", sourceID, true)()

	preflight, ok := msg.(mergePreflightFailureMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergePreflightFailureMsg", msg)
	}
	if preflight.sourceID != sourceID || preflight.targetID != mergeBaseTargetID {
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

func TestMergeToBasePreflightCanIgnoreDirtySource(t *testing.T) {
	sourceID := "az-source"
	var sawIgnoredPreflight bool
	targetWorktree := "."
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
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
			case daemonclient.CommandTaskMergeBaseTarget:
				respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
					IssueID:  sourceID,
					TargetID: mergeBaseTargetID,
					Branch:   "trunk",
				})
				if err != nil {
					t.Fatalf("marshal merge base target response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMergePreflight:
				var body daemonclient.GitMergePreflightRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal preflight request: %v", err)
				}
				if !body.IgnoreSourceDirty {
					t.Fatalf("preflight request = %+v, want ignore source dirty", body)
				}
				sawIgnoredPreflight = true
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       sourceID,
					SourceWorktree: "/tmp/az-source",
					TargetID:       mergeBaseTargetID,
					TargetWorktree: targetWorktree,
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitFetch, daemonclient.CommandGitCheckout:
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{})
				if err != nil {
					t.Fatalf("marshal git response: %v", err)
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
					Worktree: targetWorktree,
					Branch:   "az/az-source",
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
	targetWorktree = m.activeProjectPath()
	m.daemonClient = daemonclient.New(transport)
	msg := m.mergeToBaseCmdWithOptions("/tmp/az-source", sourceID, true, mergePreflightOptions{ignoreSourceDirty: true})()

	mergeMsg, ok := msg.(mergeResultMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergeResultMsg", msg)
	}
	if mergeMsg.err != nil || mergeMsg.result == nil || !mergeMsg.result.Success {
		t.Fatalf("merge message = %+v", mergeMsg)
	}
	if !sawIgnoredPreflight {
		t.Fatalf("requests = %v, want ignored preflight request", transport.requests)
	}
	for _, want := range []string{daemonclient.CommandGitMergePreflight, daemonclient.CommandGitMerge} {
		var saw bool
		for _, got := range transport.requests {
			if got == want {
				saw = true
				break
			}
		}
		if !saw {
			t.Fatalf("requests = %v, want %s", transport.requests, want)
		}
	}
}

func TestCheckMergePreflightUsesLiveGitStatusWhenRefreshFlagFalse(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
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
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.tasks = []domain.Task{
		{ID: "az-source", HasUncommittedChanges: true},
		{ID: mergeBaseTargetID, HasUncommittedChanges: true},
	}

	preflight := m.checkMergePreflight(
		context.Background(),
		"az-source",
		mergeBaseTargetID,
		"/tmp/az-source",
		"/tmp/base",
		"",
		"",
		false, // legacy flag value; should still do live status checks
		false,
	)

	if preflight != nil {
		t.Fatalf("preflight = %+v, want nil for clean live status", preflight)
	}

	var statusCalls int
	for _, command := range transport.requests {
		if command == daemonclient.CommandGitStatus {
			statusCalls++
		}
	}
	if statusCalls != 2 {
		t.Fatalf("git status calls = %d, want 2", statusCalls)
	}
}

func TestCheckMergePreflightDoesNotBlockUntrackedOnlyStatus(t *testing.T) {
	var sawPreflight bool
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandGitStatus:
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{Status: git.GitStatus{HasChanges: true, Untracked: []string{".azedarach/images/", "docs/"}}})
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
			case daemonclient.CommandGitMergePreflight:
				sawPreflight = true
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       "az-source",
					SourceWorktree: "/tmp/az-source",
					TargetID:       mergeBaseTargetID,
					TargetWorktree: "/tmp/base",
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
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

	preflight := m.checkMergePreflight(
		context.Background(),
		"az-source",
		mergeBaseTargetID,
		"/tmp/az-source",
		"/tmp/base",
		"trunk",
		"az/source",
		false,
		false,
	)

	if preflight != nil {
		t.Fatalf("preflight = %+v, want nil for untracked-only live status", preflight)
	}
	if !sawPreflight {
		t.Fatalf("requests = %v, want merge preflight after untracked-only status", transport.requests)
	}
}

func TestCheckMergePreflightReconcilesRuntimeWhenRefreshFlagTrue(t *testing.T) {
	var reconcileBody protocol.RuntimeReconcileIssueRequestBody
	var statusBodies []map[string]any
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandRuntimeReconcileIssue:
				if err := json.Unmarshal(req.Body, &reconcileBody); err != nil {
					t.Fatalf("unmarshal reconcile issue body: %v", err)
				}
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitStatus:
				var body map[string]any
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal git status body: %v", err)
				}
				statusBodies = append(statusBodies, body)
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
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)

	preflight := m.checkMergePreflight(
		context.Background(),
		"az-source",
		"main",
		"/tmp/az-source",
		"/tmp/main",
		"",
		"",
		true,
		false,
	)
	if preflight != nil {
		t.Fatalf("preflight = %+v, want nil for clean status", preflight)
	}
	if len(transport.requests) < 3 {
		t.Fatalf("requests = %v, want reconcile + two status calls", transport.requests)
	}
	if transport.requests[0] != daemonclient.CommandRuntimeReconcileIssue {
		t.Fatalf("first command = %q, want %q", transport.requests[0], daemonclient.CommandRuntimeReconcileIssue)
	}
	if got := len(reconcileBody.IssueIDs); got != 1 {
		t.Fatalf("reconcile issue IDs len = %d, want 1", got)
	}
	if got := reconcileBody.IssueIDs[0].String(); got != "az-source" {
		t.Fatalf("reconcile issue ID = %q, want az-source", got)
	}
	if len(statusBodies) != 2 {
		t.Fatalf("git status bodies = %+v, want 2", statusBodies)
	}
	for _, body := range statusBodies {
		if body["refresh"] != true {
			t.Fatalf("git status body = %+v, want refresh=true", body)
		}
	}
}

func TestMergeToBasePreflightBlocksPredictedConflicts(t *testing.T) {
	sourceID := "az-source"
	targetWorktree := ""
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
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandTaskMergeBaseTarget:
				respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
					IssueID:  sourceID,
					TargetID: mergeBaseTargetID,
					Branch:   "trunk",
				})
				if err != nil {
					t.Fatalf("marshal merge base target response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandGitMergePreflight:
				var body daemonclient.GitMergePreflightRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal preflight request: %v", err)
				}
				if body.SourceID != sourceID || body.SourceWorktree != "/tmp/az-source" {
					t.Fatalf("preflight source = %+v", body)
				}
				if body.TargetID != mergeBaseTargetID || body.TargetWorktree != targetWorktree {
					t.Fatalf("preflight target = %+v, want target worktree %q", body, targetWorktree)
				}
				if body.TargetRef != "trunk" || body.SourceBranch != "az/az-source" {
					t.Fatalf("preflight refs = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       sourceID,
					SourceWorktree: "/tmp/az-source",
					TargetID:       mergeBaseTargetID,
					TargetWorktree: targetWorktree,
					Clean:          false,
					ConflictFiles:  []string{"cmd/az/main.go"},
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
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
	m.config.Git.BaseBranch = "trunk"
	targetWorktree = m.activeProjectPath()
	m.daemonClient = daemonclient.New(transport)
	msg := m.mergeToBaseCmd("/tmp/az-source", sourceID, true)()

	preflight, ok := msg.(mergePreflightFailureMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergePreflightFailureMsg", msg)
	}
	if len(preflight.reasons) == 0 || !strings.Contains(preflight.reasons[0], "Merge would conflict") {
		t.Fatalf("preflight reasons = %+v, want conflict reason", preflight.reasons)
	}
	for _, command := range transport.requests {
		if command == daemonclient.CommandGitFetch || command == daemonclient.CommandGitCheckout || command == daemonclient.CommandGitMerge {
			t.Fatalf("unexpected git merge command during preflight conflict failure: %v", transport.requests)
		}
	}
	var sawPreflight bool
	for _, command := range transport.requests {
		if command == daemonclient.CommandGitMergePreflight {
			sawPreflight = true
			break
		}
	}
	if !sawPreflight {
		t.Fatalf("requests = %v, want %q", transport.requests, daemonclient.CommandGitMergePreflight)
	}
}

func TestMergeToBaseUsesNearestNonClosedAncestorBranch(t *testing.T) {
	sourceID := "az-child"
	parentID := "az-parent"
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(sourceID)
	targetWorktree := ""

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
						{Path: "/tmp/az-child", Branch: "az/az-child", IssueID: sourceID},
						{Path: "/tmp/az-parent", Branch: "az/az-parent", IssueID: parentID},
					},
				})
				if err != nil {
					t.Fatalf("marshal worktree response: %v", err)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case daemonclient.CommandTaskMergeBaseTarget:
				respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
					IssueID:        sourceID,
					TargetID:       parentID,
					Branch:         "az/az-parent",
					WorktreePath:   "/tmp/az-parent",
					BranchAttached: true,
					AncestorChain:  []string{parentID},
				})
				if err != nil {
					t.Fatalf("marshal merge base target response: %v", err)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case daemonclient.CommandGitStatus:
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{Status: git.GitStatus{HasChanges: false}})
				if err != nil {
					t.Fatalf("marshal status response: %v", err)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case daemonclient.CommandGitMergePreflight:
				var body daemonclient.GitMergePreflightRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal preflight request: %v", err)
				}
				if body.TargetID != parentID || body.TargetWorktree != targetWorktree {
					t.Fatalf("preflight target = %+v, want %q %q", body, parentID, targetWorktree)
				}
				if body.TargetRef != "az/az-parent" {
					t.Fatalf("preflight target ref = %q, want az/az-parent", body.TargetRef)
				}
				respBody, err := json.Marshal(daemonclient.GitMergePreflightResponse{
					SourceID:       sourceID,
					SourceWorktree: "/tmp/az-child",
					TargetID:       parentID,
					TargetWorktree: targetWorktree,
					Clean:          true,
				})
				if err != nil {
					t.Fatalf("marshal preflight response: %v", err)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case daemonclient.CommandGitFetch:
				respBody, _ := json.Marshal(daemonclient.GitCommandResponse{})
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case daemonclient.CommandGitCheckout:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal checkout request: %v", err)
				}
				if body.Branch != "az/az-parent" {
					t.Fatalf("checkout branch = %q, want az/az-parent", body.Branch)
				}
				respBody, _ := json.Marshal(daemonclient.GitCommandResponse{Branch: body.Branch})
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			case daemonclient.CommandGitMerge:
				respBody, _ := json.Marshal(daemonclient.GitMergeCommandResponse{Result: daemonclient.MergeResult{Success: true}})
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.tasks = []domain.Task{
		{ID: childIssueID, Status: domain.StatusInProgress, ParentID: &parentIssueID},
		{ID: parentIssueID, Status: domain.StatusInProgress},
	}
	targetWorktree = "/tmp/az-parent"

	msg := m.mergeToBaseCmd("/tmp/az-child", sourceID, true)()
	mergeMsg, ok := msg.(mergeResultMsg)
	if !ok {
		t.Fatalf("msg type = %T, want mergeResultMsg", msg)
	}
	if mergeMsg.err != nil {
		t.Fatalf("merge err = %v", mergeMsg.err)
	}
}

func TestResolveMergeBaseTargetFallsBackWhenNoAncestorWorktreeExists(t *testing.T) {
	sourceID := "az-child"
	parentID := "az-parent"
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(sourceID)

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandTaskMergeBaseTarget:
				respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
					IssueID:       sourceID,
					TargetID:      mergeBaseTargetID,
					Branch:        "main",
					AncestorChain: []string{parentID},
				})
				if err != nil {
					t.Fatalf("marshal merge base target response: %v", err)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, OK: true, Body: respBody}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newTestModel()
	m.daemonClient = daemonclient.New(transport)
	m.tasks = []domain.Task{
		{ID: childIssueID, Status: domain.StatusInProgress, ParentID: &parentIssueID},
		{ID: parentIssueID, Status: domain.StatusInProgress},
	}

	target, err := m.resolveMergeBaseTarget(context.Background(), sourceID)
	if err != nil {
		t.Fatalf("resolveMergeBaseTarget error = %v", err)
	}
	if target.targetID != mergeBaseTargetID || target.targetBranch != "main" || target.targetWorktree != "" {
		t.Fatalf("target = %+v, want default base target", target)
	}
}

func TestDiscardChangesCmdUsesDaemonClient(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandGitDiscard {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandGitDiscard)
			}
			var body daemonclient.GitDiscardRequest
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal discard request: %v", err)
			}
			if body.Worktree != "/tmp/az-1" {
				t.Fatalf("discard body = %+v", body)
			}
			resultBody, err := json.Marshal(daemonclient.GitDiscardResponse{Worktree: body.Worktree})
			if err != nil {
				t.Fatalf("marshal discard result: %v", err)
			}
			respBody, err := json.Marshal(map[string]any{
				"operation_id": "op-discard",
				"state":        string(protocol.OperationStateDone),
				"result":       json.RawMessage(resultBody),
			})
			if err != nil {
				t.Fatalf("marshal discard response: %v", err)
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
	msg := m.discardChangesCmd("source", "/tmp/az-1")()

	result, ok := msg.(mergePreflightActionResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergePreflightActionResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("discard err = %v", result.err)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandGitDiscard {
		t.Fatalf("requests = %v", got)
	}
}

func TestDiscardChangesCmdReturnsDaemonError(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              false,
				Error: &protocol.ErrorEnvelope{
					Code:      protocol.ErrorCodeInternal,
					Message:   "failed to clean changes: clean failed",
					Retryable: false,
				},
			}, nil
		},
	}

	m := newDaemonTestModel(transport)
	msg := m.discardChangesCmd("target", "/tmp/az-2")()

	result, ok := msg.(mergePreflightActionResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergePreflightActionResultMsg", msg)
	}
	if result.err == nil || result.err.Error() != "failed to clean changes: clean failed" {
		t.Fatalf("discard err = %v, want daemon error message", result.err)
	}
}

func TestCommitChangesCmdUsesDaemonClient(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandGitStatus:
				respBody, err := json.Marshal(struct {
					Status git.GitStatus `json:"status"`
				}{
					Status: git.GitStatus{HasChanges: true},
				})
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
			case daemonclient.CommandGitCheckpoint:
				var body daemonclient.GitCheckpointRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal checkpoint request: %v", err)
				}
				if body.Worktree != "/tmp/az-3" || body.Message != git.DefaultCheckpointMessage {
					t.Fatalf("checkpoint body = %+v", body)
				}
				resultBody, err := json.Marshal(daemonclient.GitCheckpointResponse{Worktree: body.Worktree})
				if err != nil {
					t.Fatalf("marshal checkpoint result: %v", err)
				}
				respBody, err := json.Marshal(map[string]any{
					"operation_id": "op-checkpoint",
					"state":        string(protocol.OperationStateDone),
					"result":       json.RawMessage(resultBody),
				})
				if err != nil {
					t.Fatalf("marshal checkpoint response: %v", err)
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
	msg := m.commitChangesCmd("source", "/tmp/az-3")()

	result, ok := msg.(mergePreflightActionResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergePreflightActionResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("commit err = %v", result.err)
	}
	want := []string{daemonclient.CommandGitStatus, daemonclient.CommandGitCheckpoint}
	if !reflect.DeepEqual(transport.requests, want) {
		t.Fatalf("requests = %v, want %v", transport.requests, want)
	}
}

func TestCommitChangesCmdReturnsNoChangesWhenClean(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandGitStatus {
				t.Fatalf("unexpected command: %s", req.Command)
			}
			respBody, err := json.Marshal(struct {
				Status git.GitStatus `json:"status"`
			}{
				Status: git.GitStatus{HasChanges: false},
			})
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
		},
	}

	m := newDaemonTestModel(transport)
	msg := m.commitChangesCmd("target", "/tmp/az-4")()

	result, ok := msg.(mergePreflightActionResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want mergePreflightActionResultMsg", msg)
	}
	if result.err == nil || result.err.Error() != "no changes to commit" {
		t.Fatalf("commit err = %v, want no changes to commit", result.err)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandGitStatus {
		t.Fatalf("requests = %v", got)
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
				case daemonclient.CommandRuntimeReconcileIssue:
					respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{})
					if err != nil {
						t.Fatalf("marshal reconcile response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandBoardFetch:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
							{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, HasWorktree: true},
						}),
					}, nil
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
			{ID: "az-1", Title: "Task 1 stale", Status: domain.StatusOpen},
			{ID: "az-2", Title: "Unrelated", Status: domain.StatusInReview},
		}
		setTaskSession(t, &m, "az-1", &domain.Session{
			IssueID:  "az-1",
			State:    domain.SessionBusy,
			Worktree: "/tmp/az-1",
		})
		m.nav.SelectTask("az-1", 1)

		updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: "w"})
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if cmd == nil {
			t.Fatal("expected cleanup preflight command")
		}

		preflightMsg := cmd()
		prompt, ok := preflightMsg.(worktreeCleanupConfirmPromptMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupConfirmPromptMsg", preflightMsg)
		}

		updatedAny, confirmCmd := updated.Update(prompt)
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if confirmCmd != nil {
			_ = confirmCmd()
		}
		if updated.pendingCleanup == nil {
			t.Fatal("expected pending cleanup confirmation")
		}
		if len(updated.tasks) != 2 || updated.tasks[0].Title != "Task 1" || updated.tasks[0].Status != domain.StatusInProgress || !updated.tasks[0].HasWorktree {
			t.Fatalf("selected task after cleanup preflight = %+v, want refreshed task plus unrelated task", updated.tasks)
		}
		if updated.tasks[1].ID.String() != "az-2" || updated.tasks[1].Status != domain.StatusInReview {
			t.Fatalf("unrelated task after cleanup preflight = %+v, want preserved blocked az-2", updated.tasks[1])
		}
		if got := transport.requests; len(got) != 2 ||
			got[0] != daemonclient.CommandRuntimeReconcileIssue ||
			got[1] != daemonclient.CommandBoardFetch {
			t.Fatalf("requests before confirmation = %v", got)
		}

		updatedAny, enterCmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if enterCmd != nil {
			t.Fatal("expected enter to be ignored for cleanup confirmation")
		}
		if updated.pendingCleanup == nil {
			t.Fatal("expected pending cleanup confirmation to remain after enter")
		}

		updatedAny, runCleanupCmd := updated.handleSelection(overlay.SelectionMsg{
			Key:   "yes",
			Value: overlay.ConfirmResult{Confirmed: true},
		})
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if runCleanupCmd == nil {
			t.Fatal("expected cleanup command after confirmation")
		}

		resultMsg := runCleanupCmd()
		result, ok := resultMsg.(worktreeCleanupResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupResultMsg", resultMsg)
		}
		if result.err != nil {
			t.Fatalf("cleanup result err = %v", result.err)
		}
		if result.deletedTask {
			t.Fatalf("deletedTask = true, want false")
		}
		if got := transport.requests; len(got) != 4 ||
			got[0] != daemonclient.CommandRuntimeReconcileIssue ||
			got[1] != daemonclient.CommandBoardFetch ||
			got[2] != daemonclient.CommandSessionStop ||
			got[3] != daemonclient.CommandWorktreeRemove {
			t.Fatalf("requests = %v", got)
		}
		if len(transport.commandBudgets) != 4 {
			t.Fatalf("command budget count = %d, want 4", len(transport.commandBudgets))
		}
		if removeBudget := transport.commandBudgets[3]; removeBudget < (worktreeCleanupMutationTimeout - 10*time.Second) {
			t.Fatalf("worktree remove timeout budget = %s, want near %s", removeBudget, worktreeCleanupMutationTimeout)
		}
	})

	t.Run("delete task and cleanup worktree", func(t *testing.T) {
		var deleteBody struct {
			TaskID  string `json:"task_id"`
			Cleanup bool   `json:"cleanup"`
		}
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandRuntimeReconcileIssue:
					respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{})
					if err != nil {
						t.Fatalf("marshal reconcile response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandBoardFetch:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
							{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, HasWorktree: true},
						}),
					}, nil
				case daemonclient.CommandTaskDelete:
					if err := json.Unmarshal(req.Body, &deleteBody); err != nil {
						t.Fatalf("unmarshal task delete request: %v", err)
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
		setTaskSession(t, &m, "az-1", &domain.Session{
			IssueID:  "az-1",
			State:    domain.SessionBusy,
			Worktree: "/tmp/az-1",
		})
		m.nav.SelectTask("az-1", 1)

		updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: "W"})
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if cmd == nil {
			t.Fatal("expected cleanup preflight command")
		}

		preflightMsg := cmd()
		prompt, ok := preflightMsg.(worktreeCleanupConfirmPromptMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupConfirmPromptMsg", preflightMsg)
		}

		updatedAny, confirmCmd := updated.Update(prompt)
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if confirmCmd != nil {
			_ = confirmCmd()
		}
		if updated.pendingCleanup == nil {
			t.Fatal("expected pending cleanup confirmation")
		}

		updatedAny, runCleanupCmd := updated.handleSelection(overlay.SelectionMsg{
			Key:   "yes",
			Value: overlay.ConfirmResult{Confirmed: true},
		})
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if runCleanupCmd == nil {
			t.Fatal("expected full cleanup command")
		}

		resultMsg := runCleanupCmd()
		result, ok := resultMsg.(worktreeCleanupResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupResultMsg", resultMsg)
		}
		if result.err != nil {
			t.Fatalf("cleanup result err = %v", result.err)
		}
		if !result.deletedTask {
			t.Fatalf("deletedTask = false, want true")
		}
		if deleteBody.TaskID != "az-1" || !deleteBody.Cleanup {
			t.Fatalf("delete body = %+v, want az-1 cleanup", deleteBody)
		}
		if got := transport.requests; len(got) != 3 ||
			got[0] != daemonclient.CommandRuntimeReconcileIssue ||
			got[1] != daemonclient.CommandBoardFetch ||
			got[2] != daemonclient.CommandTaskDelete {
			t.Fatalf("requests = %v", got)
		}
	})

	t.Run("cleanup continues when session stop freshness times out", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandRuntimeReconcileIssue:
					respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{})
					if err != nil {
						t.Fatalf("marshal reconcile response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandBoardFetch:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
							{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, HasWorktree: true},
						}),
					}, nil
				case daemonclient.CommandSessionStop:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              false,
						Error: &protocol.ErrorEnvelope{
							Code:      protocol.ErrorCodeTimeout,
							Message:   "refresh runtime state before mutation: wait runtime reconcile: context deadline exceeded",
							Retryable: true,
						},
					}, nil
				case daemonclient.CommandWorktreeRemove:
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
		m.tasks = []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress},
		}
		setTaskSession(t, &m, "az-1", &domain.Session{
			IssueID:  "az-1",
			State:    domain.SessionBusy,
			Worktree: "/tmp/az-1",
		})
		m.nav.SelectTask("az-1", 1)

		updatedAny, cmd := m.handleSelection(overlay.SelectionMsg{Key: "w"})
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if cmd == nil {
			t.Fatal("expected cleanup preflight command")
		}

		preflightMsg := cmd()
		prompt, ok := preflightMsg.(worktreeCleanupConfirmPromptMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupConfirmPromptMsg", preflightMsg)
		}

		updatedAny, confirmCmd := updated.Update(prompt)
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if confirmCmd != nil {
			_ = confirmCmd()
		}
		if updated.pendingCleanup == nil {
			t.Fatal("expected pending cleanup confirmation")
		}

		updatedAny, runCleanupCmd := updated.handleSelection(overlay.SelectionMsg{
			Key:   "yes",
			Value: overlay.ConfirmResult{Confirmed: true},
		})
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if runCleanupCmd == nil {
			t.Fatal("expected cleanup command after confirmation")
		}

		resultMsg := runCleanupCmd()
		result, ok := resultMsg.(worktreeCleanupResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupResultMsg", resultMsg)
		}
		if result.err != nil {
			t.Fatalf("cleanup result err = %v", result.err)
		}

		if got := transport.requests; len(got) != 4 ||
			got[0] != daemonclient.CommandRuntimeReconcileIssue ||
			got[1] != daemonclient.CommandBoardFetch ||
			got[2] != daemonclient.CommandSessionStop ||
			got[3] != daemonclient.CommandWorktreeRemove {
			t.Fatalf("requests = %v", got)
		}
	})

	t.Run("stale clean preflight prompts before forced cleanup after daemon conflict", func(t *testing.T) {
		forceFlags := []bool{}
		stopCalls := 0
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandRuntimeReconcileIssue:
					respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{})
					if err != nil {
						t.Fatalf("marshal reconcile response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandBoardFetch:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
							{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, HasWorktree: true, HasUncommittedChanges: false, GitAdditions: 4, GitDeletions: 2},
						}),
					}, nil
				case daemonclient.CommandSessionStop:
					stopCalls++
					if stopCalls > 1 {
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							OK:              false,
							Error: &protocol.ErrorEnvelope{
								Code:      protocol.ErrorCodeInvalidRequest,
								Message:   "no active session found for issue: az-1",
								Retryable: false,
							},
						}, nil
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
					}, nil
				case daemonclient.CommandWorktreeRemove:
					var body struct {
						ProjectID string `json:"project_id"`
						IssueID   string `json:"issue_id"`
						Force     bool   `json:"force,omitempty"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal worktree remove request: %v", err)
					}
					forceFlags = append(forceFlags, body.Force)
					if !body.Force {
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							OK:              false,
							Error: &protocol.ErrorEnvelope{
								Code:      protocol.ErrorCodeConflict,
								Message:   "failed to remove worktree: contains modified or untracked files, use --force to delete it",
								Retryable: false,
							},
						}, nil
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
		m.tasks = []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress},
		}
		setTaskSession(t, &m, "az-1", &domain.Session{
			IssueID:  "az-1",
			State:    domain.SessionBusy,
			Worktree: "/tmp/az-1",
		})
		m.nav.SelectTask("az-1", 1)

		_, cmd := m.handleSelection(overlay.SelectionMsg{Key: "w"})
		if cmd == nil {
			t.Fatal("expected cleanup preflight command")
		}

		preflightMsg := cmd()
		prompt, ok := preflightMsg.(worktreeCleanupConfirmPromptMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupConfirmPromptMsg", preflightMsg)
		}

		updatedAny, confirmCmd := m.Update(prompt)
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if confirmCmd != nil {
			_ = confirmCmd()
		}
		if updated.pendingCleanup == nil {
			t.Fatal("expected pending cleanup confirmation")
		}

		updatedAny, runInitialCleanupCmd := updated.handleSelection(overlay.SelectionMsg{
			Key:   "yes",
			Value: overlay.ConfirmResult{Confirmed: true},
		})
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if runInitialCleanupCmd == nil {
			t.Fatal("expected initial cleanup command")
		}

		initialMsg := runInitialCleanupCmd()
		cleanupResult, ok := initialMsg.(worktreeCleanupResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupResultMsg", initialMsg)
		}
		if !cleanupResult.needsForce {
			t.Fatalf("needsForce = false, want true")
		}

		updatedAny, forceConfirmCmd := m.Update(cleanupResult)
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if forceConfirmCmd != nil {
			_ = forceConfirmCmd()
		}
		if updated.pendingCleanup == nil {
			t.Fatal("expected pending forced cleanup confirmation")
		}

		updatedAny, runForceCmd := updated.handleSelection(overlay.SelectionMsg{
			Key:   "yes",
			Value: overlay.ConfirmResult{Confirmed: true},
		})
		updated, ok = updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if runForceCmd == nil {
			t.Fatal("expected forced cleanup command")
		}
		forceMsg := runForceCmd()
		forceResult, ok := forceMsg.(worktreeCleanupResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupResultMsg", forceMsg)
		}
		if forceResult.err != nil {
			t.Fatalf("forced cleanup err = %v", forceResult.err)
		}
		if len(forceFlags) != 2 || forceFlags[0] || !forceFlags[1] {
			t.Fatalf("force flags = %v, want [false true]", forceFlags)
		}
		if stopCalls != 2 {
			t.Fatalf("stop calls = %d, want 2", stopCalls)
		}
		if got := transport.requests; len(got) != 6 ||
			got[0] != daemonclient.CommandRuntimeReconcileIssue ||
			got[1] != daemonclient.CommandBoardFetch ||
			got[2] != daemonclient.CommandSessionStop ||
			got[3] != daemonclient.CommandWorktreeRemove ||
			got[4] != daemonclient.CommandSessionStop ||
			got[5] != daemonclient.CommandWorktreeRemove {
			t.Fatalf("requests = %v", got)
		}
	})

	t.Run("dirty preflight forces first removal after confirmation", func(t *testing.T) {
		forceFlags := []bool{}
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandRuntimeReconcileIssue:
					respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{})
					if err != nil {
						t.Fatalf("marshal reconcile response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandBoardFetch:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
							{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, HasWorktree: true, HasUncommittedChanges: true, GitAdditions: 4, GitDeletions: 2},
						}),
					}, nil
				case daemonclient.CommandSessionStop:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
					}, nil
				case daemonclient.CommandWorktreeRemove:
					var body struct {
						Force bool `json:"force,omitempty"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal worktree remove request: %v", err)
					}
					forceFlags = append(forceFlags, body.Force)
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
		m.tasks = []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress}}
		setTaskSession(t, &m, "az-1", &domain.Session{
			IssueID:  "az-1",
			State:    domain.SessionBusy,
			Worktree: "/tmp/az-1",
		})
		m.nav.SelectTask("az-1", 1)

		_, cmd := m.handleSelection(overlay.SelectionMsg{Key: "w"})
		if cmd == nil {
			t.Fatal("expected cleanup preflight command")
		}
		preflightMsg := cmd()
		prompt, ok := preflightMsg.(worktreeCleanupConfirmPromptMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupConfirmPromptMsg", preflightMsg)
		}
		if !prompt.force {
			t.Fatal("prompt.force = false, want true for dirty preflight")
		}
		if promptText := formatWorktreeCleanupConfirmPrompt(prompt); !strings.Contains(promptText, "Force removal will discard modified/untracked files.") {
			t.Fatalf("prompt = %q, want force warning", promptText)
		}

		updatedAny, confirmCmd := m.Update(prompt)
		updated := updatedAny.(Model)
		if confirmCmd != nil {
			_ = confirmCmd()
		}
		if updated.pendingCleanup == nil || !updated.pendingCleanup.force {
			t.Fatalf("pending cleanup = %+v, want force confirmation", updated.pendingCleanup)
		}

		_, runCleanupCmd := updated.handleSelection(overlay.SelectionMsg{
			Key:   "yes",
			Value: overlay.ConfirmResult{Confirmed: true},
		})
		if runCleanupCmd == nil {
			t.Fatal("expected cleanup command")
		}
		cleanupMsg := runCleanupCmd()
		result, ok := cleanupMsg.(worktreeCleanupResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want worktreeCleanupResultMsg", cleanupMsg)
		}
		if result.err != nil {
			t.Fatalf("cleanup result err = %v", result.err)
		}
		if len(forceFlags) != 1 || !forceFlags[0] {
			t.Fatalf("force flags = %v, want [true]", forceFlags)
		}
	})
}

func TestSpaceOpensWorkspaceImmediatelyAndRefreshesInBackground(t *testing.T) {
	const issueID = "az-1"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandRuntimeReconcileIssue:
				var body protocol.RuntimeReconcileIssueRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal reconcile body: %v", err)
				}
				if len(body.IssueIDs) != 1 || body.IssueIDs[0].String() != issueID {
					t.Fatalf("reconcile issue_ids = %+v, want [%s]", body.IssueIDs, issueID)
				}
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandTaskGet:
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalFullTaskSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
						{ID: naming.IssueID(issueID), Title: "Task fresh", Description: "persisted description", Status: domain.StatusDone, Type: domain.TypeTask},
					}),
				}, nil
			case daemonclient.CommandDecisionLinkList:
				body, _ := json.Marshal(daemonclient.DecisionLinkListResult{})
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.editor.EnterNormal()
	m.tasks = []domain.Task{
		{ID: naming.IssueID(issueID), Title: "Task stale", Status: domain.StatusOpen, Type: domain.TypeTask},
		{ID: naming.IssueID("az-2"), Title: "Unrelated board task", Status: domain.StatusInReview, Type: domain.TypeTask},
	}
	m.nav.SelectTask(issueID, 0)

	updatedAny, _ := m.handleNormalMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}

	current := updated.overlayStack.Current()
	workspace, ok := current.(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay on top, got %T", current)
	}
	if !strings.Contains(workspace.View(), "Task stale") {
		t.Fatalf("workspace should open immediately with current projection, got %q", workspace.View())
	}

	refreshMsg := updated.refreshTaskWorkspaceInBackgroundCmd(issueID)()
	nextAny, _ := updated.Update(refreshMsg)
	next, ok := nextAny.(Model)
	if !ok {
		t.Fatalf("next model type = %T, want Model", nextAny)
	}
	refreshed, ok := next.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay after refresh, got %T", next.overlayStack.Current())
	}
	if !strings.Contains(refreshed.View(), "Task fresh") {
		t.Fatalf("workspace should refresh from daemon snapshot, got %q", refreshed.View())
	}
	if !strings.Contains(refreshed.View(), "persisted description") {
		t.Fatalf("workspace should render full task details after refresh, got %q", refreshed.View())
	}
	if len(next.tasks) != 2 || next.tasks[0].Title != "Task fresh" || next.tasks[0].Description != "" || next.tasks[0].Status != domain.StatusDone {
		t.Fatalf("board task after workspace refresh = %+v, want fresh summary task plus preserved unrelated task", next.tasks)
	}
	if next.tasks[1].ID.String() != "az-2" || next.tasks[1].Status != domain.StatusInReview {
		t.Fatalf("unrelated board task after workspace refresh = %+v, want preserved blocked az-2", next.tasks[1])
	}
	columns := next.buildColumns()
	doneTasks := columns[domain.StatusDone.Column()].Tasks
	if len(doneTasks) != 1 || doneTasks[0].ID.String() != issueID {
		t.Fatalf("done column after workspace refresh = %+v, want %s", doneTasks, issueID)
	}
	if got := transport.requests; len(got) != 3 || got[0] != daemonclient.CommandRuntimeReconcileIssue || got[1] != daemonclient.CommandTaskGet || got[2] != daemonclient.CommandDecisionLinkList {
		t.Fatalf("requests = %v", got)
	}
}

func TestTaskWorkspaceRKeyRefreshesCurrentIssue(t *testing.T) {
	const issueID = "az-1"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandRuntimeReconcileIssue:
				var body protocol.RuntimeReconcileIssueRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal reconcile body: %v", err)
				}
				if len(body.IssueIDs) != 1 || body.IssueIDs[0].String() != issueID {
					t.Fatalf("reconcile issue_ids = %+v, want [%s]", body.IssueIDs, issueID)
				}
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{ProjectID: "default"})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandTaskGet:
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalFullTaskSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
						{ID: naming.IssueID(issueID), Title: "Task fresh from r", Description: "refreshed description", Status: domain.StatusInReview, Type: domain.TypeTask},
					}),
				}, nil
			case daemonclient.CommandDecisionLinkList:
				body, _ := json.Marshal(daemonclient.DecisionLinkListResult{})
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{
		{ID: naming.IssueID(issueID), Title: "Task stale", Status: domain.StatusOpen, Type: domain.TypeTask},
	}
	m.nav.SelectTask(issueID, 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))

	updatedAny, selectionCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if selectionCmd == nil {
		t.Fatal("expected r key in workspace to emit a selection command")
	}

	nextAny, refreshCmd := updated.Update(selectionCmd())
	next, ok := nextAny.(Model)
	if !ok {
		t.Fatalf("next model type = %T, want Model", nextAny)
	}
	if refreshCmd == nil {
		t.Fatal("expected r selection to start issue refresh")
	}

	refreshedMsg := refreshCmd()
	refreshedAny, _ := next.Update(refreshedMsg)
	refreshed, ok := refreshedAny.(Model)
	if !ok {
		t.Fatalf("refreshed model type = %T, want Model", refreshedAny)
	}
	workspace, ok := refreshed.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected TaskWorkspaceOverlay after refresh, got %T", refreshed.overlayStack.Current())
	}
	if !strings.Contains(workspace.View(), "Task fresh from r") {
		t.Fatalf("workspace should refresh current issue on r, got %q", workspace.View())
	}
	if !strings.Contains(workspace.View(), "refreshed description") {
		t.Fatalf("workspace should render full task details after r refresh, got %q", workspace.View())
	}
	if len(refreshed.tasks) != 1 || refreshed.tasks[0].Title != "Task fresh from r" || refreshed.tasks[0].Description != "" || refreshed.tasks[0].Status != domain.StatusInReview {
		t.Fatalf("board task after r refresh = %+v", refreshed.tasks)
	}
	if got := transport.requests; len(got) != 3 || got[0] != daemonclient.CommandRuntimeReconcileIssue || got[1] != daemonclient.CommandTaskGet || got[2] != daemonclient.CommandDecisionLinkList {
		t.Fatalf("requests = %v", got)
	}
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

func TestRefreshOpenDiffOverlayFromProjectionBody(t *testing.T) {
	m := newDaemonTestModel(&recordingDaemonTransport{})

	client := &refreshableDiffClient{paths: []string{"old.go"}}
	viewer := diff.NewDiffViewer("/tmp/az-1", "main", client, nil)
	m.overlayStack.Push(viewer)

	initCmd := viewer.Init()
	if initCmd == nil {
		t.Fatal("expected init command")
	}
	initMsg := initCmd()
	updatedViewer, _ := viewer.Update(initMsg)
	viewer = updatedViewer.(*diff.DiffViewer)
	if view := viewer.View(); !strings.Contains(view, "old.go") {
		t.Fatalf("initial view = %q, want old.go", view)
	}

	client.paths = []string{"new.go"}
	refreshCmd := m.refreshOpenDiffOverlayFromProjectionBody(protocol.ProjectionUpdateEventBody{
		Worktree: "/tmp/az-1",
	})
	if refreshCmd == nil {
		t.Fatal("expected diff refresh command for matching worktree")
	}
	if view := viewer.View(); !strings.Contains(view, "Loading changed files...") {
		t.Fatalf("view after refresh trigger = %q, want loading state", view)
	}

	refreshMsg := refreshCmd()
	updatedViewer, _ = viewer.Update(refreshMsg)
	viewer = updatedViewer.(*diff.DiffViewer)

	view := viewer.View()
	if !strings.Contains(view, "new.go") {
		t.Fatalf("refreshed view = %q, want new.go", view)
	}
	if strings.Contains(view, "old.go") {
		t.Fatalf("refreshed view = %q, should not contain old.go", view)
	}
}

func TestHandleSelectionOpenPRAndHelixPaths(t *testing.T) {
	openDiffFromSelection := func(t *testing.T, m Model) Model {
		t.Helper()
		updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "f"})
		if cmd == nil {
			t.Fatal("expected diff base resolution command")
		}
		updatedModel, ok := updated.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updated)
		}
		msg := cmd()
		resolved, ok := msg.(diffViewerResolvedMsg)
		if !ok {
			t.Fatalf("message type = %T, want diffViewerResolvedMsg", msg)
		}
		next, _ := updatedModel.Update(resolved)
		nextModel, ok := next.(Model)
		if !ok {
			t.Fatalf("next model type = %T, want Model", next)
		}
		return nextModel
	}
	diffBaseTransport := func(branch string) *recordingDaemonTransport {
		return &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskMergeBaseTarget {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
					}, nil
				}
				respBody, err := json.Marshal(daemonclient.TaskMergeBaseTarget{
					TargetID: mergeBaseTargetID,
					Branch:   branch,
				})
				if err != nil {
					return protocol.ResponseEnvelope{}, err
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
	}

	t.Run("open diff without session uses repo root", func(t *testing.T) {
		m := newDaemonTestModel(&recordingDaemonTransport{})
		m.tasks = []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, Type: domain.TypeTask}}
		m.repoDir = "/tmp/repo-root"
		m.nav.SelectTask("az-1", 1)

		updatedModel := openDiffFromSelection(t, m)
		current := updatedModel.overlayStack.Current()
		diffOverlay, ok := current.(*diff.DiffViewer)
		if !ok {
			t.Fatalf("overlay type = %T, want *diff.DiffViewer", current)
		}
		if diffOverlay == nil {
			t.Fatal("expected diff overlay instance")
		}
		worktreeField := reflect.ValueOf(diffOverlay).Elem().FieldByName("worktree")
		if !worktreeField.IsValid() || worktreeField.Kind() != reflect.String {
			t.Fatal("diff overlay missing worktree field")
		}
		if got := worktreeField.String(); got != "/tmp/repo-root" {
			t.Fatalf("diff worktree = %q, want %q", got, "/tmp/repo-root")
		}
		if len(updatedModel.toasts) != 0 {
			t.Fatalf("unexpected toasts: %+v", updatedModel.toasts)
		}
	})

	t.Run("open diff prefers task session worktree when present", func(t *testing.T) {
		m := newDaemonTestModel(&recordingDaemonTransport{})
		m.tasks = []domain.Task{{
			ID:     "az-1",
			Title:  "Task 1",
			Status: domain.StatusInProgress,
			Type:   domain.TypeTask,
			Session: &domain.Session{
				IssueID:  "az-1",
				Worktree: "/tmp/az-1",
			},
		}}
		m.repoDir = "/tmp/repo-root"
		m.nav.SelectTask("az-1", 1)

		updatedModel := openDiffFromSelection(t, m)
		diffOverlay, ok := updatedModel.overlayStack.Current().(*diff.DiffViewer)
		if !ok {
			t.Fatalf("overlay type = %T, want *diff.DiffViewer", updatedModel.overlayStack.Current())
		}
		worktreeField := reflect.ValueOf(diffOverlay).Elem().FieldByName("worktree")
		if got := worktreeField.String(); got != "/tmp/az-1" {
			t.Fatalf("diff worktree = %q, want %q", got, "/tmp/az-1")
		}
		baseBranchField := reflect.ValueOf(diffOverlay).Elem().FieldByName("baseBranch")
		if got := baseBranchField.String(); got != "main" {
			t.Fatalf("diff base branch = %q, want %q", got, "main")
		}
		if len(updatedModel.toasts) != 0 {
			t.Fatalf("unexpected toasts: %+v", updatedModel.toasts)
		}
	})

	t.Run("open diff targets direct parent branch", func(t *testing.T) {
		m := newDaemonTestModel(diffBaseTransport("az/az-parent"))
		rootID := naming.IssueID("az-root")
		parentID := naming.IssueID("az-parent")
		childID := naming.IssueID("az-child")
		m.tasks = []domain.Task{
			{
				ID:     rootID,
				Title:  "Root task",
				Status: domain.StatusInProgress,
				Type:   domain.TypeTask,
			},
			{
				ID:          parentID,
				Title:       "Parent task",
				Status:      domain.StatusDone,
				Type:        domain.TypeTask,
				HasWorktree: true,
				ParentID:    &rootID,
			},
			{
				ID:       childID,
				Title:    "Child task",
				Status:   domain.StatusInProgress,
				Type:     domain.TypeTask,
				ParentID: &parentID,
				Session: &domain.Session{
					IssueID:  childID,
					Worktree: "/tmp/az-child",
				},
			},
		}
		m.nav.SelectTask("az-child", 1)

		updatedModel := openDiffFromSelection(t, m)
		diffOverlay, ok := updatedModel.overlayStack.Current().(*diff.DiffViewer)
		if !ok {
			t.Fatalf("overlay type = %T, want *diff.DiffViewer", updatedModel.overlayStack.Current())
		}
		baseBranchField := reflect.ValueOf(diffOverlay).Elem().FieldByName("baseBranch")
		if got := baseBranchField.String(); got != "az/az-parent" {
			t.Fatalf("diff base branch = %q, want %q", got, "az/az-parent")
		}
	})

	t.Run("open diff skips parent without worktree and targets closest ancestor worktree branch", func(t *testing.T) {
		m := newDaemonTestModel(diffBaseTransport("riordan/az-root/root-branch"))
		rootID := naming.IssueID("az-root")
		parentID := naming.IssueID("az-parent")
		childID := naming.IssueID("az-child")
		m.tasks = []domain.Task{
			{
				ID:          rootID,
				Title:       "Root task",
				Status:      domain.StatusInProgress,
				Type:        domain.TypeTask,
				HasWorktree: true,
			},
			{
				ID:       parentID,
				Title:    "Parent task",
				Status:   domain.StatusInProgress,
				Type:     domain.TypeTask,
				ParentID: &rootID,
			},
			{
				ID:       childID,
				Title:    "Child task",
				Status:   domain.StatusInProgress,
				Type:     domain.TypeTask,
				ParentID: &parentID,
				Session: &domain.Session{
					IssueID:  childID,
					Worktree: "/tmp/az-child",
				},
			},
		}
		m.runtimeSignalBranchByTask = map[string]string{
			"az-root": "riordan/az-root/root-branch",
		}
		m.nav.SelectTask("az-child", 1)

		updatedModel := openDiffFromSelection(t, m)
		diffOverlay, ok := updatedModel.overlayStack.Current().(*diff.DiffViewer)
		if !ok {
			t.Fatalf("overlay type = %T, want *diff.DiffViewer", updatedModel.overlayStack.Current())
		}
		baseBranchField := reflect.ValueOf(diffOverlay).Elem().FieldByName("baseBranch")
		if got := baseBranchField.String(); got != "riordan/az-root/root-branch" {
			t.Fatalf("diff base branch = %q, want closest ancestor worktree branch", got)
		}
	})

	t.Run("open diff targets actual parent worktree branch", func(t *testing.T) {
		m := newDaemonTestModel(diffBaseTransport("riordan/az-parent/real-parent-branch"))
		parentID := naming.IssueID("az-parent")
		childID := naming.IssueID("az-child")
		m.tasks = []domain.Task{
			{
				ID:          parentID,
				Title:       "Parent task",
				Status:      domain.StatusDone,
				Type:        domain.TypeTask,
				HasWorktree: true,
			},
			{
				ID:       childID,
				Title:    "Child task",
				Status:   domain.StatusInProgress,
				Type:     domain.TypeTask,
				ParentID: &parentID,
				Session: &domain.Session{
					IssueID:  childID,
					Worktree: "/tmp/az-child",
				},
			},
		}
		m.runtimeSignalBranchByTask = map[string]string{
			"az-parent": "riordan/az-parent/real-parent-branch",
		}
		m.nav.SelectTask("az-child", 1)

		updatedModel := openDiffFromSelection(t, m)
		diffOverlay, ok := updatedModel.overlayStack.Current().(*diff.DiffViewer)
		if !ok {
			t.Fatalf("overlay type = %T, want *diff.DiffViewer", updatedModel.overlayStack.Current())
		}
		baseBranchField := reflect.ValueOf(diffOverlay).Elem().FieldByName("baseBranch")
		if got := baseBranchField.String(); got != "riordan/az-parent/real-parent-branch" {
			t.Fatalf("diff base branch = %q, want actual parent worktree branch", got)
		}
	})

	t.Run("open diff uses done direct parent branch", func(t *testing.T) {
		m := newDaemonTestModel(diffBaseTransport("az/az-parent"))
		parentID := naming.IssueID("az-parent")
		childID := naming.IssueID("az-child")
		m.tasks = []domain.Task{
			{
				ID:          parentID,
				Title:       "Parent task",
				Status:      domain.StatusDone,
				Type:        domain.TypeTask,
				HasWorktree: true,
			},
			{
				ID:       childID,
				Title:    "Child task",
				Status:   domain.StatusInProgress,
				Type:     domain.TypeTask,
				ParentID: &parentID,
				Session: &domain.Session{
					IssueID:  childID,
					Worktree: "/tmp/az-child",
				},
			},
		}
		m.nav.SelectTask("az-child", 1)

		updatedModel := openDiffFromSelection(t, m)
		diffOverlay, ok := updatedModel.overlayStack.Current().(*diff.DiffViewer)
		if !ok {
			t.Fatalf("overlay type = %T, want *diff.DiffViewer", updatedModel.overlayStack.Current())
		}
		baseBranchField := reflect.ValueOf(diffOverlay).Elem().FieldByName("baseBranch")
		if got := baseBranchField.String(); got != "az/az-parent" {
			t.Fatalf("diff base branch = %q, want %q", got, "az/az-parent")
		}
	})

	t.Run("open diff uses runtime worktree signal when task is worktree-backed but session is absent", func(t *testing.T) {
		m := newDaemonTestModel(&recordingDaemonTransport{})
		m.tasks = []domain.Task{{
			ID:          "az-1",
			Title:       "Task 1",
			Status:      domain.StatusInProgress,
			Type:        domain.TypeTask,
			HasWorktree: true,
		}}
		m.runtimeSignalWorktreeByTask = map[string]string{"az-1": "/tmp/az-1"}
		m.repoDir = "/tmp/repo-root"
		m.nav.SelectTask("az-1", 1)

		updatedModel := openDiffFromSelection(t, m)
		diffOverlay, ok := updatedModel.overlayStack.Current().(*diff.DiffViewer)
		if !ok {
			t.Fatalf("overlay type = %T, want *diff.DiffViewer", updatedModel.overlayStack.Current())
		}
		worktreeField := reflect.ValueOf(diffOverlay).Elem().FieldByName("worktree")
		if got := worktreeField.String(); got != "/tmp/az-1" {
			t.Fatalf("diff worktree = %q, want %q", got, "/tmp/az-1")
		}
		if len(updatedModel.toasts) != 0 {
			t.Fatalf("unexpected toasts: %+v", updatedModel.toasts)
		}
	})

	t.Run("open diff with task worktree flag but unknown path warns instead of falling back to repo root", func(t *testing.T) {
		m := newDaemonTestModel(&recordingDaemonTransport{})
		m.tasks = []domain.Task{{
			ID:          "az-1",
			Title:       "Task 1",
			Status:      domain.StatusInProgress,
			Type:        domain.TypeTask,
			HasWorktree: true,
		}}
		m.repoDir = "/tmp/repo-root"
		m.nav.SelectTask("az-1", 1)

		updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "f"})
		if cmd != nil {
			t.Fatal("expected no diff overlay command")
		}
		updatedModel := updated.(Model)
		if len(updatedModel.toasts) != 1 {
			t.Fatalf("toasts = %+v, want 1 warning toast", updatedModel.toasts)
		}
		if got := updatedModel.toasts[0].Message; !strings.Contains(got, "No task worktree available for diff") {
			t.Fatalf("toast message = %q", got)
		}
	})

	t.Run("open PR without session defers to daemon and returns error", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            []byte(`{"project_id":"proj-daemon","worktrees":[]}`),
					}, nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}
		m := newDaemonTestModel(transport)
		m.tasks = []domain.Task{{ID: "az-1", Title: "Task 1", Status: domain.StatusInProgress, Type: domain.TypeTask}}
		m.nav.SelectTask("az-1", 1)

		updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "O"})
		if cmd == nil {
			t.Fatal("expected command when session projection is missing")
		}
		msg := cmd()
		result, ok := msg.(openPRResultMsg)
		if !ok {
			t.Fatalf("cmd message type = %T, want openPRResultMsg", msg)
		}
		if result.err == nil {
			t.Fatal("expected open PR error when daemon has no worktree")
		}
		updatedModel, ok := updated.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updated)
		}
		if len(updatedModel.toasts) == 0 {
			t.Fatal("expected immediate feedback toast")
		}
		if got := updatedModel.toasts[len(updatedModel.toasts)-1].Message; got != "Opening PR for az-1" {
			t.Fatalf("toast = %q, want Opening PR for az-1", got)
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

func TestHandleSelectionUpdateFromMainResolvesWorktreeWithoutSession(t *testing.T) {
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
						{Path: "/tmp/az-1", Branch: "az/az-1", IssueID: "az-1"},
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
			case daemonclient.CommandGitFetch:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal fetch request: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Remote != "origin" {
					t.Fatalf("fetch body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{
					Worktree: body.Worktree,
					Remote:   body.Remote,
				})
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
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{
		ID:          "az-1",
		Title:       "Task 1",
		Status:      domain.StatusInProgress,
		Type:        domain.TypeTask,
		HasWorktree: true,
	}}
	m.nav.SelectTask("az-1", 1)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "u"})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected update-from-main command")
	}

	msg := cmd()
	result, ok := msg.(fetchAndMergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want fetchAndMergeResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("fetch-and-merge err = %v", result.err)
	}

	if got := transport.requests; len(got) != 3 ||
		got[0] != daemonclient.CommandWorktreeList ||
		got[1] != daemonclient.CommandGitFetch ||
		got[2] != daemonclient.CommandGitMerge {
		t.Fatalf("requests = %v", got)
	}
}

func TestHandleSelectionUpdateFromMainUsesLocalBaseBranchInLocalWorkflowMode(t *testing.T) {
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
						{Path: "/tmp/az-1", Branch: "az/az-1", IssueID: "az-1"},
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
			case daemonclient.CommandGitFetch:
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{
					Worktree: "/tmp/az-1",
					Remote:   "origin",
				})
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
				if body.Worktree != "/tmp/az-1" || body.Branch != "main" {
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

	m := newDaemonTestModel(transport)
	m.config.Git.WorkflowMode = "local"
	m.tasks = []domain.Task{{
		ID:          "az-1",
		Title:       "Task 1",
		Status:      domain.StatusInProgress,
		Type:        domain.TypeTask,
		HasWorktree: true,
	}}
	m.nav.SelectTask("az-1", 1)

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "u"})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected update-from-main command")
	}

	msg := cmd()
	result, ok := msg.(fetchAndMergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want fetchAndMergeResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("fetch-and-merge err = %v", result.err)
	}
}

func TestHandleSelectionUpdateFromMainUsesTaskWorkspaceTaskWhenCursorUnavailable(t *testing.T) {
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
						{Path: "/tmp/az-1", Branch: "az/az-1", IssueID: "az-1"},
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
			case daemonclient.CommandGitFetch:
				var body daemonclient.GitCommandRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal fetch request: %v", err)
				}
				if body.Worktree != "/tmp/az-1" || body.Remote != "origin" {
					t.Fatalf("fetch body = %+v", body)
				}
				respBody, err := json.Marshal(daemonclient.GitCommandResponse{
					Worktree: body.Worktree,
					Remote:   body.Remote,
				})
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
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{
		ID:             "az-1",
		Title:          "Task 1",
		Status:         domain.StatusInProgress,
		Type:           domain.TypeTask,
		HasTmuxSession: true,
	}}
	// Simulate cursor/projection drift while task workspace is open.
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "u"})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected update-from-main command")
	}

	msg := cmd()
	result, ok := msg.(fetchAndMergeResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want fetchAndMergeResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("fetch-and-merge err = %v", result.err)
	}

	if got := transport.requests; len(got) != 3 ||
		got[0] != daemonclient.CommandWorktreeList ||
		got[1] != daemonclient.CommandGitFetch ||
		got[2] != daemonclient.CommandGitMerge {
		t.Fatalf("requests = %v", got)
	}
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
	origGeneratePRContent := generatePRContent
	t.Cleanup(func() { generatePRContent = origGeneratePRContent })
	generatePRContent = func(_ context.Context, req prGenerationRequest) (prGeneratedContent, error) {
		if req.IssueID != "az-1" || req.Branch != "az/az-1" || req.BaseBranch != "main" {
			t.Fatalf("generation request = %+v", req)
		}
		if req.IssueTitle != "Task 1" || req.IssueDescription != "Desc 1" {
			t.Fatalf("generation context = %+v", req)
		}
		return prGeneratedContent{
			Title: "Generated PR title",
			Body:  "Generated PR body",
		}, nil
	}

	expectedDraft := true
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
			case daemonclient.CommandTaskGet:
				body, err := json.Marshal(protocol.TaskListSnapshotPayload{
					SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
					ProtocolVersion:  req.ProtocolVersion,
					SnapshotRevision: 2,
					ProjectID:        "default",
					LastCheckedAt:    time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC),
					Freshness:        protocol.TaskListFreshnessFresh,
					Tasks:            []domain.Task{{ID: "az-1", Title: "Task 1", Description: "Desc 1"}},
				})
				if err != nil {
					t.Fatalf("marshal task.get response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			case daemonclient.CommandPRCreate:
				var body daemonclient.CreatePullRequestParams
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Title != "Generated PR title" || body.Body != "Generated PR body" {
					t.Fatalf("unexpected generated payload: %+v", body)
				}
				if body.Branch != "az/az-1" || body.BaseBranch != "main" || body.IssueID != "az-1" {
					t.Fatalf("request body = %+v", body)
				}
				if body.Draft != expectedDraft {
					t.Fatalf("draft = %v, want %v", body.Draft, expectedDraft)
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
			default:
				t.Fatalf("unexpected command %q", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	expectedDraft = m.config.PR.DraftByDefault
	m.tasks = []domain.Task{{ID: "az-1", Title: "Task 1"}}
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
		t.Fatal("expected PR create command")
	}
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	prCreatedMsg := cmd()
	created, ok := prCreatedMsg.(prCreatedResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want prCreatedResultMsg", prCreatedMsg)
	}
	if created.err != nil || created.url != "https://example.com/pr/7" || created.title != "Generated PR title" {
		t.Fatalf("create result = %+v", created)
	}
	afterCreate, _ := updatedModel.Update(created)
	afterModel, ok := afterCreate.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", afterCreate)
	}
	if len(afterModel.toasts) == 0 {
		t.Fatal("expected success toast after PR creation")
	}
	if got := transport.requests; len(got) != 3 || got[0] != daemonclient.CommandWorktreeList || got[1] != daemonclient.CommandTaskGet || got[2] != daemonclient.CommandPRCreate {
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

func TestCheckBranchBehindCmdUsesFallbackBaseBranch(t *testing.T) {
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
				CommitsBehind: 0,
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
	m.config.Git.BaseBranch = ""
	msg := m.checkBranchBehindCmd("/tmp/az-1", "az-1")()
	result, ok := msg.(branchBehindMsg)
	if !ok {
		t.Fatalf("message type = %T, want branchBehindMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("result = %+v, want nil err", result)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandGitBranchBehind {
		t.Fatalf("requests = %v", got)
	}
}

func TestCheckBranchBehindCmdSkipsDaemonWhenWorktreeUnavailable(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandWorktreeList {
				t.Fatalf("unexpected daemon command: %s", req.Command)
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
				}{},
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
	m.config.Git.BaseBranch = ""

	msg := m.checkBranchBehindCmd("", "az-1")()
	result, ok := msg.(branchBehindMsg)
	if !ok {
		t.Fatalf("message type = %T, want branchBehindMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("result = %+v, want nil err", result)
	}
	if result.commitsBehind != 0 {
		t.Fatalf("commitsBehind = %d, want 0", result.commitsBehind)
	}
	if result.worktree != "" {
		t.Fatalf("worktree = %q, want empty", result.worktree)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandWorktreeList {
		t.Fatalf("requests = %v, want only worktree list", got)
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
	if !strings.Contains(view, "Merge into") || !strings.Contains(view, target.ID.String()) {
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
			case protocol.CommandOperationSubmit:
				body := decodeSessionOperationSubmit(t, req, daemonclient.CommandSessionStart)
				if body.SessionID != naming.SessionID(childID) {
					t.Fatalf("session ID = %q, want %q", body.SessionID, childID)
				}
				if body.BaseBranch != baseBranch {
					t.Fatalf("base branch = %q, want %q", body.BaseBranch, baseBranch)
				}
				if body.Yolo {
					t.Fatal("expected yolo=false for Shift+S start")
				}
				if body.StartWork == nil || !*body.StartWork {
					t.Fatal("expected start_work=true for Shift+S start")
				}
				return sessionOperationSubmitResponse(t, req, "op-start", daemonclient.CommandSessionStart, childID, protocol.OperationStateQueued), nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.config.Git.BaseBranch = baseBranch
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{
			ID:     childIssueID,
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
	if len(transport.requests) != 1 || transport.requests[0] != protocol.CommandOperationSubmit {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestStartSessionLowercaseSStartsTmuxOnly(t *testing.T) {
	baseBranch := "develop"
	childID := "az-child"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case protocol.CommandOperationSubmit:
				body := decodeSessionOperationSubmit(t, req, daemonclient.CommandSessionStart)
				if body.SessionID != naming.SessionID(childID) {
					t.Fatalf("session ID = %q, want %q", body.SessionID, childID)
				}
				if body.BaseBranch != baseBranch {
					t.Fatalf("base branch = %q, want %q", body.BaseBranch, baseBranch)
				}
				if body.Yolo {
					t.Fatal("expected yolo=false for s start")
				}
				if body.StartWork == nil || *body.StartWork {
					t.Fatal("expected start_work=false for s start")
				}
				return sessionOperationSubmitResponse(t, req, "op-start", daemonclient.CommandSessionStart, childID, protocol.OperationStateQueued), nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.config.Git.BaseBranch = baseBranch
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{
			ID:     childIssueID,
			Title:  "Child task",
			Status: domain.StatusInProgress,
			Type:   domain.TypeTask,
		},
	}
	m.nav.SelectTask(childID, 0)

	_, startCmd := m.handleSelection(overlay.SelectionMsg{Key: "s"})
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
	if len(transport.requests) != 1 || transport.requests[0] != protocol.CommandOperationSubmit {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestStartSessionCommandReturnsPendingOperationToast(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			_ = decodeSessionOperationSubmit(t, req, daemonclient.CommandSessionStart)
			return sessionOperationSubmitResponse(t, req, "op-start", daemonclient.CommandSessionStart, "az-child", protocol.OperationStateQueued), nil
		},
	}

	m := newDaemonTestModel(transport)
	startMsg := m.startSessionCmd("az-child", "main", false, true)()
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
	parentIssueID := naming.IssueID(parentID)
	childIssueID := naming.IssueID(childID)

	m := newDaemonTestModel(&recordingDaemonTransport{})
	m.config.Git.BaseBranch = baseBranch
	m.tasks = []domain.Task{
		{
			ID:       childIssueID,
			Title:    "Child task",
			Status:   domain.StatusInProgress,
			Type:     domain.TypeTask,
			ParentID: &parentIssueID,
		},
		{
			ID:     parentIssueID,
			Title:  "Parent task",
			Status: domain.StatusDone,
			Type:   domain.TypeTask,
		},
	}
	setTaskSession(t, &m, parentID, &domain.Session{
		IssueID:  naming.IssueID(parentID),
		State:    domain.SessionBusy,
		Worktree: "/tmp/parent",
	})

	candidates, upstreamCount := m.sessionOriginCandidates(&m.tasks[0])
	if upstreamCount != 0 {
		t.Fatalf("upstreamCount = %d, want 0", upstreamCount)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v, want base branch only", candidates)
	}
	if candidates[0].ID != baseBranch || candidates[0].Label != baseBranch || !candidates[0].IsMain {
		t.Fatalf("base candidate = %+v, want base branch %q", candidates[0], baseBranch)
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
			case protocol.CommandOperationSubmit:
				body := decodeSessionOperationSubmit(t, req, daemonclient.CommandSessionStart)
				if body.SessionID != naming.SessionID(childID) {
					t.Fatalf("session ID = %q, want %q", body.SessionID, childID)
				}
				if body.BaseBranch != baseBranch {
					t.Fatalf("base branch = %q, want %q", body.BaseBranch, baseBranch)
				}
				if body.Yolo {
					t.Fatal("expected yolo=false for Shift+S start")
				}
				if body.StartWork == nil || !*body.StartWork {
					t.Fatal("expected start_work=true for Shift+S start")
				}
				return sessionOperationSubmitResponse(t, req, "op-start", daemonclient.CommandSessionStart, childID, protocol.OperationStateQueued), nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.config.Git.BaseBranch = baseBranch
	childIssueID := naming.IssueID(childID)
	parentAIssueID := naming.IssueID(parentA)
	parentBIssueID := naming.IssueID(parentB)
	m.tasks = []domain.Task{
		{
			ID:           childIssueID,
			Title:        "Child task",
			Status:       domain.StatusInProgress,
			Type:         domain.TypeTask,
			ParentID:     &parentAIssueID,
			Dependencies: []domain.Dependency{{ID: parentBIssueID, Type: domain.DependencyBlocks}},
		},
		{
			ID:     parentAIssueID,
			Title:  "Parent A",
			Status: domain.StatusDone,
			Type:   domain.TypeTask,
		},
		{
			ID:     parentBIssueID,
			Title:  "Parent B",
			Status: domain.StatusInProgress,
			Type:   domain.TypeTask,
		},
	}
	setTaskSession(t, &m, parentA, &domain.Session{
		IssueID:  naming.IssueID(parentA),
		State:    domain.SessionBusy,
		Worktree: "/tmp/parent-a",
	})
	setTaskSession(t, &m, parentB, &domain.Session{
		IssueID:  naming.IssueID(parentB),
		State:    domain.SessionBusy,
		Worktree: "/tmp/parent-b",
	})
	m.nav.SelectTask(childID, 0)

	candidates, upstreamCount := m.sessionOriginCandidates(&m.tasks[0])
	if upstreamCount != 0 {
		t.Fatalf("upstreamCount = %d, want 0", upstreamCount)
	}
	if len(candidates) != 1 || candidates[0].ID != baseBranch {
		t.Fatalf("candidates = %+v, want base branch only", candidates)
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
	if len(transport.requests) != 1 || transport.requests[0] != protocol.CommandOperationSubmit {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestStartSessionBangStartsYoloFromBaseBranch(t *testing.T) {
	baseBranch := "develop"
	childID := "az-child"

	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case protocol.CommandOperationSubmit:
				body := decodeSessionOperationSubmit(t, req, daemonclient.CommandSessionStart)
				if body.SessionID != naming.SessionID(childID) {
					t.Fatalf("session ID = %q, want %q", body.SessionID, childID)
				}
				if body.BaseBranch != baseBranch {
					t.Fatalf("base branch = %q, want %q", body.BaseBranch, baseBranch)
				}
				if !body.Yolo {
					t.Fatal("expected yolo=true for ! start")
				}
				if body.StartWork == nil || !*body.StartWork {
					t.Fatal("expected start_work=true for ! start")
				}
				return sessionOperationSubmitResponse(t, req, "op-start", daemonclient.CommandSessionStart, childID, protocol.OperationStateQueued), nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.config.Git.BaseBranch = baseBranch
	childIssueID := naming.IssueID(childID)
	m.tasks = []domain.Task{
		{
			ID:     childIssueID,
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
	if len(transport.requests) != 1 || transport.requests[0] != protocol.CommandOperationSubmit {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestStopSessionCommandPreservesDaemonProjection(t *testing.T) {
	startedAt := time.Date(2026, time.March, 25, 11, 0, 0, 0, time.UTC)
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			body := decodeSessionOperationSubmit(t, req, daemonclient.CommandSessionStop)
			if body.SessionID != naming.SessionID("az-child") {
				t.Fatalf("stop body = %+v, want az-child", body)
			}
			return sessionOperationSubmitResponse(t, req, "op-stop", daemonclient.CommandSessionStop, "az-child", protocol.OperationStateQueued), nil
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
	if cmd == nil {
		t.Fatal("expected session stop completion to refresh daemon projection")
	}
	updatedModel, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if session, ok := updatedModel.sessions["az-child"]; !ok || session == nil || session.Worktree != "/tmp/az-child" {
		t.Fatalf("session projection = %+v, want preserved worktree /tmp/az-child", session)
	}
	if len(transport.requests) != 1 || transport.requests[0] != protocol.CommandOperationSubmit {
		t.Fatalf("requests = %v", transport.requests)
	}
}

func TestTaskWorkspaceStopSessionKeyKeepsOverlayOpen(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case protocol.CommandOperationSubmit:
				body := decodeSessionOperationSubmit(t, req, daemonclient.CommandSessionStop)
				if body.SessionID != naming.SessionID("az-1") {
					t.Fatalf("stop body = %+v, want az-1", body)
				}
				return sessionOperationSubmitResponse(t, req, "op-stop", daemonclient.CommandSessionStop, "az-1", protocol.OperationStateQueued), nil
			case daemonclient.CommandBoardFetch:
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{{
						ID:          "az-1",
						Title:       "Task 1",
						Status:      domain.StatusInProgress,
						Type:        domain.TypeTask,
						HasWorktree: true,
					}}),
				}, nil
			default:
				t.Fatalf("command = %q, want %q or %q", req.Command, protocol.CommandOperationSubmit, daemonclient.CommandBoardFetch)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	task := m.tasks[0]
	task.HasTmuxSession = true
	task.Session = &domain.Session{IssueID: "az-1", State: domain.SessionBusy}
	m.tasks[0] = task
	m.nav.SelectTask(task.ID.String(), 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(task, m.tasks, nil, 120, 30))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("expected selection command from task workspace")
	}
	selectionMsg := cmd()
	selection, ok := selectionMsg.(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("command message type = %T, want overlay.SelectionMsg", selectionMsg)
	}
	if selection.Key != "x" {
		t.Fatalf("selection key = %q, want x", selection.Key)
	}
	updated, cmd := next.(Model).Update(selection)
	if cmd == nil {
		t.Fatal("expected stop session command")
	}
	stopMsg := cmd()
	stopped, ok := stopMsg.(sessionStoppedMsg)
	if !ok {
		t.Fatalf("stop command message type = %T, want sessionStoppedMsg", stopMsg)
	}
	if stopped.issueID != "az-1" {
		t.Fatalf("stopped issue = %q, want az-1", stopped.issueID)
	}

	updatedModel := updated.(Model)
	if _, ok := updatedModel.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); !ok {
		t.Fatalf("expected task workspace overlay to remain open, got %T", updatedModel.overlayStack.Current())
	}
	if len(transport.requests) != 1 || transport.requests[0] != protocol.CommandOperationSubmit {
		t.Fatalf("requests = %v", transport.requests)
	}

	stoppedModelAny, refreshCmd := updatedModel.Update(stopMsg)
	if refreshCmd == nil {
		t.Fatal("expected stop completion to refresh daemon projection")
	}
	stoppedModel := stoppedModelAny.(Model)
	if _, ok := stoppedModel.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); !ok {
		t.Fatalf("expected task workspace overlay to remain open after stop completion, got %T", stoppedModel.overlayStack.Current())
	}

	loadedMsg := refreshCmd()
	loadedModelAny, _ := stoppedModel.Update(loadedMsg)
	loadedModel := loadedModelAny.(Model)
	workspace, ok := loadedModel.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace overlay after refresh, got %T", loadedModel.overlayStack.Current())
	}
	view := workspace.View()
	if strings.Contains(view, "Pause session") || strings.Contains(view, "Stop session") {
		t.Fatalf("stopped session actions should not remain after daemon refresh: %q", view)
	}
	if !strings.Contains(view, "Start session") {
		t.Fatalf("expected start action after daemon refresh removed session: %q", view)
	}
	if got := transport.requests; len(got) != 2 || got[0] != protocol.CommandOperationSubmit || got[1] != daemonclient.CommandBoardFetch {
		t.Fatalf("requests = %v", got)
	}
}

func TestTaskWorkspaceCleanupSelectsTargetAndConfirmReplacesWorkspace(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandRuntimeReconcileIssue:
				respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{})
				if err != nil {
					t.Fatalf("marshal reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			case daemonclient.CommandBoardFetch:
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
						{ID: "az-1", Title: "Cleanup target", Status: domain.StatusOpen, HasWorktree: true},
						{ID: "az-2", Title: "Previous board selection", Status: domain.StatusOpen},
					}),
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		},
	}

	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{
		{ID: "az-1", Title: "Cleanup target", Status: domain.StatusOpen, HasWorktree: true},
		{ID: "az-2", Title: "Previous board selection", Status: domain.StatusOpen},
	}
	m.nav.SelectTask("az-2", 0)
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if cmd == nil {
		t.Fatal("expected cleanup selection command from task workspace")
	}
	selection, ok := cmd().(overlay.SelectionMsg)
	if !ok || selection.Key != "w" {
		t.Fatalf("selection = %#v, want w", selection)
	}

	updatedAny, preflightCmd := next.(Model).Update(selection)
	updated := updatedAny.(Model)
	if preflightCmd == nil {
		t.Fatal("expected cleanup preflight command")
	}
	if got := updated.nav.GetCursor().TaskID; got != "az-1" {
		t.Fatalf("cursor task after cleanup action = %q, want az-1", got)
	}
	if _, ok := updated.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); !ok {
		t.Fatalf("expected task workspace to remain under cleanup preflight, got %T", updated.overlayStack.Current())
	}

	preflightMsg := preflightCmd()
	prompt, ok := preflightMsg.(worktreeCleanupConfirmPromptMsg)
	if !ok {
		t.Fatalf("preflight message = %T, want worktreeCleanupConfirmPromptMsg", preflightMsg)
	}
	promptedAny, _ := updated.Update(prompt)
	prompted := promptedAny.(Model)
	if _, ok := prompted.overlayStack.Current().(*overlay.TaskWorkspaceOverlay); ok {
		t.Fatal("expected confirm dialog on top of task workspace")
	}
	if got := prompted.nav.GetCursor().TaskID; got != "az-1" {
		t.Fatalf("cursor task after cleanup prompt = %q, want az-1", got)
	}

	cancelledAny, _ := prompted.handleSelection(overlay.SelectionMsg{Key: "no"})
	cancelled := cancelledAny.(Model)
	if current := cancelled.overlayStack.Current(); current != nil {
		t.Fatalf("expected no stacked workspace after cancelling cleanup, got %T", current)
	}
}

func TestPerformCleanupRoutesDaemonCleanupAndPreservesCounts(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandProjectCleanup {
				t.Fatalf("unexpected command: %s", req.Command)
			}
			var body protocol.ProjectCleanupRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal project cleanup request: %v", err)
			}
			if body.ProjectID != "proj-1" {
				t.Fatalf("project cleanup body = %+v", body)
			}
			wantCategories := []string{
				"delete_old_done",
				"archive_done",
				"remove_orphaned_worktrees",
				"clean_stale_sessions",
			}
			if !reflect.DeepEqual(body.Categories, wantCategories) {
				t.Fatalf("cleanup categories = %+v, want %+v", body.Categories, wantCategories)
			}
			respBody, err := json.Marshal(protocol.ProjectCleanupResponseBody{
				ProjectID:        body.ProjectID,
				Deleted:          1,
				Archived:         2,
				WorktreesRemoved: 2,
				SessionsCleaned:  1,
			})
			if err != nil {
				t.Fatalf("marshal project cleanup response: %v", err)
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
	m.currentProject = "proj-1"
	m.daemonClient.WithProjectID(m.daemonProjectID())

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
	if got := transport.requests; len(got) != 1 || got[0] != protocol.CommandProjectCleanup {
		t.Fatalf("requests = %v", got)
	}
}

func TestPerformCleanupOrphanedWorktreesUsesExtendedDeadline(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandProjectCleanup {
				t.Fatalf("unexpected command: %s", req.Command)
			}
			respBody, err := json.Marshal(protocol.ProjectCleanupResponseBody{
				ProjectID:        naming.ProjectID("proj-1"),
				WorktreesRemoved: 1,
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
		},
	}

	m := newDaemonTestModel(transport)
	m.currentProject = "proj-1"
	m.daemonClient.WithProjectID(m.daemonProjectID())

	result, err := m.performCleanup(context.Background(), []string{"remove_orphaned_worktrees"})
	if err != nil {
		t.Fatalf("performCleanup error: %v", err)
	}
	if result.WorktreesRemoved != 1 {
		t.Fatalf("worktrees removed = %d, want 1", result.WorktreesRemoved)
	}
	if got := len(transport.requests); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if transport.requests[0] != protocol.CommandProjectCleanup {
		t.Fatalf("command = %s, want %s", transport.requests[0], protocol.CommandProjectCleanup)
	}
	if got := len(transport.commandBudgets); got != 1 {
		t.Fatalf("command deadline count = %d, want 1", got)
	}
	if transport.commandBudgets[0] < (worktreeCleanupMutationTimeout - 10*time.Second) {
		t.Fatalf("cleanup timeout budget = %s, want near %s", transport.commandBudgets[0], worktreeCleanupMutationTimeout)
	}
}

func TestCleanupWorktreeUsesExtendedDaemonDeadlines(t *testing.T) {
	transport := &recordingDaemonTransport{}
	m := newDaemonTestModel(transport)

	msg := m.cleanupWorktreeCmd("az-1", false, false)()
	result, ok := msg.(worktreeCleanupResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want worktreeCleanupResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("cleanup result err = %v", result.err)
	}
	if got := transport.requests; len(got) != 2 || got[0] != daemonclient.CommandSessionStop || got[1] != daemonclient.CommandWorktreeRemove {
		t.Fatalf("requests = %v, want session.stop then worktree.remove", got)
	}
	if got := len(transport.commandBudgets); got != 2 {
		t.Fatalf("command deadline count = %d, want 2", got)
	}
	for i, budget := range transport.commandBudgets {
		if budget < worktreeCleanupMutationTimeout-10*time.Second {
			t.Fatalf("command budget[%d] = %s, want near %s", i, budget, worktreeCleanupMutationTimeout)
		}
	}
}

func TestBulkCleanupWorktreeUsesPerStepExtendedDaemonDeadlines(t *testing.T) {
	transport := &recordingDaemonTransport{}
	m := newDaemonTestModel(transport)
	m.tasks = []domain.Task{{ID: "az-1", Status: domain.StatusOpen}}

	msg := m.bulkCleanupWorktreeCmd([]string{"az-1"}, true)()
	result, ok := msg.(bulkStatusResultMsg)
	if !ok {
		t.Fatalf("message type = %T, want bulkStatusResultMsg", msg)
	}
	if result.failed != 0 || result.updated != 1 {
		t.Fatalf("bulk result = %+v, want one success", result)
	}
	if got := transport.requests; len(got) != 1 || got[0] != daemonclient.CommandTaskDelete {
		t.Fatalf("requests = %v, want daemon task.delete cleanup", got)
	}
	if got := len(transport.commandBudgets); got != 1 {
		t.Fatalf("command deadline count = %d, want 1", got)
	}
	if transport.commandBudgets[0] < worktreeCleanupMutationTimeout-10*time.Second {
		t.Fatalf("delete cleanup budget = %s, want near %s", transport.commandBudgets[0], worktreeCleanupMutationTimeout)
	}
}

func TestBulkCleanupWorktreeTracksPendingDaemonOperations(t *testing.T) {
	pendingResponse := func(t *testing.T, req protocol.RequestEnvelope, operationID string) protocol.ResponseEnvelope {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"operation_id": operationID,
			"state":        string(protocol.OperationStateQueued),
		})
		if err != nil {
			t.Fatalf("marshal pending response: %v", err)
		}
		return protocol.ResponseEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindResponse,
			OK:              true,
			Body:            body,
		}
	}

	t.Run("cleanup only", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandSessionStop:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
					}, nil
				case daemonclient.CommandWorktreeRemove:
					return pendingResponse(t, req, "op-remove"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}
		m := newDaemonTestModel(transport)
		m.tasks = []domain.Task{{ID: "az-1", Status: domain.StatusOpen}}

		msg := m.bulkCleanupWorktreeCmd([]string{"az-1"}, false)()
		result, ok := msg.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want bulkStatusResultMsg", msg)
		}
		if result.updated != 0 || result.failed != 0 || len(result.pending) != 1 {
			t.Fatalf("bulk result = %+v, want one pending operation", result)
		}

		updatedAny, cmd := m.Update(result)
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if cmd == nil {
			t.Fatal("expected refresh command after pending cleanup")
		}
		if pending := updated.pendingCleanupOps["op-remove"]; pending.taskID != "az-1" || pending.deletedTask || pending.force {
			t.Fatalf("pending cleanup = %+v, want cleanup-only az-1", pending)
		}
		if got := updated.operationTaskID["op-remove"]; got != "az-1" {
			t.Fatalf("operation task id = %q, want az-1", got)
		}
		progress := updated.pendingMutationForTask("az-1")
		if progress == nil || progress.OperationID != "op-remove" || progress.State != string(protocol.OperationStateQueued) {
			t.Fatalf("pending progress = %+v, want queued op-remove", progress)
		}
	})

	t.Run("delete and cleanup", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskDelete:
					return pendingResponse(t, req, "op-delete"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}
		m := newDaemonTestModel(transport)
		m.tasks = []domain.Task{{ID: "az-1", Status: domain.StatusOpen}}

		msg := m.bulkCleanupWorktreeCmd([]string{"az-1"}, true)()
		result, ok := msg.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want bulkStatusResultMsg", msg)
		}
		if result.updated != 0 || result.failed != 0 || len(result.pending) != 1 {
			t.Fatalf("bulk result = %+v, want one pending operation", result)
		}

		updatedAny, cmd := m.Update(result)
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if cmd == nil {
			t.Fatal("expected refresh command after pending cleanup")
		}
		if pending := updated.pendingCleanupOps["op-delete"]; pending.taskID != "az-1" || !pending.deletedTask || pending.force {
			t.Fatalf("pending cleanup = %+v, want delete+cleanup az-1", pending)
		}
		if got := updated.operationTaskID["op-delete"]; got != "az-1" {
			t.Fatalf("operation task id = %q, want az-1", got)
		}
		progress := updated.pendingMutationForTask("az-1")
		if progress == nil || progress.OperationID != "op-delete" || progress.State != string(protocol.OperationStateQueued) {
			t.Fatalf("pending progress = %+v, want queued op-delete", progress)
		}
	})
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
	m.tasks = append(m.tasks, domain.Task{ID: "az-child", Title: "Child", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask})
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
	signals := updatedModel.runtimeSignalsForBoard()["az-child"]
	if signals.PendingOperationID != "op-merge" || signals.PendingOperationState != string(protocol.OperationStateRunning) {
		t.Fatalf("pending update-from-base signals = %+v", signals)
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

func TestTaskEventBodyAppliesWithoutSnapshotRefresh(t *testing.T) {
	m := newTestModel()
	m.daemonRevision = 4
	m.daemonEvents = make(chan protocol.EventEnvelope)
	m.tasks = []domain.Task{{ID: "az-1", Title: "Old", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask}}
	m.nav.SelectTask("az-1", 0)

	updatedTask := domain.Task{ID: "az-1", Title: "Updated", Description: "Updated detail should stay out of board state", Status: domain.StatusInReview, Priority: domain.P1, Type: domain.TypeBug}
	body, err := json.Marshal(protocol.TaskEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		TaskID:    "az-1",
		Task:      &updatedTask,
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal task event body: %v", err)
	}

	updatedAny, cmd := m.Update(daemonStreamEventMsg{event: protocol.EventEnvelope{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		Revision:  5,
		Event:     protocol.EventTaskUpdated,
		Body:      body,
	}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if cmd == nil {
		t.Fatal("expected next stream wait command")
	}
	if updated.daemonRevision != 5 {
		t.Fatalf("daemonRevision = %d, want 5", updated.daemonRevision)
	}
	if len(updated.tasks) != 1 || updated.tasks[0].Title != "Updated" || updated.tasks[0].Description != "" || updated.tasks[0].Status != domain.StatusInReview {
		t.Fatalf("tasks after task event = %+v", updated.tasks)
	}
	if got := updated.nav.GetCursor().TaskID; got != "az-1" {
		t.Fatalf("cursor task = %q, want az-1", got)
	}

	deleteBody, err := json.Marshal(protocol.TaskEventBody{
		ProjectID: naming.ProjectID(updated.daemonProjectID()),
		TaskID:    "az-1",
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal delete task event body: %v", err)
	}
	deletedAny, _ := updated.Update(daemonStreamEventMsg{event: protocol.EventEnvelope{
		ProjectID: naming.ProjectID(updated.daemonProjectID()),
		Revision:  6,
		Event:     protocol.EventTaskDeleted,
		Body:      deleteBody,
	}})
	deleted := deletedAny.(Model)
	if deleted.daemonRevision != 6 {
		t.Fatalf("daemonRevision after delete = %d, want 6", deleted.daemonRevision)
	}
	if len(deleted.tasks) != 0 {
		t.Fatalf("tasks after delete event = %+v, want empty", deleted.tasks)
	}
}

func TestDaemonStreamEventBatchAppliesTaskEventBehindProjectionBacklog(t *testing.T) {
	m := newTestModel()
	m.daemonRevision = 4
	m.daemonEvents = make(chan protocol.EventEnvelope)
	m.tasks = []domain.Task{{ID: "az-1", Title: "Existing", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask}}

	createdTask := domain.Task{ID: "az-2", Title: "Created behind backlog", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask}
	body, err := json.Marshal(protocol.TaskEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		TaskID:    "az-2",
		Task:      &createdTask,
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal task event body: %v", err)
	}

	updatedAny, cmd := m.Update(daemonStreamEventMsg{events: []protocol.EventEnvelope{
		{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  5,
			Event:     protocol.EventGitStatusUpdated,
		},
		{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  6,
			Event:     protocol.EventTaskCreated,
			Body:      body,
		},
	}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if cmd == nil {
		t.Fatal("expected next stream wait command")
	}
	if updated.daemonRevision != 6 {
		t.Fatalf("daemonRevision = %d, want 6", updated.daemonRevision)
	}
	if len(updated.tasks) != 2 || updated.tasks[1].ID.String() != "az-2" {
		t.Fatalf("tasks after batch = %+v, want created task appended", updated.tasks)
	}
}

func TestDaemonStreamPriorityTaskEventAppliesAcrossAnnotatedTelemetryGap(t *testing.T) {
	m := newTestModel()
	m.daemonRevision = 0
	m.daemonEvents = make(chan protocol.EventEnvelope)
	m.tasks = []domain.Task{{ID: "az-1", Title: "Existing", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask}}

	createdTask := domain.Task{ID: "az-2", Title: "Priority created", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask}
	body, err := json.Marshal(protocol.TaskEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		TaskID:    "az-2",
		Task:      &createdTask,
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal task event body: %v", err)
	}

	updatedAny, cmd := m.Update(daemonStreamEventMsg{event: protocol.EventEnvelope{
		ProjectID:        naming.ProjectID(m.daemonProjectID()),
		Revision:         3,
		SkippedRevisions: []uint64{1, 2},
		Event:            protocol.EventTaskCreated,
		Body:             body,
	}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if cmd == nil {
		t.Fatal("expected next stream wait command")
	}
	if updated.daemonRevision != 3 {
		t.Fatalf("daemonRevision = %d, want 3", updated.daemonRevision)
	}
	if len(updated.tasks) != 2 || updated.tasks[1].ID.String() != "az-2" {
		t.Fatalf("tasks after priority event = %+v, want created task appended without rehydrate", updated.tasks)
	}
	if updated.daemonEvents == nil {
		t.Fatal("priority annotated telemetry gap should not clear stream for rehydrate")
	}
}

func TestDaemonStreamRetainedCommandAndPriorityTaskApplyAcrossAnnotatedTelemetryGaps(t *testing.T) {
	m := newTestModel()
	m.daemonRevision = 0
	m.daemonEvents = make(chan protocol.EventEnvelope)
	m.tasks = []domain.Task{{ID: "az-1", Title: "Existing", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask}}

	createdTask := domain.Task{ID: "az-2", Title: "Priority created", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask}
	taskBody, err := json.Marshal(protocol.TaskEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		TaskID:    "az-2",
		Task:      &createdTask,
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal task event body: %v", err)
	}
	uiBody, err := json.Marshal(protocol.UICommandEventBody{
		ProjectID: naming.ProjectID(m.daemonProjectID()),
		Command:   protocol.UICommandOpenTaskWorkspace,
		IssueID:   "az-1",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal ui event body: %v", err)
	}

	updatedAny, cmd := m.Update(daemonStreamEventMsg{events: []protocol.EventEnvelope{
		{
			ProjectID:        naming.ProjectID(m.daemonProjectID()),
			Revision:         2,
			SkippedRevisions: []uint64{1},
			Event:            protocol.EventUICommandRequested,
			Body:             uiBody,
		},
		{
			ProjectID:        naming.ProjectID(m.daemonProjectID()),
			Revision:         4,
			SkippedRevisions: []uint64{1, 3},
			Event:            protocol.EventTaskCreated,
			Body:             taskBody,
		},
	}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if cmd == nil {
		t.Fatal("expected batched command")
	}
	if updated.daemonRevision != 4 {
		t.Fatalf("daemonRevision = %d, want 4", updated.daemonRevision)
	}
	if len(updated.tasks) != 2 || updated.tasks[1].ID.String() != "az-2" {
		t.Fatalf("tasks after mixed priority batch = %+v, want created task appended without rehydrate", updated.tasks)
	}
	if updated.daemonEvents == nil {
		t.Fatal("annotated retained event gap should not clear stream for rehydrate")
	}
}

func TestDaemonStreamEventBatchCoalescesSnapshotRefreshCommands(t *testing.T) {
	m := newTestModel()
	m.daemonRevision = 4
	m.daemonEvents = make(chan protocol.EventEnvelope)

	updatedAny, cmd := m.Update(daemonStreamEventMsg{events: []protocol.EventEnvelope{
		{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  5,
			Event:     "task.external.changed",
		},
		{
			ProjectID: naming.ProjectID(m.daemonProjectID()),
			Revision:  6,
			Event:     "task.external.changed",
		},
	}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if cmd == nil {
		t.Fatal("expected batched command")
	}
	if updated.daemonRevision != 6 {
		t.Fatalf("daemonRevision = %d, want 6", updated.daemonRevision)
	}
	if updated.issueRefreshSeq != 1 {
		t.Fatalf("issueRefreshSeq = %d, want one coalesced refresh", updated.issueRefreshSeq)
	}
	if updated.daemonStreamMetrics.EventsDrained != 2 {
		t.Fatalf("events drained = %d, want 2", updated.daemonStreamMetrics.EventsDrained)
	}
	if updated.daemonStreamMetrics.RefreshesCoalesced != 1 {
		t.Fatalf("refreshes coalesced = %d, want 1", updated.daemonStreamMetrics.RefreshesCoalesced)
	}
	if updated.daemonStreamMetrics.MaxBatchSize != 2 {
		t.Fatalf("max batch size = %d, want 2", updated.daemonStreamMetrics.MaxBatchSize)
	}
}

func TestDaemonStreamEventBatchCoalescesPureRuntimeProjectionByIssue(t *testing.T) {
	makeProjectionEvent := func(revision uint64, issueID string, dirty bool, additions, deletions int) protocol.EventEnvelope {
		body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
			ProjectID: naming.ProjectID(newTestModel().daemonProjectID()),
			IssueID:   naming.IssueID(issueID),
			UpdatedAt: time.Date(2026, time.May, 25, 12, 0, 0, 0, time.UTC),
			Runtime: &protocol.RuntimeProjectionEventBody{
				ProjectID: naming.ProjectID(newTestModel().daemonProjectID()),
				Revision:  revision,
				Projection: protocol.RuntimeProjection{
					ProjectID: naming.ProjectID(newTestModel().daemonProjectID()),
					IssueID:   naming.IssueID(issueID),
					Worktree: protocol.RuntimeWorktreeProjection{
						Exists:  true,
						Path:    "/tmp/repo-" + issueID,
						Healthy: true,
					},
					Git: protocol.RuntimeGitProjection{
						HasUncommittedChanges: dirty,
						GitAdditions:          additions,
						GitDeletions:          deletions,
					},
					Session: protocol.RuntimeSessionProjection{
						HasSession: true,
						State:      protocol.SessionLifecycleStateAttached,
						Worktree:   "/tmp/repo-" + issueID,
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal projection body: %v", err)
		}
		return protocol.EventEnvelope{
			ProjectID: naming.ProjectID(newTestModel().daemonProjectID()),
			Revision:  revision,
			Event:     protocol.EventGitStatusUpdated,
			Body:      body,
		}
	}

	m := newTestModel()
	m.daemonRevision = 4
	m.daemonEvents = make(chan protocol.EventEnvelope)
	m.tasks = []domain.Task{{ID: "az-1", Title: "Existing", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeTask}}

	updatedAny, cmd := m.Update(daemonStreamEventMsg{events: []protocol.EventEnvelope{
		makeProjectionEvent(5, "az-1", true, 9, 3),
		makeProjectionEvent(6, "az-1", false, 0, 0),
	}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if cmd == nil {
		t.Fatal("expected next stream wait command")
	}
	if updated.daemonRevision != 6 {
		t.Fatalf("daemonRevision = %d, want 6", updated.daemonRevision)
	}
	if updated.daemonStreamMetrics.RuntimeProjectionsCoalesced != 1 {
		t.Fatalf("runtime projections coalesced = %d, want 1", updated.daemonStreamMetrics.RuntimeProjectionsCoalesced)
	}
	if updated.tasks[0].HasUncommittedChanges || updated.tasks[0].GitAdditions != 0 || updated.tasks[0].GitDeletions != 0 {
		t.Fatalf("task projection = %+v, want latest clean projection only", updated.tasks[0])
	}
}

func TestDaemonStreamEventBatchRehydratesOnGapAfterCoalescedProjection(t *testing.T) {
	projectID := newTestModel().daemonProjectID()
	makeProjectionEvent := func(revision uint64, dirty bool) protocol.EventEnvelope {
		body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
			ProjectID: naming.ProjectID(projectID),
			IssueID:   "az-1",
			UpdatedAt: time.Date(2026, time.May, 25, 12, 0, 0, 0, time.UTC),
			Runtime: &protocol.RuntimeProjectionEventBody{
				ProjectID: naming.ProjectID(projectID),
				Revision:  revision,
				Projection: protocol.RuntimeProjection{
					ProjectID: naming.ProjectID(projectID),
					IssueID:   "az-1",
					Worktree: protocol.RuntimeWorktreeProjection{
						Exists:  true,
						Path:    "/tmp/repo-az-1",
						Healthy: true,
					},
					Git: protocol.RuntimeGitProjection{
						HasUncommittedChanges: dirty,
						GitAdditions:          3,
						GitDeletions:          1,
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal projection body: %v", err)
		}
		return protocol.EventEnvelope{
			ProjectID: naming.ProjectID(projectID),
			Revision:  revision,
			Event:     protocol.EventGitStatusUpdated,
			Body:      body,
		}
	}

	m := newTestModel()
	m.daemonRevision = 4
	m.daemonEvents = make(chan protocol.EventEnvelope)
	m.tasks[0].HasUncommittedChanges = false

	updatedAny, cmd := m.Update(daemonStreamEventMsg{events: []protocol.EventEnvelope{
		makeProjectionEvent(5, true),
		makeProjectionEvent(7, true),
	}})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if cmd == nil {
		t.Fatal("expected rehydrate command")
	}
	if updated.daemonEvents != nil {
		t.Fatal("expected daemon stream to be cleared for rehydrate")
	}
	if updated.daemonRevision != 5 {
		t.Fatalf("daemonRevision = %d, want 5", updated.daemonRevision)
	}
	if updated.tasks[0].HasUncommittedChanges {
		t.Fatalf("gap event should not apply latest projection before rehydrate: %+v", updated.tasks[0])
	}
	if updated.daemonStreamMetrics.Rehydrates != 1 {
		t.Fatalf("rehydrates = %d, want 1", updated.daemonStreamMetrics.Rehydrates)
	}
}

func TestScheduleIssuesRefreshDedupesInFlightAndReplaysPending(t *testing.T) {
	snapshotReads := uint64(0)
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != daemonclient.CommandBoardFetch {
				t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandBoardFetch)
			}
			snapshotReads++
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, snapshotReads, req.Meta.ProjectID.String(), []domain.Task{
					{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen},
				}),
			}, nil
		},
	}
	m := newDaemonTestModel(transport)
	m.hasRefreshLoop = true

	firstCmd := m.scheduleIssuesRefreshCmd()
	if firstCmd == nil {
		t.Fatal("expected first refresh command")
	}
	if !m.issueRefreshInFlight || m.issueRefreshSeq != 1 {
		t.Fatalf("refresh state after first schedule: inFlight=%v seq=%d", m.issueRefreshInFlight, m.issueRefreshSeq)
	}

	secondCmd := m.scheduleIssuesRefreshCmd()
	if secondCmd != nil {
		t.Fatal("expected duplicate in-flight refresh to be coalesced")
	}
	if !m.issueRefreshPending {
		t.Fatal("expected duplicate refresh to mark pending replay")
	}
	if m.issueRefreshSeq != 1 {
		t.Fatalf("issueRefreshSeq after coalesced schedule = %d, want 1", m.issueRefreshSeq)
	}
	if m.daemonStreamMetrics.RefreshesCoalesced != 1 {
		t.Fatalf("refreshes coalesced = %d, want 1", m.daemonStreamMetrics.RefreshesCoalesced)
	}

	firstMsg := firstCmd()
	loaded, ok := firstMsg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("first command message type = %T, want issuesLoadedMsg", firstMsg)
	}
	updatedAny, replayCmd := m.Update(loaded)
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedAny)
	}
	if replayCmd == nil {
		t.Fatal("expected pending refresh replay command after first load completes")
	}
	if !updated.issueRefreshInFlight || updated.issueRefreshPending {
		t.Fatalf("refresh state after replay schedule: inFlight=%v pending=%v", updated.issueRefreshInFlight, updated.issueRefreshPending)
	}
	if updated.issueRefreshSeq != 2 {
		t.Fatalf("issueRefreshSeq after replay schedule = %d, want 2", updated.issueRefreshSeq)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests before replay command executes = %v, want one snapshot read", transport.requests)
	}

	replayMsg := replayCmd()
	if _, ok := replayMsg.(issuesLoadedMsg); !ok {
		t.Fatalf("replay command message type = %T, want issuesLoadedMsg", replayMsg)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests after replay command executes = %v, want two snapshot reads", transport.requests)
	}
}

func TestProjectionEventRevisionGate(t *testing.T) {
	makeProjectionEvent := func(revision uint64, dirty bool) daemonStreamEventMsg {
		body, err := json.Marshal(protocol.ProjectionUpdateEventBody{
			ProjectID: "azedarach-bte",
			IssueID:   "az-1",
			UpdatedAt: time.Date(2026, time.April, 9, 12, 0, 0, 0, time.UTC),
			Runtime: &protocol.RuntimeProjectionEventBody{
				ProjectID: "azedarach-bte",
				Revision:  revision,
				Projection: protocol.RuntimeProjection{
					ProjectID: "azedarach-bte",
					IssueID:   "az-1",
					Worktree: protocol.RuntimeWorktreeProjection{
						Exists:  true,
						Path:    "/tmp/repo-az-1",
						Healthy: true,
					},
					Git: protocol.RuntimeGitProjection{
						HasUncommittedChanges: dirty,
						GitAdditions:          3,
						GitDeletions:          1,
					},
					Session: protocol.RuntimeSessionProjection{
						HasSession: true,
						State:      protocol.SessionLifecycleStateAttached,
						Worktree:   "/tmp/repo-az-1",
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal projection body: %v", err)
		}
		return daemonStreamEventMsg{
			event: protocol.EventEnvelope{
				Event:    protocol.EventGitStatusUpdated,
				Revision: revision,
				Body:     body,
			},
		}
	}

	t.Run("stale projection event ignored", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.daemonEvents = make(chan protocol.EventEnvelope)
		m.tasks[0].HasUncommittedChanges = false
		m.tasks[0].GitAdditions = 0
		m.tasks[0].GitDeletions = 0

		updated, cmd := m.Update(makeProjectionEvent(7, true))
		if cmd == nil {
			t.Fatal("expected wait command")
		}
		next := updated.(Model)
		if next.daemonRevision != 8 {
			t.Fatalf("daemon revision = %d, want 8", next.daemonRevision)
		}
		if next.tasks[0].HasUncommittedChanges {
			t.Fatalf("stale event should not update dirty flag: %+v", next.tasks[0])
		}
	})

	t.Run("gap projection event triggers rehydrate", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.daemonEvents = make(chan protocol.EventEnvelope)
		m.tasks[0].HasUncommittedChanges = false

		updated, cmd := m.Update(makeProjectionEvent(10, true))
		if cmd == nil {
			t.Fatal("expected reattach command")
		}
		next := updated.(Model)
		if next.daemonEvents != nil {
			t.Fatal("expected daemon stream to be cleared for rehydrate")
		}
		if next.daemonRevision != 8 {
			t.Fatalf("daemon revision = %d, want 8", next.daemonRevision)
		}
		if next.tasks[0].HasUncommittedChanges {
			t.Fatalf("gap event should not apply projection before rehydrate: %+v", next.tasks[0])
		}
	})

	t.Run("sequential projection event applies", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.daemonEvents = make(chan protocol.EventEnvelope)
		m.tasks[0].HasUncommittedChanges = false

		updated, cmd := m.Update(makeProjectionEvent(9, true))
		if cmd == nil {
			t.Fatal("expected wait command")
		}
		next := updated.(Model)
		if next.daemonRevision != 9 {
			t.Fatalf("daemon revision = %d, want 9", next.daemonRevision)
		}
		if !next.tasks[0].HasUncommittedChanges {
			t.Fatalf("sequential projection should update dirty flag: %+v", next.tasks[0])
		}
	})
}

func TestSessionProjectionEventRevisionGate(t *testing.T) {
	testProjectID := newTestModel().daemonProjectID()
	makeSessionEvent := func(revision uint64, dirty bool) daemonStreamEventMsg {
		body, err := json.Marshal(protocol.SessionProjectionEventBody{
			ProjectID: naming.ProjectID(testProjectID),
			Revision:  revision,
			Session: protocol.SessionProjection{
				SessionID: naming.SessionID("azedarach-bte-az-1"),
				IssueID:   "az-1",
				State:     protocol.SessionLifecycleStateAttached,
				UpdatedAt: time.Date(2026, time.April, 10, 8, 0, 0, 0, time.UTC),
			},
			Runtime: &protocol.RuntimeProjectionEventBody{
				ProjectID: "default",
				Revision:  revision,
				Projection: protocol.RuntimeProjection{
					ProjectID: "default",
					IssueID:   "az-1",
					Worktree: protocol.RuntimeWorktreeProjection{
						Exists:  true,
						Path:    "/tmp/repo-az-1",
						Healthy: true,
					},
					Git: protocol.RuntimeGitProjection{
						HasUncommittedChanges: dirty,
						GitAdditions:          4,
						GitDeletions:          2,
					},
					Session: protocol.RuntimeSessionProjection{
						HasSession: true,
						SessionID:  naming.SessionID("azedarach-bte-az-1"),
						State:      protocol.SessionLifecycleStateAttached,
						Worktree:   "/tmp/repo-az-1",
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal session projection body: %v", err)
		}
		return daemonStreamEventMsg{
			event: protocol.EventEnvelope{
				Event:    protocol.EventSessionUpdated,
				Revision: revision,
				Body:     body,
			},
		}
	}

	t.Run("stale session projection event ignored", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.daemonEvents = make(chan protocol.EventEnvelope)
		m.tasks[0].HasUncommittedChanges = false
		m.tasks[0].GitAdditions = 0
		m.tasks[0].GitDeletions = 0

		updated, cmd := m.Update(makeSessionEvent(7, true))
		if cmd == nil {
			t.Fatal("expected wait command")
		}
		next := updated.(Model)
		if next.daemonRevision != 8 {
			t.Fatalf("daemon revision = %d, want 8", next.daemonRevision)
		}
		if next.tasks[0].HasUncommittedChanges {
			t.Fatalf("stale session event should not update dirty flag: %+v", next.tasks[0])
		}
	})

	t.Run("gap session projection event rehydrates before apply", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.daemonEvents = make(chan protocol.EventEnvelope)
		m.tasks[0].HasUncommittedChanges = false

		updated, cmd := m.Update(makeSessionEvent(10, true))
		if cmd == nil {
			t.Fatal("expected reattach command")
		}
		next := updated.(Model)
		if next.daemonEvents != nil {
			t.Fatal("expected daemon stream to be cleared for rehydrate")
		}
		if next.daemonRevision != 8 {
			t.Fatalf("daemon revision = %d, want 8", next.daemonRevision)
		}
		if next.tasks[0].HasUncommittedChanges {
			t.Fatalf("gap session event should not apply projection before rehydrate: %+v", next.tasks[0])
		}
	})

	t.Run("sequential session projection event applies and advances", func(t *testing.T) {
		m := newTestModel()
		m.daemonRevision = 8
		m.daemonEvents = make(chan protocol.EventEnvelope)
		m.tasks[0].HasUncommittedChanges = false
		m.tasks[0].GitAdditions = 0
		m.tasks[0].GitDeletions = 0

		updated, cmd := m.Update(makeSessionEvent(9, true))
		if cmd == nil {
			t.Fatal("expected wait command")
		}
		next := updated.(Model)
		if next.daemonRevision != 9 {
			t.Fatalf("daemon revision = %d, want 9", next.daemonRevision)
		}
		if !next.tasks[0].HasUncommittedChanges || next.tasks[0].GitAdditions != 4 || next.tasks[0].GitDeletions != 2 {
			t.Fatalf("sequential session event should update runtime projection: %+v", next.tasks[0])
		}
	})
}

func TestIssuesLoadedIgnoresSnapshotOlderThanAppliedSessionEvent(t *testing.T) {
	projectID := newTestModel().daemonProjectID()
	startedAt := time.Date(2026, time.May, 6, 6, 0, 0, 0, time.UTC)
	staleSession := &domain.Session{
		IssueID:   "az-1",
		State:     domain.SessionBusy,
		StartedAt: &startedAt,
		Worktree:  "/tmp/repo-az-1",
	}
	m := newTestModel()
	m.daemonRevision = 8
	m.daemonEvents = make(chan protocol.EventEnvelope)
	m.tasks[0].Session = cloneSession(staleSession)
	m.tasks[0].HasTmuxSession = true
	m.tasks[0].HasWorktree = true
	m.overlayStack.Push(overlay.NewTaskWorkspaceOverlay(m.tasks[0], m.tasks, nil, 120, 30))
	m.syncProjectionIndexesFromTasks()
	m.syncTaskWorkspaceOverlay()

	body, err := json.Marshal(protocol.SessionProjectionEventBody{
		ProjectID: naming.ProjectID(projectID),
		Revision:  9,
		Session: protocol.SessionProjection{
			SessionID: naming.SessionID("azedarach-bte-az-1"),
			IssueID:   "az-1",
			State:     protocol.SessionLifecycleStateStopped,
			UpdatedAt: startedAt.Add(time.Minute),
		},
		Runtime: &protocol.RuntimeProjectionEventBody{
			ProjectID: naming.ProjectID(projectID),
			Revision:  9,
			Projection: protocol.RuntimeProjection{
				ProjectID: naming.ProjectID(projectID),
				IssueID:   "az-1",
				Worktree: protocol.RuntimeWorktreeProjection{
					Exists: true,
					Path:   "/tmp/repo-az-1",
				},
				Session: protocol.RuntimeSessionProjection{
					HasSession: false,
					State:      protocol.SessionLifecycleStateStopped,
					UpdatedAt:  &startedAt,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal stopped session event: %v", err)
	}

	updated, cmd := m.Update(daemonStreamEventMsg{
		event: protocol.EventEnvelope{
			Event:    protocol.EventSessionUpdated,
			Revision: 9,
			Body:     body,
		},
	})
	if cmd == nil {
		t.Fatal("expected wait command after session event")
	}
	afterStop := updated.(Model)
	workspace, ok := afterStop.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace overlay after stop event, got %T", afterStop.overlayStack.Current())
	}
	if view := workspace.View(); strings.Contains(view, "Attach to session") || strings.Contains(view, "Stop session") {
		t.Fatalf("stopped session event should remove active session actions: %q", view)
	}

	staleSnapshot := []domain.Task{{
		ID:             "az-1",
		Title:          "Task 1",
		Status:         domain.StatusInProgress,
		Type:           domain.TypeTask,
		Session:        cloneSession(staleSession),
		HasTmuxSession: true,
		HasWorktree:    true,
	}}
	replayedAny, _ := afterStop.Update(issuesLoadedMsg{
		projectID: projectID,
		revision:  8,
		tasks:     staleSnapshot,
	})
	replayed := replayedAny.(Model)
	workspace, ok = replayed.overlayStack.Current().(*overlay.TaskWorkspaceOverlay)
	if !ok {
		t.Fatalf("expected task workspace overlay after stale snapshot, got %T", replayed.overlayStack.Current())
	}
	if view := workspace.View(); strings.Contains(view, "Attach to session") || strings.Contains(view, "Stop session") {
		t.Fatalf("stale snapshot should not restore stopped session actions: %q", view)
	}
	if replayed.daemonRevision != 9 {
		t.Fatalf("daemon revision = %d, want 9", replayed.daemonRevision)
	}
}

func TestLoadIssuesAfterRuntimeReconcileCmd_ReconcilesBeforeSnapshot(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandRuntimeReconcile:
				body, err := json.Marshal(daemonclient.RuntimeReconcileResult{
					ProjectID: "azedarach-bte",
				})
				if err != nil {
					t.Fatalf("marshal runtime reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			case daemonclient.CommandBoardFetch:
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 12, "azedarach-bte", []domain.Task{
						{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen},
					}),
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
				return protocol.ResponseEnvelope{}, nil
			}
		},
	}

	m := newDaemonTestModel(transport)
	m.issueRefreshSeq = 42
	cmd := m.loadIssuesAfterRuntimeReconcileCmd()
	if cmd == nil {
		t.Fatal("expected command")
	}
	msg := cmd()
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
	}
	if loaded.refreshSeq != 42 {
		t.Fatalf("refresh seq = %d, want 42", loaded.refreshSeq)
	}
	if loaded.revision != 12 {
		t.Fatalf("revision = %d, want 12", loaded.revision)
	}
	if len(transport.requests) < 2 {
		t.Fatalf("requests = %v, want at least two commands", transport.requests)
	}
	if transport.requests[0] != daemonclient.CommandRuntimeReconcile || transport.requests[1] != daemonclient.CommandBoardFetch {
		t.Fatalf("command order = %v, want runtime reconcile before task list", transport.requests[:2])
	}
}

func TestLoadIssuesAfterIssueReconcileCmd_ReconcilesSelectedIssuesBeforeSnapshot(t *testing.T) {
	transport := &recordingDaemonTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandRuntimeReconcileIssue:
				body, err := json.Marshal(daemonclient.RuntimeReconcileResult{
					ProjectID: "azedarach-bte",
				})
				if err != nil {
					t.Fatalf("marshal runtime issue reconcile response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			case daemonclient.CommandBoardFetch:
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 18, "azedarach-bte", []domain.Task{
						{ID: "az-parent", Title: "Parent", Status: domain.StatusOpen},
						{ID: "az-child", Title: "Child", Status: domain.StatusOpen},
					}),
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
				return protocol.ResponseEnvelope{}, nil
			}
		},
	}

	m := newDaemonTestModel(transport)
	m.issueRefreshSeq = 7
	cmd := m.loadIssuesAfterIssueReconcileCmd([]string{"az-parent", "az-child", "az-child"})
	if cmd == nil {
		t.Fatal("expected command")
	}
	msg := cmd()
	loaded, ok := msg.(issuesLoadedMsg)
	if !ok {
		t.Fatalf("message type = %T, want issuesLoadedMsg", msg)
	}
	if loaded.refreshSeq != 7 {
		t.Fatalf("refresh seq = %d, want 7", loaded.refreshSeq)
	}
	if loaded.revision != 18 {
		t.Fatalf("revision = %d, want 18", loaded.revision)
	}
	if len(transport.requests) < 2 {
		t.Fatalf("requests = %v, want at least two commands", transport.requests)
	}
	if transport.requests[0] != daemonclient.CommandRuntimeReconcileIssue || transport.requests[1] != daemonclient.CommandBoardFetch {
		t.Fatalf("command order = %v, want issue reconcile before task list", transport.requests[:2])
	}
}

func TestBulkTaskCommandsUseDaemonClient(t *testing.T) {
	t.Run("bulk status delete archive", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return emptyWorktreeListResponse(t, req), nil
				case daemonclient.CommandTaskClose:
					var body struct {
						TaskID               string `json:"task_id"`
						IntegrateBeforeClose bool   `json:"integrate_before_close"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal close request: %v", err)
					}
					if !body.IntegrateBeforeClose {
						t.Fatalf("close body = %+v, want integrate_before_close", body)
					}
					respBody, err := json.Marshal(daemonclient.TaskCloseResult{
						TaskID: body.TaskID,
						Status: string(domain.StatusDone),
					})
					if err != nil {
						t.Fatalf("marshal close response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandBoardFetch:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, protocol.DefaultProjectID, []domain.Task{
							{ID: "az-1", Status: domain.StatusOpen},
							{ID: "az-2", Status: domain.StatusOpen},
						}),
					}, nil
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
		if transport.requests[0] != daemonclient.CommandTaskClose ||
			transport.requests[1] != daemonclient.CommandTaskClose ||
			transport.requests[2] != daemonclient.CommandTaskDelete ||
			transport.requests[3] != daemonclient.CommandTaskDelete ||
			transport.requests[4] != daemonclient.CommandTaskArchive {
			t.Fatalf("requests = %v", transport.requests)
		}
		if got := len(transport.commandBudgets); got != 5 {
			t.Fatalf("command budget count = %d, want 5", got)
		}
		for i := 0; i < 2; i++ {
			if budget := transport.commandBudgets[i]; budget < taskCloseMutationTimeout-10*time.Second {
				t.Fatalf("close command budget[%d] = %s, want near %s", i, budget, taskCloseMutationTimeout)
			}
		}
	})

	t.Run("bulk status done tracks pending close", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskClose {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskClose)
				}
				var body struct {
					TaskID               string `json:"task_id"`
					IntegrateBeforeClose bool   `json:"integrate_before_close"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal close request: %v", err)
				}
				if body.TaskID != "az-1" || !body.IntegrateBeforeClose {
					t.Fatalf("close body = %+v, want az-1 integrate", body)
				}
				respBody, err := json.Marshal(map[string]any{
					"operation_id": "op-close",
					"state":        string(protocol.OperationStateQueued),
				})
				if err != nil {
					t.Fatalf("marshal pending close response: %v", err)
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
		m.tasks = []domain.Task{{ID: "az-1", Status: domain.StatusInReview}}

		msg := m.bulkSetStatusCmd([]string{"az-1"}, domain.StatusDone)()
		result, ok := msg.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want bulkStatusResultMsg", msg)
		}
		if result.updated != 0 || result.failed != 0 || len(result.pending) != 1 {
			t.Fatalf("bulk status result = %+v, want one pending close", result)
		}

		updatedAny, cmd := m.Update(result)
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if cmd == nil {
			t.Fatal("expected refresh command after pending close")
		}
		if got := updated.operationTaskID["op-close"]; got != "az-1" {
			t.Fatalf("operation task id = %q, want az-1", got)
		}
		progress := updated.pendingMutationForTask("az-1")
		if progress == nil {
			t.Fatal("expected pending mutation for az-1")
		}
		if progress.OperationID != "op-close" || progress.State != string(protocol.OperationStateQueued) {
			t.Fatalf("pending progress = %+v, want queued op-close", progress)
		}
		if progress.PreviousStatus != domain.StatusInReview || progress.TargetStatus != domain.StatusDone {
			t.Fatalf("pending statuses = %s -> %s, want in_review -> done", progress.PreviousStatus, progress.TargetStatus)
		}
		if got := updated.tasks[0].Status; got != domain.StatusDone {
			t.Fatalf("optimistic status = %s, want done", got)
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
					return protocol.ResponseEnvelope{}, errors.New("permission denied")
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
		if result.issues[0].taskID != "az-2" || !strings.Contains(result.issues[0].reason, "permission denied") {
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
		if !strings.Contains(gotToast, "az-2:") || !strings.Contains(gotToast, "permission denied") {
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
			{ID: "az-3", Status: domain.StatusInReview},
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
		if statusBodies[1].TaskID != "az-2" || statusBodies[1].Status != domain.StatusInReview {
			t.Fatalf("second update = %+v, want az-2 -> blocked", statusBodies[1])
		}
		if got := transport.requests; len(got) != 2 ||
			got[0] != daemonclient.CommandTaskUpdateStatus ||
			got[1] != daemonclient.CommandTaskUpdateStatus {
			t.Fatalf("requests = %v", got)
		}
	})

	t.Run("bulk move right tracks pending close", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskClose {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskClose)
				}
				respBody, err := json.Marshal(map[string]any{
					"operation_id": "op-move-close",
					"state":        string(protocol.OperationStateQueued),
				})
				if err != nil {
					t.Fatalf("marshal pending close response: %v", err)
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
		m.tasks = []domain.Task{{ID: "az-1", Status: domain.StatusInReview}}

		msg := m.bulkMoveStatusCmd([]string{"az-1"}, 1)()
		result, ok := msg.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want bulkStatusResultMsg", msg)
		}
		if result.updated != 0 || result.failed != 0 || len(result.pending) != 1 {
			t.Fatalf("bulk move result = %+v, want one pending close", result)
		}

		updatedAny, cmd := m.Update(result)
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if cmd == nil {
			t.Fatal("expected refresh command after pending move close")
		}
		if got := updated.operationTaskID["op-move-close"]; got != "az-1" {
			t.Fatalf("operation task id = %q, want az-1", got)
		}
		progress := updated.pendingMutationForTask("az-1")
		if progress == nil || progress.OperationID != "op-move-close" || progress.State != string(protocol.OperationStateQueued) {
			t.Fatalf("pending progress = %+v, want queued op-move-close", progress)
		}
		if progress.PreviousStatus != domain.StatusInReview || progress.TargetStatus != domain.StatusDone {
			t.Fatalf("pending statuses = %s -> %s, want in_review -> done", progress.PreviousStatus, progress.TargetStatus)
		}
		if got := updated.tasks[0].Status; got != domain.StatusDone {
			t.Fatalf("optimistic status = %s, want done", got)
		}
	})

	t.Run("bulk done requires integrate and close confirmation", func(t *testing.T) {
		closeBodies := make([]struct {
			TaskID               string `json:"task_id"`
			IntegrateBeforeClose bool   `json:"integrate_before_close"`
		}, 0, 2)
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandWorktreeList {
					return emptyWorktreeListResponse(t, req), nil
				}
				if req.Command == daemonclient.CommandBoardFetch {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
							{ID: "az-1", Status: domain.StatusOpen},
							{ID: "az-2", Status: domain.StatusInReview},
						}),
					}, nil
				}
				if req.Command != daemonclient.CommandTaskClose {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				var body struct {
					TaskID               string `json:"task_id"`
					IntegrateBeforeClose bool   `json:"integrate_before_close"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal close request: %v", err)
				}
				closeBodies = append(closeBodies, body)
				respBody, err := json.Marshal(daemonclient.TaskCloseResult{
					TaskID: body.TaskID,
					Status: string(domain.StatusDone),
				})
				if err != nil {
					t.Fatalf("marshal close response: %v", err)
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
		m.tasks = []domain.Task{
			{ID: "az-1", Status: domain.StatusOpen},
			{ID: "az-2", Status: domain.StatusInReview},
		}

		updatedAny, promptCmd := m.handleBulkAction(overlay.BulkActionMsg{
			Action:      "4",
			SelectedIDs: []string{"az-1", "az-2"},
		})
		if promptCmd == nil {
			t.Fatal("expected bulk close confirmation command")
		}
		prompted := updatedAny.(Model)
		if prompted.pendingClose == nil {
			t.Fatal("expected pending bulk close confirmation")
		}
		if len(transport.requests) != 0 {
			t.Fatalf("daemon requests before confirmation = %v, want none", transport.requests)
		}

		confirmedAny, bulkCmd := prompted.handleSelection(overlay.SelectionMsg{Key: "yes"})
		if bulkCmd == nil {
			t.Fatal("expected bulk status command after confirmation")
		}
		confirmed := confirmedAny.(Model)
		if confirmed.pendingClose != nil {
			t.Fatal("pending bulk close confirmation was not cleared")
		}
		if len(confirmed.toasts) == 0 || !strings.Contains(confirmed.toasts[len(confirmed.toasts)-1].Message, "Bulk close queued for 2 task(s)") {
			t.Fatalf("toasts after confirmation = %+v, want bulk close queued toast", confirmed.toasts)
		}

		msg := bulkCmd()
		result, ok := msg.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("bulk result = %T, want bulkStatusResultMsg", msg)
		}
		if result.updated != 2 || result.failed != 0 {
			t.Fatalf("bulk result = %+v, want 2 updated", result)
		}
		if len(closeBodies) != 2 {
			t.Fatalf("close bodies = %+v, want 2 closes", closeBodies)
		}
		for _, body := range closeBodies {
			if body.TaskID == "" {
				t.Fatalf("close body = %+v, want task id", body)
			}
			if !body.IntegrateBeforeClose {
				t.Fatalf("close body = %+v, want integrate_before_close", body)
			}
		}
		if got := transport.requests; len(got) != 2 ||
			got[0] != daemonclient.CommandTaskClose ||
			got[1] != daemonclient.CommandTaskClose {
			t.Fatalf("requests = %v", got)
		}
	})

	t.Run("bulk cleanup and delete+cleanup use daemon lifecycle commands", func(t *testing.T) {
		removeBodies := make([]struct {
			IssueID string `json:"issue_id"`
			Force   bool   `json:"force,omitempty"`
		}, 0, 2)
		deleteBodies := make([]struct {
			TaskID  string `json:"task_id"`
			Cleanup bool   `json:"cleanup"`
		}, 0, 1)

		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandSessionStop:
					// No-op: stop may legitimately be called even when already stopped.
				case daemonclient.CommandWorktreeRemove:
					var body struct {
						IssueID string `json:"issue_id"`
						Force   bool   `json:"force,omitempty"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal worktree remove request: %v", err)
					}
					removeBodies = append(removeBodies, body)
				case daemonclient.CommandTaskDelete:
					var body struct {
						TaskID  string `json:"task_id"`
						Cleanup bool   `json:"cleanup"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task delete request: %v", err)
					}
					deleteBodies = append(deleteBodies, body)
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

		cleanupOnly := m.bulkCleanupWorktreeCmd([]string{"az-1"}, false)()
		cleanupOnlyResult, ok := cleanupOnly.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("cleanup-only message type = %T, want bulkStatusResultMsg", cleanupOnly)
		}
		if cleanupOnlyResult.updated != 1 || cleanupOnlyResult.failed != 0 {
			t.Fatalf("cleanup-only result = %+v", cleanupOnlyResult)
		}

		deleteAndCleanup := m.bulkCleanupWorktreeCmd([]string{"az-2"}, true)()
		deleteAndCleanupResult, ok := deleteAndCleanup.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("delete+cleanup message type = %T, want bulkStatusResultMsg", deleteAndCleanup)
		}
		if deleteAndCleanupResult.updated != 1 || deleteAndCleanupResult.failed != 0 {
			t.Fatalf("delete+cleanup result = %+v", deleteAndCleanupResult)
		}

		if len(removeBodies) != 1 || removeBodies[0].IssueID != "az-1" {
			t.Fatalf("worktree remove bodies = %+v, want cleanup-only az-1", removeBodies)
		}
		if removeBodies[0].Force {
			t.Fatalf("worktree remove force flag = %+v, want false", removeBodies)
		}
		if len(deleteBodies) != 1 || deleteBodies[0].TaskID != "az-2" || !deleteBodies[0].Cleanup {
			t.Fatalf("delete bodies = %+v, want one cleanup delete for az-2", deleteBodies)
		}

		if got := transport.requests; len(got) != 3 ||
			got[0] != daemonclient.CommandSessionStop ||
			got[1] != daemonclient.CommandWorktreeRemove ||
			got[2] != daemonclient.CommandTaskDelete {
			t.Fatalf("requests = %v", got)
		}
	})

	t.Run("bulk cleanup preflight prompts when selected tasks are dirty or ahead", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandRuntimeReconcileIssue:
					respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{})
					if err != nil {
						t.Fatalf("marshal reconcile response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandBoardFetch:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
							{ID: "az-1", Status: domain.StatusOpen, HasUncommittedChanges: true, GitAdditions: 3, GitDeletions: 1},
							{ID: "az-2", Status: domain.StatusOpen, GitAheadCount: 2},
						}),
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}
		m := newDaemonTestModel(transport)
		m.tasks = []domain.Task{
			{ID: "az-1", Status: domain.StatusOpen},
			{ID: "az-2", Status: domain.StatusOpen},
			{ID: "az-3", Status: domain.StatusInReview},
		}

		updatedAny, cmd := m.handleBulkAction(overlay.BulkActionMsg{
			Action:      "w",
			SelectedIDs: []string{"az-1", "az-2"},
		})
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if cmd == nil {
			t.Fatal("expected bulk cleanup preflight command")
		}

		preflightMsg := cmd()
		preflight, ok := preflightMsg.(bulkCleanupPreflightMsg)
		if !ok {
			t.Fatalf("message type = %T, want bulkCleanupPreflightMsg", preflightMsg)
		}
		if len(preflight.risks) != 2 {
			t.Fatalf("preflight risks = %+v, want 2 flagged tasks", preflight.risks)
		}

		nextAny, _ := updated.Update(preflight)
		next, ok := nextAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", nextAny)
		}
		if next.pendingBulkCleanup == nil {
			t.Fatal("expected pending bulk cleanup confirmation")
		}
		if len(next.tasks) != 3 {
			t.Fatalf("tasks after preflight = %+v, want selected refreshes plus unrelated task", next.tasks)
		}
		if !next.tasks[0].HasUncommittedChanges || next.tasks[0].GitAdditions != 3 || next.tasks[0].GitDeletions != 1 {
			t.Fatalf("selected dirty task after preflight = %+v, want refreshed git status", next.tasks[0])
		}
		if next.tasks[1].GitAheadCount != 2 {
			t.Fatalf("selected ahead task after preflight = %+v, want refreshed ahead count", next.tasks[1])
		}
		if next.tasks[2].ID.String() != "az-3" || next.tasks[2].Status != domain.StatusInReview {
			t.Fatalf("unrelated task after preflight = %+v, want preserved blocked az-3", next.tasks[2])
		}
		if got := transport.requests; len(got) != 2 || got[0] != daemonclient.CommandRuntimeReconcileIssue || got[1] != daemonclient.CommandBoardFetch {
			t.Fatalf("requests = %v", got)
		}
	})

	t.Run("bulk cleanup preflight proceeds immediately when no selected task is dirty/ahead", func(t *testing.T) {
		transport := &recordingDaemonTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandRuntimeReconcileIssue:
					respBody, err := json.Marshal(daemonclient.RuntimeReconcileResult{})
					if err != nil {
						t.Fatalf("marshal reconcile response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case daemonclient.CommandBoardFetch:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body: mustMarshalBoardSnapshot(t, req.ProtocolVersion, 1, req.Meta.ProjectID.String(), []domain.Task{
							{ID: "az-1", Status: domain.StatusOpen, HasUncommittedChanges: false, GitAheadCount: 0},
						}),
					}, nil
				case daemonclient.CommandSessionStop, daemonclient.CommandWorktreeRemove:
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
		m.tasks = []domain.Task{
			{ID: "az-1", Status: domain.StatusOpen},
		}

		_, cmd := m.handleBulkAction(overlay.BulkActionMsg{
			Action:      "w",
			SelectedIDs: []string{"az-1"},
		})
		if cmd == nil {
			t.Fatal("expected bulk cleanup preflight command")
		}

		preflightMsg := cmd()
		preflight, ok := preflightMsg.(bulkCleanupPreflightMsg)
		if !ok {
			t.Fatalf("message type = %T, want bulkCleanupPreflightMsg", preflightMsg)
		}

		updatedAny, runCleanupCmd := m.Update(preflight)
		updated, ok := updatedAny.(Model)
		if !ok {
			t.Fatalf("updated model type = %T, want Model", updatedAny)
		}
		if updated.pendingBulkCleanup != nil {
			t.Fatal("did not expect pending bulk cleanup confirmation for clean preflight")
		}
		if len(updated.tasks) != 1 || updated.tasks[0].HasUncommittedChanges || updated.tasks[0].GitAheadCount != 0 {
			t.Fatalf("tasks after clean preflight = %+v, want refreshed clean selected task", updated.tasks)
		}
		if runCleanupCmd == nil {
			t.Fatal("expected cleanup command to run immediately for clean preflight")
		}
		resultMsg := runCleanupCmd()
		result, ok := resultMsg.(bulkStatusResultMsg)
		if !ok {
			t.Fatalf("message type = %T, want bulkStatusResultMsg", resultMsg)
		}
		if result.updated != 1 || result.failed != 0 {
			t.Fatalf("bulk cleanup result = %+v", result)
		}
		if got := transport.requests; len(got) != 4 ||
			got[0] != daemonclient.CommandRuntimeReconcileIssue ||
			got[1] != daemonclient.CommandBoardFetch ||
			got[2] != daemonclient.CommandSessionStop ||
			got[3] != daemonclient.CommandWorktreeRemove {
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
