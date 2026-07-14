package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const projectReadMaterializerBatchSize = 500

// projectReadMaterializer is a disposable daemon-local read model. Its cursor
// is the upstream transitional delivery position; it is not an authority
// revision and is never written back to the project database.
type projectReadMaterializer struct {
	mu        sync.RWMutex
	updateMu  sync.Mutex
	projectID string
	authority *ProjectionDeltaAuthority
	hydrate   func(context.Context, []domain.Task) ([]domain.Task, error)
	tasks     map[string]domain.Task
	worktrees map[string]git.Worktree
	metadata  protocol.MaterializedSnapshotMetadata
	cancel    context.CancelFunc
	done      chan struct{}
}

func newProjectReadMaterializer(projectID string, authority *ProjectionDeltaAuthority, hydrate func(context.Context, []domain.Task) ([]domain.Task, error)) *projectReadMaterializer {
	return &projectReadMaterializer{projectID: strings.TrimSpace(projectID), authority: authority, hydrate: hydrate, tasks: map[string]domain.Task{}, worktrees: map[string]git.Worktree{}}
}

func (m *projectReadMaterializer) bootstrap(ctx context.Context) error {
	if m == nil {
		return errors.New("project read materializer authority unavailable")
	}
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if m.authority == nil {
		return errors.New("project read materializer authority unavailable")
	}
	probe, err := m.authority.List(ctx, protocol.DefaultProjectID, 0, 1)
	if err != nil {
		return fmt.Errorf("read projection head: %w", err)
	}
	snapshot, err := m.authority.Snapshot(ctx, protocol.DefaultProjectID, probe.HeadCursor)
	if err != nil {
		return fmt.Errorf("read projection bootstrap snapshot: %w", err)
	}
	if err := protocol.VerifyProjectionSnapshot(snapshot, issueProjectionProjector()); err != nil {
		return fmt.Errorf("verify projection bootstrap snapshot: %w", err)
	}
	canonical, err := decodeProjectionValues(snapshot.Values)
	if err != nil {
		return err
	}
	hydrated, runtimeChecksum, err := m.hydrateTasks(ctx, canonical)
	if err != nil {
		return fmt.Errorf("hydrate projection bootstrap: %w", err)
	}
	metadata := materializedMetadata(snapshot.Cursor, snapshot.HeadCursor, snapshot.Projector, snapshot.SourceVector, snapshot.SemanticChecksum, runtimeChecksum, snapshot.Health)
	m.replace(hydrated, metadata)
	return nil
}

func (m *projectReadMaterializer) bootstrapLegacyExport(ctx context.Context, exported []domain.Task) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	base := m.snapshotMetadata()
	canonical := make(map[string]domain.Task, len(exported))
	canonicalValues := make([]domain.Task, 0, len(exported))
	for _, task := range exported {
		task = domain.CanonicalIssueProjectionTask(task)
		canonical[task.ID.String()] = task
		canonicalValues = append(canonicalValues, task)
	}
	sortTasksDeterministically(canonicalValues)
	hydrated, runtimeChecksum, err := m.hydrateTasks(ctx, canonical)
	if err != nil {
		return err
	}
	sources := append([]protocol.ProjectionSourceRange(nil), base.SourceVector...)
	sources = append(sources, protocol.ProjectionSourceRange{Authority: "legacy_bootstrap_export", SourceFrom: "full-export", SourceTo: "full-export", Transitional: true})
	metadata := materializedMetadata(base.DeliveryCursor, base.DeliveryHead, issueProjectionProjector(), sources, checksumJSON(canonicalValues), runtimeChecksum, "healthy")
	m.replace(hydrated, metadata)
	return nil
}

