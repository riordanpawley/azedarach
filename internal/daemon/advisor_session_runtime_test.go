package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestInteractionDiscussStartsAndAttachesLiveAdvisorWithoutMutatingIssueLifecycle(t *testing.T) {
	ctx := withDaemonProjectIDContext(context.Background(), protocol.DefaultProjectID)
	repoDir := t.TempDir()
	client := issues.NewClient(repoDir, slog.Default())
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "implementation remains open", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "request-live", IssueID: issueID, DecisionKey: "choice", OrchestrationScope: "project", Question: "Which option?", Why: "Human judgment", Options: []domain.InteractionOption{{Key: "a", Label: "A"}}, Significance: domain.InteractionSignificanceRoutine, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := client.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issues: client, tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore()}
	service := issueInteractionService{daemon: d}

	first, err := service.MutateInteraction(ctx, protocol.CommandInteractionDiscuss, protocol.InteractionMutationRequestBody{ID: request.ID, ExpectedRevision: 1, Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.SessionStarted || first.SessionAttached || !runner.sessions[first.Request.SessionID] {
		t.Fatalf("first discuss = %+v sessions=%v", first, runner.sessions)
	}
	if _, err := service.MutateInteraction(ctx, protocol.CommandInteractionRecover, protocol.InteractionMutationRequestBody{ID: request.ID, ExpectedRevision: first.Request.Revision, Actor: "advisor"}); err == nil {
		t.Fatal("advisor unexpectedly received recovery authority")
	}
	launch := requireNewSessionLaunchCommand(t, runner, first.Request.SessionID)
	if !strings.Contains(launch, "AZEDARACH_SESSION_ROLE=advisor") || !strings.Contains(launch, "AZEDARACH_INTERACTION_ID") || !strings.Contains(launch, "AZEDARACH_ISSUE_ID=\"\"") {
		t.Fatalf("advisor launch command = %s", launch)
	}
	if err := d.persistTmuxSessionRuntimeState(ctx, protocol.DefaultProjectID, []tmux.SessionInfo{{Name: first.Request.SessionID}}, nil); err != nil {
		t.Fatal(err)
	}
	canonicalProjectID := d.canonicalProjectID(protocol.DefaultProjectID)
	projection, found, err := d.sessionRuntimeStateStore(canonicalProjectID).GetSessionState(ctx, canonicalProjectID, first.Request.SessionID)
	if err != nil || !found {
		t.Fatalf("get reconciled advisor projection: found=%v err=%v", found, err)
	}
	if projection.Role != daemonstate.SessionRoleAdvisor || projection.ScopeKind != daemonstate.SessionScopeInteraction || projection.ScopeID != request.ID {
		t.Fatalf("reconcile lost advisor metadata: %+v", projection)
	}

	second, err := service.MutateInteraction(ctx, protocol.CommandInteractionDiscuss, protocol.InteractionMutationRequestBody{ID: request.ID, ExpectedRevision: first.Request.Revision, Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.SessionAttached || second.SessionStarted || second.Request.SessionID != first.Request.SessionID {
		t.Fatalf("second discuss = %+v", second)
	}
	if got := len(runner.commands); got == 0 {
		t.Fatal("expected tmux commands")
	}
	projection.State, projection.ObservedState = daemonstate.SessionStatePaused, daemonstate.SessionStatePaused
	if err := d.sessionRuntimeStateStore(canonicalProjectID).UpsertSessionState(ctx, canonicalProjectID, projection); err != nil {
		t.Fatal(err)
	}
	resumed, err := service.MutateInteraction(ctx, protocol.CommandInteractionDiscuss, protocol.InteractionMutationRequestBody{ID: request.ID, ExpectedRevision: second.Request.Revision, Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.SessionAttached || !resumed.SessionResumed || resumed.SessionStarted {
		t.Fatalf("paused discuss = %+v", resumed)
	}
	delete(runner.sessions, first.Request.SessionID)
	restarted, err := service.MutateInteraction(ctx, protocol.CommandInteractionDiscuss, protocol.InteractionMutationRequestBody{ID: request.ID, ExpectedRevision: second.Request.Revision, Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.SessionStarted || restarted.SessionAttached || restarted.Request.Revision != second.Request.Revision || !runner.sessions[first.Request.SessionID] {
		t.Fatalf("missing-runtime discuss = %+v sessions=%v", restarted, runner.sessions)
	}

	if err := d.cleanupAdvisorSessionRuntime(ctx, protocol.DefaultProjectID, request.ID); err != nil {
		t.Fatal(err)
	}
	if runner.sessions[first.Request.SessionID] {
		t.Fatal("advisor tmux session still live after cleanup")
	}
	if _, found, err := d.sessionRuntimeStateStore(canonicalProjectID).GetSessionState(ctx, canonicalProjectID, first.Request.SessionID); err != nil || found {
		t.Fatalf("advisor projection remains after cleanup: found=%t err=%v", found, err)
	}
	unchanged, found, err := client.GetInteraction(ctx, request.ID)
	if err != nil || !found {
		t.Fatalf("get interaction after cleanup: found=%v err=%v", found, err)
	}
	if unchanged.State != domain.InteractionDiscussing || unchanged.Revision != first.Request.Revision || unchanged.SessionID != first.Request.SessionID {
		t.Fatalf("cleanup mutated interaction: %+v", unchanged)
	}
	task, err := client.GetWithRuntime(ctx, protocol.DefaultProjectID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusOpen {
		t.Fatalf("advisor discussion mutated implementation lifecycle: %s", task.Status)
	}
}

func TestRuntimeReconcileRecoversAndCleansAdvisorSessionsFromDurableRequests(t *testing.T) {
	ctx := withDaemonProjectIDContext(context.Background(), protocol.DefaultProjectID)
	repoDir := t.TempDir()
	client := issues.NewClient(repoDir, slog.Default())
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "advisor recovery", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "request-recover", IssueID: issueID, DecisionKey: "choice", OrchestrationScope: "project", Question: "Which option?", Why: "Human judgment", Options: []domain.InteractionOption{{Key: "a", Label: "A"}}, Significance: domain.InteractionSignificanceRoutine, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := client.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issues: client, tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(), hub: publish.NewHub(32, 8, slog.Default()), revision: map[string]uint64{}}
	service := issueInteractionService{daemon: d}
	discussed, err := service.MutateInteraction(ctx, protocol.CommandInteractionDiscuss, protocol.InteractionMutationRequestBody{ID: request.ID, ExpectedRevision: 1, Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	delete(runner.sessions, discussed.Request.SessionID)
	canonicalProjectID := d.canonicalProjectID(protocol.DefaultProjectID)
	manager := git.NewWorktreeManager(&testGitRunner{worktreePath: repoDir, branchName: "main"}, repoDir, slog.Default())
	d.worktreeManagersByRoot = map[string]*git.WorktreeManager{repoDir: manager}
	d.worktreeManagersByProject = map[string]*git.WorktreeManager{canonicalProjectID: manager}
	events, cancel := d.hub.Subscribe(canonicalProjectID, d.currentRevision(canonicalProjectID))
	defer cancel()

	result, err := newRuntimeReconcileService(d).Reconcile(ctx, protocol.DefaultProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if result.AdvisorSessionsRecovered != 1 || result.AdvisorSessionsCleaned != 0 || !runner.sessions[discussed.Request.SessionID] {
		t.Fatalf("reconcile result=%+v sessions=%v", result, runner.sessions)
	}
	recovered, found, err := client.GetInteraction(ctx, request.ID)
	if err != nil || !found || recovered.Recovery == nil || recovered.Recovery.SessionID != discussed.Request.SessionID || recovered.Revision != discussed.Request.Revision+1 {
		t.Fatalf("recovered request=%+v found=%t err=%v", recovered, found, err)
	}
	select {
	case event := <-events:
		if event.Event != protocol.EventSessionUpdated && event.Event != protocol.EventInteractionRecovered {
			t.Fatalf("unexpected recovery event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for advisor recovery event")
	}

	withdrawAt := time.Now().UTC()
	recovered.Disposition = &domain.InteractionDispositionAudit{Actor: "orchestrator", Reason: "obsolete", CreatedAt: withdrawAt}
	withdrawn, err := recovered.Transition(domain.InteractionWithdrawn, recovered.Revision, withdrawAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateInteraction(ctx, withdrawn, recovered.Revision); err != nil {
		t.Fatal(err)
	}
	result, err = newRuntimeReconcileService(d).Reconcile(ctx, protocol.DefaultProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if result.AdvisorSessionsRecovered != 0 || result.AdvisorSessionsCleaned != 1 || runner.sessions[discussed.Request.SessionID] {
		t.Fatalf("terminal reconcile result=%+v sessions=%v", result, runner.sessions)
	}
}

func TestImplementationSessionReconcileNeverRecreatesAdvisorProjection(t *testing.T) {
	if sessionProjectionCanRecreateTmuxSession(daemonstate.Session{ID: "advisor-request", Role: daemonstate.SessionRoleAdvisor, State: daemonstate.SessionStateRunning}) {
		t.Fatal("generic implementation reconciliation accepted advisor projection")
	}
}

func TestAdvisorRecoveryCleansRuntimeWhenTerminalRequestWinsCrossDaemonRace(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	clientA := issues.NewClient(repoDir, slog.Default())
	clientB := issues.NewClient(repoDir, slog.Default())
	issueID, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "terminal race", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "request-terminal-race", IssueID: issueID, DecisionKey: "choice", OrchestrationScope: "project", Question: "Which option?", Why: "Human judgment", Options: []domain.InteractionOption{{Key: "a", Label: "A"}}, Significance: domain.InteractionSignificanceRoutine, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := clientA.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.SessionID = advisorSessionID(request.ID)
	request, err = request.Transition(domain.InteractionDiscussing, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := clientA.UpdateInteraction(ctx, request, 1); err != nil {
		t.Fatal(err)
	}

	projectID := protocol.DefaultProjectID
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issues: clientA, tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(), hub: publish.NewHub(16, 8, slog.Default()), revision: map[string]uint64{}}
	projectID = d.canonicalProjectID(projectID)
	d.issueClientsByProject = map[string]*issues.Client{projectID: clientA}
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore}
	runner.onNewSession = func(string) {
		current, found, getErr := clientB.GetInteraction(ctx, request.ID)
		if getErr != nil || !found {
			t.Errorf("load racing interaction: found=%t err=%v", found, getErr)
			return
		}
		terminalAt := current.UpdatedAt.Add(time.Second)
		current.Disposition = &domain.InteractionDispositionAudit{Actor: "orchestrator", Reason: "superseded during recovery", CreatedAt: terminalAt}
		terminal, transitionErr := current.Transition(domain.InteractionWithdrawn, current.Revision, terminalAt)
		if transitionErr != nil {
			t.Errorf("transition racing interaction: %v", transitionErr)
			return
		}
		if updateErr := clientB.UpdateInteraction(ctx, terminal, current.Revision); updateErr != nil {
			t.Errorf("persist racing interaction: %v", updateErr)
		}
	}

	recovered, cleaned, err := d.reconcileAdvisorSessionRuntimes(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 0 || cleaned != 1 || runner.sessions[request.SessionID] {
		t.Fatalf("race result recovered=%d cleaned=%d sessions=%v", recovered, cleaned, runner.sessions)
	}
	if _, found, err := runtimeStore.GetAdvisorSession(ctx, projectID, request.ID); err != nil || found {
		t.Fatalf("advisor reservation survived terminal race: found=%t err=%v", found, err)
	}
}

func TestAdvisorRecoveryRetriesWhenNonTerminalMutationWinsCrossDaemonRace(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	clientA := issues.NewClient(repoDir, slog.Default())
	clientB := issues.NewClient(repoDir, slog.Default())
	issueID, err := clientA.Create(ctx, issues.CreateTaskParams{Title: "recovery metadata race", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{ID: "request-metadata-race", IssueID: issueID, DecisionKey: "choice", OrchestrationScope: "project", Question: "Which option?", Why: "Human judgment", Options: []domain.InteractionOption{{Key: "a", Label: "A"}}, Significance: domain.InteractionSignificanceRoutine, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := clientA.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.SessionID = advisorSessionID(request.ID)
	request, err = request.Transition(domain.InteractionDiscussing, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := clientA.UpdateInteraction(ctx, request, 1); err != nil {
		t.Fatal(err)
	}

	projectID := protocol.DefaultProjectID
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issues: clientA, tmux: tmux.NewClient(runner, slog.Default()), sessionStore: daemonstate.NewStore(), hub: publish.NewHub(16, 8, slog.Default()), revision: map[string]uint64{}}
	projectID = d.canonicalProjectID(projectID)
	d.issueClientsByProject = map[string]*issues.Client{projectID: clientA}
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore}
	events, cancel := d.hub.Subscribe(projectID, 0)
	defer cancel()
	runner.onNewSession = func(string) {
		current, found, getErr := clientB.GetInteraction(ctx, request.ID)
		if getErr != nil || !found {
			t.Errorf("load racing interaction: found=%t err=%v", found, getErr)
			return
		}
		proposalAt := current.UpdatedAt.Add(time.Second)
		current.Proposal = &domain.InteractionAnswerAudit{Answer: "A", Actor: "advisor", CreatedAt: proposalAt}
		proposed, transitionErr := current.Transition(domain.InteractionAnswerProposed, current.Revision, proposalAt)
		if transitionErr != nil {
			t.Errorf("transition racing interaction: %v", transitionErr)
			return
		}
		if updateErr := clientB.UpdateInteraction(ctx, proposed, current.Revision); updateErr != nil {
			t.Errorf("persist racing interaction: %v", updateErr)
		}
	}

	recovered, cleaned, err := d.reconcileAdvisorSessionRuntimes(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 || cleaned != 0 || !runner.sessions[request.SessionID] {
		t.Fatalf("race result recovered=%d cleaned=%d sessions=%v", recovered, cleaned, runner.sessions)
	}
	current, found, err := clientA.GetInteraction(ctx, request.ID)
	if err != nil || !found || current.State != domain.InteractionAnswerProposed || current.Recovery == nil || current.Recovery.SessionID != request.SessionID {
		t.Fatalf("recovered request=%+v found=%t err=%v", current, found, err)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Event == protocol.EventInteractionRecovered {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for interaction recovery event")
		}
	}
}

func TestAdvisorSessionIDDoesNotCollideAfterSanitization(t *testing.T) {
	if advisorSessionID("request_a") == advisorSessionID("request-a") {
		t.Fatal("distinct request IDs produced the same advisor session ID")
	}
}
