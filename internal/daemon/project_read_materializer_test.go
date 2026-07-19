package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/observability"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	_ "modernc.org/sqlite"
)

type watchErrorProjectionStore struct {
	projectionDeltaStore
	watch func(context.Context, string, uint64, int) ([]domain.ProjectionDelta, uint64, error)
}

type listErrorProjectionStore struct {
	projectionDeltaStore
	list func(context.Context, string, uint64, int) ([]domain.ProjectionDelta, uint64, error)
}

func (s listErrorProjectionStore) ListProjectionDeltas(ctx context.Context, projectID string, after uint64, limit int) ([]domain.ProjectionDelta, uint64, error) {
	return s.list(ctx, projectID, after, limit)
}

func (s watchErrorProjectionStore) WatchProjectionDeltas(ctx context.Context, projectID string, after uint64, limit int) ([]domain.ProjectionDelta, uint64, error) {
	return s.watch(ctx, projectID, after, limit)
}

type watchBarrierProjectionStore struct {
	projectionDeltaStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *watchBarrierProjectionStore) WatchProjectionDeltas(ctx context.Context, projectID string, after uint64, limit int) ([]domain.ProjectionDelta, uint64, error) {
	s.once.Do(func() {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, 0, &domain.ProjectionCanceledError{Cause: err}
	}
	return s.projectionDeltaStore.WatchProjectionDeltas(ctx, projectID, after, limit)
}

func TestProjectReadMaterializerTransientWatchErrorRetainsLastGoodWithoutLegacyExport(t *testing.T) {
	client, _ := newTestIssueClient(t)
	ctx := context.Background()
	created, err := client.Create(ctx, issues.CreateTaskParams{Title: "last good", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	var watchCalls, legacyExports atomic.Int32
	watchFailed := make(chan protocol.MaterializedSnapshotMetadata, 1)
	store := watchErrorProjectionStore{projectionDeltaStore: client, watch: func(context.Context, string, uint64, int) ([]domain.ProjectionDelta, uint64, error) {
		watchCalls.Add(1)
		return nil, 0, fmt.Errorf("%w: transient watch", domain.ErrProjectionRetryable)
	}}
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(store), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) { return tasks, nil })
	materializer.legacy = func(ctx context.Context) ([]domain.Task, error) {
		legacyExports.Add(1)
		return client.ListWithRuntimeArchiveMode(ctx, protocol.DefaultProjectID, issues.ArchiveInclude)
	}
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	before, beforeMeta := materializer.snapshot()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		materializer.run(runCtx, func(metadata protocol.MaterializedSnapshotMetadata) {
			select {
			case watchFailed <- metadata:
			default:
			}
		})
		close(done)
	}()
	failedMeta := <-watchFailed
	after, afterMeta := materializer.snapshot()
	if watchCalls.Load() == 0 || legacyExports.Load() != 1 {
		t.Fatalf("watch calls=%d legacy exports=%d, want transient retry without re-export", watchCalls.Load(), legacyExports.Load())
	}
	if len(after) != 1 || after[0].ID.String() != created || checksumJSON(after) != checksumJSON(before) || afterMeta.DeliveryCursor != beforeMeta.DeliveryCursor || !strings.HasPrefix(afterMeta.Health, "stale:") {
		t.Fatalf("transient watch changed last-good materialization: before=%+v/%+v after=%+v/%+v", before, beforeMeta, after, afterMeta)
	}
	if failedMeta.Health != afterMeta.Health {
		t.Fatalf("callback health=%q snapshot health=%q", failedMeta.Health, afterMeta.Health)
	}
	d := &Daemon{materializers: map[string]*projectReadMaterializer{"project": materializer}, materializersStarted: true}
	served, servedMeta, err := d.projectReadSnapshot("project")
	if err != nil || len(served) != 1 || checksumJSON(served) != checksumJSON(before) || servedMeta.Health != afterMeta.Health {
		t.Fatalf("retryable stale snapshot not served: tasks=%+v metadata=%+v err=%v", served, servedMeta, err)
	}
	materializer.markUnhealthy(errors.New("structural projection corruption"), false)
	served, servedMeta, err = d.projectReadSnapshot("project")
	if err == nil || served != nil || !strings.Contains(servedMeta.Health, "structural projection corruption") {
		t.Fatalf("non-retryable failure did not fail closed: tasks=%+v metadata=%+v err=%v", served, servedMeta, err)
	}
	cancel()
	<-done
}

func TestAuthoritativeReadResultCannotClearNewerMutationConvergenceFailure(t *testing.T) {
	materializer := newProjectReadMaterializer("project", nil, nil)
	readAttempt := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshCanonical)
	mutationAttempt := materializer.beginMutationConvergence(7)
	if !materializer.finishMutationConvergence(7, mutationAttempt, errors.New("newer mutation failure")) {
		t.Fatal("newer mutation convergence result was not recorded")
	}
	if materializer.finishAuthoritativeReadRefresh(readAttempt, nil) {
		t.Fatal("older authoritative read result cleared a newer health result")
	}
	if materializer.finishAuthoritativeReadRefresh(readAttempt, errors.New("older canonical failure")) {
		t.Fatal("older authoritative read failure overwrote a newer health result")
	}
	metadata := materializer.snapshotMetadata()
	if !strings.Contains(metadata.Health, "newer mutation failure") {
		t.Fatalf("health = %q, want newer mutation failure retained", metadata.Health)
	}
}

func TestNewestAuthoritativeReadFailureWinsOlderConcurrentSuccess(t *testing.T) {
	materializer := newProjectReadMaterializer("project", nil, nil)
	materializer.metadata.Health = "healthy"
	older := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshRuntime)
	newer := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshRuntime)
	if materializer.finishAuthoritativeReadRefresh(older, nil) {
		t.Fatal("older authoritative success published while newer refresh owned completion")
	}
	if !materializer.finishAuthoritativeReadRefresh(newer, errors.New("newer runtime failure")) {
		t.Fatal("newer authoritative failure was not published")
	}
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, "newer runtime failure") {
		t.Fatalf("health = %q, want newer runtime failure", got)
	}
}

func TestCanonicalRefreshDoesNotClearRuntimeRefreshFailure(t *testing.T) {
	materializer := newProjectReadMaterializer("project", nil, nil)
	materializer.metadata.Health = "healthy"

	runtimeAttempt := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshRuntime)
	if !materializer.finishAuthoritativeReadRefresh(runtimeAttempt, errors.New("runtime projection unavailable")) {
		t.Fatal("runtime refresh failure was not published")
	}
	canonicalAttempt := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshCanonical)
	if !materializer.finishAuthoritativeReadRefresh(canonicalAttempt, nil) {
		t.Fatal("canonical refresh success was not published")
	}
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, "runtime projection unavailable") {
		t.Fatalf("canonical refresh cleared runtime health: %q", got)
	}

	runtimeRecovery := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshRuntime)
	if !materializer.finishAuthoritativeReadRefresh(runtimeRecovery, nil) {
		t.Fatal("runtime refresh recovery was not published")
	}
	if got := materializer.snapshotMetadata().Health; got != "healthy" {
		t.Fatalf("runtime recovery health = %q, want healthy", got)
	}
}

func TestCanonicalReadDoesNotStrandConcurrentRuntimeRecovery(t *testing.T) {
	materializer := newProjectReadMaterializer("project", nil, nil)
	runtimeFailure := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshRuntime)
	materializer.finishAuthoritativeReadRefresh(runtimeFailure, errors.New("runtime projection unavailable"))

	runtimeRecovery := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshRuntime)
	canonicalRead := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshCanonical)
	if !materializer.finishAuthoritativeReadRefresh(canonicalRead, nil) {
		t.Fatal("canonical read completion was not accepted")
	}
	if !materializer.finishAuthoritativeReadRefresh(runtimeRecovery, nil) {
		t.Fatal("canonical read invalidated concurrent runtime recovery")
	}
	if got := materializer.snapshotMetadata().Health; got != "healthy" {
		t.Fatalf("runtime recovery health = %q, want healthy", got)
	}
}

