package latencytrace

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/observability"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestEnabledUsesConfigWhenEnvUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	SetConfigEnabled(false)
	if Enabled() {
		t.Fatal("Enabled() = true, want false from config")
	}
	SetConfigEnabled(true)
	if !Enabled() {
		t.Fatal("Enabled() = false, want true from config")
	}
	SetConfigEnabled(false)
}

func TestEnabledEnvOverridesConfig(t *testing.T) {
	SetConfigEnabled(true)
	t.Setenv(EnvVar, "0")
	if Enabled() {
		t.Fatal("Enabled() = true, want false from env override")
	}
	SetConfigEnabled(false)
	t.Setenv(EnvVar, "1")
	if !Enabled() {
		t.Fatal("Enabled() = false, want true from env override")
	}
	SetConfigEnabled(false)
}

func TestCommandShapeRedactsFlagValues(t *testing.T) {
	got := CommandShape([]string{"issue", "update", "cji", "--notes", "secret body"})
	if got != "issue update cji" {
		t.Fatalf("CommandShape() = %q, want issue update cji", got)
	}
	got = CommandShape([]string{"config", "set", "diagnostics.latencyTrace", "true"})
	if got != "config set diagnostics.latencyTrace" {
		t.Fatalf("CommandShape() = %q, want config set diagnostics.latencyTrace", got)
	}
}

func TestLogPhaseEmitsSpanWhenEnabled(t *testing.T) {
	t.Setenv(EnvVar, "")
	t.Setenv(observability.EnvVar, "")
	SetConfigEnabled(true)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		SetConfigEnabled(false)
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	LogPhase(logger, "cli", "command_execute", time.Now().Add(-time.Millisecond),
		"command_shape", "issue get cxk",
		"request_id", "req-1",
	)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if got, want := spans[0].Name(), "cli.command_execute"; got != want {
		t.Fatalf("span name = %q, want %q", got, want)
	}
	attrs := map[string]string{}
	for _, attr := range spans[0].Attributes() {
		if attr.Value.Type().String() == "STRING" {
			attrs[string(attr.Key)] = attr.Value.AsString()
		}
	}
	if attrs["command_shape"] != "issue get cxk" {
		t.Fatalf("command_shape attr = %q, want issue get cxk", attrs["command_shape"])
	}
	if attrs["request_id"] != "req-1" {
		t.Fatalf("request_id attr = %q, want req-1", attrs["request_id"])
	}
}

func TestLogPhaseFiltersUnsafeSpanStringAttributes(t *testing.T) {
	t.Setenv(EnvVar, "")
	t.Setenv(observability.EnvVar, "true")
	SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		SetConfigEnabled(false)
	})

	LogPhase(nil, "cli", "command_execute", time.Now().Add(-time.Millisecond),
		"command_shape", "issue get cxk",
		"repo_dir", "/Users/example/private/repo",
		"socket", "/tmp/private.sock",
		"body", "secret body",
		"task_count", 3,
	)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	attrs := map[string]bool{}
	for _, attr := range spans[0].Attributes() {
		attrs[string(attr.Key)] = true
	}
	for _, key := range []string{"repo_dir", "socket", "body"} {
		if attrs[key] {
			t.Fatalf("span included unsafe attr %q", key)
		}
	}
	if !attrs["command_shape"] {
		t.Fatal("span missing command_shape attr")
	}
	if !attrs["task_count"] {
		t.Fatal("span missing numeric task_count attr")
	}
}

func TestLogPhaseDoesNotEmitSpanWhenOTelEnvDisablesConfig(t *testing.T) {
	t.Setenv(EnvVar, "1")
	t.Setenv(observability.EnvVar, "false")
	SetConfigEnabled(true)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		SetConfigEnabled(false)
	})

	LogPhase(nil, "cli", "command_execute", time.Now().Add(-time.Millisecond))

	if spans := recorder.Ended(); len(spans) != 0 {
		t.Fatalf("ended spans = %d, want 0", len(spans))
	}
}

