package latencytrace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/riordanpawley/azedarach/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const EnvVar = "AZEDARACH_LATENCY_TRACE"
const tracerName = "github.com/riordanpawley/azedarach/internal/latencytrace"

var configEnabled atomic.Bool

var spanStringAttributeKeys = map[string]struct{}{
	"client_name":    {},
	"command":        {},
	"command_shape":  {},
	"daemon_version": {},
	"freshness":      {},
	"issue_id":       {},
	"project_id":     {},
	"reason":         {},
	"request_id":     {},
	"root_issue_id":  {},
	"task_id":        {},
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
	duration := time.Since(startedAt)
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
	spanAttrs := []attribute.KeyValue{
		attribute.String("component", component),
		attribute.String("phase", phase),
		attribute.Int64("duration_ms", duration.Milliseconds()),
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
	_, span := otel.Tracer(tracerName).Start(
		ctx,
		component+"."+phase,
		oteltrace.WithTimestamp(startedAt),
		oteltrace.WithAttributes(spanAttrs...),
	)
	if hadError {
		span.SetStatus(codes.Error, "phase failed")
	}
	span.End(oteltrace.WithTimestamp(startedAt.Add(duration)))
}

func spanAttribute(key string, value any) (attribute.KeyValue, bool, bool) {
	switch v := value.(type) {
	case nil:
		return attribute.KeyValue{}, false, false
	case error:
		return attribute.Bool("error", true), true, true
	case string:
		if !allowSpanStringAttribute(key) {
			return attribute.KeyValue{}, false, false
		}
		return attribute.String(key, v), false, true
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
