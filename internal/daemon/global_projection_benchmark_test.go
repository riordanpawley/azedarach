package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/userstore"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const productionProjectionFixtureTasks = 4610

// BenchmarkProductionSizedGlobalProjectionRefresh records the bridge metrics
// requested by dex against the observed Chefy-sized projection. Run with
// -benchtime=1x so each before/after scenario uses one fixed sample window.
func BenchmarkProductionSizedGlobalProjectionRefresh(b *testing.B) {
	tasks := make([]domain.Task, 0, productionProjectionFixtureTasks)
	now := time.Now().UTC()
	for i := 0; i < productionProjectionFixtureTasks; i++ {
		tasks = append(tasks, domain.Task{
			ID:        naming.IssueID(fmt.Sprintf("fixture-%05d", i)),
			Title:     fmt.Sprintf("Production-sized fixture task %d", i),
			Status:    domain.StatusOpen,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	for _, scenario := range []struct {
		name    string
		bounded bool
	}{
		{name: "before_immediate_dirty_loop", bounded: false},
		{name: "after_10s_project_bound", bounded: true},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				result := runGlobalProjectionRefreshBenchmark(b, tasks, scenario.bounded)
				b.ReportMetric(float64(result.refreshCount), "refreshes")
				b.ReportMetric(float64(result.lockFailures), "lock_failures")
				b.ReportMetric(durationPercentile(result.snapshotLatency, 50), "global_snapshot_p50_ms")
				b.ReportMetric(durationPercentile(result.snapshotLatency, 95), "global_snapshot_p95_ms")
				b.ReportMetric(durationPercentile(result.daemonLatency, 95), "board_view_list_p95_ms")
				b.ReportMetric(durationPercentile(result.runtimeReconcileLatency, 95), "runtime_reconcile_p95_ms")
				if scenario.bounded && result.refreshCount < 2 {
					b.Fatalf("bounded refresh count = %d, want dirty replay beyond cooldown", result.refreshCount)
				}
			}
		})
	}
}

type globalProjectionBenchmarkResult struct {
	refreshCount            int64
	lockFailures            int64
	snapshotLatency         []time.Duration
	daemonLatency           []time.Duration
	runtimeReconcileLatency []time.Duration
}

func runGlobalProjectionRefreshBenchmark(b *testing.B, tasks []domain.Task, bounded bool) globalProjectionBenchmarkResult {
	b.Helper()
	store, err := userstore.Open(b.TempDir() + "/user.db")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	input := userstore.ProjectInput{ProjectID: "production-fixture", Name: "Production fixture", Path: "/fixture", DBPath: "/fixture/.azedarach/azedarach.db", Tasks: tasks}
	if err = store.ReplaceProject(context.Background(), input); err != nil {
		b.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var refreshCount atomic.Int64
	var lockFailures atomic.Int64
	refresh := func(refreshCtx context.Context, _ string) error {
		refreshCount.Add(1)
		err := store.ReplaceProject(refreshCtx, input)
		if isProjectionBenchmarkLockError(err) {
			lockFailures.Add(1)
		}
		return err
	}

	var refreshWG sync.WaitGroup
	d := &Daemon{userStore: store}
	if bounded {
		d.userStoreRefreshPending = map[string]bool{}
		d.userStoreRefreshDirty = map[string]bool{}
		d.userStoreRefreshActive = map[string]bool{}
		d.userStoreRefreshInterval = userProjectionMutationRefreshInterval
		d.userStoreRefreshProjectFn = refresh
		d.enqueueUserProjectionRefresh("production-fixture")
	} else {
		refreshWG.Add(1)
		go func() {
			defer refreshWG.Done()
			for ctx.Err() == nil {
				_ = refresh(ctx, "production-fixture")
			}
		}()
	}

	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if bounded {
					d.enqueueUserProjectionRefresh("production-fixture")
				}
			}
		}
	}()

	result := globalProjectionBenchmarkResult{}
	boardBody, err := json.Marshal(protocol.BoardViewListRequestBody{ProjectID: "global"})
	if err != nil {
		b.Fatal(err)
	}
	runtimeBody, err := json.Marshal(runtimeReconcileCommandBody{ProjectID: "production-fixture"})
	if err != nil {
		b.Fatal(err)
	}
	for ctx.Err() == nil {
		started := time.Now()
		snapshotResp, snapshotErr := d.handleGlobalSnapshot(ctx, protocol.RequestEnvelope{})
		snapshotErr = globalProjectionBenchmarkResponseError("global snapshot", snapshotResp, snapshotErr)
		result.snapshotLatency = append(result.snapshotLatency, time.Since(started))
		if isProjectionBenchmarkLockError(snapshotErr) {
			lockFailures.Add(1)
		}
		started = time.Now()
		boardResp, daemonErr := d.handleBoardViewList(ctx, protocol.RequestEnvelope{Body: boardBody})
		daemonErr = globalProjectionBenchmarkResponseError("board view list", boardResp, daemonErr)
		result.daemonLatency = append(result.daemonLatency, time.Since(started))
		if isProjectionBenchmarkLockError(daemonErr) {
			lockFailures.Add(1)
		}
		started = time.Now()
		runtimeResp, runtimeErr := d.handleRuntimeReconcile(ctx, protocol.RequestEnvelope{Body: runtimeBody})
		runtimeErr = globalProjectionBenchmarkResponseError("runtime reconcile", runtimeResp, runtimeErr)
		result.runtimeReconcileLatency = append(result.runtimeReconcileLatency, time.Since(started))
		if isProjectionBenchmarkLockError(runtimeErr) {
			lockFailures.Add(1)
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	<-producerDone
	if bounded {
		d.stopUserProjectionWorkers()
	}
	if d.runtimeReconcileQueue != nil {
		if err = d.runtimeReconcileQueue.Close(); err != nil {
			b.Fatal(err)
		}
	}
	refreshWG.Wait()
	result.refreshCount = refreshCount.Load()
	result.lockFailures = lockFailures.Load()
	return result
}

func globalProjectionBenchmarkResponseError(label string, resp protocol.ResponseEnvelope, err error) error {
	if err != nil || resp.OK {
		return err
	}
	if resp.Error == nil {
		return fmt.Errorf("%s failed without response error", label)
	}
	return fmt.Errorf("%s: %s", label, resp.Error.Message)
}

func isProjectionBenchmarkLockError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "locked") || strings.Contains(message, "busy")
}

func durationPercentile(samples []time.Duration, percentile int) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (len(sorted)*percentile + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return float64(sorted[index-1]) / float64(time.Millisecond)
}