func TestLogPhaseEmitsSpanWhenOTelEnvOverridesLatencyTrace(t *testing.T) {
	t.Setenv(EnvVar, "0")
	t.Setenv(observability.EnvVar, "true")
	SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		SetConfigEnabled(false)
	})

	LogPhase(nil, "cli", "dependencies_init", time.Now().Add(-time.Millisecond))

	if spans := recorder.Ended(); len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
}

func TestStandaloneDependencyRetentionBoundsRootSpanVolume(t *testing.T) {
	t.Setenv(EnvVar, "")
	t.Setenv(observability.EnvVar, "true")
	SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		SetConfigEnabled(false)
	})

	start := time.Unix(1_000, 0)
	fastEnd := func() time.Time { return start.Add(10 * time.Millisecond) }
	for range 1_000 {
		_, endSpan := startSpanAt(context.Background(), "dependency", "sqlite.query", start, fastEnd,
			"dependency.name", "sqlite",
			"dependency.operation", "select",
		)
		endSpan(nil)
	}
	if spans := recorder.Ended(); len(spans) != 0 {
		t.Fatalf("fast successful standalone dependency spans = %d, want 0", len(spans))
	}

	_, endError := startSpanAt(context.Background(), "dependency", "sqlite.query", start, fastEnd,
		"dependency.name", "sqlite",
		"dependency.operation", "select",
	)
	endError(errors.New("query failed"))
	assertStandaloneSpanReason(t, recorder.Ended(), "error")

	before := len(recorder.Ended())
	slowEnd := func() time.Time { return start.Add(standaloneDependencySlowThreshold) }
	_, endSlow := startSpanAt(context.Background(), "dependency", "git", start, slowEnd,
		"dependency.name", "git",
		"dependency.operation", "status",
	)
	endSlow(nil)
	assertStandaloneSpanReason(t, recorder.Ended()[before:], "slow")

	before = len(recorder.Ended())
	_, endDiagnostic := startSpanAt(context.Background(), "dependency", "tmux", start, fastEnd,
		"dependency.name", "tmux",
		"dependency.operation", "list-panes",
		"standalone.reason", "diagnostic",
	)
	endDiagnostic(nil)
	assertStandaloneSpanReason(t, recorder.Ended()[before:], "diagnostic")

	before = len(recorder.Ended())
	parentCtx, parent := otel.Tracer("test").Start(context.Background(), "daemon.background_scan")
	_, endParented := startSpanAt(parentCtx, "dependency", "sqlite.query", start, fastEnd,
		"dependency.name", "sqlite",
		"dependency.operation", "select",
	)
	endParented(nil)
	parent.End()
	spans := recorder.Ended()[before:]
	if len(spans) != 2 {
		t.Fatalf("parented trace spans = %d, want dependency plus parent", len(spans))
	}
	if spans[0].Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("dependency parent = %s, want %s", spans[0].Parent().SpanID(), parent.SpanContext().SpanID())
	}
}

func TestStandaloneDependencyPhaseRetainsErrorOutcome(t *testing.T) {
	t.Setenv(EnvVar, "")
	t.Setenv(observability.EnvVar, "true")
	SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		SetConfigEnabled(false)
	})

	LogPhaseContext(context.Background(), nil, "dependency", "sqlite.issue.runtime_projection", time.Now(),
		"dependency.name", "sqlite",
		"dependency.operation", "issue.runtime_projection",
		"outcome", "error",
	)
	assertStandaloneSpanReason(t, recorder.Ended(), "error")
}

func assertStandaloneSpanReason(t *testing.T, spans []sdktrace.ReadOnlySpan, want string) {
	t.Helper()
	if len(spans) != 1 {
		t.Fatalf("standalone spans = %d, want 1", len(spans))
	}
	if spans[0].Parent().IsValid() {
		t.Fatalf("standalone span parent = %s, want invalid", spans[0].Parent().SpanID())
	}
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == "standalone.reason" {
			if got := attr.Value.AsString(); got != want {
				t.Fatalf("standalone.reason = %q, want %q", got, want)
			}
			return
		}
	}
	t.Fatal("standalone span missing standalone.reason")
}
