// Package postgres holds the PostgreSQL adapters (pgx): the connection pool
// (whose Ping backs the api's /readyz), the schema migrations (goose, see
// docs/database.md) and the user and video repositories.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool returns a pgx connection pool for the DSN
// (postgres://user:pass@host:port/db?sslmode=...). Connections are opened
// lazily, so the database need not be up yet; use Ping to check it.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: invalid DSN: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return pool, nil
}
