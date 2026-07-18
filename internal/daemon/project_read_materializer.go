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

const projectReadMutationConvergenceTimeout = 5 * time.Second

type projectReadUnavailableError struct {
	cause error
}

func (e *projectReadUnavailableError) Error() string {
	if e == nil || e.cause == nil {
		return "project read materialization unavailable"
	}
	return e.cause.Error()
}

func (e *projectReadUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newProjectReadUnavailableError(format string, args ...any) error {
	return &projectReadUnavailableError{cause: fmt.Errorf(format, args...)}
}

func isProjectReadUnavailableError(err error) bool {
	var unavailable *projectReadUnavailableError
	return errors.As(err, &unavailable)
}

type suppressSynchronousProjectReadRuntimeRefreshKey struct{}

func withProjectReadUpdateWaitHookForTest(ctx context.Context, hook func(string, string)) context.Context {
	return withContextOperationLockWaitHookForTest(ctx, hook)
}

func withProjectReadUpdateQueuedHookForTest(ctx context.Context, hook func(string)) context.Context {
	return withContextOperationLockQueuedHookForTest(ctx, hook)
}

func withProjectReadCanonicalQueuedHookForTest(ctx context.Context, hook func(string)) context.Context {
	return withContextOperationLockQueuedHookForTest(ctx, hook)
}

func withoutSynchronousProjectReadRuntimeRefresh(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, suppressSynchronousProjectReadRuntimeRefreshKey{}, true)
}

// projectReadMaterializer is a disposable daemon-local read model. Its cursor
// is the upstream transitional delivery position; it is not an authority
// revision and is never written back to the project database.
type projectReadMaterializer struct {
	mu                          sync.RWMutex
	updateMu                    contextOperationLock
	canonicalMu                 contextOperationLock
	mutationConvergenceSequence uint64
	mutationConvergenceRevision uint64
	mutationConvergenceAttempt  uint64
	mutationConvergenceResult   uint64
	projectID                   string
	authority                   *ProjectionDeltaAuthority
	hydrate                     func(context.Context, []domain.Task) ([]domain.Task, error)
	hydrateDegraded             func(context.Context, []domain.Task) ([]domain.Task, error)
	affected                    func(context.Context, protocol.ProjectionDeltaBatch) ([]string, error)
	legacy                      func(context.Context) ([]domain.Task, error)
	canonical                   map[string]domain.Task
	tasks                       map[string]domain.Task
	worktrees                   map[string]git.Worktree
	metadata                    protocol.MaterializedSnapshotMetadata
	retryableFailure            bool
	healthEpoch                 uint64
	runtimeRefreshSequence      uint64
	runtimeRefreshEpoch         map[string]uint64
	runtimePublishedEpoch       map[string]uint64
	issueKeys                   keyedCheckpoint
	runtimeKeys                 keyedCheckpoint
	cancel                      context.CancelFunc
	done                        chan struct{}
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
	return &projectReadMaterializer{
		projectID: strings.TrimSpace(projectID), authority: authority, hydrate: hydrate,
		canonical: map[string]domain.Task{}, tasks: map[string]domain.Task{}, worktrees: map[string]git.Worktree{},
		runtimeRefreshEpoch: map[string]uint64{}, runtimePublishedEpoch: map[string]uint64{},
	}
}

func (m *projectReadMaterializer) lockUpdate(ctx context.Context, fallbackOperation string) (func(), error) {
	operation := runtimeProjectionWriterOperationFromContext(ctx, fallbackOperation)
	holderOperation := m.updateMu.currentHolder()
	ctx, endSpan := latencytrace.StartSpanWithEndAttributes(ctx, "daemon", "runtime_projection.writer_refresh_admission",
		"refresh.waiter_operation", operation,
		"refresh.holder_operation", holderOperation,
	)
	holderOperation, err := m.updateMu.acquire(ctx, operation)
	if err != nil {
		endSpan(err, "refresh.holder_operation", holderOperation)
		return nil, err
	}
	endSpan(nil, "refresh.holder_operation", holderOperation)
	return m.updateMu.release, nil
}

