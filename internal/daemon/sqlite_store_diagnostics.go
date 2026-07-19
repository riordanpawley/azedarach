package daemon

import (
	"database/sql"
	"sort"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

func (d *Daemon) sqliteStoreDiagnostics() []protocol.TaskSQLiteStoreInfo {
	stores := append(d.issueStoreDiagnostics(), d.runtimeStoreDiagnostics()...)
	sort.Slice(stores, func(i, j int) bool {
		if stores[i].DBPath != stores[j].DBPath {
			return stores[i].DBPath < stores[j].DBPath
		}
		return stores[i].Owner < stores[j].Owner
	})
	return stores
}

func (d *Daemon) issueStoreDiagnostics() []protocol.TaskSQLiteStoreInfo {
	d.issueClientsMu.Lock()
	projects := make(map[*issues.Client][]string, len(d.issueClientsByProject))
	for projectID, client := range d.issueClientsByProject {
		if client != nil {
			projects[client] = append(projects[client], projectID)
		}
	}
	for _, client := range d.issueClientsByRoot {
		if client != nil {
			if _, exists := projects[client]; !exists {
				projects[client] = nil
			}
		}
	}
	d.issueClientsMu.Unlock()

	out := make([]protocol.TaskSQLiteStoreInfo, 0, len(projects))
	for client, projectIDs := range projects {
		sort.Strings(projectIDs)
		diagnostic := client.ResourceDiagnostics()
		info := sqlitePoolInfo("issues", projectIDs, diagnostic.DBPath, diagnostic.Open, diagnostic.DBStats)
		info.MutationHolder = diagnostic.MutationHolder
		info.MutationHeldMillisecond = diagnostic.MutationHeldFor.Milliseconds()
		info.SQLiteWriteHolder = diagnostic.SQLiteWriteHolder
		info.SQLiteWriteHeldMillisecond = diagnostic.SQLiteWriteHeldFor.Milliseconds()
		info.ProjectionWatchesActive = diagnostic.ProjectionWatchesActive
		info.ProjectionWatchesStarted = diagnostic.ProjectionWatchesStarted
		info.ProjectionWatchesDone = diagnostic.ProjectionWatchesDone
		out = append(out, info)
	}
	return out
}

func (d *Daemon) runtimeStoreDiagnostics() []protocol.TaskSQLiteStoreInfo {
	d.runtimeStoresMu.Lock()
	projects := make(map[*state.RuntimeStateStore][]string, len(d.runtimeStoresByProject))
	for projectID, store := range d.runtimeStoresByProject {
		if store != nil {
			projects[store] = append(projects[store], projectID)
		}
	}
	for _, store := range d.runtimeStoresByRoot {
		if store != nil {
			if _, exists := projects[store]; !exists {
				projects[store] = nil
			}
		}
	}
	d.runtimeStoresMu.Unlock()

	out := make([]protocol.TaskSQLiteStoreInfo, 0, len(projects)*2)
	for store, projectIDs := range projects {
		sort.Strings(projectIDs)
		dbPath, open, stats := store.StoreResourceDiagnostics()
		info := sqlitePoolInfo("runtime", projectIDs, dbPath, open, stats)
		writeLock := sqliteutil.WriteLockResourceDiagnostics(dbPath)
		info.SQLiteWriteHolder = writeLock.Holder
		info.SQLiteWriteHeldMillisecond = writeLock.HeldFor.Milliseconds()
		out = append(out, info)
		identityPath, identityOpen, identityStats := store.ManagedIdentityResourceDiagnostics()
		out = append(out, sqlitePoolInfo("runtime-managed-identity", projectIDs, identityPath, identityOpen, identityStats))
	}
	return out
}

func sqlitePoolInfo(owner string, projectIDs []string, dbPath string, open bool, stats sql.DBStats) protocol.TaskSQLiteStoreInfo {
	return protocol.TaskSQLiteStoreInfo{
		ProjectIDs:              projectIDs,
		Owner:                   owner,
		DBPath:                  dbPath,
		Open:                    open,
		MaxOpenConnections:      stats.MaxOpenConnections,
		OpenConnections:         stats.OpenConnections,
		InUse:                   stats.InUse,
		Idle:                    stats.Idle,
		WaitCount:               stats.WaitCount,
		WaitDurationMillisecond: stats.WaitDuration.Milliseconds(),
	}
}
