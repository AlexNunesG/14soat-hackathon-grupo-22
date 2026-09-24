package app

import (
	"errors"
	"fmt"

	"video-processor/internal/domain"
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
	// ErrMissingFile is returned by an upload without any file.
	ErrMissingFile = errors.New("no file to upload")
	// ErrVideoNotReady is wrapped by *NotReadyError.
	ErrVideoNotReady = errors.New("video is not ready for download")
)

// NotReadyError reports a download of a video that is not DONE. It wraps
// ErrVideoNotReady.
type NotReadyError struct {
	Status domain.VideoStatus
}

func (e *NotReadyError) Error() string {
	return fmt.Sprintf("video is not ready for download (status: %s)", e.Status)
}

// Unwrap makes errors.Is(err, ErrVideoNotReady) true.
func (e *NotReadyError) Unwrap() error { return ErrVideoNotReady }

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
