// Package users provides the user model and repository shared by services
// that need to authenticate or look up application users.
package users

import "time"

// User mirrors the `users` table (infra/postgres/init/001_init.sql).
type User struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
