package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestSelectBulkCleanupTasksUnionsExplicitAndFilteredAndOrdersLeavesFirst(t *testing.T) {
	parent := naming.IssueID("parent")
	old := time.Now().Add(-48 * time.Hour)
	tasks := []domain.Task{
		{ID: parent, Title: "parent", Status: domain.StatusInReview, UpdatedAt: old},
		{ID: "child", Title: "child", ParentID: &parent, Status: domain.StatusInReview, UpdatedAt: old},
		{ID: "explicit", Title: "recent", Status: domain.StatusOpen, UpdatedAt: time.Now()},
		{ID: "ignored", Title: "recent", Status: domain.StatusOpen, UpdatedAt: time.Now()},
	}
	got := selectBulkCleanupTasks(tasks, taskBulkCleanupRequest{
		TaskIDs: []string{"explicit"}, Statuses: []string{"in_review"}, UpdatedBefore: ptrTime(time.Now().Add(-24 * time.Hour)),
	})
	want := []string{"child", "explicit", "parent"}
	if len(got) != len(want) {
		t.Fatalf("selected = %+v, want ids %v", got, want)
	}
	for i := range want {
		if got[i].ID.String() != want[i] {
			t.Fatalf("selected[%d] = %s, want %s", i, got[i].ID, want[i])
		}
	}
	limited := selectBulkCleanupTasks(tasks, taskBulkCleanupRequest{TaskIDs: []string{"explicit"}, Statuses: []string{"in_review"}, Limit: 1})
	if len(limited) != 2 || limited[0].ID.String() != "child" || limited[1].ID.String() != "explicit" {
		t.Fatalf("limited selection = %+v, want leaf candidate plus explicit id", limited)
	}
}

func TestTaskBulkCleanupContinuesAfterUnresolvedDescendantFailure(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-bulk-cleanup"
	repoDir := t.TempDir()
	client := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	parent, err := client.Create(ctx, issues.CreateTaskParams{Title: "blocked parent", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.Create(ctx, issues.CreateTaskParams{Title: "unselected child", Type: domain.TypeTask, ParentID: &parent})
	if err != nil {
		t.Fatal(err)
	}
	_ = child
	independent, err := client.Create(ctx, issues.CreateTaskParams{Title: "independent", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	store := state.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, hub: publish.NewHub(16, 8, slog.Default()), issueClientsByProject: map[string]*issues.Client{projectID: client}, runtimeStoresByProject: map[string]*state.RuntimeStateStore{projectID: store}, revision: map[string]uint64{}}
	body, _ := json.Marshal(taskBulkCleanupRequest{TaskIDs: []string{parent, independent}, CloseOutcome: string(domain.IssueCloseCancelled)})
	resp, err := d.handleTaskBulkCleanup(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "bulk", Kind: protocol.EnvelopeKindCommand, Command: "task.bulk_cleanup", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil || !resp.OK {
		t.Fatalf("response = %+v, err = %v", resp, err)
	}
	var result taskBulkCleanupResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %+v", result.Items)
	}
	byID := map[string]taskBulkCleanupItem{}
	for _, item := range result.Items {
		byID[item.TaskID] = item
	}
	if !byID[independent].Success {
		t.Fatalf("independent result = %+v", byID[independent])
	}
	if byID[parent].Success || byID[parent].Error == "" {
		t.Fatalf("parent result = %+v, want failure", byID[parent])
	}
	body, _ = json.Marshal(taskBulkCleanupRequest{TaskIDs: []string{independent}, CloseOutcome: string(domain.IssueCloseCancelled)})
	resp, err = d.handleTaskBulkCleanup(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "bulk-retry", Kind: protocol.EnvelopeKindCommand, Command: "task.bulk_cleanup", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil || !resp.OK {
		t.Fatalf("retry response = %+v, err = %v", resp, err)
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || !result.Items[0].Success || !result.Items[0].Skipped {
		t.Fatalf("retry item = %+v", result.Items)
	}
	timed, err := client.Create(ctx, issues.CreateTaskParams{Title: "times out", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(taskBulkCleanupRequest{TaskIDs: []string{timed}, CloseOutcome: string(domain.IssueCloseCancelled), PerIssueTimeout: time.Nanosecond})
	resp, err = d.handleTaskBulkCleanup(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "bulk-timeout", Kind: protocol.EnvelopeKindCommand, Command: "task.bulk_cleanup", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil || !resp.OK {
		t.Fatalf("timeout response = %+v, err = %v", resp, err)
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Success || result.Items[0].Error == "" {
		t.Fatalf("timeout item = %+v", result.Items)
	}
}

func TestTaskBulkCleanupDryRunSelection(t *testing.T) {
	now := time.Now()
	tasks := []domain.Task{{ID: "done", Status: domain.StatusDone, UpdatedAt: now}, {ID: "open", Status: domain.StatusOpen, UpdatedAt: now}}
	selected := selectBulkCleanupTasks(tasks, taskBulkCleanupRequest{TaskIDs: []string{"done", "open"}, DryRun: true})
	if len(selected) != 2 {
		t.Fatalf("selected = %+v", selected)
	}
}

func TestTaskBulkCleanupAcceptsClosedActionAlias(t *testing.T) {
	outcome, status, err := daemonTaskCloseOutcomeStatus("closed")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != domain.IssueCloseCompleted || status != domain.StatusDone {
		t.Fatalf("outcome = %s status = %s", outcome, status)
	}
}

func TestTaskBulkCleanupLaterItemGetsFreshTimeoutBudget(t *testing.T) {
	parent := context.Background()
	first, cancelFirst := taskBulkCleanupItemContext(parent, 5*time.Millisecond)
	<-first.Done()
	cancelFirst()

	second, cancelSecond := taskBulkCleanupItemContext(parent, 50*time.Millisecond)
	defer cancelSecond()
	select {
	case <-second.Done():
		t.Fatalf("later item started with consumed budget: %v", second.Err())
	case <-time.After(10 * time.Millisecond):
	}
}

func TestTaskBulkCleanupRejectsOutOfRangePerIssueTimeout(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-bulk-timeout-validation"
	repoDir := t.TempDir()
	client := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	store := state.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, ".azedarach", "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, hub: publish.NewHub(16, 8, slog.Default()), issueClientsByProject: map[string]*issues.Client{projectID: client}, runtimeStoresByProject: map[string]*state.RuntimeStateStore{projectID: store}, revision: map[string]uint64{}}
	for _, timeout := range []time.Duration{-time.Second, taskBulkCleanupMaxPerIssueTimeout + time.Second} {
		body, _ := json.Marshal(taskBulkCleanupRequest{TaskIDs: []string{"missing"}, PerIssueTimeout: timeout})
		resp, err := d.handleTaskBulkCleanup(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "timeout-validation", Kind: protocol.EnvelopeKindCommand, Command: "task.bulk_cleanup", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
			t.Fatalf("timeout %s response = %+v, want invalid request", timeout, resp)
		}
	}
}

func ptrTime(v time.Time) *time.Time { return &v }
