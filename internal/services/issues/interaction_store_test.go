package issues

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestResolveInteractionAtomicEffectsAndRollback(t *testing.T) {
	ctx := context.Background()
	c := NewClient(t.TempDir(), nil)
	issueID, err := c.Create(ctx, CreateTaskParams{Title: "before", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	r := testInteractionRequest("resolve", issueID, "atomic")
	if err = c.CreateInteraction(ctx, r); err != nil {
		t.Fatal(err)
	}
	now := r.UpdatedAt.Add(time.Second)
	title := "must roll back"
	answer := interactionStoreTestAnswer(1)
	answer.ApprovedIssueFieldEffects.Title = &title
	r.FinalAnswer = &domain.InteractionAnswerAudit{Answer: answer, Actor: "human", CreatedAt: now}
	r, err = r.Transition(domain.InteractionResolved, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	err = c.ResolveInteraction(ctx, InteractionResolution{Request: r, ExpectedRevision: 1, Decision: &InteractionDecisionEffect{Title: "bad"}})
	if err == nil {
		t.Fatal("expected decision validation failure")
	}
	got, ok, err := c.GetInteraction(ctx, "resolve")
	if err != nil || !ok {
		t.Fatalf("get interaction: %v %v", ok, err)
	}
	if got.State != domain.InteractionOpen || got.Revision != 1 {
		t.Fatalf("request changed after rollback: %+v", got)
	}
	task := interactionTestTask(t, ctx, c, issueID)
	if task.Title != "before" {
		t.Fatalf("title changed after rollback: %q", task.Title)
	}
	err = c.ResolveInteraction(ctx, InteractionResolution{Request: r, ExpectedRevision: 1, Decision: &InteractionDecisionEffect{Title: "Proceed", Rationale: "Human approved"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ = c.GetInteraction(ctx, "resolve")
	if got.State != domain.InteractionResolved || got.FinalAnswer == nil {
		t.Fatalf("request not resolved: %+v", got)
	}
	task = interactionTestTask(t, ctx, c, issueID)
	if task.Title != title {
		t.Fatalf("title=%q want %q", task.Title, title)
	}
	if err = c.ResolveInteraction(ctx, InteractionResolution{Request: r, ExpectedRevision: 1}); !errors.Is(err, domain.ErrStaleInteractionRevision) {
		t.Fatalf("stale resolution error=%v", err)
	}
}

func TestResolveInteractionAnswerOnlyPreservesIssueMetadataAndObservations(t *testing.T) {
	ctx := context.Background()
	c := NewClient(t.TempDir(), nil)
	issueID, err := c.Create(ctx, CreateTaskParams{Title: "unchanged", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	before := interactionTestTask(t, ctx, c, issueID)
	beforeEvents, err := c.ListIssueObservationEvents(ctx, issueID, IssueObservationEventListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := testInteractionRequest("answer-only", issueID, "answer-only")
	if err = c.CreateInteraction(ctx, r); err != nil {
		t.Fatal(err)
	}
	now := r.UpdatedAt.Add(time.Second)
	r.FinalAnswer = &domain.InteractionAnswerAudit{Answer: interactionStoreTestAnswer(1), Actor: "human", CreatedAt: now}
	r, err = r.Transition(domain.InteractionResolved, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ResolveInteraction(ctx, InteractionResolution{Request: r, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	after := interactionTestTask(t, ctx, c, issueID)
	if !after.UpdatedAt.Equal(before.UpdatedAt) || after.Title != before.Title {
		t.Fatalf("answer-only resolution changed issue metadata: before=%+v after=%+v", before, after)
	}
	afterEvents, err := c.ListIssueObservationEvents(ctx, issueID, IssueObservationEventListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("answer-only resolution appended issue observation: before=%d after=%d", len(beforeEvents), len(afterEvents))
	}
	got, ok, err := c.GetInteraction(ctx, r.ID)
	if err != nil || !ok || got.State != domain.InteractionResolved {
		t.Fatalf("interaction not resolved: got=%+v ok=%v err=%v", got, ok, err)
	}
}

func interactionTestTask(t *testing.T, ctx context.Context, c *Client, id string) domain.Task {
	t.Helper()
	tasks, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID.String() == id {
			return task
		}
	}
	t.Fatalf("task %s not found", id)
	return domain.Task{}
}

func TestInteractionStoreDurabilityDecisionKeyAndStaleCache(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	writer, reader := NewClientAtPath(path, nil), NewClientAtPath(path, nil)
	if _, err := writer.List(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.List(ctx); err != nil {
		t.Fatal(err)
	}
	issueID, err := writer.Create(ctx, CreateTaskParams{Title: "Issue", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	r := testInteractionRequest("req-1", issueID, "deploy-region")
	if err := writer.CreateInteraction(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := writer.CreateInteraction(ctx, r); err == nil || errors.Is(err, domain.ErrDuplicateUnresolvedDecision) {
		t.Fatalf("duplicate id error = %v", err)
	}
	got, ok, err := reader.InteractionByDecisionKey(ctx, r.IssueID, r.DecisionKey)
	if err != nil || !ok || got.ID != r.ID {
		t.Fatalf("cross-client lookup = %+v, %v, %v", got, ok, err)
	}
	if err := writer.CreateInteraction(ctx, testInteractionRequest("req-2", r.IssueID, r.DecisionKey)); !errors.Is(err, domain.ErrDuplicateUnresolvedDecision) {
		t.Fatalf("duplicate error = %v", err)
	}
	withdrawnAt := r.UpdatedAt.Add(time.Second)
	r.Disposition = &domain.InteractionDispositionAudit{Actor: "orchestrator", Reason: "obsolete", CreatedAt: withdrawnAt}
	next, err := r.Transition(domain.InteractionWithdrawn, r.Revision, withdrawnAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.UpdateInteraction(ctx, next, r.Revision); err != nil {
		t.Fatal(err)
	}
	waiting, err := reader.IssueHasUnresolvedInteraction(ctx, r.IssueID)
	if err != nil || waiting {
		t.Fatalf("reader retained stale waiting projection: waiting=%v err=%v", waiting, err)
	}
	if _, ok, err := NewClientAtPath(path, nil).GetInteraction(ctx, r.ID); err != nil || !ok {
		t.Fatalf("restart lookup ok=%v err=%v", ok, err)
	}
}

func TestInteractionStoreRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	c := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	if _, err := c.List(ctx); err != nil {
		t.Fatal(err)
	}
	issueID, err := c.Create(ctx, CreateTaskParams{Title: "Issue", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	r := testInteractionRequest("req", issueID, "key")
	if err := c.CreateInteraction(ctx, r); err != nil {
		t.Fatal(err)
	}
	next, err := r.Transition(domain.InteractionDiscussing, 1, r.UpdatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateInteraction(ctx, next, 1); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	if err := c.UpdateInteraction(ctx, next, 1); !errors.Is(err, domain.ErrStaleInteractionRevision) {
		t.Fatalf("stale error = %v", err)
	}
}

func TestInteractionStoreRejectsBypassedTransition(t *testing.T) {
	ctx := context.Background()
	c := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	if _, err := c.List(ctx); err != nil {
		t.Fatal(err)
	}
	issueID, err := c.Create(ctx, CreateTaskParams{Title: "Issue", Type: domain.TypeTask, Priority: domain.P2, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	r := testInteractionRequest("req", issueID, "key")
	if err := c.CreateInteraction(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.State, r.Revision, r.UpdatedAt = domain.InteractionOpen, 2, r.UpdatedAt.Add(time.Second)
	if err := c.UpdateInteraction(ctx, r, 1); err == nil {
		t.Fatal("same-state replacement bypassed transition graph")
	}
}

func TestInteractionStoreReadsLegacyStringAnswerAuditAndRewritesStructuredFinal(t *testing.T) {
	ctx := context.Background()
	c := NewClient(t.TempDir(), nil)
	issueID, err := c.Create(ctx, CreateTaskParams{Title: "Issue", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	r := testInteractionRequest("legacy-answer", issueID, "legacy")
	if err := c.CreateInteraction(ctx, r); err != nil {
		t.Fatal(err)
	}
	legacyTime := r.UpdatedAt.Add(time.Second)
	var legacy map[string]any
	raw, _ := json.Marshal(r)
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["proposal"] = map[string]any{"answer": "yes", "actor": "advisor", "created_at": legacyTime}
	legacy["state"], legacy["revision"], legacy["updated_at"] = domain.InteractionAnswerProposed, 2, legacyTime
	legacyRaw, _ := json.Marshal(legacy)
	db, err := c.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE interaction_requests SET state=?, revision=?, request_json=?, updated_at=? WHERE id=?`, domain.InteractionAnswerProposed, 2, legacyRaw, legacyTime.Format(time.RFC3339Nano), r.ID); err != nil {
		t.Fatal(err)
	}
	got, found, err := c.GetInteraction(ctx, r.ID)
	if err != nil || !found || got.Proposal == nil {
		t.Fatalf("legacy proposal load: got=%+v found=%v err=%v", got, found, err)
	}
	if got.Proposal.Answer.SelectedOption != "yes" || got.Proposal.Answer.Rationale == "" || got.Proposal.Answer.Revision != 1 {
		t.Fatalf("legacy proposal conversion = %+v", got.Proposal.Answer)
	}
	now := legacyTime.Add(time.Second)
	got.FinalAnswer = &domain.InteractionAnswerAudit{Answer: interactionStoreTestAnswer(2), Actor: "human", CreatedAt: now}
	got, err = got.Transition(domain.InteractionResolved, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ResolveInteraction(ctx, InteractionResolution{Request: got, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := c.GetInteraction(ctx, r.ID)
	if err != nil || reloaded.FinalAnswer == nil || reloaded.FinalAnswer.Answer.Revision != 2 {
		t.Fatalf("structured rewrite: got=%+v err=%v", reloaded, err)
	}
}

func testInteractionRequest(id, issueID, key string) domain.InteractionRequest {
	now := time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC)
	return domain.InteractionRequest{ID: id, IssueID: issueID, DecisionKey: key, OrchestrationScope: "root", Question: "Proceed?", Why: "Material choice", RequiredDecisions: []string{"yes or no"}, Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func interactionStoreTestAnswer(revision int64) domain.InteractionAnswerPayload {
	return domain.InteractionAnswerPayload{
		SelectedOption: "yes", Rationale: "Proceed safely.", Constraints: []string{"keep audit history"},
		SignificanceRecommendation: domain.InteractionSignificanceMaterial, Revision: revision,
	}
}
