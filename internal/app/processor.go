package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/domain"
)

// Processor is the worker's use case (RF1): it turns an uploaded video into
// the zip of its frames and records the outcome.
//
// It is idempotent, so a job may be delivered more than once (a worker
// crash, a duplicate publish):
//   - a job for an unknown, DONE or FAILED video does nothing;
//   - a job for a PENDING or PROCESSING video (re)does the whole work; the
//     zip goes to a fresh key, and only the first run to finish records
//     DONE, the others remove their zip;
//   - every status change is conditional on the current status.
//
// A final status change also queues its event (TopicVideoFailed or
// TopicVideoProcessed) in the outbox, in the same transaction, so exactly
// the run that wins the change records the event (RF5).
type Processor struct {
	repo      ProcessingRepository
	users     UserReader
	storage   ObjectStorage
	extractor FrameExtractor
	archiver  Archiver
	tempDir   string
	log       *slog.Logger
	now       func() time.Time
	newID     func() string
	lists     ListInvalidator
	onEvent   func()
}

// ProcessorOption customizes Processor.
type ProcessorOption func(*Processor)

// WithProcessorTempDir sets the directory for the jobs' temporary files
// (default: os.TempDir()).
func WithProcessorTempDir(dir string) ProcessorOption {
	return func(p *Processor) { p.tempDir = dir }
}

// WithProcessorLogger sets the logger.
func WithProcessorLogger(log *slog.Logger) ProcessorOption {
	return func(p *Processor) { p.log = log }
}

// WithProcessorClock sets the clock used for timestamps.
func WithProcessorClock(now func() time.Time) ProcessorOption {
	return func(p *Processor) { p.now = now }
}

// WithProcessorIDs sets the generator of the zip keys' unique part.
func WithProcessorIDs(newID func() string) ProcessorOption {
	return func(p *Processor) { p.newID = newID }
}

// WithProcessorListInvalidator sets the cache of video lists to invalidate
// after every status change (see VideoListCache).
func WithProcessorListInvalidator(inv ListInvalidator) ProcessorOption {
	return func(p *Processor) { p.lists = inv }
}

// OnEvent sets a function called after an event was committed to the
// outbox, e.g. OutboxRelay.Notify, so it is published at once.
func OnEvent(f func()) ProcessorOption {
	return func(p *Processor) { p.onEvent = f }
}

