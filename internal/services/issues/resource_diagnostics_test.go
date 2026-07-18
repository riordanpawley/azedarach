package issues

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRepeatedConsumerReadsKeepPoolBoundedAndWritesAvailable(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "consumer.db"), nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })
	ctx := context.Background()

	for range 20 {
		_, err := client.List(ctx)
		require.NoError(t, err)
		_, err = client.ExportProjection(ctx, "portable-consumer")
		require.NoError(t, err)
		_, err = client.ExportOrchestrationProjection(ctx, "portable-consumer", 10)
		require.NoError(t, err)
		_, head, err := client.ListProjectionDeltas(ctx, "portable-consumer", 0, 10)
		require.NoError(t, err)
		_, err = client.ProjectionSnapshotAt(ctx, "portable-consumer", head)
		require.NoError(t, err)
	}

	diagnostic := client.ResourceDiagnostics()
	require.Equal(t, sqliteMaxOpenConns, diagnostic.DBStats.MaxOpenConnections)
	require.LessOrEqual(t, diagnostic.DBStats.OpenConnections, sqliteMaxOpenConns)
	require.Zero(t, diagnostic.DBStats.InUse)
	created, err := client.Create(ctx, CreateTaskParams{Title: "write after repeated consumer reads", Type: domain.TypeTask, Priority: domain.P3})
	require.NoError(t, err)
	require.NotEmpty(t, created)
}

func TestResourceDiagnosticsAttributesActiveMutationHolder(t *testing.T) {
	client := NewClientAtPath(filepath.Join(t.TempDir(), "consumer.db"), nil)
	require.NoError(t, client.OpenProjectionDeltaStore())
	t.Cleanup(func() { require.NoError(t, client.CloseDB()) })

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	ctx := ContextWithMutationOperation(context.Background(), "test.blocked_consumer_write")
	go func() {
		done <- client.withMutationLock(ctx, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	diagnostic := client.ResourceDiagnostics()
	require.True(t, diagnostic.Open)
	require.Equal(t, "test.blocked_consumer_write", diagnostic.MutationHolder)
	require.Equal(t, "test.blocked_consumer_write", diagnostic.SQLiteWriteHolder)
	close(release)
	require.NoError(t, <-done)
	require.Empty(t, client.ResourceDiagnostics().MutationHolder)
	require.Empty(t, client.ResourceDiagnostics().SQLiteWriteHolder)
}
