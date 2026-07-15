package cli

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestSleepWatchPollReturnsWhenWatchContextCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepWatchPoll(ctx, time.Hour)
	if err == nil {
		t.Fatal("sleepWatchPoll error = nil, want context cancellation")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("sleepWatchPoll elapsed = %v, want prompt cancellation", elapsed)
	}
}

func TestWatchSegmentContextStartsLinkedTraceRoot(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	parentCtx, parent := otel.Tracer("test").Start(context.Background(), "command")
	segmentCtx, end := newWatchTraceSegment(context.Background(), parentCtx, "orchestrate")
	segment := oteltrace.SpanFromContext(segmentCtx)
	if segment.SpanContext().TraceID() == parent.SpanContext().TraceID() {
		t.Fatal("watch segment reused command trace; want bounded trace root")
	}
	end(nil)
	parent.End()

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	var segmentLinks []sdktrace.Link
	for _, span := range spans {
		if span.Name() == "cli.watch_segment" {
			segmentLinks = span.Links()
		}
	}
	if len(segmentLinks) != 1 || segmentLinks[0].SpanContext.TraceID() != parent.SpanContext().TraceID() {
		t.Fatalf("segment links = %+v, want command trace %s", segmentLinks, parent.SpanContext().TraceID())
	}
}
