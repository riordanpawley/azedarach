package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type recordingApplyService struct {
	calls            []string
	createErr        error
	updateErr        error
	updateDetailsErr error
	addDepErr        error
	removeDepErr     error
	deleteErr        error
	archiveErr       error
}

func (r *recordingApplyService) Create(_ context.Context, params issues.CreateTaskParams) (string, error) {
	parentID := ""
	if params.ParentID != nil {
		parentID = *params.ParentID
	}
	r.calls = append(r.calls, fmt.Sprintf("create:%s:%s:%s:%s", params.Title, params.Priority.String(), params.Type, parentID))
	if r.createErr != nil {
		return "", r.createErr
	}
	return "az-new", nil
}

func (r *recordingApplyService) Update(_ context.Context, id string, status domain.Status) error {
	r.calls = append(r.calls, fmt.Sprintf("status:%s:%s", id, status))
	return r.updateErr
}

func (r *recordingApplyService) UpdateDetails(_ context.Context, id string, params issues.UpdateTaskParams) error {
	r.calls = append(r.calls, fmt.Sprintf("update:%s:%s:%s:%s", id, params.Title, params.Priority.String(), params.Type))
	return r.updateDetailsErr
}

func (r *recordingApplyService) AddDependency(_ context.Context, issueID, dependsOnID, dependencyType string) error {
	r.calls = append(r.calls, fmt.Sprintf("dep-add:%s:%s:%s", issueID, dependsOnID, dependencyType))
	return r.addDepErr
}

func (r *recordingApplyService) RemoveDependency(_ context.Context, issueID, dependsOnID, dependencyType string) error {
	r.calls = append(r.calls, fmt.Sprintf("dep-remove:%s:%s:%s", issueID, dependsOnID, dependencyType))
	return r.removeDepErr
}

func (r *recordingApplyService) Delete(_ context.Context, id string) error {
	r.calls = append(r.calls, fmt.Sprintf("delete:%s", id))
	return r.deleteErr
}

func (r *recordingApplyService) Archive(_ context.Context, id string) error {
	r.calls = append(r.calls, fmt.Sprintf("archive:%s", id))
	return r.archiveErr
}

type recordingApplyRevisions struct {
	current      uint64
	currentCalls int
	next         []uint64
	published    []string
}

func (r *recordingApplyRevisions) CurrentRevision(string) uint64 {
	r.currentCalls++
	return r.current
}

func (r *recordingApplyRevisions) NextRevision(string) uint64 {
	r.current++
	r.next = append(r.next, r.current)
	return r.current
}

func (r *recordingApplyRevisions) PublishTaskEvent(_ protocol.RequestEnvelope, eventName string, rev uint64) {
	r.published = append(r.published, fmt.Sprintf("%s:%d", eventName, rev))
}

