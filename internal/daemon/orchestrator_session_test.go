package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
	"github.com/riordanpawley/azedarach/internal/testutil/issuefixture"
	_ "modernc.org/sqlite"
)

func TestProjectOrchestratorSessionStartAttachesExactScopeSingleton(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "project-session"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	managedDir := filepath.Join(t.TempDir(), ".azedarach-generations", "generation.current")
	staleDir := filepath.Join(repoDir, "bin")
	t.Setenv("PATH", staleDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, ManagedGenerationBinDir: managedDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		tmux:                   tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
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
	launchCommand := requireNewSessionLaunchCommand(t, runner, first.SessionID)
	if strings.Contains(launchCommand, "export PATH=") || strings.Contains(launchCommand, managedDir) {
		t.Fatalf("project orchestrator launch injects managed PATH: %s", launchCommand)
	}
	for _, command := range runner.commands {
		if len(command) == 0 || command[0] != "new-session" {
			continue
		}
		pathValue, ok := tmuxCommandEnvironmentValue(command, "PATH")
		if ok || pathValue != "" {
			t.Fatalf("project orchestrator tmux injected PATH = %q, %t; command=%v", pathValue, ok, command)
		}
		break
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

func TestProjectOrchestratorSessionStartRejectsImmediateManagedAgentExitAndRetries(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "project-session-exit"
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
	runner.onNewSession = func(sessionID string) { runner.sessionsWithoutPanes[sessionID] = true }
	failed, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || failed.Error == nil || failed.Error.Code != protocol.ErrorCodeUnavailable {
		t.Fatalf("immediate-exit start response=%+v err=%v", failed.Error, err)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	if lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, identity); err != nil || found {
		t.Fatalf("lease after rejected launch=%+v found=%t err=%v", lease, found, err)
	}
	sessionID := d.orchestratorSessionID(projectID, domain.ProjectOrchestrationScope())
	if live, err := d.tmux.HasSession(ctx, sessionID); err != nil || live {
		t.Fatalf("runtime after rejected launch live=%t err=%v", live, err)
	}
	if projection, found, err := store.GetSessionState(ctx, projectID, sessionID); err != nil || found {
		t.Fatalf("projection after rejected launch=%+v found=%t err=%v", projection, found, err)
	}

	runner.onNewSession = nil
	delete(runner.sessionsWithoutPanes, sessionID)
	acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
	retried, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || retried.Error != nil {
		t.Fatalf("retry start response=%+v err=%v", retried.Error, err)
	}
	var result protocol.OrchestratorSessionResult
	if err := json.Unmarshal(retried.Body, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Live || result.Lifecycle != domain.OrchestratorWorking {
		t.Fatalf("retry result=%+v", result)
	}
}

func TestProjectOrchestratorSessionRetryRejectsCrashOrphanedShell(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "project-session-orphan"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	runner.currentCommand = "zsh"
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, SessionShell: "zsh", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		tmux:                   tmux.NewClient(runner, slog.Default()),
	}
	scope := domain.ProjectOrchestrationScope()
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := d.orchestratorSessionID(projectID, scope)
	runner.sessions[sessionID] = true
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).Acquire(ctx, identity, sessionID, d.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	request := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body}
	failed, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || failed.Error == nil || failed.Error.Code != protocol.ErrorCodeUnavailable {
		t.Fatalf("orphan retry response=%+v err=%v", failed.Error, err)
	}
	if runner.sessions[sessionID] {
		t.Fatalf("crash-orphaned shell %s survived readiness rejection", sessionID)
	}
	lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, identity)
	if err != nil || !found || lease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("orphan lease=%+v found=%t err=%v", lease, found, err)
	}

	runner.currentCommand = "codex"
	acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
	retried, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || retried.Error != nil {
		t.Fatalf("retry after orphan cleanup response=%+v err=%v", retried.Error, err)
	}
	var result protocol.OrchestratorSessionResult
	if err := json.Unmarshal(retried.Body, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Live || result.Lifecycle != domain.OrchestratorWorking {
		t.Fatalf("retry result=%+v", result)
	}
}

func TestRootedOrchestratorSessionRetryRejectsStaleShellRuntime(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Root retry fence", Type: domain.TypeEpic})
	if err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	manager := git.NewWorktreeManager(&worktreeCreateRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+rootID), branchName: "test/" + rootID}, repoDir, slog.Default())
	memoryStore := daemonstate.NewStore()
	d := &Daemon{
		cfg:    Config{RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues: issuesClient, tmux: tmux.NewClient(runner, slog.Default()), session: daemonhandlers.NewSessionHandler(memoryStore), sessionStore: memoryStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{repoDir: store}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: manager}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager}, revision: map[string]uint64{},
	}
	acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	request := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body}
	started, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || started.Error != nil {
		t.Fatalf("initial rooted start response=%+v err=%v", started.Error, err)
	}
	sessionID := d.orchestratorSessionID(projectID, scope)
	runner.currentCommand = "zsh"
	failed, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || failed.Error == nil || failed.Error.Code != protocol.ErrorCodeUnavailable {
		t.Fatalf("rooted shell retry response=%+v err=%v", failed.Error, err)
	}
	if runner.sessions[sessionID] {
		t.Fatalf("stale rooted shell %s survived", sessionID)
	}
	identity, _ := domain.NewOrchestratorIdentity(projectID, scope)
	lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, identity)
	if err != nil || !found || lease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("rooted shell lease=%+v found=%t err=%v", lease, found, err)
	}
	projection, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || projection.State != daemonstate.SessionStateStopped || projection.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("rooted shell projection=%+v found=%t err=%v", projection, found, err)
	}

	runner.currentCommand = "codex"
	retried, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || retried.Error != nil {
		t.Fatalf("rooted retry response=%+v err=%v", retried.Error, err)
	}
	if !runner.sessions[sessionID] {
		t.Fatalf("rooted retry did not recreate %s", sessionID)
	}

	// A reused pane identity must fail even when the visible command still
	// looks like the expected agent executable.
	runner.panePIDs[sessionID] = 999
	stale, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || stale.Error == nil || stale.Error.Code != protocol.ErrorCodeUnavailable {
		t.Fatalf("rooted stale-identity retry response=%+v err=%v", stale.Error, err)
	}
	if runner.sessions[sessionID] {
		t.Fatalf("stale rooted pane identity survived for %s", sessionID)
	}
	finalRetry, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || finalRetry.Error != nil || !runner.sessions[sessionID] {
		t.Fatalf("rooted final retry response=%+v err=%v live=%t", finalRetry.Error, err, runner.sessions[sessionID])
	}
}

func TestRootedOrchestratorSessionCancelledLaunchCleansDurableStateAndRetries(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	rootID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{Title: "Cancelled rooted start", Type: domain.TypeEpic})
	if err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	manager := git.NewWorktreeManager(&worktreeCreateRunner{
		worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+rootID),
		branchName:   "test/" + rootID,
	}, repoDir, slog.Default())
	memoryStore := daemonstate.NewStore()
	d := &Daemon{
		cfg:    Config{RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues: issuesClient, tmux: tmux.NewClient(runner, slog.Default()), session: daemonhandlers.NewSessionHandler(memoryStore), sessionStore: memoryStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{repoDir: store}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: manager}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager}, revision: map[string]uint64{},
	}
	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	request := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body}

	startCtx, cancelStart := context.WithCancel(context.Background())
	runner.onNewSessionCommand = func(context.Context, string) error {
		cancelStart()
		return context.Canceled
	}
	failed, err := d.handleOrchestratorSession(startCtx, request)
	if err != nil || failed.Error == nil || !strings.Contains(failed.Error.Message, context.Canceled.Error()) {
		t.Fatalf("cancelled rooted start response=%+v err=%v", failed.Error, err)
	}
	if lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(context.Background(), identity); err != nil || found {
		t.Fatalf("rooted lease after cancellation=%+v found=%t err=%v", lease, found, err)
	}
	sessionID := d.orchestratorSessionID(projectID, scope)
	if runner.sessions[sessionID] {
		t.Fatalf("cancelled rooted runtime %s survived", sessionID)
	}
	if projection, found, err := store.GetSessionIntent(context.Background(), projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, rootID); err != nil || (found && projection.State != daemonstate.SessionStateStopped) {
		t.Fatalf("rooted projection after cancellation=%+v found=%t err=%v", projection, found, err)
	}

	runner.onNewSessionCommand = nil
	acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
	retried, err := d.handleOrchestratorSession(context.Background(), request)
	if err != nil || retried.Error != nil || !runner.sessions[sessionID] {
		t.Fatalf("rooted retry response=%+v err=%v live=%t", retried.Error, err, runner.sessions[sessionID])
	}
}