func (m *projectReadMaterializer) run(ctx context.Context, advanced func(protocol.MaterializedSnapshotMetadata)) {
	for ctx.Err() == nil {
		cursor := m.snapshotMetadata().DeliveryCursor
		batch, err := m.authority.Watch(ctx, protocol.DefaultProjectID, cursor, projectReadMaterializerBatchSize)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, domain.ErrProjectionCanceled) {
				return
			}
			if bootstrapErr := m.bootstrap(ctx); bootstrapErr != nil {
				m.markUnhealthy(bootstrapErr)
				return
			}
			if advanced != nil {
				advanced(m.snapshotMetadata())
			}
			continue
		}
		if err := m.apply(ctx, batch); err != nil {
			var verification *protocol.ProjectionVerificationError
			if errors.As(err, &verification) && (verification.Kind == protocol.ProjectionVerificationGap || verification.Kind == protocol.ProjectionVerificationOverlap) {
				if bootstrapErr := m.bootstrap(ctx); bootstrapErr == nil {
					if advanced != nil {
						advanced(m.snapshotMetadata())
					}
					continue
				}
			}
			m.markUnhealthy(err)
			return
		}
		if advanced != nil {
			advanced(m.snapshotMetadata())
		}
	}
}

func (m *projectReadMaterializer) apply(ctx context.Context, batch protocol.ProjectionDeltaBatch) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	expected := m.snapshotMetadata().DeliveryCursor
	if err := protocol.VerifyProjectionDeltaBatch(batch, expected, issueProjectionProjector()); err != nil {
		return err
	}
	m.mu.RLock()
	canonical := make(map[string]domain.Task, len(m.tasks))
	for id, task := range m.tasks {
		canonical[id] = domain.CanonicalIssueProjectionTask(task)
	}
	m.mu.RUnlock()
	for _, delta := range batch.Deltas {
		if delta.Kind != protocol.ProjectionKind(domain.ProjectionKindIssue) {
			return &protocol.ProjectionVerificationError{Kind: protocol.ProjectionVerificationIncompatible, Message: "unknown projection kind " + string(delta.Kind)}
		}
		if delta.Operation == protocol.ProjectionDeltaDelete {
			delete(canonical, delta.Key)
			continue
		}
		var payload domain.IssueProjectionDeltaPayload
		if err := json.Unmarshal(delta.Payload, &payload); err != nil {
			return fmt.Errorf("decode issue projection %s: %w", delta.Key, err)
		}
		if payload.SchemaVersion != domain.IssueProjectionDeltaSchemaVersion || payload.Deleted || payload.Issue == nil || payload.Issue.ID.String() != delta.Key {
			return &protocol.ProjectionVerificationError{Kind: protocol.ProjectionVerificationIncompatible, Message: "invalid complete issue value for " + delta.Key}
		}
		canonical[delta.Key] = domain.CanonicalIssueProjectionTask(*payload.Issue)
	}
	hydrated, runtimeChecksum, err := m.hydrateTasks(ctx, canonical)
	if err != nil {
		return err
	}
	declared, err := m.authority.Snapshot(ctx, protocol.DefaultProjectID, batch.DeliveryToCursor)
	if err != nil {
		return fmt.Errorf("read declared materializer snapshot: %w", err)
	}
	if err := protocol.VerifyProjectionSnapshot(declared, issueProjectionProjector()); err != nil {
		return fmt.Errorf("verify declared materializer snapshot: %w", err)
	}
	sources, issueChecksum := declared.SourceVector, declared.SemanticChecksum
	previous := m.snapshotMetadata()
	for _, source := range previous.SourceVector {
		if source.Authority == "legacy_bootstrap_export" {
			sources = append(append([]protocol.ProjectionSourceRange(nil), declared.SourceVector...), source)
			values := make([]domain.Task, 0, len(canonical))
			for _, task := range canonical {
				values = append(values, task)
			}
			sortTasksDeterministically(values)
			issueChecksum = checksumJSON(values)
			break
		}
	}
	metadata := materializedMetadata(declared.Cursor, declared.HeadCursor, declared.Projector, sources, issueChecksum, runtimeChecksum, declared.Health)
	m.replace(hydrated, metadata)
	return nil
}

