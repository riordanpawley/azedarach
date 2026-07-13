package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

var (
	currentParentPID        = os.Getppid
	watchParentPollInterval = 250 * time.Millisecond
)

func newWatchCommandContext(commandName string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	initialPPID := currentParentPID()
	if initialPPID <= 1 {
		warnWatchParentDisappeared(commandName, initialPPID, initialPPID)
		cancel()
		return ctx, cancel
	}
	go func() {
		ticker := time.NewTicker(watchParentPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentPPID := currentParentPID()
				if currentPPID <= 1 {
					warnWatchParentDisappeared(commandName, initialPPID, currentPPID)
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func sleepWatchPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// newWatchTraceSegment bounds long-lived watch traces by starting one linked
// root per daemon interaction. Cancellation and values come from ctx while
// linkCtx preserves lineage to the finite CLI command span.
func newWatchTraceSegment(ctx, linkCtx context.Context, watchKind string) (context.Context, func(error)) {
	if ctx == nil {
		ctx = context.Background()
	}
	options := []oteltrace.SpanStartOption{
		oteltrace.WithNewRoot(),
		oteltrace.WithAttributes(attribute.String("watch.kind", watchKind)),
	}
	if linkCtx != nil {
		if linked := oteltrace.SpanContextFromContext(linkCtx); linked.IsValid() {
			options = append(options, oteltrace.WithLinks(oteltrace.Link{SpanContext: linked}))
		}
	}
	segmentCtx, span := otel.Tracer("github.com/riordanpawley/azedarach/internal/cli").Start(ctx, "cli.watch_segment", options...)
	return segmentCtx, func(err error) {
		if err != nil {
			span.SetAttributes(attribute.Bool("error", true))
			span.SetStatus(codes.Error, "watch segment failed")
		}
		span.End()
	}
}

func isWatchContextDone(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func warnWatchParentDisappeared(commandName string, initialPPID, currentPPID int) {
	fmt.Fprintf(os.Stderr, "warning: az %s exiting because its owning parent process disappeared (initial_ppid=%d current_ppid=%d)\n", commandName, initialPPID, currentPPID)
}
