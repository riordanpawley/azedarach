package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	EnvVar                 = "AZEDARACH_OTEL"
	managedEndpointFileEnv = "AZEDARACH_JAEGER_ENDPOINT_FILE"
	DefaultOTLPEndpoint    = "localhost:4318"
	defaultOTLPURL         = "http://" + DefaultOTLPEndpoint + "/v1/traces"
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
	if endpoint, ok := managedOTLPEndpoint(); ok {
		return "http://" + endpoint + "/v1/traces"
	}
	return defaultOTLPURL
}

func exporterOptions() []otlptracehttp.Option {
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != "" ||
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" {
		return nil
	}
	if endpoint, ok := managedOTLPEndpoint(); ok {
		return []otlptracehttp.Option{
			otlptracehttp.WithEndpoint(endpoint),
			otlptracehttp.WithInsecure(),
		}
	}
	return []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(DefaultOTLPEndpoint),
		otlptracehttp.WithInsecure(),
	}
}

func managedOTLPEndpoint() (string, bool) {
	path := strings.TrimSpace(os.Getenv(managedEndpointFileEnv))
	if path == "" {
		stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
		if stateHome == "" {
			home, err := os.UserHomeDir()
			if err != nil || strings.TrimSpace(home) == "" {
				return "", false
			}
			stateHome = filepath.Join(home, ".local", "state")
		}
		path = filepath.Join(stateHome, "azedarach", "jaeger-otlp-endpoint")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1024 {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		return "", false
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(lines[0]))
	if err != nil || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
		return "", false
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", false
	}
	expires, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil || expires < 0 {
		return "", false
	}
	if expires != 0 && time.Now().Unix() >= expires {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}
