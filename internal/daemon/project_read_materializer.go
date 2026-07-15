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
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const projectReadMaterializerBatchSize = 500

// projectReadMaterializer is a disposable daemon-local read model. Its cursor
// is the upstream transitional delivery position; it is not an authority
// revision and is never written back to the project database.
type projectReadMaterializer struct {
	mu          sync.RWMutex
	updateMu    sync.Mutex
	projectID   string
	authority   *ProjectionDeltaAuthority
	hydrate     func(context.Context, []domain.Task) ([]domain.Task, error)
	affected    func(context.Context, protocol.ProjectionDeltaBatch) ([]string, error)
	legacy      func(context.Context) ([]domain.Task, error)
	canonical   map[string]domain.Task
	tasks       map[string]domain.Task
	worktrees   map[string]git.Worktree
	metadata    protocol.MaterializedSnapshotMetadata
	issueKeys   keyedCheckpoint
	runtimeKeys keyedCheckpoint
	cancel      context.CancelFunc
	done        chan struct{}
}

type keyedCheckpoint struct {
	digest [sha256.Size]byte
	count  int
}

func (c *keyedCheckpoint) add(namespace, key string, value any) {
	entry := checksumEntry(namespace, key, value)
	for i := range c.digest {
		c.digest[i] ^= entry[i]
	}
	c.count++
}

func (c *keyedCheckpoint) remove(namespace, key string, value any) {
	entry := checksumEntry(namespace, key, value)
	for i := range c.digest {
		c.digest[i] ^= entry[i]
	}
	c.count--
}

func (c keyedCheckpoint) sum() string {
	raw, _ := json.Marshal(struct {
		Digest string `json:"digest"`
		Count  int    `json:"count"`
	}{hex.EncodeToString(c.digest[:]), c.count})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func checksumEntry(namespace, key string, value any) [sha256.Size]byte {
	raw, _ := json.Marshal(struct {
		Namespace string `json:"namespace"`
		Key       string `json:"key"`
		Value     any    `json:"value"`
	}{namespace, key, value})
	return sha256.Sum256(raw)
}

func newProjectReadMaterializer(projectID string, authority *ProjectionDeltaAuthority, hydrate func(context.Context, []domain.Task) ([]domain.Task, error)) *projectReadMaterializer {
	return &projectReadMaterializer{projectID: strings.TrimSpace(projectID), authority: authority, hydrate: hydrate, canonical: map[string]domain.Task{}, tasks: map[string]domain.Task{}, worktrees: map[string]git.Worktree{}}
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
	sources := append([]protocol.ProjectionSourceRange(nil), snapshot.SourceVector...)
	if m.legacy != nil {
		exported, exportErr := m.legacy(ctx)
		if exportErr != nil {
			return fmt.Errorf("read durable legacy bootstrap overlay: %w", exportErr)
		}
		// The immutable 0047 delivery bridge has no genesis backfill, so its
		// snapshot cannot declare the complete current key set for historical
		// databases. The durable full export is the exact bootstrap value set;
		// the verified snapshot contributes only cursor/source checkpointing.
		canonical = make(map[string]domain.Task, len(exported))
		for _, task := range exported {
			task = domain.CanonicalIssueProjectionTask(task)
			canonical[task.ID.String()] = task
		}
		sources = append(sources, protocol.ProjectionSourceRange{Authority: "legacy_bootstrap_export", SourceFrom: "full-export", SourceTo: "full-export", Transitional: true})
	}
	hydrated, _, err := m.hydrateTasks(ctx, canonical)
	if err != nil {
		return fmt.Errorf("hydrate projection bootstrap: %w", err)
	}
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, hydrated)
	metadata := materializedMetadata(snapshot.Cursor, snapshot.HeadCursor, snapshot.Projector, sources, issueKeys.sum(), runtimeKeys.sum(), snapshot.Health)
	m.replaceBootstrap(canonical, hydrated, metadata, issueKeys, runtimeKeys)
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
			var gap *domain.ProjectionGapError
			if errors.As(err, &gap) {
				if bootstrapErr := m.bootstrap(ctx); bootstrapErr == nil {
					if advanced != nil {
						advanced(m.snapshotMetadata())
					}
					continue
				}
			}
			m.markUnhealthy(fmt.Errorf("watch projection deltas: %w", err))
			if advanced != nil {
				advanced(m.snapshotMetadata())
			}
			if !waitProjectionConsumerRetry(ctx) {
				return
			}
			continue
		}
		if err := m.apply(ctx, batch); err != nil {
			var verification *protocol.ProjectionVerificationError
			if errors.As(err, &verification) && projectionVerificationRequiresRecovery(verification.Kind) {
				if bootstrapErr := m.bootstrap(ctx); bootstrapErr == nil {
					if advanced != nil {
						advanced(m.snapshotMetadata())
					}
					continue
				}
			}
			m.markUnhealthy(err)
			if advanced != nil {
				advanced(m.snapshotMetadata())
			}
			if !waitProjectionConsumerRetry(ctx) {
				return
			}
			continue
		}
		if advanced != nil {
			advanced(m.snapshotMetadata())
		}
	}
}

