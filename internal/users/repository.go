package users

import (
	"context"
	"errors"
)

// ErrNotFound is returned when no user matches the lookup.
var ErrNotFound = errors.New("users: not found")

// ErrDuplicateEmail is returned by Create when the email is already
// registered (maps to the `users.email` UNIQUE constraint).
var ErrDuplicateEmail = errors.New("users: email already registered")

// Repository is the persistence contract for users, kept small and
// interface-based so handlers can be unit tested against a mock.
type Repository interface {
	// Create inserts a new user with the given email and bcrypt password
	// hash, returning the persisted row (including generated id/timestamps).
	Create(ctx context.Context, email, passwordHash string) (*User, error)

	// FindByEmail looks up a user by email, returning ErrNotFound if none
	// exists.
	FindByEmail(ctx context.Context, email string) (*User, error)
}
