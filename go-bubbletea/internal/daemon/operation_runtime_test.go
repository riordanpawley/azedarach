package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type runtimeGitService struct{}

type runtimeWorktreeService struct{}

func (runtimeGitService) Fetch(context.Context, string, string) error { return nil }
func (runtimeGitService) Merge(context.Context, string, string) (*git.MergeResult, error) {
	return &git.MergeResult{Success: true}, nil
}
func (runtimeGitService) Checkout(context.Context, string, string) error           { return nil }
func (runtimeGitService) AbortMerge(context.Context, string) error                 { return nil }
func (runtimeGitService) DiffStat(context.Context, string, string) (string, error) { return "", nil }
func (runtimeGitService) Status(context.Context, string) (*git.GitStatus, error) {
	return &git.GitStatus{}, nil
}

func (runtimeWorktreeService) List(context.Context, string) ([]git.Worktree, error) { return nil, nil }
func (runtimeWorktreeService) Create(context.Context, string, string, string) (*git.Worktree, error) {
	return &git.Worktree{Path: "/tmp/worktree", Branch: "az/test", IssueID: "AZ-1"}, nil
}
func (runtimeWorktreeService) Delete(context.Context, string, string) error { return nil }
func (runtimeWorktreeService) CleanupOrphaned(context.Context, string) (*daemonhandlers.CleanupOrphanedResult, error) {
	return &daemonhandlers.CleanupOrphanedResult{}, nil
}

func TestOperationRuntimeSubmitGetListPublishesLifecycleEvents(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	runtime.sessionStart = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "session started"}), nil
	}

	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-1"})
	submitReq := testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      "session.start",
		IssueID:   "AZ-1",
		Payload:   payload,
	})
	ch, cancel := runtime.hub.Subscribe("proj-1", 0)
	defer cancel()

	resp := runtime.Handle(context.Background(), submitReq)
	if !resp.OK {
		t.Fatalf("submit response = %+v", resp)
	}
	var submitBody protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}
	if !submitBody.Created {
		t.Fatal("expected created operation")
	}
	if submitBody.Operation.OperationID == "" {
		t.Fatal("expected operation id")
	}

	record := waitForRuntimeState(t, runtime, submitBody.Operation.OperationID, daemonops.StateDone)
	if got := string(record.ResultPayload); got == "" {
		t.Fatal("expected persisted result payload")
	}

	getResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationGet, protocol.OperationGetRequestBody{OperationID: submitBody.Operation.OperationID}))
	if !getResp.OK {
		t.Fatalf("get response = %+v", getResp)
	}
	var getBody protocol.OperationGetResponseBody
	if err := json.Unmarshal(getResp.Body, &getBody); err != nil {
		t.Fatalf("unmarshal get body: %v", err)
	}
	if getBody.Operation.State != protocol.OperationStateDone {
		t.Fatalf("get state = %s, want done", getBody.Operation.State)
	}

	listResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationList, protocol.OperationListRequestBody{ProjectID: "proj-1"}))
	if !listResp.OK {
		t.Fatalf("list response = %+v", listResp)
	}
	var listBody protocol.OperationListResponseBody
	if err := json.Unmarshal(listResp.Body, &listBody); err != nil {
		t.Fatalf("unmarshal list body: %v", err)
	}
	if len(listBody.Operations) != 1 {
		t.Fatalf("operations len = %d, want 1", len(listBody.Operations))
	}

	events := collectOperationEvents(t, ch, 3)
	want := []string{protocol.EventOperationQueued, protocol.EventOperationRunning, protocol.EventOperationDone}
	for i, event := range events {
		if event.Event != want[i] {
			t.Fatalf("event[%d] = %s, want %s", i, event.Event, want[i])
		}
		var body protocol.OperationEventBody
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("unmarshal event body: %v", err)
		}
		if body.Operation.OperationID != submitBody.Operation.OperationID {
			t.Fatalf("event operation id = %s, want %s", body.Operation.OperationID, submitBody.Operation.OperationID)
		}
	}
}

