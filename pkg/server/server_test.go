package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ldbl/sre/backend/pkg/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Config{
		Port:            0,
		UIMessage:       "Test Message",
		UIColor:         "#ffffff",
		Version:         "vtest",
		Commit:          "deadbeef",
		CommitShort:     "deadbee",
		BuildDate:       "2024-01-01T00:00:00Z",
		RandomDelayMax:  0,
		RandomErrorRate: 0,
	}
	srv := New(cfg, nil)
	return srv
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
}

func TestReadyToggling(t *testing.T) {
	srv := newTestServer(t)

	// disable readiness
	reqDisable := httptest.NewRequest(http.MethodPut, "/readyz/disable", nil)
	rrDisable := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrDisable, reqDisable)
	if rrDisable.Code != http.StatusOK {
		t.Fatalf("expected status 200 on disable, got %d", rrDisable.Code)
	}

	// readiness should now fail
	reqReady := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rrReady := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrReady, reqReady)
	if rrReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503 when not ready, got %d", rrReady.Code)
	}

	// enable readiness
	reqEnable := httptest.NewRequest(http.MethodPut, "/readyz/enable", nil)
	rrEnable := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrEnable, reqEnable)
	if rrEnable.Code != http.StatusOK {
		t.Fatalf("expected status 200 on enable, got %d", rrEnable.Code)
	}
}

func TestStatusEndpoint(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/status/418", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 418 {
		t.Fatalf("expected status 418, got %d", rr.Code)
	}
}

func TestPanicEndpoint(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if payload["status"] != "terminating" {
		t.Fatalf("unexpected panic response: %v", payload)
	}
}

func TestEchoEndpoint(t *testing.T) {
	srv := newTestServer(t)
	payload := "hello"
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(payload))
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if got := rr.Body.String(); got != payload {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestVersionEndpoint(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if body["version"] != "vtest" {
		t.Fatalf("expected version vtest, got %s", body["version"])
	}
	if body["build_time"] != "2024-01-01T00:00:00Z" {
		t.Fatalf("expected build date, got %s", body["build_time"])
	}
	if body["commit_short"] != "deadbee" {
		t.Fatalf("expected short commit deadbee, got %s", body["commit_short"])
	}
}

func TestMetricsEndpoint(t *testing.T) {
	srv := newTestServer(t)

	primeReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	primeRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(primeRec, primeReq)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "app_http_requests_total") {
		t.Fatalf("metrics payload missing expected counter")
	}
}

func TestOpenAPIEndpoint(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/openapi", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("failed to parse openapi spec: %v", err)
	}
	if doc["openapi"] != "3.0.3" {
		t.Fatalf("unexpected openapi version: %v", doc["openapi"])
	}
	info := doc["info"].(map[string]any)
	if info["x-commit-short"] != "deadbee" {
		t.Fatalf("expected x-commit-short deadbee, got %v", info["x-commit-short"])
	}
}

func TestSwaggerEndpoint(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/swagger", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("expected text/html content-type, got %s", rr.Header().Get("Content-Type"))
	}
	if !strings.Contains(rr.Body.String(), "SwaggerUIBundle") {
		t.Fatalf("swagger ui bundle not rendered")
	}
}
