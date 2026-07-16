package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	globalProjectionRepairInterval = 2 * time.Minute
	selectorSnapshotRefreshTimeout = 5 * time.Second
)

func selectorSnapshotRequestKey(body protocol.GlobalSnapshotRequestBody) (string, error) {
	body.Query = strings.TrimSpace(body.Query)
	body.ViewID = strings.TrimSpace(body.ViewID)
	body.Scope.CurrentProjectID = naming.ProjectID(strings.TrimSpace(body.Scope.CurrentProjectID.String()))
	body.Scope.ProjectIDs = append([]naming.ProjectID(nil), body.Scope.ProjectIDs...)
	for index := range body.Scope.ProjectIDs {
		body.Scope.ProjectIDs[index] = naming.ProjectID(strings.TrimSpace(body.Scope.ProjectIDs[index].String()))
	}
	sort.Slice(body.Scope.ProjectIDs, func(i, j int) bool { return body.Scope.ProjectIDs[i] < body.Scope.ProjectIDs[j] })
	body.Scope.ProjectIDs = dedupeProjectIDs(body.Scope.ProjectIDs)
	body.HydrateTaskIDs = append([]protocol.ScopedIssueID(nil), body.HydrateTaskIDs...)
	for index := range body.HydrateTaskIDs {
		body.HydrateTaskIDs[index].ProjectID = naming.ProjectID(protocol.NormalizeProjectID(body.HydrateTaskIDs[index].ProjectID.String()))
		body.HydrateTaskIDs[index].IssueID = naming.IssueID(strings.TrimSpace(body.HydrateTaskIDs[index].IssueID.String()))
	}
	sort.Slice(body.HydrateTaskIDs, func(i, j int) bool {
		if body.HydrateTaskIDs[i].ProjectID != body.HydrateTaskIDs[j].ProjectID {
			return body.HydrateTaskIDs[i].ProjectID < body.HydrateTaskIDs[j].ProjectID
		}
		return body.HydrateTaskIDs[i].IssueID < body.HydrateTaskIDs[j].IssueID
	})
	body.HydrateTaskIDs = dedupeScopedIssueIDs(body.HydrateTaskIDs)
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode tmux selector snapshot cache key: %w", err)
	}
	return string(raw), nil
}

func dedupeProjectIDs(ids []naming.ProjectID) []naming.ProjectID {
	out := ids[:0]
	for _, id := range ids {
		if id == "" || len(out) > 0 && out[len(out)-1] == id {
			continue
		}
		out = append(out, id)
	}
	return out
}