func TestRootedOrchestratorSessionStartupSeedsRoleAndRepairsMissingBootstrap(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Coordinate a rooted migration without implementing it",
		Type:  domain.TypeEpic,
	})
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+rootID)
	worktreeRunner := &worktreeCreateRunner{worktreePath: worktreePath, branchName: "test/" + rootID}
	manager := git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default())
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	tmuxRunner := newSessionStartTmuxRunner()
	d := &Daemon{
		cfg:                       Config{RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:                    issuesClient,
		tmux:                      tmux.NewClient(tmuxRunner, slog.Default()),
		session:                   daemonhandlers.NewSessionHandler(daemonstate.NewStore()),
		sessionStore:              daemonstate.NewStore(),
		runtimeStoresByRoot:       map[string]*daemonstate.RuntimeStateStore{repoDir: runtimeStore},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByRoot:    map[string]*git.WorktreeManager{repoDir: manager},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
		revision:                  map[string]uint64{},
	}
	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	request := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body}
	// Upgrade/recovery may encounter the legacy worker intent before the rooted
	// role has ever been materialized. The first exact-scope transition must
	// replace it atomically.
	rootedSessionID := d.orchestratorSessionID(projectID, scope)
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID: rootedSessionID, IssueID: rootID, Role: daemonstate.SessionRoleWorker,
		ScopeKind: daemonstate.SessionScopeIssue, ScopeID: rootID,
		State: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	startWork := true
	genericStart := protocol.RequestEnvelope{
		Command: daemonhandlers.CommandSessionStart,
		Meta:    protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(sessionCommandBody{
			ProjectID: projectID, IssueID: rootID, SessionID: rootedSessionID, StartWork: &startWork,
		}),
	}
	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	delegated, err := d.handleSessionStartDirect(ctx, genericStart)
	if err != nil || delegated.Error != nil || !strings.Contains(string(delegated.Body), "Starting rooted orchestrator session for epic") {
		t.Fatalf("generic epic start did not delegate: response=%+v body=%q err=%v", delegated.Error, delegated.Body, err)
	}
	statusRequest := request
	statusRequest.Command = protocol.CommandOrchestratorSessionStatus
	response, err := d.handleOrchestratorSession(ctx, statusRequest)
	if err != nil || response.Error != nil {
		t.Fatalf("status delegated rooted orchestrator: response=%+v err=%v", response.Error, err)
	}
	var started protocol.OrchestratorSessionResult
	if err := json.Unmarshal(response.Body, &started); err != nil {
		t.Fatal(err)
	}
	if !started.Live {
		t.Fatalf("started = %+v", started)
	}
	prompt := tmuxRunner.launchPromptContents[started.SessionID]
	for _, want := range []string{
		"Role: orchestrator",
		"Rooted startup contract (root " + rootID + ")",
		"`az prime`",
		"az orchestrate status --root " + rootID,
		"az orchestrate watch --root " + rootID + " --since 0 --jsonl",
		"never implement the root's worker scope",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("rooted prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Role: contributor") || strings.Contains(prompt, "Role: worker") {
		t.Fatalf("rooted prompt contains worker role:\n%s", prompt)
	}
	projection, found, err := runtimeStore.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, rootID)
	if err != nil || !found || projection.Role != daemonstate.SessionRoleOrchestrator || projection.ScopeKind != daemonstate.SessionScopeOrchestration || projection.ScopeID != rootID {
		t.Fatalf("rooted projection = %+v found=%t err=%v", projection, found, err)
	}
	workerProjection, found, err := runtimeStore.GetWorkerSessionStateByIssueID(ctx, projectID, rootID, started.SessionID)
	if err != nil {
		t.Fatalf("load rooted worker projection: %v", err)
	}
	if found {
		t.Fatalf("rooted startup retained recoverable worker intent: %+v", workerProjection)
	}
	intents, err := runtimeStore.ListSessionIntentStates(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || !orchestratorSessionProjection(intents[0]) {
		t.Fatalf("rooted startup intents = %+v, want one orchestrator intent", intents)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	ackAuthority := daemonstate.NewRootedBootstrapAcknowledgementAuthority(runtimeStore)
	acknowledgement, found, err := ackAuthority.Get(ctx, identity)
	if err != nil || !found || acknowledgement.SessionID != started.SessionID || acknowledgement.RuntimeNonce == "" || acknowledgement.PromptHash != rootedOrchestratorPromptHash(prompt) || acknowledgement.AcknowledgedAt.IsZero() {
		t.Fatalf("bootstrap acknowledgement = %+v found=%t err=%v", acknowledgement, found, err)
	}
	if got := tmuxRunner.env[started.SessionID][rootedOrchestratorBootstrapNonceEnvironment]; got != acknowledgement.RuntimeNonce {
		t.Fatalf("runtime nonce = %q, acknowledgement = %q", got, acknowledgement.RuntimeNonce)
	}
	lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(runtimeStore).Get(ctx, identity)
	if err != nil || !found || lease.SessionID != started.SessionID || lease.Lifecycle != domain.OrchestratorWorking || lease.AcquiredAt.After(acknowledgement.AcknowledgedAt) {
		t.Fatalf("rooted lease = %+v found=%t err=%v acknowledgement=%+v", lease, found, err, acknowledgement)
	}
	inputsBefore := len(tmuxRunner.inputPayloads)
	response, err = d.handleOrchestratorSession(ctx, request)
	if err != nil || response.Error != nil {
		t.Fatalf("verify same rooted runtime: response=%+v err=%v", response.Error, err)
	}
	if len(tmuxRunner.inputPayloads) != inputsBefore {
		t.Fatalf("same rooted runtime was re-prompted: inputs=%d, want %d", len(tmuxRunner.inputPayloads), inputsBefore)
	}
	if worker, found, err := runtimeStore.GetWorkerSessionStateByIssueID(ctx, projectID, rootID, started.SessionID); err != nil || found {
		t.Fatalf("legacy rooted worker intent = %+v found=%t err=%v", worker, found, err)
	}
	attached, err := d.handleSessionAttach(ctx, protocol.RequestEnvelope{
		Command: daemonhandlers.CommandSessionAttach,
		Meta:    protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:    genericStart.Body,
	})
	if err != nil || attached.Error != nil || !strings.Contains(string(attached.Body), "Attaching to rooted orchestrator session") {
		t.Fatalf("generic rooted attach did not delegate: response=%+v body=%q err=%v", attached.Error, attached.Body, err)
	}
	if worker, found, err := runtimeStore.GetWorkerSessionStateByIssueID(ctx, projectID, rootID, started.SessionID); err != nil || found {
		t.Fatalf("generic rooted attach recreated worker intent: %+v found=%t err=%v", worker, found, err)
	}

	// restart-all replaces and re-acknowledges the rooted agent while holding
	// the same exact-scope transition lock used by rooted start/attach.
	tmuxRunner.onRespawnPane = seedManagedRestartIdentity(t, d, tmuxRunner, projectID, started.SessionID)
	restartRequest := protocol.RequestEnvelope{
		Command: protocol.CommandSessionRestartAll,
		Meta:    protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:    marshalJSON(protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID(projectID), ForceBusy: true}),
	}
	inputsBefore = len(tmuxRunner.inputPayloads)
	handoffsBefore := len(tmuxRunner.handoffPromptContents)
	restartResponse, err := d.handleSessionRestartAll(ctx, restartRequest)
	if err != nil || restartResponse.Error != nil {
		t.Fatalf("restart rooted agent: response=%+v err=%v", restartResponse.Error, err)
	}
	var restartResult protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(restartResponse.Body, &restartResult); err != nil {
		t.Fatal(err)
	}
	if restartResult.Restarted != 1 || restartResult.Failed != 0 {
		t.Fatalf("restart rooted agent result = %+v", restartResult)
	}
	if len(tmuxRunner.inputPayloads) != inputsBefore+1 || len(tmuxRunner.handoffPromptContents) != handoffsBefore+1 {
		t.Fatalf("restart rooted acknowledgement delivery inputs=%d handoffs=%d", len(tmuxRunner.inputPayloads)-inputsBefore, len(tmuxRunner.handoffPromptContents)-handoffsBefore)
	}
	restartedNonce := tmuxRunner.env[started.SessionID][rootedOrchestratorBootstrapNonceEnvironment]
	if restartedNonce == "" || restartedNonce == acknowledgement.RuntimeNonce {
		t.Fatalf("restart nonce = %q, seeded nonce = %q", restartedNonce, acknowledgement.RuntimeNonce)
	}
	inputsBefore = len(tmuxRunner.inputPayloads)
	response, err = d.handleOrchestratorSession(ctx, request)
	if err != nil || response.Error != nil {
		t.Fatalf("verify restarted rooted agent: response=%+v err=%v", response.Error, err)
	}
	if len(tmuxRunner.inputPayloads) != inputsBefore {
		t.Fatalf("acknowledged restarted rooted agent was re-prompted")
	}

	// A failed exact replacement leaves durable acknowledgement absent, so a
	// later rooted start repairs whichever process survived.
	d.sessionRestartRespawn = func(context.Context, string, string, string) (error, bool) {
		return context.Canceled, false
	}
	cancelledResponse, err := d.handleSessionRestartAll(ctx, restartRequest)
	if err != nil || cancelledResponse.Error != nil {
		t.Fatalf("cancel rooted replacement: response=%+v err=%v", cancelledResponse.Error, err)
	}
	var cancelledResult protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(cancelledResponse.Body, &cancelledResult); err != nil {
		t.Fatal(err)
	}
	if cancelledResult.Restarted != 0 || cancelledResult.Failed != 1 {
		t.Fatalf("cancelled rooted replacement result = %+v", cancelledResult)
	}
	cancelledNonce := tmuxRunner.env[started.SessionID][rootedOrchestratorBootstrapNonceEnvironment]
	if cancelledNonce != "" {
		t.Fatalf("cancelled replacement nonce = %q, want invalidated", cancelledNonce)
	}
	if _, found, err := ackAuthority.Get(ctx, identity); err != nil || found {
		t.Fatalf("cancelled replacement acknowledgement found=%t err=%v", found, err)
	}
	d.sessionRestartRespawn = nil
	inputsBefore = len(tmuxRunner.inputPayloads)
	response, err = d.handleOrchestratorSession(ctx, request)
	if err != nil || response.Error != nil {
		t.Fatalf("repair cancelled rooted replacement: response=%+v err=%v", response.Error, err)
	}
	if len(tmuxRunner.inputPayloads) != inputsBefore+1 {
		t.Fatalf("cancelled rooted replacement repair inputs=%d, want 1", len(tmuxRunner.inputPayloads)-inputsBefore)
	}
	currentAck, found, err := ackAuthority.Get(ctx, identity)
	if err != nil || !found {
		t.Fatalf("load repaired acknowledgement found=%t err=%v", found, err)
	}
	if err := ackAuthority.Invalidate(ctx, identity, started.SessionID); err != nil {
		t.Fatal(err)
	}
	inputsBefore = len(tmuxRunner.inputPayloads)
	response, err = d.handleOrchestratorSession(ctx, request)
	if err != nil || response.Error != nil {
		t.Fatalf("repair rooted bootstrap: response=%+v err=%v", response.Error, err)
	}
	var repaired protocol.OrchestratorSessionResult
	if err := json.Unmarshal(response.Body, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Disposition != string(daemonstate.OrchestratorLeaseAttached) {
		t.Fatalf("repaired = %+v", repaired)
	}
	if len(tmuxRunner.inputPayloads) != inputsBefore+1 || !strings.Contains(tmuxRunner.inputPayloads[len(tmuxRunner.inputPayloads)-1], sessionLaunchArtifactPrefix) {
		t.Fatalf("bootstrap repair delivery = %+v", tmuxRunner.inputPayloads[inputsBefore:])
	}
	repairedAck, found, err := ackAuthority.Get(ctx, identity)
	if err != nil || !found || repairedAck.RuntimeNonce == "" || repairedAck.RuntimeNonce == currentAck.RuntimeNonce {
		t.Fatalf("repaired acknowledgement = %+v found=%t err=%v", repairedAck, found, err)
	}

	// Lose the rooted tmux runtime while retaining its acknowledgement, then reuse
	// the deterministic session ID for an ordinary task runtime. The stale
	// projection must not suppress rooted role repair on the next start.
	delete(tmuxRunner.sessions, started.SessionID)
	delete(tmuxRunner.env, started.SessionID)
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(runtimeStore).SetLifecycle(ctx, identity, started.SessionID, domain.OrchestratorPaused); err != nil {
		t.Fatal(err)
	}
	tmuxRunner.sessions[started.SessionID] = true
	tmuxRunner.launchPromptContents[started.SessionID] = "Role: contributor"
	inputsBefore = len(tmuxRunner.inputPayloads)
	handoffsBefore = len(tmuxRunner.handoffPromptContents)
	response, err = d.handleOrchestratorSession(ctx, request)
	if err != nil || response.Error != nil {
		t.Fatalf("repair reused ordinary runtime: response=%+v err=%v", response.Error, err)
	}
	if len(tmuxRunner.inputPayloads) != inputsBefore+1 || len(tmuxRunner.handoffPromptContents) != handoffsBefore+1 {
		t.Fatalf("ordinary runtime repair delivery inputs=%d handoffs=%d", len(tmuxRunner.inputPayloads)-inputsBefore, len(tmuxRunner.handoffPromptContents)-handoffsBefore)
	}
	repairPrompt := tmuxRunner.handoffPromptContents[len(tmuxRunner.handoffPromptContents)-1]
	if !strings.Contains(repairPrompt, "Role: orchestrator") || strings.Contains(repairPrompt, "Role: contributor") || strings.Contains(repairPrompt, "Role: worker") {
		t.Fatalf("ordinary runtime repair prompt = %q", repairPrompt)
	}
	incarnationAck, found, err := ackAuthority.Get(ctx, identity)
	if err != nil || !found || incarnationAck.RuntimeNonce == "" || incarnationAck.RuntimeNonce == repairedAck.RuntimeNonce {
		t.Fatalf("reused runtime acknowledgement = %+v found=%t err=%v", incarnationAck, found, err)
	}
	if got := tmuxRunner.env[started.SessionID][rootedOrchestratorBootstrapNonceEnvironment]; got != incarnationAck.RuntimeNonce {
		t.Fatalf("reused runtime nonce = %q, acknowledgement = %q", got, incarnationAck.RuntimeNonce)
	}

	// Runtime loss while desired-working must recover only through rooted
	// orchestrator authority, including a fresh role prompt and acknowledgement.
	delete(tmuxRunner.sessions, started.SessionID)
	delete(tmuxRunner.env, started.SessionID)
	workerBody := marshalJSON(sessionCommandBody{ProjectID: projectID, IssueID: rootID, SessionID: started.SessionID, Prompt: "Role: contributor"})
	workerResponse, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		Command: daemonhandlers.CommandSessionStart,
		Meta:    protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:    workerBody,
	})
	if err != nil || workerResponse.Error != nil || !strings.Contains(string(workerResponse.Body), "Starting rooted orchestrator session for epic") {
		t.Fatalf("generic epic recovery did not delegate: response=%+v body=%q err=%v", workerResponse.Error, workerResponse.Body, err)
	}
	if !tmuxRunner.sessions[started.SessionID] {
		t.Fatal("generic epic recovery did not recreate rooted runtime")
	}
	if worker, found, err := runtimeStore.GetWorkerSessionStateByIssueID(ctx, projectID, rootID, started.SessionID); err != nil || found {
		t.Fatalf("generic epic recovery recreated worker intent: %+v found=%t err=%v", worker, found, err)
	}
	delete(tmuxRunner.sessions, started.SessionID)
	delete(tmuxRunner.env, started.SessionID)
	launchesBefore := len(tmuxRunner.launchPromptContents)
	recovery, err := d.reconcileTmuxAndDaemonSessions(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("reconcile missing rooted runtime: %v", err)
	}
	if recovery.RecreatedTmuxSessions != 1 || !tmuxRunner.sessions[started.SessionID] {
		t.Fatalf("rooted recovery = %+v live=%t", recovery, tmuxRunner.sessions[started.SessionID])
	}
	if len(tmuxRunner.launchPromptContents) != launchesBefore || !strings.Contains(tmuxRunner.launchPromptContents[started.SessionID], "Role: orchestrator") {
		t.Fatalf("rooted recovery prompt = %q", tmuxRunner.launchPromptContents[started.SessionID])
	}
	recoveryAck, found, err := ackAuthority.Get(ctx, identity)
	if err != nil || !found || recoveryAck.RuntimeNonce == "" || recoveryAck.RuntimeNonce == incarnationAck.RuntimeNonce {
		t.Fatalf("rooted recovery acknowledgement = %+v found=%t err=%v", recoveryAck, found, err)
	}
	if worker, found, err := runtimeStore.GetWorkerSessionStateByIssueID(ctx, projectID, rootID, started.SessionID); err != nil || found {
		t.Fatalf("rooted recovery worker intent = %+v found=%t err=%v", worker, found, err)
	}

	// Explicit pause is durable desired-stopped intent. Reconcile must neither
	// relaunch the rooted runtime nor fall back to generic worker recovery.
	d.orchestratorStopGracePeriod = time.Millisecond
	d.orchestratorStopPollInterval = time.Millisecond
	stopRequest := protocol.RequestEnvelope{Command: daemonhandlers.CommandSessionStop, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: genericStart.Body}
	stopResponse, err := d.handleSessionStopDirect(ctx, stopRequest)
	if err != nil || stopResponse.Error != nil || !strings.Contains(string(stopResponse.Body), "Stopped rooted orchestrator session") {
		t.Fatalf("generic rooted stop did not delegate: response=%+v body=%q err=%v", stopResponse.Error, stopResponse.Body, err)
	}
	recovery, err = d.reconcileTmuxAndDaemonSessions(ctx, projectID, rootID)
	if err != nil {
		t.Fatalf("reconcile paused rooted runtime: %v", err)
	}
	if recovery.RecreatedTmuxSessions != 0 || tmuxRunner.sessions[started.SessionID] {
		t.Fatalf("paused rooted recovery = %+v live=%t", recovery, tmuxRunner.sessions[started.SessionID])
	}
	if worker, found, err := runtimeStore.GetWorkerSessionStateByIssueID(ctx, projectID, rootID, started.SessionID); err != nil || found {
		t.Fatalf("paused rooted worker intent = %+v found=%t err=%v", worker, found, err)
	}
}