func projectionVerificationRequiresRecovery(kind protocol.ProjectionVerificationErrorKind) bool {
	switch kind {
	case protocol.ProjectionVerificationGap, protocol.ProjectionVerificationOverlap, protocol.ProjectionVerificationIncompatible:
		return true
	default:
		return false
	}
}

func (m *projectReadMaterializer) apply(ctx context.Context, batch protocol.ProjectionDeltaBatch) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	expected := m.snapshotMetadata().DeliveryCursor
	if err := protocol.VerifyProjectionDeltaBatch(batch, expected, issueProjectionProjector()); err != nil {
		return err
	}
	affected := make(map[string]struct{}, len(batch.Deltas)+len(batch.EmptyAdvances))
	if m.affected != nil {
		ids, err := m.affected(ctx, batch)
		if err != nil {
			return fmt.Errorf("resolve affected projection keys: %w", err)
		}
		for _, issueID := range ids {
			if issueID = strings.TrimSpace(issueID); issueID != "" {
				affected[issueID] = struct{}{}
			}
		}
	}
	canonical := make(map[string]domain.Task, len(affected)+len(batch.Deltas))
	deleted := make(map[string]struct{}, len(batch.Deltas))
	for _, delta := range batch.Deltas {
		if delta.Kind != protocol.ProjectionKind(domain.ProjectionKindIssue) {
			return &protocol.ProjectionVerificationError{Kind: protocol.ProjectionVerificationIncompatible, Message: "unknown projection kind " + string(delta.Kind)}
		}
		if delta.Operation == protocol.ProjectionDeltaDelete {
			deleted[delta.Key] = struct{}{}
			delete(affected, delta.Key)
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
		affected[delta.Key] = struct{}{}
	}
	m.mu.RLock()
	for issueID := range affected {
		if _, exists := canonical[issueID]; exists {
			continue
		}
		if current, exists := m.canonical[issueID]; exists {
			canonical[issueID] = current
		}
	}
	m.mu.RUnlock()
	hydrated, _, err := m.hydrateTasks(ctx, canonical)
	if err != nil {
		return err
	}
	m.mu.Lock()
	issueKeys, runtimeKeys := m.issueKeys, m.runtimeKeys
	for issueID := range deleted {
		if current, exists := m.canonical[issueID]; exists {
			issueKeys.remove("issue", issueID, current)
			delete(m.canonical, issueID)
		}
		if current, exists := m.tasks[issueID]; exists {
			runtimeKeys.remove("task-runtime", issueID, current)
			delete(m.tasks, issueID)
		}
	}
	for issueID, task := range hydrated {
		if current, exists := m.canonical[issueID]; exists {
			issueKeys.remove("issue", issueID, current)
		}
		if current, exists := m.tasks[issueID]; exists {
			runtimeKeys.remove("task-runtime", issueID, current)
		}
		issueKeys.add("issue", issueID, canonical[issueID])
		runtimeKeys.add("task-runtime", issueID, task)
		m.canonical[issueID] = canonical[issueID]
		m.tasks[issueID] = task
	}
	m.issueKeys, m.runtimeKeys = issueKeys, runtimeKeys
	sources := mergeRootProjectionSources(m.metadata.SourceVector, batch.SourceVector)
	m.metadata = materializedMetadata(batch.DeliveryToCursor, batch.HeadCursor, batch.Projector, sources, issueKeys.sum(), runtimeKeys.sum(), batch.Health)
	m.mu.Unlock()
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
		issueID := task.ID.String()
		if _, expected := canonical[issueID]; !expected {
			return nil, "", fmt.Errorf("runtime hydration returned unexpected issue %s", issueID)
		}
		out[issueID] = task
	}
	if len(out) != len(canonical) {
		return nil, "", fmt.Errorf("runtime hydration returned %d issues, want %d", len(out), len(canonical))
	}
	return out, checksumJSON(tasks), nil
}

func checkpointMaterializedTasks(canonical, hydrated map[string]domain.Task) (keyedCheckpoint, keyedCheckpoint) {
	var issueKeys, runtimeKeys keyedCheckpoint
	for issueID, task := range canonical {
		issueKeys.add("issue", issueID, task)
	}
	for issueID, task := range hydrated {
		runtimeKeys.add("task-runtime", issueID, task)
	}
	return issueKeys, runtimeKeys
}

