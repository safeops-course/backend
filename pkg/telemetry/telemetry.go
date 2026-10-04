// Package telemetry sets up OpenTelemetry for the backend: traces, metrics and logs exported over
// OTLP/HTTP to Uptrace when UPTRACE_DSN is set, plus span helpers. It uses the plain OpenTelemetry SDK
// (the setup Uptrace documents as "OTLP"), not the uptrace-go wrapper: the wrapper lagged behind the
// SDK and pinned log packages with known vulnerabilities.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/ldbl/sre/backend/pkg/version"
)

// Init sets up propagation and, when UPTRACE_DSN is set, the trace, metric and log pipelines to
// Uptrace. It returns the function that flushes and stops them on shutdown.
func Init(ctx context.Context) func() {
	serviceName := getEnv("SERVICE_NAME", "backend")
	// The version baked in at build time (ldflags, the image tag) - so every span says which release
	// produced it. SERVICE_VERSION still overrides it when set.
	serviceVersion := getEnv("SERVICE_VERSION", version.Version)
	deploymentEnv := getEnv("DEPLOYMENT_ENVIRONMENT", "development")
	uptraceDSN := os.Getenv("UPTRACE_DSN")

	// W3C trace context and baggage across service boundaries (the browser sends traceparent).
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	// Export errors are not silent: they go to the process log.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Printf("OpenTelemetry error: %v", err)
	}))

	if uptraceDSN == "" {
		log.Printf("OpenTelemetry running without Uptrace export: UPTRACE_DSN is not set (service=%s env=%s)", serviceName, deploymentEnv)
		return func() {}
	}

	shutdown, err := setupExport(ctx, uptraceDSN, serviceName, serviceVersion, deploymentEnv)
	if err != nil {
		log.Printf("OpenTelemetry export disabled: %v", err)
		return func() {}
	}
	log.Printf("OpenTelemetry initialized: service=%s version=%s env=%s", serviceName, serviceVersion, deploymentEnv)
	return func() {
		if err := shutdown(ctx); err != nil {
			log.Printf("OpenTelemetry shutdown: %v", err)
		}
	}
}

// setupExport builds the three OTLP/HTTP pipelines and installs them as the global providers.
// The DSN (https://<token>@api.uptrace.dev?grpc=4317) gives the endpoint host and is sent whole as
// the uptrace-dsn header, as Uptrace documents.
func setupExport(ctx context.Context, dsn, serviceName, serviceVersion, deploymentEnv string) (func(context.Context) error, error) {
	endpoint, insecure, err := exportTarget(dsn)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"uptrace-dsn": dsn}

	res, err := resource.New(ctx,
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES: k8s.cluster.name, k8s.namespace.name, k8s.pod.name
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			attribute.String("deployment.environment.name", deploymentEnv),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	}
	if insecure {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
	}
	traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithHeaders(headers),
		otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression),
		otlpmetrichttp.WithTemporalitySelector(preferDelta),
	}
	if insecure {
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
	}
	metricExporter, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		return nil, fmt.Errorf("metric exporter: %w", err)
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(res),
	)

	logOpts := []otlploghttp.Option{
		otlploghttp.WithEndpoint(endpoint),
		otlploghttp.WithHeaders(headers),
		otlploghttp.WithCompression(otlploghttp.GzipCompression),
	}
	if insecure {
		logOpts = append(logOpts, otlploghttp.WithInsecure())
	}
	logExporter, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		return nil, fmt.Errorf("log exporter: %w", err)
	}
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetLoggerProvider(loggerProvider)

	// Go runtime metrics (goroutines, memory, GC), as the uptrace-go wrapper used to add.
	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		log.Printf("OpenTelemetry runtime metrics disabled: %v", err)
	}

	return func(ctx context.Context) error {
		return errors.Join(
			tracerProvider.Shutdown(ctx),
			meterProvider.Shutdown(ctx),
			loggerProvider.Shutdown(ctx),
		)
	}, nil
}

// exportTarget returns the OTLP endpoint (host[:port]) and whether to send it plain HTTP, from the DSN.
// https:// - TLS (Uptrace Cloud). http:// - plain HTTP for a self-hosted Uptrace, but a DSN carries its
// token as the user part, so plain HTTP is accepted only to a loopback host: anywhere else the token
// would cross the network unencrypted. Any other scheme is refused.
func exportTarget(dsn string) (endpoint string, insecure bool, err error) {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" {
		return "", false, fmt.Errorf("UPTRACE_DSN is not a URL with a host")
	}
	switch parsed.Scheme {
	case "https":
		return parsed.Host, false, nil
	case "http":
		if parsed.User != nil && !isLoopback(parsed.Hostname()) {
			return "", false, fmt.Errorf("UPTRACE_DSN uses http:// with a token for %s: use https://, or plain HTTP only to localhost", parsed.Hostname())
		}
		return parsed.Host, true, nil
	default:
		return "", false, fmt.Errorf("UPTRACE_DSN scheme %q is not http or https", parsed.Scheme)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// preferDelta sends counters and histograms as deltas, which Uptrace prefers; the rest stays
// cumulative.
func preferDelta(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	switch kind {
	case sdkmetric.InstrumentKindCounter, sdkmetric.InstrumentKindObservableCounter, sdkmetric.InstrumentKindHistogram:
		return metricdata.DeltaTemporality
	default:
		return metricdata.CumulativeTemporality
	}
}

// Tracer returns the global tracer
func Tracer() trace.Tracer {
	return otel.Tracer("backend")
}

// StartSpan starts a new span with the given name and options
func StartSpan(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, spanName, opts...)
}

// AddEvent adds an event to the current span
func AddEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.AddEvent(name, trace.WithAttributes(attrs...))
}

// SetAttributes sets attributes on the current span
func SetAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(attrs...)
}

// RecordError records an error on the current span with stack trace
func RecordError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, err.Error())
}

// NewHTTPTransport returns an http.RoundTripper instrumented with OpenTelemetry
func NewHTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