func TestBlockedRootRejectsWorkerAndOrchestratorStartWithoutSideEffects(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	blockerID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Blocking migration", Type: domain.TypeTask, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Blocked epic", Type: domain.TypeEpic, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if err := issuesClient.AddDependency(ctx, rootID, blockerID, string(domain.DependencyBlocks)); err != nil {
		t.Fatal(err)
	}
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	tmuxRunner := newSessionStartTmuxRunner()
	worktreeRunner := &worktreeCreateRunner{worktreePath: filepath.Join(t.TempDir(), rootID)}
	manager := git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default())
	d := &Daemon{
		cfg:                       Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:                    issuesClient,
		tmux:                      tmux.NewClient(tmuxRunner, slog.Default()),
		session:                   daemonhandlers.NewSessionHandler(daemonstate.NewStore()),
		sessionStore:              daemonstate.NewStore(),
		runtimeStoresByRoot:       map[string]*daemonstate.RuntimeStateStore{repoDir: runtimeStore},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByRoot:    map[string]*git.WorktreeManager{repoDir: manager},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
		revision:                  map[string]uint64{},
	}

	workerBody, _ := json.Marshal(sessionCommandBody{ProjectID: projectID, IssueID: rootID, SessionID: "az-" + rootID})
	workerResp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{Command: daemonhandlers.CommandSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: workerBody})
	if err != nil || workerResp.Error == nil || workerResp.Error.Code != protocol.ErrorCodeConflict || !strings.Contains(workerResp.Error.Message, "root="+rootID) || !strings.Contains(workerResp.Error.Message, "blockers="+blockerID) {
		t.Fatalf("worker response=%+v err=%v", workerResp.Error, err)
	}

	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	orchestratorBody, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	orchestratorResp, err := d.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: orchestratorBody})
	if err != nil || orchestratorResp.Error == nil || orchestratorResp.Error.Code != protocol.ErrorCodeConflict || !strings.Contains(orchestratorResp.Error.Message, "root="+rootID) || !strings.Contains(orchestratorResp.Error.Message, "blockers="+blockerID) {
		t.Fatalf("orchestrator response=%+v err=%v", orchestratorResp.Error, err)
	}

	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(runtimeStore).Get(ctx, identity); err != nil || found {
		t.Fatalf("lease=%+v found=%t err=%v", lease, found, err)
	}
	intents, err := runtimeStore.ListSessionIntentStates(ctx, projectID)
	if err != nil || len(intents) != 0 {
		t.Fatalf("intents=%+v err=%v", intents, err)
	}
	for _, command := range tmuxRunner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			t.Fatalf("runtime side effect: tmux=%v", tmuxRunner.commands)
		}
	}
	if worktreeRunner.worktreeAddCalls != 0 {
		t.Fatalf("worktree side effect: git=%+v", worktreeRunner)
	}
	root, err := issuesClient.GetWithRuntime(ctx, projectID, rootID)
	if err != nil || root.Status != domain.StatusOpen {
		t.Fatalf("root lifecycle=%s err=%v", root.Status, err)
	}
}

