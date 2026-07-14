package tmuxselector

import (
	"context"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
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

type daemonKillerState struct {
	stopCalls             []daemonStopCall
	stopErr               error
	orchestratorStatus    protocol.OrchestratorSessionResult
	orchestratorStatusErr error
	orchestratorScopes    []domain.OrchestrationScope
	orchestratorStopCalls []domain.OrchestrationScope
	orchestratorExpected  []string
	orchestratorStopErr   error
}

func newTestDaemonKiller(t *testing.T) (*DaemonKiller, *fakeTmuxKiller, *daemonKillerState) {
	t.Helper()
	tmux := &fakeTmuxKiller{}
	state := &daemonKillerState{}
	killer := NewDaemonKiller(tmux, nil)
	killer.daemonStop = func(_ context.Context, socket, projectID, issueID string) error {
		state.stopCalls = append(state.stopCalls, daemonStopCall{socketPath: socket, projectID: projectID, issueID: issueID})
		return state.stopErr
	}
	killer.daemonOrchestratorStatus = func(_ context.Context, _, _ string, scope domain.OrchestrationScope) (protocol.OrchestratorSessionResult, error) {
		state.orchestratorScopes = append(state.orchestratorScopes, scope)
		return state.orchestratorStatus, state.orchestratorStatusErr
	}
	killer.daemonOrchestratorStop = func(_ context.Context, _, _ string, scope domain.OrchestrationScope, expectedSessionID string) error {
		state.orchestratorStopCalls = append(state.orchestratorStopCalls, scope)
		state.orchestratorExpected = append(state.orchestratorExpected, expectedSessionID)
		return state.orchestratorStopErr
	}
	return killer, tmux, state
}

func TestDaemonKillerRoutesAzIssueThroughDaemon(t *testing.T) {
	killer, tmux, state := newTestDaemonKiller(t)
	entry := InventoryEntry{
		SessionID:   "aa-one",
		IssueID:     "one",
		ProjectID:   "aa",
		ProjectPath: "/tmp/aa-project",
	}
	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("kill returned error: %v", err)
	}
	if len(state.stopCalls) != 1 {
		t.Fatalf("daemon stop calls = %d, want 1 (entry should route through daemon)", len(state.stopCalls))
	}
	got := state.stopCalls[0]
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
	killer, tmux, state := newTestDaemonKiller(t)
	entry := InventoryEntry{
		SessionID: "az",
	}
	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("kill returned error: %v", err)
	}
	if len(state.stopCalls) != 0 {
		t.Fatalf("daemon stop calls = %d, want 0 (literal az has no issue id)", len(state.stopCalls))
	}
	if len(tmux.killed) != 1 || tmux.killed[0] != "az" {
		t.Fatalf("tmux killed = %v, want [az]", tmux.killed)
	}
}

func TestDaemonKillerFallsBackToTmuxWhenProjectMissing(t *testing.T) {
	killer, tmux, state := newTestDaemonKiller(t)
	entry := InventoryEntry{
		SessionID: "aa-loose",
		IssueID:   "loose",
	}
	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("kill returned error: %v", err)
	}
	if len(state.stopCalls) != 0 {
		t.Fatalf("daemon stop calls = %d, want 0 when no project path/id is known", len(state.stopCalls))
	}
	if len(tmux.killed) != 1 || tmux.killed[0] != "aa-loose" {
		t.Fatalf("tmux killed = %v, want [aa-loose]", tmux.killed)
	}
}

func TestDaemonKillerFallsBackToTmuxForUntrackedSession(t *testing.T) {
	killer, tmux, state := newTestDaemonKiller(t)
	entry := InventoryEntry{
		SessionID: "plain-tmux",
	}
	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("kill returned error: %v", err)
	}
	if len(state.stopCalls) != 0 {
		t.Fatalf("daemon stop calls = %d, want 0 for untracked session", len(state.stopCalls))
	}
	if len(tmux.killed) != 1 || tmux.killed[0] != "plain-tmux" {
		t.Fatalf("tmux killed = %v, want [plain-tmux]", tmux.killed)
	}
}

