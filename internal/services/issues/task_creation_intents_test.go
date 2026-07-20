package issues

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestCreateIdempotentWithRuntimeFailurePhasesRollbackAndRetry(t *testing.T) {
	for _, phase := range []string{"after_issue", "after_parent_edge", "after_created_in_edge"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			client := newTestClient(t)
			t.Cleanup(func() { _ = client.CloseDB() })
			parent, err := client.Create(ctx, CreateTaskParams{Title: "parent", Type: domain.TypeEpic})
			if err != nil {
				t.Fatal(err)
			}
			params := CreateTaskParams{IntentKey: "split", RequestDigest: "digest", Title: "child", Type: domain.TypeTask, ParentID: &parent, CreatedFromID: &parent}
			client.taskCreationIntentFailureHook = func(stage string) error {
				if stage == phase {
					return fmt.Errorf("injected %s", phase)
				}
				return nil
			}
			if _, _, err := client.CreateIdempotentWithRuntime(ctx, "project", params); err == nil {
				t.Fatal("injected create unexpectedly succeeded")
			}
			client.taskCreationIntentFailureHook = nil
			task, created, err := client.CreateIdempotentWithRuntime(ctx, "project", params)
			if err != nil || !created {
				t.Fatalf("retry=(%s,%t,%v)", task.ID, created, err)
			}
			db, err := client.dbHandle()
			if err != nil {
				t.Fatal(err)
			}
			var children, intents, edges int
			_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE title='child'`).Scan(&children)
			_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_creation_intents WHERE project_id='project' AND intent_key='split'`).Scan(&intents)
			_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_dependencies WHERE issue_id=? AND depends_on_id=? AND tombstoned_at IS NULL`, task.ID.String(), parent).Scan(&edges)
			if children != 1 || intents != 1 || edges != 2 {
				t.Fatalf("children=%d intents=%d edges=%d", children, intents, edges)
			}
		})
	}
}

func TestCreateIdempotentWithRuntimeReplaysCanonicalTaskAndAtomicEdges(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.CloseDB() })
	parent, err := client.Create(ctx, CreateTaskParams{Title: "parent", Type: domain.TypeEpic})
	if err != nil {
		t.Fatal(err)
	}
	params := CreateTaskParams{IntentKey: "split-1", RequestDigest: "digest-1", Title: "child", Type: domain.TypeTask, ParentID: &parent, CreatedFromID: &parent}
	first, created, err := client.CreateIdempotentWithRuntime(ctx, "project", params)
	if err != nil || !created {
		t.Fatalf("first=(%+v,%t,%v)", first, created, err)
	}
	replay, created, err := client.CreateIdempotentWithRuntime(ctx, "project", params)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay=(%+v,%t,%v), first=%s", replay, created, err, first.ID)
	}
	db, err := client.dbHandle()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []domain.DependencyType{domain.DependencyParentChild, domain.DependencyCreatedIn} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_dependencies WHERE issue_id=? AND depends_on_id=? AND dependency_type=? AND tombstoned_at IS NULL`, first.ID.String(), parent, string(kind)).Scan(&count); err != nil || count != 1 {
			t.Fatalf("edge %s count=%d err=%v", kind, count, err)
		}
	}
	params.RequestDigest = "changed"
	if _, _, err := client.CreateIdempotentWithRuntime(ctx, "project", params); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM issues WHERE id=?`, first.ID.String()); err != nil {
		t.Fatalf("explicit child deletion blocked by split receipt: %v", err)
	}
	var receipts int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_creation_intents WHERE issue_id=?`, first.ID.String()).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipts after explicit delete=%d err=%v", receipts, err)
	}
}

func TestCreateIdempotentWithRuntimeConcurrentClientsConverge(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	first := newTestClientAtPath(t, path, slog.Default())
	second := newTestClientAtPath(t, path, slog.Default())
	t.Cleanup(func() { _ = first.CloseDB(); _ = second.CloseDB() })
	params := CreateTaskParams{IntentKey: "split-concurrent", RequestDigest: "same", Title: "child", Type: domain.TypeTask}
	start := make(chan struct{})
	type outcome struct {
		id      string
		created bool
		err     error
	}
	outcomes := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, client := range []*Client{first, second} {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			<-start
			task, created, err := client.CreateIdempotentWithRuntime(ctx, "project", params)
			outcomes <- outcome{id: task.ID.String(), created: created, err: err}
		}(client)
	}
	close(start)
	wg.Wait()
	close(outcomes)
	createdCount, canonical := 0, ""
	for result := range outcomes {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if canonical == "" {
			canonical = result.id
		} else if result.id != canonical {
			t.Fatalf("ids diverged: %s != %s", result.id, canonical)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count=%d, want 1", createdCount)
	}
}
