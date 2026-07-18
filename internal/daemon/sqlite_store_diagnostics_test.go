package daemon

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStoreDiagnosticsEnumeratesUniqueRegisteredOwners(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first-consumer.db")
	secondPath := filepath.Join(dir, "second-consumer.db")
	firstIssues := issues.NewClientAtPath(firstPath, slog.Default())
	secondIssues := issues.NewClientAtPath(secondPath, slog.Default())
	require.NoError(t, firstIssues.OpenProjectionDeltaStore())
	require.NoError(t, secondIssues.OpenProjectionDeltaStore())
	t.Cleanup(func() {
		require.NoError(t, firstIssues.CloseDB())
		require.NoError(t, secondIssues.CloseDB())
	})
	firstRuntime := state.NewRuntimeStateStoreAtPath(firstPath, slog.Default())
	secondRuntime := state.NewRuntimeStateStoreAtPath(secondPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, firstRuntime.Close())
		require.NoError(t, secondRuntime.Close())
	})

	d := &Daemon{
		issueClientsByProject:  map[string]*issues.Client{"consumer-a": firstIssues, "consumer-a-alias": firstIssues, "consumer-b": secondIssues},
		issueClientsByRoot:     map[string]*issues.Client{"/consumer/a": firstIssues, "/consumer/b": secondIssues},
		runtimeStoresByProject: map[string]*state.RuntimeStateStore{"consumer-a": firstRuntime, "consumer-a-alias": firstRuntime, "consumer-b": secondRuntime},
		runtimeStoresByRoot:    map[string]*state.RuntimeStateStore{"/consumer/a": firstRuntime, "/consumer/b": secondRuntime},
	}

	got := d.sqliteStoreDiagnostics()
	require.Len(t, got, 4)
	require.Equal(t, []string{"consumer-a", "consumer-a-alias"}, got[0].ProjectIDs)
	require.Equal(t, "issues", got[0].Owner)
	require.Equal(t, "runtime", got[1].Owner)
	require.Equal(t, []string{"consumer-b"}, got[2].ProjectIDs)
	require.Equal(t, "issues", got[2].Owner)
	require.Equal(t, "runtime", got[3].Owner)
	for _, diagnostic := range got {
		require.Equal(t, 0, diagnostic.InUse)
		require.GreaterOrEqual(t, diagnostic.OpenConnections, diagnostic.Idle)
	}
}
