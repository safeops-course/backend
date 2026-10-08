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

// postAuth posts body to path, requires the status, and returns the raw response body.
func postAuth(t *testing.T, srv *Server, path, body string, want int) []byte {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if rr.Code != want {
		t.Fatalf("%s: %d, want %d", path, rr.Code, want)
	}
	return rr.Body.Bytes()
}

// responseUser returns the "user" object of an auth response as raw JSON fields, so a test can tell
// a missing key from an empty value (the struct's omitempty would hide the difference).
func responseUser(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var resp struct {
		User map[string]json.RawMessage `json:"user"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.User == nil {
		t.Fatalf("no user object in the response")
	}
	return resp.User
}

// displayName returns the user's display_name, and whether the key is there at all.
func displayName(t *testing.T, raw []byte) (string, bool) {
	t.Helper()
	field, ok := responseUser(t, raw)["display_name"]
	if !ok {
		return "", false
	}
	var name string
	if err := json.Unmarshal(field, &name); err != nil {
		t.Fatal(err)
	}
	return name, true
}

// The display name is written on register either way; FEATURE_DISPLAY_NAME only decides whether it is
// read back - in the register and the login response - so switching the flag off hides it again
// without a deploy or a data change.
func TestDisplayNameFollowsTheFeatureFlag(t *testing.T) {
	register := `{"username":"ana","password":"verysecure123","display_name":"  Ana Petrova "}`
	login := `{"username":"ana","password":"verysecure123"}`

	// Flag off: the key is absent from the user object - not present and empty.
	off := newTestServer(t)
	for _, step := range []struct {
		path, body string
		status     int
	}{
		{"/auth/register", register, http.StatusCreated},
		{"/auth/login", login, http.StatusOK},
	} {
		raw := postAuth(t, off, step.path, step.body, step.status)
		if name, present := displayName(t, raw); present {
			t.Fatalf("flag off, %s: display_name key present (%q), want it absent", step.path, name)
		}
		if _, ok := responseUser(t, raw)["username"]; !ok {
			t.Fatalf("flag off, %s: username missing from the user object", step.path)
		}
	}

	// Flag on: the stored, trimmed name - from register, and read back from the store on login.
	on := newTestServer(t, func(c *config.Config) { c.FeatureDisplayName = true })
	for _, step := range []struct {
		path, body string
		status     int
	}{
		{"/auth/register", register, http.StatusCreated},
		{"/auth/login", login, http.StatusOK},
	} {
		raw := postAuth(t, on, step.path, step.body, step.status)
		if name, present := displayName(t, raw); !present || name != "Ana Petrova" {
			t.Fatalf("flag on, %s: display_name %q (present %v), want %q", step.path, name, present, "Ana Petrova")
		}
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
	// A NUL character cannot be stored in PostgreSQL text: refused as the client's input (400), not
	// left to fail in the INSERT (500) - in the display name and in the username.
	if _, err := store.createUser(ctx, "dan", "verysecure123", "Dan\x00"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("NUL in display_name: %v, want errInvalidInput", err)
	}
	if _, err := store.createUser(ctx, "e\x00ve", "verysecure123", ""); !errors.Is(err, errInvalidInput) {
		t.Fatalf("NUL in username: %v, want errInvalidInput", err)
	}
}

// TestRegisterWithNULIsTheClientsError: through the API a NUL arrives as \u0000 in the JSON.
func TestRegisterWithNULIsTheClientsError(t *testing.T) {
	srv := newTestServer(t)
	for _, body := range []string{
		`{"username":"fay","password":"verysecure123","display_name":"Fay\u0000"}`,
		`{"username":"g\u0000us","password":"verysecure123"}`,
	} {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", body, rr.Code)
		}
	}
}
