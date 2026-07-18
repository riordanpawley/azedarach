package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

type projectionCodedError struct{ code int }

func (e projectionCodedError) Error() string { return "injected sqlite read failure" }
func (e projectionCodedError) Code() int     { return e.code }

type blockingProjectionDeltaNotifier struct {
	events       chan struct{}
	errors       chan error
	closeStarted chan struct{}
	releaseClose chan struct{}
	closeOnce    sync.Once
}

func newBlockingProjectionDeltaNotifier() *blockingProjectionDeltaNotifier {
	return &blockingProjectionDeltaNotifier{
		events:       make(chan struct{}),
		errors:       make(chan error),
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
}

func (n *blockingProjectionDeltaNotifier) Events() <-chan struct{} { return n.events }
func (n *blockingProjectionDeltaNotifier) Errors() <-chan error    { return n.errors }
func (n *blockingProjectionDeltaNotifier) Close() error {
	n.closeOnce.Do(func() {
		close(n.closeStarted)
		<-n.releaseClose
		close(n.events)
		close(n.errors)
	})
	return nil
}

type failingProjectionDeltaRows struct {
	projectionDeltaRows
	failAfter int
	read      int
	err       error
}

func (r *failingProjectionDeltaRows) Next() bool {
	if r.read == r.failAfter {
		return false
	}
	if !r.projectionDeltaRows.Next() {
		return false
	}
	r.read++
	return true
}

func (r *failingProjectionDeltaRows) Err() error {
	if r.read == r.failAfter {
		return r.err
	}
	return r.projectionDeltaRows.Err()
}

func TestProjectionReadErrorClassifiesOnlyShortReadIOErrorAsRetryable(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "consumer.db"), nil)
	shortRead := client.projectionReadError("read projection delta head", projectionCodedError{code: sqliteutil.SQLiteIOErrorShortRead})
	require.ErrorIs(t, shortRead, domain.ErrProjectionRetryable)
	require.ErrorContains(t, shortRead, "db_path="+sqliteutil.CanonicalPath(client.dbPath))
	require.ErrorContains(t, shortRead, "sqlite_extended_code=522")
	require.ErrorContains(t, shortRead, "sqlite_symbol=SQLITE_IOERR_SHORT_READ")

	structural := client.projectionReadError("read projection delta head", projectionCodedError{code: 11})
	require.NotErrorIs(t, structural, domain.ErrProjectionRetryable)
	require.ErrorIs(t, structural, ErrSQLiteCorrupt)
	require.ErrorContains(t, structural, "sqlite_symbol=SQLITE_CORRUPT")
	require.ErrorIs(t, client.CorruptionError(), ErrSQLiteCorrupt)
}

func TestProjectionSnapshotSourceIterationErrorCannotCommitIncompleteVector(t *testing.T) {
	tests := []struct {
		name      string
		code      int
		retryable bool
	}{
		{name: "short read", code: sqliteutil.SQLiteIOErrorShortRead, retryable: true},
		{name: "structural corruption", code: 11, retryable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := NewClientAtPath(filepath.Join(t.TempDir(), "consumer.db"), nil)
			t.Cleanup(func() { require.NoError(t, client.CloseDB()) })
			for index, key := range []string{"a", "b"} {
				_, err := client.CommitProjectionDelta(ctx, ProjectionDeltaParams{
					ProjectID: "p", Kind: domain.ProjectionKindIssue, Key: key,
					Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: fmt.Sprintf("source-%d", index),
					Payload: json.RawMessage(`{"state":"open"}`),
				}, nil)
				require.NoError(t, err)
			}
			client.projectionSnapshotSourceRowsHook = func(rows projectionDeltaRows) projectionDeltaRows {
				return &failingProjectionDeltaRows{projectionDeltaRows: rows, failAfter: 1, err: projectionCodedError{code: tt.code}}
			}

			snapshot, err := client.ProjectionSnapshotAt(ctx, "p", 2)
			require.Error(t, err)
			require.Empty(t, snapshot.Sources)
			require.Equal(t, tt.retryable, errors.Is(err, domain.ErrProjectionRetryable))
			require.ErrorContains(t, err, "iterate projection snapshot sources")
			require.ErrorContains(t, err, fmt.Sprintf("sqlite_extended_code=%d", tt.code))
			if tt.retryable {
				require.NoError(t, client.CorruptionError())
				_, mutationErr := client.CommitProjectionDelta(ctx, ProjectionDeltaParams{
					ProjectID: "p", Kind: domain.ProjectionKindIssue, Key: "c",
					Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "source-after-short-read",
					Payload: json.RawMessage(`{"state":"open"}`),
				}, nil)
				require.NoError(t, mutationErr)
			} else {
				require.ErrorIs(t, client.CorruptionError(), ErrSQLiteCorrupt)
				client.projectionSnapshotSourceRowsHook = nil
				_, subsequentReadErr := client.ProjectionSnapshotAt(ctx, "p", 2)
				require.ErrorIs(t, subsequentReadErr, ErrSQLiteCorrupt)
				_, mutationErr := client.CommitProjectionDelta(ctx, ProjectionDeltaParams{
					ProjectID: "p", Kind: domain.ProjectionKindIssue, Key: "c",
					Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "source-after-corruption",
					Payload: json.RawMessage(`{"state":"open"}`),
				}, nil)
				require.ErrorIs(t, mutationErr, ErrSQLiteCorrupt)
			}
		})
	}
}