func TestCanonicalReadDoesNotLoseConcurrentRuntimeFailure(t *testing.T) {
	materializer := newProjectReadMaterializer("project", nil, nil)
	materializer.metadata.Health = "healthy"

	runtimeFailure := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshRuntime)
	canonicalRead := materializer.beginAuthoritativeReadRefresh(authoritativeReadRefreshCanonical)
	if !materializer.finishAuthoritativeReadRefresh(canonicalRead, nil) {
		t.Fatal("canonical read completion was not accepted")
	}
	if !materializer.finishAuthoritativeReadRefresh(runtimeFailure, errors.New("runtime projection unavailable")) {
		t.Fatal("canonical read invalidated concurrent runtime failure")
	}
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, "runtime projection unavailable") {
		t.Fatalf("runtime failure health = %q, want failure retained", got)
	}
}

func TestProjectReadMaterializerGapWatchPerformsVerifiedBootstrapRecovery(t *testing.T) {
	client, _ := newTestIssueClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, issues.CreateTaskParams{Title: "recover", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	var watchCalls, legacyExports atomic.Int32
	store := watchErrorProjectionStore{projectionDeltaStore: client, watch: func(context.Context, string, uint64, int) ([]domain.ProjectionDelta, uint64, error) {
		watchCalls.Add(1)
		return nil, 0, &domain.ProjectionGapError{ProjectID: "project", Expected: 1, Actual: 2}
	}}
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(store), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) { return tasks, nil })
	materializer.legacy = func(ctx context.Context) ([]domain.Task, error) {
		legacyExports.Add(1)
		return client.ListWithRuntimeArchiveMode(ctx, protocol.DefaultProjectID, issues.ArchiveInclude)
	}
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		materializer.run(runCtx, nil)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for legacyExports.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if legacyExports.Load() < 2 {
		t.Fatalf("verified gap recovery exports=%d watch calls=%d, want bootstrap recovery", legacyExports.Load(), watchCalls.Load())
	}
	cancel()
	<-done
}

