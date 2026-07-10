package issues

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

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
	next, err := r.Transition(domain.InteractionWithdrawn, r.Revision, r.UpdatedAt.Add(time.Second))
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

func testInteractionRequest(id, issueID, key string) domain.InteractionRequest {
	now := time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC)
	return domain.InteractionRequest{ID: id, IssueID: issueID, DecisionKey: key, OrchestrationScope: "root", Question: "Proceed?", Why: "Material choice", RequiredDecisions: []string{"yes or no"}, Significance: domain.InteractionSignificanceMaterial, Respondent: "human", DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose"}, State: domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now}
}
