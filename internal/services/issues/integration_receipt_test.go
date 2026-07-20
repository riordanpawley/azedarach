package issues

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestAppendTaskIntegrationReceiptIfAbsentRollsBackEventAndAdvanceBeforeRetry(t *testing.T) {
	ctx := context.Background()
	client := newTestClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "Receipt rollback", Type: domain.TypeBug})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	receipt := testTaskIntegrationReceipt()
	advanceBaseline := projectionSourceAdvanceCount(t, client)
	injected := errors.New("injected after receipt insert")
	failingCtx := WithTaskIntegrationReceiptMutationHookForTest(ctx, func(stage string) error {
		if stage == taskIntegrationReceiptHookAfterEventInsert {
			return injected
		}
		return nil
	})
	inserted, err := client.AppendTaskIntegrationReceiptIfAbsent(failingCtx, issueID, receipt, "/tmp/worktree")
	if !errors.Is(err, injected) {
		t.Fatalf("append error = %v, want injected failure", err)
	}
	if inserted {
		t.Fatal("failed append reported inserted")
	}
	assertIntegrationReceiptPersistenceCounts(t, client, issueID, receipt.SourceOID, 0, 0)
	if got := projectionSourceAdvanceCount(t, client); got != advanceBaseline {
		t.Fatalf("source advances after rollback = %d, want baseline %d", got, advanceBaseline)
	}

	inserted, err = client.AppendTaskIntegrationReceiptIfAbsent(ctx, issueID, receipt, "/tmp/worktree")
	if err != nil {
		t.Fatalf("retry append: %v", err)
	}
	if !inserted {
		t.Fatal("retry append inserted = false, want true")
	}
	assertIntegrationReceiptPersistenceCounts(t, client, issueID, receipt.SourceOID, 1, 1)
	if got := projectionSourceAdvanceCount(t, client); got != advanceBaseline+1 {
		t.Fatalf("source advances after retry = %d, want %d", got, advanceBaseline+1)
	}

	inserted, err = client.AppendTaskIntegrationReceiptIfAbsent(ctx, issueID, receipt, "/tmp/worktree")
	if err != nil {
		t.Fatalf("idempotent retry append: %v", err)
	}
	if inserted {
		t.Fatal("idempotent retry inserted duplicate receipt")
	}
	assertIntegrationReceiptPersistenceCounts(t, client, issueID, receipt.SourceOID, 1, 1)
}

func TestAppendTaskIntegrationReceiptIfAbsentContendsAcrossProcessAuthorities(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	seed := newTestClientAtPath(t, dbPath, slog.Default())
	issueID, err := seed.Create(ctx, CreateTaskParams{Title: "Cross-process receipt", Type: domain.TypeBug})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	type worker struct {
		cmd   *exec.Cmd
		stdin io.WriteCloser
		out   *bufio.Reader
	}
	workers := make([]worker, 2)
	for i := range workers {
		cmd := exec.Command(executable, "-test.run=^TestTaskIntegrationReceiptIndependentAuthorityHelper$")
		cmd.Env = append(os.Environ(), "AZEDARACH_RECEIPT_HELPER=1", "AZEDARACH_RECEIPT_DB="+dbPath, "AZEDARACH_RECEIPT_ISSUE="+issueID)
		stdin, pipeErr := cmd.StdinPipe()
		if pipeErr != nil {
			t.Fatalf("worker %d stdin: %v", i, pipeErr)
		}
		readyRead, readyWrite, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatalf("worker %d coordination pipe: %v", i, pipeErr)
		}
		cmd.ExtraFiles = []*os.File{readyWrite}
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start worker %d: %v", i, err)
		}
		_ = readyWrite.Close()
		workers[i] = worker{cmd: cmd, stdin: stdin, out: bufio.NewReader(readyRead)}
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
		line, readErr := workers[i].out.ReadString('\n')
		if readErr != nil || strings.TrimSpace(line) != "INITIALIZED" {
			t.Fatalf("worker %d initialization = %q, %v", i, line, readErr)
		}
	}
	for i := range workers {
		if _, err := io.WriteString(workers[i].stdin, "start\n"); err != nil {
			t.Fatalf("start receipt mutation in worker %d: %v", i, err)
		}
	}
	for i := range workers {
		line, readErr := workers[i].out.ReadString('\n')
		if readErr != nil || strings.TrimSpace(line) != "READY" {
			t.Fatalf("worker %d readiness = %q, %v", i, line, readErr)
		}
	}
	for i := range workers {
		if _, err := io.WriteString(workers[i].stdin, "go\n"); err != nil {
			t.Fatalf("release worker %d: %v", i, err)
		}
		_ = workers[i].stdin.Close()
	}
	insertCount := 0
	for i := range workers {
		line, readErr := workers[i].out.ReadString('\n')
		if readErr != nil {
			t.Fatalf("worker %d result: %v", i, readErr)
		}
		if strings.TrimSpace(line) == "INSERTED" {
			insertCount++
		} else if strings.TrimSpace(line) != "EXISTS" {
			t.Fatalf("worker %d result = %q", i, line)
		}
		if err := workers[i].cmd.Wait(); err != nil {
			t.Fatalf("worker %d wait: %v", i, err)
		}
	}
	if insertCount != 1 {
		t.Fatalf("cross-process insert count = %d, want 1", insertCount)
	}
	assertIntegrationReceiptPersistenceCounts(t, seed, issueID, testTaskIntegrationReceipt().SourceOID, 1, 1)
}