func dedupeScopedIssueIDs(ids []protocol.ScopedIssueID) []protocol.ScopedIssueID {
	out := ids[:0]
	for _, id := range ids {
		if id.ProjectID == "" || id.IssueID == "" || len(out) > 0 && out[len(out)-1] == id {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (d *Daemon) scheduleSelectorSnapshotRefresh(ctx context.Context, key string, body protocol.GlobalSnapshotRequestBody) {
	d.userStoreRefreshMu.Lock()
	if d.userStoreRefreshStopping {
		d.userStoreRefreshMu.Unlock()
		return
	}
	refresh, ok := d.selectorSnapshots.beginRefresh(key)
	if !ok {
		d.userStoreRefreshMu.Unlock()
		return
	}
	baseCtx := d.userStoreRefreshCtx
	if baseCtx == nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	d.userStoreRefreshWG.Add(1)
	d.userStoreRefreshMu.Unlock()
	refreshCtx, cancel := context.WithTimeout(baseCtx, selectorSnapshotRefreshTimeout)

	go func() {
		defer d.userStoreRefreshWG.Done()
		defer cancel()
		body, err := d.buildGlobalSnapshot(refreshCtx, body)
		d.selectorSnapshots.finishLoad(refresh, body, err)
		if err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Debug("tmux selector snapshot cache refresh failed", "error", err)
		}
	}()
}

func (d *Daemon) loadSelectorSnapshot(ctx context.Context, body protocol.GlobalSnapshotRequestBody) ([]byte, error) {
	d.userStoreRefreshMu.Lock()
	if d.userStoreRefreshStopping {
		d.userStoreRefreshMu.Unlock()
		return nil, context.Canceled
	}
	baseCtx := d.userStoreRefreshCtx
	if baseCtx == nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	d.userStoreRefreshWG.Add(1)
	d.userStoreRefreshMu.Unlock()

	loadCtx, cancel := context.WithTimeout(baseCtx, selectorSnapshotRefreshTimeout)
	defer d.userStoreRefreshWG.Done()
	defer cancel()
	return d.buildGlobalSnapshot(loadCtx, body)
}

func (d *Daemon) startUserProjectionWorkContext(ctx context.Context) {
	d.userStoreRefreshMu.Lock()
	defer d.userStoreRefreshMu.Unlock()
	if d.userStoreRefreshStopping || d.userStoreRefreshCancel != nil {
		return
	}
	d.userStoreRefreshCtx, d.userStoreRefreshCancel = context.WithCancel(ctx)
}

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
	if registry, err := appconfig.LoadProjectsRegistry(); err == nil {
		d.ensureUserProjectionConsumers(ctx, registry.Projects)
	} else if d.cfg.Logger != nil {
		d.cfg.Logger.Warn("start user projection consumers", "error", err)
	}
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
				registry, err := appconfig.LoadProjectsRegistry()
				if err == nil {
					d.ensureUserProjectionConsumers(repairCtx, registry.Projects)
					err = d.reconcileUserProjectionProjects(repairCtx, registry.Projects)
				} else {
					err = fmt.Errorf("load project registry: %w", err)
				}
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
	return d.reconcileUserProjectionProjects(ctx, registry.Projects)
}

func (d *Daemon) reconcileUserProjectionProjects(ctx context.Context, registered []appconfig.Project) error {
	projects := make([]userstore.CatalogProject, 0, len(registered))
	for _, project := range registered {
		projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
		if projectID == "" {
			continue
		}
		root, resolveErr := appconfig.ResolveProjectRoot(project.Path)
		if resolveErr != nil {
			root = filepath.Clean(project.Path)
		}
		projects = append(projects, userstore.CatalogProject{ProjectID: projectID, Name: project.Name, Path: root, DBPath: filepath.Join(root, ".azedarach", "azedarach.db")})
	}
	if err := d.userStore.ReconcileProjects(ctx, projects); err != nil {
		return fmt.Errorf("reconcile user projection catalog: %w", err)
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
	d.stopAllProjectReadMaterializers(context.Background())
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

// bootstrapUserProjection initializes projects that have never published a
// verified delta component. Existing components are resumed by the blocking
// consumers from their durable per-project cursors instead of being replaced
// by a routine startup export.
func (d *Daemon) bootstrapUserProjection(ctx context.Context) error {
	if d == nil || d.userStore == nil {
		return nil
	}
	registry, err := appconfig.LoadProjectsRegistry()
	if err != nil {
		return fmt.Errorf("load project registry: %w", err)
	}
	projects := make([]userstore.CatalogProject, 0, len(registry.Projects))
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
	}
	if err := d.userStore.ReconcileProjects(ctx, projects); err != nil {
		return fmt.Errorf("reconcile user projection bootstrap catalog: %w", err)
	}
	var firstErr error
	for _, project := range registry.Projects {
		projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
		if projectID == "" {
			continue
		}
		state, stateErr := d.userStore.ProjectDeltaState(ctx, projectID)
		if stateErr == nil && state.Initialized && state.Projector == issueProjectionProjector() {
			continue
		}
		if refreshErr := d.refreshRegisteredUserProject(ctx, project); refreshErr != nil && firstErr == nil {
			firstErr = refreshErr
		}
	}
	return firstErr
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
	for attempt := 0; attempt < 4; attempt++ {
		before, err := d.projectDeltaHead(ctx, projectID, client)
		if err != nil {
			return fmt.Errorf("read project delta head before export: %w", err)
		}
		export, err := client.ExportProjection(ctx, projectID)
		if err != nil {
			return err
		}
		deltaState, err := d.projectDeltaSnapshotState(ctx, projectID, client)
		if err != nil {
			return fmt.Errorf("read project delta bootstrap snapshot: %w", err)
		}
		if before != deltaState.Cursor {
			continue
		}
		export.Tasks = d.enrichTasksWithSessionState(ctx, projectID, export.Tasks)
		return d.userStore.ReplaceProject(ctx, userstore.ProjectInput{ProjectID: projectID, Name: name, Path: root, DBPath: dbPath, SchemaVersion: export.SchemaVersion, SchemaFingerprint: export.SchemaFingerprint, Checkpoint: export.Checkpoint, RefreshGeneration: generation, Tasks: export.Tasks, Delta: &deltaState})
	}
	return fmt.Errorf("project authority changed throughout verified export: %w", domain.ErrProjectionRetryable)
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
	if body.Consumer == protocol.GlobalViewConsumerTmuxSelector {
		key, err := selectorSnapshotRequestKey(body)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
		}
		if cached, ok := d.selectorSnapshots.get(key); ok {
			d.scheduleSelectorSnapshotRefresh(ctx, key, body)
			resp := d.successResponse(req)
			resp.Body = cached
			return resp, nil
		}
		raw, err := d.selectorSnapshots.loadCold(ctx, key, func() ([]byte, error) {
			return d.loadSelectorSnapshot(ctx, body)
		})
		if err != nil {
			return d.globalSnapshotErrorResponse(req, err), nil
		}
		resp := d.successResponse(req)
		resp.Body = raw
		return resp, nil
	}
	raw, err := d.buildGlobalSnapshot(ctx, body)
	if err != nil {
		return d.globalSnapshotErrorResponse(req, err), nil
	}
	resp := d.successResponse(req)
	resp.Body = raw
	return resp, nil
}

type globalSnapshotRequestError struct{ err error }

func (e globalSnapshotRequestError) Error() string { return e.err.Error() }
func (e globalSnapshotRequestError) Unwrap() error { return e.err }

func (d *Daemon) buildGlobalSnapshot(ctx context.Context, body protocol.GlobalSnapshotRequestBody) ([]byte, error) {
	record, err := d.userStore.ResolveGlobalView(ctx, body.ViewID, string(body.Consumer))
	if err != nil {
		return nil, globalSnapshotRequestError{err: fmt.Errorf("resolve global view: %w", err)}
	}
	view := record.View
	scope := record.Scope
	if body.Scope.Kind != "" {
		scope = body.Scope
	}
	snapshot, err := d.userStore.SnapshotForScopedViewWithTasks(ctx, strings.TrimSpace(body.Query), &view, scope, body.HydrateTaskIDs)
	if err != nil {
		return nil, err
	}
	projection, err := projectGlobalView(view, filterGlobalProjects(snapshot.Projects, scope))
	if err != nil {
		return nil, err
	}
	projection.KnownTaskIDs = augmentGlobalProjectionKnownTasks(projection.KnownTaskIDs, snapshot.Projects)
	snapshot.Projection = projection
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (d *Daemon) globalSnapshotErrorResponse(req protocol.RequestEnvelope, err error) protocol.ResponseEnvelope {
	var requestErr globalSnapshotRequestError
	if errors.As(err, &requestErr) {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, requestErr.Error())
	}
	return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error())
}

