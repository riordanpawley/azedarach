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

func TestCompensateOrchestrationStartDurablyReleasesOnlyAcquiredClaim(t *testing.T) {
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

func TestOrchestrationStartResumesSameIntentAfterClientRestart(t *testing.T) {
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