func TestProjectReadMaterializerBootstrapReplayEmptyAdvanceAndRestart(t *testing.T) {
	client, _ := newTestIssueClient(t)
	ctx := context.Background()
	created, err := client.Create(ctx, issues.CreateTaskParams{Title: "materialized", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	authority := NewProjectionDeltaAuthority(client)
	hydrate := func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) { return tasks, nil }
	first := newProjectReadMaterializer("project", authority, hydrate)
	if err := first.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	before, beforeMeta := first.snapshot()
	if len(before) != 1 || before[0].ID.String() != created || beforeMeta.DeliveryCursor == 0 || !beforeMeta.DeliveryCursorTransitional || beforeMeta.SemanticChecksum == "" {
		t.Fatalf("bootstrap snapshot = %+v source=%+v", before, beforeMeta)
	}
	if err := client.Update(ctx, created, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	batch, err := authority.List(ctx, protocol.DefaultProjectID, beforeMeta.DeliveryCursor, 20)
	if err != nil {
		t.Fatal(err)
	}
	gap := batch
	gap.AfterCursor++
	protocol.FinalizeProjectionDeltaBatch(&gap)
	if err := first.apply(ctx, gap); err == nil {
		t.Fatal("gap replay unexpectedly advanced materializer")
	} else {
		var verification *protocol.ProjectionVerificationError
		if !errors.As(err, &verification) || verification.Kind != protocol.ProjectionVerificationGap {
			t.Fatalf("gap replay error = %v, want gap", err)
		}
	}
	if err := first.apply(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := first.apply(ctx, batch); err == nil {
		t.Fatal("overlap replay unexpectedly advanced materializer")
	} else {
		var verification *protocol.ProjectionVerificationError
		if !errors.As(err, &verification) || verification.Kind != protocol.ProjectionVerificationOverlap {
			t.Fatalf("replay error = %v, want overlap", err)
		}
	}
	updated, updatedMeta := first.snapshot()
	if updated[0].Status != domain.StatusInProgress || updatedMeta.DeliveryCursor <= beforeMeta.DeliveryCursor {
		t.Fatalf("updated snapshot = %+v source=%+v", updated, updatedMeta)
	}
	if _, err := client.CommitProjectionEmptyAdvance(ctx, issues.ProjectionSourceAdvance{ProjectID: protocol.DefaultProjectID, SourceAuthority: "external-observation", SourcePosition: "17", IdempotencyKey: "test-empty-17"}); err != nil {
		t.Fatal(err)
	}
	emptyBatch, err := authority.List(ctx, protocol.DefaultProjectID, updatedMeta.DeliveryCursor, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyBatch.Deltas) != 0 || len(emptyBatch.EmptyAdvances) != 1 {
		t.Fatalf("empty advance batch = %+v", emptyBatch)
	}
	if err := first.apply(ctx, emptyBatch); err != nil {
		t.Fatal(err)
	}
	afterEmpty, afterEmptyMeta := first.snapshot()
	if afterEmptyMeta.DeliveryCursor <= updatedMeta.DeliveryCursor || checksumJSON(afterEmpty) != checksumJSON(updated) {
		t.Fatalf("empty advance changed values or failed to advance: before=%+v after=%+v", updatedMeta, afterEmptyMeta)
	}
	restarted := newProjectReadMaterializer("project", authority, hydrate)
	if err := restarted.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	restartedTasks, restartedMeta := restarted.snapshot()
	if checksumJSON(restartedTasks) != checksumJSON(afterEmpty) || restartedMeta.DeliveryCursor != afterEmptyMeta.DeliveryCursor || restartedMeta.SemanticChecksum != afterEmptyMeta.SemanticChecksum {
		t.Fatalf("restart mismatch tasks=%+v source=%+v want source=%+v", restartedTasks, restartedMeta, afterEmptyMeta)
	}
}

func TestProjectReadMaterializerHistoricalNonzeroCursorRestartRetainsLegacyOverlay(t *testing.T) {
	ctx := context.Background()
	client, repoDir := newTestIssueClient(t)
	legacyID, err := client.Create(ctx, issues.CreateTaskParams{Title: "before projection bridge", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF; DROP TABLE projection_consumer_cursors; DROP TABLE projection_deltas; DROP TABLE projection_streams; DELETE FROM schema_migrations WHERE id='0047_projection_delta_authority'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = upgraded.CloseDB() })
	if err := upgraded.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	postBridgeID, err := upgraded.Create(ctx, issues.CreateTaskParams{Title: "after projection bridge", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	authority := NewProjectionDeltaAuthority(upgraded)
	d := &Daemon{}
	bootstrap := func() (*projectReadMaterializer, []domain.Task, protocol.MaterializedSnapshotMetadata) {
		materializer := newProjectReadMaterializer("project", authority, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) { return tasks, nil })
		d.configureProjectReadMaterializer(materializer, protocol.DefaultProjectID, upgraded)
		if err := materializer.bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
		tasks, metadata := materializer.snapshot()
		return materializer, tasks, metadata
	}
	_, beforeRestart, beforeMetadata := bootstrap()
	if beforeMetadata.DeliveryCursor == 0 || len(beforeRestart) != 2 {
		t.Fatalf("historical bootstrap = %+v metadata=%+v", beforeRestart, beforeMetadata)
	}
	ids := taskIDsFromTasks(beforeRestart)
	sort.Strings(ids)
	wantIDs := []string{legacyID, postBridgeID}
	sort.Strings(wantIDs)
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("historical bootstrap IDs = %v, want %v", ids, wantIDs)
	}
	if !materializedSourceHasAuthority(beforeMetadata.SourceVector, "legacy_bootstrap_export") {
		t.Fatalf("historical bootstrap source = %+v, want durable legacy overlay", beforeMetadata.SourceVector)
	}
	_, afterRestart, afterMetadata := bootstrap()
	if checksumJSON(afterRestart) != checksumJSON(beforeRestart) || afterMetadata.DeliveryCursor != beforeMetadata.DeliveryCursor || afterMetadata.IssueChecksum != beforeMetadata.IssueChecksum {
		t.Fatalf("historical restart diverged: before=%+v/%+v after=%+v/%+v", beforeRestart, beforeMetadata, afterRestart, afterMetadata)
	}
}

func TestProjectReadMaterializerApplyIsKeyedAtProductionSize(t *testing.T) {
	const taskCount = 5000
	materializer, target, batch, hydrated := productionMaterializerDelta(t, taskCount, 1)
	before := materializer.snapshotMetadata()
	if err := materializer.apply(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	tasks, after := materializer.snapshot()
	if got := hydrated.Load(); got != 1 {
		t.Fatalf("hydrated task count = %d, want 1 affected key", got)
	}
	if len(tasks) != taskCount || after.DeliveryCursor != before.DeliveryCursor+1 || after.IssueChecksum == before.IssueChecksum {
		t.Fatalf("keyed production apply tasks=%d before=%+v after=%+v", len(tasks), before, after)
	}
	updated, ok := findDaemonTaskByID(tasks, target.ID.String())
	if !ok || updated.Status != domain.StatusInProgress {
		t.Fatalf("target after keyed apply = %+v, found=%t", updated, ok)
	}
	hydrated.Store(0)
	if err := materializer.refreshRuntime(context.Background(), []string{target.ID.String()}); err != nil {
		t.Fatal(err)
	}
	if got := hydrated.Load(); got != 1 {
		t.Fatalf("runtime refresh hydrated task count = %d, want 1 affected key", got)
	}
	deleteBatch := protocol.ProjectionDeltaBatch{
		SchemaVersion: protocol.ProjectionDeltaSchemaVersion, ProjectID: "project", AfterCursor: after.DeliveryCursor, HeadCursor: after.DeliveryCursor + 1, DeliveryToCursor: after.DeliveryCursor + 1,
		DeliveryContract: protocol.ProjectionDeliveryContract, DeliveryCursorTransitional: true, Projector: issueProjectionProjector(), Health: "healthy",
		Deltas: []protocol.ProjectionDelta{{Cursor: after.DeliveryCursor + 1, Kind: protocol.ProjectionKind(domain.ProjectionKindIssue), Key: target.ID.String(), Operation: protocol.ProjectionDeltaDelete, Source: protocol.ProjectionSourceRange{Authority: "load", SourceFrom: fmt.Sprint(after.DeliveryCursor + 1), SourceTo: fmt.Sprint(after.DeliveryCursor + 1), Transitional: true}}},
	}
	protocol.FinalizeProjectionDeltaBatch(&deleteBatch)
	if err := materializer.apply(context.Background(), deleteBatch); err != nil {
		t.Fatal(err)
	}
	tasks, deletedMetadata := materializer.snapshot()
	if _, ok := findDaemonTaskByID(tasks, target.ID.String()); ok || len(tasks) != taskCount-1 {
		t.Fatalf("keyed delete retained target or wrong count: found=%t tasks=%d", ok, len(tasks))
	}
	materializer.mu.RLock()
	issueKeys, runtimeKeys := checkpointMaterializedTasks(materializer.canonical, materializer.tasks)
	materializer.mu.RUnlock()
	if deletedMetadata.IssueChecksum != issueKeys.sum() || deletedMetadata.RuntimeChecksum != runtimeKeys.sum() {
		t.Fatalf("keyed delete checkpoints diverged: metadata=%+v issue=%s runtime=%s", deletedMetadata, issueKeys.sum(), runtimeKeys.sum())
	}
}

func TestProjectReadMaterializerEmptyObservationRefreshesOnlyAffectedIssue(t *testing.T) {
	ctx := context.Background()
	client, _ := newTestIssueClient(t)
	targetID, err := client.Create(ctx, issues.CreateTaskParams{Title: "human findings", Type: domain.TypeInvestigation, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(ctx, issues.CreateTaskParams{Title: "unaffected", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{issues: client}
	var hydrated atomic.Int64
	materializer := newProjectReadMaterializer(protocol.DefaultProjectID, NewProjectionDeltaAuthority(client), func(hydrateCtx context.Context, tasks []domain.Task) ([]domain.Task, error) {
		hydrated.Add(int64(len(tasks)))
		tasks, hydrateErr := client.HydrateRuntime(hydrateCtx, protocol.DefaultProjectID, tasks)
		if hydrateErr != nil {
			return nil, hydrateErr
		}
		return d.enrichTasksWithSessionState(hydrateCtx, protocol.DefaultProjectID, tasks), nil
	})
	d.configureProjectReadMaterializer(materializer, protocol.DefaultProjectID, client)
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	before, beforeMetadata := materializer.snapshot()
	target, ok := findDaemonTaskByID(before, targetID)
	if !ok || !target.IssueFacts().WaitingHuman {
		t.Fatalf("target before acceptance = %+v, found=%t", target, ok)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, targetID, issues.IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Source: "human", Payload: map[string]any{"investigation_findings_accepted": true}}); err != nil {
		t.Fatal(err)
	}
	batch, err := NewProjectionDeltaAuthority(client).List(ctx, protocol.DefaultProjectID, beforeMetadata.DeliveryCursor, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Deltas) != 0 || len(batch.EmptyAdvances) != 1 {
		t.Fatalf("acceptance batch = %+v", batch)
	}
	hydrated.Store(0)
	if err := materializer.apply(ctx, batch); err != nil {
		t.Fatal(err)
	}
	after, _ := materializer.snapshot()
	target, ok = findDaemonTaskByID(after, targetID)
	if !ok || target.IssueFacts().WaitingHuman || hydrated.Load() != 1 {
		t.Fatalf("target after keyed acceptance = %+v found=%t hydrated=%d", target, ok, hydrated.Load())
	}
}

func TestProjectReadMaterializerRuntimeHydrationFailureReturnsTicketData(t *testing.T) {
	recorder := newProjectReadTraceRecorder(t)
	ctx := context.Background()
	client, _ := newTestIssueClient(t)
	id, err := client.Create(ctx, issues.CreateTaskParams{Title: "ticket survives degraded runtime", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:           Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:        client,
		materializers: map[string]*projectReadMaterializer{},
		projectReadRuntimeHydrate: func(context.Context, string, []domain.Task) ([]domain.Task, error) {
			return nil, errors.New("injected runtime hydration failure")
		},
	}
	materializer, err := d.ensureProjectReadMaterializer(ctx, protocol.DefaultProjectID, client)
	if err != nil {
		t.Fatalf("bootstrap degraded runtime materializer: %v", err)
	}
	if err := materializer.refreshRuntimeDegraded(ctx, []string{id}); err != nil {
		t.Fatalf("background degraded runtime refresh: %v", err)
	}
	tasks, _ := materializer.snapshot()
	got, ok := findDaemonTaskByID(tasks, id)
	if !ok || got.Title != "ticket survives degraded runtime" {
		t.Fatalf("degraded ticket = %+v found=%t", got, ok)
	}
	assertProjectReadSpans(t, recorder, "daemon.project_read.runtime_hydration", "daemon.project_read.degraded_fallback")
}

func TestBackgroundProjectReadRuntimeRefreshDoesNotEnsureMaterializer(t *testing.T) {
	ctx := context.Background()
	client, _ := newTestIssueClient(t)
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "background refresh remains best effort", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	var hydrateCalls atomic.Int32
	d := &Daemon{
		issues:               client,
		materializers:        map[string]*projectReadMaterializer{},
		materializersStarted: true,
		projectReadRuntimeHydrate: func(_ context.Context, _ string, tasks []domain.Task) ([]domain.Task, error) {
			hydrateCalls.Add(1)
			return tasks, nil
		},
	}

	d.refreshProjectReadRuntime(ctx, protocol.DefaultProjectID, issueID)

	if got := hydrateCalls.Load(); got != 0 {
		t.Fatalf("background hydration calls = %d, want no on-demand materializer ensure", got)
	}
	if materializer := d.activeProjectReadMaterializer(protocol.DefaultProjectID); materializer != nil {
		t.Fatal("background refresh installed a project read materializer")
	}
}

func TestProjectReadMaterializerWorktreeFailureDoesNotAbortBootstrap(t *testing.T) {
	recorder := newProjectReadTraceRecorder(t)
	ctx := context.Background()
	client, _ := newTestIssueClient(t)
	id, err := client.Create(ctx, issues.CreateTaskParams{Title: "ticket survives degraded worktrees", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:           Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:        client,
		materializers: map[string]*projectReadMaterializer{},
		projectReadWorktreeRefresh: func(context.Context, string, *projectReadMaterializer) error {
			return errors.New("injected worktree enrichment failure")
		},
	}
	materializer, err := d.ensureProjectReadMaterializer(ctx, protocol.DefaultProjectID, client)
	if err != nil {
		t.Fatalf("bootstrap degraded worktree materializer: %v", err)
	}
	tasks, _ := materializer.snapshot()
	if got, ok := findDaemonTaskByID(tasks, id); !ok || got.Title != "ticket survives degraded worktrees" {
		t.Fatalf("degraded ticket = %+v found=%t", got, ok)
	}
	if worktrees := materializer.snapshotWorktrees(); len(worktrees) != 0 {
		t.Fatalf("degraded worktrees = %+v want empty", worktrees)
	}
	assertProjectReadSpans(t, recorder, "daemon.project_read.runtime_hydration", "daemon.project_read.degraded_fallback")
}

func newProjectReadTraceRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	t.Setenv(latencytrace.EnvVar, "")
	t.Setenv(observability.EnvVar, "true")
	latencytrace.SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		latencytrace.SetConfigEnabled(false)
	})
	return recorder
}

func assertProjectReadSpans(t *testing.T, recorder *tracetest.SpanRecorder, names ...string) {
	t.Helper()
	found := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range recorder.Ended() {
		found[span.Name()] = span
	}
	for _, name := range names {
		span := found[name]
		if span == nil {
			t.Fatalf("missing span %q; ended=%v", name, found)
		}
		attrs := map[string]bool{}
		for _, attr := range span.Attributes() {
			attrs[string(attr.Key)] = true
		}
		for _, key := range []string{"component", "phase", "outcome"} {
			if !attrs[key] {
				t.Fatalf("span %q missing attribute %q", name, key)
			}
		}
	}
}

func materializedSourceHasAuthority(sources []protocol.ProjectionSourceRange, authority string) bool {
	for _, source := range sources {
		if source.Authority == authority {
			return true
		}
	}
	return false
}

func productionMaterializerDelta(t testing.TB, taskCount int, cursor uint64) (*projectReadMaterializer, domain.Task, protocol.ProjectionDeltaBatch, *atomic.Int64) {
	t.Helper()
	now := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	canonical := make(map[string]domain.Task, taskCount)
	for i := 0; i < taskCount; i++ {
		issueID := fmt.Sprintf("load-%05d", i)
		canonical[issueID] = domain.Task{ID: naming.IssueID(issueID), Title: "production load", Type: domain.TypeTask, Status: domain.StatusOpen, CreatedAt: now, UpdatedAt: now}
	}
	target := canonical["load-02500"]
	var hydrated atomic.Int64
	materializer := newProjectReadMaterializer("project", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		hydrated.Add(int64(len(tasks)))
		return tasks, nil
	})
	hydratedTasks := make(map[string]domain.Task, len(canonical))
	for issueID, task := range canonical {
		hydratedTasks[issueID] = task
	}
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, hydratedTasks)
	materializer.replaceBootstrap(canonical, hydratedTasks, materializedMetadata(cursor, cursor+1, issueProjectionProjector(), nil, issueKeys.sum(), runtimeKeys.sum(), "healthy"), issueKeys, runtimeKeys)
	hydrated.Store(0)
	target.Status = domain.StatusInProgress
	target.UpdatedAt = now.Add(time.Second)
	return materializer, target, productionMaterializerBatch(t, target, cursor), &hydrated
}

func productionMaterializerBatch(t testing.TB, target domain.Task, cursor uint64) protocol.ProjectionDeltaBatch {
	t.Helper()
	payload, err := json.Marshal(domain.IssueProjectionDeltaPayload{SchemaVersion: domain.IssueProjectionDeltaSchemaVersion, IssueID: target.ID.String(), Issue: &target})
	if err != nil {
		t.Fatal(err)
	}
	batch := protocol.ProjectionDeltaBatch{
		SchemaVersion: protocol.ProjectionDeltaSchemaVersion, ProjectID: "project", AfterCursor: cursor, HeadCursor: cursor + 1, DeliveryToCursor: cursor + 1,
		DeliveryContract: protocol.ProjectionDeliveryContract, DeliveryCursorTransitional: true, Projector: issueProjectionProjector(), Health: "healthy",
		Deltas: []protocol.ProjectionDelta{{Cursor: cursor + 1, Kind: protocol.ProjectionKind(domain.ProjectionKindIssue), Key: target.ID.String(), Operation: protocol.ProjectionDeltaUpsert, Payload: payload, Source: protocol.ProjectionSourceRange{Authority: "load", SourceFrom: fmt.Sprint(cursor + 1), SourceTo: fmt.Sprint(cursor + 1), Transitional: true}}},
	}
	protocol.FinalizeProjectionDeltaBatch(&batch)
	return batch
}

func TestProjectReadMaterializerWatchBlocksAndWakesAcrossClients(t *testing.T) {
	repoDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := newMigratedIssueClient(t, repoDir, logger)
	reader := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = reader.CloseDB() })
	if err := reader.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(reader), nil)
	if err := materializer.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	advanced := make(chan protocol.MaterializedSnapshotMetadata, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		materializer.run(ctx, func(metadata protocol.MaterializedSnapshotMetadata) { advanced <- metadata })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	for reader.ResourceDiagnostics().ProjectionWatchesActive == 0 {
		runtime.Gosched()
	}
	select {
	case <-advanced:
		t.Fatal("watch returned without source advancement")
	default:
	}
	if _, err := writer.Create(context.Background(), issues.CreateTaskParams{Title: "cross-client", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	metadata := <-advanced
	if metadata.DeliveryCursor == 0 {
		t.Fatalf("watch metadata = %+v", metadata)
	}
}

func TestProjectReadMaterializersConvergeAcrossDaemons(t *testing.T) {
	repoDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := newMigratedIssueClient(t, repoDir, logger)
	readers := []*issues.Client{issues.NewClient(repoDir, logger), issues.NewClient(repoDir, logger)}
	materializers := make([]*projectReadMaterializer, 0, len(readers))
	watchers := make([]*watchBarrierProjectionStore, 0, len(readers))
	ctx, cancel := context.WithCancel(context.Background())
	advanced := make(chan int, len(readers))
	done := make(chan struct{}, len(readers))
	for index, reader := range readers {
		t.Cleanup(func() { _ = reader.CloseDB() })
		if err := reader.OpenProjectionDeltaStore(); err != nil {
			t.Fatal(err)
		}
		watcher := &watchBarrierProjectionStore{
			projectionDeltaStore: reader,
			entered:              make(chan struct{}),
			release:              make(chan struct{}),
		}
		watchers = append(watchers, watcher)
		materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(watcher), nil)
		if err := materializer.bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
		materializers = append(materializers, materializer)
		go func() {
			defer func() { done <- struct{}{} }()
			materializer.run(ctx, func(protocol.MaterializedSnapshotMetadata) { advanced <- index })
		}()
	}
	t.Cleanup(func() {
		cancel()
		for range readers {
			<-done
		}
	})
	for _, watcher := range watchers {
		<-watcher.entered
	}
	if _, err := writer.Create(ctx, issues.CreateTaskParams{Title: "multi-daemon", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	for _, watcher := range watchers {
		close(watcher.release)
	}
	seen := map[int]bool{}
	for len(seen) < len(materializers) {
		seen[<-advanced] = true
	}
	firstTasks, firstSource := materializers[0].snapshot()
	secondTasks, secondSource := materializers[1].snapshot()
	if checksumJSON(firstTasks) != checksumJSON(secondTasks) || firstSource.SemanticChecksum != secondSource.SemanticChecksum {
		t.Fatalf("multi-daemon snapshots diverged: first=%+v second=%+v", firstSource, secondSource)
	}
}

func TestProjectReadMaterializerPublishesDeltaWhileRuntimeRefreshHydrationIsBlocked(t *testing.T) {
	client, _ := newTestIssueClient(t)
	ctx := context.Background()
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "serialized", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	var hydrateCalls atomic.Int32
	refreshEntered := make(chan struct{})
	releaseRefresh := make(chan struct{})
	hydrate := func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		if hydrateCalls.Add(1) == 2 {
			tasks[0].HasTmuxSession = true
			close(refreshEntered)
			<-releaseRefresh
		}
		return tasks, nil
	}
	authority := NewProjectionDeltaAuthority(client)
	materializer := newProjectReadMaterializer("project", authority, hydrate)
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	before := materializer.snapshotMetadata().DeliveryCursor
	if err := client.Update(ctx, issueID, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	batch, err := authority.List(ctx, protocol.DefaultProjectID, before, 20)
	if err != nil {
		t.Fatal(err)
	}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- materializer.refreshRuntime(ctx, []string{issueID}) }()
	<-refreshEntered
	applyDone := make(chan error, 1)
	go func() { applyDone <- materializer.apply(ctx, batch) }()
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
	tasks, _ := materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("snapshot during blocked runtime hydration = %+v, want latest delta status", tasks)
	}
	close(releaseRefresh)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	tasks, _ = materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Status != domain.StatusInProgress || tasks[0].HasTmuxSession {
		t.Fatalf("serialized snapshot = %+v, want latest delta status without superseded runtime overlay", tasks)
	}
}

func TestProjectReadMaterializerPublishesCommittedCanonicalBeforeRuntimeEnrichment(t *testing.T) {
	client, _ := newTestIssueClient(t)
	ctx := context.Background()
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "committed lifecycle", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	var hydrateCalls atomic.Int32
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(client), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		if hydrateCalls.Add(1) == 2 {
			close(hydrateEntered)
			<-releaseHydrate
		}
		return tasks, nil
	})
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	before := materializer.snapshotMetadata().DeliveryCursor
	if _, err := client.CloseWithRuntime(ctx, "project", issueID, domain.StatusDone); err != nil {
		t.Fatal(err)
	}
	batch, err := materializer.authority.List(ctx, protocol.DefaultProjectID, before, projectReadMaterializerBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	applyDone := make(chan error, 1)
	go func() { applyDone <- materializer.apply(ctx, batch) }()
	<-hydrateEntered

	tasks, metadata := materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Status != domain.StatusDone {
		t.Fatalf("snapshot during blocked enrichment = %+v, want committed closed lifecycle", tasks)
	}
	if metadata.DeliveryCursor != batch.DeliveryToCursor {
		t.Fatalf("delivery cursor during blocked enrichment = %d, want %d", metadata.DeliveryCursor, batch.DeliveryToCursor)
	}

	close(releaseHydrate)
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
}

func TestProjectReadMaterializerRunAnnouncesCanonicalAdvanceBeforeRuntimeEnrichment(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer, repoDir := newTestIssueClient(t)
	reader := issues.NewClient(repoDir, logger)
	t.Cleanup(func() { _ = reader.CloseDB() })
	if err := reader.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	issueID, err := writer.Create(ctx, issues.CreateTaskParams{Title: "cross daemon lifecycle", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	var hydrateCalls atomic.Int32
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(reader), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		if hydrateCalls.Add(1) == 2 {
			close(hydrateEntered)
			<-releaseHydrate
		}
		return tasks, nil
	})
	if err := materializer.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	advanced := make(chan protocol.MaterializedSnapshotMetadata, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		materializer.run(ctx, func(metadata protocol.MaterializedSnapshotMetadata) { advanced <- metadata })
	}()
	if _, err := writer.CloseWithRuntime(ctx, "project", issueID, domain.StatusDone); err != nil {
		t.Fatal(err)
	}
	<-hydrateEntered
	select {
	case metadata := <-advanced:
		if metadata.DeliveryCursor == 0 {
			t.Fatalf("announced metadata = %+v", metadata)
		}
	default:
		t.Fatal("canonical delta was not announced before runtime enrichment began")
	}
	tasks, _ := materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Status != domain.StatusDone {
		t.Fatalf("cross-daemon snapshot = %+v, want committed closed lifecycle", tasks)
	}
	close(releaseHydrate)
	cancel()
	<-done
}

func TestProjectReadMaterializerNewerRuntimeRefreshSupersedesBlockedOlderResult(t *testing.T) {
	const issueID = "az-refresh-contention"
	canonical := map[string]domain.Task{issueID: {ID: issueID, Title: "contended refresh", Type: domain.TypeTask}}
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	var hydrateCalls atomic.Int32
	materializer := newProjectReadMaterializer("project", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		if hydrateCalls.Add(1) == 1 {
			close(hydrateEntered)
			<-releaseHydrate
			tasks[0].Session = &domain.Session{Activity: "older"}
			return tasks, nil
		}
		tasks[0].Session = &domain.Session{Activity: "newer"}
		return tasks, nil
	})
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, canonical)
	materializer.replaceBootstrap(canonical, canonical, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)

	holderDone := make(chan error, 1)
	go func() {
		holderCtx := contextWithRuntimeProjectionWriterOperation(context.Background(), "background.projection_refresh")
		holderDone <- materializer.refreshRuntime(holderCtx, []string{issueID})
	}()
	<-hydrateEntered

	if err := materializer.refreshRuntime(context.Background(), []string{issueID}); err != nil {
		t.Fatalf("newer refresh: %v", err)
	}
	tasks, _ := materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Session == nil || tasks[0].Session.Activity != "newer" {
		t.Fatalf("snapshot after newer refresh = %+v, want newer runtime result", tasks)
	}

	close(releaseHydrate)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder refresh: %v", err)
	}
	tasks, _ = materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Session == nil || tasks[0].Session.Activity != "newer" {
		t.Fatalf("older blocked refresh overwrote newer result: %+v", tasks)
	}
}

