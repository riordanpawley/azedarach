package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/riordanpawley/azedarach/internal/buildinfo"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	EnvVar              = "AZEDARACH_OTEL"
	DefaultOTLPEndpoint = "localhost:4318"
	defaultOTLPURL      = "http://" + DefaultOTLPEndpoint + "/v1/traces"
)

var errorMessageURLPattern = regexp.MustCompile(`https?://[^\s"]+`)

// Options controls process-level OpenTelemetry setup.
type Options struct {
	ServiceName    string
	ServiceVersion string
	Enabled        bool
	Logger         *slog.Logger
}

// Enabled reports whether OpenTelemetry export should be active.
func Enabled(configDefault bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(EnvVar)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	case "":
		return configDefault
	default:
		return false
	}
}

// Configure installs a process-wide trace provider when OTel is enabled.
func Configure(ctx context.Context, opts Options) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	installErrorHandler(opts.Logger)
	if !Enabled(opts.Enabled) {
		return func(context.Context) error { return nil }, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = "azedarach"
	}
	serviceVersion := strings.TrimSpace(opts.ServiceVersion)
	if serviceVersion == "" {
		serviceVersion = buildinfo.VersionString()
	}

	exporter, err := otlptracehttp.New(ctx, exporterOptions()...)
	if err != nil {
		return nil, fmt.Errorf("create otel trace exporter: %w", err)
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			attribute.String("service.name", serviceName),
			attribute.String("service.version", serviceVersion),
			attribute.String("service.namespace", "azedarach"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create otel resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(provider)
	if opts.Logger != nil {
		opts.Logger.Info("otel tracing enabled",
			"service", serviceName,
			"endpoint", configuredEndpointSummary(),
			"env_var", EnvVar,
		)
	}
	return provider.Shutdown, nil
}

func installErrorHandler(logger *slog.Logger) {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		if err == nil {
			return
		}
		activeLogger := logger
		if activeLogger == nil {
			activeLogger = slog.Default()
		}
		if activeLogger == nil {
			return
		}
		activeLogger.Warn("otel trace export failed",
			"event", "otel.trace_export.failed",
			"error_class", "dependency",
			"error_boundary", "dependency",
			"error_retryable", true,
			"error_message", sanitizeErrorMessage(err),
		)
	}))
}

func sanitizeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return errorMessageURLPattern.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			return raw
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	})
}

// InjectMetadata writes W3C trace context into daemon protocol metadata.
func InjectMetadata(ctx context.Context, meta *protocol.Metadata) {
	if ctx == nil || meta == nil {
		return
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	meta.TraceParent = carrier.Get("traceparent")
	meta.TraceState = carrier.Get("tracestate")
}

// ExtractMetadata reads W3C trace context from daemon protocol metadata.
func ExtractMetadata(ctx context.Context, meta protocol.Metadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(meta.TraceParent) == "" && strings.TrimSpace(meta.TraceState) == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{}
	carrier.Set("traceparent", meta.TraceParent)
	carrier.Set("tracestate", meta.TraceState)
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

func configuredEndpointSummary() string {
	if endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	if endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	return defaultOTLPURL
}

func exporterOptions() []otlptracehttp.Option {
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != "" ||
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" {
		return nil
	}
	return []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(DefaultOTLPEndpoint),
		otlptracehttp.WithInsecure(),
	}
}
