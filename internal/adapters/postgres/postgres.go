package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is what the repositories need from the database: satisfied by
// *pgxpool.Pool, *pgx.Conn and pgx.Tx, so a repository can also run inside
// a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB is a Querier that can also open transactions: satisfied by
// *pgxpool.Pool, *pgx.Conn and pgx.Tx (a nested Begin is a savepoint).
type DB interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

// SQLSTATE codes the repositories handle
// (https://www.postgresql.org/docs/current/errcodes-appendix.html).
const (
	uniqueViolation = "23505"
)

// pgErrorCode returns the SQLSTATE of err and the constraint it names, or
// empty strings when err is not a PostgreSQL error.
func pgErrorCode(err error) (code, constraint string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName
	}
	return "", ""
}

// nullString maps "" to NULL.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullInt maps 0 to NULL.
func nullInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// deref returns *p, or the zero value when p is nil (NULL).
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