func TestProjectReadMaterializerFailedNewerRefreshReleasesOwnershipToOlderResult(t *testing.T) {
	const issueID = "az-refresh-failed-owner"
	canonical := map[string]domain.Task{issueID: {ID: issueID, Title: "failed refresh owner", Type: domain.TypeTask}}
	olderEntered := make(chan struct{})
	releaseOlder := make(chan struct{})
	var hydrateCalls atomic.Int32
	materializer := newProjectReadMaterializer("project", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		if hydrateCalls.Add(1) == 1 {
			close(olderEntered)
			<-releaseOlder
			tasks[0].Session = &domain.Session{Activity: "older-completed"}
			return tasks, nil
		}
		return nil, errors.New("newer hydration failed")
	})
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, canonical)
	materializer.replaceBootstrap(canonical, canonical, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)

	olderDone := make(chan error, 1)
	go func() { olderDone <- materializer.refreshRuntime(context.Background(), []string{issueID}) }()
	<-olderEntered
	if err := materializer.refreshRuntime(context.Background(), []string{issueID}); err == nil || !strings.Contains(err.Error(), "newer hydration failed") {
		t.Fatalf("newer refresh error = %v, want hydration failure", err)
	}
	close(releaseOlder)
	if err := <-olderDone; err != nil {
		t.Fatalf("older refresh after newer owner failed: %v", err)
	}
	tasks, _ := materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Session == nil || tasks[0].Session.Activity != "older-completed" {
		t.Fatalf("snapshot after failed newer owner = %+v, want completed older result", tasks)
	}
}

