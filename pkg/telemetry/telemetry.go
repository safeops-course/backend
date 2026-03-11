package telemetry

import (
	"context"
	"log"
	"os"

	"net/http"

	"github.com/uptrace/uptrace-go/uptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Metrics holds OpenTelemetry metric instruments
type Metrics struct {
	RequestCounter  metric.Int64Counter
	ErrorCounter    metric.Int64Counter
	RequestDuration metric.Float64Histogram
}

var metrics *Metrics

// Init initializes OpenTelemetry with Uptrace Cloud
func Init(ctx context.Context) func() {
	// Get service configuration from environment
	serviceName := getEnv("SERVICE_NAME", "backend")
	serviceVersion := getEnv("SERVICE_VERSION", "v1.0.0")
	deploymentEnv := getEnv("DEPLOYMENT_ENVIRONMENT", "development")
	uptraceDSN := os.Getenv("UPTRACE_DSN")

	// Ensure W3C trace context and baggage propagation across service boundaries.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if uptraceDSN == "" {
		log.Printf("OpenTelemetry running without Uptrace export: UPTRACE_DSN is not set (service=%s env=%s)", serviceName, deploymentEnv)
	} else {
		// Configure Uptrace export when DSN is available.
		uptrace.ConfigureOpentelemetry(
			uptrace.WithDSN(uptraceDSN),
			uptrace.WithServiceName(serviceName),
			uptrace.WithServiceVersion(serviceVersion),
			uptrace.WithDeploymentEnvironment(deploymentEnv),
		)
	}

	log.Printf("OpenTelemetry initialized: service=%s version=%s env=%s", serviceName, serviceVersion, deploymentEnv)

	// Initialize OTel metrics
	initMetrics()

	// Return shutdown function
	return func() {
		if uptraceDSN == "" {
			return
		}
		if err := uptrace.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down Uptrace: %v", err)
		}
	}
}

// initMetrics creates OpenTelemetry metric instruments
func initMetrics() {
	meter := otel.Meter("backend")

	requestCounter, err := meter.Int64Counter(
		"backend.requests.total",
		metric.WithDescription("Total number of requests processed"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		log.Printf("Failed to create request counter: %v", err)
	}

	errorCounter, err := meter.Int64Counter(
		"backend.errors.total",
		metric.WithDescription("Total number of errors"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		log.Printf("Failed to create error counter: %v", err)
	}

	requestDuration, err := meter.Float64Histogram(
		"backend.request.duration",
		metric.WithDescription("Request duration in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		log.Printf("Failed to create duration histogram: %v", err)
	}

	metrics = &Metrics{
		RequestCounter:  requestCounter,
		ErrorCounter:    errorCounter,
		RequestDuration: requestDuration,
	}
}

// GetMetrics returns the metrics instance
func GetMetrics() *Metrics {
	return metrics
}

// RecordRequest records a request metric using OTel semantic conventions.
func RecordRequest(ctx context.Context, method, path string, statusCode int, duration float64) {
	if metrics == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", method),
		attribute.String("http.route", path),
		attribute.Int("http.response.status_code", statusCode),
	}

	metrics.RequestCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	metrics.RequestDuration.Record(ctx, duration, metric.WithAttributes(attrs...))

	if statusCode >= 500 {
		metrics.ErrorCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
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
