package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ldbl/sre/backend/pkg/config"
)

// These tests guard the fixes from the 2026-09-27 security review. Each one fails if a public
// leak or bypass comes back; change them only together with a reviewed security decision.

func TestEnvReturnsOnlyAllowlistedKeys(t *testing.T) {
	secrets := map[string]string{
		"JWT_SECRET":                 "jwt-value-must-not-leak",
		"POSTGRES_PASSWORD":          "db-password-must-not-leak",
		"UPTRACE_DSN":                "https://dsn-token-must-not-leak@uptrace.example/1",
		"OTEL_EXPORTER_OTLP_HEADERS": "uptrace-dsn=header-must-not-leak",
		"SOME_FUTURE_API_KEY":        "future-key-must-not-leak",
		"BACKEND_SERVICE_HOST":       "10.96.0.42",
	}
	for key, value := range secrets {
		t.Setenv(key, value)
	}
	t.Setenv("NAMESPACE", "develop")
	t.Setenv("FEATURE_LOGIN_V2", "false")

	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/env", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	body := rr.Body.String()
	for key, value := range secrets {
		// Never print the body: on failure it holds the real environment of the test process (CI too).
		if strings.Contains(body, key) || strings.Contains(body, value) {
			t.Fatalf("/env leaked %s", key)
		}
	}
	for _, want := range []string{`"NAMESPACE":"develop"`, `"FEATURE_LOGIN_V2":"false"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("/env is missing allowlisted %s", want)
		}
	}
}

// TestRouteInventory fails when a route is added or removed without updating this list, so a new
// debug endpoint cannot reach the internet by accident. Review what a new route exposes, then add it.
func TestRouteInventory(t *testing.T) {
	base := []string{
		"GET /",
		"GET /configs",
		"GET /delay/{seconds}",
		"GET /env",
		"GET /error",
		"GET /error/{level}",
		"GET /headers",
		"GET /healthz",
		"GET /livez",
		"GET /metrics",
		"GET /openapi",
		"GET /readyz",
		"GET /status/{code}",
		"GET /swagger/*",
		"GET /token/validate",
		"GET /version",
		"GET /auth/me",
		"POST /auth/login",
		"POST /auth/register",
		"POST /echo",
	}
	chaos := []string{
		"GET /panic",
		"PUT /livez/disable",
		"PUT /livez/enable",
		"PUT /readyz/disable",
		"PUT /readyz/enable",
	}

	cases := []struct {
		name    string
		options []func(*config.Config)
		want    []string
	}{
		{name: "chaos off (default)", want: base},
		{name: "chaos on", options: []func(*config.Config){withChaos}, want: append(slices.Clone(base), chaos...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, tc.options...)
			var got []string
			err := chi.Walk(srv.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
				got = append(got, method+" "+route)
				return nil
			})
			if err != nil {
				t.Fatalf("walk routes: %v", err)
			}
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("route inventory changed\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

func TestNoEndpointIssuesTokensWithoutPassword(t *testing.T) {
	srv := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/token", strings.NewReader("admin")))
	if rr.Code == http.StatusOK || strings.Contains(rr.Body.String(), "token") {
		t.Fatalf("POST /token must not issue tokens, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestPprofIsNotOnThePublicRouter(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/cmdline"} {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s on the public router: expected 404, got %d", path, rr.Code)
		}
	}

	rr := httptest.NewRecorder()
	PprofHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("PprofHandler index: expected 200, got %d", rr.Code)
	}
}

func TestChaosEndpointsDoNotExistByDefault(t *testing.T) {
	srv := newTestServer(t)
	token := registerAndGetToken(t, srv)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/panic"},
		{http.MethodPut, "/readyz/disable"},
		{http.MethodPut, "/readyz/enable"},
		{http.MethodPut, "/livez/disable"},
		{http.MethodPut, "/livez/enable"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound && rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s with chaos off: expected 404/405, got %d", tc.method, tc.path, rr.Code)
		}
	}
}

func TestDelayRejectsOutOfRangeValues(t *testing.T) {
	srv := newTestServer(t, func(cfg *config.Config) { cfg.DelayMaxSeconds = 1 })

	for _, seconds := range []string{"1.5", "99999", "-1", "NaN", "Inf", "+Inf", "abc"} {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/delay/"+seconds, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("/delay/%s: expected 400, got %d", seconds, rr.Code)
		}
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/delay/0", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/delay/0: expected 200, got %d", rr.Code)
	}
}

func TestDelayStopsWhenTheClientDisconnects(t *testing.T) {
	srv := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/delay/10", nil).WithContext(ctx)

	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("handler kept sleeping after the client left: %s", elapsed)
	}
}

func TestEchoNeverReflectsTheRequestContentType(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("<script>alert(1)</script>"))
	req.Header.Set("Content-Type", "text/html")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("expected application/octet-stream, got %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}
}

func TestNoCORSHeaders(t *testing.T) {
	srv := newTestServer(t)
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		req := httptest.NewRequest(method, "/healthz", nil)
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%s: unexpected Access-Control-Allow-Origin %q", method, got)
		}
	}
}

func TestLoginIsRateLimitedPerUsername(t *testing.T) {
	srv := newTestServer(t, func(cfg *config.Config) { cfg.LoginAttemptsPerMinute = 3 })

	login := func(username string) *httptest.ResponseRecorder {
		body := `{"username":"` + username + `","password":"wrong-password"}`
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body)))
		return rr
	}

	for i := range 3 {
		if rr := login("victim"); rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, rr.Code)
		}
	}
	rr := login("VICTIM") // same user, different case
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("4th attempt: expected 429, got %d", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatalf("429 without Retry-After")
	}
	if rr := login("someone-else"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("another username must not be limited, got %d", rr.Code)
	}
}

func TestRegistrationIsRateLimited(t *testing.T) {
	srv := newTestServer(t, func(cfg *config.Config) { cfg.RegistrationsPerMinute = 2 })

	codes := make([]int, 0, 3)
	for _, username := range []string{"user-one", "user-two", "user-three"} {
		body := `{"username":"` + username + `","password":"verysecure123"}`
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body)))
		codes = append(codes, rr.Code)
	}
	want := []int{http.StatusCreated, http.StatusCreated, http.StatusTooManyRequests}
	if !slices.Equal(codes, want) {
		t.Fatalf("expected %v, got %v", want, codes)
	}
}

func TestRegistrationCanBeDisabled(t *testing.T) {
	srv := newTestServer(t, func(cfg *config.Config) { cfg.RegistrationEnabled = false })
	body := `{"username":"alice","password":"verysecure123"}`
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body)))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with registration disabled, got %d", rr.Code)
	}
}

func TestFixedWindowLimiter(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	limiter := newFixedWindowLimiter(2)
	limiter.now = func() time.Time { return now }

	for i := range 2 {
		if allowed, _ := limiter.allow("k"); !allowed {
			t.Fatalf("event %d should be allowed", i+1)
		}
	}
	allowed, retryAfter := limiter.allow("k")
	if allowed || retryAfter <= 0 || retryAfter > time.Minute {
		t.Fatalf("3rd event: expected refusal with 0 < retryAfter <= 1m, got %v %s", allowed, retryAfter)
	}

	now = now.Add(time.Minute)
	if allowed, _ := limiter.allow("k"); !allowed {
		t.Fatalf("a new window should allow the key again")
	}
}

// A full limiter must keep admitting new keys (no login lockout for everyone), evicting the oldest
// window, while keys that are still tracked keep their limit.
func TestFixedWindowLimiterEvictsOldestWhenFull(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	limiter := newFixedWindowLimiter(1)
	limiter.now = func() time.Time { return now }

	for i := range limiterMaxKeys {
		limiter.allow("key-" + strconv.Itoa(i))
		now = now.Add(time.Millisecond) // key-0 is the oldest window
	}

	if allowed, _ := limiter.allow("new-user"); !allowed {
		t.Fatalf("a full limiter must still admit a new key")
	}
	if len(limiter.windows) != limiterMaxKeys {
		t.Fatalf("map must stay bounded at %d, got %d", limiterMaxKeys, len(limiter.windows))
	}
	if _, stillTracked := limiter.windows["key-0"]; stillTracked {
		t.Fatalf("the oldest window (key-0) should have been evicted")
	}
	lastKey := "key-" + strconv.Itoa(limiterMaxKeys-1)
	if allowed, _ := limiter.allow(lastKey); allowed {
		t.Fatalf("a tracked key must keep its limit while the map is full")
	}
}
