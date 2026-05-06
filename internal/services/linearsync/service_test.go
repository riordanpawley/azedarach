package linearsync

import (
	"context"
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

type fakeLinear struct {
	issues      []linearapi.Issue
	updates     []linearapi.IssueInput
	lastTeam    string
	lastProject string
	lastOptions linearapi.ListIssuesOptions
	viewerID    string
	viewerCalls int
}

func (f *fakeLinear) ListIssues(_ context.Context, opts linearapi.ListIssuesOptions) ([]linearapi.Issue, error) {
	f.lastOptions = opts
	f.lastTeam = opts.TeamKey
	f.lastProject = opts.Project
	return append([]linearapi.Issue(nil), f.issues...), nil
}

func (f *fakeLinear) ViewerID(context.Context) (string, error) {
	f.viewerCalls++
	return f.viewerID, nil
}

func (f *fakeLinear) UpdateIssue(_ context.Context, id string, input linearapi.IssueInput) (linearapi.Issue, error) {
	f.updates = append(f.updates, input)
	for _, issue := range f.issues {
		if issue.ID == id {
			return issue, nil
		}
	}
	return linearapi.Issue{ID: id, Identifier: id, Title: input.Title, UpdatedAt: time.Now().UTC()}, nil
}

type fakeStore struct {
	tasks     []domain.Task
	refs      []issues.ExternalRef
	conflicts []issues.SyncConflict
}

func (s *fakeStore) List(context.Context) ([]domain.Task, error) {
	return append([]domain.Task(nil), s.tasks...), nil
}

func (s *fakeStore) UpsertSyncedTask(_ context.Context, task domain.Task) (bool, error) {
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
