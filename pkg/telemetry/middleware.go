package telemetry

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// shouldTrace returns true if the request should be traced
// Filters out health check endpoints to reduce noise
func shouldTrace(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/livez", "/readyz":
		return false
	default:
		return true
	}
}

// HTTPMiddleware wraps an HTTP handler with OpenTelemetry tracing
func HTTPMiddleware(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "backend",
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
		otelhttp.WithFilter(shouldTrace),
	)
}
