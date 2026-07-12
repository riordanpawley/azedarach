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