func augmentGlobalProjectionKnownTasks(known []protocol.ScopedIssueID, projects []protocol.GlobalProjectSnapshot) []protocol.ScopedIssueID {
	seen := make(map[protocol.ScopedIssueID]struct{}, len(known))
	for _, identity := range known {
		identity.ProjectID = naming.ProjectID(protocol.NormalizeProjectID(identity.ProjectID.String()))
		seen[identity] = struct{}{}
	}
	for _, project := range projects {
		projectID := protocol.NormalizeProjectID(project.ProjectID)
		for _, task := range project.Tasks {
			if projectID == "" || task.ID == "" {
				continue
			}
			identity := protocol.ScopedIssueID{ProjectID: naming.ProjectID(projectID), IssueID: task.ID}
			if _, ok := seen[identity]; ok {
				continue
			}
			seen[identity] = struct{}{}
			known = append(known, identity)
		}
	}
	return known
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
	allItems := make([]protocol.GlobalViewProjectedItem, 0)
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
			projected := protocol.GlobalViewProjectedItem{Identity: protocol.ScopedIssueID{ProjectID: naming.ProjectID(project.ProjectID), IssueID: item.Task.ID}, Task: item.Task, GroupID: item.GroupID, Depth: item.Depth, OrchestrationState: item.OrchestrationState}
			groups[item.GroupID] = append(groups[item.GroupID], projected)
			allItems = append(allItems, projected)
		}
	}
	rules := view.Sort
	if len(rules) == 0 && view.Options.SortPolicy == domain.BoardViewSortHumanAttention {
		rules = []domain.BoardViewSortRule{{Key: domain.BoardViewSortKeyHumanAttention, Direction: domain.BoardViewSortDescending}}
	}
	if view.Layout == domain.BoardViewLayoutTreeList {
		out.Items = sortGlobalTreeBranches(allItems, rules)
		groups = make(map[domain.BoardColumnID][]protocol.GlobalViewProjectedItem, len(view.Columns))
		for _, item := range out.Items {
			groups[item.GroupID] = append(groups[item.GroupID], item)
		}
	}
	for _, groupID := range groupOrder {
		items := groups[groupID]
		if view.Layout != domain.BoardViewLayoutTreeList {
			sort.SliceStable(items, func(i, j int) bool {
				return globalProjectedItemLess(items[i], items[j], rules)
			})
		}
		g := protocol.GlobalViewProjectedGroup{GroupID: groupID}
		for _, item := range items {
			g.TaskIDs = append(g.TaskIDs, item.Identity)
			if view.Layout != domain.BoardViewLayoutTreeList {
				out.Items = append(out.Items, item)
			}
		}
		if !view.Options.HideEmptyColumns || len(g.TaskIDs) > 0 {
			out.Groups = append(out.Groups, g)
		}
	}
	return out, nil
}