func (m *projectReadMaterializer) lockCanonical(ctx context.Context, fallbackOperation string) (func(), error) {
	operation := runtimeProjectionWriterOperationFromContext(ctx, fallbackOperation)
	holderOperation := m.canonicalMu.currentHolder()
	ctx, endSpan := latencytrace.StartSpanWithEndAttributes(ctx, "daemon", "project_read.canonical_admission",
		"canonical.waiter_operation", operation,
		"canonical.holder_operation", holderOperation,
	)
	holderOperation, err := m.canonicalMu.acquire(ctx, operation)
	if err != nil {
		endSpan(err, "canonical.holder_operation", holderOperation)
		return nil, err
	}
	endSpan(nil, "canonical.holder_operation", holderOperation)
	return m.canonicalMu.release, nil
}

func (m *projectReadMaterializer) bootstrap(ctx context.Context) error {
	if m == nil {
		return errors.New("project read materializer authority unavailable")
	}
	convergenceResult := m.mutationConvergenceResultEpoch()
	unlock, err := m.lockUpdate(ctx, "project_read.bootstrap")
	if err != nil {
		return err
	}
	defer unlock()
	canonicalUnlock, err := m.lockCanonical(ctx, "project_read.bootstrap")
	if err != nil {
		return err
	}
	defer canonicalUnlock()
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
	hydrated, _, err := m.hydrateTasksDegraded(ctx, canonical)
	if err != nil {
		return fmt.Errorf("hydrate projection bootstrap: %w", err)
	}
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, hydrated)
	metadata := materializedMetadata(snapshot.Cursor, snapshot.HeadCursor, snapshot.Projector, sources, issueKeys.sum(), runtimeKeys.sum(), snapshot.Health)
	m.replaceBootstrapAfterConvergenceResult(canonical, hydrated, metadata, issueKeys, runtimeKeys, convergenceResult)
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
			m.markUnhealthy(fmt.Errorf("watch projection deltas: %w", err), errors.Is(err, domain.ErrProjectionRetryable))
			if advanced != nil {
				advanced(m.snapshotMetadata())
			}
			if !waitProjectionConsumerRetry(ctx) {
				return
			}
			continue
		}
		affected, err := m.applyCanonical(ctx, batch)
		if err != nil {
			var verification *protocol.ProjectionVerificationError
			if errors.As(err, &verification) && verification.Kind == protocol.ProjectionVerificationOverlap && m.snapshotMetadata().DeliveryCursor > batch.AfterCursor {
				continue
			}
			if errors.As(err, &verification) && projectionVerificationRequiresRecovery(verification.Kind) {
				if bootstrapErr := m.bootstrap(ctx); bootstrapErr == nil {
					if advanced != nil {
						advanced(m.snapshotMetadata())
					}
					continue
				}
			}
			m.markUnhealthy(err, false)
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
		if err := m.refreshRuntimeDegraded(ctx, affected); err != nil {
			m.markUnhealthy(fmt.Errorf("refresh runtime enrichment: %w", err), false)
			if advanced != nil {
				advanced(m.snapshotMetadata())
			}
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
	affected, err := m.applyCanonical(ctx, batch)
	if err != nil {
		return err
	}
	// Runtime enrichment is disposable and may be slow or unavailable. The
	// canonical issue delta is already visible before this work begins so it
	// can never hide or roll back a committed lifecycle mutation.
	return m.refreshRuntime(ctx, affected)
}

func (m *projectReadMaterializer) applyCanonical(ctx context.Context, batch protocol.ProjectionDeltaBatch) ([]string, error) {
	unlock, err := m.lockCanonical(ctx, "project_read.delta_apply")
	if err != nil {
		return nil, err
	}
	defer unlock()
	return m.applyCanonicalBatch(ctx, batch)
}

func (m *projectReadMaterializer) convergeCanonical(ctx context.Context) (protocol.MaterializedSnapshotMetadata, error) {
	if m == nil || m.authority == nil {
		return protocol.MaterializedSnapshotMetadata{}, errors.New("project read materializer authority unavailable")
	}
	unlock, err := m.lockCanonical(ctx, "project_read.converge")
	if err != nil {
		return m.snapshotMetadata(), err
	}
	defer unlock()
	return m.convergeCanonicalLocked(ctx)
}

func (m *projectReadMaterializer) convergeCanonicalLocked(ctx context.Context) (protocol.MaterializedSnapshotMetadata, error) {
	for {
		cursor := m.snapshotMetadata().DeliveryCursor
		batch, err := m.authority.List(ctx, protocol.DefaultProjectID, cursor, projectReadMaterializerBatchSize)
		if err != nil {
			return m.snapshotMetadata(), fmt.Errorf("read committed projection deltas: %w", err)
		}
		if batch.DeliveryToCursor == cursor {
			return m.snapshotMetadata(), nil
		}
		if _, err := m.applyCanonicalBatch(ctx, batch); err != nil {
			var verification *protocol.ProjectionVerificationError
			if errors.As(err, &verification) && verification.Kind == protocol.ProjectionVerificationOverlap && m.snapshotMetadata().DeliveryCursor > cursor {
				continue
			}
			return m.snapshotMetadata(), fmt.Errorf("apply committed projection deltas: %w", err)
		}
		if m.snapshotMetadata().DeliveryCursor >= batch.HeadCursor {
			return m.snapshotMetadata(), nil
		}
	}
}

func (m *projectReadMaterializer) convergeMutation(ctx context.Context, revision uint64) (protocol.MaterializedSnapshotMetadata, string, error) {
	attempt := m.beginMutationConvergence(revision)
	before := m.snapshotMetadata().DeliveryCursor
	metadata, err := m.convergeCanonical(ctx)
	outcome := "current"
	if err != nil {
		outcome = "unavailable"
	} else if metadata.DeliveryCursor > before {
		outcome = "advanced"
	}
	if !m.finishMutationConvergence(revision, attempt, err) {
		outcome = "superseded"
	}
	return metadata, outcome, err
}

func (m *projectReadMaterializer) applyCanonicalBatch(ctx context.Context, batch protocol.ProjectionDeltaBatch) ([]string, error) {
	expected := m.snapshotMetadata().DeliveryCursor
	if err := protocol.VerifyProjectionDeltaBatch(batch, expected, issueProjectionProjector()); err != nil {
		return nil, err
	}
	affected := make(map[string]struct{}, len(batch.Deltas)+len(batch.EmptyAdvances))
	if m.affected != nil {
		ids, err := m.affected(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("resolve affected projection keys: %w", err)
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
			return nil, &protocol.ProjectionVerificationError{Kind: protocol.ProjectionVerificationIncompatible, Message: "unknown projection kind " + string(delta.Kind)}
		}
		if delta.Operation == protocol.ProjectionDeltaDelete {
			deleted[delta.Key] = struct{}{}
			delete(affected, delta.Key)
			continue
		}
		var payload domain.IssueProjectionDeltaPayload
		if err := json.Unmarshal(delta.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode issue projection %s: %w", delta.Key, err)
		}
		if payload.SchemaVersion != domain.IssueProjectionDeltaSchemaVersion || payload.Deleted || payload.Issue == nil || payload.Issue.ID.String() != delta.Key {
			return nil, &protocol.ProjectionVerificationError{Kind: protocol.ProjectionVerificationIncompatible, Message: "invalid complete issue value for " + delta.Key}
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
	m.mu.Lock()
	issueKeys, runtimeKeys := m.issueKeys, m.runtimeKeys
	if m.runtimeRefreshEpoch == nil {
		m.runtimeRefreshEpoch = map[string]uint64{}
	}
	if m.runtimePublishedEpoch == nil {
		m.runtimePublishedEpoch = map[string]uint64{}
	}
	m.runtimeRefreshSequence++
	canonicalEpoch := m.runtimeRefreshSequence
	for issueID := range deleted {
		if current, exists := m.canonical[issueID]; exists {
			issueKeys.remove("issue", issueID, current)
			delete(m.canonical, issueID)
		}
		if current, exists := m.tasks[issueID]; exists {
			runtimeKeys.remove("task-runtime", issueID, current)
			delete(m.tasks, issueID)
		}
		delete(m.runtimeRefreshEpoch, issueID)
		delete(m.runtimePublishedEpoch, issueID)
	}
	for issueID, task := range canonical {
		m.runtimeRefreshEpoch[issueID] = canonicalEpoch
		m.runtimePublishedEpoch[issueID] = canonicalEpoch
		if current, exists := m.canonical[issueID]; exists {
			issueKeys.remove("issue", issueID, current)
		}
		materialized := task
		if current, exists := m.tasks[issueID]; exists {
			runtimeKeys.remove("task-runtime", issueID, current)
			materialized = taskWithRuntimeOverlay(task, current)
		}
		issueKeys.add("issue", issueID, task)
		runtimeKeys.add("task-runtime", issueID, materialized)
		m.canonical[issueID] = task
		m.tasks[issueID] = materialized
	}
	m.issueKeys, m.runtimeKeys = issueKeys, runtimeKeys
	sources := mergeRootProjectionSources(m.metadata.SourceVector, batch.SourceVector)
	m.metadata = materializedMetadata(batch.DeliveryToCursor, batch.HeadCursor, batch.Projector, sources, issueKeys.sum(), runtimeKeys.sum(), batch.Health)
	m.retryableFailure = false
	m.healthEpoch++
	m.mu.Unlock()
	ids := make([]string, 0, len(affected))
	for issueID := range affected {
		ids = append(ids, issueID)
	}
	sort.Strings(ids)
	return ids, nil
}

func taskWithRuntimeOverlay(canonical, runtime domain.Task) domain.Task {
	canonical.Session = cloneSession(runtime.Session)
	canonical.HasTmuxSession = runtime.HasTmuxSession
	canonical.HasWorktree = runtime.HasWorktree
	canonical.GitAheadCount = runtime.GitAheadCount
	canonical.GitBehindCount = runtime.GitBehindCount
	canonical.HasUncommittedChanges = runtime.HasUncommittedChanges
	canonical.HasConflicts = runtime.HasConflicts
	canonical.ConflictFiles = append([]string(nil), runtime.ConflictFiles...)
	canonical.GitAdditions = runtime.GitAdditions
	canonical.GitDeletions = runtime.GitDeletions
	canonical.Origin = runtime.Origin
	canonical.PullRequest = clonePullRequest(runtime.PullRequest)
	canonical.RuntimeUpdatedAt = runtime.RuntimeUpdatedAt
	canonical.Ownership = cloneIssueOwnership(runtime.Ownership)
	canonical.CoordinationLeases = append([]domain.CoordinationLease(nil), runtime.CoordinationLeases...)
	return canonical
}

func cloneSession(session *domain.Session) *domain.Session {
	if session == nil {
		return nil
	}
	cloned := *session
	return &cloned
}

func clonePullRequest(pr *domain.PullRequest) *domain.PullRequest {
	if pr == nil {
		return nil
	}
	cloned := *pr
	return &cloned
}

func cloneIssueOwnership(ownership *domain.IssueOwnership) *domain.IssueOwnership {
	if ownership == nil {
		return nil
	}
	cloned := *ownership
	return &cloned
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
	return m.hydrateTasksWith(ctx, canonical, m.hydrate)
}

func (m *projectReadMaterializer) hydrateTasksDegraded(ctx context.Context, canonical map[string]domain.Task) (map[string]domain.Task, string, error) {
	hydrate := m.hydrateDegraded
	if hydrate == nil {
		hydrate = m.hydrate
	}
	return m.hydrateTasksWith(ctx, canonical, hydrate)
}

func (m *projectReadMaterializer) hydrateTasksWith(ctx context.Context, canonical map[string]domain.Task, hydrate func(context.Context, []domain.Task) ([]domain.Task, error)) (map[string]domain.Task, string, error) {
	tasks := make([]domain.Task, 0, len(canonical))
	for _, task := range canonical {
		tasks = append(tasks, task)
	}
	sortTasksDeterministically(tasks)
	if hydrate != nil {
		var err error
		tasks, err = hydrate(ctx, tasks)
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
	m.replaceBootstrapAfterConvergenceResult(canonical, tasks, metadata, issueKeys, runtimeKeys, m.mutationConvergenceResultEpoch())
}

func (m *projectReadMaterializer) replaceBootstrapAfterConvergenceResult(canonical, tasks map[string]domain.Task, metadata protocol.MaterializedSnapshotMetadata, issueKeys, runtimeKeys keyedCheckpoint, convergenceResult uint64) {
	m.mu.Lock()
	for issueID, worktree := range m.worktrees {
		runtimeKeys.add("worktree", issueID, worktree)
	}
	if m.mutationConvergenceResult > convergenceResult && strings.HasPrefix(m.metadata.Health, "stale: committed mutation convergence:") {
		metadata.Health = m.metadata.Health
	}
	metadata.IssueChecksum = issueKeys.sum()
	metadata.RuntimeChecksum = runtimeKeys.sum()
	metadata.SemanticChecksum = joinedMaterializedChecksum(metadata)
	m.canonical, m.tasks, m.metadata, m.issueKeys, m.runtimeKeys = canonical, tasks, metadata, issueKeys, runtimeKeys
	m.retryableFailure = false
	m.healthEpoch++
	m.runtimeRefreshSequence++
	m.runtimeRefreshEpoch = make(map[string]uint64, len(tasks))
	m.runtimePublishedEpoch = make(map[string]uint64, len(tasks))
	for issueID := range tasks {
		m.runtimeRefreshEpoch[issueID] = m.runtimeRefreshSequence
		m.runtimePublishedEpoch[issueID] = m.runtimeRefreshSequence
	}
	m.mu.Unlock()
}

func (m *projectReadMaterializer) markUnhealthy(err error, serveLastGood bool) {
	m.mu.Lock()
	m.metadata.Health = "stale: " + err.Error()
	m.retryableFailure = serveLastGood
	m.metadata.SemanticChecksum = joinedMaterializedChecksum(m.metadata)
	m.healthEpoch++
	m.mu.Unlock()
}

func (m *projectReadMaterializer) healthResultEpoch() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.healthEpoch
}

func (m *projectReadMaterializer) finishAuthoritativeReadRefresh(startedHealthEpoch uint64, err error, runtimeRefreshed bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.healthEpoch != startedHealthEpoch {
		return false
	}
	if err != nil {
		m.metadata.Health = "stale: authoritative read refresh: " + err.Error()
		m.retryableFailure = false
	} else if runtimeRefreshed || m.retryableFailure || strings.HasPrefix(m.metadata.Health, "stale: committed mutation convergence:") || strings.HasPrefix(m.metadata.Health, "stale: authoritative read refresh:") {
		m.metadata.Health = "healthy"
		m.retryableFailure = false
	}
	m.metadata.SemanticChecksum = joinedMaterializedChecksum(m.metadata)
	m.healthEpoch++
	return true
}

func (m *projectReadMaterializer) beginMutationConvergence(revision uint64) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mutationConvergenceSequence++
	attempt := m.mutationConvergenceSequence
	if revision >= m.mutationConvergenceRevision {
		m.mutationConvergenceRevision = revision
		m.mutationConvergenceAttempt = attempt
	}
	return attempt
}

func (m *projectReadMaterializer) finishMutationConvergence(revision, attempt uint64, convergenceErr error) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if revision != m.mutationConvergenceRevision || attempt != m.mutationConvergenceAttempt {
		return false
	}
	m.mutationConvergenceResult++
	m.healthEpoch++
	if convergenceErr != nil {
		m.metadata.Health = "stale: committed mutation convergence: " + convergenceErr.Error()
		m.retryableFailure = false
	} else if strings.HasPrefix(m.metadata.Health, "stale: committed mutation convergence:") {
		m.metadata.Health = "healthy"
		m.retryableFailure = false
	}
	m.metadata.SemanticChecksum = joinedMaterializedChecksum(m.metadata)
	return true
}

func (m *projectReadMaterializer) mutationConvergenceResultEpoch() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mutationConvergenceResult
}

