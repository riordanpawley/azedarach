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
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
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
	d.sessionResumeWait = immediateSessionResumeWait
	restartRequest := protocol.RequestEnvelope{
		Command: protocol.CommandSessionRestartAll,
		Meta:    protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:    marshalJSON(protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID(projectID)}),
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

	// Cancellation after the interrupt leaves durable acknowledgement absent,
	// so a later rooted start repairs whichever process survived.
	var replacementWaits int
	d.sessionResumeWait = func(context.Context, time.Duration) error {
		replacementWaits++
		return context.Canceled
	}
	cancelledResponse, err := d.handleSessionRestartAll(ctx, restartRequest)
	if err != nil || cancelledResponse.Error != nil {
		t.Fatalf("cancel rooted replacement: response=%+v err=%v", cancelledResponse.Error, err)
	}
	var cancelledResult protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(cancelledResponse.Body, &cancelledResult); err != nil {
		t.Fatal(err)
	}
	if replacementWaits != 1 || cancelledResult.Restarted != 0 || cancelledResult.Failed != 1 {
		t.Fatalf("cancelled rooted replacement result = %+v waits=%d", cancelledResult, replacementWaits)
	}
	cancelledNonce := tmuxRunner.env[started.SessionID][rootedOrchestratorBootstrapNonceEnvironment]
	if cancelledNonce != "" {
		t.Fatalf("cancelled replacement nonce = %q, want invalidated", cancelledNonce)
	}
	if _, found, err := ackAuthority.Get(ctx, identity); err != nil || found {
		t.Fatalf("cancelled replacement acknowledgement found=%t err=%v", found, err)
	}
	d.sessionResumeWait = immediateSessionResumeWait
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

func TestRootedRestartSerializesAcrossDaemonsAndAcknowledgesReplacement(t *testing.T) {
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
	firstWait := true
	first.sessionResumeWait = func(ctx context.Context, _ time.Duration) error {
		if firstWait {
			firstWait = false
			close(replacementPaused)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseReplacement:
			}
		}
		return nil
	}
	restartRequest := protocol.RequestEnvelope{Command: protocol.CommandSessionRestartAll, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: marshalJSON(protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID(projectID)})}
	type commandResult struct {
		response protocol.ResponseEnvelope
		err      error
	}
	restartDone := make(chan commandResult, 1)
	inputsBefore := len(runner.inputPayloads)
	go func() {
		response, err := first.handleSessionRestartAll(ctx, restartRequest)
		restartDone <- commandResult{response: response, err: err}
	}()
	select {
	case <-replacementPaused:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not reach replacement boundary")
	}
	attachDone := make(chan commandResult, 1)
	go func() {
		response, err := second.handleOrchestratorSession(ctx, startRequest)
		attachDone <- commandResult{response: response, err: err}
	}()
	select {
	case result := <-attachDone:
		t.Fatalf("concurrent rooted start escaped exact-scope lock: response=%+v err=%v", result.response.Error, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseReplacement)
	var restartResult, attachResult commandResult
	select {
	case restartResult = <-restartDone:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not finish")
	}
	select {
	case attachResult = <-attachDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent rooted start did not resume")
	}
	if restartResult.err != nil || restartResult.response.Error != nil {
		t.Fatalf("restart result: response=%+v err=%v", restartResult.response.Error, restartResult.err)
	}
	if attachResult.err != nil || attachResult.response.Error != nil {
		t.Fatalf("attach result: response=%+v err=%v", attachResult.response.Error, attachResult.err)
	}
	if got := len(runner.inputPayloads) - inputsBefore; got != 1 {
		t.Fatalf("rooted replacement prompt deliveries = %d, want one acknowledged replacement", got)
	}
	identity, err := domain.NewOrchestratorIdentity(projectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	ack, found, err := daemonstate.NewRootedBootstrapAcknowledgementAuthority(secondStore).Get(ctx, identity)
	if err != nil || !found || ack.SessionID != started.SessionID || ack.RuntimeNonce == "" {
		t.Fatalf("post-restart acknowledgement = %+v found=%t err=%v", ack, found, err)
	}
	if got := runner.env[started.SessionID][rootedOrchestratorBootstrapNonceEnvironment]; got != ack.RuntimeNonce {
		t.Fatalf("live marker = %q, durable acknowledgement = %q", got, ack.RuntimeNonce)
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
	if output, err := runner.run(context.Background(), "-f", "/dev/null", "new-session", "-d", "-s", "rooted-replacement", "/bin/sh -c 'while :; do sleep 30; done'"); err != nil {
		t.Fatalf("start isolated tmux: %v (%s)", err, output)
	}
	t.Cleanup(func() { _, _ = runner.run(context.Background(), "kill-server") })
	client := tmux.NewClient(runner, slog.Default())
	if err := client.SetEnvironment(context.Background(), "rooted-replacement", rootedOrchestratorBootstrapNonceEnvironment, "durable-marker"); err != nil {
		t.Fatal(err)
	}
	panePID, err := runner.run(context.Background(), "display-message", "-p", "-t", "rooted-replacement", "#{pane_pid}")
	if err != nil {
		t.Fatal(err)
	}
	panePID = strings.TrimSpace(panePID)
	paneSleepChildren := func() []string {
		output, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "comm=").CombinedOutput()
		if err != nil {
			return nil
		}
		children := make([]string, 0, 1)
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[1] == panePID && filepath.Base(fields[2]) == "sleep" {
				children = append(children, fields[0])
			}
		}
		return children
	}
	childPID := func(exclude string) string {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			for _, pid := range paneSleepChildren() {
				if pid != exclude {
					return pid
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		return ""
	}
	firstChild := childPID("")
	if firstChild == "" {
		t.Fatal("first pane child did not start")
	}
	if output, err := exec.Command("kill", "-TERM", firstChild).CombinedOutput(); err != nil {
		t.Fatalf("stop first child: %v (%s)", err, output)
	}
	secondChild := childPID(firstChild)
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
