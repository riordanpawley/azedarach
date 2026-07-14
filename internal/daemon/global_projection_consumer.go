package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

const rootProjectionDeltaBatchLimit = 500

type userProjectionConsumerHandle struct {
	path   string
	cancel context.CancelFunc
	done   chan struct{}
}

func (d *Daemon) ensureUserProjectionConsumers(ctx context.Context, projects []appconfig.Project) {
	if d == nil || d.userStore == nil || d.cfg.ScopedRuntime {
		return
	}
	d.userStoreRefreshMu.Lock()
	workerCtx := d.userStoreRefreshCtx
	if workerCtx == nil {
		d.userStoreRefreshCtx, d.userStoreRefreshCancel = context.WithCancel(ctx)
		workerCtx = d.userStoreRefreshCtx
	}
	stopping := d.userStoreRefreshStopping
	d.userStoreRefreshMu.Unlock()
	if stopping {
		return
	}
	desired := make(map[string]appconfig.Project, len(projects))
	for _, project := range projects {
		if projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project)); projectID != "" {
			desired[projectID] = project
		}
	}
	d.userProjectionConsumerMu.Lock()
	var stopped []*userProjectionConsumerHandle
	var stoppedProjectIDs []string
	for projectID, handle := range d.userProjectionConsumers {
		project, exists := desired[projectID]
		if exists && filepath.Clean(project.Path) == handle.path {
			continue
		}
		handle.cancel()
		delete(d.userProjectionConsumers, projectID)
		stopped = append(stopped, handle)
		stoppedProjectIDs = append(stoppedProjectIDs, projectID)
	}
	d.userProjectionConsumerMu.Unlock()
	for _, handle := range stopped {
		if handle.done == nil {
			continue
		}
		select {
		case <-handle.done:
		case <-ctx.Done():
			return
		}
	}
	for _, projectID := range stoppedProjectIDs {
		d.stopProjectReadMaterializer(ctx, projectID)
	}
	for _, project := range projects {
		projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
		if projectID == "" {
			continue
		}
		d.userProjectionConsumerMu.Lock()
		if d.userProjectionConsumers == nil {
			d.userProjectionConsumers = map[string]*userProjectionConsumerHandle{}
		}
		if _, exists := d.userProjectionConsumers[projectID]; exists {
			d.userProjectionConsumerMu.Unlock()
			continue
		}
		consumerCtx, consumerCancel := context.WithCancel(workerCtx)
		handle := &userProjectionConsumerHandle{path: filepath.Clean(project.Path), cancel: consumerCancel, done: make(chan struct{})}
		d.userProjectionConsumers[projectID] = handle
		d.userProjectionConsumerMu.Unlock()

		d.userStoreRefreshMu.Lock()
		if d.userStoreRefreshStopping {
			d.userStoreRefreshMu.Unlock()
			consumerCancel()
			d.userProjectionConsumerMu.Lock()
			if d.userProjectionConsumers[projectID] == handle {
				delete(d.userProjectionConsumers, projectID)
			}
			d.userProjectionConsumerMu.Unlock()
			return
		}
		d.userStoreRefreshWG.Add(1)
		d.userStoreRefreshMu.Unlock()
		go d.runUserProjectionConsumer(consumerCtx, project, handle)
	}
}