func decodeProjectionValues(values []protocol.ProjectionValue) (map[string]domain.Task, error) {
	out := make(map[string]domain.Task, len(values))
	for _, value := range values {
		if value.Kind != protocol.ProjectionKind(domain.ProjectionKindIssue) {
			return nil, &protocol.ProjectionVerificationError{Kind: protocol.ProjectionVerificationIncompatible, Message: "unknown snapshot projection kind " + string(value.Kind)}
		}
		var payload domain.IssueProjectionDeltaPayload
		if err := json.Unmarshal(value.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode issue projection %s: %w", value.Key, err)
		}
		if payload.SchemaVersion != domain.IssueProjectionDeltaSchemaVersion || payload.Deleted || payload.Issue == nil || payload.Issue.ID.String() != value.Key {
			return nil, &protocol.ProjectionVerificationError{Kind: protocol.ProjectionVerificationIncompatible, Message: "invalid snapshot complete issue value for " + value.Key}
		}
		out[value.Key] = domain.CanonicalIssueProjectionTask(*payload.Issue)
	}
	return out, nil
}

func (m *projectReadMaterializer) hydrateTasks(ctx context.Context, canonical map[string]domain.Task) (map[string]domain.Task, string, error) {
	tasks := make([]domain.Task, 0, len(canonical))
	for _, task := range canonical {
		tasks = append(tasks, task)
	}
	sortTasksDeterministically(tasks)
	if m.hydrate != nil {
		var err error
		tasks, err = m.hydrate(ctx, tasks)
		if err != nil {
			return nil, "", err
		}
	}
	out := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		out[task.ID.String()] = task
	}
	return out, checksumJSON(tasks), nil
}

func (m *projectReadMaterializer) replace(tasks map[string]domain.Task, metadata protocol.MaterializedSnapshotMetadata) {
	m.mu.Lock()
	metadata.RuntimeChecksum = runtimeMaterializationChecksum(tasks, m.worktrees)
	metadata.SemanticChecksum = joinedMaterializedChecksum(metadata)
	m.tasks, m.metadata = tasks, metadata
	m.mu.Unlock()
}

func (m *projectReadMaterializer) markUnhealthy(err error) {
	m.mu.Lock()
	m.metadata.Health = "unhealthy: " + err.Error()
	m.mu.Unlock()
}

func (m *projectReadMaterializer) snapshotMetadata() protocol.MaterializedSnapshotMetadata {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneMaterializedMetadata(m.metadata)
}

func (m *projectReadMaterializer) snapshot() ([]domain.Task, protocol.MaterializedSnapshotMetadata) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tasks := make([]domain.Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	sortTasksDeterministically(tasks)
	return cloneTasks(tasks), cloneMaterializedMetadata(m.metadata)
}

func (m *projectReadMaterializer) refreshRuntime(ctx context.Context) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.RLock()
	canonical := make(map[string]domain.Task, len(m.tasks))
	for id, task := range m.tasks {
		canonical[id] = domain.CanonicalIssueProjectionTask(task)
	}
	metadata := cloneMaterializedMetadata(m.metadata)
	m.mu.RUnlock()
	hydrated, runtimeChecksum, err := m.hydrateTasks(ctx, canonical)
	if err != nil {
		return err
	}
	metadata.RuntimeChecksum = runtimeChecksum
	metadata.SemanticChecksum = joinedMaterializedChecksum(metadata)
	m.replace(hydrated, metadata)
	return nil
}

func (m *projectReadMaterializer) replaceWorktrees(worktrees map[string]git.Worktree) {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.Lock()
	m.worktrees = worktrees
	m.metadata.RuntimeChecksum = runtimeMaterializationChecksum(m.tasks, worktrees)
	m.metadata.SemanticChecksum = joinedMaterializedChecksum(m.metadata)
	m.mu.Unlock()
}

func runtimeMaterializationChecksum(tasksByID map[string]domain.Task, worktrees map[string]git.Worktree) string {
	tasks := make([]domain.Task, 0, len(tasksByID))
	for _, task := range tasksByID {
		tasks = append(tasks, task)
	}
	sortTasksDeterministically(tasks)
	return checksumJSON(struct {
		Tasks     []domain.Task
		Worktrees map[string]git.Worktree
	}{tasks, worktrees})
}