func sortGlobalTreeBranches(items []protocol.GlobalViewProjectedItem, rules []domain.BoardViewSortRule) []protocol.GlobalViewProjectedItem {
	if len(items) < 2 {
		return items
	}
	branches := make([][]protocol.GlobalViewProjectedItem, 0, len(items))
	for _, item := range items {
		if item.Depth <= 0 || len(branches) == 0 {
			branches = append(branches, nil)
		}
		branches[len(branches)-1] = append(branches[len(branches)-1], item)
	}
	sort.SliceStable(branches, func(i, j int) bool {
		return globalProjectedItemLess(branches[i][0], branches[j][0], rules)
	})
	ordered := make([]protocol.GlobalViewProjectedItem, 0, len(items))
	for _, branch := range branches {
		ordered = append(ordered, branch...)
	}
	return ordered
}

func globalProjectedItemLess(left, right protocol.GlobalViewProjectedItem, rules []domain.BoardViewSortRule) bool {
	for _, rule := range rules {
		cmp := domain.CompareBoardViewTasks(rule.Key, left.Task, right.Task)
		if cmp == 0 {
			continue
		}
		if rule.Direction == domain.BoardViewSortDescending {
			return cmp > 0
		}
		return cmp < 0
	}
	if left.Identity.ProjectID != right.Identity.ProjectID {
		return left.Identity.ProjectID < right.Identity.ProjectID
	}
	return left.Identity.IssueID < right.Identity.IssueID
}
