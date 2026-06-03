package tmuxselector

import (
	"context"
	"errors"
	"testing"
)

type fakeTmuxKiller struct {
	killed []string
	err    error
}

func (f *fakeTmuxKiller) KillSession(_ context.Context, sessionName string) error {
	f.killed = append(f.killed, sessionName)
	return f.err
}

type daemonStopCall struct {
	socketPath string
	projectID  string
	issueID    string
}

func newTestDaemonKiller(t *testing.T) (*DaemonKiller, *fakeTmuxKiller, *[]daemonStopCall, *error) {
	t.Helper()
	tmux := &fakeTmuxKiller{}
	calls := []daemonStopCall{}
	var stopErr error
	killer := NewDaemonKiller(tmux, nil)
	killer.daemonStop = func(_ context.Context, socket, projectID, issueID string) error {
		calls = append(calls, daemonStopCall{socketPath: socket, projectID: projectID, issueID: issueID})
		return stopErr
	}
	return killer, tmux, &calls, &stopErr
}

func TestDaemonKillerRoutesAzIssueThroughDaemon(t *testing.T) {
	killer, tmux, calls, _ := newTestDaemonKiller(t)
	entry := InventoryEntry{
		SessionID:   "aa-one",
		IssueID:     "one",
		ProjectID:   "aa",
		ProjectPath: "/tmp/aa-project",
	}
	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("kill returned error: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("daemon stop calls = %d, want 1 (entry should route through daemon)", len(*calls))
	}
	got := (*calls)[0]
	if got.issueID != "one" {
		t.Fatalf("daemon stop issueID = %q, want one", got.issueID)
	}
	if got.projectID == "" {
		t.Fatalf("daemon stop projectID empty, want resolved from project path")
	}
	if got.socketPath == "" {
		t.Fatalf("daemon stop socketPath empty, want resolved from project path")
	}
	if len(tmux.killed) != 0 {
		t.Fatalf("tmux killed = %v, want none (daemon path should not fall back)", tmux.killed)
	}
}

func TestDaemonKillerFallsBackToTmuxForLiteralAzSession(t *testing.T) {
	killer, tmux, calls, _ := newTestDaemonKiller(t)
	entry := InventoryEntry{
		SessionID: "az",
	}
	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("kill returned error: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("daemon stop calls = %d, want 0 (literal az has no issue id)", len(*calls))
	}
	if len(tmux.killed) != 1 || tmux.killed[0] != "az" {
		t.Fatalf("tmux killed = %v, want [az]", tmux.killed)
	}
}

func TestDaemonKillerFallsBackToTmuxWhenProjectMissing(t *testing.T) {
	killer, tmux, calls, _ := newTestDaemonKiller(t)
	entry := InventoryEntry{
		SessionID: "aa-loose",
		IssueID:   "loose",
	}
	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("kill returned error: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("daemon stop calls = %d, want 0 when no project path/id is known", len(*calls))
	}
	if len(tmux.killed) != 1 || tmux.killed[0] != "aa-loose" {
		t.Fatalf("tmux killed = %v, want [aa-loose]", tmux.killed)
	}
}

func TestDaemonKillerFallsBackToTmuxForUntrackedSession(t *testing.T) {
	killer, tmux, calls, _ := newTestDaemonKiller(t)
	entry := InventoryEntry{
		SessionID: "plain-tmux",
	}
	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("kill returned error: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("daemon stop calls = %d, want 0 for untracked session", len(*calls))
	}
	if len(tmux.killed) != 1 || tmux.killed[0] != "plain-tmux" {
		t.Fatalf("tmux killed = %v, want [plain-tmux]", tmux.killed)
	}
}

func TestDaemonKillerSurfacesDaemonError(t *testing.T) {
	killer, _, _, stopErr := newTestDaemonKiller(t)
	*stopErr = errors.New("daemon boom")
	entry := InventoryEntry{
		SessionID:   "aa-one",
		IssueID:     "one",
		ProjectID:   "aa",
		ProjectPath: "/tmp/aa-project",
	}
	err := killer.KillSession(context.Background(), entry)
	if err == nil {
		t.Fatal("expected error from daemon stop to surface")
	}
}
