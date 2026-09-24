package app_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// fakeMetrics records what a use case reports to its metrics port.
type fakeMetrics struct {
	mu       sync.Mutex
	outcomes []app.Outcome
	frames   int
}

func (m *fakeMetrics) RecordOutcome(o app.Outcome, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d < 0 {
		panic("negative duration")
	}
	m.outcomes = append(m.outcomes, o)
}

func (m *fakeMetrics) FramesExtracted(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frames += n
}

func (e *processorEnv) meteredProcessor(m *fakeMetrics) *app.Processor {
	return app.NewProcessor(e.repo, e.users, e.store, e.extractor, e.archiver,
		app.WithProcessorTempDir(e.tempDir), app.WithProcessorMetrics(m))
}

func TestProcessorReportsJobOutcomes(t *testing.T) {
	finished := pendingVideo()
	finished.Status, finished.ZipKey, finished.FrameCount = domain.StatusDone, "old.zip", 1
	tests := []struct {
		name       string
		video      *domain.Video // nil: unknown video
		setup      func(env *processorEnv)
		body       []byte // nil: a job for videoID
		want       []app.Outcome
		wantFrames int
		wantErr    bool
	}{
		{name: "done", video: ptr(pendingVideo()), want: []app.Outcome{app.OutcomeDone}, wantFrames: 3},
		{
			name: "unprocessable video fails", video: ptr(pendingVideo()),
			setup: func(env *processorEnv) {
				env.extractor.err = fmt.Errorf("%w: exit status 1", app.ErrUnprocessableVideo)
			},
			want: []app.Outcome{app.OutcomeFailed},
		},
		{
			name: "missing upload fails", video: ptr(pendingVideo()),
			setup: func(env *processorEnv) { delete(env.store.objects, inputKey) },
			want:  []app.Outcome{app.OutcomeFailed},
		},
		{name: "unknown video is ignored", want: []app.Outcome{app.OutcomeIgnored}},
		{name: "finished video is ignored", video: &finished, want: []app.Outcome{app.OutcomeIgnored}},
		{
			name: "losing run is ignored", video: ptr(pendingVideo()),
			setup: func(env *processorEnv) {
				env.repo.beforeDone = func(v *domain.Video) { v.Status = domain.StatusDone }
			},
			want: []app.Outcome{app.OutcomeIgnored},
		},
		{
			// Errors are reported by the consumer, which decides between
			// retried, dead_lettered and requeued.
			name: "transient error reports nothing", video: ptr(pendingVideo()),
			setup:   func(env *processorEnv) { env.extractor.err = errBoom },
			wantErr: true,
		},
		{name: "malformed job reports nothing", body: []byte("{"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var videos []domain.Video
			if tt.video != nil {
				videos = append(videos, *tt.video)
			}
			env := newProcessorEnv(t, videos...)
			if tt.setup != nil {
				tt.setup(env)
			}
			body := tt.body
			if body == nil {
				body = jobBody(t, videoID)
			}
			m := &fakeMetrics{}
			err := env.meteredProcessor(m).Handle(context.Background(), body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if !slices.Equal(m.outcomes, tt.want) {
				t.Errorf("outcomes %v, want %v", m.outcomes, tt.want)
			}
			if m.frames != tt.wantFrames {
				t.Errorf("frames %d, want %d", m.frames, tt.wantFrames)
			}
		})
	}
}

func TestGiveUpReportsNothing(t *testing.T) {
	// The consumer reports a given-up job as dead_lettered.
	env := newProcessorEnv(t, pendingVideo())
	m := &fakeMetrics{}
	if err := env.meteredProcessor(m).GiveUp(context.Background(), jobBody(t, videoID), errBoom); err != nil {
		t.Fatal(err)
	}
	if len(m.outcomes) != 0 {
		t.Errorf("outcomes %v, want none", m.outcomes)
	}
}

func TestNotifierReportsOutcomes(t *testing.T) {
	processed := failedEvent()
	processed.Type = app.TopicVideoProcessed
	m := &fakeMetrics{}
	mailer, sent := &fakeMailer{}, newFakeNotificationLog()
	n := app.NewNotifier(mailer, sent, app.WithNotifierMetrics(m))
	ctx := context.Background()

	for _, body := range [][]byte{
		eventBody(t, failedEvent()), // sent
		eventBody(t, failedEvent()), // duplicate
		eventBody(t, processed),     // ignored
	} {
		if err := n.Handle(ctx, body); err != nil {
			t.Fatal(err)
		}
	}
	// Sent but not recorded still counts as sent.
	other := failedEvent()
	other.EventID = "00000000-0000-4000-8000-000000000002"
	sent.recordErr = errBoom
	if err := n.Handle(ctx, eventBody(t, other)); err != nil {
		t.Fatal(err)
	}
	// Errors are the consumer's to report.
	mailer.err = errors.New("421 try later")
	third := failedEvent()
	third.EventID = "00000000-0000-4000-8000-000000000003"
	if err := n.Handle(ctx, eventBody(t, third)); err == nil {
		t.Fatal("want the mailer's error")
	}
	if err := n.Handle(ctx, []byte("{")); err == nil {
		t.Fatal("want a malformed message error")
	}

	want := []app.Outcome{app.OutcomeSent, app.OutcomeDuplicate, app.OutcomeIgnored, app.OutcomeSent}
	if !slices.Equal(m.outcomes, want) {
		t.Errorf("outcomes %v, want %v", m.outcomes, want)
	}
}

func ptr[T any](v T) *T { return &v }
