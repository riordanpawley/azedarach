package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
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
	managedDir := filepath.Join(t.TempDir(), ".azedarach-generations", "generation.current")
	staleDir := filepath.Join(repoDir, "bin")
	t.Setenv("PATH", staleDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, ManagedGenerationBinDir: managedDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
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