func (m *projectReadMaterializer) snapshotWorktrees() map[string]git.Worktree {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]git.Worktree, len(m.worktrees))
	for issueID, worktree := range m.worktrees {
		out[issueID] = worktree
	}
	return out
}

func materializedMetadata(cursor, head uint64, projector protocol.ProjectionProjector, sources []protocol.ProjectionSourceRange, issueChecksum, runtimeChecksum, health string) protocol.MaterializedSnapshotMetadata {
	metadata := protocol.MaterializedSnapshotMetadata{DeliveryCursor: cursor, DeliveryHead: head, DeliveryCursorTransitional: true, Projector: projector, SourceVector: append([]protocol.ProjectionSourceRange(nil), sources...), IssueChecksum: issueChecksum, RuntimeChecksum: runtimeChecksum, Health: health}
	metadata.SemanticChecksum = joinedMaterializedChecksum(metadata)
	return metadata
}

func joinedMaterializedChecksum(metadata protocol.MaterializedSnapshotMetadata) string {
	metadata.SemanticChecksum = ""
	return checksumJSON(metadata)
}

func cloneMaterializedMetadata(metadata protocol.MaterializedSnapshotMetadata) protocol.MaterializedSnapshotMetadata {
	metadata.SourceVector = append([]protocol.ProjectionSourceRange(nil), metadata.SourceVector...)
	return metadata
}

func checksumJSON(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func sortTasksDeterministically(tasks []domain.Task) {
	sort.Slice(tasks, func(i, j int) bool {
		if !tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
		}
		return tasks[i].ID.String() < tasks[j].ID.String()
	})
}

func (d *Daemon) startProjectReadMaterializers(ctx context.Context) error {
	d.materializersMu.Lock()
	if d.materializers == nil {
		d.materializers = map[string]*projectReadMaterializer{}
	}
	d.materializersStarted = true
	d.materializersContext = ctx
	d.materializersMu.Unlock()
	if d.issues == nil {
		return nil
	}
	_, err := d.ensureProjectReadMaterializer(ctx, d.canonicalProjectID(protocol.DefaultProjectID), d.issues)
	return err
}

func (d *Daemon) ensureProjectReadMaterializer(ctx context.Context, projectID string, client *issues.Client) (*projectReadMaterializer, error) {
	projectID = d.canonicalProjectID(projectID)
	d.materializersInitMu.Lock()
	defer d.materializersInitMu.Unlock()
	d.materializersMu.Lock()
	if d.materializers == nil {
		d.materializers = map[string]*projectReadMaterializer{}
	}
	d.materializersMu.Unlock()
	d.materializersMu.RLock()
	materializer := d.materializers[projectID]
	runCtx := d.materializersContext
	d.materializersMu.RUnlock()
	if materializer != nil {
		return materializer, nil
	}
	if client == nil {
		client = d.issueClientForProject(projectID)
	}
	if client == nil {
		return nil, fmt.Errorf("project read materialization unavailable for %s", projectID)
	}
	materializer = newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(client), func(hydrateCtx context.Context, tasks []domain.Task) ([]domain.Task, error) {
		hydrated, err := client.HydrateRuntime(hydrateCtx, projectID, tasks)
		if err != nil {
			return nil, err
		}
		return d.enrichTasksWithSessionState(hydrateCtx, projectID, hydrated), nil
	})
	if err := materializer.bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("bootstrap project %s: %w", projectID, err)
	}
	if tasks, metadata := materializer.snapshot(); metadata.DeliveryCursor == 0 && len(tasks) == 0 {
		exported, exportErr := client.ListWithRuntimeArchiveMode(ctx, projectID, issues.ArchiveInclude)
		if exportErr != nil {
			return nil, fmt.Errorf("bootstrap legacy full export for project %s: %w", projectID, exportErr)
		}
		if len(exported) > 0 {
			if err := materializer.bootstrapLegacyExport(ctx, exported); err != nil {
				return nil, fmt.Errorf("materialize legacy full export for project %s: %w", projectID, err)
			}
		}
	}
	if err := d.refreshProjectReadWorktrees(ctx, projectID, materializer); err != nil {
		return nil, fmt.Errorf("hydrate project %s worktree materialization: %w", projectID, err)
	}
	var materializerRunCtx context.Context
	if runCtx != nil {
		materializerRunCtx, materializer.cancel = context.WithCancel(runCtx)
		materializer.done = make(chan struct{})
	}
	d.materializersMu.Lock()
	if existing := d.materializers[projectID]; existing != nil {
		d.materializersMu.Unlock()
		if materializer.cancel != nil {
			materializer.cancel()
		}
		return existing, nil
	}
	d.materializers[projectID] = materializer
	d.materializersMu.Unlock()
	if runCtx != nil {
		go func() {
			defer close(materializer.done)
			materializer.run(materializerRunCtx, func(metadata protocol.MaterializedSnapshotMetadata) {
				revision := d.nextRevision(projectID)
				body, _ := json.Marshal(struct {
					ProjectID naming.ProjectID                      `json:"project_id"`
					Source    protocol.MaterializedSnapshotMetadata `json:"source"`
				}{naming.ProjectID(projectID), metadata})
				d.hub.Publish(protocol.EventEnvelope{ProtocolVersion: protocol.CurrentVersion, ProjectID: naming.ProjectID(projectID), Revision: revision, Event: "projection.materialized", Kind: protocol.EnvelopeKindEvent, EmittedAt: time.Now().UTC(), Body: body})
			})
		}()
	}
	return materializer, nil
}

