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
		ProjectID:    "project-a",
		SourceBranch: "feature/atomic-receipt",
		TargetBranch: "main",
		SourceOID:    "source-exact",
		TargetOID:    "target-exact",
	}
	inserted, err := first.AppendTaskIntegrationReceiptIfAbsent(ctx, issueID, receipt, "/tmp/worktree")
	if err != nil {
		t.Fatalf("append original receipt: %v", err)
	}
	if !inserted {
		t.Fatal("original receipt inserted = false, want true")
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
	want := defaultIssueObservationEventLimit + 2
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
			params := IssueObservationEventParams{
				Type:          domain.IssueEventTaskIntegrationCompleted,
				Source:        "daemon-task-close",
				SourceCommand: "integrate-before-close",
				WorktreePath:  "/tmp/worktree",
				Payload: map[string]any{
					"project_id":    receipt.ProjectID,
					"source_branch": receipt.SourceBranch,
					"target_branch": receipt.TargetBranch,
					"source_oid":    receipt.SourceOID,
					"target_oid":    receipt.TargetOID,
				},
			}
			var inserted bool
			appendErr := client.retrySQLiteBusy(ctx, func() error {
				var err error
				inserted, err = client.appendTaskIntegrationReceiptTransaction(ctx, concurrentIssueID, receipt, params)
				return err
			})
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
