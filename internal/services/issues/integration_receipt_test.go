package issues

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestAppendTaskIntegrationReceiptIfAbsentIsAtomicAcrossClientsAndUnbounded(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	first := newTestClientAtPath(t, dbPath, slog.Default())
	second := newTestClientAtPath(t, dbPath, slog.Default())

	issueID, err := first.Create(ctx, CreateTaskParams{
		Title: "Atomic integration receipt",
		Type:  domain.TypeBug,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	receipt := TaskIntegrationReceipt{
		ProjectID:              "project-a",
		SourceBranch:           "feature/atomic-receipt",
		TargetBranch:           "main",
		Integrated:             true,
		ConfiguredBaseTarget:   true,
		TargetID:               "base",
		BaseOID:                "base-exact",
		SourceOID:              "source-exact",
		TargetOID:              "target-exact",
		PublicationOperationID: "publication-exact",
	}
	inserted, err := first.AppendTaskIntegrationReceiptIfAbsent(ctx, issueID, receipt, "/tmp/worktree")
	if err != nil {
		t.Fatalf("append original receipt: %v", err)
	}
	if !inserted {
		t.Fatal("original receipt inserted = false, want true")
	}
	if _, err := first.AppendIssueObservationEvent(ctx, issueID, IssueObservationEventParams{
		Type: domain.IssueEventTaskIntegrationCompleted, Source: "agent", SourceCommand: "fabricated",
		Payload: map[string]any{
			"project_id": receipt.ProjectID, "source_branch": receipt.SourceBranch, "target_branch": receipt.TargetBranch,
			"integrated": receipt.Integrated, "configured_base_target": receipt.ConfiguredBaseTarget, "target_id": receipt.TargetID,
			"base_oid": receipt.BaseOID, "source_oid": "untrusted-source", "target_oid": receipt.TargetOID,
			"publication_operation_id": receipt.PublicationOperationID,
		},
	}); err != nil {
		t.Fatalf("append untrusted matching receipt: %v", err)
	}
	untrusted := receipt
	untrusted.SourceOID = "untrusted-source"
	inserted, err = first.AppendTaskIntegrationReceiptIfAbsent(ctx, issueID, untrusted, "/tmp/worktree")
	if err != nil {
		t.Fatalf("append trusted receipt after untrusted match: %v", err)
	}
	if !inserted {
		t.Fatal("trusted receipt was suppressed by untrusted matching payload")
	}
	for i := 0; i < defaultIssueObservationEventLimit+1; i++ {
		other := receipt
		other.SourceOID = fmt.Sprintf("other-%d", i)
		if _, err := first.AppendTaskIntegrationReceiptIfAbsent(ctx, issueID, other, "/tmp/worktree"); err != nil {
			t.Fatalf("append newer receipt %d: %v", i, err)
		}
	}

	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, client := range []*Client{first, second} {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			<-start
			inserted, appendErr := client.AppendTaskIntegrationReceiptIfAbsent(ctx, issueID, receipt, "/tmp/worktree")
			results <- inserted
			errs <- appendErr
		}(client)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	}
	for inserted := range results {
		if inserted {
			t.Fatal("duplicate receipt inserted after exact identity fell outside bounded history")
		}
	}

	db, err := first.dbHandle()
	if err != nil {
		t.Fatalf("open issue database: %v", err)
	}
	var receiptCount, exactCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events WHERE issue_id=? AND event_type=?`, issueID, domain.IssueEventTaskIntegrationCompleted).Scan(&receiptCount); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events WHERE issue_id=? AND event_type=? AND json_extract(payload_json, '$.source_oid')=?`, issueID, domain.IssueEventTaskIntegrationCompleted, receipt.SourceOID).Scan(&exactCount); err != nil {
		t.Fatalf("count exact receipts: %v", err)
	}
	want := defaultIssueObservationEventLimit + 4
	if receiptCount != want || exactCount != 1 {
		t.Fatalf("receipt count = %d exact count = %d, want total %d exact 1", receiptCount, exactCount, want)
	}

	concurrentIssueID, err := first.Create(ctx, CreateTaskParams{Title: "Concurrent first insert", Type: domain.TypeBug})
	if err != nil {
		t.Fatalf("create concurrent issue: %v", err)
	}
	start = make(chan struct{})
	results = make(chan bool, 2)
	errs = make(chan error, 2)
	for _, client := range []*Client{first, second} {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			<-start
			inserted, appendErr := client.AppendTaskIntegrationReceiptIfAbsent(ctx, concurrentIssueID, receipt, "/tmp/worktree")
			results <- inserted
			errs <- appendErr
		}(client)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	insertCount := 0
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent first append: %v", err)
		}
	}
	for inserted := range results {
		if inserted {
			insertCount++
		}
	}
	if insertCount != 1 {
		t.Fatalf("concurrent insert count = %d, want 1", insertCount)
	}
}
