package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func commandMutatesProjectProjection(command string) bool {
	switch command {
	case "task.event.append", "task.create", "task.close", protocol.CommandTaskBulkCleanup,
		"task.ownership.claim", "task.ownership.release", "task.update_status", "task.update_details",
		"task.append_notes", "task.delete", "task.archive", "task.unarchive",
		"task.dependency.add", "task.dependency.remove", protocol.CommandTaskBulkApply,
		"session.start", "session.attach", "session.pause", "session.resume", "session.stop",
		daemonhandlers.CommandSessionMessage, protocol.CommandSessionResolveConflict,
		protocol.CommandSessionRestartAll, "session.recover", protocol.CommandRuntimeReconcile,
		protocol.CommandRuntimeReconcileIssue, commandSyncRun,
		protocol.CommandInteractionCreate, protocol.CommandInteractionDiscuss,
		protocol.CommandInteractionPropose, protocol.CommandInteractionAnswer,
		protocol.CommandInteractionResolve, protocol.CommandInteractionWithdraw,
		protocol.CommandInteractionSupersede, protocol.CommandInteractionRecover,
		protocol.CommandOrchestrationIntent, protocol.CommandOrchestratorSessionStart,
		protocol.CommandOrchestratorSessionAttach,
		daemonhandlers.CommandWorktreeCreate, daemonhandlers.CommandWorktreeRemove,
		daemonhandlers.CommandWorktreeCleanupOrphaned,
		daemonhandlers.CommandGitFetch, daemonhandlers.CommandGitPullBase,
		daemonhandlers.CommandGitPush, daemonhandlers.CommandGitMerge,
		daemonhandlers.CommandGitCheckout, daemonhandlers.CommandGitAbortMerge,
		daemonhandlers.CommandGitDiscardChanges, daemonhandlers.CommandGitCheckpoint:
		return true
	default:
		return false
	}
}

func (d *Daemon) enqueueUserProjectionRefresh(projectID string) {
	if d == nil || d.userStore == nil || d.cfg.ScopedRuntime {
		return
	}
	projectID = d.canonicalProjectID(projectID)
	d.userStoreRefreshMu.Lock()
	if d.userStoreRefreshPending == nil {
		d.userStoreRefreshPending = map[string]bool{}
	}
	if d.userStoreRefreshDirty == nil {
		d.userStoreRefreshDirty = map[string]bool{}
	}
	if d.userStoreRefreshActive == nil {
		d.userStoreRefreshActive = map[string]bool{}
	}
	if d.userStoreRefreshStopping {
		d.userStoreRefreshMu.Unlock()
		return
	}
	if d.userStoreRefreshPending[projectID] {
		d.userStoreRefreshDirty[projectID] = true
		d.userStoreRefreshMu.Unlock()
		return
	}
	d.userStoreRefreshPending[projectID] = true
	d.userStoreRefreshDirty[projectID] = true
	d.userStoreRefreshWG.Add(1)
	workerCtx := d.userStoreRefreshCtx
	if workerCtx == nil {
		d.userStoreRefreshCtx, d.userStoreRefreshCancel = context.WithCancel(context.Background())
		workerCtx = d.userStoreRefreshCtx
	}
	interval := d.userStoreRefreshInterval
	if interval <= 0 {
		interval = userProjectionMutationRefreshInterval
	}
	refreshProject := d.userStoreRefreshProjectFn
	if refreshProject == nil {
		refreshProject = d.refreshUserProject
	}
	d.userStoreRefreshMu.Unlock()
	go func() {
		defer d.userStoreRefreshWG.Done()
		for {
			d.userStoreRefreshMu.Lock()
			d.userStoreRefreshDirty[projectID] = false
			d.userStoreRefreshActive[projectID] = true
			d.userStoreRefreshMu.Unlock()
			ctx, cancel := context.WithTimeout(workerCtx, 30*time.Second)
			err := refreshProject(ctx, projectID)
			cancel()
			if err != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("refresh user cross-project projection after mutation", "project_id", projectID, "error", err)
			}
			d.userStoreRefreshMu.Lock()
			d.userStoreRefreshActive[projectID] = false
			stopping := d.userStoreRefreshStopping
			d.userStoreRefreshMu.Unlock()
			if stopping {
				d.finishUserProjectionRefreshWorker(projectID)
				return
			}

			// Retain the project's single worker throughout the cooldown. This
			// prevents a mutation arriving just after a clean refresh from creating
			// a fresh worker that bypasses the per-project rate bound.
			timer := time.NewTimer(interval)
			select {
			case <-workerCtx.Done():
				timer.Stop()
				d.finishUserProjectionRefreshWorker(projectID)
				return
			case <-timer.C:
			}
			d.userStoreRefreshMu.Lock()
			dirty := d.userStoreRefreshDirty[projectID]
			stopping = d.userStoreRefreshStopping
			if !dirty || stopping {
				delete(d.userStoreRefreshPending, projectID)
				delete(d.userStoreRefreshDirty, projectID)
				delete(d.userStoreRefreshActive, projectID)
			}
			d.userStoreRefreshMu.Unlock()
			if !dirty || stopping {
				return
			}
		}
	}()
}