func (m *projectReadMaterializer) replaceBootstrap(canonical, tasks map[string]domain.Task, metadata protocol.MaterializedSnapshotMetadata, issueKeys, runtimeKeys keyedCheckpoint) {
	m.mu.Lock()
	for issueID, worktree := range m.worktrees {
		runtimeKeys.add("worktree", issueID, worktree)
	}
	metadata.IssueChecksum = issueKeys.sum()
	metadata.RuntimeChecksum = runtimeKeys.sum()
	metadata.SemanticChecksum = joinedMaterializedChecksum(metadata)
	m.canonical, m.tasks, m.metadata, m.issueKeys, m.runtimeKeys = canonical, tasks, metadata, issueKeys, runtimeKeys
	m.mu.Unlock()
}

func (m *projectReadMaterializer) markUnhealthy(err error) {
	m.mu.Lock()
	m.metadata.Health = "stale: " + err.Error()
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

func (m *projectReadMaterializer) snapshotIssues(issueIDs map[string]struct{}) ([]domain.Task, protocol.MaterializedSnapshotMetadata) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tasks := make([]domain.Task, 0, len(issueIDs))
	for issueID := range issueIDs {
		if task, exists := m.tasks[issueID]; exists {
			tasks = append(tasks, task)
		}
	}
	sortTasksDeterministically(tasks)
	return cloneTasks(tasks), cloneMaterializedMetadata(m.metadata)
}

func (m *projectReadMaterializer) refreshRuntime(ctx context.Context, issueIDs []string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	wanted := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		if issueID = strings.TrimSpace(issueID); issueID != "" {
			wanted[issueID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	m.mu.RLock()
	canonical := make(map[string]domain.Task, len(wanted))
	for issueID := range wanted {
		if task, exists := m.canonical[issueID]; exists {
			canonical[issueID] = task
		}
	}
	m.mu.RUnlock()
	if len(canonical) == 0 {
		return nil
	}
	hydrated, _, err := m.hydrateTasks(ctx, canonical)
	if err != nil {
		return err
	}
	m.mu.Lock()
	runtimeKeys := m.runtimeKeys
	for issueID, task := range hydrated {
		if current, exists := m.tasks[issueID]; exists {
			runtimeKeys.remove("task-runtime", issueID, current)
		}
		runtimeKeys.add("task-runtime", issueID, task)
		m.tasks[issueID] = task
	}
	m.runtimeKeys = runtimeKeys
	m.metadata.RuntimeChecksum = runtimeKeys.sum()
	m.metadata.SemanticChecksum = joinedMaterializedChecksum(m.metadata)
	m.mu.Unlock()
	return nil
}

func (m *projectReadMaterializer) replaceWorktrees(worktrees map[string]git.Worktree) {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.Lock()
	runtimeKeys := m.runtimeKeys
	for issueID, worktree := range m.worktrees {
		runtimeKeys.remove("worktree", issueID, worktree)
	}
	for issueID, worktree := range worktrees {
		runtimeKeys.add("worktree", issueID, worktree)
	}
	m.worktrees = worktrees
	m.runtimeKeys = runtimeKeys
	m.metadata.RuntimeChecksum = runtimeKeys.sum()
	m.metadata.SemanticChecksum = joinedMaterializedChecksum(m.metadata)
	m.mu.Unlock()
}

func (m *projectReadMaterializer) replaceWorktreesForIssues(worktrees map[string]*git.Worktree) {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.Lock()
	runtimeKeys := m.runtimeKeys
	for issueID, worktree := range worktrees {
		if current, exists := m.worktrees[issueID]; exists {
			runtimeKeys.remove("worktree", issueID, current)
			delete(m.worktrees, issueID)
		}
		if worktree != nil {
			runtimeKeys.add("worktree", issueID, *worktree)
			m.worktrees[issueID] = *worktree
		}
	}
	m.runtimeKeys = runtimeKeys
	m.metadata.RuntimeChecksum = runtimeKeys.sum()
	m.metadata.SemanticChecksum = joinedMaterializedChecksum(m.metadata)
	m.mu.Unlock()
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
		return d.hydrateProjectReadTasks(hydrateCtx, projectID, client, tasks), nil
	})
	d.configureProjectReadMaterializer(materializer, projectID, client)
	if err := materializer.bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("bootstrap project %s: %w", projectID, err)
	}
	if err := d.refreshProjectReadWorktreesForBootstrap(ctx, projectID, materializer); err != nil {
		_, endFallback := latencytrace.StartSpanWithEndAttributes(ctx, "daemon", "project_read.degraded_fallback", "project_id", projectID, "reason", "worktree_enrichment")
		endFallback(nil, "outcome", "ticket_only")
		if d.cfg.Logger != nil {
			d.cfg.Logger.WarnContext(ctx, "project read materializer continuing without runtime worktree enrichment", "project_id", projectID, "error", err)
		}
		materializer.replaceWorktrees(map[string]git.Worktree{})
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
			return d.hydrateProjectReadTasks(hydrateCtx, projectID, client, tasks), nil
		})
		d.configureProjectReadMaterializer(candidate, projectID, client)
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

