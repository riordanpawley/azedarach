package linearsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/linearapi"
)

func TestServiceRunImportsRemoteIssueAndRecordsRef(t *testing.T) {
	store := &fakeStore{}
	remoteUpdated := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	service := Service{
		Store: store,
		Linear: &fakeLinear{issues: []linearapi.Issue{{
			ID:          "lin-1",
			Identifier:  "CHE-1",
			Title:       "Remote issue",
			Description: "Body",
			Priority:    2,
			State:       linearapi.State{Name: "Todo", Type: "unstarted"},
			CreatedAt:   remoteUpdated.Add(-time.Hour),
			UpdatedAt:   remoteUpdated,
		}}},
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear:  config.LinearTrackerConfig{Team: "CHE", Project: "Chefy"},
		},
		ProjectID: "chefy",
		Now:       func() time.Time { return remoteUpdated.Add(time.Hour) },
	}
	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Imported != 1 || summary.RemoteIssues != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(store.tasks) != 1 || store.tasks[0].ID.String() != "CHE-1" {
		t.Fatalf("tasks = %+v", store.tasks)
	}
	if len(store.refs) != 1 || store.refs[0].ExternalID != "lin-1" {
		t.Fatalf("refs = %+v", store.refs)
	}
}

func TestServiceRunDoesNotReimportKnownArchivedLocalIssue(t *testing.T) {
	store := &fakeStore{
		refs: []issues.ExternalRef{{
			Provider:           ProviderLinear,
			IssueID:            "CHE-1",
			ExternalID:         "lin-1",
			ExternalIdentifier: "CHE-1",
			LastSyncHash:       "previous",
		}},
	}
	remoteUpdated := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	service := Service{
		Store: store,
		Linear: &fakeLinear{issues: []linearapi.Issue{{
			ID:          "lin-1",
			Identifier:  "CHE-1",
			Title:       "Remote issue",
			Description: "Body",
			Priority:    2,
			State:       linearapi.State{Name: "Todo", Type: "unstarted"},
			CreatedAt:   remoteUpdated.Add(-time.Hour),
			UpdatedAt:   remoteUpdated,
		}}},
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear:  config.LinearTrackerConfig{Team: "CHE"},
		},
		ProjectID: "chefy",
		Now:       func() time.Time { return remoteUpdated.Add(time.Hour) },
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Imported != 0 || summary.UpdatedLocal != 0 {
		t.Fatalf("summary = %+v, want no local import/update", summary)
	}
	if store.upsertSyncedCalls != 0 {
		t.Fatalf("upsert synced calls = %d, want 0", store.upsertSyncedCalls)
	}
	if len(store.tasks) != 0 {
		t.Fatalf("tasks = %+v, want archived local issue to remain absent from active list", store.tasks)
	}
	if len(store.refs) != 1 || store.refs[0].LastSyncHash == "previous" {
		t.Fatalf("refs = %+v, want external ref refreshed without local reimport", store.refs)
	}
}

func TestServiceRunTreatsMissingLinearProjectAsTeamOnlySync(t *testing.T) {
	store := &fakeStore{}
	linear := &fakeLinear{}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear:  config.LinearTrackerConfig{Team: "CHE"},
		},
		ProjectID: "chefy",
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Skipped || !summary.Enabled {
		t.Fatalf("summary = %+v, want enabled non-skipped sync", summary)
	}
	if got, want := linear.lastTeam, "CHE"; got != want {
		t.Fatalf("linear team = %q, want %q", got, want)
	}
	if got := linear.lastProject; got != "" {
		t.Fatalf("linear project = %q, want blank team-only filter", got)
	}
}

