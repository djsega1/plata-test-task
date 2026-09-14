//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrations_Stairway is the stairway pattern: for each migration, up,
// down to just before it, up again — before moving to the next one.
func TestMigrations_Stairway(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := t.Context()

	db, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, "DROP TABLE IF EXISTS quote_updates, quotes, goose_db_version")
	require.NoError(t, err)

	provider, err := newMigrationProvider(db)
	require.NoError(t, err)

	sources := provider.ListSources()
	require.NotEmpty(t, sources, "no migration files found under migrations/")

	var floor int64
	for _, src := range sources {
		_, err := provider.ApplyVersion(ctx, src.Version, true)
		require.NoErrorf(t, err, "up %d", src.Version)

		_, err = provider.DownTo(ctx, floor)
		require.NoErrorf(t, err, "down to %d", floor)

		_, err = provider.ApplyVersion(ctx, src.Version, true)
		require.NoErrorf(t, err, "up %d again", src.Version)

		floor = src.Version
	}
	assertTablesExist(t, ctx, db, true)
}

func assertTablesExist(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{"quotes", "quote_updates"} {
		var exists bool
		err := db.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)", table,
		).Scan(&exists)
		require.NoError(t, err)
		assert.Equal(t, want, exists, "table %q existence", table)
	}
}

// TestMigrate_ConcurrentReplicasAreSafe: without the session locker, this
// reliably fails with "relation already exists" (verified by temporarily
// dropping WithSessionLocker).
func TestMigrate_ConcurrentReplicasAreSafe(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := t.Context()

	db, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, "DROP TABLE IF EXISTS quote_updates, quotes, goose_db_version")
	require.NoError(t, err)

	const numReplicas = 5
	var wg sync.WaitGroup
	errs := make([]error, numReplicas)
	for i := range numReplicas {
		wg.Go(func() {
			errs[i] = Migrate(ctx, databaseURL)
		})
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "replica %d", i)
	}

	// goose records a baseline version_id=0 row plus one per applied
	// migration; version 1 must appear exactly once, not once per replica.
	var timesApplied int
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT count(*) FROM goose_db_version WHERE version_id = 1",
	).Scan(&timesApplied))
	assert.Equal(t, 1, timesApplied)

	assertTablesExist(t, ctx, db, true)
}