func (d *Daemon) runUserProjectionConsumer(ctx context.Context, project appconfig.Project, handle *userProjectionConsumerHandle) {
	projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
	defer d.userStoreRefreshWG.Done()
	defer close(handle.done)
	defer func() {
		d.userProjectionConsumerMu.Lock()
		if d.userProjectionConsumers[projectID] == handle {
			delete(d.userProjectionConsumers, projectID)
		}
		d.userProjectionConsumerMu.Unlock()
	}()
	for ctx.Err() == nil {
		state, err := d.userStore.ProjectDeltaState(ctx, projectID)
		if err != nil || !state.Initialized || state.Projector != issueProjectionProjector() {
			if err == nil {
				err = fmt.Errorf("project delta component is uninitialized or incompatible")
			}
			if recoverErr := d.recoverUserProjectionConsumer(ctx, project, err); recoverErr != nil {
				d.logUserProjectionConsumerError(projectID, recoverErr)
				if !waitProjectionConsumerRetry(ctx) {
					return
				}
			}
			continue
		}
		client := d.issueClientForProject(projectID)
		if client == nil {
			if !d.retryUnavailableUserProjection(ctx, project, errors.New("issue store unavailable")) {
				return
			}
			continue
		}
		if d.activeProjectReadMaterializer(projectID) == nil {
			if _, err := d.ensureProjectReadMaterializer(ctx, projectID, client); err != nil {
				d.logUserProjectionConsumerError(projectID, fmt.Errorf("start keyed current-state materializer: %w", err))
				if !waitProjectionConsumerRetry(ctx) {
					return
				}
				continue
			}
		}
		batch, err := NewProjectionDeltaAuthority(client).Watch(ctx, "default", state.Cursor, rootProjectionDeltaBatchLimit)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, domain.ErrProjectionCanceled) {
				return
			}
			var gap *domain.ProjectionGapError
			if errors.As(err, &gap) {
				if recoverErr := d.recoverUserProjectionConsumer(ctx, project, err); recoverErr != nil {
					d.logUserProjectionConsumerError(projectID, recoverErr)
					if !waitProjectionConsumerRetry(ctx) {
						return
					}
				}
				continue
			}
			if !d.retryUnavailableUserProjection(ctx, project, err) {
				return
			}
			continue
		}
		remapProjectionDeltaBatch(&batch, projectID)
		if err := protocol.VerifyProjectionDeltaBatch(batch, state.Cursor, issueProjectionProjector()); err != nil {
			if recoverErr := d.recoverUserProjectionConsumer(ctx, project, err); recoverErr != nil {
				d.logUserProjectionConsumerError(projectID, recoverErr)
				if !waitProjectionConsumerRetry(ctx) {
					return
				}
			}
			continue
		}
		changes, err := decodeUserProjectionChanges(batch)
		if err != nil {
			if recoverErr := d.recoverUserProjectionConsumer(ctx, project, err); recoverErr != nil {
				d.logUserProjectionConsumerError(projectID, recoverErr)
				if !waitProjectionConsumerRetry(ctx) {
					return
				}
			}
			continue
		}
		changes, err = d.hydrateUserProjectionChanges(ctx, projectID, client, batch, changes)
		if err != nil {
			if recoverErr := d.recoverUserProjectionConsumer(ctx, project, err); recoverErr != nil {
				d.logUserProjectionConsumerError(projectID, recoverErr)
				if !waitProjectionConsumerRetry(ctx) {
					return
				}
			}
			continue
		}
		next := userstore.ProjectDeltaState{
			ProjectID: projectID, Cursor: batch.DeliveryToCursor,
			SourceVector: mergeRootProjectionSources(state.SourceVector, batch.SourceVector),
			Projector:    batch.Projector, Initialized: true,
		}
		next.Hash = chainRootProjectDelta(state, next, batch.SemanticChecksum)
		root, resolveErr := appconfig.ResolveProjectRoot(project.Path)
		if resolveErr != nil {
			root = filepath.Clean(project.Path)
		}
		applyErr := d.userStore.ApplyProjectDelta(ctx, userstore.ProjectDeltaApply{
			Project:  userstore.CatalogProject{ProjectID: projectID, Name: project.Name, Path: root, DBPath: filepath.Join(root, ".azedarach", "azedarach.db")},
			Expected: state, Next: next, Changes: changes,
		})
		if errors.Is(applyErr, userstore.ErrProjectDeltaConflict) {
			continue
		}
		if applyErr != nil {
			if recoverErr := d.recoverUserProjectionConsumer(ctx, project, applyErr); recoverErr != nil {
				d.logUserProjectionConsumerError(projectID, recoverErr)
				if !waitProjectionConsumerRetry(ctx) {
					return
				}
			}
		}
	}
}

