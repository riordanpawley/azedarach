package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestGlobalBoardViewCommandsReturnUnavailableWithoutUserStore(t *testing.T) {
	d := &Daemon{}
	tests := []struct {
		name string
		body any
		call func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	}{
		{"get", protocol.BoardViewGetRequestBody{ProjectID: "global"}, d.handleBoardViewGet},
		{"save", protocol.BoardViewSaveRequestBody{ProjectID: "global", View: domain.DefaultBoardView()}, d.handleBoardViewSave},
		{"delete", protocol.BoardViewDeleteRequestBody{ProjectID: "global", ViewID: "custom"}, d.handleBoardViewDelete},
		{"select", protocol.BoardViewSelectRequestBody{ProjectID: "global", ViewID: "default"}, d.handleBoardViewSelect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := tt.call(context.Background(), protocol.RequestEnvelope{Body: raw})
			if err != nil {
				t.Fatal(err)
			}
			if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeUnavailable {
				t.Fatalf("response = %+v, want unavailable", resp)
			}
		})
	}
}

func TestCommandMutatesProjectProjectionIsExplicit(t *testing.T) {
	for _, command := range []string{"task.create", "session.stop", protocol.CommandInteractionAnswer, protocol.CommandRuntimeReconcile} {
		if !commandMutatesProjectProjection(command) {
			t.Errorf("%s should trigger refresh", command)
		}
	}
	for _, command := range []string{"task.list", "task.complete_check", "session.status", protocol.CommandSessionCapture, protocol.CommandGlobalSnapshot, "task.unknown_future_command"} {
		if commandMutatesProjectProjection(command) {
			t.Errorf("%s should not trigger refresh", command)
		}
	}
}