func (d *Daemon) stopProjectReadMaterializer(ctx context.Context, projectID string) {
	projectID = d.canonicalProjectID(projectID)
	d.materializersMu.Lock()
	materializer := d.materializers[projectID]
	delete(d.materializers, projectID)
	d.materializersMu.Unlock()
	if materializer == nil || materializer.cancel == nil {
		return
	}
	materializer.cancel()
	if materializer.done == nil {
		return
	}
	select {
	case <-materializer.done:
	case <-ctx.Done():
	}
}

func (d *Daemon) stopAllProjectReadMaterializers(ctx context.Context) {
	if d == nil {
		return
	}
	d.materializersMu.Lock()
	materializers := make([]*projectReadMaterializer, 0, len(d.materializers))
	for projectID, materializer := range d.materializers {
		delete(d.materializers, projectID)
		materializers = append(materializers, materializer)
	}
	d.materializersMu.Unlock()
	for _, materializer := range materializers {
		if materializer != nil && materializer.cancel != nil {
			materializer.cancel()
		}
	}
	for _, materializer := range materializers {
		if materializer == nil || materializer.done == nil {
			continue
		}
		select {
		case <-materializer.done:
		case <-ctx.Done():
			return
		}
	}
}

func (d *Daemon) projectReadSnapshot(projectID string) ([]domain.Task, protocol.MaterializedSnapshotMetadata, error) {
	projectID = d.canonicalProjectID(projectID)
	d.materializersMu.RLock()
	materializer := d.materializers[projectID]
	started := d.materializersStarted
	materializerCtx := d.materializersContext
	d.materializersMu.RUnlock()
	if materializer == nil && started {
		var err error
		if materializerCtx == nil {
			materializerCtx = context.Background()
		}
		materializer, err = d.ensureProjectReadMaterializer(materializerCtx, projectID, nil)
		if err != nil {
			return nil, protocol.MaterializedSnapshotMetadata{}, err
		}
	}
	if materializer == nil {
		// Embedded/unit daemons do not execute Run's startup boundary. Bootstrap
		// their disposable read model on first use so tests and library users keep
		// the same verified semantics. Production IPC cannot reach this path.
		client := d.issueClientForProject(projectID)
		if client == nil {
			return nil, protocol.MaterializedSnapshotMetadata{}, fmt.Errorf("project read materialization unavailable for %s", projectID)
		}
		candidate := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(client), func(hydrateCtx context.Context, tasks []domain.Task) ([]domain.Task, error) {
			hydrated, err := client.HydrateRuntime(hydrateCtx, projectID, tasks)
			if err != nil {
				return nil, err
			}
			return d.enrichTasksWithSessionState(hydrateCtx, projectID, hydrated), nil
		})
		if err := candidate.bootstrap(context.Background()); err != nil {
			return nil, protocol.MaterializedSnapshotMetadata{}, fmt.Errorf("bootstrap embedded project read materialization for %s: %w", projectID, err)
		}
		tasks, metadata := candidate.snapshot()
		if !strings.HasPrefix(metadata.Health, "healthy") {
			return nil, metadata, fmt.Errorf("project read materialization unhealthy: %s", metadata.Health)
		}
		return tasks, metadata, nil
	}
	tasks, metadata := materializer.snapshot()
	if !strings.HasPrefix(metadata.Health, "healthy") {
		return nil, metadata, fmt.Errorf("project read materialization unhealthy: %s", metadata.Health)
	}
	return tasks, metadata, nil
}