func TestProjectionDeltaActiveIssueMutationEmits(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	id, err := client.Create(context.Background(), CreateTaskParams{Title: "active delta", Type: domain.TypeTask})
	require.NoError(t, err)
	deltas, head, err := client.ListProjectionDeltas(context.Background(), "default", 0, 10)
	require.NoError(t, err)
	require.Equal(t, uint64(1), head)
	require.Len(t, deltas, 1)
	require.Equal(t, id, deltas[0].Key)
	require.Equal(t, domain.ProjectionKindIssue, deltas[0].Kind)
	require.Equal(t, "issue-observation:1", deltas[0].IdempotencyKey)
	assertLatestIssueDeltaMatchesStore(t, client, id)
}

func TestProjectionDeltaObservationWithoutIssueChangeEmitsEmptyAdvance(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })
	id, err := client.Create(ctx, CreateTaskParams{Title: "observed", Type: domain.TypeTask})
	require.NoError(t, err)
	_, err = client.AppendIssueObservationEvent(ctx, id, IssueObservationEventParams{Type: domain.IssueEventValidationPassed, Source: "test", Payload: map[string]any{"command": "go test"}})
	require.NoError(t, err)
	deltas, head, err := client.ListProjectionDeltas(ctx, "default", 1, 10)
	require.NoError(t, err)
	require.Equal(t, uint64(2), head)
	require.Len(t, deltas, 1)
	require.Equal(t, domain.ProjectionKindSourceAdvance, deltas[0].Kind)
	require.Equal(t, "legacy_issue_observation", deltas[0].Source.Authority)
	require.Equal(t, "2", deltas[0].Source.SourceFrom)
}

func TestProjectionDeltaSemanticPayloadMatchesAuthoritativeMutationMatrix(t *testing.T) {
	ctx := context.Background()
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	t.Cleanup(func() { _ = client.CloseDB() })

	parent, err := client.Create(ctx, CreateTaskParams{Title: "parent", Type: domain.TypeEpic})
	require.NoError(t, err)
	child, err := client.Create(ctx, CreateTaskParams{Title: "child", Description: "initial", Type: domain.TypeTask})
	require.NoError(t, err)
	assertLatestIssueDeltaMatchesStore(t, client, child)

	task, err := client.GetWithRuntime(ctx, "default", child)
	require.NoError(t, err)
	design := "semantic design"
	require.NoError(t, client.UpdateDetails(ctx, child, UpdateTaskParams{
		Title: task.Title, Description: "changed", Design: &design, Type: domain.TypeFeature,
		Priority: domain.Priority(1), Implementations: task.Implementations,
	}))
	assertLatestIssueDeltaMatchesStore(t, client, child)

	require.NoError(t, client.Update(ctx, child, domain.StatusInProgress))
	assertLatestIssueDeltaMatchesStore(t, client, child)
	require.NoError(t, client.AppendNotes(ctx, child, "semantic note"))
	assertLatestIssueDeltaMatchesStore(t, client, child)
	require.NoError(t, client.Update(ctx, parent, domain.StatusDone))
	require.NoError(t, client.AddDependency(ctx, child, parent, string(domain.DependencyParentChild)))
	assertLatestIssueDeltaMatchesStore(t, client, child)
	assertLatestIssueDeltaMatchesStore(t, client, parent)
	confirmedCtx := WithParentChildOrphanConfirmation(WithDependencyRemovalConfirmation(ctx))
	require.NoError(t, client.RemoveDependency(confirmedCtx, child, parent, string(domain.DependencyParentChild)))
	assertLatestIssueDeltaMatchesStore(t, client, child)
	task, err = client.GetWithRuntime(ctx, "default", child)
	require.NoError(t, err)
	task.Title = "synced semantic title"
	task.UpdatedAt = task.UpdatedAt.Add(time.Second)
	_, err = client.UpsertSyncedTask(ctx, task)
	require.NoError(t, err)
	assertLatestIssueDeltaMatchesStore(t, client, child)
	_, beforeReplay, err := client.ListProjectionDeltas(ctx, "default", 0, 1000)
	require.NoError(t, err)
	_, err = client.UpsertSyncedTask(ctx, task)
	require.NoError(t, err)
	_, afterReplay, err := client.ListProjectionDeltas(ctx, "default", 0, 1000)
	require.NoError(t, err)
	require.Equal(t, beforeReplay, afterReplay, "identical sync replay must not allocate a cursor")
	require.NoError(t, client.Archive(ctx, child))
	assertLatestIssueDeltaMatchesStore(t, client, child)
	require.NoError(t, client.Unarchive(ctx, child))
	assertLatestIssueDeltaMatchesStore(t, client, child)
}

