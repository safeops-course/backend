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
		Port:               0,
		UIMessage:          "Test Message",
		UIColor:            "#ffffff",
		Version:            "vtest",
		Commit:             "deadbeef",
		CommitShort:        "deadbee",
		BuildDate:          "2024-01-01T00:00:00Z",
		RandomDelayMax:     0,
		RandomErrorRate:    0,
		JWTSecret:          "test-secret",
		JWTTokenTTLMinutes: 60,
		AuthDBPath:         t.TempDir() + "/users.json",
	}
	srv := New(cfg, nil)
	return srv
}

func registerAndGetToken(t *testing.T, srv *Server) string {
	t.Helper()
	body := `{"username":"test-user","password":"verysecure123"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201 on register, got %d (%s)", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse register response: %v", err)
	}
	token, ok := payload["token"].(string)
	if !ok || token == "" {
		t.Fatalf("token missing from register response: %v", payload)
	}

	return token
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

func TestReadyTogglingRequiresAuth(t *testing.T) {
	srv := newTestServer(t)

	unauthReq := httptest.NewRequest(http.MethodPut, "/readyz/disable", nil)
	unauthRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 for unauthenticated disable, got %d", unauthRec.Code)
	}

	token := registerAndGetToken(t, srv)

	reqDisable := httptest.NewRequest(http.MethodPut, "/readyz/disable", nil)
	reqDisable.Header.Set("Authorization", "Bearer "+token)
	rrDisable := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrDisable, reqDisable)
	if rrDisable.Code != http.StatusOK {
		t.Fatalf("expected status 200 on disable with auth, got %d", rrDisable.Code)
	}

	reqReady := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rrReady := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrReady, reqReady)
	if rrReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503 when not ready, got %d", rrReady.Code)
	}

	reqEnable := httptest.NewRequest(http.MethodPut, "/readyz/enable", nil)
	reqEnable.Header.Set("Authorization", "Bearer "+token)
	rrEnable := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrEnable, reqEnable)
	if rrEnable.Code != http.StatusOK {
		t.Fatalf("expected status 200 on enable with auth, got %d", rrEnable.Code)
	}
}

func TestPanicEndpointRequiresAuth(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
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

func TestAuthRegisterAndLogin(t *testing.T) {
	srv := newTestServer(t)

	registerBody := `{"username":"alice","password":"password123"}`
	registerReq := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(registerBody))
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201 on register, got %d", registerRec.Code)
	}

	loginBody := `{"username":"alice","password":"password123"}`
	loginReq := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected status 200 on login, got %d", loginRec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(loginRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse login response: %v", err)
	}
	token, ok := payload["token"].(string)
	if !ok || token == "" {
		t.Fatalf("token missing from login response: %v", payload)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+token)
	meRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("expected status 200 on /auth/me, got %d", meRec.Code)
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
	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
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