func TestAncestorRootBlockerPropagatesThroughSharedAuthorityAndActiveStartPaths(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	writer := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = writer.CloseDB() })
	if _, err := issuefixture.SeedPath(ctx, dbPath, issuefixture.Fixture{
		Issues: []issuefixture.Issue{
			{ID: "upstream-blocker", Title: "Active upstream blocker", Type: domain.TypeEpic, Status: domain.StatusInProgress},
			{ID: "parent-root", Title: "Blocked parent root", Type: domain.TypeEpic, Status: domain.StatusOpen},
			{ID: "nested-root", Title: "Nested root", Type: domain.TypeEpic, Status: domain.StatusOpen},
			{ID: "runnable-child", Title: "Otherwise runnable child", Type: domain.TypeTask, Status: domain.StatusOpen},
		},
		Dependencies: []issuefixture.Dependency{
			{IssueID: "nested-root", DependsOnID: "parent-root", Type: domain.DependencyParentChild},
			{IssueID: "runnable-child", DependsOnID: "parent-root", Type: domain.DependencyParentChild},
		},
	}); err != nil {
		t.Fatal(err)
	}
	reader := issues.NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = reader.CloseDB() })
	if err := reader.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	watchStore := &watchBarrierProjectionStore{projectionDeltaStore: reader, entered: make(chan struct{}), release: make(chan struct{})}
	materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(watchStore), nil)
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	tmuxRunner := newSessionStartTmuxRunner()
	worktreeRunner := &worktreeCreateRunner{worktreePath: filepath.Join(t.TempDir(), "parent-root")}
	manager := git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default())
	dReader := &Daemon{
		cfg:                       Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:                    reader,
		tmux:                      tmux.NewClient(tmuxRunner, slog.Default()),
		session:                   daemonhandlers.NewSessionHandler(daemonstate.NewStore()),
		sessionStore:              daemonstate.NewStore(),
		runtimeStoresByRoot:       map[string]*daemonstate.RuntimeStateStore{repoDir: runtimeStore},
		runtimeStoresByProject:    map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByRoot:    map[string]*git.WorktreeManager{repoDir: manager},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
		materializers:             map[string]*projectReadMaterializer{projectID: materializer},
		materializersStarted:      true,
		revision:                  map[string]uint64{},
		taskGraphReadinessCache:   map[string]taskGraphReadinessCacheEntry{},
		taskGraphReadinessLoads:   map[string]*taskGraphReadinessLoad{},
	}
	dReader.configureProjectReadMaterializer(materializer, projectID, reader)
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	materializer.done = make(chan struct{})
	go func() {
		defer close(materializer.done)
		materializer.run(ctx, nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-materializer.done
	})
	<-watchStore.entered

	dWriter := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, issues: writer, revision: map[string]uint64{}, hub: publish.NewHub(16, 8, slog.Default())}
	dependencyBody, _ := json.Marshal(map[string]any{"task_id": "parent-root", "depends_on_id": "upstream-blocker", "dependency_type": string(domain.DependencyBlocks)})
	dependencyResp, err := dWriter.handleTaskDependencyAdd(ctx, protocol.RequestEnvelope{Command: "task.dependency.add", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: dependencyBody})
	if err != nil || dependencyResp.Error != nil {
		t.Fatalf("second daemon dependency write: response=%+v err=%v", dependencyResp.Error, err)
	}

	waiting := make(chan struct{}, 1)
	readCtx := withProjectReadCurrentWaitHookForTest(ctx, func(gotProject string, _ uint64) {
		if gotProject != projectID {
			t.Errorf("wait project = %q, want %q", gotProject, projectID)
		}
		waiting <- struct{}{}
	})
	readinessDone := make(chan protocol.ResponseEnvelope, 1)
	readinessBody, _ := json.Marshal(map[string]string{"task_id": "parent-root"})
	go func() {
		resp, _ := dReader.handleTaskGraphReadiness(readCtx, protocol.RequestEnvelope{Command: "task.graph_readiness", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: readinessBody})
		readinessDone <- resp
	}()
	<-waiting
	close(watchStore.release)
	readinessResp := <-readinessDone
	if readinessResp.Error != nil {
		t.Fatalf("readiness response = %+v", readinessResp.Error)
	}
	var readiness taskGraphReadinessResult
	if err := json.Unmarshal(readinessResp.Body, &readiness); err != nil {
		t.Fatal(err)
	}
	if len(readiness.Runnable) != 0 || !slices.Equal(readiness.RootBlockers, []string{"upstream-blocker"}) || !strings.Contains(readiness.Blocked["runnable-child"], "root waiting on upstream-blocker") || !strings.Contains(readiness.Blocked["nested-root"], "root waiting on upstream-blocker") {
		t.Fatalf("ancestor-blocked readiness = %+v", readiness)
	}

	nestedScope, err := domain.RootedOrchestrationScope("nested-root")
	if err != nil {
		t.Fatal(err)
	}
	nestedBody, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: nestedScope})
	nestedWorkerBody, _ := json.Marshal(sessionCommandBody{ProjectID: projectID, IssueID: "nested-root", SessionID: "az-nested-root"})
	nestedWorkerResp, err := dReader.handleSessionStartDirect(ctx, protocol.RequestEnvelope{Command: daemonhandlers.CommandSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: nestedWorkerBody})
	if err != nil || nestedWorkerResp.Error == nil || nestedWorkerResp.Error.Code != protocol.ErrorCodeConflict || !strings.Contains(nestedWorkerResp.Error.Message, "requested=nested-root root=parent-root blockers=upstream-blocker") {
		t.Fatalf("nested worker response=%+v err=%v", nestedWorkerResp.Error, err)
	}
	nestedResp, err := dReader.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: nestedBody})
	if err != nil || nestedResp.Error == nil || nestedResp.Error.Code != protocol.ErrorCodeConflict || !strings.Contains(nestedResp.Error.Message, "requested=nested-root root=parent-root blockers=upstream-blocker") {
		t.Fatalf("nested orchestrator response=%+v err=%v", nestedResp.Error, err)
	}
	nestedIdentity, err := domain.NewOrchestratorIdentity(projectID, nestedScope)
	if err != nil {
		t.Fatal(err)
	}
	if lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(runtimeStore).Get(ctx, nestedIdentity); err != nil || found {
		t.Fatalf("blocked nested lease=%+v found=%t err=%v", lease, found, err)
	}
	if intents, err := runtimeStore.ListSessionIntentStates(ctx, projectID); err != nil || len(intents) != 0 {
		t.Fatalf("blocked nested intents=%+v err=%v", intents, err)
	}
	nestedTask, err := reader.GetWithRuntime(ctx, projectID, "nested-root")
	if err != nil || nestedTask.Status != domain.StatusOpen {
		t.Fatalf("blocked nested lifecycle=%s err=%v", nestedTask.Status, err)
	}
	rootTask, err := reader.GetWithRuntime(ctx, projectID, "parent-root")
	if err != nil || rootTask.Status != domain.StatusOpen {
		t.Fatalf("blocked ancestor lifecycle=%s err=%v", rootTask.Status, err)
	}
	if worktreeRunner.worktreeAddCalls != 0 || len(tmuxRunner.commands) != 0 {
		t.Fatalf("blocked nested start created side effects: worktree=%d tmux=%v", worktreeRunner.worktreeAddCalls, tmuxRunner.commands)
	}

	workerBody, _ := json.Marshal(sessionCommandBody{ProjectID: projectID, IssueID: "parent-root", SessionID: "az-parent-root"})
	workerResp, err := dReader.handleSessionStartDirect(ctx, protocol.RequestEnvelope{Command: daemonhandlers.CommandSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: workerBody})
	if err != nil || workerResp.Error == nil || workerResp.Error.Code != protocol.ErrorCodeConflict || !strings.Contains(workerResp.Error.Message, "root=parent-root blockers=upstream-blocker") {
		t.Fatalf("worker response=%+v err=%v", workerResp.Error, err)
	}
	scope, _ := domain.RootedOrchestrationScope("parent-root")
	orchestratorBody, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	orchestratorResp, err := dReader.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: orchestratorBody})
	if err != nil || orchestratorResp.Error == nil || orchestratorResp.Error.Code != protocol.ErrorCodeConflict || !strings.Contains(orchestratorResp.Error.Message, "root=parent-root blockers=upstream-blocker") {
		t.Fatalf("orchestrator response=%+v err=%v", orchestratorResp.Error, err)
	}
	if worktreeRunner.worktreeAddCalls != 0 || len(tmuxRunner.commands) != 0 {
		t.Fatalf("blocked starts created side effects: worktree=%d tmux=%v", worktreeRunner.worktreeAddCalls, tmuxRunner.commands)
	}
	if err := writer.Update(ctx, "upstream-blocker", domain.StatusDone); err != nil {
		t.Fatal(err)
	}
	settledResp, err := dReader.handleTaskGraphReadiness(ctx, protocol.RequestEnvelope{Command: "task.graph_readiness", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: readinessBody})
	if err != nil || settledResp.Error != nil {
		t.Fatalf("settled readiness response=%+v err=%v", settledResp.Error, err)
	}
	var settled taskGraphReadinessResult
	if err := json.Unmarshal(settledResp.Body, &settled); err != nil {
		t.Fatal(err)
	}
	if len(settled.RootBlockers) != 0 || !slices.Equal(settled.Runnable, []string{"runnable-child"}) {
		t.Fatalf("settled ancestor-blocked readiness = %+v", settled)
	}
	nestedWorkerResp, err = dReader.handleSessionStartDirect(ctx, protocol.RequestEnvelope{Command: daemonhandlers.CommandSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: nestedWorkerBody})
	if err != nil || nestedWorkerResp.Error != nil {
		t.Fatalf("settled nested start response=%+v err=%v", nestedWorkerResp.Error, err)
	}
	if lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(runtimeStore).Get(ctx, nestedIdentity); err != nil || !found || lease.SessionID == "" {
		t.Fatalf("settled nested lease=%+v found=%t err=%v", lease, found, err)
	}
}