func (d *Daemon) finishUserProjectionRefreshWorker(projectID string) {
	d.userStoreRefreshMu.Lock()
	delete(d.userStoreRefreshPending, projectID)
	delete(d.userStoreRefreshDirty, projectID)
	delete(d.userStoreRefreshActive, projectID)
	d.userStoreRefreshMu.Unlock()
}

const (
	userProjectionMutationRefreshInterval = 10 * time.Second
	globalProjectionRepairInterval        = 2 * time.Minute
)

func (d *Daemon) startGlobalProjectionRepairWorker(ctx context.Context) {
	if d == nil || d.userStore == nil || d.cfg.ScopedRuntime {
		return
	}
	d.userStoreRefreshMu.Lock()
	if d.userStoreRefreshStopping {
		d.userStoreRefreshMu.Unlock()
		return
	}
	if d.userStoreRefreshCancel == nil {
		d.userStoreRefreshCtx, d.userStoreRefreshCancel = context.WithCancel(ctx)
		ctx = d.userStoreRefreshCtx
	}
	d.userStoreRefreshWG.Add(1)
	d.userStoreRefreshMu.Unlock()
	go func() {
		defer d.userStoreRefreshWG.Done()
		ticker := time.NewTicker(globalProjectionRepairInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				repairCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				err := d.scheduleUserProjectionRepair(repairCtx)
				cancel()
				if err != nil && d.cfg.Logger != nil {
					d.cfg.Logger.Warn("periodic user projection repair completed partially", "error", err)
				}
			}
		}
	}()
}

func (d *Daemon) scheduleUserProjectionRepair(ctx context.Context) error {
	if d == nil || d.userStore == nil {
		return nil
	}
	registry, err := appconfig.LoadProjectsRegistry()
	if err != nil {
		return fmt.Errorf("load project registry: %w", err)
	}
	projects := make([]userstore.CatalogProject, 0, len(registry.Projects))
	ids := make([]string, 0, len(registry.Projects))
	for _, project := range registry.Projects {
		projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
		if projectID == "" {
			continue
		}
		root, resolveErr := appconfig.ResolveProjectRoot(project.Path)
		if resolveErr != nil {
			root = filepath.Clean(project.Path)
		}
		projects = append(projects, userstore.CatalogProject{ProjectID: projectID, Name: project.Name, Path: root, DBPath: filepath.Join(root, ".azedarach", "azedarach.db")})
		ids = append(ids, projectID)
	}
	if err := d.userStore.ReconcileProjects(ctx, projects); err != nil {
		return fmt.Errorf("reconcile user projection catalog: %w", err)
	}
	for _, projectID := range ids {
		d.enqueueUserProjectionRefresh(projectID)
	}
	return nil
}

func (d *Daemon) stopUserProjectionWorkers() {
	if d == nil {
		return
	}
	d.userStoreRefreshMu.Lock()
	d.userStoreRefreshStopping = true
	if d.userStoreRefreshCancel != nil {
		d.userStoreRefreshCancel()
	}
	d.userStoreRefreshMu.Unlock()
	d.userStoreRefreshWG.Wait()
}

func (d *Daemon) refreshUserProject(ctx context.Context, wantedProjectID string) error {
	if d == nil || d.userStore == nil {
		return nil
	}
	registry, err := appconfig.LoadProjectsRegistry()
	if err != nil {
		return err
	}
	for _, project := range registry.Projects {
		id := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
		if id == "" || id != wantedProjectID {
			continue
		}
		return d.refreshRegisteredUserProject(ctx, project)
	}
	return fmt.Errorf("registered project %s not found", wantedProjectID)
}

func (d *Daemon) refreshUserProjection(ctx context.Context) error {
	if d == nil || d.userStore == nil {
		return nil
	}
	registry, err := appconfig.LoadProjectsRegistry()
	if err != nil {
		return fmt.Errorf("load project registry: %w", err)
	}
	ids := make([]string, 0, len(registry.Projects))
	var firstErr error
	for _, project := range registry.Projects {
		projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
		if projectID == "" {
			continue
		}
		ids = append(ids, projectID)
		if refreshErr := d.refreshRegisteredUserProject(ctx, project); refreshErr != nil && firstErr == nil {
			firstErr = refreshErr
		}
	}
	if err := d.userStore.ReconcileCatalog(ctx, ids); err != nil {
		return err
	}
	return firstErr
}