func projectionSourceAdvanceCount(t *testing.T, client *Client) int {
	t.Helper()
	db, err := client.dbHandle()
	if err != nil {
		t.Fatalf("open issue database: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM projection_deltas WHERE kind=?`, domain.ProjectionKindSourceAdvance).Scan(&count); err != nil {
		t.Fatalf("count all source advances: %v", err)
	}
	return count
}

func TestTaskIntegrationReceiptIndependentAuthorityHelper(t *testing.T) {
	if os.Getenv("AZEDARACH_RECEIPT_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	client := NewClientAtPath(os.Getenv("AZEDARACH_RECEIPT_DB"), slog.Default(), WithExistingDatabaseOnly())
	defer client.CloseDB()
	coordination := os.NewFile(3, "receipt-coordination")
	if coordination == nil {
		t.Fatal("open receipt coordination descriptor")
	}
	defer coordination.Close()
	if _, err := client.dbHandle(); err != nil {
		fmt.Fprintln(coordination, "ERROR initialize:", err)
		return
	}
	fmt.Fprintln(coordination, "INITIALIZED")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatalf("wait for mutation start: %v", err)
	}
	var once sync.Once
	ctx := WithTaskIntegrationReceiptMutationHookForTest(context.Background(), func(stage string) error {
		if stage != taskIntegrationReceiptHookBeforeTxBegin {
			return nil
		}
		once.Do(func() {
			fmt.Fprintln(coordination, "READY")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		})
		return nil
	})
	inserted, err := client.AppendTaskIntegrationReceiptIfAbsent(ctx, os.Getenv("AZEDARACH_RECEIPT_ISSUE"), testTaskIntegrationReceipt(), "/tmp/worktree")
	if err != nil {
		fmt.Fprintln(coordination, "ERROR append:", err)
		return
	}
	if inserted {
		fmt.Fprintln(coordination, "INSERTED")
	} else {
		fmt.Fprintln(coordination, "EXISTS")
	}
}

func testTaskIntegrationReceipt() TaskIntegrationReceipt {
	return TaskIntegrationReceipt{
		ProjectID: "project-a", SourceBranch: "feature/atomic-receipt", TargetBranch: "main",
		Integrated: true, ConfiguredBaseTarget: true, TargetID: "base", BaseOID: "base-exact",
		SourceOID: "source-exact", TargetOID: "target-exact", PublicationOperationID: "publication-exact",
	}
}

func assertIntegrationReceiptPersistenceCounts(t *testing.T, client *Client, issueID, sourceOID string, wantReceipts, wantAdvances int) {
	t.Helper()
	db, err := client.dbHandle()
	if err != nil {
		t.Fatalf("open issue database: %v", err)
	}
	var receipts, advances int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_observation_events WHERE issue_id=? AND event_type=? AND json_extract(payload_json, '$.source_oid')=?`, issueID, domain.IssueEventTaskIntegrationCompleted, sourceOID).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM projection_deltas WHERE kind=? AND idempotency_key IN (SELECT 'issue-observation:' || id FROM issue_observation_events WHERE issue_id=? AND event_type=? AND json_extract(payload_json, '$.source_oid')=?)`, domain.ProjectionKindSourceAdvance, issueID, domain.IssueEventTaskIntegrationCompleted, sourceOID).Scan(&advances); err != nil {
		t.Fatalf("count source advances: %v", err)
	}
	if receipts != wantReceipts || advances != wantAdvances {
		t.Fatalf("receipt/advance counts = %d/%d, want %d/%d", receipts, advances, wantReceipts, wantAdvances)
	}
}

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
