package issues

import (
	"context"
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