func (d *Daemon) refreshRegisteredUserProject(ctx context.Context, project appconfig.Project) error {
	projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
	if projectID == "" {
		return nil
	}
	lock, _ := d.userStoreProjectRefreshLocks.LoadOrStore(projectID, &sync.Mutex{})
	projectLock := lock.(*sync.Mutex)
	projectLock.Lock()
	defer projectLock.Unlock()
	if d.userStoreProjectLockHook != nil {
		d.userStoreProjectLockHook(projectID, true)
		defer d.userStoreProjectLockHook(projectID, false)
	}

	root, resolveErr := appconfig.ResolveProjectRoot(project.Path)
	if resolveErr != nil {
		root = filepath.Clean(project.Path)
	}
	dbPath := filepath.Join(root, ".azedarach", "azedarach.db")
	generation, err := d.userStore.BeginProjectRefresh(ctx, userstore.CatalogProject{ProjectID: projectID, Name: project.Name, Path: root, DBPath: dbPath})
	if err != nil {
		return err
	}
	if resolveErr != nil {
		_ = d.userStore.MarkUnavailableGeneration(ctx, projectID, project.Name, root, dbPath, generation, resolveErr)
		return resolveErr
	}
	if err = d.exportProjectToUserProjection(ctx, projectID, project.Name, root, dbPath, generation); err != nil {
		_ = d.userStore.MarkUnavailableGeneration(ctx, projectID, project.Name, root, dbPath, generation, err)
	}
	return err
}

func (d *Daemon) exportProjectToUserProjection(ctx context.Context, projectID, name, root, dbPath string, generation uint64) error {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return fmt.Errorf("issue store unavailable")
	}
	export, err := client.ExportProjection(ctx, projectID)
	if err != nil {
		return err
	}
	return d.userStore.ReplaceProject(ctx, userstore.ProjectInput{ProjectID: projectID, Name: name, Path: root, DBPath: dbPath, SchemaVersion: export.SchemaVersion, SchemaFingerprint: export.SchemaFingerprint, Checkpoint: export.Checkpoint, RefreshGeneration: generation, Tasks: export.Tasks})
}

func (d *Daemon) handleGlobalSnapshot(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if d.userStore == nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "user cross-project projection unavailable"), nil
	}
	var body protocol.GlobalSnapshotRequestBody
	if err := decodeOptionalJSON(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if body.Consumer != "" && !body.Consumer.Valid() {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid global view consumer %q", body.Consumer)), nil
	}
	if err := body.Scope.Validate(); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	record, err := d.userStore.ResolveGlobalView(ctx, body.ViewID, string(body.Consumer))
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("resolve global view: %v", err)), nil
	}
	view := record.View
	scope := record.Scope
	if body.Scope.Kind != "" {
		scope = body.Scope
	}
	snapshot, err := d.userStore.SnapshotForScopedViewWithTasks(ctx, strings.TrimSpace(body.Query), &view, scope, body.HydrateTaskIDs)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	d.overlayScheduledUserProjectionFreshness(&snapshot)
	projection, err := projectGlobalView(view, filterGlobalProjects(snapshot.Projects, scope))
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	snapshot.Projection = projection
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = raw
	return resp, nil
}

func (d *Daemon) overlayScheduledUserProjectionFreshness(snapshot *protocol.GlobalSnapshotResponseBody) {
	if d == nil || snapshot == nil {
		return
	}
	d.userStoreRefreshMu.Lock()
	stale := make(map[string]struct{}, len(d.userStoreRefreshDirty)+len(d.userStoreRefreshActive))
	for projectID, dirty := range d.userStoreRefreshDirty {
		if dirty {
			stale[projectID] = struct{}{}
		}
	}
	for projectID, active := range d.userStoreRefreshActive {
		if active {
			stale[projectID] = struct{}{}
		}
	}
	d.userStoreRefreshMu.Unlock()
	for i := range snapshot.Projects {
		if _, ok := stale[protocol.NormalizeProjectID(snapshot.Projects[i].ProjectID)]; !ok {
			continue
		}
		if snapshot.Projects[i].Freshness == protocol.GlobalProjectionFreshnessFresh {
			snapshot.Projects[i].Freshness = protocol.GlobalProjectionFreshnessStale
		}
		snapshot.Partial = true
	}
}