func TestProjectReadMaterializerFailedOwnerChainReturnsToOldestLiveRefresh(t *testing.T) {
	const issueID = "az-refresh-failed-owner-chain"
	canonical := map[string]domain.Task{issueID: {ID: issueID, Title: "failed refresh owner chain", Type: domain.TypeTask}}
	entered := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	release := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var hydrateCalls atomic.Int32
	materializer := newProjectReadMaterializer("project", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		call := int(hydrateCalls.Add(1)) - 1
		close(entered[call])
		<-release[call]
		if call > 0 {
			return nil, fmt.Errorf("failed owner %d", call)
		}
		tasks[0].Session = &domain.Session{Activity: "oldest-live"}
		return tasks, nil
	})
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, canonical)
	materializer.replaceBootstrap(canonical, canonical, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)

	done := []chan error{make(chan error, 1), make(chan error, 1), make(chan error, 1)}
	for i := range done {
		go func(index int) { done[index] <- materializer.refreshRuntime(context.Background(), []string{issueID}) }(i)
		<-entered[i]
	}
	close(release[1])
	if err := <-done[1]; err == nil || !strings.Contains(err.Error(), "failed owner 1") {
		t.Fatalf("middle owner error = %v", err)
	}
	close(release[2])
	if err := <-done[2]; err == nil || !strings.Contains(err.Error(), "failed owner 2") {
		t.Fatalf("newest owner error = %v", err)
	}
	close(release[0])
	if err := <-done[0]; err != nil {
		t.Fatalf("oldest live owner completion: %v", err)
	}
	tasks, _ := materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Session == nil || tasks[0].Session.Activity != "oldest-live" {
		t.Fatalf("snapshot after failed owner chain = %+v, want oldest live result", tasks)
	}
}

