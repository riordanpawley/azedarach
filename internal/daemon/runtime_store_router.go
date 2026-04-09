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
	projectID = d.canonicalProjectID(projectID)

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
	return store
}

func (d *Daemon) migrateLegacyRuntimeState(ctx context.Context) error {
	source := d.runtimeStateStoreForProject(d.canonicalProjectID(protocol.DefaultProjectID))
	if source == nil {
		return fmt.Errorf("runtime state store unavailable")
	}
	projectIDs, err := source.ListProjectIDs(ctx)
	if err != nil {
		return fmt.Errorf("list runtime state project ids: %w", err)
	}
	slices.Sort(projectIDs)
	for _, listedProjectID := range projectIDs {
		rawProjectID := protocol.NormalizeProjectID(listedProjectID)
		canonicalProjectID := d.canonicalProjectID(rawProjectID)
		if canonicalProjectID == "" {
			continue
		}
		target := d.runtimeStateStoreForProject(canonicalProjectID)
		if target == nil || target == source {
			continue
		}

		existingSessions, err := target.ListSessionStates(ctx, canonicalProjectID)
		if err != nil {
			return fmt.Errorf("check migrated session state %s: %w", canonicalProjectID, err)
		}
		existingWorktrees, err := target.ListWorktreeStates(ctx, canonicalProjectID)
		if err != nil {
			return fmt.Errorf("check migrated worktree state %s: %w", canonicalProjectID, err)
		}
		if len(existingSessions) > 0 || len(existingWorktrees) > 0 {
			continue
		}

		sourceSessions, err := source.ListSessionStates(ctx, rawProjectID)
		if err != nil {
			return fmt.Errorf("load legacy session state %s: %w", rawProjectID, err)
		}
		sourceWorktrees, err := source.ListWorktreeStates(ctx, rawProjectID)
		if err != nil {
			return fmt.Errorf("load legacy worktree state %s: %w", rawProjectID, err)
		}
		if len(sourceSessions) == 0 && len(sourceWorktrees) == 0 {
			continue
		}
		if err := target.ReplaceSessionStates(ctx, canonicalProjectID, sourceSessions); err != nil {
			return fmt.Errorf("migrate session state %s: %w", canonicalProjectID, err)
		}
		if err := target.ReplaceWorktreeStates(ctx, canonicalProjectID, sourceWorktrees); err != nil {
			return fmt.Errorf("migrate worktree state %s: %w", canonicalProjectID, err)
		}
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info(
				"migrated legacy runtime state to routed project store",
				"project_id", canonicalProjectID,
				"legacy_project_id", rawProjectID,
				"session_rows", len(sourceSessions),
				"worktree_rows", len(sourceWorktrees),
			)
		}
	}
	return nil
}