func filterGlobalProjects(projects []protocol.GlobalProjectSnapshot, scope protocol.GlobalViewScope) []protocol.GlobalProjectSnapshot {
	if scope.Kind == "" || scope.Kind == protocol.GlobalViewScopeAllProjects {
		return projects
	}
	wanted := make(map[string]struct{}, len(scope.ProjectIDs)+1)
	if scope.Kind == protocol.GlobalViewScopeCurrentProject {
		wanted[protocol.NormalizeProjectID(scope.CurrentProjectID.String())] = struct{}{}
	} else {
		for _, id := range scope.ProjectIDs {
			wanted[protocol.NormalizeProjectID(id.String())] = struct{}{}
		}
	}
	out := make([]protocol.GlobalProjectSnapshot, 0, len(projects))
	for _, project := range projects {
		if _, ok := wanted[protocol.NormalizeProjectID(project.ProjectID)]; ok {
			out = append(out, project)
		}
	}
	return out
}

func (d *Daemon) validateGlobalViewScopeProjects(ctx context.Context, scope protocol.GlobalViewScope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if scope.Kind == protocol.GlobalViewScopeAllProjects || scope.Kind == "" {
		return nil
	}
	snapshot, err := d.userStore.Snapshot(ctx, "")
	if err != nil {
		return fmt.Errorf("load project catalog: %w", err)
	}
	known := make(map[string]struct{}, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		if !project.Registered {
			continue
		}
		known[protocol.NormalizeProjectID(project.ProjectID)] = struct{}{}
	}
	ids := scope.ProjectIDs
	if scope.Kind == protocol.GlobalViewScopeCurrentProject {
		ids = []naming.ProjectID{scope.CurrentProjectID}
	}
	for _, id := range ids {
		if _, ok := known[protocol.NormalizeProjectID(id.String())]; !ok {
			return fmt.Errorf("global view scope references unknown project %q", id)
		}
	}
	return nil
}

func (d *Daemon) handleGlobalProjectionRebuild(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if d.userStore == nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "user cross-project projection unavailable"), nil
	}
	rebuildErr := d.refreshUserProjection(ctx)
	snapshot, err := d.userStore.Snapshot(ctx, "")
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	if rebuildErr != nil {
		snapshot.Partial = true
	}
	d.overlayScheduledUserProjectionFreshness(&snapshot)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = raw
	return resp, nil
}

func projectGlobalView(view domain.BoardView, projects []protocol.GlobalProjectSnapshot) (protocol.GlobalViewProjection, error) {
	out := protocol.GlobalViewProjection{View: view}
	groups := make(map[domain.BoardColumnID][]protocol.GlobalViewProjectedItem, len(view.Columns))
	groupOrder := make([]domain.BoardColumnID, 0, len(view.Columns))
	for _, c := range view.Columns {
		groupOrder = append(groupOrder, c.ID)
	}
	for _, project := range projects {
		p, err := domain.ProjectTasksByBoardView(view, append([]domain.Task(nil), project.Tasks...))
		if err != nil {
			return out, err
		}
		for _, id := range p.KnownTaskIDs {
			out.KnownTaskIDs = append(out.KnownTaskIDs, protocol.ScopedIssueID{ProjectID: naming.ProjectID(project.ProjectID), IssueID: id})
		}
		for _, item := range p.Items {
			groups[item.GroupID] = append(groups[item.GroupID], protocol.GlobalViewProjectedItem{Identity: protocol.ScopedIssueID{ProjectID: naming.ProjectID(project.ProjectID), IssueID: item.Task.ID}, Task: item.Task, GroupID: item.GroupID, Depth: item.Depth, OrchestrationState: item.OrchestrationState})
		}
	}
	rules := view.Sort
	if len(rules) == 0 && view.Options.SortPolicy == domain.BoardViewSortHumanAttention {
		rules = []domain.BoardViewSortRule{{Key: domain.BoardViewSortKeyHumanAttention, Direction: domain.BoardViewSortDescending}}
	}
	for _, groupID := range groupOrder {
		items := groups[groupID]
		sort.SliceStable(items, func(i, j int) bool {
			for _, rule := range rules {
				cmp := domain.CompareBoardViewTasks(rule.Key, items[i].Task, items[j].Task)
				if cmp == 0 {
					continue
				}
				if rule.Direction == domain.BoardViewSortDescending {
					return cmp > 0
				}
				return cmp < 0
			}
			if items[i].Identity.ProjectID != items[j].Identity.ProjectID {
				return items[i].Identity.ProjectID < items[j].Identity.ProjectID
			}
			return items[i].Identity.IssueID < items[j].Identity.IssueID
		})
		g := protocol.GlobalViewProjectedGroup{GroupID: groupID}
		for _, item := range items {
			g.TaskIDs = append(g.TaskIDs, item.Identity)
			out.Items = append(out.Items, item)
		}
		if !view.Options.HideEmptyColumns || len(g.TaskIDs) > 0 {
			out.Groups = append(out.Groups, g)
		}
	}
	return out, nil
}