func TestApplyHandlerExecutesOperationsInOrder(t *testing.T) {
	service := &recordingApplyService{}
	revisions := &recordingApplyRevisions{current: 7}
	h := NewApplyHandler(service, revisions)

	reqBody := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 7,
		DryRun:           false,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskCreate,
				Body: mustApplyJSON(t, map[string]any{
					"title":       "First",
					"description": "Draft",
					"type":        "task",
					"priority":    "high",
				}),
			},
			{
				Command: applyCommandTaskUpdateStatus,
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-2",
					"status":  "done",
				}),
			},
			{
				Command: applyCommandTaskUpdate,
				Body: mustApplyJSON(t, map[string]any{
					"task_id":     "az-3",
					"title":       "Updated",
					"description": "Changed",
					"type":        "bug",
					"priority":    "medium",
				}),
			},
			{
				Command: applyCommandTaskDelete,
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-4",
				}),
			},
			{
				Command: applyCommandTaskArchive,
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-5",
				}),
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-apply",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandTaskBulkApply,
		Meta: protocol.Metadata{
			ProjectID: "proj",
		},
		Body: body,
	})

	if !resp.OK {
		t.Fatalf("Handle() error = %+v", resp.Error)
	}
	if resp.Revision != 12 {
		t.Fatalf("Revision = %d, want 12", resp.Revision)
	}

	var result ApplyExecutionResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if result.ProjectID != "proj" {
		t.Fatalf("ProjectID = %q, want proj", result.ProjectID)
	}
	if result.SnapshotRevision != 7 {
		t.Fatalf("SnapshotRevision = %d, want 7", result.SnapshotRevision)
	}
	if result.Revision != 12 {
		t.Fatalf("Result.Revision = %d, want 12", result.Revision)
	}
	if result.DryRun {
		t.Fatal("DryRun = true, want false")
	}
	if got, want := len(result.Operations), 5; got != want {
		t.Fatalf("Operations len = %d, want %d", got, want)
	}
	if got, want := len(result.Outcomes), 5; got != want {
		t.Fatalf("Outcomes len = %d, want %d", got, want)
	}
	for i, op := range result.Operations {
		if op.Index != i {
			t.Fatalf("Operations[%d].Index = %d, want %d", i, op.Index, i)
		}
	}
	for i, outcome := range result.Outcomes {
		if outcome.Index != i {
			t.Fatalf("Outcomes[%d].Index = %d, want %d", i, outcome.Index, i)
		}
		if outcome.Status != applyExecutionOutcomeStatusSuccess {
			t.Fatalf("Outcomes[%d].Status = %q, want %q", i, outcome.Status, applyExecutionOutcomeStatusSuccess)
		}
	}
	if result.Summary != (ApplyExecutionSummary{Total: 5, Succeeded: 5, Failed: 0}) {
		t.Fatalf("Summary = %+v, want all success", result.Summary)
	}

	if got, want := service.calls, []string{
		"create:First:P1:task:",
		"status:az-2:closed",
		"update:az-3:Updated:P2:bug",
		"delete:az-4",
		"archive:az-5",
	}; !equalStrings(got, want) {
		t.Fatalf("service calls = %v, want %v", got, want)
	}

	if got, want := revisions.published, []string{
		"task.created:8",
		"task.updated:9",
		"task.updated:10",
		"task.deleted:11",
		"task.archived:12",
	}; !equalStrings(got, want) {
		t.Fatalf("published events = %v, want %v", got, want)
	}
}

func TestApplyHandlerAggregatesPartialFailuresInOrder(t *testing.T) {
	service := &recordingApplyService{
		deleteErr: fmt.Errorf("delete failed"),
	}
	revisions := &recordingApplyRevisions{current: 3}
	h := NewApplyHandler(service, revisions)

	reqBody := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 3,
		DryRun:           false,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskCreate,
				Body: mustApplyJSON(t, map[string]any{
					"title":       "First",
					"description": "Draft",
					"type":        "task",
					"priority":    "high",
				}),
			},
			{
				Command: applyCommandTaskDelete,
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-2",
				}),
			},
			{
				Command: applyCommandTaskArchive,
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-3",
				}),
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-apply",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandTaskBulkApply,
		Meta: protocol.Metadata{
			ProjectID: "proj",
		},
		Body: body,
	})

	if !resp.OK {
		t.Fatalf("Handle() error = %+v", resp.Error)
	}

	var result ApplyExecutionResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if got, want := result.Summary, (ApplyExecutionSummary{Total: 3, Succeeded: 2, Failed: 1}); got != want {
		t.Fatalf("Summary = %+v, want %+v", got, want)
	}
	if got, want := len(result.Operations), 2; got != want {
		t.Fatalf("Operations len = %d, want %d", got, want)
	}
	if got, want := len(result.Outcomes), 3; got != want {
		t.Fatalf("Outcomes len = %d, want %d", got, want)
	}
	for i, outcome := range result.Outcomes {
		if outcome.Index != i {
			t.Fatalf("Outcomes[%d].Index = %d, want %d", i, outcome.Index, i)
		}
	}
	if result.Outcomes[1].Status != applyExecutionOutcomeStatusFailure {
		t.Fatalf("Outcomes[1].Status = %q, want %q", result.Outcomes[1].Status, applyExecutionOutcomeStatusFailure)
	}
	if result.Outcomes[1].Error != "delete failed" {
		t.Fatalf("Outcomes[1].Error = %q, want delete failed", result.Outcomes[1].Error)
	}
	if got, want := service.calls, []string{
		"create:First:P1:task:",
		"delete:az-2",
		"archive:az-3",
	}; !equalStrings(got, want) {
		t.Fatalf("service calls = %v, want %v", got, want)
	}
	if got, want := revisions.published, []string{
		"task.created:4",
		"task.archived:5",
	}; !equalStrings(got, want) {
		t.Fatalf("published events = %v, want %v", got, want)
	}
}

