package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestProjectionDeltaWatchCancellationReleasesDirectoryDescriptors(t *testing.T) {
	dir := t.TempDir()
	for i := range 12 {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("consumer-%02d.state", i)), nil, 0o600))
	}

	client := NewClientAtPath(filepath.Join(dir, "consumer.db"), nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })
	_, _, err := client.ListProjectionDeltas(context.Background(), "portable-consumer", 0, 1)
	require.NoError(t, err)

	baseline := openFileDescriptorCount(t)
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
	}
	client.projectionDeltaReadHook = nil

	require.Equal(t, baseline, openFileDescriptorCount(t), "completed watches must not retain descriptors for files beside a consumer database")
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
}

func openFileDescriptorCount(t *testing.T) int {
	t.Helper()
	dir, err := os.Open("/dev/fd")
	require.NoError(t, err)
	names, err := dir.Readdirnames(-1)
	require.NoError(t, err)
	require.NoError(t, dir.Close())
	return len(names)
}
