package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver, for goose only
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Migrate applies every pending migration, serialized across replicas by a
// Postgres session advisory lock.
func Migrate(ctx context.Context, databaseURL string) (err error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("postgres: open for migration: %w", err)
	}

	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(
				err,
				fmt.Errorf("postgres: close migration connection: %w", closeErr),
			)
		}
	}()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("postgres: new session locker: %w", err)
	}

	provider, err := newMigrationProvider(db, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}

	return nil
}

func newMigrationProvider(db *sql.DB, opts ...goose.ProviderOption) (*goose.Provider, error) {
	migrationsDir, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("postgres: migrations subdir: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrationsDir, opts...)
	if err != nil {
		return nil, fmt.Errorf("postgres: new migration provider: %w", err)
	}
	return provider, nil
}