func (m *projectReadMaterializer) snapshotMetadata() protocol.MaterializedSnapshotMetadata {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneMaterializedMetadata(m.metadata)
}

func (m *projectReadMaterializer) snapshot() ([]domain.Task, protocol.MaterializedSnapshotMetadata) {
	tasks, metadata, _ := m.snapshotWithFailureDisposition()
	return tasks, metadata
}

func (m *projectReadMaterializer) snapshotWithFailureDisposition() ([]domain.Task, protocol.MaterializedSnapshotMetadata, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tasks := make([]domain.Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	sortTasksDeterministically(tasks)
	return cloneTasks(tasks), cloneMaterializedMetadata(m.metadata), m.retryableFailure
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
	return m.refreshRuntimeWith(ctx, issueIDs, m.hydrateTasks)
}

func (m *projectReadMaterializer) refreshRuntimeDegraded(ctx context.Context, issueIDs []string) error {
	return m.refreshRuntimeWith(ctx, issueIDs, m.hydrateTasksDegraded)
}

func (m *projectReadMaterializer) refreshRuntimeWith(ctx context.Context, issueIDs []string, hydrate func(context.Context, map[string]domain.Task) (map[string]domain.Task, string, error)) error {
	wanted := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		if issueID = strings.TrimSpace(issueID); issueID != "" {
			wanted[issueID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	m.mu.Lock()
	if m.runtimeRefreshEpoch == nil {
		m.runtimeRefreshEpoch = map[string]uint64{}
	}
	if m.runtimePublishedEpoch == nil {
		m.runtimePublishedEpoch = map[string]uint64{}
	}
	m.runtimeRefreshSequence++
	refreshEpoch := m.runtimeRefreshSequence
	canonical := make(map[string]domain.Task, len(wanted))
	for issueID := range wanted {
		if task, exists := m.canonical[issueID]; exists {
			canonical[issueID] = task
			m.runtimeRefreshEpoch[issueID] = refreshEpoch
		}
	}
	m.mu.Unlock()
	if len(canonical) == 0 {
		return nil
	}
	hydrated, _, err := hydrate(ctx, canonical)
	if err != nil {
		return err
	}
	unlock, err := m.lockUpdate(ctx, "project_read.runtime_refresh")
	if err != nil {
		return err
	}
	defer unlock()
	m.mu.Lock()
	runtimeKeys := m.runtimeKeys
	supersededPending := false
	for issueID, runtime := range hydrated {
		if currentEpoch := m.runtimeRefreshEpoch[issueID]; currentEpoch != refreshEpoch {
			if m.runtimePublishedEpoch[issueID] < currentEpoch {
				supersededPending = true
			}
			continue
		}
		if current, exists := m.tasks[issueID]; exists {
			runtimeKeys.remove("task-runtime", issueID, current)
		}
		canonical, exists := m.canonical[issueID]
		if !exists {
			continue
		}
		task := taskWithRuntimeOverlay(canonical, runtime)
		runtimeKeys.add("task-runtime", issueID, task)
		m.tasks[issueID] = task
		m.runtimePublishedEpoch[issueID] = refreshEpoch
	}
	m.runtimeKeys = runtimeKeys
	m.metadata.RuntimeChecksum = runtimeKeys.sum()
	m.metadata.SemanticChecksum = joinedMaterializedChecksum(m.metadata)
	m.mu.Unlock()
	if supersededPending {
		return errors.New("newer project read runtime refresh is still pending")
	}
	return nil
}

func (m *projectReadMaterializer) replaceWorktrees(worktrees map[string]git.Worktree) {
	unlock, err := m.lockUpdate(context.Background(), "project_read.worktree_replace")
	if err != nil {
		return
	}
	defer unlock()
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
	unlock, err := m.lockUpdate(context.Background(), "project_read.worktree_replace_issues")
	if err != nil {
		return
	}
	defer unlock()
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
	projectID := d.canonicalProjectID(protocol.DefaultProjectID)
	if healthErr, unhealthy := d.projectIssueStoreHealthError(projectID); unhealthy {
		if d.cfg.Logger != nil {
			d.cfg.Logger.WarnContext(ctx, "project read materializer skipped for quarantined startup store", "project_id", projectID, "error", healthErr)
		}
		return nil
	}
	_, err := d.ensureProjectReadMaterializer(ctx, projectID, d.issues)
	return err
}

func (d *Daemon) ensureProjectReadMaterializer(ctx context.Context, projectID string, client *issues.Client) (*projectReadMaterializer, error) {
	projectID = d.canonicalProjectID(projectID)
	if healthErr, unhealthy := d.projectIssueStoreHealthError(projectID); unhealthy {
		return nil, fmt.Errorf("project read materialization unavailable for %s: %w", projectID, healthErr)
	}
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
		return d.hydrateProjectReadTasks(hydrateCtx, projectID, client, tasks)
	})
	materializer.hydrateDegraded = func(hydrateCtx context.Context, tasks []domain.Task) ([]domain.Task, error) {
		return d.hydrateProjectReadTasksDegraded(hydrateCtx, projectID, client, tasks), nil
	}
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
			return nil, protocol.MaterializedSnapshotMetadata{}, newProjectReadUnavailableError("project read materialization unavailable for %s: %w", projectID, err)
		}
	}
	if materializer == nil {
		// Embedded/unit daemons do not execute Run's startup boundary. Bootstrap
		// their disposable read model on first use so tests and library users keep
		// the same verified semantics. Production IPC cannot reach this path.
		candidate, err := d.bootstrapEmbeddedProjectReadMaterializer(context.Background(), projectID)
		if err != nil {
			return nil, protocol.MaterializedSnapshotMetadata{}, err
		}
		tasks, metadata := candidate.snapshot()
		if !strings.HasPrefix(metadata.Health, "healthy") {
			return nil, metadata, newProjectReadUnavailableError("project read materialization unhealthy: %s", metadata.Health)
		}
		return tasks, metadata, nil
	}
	tasks, metadata, retryableFailure := materializer.snapshotWithFailureDisposition()
	if !strings.HasPrefix(metadata.Health, "healthy") && !retryableFailure {
		return nil, metadata, newProjectReadUnavailableError("project read materialization unhealthy: %s", metadata.Health)
	}
	return tasks, metadata, nil
}

