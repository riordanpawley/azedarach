package daemon

import (
	"context"
	"testing"
	"time"
)

func BenchmarkProjectReadMaterializerKeyedApplyProductionSized(b *testing.B) {
	const taskCount = 5000
	materializer, target, _, hydrated := productionMaterializerDelta(b, taskCount, 1)
	hydrated.Store(0)
	b.ReportAllocs()
	b.ReportMetric(taskCount, "project_tasks")
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if i%2 == 0 {
			target.Status = "in_progress"
		} else {
			target.Status = "open"
		}
		target.UpdatedAt = target.CreatedAt.Add(time.Duration(i+1) * time.Second)
		batch := productionMaterializerBatch(b, target, uint64(i+1))
		b.StartTimer()
		if err := materializer.apply(context.Background(), batch); err != nil {
			b.Fatal(err)
		}
	}
	if got := hydrated.Load(); got != int64(b.N) {
		b.Fatalf("hydrated task count = %d, want %d", got, b.N)
	}
	b.ReportMetric(1, "hydrated_tasks/op")
}
