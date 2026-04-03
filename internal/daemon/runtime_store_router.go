package daemon

import (
	"context"
	"fmt"
	"slices"
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

func (d *Daemon) migrateLegacyRuntimeState(ctx context.Context) error {
	source := d.runtimeStateStoreForProject(protocol.DefaultProjectID)
	if source == nil {
		return fmt.Errorf("runtime state store unavailable")
	}
	projectIDs, err := source.ListProjectIDs(ctx)
	if err != nil {
		return fmt.Errorf("list runtime state project ids: %w", err)
	}
	slices.Sort(projectIDs)
	for _, projectID := range projectIDs {
		projectID = protocol.NormalizeProjectID(projectID)
		if projectID == "" {
			continue
		}
		target := d.runtimeStateStoreForProject(projectID)
		if target == nil || target == source {
			continue
		}

		existingSessions, err := target.ListSessionStates(ctx, projectID)
		if err != nil {
			return fmt.Errorf("check migrated session state %s: %w", projectID, err)
		}
		existingWorktrees, err := target.ListWorktreeStates(ctx, projectID)
		if err != nil {
			return fmt.Errorf("check migrated worktree state %s: %w", projectID, err)
		}
		if len(existingSessions) > 0 || len(existingWorktrees) > 0 {
			continue
		}

		sourceSessions, err := source.ListSessionStates(ctx, projectID)
		if err != nil {
			return fmt.Errorf("load legacy session state %s: %w", projectID, err)
		}
		sourceWorktrees, err := source.ListWorktreeStates(ctx, projectID)
		if err != nil {
			return fmt.Errorf("load legacy worktree state %s: %w", projectID, err)
		}
		if len(sourceSessions) == 0 && len(sourceWorktrees) == 0 {
			continue
		}
		if err := target.ReplaceSessionStates(ctx, projectID, sourceSessions); err != nil {
			return fmt.Errorf("migrate session state %s: %w", projectID, err)
		}
		if err := target.ReplaceWorktreeStates(ctx, projectID, sourceWorktrees); err != nil {
			return fmt.Errorf("migrate worktree state %s: %w", projectID, err)
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info(
				"migrated legacy runtime state to routed project store",
				"project_id", projectID,
				"session_rows", len(sourceSessions),
				"worktree_rows", len(sourceWorktrees),
			)
		}
	}
	return nil
}
