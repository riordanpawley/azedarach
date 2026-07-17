package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func TestApplyGlobalBoardSnapshotScopesDuplicateIssueIDs(t *testing.T) {
	m := New(config.DefaultConfig())
	m.projectOrchestrator = &projectOrchestratorSnapshot{Name: "stale-project"}
	view := domain.DefaultBoardView()
	group := view.Columns[0].ID
	snapshot := protocol.GlobalSnapshotResponseBody{
		Projects: []protocol.GlobalProjectSnapshot{
			{ProjectID: "alpha", Name: "Alpha", Path: "/projects/alpha"},
			{ProjectID: "beta", Name: "Beta", Path: "/projects/beta"},
		},
		Projection: protocol.GlobalViewProjection{
			View: view,
			ChildProgress: []protocol.GlobalViewChildProgress{{
				ParentID: protocol.ScopedIssueID{ProjectID: "alpha", IssueID: "ddm"}, Done: 3, Total: 4,
			}},
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
	if m.projectOrchestrator != nil {
		t.Fatal("global scope retained project orchestrator chrome")
	}
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
	if got := m.boardProjection.ChildProgress; len(got) != 1 || got[0].ParentID != "alpha::ddm" || got[0].Done != 3 || got[0].Total != 4 {
		t.Fatalf("scoped child progress = %+v", got)
	}
}

func TestGlobalBoardActionHydratesOwningProjectBeforeMutation(t *testing.T) {
	m := New(config.DefaultConfig())
	m.scope = globalTUIScope()
	m.tasks = []domain.Task{{ID: "alpha::ddm", Title: "Issue"}}
	m.boardView = domain.DefaultBoardView()
	m.boardColumns = []domain.BoardViewColumnSnapshot{{Definition: m.boardView.Columns[0], Tasks: m.tasks}}
	m.globalTaskScopes = map[string]protocol.ScopedIssueID{"alpha::ddm": {ProjectID: "alpha", IssueID: "ddm"}}
	m.globalTaskProjects = map[string]config.Project{"alpha::ddm": {Name: "Alpha", Path: "/projects/alpha"}}
	m.nav.SelectTask("alpha::ddm", 0)

	next, cmd := m.leaveGlobalBoardForCurrentTask()
	got := next.(Model)
	if got.scope.IsGlobal() || !got.projectSwitchInFlight || cmd == nil || got.pendingUIOpenTaskID != "ddm" {
		t.Fatalf("route state global=%v switching=%v cmd=%v", got.scope.IsGlobal(), got.projectSwitchInFlight, cmd != nil)
	}
}

func TestGlobalBoardRefreshSchedulesProjectionReload(t *testing.T) {
	m := New(config.DefaultConfig())
	m.scope = globalTUIScope()
	next, cmd := m.Update(tickMsg{})
	got := next.(Model)
	if !got.boardRefreshing || cmd == nil {
		t.Fatalf("global refresh state refreshing=%v cmd=%v", got.boardRefreshing, cmd != nil)
	}
}

func TestGlobalBoardIgnoresStaleReplyAfterLeaving(t *testing.T) {
	m := New(config.DefaultConfig())
	m.scope = projectTUIScope()
	m.loading = true
	next, _ := m.Update(globalBoardLoadedMsg{seq: 1, err: context.DeadlineExceeded})
	got := next.(Model)
	if !got.loading || len(got.toasts) != 0 {
		t.Fatalf("stale reply mutated model loading=%v toasts=%v", got.loading, got.toasts)
	}
}

func TestGlobalBoardGotoSpecRequiresOwningProject(t *testing.T) {
	m := New(config.DefaultConfig())
	m.scope = globalTUIScope()
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

func TestGlobalBoardViewKeysStayInGlobalScope(t *testing.T) {
	for _, tc := range []struct {
		key      tea.KeyMsg
		wantCmd  bool
		wantTree bool
	}{
		{key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}}, wantCmd: true},
		{key: tea.KeyMsg{Type: tea.KeyTab}, wantCmd: true},
		{key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}, wantCmd: true, wantTree: true},
	} {
		m := New(config.DefaultConfig())
		m.scope = globalTUIScope()
		m.daemonClient = nil
		next, cmd := m.handleNormalMode(tc.key)
		got := next.(Model)
		if (cmd != nil) != tc.wantCmd || !got.scope.IsGlobal() || got.projectSwitchInFlight {
			t.Fatalf("key=%q scope=%+v switching=%v cmd=%v", tc.key.String(), got.scope, got.projectSwitchInFlight, cmd != nil)
		}
		if tc.wantTree {
			if !got.sessionTreeFilterOnly {
				t.Fatalf("key=%q did not toggle projection-local filter", tc.key.String())
			}
			continue
		}
		switch msg := cmd().(type) {
		case boardViewsLoadedMsg:
			if !msg.scope.global {
				t.Fatalf("key=%q loaded project views", tc.key.String())
			}
		case boardViewSelectedMsg:
			if !msg.scope.global {
				t.Fatalf("key=%q selected project view", tc.key.String())
			}
		default:
			t.Fatalf("key=%q command message=%T", tc.key.String(), msg)
		}
	}
}

