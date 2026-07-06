package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonnotices "github.com/riordanpawley/azedarach/internal/daemon/notices"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
)

func TestOperationFailureEmitsTypedNotice(t *testing.T) {
	ctx := context.Background()
	runtime, notices := newOperationNoticeTestRuntime(t)

	result, err := runtime.manager.Submit(ctx, daemonops.SubmitRequest{
		ProjectID:    "proj-1",
		IssueID:      "AZ-1",
		Kind:         "session.start",
		DedupeKey:    "session.start:AZ-1",
		ResourceKeys: []string{"issue:proj-1:AZ-1"},
	}, func(context.Context) ([]byte, error) {
		return nil, errors.New("worktree dirty: modified or untracked files")
	})
	if err != nil {
		t.Fatalf("submit operation: %v", err)
	}
	record := waitForRuntimeState(t, runtime, result.Record.ID, daemonops.StateFailed)

	records, err := notices.List(ctx, daemonnotices.Query{
		ProjectID: "proj-1",
		States:    []daemonnotices.State{daemonnotices.StateActive},
		Category:  "operation_failed",
	})
	if err != nil {
		t.Fatalf("list notices: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("notices len = %d, want 1: %+v", len(records), records)
	}
	notice := records[0]
	if notice.Scope.Type != "task" || notice.Scope.ID != "AZ-1" {
		t.Fatalf("notice scope = %+v, want task/AZ-1", notice.Scope)
	}
	if notice.Source == nil ||
		notice.Source.OperationID.String() != record.ID ||
		notice.Source.OperationKind != "session.start" ||
		notice.Source.OperationState != protocol.OperationStateFailed ||
		notice.Source.Producer != "daemon.operation" {
		t.Fatalf("notice source = %+v, want failed session.start operation %s", notice.Source, record.ID)
	}
	if notice.Severity != daemonnotices.SeverityError || notice.State != daemonnotices.StateActive || notice.Read {
		t.Fatalf("notice severity/state/read = %s/%s/%v, want error/active/unread", notice.Severity, notice.State, notice.Read)
	}
	if notice.Cause == nil || notice.Cause.Code != "worktree_dirty" || notice.Cause.ErrorCode != protocol.ErrorCodeInternal {
		t.Fatalf("notice cause = %+v, want worktree_dirty internal", notice.Cause)
	}
	if !noticeHasAction(notice, "dismiss") || !noticeHasAction(notice, "open_task") || !noticeHasAction(notice, "copy_details") {
		t.Fatalf("notice actions = %+v, want dismiss/open_task/copy_details", notice.Actions)
	}
	if notice.DedupeKey != "operation_failed:session.start:session.start:AZ-1" {
		t.Fatalf("notice dedupe key = %q", notice.DedupeKey)
	}
}

func TestOperationFailureNoticeDedupesRepeatedFailures(t *testing.T) {
	ctx := context.Background()
	runtime, notices := newOperationNoticeTestRuntime(t)
	req := daemonops.SubmitRequest{
		ProjectID:    "proj-1",
		IssueID:      "AZ-1",
		Kind:         "session.start",
		DedupeKey:    "session.start:AZ-1",
		ResourceKeys: []string{"issue:proj-1:AZ-1"},
	}
	for i := 0; i < 2; i++ {
		result, err := runtime.manager.Submit(ctx, req, func(context.Context) ([]byte, error) {
			return nil, errors.New("conflict while starting session")
		})
		if err != nil {
			t.Fatalf("submit operation %d: %v", i, err)
		}
		waitForRuntimeState(t, runtime, result.Record.ID, daemonops.StateFailed)
	}

	records, err := notices.List(ctx, daemonnotices.Query{
		ProjectID: "proj-1",
		States:    []daemonnotices.State{daemonnotices.StateActive},
		Category:  "operation_failed",
		DedupeKey: "operation_failed:session.start:session.start:AZ-1",
	})
	if err != nil {
		t.Fatalf("list notices: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("notices len = %d, want one deduped notice: %+v", len(records), records)
	}
	if records[0].OccurrenceCount != 2 || records[0].Cause == nil || records[0].Cause.Code != "conflict" {
		t.Fatalf("notice = %+v cause=%+v, want occurrence_count=2 conflict", records[0], records[0].Cause)
	}
}

func TestOperationSuccessResolvesMatchingFailureNotice(t *testing.T) {
	ctx := context.Background()
	runtime, notices := newOperationNoticeTestRuntime(t)
	req := daemonops.SubmitRequest{
		ProjectID:    "proj-1",
		IssueID:      "AZ-1",
		Kind:         "session.start",
		DedupeKey:    "session.start:AZ-1",
		ResourceKeys: []string{"issue:proj-1:AZ-1"},
	}
	failed, err := runtime.manager.Submit(ctx, req, func(context.Context) ([]byte, error) {
		return nil, errors.New("session start failed")
	})
	if err != nil {
		t.Fatalf("submit failed operation: %v", err)
	}
	waitForRuntimeState(t, runtime, failed.Record.ID, daemonops.StateFailed)

	succeeded, err := runtime.manager.Submit(ctx, req, func(context.Context) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatalf("submit successful operation: %v", err)
	}
	waitForRuntimeState(t, runtime, succeeded.Record.ID, daemonops.StateDone)

	active, err := notices.List(ctx, daemonnotices.Query{
		ProjectID: "proj-1",
		States:    []daemonnotices.State{daemonnotices.StateActive},
		Category:  "operation_failed",
	})
	if err != nil {
		t.Fatalf("list active notices: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active notices = %+v, want none after success", active)
	}
	resolved, err := notices.List(ctx, daemonnotices.Query{
		ProjectID: "proj-1",
		States:    []daemonnotices.State{daemonnotices.StateResolved},
		Category:  "operation_failed",
		DedupeKey: "operation_failed:session.start:session.start:AZ-1",
	})
	if err != nil {
		t.Fatalf("list resolved notices: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ResolvedAt == nil {
		t.Fatalf("resolved notices = %+v, want one resolved notice", resolved)
	}
}

func TestStaleOperationFailureDoesNotReactivateResolvedNotice(t *testing.T) {
	ctx := context.Background()
	_, notices := newOperationNoticeTestRuntime(t)
	adapter := &operationStoreAdapter{noticeService: notices}
	successAt := time.Unix(1_700_000_100, 0).UTC()
	record := daemonops.Record{
		ID:           "20260405163240.161281000",
		ProjectID:    "proj-1",
		IssueID:      "AZ-1",
		Kind:         "session.start",
		DedupeKey:    "session.start:AZ-1",
		ResourceKeys: []string{"issue:proj-1:AZ-1"},
		State:        daemonops.StateFailed,
		CreatedAt:    successAt.Add(-time.Minute),
		UpdatedAt:    successAt.Add(time.Minute),
		ErrorMessage: "late failure",
	}
	finishedAt := successAt.Add(time.Minute)
	record.FinishedAt = &finishedAt

	active, _, _, err := notices.Upsert(ctx, daemonnotices.Candidate{
		ProjectID: "proj-1",
		Scope:     daemonnotices.Scope{Type: "task", ID: "AZ-1"},
		Source: &daemonnotices.Source{
			OperationID:    "20260405163239.000000000",
			OperationKind:  "session.start",
			OperationState: protocol.OperationStateFailed,
			Producer:       "daemon.operation",
		},
		Severity:       daemonnotices.SeverityError,
		Category:       "operation_failed",
		Title:          "Session start failed",
		Summary:        "Could not complete session start for AZ-1",
		Cause:          &daemonnotices.Cause{Code: "operation_failed", Message: "first failure", ErrorCode: protocol.ErrorCodeInternal},
		DedupeKey:      operationNoticeDedupeKey(record),
		OccurredAt:     successAt.Add(-2 * time.Minute),
		RetentionClass: daemonnotices.RetentionError,
	})
	if err != nil {
		t.Fatalf("seed active notice: %v", err)
	}
	if _, _, _, err := notices.Update(ctx, daemonnotices.UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  active.NoticeID,
		State:     daemonnotices.StateResolved,
		Now:       successAt,
	}); err != nil {
		t.Fatalf("resolve seed notice: %v", err)
	}
	if _, _, _, err := notices.Update(ctx, daemonnotices.UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  active.NoticeID,
		State:     daemonnotices.StateDismissed,
		Now:       successAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("dismiss resolved seed notice: %v", err)
	}

	adapter.upsertOperationFailureNotice(ctx, record)

	activeRecords, err := notices.List(ctx, daemonnotices.Query{
		ProjectID: "proj-1",
		States:    []daemonnotices.State{daemonnotices.StateActive},
		Category:  "operation_failed",
		DedupeKey: operationNoticeDedupeKey(record),
	})
	if err != nil {
		t.Fatalf("list active notices: %v", err)
	}
	if len(activeRecords) != 0 {
		t.Fatalf("active notices = %+v, want stale failure ignored", activeRecords)
	}
}

func TestStaleOperationFailureDoesNotReactivateExpiredNotice(t *testing.T) {
	ctx := context.Background()
	_, notices := newOperationNoticeTestRuntime(t)
	adapter := &operationStoreAdapter{noticeService: notices}
	successAt := time.Unix(1_700_000_100, 0).UTC()
	expiresAt := successAt.Add(time.Second)
	record := daemonops.Record{
		ID:           "20260405163240.161281000",
		ProjectID:    "proj-1",
		IssueID:      "AZ-1",
		Kind:         "session.start",
		DedupeKey:    "session.start:AZ-1",
		ResourceKeys: []string{"issue:proj-1:AZ-1"},
		State:        daemonops.StateFailed,
		CreatedAt:    successAt.Add(-time.Minute),
		UpdatedAt:    successAt.Add(2 * time.Second),
		ErrorMessage: "late failure",
	}
	finishedAt := successAt.Add(2 * time.Second)
	record.FinishedAt = &finishedAt

	active, _, _, err := notices.Upsert(ctx, daemonnotices.Candidate{
		ProjectID: "proj-1",
		Scope:     daemonnotices.Scope{Type: "task", ID: "AZ-1"},
		Source: &daemonnotices.Source{
			OperationID:    "20260405163239.000000000",
			OperationKind:  "session.start",
			OperationState: protocol.OperationStateFailed,
			Producer:       "daemon.operation",
		},
		Severity:       daemonnotices.SeverityError,
		Category:       "operation_failed",
		Title:          "Session start failed",
		Summary:        "Could not complete session start for AZ-1",
		Cause:          &daemonnotices.Cause{Code: "operation_failed", Message: "first failure", ErrorCode: protocol.ErrorCodeInternal},
		DedupeKey:      operationNoticeDedupeKey(record),
		OccurredAt:     successAt.Add(-2 * time.Minute),
		ExpiresAt:      &expiresAt,
		RetentionClass: daemonnotices.RetentionError,
	})
	if err != nil {
		t.Fatalf("seed active notice: %v", err)
	}
	if _, _, _, err := notices.Update(ctx, daemonnotices.UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  active.NoticeID,
		State:     daemonnotices.StateResolved,
		Now:       successAt,
	}); err != nil {
		t.Fatalf("resolve seed notice: %v", err)
	}
	expired, _, err := notices.ExpireDue(ctx, daemonnotices.ExpireQuery{ProjectID: "proj-1", Now: expiresAt.Add(time.Second)})
	if err != nil {
		t.Fatalf("expire resolved notice: %v", err)
	}
	if len(expired) != 1 || expired[0].State != daemonnotices.StateExpired {
		t.Fatalf("expired notices = %+v, want one expired notice", expired)
	}

	adapter.upsertOperationFailureNotice(ctx, record)

	activeRecords, err := notices.List(ctx, daemonnotices.Query{
		ProjectID: "proj-1",
		States:    []daemonnotices.State{daemonnotices.StateActive},
		Category:  "operation_failed",
		DedupeKey: operationNoticeDedupeKey(record),
	})
	if err != nil {
		t.Fatalf("list active notices: %v", err)
	}
	if len(activeRecords) != 0 {
		t.Fatalf("active notices = %+v, want stale failure ignored after expiry", activeRecords)
	}
}

func newOperationNoticeTestRuntime(t *testing.T) (*operationRuntime, *daemonnotices.Service) {
	t.Helper()
	hub := publish.NewHub(64, 16, nil)
	noticeStore := daemonnotices.NewAtPath(filepath.Join(t.TempDir(), "notices.db"), nil)
	notices := daemonnotices.NewService(daemonnotices.ServiceConfig{
		Repository:   noticeStore,
		Hub:          hub,
		NextRevision: sequentialRevision(),
	})
	t.Cleanup(func() {
		_ = notices.Close()
	})
	runtime := newOperationRuntime(operationRuntimeConfig{
		repoDir:       t.TempDir(),
		hub:           hub,
		nextRevision:  sequentialRevision(),
		noticeService: notices,
	})
	t.Cleanup(func() {
		_ = runtime.Close()
	})
	return runtime, notices
}

func noticeHasAction(record daemonnotices.Record, actionID string) bool {
	for _, action := range record.Actions {
		if action.ActionID == actionID {
			return true
		}
	}
	return false
}
