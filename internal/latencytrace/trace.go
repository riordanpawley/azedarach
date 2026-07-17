package latencytrace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riordanpawley/azedarach/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	EnvVar                            = "AZEDARACH_LATENCY_TRACE"
	standaloneDependencySlowThreshold = 100 * time.Millisecond
)
const tracerName = "github.com/riordanpawley/azedarach/internal/latencytrace"

var configEnabled atomic.Bool

var spanStringAttributeKeys = map[string]struct{}{
	"client_name":               {},
	"command":                   {},
	"command_shape":             {},
	"daemon_version":            {},
	"dependency.name":           {},
	"dependency.operation":      {},
	"freshness":                 {},
	"hook":                      {},
	"hook_command_shape":        {},
	"mutation.holder_operation": {},
	"mutation.waiter_operation": {},
	"issue_id":                  {},
	"operation":                 {},
	"outcome":                   {},
	"project_id":                {},
	"refresh.holder_operation":  {},
	"refresh.waiter_operation":  {},
	"reason":                    {},
	"request_id":                {},
	"root_issue_id":             {},
	"standalone.reason":    {},
	"transport":                 {},
	"task_id":                   {},
}

// SetConfigEnabled sets the persisted config default for latency phase logging.
func SetConfigEnabled(enabled bool) {
	configEnabled.Store(enabled)
}

// Enabled reports whether latency phase logging is active.
func Enabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(EnvVar)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	case "":
		return configEnabled.Load()
	default:
		return false
	}
}

// CommandShape returns a low-cardinality command shape suitable for logs.
func CommandShape(args []string) string {
	parts := make([]string, 0, 4)
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			parts = append(parts, "--...")
			break
		}
		parts = append(parts, arg)
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}

// LogPhase emits one structured phase timing record when latency tracing is enabled.
func LogPhase(logger *slog.Logger, component, phase string, startedAt time.Time, attrs ...any) {
	LogPhaseContext(context.Background(), logger, component, phase, startedAt, attrs...)
}

// LogPhaseContext emits one structured phase timing record and matching span when enabled.
func LogPhaseContext(ctx context.Context, logger *slog.Logger, component, phase string, startedAt time.Time, attrs ...any) {
	logEnabled := Enabled()
	spanEnabled := observability.Enabled(configEnabled.Load())
	if (!logEnabled && !spanEnabled) || startedAt.IsZero() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	endedAt := time.Now()
	duration := elapsed(startedAt, endedAt)
	base := []any{
		"component", component,
		"phase", phase,
		"duration_ms", duration.Milliseconds(),
	}
	base = append(base, attrs...)
	if logger != nil && logEnabled {
		logger.InfoContext(ctx, "latency phase", base...)
	}
	if !spanEnabled {
		return
	}
	_, endSpan := startSpanAt(ctx, component, phase, startedAt, func() time.Time { return endedAt }, attrs...)
	endSpan(nil)
}

// StartSpan starts a safe, bounded span when OpenTelemetry diagnostics are enabled.
func StartSpan(ctx context.Context, component, phase string, attrs ...any) (context.Context, func(error)) {
	ctx, endSpan := StartSpanWithEndAttributes(ctx, component, phase, attrs...)
	return ctx, func(err error) {
		endSpan(err)
	}
}

// DetachedSpanContext preserves context cancellation and values while forcing
// the next span started from the context to become a new trace root.
func DetachedSpanContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return oteltrace.ContextWithSpanContext(ctx, oteltrace.SpanContext{})
}

// StartSpanWithEndAttributes starts a safe, bounded span and allows callers to
// add final low-cardinality attributes once the operation outcome is known.
func StartSpanWithEndAttributes(ctx context.Context, component, phase string, attrs ...any) (context.Context, func(error, ...any)) {
	return startSpanAt(ctx, component, phase, time.Now(), time.Now, attrs...)
}