func TestRootedStartFailsClosedForUnavailableForeignProjectBlocker(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	client := newMigratedIssueClientAtPath(t, dbPath, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	if _, err := issuefixture.SeedPath(ctx, dbPath, issuefixture.Fixture{Issues: []issuefixture.Issue{
		{ID: "local-root", Title: "Local root", Type: domain.TypeEpic, Status: domain.StatusOpen},
		{ID: "local-child", Title: "Local child", Type: domain.TypeTask, Status: domain.StatusOpen},
	}, Dependencies: []issuefixture.Dependency{{IssueID: "local-child", DependsOnID: "local-root", Type: domain.DependencyParentChild}}}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO issue_dependencies(issue_id,depends_on_id,dependency_type,tombstoned_at) VALUES('local-root','foreign-project-blocker','blocks',NULL)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	materializer := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(client), nil)
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, issues: client, tmux: tmux.NewClient(newSessionStartTmuxRunner(), slog.Default()), materializers: map[string]*projectReadMaterializer{projectID: materializer}, materializersStarted: true, runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{repoDir: runtimeStore}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore}, revision: map[string]uint64{}, taskGraphReadinessCache: map[string]taskGraphReadinessCacheEntry{}, taskGraphReadinessLoads: map[string]*taskGraphReadinessLoad{}}
	d.configureProjectReadMaterializer(materializer, projectID, client)
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	readinessBody, _ := json.Marshal(map[string]string{"task_id": "local-root"})
	readinessResp, err := d.handleTaskGraphReadiness(ctx, protocol.RequestEnvelope{Command: "task.graph_readiness", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: readinessBody})
	if err != nil || readinessResp.Error != nil {
		t.Fatalf("readiness response=%+v err=%v", readinessResp.Error, err)
	}
	var readiness taskGraphReadinessResult
	if err := json.Unmarshal(readinessResp.Body, &readiness); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(readiness.RootBlockers, []string{"foreign-project-blocker(missing)"}) || !strings.Contains(readiness.Blocked["local-child"], "foreign-project-blocker(missing)") {
		t.Fatalf("foreign unavailable readiness = %+v", readiness)
	}
	scope, _ := domain.RootedOrchestrationScope("local-root")
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	resp, err := d.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeConflict || !strings.Contains(resp.Error.Message, "foreign-project-blocker(missing)") {
		t.Fatalf("foreign unavailable start response=%+v err=%v", resp.Error, err)
	}
}

func TestRootedRestartAfterCallerCancellationSerializesAndAcknowledgesReplacement(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	rootID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Coordinate exact-scope restart", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+rootID)
	manager := git.NewWorktreeManager(&worktreeCreateRunner{worktreePath: worktreePath, branchName: "test/" + rootID}, repoDir, slog.Default())
	runtimePath := filepath.Join(repoDir, "runtime.db")
	firstStore := daemonstate.NewRuntimeStateStoreAtPath(runtimePath, slog.Default())
	secondStore := daemonstate.NewRuntimeStateStoreAtPath(runtimePath, slog.Default())
	t.Cleanup(func() { _ = firstStore.Close() })
	t.Cleanup(func() { _ = secondStore.Close() })
	runner := newSessionStartTmuxRunner()
	newDaemon := func(store *daemonstate.RuntimeStateStore) *Daemon {
		memoryStore := daemonstate.NewStore()
		return &Daemon{
			cfg:    Config{RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
			issues: issuesClient, tmux: tmux.NewClient(runner, slog.Default()), session: daemonhandlers.NewSessionHandler(memoryStore), sessionStore: memoryStore,
			runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{repoDir: store}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
			worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: manager}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager}, revision: map[string]uint64{},
		}
	}
	first, second := newDaemon(firstStore), newDaemon(secondStore)
	scope, err := domain.RootedOrchestrationScope(rootID)
	if err != nil {
		t.Fatal(err)
	}
	body := marshalJSON(protocol.OrchestratorSessionRequest{Scope: scope})
	startRequest := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body}
	acknowledgeManagedAgentOnInitialLaunch(t, first, runner, projectID)
	startResponse, err := first.handleOrchestratorSession(ctx, startRequest)
	if err != nil || startResponse.Error != nil {
		t.Fatalf("initial rooted start: response=%+v err=%v", startResponse.Error, err)
	}
	var started protocol.OrchestratorSessionResult
	if err := json.Unmarshal(startResponse.Body, &started); err != nil {
		t.Fatal(err)
	}

	replacementPaused := make(chan struct{})
	releaseReplacement := make(chan struct{})
	restartBaseCtx, cancelRestart := context.WithCancel(context.Background())
	t.Cleanup(cancelRestart)
	var progressMu sync.Mutex
	var progressPhases []string
	restartCtx := daemonops.WithProgressReporter(restartBaseCtx, func(progressCtx context.Context, progress daemonops.Progress) error {
		if progress.Phase == "session.restart_all.complete" && progressCtx.Err() != nil {
			t.Errorf("complete progress used canceled caller context: %v", progressCtx.Err())
		}
		progressMu.Lock()
		progressPhases = append(progressPhases, progress.Phase)
		progressMu.Unlock()
		return nil
	})
	updateReplacement := seedManagedRestartIdentity(t, first, runner, projectID, started.SessionID)
	runner.onRespawnPane = func(ctx context.Context, args []string) error {
		close(replacementPaused)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseReplacement:
		}
		if err := updateReplacement(ctx, args); err != nil {
			return err
		}
		cancelRestart()
		return context.Canceled
	}
	restartRequest := protocol.RequestEnvelope{Command: protocol.CommandSessionRestartAll, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: marshalJSON(protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID(projectID), ForceBusy: true})}
	type commandResult struct {
		response protocol.ResponseEnvelope
		err      error
	}
	restartDone := make(chan commandResult, 1)
	inputsBefore := len(runner.inputPayloads)
	go func() {
		response, err := first.handleSessionRestartAll(restartCtx, restartRequest)
		restartDone <- commandResult{response: response, err: err}
	}()
	select {
	case <-replacementPaused:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not reach replacement boundary")
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancelBlocked := context.WithCancel(ctx)
	cancelBlocked()
	enteredLockedTransition := false
	blockedErr := secondStore.WithOrchestratorScopeTransition(blockedCtx, identity, func(context.Context) error {
		enteredLockedTransition = true
		return nil
	})
	if !errors.Is(blockedErr, context.Canceled) || enteredLockedTransition {
		t.Fatalf("concurrent rooted transition exclusion: entered=%t err=%v", enteredLockedTransition, blockedErr)
	}
	close(releaseReplacement)
	var restartResult commandResult
	select {
	case restartResult = <-restartDone:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not finish")
	}
	if restartResult.err != nil || restartResult.response.Error != nil {
		t.Fatalf("restart result: response=%+v err=%v", restartResult.response.Error, restartResult.err)
	}
	attachResponse, attachErr := second.handleOrchestratorSession(ctx, startRequest)
	attachResult := commandResult{response: attachResponse, err: attachErr}
	if attachResult.err != nil || attachResult.response.Error != nil {
		t.Fatalf("attach result: response=%+v err=%v", attachResult.response.Error, attachResult.err)
	}
	if got := len(runner.inputPayloads) - inputsBefore; got != 1 {
		t.Fatalf("rooted replacement prompt deliveries = %d, want one acknowledged replacement", got)
	}
	ack, found, err := daemonstate.NewRootedBootstrapAcknowledgementAuthority(secondStore).Get(ctx, identity)
	if err != nil || !found || ack.SessionID != started.SessionID || ack.RuntimeNonce == "" {
		t.Fatalf("post-restart acknowledgement = %+v found=%t err=%v", ack, found, err)
	}
	if got := runner.env[started.SessionID][rootedOrchestratorBootstrapNonceEnvironment]; got != ack.RuntimeNonce {
		t.Fatalf("live marker = %q, durable acknowledgement = %q", got, ack.RuntimeNonce)
	}
	progressMu.Lock()
	gotProgressPhases := append([]string(nil), progressPhases...)
	progressMu.Unlock()
	if len(gotProgressPhases) == 0 {
		t.Fatal("restart persisted no progress checkpoints")
	}
	if got := gotProgressPhases[len(gotProgressPhases)-1]; got != "session.restart_all.batch.completed" {
		t.Fatalf("last progress phase = %q, want exact complete checkpoint; phases=%v", got, gotProgressPhases)
	}
}