func TestOperationRuntimeCancelMarksRunningOperationCancelled(t *testing.T) {
	blocked := make(chan struct{})
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), hub: publish.NewHub(32, 16, nil), nextRevision: sequentialRevision()})
	runtime.sessionStop = func(ctx context.Context, _ protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		close(blocked)
		<-ctx.Done()
		return protocol.ResponseEnvelope{}, ctx.Err()
	}

	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-2"})
	submitResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationSubmit, protocol.OperationSubmitRequestBody{
		ProjectID: "proj-1",
		Kind:      "session.stop",
		IssueID:   "AZ-2",
		Payload:   payload,
	}))
	if !submitResp.OK {
		t.Fatalf("submit response = %+v", submitResp)
	}
	var submitBody protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(submitResp.Body, &submitBody); err != nil {
		t.Fatalf("unmarshal submit body: %v", err)
	}

	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("operation did not start running")
	}

	cancelResp := runtime.Handle(context.Background(), testRequest(protocol.CommandOperationCancel, protocol.OperationCancelRequestBody{
		ProjectID:   "proj-1",
		OperationID: submitBody.Operation.OperationID,
		Reason:      "user requested cancel",
	}))
	if !cancelResp.OK {
		t.Fatalf("cancel response = %+v", cancelResp)
	}

	record := waitForRuntimeState(t, runtime, submitBody.Operation.OperationID, daemonops.StateCancelled)
	if record.ErrorMessage != "user requested cancel" {
		t.Fatalf("cancel error message = %q, want user requested cancel", record.ErrorMessage)
	}
}

func TestSessionOperationExecutorWrapsNestedResultEnvelope(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.sessionStart = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "started"}), nil
	}
	executor := sessionOperationExecutor{runtime: runtime}
	payload := mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-3"})
	req := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "req-session", Kind: protocol.EnvelopeKindCommand, Command: "session.start", Meta: protocol.Metadata{ProjectID: "proj-1"}, Body: payload}

	resp, err := executor.Execute(context.Background(), req, req.Command, func(_ context.Context) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "started"}), nil
	})
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response = %+v", resp)
	}
	wrapped := decodeWrappedResult(t, resp.Body)
	if wrapped.OperationID == "" || wrapped.State != string(daemonops.StateDone) {
		t.Fatalf("wrapped response = %+v", wrapped)
	}
	var nested struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(wrapped.Result, &nested); err != nil {
		t.Fatalf("unmarshal nested result: %v", err)
	}
	if nested.Output != "started" {
		t.Fatalf("nested output = %q, want started", nested.Output)
	}
}

func TestCommandExecutorWrapsGitAndWorktreeResults(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.gitHandler = daemonhandlers.NewGitHandler(runtimeGitService{})
	runtime.worktreeHandler = daemonhandlers.NewWorktreeHandler(runtimeWorktreeService{})
	executor := operationCommandExecutor{runtime: runtime}

	cases := []struct {
		name    string
		command string
		body    any
		result  any
	}{
		{
			name:    "git merge",
			command: daemonhandlers.CommandGitMerge,
			body:    map[string]string{"worktree": "/tmp/wt", "branch": "main"},
			result:  map[string]string{"worktree": "/tmp/wt", "branch": "main"},
		},
		{
			name:    "worktree cleanup",
			command: daemonhandlers.CommandWorktreeCleanupOrphaned,
			body:    map[string]string{"project_id": "proj-1"},
			result:  protocol.CleanupOrphanedResponseBody{ProjectID: "proj-1", WorktreesRemoved: 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "req-" + tc.name, Kind: protocol.EnvelopeKindCommand, Command: tc.command, Meta: protocol.Metadata{ProjectID: "proj-1"}, Body: mustJSON(t, tc.body)}
			resp := executor.Execute(context.Background(), req, tc.command, func(_ context.Context) protocol.ResponseEnvelope {
				return testResponse(req, tc.result)
			})
			if !resp.OK {
				t.Fatalf("response = %+v", resp)
			}
			wrapped := decodeWrappedResult(t, resp.Body)
			if wrapped.OperationID == "" || wrapped.State != string(daemonops.StateDone) {
				t.Fatalf("wrapped response = %+v", wrapped)
			}
		})
	}
}

