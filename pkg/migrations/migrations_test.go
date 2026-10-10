package migrations

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"regexp"
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

// lockTimeoutLine is the first statement of every transactional migration file: its DDL waits at
// most that long for a table lock, so the running release's queries never queue behind it for longer.
var lockTimeoutLine = regexp.MustCompile(`^SET LOCAL lock_timeout = '[0-9]+s';$`)

// concurrentIndex is a statement that cannot run in a transaction - the one kind of file without
// lockTimeoutLine. Matched on the parsed statement, so a comment that mentions it does not count.
var concurrentIndex = regexp.MustCompile(`(?is)^(CREATE\s+(UNIQUE\s+)?INDEX|DROP\s+INDEX|REINDEX\s+\w+)\s+CONCURRENTLY\b`)

// statements returns a migration file's SQL statements: comment lines dropped, split on ";".
func statements(body string) []string {
	var sqlLines []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			sqlLines = append(sqlLines, line)
		}
	}
	var out []string
	for _, stmt := range strings.Split(strings.Join(sqlLines, "\n"), ";") {
		if stmt = strings.TrimSpace(stmt); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// TestEveryMigrationLimitsItsLockWait: a file runs as one transaction and starts with the SET LOCAL
// line - without it, its DDL would wait for a table lock as long as the connection's lock_timeout
// (lockWait, minutes), with every query on that table queued behind. The one exception is a file
// that cannot run in a transaction: CONCURRENTLY, alone in its file and without the line (a second
// statement would make the file a transaction, and Postgres refuses CONCURRENTLY inside one).
func TestEveryMigrationLimitsItsLockWait(t *testing.T) {
	entries, err := files.ReadDir("sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := files.ReadFile("sql/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		stmts := statements(string(body))
		if len(stmts) == 0 {
			t.Errorf("%s: no statements", e.Name())
			continue
		}
		concurrent := 0
		for _, stmt := range stmts {
			if concurrentIndex.MatchString(stmt) {
				concurrent++
			}
		}
		if concurrent > 0 {
			if len(stmts) != 1 {
				t.Errorf("%s: a CONCURRENTLY statement must be the only statement in its file (got %d) - "+
					"with more, the file runs as a transaction and Postgres refuses it", e.Name(), len(stmts))
			}
			continue
		}
		if !lockTimeoutLine.MatchString(stmts[0] + ";") {
			t.Errorf("%s: the first statement must be SET LOCAL lock_timeout = '5s'; (got %q)", e.Name(), stmts[0])
		}
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

// TestUpOnANewerSchemaChangesNothing: the image-rollback case. The next release migrated past this
// build's newest file; this build's migrate (the initContainer) must succeed without touching it -
// golang-migrate alone fails with "no migration found for version N".
func TestUpOnANewerSchemaChangesNothing(t *testing.T) {
	db, dsn := testDB(t)
	ctx := context.Background()
	if _, _, err := Up(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	const newer = RequiredVersion + 1
	if _, err := db.Exec(`UPDATE `+versionTable+` SET version = $1`, int64(newer)); err != nil {
		t.Fatal(err)
	}
	before, after, err := Up(ctx, dsn)
	if err != nil {
		t.Fatalf("Up on a newer schema: %v", err)
	}
	if before != newer || after != newer {
		t.Fatalf("Up on a newer schema: %d -> %d, want %d -> %d (unchanged)", before, after, newer, newer)
	}
	var v int64
	if err := db.QueryRow(`SELECT version FROM ` + versionTable).Scan(&v); err != nil || v != int64(newer) {
		t.Fatalf("schema version after Up: %d (%v), want %d", v, err, newer)
	}
}

// TestUpOnANewerDirtySchemaFails: a newer schema that is dirty is a failed migration of the next
// release - never skipped.
func TestUpOnANewerDirtySchemaFails(t *testing.T) {
	db, dsn := testDB(t)
	ctx := context.Background()
	if _, _, err := Up(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE `+versionTable+` SET version = $1, dirty = true`, int64(RequiredVersion)+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Up(ctx, dsn); err == nil {
		t.Fatal("Up skipped a dirty newer schema")
	}
}

// TestUpGivesUpOnABusyTable: the running release holds a lock on app_users (an open transaction that
// read it) while migration 0002 needs to ALTER it. The migration must give up after its own 5 s
// lock_timeout - not the connection's lockWait - and, run as one transaction, change nothing: the
// column is still missing, and the version is left dirty for a person to clear.
func TestUpGivesUpOnABusyTable(t *testing.T) {
	db, dsn := testDB(t)
	ctx := context.Background()
	if _, _, err := Up(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	// Back to version 1: 0002 is pending again.
	if _, err := db.Exec(`ALTER TABLE app_users DROP COLUMN display_name`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ` + versionTable + ` SET version = 1`); err != nil {
		t.Fatal(err)
	}
	reader, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback() }()
	if _, err := reader.Exec(`SELECT count(*) FROM app_users`); err != nil { // holds ACCESS SHARE until rollback
		t.Fatal(err)
	}

	start := time.Now()
	_, _, err = Up(ctx, dsn)
	took := time.Since(start)
	if err == nil {
		t.Fatal("Up altered a table another transaction was reading")
	}
	if took > 30*time.Second {
		t.Fatalf("Up gave up after %s, want about 5 s (the file's lock_timeout, not lockWait)", took)
	}
	_ = reader.Rollback()

	var version int64
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM `+versionTable).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 2 || !dirty {
		t.Fatalf("after the failed migration: version %d dirty %t, want 2 dirty", version, dirty)
	}
	var hasColumn bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'app_users' AND column_name = 'display_name')`).Scan(&hasColumn); err != nil {
		t.Fatal(err)
	}
	if hasColumn {
		t.Fatal("the failed migration left display_name behind - it did not run as one transaction")
	}
	t.Logf("gave up after %s: %v", took.Round(time.Millisecond), err)
}

// TestConcurrentIndexNeedsAFileOfItsOwn: how the driver sends a file decides what it may contain.
// One Exec with no arguments is one simple-protocol query; with two statements Postgres runs it as
// one transaction, and refuses CREATE INDEX CONCURRENTLY inside it. Alone - comments allowed - it runs.
func TestConcurrentIndexNeedsAFileOfItsOwn(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE concurrent_check (id int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "SET LOCAL lock_timeout = '5s';\nCREATE INDEX CONCURRENTLY concurrent_check_a ON concurrent_check (id);"); err == nil ||
		!strings.Contains(err.Error(), "cannot run inside a transaction block") {
		t.Fatalf("CONCURRENTLY after SET LOCAL in one Exec: %v, want 'cannot run inside a transaction block'", err)
	}
	if _, err := db.ExecContext(ctx, "-- an index of its own\nCREATE INDEX CONCURRENTLY concurrent_check_b ON concurrent_check (id);"); err != nil {
		t.Fatalf("CONCURRENTLY alone in one Exec: %v", err)
	}
}