func TestGlobalScopeBoardViewCommandsUseRootViewContracts(t *testing.T) {
	view := domain.DefaultBoardView()
	view.ID, view.Title = "custom", "Custom"
	scope := protocol.GlobalViewScope{Kind: protocol.GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"alpha"}}
	transport := &recordingDaemonTransport{replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		response := protocol.ResponseEnvelope{OK: true, RequestID: req.RequestID, Meta: req.Meta}
		switch req.Command {
		case protocol.CommandBoardViewList:
			var body protocol.BoardViewListRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil || body.ProjectID != "global" {
				t.Fatalf("list body=%+v err=%v", body, err)
			}
			response.Body, _ = json.Marshal(protocol.BoardViewListResponseBody{ProjectID: "global", SelectedViewID: "custom", GlobalViews: []protocol.GlobalViewRecord{{View: view, Scope: scope}}})
		case protocol.CommandBoardViewSelect:
			var body protocol.BoardViewSelectRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil || body.ProjectID != "global" || body.Consumer != protocol.GlobalViewConsumerBoard || body.ViewID != "custom" {
				t.Fatalf("select body=%+v err=%v", body, err)
			}
			response.Body, _ = json.Marshal(protocol.BoardViewSelectResponseBody{ProjectID: "global", ViewID: body.ViewID})
		case protocol.CommandBoardViewSave:
			var body protocol.BoardViewSaveRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil || body.ProjectID != "global" || body.Scope.Kind != protocol.GlobalViewScopeSelectedProjects {
				t.Fatalf("save body=%+v err=%v", body, err)
			}
			response.Body, _ = json.Marshal(protocol.BoardViewResponseBody{ProjectID: "global", View: domain.BoardViewRecord{ProjectID: "global", View: body.View}})
		case protocol.CommandBoardViewDelete:
			var body protocol.BoardViewDeleteRequestBody
			if err := json.Unmarshal(req.Body, &body); err != nil || body.ProjectID != "global" || body.ViewID != "custom" {
				t.Fatalf("delete body=%+v err=%v", body, err)
			}
		default:
			t.Fatalf("unexpected command %q", req.Command)
		}
		return response, nil
	}}
	m := New(config.DefaultConfig())
	m.scope = globalTUIScope()
	m.daemonClient = daemonclient.New(transport).WithProjectID("project-local")

	loaded := m.loadBoardViewsCmd()().(boardViewsLoadedMsg)
	if loaded.err != nil || !loaded.scope.global || len(loaded.globalViews) != 1 || loaded.globalViews[0].Scope.Kind != scope.Kind {
		t.Fatalf("loaded = %+v", loaded)
	}
	selected := m.selectBoardViewCmd("custom")().(boardViewSelectedMsg)
	if selected.err != nil || !selected.scope.global || selected.viewID != "custom" {
		t.Fatalf("selected = %+v", selected)
	}
	mutated := m.saveBoardViewCmd(view, scope)().(boardViewMutatedMsg)
	if mutated.err != nil || !mutated.scope.global || mutated.viewID != "custom" {
		t.Fatalf("saved = %+v", mutated)
	}
	deleted := m.deleteBoardViewCmd("custom")().(boardViewMutatedMsg)
	if deleted.err != nil || !deleted.scope.global {
		t.Fatalf("deleted = %+v", deleted)
	}
}

func TestBoardViewCallbacksRejectStaleProjectIdentity(t *testing.T) {
	base := New(config.DefaultConfig())
	base.scope = projectTUIScope()
	base.currentProject = "project-a"
	staleScope := base.currentBoardViewCommandScope()
	base.currentProject = "project-b"
	base.selectedBoardViewID = "current"
	base.boardViews = []domain.BoardViewRecord{{View: domain.DefaultBoardView()}}

	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "list", msg: boardViewsLoadedMsg{scope: staleScope, selectedViewID: "stale"}},
		{name: "select", msg: boardViewSelectedMsg{scope: staleScope, viewID: "stale"}},
		{name: "save", msg: boardViewMutatedMsg{scope: staleScope, action: "save", viewID: "stale"}},
		{name: "delete", msg: boardViewMutatedMsg{scope: staleScope, action: "delete", viewID: "stale"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			next, cmd := m.Update(tc.msg)
			got := next.(Model)
			if cmd != nil || got.selectedBoardViewID != "current" || len(got.toasts) != 0 || got.overlayStack.Current() != nil {
				t.Fatalf("stale callback mutated project B: selected=%q toasts=%v overlay=%T cmd=%v", got.selectedBoardViewID, got.toasts, got.overlayStack.Current(), cmd != nil)
			}
		})
	}
}

