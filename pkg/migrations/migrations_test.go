package migrations

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4/database"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestRequiredVersionIsNewestFile: a new migration file without a raised RequiredVersion would let
// the new code start on a schema that lacks what it needs.
func TestRequiredVersionIsNewestFile(t *testing.T) {
	entries, err := files.ReadDir("sql")
	if err != nil {
		t.Fatal(err)
	}
	var newest uint64
	for _, e := range entries {
		n, err := strconv.ParseUint(strings.SplitN(e.Name(), "_", 2)[0], 10, 32)
		if err != nil {
			t.Fatalf("%s: a migration file starts with its number: %v", e.Name(), err)
		}
		if !strings.HasSuffix(e.Name(), ".up.sql") {
			t.Fatalf("%s: only .up.sql files - schemas roll forward, never back", e.Name())
		}
		if n > newest {
			newest = n
		}
	}
	if uint(newest) != RequiredVersion {
		t.Fatalf("newest migration is %d, RequiredVersion is %d - raise it with the new file", newest, RequiredVersion)
	}
}

// testDB returns a fresh, empty database for one test, or skips when TEST_DATABASE_URL is unset (CI
// sets it to a Postgres service; locally: docker run postgres:17, see the backend README).
func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set - integration test against a real Postgres")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := "mig_" + strings.ToLower(strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + name); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
	})
	return db, u.String()
}

func TestUpOnEmptyDatabase(t *testing.T) {
	db, dsn := testDB(t)
	ctx := context.Background()
	if err := Check(ctx, db); err == nil {
		t.Fatal("Check passed on a database that was never migrated")
	}
	before, after, err := Up(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if before != 0 || after != RequiredVersion {
		t.Fatalf("Up: %d -> %d, want 0 -> %d", before, after, RequiredVersion)
	}
	if err := Check(ctx, db); err != nil {
		t.Fatalf("Check after Up: %v", err)
	}
	// A second run changes nothing - every initContainer start runs it.
	before, after, err = Up(ctx, dsn)
	if err != nil || before != after {
		t.Fatalf("second Up: %d -> %d, %v", before, after, err)
	}
}

// TestUpKeepsAnExistingTable: every environment had app_users before versioned migrations. Version 1
// must adopt it without touching a row.
func TestUpKeepsAnExistingTable(t *testing.T) {
	db, dsn := testDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE app_users (id BIGSERIAL PRIMARY KEY, username VARCHAR(64) NOT NULL,
		password_hash TEXT NOT NULL, password_salt TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
		INSERT INTO app_users (username, password_hash) VALUES ('existing', 'x');`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Up(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM app_users WHERE username = 'existing'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("existing row after Up: count=%d err=%v", n, err)
	}
}

func TestCheckRefusesADirtySchema(t *testing.T) {
	db, dsn := testDB(t)
	ctx := context.Background()
	if _, _, err := Up(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ` + versionTable + ` SET dirty = true`); err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, db); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("Check on a dirty schema: %v", err)
	}
}

// TestCheckAcceptsANewerSchema: after the next release migrated, this release must still start -
// that is what makes rolling back the image safe.
func TestCheckAcceptsANewerSchema(t *testing.T) {
	db, dsn := testDB(t)
	ctx := context.Background()
	if _, _, err := Up(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE `+versionTable+` SET version = $1`, int64(RequiredVersion)+1); err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, db); err != nil {
		t.Fatalf("Check on a newer schema: %v", err)
	}
}

// TestUpGivesUpOnAHeldLock: another session holds the migration lock and never lets go (a stuck
// migrator). Up must fail within lockWait - cancelled by Postgres - and return, not hang in Close.
func TestUpGivesUpOnAHeldLock(t *testing.T) {
	db, dsn := testDB(t)
	ctx := context.Background()
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	id, err := database.GenerateAdvisoryLockId(name, "public", versionTable)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, id); err != nil {
		t.Fatal(err)
	}

	saved := lockWait
	lockWait = 2 * time.Second
	defer func() { lockWait = saved }()

	start := time.Now()
	_, _, err = Up(ctx, dsn)
	if err == nil {
		t.Fatal("Up succeeded while another session held the migration lock")
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("Up returned after %s, want about lockWait (2s)", took)
	}
}
