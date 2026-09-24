package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// usersEmailKey is the unique index on users.email (db/migrations).
const usersEmailKey = "users_email_key"

// Users is the PostgreSQL app.UserRepository.
type Users struct {
	db Querier
}

var _ app.UserRepository = (*Users)(nil)

// NewUsers returns the user repository over db.
func NewUsers(db Querier) *Users { return &Users{db: db} }

const userColumns = `id, name, email, password_hash, created_at`

// Create inserts u. A duplicate e-mail yields an error wrapping
// app.ErrEmailTaken.
func (r *Users) Create(ctx context.Context, u *domain.User) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO users (`+userColumns+`) VALUES ($1, $2, $3, $4, $5)`,
		u.ID, u.Name, u.Email, u.PasswordHash, u.CreatedAt)
	if err != nil {
		if code, constraint := pgErrorCode(err); code == uniqueViolation && constraint == usersEmailKey {
			return fmt.Errorf("postgres: insert user: %w", app.ErrEmailTaken)
		}
		return fmt.Errorf("postgres: insert user: %w", err)
	}
	return nil
}

// GetByEmail returns the user with the e-mail (already normalized), or an
// error wrapping app.ErrNotFound.
func (r *Users) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	return r.get(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email)
}

// GetByID returns the user with the id, or an error wrapping
// app.ErrNotFound (also for a malformed id).
func (r *Users) GetByID(ctx context.Context, id string) (*domain.User, error) {
	if !validUUID(id) {
		return nil, fmt.Errorf("postgres: user %q: %w", id, app.ErrNotFound)
	}
	return r.get(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
}

func (r *Users) get(ctx context.Context, query string, arg any) (*domain.User, error) {
	var u domain.User
	err := r.db.QueryRow(ctx, query, arg).Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("postgres: user: %w", app.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: select user: %w", err)
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return &u, nil
}
