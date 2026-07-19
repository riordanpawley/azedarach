package observability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestManagedOTLPEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jaeger-endpoint")
	t.Setenv(managedEndpointFileEnv, path)

	tests := []struct {
		name    string
		content string
		want    string
		ok      bool
	}{
		{name: "primary", content: "localhost:4318\n0\n", want: "localhost:4318", ok: true},
		{name: "live fallback", content: "127.0.0.1:34318\n" + fmt.Sprint(time.Now().Add(time.Hour).Unix()) + "\n", want: "127.0.0.1:34318", ok: true},
		{name: "expired fallback", content: "localhost:34318\n1\n"},
		{name: "remote host rejected", content: "collector.example.com:4318\n0\n"},
		{name: "invalid port rejected", content: "localhost:0\n0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write endpoint state: %v", err)
			}
			got, ok := managedOTLPEndpoint()
			if ok != tt.ok || got != tt.want {
				t.Fatalf("managedOTLPEndpoint() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestConfiguredEndpointSummaryPrecedence(t *testing.T) {
	const (
		parentTracesEndpoint  = "http://parent.example:4318/v1/traces"
		parentGenericEndpoint = "http://parent.example:4318"
		parentManagedFile     = "/parent/managed-endpoint"
	)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", parentTracesEndpoint)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", parentGenericEndpoint)
	t.Setenv(managedEndpointFileEnv, parentManagedFile)

	tests := []struct {
		name            string
		tracesEndpoint  string
		genericEndpoint string
		managedContent  string
		want            string
	}{
		{
			name:            "traces-specific over generic and managed",
			tracesEndpoint:  "http://localhost:64318/v1/traces",
			genericEndpoint: "http://localhost:54318",
			managedContent:  "localhost:34318\n0\n",
			want:            "http://localhost:64318/v1/traces",
		},
		{
			name:            "generic over managed",
			genericEndpoint: "http://localhost:54318",
			managedContent:  "localhost:34318\n0\n",
			want:            "http://localhost:54318",
		},
		{
			name:           "managed over default",
			managedContent: "localhost:34318\n0\n",
			want:           "http://localhost:34318/v1/traces",
		},
		{
			name:            "whitespace environment falls through to managed",
			tracesEndpoint:  " \t ",
			genericEndpoint: "\n",
			managedContent:  "localhost:34318\n0\n",
			want:            "http://localhost:34318/v1/traces",
		},
		{
			name:            "empty and whitespace sources use default",
			tracesEndpoint:  "",
			genericEndpoint: " ",
			managedContent:  " \n",
			want:            defaultOTLPURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jaeger-endpoint")
			if err := os.WriteFile(path, []byte(tt.managedContent), 0o600); err != nil {
				t.Fatalf("write endpoint state: %v", err)
			}
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", tt.tracesEndpoint)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", tt.genericEndpoint)
			t.Setenv(managedEndpointFileEnv, path)

			if got := configuredEndpointSummary(); got != tt.want {
				t.Fatalf("configuredEndpointSummary() = %q, want %q", got, tt.want)
			}
		})
	}

	for env, want := range map[string]string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": parentTracesEndpoint,
		"OTEL_EXPORTER_OTLP_ENDPOINT":        parentGenericEndpoint,
		managedEndpointFileEnv:               parentManagedFile,
	} {
		if got := os.Getenv(env); got != want {
			t.Fatalf("%s leaked from subtest: got %q, want parent value %q", env, got, want)
		}
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