func TestRealProcessProfileRootedMarkerSurvivesPaneChildReplacement(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	tmuxDir, err := os.MkdirTemp("/tmp", "az-die-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmuxDir) })
	runner := &isolatedTmuxTestRunner{tmuxPath: tmuxPath, socketPath: filepath.Join(tmuxDir, "server.sock")}
	childPIDPath := filepath.Join(tmuxDir, "child.pid")
	childReadyPrefix := "rooted-replacement-child-ready"
	childLoopPath := filepath.Join(tmuxDir, "child-loop.sh")
	childLoop := "#!/bin/sh\n" +
		"generation=1\n" +
		"while :; do\n" +
		"  " + singleQuoteForShell(tmuxPath) + " -S " + singleQuoteForShell(runner.socketPath) + " wait-for " + singleQuoteForShell(childReadyPrefix) + "-release-\"$generation\" &\n" +
		"  child=$!\n" +
		"  printf '%s\\n' \"$child\" > " + singleQuoteForShell(childPIDPath) + "\n" +
		"  " + singleQuoteForShell(tmuxPath) + " -S " + singleQuoteForShell(runner.socketPath) + " wait-for -S " + singleQuoteForShell(childReadyPrefix) + "-\"$generation\"\n" +
		"  wait \"$child\"\n" +
		"  generation=$((generation + 1))\n" +
		"done\n"
	if err := os.WriteFile(childLoopPath, []byte(childLoop), 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := runner.run(
		context.Background(),
		"-f", "/dev/null",
		"new-session", "-d", "-s", "rooted-replacement", singleQuoteForShell(childLoopPath),
		";", "set-option", "-s", "exit-empty", "off",
	); err != nil {
		t.Fatalf("start isolated tmux: %v (%s)", err, output)
	}
	t.Cleanup(func() { _, _ = runner.run(context.Background(), "kill-server") })
	client := tmux.NewClient(runner, slog.Default())
	if err := client.SetEnvironment(context.Background(), "rooted-replacement", rootedOrchestratorBootstrapNonceEnvironment, "durable-marker"); err != nil {
		t.Fatal(err)
	}
	childPID := func(readyChannel string) string {
		t.Helper()
		if output, err := runner.run(context.Background(), "wait-for", readyChannel); err != nil {
			t.Fatalf("wait for pane child on %q: %v (%s)", readyChannel, err, output)
		}
		pid, err := os.ReadFile(childPIDPath)
		if err != nil {
			t.Fatalf("read pane child pid: %v", err)
		}
		return strings.TrimSpace(string(pid))
	}
	firstChild := childPID(childReadyPrefix + "-1")
	if firstChild == "" {
		t.Fatal("first pane child did not start")
	}
	if output, err := runner.run(context.Background(), "wait-for", "-S", childReadyPrefix+"-release-1"); err != nil {
		t.Fatalf("release first pane child: %v (%s)", err, output)
	}
	secondChild := childPID(childReadyPrefix + "-2")
	if secondChild == "" || secondChild == firstChild {
		t.Fatalf("replacement child pid = %q, first = %q", secondChild, firstChild)
	}
	marker, found, err := client.EnvironmentValue(context.Background(), "rooted-replacement", rootedOrchestratorBootstrapNonceEnvironment)
	if err != nil || !found || marker != "durable-marker" {
		t.Fatalf("marker after child replacement = %q found=%t err=%v", marker, found, err)
	}
}

func TestProjectOrchestratorLaunchUsesInheritedAzForInitialHookAndBackgroundCommands(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	managedDir := filepath.Join(base, "install", ".azedarach-generations", "generation.current")
	staleDir := filepath.Join(repoDir, "bin")
	toolDir := filepath.Join(base, "tools")
	for _, dir := range []string{repoDir, managedDir, staleDir, toolDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tracePath := filepath.Join(base, "trace")
	writeExecutable := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, binary := range []string{"az", "azd"} {
		writeExecutable(filepath.Join(managedDir, binary), "printf 'fresh-"+binary+"|%s\\n' \"$*\" >> \"$TRACE\"\n")
		writeExecutable(filepath.Join(staleDir, binary), "printf 'STALE-"+binary+"|%s\\n' \"$*\" >> \"$TRACE\"\n")
	}
	writeExecutable(filepath.Join(toolDir, "codex"), "az prime\n(az background-child) &\nwait\n")

	projectID := "project-managed-path"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(base, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, CLITool: "codex", SessionShell: "/bin/sh", ManagedGenerationBinDir: managedDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		tmux:                   tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
	body, err := json.Marshal(protocol.OrchestratorSessionRequest{Scope: domain.ProjectOrchestrationScope()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil || resp.Error != nil {
		t.Fatalf("start project orchestrator: response=%+v err=%v", resp.Error, err)
	}
	var result protocol.OrchestratorSessionResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	launchCommand := requireNewSessionLaunchCommand(t, runner, result.SessionID)
	// The tmux fixture removes one-shot artifacts to model successful handoff;
	// recreate its captured bytes for this real-process assertion.
	artifactCopy := filepath.Join(t.TempDir(), "orchestrator-launch.sh")
	if err := os.WriteFile(artifactCopy, []byte(runner.launchScriptContents[result.SessionID]), 0o700); err != nil {
		t.Fatal(err)
	}
	launchCommand = strings.Replace(launchCommand, runner.launchScriptPaths[result.SessionID], artifactCopy, 1)
	cmd := exec.Command("/bin/sh", "-c", launchCommand)
	cmd.Env = append(os.Environ(), "PATH="+managedDir+string(os.PathListSeparator)+staleDir+string(os.PathListSeparator)+toolDir+":/usr/bin:/bin", "TRACE="+tracePath)
	cmd.Stdin = strings.NewReader("exit\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execute project orchestrator launch: %v\n%s\n%s", err, output, launchCommand)
	}
	traceBytes, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(traceBytes)
	if strings.Contains(trace, "STALE-") {
		t.Fatalf("project orchestrator used stale binary:\n%s", trace)
	}
	for _, want := range []string{"fresh-az|prime", "fresh-az|background-child", "fresh-az|ai hook run --agent=codex session_end"} {
		if !strings.Contains(trace, want) {
			t.Fatalf("project orchestrator trace missing %q:\n%s", want, trace)
		}
	}
}

func TestOrchestratorSessionStopPausesPreservesCursorAndIsolatesScopes(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	const projectID = "project-stop"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{
		cfg:                          Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeStoresByProject:       map[string]*daemonstate.RuntimeStateStore{projectID: store},
		tmux:                         tmux.NewClient(runner, slog.Default()),
		orchestratorStopGracePeriod:  time.Millisecond,
		orchestratorStopPollInterval: time.Millisecond,
		hub:                          publish.NewHub(32, 32, slog.Default()),
	}
	acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
	events, cancelEvents := d.hub.Subscribe(projectID, 0)
	defer cancelEvents()
	projectScope := domain.ProjectOrchestrationScope()
	rootScope, err := domain.RootedOrchestrationScope("az-other")
	if err != nil {
		t.Fatal(err)
	}
	projectIdentity, _ := domain.NewOrchestratorIdentity(projectID, projectScope)
	rootIdentity, _ := domain.NewOrchestratorIdentity(projectID, rootScope)
	projectSession, rootSession := d.orchestratorSessionID(projectID, projectScope), "root-orchestrator"
	runner.sessions[projectSession], runner.sessions[rootSession] = true, true
	authority := daemonstate.NewOrchestratorLeaseAuthority(store)
	if _, err := authority.Acquire(ctx, projectIdentity, projectSession, d.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AdvanceCursor(ctx, projectIdentity, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Acquire(ctx, rootIdentity, rootSession, d.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	for scope, sessionID := range map[domain.OrchestrationScope]string{projectScope: projectSession, rootScope: rootSession} {
		if err := d.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: projectID}, projectID, scope, sessionID); err != nil {
			t.Fatal(err)
		}
	}
	mismatchBody, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: projectScope, ExpectedSessionID: "replacement-session"})
	mismatchResp, mismatchErr := d.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStop, Meta: protocol.Metadata{ProjectID: projectID}, Body: mismatchBody})
	if mismatchErr != nil || mismatchResp.Error == nil || mismatchResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("mismatched expected session: response=%+v err=%v", mismatchResp.Error, mismatchErr)
	}
	projectLeaseBeforeStop, found, err := authority.Get(ctx, projectIdentity)
	if err != nil || !found || projectLeaseBeforeStop.Lifecycle != domain.OrchestratorWorking || !runner.sessions[projectSession] {
		t.Fatalf("mismatched stop mutated lease/runtime: lease=%+v live=%t found=%t err=%v", projectLeaseBeforeStop, runner.sessions[projectSession], found, err)
	}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: projectScope})
	req := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStop, Meta: protocol.Metadata{ProjectID: projectID}, Body: body}
	resp, err := d.handleOrchestratorSession(ctx, req)
	if err != nil || resp.Error != nil {
		t.Fatalf("stop: response=%+v err=%v", resp.Error, err)
	}
	var result protocol.OrchestratorSessionResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Disposition != "stopped-forced" || result.Live || !result.Forced || result.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("stop result = %+v", result)
	}
	publishedStopped := false
	eventDeadline := time.NewTimer(time.Second)
	defer eventDeadline.Stop()
	for !publishedStopped {
		var event protocol.EventEnvelope
		select {
		case event = <-events:
		case <-eventDeadline.C:
			t.Fatal("stop did not publish a stopped orchestrator session event")
		}
		if event.Event != protocol.EventSessionUpdated {
			continue
		}
		var eventBody protocol.SessionProjectionEventBody
		if err := json.Unmarshal(event.Body, &eventBody); err != nil {
			t.Fatal(err)
		}
		if eventBody.Session.Role == protocol.SessionRoleOrchestrator && eventBody.Session.ScopeID == "project" && eventBody.Session.State == protocol.SessionLifecycleStateStopped {
			if eventBody.Runtime != nil {
				t.Fatalf("rootless session event carried issue runtime projection: %+v", eventBody.Runtime)
			}
			publishedStopped = true
		}
	}
	projectLease, found, err := authority.Get(ctx, projectIdentity)
	if err != nil || !found || projectLease.Lifecycle != domain.OrchestratorPaused || projectLease.Cursor != 42 {
		t.Fatalf("project lease = %+v found=%t err=%v", projectLease, found, err)
	}
	rootLease, found, err := authority.Get(ctx, rootIdentity)
	if err != nil || !found || rootLease.Lifecycle != domain.OrchestratorWorking || !runner.sessions[rootSession] {
		t.Fatalf("root lease/runtime changed = %+v live=%t found=%t err=%v", rootLease, runner.sessions[rootSession], found, err)
	}
	projection, found, err := store.GetSessionState(ctx, projectID, projectSession)
	if err != nil || !found || projection.State != daemonstate.SessionStateStopped || projection.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("stopped projection = %+v found=%t err=%v", projection, found, err)
	}
	if stopped, err := orchestratorLeaseHasStoppedSessionIntent(ctx, store, projectLease); err != nil || !stopped {
		t.Fatalf("durable explicit stop intent = %t err=%v", stopped, err)
	}
	resp, err = d.handleOrchestratorSession(ctx, req)
	if err != nil || resp.Error != nil {
		t.Fatalf("idempotent stop: response=%+v err=%v", resp.Error, err)
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil || result.Disposition != "already-stopped" {
		t.Fatalf("idempotent result = %+v err=%v", result, err)
	}

	req.Command = protocol.CommandOrchestratorSessionStart
	resp, err = d.handleOrchestratorSession(ctx, req)
	if err != nil || resp.Error != nil {
		t.Fatalf("restart: response=%+v err=%v", resp.Error, err)
	}
	result = protocol.OrchestratorSessionResult{}
	if err := json.Unmarshal(resp.Body, &result); err != nil || result.Disposition != "resumed" || !result.Live || result.Lifecycle != domain.OrchestratorWorking {
		t.Fatalf("restart result = %+v err=%v", result, err)
	}
	projectLease, _, _ = authority.Get(ctx, projectIdentity)
	if projectLease.Cursor != 42 {
		t.Fatalf("restart cursor = %d, want 42", projectLease.Cursor)
	}
	req.Command = protocol.CommandOrchestratorSessionStop
	if resp, err = d.handleOrchestratorSession(ctx, req); err != nil || resp.Error != nil {
		t.Fatalf("second stop: response=%+v err=%v", resp.Error, err)
	}
	runner.newSessionErr = errors.New("launch failed")
	req.Command = protocol.CommandOrchestratorSessionStart
	resp, err = d.handleOrchestratorSession(ctx, req)
	if err != nil || resp.Error == nil {
		t.Fatalf("failed restart: response=%+v err=%v", resp.Error, err)
	}
	projectLease, found, err = authority.Get(ctx, projectIdentity)
	if err != nil || !found || projectLease.Lifecycle != domain.OrchestratorPaused || projectLease.Cursor != 42 {
		t.Fatalf("failed restart lease = %+v found=%t err=%v", projectLease, found, err)
	}
	rootBody, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: rootScope})
	rootResp, err := d.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStop, Meta: protocol.Metadata{ProjectID: projectID}, Body: rootBody})
	if err != nil || rootResp.Error != nil {
		t.Fatalf("rooted stop: response=%+v err=%v", rootResp.Error, err)
	}
	rootLease, found, err = authority.Get(ctx, rootIdentity)
	if err != nil || !found || rootLease.Lifecycle != domain.OrchestratorPaused || runner.sessions[rootSession] {
		t.Fatalf("rooted stop lease/runtime = %+v live=%t found=%t err=%v", rootLease, runner.sessions[rootSession], found, err)
	}
}