func assertLatestIssueDeltaMatchesStore(t *testing.T, client *Client, issueID string) {
	t.Helper()
	deltas, head, err := client.ListProjectionDeltas(context.Background(), "default", 0, 1000)
	require.NoError(t, err)
	require.NotZero(t, head)
	var latest *domain.ProjectionDelta
	for i := range deltas {
		if deltas[i].Kind == domain.ProjectionKindIssue && deltas[i].Key == issueID {
			latest = &deltas[i]
		}
	}
	require.NotNil(t, latest)
	require.Equal(t, domain.ProjectionDeltaUpsert, latest.Operation)
	var payload domain.IssueProjectionDeltaPayload
	require.NoError(t, json.Unmarshal(latest.Payload, &payload))
	require.Equal(t, domain.IssueProjectionDeltaSchemaVersion, payload.SchemaVersion)
	require.Equal(t, issueID, payload.IssueID)
	require.False(t, payload.Deleted)
	require.NotNil(t, payload.Issue)
	want, err := client.GetWithRuntimeArchiveMode(context.Background(), "default", issueID, ArchiveInclude)
	require.NoError(t, err)
	want = domain.CanonicalIssueProjectionTask(want)
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(payload.Issue)
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(gotJSON))
}

func TestProjectionDeltaFirstReadIsPureAndRequiresExplicitOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, nil)
	_, _, err := client.ListProjectionDeltas(context.Background(), "default", 0, 1)
	require.ErrorIs(t, err, domain.ErrProjectionRetryable)
	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestProjectionDeltaWatchHasNoIdlePolling(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { _ = client.CloseDB() })
	var reads atomic.Int32
	client.projectionDeltaReadHook = func() { reads.Add(1) }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := client.WatchProjectionDeltas(ctx, "default", 0, 1)
	require.ErrorIs(t, err, domain.ErrProjectionCanceled)
	require.Equal(t, int32(2), reads.Load(), "idle watch must only read before and after event registration")
}

func TestProjectionDeltaCrossProcessWritersAreGapFreeAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := NewClientAtPath(path, nil)
	require.NoError(t, seed.OpenProjectionDeltaStore())
	require.NoError(t, seed.CloseDB())
	executable, err := os.Executable()
	require.NoError(t, err)
	commands := make([]*exec.Cmd, 2)
	for worker := range commands {
		commands[worker] = exec.Command(executable, "-test.run=TestProjectionDeltaSubprocessWriter$")
		commands[worker].Env = append(os.Environ(), "AZEDARACH_PROJECTION_SUBPROCESS=1", "AZEDARACH_PROJECTION_DB="+path, fmt.Sprintf("AZEDARACH_PROJECTION_WORKER=%d", worker))
		require.NoError(t, commands[worker].Start())
	}
	for _, command := range commands {
		require.NoError(t, command.Wait())
	}
	reader := NewClientAtPath(path, nil)
	require.NoError(t, reader.OpenProjectionDeltaStore())
	t.Cleanup(func() { _ = reader.CloseDB() })
	deltas, head, err := reader.ListProjectionDeltas(context.Background(), "p", 0, 100)
	require.NoError(t, err)
	require.Equal(t, uint64(41), head)
	require.Len(t, deltas, 41)
	for index, delta := range deltas {
		require.Equal(t, uint64(index+1), delta.Cursor)
	}
}

