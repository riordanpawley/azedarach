package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestEnabledUsesConfigDefaultWhenEnvUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	if Enabled(false) {
		t.Fatal("Enabled(false) = true, want false")
	}
	if !Enabled(true) {
		t.Fatal("Enabled(true) = false, want true")
	}
}

func TestEnabledEnvOverridesConfigDefault(t *testing.T) {
	t.Setenv(EnvVar, "0")
	if Enabled(true) {
		t.Fatal("Enabled(true) = true, want false from env override")
	}
	t.Setenv(EnvVar, "on")
	if !Enabled(false) {
		t.Fatal("Enabled(false) = false, want true from env override")
	}
}

func TestConfigureDisabledReturnsNoopShutdown(t *testing.T) {
	t.Setenv(EnvVar, "off")
	shutdown, err := Configure(context.Background(), Options{Enabled: true})
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("Configure() returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown() error = %v", err)
	}
}

func TestConfigureRoutesOTelErrorsToLogger(t *testing.T) {
	t.Setenv(EnvVar, "on")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	shutdown, err := Configure(context.Background(), Options{Enabled: true, Logger: logger})
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	t.Cleanup(func() {
		_ = shutdown(context.Background())
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
	})

	otel.Handle(errors.New(`traces export: Post "http://user:secret@localhost:4318/v1/traces?token=abc#frag": dial tcp [::1]:4318: connect: connection refused`))

	got := log.String()
	for _, want := range []string{
		"otel trace export failed",
		"event=otel.trace_export.failed",
		"error_class=dependency",
		"error_boundary=dependency",
		"error_retryable=true",
		"http://localhost:4318/v1/traces",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log = %q, want %q", got, want)
		}
	}
	for _, leaked := range []string{"user:secret", "token=abc", "#frag"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("log = %q, leaked %q", got, leaked)
		}
	}
}

func TestMetadataPropagationRoundTrip(t *testing.T) {
	t.Setenv(EnvVar, "off")
	shutdown, err := Configure(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	t.Cleanup(func() {
		_ = shutdown(context.Background())
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
	})
	provider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(provider)

	ctx, span := otel.Tracer("test").Start(context.Background(), "root")
	defer span.End()
	var meta protocol.Metadata
	InjectMetadata(ctx, &meta)
	if meta.TraceParent == "" {
		t.Fatal("TraceParent is empty")
	}

	extracted := ExtractMetadata(context.Background(), meta)
	got := oteltrace.SpanContextFromContext(extracted)
	if !got.IsValid() {
		t.Fatal("extracted span context is invalid")
	}
	if got.TraceID() != span.SpanContext().TraceID() {
		t.Fatalf("trace id = %s, want %s", got.TraceID(), span.SpanContext().TraceID())
	}
}