func TestConcurrentStopThenStartAcrossDaemonsConvergesToStartWinner(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	dbPath := filepath.Join(repoDir, "runtime.db")
	const projectID = "project-concurrent-stop-start"
	firstStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	secondStore := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		_ = firstStore.Close()
		_ = secondStore.Close()
	})
	runner := newSessionStartTmuxRunner()
	stopIntentPersisted := make(chan struct{})
	releaseStop := make(chan struct{})
	first := &Daemon{
		cfg:                                  Config{RepoDir: repoDir, Logger: slog.Default()},
		runtimeStoresByProject:               map[string]*daemonstate.RuntimeStateStore{projectID: firstStore},
		tmux:                                 tmux.NewClient(runner, slog.Default()),
		orchestratorStopGracePeriod:          time.Millisecond,
		orchestratorStopPollInterval:         time.Millisecond,
		orchestratorStopAfterIntentPersisted: func() { close(stopIntentPersisted); <-releaseStop },
	}
	second := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: slog.Default()},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: secondStore},
		tmux:                   tmux.NewClient(runner, slog.Default()),
	}
	acknowledgeManagedAgentOnInitialLaunch(t, second, runner, projectID)
	scope := domain.ProjectOrchestrationScope()
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := first.orchestratorSessionID(projectID, scope)
	runner.sessions[sessionID] = true
	runner.onSendKeys = func(target, _ string) {
		delete(runner.sessions, target)
	}
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(firstStore).Acquire(ctx, identity, sessionID, first.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	if err := first.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: projectID}, projectID, scope, sessionID); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	stopReq := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStop, Meta: protocol.Metadata{ProjectID: projectID}, Body: body}
	startReq := stopReq
	startReq.Command = protocol.CommandOrchestratorSessionStart
	type commandResult struct {
		response protocol.ResponseEnvelope
		err      error
	}
	stopDone := make(chan commandResult, 1)
	go func() {
		response, commandErr := first.handleOrchestratorSession(ctx, stopReq)
		stopDone <- commandResult{response: response, err: commandErr}
	}()
	<-stopIntentPersisted

	startDone := make(chan commandResult, 1)
	go func() {
		response, commandErr := second.handleOrchestratorSession(ctx, startReq)
		startDone <- commandResult{response: response, err: commandErr}
	}()
	select {
	case result := <-startDone:
		t.Fatalf("concurrent start bypassed held exact-scope transition: response=%+v err=%v", result.response.Error, result.err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseStop)

	stopResult := <-stopDone
	if stopResult.err != nil || stopResult.response.Error != nil {
		t.Fatalf("stop response=%+v err=%v", stopResult.response.Error, stopResult.err)
	}
	startResult := <-startDone
	if startResult.err != nil || startResult.response.Error != nil {
		t.Fatalf("start response=%+v err=%v", startResult.response.Error, startResult.err)
	}

	lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(secondStore).Get(ctx, identity)
	if err != nil || !found || lease.Lifecycle != domain.OrchestratorWorking || lease.SessionID != sessionID {
		t.Fatalf("final lease = %+v found=%t err=%v", lease, found, err)
	}
	projection, found, err := secondStore.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleOrchestrator, daemonstate.SessionScopeOrchestration, "project")
	if err != nil || !found || projection.ID != sessionID || projection.State != daemonstate.SessionStateRunning || projection.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("final desired projection = %+v found=%t err=%v", projection, found, err)
	}
	if !runner.sessions[sessionID] {
		t.Fatalf("final runtime %q is not live", sessionID)
	}
}

func TestOrchestratorSessionStatusReportsMissingRuntimeWithoutMutating(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	const projectID, sessionID = "project-repair", "project-orchestrator"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}, tmux: tmux.NewClient(runner, slog.Default())}
	scope := domain.ProjectOrchestrationScope()
	identity, _ := domain.NewOrchestratorIdentity(projectID, scope)
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).Acquire(ctx, identity, sessionID, d.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	if err := d.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: projectID}, projectID, scope, sessionID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	resp, err := d.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStatus, Meta: protocol.Metadata{ProjectID: projectID}, Body: body})
	if err != nil || resp.Error != nil {
		t.Fatalf("status repair: response=%+v err=%v", resp.Error, err)
	}
	var result protocol.OrchestratorSessionResult
	if err := json.Unmarshal(resp.Body, &result); err != nil || result.Disposition != "stale-runtime" || result.Lifecycle != domain.OrchestratorWorking || result.Live {
		t.Fatalf("status result = %+v err=%v", result, err)
	}
}