func TestProjectGlobalViewPreservesCollidingScopedIssueIDs(t *testing.T) {
	now := time.Now().UTC()
	projects := []protocol.GlobalProjectSnapshot{{ProjectID: "p-a", Tasks: []domain.Task{{ID: "same", Title: "A", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}}, {ProjectID: "p-b", Tasks: []domain.Task{{ID: "same", Title: "B", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}}}}
	projection, err := projectGlobalView(domain.DefaultBoardView(), projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Items) != 2 {
		t.Fatalf("items=%d", len(projection.Items))
	}
	if projection.Items[0].Identity.ProjectID == projection.Items[1].Identity.ProjectID {
		t.Fatalf("identities=%+v", projection.Items)
	}
}

func TestProjectGlobalViewLeavesHydratedOutOfViewTaskOutOfOrdering(t *testing.T) {
	now := time.Now().UTC()
	projects := []protocol.GlobalProjectSnapshot{{ProjectID: "p", Tasks: []domain.Task{
		{ID: "excluded-live", Title: "Hydrated title", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug, CreatedAt: now, UpdatedAt: now},
		{ID: "active", Title: "Visible", Status: domain.StatusInProgress, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
	}}}
	projection, err := projectGlobalView(domain.OrchestrationBoardView(), projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Items) != 1 || projection.Items[0].Identity.IssueID != "active" {
		t.Fatalf("configured view ordering included hydrated fallback: %+v", projection.Items)
	}
	if len(projection.KnownTaskIDs) != 2 {
		t.Fatalf("known task IDs = %+v, want both durable tasks", projection.KnownTaskIDs)
	}
}

func TestProjectGlobalTreeViewPreservesBranchesAcrossProjects(t *testing.T) {
	now := time.Now().UTC()
	alphaRoot := naming.IssueID("alpha-root")
	betaRoot := naming.IssueID("beta-root")
	projects := []protocol.GlobalProjectSnapshot{
		{ProjectID: "alpha", Tasks: []domain.Task{
			{ID: alphaRoot, Title: "Ordinary root", Status: domain.StatusInProgress, Priority: domain.P0, UpdatedAt: now},
			{ID: "alpha-child", Title: "Human-waiting child", Status: domain.StatusInProgress, Priority: domain.P4, ParentID: &alphaRoot, Session: &domain.Session{Activity: "waiting-for-human"}, HasTmuxSession: true, UpdatedAt: now},
		}},
		{ProjectID: "beta", Tasks: []domain.Task{
			{ID: betaRoot, Title: "Review root", Status: domain.StatusInReview, Priority: domain.P4, Session: &domain.Session{Activity: string(domain.SessionIdle)}, HasTmuxSession: true, UpdatedAt: now},
			{ID: "beta-child", Title: "Review child", Status: domain.StatusInProgress, Priority: domain.P2, ParentID: &betaRoot, UpdatedAt: now},
		}},
	}

	projection, err := projectGlobalView(domain.TreeBoardView(), projects)
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.ScopedIssueID{
		{ProjectID: "beta", IssueID: betaRoot},
		{ProjectID: "beta", IssueID: "beta-child"},
		{ProjectID: "alpha", IssueID: alphaRoot},
		{ProjectID: "alpha", IssueID: "alpha-child"},
	}
	if len(projection.Items) != len(want) {
		t.Fatalf("items = %+v, want %d tree items", projection.Items, len(want))
	}
	for i, identity := range want {
		if projection.Items[i].Identity != identity {
			t.Fatalf("item identities = %+v, want %+v", projection.Items, want)
		}
		wantDepth := i % 2
		if projection.Items[i].Depth != wantDepth {
			t.Fatalf("item %s depth = %d, want %d", identity.IssueID, projection.Items[i].Depth, wantDepth)
		}
	}
}

func TestFilterGlobalProjectsUsesCanonicalScopedIdentity(t *testing.T) {
	projects := []protocol.GlobalProjectSnapshot{{ProjectID: "alpha"}, {ProjectID: "beta"}}
	selected := filterGlobalProjects(projects, protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"beta"}})
	if len(selected) != 1 || selected[0].ProjectID != "beta" {
		t.Fatalf("selected projects = %+v", selected)
	}
	current := filterGlobalProjects(projects, protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeCurrentProject, CurrentProjectID: "alpha"})
	if len(current) != 1 || current[0].ProjectID != "alpha" {
		t.Fatalf("current projects = %+v", current)
	}
}

func TestScopedViewKeepsHydratedOutsideScopeTaskKnownButUnprojected(t *testing.T) {
	projects := []protocol.GlobalProjectSnapshot{
		{ProjectID: "alpha", Tasks: []domain.Task{{ID: "same", Status: domain.StatusInProgress}}},
		{ProjectID: "beta", Tasks: []domain.Task{{ID: "same", Status: domain.StatusOpen}}},
	}
	scope := protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"alpha"}}
	projected := filterGlobalProjects(projects, scope)
	if len(projected) != 1 || projected[0].ProjectID != "alpha" {
		t.Fatalf("projection projects = %+v, want alpha only", projected)
	}
	projection, err := projectGlobalView(domain.OrchestrationBoardView(), projected)
	if err != nil {
		t.Fatal(err)
	}
	projection.KnownTaskIDs = augmentGlobalProjectionKnownTasks(projection.KnownTaskIDs, projects)
	if len(projection.Items) != 1 || projection.Items[0].Identity.ProjectID != "alpha" {
		t.Fatalf("scoped items = %+v, want alpha only", projection.Items)
	}
	wantKnown := []protocol.ScopedIssueID{{ProjectID: "alpha", IssueID: "same"}, {ProjectID: "beta", IssueID: "same"}}
	if len(projection.KnownTaskIDs) != len(wantKnown) {
		t.Fatalf("known identities = %+v, want %+v", projection.KnownTaskIDs, wantKnown)
	}
	for i := range wantKnown {
		if projection.KnownTaskIDs[i] != wantKnown[i] {
			t.Fatalf("known identities = %+v, want %+v", projection.KnownTaskIDs, wantKnown)
		}
	}
}
