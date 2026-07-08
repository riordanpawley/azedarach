package latencytrace

import (
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
