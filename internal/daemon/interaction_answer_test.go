package daemon

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestInteractionStructuredProposalCanBeHumanEditedAndAtomicallyResolved(t *testing.T) {
	ctx, service, client, request := newInteractionAnswerTestService(t, "edit-proposal")
	proposedTitle := "advisor title"
	proposal := interactionAnswerTestPayload("yes", 1)
	proposal.Rationale = "Advisor recommends proceeding."
	proposal.ApprovedIssueFieldEffects.Title = &proposedTitle
	proposed, err := service.MutateInteraction(ctx, protocol.CommandInteractionPropose, protocol.InteractionMutationRequestBody{
		ID: request.ID, ExpectedRevision: 1, Actor: "advisor:session", Answer: proposal,
	})
	if err != nil {
		t.Fatal(err)
	}

	finalTitle := "human title"
	final := interactionAnswerTestPayload("no", proposed.Request.Revision)
	final.Rationale = "Human rejected the proposal after reviewing the constraint."
	final.Constraints = []string{"wait for approval"}
	final.ApprovedIssueFieldEffects.Title = &finalTitle
	resolved, err := service.ResolveInteraction(ctx, protocol.InteractionResolveRequestBody{InteractionMutationRequestBody: protocol.InteractionMutationRequestBody{
		ID: request.ID, ExpectedRevision: proposed.Request.Revision, Actor: "human:owner", Answer: final,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Request.Proposal == nil || resolved.Request.FinalAnswer == nil {
		t.Fatalf("proposal/final audit missing: %+v", resolved.Request)
	}
	if got := resolved.Request.Proposal.Answer; got.SelectedOption != "yes" || got.Rationale != proposal.Rationale || got.Revision != 1 {
		t.Fatalf("proposal audit changed: %+v", got)
	}
	if got := resolved.Request.FinalAnswer.Answer; got.SelectedOption != "no" || got.Rationale != final.Rationale || got.Revision != 2 {
		t.Fatalf("final answer audit = %+v", got)
	}
	task := interactionTestTaskFromClient(t, ctx, client, request.IssueID)
	if task.Title != finalTitle {
		t.Fatalf("title = %q, want approved human edit %q", task.Title, finalTitle)
	}
}

func TestInteractionStructuredAnswerRejectsStaleOrMalformedEffectsWithoutMutation(t *testing.T) {
	ctx, service, client, request := newInteractionAnswerTestService(t, "reject-invalid")
	proposal := interactionAnswerTestPayload("yes", 1)
	proposed, err := service.MutateInteraction(ctx, protocol.CommandInteractionPropose, protocol.InteractionMutationRequestBody{
		ID: request.ID, ExpectedRevision: 1, Actor: "advisor", Answer: proposal,
	})
	if err != nil {
		t.Fatal(err)
	}

	stale := interactionAnswerTestPayload("no", 1)
	_, err = service.ResolveInteraction(ctx, protocol.InteractionResolveRequestBody{InteractionMutationRequestBody: protocol.InteractionMutationRequestBody{
		ID: request.ID, ExpectedRevision: proposed.Request.Revision, Actor: "human", Answer: stale,
	}})
	if !errors.Is(err, domain.ErrStaleInteractionRevision) {
		t.Fatalf("stale answer error = %v", err)
	}

	malformed := interactionAnswerTestPayload("no", proposed.Request.Revision)
	empty := " "
	malformed.ApprovedIssueFieldEffects.Title = &empty
	_, err = service.ResolveInteraction(ctx, protocol.InteractionResolveRequestBody{InteractionMutationRequestBody: protocol.InteractionMutationRequestBody{
		ID: request.ID, ExpectedRevision: proposed.Request.Revision, Actor: "human", Answer: malformed,
	}})
	if err == nil {
		t.Fatal("malformed approved effect was accepted")
	}
	got, found, err := client.GetInteraction(ctx, request.ID)
	if err != nil || !found || got.State != domain.InteractionAnswerProposed || got.Revision != proposed.Request.Revision || got.FinalAnswer != nil {
		t.Fatalf("failed answers mutated request: got=%+v found=%v err=%v", got, found, err)
	}
}

func TestInteractionDirectHumanStructuredAnswerNeedsNoProposal(t *testing.T) {
	ctx, service, _, request := newInteractionAnswerTestService(t, "direct-answer")
	answer := interactionAnswerTestPayload("yes", 1)
	resolved, err := service.MutateInteraction(ctx, protocol.CommandInteractionAnswer, protocol.InteractionMutationRequestBody{
		ID: request.ID, ExpectedRevision: 1, Actor: "human", Answer: answer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Request.State != domain.InteractionResolved || resolved.Request.Proposal != nil || resolved.Request.FinalAnswer == nil {
		t.Fatalf("direct answer result = %+v", resolved.Request)
	}
}

func newInteractionAnswerTestService(t *testing.T, requestID string) (context.Context, issueInteractionService, *issues.Client, domain.InteractionRequest) {
	t.Helper()
	ctx := withDaemonProjectIDContext(context.Background(), protocol.DefaultProjectID)
	client := issues.NewClient(t.TempDir(), slog.Default())
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "before", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := domain.InteractionRequest{
		ID: requestID, IssueID: issueID, DecisionKey: requestID, OrchestrationScope: "project",
		Question: "Proceed?", Why: "Human judgment is required.",
		Options:      []domain.InteractionOption{{Key: "yes", Label: "Yes"}, {Key: "no", Label: "No"}},
		Significance: domain.InteractionSignificanceMaterial, Respondent: "human",
		DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose whether to proceed."},
		State:          domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := client.CreateInteraction(ctx, request); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg: Config{Logger: slog.Default()}, issues: client,
		hub: publish.NewHub(16, 8, slog.Default()), revision: map[string]uint64{},
	}
	return ctx, issueInteractionService{daemon: d}, client, request
}

func interactionAnswerTestPayload(selected string, revision int64) domain.InteractionAnswerPayload {
	return domain.InteractionAnswerPayload{
		SelectedOption: selected, Rationale: "Proceed with care.", Constraints: []string{"preserve audit history"},
		SignificanceRecommendation: domain.InteractionSignificanceMaterial, Revision: revision,
	}
}

func interactionTestTaskFromClient(t *testing.T, ctx context.Context, client *issues.Client, issueID string) domain.Task {
	t.Helper()
	tasks, err := client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID.String() == issueID {
			return task
		}
	}
	t.Fatalf("task %s not found", issueID)
	return domain.Task{}
}
