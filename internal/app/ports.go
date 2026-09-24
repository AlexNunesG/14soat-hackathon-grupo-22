// Package app holds the use cases of the video processor and the ports
// (interfaces) they need from the outside world. Adapters under
// internal/adapters implement the ports; cmd/* wires them together.
package app

import (
	"context"
	"errors"
	"io"
	"time"

	"video-processor/internal/domain"
)

var (
	// ErrObjectNotFound is returned by ObjectStorage.Get when the key does
	// not exist.
	ErrObjectNotFound = errors.New("object not found")

	// ErrUnprocessableVideo is wrapped by FrameExtractor errors caused by the
	// input itself (undecodable, no video stream, no frames): retrying does
	// not help, the video should end FAILED.
	ErrUnprocessableVideo = errors.New("unprocessable video")
)

// FrameExtractor turns a video file into PNG frames at 1 frame per second.
type FrameExtractor interface {
	// ExtractFrames writes the frames of the video at inputPath into the
	// existing directory outDir, named domain.FrameName(1..n), and returns
	// their paths in order. It fails when the input cannot be decoded, has
	// no video stream, or yields no frame (errors wrapping
	// ErrUnprocessableVideo), and when ctx is done.
	ExtractFrames(ctx context.Context, inputPath, outDir string) ([]string, error)
}

// Archiver packs files into a zip archive.
type Archiver interface {
	// Archive writes a zip holding the given files at its root, under their
	// base names and in the given order, to w.
	Archive(ctx context.Context, w io.Writer, paths []string) error
}

// ObjectStorage stores videos and frame archives as objects in one bucket.
type ObjectStorage interface {
	// Put streams r to key. size is the length of r, or -1 when unknown.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get opens the object at key; the caller closes it. A missing key
	// yields an error wrapping ErrObjectNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// EnsureBucket creates the bucket when it does not exist yet.
	EnsureBucket(ctx context.Context) error
	// Ping checks that the storage is reachable and the bucket exists.
	Ping(ctx context.Context) error
}

// UserRepository persists users.
type UserRepository interface {
	// Create inserts u. An e-mail that is already registered yields an error
	// wrapping ErrEmailTaken.
	Create(ctx context.Context, u *domain.User) error
	// GetByEmail returns the user with the (normalized) e-mail, or an error
	// wrapping ErrNotFound.
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	// GetByID returns the user with the id, or an error wrapping ErrNotFound.
	GetByID(ctx context.Context, id string) (*domain.User, error)
}

// VideoRepository persists videos. Every read on behalf of a user is
// scoped to its owner.
type VideoRepository interface {
	// Create inserts v.
	Create(ctx context.Context, v *domain.Video) error
	// GetByIDForOwner returns the video with the id owned by ownerID. An
	// unknown id, a malformed id and another user's video all yield an
	// error wrapping ErrNotFound.
	GetByIDForOwner(ctx context.Context, id, ownerID string) (*domain.Video, error)
	// ListByOwner returns one page of the owner's videos, newest first
	// (created_at descending, id descending), and the owner's total number
	// of videos.
	ListByOwner(ctx context.Context, ownerID string, page Page) ([]domain.Video, int, error)
}

// PasswordHasher hashes passwords and checks them against a hash.
type PasswordHasher interface {
	Hash(password string) (string, error)
	// Compare returns nil when password matches hash, and an error
	// otherwise.
	Compare(hash, password string) error
}

// AccessToken is a bearer token and its lifetime.
type AccessToken struct {
	Value     string
	ExpiresIn time.Duration
}

// TokenIssuer issues access tokens for users.
type TokenIssuer interface {
	Issue(userID string) (AccessToken, error)
}

// TokenVerifier checks access tokens.
type TokenVerifier interface {
	// Verify returns the id of the user the token was issued to. A
	// malformed, badly signed or expired token yields an error wrapping
	// ErrInvalidToken.
	Verify(token string) (userID string, err error)
}
