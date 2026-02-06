package telemetry

import (
	"context"
	"log"
	"os"

	"github.com/uptrace/uptrace-go/uptrace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

// Init initializes OpenTelemetry with Uptrace Cloud
func Init(ctx context.Context) func() {
	// Get service configuration from environment
	serviceName := getEnv("SERVICE_NAME", "backend")
	serviceVersion := getEnv("SERVICE_VERSION", "v1.0.0")
	deploymentEnv := getEnv("DEPLOYMENT_ENVIRONMENT", "development")

	// Configure Uptrace
	uptrace.ConfigureOpentelemetry(
		uptrace.WithServiceName(serviceName),
		uptrace.WithServiceVersion(serviceVersion),
		uptrace.WithDeploymentEnvironment(deploymentEnv),
	)

	// Set additional resource attributes
	_, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			semconv.DeploymentEnvironmentName(deploymentEnv),
		),
	)
	if err != nil {
		log.Printf("Failed to create resource: %v", err)
	} else {
		log.Printf("OpenTelemetry initialized: service=%s version=%s env=%s", serviceName, serviceVersion, deploymentEnv)
	}

	// Return shutdown function
	return func() {
		if err := uptrace.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down Uptrace: %v", err)
		}
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
	span.AddEvent(name, trace.WithAttributes(attrs...))
}

// SetAttributes sets attributes on the current span
func SetAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attrs...)
}

// RecordError records an error on the current span
func RecordError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