func TestProjectionDeltaWatchWakesForCrossProcessCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { _ = client.CloseDB() })
	ready := make(chan struct{})
	var reads atomic.Int32
	client.projectionDeltaReadHook = func() {
		if reads.Add(1) == 2 {
			close(ready)
		}
	}
	type result struct {
		deltas []domain.ProjectionDelta
		err    error
	}
	resultCh := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		deltas, _, err := client.WatchProjectionDeltas(ctx, "p", 0, 100)
		resultCh <- result{deltas: deltas, err: err}
	}()
	<-ready
	executable, err := os.Executable()
	require.NoError(t, err)
	command := exec.Command(executable, "-test.run=TestProjectionDeltaSubprocessWriter$")
	command.Env = append(os.Environ(), "AZEDARACH_PROJECTION_SUBPROCESS=1", "AZEDARACH_PROJECTION_DB="+path, "AZEDARACH_PROJECTION_WORKER=watch")
	require.NoError(t, command.Run())
	got := <-resultCh
	require.NoError(t, got.err)
	require.NotEmpty(t, got.deltas)
	require.Equal(t, uint64(1), got.deltas[0].Cursor)
}

func TestProjectionDeltaNotifierFansOutAndReopensWithStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := NewClientAtPath(path, nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { _ = client.CloseDB() })
	_, generation, err := client.projectionReadDBHandleWithGeneration()
	require.NoError(t, err)

	first, unsubscribeFirst, err := client.subscribeProjectionDeltaNotifier(generation)
	require.NoError(t, err)
	second, unsubscribeSecond, err := client.subscribeProjectionDeltaNotifier(generation)
	require.NoError(t, err)
	_, err = client.CommitProjectionDelta(context.Background(), ProjectionDeltaParams{
		ProjectID: "p", Kind: domain.ProjectionKindIssue, Key: "first",
		Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "first", Payload: json.RawMessage(`{}`),
	}, nil)
	require.NoError(t, err)
	<-first.events
	<-second.events
	unsubscribeFirst()
	unsubscribeSecond()

	require.NoError(t, client.CloseDB())
	require.NoError(t, client.OpenProjectionDeltaStore())
	_, generation, err = client.projectionReadDBHandleWithGeneration()
	require.NoError(t, err)
	reopened, unsubscribeReopened, err := client.subscribeProjectionDeltaNotifier(generation)
	require.NoError(t, err)
	defer unsubscribeReopened()
	_, err = client.CommitProjectionDelta(context.Background(), ProjectionDeltaParams{
		ProjectID: "p", Kind: domain.ProjectionKindIssue, Key: "reopened",
		Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "reopened", Payload: json.RawMessage(`{}`),
	}, nil)
	require.NoError(t, err)
	<-reopened.events
}

func TestProjectionDeltaCloseHoldsLifecycleBoundaryThroughNotifierTeardown(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	require.NoError(t, client.OpenProjectionDeltaStore())

	notifier := newBlockingProjectionDeltaNotifier()
	client.projectionNotifierMu.Lock()
	client.projectionNotifier = notifier
	client.projectionNotifierSubscriptions = make(map[*projectionDeltaSubscription]struct{})
	client.projectionNotifierWG.Add(1)
	go client.runProjectionDeltaNotifier(notifier)
	client.projectionNotifierMu.Unlock()

	closeDone := make(chan error, 1)
	go func() { closeDone <- client.CloseDB() }()
	<-notifier.closeStarted

	lifecycleAvailable := client.mu.TryLock()
	if lifecycleAvailable {
		client.mu.Unlock()
	}
	reopenDone := make(chan error, 1)
	go func() { reopenDone <- client.OpenProjectionDeltaStore() }()
	close(notifier.releaseClose)
	require.NoError(t, <-closeDone)
	require.NoError(t, <-reopenDone)
	t.Cleanup(func() { _ = client.CloseDB() })
	require.False(t, lifecycleAvailable, "reopen must not enter while notifier teardown still owns the client lifecycle")
}

