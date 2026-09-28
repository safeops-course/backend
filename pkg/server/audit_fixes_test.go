package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/ldbl/sre/backend/pkg/config"
)

func get(t *testing.T, srv *Server, path string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// Chaos must not reach the kubelet's probes or Prometheus.
func TestChaosSparesProbesAndMetrics(t *testing.T) {
	srv := newTestServer(t, func(c *config.Config) { c.RandomErrorRate = 1.0 })
	for _, path := range []string{"/healthz", "/readyz", "/livez", "/metrics"} {
		if rr := get(t, srv, path); rr.Code != http.StatusOK {
			t.Errorf("%s with RANDOM_ERROR_RATE=1: got %d, want 200", path, rr.Code)
		}
	}
	if rr := get(t, srv, "/version"); rr.Code != http.StatusInternalServerError {
		t.Errorf("/version with RANDOM_ERROR_RATE=1: got %d, want 500 (chaos still applies to the app)", rr.Code)
	}
}

// /configs shows key names and a hash, never a value (CONFIG_PATH may be a Secret).
func TestConfigsHidesValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api-key"), []byte("s3cret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, func(c *config.Config) { c.ConfigPath = dir })
	var body string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		body = get(t, srv, "/configs").Body.String()
		if strings.Contains(body, "api-key") {
			break
		}
	}
	if !strings.Contains(body, `"api-key"`) || !strings.Contains(body, `"fingerprint"`) {
		t.Fatalf("expected the key with a fingerprint, got %s", body)
	}
	if strings.Contains(body, "s3cret-value") {
		t.Fatalf("/configs leaked a value: %s", body)
	}
	// Nothing an outsider can check a guess against: no plain hash, no length.
	plain := sha256.Sum256([]byte("s3cret-value"))
	if strings.Contains(body, hex.EncodeToString(plain[:])[:12]) || strings.Contains(body, `"bytes"`) {
		t.Fatalf("/configs exposes value-derived data an outsider can verify: %s", body)
	}
}

// The jwt library's reason stays in the log.
func TestTokenValidateGenericError(t *testing.T) {
	srv := newTestServer(t)
	rr := get(t, srv, "/token/validate", "Authorization", "Bearer bad.token.here")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rr.Code)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "invalid or expired token" {
		t.Fatalf("client sees %q, want the generic message", got)
	}
}

// Inside Kubernetes, no database must stop the process instead of using a per-pod file.
func TestNoDatabaseInKubernetesIsFatal(t *testing.T) {
	if os.Getenv("BACKEND_FATAL_CHILD") == "1" {
		newTestServer(t) // no DatabaseURL; must exit before returning
		return
	}
	// Re-runs this test binary itself (os.Args[0]) with fixed arguments: no external input.
	cmd := exec.Command(os.Args[0], "-test.run=^TestNoDatabaseInKubernetesIsFatal$") //nolint:gosec // self re-exec, fixed args
	cmd.Env = append(os.Environ(), "BACKEND_FATAL_CHILD=1", "KUBERNETES_SERVICE_HOST=10.0.0.1")
	out, err := cmd.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.Success() {
		t.Fatalf("expected a non-zero exit, got err=%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "refusing the per-pod file auth store") {
		t.Fatalf("expected the fatal message, got:\n%s", out)
	}
}

// Outside Kubernetes the file store still works (local development).
func TestNoDatabaseOutsideKubernetesUsesFileStore(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	srv := newTestServer(t)
	if srv.users == nil {
		t.Fatal("expected the file auth store")
	}
}

// Each replica gets a fixed share of Postgres' connections.
func TestPoolIsLimited(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://user:pass@127.0.0.1:1/db")
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg) // does not connect until used
	defer func() { _ = db.Close() }()
	limitPool(db)
	if got := db.Stats().MaxOpenConnections; got != maxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, maxOpenConns)
	}
}
