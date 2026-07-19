package daemon

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
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
	userProjection, err := userstore.Open(filepath.Join(dir, "user.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, firstRuntime.Close())
		require.NoError(t, secondRuntime.Close())
		require.NoError(t, userProjection.Close())
	})

	d := &Daemon{
		issueClientsByProject:  map[string]*issues.Client{"consumer-a": firstIssues, "consumer-a-alias": firstIssues, "consumer-b": secondIssues},
		issueClientsByRoot:     map[string]*issues.Client{"/consumer/a": firstIssues, "/consumer/b": secondIssues},
		runtimeStoresByProject: map[string]*state.RuntimeStateStore{"consumer-a": firstRuntime, "consumer-a-alias": firstRuntime, "consumer-b": secondRuntime},
		runtimeStoresByRoot:    map[string]*state.RuntimeStateStore{"/consumer/a": firstRuntime, "/consumer/b": secondRuntime},
		userStore:              userProjection,
	}

	got := d.sqliteStoreDiagnostics()
	require.Len(t, got, 5)
	byKey := make(map[string]protocol.TaskSQLiteStoreInfo, len(got))
	for _, diagnostic := range got {
		byKey[diagnostic.DBPath+"\x00"+diagnostic.Owner] = diagnostic
	}
	require.Equal(t, []string{"consumer-a", "consumer-a-alias"}, byKey[firstPath+"\x00issues"].ProjectIDs)
	require.Equal(t, []string{"consumer-a", "consumer-a-alias"}, byKey[firstPath+"\x00runtime"].ProjectIDs)
	require.Equal(t, []string{"consumer-b"}, byKey[secondPath+"\x00issues"].ProjectIDs)
	require.Equal(t, []string{"consumer-b"}, byKey[secondPath+"\x00runtime"].ProjectIDs)
	userProjectionFound := false
	for _, diagnostic := range got {
		userProjectionFound = userProjectionFound || diagnostic.Owner == "user_projection" && filepath.Base(diagnostic.DBPath) == "user.db"
	}
	require.True(t, userProjectionFound)
	for _, diagnostic := range got {
		require.Equal(t, 0, diagnostic.InUse)
		require.GreaterOrEqual(t, diagnostic.OpenConnections, diagnostic.Idle)
	}
}