// NewProcessor returns the processing use case. users resolves the owner of
// a video for its events.
func NewProcessor(repo ProcessingRepository, users UserReader, storage ObjectStorage, extractor FrameExtractor, archiver Archiver, opts ...ProcessorOption) *Processor {
	p := &Processor{
		repo:      repo,
		users:     users,
		storage:   storage,
		extractor: extractor,
		archiver:  archiver,
		log:       slog.New(slog.DiscardHandler),
		now:       time.Now,
		newID:     uuid.NewString,
		onEvent:   func() {},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ZipKey is the object key of one run's frames archive:
// frames/<video id>/<run id>.zip.
func ZipKey(videoID, runID string) string {
	return "frames/" + videoID + "/" + runID + ".zip"
}

// permanentError is a failure caused by the video itself: retrying cannot
// help, so the video ends FAILED with reason as its error message.
type permanentError struct {
	reason string
	err    error
}

func (e *permanentError) Error() string { return e.reason }
func (e *permanentError) Unwrap() error { return e.err }

// Handle processes the job in a TopicVideoUploaded message body. It returns
// nil when the job is finished (DONE, FAILED because of the input, or
// nothing to do), an error wrapping ErrMalformedMessage for a body that is
// not a job, and any other error for a failure worth retrying (or an
// interruption, when ctx is done).
func (p *Processor) Handle(ctx context.Context, body []byte) error {
	msg, err := DecodeVideoUploaded(body)
	if err != nil {
		return err
	}
	return p.Process(ctx, msg.VideoID)
}

// GiveUp records that the job in body failed for good after the retries
// ran out: its video ends FAILED with cause as the reason.
func (p *Processor) GiveUp(ctx context.Context, body []byte, cause error) error {
	msg, err := DecodeVideoUploaded(body)
	if err != nil {
		return nil // nothing to record: the message is dead-lettered as is
	}
	v, err := p.repo.GetByID(ctx, msg.VideoID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load video %s: %w", msg.VideoID, err)
	}
	return p.fail(ctx, v, "processing failed after several attempts: "+cause.Error())
}

// Process processes the video with the id; see Handle.
func (p *Processor) Process(ctx context.Context, id string) error {
	log := p.log.With(slog.String("video_id", id))
	v, err := p.repo.GetByID(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		log.WarnContext(ctx, "job for an unknown video ignored")
		return nil
	case err != nil:
		return fmt.Errorf("load video %s: %w", id, err)
	case v.Status.IsFinal():
		log.InfoContext(ctx, "job for a finished video ignored", slog.String("status", v.Status.String()))
		return nil
	}

	started, err := p.repo.MarkProcessing(ctx, id, p.timestamp())
	if err != nil {
		return fmt.Errorf("mark video %s processing: %w", id, err)
	}
	if !started {
		log.InfoContext(ctx, "job for a video finished meanwhile ignored")
		return nil
	}
	invalidateList(ctx, p.lists, p.log, v.OwnerID)

	begin := time.Now()
	log.InfoContext(ctx, "processing video", slog.String("original_name", v.OriginalName))
	zipKey, frames, err := p.run(ctx, v)
	if err != nil {
		var perm *permanentError
		if ctx.Err() == nil && errors.As(err, &perm) {
			log.InfoContext(ctx, "video cannot be processed", slog.String("reason", perm.reason))
			return p.fail(ctx, v, perm.reason)
		}
		return fmt.Errorf("process video %s: %w", id, err)
	}

	at := p.timestamp()
	events, err := p.event(ctx, v, TopicVideoProcessed, at, func(e *VideoEvent) { e.FrameCount = frames })
	if err != nil {
		p.removeObject(ctx, zipKey)
		return err
	}
	done, err := p.repo.MarkDone(ctx, id, zipKey, frames, at, events...)
	if err != nil || !done {
		// Either the outcome was not recorded (the job is retried and makes
		// a new zip) or another run finished first: this zip is unused.
		p.removeObject(ctx, zipKey)
	}
	if err != nil {
		return fmt.Errorf("mark video %s done: %w", id, err)
	}
	if !done {
		log.InfoContext(ctx, "video was finished by another run; result discarded")
		return nil
	}
	invalidateList(ctx, p.lists, p.log, v.OwnerID)
	p.eventQueued(events)
	log.InfoContext(ctx, "video processed", slog.Int("frames", frames),
		slog.Float64("duration_ms", float64(time.Since(begin).Microseconds())/1000))
	return nil
}

// fail records the video v as FAILED with reason, with its
// TopicVideoFailed event.
func (p *Processor) fail(ctx context.Context, v *domain.Video, reason string) error {
	at := p.timestamp()
	events, err := p.event(ctx, v, TopicVideoFailed, at, func(e *VideoEvent) { e.ErrorMessage = reason })
	if err != nil {
		return err
	}
	failed, err := p.repo.MarkFailed(ctx, v.ID, reason, at, events...)
	if err != nil {
		return fmt.Errorf("mark video %s failed: %w", v.ID, err)
	}
	if !failed {
		p.log.InfoContext(ctx, "video already final; failure not recorded", slog.String("video_id", v.ID))
		return nil
	}
	invalidateList(ctx, p.lists, p.log, v.OwnerID)
	p.eventQueued(events)
	return nil
}

// event returns the message of the event of type topic about v, which
// reaches its final status at, with fill applied. The owner is read now,
// so the event carries the e-mail registered when it occurred. It returns
// no message when the owner no longer exists (its videos are gone too).
func (p *Processor) event(ctx context.Context, v *domain.Video, topic string, at time.Time, fill func(*VideoEvent)) ([]Message, error) {
	owner, err := p.users.GetByID(ctx, v.OwnerID)
	if errors.Is(err, ErrNotFound) {
		p.log.WarnContext(ctx, "owner of the video not found; no event recorded",
			slog.String("video_id", v.ID), slog.String("owner_id", v.OwnerID))
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load owner of video %s: %w", v.ID, err)
	}
	e := VideoEvent{
		EventID:      p.newID(),
		Type:         topic,
		VideoID:      v.ID,
		OwnerID:      owner.ID,
		OwnerEmail:   owner.Email,
		OwnerName:    owner.Name,
		OriginalName: v.OriginalName,
		UploadedAt:   v.CreatedAt,
		OccurredAt:   at,
	}
	fill(&e)
	msg, err := NewVideoEventMessage(e)
	if err != nil {
		return nil, err
	}
	return []Message{msg}, nil
}

// eventQueued wakes the outbox relay after events were committed.
func (p *Processor) eventQueued(events []Message) {
	if len(events) > 0 {
		p.onEvent()
	}
}

// run downloads the video into a temporary directory, extracts its frames,
// zips them and stores the zip. The directory is always removed.
func (p *Processor) run(ctx context.Context, v *domain.Video) (zipKey string, frames int, err error) {
	dir, err := os.MkdirTemp(p.tempDir, "job-"+v.ID+"-")
	if err != nil {
		return "", 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			p.log.WarnContext(ctx, "could not remove a job's temp dir", slog.String("dir", dir), slog.Any("error", rerr))
		}
	}()

	input := filepath.Join(dir, "input")
	if ext, ok := domain.Extension(v.OriginalName); ok {
		input += "." + ext
	}
	if err := p.download(ctx, v.StorageKey, input); err != nil {
		return "", 0, err
	}

	framesDir := filepath.Join(dir, "frames")
	if err := os.Mkdir(framesDir, 0o700); err != nil {
		return "", 0, fmt.Errorf("create frames dir: %w", err)
	}
	paths, err := p.extractor.ExtractFrames(ctx, input, framesDir)
	switch {
	case err == nil:
	case errors.Is(err, ErrUnprocessableVideo):
		return "", 0, &permanentError{reason: err.Error(), err: err}
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		// The extractor's own timeout: a video that slow will be as slow on
		// the next attempt.
		return "", 0, &permanentError{reason: "processing timed out: " + err.Error(), err: err}
	default:
		return "", 0, fmt.Errorf("extract frames: %w", err)
	}

	zipPath := filepath.Join(dir, "frames.zip")
	size, err := p.archive(ctx, zipPath, paths)
	if err != nil {
		return "", 0, err
	}
	zf, err := os.Open(zipPath) // #nosec G304 -- a path inside the job's own temp dir
	if err != nil {
		return "", 0, fmt.Errorf("open zip: %w", err)
	}
	defer zf.Close()
	key := ZipKey(v.ID, p.newID())
	if err := p.storage.Put(ctx, key, zf, size, "application/zip"); err != nil {
		return "", 0, fmt.Errorf("store zip: %w", err)
	}
	return key, len(paths), nil
}

// download copies the object at key to the file path.
func (p *Processor) download(ctx context.Context, key, path string) error {
	obj, err := p.storage.Get(ctx, key)
	if errors.Is(err, ErrObjectNotFound) {
		return &permanentError{reason: "the uploaded video is missing from storage", err: err}
	}
	if err != nil {
		return fmt.Errorf("fetch video: %w", err)
	}
	defer obj.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a path inside the job's own temp dir
	if err != nil {
		return fmt.Errorf("create input file: %w", err)
	}
	if _, err := io.Copy(f, obj); err != nil {
		f.Close()
		return fmt.Errorf("fetch video: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write input file: %w", err)
	}
	return nil
}

// archive writes the zip of paths to zipPath and returns its size.
func (p *Processor) archive(ctx context.Context, zipPath string, paths []string) (int64, error) {
	f, err := os.OpenFile(zipPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a path inside the job's own temp dir
	if err != nil {
		return 0, fmt.Errorf("create zip: %w", err)
	}
	if err := p.archiver.Archive(ctx, f, paths); err != nil {
		f.Close()
		return 0, fmt.Errorf("zip frames: %w", err)
	}
	info, err := f.Stat()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, fmt.Errorf("write zip: %w", err)
	}
	return info.Size(), nil
}

// removeObject deletes an object that is no longer needed, best effort.
func (p *Processor) removeObject(ctx context.Context, key string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := p.storage.Delete(ctx, key); err != nil {
		p.log.WarnContext(ctx, "could not remove an unused zip", slog.String("key", key), slog.Any("error", err))
	}
}

// timestamp returns the current time with the database's precision.
func (p *Processor) timestamp() time.Time { return p.now().UTC().Truncate(time.Microsecond) }
