package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestProjectionDeltaCommitIsAtomicIdempotentAndSnapshotHistorical(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	var mutationCalls atomic.Int32
	first, err := client.CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "issue", Key: "a", Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "create-a", Payload: json.RawMessage(`{"state":"open"}`)}, func(ctx context.Context, tx ProjectionMutation) error {
		mutationCalls.Add(1)
		_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS projection_test_authority(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO projection_test_authority(key,value) VALUES('a','open')`)
		return err
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.Cursor)

	replayed, err := client.CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "issue", Key: "a", Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "create-a", Payload: json.RawMessage(`{ "state": "open" }`)}, func(context.Context, ProjectionMutation) error {
		mutationCalls.Add(1)
		return errors.New("replayed mutation must not run")
	})
	require.NoError(t, err)
	require.Equal(t, first.Cursor, replayed.Cursor)
	require.Equal(t, int32(1), mutationCalls.Load())

	second, err := client.CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "issue", Key: "a", Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "update-a", Payload: json.RawMessage(`{"state":"active"}`)}, func(ctx context.Context, tx ProjectionMutation) error {
		_, err := tx.ExecContext(ctx, `UPDATE projection_test_authority SET value='active' WHERE key='a'`)
		return err
	})
	require.NoError(t, err)
	require.Equal(t, uint64(2), second.Cursor)
	_, err = client.CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "issue", Key: "a", Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "failed-update", Payload: json.RawMessage(`{"state":"broken"}`)}, func(ctx context.Context, tx ProjectionMutation) error {
		if _, err := tx.ExecContext(ctx, `UPDATE projection_test_authority SET value='broken' WHERE key='a'`); err != nil {
			return err
		}
		return errors.New("injected authority failure")
	})
	require.ErrorContains(t, err, "injected authority failure")
	db, err := client.dbHandle()
	require.NoError(t, err)
	var authorityValue string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM projection_test_authority WHERE key='a'`).Scan(&authorityValue))
	require.Equal(t, "active", authorityValue)

	atOne, err := client.ProjectionSnapshotAt(ctx, "p", 1)
	require.NoError(t, err)
	require.JSONEq(t, `{"state":"open"}`, string(atOne.Values[0].Payload))
	atTwo, err := client.ProjectionSnapshotAt(ctx, "p", 2)
	require.NoError(t, err)
	require.JSONEq(t, `{"state":"active"}`, string(atTwo.Values[0].Payload))

	_, err = client.CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "issue", Key: "a", Operation: domain.ProjectionDeltaDelete, IdempotencyKey: "delete-a"}, nil)
	require.NoError(t, err)
	atThree, err := client.ProjectionSnapshotAt(ctx, "p", 3)
	require.NoError(t, err)
	require.Empty(t, atThree.Values)

	_, err = client.CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "issue", Key: "a", Operation: domain.ProjectionDeltaDelete, IdempotencyKey: "create-a"}, nil)
	require.ErrorIs(t, err, domain.ErrConflict)
}

func TestProjectionSnapshotReadDoesNotContendWithWriterOrMutateAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, nil)
	defer client.CloseDB()
	_, err := client.CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "task", Key: "a", Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "a", Payload: json.RawMessage(`{"n":1}`)}, nil)
	require.NoError(t, err)
	db, err := client.dbHandle()
	require.NoError(t, err)
	var beforeHead uint64
	var beforeUpdated string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT head_cursor,updated_at FROM projection_streams WHERE project_id='p'`).Scan(&beforeHead, &beforeUpdated))

	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(100)&_txlock=immediate")
	require.NoError(t, err)
	defer writer.Close()
	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	readCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	snapshot, err := client.ProjectionSnapshotAt(readCtx, "p", 1)
	require.NoError(t, err)
	require.Len(t, snapshot.Values, 1)
	var afterHead uint64
	var afterUpdated string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT head_cursor,updated_at FROM projection_streams WHERE project_id='p'`).Scan(&afterHead, &afterUpdated))
	require.Equal(t, beforeHead, afterHead)
	require.Equal(t, beforeUpdated, afterUpdated)
}

func TestProjectionDeltaMultiClientWritersAreMonotonicAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	clients := []*Client{NewClientAtPath(path, nil), NewClientAtPath(path, nil)}
	for _, client := range clients {
		defer client.CloseDB()
	}
	_, err := clients[0].ProjectionSnapshotAt(ctx, "p", 0)
	require.NoError(t, err)

	const writes = 40
	cursors := make(chan uint64, writes)
	errs := make(chan error, writes+1)
	var wg sync.WaitGroup
	readerDone := make(chan struct{})
	writersDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		writersFinished := false
		for {
			deltas, head, err := clients[0].ListProjectionDeltas(ctx, "p", 0, writes+1)
			if err != nil {
				errs <- err
				return
			}
			for i, delta := range deltas {
				if delta.Cursor != uint64(i+1) || delta.Cursor > head {
					errs <- fmt.Errorf("inconsistent concurrent batch: index=%d cursor=%d head=%d", i, delta.Cursor, head)
					return
				}
			}
			if head == writes {
				return
			}
			if writersFinished {
				errs <- fmt.Errorf("writers completed with projection head %d, want %d", head, writes)
				return
			}
			select {
			case <-writersDone:
				writersFinished = true
			default:
			}
		}
	}()
	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			delta, err := clients[i%len(clients)].CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "task", Key: fmt.Sprintf("k-%02d", i), Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: fmt.Sprintf("write-%02d", i), Payload: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))}, nil)
			if err != nil {
				errs <- err
				return
			}
			cursors <- delta.Cursor
		}(i)
	}
	wg.Wait()
	close(writersDone)
	<-readerDone
	close(errs)
	close(cursors)
	for err := range errs {
		require.NoError(t, err)
	}
	got := make([]int, 0, writes)
	for cursor := range cursors {
		got = append(got, int(cursor))
	}
	sort.Ints(got)
	for i := range got {
		require.Equal(t, i+1, got[i])
	}

	for _, client := range clients {
		require.NoError(t, client.CloseDB())
	}
	restarted := NewClientAtPath(path, nil)
	defer restarted.CloseDB()
	deltas, head, err := restarted.ListProjectionDeltas(ctx, "p", 0, writes+1)
	require.NoError(t, err)
	require.Equal(t, uint64(writes), head)
	require.Len(t, deltas, writes)
	for i, delta := range deltas {
		require.Equal(t, uint64(i+1), delta.Cursor)
	}
}

