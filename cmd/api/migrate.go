package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ldbl/sre/backend/pkg/config"
	"github.com/ldbl/sre/backend/pkg/migrations"
)

// runMigrate is `backend migrate`: apply the embedded migrations to the database the app would use
// (DATABASE_URL or POSTGRES_*), print what changed, exit. In Kubernetes it runs as the Deployment's
// migrate initContainer - the same image, so the same signed digest, as the app it prepares for.
// Exit code 0 when the schema is at the newest version this binary knows, 1 otherwise.
func runMigrate() int {
	databaseURL := config.DatabaseURLFromEnv()
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "migrate: no database configured (DATABASE_URL or POSTGRES_HOST/POSTGRES_USER)")
		return 1
	}
	// Generous: an initContainer that gives up too early only restarts, but a migration of a large
	// table may legitimately take minutes.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	before, after, err := migrations.Up(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		return 1
	}
	if before == after {
		fmt.Printf("migrate: schema already at version %d (this build needs %d)\n", after, migrations.RequiredVersion)
	} else {
		fmt.Printf("migrate: schema %d -> %d (this build needs %d)\n", before, after, migrations.RequiredVersion)
	}
	return 0
}
