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
	materializer.markUnhealthy(errors.New("structural projection corruption"))
	served, servedMeta, err = d.projectReadSnapshot("project")
	if err == nil || served != nil || !strings.Contains(servedMeta.Health, "structural projection corruption") {
		t.Fatalf("non-retryable failure did not fail closed: tasks=%+v metadata=%+v err=%v", served, servedMeta, err)
	}
	cancel()
	<-done
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
	tasks, _ := materializer.snapshot()
	got, ok := findDaemonTaskByID(tasks, id)
	if !ok || got.Title != "ticket survives degraded runtime" {
		t.Fatalf("degraded ticket = %+v found=%t", got, ok)
	}
	assertProjectReadSpans(t, recorder, "daemon.project_read.runtime_hydration", "daemon.project_read.degraded_fallback")
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
	watchStore := &watchBarrierProjectionStore{
		projectionDeltaStore: reader,
		entered:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(watchStore), nil)
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
	<-watchStore.entered
	select {
	case <-advanced:
		t.Fatal("watch returned without source advancement")
	default:
	}
	close(watchStore.release)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	advanced := make(chan int, len(readers))
	for index, reader := range readers {
		t.Cleanup(func() { _ = reader.CloseDB() })
		if err := reader.OpenProjectionDeltaStore(); err != nil {
			t.Fatal(err)
		}
		materializer := newProjectReadMaterializer("project", NewProjectionDeltaAuthority(reader), nil)
		if err := materializer.bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
		materializers = append(materializers, materializer)
		go materializer.run(ctx, func(protocol.MaterializedSnapshotMetadata) { advanced <- index })
	}
	if _, err := writer.Create(ctx, issues.CreateTaskParams{Title: "multi-daemon", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for len(seen) < len(materializers) {
		select {
		case index := <-advanced:
			seen[index] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("materializers did not converge: woke=%v", seen)
		}
	}
	firstTasks, firstSource := materializers[0].snapshot()
	secondTasks, secondSource := materializers[1].snapshot()
	if checksumJSON(firstTasks) != checksumJSON(secondTasks) || firstSource.SemanticChecksum != secondSource.SemanticChecksum {
		t.Fatalf("multi-daemon snapshots diverged: first=%+v second=%+v", firstSource, secondSource)
	}
}

func TestProjectReadMaterializerSerializesRuntimeRefreshWithDeltaApply(t *testing.T) {
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
	select {
	case err := <-applyDone:
		t.Fatalf("delta apply bypassed blocked refresh serialization: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseRefresh)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
	tasks, _ := materializer.snapshot()
	if len(tasks) != 1 || tasks[0].Status != domain.StatusInProgress {
		t.Fatalf("serialized snapshot = %+v, want latest delta status", tasks)
	}
}

func TestProjectReadMaterializerRuntimeRefreshWaitIsCancelableAndAttributed(t *testing.T) {
	const issueID = "az-refresh-contention"
	canonical := map[string]domain.Task{issueID: {ID: issueID, Title: "contended refresh", Type: domain.TypeTask}}
	hydrateEntered := make(chan struct{})
	releaseHydrate := make(chan struct{})
	materializer := newProjectReadMaterializer("project", nil, func(_ context.Context, tasks []domain.Task) ([]domain.Task, error) {
		close(hydrateEntered)
		<-releaseHydrate
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

	waitObserved := make(chan struct{})
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitCtx = contextWithRuntimeProjectionWriterOperation(waitCtx, "orchestration.snapshot")
	waitCtx = withProjectReadUpdateQueuedHookForTest(waitCtx, func(waiterOperation string) {
		if waiterOperation != "orchestration.snapshot" {
			t.Errorf("refresh queued waiter=%q", waiterOperation)
		}
		close(waitObserved)
	})
	waitDone := make(chan error, 1)
	go func() { waitDone <- materializer.refreshRuntime(waitCtx, []string{issueID}) }()
	<-waitObserved
	cancelWait()
	if err := <-waitDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh wait error = %v, want context.Canceled", err)
	}

	close(releaseHydrate)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder refresh: %v", err)
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
