package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/domain"
)

// cleanupTimeout bounds the best-effort removal of objects after a failure.
const cleanupTimeout = 30 * time.Second

// UploadFile is one file of an upload request.
type UploadFile struct {
	// Name is the original file name, as sent by the client.
	Name string
	// Size is the length of Content in bytes, or -1 when unknown.
	Size int64
	// Content is the file's bytes.
	Content io.Reader
}

// Uploads is the upload use case (RF2): it stores the videos, records them
// as PENDING and queues their processing jobs in the same transaction
// (transactional outbox, ADR 0004).
type Uploads struct {
	repo     UploadRepository
	storage  ObjectStorage
	log      *slog.Logger
	now      func() time.Time
	newID    func() string
	enqueued func()
}

// UploadsOption customizes Uploads.
type UploadsOption func(*Uploads)

// WithUploadClock sets the clock used for timestamps.
func WithUploadClock(now func() time.Time) UploadsOption { return func(u *Uploads) { u.now = now } }

// WithUploadIDs sets the generator of video ids.
func WithUploadIDs(newID func() string) UploadsOption { return func(u *Uploads) { u.newID = newID } }

// WithUploadLogger sets the logger of cleanup failures.
func WithUploadLogger(log *slog.Logger) UploadsOption { return func(u *Uploads) { u.log = log } }

// OnEnqueued sets a function called after messages are queued in the
// outbox, e.g. OutboxRelay.Notify, so they are published at once instead of
// at the relay's next poll.
func OnEnqueued(f func()) UploadsOption { return func(u *Uploads) { u.enqueued = f } }

// NewUploads returns the upload use case.
func NewUploads(repo UploadRepository, storage ObjectStorage, opts ...UploadsOption) *Uploads {
	u := &Uploads{
		repo:     repo,
		storage:  storage,
		log:      slog.New(slog.DiscardHandler),
		now:      time.Now,
		newID:    uuid.NewString,
		enqueued: func() {},
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// VideoKey is the object key of an uploaded video: uploads/<id>.<ext>.
func VideoKey(id, originalName string) string {
	key := "uploads/" + id
	if ext, ok := domain.Extension(originalName); ok {
		key += "." + ext
	}
	return key
}

// ValidateUpload checks a file name the way Upload does: it returns nil or
// an error wrapping domain.ErrUnsupportedFormat. Adapters use it to reject
// a request before reading the rest of it.
func ValidateUpload(name string) error { return domain.ValidateFormat(name) }

// Upload creates one PENDING video per file, in order, and returns them.
// The request is all-or-nothing: without files it fails with
// ErrMissingFile, and when any name has an unsupported extension it fails
// with an error wrapping domain.ErrUnsupportedFormat (whose message lists
// the supported formats) before anything is stored. When it returns nil,
// every video's processing job is durably queued.
func (u *Uploads) Upload(ctx context.Context, ownerID string, files []UploadFile) ([]*domain.Video, error) {
	if len(files) == 0 {
		return nil, ErrMissingFile
	}
	for _, f := range files {
		if err := ValidateUpload(f.Name); err != nil {
			return nil, err
		}
	}

	// The database keeps microseconds: truncate so the returned videos match
	// what a later read returns.
	now := u.now().UTC().Truncate(time.Microsecond)
	videos := make([]*domain.Video, 0, len(files))
	msgs := make([]Message, 0, len(files))
	for _, f := range files {
		id := u.newID()
		v, err := domain.NewVideo(id, ownerID, f.Name, VideoKey(id, f.Name), now)
		if err != nil {
			return nil, fmt.Errorf("new video: %w", err)
		}
		msg, err := NewVideoUploadedMessage(v)
		if err != nil {
			return nil, err
		}
		videos, msgs = append(videos, v), append(msgs, msg)
	}

	var stored []string
	for i, f := range files {
		key := videos[i].StorageKey
		if err := u.storage.Put(ctx, key, f.Content, f.Size, "application/octet-stream"); err != nil {
			u.cleanup(ctx, stored)
			return nil, fmt.Errorf("store video %q: %w", f.Name, err)
		}
		stored = append(stored, key)
	}
	if err := u.repo.CreateWithMessages(ctx, videos, msgs); err != nil {
		u.cleanup(ctx, stored)
		return nil, fmt.Errorf("create videos: %w", err)
	}
	u.enqueued()
	return videos, nil
}

// cleanup removes objects stored by a failed upload, best effort: a leftover
// object is only wasted space, never a visible video.
func (u *Uploads) cleanup(ctx context.Context, keys []string) {
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	var errs []error
	for _, key := range keys {
		errs = append(errs, u.storage.Delete(ctx, key))
	}
	if err := errors.Join(errs...); err != nil {
		u.log.WarnContext(ctx, "could not remove the objects of a failed upload", slog.Any("error", err))
	}
}
