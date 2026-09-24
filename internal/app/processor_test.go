package app_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// fakeProcessingRepo is an in-memory app.ProcessingRepository with the
// same conditional updates as the SQL one.
type fakeProcessingRepo struct {
	mu     sync.Mutex
	videos map[string]*domain.Video
	errs   map[string]error // by operation: get, processing, done, failed
	// beforeDone runs inside MarkDone before the update (to simulate a
	// concurrent run finishing first).
	beforeDone func(v *domain.Video)
	// events holds the events queued with the committed status changes.
	events []app.Message
}

func newProcessingRepo(videos ...domain.Video) *fakeProcessingRepo {
	r := &fakeProcessingRepo{videos: map[string]*domain.Video{}, errs: map[string]error{}}
	for i := range videos {
		v := videos[i]
		r.videos[v.ID] = &v
	}
	return r
}

func (r *fakeProcessingRepo) video(id string) domain.Video {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.videos[id]
}

func (r *fakeProcessingRepo) GetByID(_ context.Context, id string) (*domain.Video, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errs["get"]; err != nil {
		return nil, err
	}
	v, ok := r.videos[id]
	if !ok {
		return nil, fmt.Errorf("video %s: %w", id, app.ErrNotFound)
	}
	c := *v
	return &c, nil
}

func (r *fakeProcessingRepo) MarkProcessing(_ context.Context, id string, at time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errs["processing"]; err != nil {
		return false, err
	}
	v, ok := r.videos[id]
	if !ok || v.Status.IsFinal() {
		return false, nil
	}
	v.Status, v.UpdatedAt = domain.StatusProcessing, at
	return true, nil
}

func (r *fakeProcessingRepo) MarkDone(_ context.Context, id, zipKey string, frames int, at time.Time, events ...app.Message) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errs["done"]; err != nil {
		return false, err
	}
	v, ok := r.videos[id]
	if ok && r.beforeDone != nil {
		r.beforeDone(v)
	}
	if !ok || v.Status != domain.StatusProcessing {
		return false, nil
	}
	v.Status, v.ZipKey, v.FrameCount, v.UpdatedAt = domain.StatusDone, zipKey, frames, at
	r.events = append(r.events, events...)
	return true, nil
}

func (r *fakeProcessingRepo) MarkFailed(_ context.Context, id, reason string, at time.Time, events ...app.Message) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errs["failed"]; err != nil {
		return false, err
	}
	v, ok := r.videos[id]
	if !ok || v.Status.IsFinal() {
		return false, nil
	}
	v.Status, v.ErrorMessage, v.UpdatedAt = domain.StatusFailed, reason, at
	r.events = append(r.events, events...)
	return true, nil
}

// fakeExtractor writes frames frame_0001.png... into outDir, or fails.
type fakeExtractor struct {
	frames int
	err    error
	// block makes it wait until ctx is done and return ctx.Err().
	block   bool
	started chan struct{}
	input   []byte // content of the input it was given
	inputAt string
}