func (d *Daemon) refreshProjectReadRuntime(ctx context.Context, projectID string, issueIDs ...string) {
	projectID = d.canonicalProjectID(projectID)
	d.materializersMu.RLock()
	materializer := d.materializers[projectID]
	d.materializersMu.RUnlock()
	if materializer == nil {
		return
	}
	if err := materializer.refreshRuntime(ctx); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("refresh project read runtime materialization", "project_id", projectID, "error", err)
	}
	if err := d.refreshProjectReadWorktrees(ctx, projectID, materializer); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("refresh project read worktree materialization", "project_id", projectID, "error", err)
	}
	if err := d.syncUserProjectionMaterializedIssues(ctx, projectID, issueIDs); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("apply keyed user projection materialization", "project_id", projectID, "issue_ids", issueIDs, "error", err)
	}
}

func (d *Daemon) syncUserProjectionMaterializedIssues(ctx context.Context, projectID string, issueIDs []string) error {
	if d == nil || d.userStore == nil || d.cfg.ScopedRuntime || len(issueIDs) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		if issueID = strings.TrimSpace(issueID); issueID != "" {
			wanted[issueID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	tasks, _, err := d.projectReadSnapshot(projectID)
	if err != nil {
		return err
	}
	changes := make([]userstore.ProjectDeltaChange, 0, len(wanted))
	for i := range tasks {
		issueID := tasks[i].ID.String()
		if _, ok := wanted[issueID]; !ok {
			continue
		}
		task := tasks[i]
		changes = append(changes, userstore.ProjectDeltaChange{IssueID: issueID, Issue: &task})
		delete(wanted, issueID)
	}
	return d.userStore.ApplyProjectMaterializedIssues(ctx, d.canonicalProjectID(projectID), changes)
}

func (d *Daemon) refreshProjectReadWorktrees(ctx context.Context, projectID string, materializer *projectReadMaterializer) error {
	if materializer == nil {
		return nil
	}
	store := d.worktreeRuntimeStateStoreIfConfigured(projectID)
	if store == nil {
		materializer.replaceWorktrees(map[string]git.Worktree{})
		return nil
	}
	rows, err := store.ListWorktreeStates(ctx, projectID)
	if err != nil {
		return err
	}
	worktrees := make(map[string]git.Worktree, len(rows))
	for _, row := range rows {
		issueID := strings.TrimSpace(row.IssueID)
		if issueID != "" {
			worktrees[issueID] = git.Worktree{IssueID: issueID, Path: strings.TrimSpace(row.Path), Branch: strings.TrimSpace(row.Branch)}
		}
	}
	materializer.replaceWorktrees(worktrees)
	return nil
}

func (d *Daemon) projectReadWorktrees(projectID string) map[string]git.Worktree {
	if materializer := d.activeProjectReadMaterializer(projectID); materializer != nil {
		return materializer.snapshotWorktrees()
	}
	return map[string]git.Worktree{}
}

func (d *Daemon) activeProjectReadMaterializer(projectID string) *projectReadMaterializer {
	projectID = d.canonicalProjectID(projectID)
	d.materializersMu.RLock()
	defer d.materializersMu.RUnlock()
	return d.materializers[projectID]
}

func (d *Daemon) materializedReadsEnabled() bool {
	d.materializersMu.RLock()
	defer d.materializersMu.RUnlock()
	return d.materializersStarted
}

func materializedTaskContext(tasks []domain.Task, requested []string, includeDependencyContext, includeAncestors, includeDependents, parentChildDependentsOnly bool, archiveMode protocol.ArchiveMode) []domain.Task {
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	requestedSet := make(map[string]struct{}, len(requested))
	selected := make(map[string]struct{}, len(requested)*2)
	for _, id := range requested {
		if task, ok := byID[id]; ok && materializedArchiveMatches(task, archiveMode) {
			requestedSet[id] = struct{}{}
			selected[id] = struct{}{}
		}
	}
	if includeDependencyContext {
		for id := range requestedSet {
			task := byID[id]
			for _, dependency := range task.Dependencies {
				selected[dependency.ID.String()] = struct{}{}
			}
		}
	}
	if includeDependencyContext && includeDependents {
		for _, task := range tasks {
			if task.ParentID != nil && !task.ParentID.IsZero() {
				if _, ok := requestedSet[task.ParentID.String()]; ok {
					selected[task.ID.String()] = struct{}{}
					if parentChildDependentsOnly {
						continue
					}
				}
			}
			for _, dependency := range task.Dependencies {
				if _, ok := requestedSet[dependency.ID.String()]; !ok {
					continue
				}
				if !parentChildDependentsOnly || dependency.Type == domain.DependencyParentChild || dependency.Type == domain.DependencyCreatedIn {
					selected[task.ID.String()] = struct{}{}
				}
			}
		}
	}
	if includeAncestors {
		for id := range requestedSet {
			visited := map[string]struct{}{id: {}}
			for task := byID[id]; task.ParentID != nil && !task.ParentID.IsZero(); {
				parent := task.ParentID.String()
				if _, cycle := visited[parent]; cycle {
					break
				}
				visited[parent] = struct{}{}
				selected[parent] = struct{}{}
				next, ok := byID[parent]
				if !ok {
					break
				}
				task = next
			}
		}
	}
	out := make([]domain.Task, 0, len(selected))
	for id := range selected {
		if task, ok := byID[id]; ok && materializedArchiveMatches(task, archiveMode) {
			out = append(out, task)
		}
	}
	sortTasksDeterministically(out)
	return out
}

func materializedArchiveMatches(task domain.Task, mode protocol.ArchiveMode) bool {
	archived := task.State.IsArchived()
	switch mode {
	case protocol.ArchiveModeInclude:
		return true
	case protocol.ArchiveModeOnly:
		return archived
	default:
		return !archived
	}
}

func materializedLastCheckedAt(tasks []domain.Task) time.Time {
	last := time.Unix(0, 0).UTC()
	for _, task := range tasks {
		last = laterTime(last, laterTime(task.UpdatedAt, task.RuntimeUpdatedAt))
		if task.Session != nil {
			last = laterTime(last, task.Session.UpdatedAt)
		}
	}
	return last
}

func materializedParentChildClosure(tasks []domain.Task, rootID string) []domain.Task {
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return tasks
	}
	selected := map[string]struct{}{rootID: {}}
	for changed := true; changed; {
		changed = false
		for _, task := range tasks {
			if task.ParentID == nil || task.ParentID.IsZero() {
				continue
			}
			if _, ok := selected[task.ParentID.String()]; ok {
				if _, exists := selected[task.ID.String()]; !exists {
					selected[task.ID.String()], changed = struct{}{}, true
				}
			}
		}
	}
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	for changed := true; changed; {
		changed = false
		for id := range selected {
			for _, dependency := range byID[id].Dependencies {
				dependencyID := dependency.ID.String()
				if _, ok := selected[dependencyID]; !ok {
					selected[dependencyID], changed = struct{}{}, true
				}
			}
		}
	}
	out := make([]domain.Task, 0, len(selected))
	for _, task := range tasks {
		if _, ok := selected[task.ID.String()]; ok {
			out = append(out, task)
		}
	}
	return out
}