func TestBoardViewCallbacksRejectPriorGlobalGeneration(t *testing.T) {
	m := New(config.DefaultConfig())
	m.scope = globalTUIScope()
	m.selectedBoardViewID = "current"
	staleScope := m.currentBoardViewCommandScope()
	m.beginBoardViewScopeTransition()
	m.beginBoardViewScopeTransition()

	next, cmd := m.Update(boardViewSelectedMsg{scope: staleScope, viewID: "stale"})
	got := next.(Model)
	if cmd != nil || got.selectedBoardViewID != "current" || len(got.toasts) != 0 {
		t.Fatalf("prior Global generation was accepted: selected=%q toasts=%v cmd=%v", got.selectedBoardViewID, got.toasts, cmd != nil)
	}
}

func TestScopeSelectionGlobalIsAuthoritativeAndStopsProjectEvents(t *testing.T) {
	m := New(config.DefaultConfig())
	m.scope = projectTUIScope()
	cancelled := false
	m.daemonEvents = make(chan protocol.EventEnvelope)
	m.daemonEventsCancel = func() { cancelled = true }
	next, cmd := m.Update(overlay.ScopeSelectedMsg{Global: true})
	got := next.(Model)
	if cmd == nil || got.scope.IsGlobal() || !got.globalScopeSwitchPending || !got.projectSwitchInFlight || cancelled || got.daemonEvents == nil {
		t.Fatalf("scope=%+v pending=%v switching=%v cancelled=%v events=%v cmd=%v", got.scope, got.globalScopeSwitchPending, got.projectSwitchInFlight, cancelled, got.daemonEvents, cmd != nil)
	}
	loaded, _ := got.Update(globalBoardLoadedMsg{seq: got.globalLoadSeq, snapshot: protocol.GlobalSnapshotResponseBody{Projection: protocol.GlobalViewProjection{View: domain.DefaultBoardView()}}})
	loadedModel := loaded.(Model)
	if loadedModel.projectSwitchInFlight || !loadedModel.scope.IsGlobal() || !cancelled || loadedModel.daemonEvents != nil {
		t.Fatalf("loaded scope=%+v switching=%v cancelled=%v events=%v", loadedModel.scope, loadedModel.projectSwitchInFlight, cancelled, loadedModel.daemonEvents)
	}
}

func TestScopeSelectionGlobalFailureRetainsProjectScopeAndStream(t *testing.T) {
	m := New(config.DefaultConfig())
	m.currentProject = "alpha"
	m.daemonEvents = make(chan protocol.EventEnvelope)
	next, _ := m.Update(overlay.ScopeSelectedMsg{Global: true})
	pending := next.(Model)
	next, _ = pending.Update(globalBoardLoadedMsg{seq: pending.globalLoadSeq, err: context.DeadlineExceeded})
	got := next.(Model)
	if got.scope.IsGlobal() || got.globalScopeSwitchPending || got.projectSwitchInFlight || got.daemonEvents == nil {
		t.Fatalf("scope=%+v pending=%v switching=%v events=%v", got.scope, got.globalScopeSwitchPending, got.projectSwitchInFlight, got.daemonEvents)
	}
}

func TestScopeSelectionProjectLeavesGlobalThroughProjectSwitch(t *testing.T) {
	m := New(config.DefaultConfig())
	m.scope = globalTUIScope()
	next, cmd := m.Update(overlay.ScopeSelectedMsg{Project: config.Project{Name: "alpha", Path: "/work/alpha"}})
	got := next.(Model)
	if cmd == nil || got.scope.IsGlobal() || !got.projectSwitchFromGlobal || !got.projectSwitchInFlight {
		t.Fatalf("scope=%+v fromGlobal=%v switching=%v cmd=%v", got.scope, got.projectSwitchFromGlobal, got.projectSwitchInFlight, cmd != nil)
	}
}

func TestGlobalScopeRejectsLateProjectSnapshot(t *testing.T) {
	m := New(config.DefaultConfig())
	m.scope = globalTUIScope()
	m.tasks = []domain.Task{{ID: "alpha::dep", Title: "Global item"}}
	cancelled := false
	next, cmd := m.Update(issuesLoadedMsg{
		projectID: "alpha",
		tasks:     []domain.Task{{ID: "dep", Title: "Late project item"}},
		eventsCancel: func() {
			cancelled = true
		},
	})
	got := next.(Model)
	if cmd != nil || !cancelled || len(got.tasks) != 1 || got.tasks[0].ID != "alpha::dep" {
		t.Fatalf("cmd=%v cancelled=%v tasks=%+v", cmd != nil, cancelled, got.tasks)
	}
}
