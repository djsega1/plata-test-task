package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a pgx connection pool and verifies it with a ping.
// maxConns/maxConnLifetime/healthCheckPeriod each override pgxpool's default
// when positive; 0 leaves the default in place. The caller decides the
// numbers, not this package.
func NewPool(ctx context.Context, databaseURL string, maxConns int, maxConnLifetime, healthCheckPeriod time.Duration) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse pool config: %w", err)
	}
	if maxConns > 0 {
		poolCfg.MaxConns = int32(maxConns)
	}
	if maxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = maxConnLifetime
	}
	if healthCheckPeriod > 0 {
		poolCfg.HealthCheckPeriod = healthCheckPeriod
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: new pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}