func TestDaemonDrainInFlightCommandsStopsIntakeCancelsQueuedAndDrainsRunning(t *testing.T) {
	release := make(chan struct{})
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir(), nextRevision: sequentialRevision()})
	runtime.sessionStart = func(ctx context.Context, _ protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		select {
		case <-release:
			return protocol.ResponseEnvelope{OK: true, Body: mustJSON(t, map[string]string{"output": "done"})}, nil
		case <-ctx.Done():
			return protocol.ResponseEnvelope{}, ctx.Err()
		}
	}
	runtime.sessionStop = func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return testResponse(req, map[string]string{"output": "queued cancel"}), nil
	}

	runningReq := runtime.buildSubmitRequestForTest(t, "session.start", "proj-1", mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-10"}))
	queuedReq := runtime.buildSubmitRequestForTest(t, "session.stop", "proj-1", mustJSON(t, map[string]string{"project_id": "proj-1", "session_id": "AZ-10"}))
	if _, err := runtime.manager.Submit(context.Background(), runningReq, func(ctx context.Context) ([]byte, error) {
		resp, err := runtime.sessionStart(ctx, protocol.RequestEnvelope{})
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}); err != nil {
		t.Fatalf("submit running operation: %v", err)
	}
	queuedResult, err := runtime.manager.Submit(context.Background(), queuedReq, func(ctx context.Context) ([]byte, error) {
		resp, err := runtime.sessionStop(ctx, protocol.RequestEnvelope{})
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	})
	if err != nil {
		t.Fatalf("submit queued operation: %v", err)
	}

	waitForRuntimeState(t, runtime, queuedResult.Record.ID, daemonops.StateQueued)

	d := &Daemon{cfg: Config{IdleTimeout: 250 * time.Millisecond}, operationRuntime: runtime}
	drained := make(chan struct{})
	go func() {
		d.drainInFlightCommands()
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("drain returned before running operation completed")
	case <-time.After(50 * time.Millisecond):
	}

	if err := d.beginCommand(); err == nil {
		t.Fatal("expected new intake rejection while draining")
	}

	close(release)

	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish")
	}

	waitForRuntimeState(t, runtime, queuedResult.Record.ID, daemonops.StateCancelled)
}

func TestBuildSubmitRequestDefaultsGitFetchRemote(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:      t.TempDir(),
		nextRevision: sequentialRevision(),
		gitHandler:   daemonhandlers.NewGitHandler(runtimeGitService{}),
	})

	req := runtime.buildSubmitRequestForTest(
		t,
		daemonhandlers.CommandGitFetch,
		"proj-1",
		mustJSON(t, map[string]string{
			"worktree": "/tmp/az-1",
		}),
	)

	if req.IssueID != "/tmp/az-1" {
		t.Fatalf("issue id = %q, want /tmp/az-1", req.IssueID)
	}
	if req.DedupeKey != "git.fetch:/tmp/az-1:origin" {
		t.Fatalf("dedupe key = %q, want default origin remote", req.DedupeKey)
	}
	if len(req.ResourceKeys) != 1 || req.ResourceKeys[0] != "worktree:/tmp/az-1" {
		t.Fatalf("resource keys = %v, want worktree routing key", req.ResourceKeys)
	}
}

func (r *operationRuntime) buildSubmitRequestForTest(t *testing.T, kind, projectID string, payload []byte) daemonops.SubmitRequest {
	t.Helper()
	req, err := r.buildSubmitRequest(kind, projectID, payload, operationSubmitOverrides{})
	if err != nil {
		t.Fatalf("build submit request: %v", err)
	}
	return req
}

func testRequest(command string, body any) protocol.RequestEnvelope {
	return protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "req-" + command, Kind: protocol.EnvelopeKindCommand, Command: command, Body: marshalJSON(body)}
}

func mustJSON(tb testing.TB, value any) []byte {
	tb.Helper()
	return marshalJSON(value)
}

func testResponse(req protocol.RequestEnvelope, body any) protocol.ResponseEnvelope {
	return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, CompletedAt: time.Now().UTC(), OK: true, Body: marshalJSON(body)}
}

func decodeWrappedResult(t *testing.T, body []byte) operationResultEnvelope {
	t.Helper()
	var wrapped operationResultEnvelope
	if err := json.Unmarshal(body, &wrapped); err != nil {
		t.Fatalf("unmarshal wrapped result: %v", err)
	}
	return wrapped
}

func waitForRuntimeState(t *testing.T, runtime *operationRuntime, operationID string, want daemonops.State) daemonops.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		record, err := runtime.manager.Get(context.Background(), operationID)
		if err == nil && record.State == want {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	record, err := runtime.manager.Get(context.Background(), operationID)
	if err != nil {
		t.Fatalf("get operation %s: %v", operationID, err)
	}
	t.Fatalf("operation %s state = %s, want %s", operationID, record.State, want)
	return daemonops.Record{}
}

func collectOperationEvents(t *testing.T, ch <-chan protocol.EventEnvelope, count int) []protocol.EventEnvelope {
	t.Helper()
	events := make([]protocol.EventEnvelope, 0, count)
	deadline := time.After(2 * time.Second)
	for len(events) < count {
		select {
		case evt := <-ch:
			events = append(events, evt)
		case <-deadline:
			t.Fatalf("timed out waiting for %d operation events, got %d", count, len(events))
		}
	}
	return events
}

func sequentialRevision() func(string) uint64 {
	var next uint64
	return func(string) uint64 {
		next++
		return next
	}
}

func marshalJSON(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}
