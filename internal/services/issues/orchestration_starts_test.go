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

func TestBeginOrchestrationStartIsCrossProcessAtomic(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	first := newTestClientAtPath(t, path, slog.Default())
	second := newTestClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = first.CloseDB(); _ = second.CloseDB() })
	issueID, err := first.Create(ctx, CreateTaskParams{Title: "worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, tc := range []struct {
		client *Client
		actor  string
	}{{first, "orchestrator-a"}, {second, "orchestrator-b"}} {
		wg.Add(1)
		go func(i int, tc struct {
			client *Client
			actor  string
		}) {
			defer wg.Done()
			<-start
			_, err := tc.client.BeginOrchestrationStart(ctx, "project", issueID, tc.actor+"-intent", tc.actor, "session.start:"+issueID)
			errs <- err
		}(i, tc)
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, conflicted := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected begin error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestRequestedOrchestrationStartIsDurableIdempotentAndCompletable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	first := NewClientAtPath(path, slog.Default())
	issueID, err := first.Create(ctx, CreateTaskParams{Title: "queued start", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := first.QueueRequestedOrchestrationStart(ctx, "project", issueID, "intent", "actor", "dedupe", "request-digest")
	if err != nil {
		t.Fatal(err)
	}
	second := NewClientAtPath(path, slog.Default())
	t.Cleanup(func() { _ = first.CloseDB(); _ = second.CloseDB() })
	replayed, err := second.QueueRequestedOrchestrationStart(ctx, "project", issueID, "intent", "actor", "dedupe", "request-digest")
	if err != nil || replayed != requested {
		t.Fatalf("replayed=%+v requested=%+v err=%v", replayed, requested, err)
	}
	if _, err := second.QueueRequestedOrchestrationStart(ctx, "project", issueID, "intent", "other", "dedupe", "request-digest"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("identity conflict=%v", err)
	}
	if _, err := second.QueueRequestedOrchestrationStart(ctx, "project", issueID, "intent", "actor", "dedupe", "changed-request"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("request digest conflict=%v", err)
	}
	if err := second.UpdateRequestedOrchestrationStart(ctx, replayed, "completed", "complete", nil); err != nil {
		t.Fatal(err)
	}
	if pending, err := first.PendingRequestedOrchestrationStarts(ctx, "project"); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestBeginOrchestrationStartSameIntentRetriesAfterCompensation(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "worker", Type: domain.TypeTask, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.BeginOrchestrationStart(ctx, "project", issueID, "split-start", "actor", "dedupe")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CompensateOrchestrationStart(ctx, first, errors.New("pre-dispatch failure")); err != nil {
		t.Fatal(err)
	}
	retry, err := client.BeginOrchestrationStart(ctx, "project", issueID, "split-start", "actor", "dedupe")
	if err != nil {
		t.Fatalf("retry compensated intent: %v", err)
	}
	if retry.State != "claimed" || !retry.ClaimAcquired {
		t.Fatalf("retry=%+v", retry)
	}
	if _, err := client.BeginOrchestrationStart(ctx, "project", issueID, "split-start", "actor", "changed"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed dedupe error=%v", err)
	}
}

func TestCompensateOrchestrationStartDurablyReleasesOnlyAcquiredClaim(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.BeginOrchestrationStart(ctx, "project", issueID, "intent-1", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	if !attempt.ClaimAcquired {
		t.Fatal("new claim not marked acquired")
	}
	if err := client.CompensateOrchestrationStart(ctx, attempt, errors.New("submit failed")); err != nil {
		t.Fatal(err)
	}
	if err := client.CompensateOrchestrationStart(ctx, attempt, errors.New("same compensation retried")); err != nil {
		t.Fatalf("idempotent compensation retry: %v", err)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Ownership != nil {
		t.Fatalf("ownership after compensation = %+v", task.Ownership)
	}
	pending, err := client.PendingOrchestrationStarts(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending attempts = %+v", pending)
	}

	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, OwnershipClaimParams{OwnerID: "orchestrator", OwnerKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	attempt, err = client.BeginOrchestrationStart(ctx, "project", issueID, "intent-2", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.ClaimAcquired {
		t.Fatal("pre-existing same-actor claim marked acquired")
	}
	if err := client.CompensateOrchestrationStart(ctx, attempt, errors.New("submit failed")); err != nil {
		t.Fatal(err)
	}
	task, err = client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Ownership == nil || task.Ownership.OwnerID != "orchestrator" {
		t.Fatalf("pre-existing ownership was released: %+v", task.Ownership)
	}
}

func TestCompleteOrchestrationStartIsIdempotent(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.BeginOrchestrationStart(ctx, "project", issueID, "intent", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteOrchestrationStart(ctx, attempt, "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteOrchestrationStart(ctx, attempt, "op-1"); err != nil {
		t.Fatal(err)
	}
	if pending, err := client.PendingOrchestrationStarts(ctx, "project"); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestCompensateOrchestrationStartOperationHandlesSubmitRaceAndRetry(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.BeginOrchestrationStart(ctx, "project", issueID, "intent", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	compensated, err := client.CompensateOrchestrationStartOperation(ctx, "project", attempt.DedupeKey, "op-fast-failure", errors.New("launch failed"))
	if err != nil || !compensated {
		t.Fatalf("compensate claimed attempt = %t, %v", compensated, err)
	}
	compensated, err = client.CompensateOrchestrationStartOperation(ctx, "project", attempt.DedupeKey, "op-fast-failure", errors.New("launch failed"))
	if err != nil || compensated {
		t.Fatalf("idempotent terminal retry = %t, %v", compensated, err)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.StatusOpen || task.Ownership != nil {
		t.Fatalf("terminal compensation left partial state: %+v", task)
	}
	if pending, err := client.PendingOrchestrationStarts(ctx, "project"); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestCompensateSubmittedOrchestrationStartOperation(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.BeginOrchestrationStart(ctx, "project", issueID, "intent", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteOrchestrationStart(ctx, attempt, "op-failed"); err != nil {
		t.Fatal(err)
	}
	compensated, err := client.CompensateOrchestrationStartOperation(ctx, "project", attempt.DedupeKey, "op-failed", errors.New("launch failed"))
	if err != nil || !compensated {
		t.Fatalf("compensate submitted attempt = %t, %v", compensated, err)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Ownership != nil {
		t.Fatalf("submitted failure retained ownership: %+v", task.Ownership)
	}
}

func TestCompensateOrchestrationStartOperationClearsAllDedupedAttempts(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, CreateTaskParams{Title: "worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	dedupeKey := "session.start:" + issueID
	first, err := client.BeginOrchestrationStart(ctx, "project", issueID, "intent-1", "orchestrator", dedupeKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteOrchestrationStart(ctx, first, "op-shared"); err != nil {
		t.Fatal(err)
	}
	second, err := client.BeginOrchestrationStart(ctx, "project", issueID, "intent-2", "orchestrator", dedupeKey)
	if err != nil {
		t.Fatal(err)
	}
	if second.ClaimAcquired {
		t.Fatal("second deduped attempt must preserve the first attempt's claim")
	}
	if err := client.CompleteOrchestrationStart(ctx, second, "op-shared"); err != nil {
		t.Fatal(err)
	}

	compensated, err := client.CompensateOrchestrationStartOperation(ctx, "project", dedupeKey, "op-shared", errors.New("launch failed"))
	if err != nil || !compensated {
		t.Fatalf("compensate shared operation = %t, %v", compensated, err)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	var residue int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orchestration_start_attempts WHERE project_id=? AND dedupe_key=? AND state IN ('claimed','submitted')`, "project", dedupeKey).Scan(&residue); err != nil {
		t.Fatal(err)
	}
	if residue != 0 {
		t.Fatalf("nonterminal attempt residue = %d, want 0", residue)
	}
	task, err := client.GetWithRuntime(ctx, "project", issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Ownership != nil {
		t.Fatalf("shared failed operation retained ownership: %+v", task.Ownership)
	}
	compensated, err = client.CompensateOrchestrationStartOperation(ctx, "project", dedupeKey, "op-shared", errors.New("same failure retried"))
	if err != nil || compensated {
		t.Fatalf("idempotent shared-operation retry = %t, %v", compensated, err)
	}
	retry, err := client.BeginOrchestrationStart(ctx, "project", issueID, "intent-1", "orchestrator", dedupeKey)
	if err != nil || retry.State != "claimed" || !retry.ClaimAcquired {
		t.Fatalf("same-intent retry = %+v, %v, want fresh claimed attempt", retry, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orchestration_start_attempts WHERE project_id=? AND dedupe_key=? AND state IN ('claimed','submitted')`, "project", dedupeKey).Scan(&residue); err != nil {
		t.Fatal(err)
	}
	if residue != 1 {
		t.Fatalf("same-intent retry active attempts = %d, want 1", residue)
	}
}

func TestOrchestrationStartResumesSameIntentAfterClientRestart(t *testing.T) {
	parallelIssueStoreTest(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	first := newTestClientAtPath(t, path, slog.Default())
	issueID, err := first.Create(ctx, CreateTaskParams{Title: "worker", Status: domain.StatusOpen, Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	original, err := first.BeginOrchestrationStart(ctx, "project", issueID, "stable-intent", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.CloseDB(); err != nil {
		t.Fatal(err)
	}

	restarted := newTestClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = restarted.CloseDB() })
	resumed, err := restarted.BeginOrchestrationStart(ctx, "project", issueID, "stable-intent", "orchestrator", "session.start:"+issueID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed != original {
		t.Fatalf("resumed attempt = %+v, want %+v", resumed, original)
	}
	if err := restarted.CompleteOrchestrationStart(ctx, resumed, "op-after-restart"); err != nil {
		t.Fatal(err)
	}
}