func TestServiceRunUpdatesLocalWhenOnlyLinearChanged(t *testing.T) {
	base := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	local := domain.Task{
		ID:          mustIssueID(t, "CHE-1"),
		Title:       "Original title",
		Description: "Original body",
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
		CreatedAt:   base,
		UpdatedAt:   base,
	}
	remote := linearapi.Issue{
		ID:          "lin-1",
		Identifier:  "CHE-1",
		Title:       "Linear title",
		Description: "Original body",
		Priority:    3,
		State:       linearapi.State{Name: "Todo", Type: "unstarted"},
		CreatedAt:   base,
		UpdatedAt:   base.Add(time.Hour),
	}
	store := &fakeStore{
		tasks: []domain.Task{local},
		refs: []issues.ExternalRef{{
			Provider:           ProviderLinear,
			IssueID:            "CHE-1",
			ExternalID:         "lin-1",
			ExternalIdentifier: "CHE-1",
			LastSyncHash:       issues.HashTaskForSync(local),
			LastSyncPayload:    encodeSyncPayload(syncPayloadFromTask(local)),
		}},
	}
	linear := &fakeLinear{issues: []linearapi.Issue{remote}}
	service := Service{
		Store:     store,
		Linear:    linear,
		Config:    config.IssueTrackerConfig{Backend: "linear", Sync: config.IssueSyncConfig{Enabled: true}, Linear: config.LinearTrackerConfig{Team: "CHE"}},
		ProjectID: "chefy",
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.UpdatedLocal != 1 || summary.PushedRemote != 0 || summary.Conflicts != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if got := store.tasks[0].Title; got != "Linear title" {
		t.Fatalf("local title = %q, want Linear title", got)
	}
	if len(linear.updates) != 0 {
		t.Fatalf("updates = %+v, want none", linear.updates)
	}
}

func TestServiceRunPushesRemoteWhenOnlyLocalChanged(t *testing.T) {
	base := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	remote := linearapi.Issue{
		ID:          "lin-1",
		Identifier:  "CHE-1",
		Title:       "Original title",
		Description: "Original body",
		Priority:    3,
		State:       linearapi.State{Name: "Todo", Type: "unstarted"},
		CreatedAt:   base,
		UpdatedAt:   base,
	}
	remoteTask, ok := taskFromLinear(remote)
	if !ok {
		t.Fatal("remote issue did not map to task")
	}
	local := remoteTask
	local.Title = "Local title"
	local.UpdatedAt = base.Add(time.Hour)
	store := &fakeStore{
		tasks: []domain.Task{local},
		refs: []issues.ExternalRef{{
			Provider:           ProviderLinear,
			IssueID:            "CHE-1",
			ExternalID:         "lin-1",
			ExternalIdentifier: "CHE-1",
			LastSyncHash:       issues.HashTaskForSync(remoteTask),
			LastSyncPayload:    encodeSyncPayload(syncPayloadFromTask(remoteTask)),
		}},
	}
	linear := &fakeLinear{issues: []linearapi.Issue{remote}}
	service := Service{
		Store:     store,
		Linear:    linear,
		Config:    config.IssueTrackerConfig{Backend: "linear", Sync: config.IssueSyncConfig{Enabled: true}, Linear: config.LinearTrackerConfig{Team: "CHE"}},
		ProjectID: "chefy",
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.PushedRemote != 1 || summary.UpdatedLocal != 0 || summary.Conflicts != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(linear.updates) != 1 || linear.updates[0].Title != "Local title" {
		t.Fatalf("updates = %+v, want one local title push", linear.updates)
	}
}

func TestServiceRunMergesNonOverlappingLocalAndLinearChanges(t *testing.T) {
	base := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	original := domain.Task{
		ID:          mustIssueID(t, "CHE-1"),
		Title:       "Original title",
		Description: "Original body",
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
		CreatedAt:   base,
		UpdatedAt:   base,
	}
	local := original
	local.Title = "Local title"
	local.UpdatedAt = base.Add(time.Hour)
	remote := linearapi.Issue{
		ID:          "lin-1",
		Identifier:  "CHE-1",
		Title:       "Original title",
		Description: "Linear body",
		Priority:    3,
		State:       linearapi.State{Name: "Todo", Type: "unstarted"},
		CreatedAt:   base,
		UpdatedAt:   base.Add(2 * time.Hour),
	}
	store := &fakeStore{
		tasks: []domain.Task{local},
		refs: []issues.ExternalRef{{
			Provider:           ProviderLinear,
			IssueID:            "CHE-1",
			ExternalID:         "lin-1",
			ExternalIdentifier: "CHE-1",
			LastSyncHash:       issues.HashTaskForSync(original),
			LastSyncPayload:    encodeSyncPayload(syncPayloadFromTask(original)),
		}},
	}
	linear := &fakeLinear{issues: []linearapi.Issue{remote}}
	service := Service{
		Store:     store,
		Linear:    linear,
		Config:    config.IssueTrackerConfig{Backend: "linear", Sync: config.IssueSyncConfig{Enabled: true}, Linear: config.LinearTrackerConfig{Team: "CHE"}},
		ProjectID: "chefy",
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Conflicts != 0 || summary.PushedRemote != 1 || summary.UpdatedLocal != 1 || summary.PendingPushes != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(linear.updates) != 1 || linear.updates[0].Title != "Local title" {
		t.Fatalf("updates = %+v, want local title push", linear.updates)
	}
	if got := store.tasks[0].Title; got != "Local title" {
		t.Fatalf("local title = %q, want local value preserved", got)
	}
	if got := store.tasks[0].Description; got != "Linear body" {
		t.Fatalf("local description = %q, want Linear body", got)
	}
	if len(store.conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", store.conflicts)
	}
}

func TestServiceRunUsesLastWriteWinsForSameFieldByDefault(t *testing.T) {
	base := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	original := domain.Task{
		ID:          mustIssueID(t, "CHE-1"),
		Title:       "Original title",
		Description: "Original body",
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
		CreatedAt:   base,
		UpdatedAt:   base,
	}
	local := original
	local.Title = "Local title"
	local.UpdatedAt = base.Add(time.Hour)
	remote := linearapi.Issue{
		ID:          "lin-1",
		Identifier:  "CHE-1",
		Title:       "Linear title",
		Description: "Original body",
		Priority:    3,
		State:       linearapi.State{Name: "Todo", Type: "unstarted"},
		CreatedAt:   base,
		UpdatedAt:   base.Add(2 * time.Hour),
	}
	store := &fakeStore{
		tasks: []domain.Task{local},
		refs: []issues.ExternalRef{{
			Provider:           ProviderLinear,
			IssueID:            "CHE-1",
			ExternalID:         "lin-1",
			ExternalIdentifier: "CHE-1",
			LastSyncHash:       issues.HashTaskForSync(original),
			LastSyncPayload:    encodeSyncPayload(syncPayloadFromTask(original)),
		}},
	}
	linear := &fakeLinear{issues: []linearapi.Issue{remote}}
	service := Service{
		Store:     store,
		Linear:    linear,
		Config:    config.IssueTrackerConfig{Backend: "linear", Sync: config.IssueSyncConfig{Enabled: true}, Linear: config.LinearTrackerConfig{Team: "CHE"}},
		ProjectID: "chefy",
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Conflicts != 0 || summary.PushedRemote != 0 || summary.UpdatedLocal != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if got := store.tasks[0].Title; got != "Linear title" {
		t.Fatalf("local title = %q, want Linear title", got)
	}
	if len(linear.updates) != 0 {
		t.Fatalf("updates = %+v, want none because Linear won LWW", linear.updates)
	}
	if len(store.conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", store.conflicts)
	}
}

func TestServiceRunRecordsConflictForSameFieldWhenConfigured(t *testing.T) {
	base := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	original := domain.Task{
		ID:          mustIssueID(t, "CHE-1"),
		Title:       "Original title",
		Description: "Original body",
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
		CreatedAt:   base,
		UpdatedAt:   base,
	}
	local := original
	local.Title = "Local title"
	local.UpdatedAt = base.Add(time.Hour)
	remote := linearapi.Issue{
		ID:          "lin-1",
		Identifier:  "CHE-1",
		Title:       "Linear title",
		Description: "Original body",
		Priority:    3,
		State:       linearapi.State{Name: "Todo", Type: "unstarted"},
		CreatedAt:   base,
		UpdatedAt:   base.Add(2 * time.Hour),
	}
	store := &fakeStore{
		tasks: []domain.Task{local},
		refs: []issues.ExternalRef{{
			Provider:           ProviderLinear,
			IssueID:            "CHE-1",
			ExternalID:         "lin-1",
			ExternalIdentifier: "CHE-1",
			LastSyncHash:       issues.HashTaskForSync(original),
			LastSyncPayload:    encodeSyncPayload(syncPayloadFromTask(original)),
		}},
	}
	linear := &fakeLinear{issues: []linearapi.Issue{remote}}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear:  config.LinearTrackerConfig{Team: "CHE", ConflictPolicy: conflictPolicyConflict},
		},
		ProjectID: "chefy",
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Conflicts != 1 || summary.PushedRemote != 0 || summary.UpdatedLocal != 0 || summary.PendingPushes != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	conflict := store.conflicts[0]
	if conflict.Field != "title" || conflict.LocalValue != "Local title" || conflict.RemoteValue != "Linear title" {
		t.Fatalf("conflict = %+v", conflict)
	}
}

func TestServiceRunResolvesMeAssigneeFilter(t *testing.T) {
	linear := &fakeLinear{viewerID: "usr_viewer"}
	service := Service{
		Store:  &fakeStore{},
		Linear: linear,
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear: config.LinearTrackerConfig{
				Team:    "CHE",
				Project: "Chefy",
				Filter:  &config.LinearFilterConfig{Assignee: "me"},
			},
		},
		ProjectID: "chefy",
	}

	if _, err := service.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if linear.viewerCalls != 1 {
		t.Fatalf("viewer calls = %d, want 1", linear.viewerCalls)
	}
	if got := linear.lastOptions.AssigneeID; got != "usr_viewer" {
		t.Fatalf("assignee id = %q, want usr_viewer", got)
	}
}

func TestServiceRunPassesExplicitAssigneeFilter(t *testing.T) {
	linear := &fakeLinear{}
	service := Service{
		Store:  &fakeStore{},
		Linear: linear,
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear: config.LinearTrackerConfig{
				Team:    "CHE",
				Project: "Chefy",
				Filter:  &config.LinearFilterConfig{Assignee: "usr_123"},
			},
		},
		ProjectID: "chefy",
	}

	if _, err := service.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if linear.viewerCalls != 0 {
		t.Fatalf("viewer calls = %d, want 0", linear.viewerCalls)
	}
	if got := linear.lastOptions.AssigneeID; got != "usr_123" {
		t.Fatalf("assignee id = %q, want usr_123", got)
	}
}

func TestServiceRunSkipsPushForOutOfScopeRef(t *testing.T) {
	remoteUpdated := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	local := domain.Task{
		ID:          mustIssueID(t, "CHE-1"),
		Title:       "Local changed",
		Description: "Body",
		Priority:    domain.P2,
		Status:      domain.StatusOpen,
		UpdatedAt:   remoteUpdated.Add(time.Hour),
	}
	store := &fakeStore{
		tasks: []domain.Task{local},
		refs: []issues.ExternalRef{{
			Provider:     ProviderLinear,
			IssueID:      "CHE-1",
			ExternalID:   "lin-away",
			LastSyncHash: "previous",
		}},
	}
	linear := &fakeLinear{
		issues: []linearapi.Issue{{
			ID:         "lin-in-scope",
			Identifier: "CHE-2",
			Title:      "In scope",
			Priority:   2,
			State:      linearapi.State{Name: "Todo", Type: "unstarted"},
			CreatedAt:  remoteUpdated,
			UpdatedAt:  remoteUpdated,
		}},
	}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear: config.LinearTrackerConfig{
				Team:   "CHE",
				Filter: &config.LinearFilterConfig{Assignee: "usr_123"},
			},
		},
		ProjectID: "chefy",
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(linear.updates) != 0 {
		t.Fatalf("updates = %+v, want none", linear.updates)
	}
	if summary.SkippedPushOutOfScope != 1 || summary.OutOfScopeRefs != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestServiceRunUsesIncrementalCursor(t *testing.T) {
	cursor := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	linear := &fakeLinear{}
	store := &fakeStore{states: map[string]issues.ExternalSyncState{}}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear:  config.LinearTrackerConfig{Team: "CHE", Filter: &config.LinearFilterConfig{Assignee: "usr_123"}},
		},
		ProjectID: "chefy",
		Now:       func() time.Time { return cursor.Add(time.Hour) },
	}
	opts, err := service.listOptions(context.Background())
	if err != nil {
		t.Fatalf("listOptions() error = %v", err)
	}
	stateID := service.syncStateID(opts)
	store.states[ProviderLinear+"::"+stateID] = issues.ExternalSyncState{
		Provider:  ProviderLinear,
		ProjectID: stateID,
		Cursor:    cursor.Format(time.RFC3339Nano),
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !summary.Incremental {
		t.Fatalf("summary = %+v, want incremental", summary)
	}
	if linear.lastOptions.UpdatedAfter == nil {
		t.Fatalf("UpdatedAfter missing from options: %+v", linear.lastOptions)
	}
	if got, want := linear.lastOptions.UpdatedAfter.UTC(), cursor.Add(-time.Second); !got.Equal(want) {
		t.Fatalf("UpdatedAfter = %s, want %s", got, want)
	}
}

