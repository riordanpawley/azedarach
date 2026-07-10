package daemon

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
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

	if err := d.cleanupAdvisorSessionRuntime(ctx, protocol.DefaultProjectID, request.ID); err != nil {
		t.Fatal(err)
	}
	if runner.sessions[first.Request.SessionID] {
		t.Fatal("advisor tmux session still live after cleanup")
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

func TestAdvisorSessionIDDoesNotCollideAfterSanitization(t *testing.T) {
	if advisorSessionID("request_a") == advisorSessionID("request-a") {
		t.Fatal("distinct request IDs produced the same advisor session ID")
	}
}
