package issues

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestDecisionPropagationOutboxMaterializationIsExactlyOnceAcrossClients(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	firstClient := NewClientAtPath(path, slog.Default())
	t.Cleanup(func() { _ = firstClient.CloseDB() })
	secondClient := NewClientAtPath(path, slog.Default())
	t.Cleanup(func() { _ = secondClient.CloseDB() })
	issueID, err := firstClient.Create(ctx, CreateTaskParams{Title: "worker", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := firstClient.RecordDecision(ctx, RecordDecisionParams{Title: "contract", Rationale: "material"})
	if err != nil {
		t.Fatal(err)
	}
	title := "contract v2"
	if _, err := firstClient.UpdateDecisionWithPropagation(ctx, decision.LocalID, UpdateDecisionParams{Title: &title}, DecisionPropagationIntent{ChangedIssueIDs: []string{issueID}, SourceCommand: "decision.update"}); err != nil {
		t.Fatal(err)
	}
	entries, err := firstClient.ListActiveDecisionPropagationOutbox(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	var wg sync.WaitGroup
	ids := make(chan int64, 2)
	errs := make(chan error, 2)
	for _, client := range []*Client{firstClient, secondClient} {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			event, err := client.MaterializeDecisionPropagationOutbox(ctx, entries[0])
			if err != nil {
				errs <- err
				return
			}
			ids <- event.ID
		}(client)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	close(ids)
	var firstID int64
	for id := range ids {
		if firstID == 0 {
			firstID = id
		} else if id != firstID {
			t.Fatalf("materialized event ids differ: %d and %d", firstID, id)
		}
	}
	events, err := firstClient.ListIssueDecisionObservationEvents(ctx, issueID)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v, want exactly one authority event", events, err)
	}
	ackIDs := make(chan int64, 2)
	errs = make(chan error, 2)
	for _, client := range []*Client{firstClient, secondClient} {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			event, err := client.AcknowledgeDecisionPropagation(ctx, issueID, decision.LocalID, entries[0].Revision, domain.DecisionAcknowledgementReconciled, "done")
			if err != nil {
				errs <- err
				return
			}
			ackIDs <- event.ID
		}(client)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	close(ackIDs)
	var firstAckID int64
	for id := range ackIDs {
		if firstAckID == 0 {
			firstAckID = id
		} else if id != firstAckID {
			t.Fatalf("acknowledgement event ids differ: %d and %d", firstAckID, id)
		}
	}
	events, err = firstClient.ListIssueDecisionObservationEvents(ctx, issueID)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%+v err=%v, want one change and one authoritative acknowledgement", events, err)
	}
}

func TestDecisionPropagationWithdrawalAllowsProvenDeleteButRejectsPhantomIssue(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "deleted worker", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := client.RecordDecision(ctx, RecordDecisionParams{Title: "contract", Rationale: "material"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(ctx, issueID); err != nil {
		t.Fatal(err)
	}

	title := "contract after delete"
	if _, err := client.UpdateDecisionWithPropagation(ctx, decision.LocalID, UpdateDecisionParams{Title: &title}, DecisionPropagationIntent{
		WithdrawnIssueIDs: []string{issueID}, SourceCommand: "decision.update",
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := client.ListActiveDecisionPropagationOutbox(ctx, 10)
	if err != nil || len(entries) != 1 || entries[0].EventKind != DecisionPropagationWithdrawn {
		t.Fatalf("entries=%+v err=%v, want one deleted-issue withdrawal", entries, err)
	}
	event, err := client.MaterializeDecisionPropagationOutbox(ctx, entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if event.IssueID.String() != issueID || event.Payload["withdrawn"] != true {
		t.Fatalf("event=%+v, want deleted issue %s withdrawal", event, issueID)
	}

	before, err := client.GetDecision(ctx, decision.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	phantomTitle := "must roll back"
	if _, err := client.UpdateDecisionWithPropagation(ctx, decision.LocalID, UpdateDecisionParams{Title: &phantomTitle}, DecisionPropagationIntent{
		WithdrawnIssueIDs: []string{"phantom-issue"}, SourceCommand: "decision.update",
	}); err == nil || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("phantom withdrawal err=%v, want not found", err)
	}
	after, err := client.GetDecision(ctx, decision.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != before.Title {
		t.Fatalf("decision title after rejected phantom = %q, want rollback to %q", after.Title, before.Title)
	}
}
