package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestProjectOrchestratorSessionStartAttachesExactScopeSingleton(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "project-session"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		tmux:                   tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	body, err := json.Marshal(protocol.OrchestratorSessionRequest{Scope: domain.ProjectOrchestrationScope()})
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body}
	statusRequest := request
	statusRequest.Command = protocol.CommandOrchestratorSessionStatus
	missingResponse, err := d.handleOrchestratorSession(ctx, statusRequest)
	if err != nil || missingResponse.Error != nil {
		t.Fatalf("missing status: response=%+v err=%v", missingResponse.Error, err)
	}
	var missing protocol.OrchestratorSessionResult
	if err := json.Unmarshal(missingResponse.Body, &missing); err != nil {
		t.Fatal(err)
	}
	if missing.Disposition != "not-found" || missing.Live {
		t.Fatalf("missing status = %+v", missing)
	}

	firstResponse, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || firstResponse.Error != nil {
		t.Fatalf("first start: response=%+v err=%v", firstResponse.Error, err)
	}
	var first protocol.OrchestratorSessionResult
	if err := json.Unmarshal(firstResponse.Body, &first); err != nil {
		t.Fatal(err)
	}
	if first.Disposition != string(daemonstate.OrchestratorLeaseAcquired) || !first.Live {
		t.Fatalf("first result = %+v", first)
	}
	staleProjection, found, err := store.GetSessionState(ctx, projectID, first.SessionID)
	if err != nil || !found {
		t.Fatalf("load stale projection: found=%t err=%v", found, err)
	}
	staleProjection.State, staleProjection.ObservedState = daemonstate.SessionStateStopped, daemonstate.SessionStateStopped
	staleProjection.Activity, staleProjection.ActivitySource = "", ""
	if err := upsertSessionStateFixture(store, ctx, projectID, staleProjection); err != nil {
		t.Fatal(err)
	}

	secondResponse, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || secondResponse.Error != nil {
		t.Fatalf("second start: response=%+v err=%v", secondResponse.Error, err)
	}
	var second protocol.OrchestratorSessionResult
	if err := json.Unmarshal(secondResponse.Body, &second); err != nil {
		t.Fatal(err)
	}
	if second.Disposition != string(daemonstate.OrchestratorLeaseAttached) || second.SessionID != first.SessionID {
		t.Fatalf("second result = %+v, first = %+v", second, first)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).SetLifecycle(ctx, identity, first.SessionID, domain.OrchestratorPaused); err != nil {
		t.Fatal(err)
	}
	attachRequest := request
	attachRequest.Command = protocol.CommandOrchestratorSessionAttach
	attachResponse, err := d.handleOrchestratorSession(ctx, attachRequest)
	if err != nil || attachResponse.Error != nil {
		t.Fatalf("attach declaration: response=%+v err=%v", attachResponse.Error, err)
	}
	var attached protocol.OrchestratorSessionResult
	if err := json.Unmarshal(attachResponse.Body, &attached); err != nil {
		t.Fatal(err)
	}
	if attached.Disposition != "attached" || !attached.Live || attached.SessionID != first.SessionID || attached.Lifecycle != domain.OrchestratorWorking {
		t.Fatalf("attached result = %+v", attached)
	}
	persistedLease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, identity)
	if err != nil || !found || persistedLease.Lifecycle != domain.OrchestratorWorking {
		t.Fatalf("persisted attach lifecycle = %+v found=%t err=%v", persistedLease, found, err)
	}
	for _, command := range runner.commands {
		if len(command) > 0 && command[0] == "attach-session" {
			t.Fatalf("daemon attach handler invoked blocking terminal operation: %+v", command)
		}
	}

	newSessions := 0
	for _, command := range runner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			newSessions++
		}
	}
	if newSessions != 1 {
		t.Fatalf("new-session calls = %d, want 1", newSessions)
	}
	runner.sessions[first.SessionID] = false
	recoveredResponse, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || recoveredResponse.Error != nil {
		t.Fatalf("recovered start: response=%+v err=%v", recoveredResponse.Error, err)
	}
	var recovered protocol.OrchestratorSessionResult
	if err := json.Unmarshal(recoveredResponse.Body, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Disposition != string(daemonstate.OrchestratorLeaseRecoveredStale) || !recovered.Live {
		t.Fatalf("recovered = %+v", recovered)
	}
	newSessions = 0
	for _, command := range runner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			newSessions++
		}
	}
	if newSessions != 2 {
		t.Fatalf("new-session calls after recovery = %d, want 2", newSessions)
	}
	projection, found, err := store.GetSessionState(ctx, projectID, first.SessionID)
	if err != nil || !found {
		t.Fatalf("projection found=%t err=%v", found, err)
	}
	if projection.Role != daemonstate.SessionRoleOrchestrator || projection.ScopeKind != daemonstate.SessionScopeOrchestration || projection.ScopeID != "project" {
		t.Fatalf("projection = %+v", projection)
	}
	if projection.State != daemonstate.SessionStateRunning || projection.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("projection lifecycle = %+v", projection)
	}
}

func TestFullReconcileRecoversMissingProjectOrchestratorThroughAuthority(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	cache := daemonstate.NewStore()
	manager := git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{repoDir: store}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}, tmux: tmux.NewClient(runner, slog.Default()), sessionStore: cache, worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: manager}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager}}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: domain.ProjectOrchestrationScope()})
	request := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body}
	response, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || response.Error != nil {
		t.Fatalf("start: response=%+v err=%v", response.Error, err)
	}
	sessionID := d.orchestratorSessionID(projectID, domain.ProjectOrchestrationScope())
	before, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || !orchestratorSessionProjection(before) || !sessionProjectionCanRecreateTmuxSession(before) {
		t.Fatalf("pre-reconcile projection=%+v found=%v err=%v", before, found, err)
	}
	intents, err := store.ListSessionIntentStates(ctx, projectID)
	if err != nil || len(intents) != 1 {
		t.Fatalf("intent projections=%+v err=%v", intents, err)
	}
	delete(runner.sessions, sessionID)
	result, err := d.reconcileTmuxAndDaemonSessions(ctx, projectID, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.RecreatedTmuxSessions != 1 || !runner.sessions[sessionID] {
		t.Fatalf("result=%+v live=%v", result, runner.sessions[sessionID])
	}
	projection, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || projection.Role != daemonstate.SessionRoleOrchestrator || projection.ScopeID != "project" || projection.IssueID != "" {
		t.Fatalf("projection=%+v found=%v err=%v", projection, found, err)
	}
}
