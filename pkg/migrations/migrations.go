// Package migrations owns the database schema: versioned SQL files embedded in the binary, applied
// by `backend migrate` (a Kubernetes initContainer, before the app starts), and checked by the app
// at startup.
//
// Rules this package exists to keep (Chapter 18):
//   - Only "up" migrations. A schema is never rolled back; a mistake is fixed by the next migration
//     (roll forward). There are no .down.sql files on purpose.
//   - Every migration is compatible with the previous release's code (expand / contract): add before
//     use, stop using before remove. Then rolling back the image never needs a rollback of the data.
//   - The app needs at least RequiredVersion and accepts a newer schema: the previous release must
//     keep running after the next release migrated - that is what makes an image rollback safe.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxv5 "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed sql/*.up.sql
var files embed.FS

// RequiredVersion is the schema version this build's code needs: the number of its newest
// migration file. migrations_test.go fails when a new file is added and this is not raised.
const RequiredVersion uint = 1

// versionTable is golang-migrate's bookkeeping table: one row, the version and a dirty flag.
const versionTable = "schema_migrations"

// Up applies every migration this binary embeds that the database does not have yet. golang-migrate
// holds a Postgres advisory lock while it runs, so replicas starting together migrate once. It
// opens its own connection pool to databaseURL and closes it; it returns the version before and after.
func Up(ctx context.Context, databaseURL string) (before, after uint, err error) {
	src, err := iofs.New(files, "sql")
	if err != nil {
		return 0, 0, fmt.Errorf("read embedded migrations: %w", err)
	}
	pgxCfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return 0, 0, fmt.Errorf("parse database URL: %w", err)
	}
	db := stdlib.OpenDB(*pgxCfg) // closed by m.Close() below, through the driver
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return 0, 0, fmt.Errorf("connect: %w", err)
	}
	driver, err := pgxv5.WithInstance(db, &pgxv5.Config{MigrationsTable: versionTable})
	if err != nil {
		_ = db.Close()
		return 0, 0, fmt.Errorf("migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return 0, 0, fmt.Errorf("migrator: %w", err)
	}
	// Closes the source and the connection pool opened above; nothing useful to do with its errors
	// once the migration itself has a result.
	defer func() { _, _ = m.Close() }()

	before, _, err = versionOf(m)
	if err != nil {
		return 0, 0, err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return before, 0, fmt.Errorf("migrate up from version %d: %w", before, err)
	}
	after, dirty, err := versionOf(m)
	if err != nil {
		return before, 0, err
	}
	if dirty {
		return before, after, fmt.Errorf("schema version %d is dirty: a migration failed half-way - fix it by hand, then mark it clean", after)
	}
	return before, after, nil
}

// versionOf reads the current version; a database that was never migrated is version 0.
func versionOf(m *migrate.Migrate) (uint, bool, error) {
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read schema version: %w", err)
	}
	return v, dirty, nil
}

// Check makes sure the schema can serve this build: migrated (schema_migrations exists), at least
// RequiredVersion, and not dirty. A newer version is fine - the expand/contract rule says this code
// must keep working on it. The app calls this at startup and refuses to start otherwise.
func Check(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, versionTable).Scan(&exists); err != nil {
		return fmt.Errorf("look for %s: %w", versionTable, err)
	}
	if !exists {
		return fmt.Errorf("the database was never migrated (no %s table): run `backend migrate` first - in Kubernetes the migrate initContainer does", versionTable)
	}
	var version int64
	var dirty bool
	err := db.QueryRowContext(ctx, `SELECT version, dirty FROM `+versionTable+` LIMIT 1`).Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s is empty: run `backend migrate` first", versionTable)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", versionTable, err)
	}
	if dirty {
		return fmt.Errorf("schema version %d is dirty: a migration failed half-way", version)
	}
	if version < int64(RequiredVersion) {
		return fmt.Errorf("schema version %d is older than this build needs (%d): run `backend migrate`", version, RequiredVersion)
	}
	return nil
}