func startSpanAt(ctx context.Context, component, phase string, startedAt time.Time, now func() time.Time, attrs ...any) (context.Context, func(error, ...any)) {
	if !observability.Enabled(configEnabled.Load()) {
		if ctx == nil {
			ctx = context.Background()
		}
		return ctx, func(error, ...any) {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	spanAttrs, hadError := spanAttributes(component, phase, attrs)
	explicitReason := standaloneReason(attrs)
	parented := oteltrace.SpanContextFromContext(ctx).IsValid()
	if component == "dependency" && !parented && explicitReason == "" {
		var once sync.Once
		return ctx, func(err error, endAttrs ...any) {
			once.Do(func() {
				endedAt := now()
				duration := elapsed(startedAt, endedAt)
				endSpanAttrs, hadEndError := spanAttributes("", "", endAttrs)
				reason := ""
				switch {
				case err != nil || hadError || hadEndError:
					reason = "error"
				case duration >= standaloneDependencySlowThreshold:
					reason = "slow"
				case standaloneReason(endAttrs) != "":
					reason = standaloneReason(endAttrs)
				}
				if reason == "" {
					return
				}
				retainedAttrs := make([]attribute.KeyValue, 0, len(spanAttrs)+len(endSpanAttrs)+2)
				retainedAttrs = append(retainedAttrs, spanAttrs...)
				retainedAttrs = append(retainedAttrs, endSpanAttrs...)
				retainedAttrs = append(retainedAttrs,
					attribute.Int64("duration_ms", duration.Milliseconds()),
					attribute.String("standalone.reason", reason),
				)
				_, span := otel.Tracer(tracerName).Start(ctx, component+"."+phase,
					oteltrace.WithTimestamp(startedAt),
					oteltrace.WithAttributes(retainedAttrs...),
				)
				if err != nil || hadError || hadEndError {
					span.SetAttributes(attribute.Bool("error", true))
					span.SetStatus(codes.Error, "operation failed")
				}
				span.End(oteltrace.WithTimestamp(endedAt))
			})
		}
	}

	ctx, span := otel.Tracer(tracerName).Start(
		ctx,
		component+"."+phase,
		oteltrace.WithTimestamp(startedAt),
		oteltrace.WithAttributes(spanAttrs...),
	)
	var once sync.Once
	return ctx, func(err error, endAttrs ...any) {
		once.Do(func() {
			endedAt := now()
			duration := elapsed(startedAt, endedAt)
			endSpanAttrs, hadEndError := spanAttributes("", "", endAttrs)
			span.SetAttributes(endSpanAttrs...)
			span.SetAttributes(attribute.Int64("duration_ms", duration.Milliseconds()))
			if err != nil || hadError || hadEndError {
				span.SetAttributes(attribute.Bool("error", true))
				span.SetStatus(codes.Error, "operation failed")
			}
			span.End(oteltrace.WithTimestamp(endedAt))
		})
	}
}

func spanAttributes(component, phase string, attrs []any) ([]attribute.KeyValue, bool) {
	spanAttrs := make([]attribute.KeyValue, 0, len(attrs)/2+2)
	if component != "" {
		spanAttrs = append(spanAttrs,
			attribute.String("component", component),
			attribute.String("phase", phase),
		)
	}
	hadError := false
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if !ok || key == "" {
			continue
		}
		attr, isError, ok := spanAttribute(key, attrs[i+1])
		if ok {
			spanAttrs = append(spanAttrs, attr)
		}
		hadError = hadError || isError
	}
	return spanAttrs, hadError
}

func standaloneReason(attrs []any) string {
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if !ok || key != "standalone.reason" {
			continue
		}
		value, _ := attrs[i+1].(string)
		switch value {
		case "diagnostic":
			return value
		default:
			return ""
		}
	}
	return ""
}

func elapsed(startedAt, endedAt time.Time) time.Duration {
	duration := endedAt.Sub(startedAt)
	if duration < 0 {
		return 0
	}
	return duration
}

func spanAttribute(key string, value any) (attribute.KeyValue, bool, bool) {
	switch v := value.(type) {
	case nil:
		return attribute.KeyValue{}, false, false
	case error:
		return attribute.Bool("error", true), true, true
	case string:
		if key == "standalone.reason" && v != "diagnostic" && v != "error" && v != "slow" {
			return attribute.KeyValue{}, false, false
		}
		if !allowSpanStringAttribute(key) {
			return attribute.KeyValue{}, false, false
		}
		return attribute.String(key, v), key == "outcome" && v == "error", true
	case fmt.Stringer:
		if !allowSpanStringAttribute(key) {
			return attribute.KeyValue{}, false, false
		}
		return attribute.String(key, v.String()), false, true
	case bool:
		return attribute.Bool(key, v), false, true
	case int:
		return attribute.Int(key, v), false, true
	case int64:
		return attribute.Int64(key, v), false, true
	case uint64:
		return attribute.Int64(key, int64(v)), false, true
	case time.Duration:
		return attribute.Int64(key, v.Milliseconds()), false, true
	default:
		if !allowSpanStringAttribute(key) {
			return attribute.KeyValue{}, false, false
		}
		return attribute.String(key, fmt.Sprint(v)), false, true
	}
}

func allowSpanStringAttribute(key string) bool {
	_, ok := spanStringAttributeKeys[key]
	return ok
}
