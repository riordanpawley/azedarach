package latencytrace

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

const EnvVar = "AZEDARACH_LATENCY_TRACE"

// Enabled reports whether opt-in latency phase logging is active.
func Enabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(EnvVar)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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