func TestProjectionDeltaWatchRejectsCloseBetweenInitialReadAndSubscription(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { _ = client.CloseDB() })
	client.projectionWatchBeforeSubscribeHook = func() {
		require.NoError(t, client.CloseDB())
	}
	defer func() { client.projectionWatchBeforeSubscribeHook = nil }()

	_, _, err := client.WatchProjectionDeltas(context.Background(), "p", 0, 1)
	require.ErrorIs(t, err, domain.ErrProjectionRetryable)
	client.projectionNotifierMu.Lock()
	notifier := client.projectionNotifier
	client.projectionNotifierMu.Unlock()
	require.Nil(t, notifier, "a watch must not install a notifier after its database generation closes")
}

func TestProjectionDeltaClosedClientRejectsNotifierSubscription(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	_, generation, err := client.projectionReadDBHandleWithGeneration()
	require.NoError(t, err)
	_, unsubscribe, err := client.subscribeProjectionDeltaNotifier(generation)
	require.NoError(t, err)
	unsubscribe()
	require.NoError(t, client.CloseDB())
	t.Cleanup(func() { _ = client.CloseDB() })

	_, _, err = client.subscribeProjectionDeltaNotifier(generation)
	require.ErrorIs(t, err, domain.ErrProjectionRetryable)
	client.projectionNotifierMu.Lock()
	notifier := client.projectionNotifier
	subscriptionCount := len(client.projectionNotifierSubscriptions)
	client.projectionNotifierMu.Unlock()
	require.Nil(t, notifier)
	require.Zero(t, subscriptionCount)
}

func TestProjectionDeltaSubprocessWriter(t *testing.T) {
	if os.Getenv("AZEDARACH_PROJECTION_SUBPROCESS") != "1" {
		t.Skip("subprocess helper")
	}
	path, worker := os.Getenv("AZEDARACH_PROJECTION_DB"), os.Getenv("AZEDARACH_PROJECTION_WORKER")
	client := NewClientAtPath(path, nil)
	defer client.CloseDB()
	for index := 0; index < 20; index++ {
		_, err := client.CommitProjectionDelta(context.Background(), ProjectionDeltaParams{ProjectID: "p", Kind: domain.ProjectionKindIssue, Key: worker + "-" + fmt.Sprint(index), Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: worker + "-" + fmt.Sprint(index), Payload: json.RawMessage(`{}`)}, nil)
		require.NoError(t, err)
	}
	_, err := client.CommitProjectionDelta(context.Background(), ProjectionDeltaParams{ProjectID: "p", Kind: domain.ProjectionKindIssue, Key: "shared", Operation: domain.ProjectionDeltaUpsert, IdempotencyKey: "shared", Payload: json.RawMessage(`{"shared":true}`)}, nil)
	require.NoError(t, err)
}

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
	require.NoError(t, clients[0].OpenProjectionDeltaStore())
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
	require.NoError(t, restarted.OpenProjectionDeltaStore())
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
	require.NoError(t, client.OpenProjectionDeltaStore())
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
	require.NoError(t, client.OpenProjectionDeltaStore())
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
	require.NoError(t, reopened.OpenProjectionDeltaStore())
	_, err = reopened.ProjectionSnapshotAt(ctx, "p", 0)
	require.NoError(t, err)
	require.NoError(t, reopened.CloseDB())

	historicalPath := filepath.Join(t.TempDir(), "historical.db")
	historical := NewClientAtPath(historicalPath, nil)
	require.NoError(t, historical.OpenProjectionDeltaStore())
	_, err = historical.ProjectionSnapshotAt(ctx, "p", 0)
	require.NoError(t, err)
	require.NoError(t, historical.CloseDB())
	historicalDB, err := sql.Open("sqlite", "file:"+historicalPath)
	require.NoError(t, err)
	_, err = historicalDB.ExecContext(ctx, `DROP TABLE projection_consumer_cursors; DROP TABLE projection_deltas; DROP TABLE projection_streams; DELETE FROM schema_migrations WHERE id='0047_projection_delta_authority'`)
	require.NoError(t, err)
	require.NoError(t, historicalDB.Close())
	upgraded := NewClientAtPath(historicalPath, nil)
	require.NoError(t, upgraded.OpenProjectionDeltaStore())
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
	err = drifted.OpenProjectionDeltaStore()
	require.ErrorContains(t, err, "missing index idx_projection_deltas_key_history")
	_ = drifted.CloseDB()
}