func TestDaemonKillerSurfacesDaemonError(t *testing.T) {
	killer, _, state := newTestDaemonKiller(t)
	state.stopErr = errors.New("daemon boom")
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

func TestDaemonKillerRoutesProjectOrchestratorThroughDaemonAuthority(t *testing.T) {
	killer, tmux, state := newTestDaemonKiller(t)
	entry := InventoryEntry{SessionID: "aa-orchestrator-project", ProjectID: "aa", ProjectPath: "/tmp/aa-project"}
	state.orchestratorStatus = protocol.OrchestratorSessionResult{SessionID: entry.SessionID, Live: true}

	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("stop project orchestrator: %v", err)
	}
	if len(state.orchestratorStopCalls) != 1 || state.orchestratorStopCalls[0].Kind != domain.OrchestrationScopeProject {
		t.Fatalf("orchestrator stop scopes = %+v, want project", state.orchestratorStopCalls)
	}
	if len(state.orchestratorExpected) != 1 || state.orchestratorExpected[0] != entry.SessionID {
		t.Fatalf("expected session preconditions = %+v, want %q", state.orchestratorExpected, entry.SessionID)
	}
	if len(state.stopCalls) != 0 || len(tmux.killed) != 0 {
		t.Fatalf("ordinary stop calls = %+v, tmux kills = %+v; want neither", state.stopCalls, tmux.killed)
	}
}

func TestDaemonKillerFailsClosedWhenProjectOrchestratorLeaseIsMissing(t *testing.T) {
	killer, tmux, state := newTestDaemonKiller(t)
	entry := InventoryEntry{SessionID: "aa-orchestrator-project", ProjectID: "aa", ProjectPath: "/tmp/aa-project"}

	err := killer.KillSession(context.Background(), entry)
	if err == nil {
		t.Fatal("project orchestrator without a lease fell back to tmux, want fail-closed error")
	}
	if len(state.orchestratorStopCalls) != 0 || len(state.stopCalls) != 0 || len(tmux.killed) != 0 {
		t.Fatalf("missing lease mutated runtime: orchestrator=%+v ordinary=%+v tmux=%+v", state.orchestratorStopCalls, state.stopCalls, tmux.killed)
	}
}

func TestDaemonKillerRoutesRootedOrchestratorThroughDaemonAuthority(t *testing.T) {
	killer, tmux, state := newTestDaemonKiller(t)
	entry := InventoryEntry{SessionID: "aa-root", IssueID: "root", ProjectID: "aa", ProjectPath: "/tmp/aa-project"}
	state.orchestratorStatus = protocol.OrchestratorSessionResult{SessionID: entry.SessionID, Live: true}

	if err := killer.KillSession(context.Background(), entry); err != nil {
		t.Fatalf("stop rooted orchestrator: %v", err)
	}
	if len(state.orchestratorStopCalls) != 1 || state.orchestratorStopCalls[0].Kind != domain.OrchestrationScopeRooted || state.orchestratorStopCalls[0].RootIssueID.String() != "root" {
		t.Fatalf("orchestrator stop scopes = %+v, want rooted root", state.orchestratorStopCalls)
	}
	if len(state.stopCalls) != 0 || len(tmux.killed) != 0 {
		t.Fatalf("ordinary stop calls = %+v, tmux kills = %+v; want neither", state.stopCalls, tmux.killed)
	}
}

func TestDaemonKillerRejectsStaleOrchestratorEntry(t *testing.T) {
	killer, tmux, state := newTestDaemonKiller(t)
	entry := InventoryEntry{SessionID: "aa-root", IssueID: "root", ProjectID: "aa", ProjectPath: "/tmp/aa-project"}
	state.orchestratorStatus = protocol.OrchestratorSessionResult{SessionID: "aa-replacement", Live: true}

	err := killer.KillSession(context.Background(), entry)
	if err == nil {
		t.Fatal("stale orchestrator stop succeeded, want refresh error")
	}
	if len(state.orchestratorStopCalls) != 0 || len(state.stopCalls) != 0 || len(tmux.killed) != 0 {
		t.Fatalf("stale entry mutated runtime: orchestrator=%+v ordinary=%+v tmux=%+v", state.orchestratorStopCalls, state.stopCalls, tmux.killed)
	}
}