func (d *Daemon) bootstrapEmbeddedProjectReadMaterializer(ctx context.Context, projectID string) (*projectReadMaterializer, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return nil, newProjectReadUnavailableError("project read materialization unavailable for %s", projectID)
	}
	candidate := newProjectReadMaterializer(projectID, NewProjectionDeltaAuthority(client), func(hydrateCtx context.Context, tasks []domain.Task) ([]domain.Task, error) {
		return d.hydrateProjectReadTasks(hydrateCtx, projectID, client, tasks)
	})
	candidate.hydrateDegraded = func(hydrateCtx context.Context, tasks []domain.Task) ([]domain.Task, error) {
		return d.hydrateProjectReadTasksDegraded(hydrateCtx, projectID, client, tasks), nil
	}
	d.configureProjectReadMaterializer(candidate, projectID, client)
	if err := candidate.bootstrap(ctx); err != nil {
		return nil, newProjectReadUnavailableError("bootstrap embedded project read materialization for %s: %w", projectID, err)
	}
	return candidate, nil
}

func (d *Daemon) convergedProjectReadSnapshot(ctx context.Context, projectID string) ([]domain.Task, protocol.MaterializedSnapshotMetadata, error) {
	projectID = d.canonicalProjectID(projectID)
	materializer := d.activeProjectReadMaterializer(projectID)
	if materializer == nil {
		d.materializersMu.RLock()
		started := d.materializersStarted
		d.materializersMu.RUnlock()
		if started {
			return d.projectReadSnapshot(projectID)
		}
		candidate, err := d.bootstrapEmbeddedProjectReadMaterializer(ctx, projectID)
		if err != nil {
			return nil, protocol.MaterializedSnapshotMetadata{}, err
		}
		tasks, metadata := candidate.snapshot()
		if err := candidate.refreshRuntime(ctx, taskIDsFromTasks(tasks)); err != nil {
			return nil, metadata, newProjectReadUnavailableError("refresh embedded project runtime facts for %s: %w", projectID, err)
		}
		tasks, metadata = candidate.snapshot()
		return tasks, metadata, nil
	}
	healthEpoch := materializer.healthResultEpoch()
	metadata, err := materializer.convergeCanonical(ctx)
	if err != nil {
		materializer.finishAuthoritativeReadRefresh(healthEpoch, err, false)
		return nil, metadata, newProjectReadUnavailableError("project read convergence unavailable for %s: %w", projectID, err)
	}
	materializer.finishAuthoritativeReadRefresh(healthEpoch, nil, false)
	return d.projectReadSnapshot(projectID)
}

