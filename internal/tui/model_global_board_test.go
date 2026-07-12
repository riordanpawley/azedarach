package app

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestApplyGlobalBoardSnapshotScopesDuplicateIssueIDs(t *testing.T) {
	m := New(config.DefaultConfig())
	view := domain.DefaultBoardView()
	group := view.Columns[0].ID
	snapshot := protocol.GlobalSnapshotResponseBody{
		Projects: []protocol.GlobalProjectSnapshot{
			{ProjectID: "alpha", Name: "Alpha", Path: "/projects/alpha"},
			{ProjectID: "beta", Name: "Beta", Path: "/projects/beta"},
		},
		Projection: protocol.GlobalViewProjection{
			View: view,
			Groups: []protocol.GlobalViewProjectedGroup{{GroupID: group, TaskIDs: []protocol.ScopedIssueID{
				{ProjectID: "alpha", IssueID: "ddm"}, {ProjectID: "beta", IssueID: "ddm"},
			}}},
			Items: []protocol.GlobalViewProjectedItem{
				{Identity: protocol.ScopedIssueID{ProjectID: "alpha", IssueID: "ddm"}, Task: domain.Task{ID: "ddm", Title: "Alpha issue", Dependencies: []domain.Dependency{{ID: "dep", Type: domain.DependencyBlocks}}}, GroupID: group},
				{Identity: protocol.ScopedIssueID{ProjectID: "beta", IssueID: "ddm"}, Task: domain.Task{ID: "ddm", Title: "Beta issue"}, GroupID: group},
			},
		},
	}
	m.applyGlobalBoardSnapshot(snapshot)
	if len(m.tasks) != 2 || m.tasks[0].ID == m.tasks[1].ID {
		t.Fatalf("global tasks = %+v, want two scoped identities", m.tasks)
	}
	for _, want := range []naming.IssueID{"alpha::ddm", "beta::ddm"} {
		if _, ok := m.globalTaskScopes[want.String()]; !ok {
			t.Fatalf("missing scope for %s", want)
		}
	}
	if got := m.tasks[0].Dependencies[0].ID.String(); got != "alpha::dep" {
		t.Fatalf("scoped dependency = %q", got)
	}
}

func TestGlobalBoardActionHydratesOwningProjectBeforeMutation(t *testing.T) {
	m := New(config.DefaultConfig())
	m.globalBoard = true
	m.tasks = []domain.Task{{ID: "alpha::ddm", Title: "Issue"}}
	m.boardView = domain.DefaultBoardView()
	m.boardColumns = []domain.BoardViewColumnSnapshot{{Definition: m.boardView.Columns[0], Tasks: m.tasks}}
	m.globalTaskScopes = map[string]protocol.ScopedIssueID{"alpha::ddm": {ProjectID: "alpha", IssueID: "ddm"}}
	m.globalTaskProjects = map[string]config.Project{"alpha::ddm": {Name: "Alpha", Path: "/projects/alpha"}}
	m.nav.SelectTask("alpha::ddm", 0)

	next, cmd := m.leaveGlobalBoardForCurrentTask()
	got := next.(Model)
	if got.globalBoard || !got.projectSwitchInFlight || cmd == nil || got.pendingUIOpenTaskID != "ddm" {
		t.Fatalf("route state global=%v switching=%v cmd=%v", got.globalBoard, got.projectSwitchInFlight, cmd != nil)
	}
}

func TestGlobalBoardRefreshSchedulesProjectionReload(t *testing.T) {
	m := New(config.DefaultConfig())
	m.globalBoard = true
	next, cmd := m.Update(tickMsg{})
	got := next.(Model)
	if !got.boardRefreshing || cmd == nil {
		t.Fatalf("global refresh state refreshing=%v cmd=%v", got.boardRefreshing, cmd != nil)
	}
}

func TestGlobalBoardIgnoresStaleReplyAfterLeaving(t *testing.T) {
	m := New(config.DefaultConfig())
	m.globalBoard = false
	m.loading = true
	next, _ := m.Update(globalBoardLoadedMsg{seq: 1, err: context.DeadlineExceeded})
	got := next.(Model)
	if !got.loading || len(got.toasts) != 0 {
		t.Fatalf("stale reply mutated model loading=%v toasts=%v", got.loading, got.toasts)
	}
}

func TestGlobalBoardGotoSpecRequiresOwningProject(t *testing.T) {
	m := New(config.DefaultConfig())
	m.globalBoard = true
	m.editor.EnterGoto()

	next, cmd := m.handleGotoMode(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	got := next.(Model)
	if cmd != nil {
		t.Fatal("global goto-spec command should be fenced")
	}
	if got.overlayStack.Current() != nil {
		t.Fatal("global goto-spec opened a project-bound overlay")
	}
	if len(got.toasts) != 1 || got.toasts[0].Expires.Before(time.Now()) {
		t.Fatalf("global goto-spec toast = %+v", got.toasts)
	}
}