func TestOrchestratorSessionStopCanExitGracefully(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	const projectID = "project-graceful-stop"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}, tmux: tmux.NewClient(runner, slog.Default()), orchestratorStopGracePeriod: 50 * time.Millisecond, orchestratorStopPollInterval: time.Millisecond}
	scope := domain.ProjectOrchestrationScope()
	sessionID := d.orchestratorSessionID(projectID, scope)
	runner.sessions[sessionID] = true
	runner.onSendKeys = func(target, payload string) {
		if target == sessionID && payload == "Enter" {
			delete(runner.sessions, sessionID)
		}
	}
	identity, _ := domain.NewOrchestratorIdentity(projectID, scope)
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).Acquire(ctx, identity, sessionID, d.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	if err := d.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: projectID}, projectID, scope, sessionID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	resp, err := d.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStop, Meta: protocol.Metadata{ProjectID: projectID}, Body: body})
	if err != nil || resp.Error != nil {
		t.Fatalf("graceful stop: response=%+v err=%v", resp.Error, err)
	}
	var result protocol.OrchestratorSessionResult
	if err := json.Unmarshal(resp.Body, &result); err != nil || result.Disposition != "stopped" || result.Forced {
		t.Fatalf("graceful result = %+v err=%v", result, err)
	}
	if len(runner.inputPayloads) != 1 || runner.inputPayloads[0] != "/exit" {
		t.Fatalf("graceful agent exit payloads = %+v", runner.inputPayloads)
	}
	for _, command := range runner.commands {
		if len(command) > 0 && command[0] == "kill-session" {
			t.Fatalf("graceful stop used forced kill: %+v", runner.commands)
		}
	}
}

func TestStoppedOrchestratorIntentSuppressesAutomaticLifecycleWake(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	const projectID, sessionID = "project-manual-pause", "project-orchestrator"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}, tmux: tmux.NewClient(runner, slog.Default())}
	scope := domain.ProjectOrchestrationScope()
	identity, _ := domain.NewOrchestratorIdentity(projectID, scope)
	authority := daemonstate.NewOrchestratorLeaseAuthority(store)
	if _, err := authority.Acquire(ctx, identity, sessionID, d.tmux.HasSession); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.SetLifecycle(ctx, identity, sessionID, domain.OrchestratorPaused); err != nil {
		t.Fatal(err)
	}
	if err := d.persistStoppedOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: projectID}, projectID, scope, sessionID, daemonstate.SessionStateStopped); err != nil {
		t.Fatal(err)
	}
	if err := d.reconcileOrchestratorLifecycles(ctx, projectID, time.Now().UTC()); err != nil {
		t.Fatalf("manual pause reached automatic lifecycle facts: %v", err)
	}
	if err := d.wakePausedOrchestratorsForRecovery(ctx, projectID, time.Now().UTC()); err != nil {
		t.Fatalf("manual pause recovery wake: %v", err)
	}
	lease, found, err := authority.Get(ctx, identity)
	if err != nil || !found || lease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("manual pause lease = %+v found=%t err=%v", lease, found, err)
	}
}

func TestOrchestratorSessionStopIsolatesRegisteredProjects(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	stores := map[string]*daemonstate.RuntimeStateStore{}
	for _, projectID := range []string{"project-a", "project-b"} {
		store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, projectID+".db"), slog.Default())
		stores[projectID] = store
		t.Cleanup(func() { _ = store.Close() })
	}
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, runtimeStoresByProject: stores, tmux: tmux.NewClient(runner, slog.Default()), orchestratorStopGracePeriod: time.Millisecond, orchestratorStopPollInterval: time.Millisecond}
	scope := domain.ProjectOrchestrationScope()
	for _, projectID := range []string{"project-a", "project-b"} {
		sessionID := projectID + "-orchestrator"
		runner.sessions[sessionID] = true
		identity, _ := domain.NewOrchestratorIdentity(projectID, scope)
		if _, err := daemonstate.NewOrchestratorLeaseAuthority(stores[projectID]).Acquire(ctx, identity, sessionID, d.tmux.HasSession); err != nil {
			t.Fatal(err)
		}
		if err := d.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, scope, sessionID); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(protocol.OrchestratorSessionRequest{Scope: scope})
	resp, err := d.handleOrchestratorSession(ctx, protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStop, Meta: protocol.Metadata{ProjectID: "project-a"}, Body: body})
	if err != nil || resp.Error != nil {
		t.Fatalf("project-a stop: response=%+v err=%v", resp.Error, err)
	}
	identityB, _ := domain.NewOrchestratorIdentity("project-b", scope)
	leaseB, found, err := daemonstate.NewOrchestratorLeaseAuthority(stores["project-b"]).Get(ctx, identityB)
	if err != nil || !found || leaseB.Lifecycle != domain.OrchestratorWorking || !runner.sessions["project-b-orchestrator"] {
		t.Fatalf("project-b changed: lease=%+v live=%t found=%t err=%v", leaseB, runner.sessions["project-b-orchestrator"], found, err)
	}
}

func TestRootlessOrchestratorSessionEndHookRecordsPausedStopIntent(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	const projectID, sessionID = "project-hook-stop", "project-orchestrator"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}}
	scope := domain.ProjectOrchestrationScope()
	identity, _ := domain.NewOrchestratorIdentity(projectID, scope)
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).Acquire(ctx, identity, sessionID, func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err := d.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: projectID}, projectID, scope, sessionID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(protocol.RuntimeSignalIngestCommandBody{Source: protocol.RuntimeSignalSourceAgentHook, Kind: protocol.RuntimeSignalKindAgentActivityChanged, ProjectID: projectID, SessionID: sessionID, Event: "session_end", Agent: "codex"})
	resp, err := d.handleRuntimeSignalIngest(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: projectID}, Body: body})
	if err != nil || resp.Error != nil {
		t.Fatalf("session-end hook: response=%+v err=%v", resp.Error, err)
	}
	var hookResult protocol.RuntimeSignalIngestResponseBody
	if err := json.Unmarshal(resp.Body, &hookResult); err != nil {
		t.Fatal(err)
	}
	for _, stage := range hookResult.Stages {
		if stage.Name == "orchestrator_continuation" {
			t.Fatalf("ended orchestrator was immediately reconsidered for continuation: %+v", hookResult.Stages)
		}
	}
	lease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, identity)
	if err != nil || !found || lease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("lease = %+v found=%t err=%v", lease, found, err)
	}
	projection, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || projection.State != daemonstate.SessionStateStopped || projection.ObservedState != daemonstate.SessionStatePaused {
		t.Fatalf("projection = %+v found=%t err=%v", projection, found, err)
	}

	rootScope, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	rootSessionID := "root-orchestrator"
	rootIdentity, _ := domain.NewOrchestratorIdentity(projectID, rootScope)
	if _, err := daemonstate.NewOrchestratorLeaseAuthority(store).Acquire(ctx, rootIdentity, rootSessionID, func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err := d.persistOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: projectID}, projectID, rootScope, rootSessionID); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(protocol.RuntimeSignalIngestCommandBody{Source: protocol.RuntimeSignalSourceAgentHook, Kind: protocol.RuntimeSignalKindAgentActivityChanged, ProjectID: projectID, IssueID: "az-root", SessionID: rootSessionID + ".pane-7", Event: "session_end", Agent: "codex"})
	resp, err = d.handleRuntimeSignalIngest(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: projectID}, Body: body})
	if err != nil || resp.Error != nil {
		t.Fatalf("rooted session-end hook: response=%+v err=%v", resp.Error, err)
	}
	rootLease, found, err := daemonstate.NewOrchestratorLeaseAuthority(store).Get(ctx, rootIdentity)
	if err != nil || !found || rootLease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("rooted lease = %+v found=%t err=%v", rootLease, found, err)
	}
	rootProjection, found, err := store.GetSessionState(ctx, projectID, rootSessionID)
	if err != nil || !found || rootProjection.State != daemonstate.SessionStateStopped || rootProjection.ObservedState != daemonstate.SessionStatePaused {
		t.Fatalf("rooted projection = %+v found=%t err=%v", rootProjection, found, err)
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
	acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
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
	identity, _ := domain.NewOrchestratorIdentity(projectID, domain.ProjectOrchestrationScope())
	authority := daemonstate.NewOrchestratorLeaseAuthority(store)
	if err := d.persistStoppedOrchestratorSessionProjection(ctx, protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, projectID, domain.ProjectOrchestrationScope(), sessionID, daemonstate.SessionStatePaused); err != nil {
		t.Fatal(err)
	}
	result, err = d.reconcileTmuxAndDaemonSessions(ctx, projectID, "")
	if err != nil {
		t.Fatal(err)
	}
	if runner.sessions[sessionID] || result.AlignedDaemonSessions != 1 {
		t.Fatalf("stopped intent residual cleanup result=%+v live=%v", result, runner.sessions[sessionID])
	}
	cleanedLease, found, err := authority.Get(ctx, identity)
	if err != nil || !found || cleanedLease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("stopped intent cleanup lease=%+v found=%t err=%v", cleanedLease, found, err)
	}
	response, err = d.handleOrchestratorSession(ctx, request)
	if err != nil || response.Error != nil {
		t.Fatalf("restart after residual cleanup: response=%+v err=%v", response.Error, err)
	}
	delete(runner.sessions, sessionID)
	if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: time.Now().UTC().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	result, err = d.reconcileTmuxAndDaemonSessions(ctx, projectID, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.RecreatedTmuxSessions != 0 || runner.sessions[sessionID] {
		t.Fatalf("interrupted exit was recreated: result=%+v live=%v", result, runner.sessions[sessionID])
	}
	lease, found, err := authority.Get(ctx, identity)
	if err != nil || !found || lease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("reconciled interrupted exit lease=%+v found=%t err=%v", lease, found, err)
	}
	projection, found, err = store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || projection.State != daemonstate.SessionStateStopped || projection.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("reconciled interrupted exit projection=%+v found=%t err=%v", projection, found, err)
	}
}
