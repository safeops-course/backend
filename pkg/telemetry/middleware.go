package telemetry

import (
	"net/http"

	"github.com/go-chi/chi/v5"
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

// HTTPMiddleware wraps an HTTP handler with OpenTelemetry tracing.
//
// Span names use the chi route pattern ("GET /delay/{seconds}"), not the raw
// path: /delay/0.1, /delay/0.11, ... would each make a new span name, and the
// set of names would never end. otelhttp calls the formatter again once the
// handler has run - by then chi has routed the request and the pattern is
// known; an unmatched path keeps just the method.
func HTTPMiddleware(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "backend",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					return r.Method + " " + pattern
				}
			}
			return r.Method
		}),
		otelhttp.WithFilter(shouldTrace),
	)
}
