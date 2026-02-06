package telemetry

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HTTPMiddleware wraps an HTTP handler with OpenTelemetry tracing
func HTTPMiddleware(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "backend",
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}