func (d *Daemon) hydrateProjectReadTasks(ctx context.Context, projectID string, client *issues.Client, tasks []domain.Task) ([]domain.Task, error) {
	hydrate := client.HydrateRuntime
	if d.projectReadRuntimeHydrate != nil {
		hydrate = func(ctx context.Context, projectID string, tasks []domain.Task) ([]domain.Task, error) {
			return d.projectReadRuntimeHydrate(ctx, projectID, tasks)
		}
	}
	hydrateCtx, endHydration := latencytrace.StartSpanWithEndAttributes(ctx, "daemon", "project_read.runtime_hydration", "project_id", projectID, "task_count", len(tasks))
	hydrated, err := hydrate(hydrateCtx, projectID, tasks)
	if err != nil {
		endHydration(err, "outcome", "unavailable")
		return nil, fmt.Errorf("hydrate project runtime: %w", err)
	}
	endHydration(nil, "outcome", "enriched")
	return d.enrichTasksWithSessionState(ctx, projectID, hydrated), nil
}

func (d *Daemon) hydrateProjectReadTasksDegraded(ctx context.Context, projectID string, client *issues.Client, tasks []domain.Task) []domain.Task {
	hydrated, err := d.hydrateProjectReadTasks(ctx, projectID, client, tasks)
	if err == nil {
		return hydrated
	}
	_, endFallback := latencytrace.StartSpanWithEndAttributes(ctx, "daemon", "project_read.degraded_fallback", "project_id", projectID, "reason", "session_hydration")
	endFallback(nil, "outcome", "ticket_only")
	if d.cfg.Logger != nil {
		d.cfg.Logger.WarnContext(ctx, "project read materializer continuing without runtime session enrichment", "project_id", projectID, "error", err)
	}
	return tasks
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
	if suppressed, _ := ctx.Value(suppressSynchronousProjectReadRuntimeRefreshKey{}).(bool); suppressed {
		return
	}
	projectID = d.canonicalProjectID(projectID)
	materializer := d.activeProjectReadMaterializer(projectID)
	if materializer == nil {
		return
	}
	if err := d.refreshActiveProjectReadRuntimeForIssues(ctx, projectID, materializer, issueIDs); err != nil && d.cfg.Logger != nil {
		d.cfg.Logger.Warn("refresh project read runtime materialization", "project_id", d.canonicalProjectID(projectID), "issue_ids", uniqueStrings(issueIDs), "error", err)
	}
}