func TestApplyHandlerRevisionMismatchStopsBeforeExecution(t *testing.T) {
	service := &recordingApplyService{}
	revisions := &recordingApplyRevisions{current: 9}
	h := NewApplyHandler(service, revisions)

	reqBody := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 7,
		DryRun:           false,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskDelete,
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-1",
				}),
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-apply",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandTaskBulkApply,
		Meta: protocol.Metadata{
			ProjectID: "proj",
		},
		Body: body,
	})

	if resp.OK {
		t.Fatal("expected revision mismatch to fail")
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeRevisionGap {
		t.Fatalf("error = %+v, want revision gap", resp.Error)
	}
	if len(service.calls) != 0 {
		t.Fatalf("service calls = %v, want none", service.calls)
	}
	if len(revisions.published) != 0 {
		t.Fatalf("published events = %v, want none", revisions.published)
	}
}

func TestApplyHandlerDryRunReturnsPreviewWithoutExecuting(t *testing.T) {
	service := &recordingApplyService{}
	revisions := &recordingApplyRevisions{current: 11}
	h := NewApplyHandler(service, revisions)

	reqBody := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 11,
		DryRun:           true,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: applyCommandTaskCreate,
				Body: mustApplyJSON(t, map[string]any{
					"title":       "Draft task",
					"description": "Preview only",
					"type":        "task",
					"priority":    "high",
				}),
			},
			{
				Command: applyCommandTaskDelete,
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-9",
				}),
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp := h.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-dry-run",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandTaskBulkApply,
		Meta: protocol.Metadata{
			ProjectID: "proj",
		},
		Body: body,
	})

	if !resp.OK {
		t.Fatalf("Handle() error = %+v", resp.Error)
	}
	if resp.Revision != 0 {
		t.Fatalf("Revision = %d, want 0 for dry run", resp.Revision)
	}
	var preview ApplyDryRunPreview
	if err := json.Unmarshal(resp.Body, &preview); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if !preview.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if preview.SnapshotRevision != 11 {
		t.Fatalf("SnapshotRevision = %d, want 11", preview.SnapshotRevision)
	}
	if got, want := len(preview.Operations), 2; got != want {
		t.Fatalf("Operations len = %d, want %d", got, want)
	}
	if preview.Operations[0].Index != 0 || preview.Operations[1].Index != 1 {
		t.Fatalf("unexpected preview indexes: %+v", preview.Operations)
	}
	if preview.Operations[0].Command != applyCommandTaskCreate {
		t.Fatalf("Operations[0].Command = %q, want %q", preview.Operations[0].Command, applyCommandTaskCreate)
	}
	if preview.Operations[1].Command != applyCommandTaskDelete {
		t.Fatalf("Operations[1].Command = %q, want %q", preview.Operations[1].Command, applyCommandTaskDelete)
	}
	if got := service.calls; len(got) != 0 {
		t.Fatalf("service calls = %v, want none", got)
	}
	if revisions.currentCalls != 0 {
		t.Fatalf("CurrentRevision calls = %d, want 0", revisions.currentCalls)
	}
	if len(revisions.next) != 0 {
		t.Fatalf("NextRevision calls = %v, want none", revisions.next)
	}
	if len(revisions.published) != 0 {
		t.Fatalf("published events = %v, want none", revisions.published)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
