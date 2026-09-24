package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Migrate applies every pending goose migration in fsys (SQL files at its
// root, e.g. migrations.FS) and returns the schema version reached. It
// holds a PostgreSQL advisory lock while running, so concurrent runs (e.g.
// several replicas starting at once) apply each migration only once.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, log *slog.Logger) (int64, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return 0, fmt.Errorf("postgres: migrate: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fsys,
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return 0, fmt.Errorf("postgres: migrate: %w", err)
	}
	results, err := provider.Up(ctx)
	for _, r := range results {
		log.InfoContext(ctx, "migration applied",
			slog.String("migration", r.Source.Path), slog.Duration("duration", r.Duration))
	}
	if err != nil {
		return 0, fmt.Errorf("postgres: migrate: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("postgres: migrate: %w", err)
	}
	return version, nil
}