func TestProjectReadMaterializerLiveNewerOwnerDoesNotMakeOlderCompletionUnhealthy(t *testing.T) {
	const issueID = "az-refresh-live-owner"
	canonical := map[string]domain.Task{issueID: {ID: issueID, Title: "live refresh owner", Type: domain.TypeTask}}
	olderEntered := make(chan struct{})
	newerEntered := make(chan struct{})
	releaseOlder := make(chan struct{})
	releaseNewer := make(chan struct{})
	var hydrateCalls atomic.Int32
	materializer := newProjectReadMaterializer("project", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		switch hydrateCalls.Add(1) {
		case 1:
			close(olderEntered)
			<-releaseOlder
			tasks[0].Session = &domain.Session{Activity: "older"}
		case 2:
			close(newerEntered)
			<-releaseNewer
			tasks[0].Session = &domain.Session{Activity: "newer"}
		}
		return tasks, nil
	})
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, canonical)
	materializer.replaceBootstrap(canonical, canonical, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)

	olderDone := make(chan error, 1)
	newerDone := make(chan error, 1)
	go func() { olderDone <- materializer.refreshRuntime(context.Background(), []string{issueID}) }()
	<-olderEntered
	go func() { newerDone <- materializer.refreshRuntime(context.Background(), []string{issueID}) }()
	<-newerEntered
	close(releaseOlder)
	if err := <-olderDone; err != nil {
		t.Fatalf("older completion while newer owner is live: %v", err)
	}
	close(releaseNewer)
	if err := <-newerDone; err != nil {
		t.Fatalf("newer owner completion: %v", err)
	}
	tasks, _ := materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Session == nil || tasks[0].Session.Activity != "newer" {
		t.Fatalf("snapshot after live newer owner = %+v, want newer result", tasks)
	}
}

func TestProjectReadMaterializerPerIssueOwnerConvergesWithOlderBulkRefresh(t *testing.T) {
	const (
		firstID  = "az-refresh-bulk-first"
		secondID = "az-refresh-bulk-second"
	)
	canonical := map[string]domain.Task{
		firstID:  {ID: firstID, Title: "bulk first", Type: domain.TypeTask},
		secondID: {ID: secondID, Title: "bulk second", Type: domain.TypeTask},
	}
	bulkEntered := make(chan struct{})
	issueEntered := make(chan struct{})
	releaseBulk := make(chan struct{})
	releaseIssue := make(chan struct{})
	var hydrateCalls atomic.Int32
	materializer := newProjectReadMaterializer("project", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		switch hydrateCalls.Add(1) {
		case 1:
			close(bulkEntered)
			<-releaseBulk
			for i := range tasks {
				tasks[i].Session = &domain.Session{Activity: "bulk"}
			}
		case 2:
			close(issueEntered)
			<-releaseIssue
			tasks[0].Session = &domain.Session{Activity: "per-issue"}
		}
		return tasks, nil
	})
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, canonical)
	materializer.replaceBootstrap(canonical, canonical, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)

	bulkDone := make(chan error, 1)
	issueDone := make(chan error, 1)
	go func() { bulkDone <- materializer.refreshRuntime(context.Background(), []string{firstID, secondID}) }()
	<-bulkEntered
	go func() { issueDone <- materializer.refreshRuntime(context.Background(), []string{firstID}) }()
	<-issueEntered
	close(releaseBulk)
	if err := <-bulkDone; err != nil {
		t.Fatalf("bulk refresh while per-issue owner is live: %v", err)
	}
	close(releaseIssue)
	if err := <-issueDone; err != nil {
		t.Fatalf("per-issue refresh: %v", err)
	}
	tasks, _ := materializer.snapshot()
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	if byID[firstID].Session == nil || byID[firstID].Session.Activity != "per-issue" {
		t.Fatalf("first task runtime = %+v, want per-issue result", byID[firstID])
	}
	if byID[secondID].Session == nil || byID[secondID].Session.Activity != "bulk" {
		t.Fatalf("second task runtime = %+v, want bulk result", byID[secondID])
	}
}

func TestProjectReadMaterializerDeleteRetiresBlockedRuntimeOwner(t *testing.T) {
	const issueID = "az-refresh-deleted-owner"
	canonical := map[string]domain.Task{issueID: {ID: issueID, Title: "deleted refresh owner", Type: domain.TypeTask}}
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	materializer := newProjectReadMaterializer("project", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		close(hydrateEntered)
		<-releaseHydrate
		return tasks, nil
	})
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonical, canonical)
	materializer.replaceBootstrap(canonical, canonical, materializedMetadata(0, 0, issueProjectionProjector(), nil, issueKeys.sum(), runtimeKeys.sum(), "healthy"), issueKeys, runtimeKeys)

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- materializer.refreshRuntime(context.Background(), []string{issueID}) }()
	<-hydrateEntered
	batch := protocol.ProjectionDeltaBatch{
		SchemaVersion: protocol.ProjectionDeltaSchemaVersion, ProjectID: "project", AfterCursor: 0, HeadCursor: 1, DeliveryToCursor: 1,
		DeliveryContract: protocol.ProjectionDeliveryContract, DeliveryCursorTransitional: true, Projector: issueProjectionProjector(), Health: "healthy",
		Deltas: []protocol.ProjectionDelta{{Cursor: 1, Kind: protocol.ProjectionKind(domain.ProjectionKindIssue), Key: issueID, Operation: protocol.ProjectionDeltaDelete}},
	}
	protocol.FinalizeProjectionDeltaBatch(&batch)
	if _, err := materializer.applyCanonical(context.Background(), batch); err != nil {
		t.Fatalf("delete canonical issue: %v", err)
	}
	close(releaseHydrate)
	if err := <-refreshDone; err != nil {
		t.Fatalf("blocked refresh after delete: %v", err)
	}
	materializer.mu.RLock()
	_, hasRefreshEpoch := materializer.runtimeRefreshEpoch[issueID]
	_, hasPublishedEpoch := materializer.runtimePublishedEpoch[issueID]
	_, hasOwners := materializer.runtimeRefreshOwners[issueID]
	materializer.mu.RUnlock()
	if hasRefreshEpoch || hasPublishedEpoch || hasOwners {
		t.Fatalf("deleted issue retained runtime ownership: refresh=%t published=%t owners=%t", hasRefreshEpoch, hasPublishedEpoch, hasOwners)
	}
}

