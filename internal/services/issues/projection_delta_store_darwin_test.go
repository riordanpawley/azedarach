package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestProjectionDeltaWatchCancellationPreservesSQLiteProcessLocks(t *testing.T) {
	if os.Getenv("AZEDARACH_SQLITE_LOCK_PROBE") == "1" {
		probeSQLiteWriteLock(t, os.Getenv("AZEDARACH_PROJECTION_DB"))
		return
	}

	path := filepath.Join(t.TempDir(), "consumer.db")
	client := NewClientAtPath(path, nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })

	db, err := client.dbHandle()
	require.NoError(t, err)
	require.NoError(t, func() error {
		_, execErr := db.ExecContext(context.Background(), `CREATE TABLE lock_probe(id INTEGER PRIMARY KEY)`)
		return execErr
	}())

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(context.Background(), `INSERT INTO lock_probe(id) VALUES(1)`)
	require.NoError(t, err)
	require.Equal(t, "busy", runSQLiteWriteLockProbe(t, path), "live SQLite transaction must exclude a second process")

	registered := make(chan struct{})
	var reads int
	client.projectionDeltaReadHook = func() {
		reads++
		if reads == 2 {
			close(registered)
		}
	}
	t.Cleanup(func() { client.projectionDeltaReadHook = nil })

	watchCtx, cancelWatch := context.WithCancel(context.Background())
	watchDone := make(chan error, 1)
	go func() {
		_, _, watchErr := client.WatchProjectionDeltas(watchCtx, "portable-consumer", 0, 1)
		watchDone <- watchErr
	}()
	<-registered

	cancelWatch()
	require.ErrorIs(t, <-watchDone, domain.ErrProjectionCanceled)
	require.Equal(t, "busy", runSQLiteWriteLockProbe(t, path), "watch teardown must not cancel SQLite's process-wide POSIX locks")

	require.NoError(t, tx.Rollback())
	require.Equal(t, "acquired", runSQLiteWriteLockProbe(t, path), "second process must acquire the lock after the owner releases it")
}

func TestProjectionDeltaWatcherClosePreservesIndependentSQLitePoolLocks(t *testing.T) {
	if os.Getenv("AZEDARACH_SQLITE_INDEPENDENT_LOCK_PROBE") == "1" {
		probeSQLiteWriteLock(t, os.Getenv("AZEDARACH_PROJECTION_DB"))
		return
	}

	for _, tt := range []struct {
		name       string
		lockDBPath func(string) string
	}{
		{name: "same database", lockDBPath: func(issuesDBPath string) string { return issuesDBPath }},
		{name: "sibling database", lockDBPath: func(issuesDBPath string) string {
			return filepath.Join(filepath.Dir(issuesDBPath), "runtime-state.db")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			issuesDBPath := filepath.Join(dir, "azedarach.db")
			client := NewClientAtPath(issuesDBPath, nil)
			require.NoError(t, client.OpenProjectionDeltaStore())

			lockDBPath := tt.lockDBPath(issuesDBPath)
			lockDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(lockDBPath)+"?_pragma=busy_timeout(0)")
			require.NoError(t, err)
			t.Cleanup(func() { _ = lockDB.Close() })
			lockDB.SetMaxOpenConns(1)
			require.NoError(t, func() error {
				_, execErr := lockDB.ExecContext(context.Background(), `CREATE TABLE lock_probe(id INTEGER PRIMARY KEY)`)
				return execErr
			}())
			lockTx, err := lockDB.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = lockTx.Rollback() })
			_, err = lockTx.ExecContext(context.Background(), `INSERT INTO lock_probe(id) VALUES(1)`)
			require.NoError(t, err)
			require.Equal(t, "busy", runIndependentSQLiteWriteLockProbe(t, lockDBPath))

			_, generation, err := client.projectionReadDBHandleWithGeneration()
			require.NoError(t, err)
			_, unsubscribe, err := client.subscribeProjectionDeltaNotifier(generation)
			require.NoError(t, err)
			unsubscribe()
			require.NoError(t, client.CloseDB())

			require.Equal(t, "busy", runIndependentSQLiteWriteLockProbe(t, lockDBPath),
				"closing the issues watcher must not cancel locks owned by an independent live SQLite pool")
			require.NoError(t, lockTx.Rollback())
			require.Equal(t, "acquired", runIndependentSQLiteWriteLockProbe(t, lockDBPath))
		})
	}
}

