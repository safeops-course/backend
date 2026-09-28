package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func postJSON(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// A request the client must fix is a 400, not a 500 that feeds the error-rate alerts.
func TestRegisterInvalidInputIs400(t *testing.T) {
	srv := newTestServer(t)
	cases := map[string]string{
		"short password":  `{"username":"alice","password":"short"}`,
		"password > 72 B": `{"username":"alice","password":"` + strings.Repeat("p", 80) + `"}`,
		"short username":  `{"username":"ab","password":"verysecure123"}`,
		"long username":   `{"username":"` + strings.Repeat("u", 65) + `","password":"verysecure123"}`,
	}
	for name, body := range cases {
		rr := postJSON(t, srv, "/auth/register", body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d (%s), want 400", name, rr.Code, rr.Body.String())
		}
	}
}

func TestLoginWithOverlongPasswordIs401(t *testing.T) {
	srv := newTestServer(t)
	registerAndGetToken(t, srv)
	rr := postJSON(t, srv, "/auth/login", `{"username":"test-user","password":"`+strings.Repeat("p", 80)+`"}`)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rr.Code)
	}
}

// With every bcrypt slot busy, login and register answer 429 at once.
func TestBcryptSlotsFullGives429(t *testing.T) {
	srv := newTestServer(t)
	for i := 0; i < cap(srv.bcryptSem); i++ {
		srv.bcryptSem <- struct{}{}
	}
	for _, path := range []string{"/auth/login", "/auth/register"} {
		rr := postJSON(t, srv, path, `{"username":"alice","password":"verysecure123"}`)
		if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
			t.Errorf("%s with full slots: got %d, want 429 with Retry-After", path, rr.Code)
		}
	}
	for i := 0; i < cap(srv.bcryptSem); i++ {
		<-srv.bcryptSem
	}
	if rr := postJSON(t, srv, "/auth/register", `{"username":"alice","password":"verysecure123"}`); rr.Code != http.StatusCreated {
		t.Fatalf("after the slots free up: got %d, want 201", rr.Code)
	}
}

func signed(t *testing.T, method jwt.SigningMethod, claims jwt.Claims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Only HS256 tokens with an expiry are accepted, even when signed with the right secret.
func TestTokenMustBeHS256WithExpiry(t *testing.T) {
	srv := newTestServer(t)
	now := time.Now()
	valid := jwtCustomClaims{Name: "alice", RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "backend", Subject: "alice", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}}
	noExp := jwtCustomClaims{Name: "alice", RegisteredClaims: jwt.RegisteredClaims{Issuer: "backend", Subject: "alice"}}

	cases := map[string]struct {
		token string
		want  int
	}{
		"HS256 with exp": {signed(t, jwt.SigningMethodHS256, valid), http.StatusOK},
		"HS384":          {signed(t, jwt.SigningMethodHS384, valid), http.StatusUnauthorized},
		"HS512":          {signed(t, jwt.SigningMethodHS512, valid), http.StatusUnauthorized},
		"no exp":         {signed(t, jwt.SigningMethodHS256, noExp), http.StatusUnauthorized},
	}
	for name, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Errorf("%s: got %d, want %d", name, rr.Code, c.want)
		}
	}
}