func (d *Daemon) hydrateUserProjectionChanges(ctx context.Context, projectID string, client *issues.Client, batch protocol.ProjectionDeltaBatch, changes []userstore.ProjectDeltaChange) ([]userstore.ProjectDeltaChange, error) {
	wanted := make(map[string]struct{}, len(changes)+len(batch.EmptyAdvances))
	byID := make(map[string]userstore.ProjectDeltaChange, len(changes)+len(batch.EmptyAdvances))
	observationPositions := make(map[int64]struct{}, len(batch.EmptyAdvances))
	var firstObservation, lastObservation int64
	for _, change := range changes {
		byID[change.IssueID] = change
		if !change.Delete {
			wanted[change.IssueID] = struct{}{}
		}
	}
	for _, advance := range batch.EmptyAdvances {
		if advance.Source.Authority != "legacy_issue_observation" {
			continue
		}
		position, err := strconv.ParseInt(strings.TrimSpace(advance.Source.SourceTo), 10, 64)
		if err != nil || position <= 0 {
			return nil, fmt.Errorf("decode issue observation source position %q", advance.Source.SourceTo)
		}
		observationPositions[position] = struct{}{}
		if firstObservation == 0 || position < firstObservation {
			firstObservation = position
		}
		if position > lastObservation {
			lastObservation = position
		}
	}
	if len(observationPositions) > 0 {
		span := lastObservation - firstObservation + 1
		if span > 5000 {
			return nil, fmt.Errorf("issue observation source span %d exceeds bounded lookup", span)
		}
		events, err := client.ListProjectIssueObservationEvents(ctx, firstObservation-1, int(span))
		if err != nil {
			return nil, fmt.Errorf("resolve issue observation sources %d..%d: %w", firstObservation, lastObservation, err)
		}
		for _, event := range events {
			if _, ok := observationPositions[event.ID]; !ok {
				continue
			}
			delete(observationPositions, event.ID)
			if issueID := event.IssueID.String(); issueID != "" {
				wanted[issueID] = struct{}{}
			}
		}
		if len(observationPositions) > 0 {
			return nil, fmt.Errorf("%d issue observation source positions unavailable", len(observationPositions))
		}
	}
	if len(wanted) == 0 {
		return changes, nil
	}
	ids := make([]string, 0, len(wanted))
	for issueID := range wanted {
		ids = append(ids, issueID)
	}
	sort.Strings(ids)
	tasks, err := client.GetManyWithRuntime(ctx, protocol.DefaultProjectID, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate keyed user projection changes: %w", err)
	}
	tasks = d.enrichTasksWithSessionState(ctx, projectID, tasks)
	for i := range tasks {
		task := tasks[i]
		change := byID[task.ID.String()]
		change.IssueID = task.ID.String()
		change.MaterializedIssue = &task
		byID[task.ID.String()] = change
		delete(wanted, task.ID.String())
	}
	for issueID := range wanted {
		if existing, ok := byID[issueID]; ok && existing.Delete {
			continue
		}
		return nil, fmt.Errorf("hydrate keyed user projection issue %s: %w", issueID, domain.ErrNotFound)
	}
	result := make([]userstore.ProjectDeltaChange, 0, len(byID))
	for _, change := range byID {
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].IssueID < result[j].IssueID })
	return result, nil
}

