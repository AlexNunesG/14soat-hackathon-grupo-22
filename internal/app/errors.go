package app

import (
	"errors"
	"fmt"
)

// Errors of the use cases and repositories. Adapters map them to their
// protocol (e.g. HTTP status codes).
var (
	// ErrNotFound is wrapped when a user or a video does not exist (or is
	// not visible to the caller).
	ErrNotFound = errors.New("not found")

	// ErrEmailTaken is wrapped when registering an e-mail that already
	// exists (case-insensitive).
	ErrEmailTaken = errors.New("e-mail is already registered")

	// ErrInvalidCredentials is returned by Login for an unknown e-mail and
	// for a wrong password alike.
	ErrInvalidCredentials = errors.New("invalid e-mail or password")

	// ErrInvalidToken is wrapped when an access token is malformed, badly
	// signed, expired or otherwise unacceptable.
	ErrInvalidToken = errors.New("invalid access token")

	// ErrInvalidInput is wrapped by *ValidationError.
	ErrInvalidInput = errors.New("invalid input")
)

// ValidationError reports an invalid field of a use case input. It wraps
// ErrInvalidInput.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// Unwrap makes errors.Is(err, ErrInvalidInput) true.
func (e *ValidationError) Unwrap() error { return ErrInvalidInput }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}
