package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/ldbl/sre/backend/pkg/config"
	"github.com/ldbl/sre/backend/pkg/migrations"
)

// registerUser posts /auth/register and returns the decoded response.
func registerUser(t *testing.T, srv *Server, body string) authResponse {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}
	var resp authResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// The display name is written on register either way; FEATURE_DISPLAY_NAME only decides whether it is
// read back - so switching the flag off hides it again without a deploy or a data change.
func TestDisplayNameFollowsTheFeatureFlag(t *testing.T) {
	body := `{"username":"ana","password":"verysecure123","display_name":"  Ana Petrova "}`

	off := newTestServer(t)
	if got := registerUser(t, off, body).User.DisplayName; got != "" {
		t.Fatalf("flag off: display_name %q in the response, want none", got)
	}

	on := newTestServer(t, func(c *config.Config) { c.FeatureDisplayName = true })
	if got := registerUser(t, on, body).User.DisplayName; got != "Ana Petrova" {
		t.Fatalf("flag on: display_name %q, want %q (trimmed)", got, "Ana Petrova")
	}
}

func TestDisplayNameTooLongIsTheClientsError(t *testing.T) {
	srv := newTestServer(t)
	body := `{"username":"bob","password":"verysecure123","display_name":"` + strings.Repeat("é", 65) + `"}`
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("65-character display_name: %d, want 400", rr.Code)
	}
}

// TestPostgresStoreDisplayName runs the store against a real, migrated Postgres (skipped without
// TEST_DATABASE_URL; CI sets it): the column round-trips, and a user without a display name reads as
// empty - the rows written before 0002, and by the previous release, have NULL there.
func TestPostgresStoreDisplayName(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set - integration test against a real Postgres")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	const name = "server_display_name"
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`) }()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	ctx := context.Background()
	if _, _, err := migrations.Up(ctx, u.String()); err != nil {
		t.Fatal(err)
	}

	store, err := newPostgresUserStore(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.createUser(ctx, "ana", "verysecure123", "Ana Petrova"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.createUser(ctx, "bob", "verysecure123", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := store.authenticate(ctx, "ana", "verysecure123"); err != nil || got.DisplayName != "Ana Petrova" {
		t.Fatalf("ana: %q, %v", got.DisplayName, err)
	}
	if got, err := store.authenticate(ctx, "bob", "verysecure123"); err != nil || got.DisplayName != "" {
		t.Fatalf("bob (no display name, NULL in the column): %q, %v", got.DisplayName, err)
	}
	var stored sql.NullString
	if err := store.db.QueryRow(`SELECT display_name FROM app_users WHERE username = 'bob'`).Scan(&stored); err != nil || stored.Valid {
		t.Fatalf("bob's display_name should be NULL, got %+v (%v)", stored, err)
	}
	if _, err := store.createUser(ctx, "cid", "verysecure123", strings.Repeat("x", 65)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("65 characters: %v, want errInvalidInput", err)
	}
}