func (d *Daemon) hydrateProjectReadTasks(ctx context.Context, projectID string, client *issues.Client, tasks []domain.Task) []domain.Task {
	hydrate := client.HydrateRuntime
	if d.projectReadRuntimeHydrate != nil {
		hydrate = func(ctx context.Context, projectID string, tasks []domain.Task) ([]domain.Task, error) {
			return d.projectReadRuntimeHydrate(ctx, projectID, tasks)
		}
	}
	hydrateCtx, endHydration := latencytrace.StartSpanWithEndAttributes(ctx, "daemon", "project_read.runtime_hydration", "project_id", projectID, "task_count", len(tasks))
	hydrated, err := hydrate(hydrateCtx, projectID, tasks)
	if err != nil {
		endHydration(err, "outcome", "degraded")
		_, endFallback := latencytrace.StartSpanWithEndAttributes(ctx, "daemon", "project_read.degraded_fallback", "project_id", projectID, "reason", "session_hydration")
		endFallback(nil, "outcome", "ticket_only")
		if d.cfg.Logger != nil {
			d.cfg.Logger.WarnContext(ctx, "project read materializer continuing without runtime session enrichment", "project_id", projectID, "error", err)
		}
		return tasks
	}
	endHydration(nil, "outcome", "enriched")
	return d.enrichTasksWithSessionState(ctx, projectID, hydrated)
}

func (d *Daemon) refreshProjectReadWorktreesForBootstrap(ctx context.Context, projectID string, materializer *projectReadMaterializer) error {
	if d.projectReadWorktreeRefresh != nil {
		return d.projectReadWorktreeRefresh(ctx, projectID, materializer)
	}
	return d.refreshProjectReadWorktrees(ctx, projectID, materializer)
}

func (d *Daemon) configureProjectReadMaterializer(materializer *projectReadMaterializer, projectID string, client *issues.Client) {
	if materializer == nil || client == nil {
		return
	}
	materializer.legacy = func(ctx context.Context) ([]domain.Task, error) {
		return client.ListWithRuntimeArchiveMode(ctx, projectID, issues.ArchiveInclude)
	}
	materializer.affected = func(ctx context.Context, batch protocol.ProjectionDeltaBatch) ([]string, error) {
		return projectionBatchAffectedIssueIDs(ctx, client, batch)
	}
}

func (d *Daemon) refreshProjectReadRuntime(ctx context.Context, projectID string, issueIDs ...string) {
	projectID = d.canonicalProjectID(projectID)
	d.materializersMu.RLock()
	materializer := d.materializers[projectID]
	d.materializersMu.RUnlock()
	if materializer == nil {
		return
	}
	issueIDs = uniqueStrings(issueIDs)
	if err := materializer.refreshRuntime(ctx, issueIDs); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("refresh project read runtime materialization", "project_id", projectID, "error", err)
	}
	if err := d.refreshProjectReadWorktreesForIssues(ctx, projectID, materializer, issueIDs); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("refresh project read worktree materialization", "project_id", projectID, "error", err)
	}
	if err := d.syncUserProjectionMaterializedIssues(ctx, projectID, issueIDs); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("apply keyed user projection materialization", "project_id", projectID, "issue_ids", issueIDs, "error", err)
	}
}

func (d *Daemon) refreshProjectReadWorktreesForIssues(ctx context.Context, projectID string, materializer *projectReadMaterializer, issueIDs []string) error {
	if materializer == nil || len(issueIDs) == 0 {
		return nil
	}
	store := d.worktreeRuntimeStateStoreIfConfigured(projectID)
	worktrees := make(map[string]*git.Worktree, len(issueIDs))
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		if store == nil {
			worktrees[issueID] = nil
			continue
		}
		row, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, issueID)
		if err != nil {
			return err
		}
		if !found {
			worktrees[issueID] = nil
			continue
		}
		worktree := git.Worktree{IssueID: issueID, Path: strings.TrimSpace(row.Path), Branch: strings.TrimSpace(row.Branch)}
		worktrees[issueID] = &worktree
	}
	materializer.replaceWorktreesForIssues(worktrees)
	return nil
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
	materializer := d.activeProjectReadMaterializer(projectID)
	if materializer == nil {
		return nil
	}
	tasks, metadata := materializer.snapshotIssues(wanted)
	if !strings.HasPrefix(metadata.Health, "healthy") {
		return fmt.Errorf("project read materialization unhealthy: %s", metadata.Health)
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