func decodeUserProjectionChanges(batch protocol.ProjectionDeltaBatch) ([]userstore.ProjectDeltaChange, error) {
	changes := make([]userstore.ProjectDeltaChange, 0, len(batch.Deltas))
	for _, delta := range batch.Deltas {
		if delta.Kind != protocol.ProjectionKind(domain.ProjectionKindIssue) {
			return nil, fmt.Errorf("unsupported root projection delta kind %q", delta.Kind)
		}
		var payload domain.IssueProjectionDeltaPayload
		if err := json.Unmarshal(delta.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode issue delta %s: %w", delta.Key, err)
		}
		if payload.SchemaVersion != domain.IssueProjectionDeltaSchemaVersion || strings.TrimSpace(payload.IssueID) != delta.Key {
			return nil, fmt.Errorf("issue delta %s payload identity/schema mismatch", delta.Key)
		}
		change := userstore.ProjectDeltaChange{IssueID: delta.Key}
		switch delta.Operation {
		case protocol.ProjectionDeltaDelete:
			if !payload.Deleted || payload.Issue != nil {
				return nil, fmt.Errorf("issue delta %s has invalid tombstone", delta.Key)
			}
			change.Delete = true
		case protocol.ProjectionDeltaUpsert:
			if payload.Deleted || payload.Issue == nil || payload.Issue.ID.String() != delta.Key {
				return nil, fmt.Errorf("issue delta %s has invalid complete value", delta.Key)
			}
			change.Issue = payload.Issue
		default:
			return nil, fmt.Errorf("issue delta %s has unsupported operation %q", delta.Key, delta.Operation)
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func (d *Daemon) projectDeltaSnapshotState(ctx context.Context, projectID string, client *issues.Client) (userstore.ProjectDeltaState, error) {
	authority := NewProjectionDeltaAuthority(client)
	batch, err := authority.List(ctx, "default", 0, 1)
	if err != nil {
		return userstore.ProjectDeltaState{}, err
	}
	remapProjectionDeltaBatch(&batch, projectID)
	if err := protocol.VerifyProjectionDeltaBatch(batch, 0, issueProjectionProjector()); err != nil {
		return userstore.ProjectDeltaState{}, err
	}
	snapshot, err := authority.Snapshot(ctx, "default", batch.HeadCursor)
	if err != nil {
		return userstore.ProjectDeltaState{}, err
	}
	snapshot.ProjectID = naming.ProjectID(projectID)
	protocol.FinalizeProjectionSnapshot(&snapshot)
	if err := protocol.VerifyProjectionSnapshot(snapshot, issueProjectionProjector()); err != nil {
		return userstore.ProjectDeltaState{}, err
	}
	return userstore.ProjectDeltaState{ProjectID: projectID, Cursor: snapshot.Cursor, Hash: snapshot.SemanticChecksum, SourceVector: snapshot.SourceVector, Projector: snapshot.Projector, Initialized: true}, nil
}

func (d *Daemon) projectDeltaHead(ctx context.Context, projectID string, client *issues.Client) (uint64, error) {
	batch, err := NewProjectionDeltaAuthority(client).List(ctx, "default", 0, 1)
	if err != nil {
		return 0, err
	}
	remapProjectionDeltaBatch(&batch, projectID)
	if err := protocol.VerifyProjectionDeltaBatch(batch, 0, issueProjectionProjector()); err != nil {
		return 0, err
	}
	return batch.HeadCursor, nil
}

func mergeRootProjectionSources(current, next []protocol.ProjectionSourceRange) []protocol.ProjectionSourceRange {
	byAuthority := make(map[string]protocol.ProjectionSourceRange, len(current)+len(next))
	for _, source := range current {
		byAuthority[source.Authority] = source
	}
	for _, source := range next {
		if previous, ok := byAuthority[source.Authority]; ok {
			source.SourceFrom = previous.SourceFrom
			if source.TerminalHash == "" {
				source.TerminalHash = previous.TerminalHash
			}
		}
		byAuthority[source.Authority] = source
	}
	authorities := make([]string, 0, len(byAuthority))
	for authority := range byAuthority {
		authorities = append(authorities, authority)
	}
	sort.Strings(authorities)
	merged := make([]protocol.ProjectionSourceRange, 0, len(authorities))
	for _, authority := range authorities {
		merged = append(merged, byAuthority[authority])
	}
	return merged
}

func chainRootProjectDelta(current, next userstore.ProjectDeltaState, batchHash string) string {
	raw, _ := json.Marshal(struct {
		ProjectID string                           `json:"project_id"`
		From      uint64                           `json:"from"`
		To        uint64                           `json:"to"`
		Previous  string                           `json:"previous"`
		Batch     string                           `json:"batch"`
		Sources   []protocol.ProjectionSourceRange `json:"sources"`
	}{next.ProjectID, current.Cursor, next.Cursor, current.Hash, batchHash, next.SourceVector})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (d *Daemon) recoverUserProjectionConsumer(ctx context.Context, project appconfig.Project, cause error) error {
	projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
	_ = d.userStore.MarkProjectDeltaStale(ctx, projectID, cause)
	return d.refreshRegisteredUserProject(ctx, project)
}

func (d *Daemon) retryUnavailableUserProjection(ctx context.Context, project appconfig.Project, cause error) bool {
	projectID := protocol.NormalizeProjectID(appconfig.RegisteredProjectID(project))
	d.logUserProjectionConsumerError(projectID, cause)
	if !waitProjectionConsumerRetry(ctx) {
		return false
	}
	if err := d.recoverUserProjectionConsumer(ctx, project, cause); err != nil {
		d.logUserProjectionConsumerError(projectID, err)
	}
	return ctx.Err() == nil
}

func waitProjectionConsumerRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (d *Daemon) logUserProjectionConsumerError(projectID string, err error) {
	if d != nil && d.cfg.Logger != nil && err != nil {
		d.cfg.Logger.Warn("consume project delta into user projection", "project_id", projectID, "error", err)
	}
}