func (e *fakeExtractor) ExtractFrames(ctx context.Context, inputPath, outDir string) ([]string, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, err
	}
	e.input, e.inputAt = data, inputPath
	if e.started != nil {
		close(e.started)
	}
	if e.block {
		<-ctx.Done()
		return nil, fmt.Errorf("ffmpeg: %w", ctx.Err())
	}
	if e.err != nil {
		return nil, e.err
	}
	var paths []string
	for i := 1; i <= e.frames; i++ {
		p := filepath.Join(outDir, domain.FrameName(i))
		if err := os.WriteFile(p, []byte("png"), 0o600); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// fakeArchiver writes the base names of the files, one per line.
type fakeArchiver struct{ err error }

func (a fakeArchiver) Archive(_ context.Context, w io.Writer, paths []string) error {
	if a.err != nil {
		return a.err
	}
	for _, p := range paths {
		if _, err := fmt.Fprintln(w, filepath.Base(p)); err != nil {
			return err
		}
	}
	return nil
}

const inputKey = "uploads/" + videoID + ".mp4"

func pendingVideo() domain.Video {
	return domain.Video{
		ID: videoID, OwnerID: ownerID, OriginalName: "holiday.MP4", StorageKey: inputKey,
		Status: domain.StatusPending, CreatedAt: fixedNow, UpdatedAt: fixedNow,
	}
}

type processorEnv struct {
	repo      *fakeProcessingRepo
	users     *fakeUserReader
	store     *fakeStorage
	extractor *fakeExtractor
	archiver  fakeArchiver
	tempDir   string
}

func newProcessorEnv(t *testing.T, videos ...domain.Video) *processorEnv {
	t.Helper()
	store := newFakeStorage()
	store.objects[inputKey] = []byte("the video")
	return &processorEnv{
		repo:      newProcessingRepo(videos...),
		users:     newFakeUserReader(),
		store:     store,
		extractor: &fakeExtractor{frames: 3},
		tempDir:   t.TempDir(),
	}
}

func (e *processorEnv) processor() *app.Processor {
	return app.NewProcessor(e.repo, e.users, e.store, e.extractor, e.archiver,
		app.WithProcessorTempDir(e.tempDir),
		app.WithProcessorClock(func() time.Time { return fixedNow.Add(time.Minute) }),
		app.WithProcessorIDs(func() string { return "run-1" }))
}

// assertTempDirEmpty checks that the job left no files behind.
func (e *processorEnv) assertTempDirEmpty(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(e.tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func jobBody(t *testing.T, id string) []byte {
	t.Helper()
	m, err := app.NewVideoUploadedMessage(&domain.Video{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	return m.Body
}

func TestProcessDone(t *testing.T) {
	for _, start := range []domain.VideoStatus{domain.StatusPending, domain.StatusProcessing} {
		t.Run(string(start), func(t *testing.T) {
			v := pendingVideo()
			v.Status = start // PROCESSING: a redelivery after a crash is processed again
			env := newProcessorEnv(t, v)
			if err := env.processor().Handle(context.Background(), jobBody(t, videoID)); err != nil {
				t.Fatal(err)
			}
			got := env.repo.video(videoID)
			wantKey := "frames/" + videoID + "/run-1.zip"
			if got.Status != domain.StatusDone || got.FrameCount != 3 || got.ZipKey != wantKey ||
				!got.UpdatedAt.Equal(fixedNow.Add(time.Minute).UTC().Truncate(time.Microsecond)) {
				t.Errorf("video %+v", got)
			}
			if zip := string(env.store.objects[wantKey]); zip != "frame_0001.png\nframe_0002.png\nframe_0003.png\n" {
				t.Errorf("zip %q", zip)
			}
			if env.store.types[wantKey] != "application/zip" {
				t.Errorf("zip content type %q", env.store.types[wantKey])
			}
			if string(env.extractor.input) != "the video" || !strings.HasSuffix(env.extractor.inputAt, ".mp4") {
				t.Errorf("extractor got %q at %s", env.extractor.input, env.extractor.inputAt)
			}
			env.assertTempDirEmpty(t)
		})
	}
}

func TestProcessIgnoresFinishedAndUnknownVideos(t *testing.T) {
	done := pendingVideo()
	done.Status, done.ZipKey, done.FrameCount = domain.StatusDone, "old.zip", 1
	failed := pendingVideo()
	failed.ID, failed.Status, failed.ErrorMessage = "33333333-3333-4333-8333-333333333333", domain.StatusFailed, "bad"
	env := newProcessorEnv(t, done, failed)
	p := env.processor()
	for _, id := range []string{done.ID, failed.ID, "44444444-4444-4444-8444-444444444444"} {
		if err := p.Handle(context.Background(), jobBody(t, id)); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	if env.extractor.input != nil || env.store.puts != 0 {
		t.Error("a finished or unknown video was processed")
	}
	if got := env.repo.video(done.ID); got != done {
		t.Errorf("DONE video changed: %+v", got)
	}
}

func TestProcessUnprocessableVideoFailsWithoutRetry(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	env.extractor.err = fmt.Errorf("%w: exit status 1", app.ErrUnprocessableVideo)
	env.extractor.err = &reasonError{msg: "no video stream found in input", err: env.extractor.err}
	if err := env.processor().Process(context.Background(), videoID); err != nil {
		t.Fatalf("err = %v, want nil (handled, no retry)", err)
	}
	got := env.repo.video(videoID)
	if got.Status != domain.StatusFailed || got.ErrorMessage != "no video stream found in input" || got.FrameCount != 0 {
		t.Errorf("video %+v", got)
	}
	if env.store.puts != 0 {
		t.Error("a zip was stored")
	}
	env.assertTempDirEmpty(t)
}

// reasonError is an extractor error with its own message, like ffmpeg.Error.
type reasonError struct {
	msg string
	err error
}

func (e *reasonError) Error() string { return e.msg }
func (e *reasonError) Unwrap() error { return e.err }

func TestProcessExtractorTimeoutFails(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	env.extractor.err = fmt.Errorf("ffmpeg: stopped after 10m0s: %w", context.DeadlineExceeded)
	if err := env.processor().Process(context.Background(), videoID); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := env.repo.video(videoID); got.Status != domain.StatusFailed || !strings.Contains(got.ErrorMessage, "timed out") {
		t.Errorf("video %+v", got)
	}
}

func TestProcessMissingUploadFails(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	delete(env.store.objects, inputKey)
	if err := env.processor().Process(context.Background(), videoID); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := env.repo.video(videoID); got.Status != domain.StatusFailed || got.ErrorMessage == "" {
		t.Errorf("video %+v", got)
	}
}

func TestProcessTransientErrorsAreReturned(t *testing.T) {
	tests := map[string]func(env *processorEnv){
		"load":            func(env *processorEnv) { env.repo.errs["get"] = errBoom },
		"mark processing": func(env *processorEnv) { env.repo.errs["processing"] = errBoom },
		"fetch video":     func(env *processorEnv) { env.store.getErr = errBoom },
		"extractor":       func(env *processorEnv) { env.extractor.err = errBoom },
		"archiver":        func(env *processorEnv) { env.archiver.err = errBoom },
		"store zip":       func(env *processorEnv) { env.store.putErr["*"] = errBoom },
		"mark done":       func(env *processorEnv) { env.repo.errs["done"] = errBoom },
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			env := newProcessorEnv(t, pendingVideo())
			setup(env)
			err := env.processor().Process(context.Background(), videoID)
			if !errors.Is(err, errBoom) {
				t.Fatalf("err = %v, want the transient error", err)
			}
			if got := env.repo.video(videoID); got.Status.IsFinal() {
				t.Errorf("video became %s on a transient error", got.Status)
			}
			if keys := env.store.keys(); len(keys) != 1 || keys[0] != inputKey {
				t.Errorf("objects %v, want only the upload (no orphan zip)", keys)
			}
			env.assertTempDirEmpty(t)
		})
	}
}

func TestProcessLosingRunDiscardsItsZip(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	env.repo.beforeDone = func(v *domain.Video) {
		// Another delivery of the same job finished first.
		v.Status, v.ZipKey, v.FrameCount = domain.StatusDone, "frames/other.zip", 3
	}
	if err := env.processor().Process(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	if got := env.repo.video(videoID); got.ZipKey != "frames/other.zip" {
		t.Errorf("the winner's result was overwritten: %+v", got)
	}
	if keys := env.store.keys(); len(keys) != 1 || keys[0] != inputKey {
		t.Errorf("objects %v, want the losing zip removed", keys)
	}
}

func TestProcessInterruptedIsNotAFailure(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	env.extractor.block, env.extractor.started = true, make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- env.processor().Process(ctx, videoID) }()
	<-env.extractor.started
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := env.repo.video(videoID); got.Status != domain.StatusProcessing {
		t.Errorf("status %s, want PROCESSING (the job is requeued)", got.Status)
	}
	env.assertTempDirEmpty(t)
}

func TestHandleMalformedMessage(t *testing.T) {
	env := newProcessorEnv(t)
	if err := env.processor().Handle(context.Background(), []byte("{")); !errors.Is(err, app.ErrMalformedMessage) {
		t.Fatalf("err = %v, want ErrMalformedMessage", err)
	}
}

func TestGiveUpMarksFailed(t *testing.T) {
	v := pendingVideo()
	v.Status = domain.StatusProcessing
	env := newProcessorEnv(t, v)
	p := env.processor()
	if err := p.GiveUp(context.Background(), jobBody(t, videoID), errors.New("storage unreachable")); err != nil {
		t.Fatal(err)
	}
	got := env.repo.video(videoID)
	if got.Status != domain.StatusFailed || !strings.Contains(got.ErrorMessage, "storage unreachable") {
		t.Errorf("video %+v", got)
	}
	// Idempotent, and harmless for final videos and malformed bodies.
	if err := p.GiveUp(context.Background(), jobBody(t, videoID), errBoom); err != nil {
		t.Error(err)
	}
	if err := p.GiveUp(context.Background(), []byte("nope"), errBoom); err != nil {
		t.Error(err)
	}
	env.repo.errs["failed"] = errBoom
	if err := p.GiveUp(context.Background(), jobBody(t, videoID), errBoom); !errors.Is(err, errBoom) {
		t.Errorf("repository failure: err = %v", err)
	}
}
