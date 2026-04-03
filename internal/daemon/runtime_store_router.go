package daemon

import (
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
)

func (d *Daemon) runtimeStateStoreForProject(projectID string) *daemonstate.RuntimeStateStore {
	if d == nil {
		return nil
	}
	projectID = protocol.NormalizeProjectID(projectID)

	d.runtimeStoresMu.Lock()
	defer d.runtimeStoresMu.Unlock()

	if d.runtimeStoresByProject == nil {
		d.runtimeStoresByProject = make(map[string]*daemonstate.RuntimeStateStore)
	}
	if d.runtimeStoresByRoot == nil {
		d.runtimeStoresByRoot = make(map[string]*daemonstate.RuntimeStateStore)
	}
	if store, ok := d.runtimeStoresByProject[projectID]; ok && store != nil {
		return store
	}
	if len(d.runtimeStoresByRoot) == 0 {
		legacy := d.sessionRuntimeStore
		if legacy == nil {
			legacy = d.worktreeRuntimeStore
		}
		if legacy != nil {
			d.runtimeStoresByProject[projectID] = legacy
			return legacy
		}
	}

	repoDir := d.resolveRepoDirForProjectLocked(projectID)
	if strings.TrimSpace(repoDir) == "" {
		return nil
	}
	if store, ok := d.runtimeStoresByRoot[repoDir]; ok && store != nil {
		d.runtimeStoresByProject[projectID] = store
		return store
	}

	store := daemonstate.NewRuntimeStateStore(repoDir, d.cfg.Logger)
	d.runtimeStoresByRoot[repoDir] = store
	d.runtimeStoresByProject[projectID] = store
	if d.sessionRuntimeStore == nil {
		d.sessionRuntimeStore = store
	}
	if d.worktreeRuntimeStore == nil {
		d.worktreeRuntimeStore = store
	}
	return store
}
