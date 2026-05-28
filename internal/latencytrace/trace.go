package latencytrace

import (
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const EnvVar = "AZEDARACH_LATENCY_TRACE"

var configEnabled atomic.Bool

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
	if logger == nil || !Enabled() || startedAt.IsZero() {
		return
	}
	base := []any{
		"component", component,
		"phase", phase,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	}
	base = append(base, attrs...)
	logger.Info("latency phase", base...)
}
