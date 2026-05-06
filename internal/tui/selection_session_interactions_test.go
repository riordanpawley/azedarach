package app

import (
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestIssuesLoadedPreservesSelectionCursorAndSessionProjection(t *testing.T) {
	startedAt := time.Date(2026, time.March, 25, 9, 30, 0, 0, time.UTC)
	devServer := &domain.DevServer{
		Port:    4242,
		Command: "npm run dev",
		Running: true,
	}
	sourceSession := &domain.Session{
		IssueID:   "az-2",
		State:     domain.SessionBusy,
		StartedAt: &startedAt,
		Worktree:  "/tmp/az-2",
		DevServer: devServer,
	}

	m := newTestModel()
	m.tasks[1].Session = sourceSession
	m.editor.Select("az-2")
	m.editor.Select("ghost")
	m.nav.SelectTask("az-2", 0)
	beforeTask, beforeSession := m.getCurrentTaskAndSession()
	if beforeTask == nil || beforeTask.ID != "az-2" {
		t.Fatalf("pre-refresh current task = %+v, want az-2", beforeTask)
	}
	if beforeSession == nil || beforeSession.Worktree != "/tmp/az-2" {
		t.Fatalf("pre-refresh current session = %+v, want az-2 session", beforeSession)
	}
	result, cmd := m.Update(issuesLoadedMsg{
		tasks: []domain.Task{
			{ID: "az-1", Title: "Task 1", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask},
			{ID: "az-2", Title: "Task 2", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug, Session: sourceSession},
			{ID: "az-3", Title: "Task 3", Status: domain.StatusInProgress, Priority: domain.P0, Type: domain.TypeFeature},
		},
		revision: 12,
	})
	if cmd == nil {
		t.Fatal("expected refresh command after issuesLoadedMsg")
	}

	newModel, ok := result.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", result)
	}

	if got := newModel.editor.SelectionCount(); got != 1 {
		t.Fatalf("selection count = %d, want 1", got)
	}
	if !newModel.editor.IsSelected("az-2") {
		t.Fatal("expected az-2 to remain selected after refresh")
	}
	if newModel.editor.IsSelected("ghost") {
		t.Fatal("expected ghost selection to be pruned after refresh")
	}

	task, session := newModel.getCurrentTaskAndSession()
	if task == nil || task.ID != beforeTask.ID {
		t.Fatalf("current task = %+v, want %s", task, beforeTask.ID)
	}
	if session == nil || session.Worktree != beforeSession.Worktree {
		t.Fatalf("current session = %+v, want projected %s session", session, beforeTask.ID)
	}

	projected := newModel.tasks[1].Session
	if projected == nil {
		t.Fatal("expected hydrated task session")
	}
	if projected == sourceSession {
		t.Fatal("hydrated task session should be cloned, not aliased")
	}
	if projected.StartedAt == nil || !projected.StartedAt.Equal(startedAt) {
		t.Fatalf("hydrated startedAt = %+v, want %v", projected.StartedAt, startedAt)
	}
	if projected.DevServer == nil || projected.DevServer == devServer {
		t.Fatalf("hydrated dev server = %+v, want cloned dev server", projected.DevServer)
	}
	if projected.DevServer.Port != devServer.Port || projected.DevServer.Command != devServer.Command || projected.DevServer.Running != devServer.Running {
		t.Fatalf("hydrated dev server = %+v, want %+v", projected.DevServer, devServer)
	}
}

func TestSessionLifecycleMessagesPreserveCurrentSelection(t *testing.T) {
	tests := []struct {
		name      string
		msg       any
		wantToast string
	}{
		{
			name:      "started",
			msg:       sessionStartedMsg{issueID: "az-1"},
			wantToast: "Session started: az-1",
		},
		{
			name:      "stopped",
			msg:       sessionStoppedMsg{issueID: "az-1"},
			wantToast: "Session stopped: az-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m.tasks[0].Session = &domain.Session{
				IssueID:  "az-1",
				State:    domain.SessionBusy,
				Worktree: "/tmp/az-1",
			}
			m.editor.Select("az-1")
			m.nav.SelectTask("az-1", 0)
			before := getCursorPosition(m)

			result, cmd := m.Update(tt.msg)
			if tt.name == "stopped" {
				if cmd == nil {
					t.Fatal("expected stopped lifecycle message to refresh daemon projection")
				}
			} else if cmd != nil {
				t.Fatalf("update command = %T, want nil", cmd)
			}

			newModel, ok := result.(Model)
			if !ok {
				t.Fatalf("updated model type = %T, want Model", result)
			}

			if got := getCursorPosition(newModel); got != before {
				t.Fatalf("cursor position = %+v, want %+v", got, before)
			}
			if got := newModel.editor.SelectionCount(); got != 1 {
				t.Fatalf("selection count = %d, want 1", got)
			}
			if !newModel.editor.IsSelected("az-1") {
				t.Fatal("expected az-1 to remain selected after session lifecycle message")
			}

			task, session := newModel.getCurrentTaskAndSession()
			if task == nil || task.ID != "az-1" {
				t.Fatalf("current task = %+v, want az-1", task)
			}
			if session == nil || session.Worktree != "/tmp/az-1" {
				t.Fatalf("current session = %+v, want projected az-1 session", session)
			}

			if len(newModel.toasts) == 0 {
				t.Fatal("expected lifecycle toast to be recorded")
			}
			if got := newModel.toasts[len(newModel.toasts)-1].Message; got != tt.wantToast {
				t.Fatalf("toast message = %q, want %q", got, tt.wantToast)
			}
		})
	}
}
