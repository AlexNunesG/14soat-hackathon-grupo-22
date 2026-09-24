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

	// ErrNotRecorded is wrapped by NotificationLog.SendOnce when the
	// notification was sent but could not be recorded as sent.
	ErrNotRecorded = errors.New("notification sent but not recorded")

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
	Get(ctx context.Context, key string) (*Object, error)
	// Delete removes the object at key. A missing key is not an error.
	Delete(ctx context.Context, key string) error
	// EnsureBucket creates the bucket when it does not exist yet.
	EnsureBucket(ctx context.Context) error
	// Ping checks that the storage is reachable and the bucket exists.
	Ping(ctx context.Context) error
}

// Object is an open stored object: its content and its size in bytes.
type Object struct {
	io.ReadCloser
	Size int64
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

// UploadRepository persists uploaded videos.
type UploadRepository interface {
	// CreateWithMessages inserts the videos and queues the messages in the
	// outbox, atomically: either all of them are stored or none is. The
	// outbox relay publishes the messages afterwards (ADR 0004).
	CreateWithMessages(ctx context.Context, videos []*domain.Video, msgs []Message) error
}

// ProcessingRepository is what the worker needs to track a video's
// processing. Every state change is conditional on the current status, so
// concurrent or repeated deliveries of the same job cannot move a video
// backwards or out of a final status.
type ProcessingRepository interface {
	// GetByID returns the video with the id, whoever owns it, or an error
	// wrapping ErrNotFound.
	GetByID(ctx context.Context, id string) (*domain.Video, error)
	// MarkProcessing moves a PENDING or PROCESSING video to PROCESSING (a
	// redelivered job restarts the work) and reports whether it did; false
	// means the video is final (or gone) and must not be processed.
	MarkProcessing(ctx context.Context, id string, at time.Time) (bool, error)
	// MarkDone moves a PROCESSING video to DONE with its archive and frame
	// count, and reports whether it did. Only when it did, it also queues
	// events in the outbox, in the same transaction (ADR 0004): a status
	// change and its events are committed together or not at all.
	MarkDone(ctx context.Context, id, zipKey string, frameCount int, at time.Time, events ...Message) (bool, error)
	// MarkFailed moves a PENDING or PROCESSING video to FAILED with the
	// reason, and reports whether it did. Only when it did, it also queues
	// events in the outbox, in the same transaction.
	MarkFailed(ctx context.Context, id, reason string, at time.Time, events ...Message) (bool, error)
}

// UserReader looks users up by id (the worker resolves a video's owner for
// its events).
type UserReader interface {
	// GetByID returns the user with the id, or an error wrapping
	// ErrNotFound.
	GetByID(ctx context.Context, id string) (*domain.User, error)
}

// Mail is a plain-text e-mail to one recipient.
type Mail struct {
	// ID identifies the e-mail (the event id); adapters use it for the
	// Message-ID header.
	ID      string
	To      string
	Subject string
	// Body is the UTF-8 text of the message, lines separated by "\n".
	Body string
}

// Mailer sends e-mails (SMTP in production, MailHog locally).
type Mailer interface {
	// Send delivers m to the mail server. An error wrapping ErrPermanent
	// means the server refused the message for good (e.g. an unknown
	// recipient); any other error is worth retrying.
	Send(ctx context.Context, m Mail) error
}

// SentNotification records one notification that was sent.
type SentNotification struct {
	EventID   string
	VideoID   string
	Kind      string // the event type, e.g. video.failed
	Recipient string
}

// NotificationLog remembers which events were already notified, so a
// redelivered event does not send a second e-mail.
type NotificationLog interface {
	// SendOnce calls send unless n.EventID is already recorded, and records
	// it when send succeeds. Concurrent calls for the same event are
	// serialized: the second waits for the first and then skips. It returns
	// whether send was called and succeeded. If send fails, nothing is
	// recorded and its error is returned. If send succeeded but recording
	// failed, it returns true and an error wrapping ErrNotRecorded.
	SendOnce(ctx context.Context, n SentNotification, send func(ctx context.Context) error) (sent bool, err error)
}

// PublishFunc publishes msgs and returns one error per message, in order:
// nil when the broker confirmed that message.
type PublishFunc func(ctx context.Context, msgs []Message) []error

// OutboxStore holds the messages waiting to be published (ADR 0004).
type OutboxStore interface {
	// Relay claims up to limit messages that are due, calls publish with
	// them, removes the published ones and delays the others (with backoff).
	// Claimed messages are locked until Relay returns, so concurrent relays
	// (several api replicas) never publish the same row at the same time.
	// It returns how many messages it claimed.
	Relay(ctx context.Context, limit int, publish PublishFunc) (int, error)
}

// MessagePublisher publishes messages to the broker with publisher
// confirms.
type MessagePublisher interface {
	Publish(ctx context.Context, msgs []Message) []error
}

// VideoListCache caches pages of a user's video list (GET /api/v1/videos,
// RF4). It is best effort: callers log its errors and fall back to the
// repository, so an unreachable cache never fails a request or a job.
//
// Freshness relies on a per-user version: a page is stored under the
// version read before the repository was queried, and every change to the
// user's videos bumps the version (InvalidateList) after it is committed.
// A page read from the repository concurrently with a change is therefore
// stored under the old version, which no reader asks for any more.
type VideoListCache interface {
	// ListVersion returns the current version of the owner's list.
	ListVersion(ctx context.Context, ownerID string) (string, error)
	// GetList returns the page stored for the owner at version, and
	// whether there was one.
	GetList(ctx context.Context, ownerID, version string, page Page) (VideoPage, bool, error)
	// PutList stores the page for the owner at version, for a short time.
	PutList(ctx context.Context, ownerID, version string, vp VideoPage) error
	// ListInvalidator is also part of the cache.
	ListInvalidator
}

// ListInvalidator makes the cached pages of a user's video list stale.
// Services that change videos (the api on upload, the worker on every
// status change) call it after the change is committed.
type ListInvalidator interface {
	// InvalidateList bumps the version of the owner's list.
	InvalidateList(ctx context.Context, ownerID string) error
}