func (d *Daemon) refreshProjectReadRuntimeForIssues(ctx context.Context, projectID string, issueIDs []string) error {
	projectID = d.canonicalProjectID(projectID)
	d.materializersMu.RLock()
	materializer := d.materializers[projectID]
	started := d.materializersStarted
	d.materializersMu.RUnlock()
	if materializer == nil {
		if !started {
			return nil
		}
		var err error
		materializer, err = d.ensureProjectReadMaterializer(ctx, projectID, nil)
		if err != nil {
			return newProjectReadUnavailableError("ensure authoritative project read materialization for %s: %w", projectID, err)
		}
	}
	return d.refreshActiveProjectReadRuntimeForIssues(ctx, projectID, materializer, issueIDs)
}

func (d *Daemon) refreshActiveProjectReadRuntimeForIssues(ctx context.Context, projectID string, materializer *projectReadMaterializer, issueIDs []string) error {
	issueIDs = uniqueStrings(issueIDs)
	healthEpoch := materializer.healthResultEpoch()
	if err := materializer.refreshRuntime(ctx, issueIDs); err != nil {
		materializer.finishAuthoritativeReadRefresh(healthEpoch, err, true)
		return fmt.Errorf("refresh runtime issues: %w", err)
	}
	if err := d.refreshProjectReadWorktreesForIssues(ctx, projectID, materializer, issueIDs); err != nil {
		materializer.finishAuthoritativeReadRefresh(healthEpoch, err, true)
		return fmt.Errorf("refresh runtime worktrees: %w", err)
	}
	if err := d.syncUserProjectionMaterializedIssues(ctx, projectID, issueIDs); err != nil {
		return fmt.Errorf("sync user projection issues: %w", err)
	}
	materializer.finishAuthoritativeReadRefresh(healthEpoch, nil, true)
	return nil
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
	if d != nil && d.projectReadUserProjectionSync != nil {
		return d.projectReadUserProjectionSync(ctx, projectID, issueIDs)
	}
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
		return newProjectReadUnavailableError("project read materialization unhealthy: %s", metadata.Health)
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