func TestProjectionDeltaWatchCancellationGapAndConsumerCursorAreTyped(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	defer client.CloseDB()
	ctx := context.Background()
	_, err := client.ProjectionSnapshotAt(ctx, "p", 1)
	var gap *domain.ProjectionGapError
	require.ErrorAs(t, err, &gap)

	watchCtx, cancel := context.WithTimeout(ctx, 35*time.Millisecond)
	defer cancel()
	_, _, err = client.WatchProjectionDeltas(watchCtx, "p", 0, 1)
	require.ErrorIs(t, err, domain.ErrProjectionCanceled)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	for i := 1; i <= 2; i++ {
		_, err := client.CommitProjectionDelta(ctx, ProjectionDeltaParams{ProjectID: "p", Kind: "task", Key: fmt.Sprint(i), Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: fmt.Sprint(i), Payload: json.RawMessage(`{}`)}, nil)
		require.NoError(t, err)
	}
	require.NoError(t, client.AdvanceProjectionConsumerCursor(ctx, "p", "global", 0, 2))
	cursor, err := client.ProjectionConsumerCursor(ctx, "p", "global")
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursor)
	_, err = client.ProjectionConsumerCursor(ctx, "p", "")
	require.ErrorContains(t, err, "consumer is required")
	err = client.AdvanceProjectionConsumerCursor(ctx, "p", "global", 0, 1)
	require.ErrorAs(t, err, &gap)
	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM projection_deltas WHERE project_id='p' AND cursor=1`)
	require.NoError(t, err)
	_, _, err = client.ListProjectionDeltas(ctx, "p", 0, 10)
	require.ErrorAs(t, err, &gap)
	_, err = client.ProjectionSnapshotAt(ctx, "p", 2)
	require.ErrorAs(t, err, &gap)
}

func TestProjectionDeltaMigrationFreshHistoricalReopenRollbackAndDrift(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, nil)
	_, err := client.ProjectionSnapshotAt(ctx, "p", 0)
	require.NoError(t, err)
	require.NoError(t, client.CloseDB())

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	var applied int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='0047_projection_delta_authority'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, db.Close())

	reopened := NewClientAtPath(path, nil)
	_, err = reopened.ProjectionSnapshotAt(ctx, "p", 0)
	require.NoError(t, err)
	require.NoError(t, reopened.CloseDB())

	historicalPath := filepath.Join(t.TempDir(), "historical.db")
	historical := NewClientAtPath(historicalPath, nil)
	_, err = historical.ProjectionSnapshotAt(ctx, "p", 0)
	require.NoError(t, err)
	require.NoError(t, historical.CloseDB())
	historicalDB, err := sql.Open("sqlite", "file:"+historicalPath)
	require.NoError(t, err)
	_, err = historicalDB.ExecContext(ctx, `DROP TABLE projection_consumer_cursors; DROP TABLE projection_deltas; DROP TABLE projection_streams; DELETE FROM schema_migrations WHERE id='0047_projection_delta_authority'`)
	require.NoError(t, err)
	require.NoError(t, historicalDB.Close())
	upgraded := NewClientAtPath(historicalPath, nil)
	_, err = upgraded.ProjectionSnapshotAt(ctx, "p", 0)
	require.NoError(t, err)
	require.NoError(t, upgraded.CloseDB())

	rollbackPath := filepath.Join(t.TempDir(), "rollback.db")
	rollbackDB, err := sql.Open("sqlite", "file:"+rollbackPath)
	require.NoError(t, err)
	require.NoError(t, ensureMigrationTable(ctx, rollbackDB))
	_, err = rollbackDB.ExecContext(ctx, `INSERT INTO schema_migrations(id,applied_at) VALUES('0047_projection_delta_authority','2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	err = applyProjectionDeltaAuthorityMigration(ctx, rollbackDB, "0047_projection_delta_authority")
	require.Error(t, err)
	for _, table := range []string{"projection_streams", "projection_deltas", "projection_consumer_cursors"} {
		var exists bool
		require.NoError(t, rollbackDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name=?)`, table).Scan(&exists))
		require.False(t, exists)
	}
	require.NoError(t, rollbackDB.Close())

	driftDB, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = driftDB.ExecContext(ctx, `DROP INDEX idx_projection_deltas_key_history`)
	require.NoError(t, err)
	require.NoError(t, driftDB.Close())
	drifted := NewClientAtPath(path, nil)
	_, err = drifted.ProjectionSnapshotAt(ctx, "p", 0)
	require.ErrorContains(t, err, "missing index idx_projection_deltas_key_history")
	_ = drifted.CloseDB()
}
