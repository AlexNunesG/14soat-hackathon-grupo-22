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

// Pinger is the part of a connection pool WaitReady needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// WaitReady pings db every interval until it answers or timeout elapses,
// and returns the last error on timeout. It lets one-shot commands such as
// `api migrate` start while the database is still coming up (for example
// while the postgres image runs its initialization server).
func WaitReady(ctx context.Context, db Pinger, timeout, interval time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		err := db.Ping(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("postgres: not ready after %s: %w", timeout, err)
		case <-time.After(interval):
		}
	}
}