func TestProjectReadSnapshotIsPureAtDeclaredChecksum(t *testing.T) {
	client, repoDir := newTestIssueClient(t)
	if _, err := client.Create(context.Background(), issues.CreateTaskParams{Title: "pure", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(client), nil)
	if err := materializer.bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{materializers: map[string]*projectReadMaterializer{"project": materializer}, revision: map[string]uint64{}}
	db, err := sql.Open("sqlite", filepath.Join(repoDir, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var before int64
	if err := db.QueryRow(`PRAGMA data_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	tasksA, sourceA, err := d.projectReadSnapshot("project")
	if err != nil {
		t.Fatal(err)
	}
	tasksB, sourceB, err := d.projectReadSnapshot("project")
	if err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := db.QueryRow(`PRAGMA data_version`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after || checksumJSON(tasksA) != checksumJSON(tasksB) || sourceA.SemanticChecksum != sourceB.SemanticChecksum {
		t.Fatalf("pure read changed source: data_version %d->%d source %s/%s", before, after, sourceA.SemanticChecksum, sourceB.SemanticChecksum)
	}
}

func TestProjectReadMaterializerClearsOnlyRecoveredMutationConvergenceFailure(t *testing.T) {
	materializer := newProjectReadMaterializer("project", nil, nil)
	materializer.metadata = protocol.MaterializedSnapshotMetadata{Health: "healthy"}
	attempt := materializer.beginMutationConvergence(1)
	materializer.finishMutationConvergence(1, attempt, errors.New("transient read failure"))
	recoveryAttempt := materializer.beginMutationConvergence(2)
	materializer.finishMutationConvergence(2, recoveryAttempt, nil)
	if got := materializer.snapshotMetadata().Health; got != "healthy" {
		t.Fatalf("recovered mutation convergence health = %q, want healthy", got)
	}
	materializer.markUnhealthy(errors.New("watch projection deltas: corrupt batch"), false)
	unrelatedAttempt := materializer.beginMutationConvergence(3)
	materializer.finishMutationConvergence(3, unrelatedAttempt, nil)
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, "corrupt batch") {
		t.Fatalf("unrelated health failure was cleared: %q", got)
	}
}

func TestProjectReadMaterializerOlderConvergenceSuccessCannotClearNewerFailure(t *testing.T) {
	olderEntered := make(chan struct{})
	releaseOlder := make(chan struct{})
	var calls atomic.Int32
	store := listErrorProjectionStore{list: func(ctx context.Context, _ string, _ uint64, _ int) ([]domain.ProjectionDelta, uint64, error) {
		if calls.Add(1) == 1 {
			close(olderEntered)
			select {
			case <-releaseOlder:
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
			return nil, 0, nil
		}
		return nil, 0, errors.New("newer failure")
	}}
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(store), nil)
	materializer.metadata = protocol.MaterializedSnapshotMetadata{Health: "healthy"}
	olderDone := make(chan error, 1)
	go func() {
		_, _, err := materializer.convergeMutation(context.Background(), 1)
		olderDone <- err
	}()
	<-olderEntered
	type convergenceResult struct {
		outcome string
		err     error
	}
	newerQueued := make(chan struct{})
	var newerQueuedOnce sync.Once
	newerCtx := withProjectReadCanonicalQueuedHookForTest(context.Background(), func(string) { newerQueuedOnce.Do(func() { close(newerQueued) }) })
	newerDone := make(chan convergenceResult, 1)
	go func() {
		_, outcome, err := materializer.convergeMutation(newerCtx, 2)
		newerDone <- convergenceResult{outcome: outcome, err: err}
	}()
	<-newerQueued
	close(releaseOlder)
	if err := <-olderDone; err != nil {
		t.Fatal(err)
	}
	newer := <-newerDone
	if newer.err == nil || newer.outcome != "unavailable" {
		t.Fatalf("newer convergence = outcome:%q error:%v, want unavailable failure", newer.outcome, newer.err)
	}
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, "newer failure") {
		t.Fatalf("older success cleared newer failure: %q", got)
	}
}

func TestProjectReadMaterializerBootstrapCannotOverwriteNewerCanonicalConvergence(t *testing.T) {
	client, _ := newTestIssueClient(t)
	ctx := context.Background()
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "bootstrap convergence fence", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHydrate) }) }
	t.Cleanup(release)
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(client), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		close(hydrateEntered)
		<-releaseHydrate
		return tasks, nil
	})
	bootstrapDone := make(chan error, 1)
	go func() { bootstrapDone <- materializer.bootstrap(ctx) }()
	<-hydrateEntered
	if _, err := client.CloseWithRuntime(ctx, "project", issueID, domain.StatusDone); err != nil {
		t.Fatal(err)
	}
	queued := make(chan struct{})
	var queuedOnce sync.Once
	convergeCtx := withProjectReadCanonicalQueuedHookForTest(ctx, func(string) { queuedOnce.Do(func() { close(queued) }) })
	convergeDone := make(chan error, 1)
	go func() {
		_, convergeErr := materializer.convergeCanonical(convergeCtx)
		convergeDone <- convergeErr
	}()
	select {
	case <-queued:
	case convergeErr := <-convergeDone:
		release()
		<-bootstrapDone
		t.Fatalf("canonical convergence bypassed bootstrap serialization: %v", convergeErr)
	}
	release()
	if err := <-bootstrapDone; err != nil {
		t.Fatal(err)
	}
	if err := <-convergeDone; err != nil {
		t.Fatal(err)
	}
	tasks, metadata := materializer.snapshot()
	if metadata.DeliveryCursor == 0 || len(tasks) != 1 || tasks[0].Status != domain.StatusDone {
		t.Fatalf("snapshot after bootstrap/convergence = metadata:%+v tasks:%+v, want newest closed lifecycle", metadata, tasks)
	}
}

func TestProjectReadMaterializerBootstrapCannotClearNewerConvergenceFailure(t *testing.T) {
	client, _ := newTestIssueClient(t)
	ctx := context.Background()
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "bootstrap failure fence", Type: domain.TypeTask, Status: domain.StatusInReview})
	if err != nil {
		t.Fatal(err)
	}
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHydrate) }) }
	t.Cleanup(release)
	convergenceListEntered := make(chan struct{})
	var failConvergence atomic.Bool
	var convergenceListOnce sync.Once
	store := listErrorProjectionStore{
		projectionDeltaStore: client,
		list: func(ctx context.Context, projectID string, after uint64, limit int) ([]domain.ProjectionDelta, uint64, error) {
			if failConvergence.Load() {
				convergenceListOnce.Do(func() { close(convergenceListEntered) })
				return nil, 0, errors.New("newer convergence failure")
			}
			return client.ListProjectionDeltas(ctx, projectID, after, limit)
		},
	}
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(store), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		close(hydrateEntered)
		<-releaseHydrate
		return tasks, nil
	})
	bootstrapDone := make(chan error, 1)
	go func() { bootstrapDone <- materializer.bootstrap(ctx) }()
	<-hydrateEntered
	if _, err := client.CloseWithRuntime(ctx, "project", issueID, domain.StatusDone); err != nil {
		t.Fatal(err)
	}
	failConvergence.Store(true)
	queued := make(chan struct{})
	var queuedOnce sync.Once
	convergeCtx := withProjectReadCanonicalQueuedHookForTest(ctx, func(string) { queuedOnce.Do(func() { close(queued) }) })
	convergeDone := make(chan error, 1)
	go func() {
		_, _, convergeErr := materializer.convergeMutation(convergeCtx, 2)
		convergeDone <- convergeErr
	}()
	<-queued
	select {
	case <-convergenceListEntered:
		t.Fatal("convergence read bypassed bootstrap canonical admission")
	default:
	}
	release()
	if err := <-bootstrapDone; err != nil {
		t.Fatal(err)
	}
	if err := <-convergeDone; err == nil || !strings.Contains(err.Error(), "newer convergence failure") {
		t.Fatalf("convergence error = %v, want injected newer failure", err)
	}
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, "newer convergence failure") {
		t.Fatalf("bootstrap cleared newer convergence failure: %q", got)
	}
}

func TestProjectReadMaterializerBootstrapCannotClearCanceledQueuedConvergence(t *testing.T) {
	client, _ := newTestIssueClient(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, issues.CreateTaskParams{Title: "bootstrap cancellation fence", Type: domain.TypeTask, Status: domain.StatusInReview}); err != nil {
		t.Fatal(err)
	}
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHydrate) }) }
	t.Cleanup(release)
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(client), func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		close(hydrateEntered)
		<-releaseHydrate
		return tasks, nil
	})
	bootstrapDone := make(chan error, 1)
	go func() { bootstrapDone <- materializer.bootstrap(ctx) }()
	<-hydrateEntered
	queued := make(chan struct{})
	var queuedOnce sync.Once
	convergeCtx, cancelConvergence := context.WithCancel(withProjectReadCanonicalQueuedHookForTest(ctx, func(string) { queuedOnce.Do(func() { close(queued) }) }))
	convergeDone := make(chan error, 1)
	go func() {
		_, _, convergeErr := materializer.convergeMutation(convergeCtx, 2)
		convergeDone <- convergeErr
	}()
	<-queued
	cancelConvergence()
	if err := <-convergeDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued convergence error = %v, want context canceled", err)
	}
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, context.Canceled.Error()) {
		t.Fatalf("queued convergence did not publish unavailable health: %q", got)
	}
	release()
	if err := <-bootstrapDone; err != nil {
		t.Fatal(err)
	}
	if got := materializer.snapshotMetadata().Health; !strings.Contains(got, context.Canceled.Error()) {
		t.Fatalf("bootstrap cleared canceled queued convergence: %q", got)
	}
}

func TestSyncUserProjectionMaterializedIssuesPropagatesBoundedCurrentState(t *testing.T) {
	ctx := context.Background()
	store, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	canonical := domain.Task{ID: "issue", Title: "canonical", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}
	state := userstore.ProjectDeltaState{ProjectID: "p", Cursor: 4, Hash: "four", Initialized: true, Projector: issueProjectionProjector()}
	if err := store.ReplaceProject(ctx, userstore.ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{canonical}, Delta: &state}); err != nil {
		t.Fatal(err)
	}
	materialized := canonical
	materialized.Session = &domain.Session{IssueID: "issue", State: domain.SessionBusy, Activity: "working", UpdatedAt: now.Add(time.Second)}
	materialized.HasTmuxSession = true
	reader := newProjectReadMaterializer("p", nil, nil)
	canonicalByID := map[string]domain.Task{canonical.ID.String(): canonical}
	materializedByID := map[string]domain.Task{canonical.ID.String(): materialized}
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonicalByID, materializedByID)
	reader.replaceBootstrap(canonicalByID, materializedByID, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)
	d := &Daemon{cfg: Config{}, userStore: store, materializers: map[string]*projectReadMaterializer{"p": reader}}
	if err := d.syncUserProjectionMaterializedIssues(ctx, "p", []string{"issue"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].DeltaCursor != 4 || len(snapshot.Projects[0].Tasks) != 1 || snapshot.Projects[0].Tasks[0].Session == nil || !snapshot.Projects[0].Tasks[0].HasTmuxSession {
		t.Fatalf("bounded current-state propagation = %+v", snapshot.Projects)
	}
}

func TestRefreshActiveProjectReadRuntimeRecoversAuthoritativeHealthBeforeUserProjectionSync(t *testing.T) {
	ctx := context.Background()
	store, err := userstore.Open(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	canonical := domain.Task{ID: "issue", Title: "canonical", Status: domain.StatusInProgress, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now}
	state := userstore.ProjectDeltaState{ProjectID: "p", Cursor: 4, Hash: "four", Initialized: true, Projector: issueProjectionProjector()}
	if err := store.ReplaceProject(ctx, userstore.ProjectInput{ProjectID: "p", Name: "P", Path: "/p", DBPath: "/p/db", Tasks: []domain.Task{canonical}, Delta: &state}); err != nil {
		t.Fatal(err)
	}

	var hydrateCalls atomic.Int32
	reader := newProjectReadMaterializer("p", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		if hydrateCalls.Add(1) == 1 {
			return nil, errors.New("transient runtime hydration failure")
		}
		tasks[0].Session = &domain.Session{IssueID: "issue", State: domain.SessionBusy, Activity: "recovered", UpdatedAt: now.Add(time.Second)}
		tasks[0].HasTmuxSession = true
		return tasks, nil
	})
	canonicalByID := map[string]domain.Task{canonical.ID.String(): canonical}
	issueKeys, runtimeKeys := checkpointMaterializedTasks(canonicalByID, canonicalByID)
	reader.replaceBootstrap(canonicalByID, canonicalByID, protocol.MaterializedSnapshotMetadata{Health: "healthy"}, issueKeys, runtimeKeys)
	d := &Daemon{cfg: Config{}, userStore: store, materializers: map[string]*projectReadMaterializer{"p": reader}}

	if err := d.refreshActiveProjectReadRuntimeForIssues(ctx, "p", reader, []string{"issue"}); err == nil || !strings.Contains(err.Error(), "transient runtime hydration failure") {
		t.Fatalf("failed refresh error = %v", err)
	}
	if got := reader.snapshotMetadata().Health; !strings.Contains(got, "stale: authoritative runtime refresh:") {
		t.Fatalf("failed refresh health = %q, want authoritative stale health", got)
	}
	if err := d.refreshActiveProjectReadRuntimeForIssues(ctx, "p", reader, []string{"issue"}); err != nil {
		t.Fatalf("recovered refresh: %v", err)
	}
	if got := reader.snapshotMetadata().Health; got != "healthy" {
		t.Fatalf("recovered refresh health = %q, want healthy", got)
	}
	snapshot, err := store.Snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 1 || len(snapshot.Projects[0].Tasks) != 1 || snapshot.Projects[0].Tasks[0].Session == nil || snapshot.Projects[0].Tasks[0].Session.Activity != "recovered" {
		t.Fatalf("recovered user projection = %+v", snapshot.Projects)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshActiveProjectReadRuntimeForIssues(ctx, "p", reader, []string{"issue"}); err == nil || !strings.Contains(err.Error(), "sync user projection issues") {
		t.Fatalf("failed user projection sync error = %v", err)
	}
	if got := reader.snapshotMetadata().Health; !strings.Contains(got, "stale: authoritative runtime refresh:") {
		t.Fatalf("failed user projection sync health = %q, want authoritative stale health", got)
	}
	reader.markUnhealthy(errors.New("structural projection failure"), false)
	if err := d.refreshActiveProjectReadRuntimeForIssues(ctx, "p", reader, []string{"issue"}); err == nil || !strings.Contains(err.Error(), "structural projection failure") {
		t.Fatalf("unrelated unhealthy refresh error = %v", err)
	}
	if got := reader.snapshotMetadata().Health; !strings.Contains(got, "structural projection failure") {
		t.Fatalf("unrelated health was cleared: %q", got)
	}
	d.materializers = map[string]*projectReadMaterializer{}
	if err := d.refreshActiveProjectReadRuntimeForIssues(ctx, "p", reader, []string{"issue"}); err == nil || !strings.Contains(err.Error(), "no longer active") {
		t.Fatalf("removed materializer refresh error = %v", err)
	}
}

func TestStopAllProjectReadMaterializersCancelsAndJoinsConsumers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(done)
	}()
	d := &Daemon{materializers: map[string]*projectReadMaterializer{"p": {projectID: "p", cancel: cancel, done: done}}}
	d.stopAllProjectReadMaterializers(context.Background())
	select {
	case <-done:
	default:
		t.Fatal("materializer shutdown did not join canceled consumer")
	}
	if d.activeProjectReadMaterializer("p") != nil {
		t.Fatal("materializer survived shutdown")
	}
}

func TestMaterializedTaskContextMatchesDirectStoreExpansion(t *testing.T) {
	tasks := []domain.Task{
		{ID: "root", Status: domain.StatusOpen},
		{ID: "requested", Status: domain.StatusOpen, ParentID: issueIDPointer("root"), Dependencies: []domain.Dependency{{ID: "dependency", Type: domain.DependencyBlocks}}},
		{ID: "dependency", Status: domain.StatusOpen, ParentID: issueIDPointer("unrelated-parent")},
		{ID: "direct-dependent", Status: domain.StatusOpen, Dependencies: []domain.Dependency{{ID: "requested", Type: domain.DependencyBlocks}}},
		{ID: "direct-child", Status: domain.StatusOpen, ParentID: issueIDPointer("requested")},
		{ID: "transitive-dependent", Status: domain.StatusOpen, Dependencies: []domain.Dependency{{ID: "dependency", Type: domain.DependencyBlocks}}},
		{ID: "unrelated-parent", Status: domain.StatusOpen},
	}
	got := materializedTaskContext(tasks, []string{"requested"}, true, true, true, false, protocol.ArchiveModeExclude)
	ids := taskIDsFromTasks(got)
	sort.Strings(ids)
	want := []string{"dependency", "direct-child", "direct-dependent", "requested", "root"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("context IDs = %v, want direct request context %v", ids, want)
	}
	metadata := materializedTaskContext(tasks, []string{"requested"}, false, true, false, false, protocol.ArchiveModeExclude)
	metadataIDs := taskIDsFromTasks(metadata)
	sort.Strings(metadataIDs)
	if want := []string{"requested", "root"}; !reflect.DeepEqual(metadataIDs, want) {
		t.Fatalf("metadata context IDs = %v, want %v", metadataIDs, want)
	}
}

func issueIDPointer(value string) *naming.IssueID {
	id := naming.IssueID(value)
	return &id
}