func runIndependentSQLiteWriteLockProbe(t *testing.T, path string) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestProjectionDeltaWatcherClosePreservesIndependentSQLitePoolLocks$")
	command.Env = append(os.Environ(), "AZEDARACH_SQLITE_INDEPENDENT_LOCK_PROBE=1", "AZEDARACH_PROJECTION_DB="+path)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "lock probe failed: %s", output)
	for _, result := range []string{"busy", "acquired"} {
		if strings.Contains(string(output), "LOCK_RESULT="+result) {
			return result
		}
	}
	t.Fatalf("lock probe returned no result marker: %s", output)
	return ""
}

func runSQLiteWriteLockProbe(t *testing.T, path string) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestProjectionDeltaWatchCancellationPreservesSQLiteProcessLocks$")
	command.Env = append(os.Environ(), "AZEDARACH_SQLITE_LOCK_PROBE=1", "AZEDARACH_PROJECTION_DB="+path)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "lock probe failed: %s", output)
	for _, result := range []string{"busy", "acquired"} {
		if strings.Contains(string(output), "LOCK_RESULT="+result) {
			return result
		}
	}
	t.Fatalf("lock probe returned no result marker: %s", output)
	return ""
}

func probeSQLiteWriteLock(t *testing.T, path string) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(0)&_txlock=immediate")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(context.Background(), nil)
	if IsSQLiteBusy(err) {
		fmt.Println("LOCK_RESULT=busy")
		return
	}
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	fmt.Println("LOCK_RESULT=acquired")
}

func TestProjectionDeltaWatchUsesBoundedDescriptorSafeNotifierUntilClientClose(t *testing.T) {
	dir := t.TempDir()
	for i := range 12 {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("consumer-%02d.state", i)), nil, 0o600))
	}
	require.Empty(t, openDescriptorsUnder(t, dir))

	client := NewClientAtPath(filepath.Join(dir, "consumer.db"), nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })
	_, generation, err := client.projectionReadDBHandleWithGeneration()
	require.NoError(t, err)
	_, _, err = client.ListProjectionDeltas(context.Background(), "portable-consumer", 0, 1)
	require.NoError(t, err)

	var sharedWatcherDescriptors int
	for cycle := range 4 {
		ctx, cancel := context.WithCancel(context.Background())
		reads := 0
		client.projectionDeltaReadHook = func() {
			reads++
			if reads == 2 {
				cancel()
			}
		}
		_, _, err := client.WatchProjectionDeltas(ctx, "portable-consumer", 0, 1)
		require.ErrorIs(t, err, domain.ErrProjectionCanceled, "cycle %d", cycle)
		require.Equal(t, 2, reads, "cycle %d must cancel after watcher registration", cycle)
		if cycle == 0 {
			sharedWatcherDescriptors = len(openDescriptorsUnder(t, dir))
		} else {
			require.Equal(t, sharedWatcherDescriptors, len(openDescriptorsUnder(t, dir)), "logical watches must reuse one bounded store watcher")
		}
	}
	client.projectionDeltaReadHook = nil

	committed, err := client.CommitProjectionDelta(context.Background(), ProjectionDeltaParams{
		ProjectID:      "portable-consumer",
		Kind:           domain.ProjectionKindIssue,
		Key:            "write-after-watch-recovery",
		Operation:      domain.ProjectionDeltaUpsert,
		IdempotencyKey: "write-after-watch-recovery",
		Payload:        json.RawMessage(`{"available":true}`),
	}, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), committed.Cursor)
	diagnostic := client.ResourceDiagnostics()
	require.Zero(t, diagnostic.ProjectionWatchesActive)
	require.Equal(t, uint64(4), diagnostic.ProjectionWatchesStarted)
	require.Equal(t, diagnostic.ProjectionWatchesStarted, diagnostic.ProjectionWatchesDone)
	require.NoError(t, client.CloseDB())
	require.Empty(t, openDescriptorsUnder(t, dir), "closing the SQLite client must release its shared watcher after the pool")
	_, _, err = client.subscribeProjectionDeltaNotifier(generation)
	require.ErrorIs(t, err, domain.ErrProjectionRetryable)
	require.Empty(t, openDescriptorsUnder(t, dir), "a closed client must not recreate watcher descriptors")
}

func openDescriptorsUnder(t *testing.T, root string) []string {
	t.Helper()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	descriptorDir, err := os.Open("/dev/fd")
	require.NoError(t, err)
	names, err := descriptorDir.Readdirnames(-1)
	require.NoError(t, err)
	require.NoError(t, descriptorDir.Close())
	var paths []string
	for _, name := range names {
		path, readErr := os.Readlink(filepath.Join("/dev/fd", name))
		if readErr != nil {
			continue
		}
		path = filepath.Clean(path)
		if path == resolvedRoot || strings.HasPrefix(path, resolvedRoot+string(filepath.Separator)) {
			paths = append(paths, path)
		}
	}
	return paths
}
