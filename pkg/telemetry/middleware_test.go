package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Span names use the route pattern, so /delay/0.1 and /delay/0.11 share one name.
func TestSpanNameIsRoutePattern(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	r := chi.NewRouter()
	r.Use(HTTPMiddleware)
	r.Get("/delay/{seconds}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, path := range []string{"/delay/0.1", "/delay/0.11", "/nope"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	var names []string
	for _, s := range recorder.Ended() {
		names = append(names, s.Name())
	}
	want := []string{"GET /delay/{seconds}", "GET /delay/{seconds}", "GET"}
	if len(names) != len(want) {
		t.Fatalf("spans %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("spans %v, want %v", names, want)
		}
	}
}

// Probes and Prometheus scrapes make no spans; a user request does.
func TestProbesAndScrapesAreNotTraced(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	r := chi.NewRouter()
	r.Use(HTTPMiddleware)
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	for _, path := range []string{"/healthz", "/livez", "/readyz", "/metrics", "/version"} {
		r.Get(path, ok)
	}

	for _, path := range []string{"/healthz", "/livez", "/readyz", "/metrics", "/version"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "GET /version" {
		var names []string
		for _, s := range spans {
			names = append(names, s.Name())
		}
		t.Fatalf("spans %v, want only [GET /version]", names)
	}
}