func TestServiceRunFetchesFullScopeBeforeIncrementalPush(t *testing.T) {
	cursor := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	local := domain.Task{ID: mustIssueID(t, "CHE-1"), Title: "Changed", Priority: domain.P2, Status: domain.StatusOpen, UpdatedAt: cursor.Add(time.Hour)}
	store := &fakeStore{
		tasks:  []domain.Task{local},
		states: map[string]issues.ExternalSyncState{},
	}
	remoteIssue := linearapi.Issue{
		ID:         "lin-1",
		Identifier: "CHE-1",
		Title:      "Remote",
		Priority:   2,
		State:      linearapi.State{Name: "Todo", Type: "unstarted"},
		CreatedAt:  cursor,
		UpdatedAt:  cursor,
	}
	remoteTask, ok := taskFromLinear(remoteIssue)
	if !ok {
		t.Fatal("remote issue did not map to task")
	}
	store.refs = []issues.ExternalRef{{
		Provider:     ProviderLinear,
		IssueID:      "CHE-1",
		ExternalID:   "lin-1",
		LastSyncHash: issues.HashTaskForSync(remoteTask),
	}}
	linear := &fakeLinear{issues: []linearapi.Issue{remoteIssue}}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{
			Backend: "linear",
			Sync:    config.IssueSyncConfig{Enabled: true},
			Linear:  config.LinearTrackerConfig{Team: "CHE", Filter: &config.LinearFilterConfig{Assignee: "usr_123"}},
		},
		ProjectID: "chefy",
	}
	opts, err := service.listOptions(context.Background())
	if err != nil {
		t.Fatalf("listOptions() error = %v", err)
	}
	stateID := service.syncStateID(opts)
	store.states[ProviderLinear+"::"+stateID] = issues.ExternalSyncState{Provider: ProviderLinear, ProjectID: stateID, Cursor: cursor.Format(time.RFC3339Nano)}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(linear.listOptions) != 2 {
		t.Fatalf("list calls = %d, want incremental plus full scope", len(linear.listOptions))
	}
	if linear.listOptions[0].UpdatedAfter == nil {
		t.Fatalf("first list should be incremental: %+v", linear.listOptions[0])
	}
	if linear.listOptions[1].UpdatedAfter != nil {
		t.Fatalf("second list should be full scope: %+v", linear.listOptions[1])
	}
	if summary.RemoteScopeIssues != 1 || summary.PushedRemote != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestServiceRunCapsPushesPerRun(t *testing.T) {
	store := &fakeStore{}
	linear := &fakeLinear{}
	for i := 0; i < defaultMaxPushesPerRun+2; i++ {
		id := mustIssueID(t, fmt.Sprintf("CHE-%d", i+1))
		externalID := "lin-" + id.String()
		store.tasks = append(store.tasks, domain.Task{ID: id, Title: "Changed", Priority: domain.P2, Status: domain.StatusOpen, UpdatedAt: time.Now().UTC()})
		remoteIssue := linearapi.Issue{ID: externalID, Identifier: id.String(), Title: "Remote", Priority: 2, State: linearapi.State{Name: "Todo", Type: "unstarted"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		remoteTask, ok := taskFromLinear(remoteIssue)
		if !ok {
			t.Fatalf("remote issue %s did not map to task", id)
		}
		store.refs = append(store.refs, issues.ExternalRef{Provider: ProviderLinear, IssueID: id.String(), ExternalID: externalID, LastSyncHash: issues.HashTaskForSync(remoteTask)})
		linear.issues = append(linear.issues, remoteIssue)
	}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{Backend: "linear", Sync: config.IssueSyncConfig{Enabled: true}, Linear: config.LinearTrackerConfig{Team: "CHE"}},
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.PushedRemote != defaultMaxPushesPerRun || !summary.PushBudgetExhausted {
		t.Fatalf("summary = %+v", summary)
	}
	if got, want := linear.updateBatch, []int{25}; !reflect.DeepEqual(got, want) {
		t.Fatalf("update batches = %v, want %v", got, want)
	}
}

func TestServiceRunStopsPushesWhenRateLimitBudgetIsLow(t *testing.T) {
	cursor := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	local := domain.Task{ID: mustIssueID(t, "CHE-1"), Title: "Changed", Priority: domain.P2, Status: domain.StatusOpen, UpdatedAt: cursor.Add(time.Hour)}
	remoteIssue := linearapi.Issue{ID: "lin-1", Identifier: "CHE-1", Title: "Remote", Priority: 2, State: linearapi.State{Name: "Todo", Type: "unstarted"}, CreatedAt: cursor, UpdatedAt: cursor}
	remoteTask, ok := taskFromLinear(remoteIssue)
	if !ok {
		t.Fatal("remote issue did not map to task")
	}
	store := &fakeStore{
		tasks: []domain.Task{local},
		refs:  []issues.ExternalRef{{Provider: ProviderLinear, IssueID: "CHE-1", ExternalID: "lin-1", LastSyncHash: issues.HashTaskForSync(remoteTask)}},
	}
	linear := &fakeLinear{
		issues:  []linearapi.Issue{remoteIssue},
		metrics: linearapi.Metrics{RateLimit: linearapi.RateLimit{Remaining: minRemainingRequests}},
	}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{Backend: "linear", Sync: config.IssueSyncConfig{Enabled: true}, Linear: config.LinearTrackerConfig{Team: "CHE"}},
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(linear.updates) != 0 || !summary.PushBudgetExhausted {
		t.Fatalf("updates=%d summary=%+v, want no pushes and exhausted budget", len(linear.updates), summary)
	}
}

func TestServiceRunRetriesRetryablePush(t *testing.T) {
	cursor := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	local := domain.Task{ID: mustIssueID(t, "CHE-1"), Title: "Changed", Priority: domain.P2, Status: domain.StatusOpen, UpdatedAt: cursor.Add(time.Hour)}
	store := &fakeStore{
		tasks: []domain.Task{local},
	}
	remoteIssue := linearapi.Issue{ID: "lin-1", Identifier: "CHE-1", Title: "Remote", Priority: 2, State: linearapi.State{Name: "Todo", Type: "unstarted"}, CreatedAt: cursor, UpdatedAt: cursor}
	remoteTask, ok := taskFromLinear(remoteIssue)
	if !ok {
		t.Fatal("remote issue did not map to task")
	}
	store.refs = []issues.ExternalRef{{Provider: ProviderLinear, IssueID: "CHE-1", ExternalID: "lin-1", LastSyncHash: issues.HashTaskForSync(remoteTask)}}
	linear := &fakeLinear{
		issues:     []linearapi.Issue{remoteIssue},
		updateErrs: []error{&linearapi.APIError{StatusCode: http.StatusTooManyRequests, Body: "rate limited"}, nil},
	}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{Backend: "linear", Sync: config.IssueSyncConfig{Enabled: true}, Linear: config.LinearTrackerConfig{Team: "CHE"}},
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.RetriedRequests != 1 || summary.PushedRemote != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestServiceRunDoesNotRetryNonRetryablePush(t *testing.T) {
	cursor := time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC)
	local := domain.Task{ID: mustIssueID(t, "CHE-1"), Title: "Changed", Priority: domain.P2, Status: domain.StatusOpen, UpdatedAt: cursor.Add(time.Hour)}
	store := &fakeStore{
		tasks: []domain.Task{local},
	}
	remoteIssue := linearapi.Issue{ID: "lin-1", Identifier: "CHE-1", Title: "Remote", Priority: 2, State: linearapi.State{Name: "Todo", Type: "unstarted"}, CreatedAt: cursor, UpdatedAt: cursor}
	remoteTask, ok := taskFromLinear(remoteIssue)
	if !ok {
		t.Fatal("remote issue did not map to task")
	}
	store.refs = []issues.ExternalRef{{Provider: ProviderLinear, IssueID: "CHE-1", ExternalID: "lin-1", LastSyncHash: issues.HashTaskForSync(remoteTask)}}
	linear := &fakeLinear{
		issues:     []linearapi.Issue{remoteIssue},
		updateErrs: []error{errors.New("bad request")},
	}
	service := Service{
		Store:  store,
		Linear: linear,
		Config: config.IssueTrackerConfig{Backend: "linear", Sync: config.IssueSyncConfig{Enabled: true}, Linear: config.LinearTrackerConfig{Team: "CHE"}},
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("sleep should not be called for non-retryable error")
			return nil
		},
	}

	_, err := service.Run(context.Background())
	if err == nil {
		t.Fatal("expected push error")
	}
	if len(linear.updates) != 1 {
		t.Fatalf("updates = %d, want exactly one attempt", len(linear.updates))
	}
}

type fakeLinear struct {
	issues      []linearapi.Issue
	updates     []linearapi.IssueInput
	updateIDs   []string
	updateBatch []int
	updateErrs  []error
	lastTeam    string
	lastProject string
	lastOptions linearapi.ListIssuesOptions
	listOptions []linearapi.ListIssuesOptions
	viewerID    string
	viewerCalls int
	metrics     linearapi.Metrics
}

func (f *fakeLinear) ListIssues(_ context.Context, opts linearapi.ListIssuesOptions) ([]linearapi.Issue, error) {
	f.lastOptions = opts
	f.listOptions = append(f.listOptions, opts)
	f.lastTeam = opts.TeamKey
	f.lastProject = opts.Project
	issues := make([]linearapi.Issue, 0, len(f.issues))
	for _, issue := range f.issues {
		if opts.UpdatedAfter != nil && !issue.UpdatedAt.After(*opts.UpdatedAfter) {
			continue
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func (f *fakeLinear) ViewerID(context.Context) (string, error) {
	f.viewerCalls++
	return f.viewerID, nil
}

func (f *fakeLinear) UpdateIssues(_ context.Context, requests []linearapi.IssueUpdateRequest) ([]linearapi.Issue, error) {
	f.updateBatch = append(f.updateBatch, len(requests))
	updated := make([]linearapi.Issue, 0, len(requests))
	for _, req := range requests {
		f.updateIDs = append(f.updateIDs, req.ID)
		f.updates = append(f.updates, req.Input)
	}
	if len(f.updateErrs) > 0 {
		err := f.updateErrs[0]
		f.updateErrs = f.updateErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	for _, req := range requests {
		updated = append(updated, f.updatedIssue(req.ID, req.Input))
	}
	return updated, nil
}

func (f *fakeLinear) updatedIssue(id string, input linearapi.IssueInput) linearapi.Issue {
	for _, issue := range f.issues {
		if issue.ID == id {
			return issue
		}
	}
	return linearapi.Issue{ID: id, Identifier: id, Title: input.Title, UpdatedAt: time.Now().UTC()}
}

func (f *fakeLinear) Metrics() linearapi.Metrics {
	return f.metrics
}

type fakeStore struct {
	tasks             []domain.Task
	refs              []issues.ExternalRef
	conflicts         []issues.SyncConflict
	states            map[string]issues.ExternalSyncState
	upsertSyncedCalls int
}

func (s *fakeStore) List(context.Context) ([]domain.Task, error) {
	return append([]domain.Task(nil), s.tasks...), nil
}

func (s *fakeStore) UpsertSyncedTask(_ context.Context, task domain.Task) (bool, error) {
	s.upsertSyncedCalls++
	for i, existing := range s.tasks {
		if existing.ID == task.ID {
			s.tasks[i] = task
			return true, nil
		}
	}
	s.tasks = append(s.tasks, task)
	return true, nil
}

func (s *fakeStore) UpsertExternalRef(_ context.Context, ref issues.ExternalRef) error {
	for i, existing := range s.refs {
		if existing.IssueID == ref.IssueID {
			s.refs[i] = ref
			return nil
		}
	}
	s.refs = append(s.refs, ref)
	return nil
}

func (s *fakeStore) ListExternalRefs(context.Context, string) ([]issues.ExternalRef, error) {
	return append([]issues.ExternalRef(nil), s.refs...), nil
}

func (s *fakeStore) GetExternalSyncState(_ context.Context, provider, projectID string) (issues.ExternalSyncState, bool, error) {
	if s.states == nil {
		return issues.ExternalSyncState{}, false, nil
	}
	state, ok := s.states[provider+"::"+projectID]
	return state, ok, nil
}

func (s *fakeStore) UpsertExternalSyncState(_ context.Context, state issues.ExternalSyncState) error {
	if s.states == nil {
		s.states = map[string]issues.ExternalSyncState{}
	}
	s.states[state.Provider+"::"+state.ProjectID] = state
	return nil
}

func (s *fakeStore) RecordSyncConflict(_ context.Context, conflict issues.SyncConflict) error {
	s.conflicts = append(s.conflicts, conflict)
	return nil
}

func (s *fakeStore) ListSyncConflicts(context.Context, string, string, bool) ([]issues.SyncConflict, error) {
	return append([]issues.SyncConflict(nil), s.conflicts...), nil
}

func mustIssueID(t *testing.T, raw string) naming.IssueID {
	t.Helper()
	id, err := naming.ParseIssueID(raw)
	if err != nil {
		t.Fatalf("ParseIssueID(%q) error = %v", raw, err)
	}
	return id
}
