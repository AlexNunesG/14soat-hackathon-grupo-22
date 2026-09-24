package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// decodeEvents decodes the events queued with the status changes.
func decodeEvents(t *testing.T, msgs []app.Message) []app.VideoEvent {
	t.Helper()
	events := make([]app.VideoEvent, len(msgs))
	for i, m := range msgs {
		e, err := app.DecodeVideoEvent(m.Body)
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if m.ID != e.EventID || m.Topic != e.Type {
			t.Errorf("message id %q topic %q, want the event id %q and type %q", m.ID, m.Topic, e.EventID, e.Type)
		}
		events[i] = e
	}
	return events
}

// eventIDs returns UUIDs, like the real generator, so events decode.
func eventIDs() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	}
}

func (e *processorEnv) eventProcessor(onEvent func()) *app.Processor {
	return app.NewProcessor(e.repo, e.users, e.store, e.extractor, e.archiver,
		app.WithProcessorTempDir(e.tempDir),
		app.WithProcessorClock(func() time.Time { return fixedNow.Add(time.Minute) }),
		app.WithProcessorIDs(eventIDs()),
		app.OnEvent(onEvent))
}

func TestProcessDoneRecordsProcessedEvent(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	woken := 0
	if err := env.eventProcessor(func() { woken++ }).Process(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	events := decodeEvents(t, env.repo.events)
	if len(events) != 1 {
		t.Fatalf("%d events, want 1", len(events))
	}
	e := events[0]
	at := fixedNow.Add(time.Minute).UTC().Truncate(time.Microsecond)
	if e.Type != app.TopicVideoProcessed || e.VideoID != videoID || e.OwnerID != ownerID ||
		e.OwnerEmail != "ana@example.com" || e.OwnerName != "Ana Souza" || e.OriginalName != "holiday.MP4" ||
		e.FrameCount != 3 || e.ErrorMessage != "" || !e.OccurredAt.Equal(at) || !e.UploadedAt.Equal(fixedNow) {
		t.Errorf("event %+v", e)
	}
	if woken != 1 {
		t.Errorf("relay woken %d times, want 1", woken)
	}
}

func TestProcessFailureRecordsFailedEvent(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	env.extractor.err = &reasonError{msg: "no video stream found in input", err: app.ErrUnprocessableVideo}
	woken := 0
	if err := env.eventProcessor(func() { woken++ }).Process(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	events := decodeEvents(t, env.repo.events)
	if len(events) != 1 {
		t.Fatalf("%d events, want 1", len(events))
	}
	e := events[0]
	if e.Type != app.TopicVideoFailed || e.ErrorMessage != "no video stream found in input" ||
		e.OwnerEmail != "ana@example.com" || e.OriginalName != "holiday.MP4" || e.FrameCount != 0 {
		t.Errorf("event %+v", e)
	}
	if woken != 1 {
		t.Errorf("relay woken %d times, want 1", woken)
	}
}

func TestGiveUpRecordsFailedEventOnce(t *testing.T) {
	v := pendingVideo()
	v.Status = domain.StatusProcessing
	env := newProcessorEnv(t, v)
	woken := 0
	p := env.eventProcessor(func() { woken++ })
	for range 2 { // the second call finds the video FAILED: no new event
		if err := p.GiveUp(context.Background(), jobBody(t, videoID), errors.New("storage unreachable")); err != nil {
			t.Fatal(err)
		}
	}
	events := decodeEvents(t, env.repo.events)
	if len(events) != 1 || woken != 1 {
		t.Fatalf("%d events, relay woken %d times; want 1 and 1", len(events), woken)
	}
	if e := events[0]; e.Type != app.TopicVideoFailed || e.ErrorMessage != env.repo.video(videoID).ErrorMessage ||
		!strings.Contains(e.ErrorMessage, "storage unreachable") {
		t.Errorf("event %+v", e)
	}
}

func TestGiveUpUnknownVideoIsIgnored(t *testing.T) {
	env := newProcessorEnv(t)
	if err := env.eventProcessor(func() {}).GiveUp(context.Background(), jobBody(t, videoID), errBoom); err != nil {
		t.Fatal(err)
	}
	if len(env.repo.events) != 0 {
		t.Errorf("events %v", env.repo.events)
	}
}

func TestLosingRunRecordsNoEvent(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	env.repo.beforeDone = func(v *domain.Video) {
		v.Status, v.ZipKey, v.FrameCount = domain.StatusDone, "frames/other.zip", 3
	}
	woken := 0
	if err := env.eventProcessor(func() { woken++ }).Process(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	if len(env.repo.events) != 0 || woken != 0 {
		t.Errorf("%d events, relay woken %d times; want none", len(env.repo.events), woken)
	}
}

func TestOwnerLookupFailureIsRetried(t *testing.T) {
	for name, setup := range map[string]func(*processorEnv){
		"done":   func(*processorEnv) {},
		"failed": func(env *processorEnv) { env.extractor.err = fmt.Errorf("%w: bad", app.ErrUnprocessableVideo) },
	} {
		t.Run(name, func(t *testing.T) {
			env := newProcessorEnv(t, pendingVideo())
			setup(env)
			env.users.err = errBoom
			err := env.eventProcessor(func() {}).Process(context.Background(), videoID)
			if !errors.Is(err, errBoom) {
				t.Fatalf("err = %v, want the lookup error (retry)", err)
			}
			if got := env.repo.video(videoID); got.Status.IsFinal() {
				t.Errorf("video became %s without its event", got.Status)
			}
			if keys := env.store.keys(); len(keys) != 1 || keys[0] != inputKey {
				t.Errorf("objects %v, want no orphan zip", keys)
			}
		})
	}
}

func TestMissingOwnerRecordsNoEvent(t *testing.T) {
	env := newProcessorEnv(t, pendingVideo())
	delete(env.users.users, ownerID)
	if err := env.eventProcessor(func() {}).Process(context.Background(), videoID); err != nil {
		t.Fatal(err)
	}
	if got := env.repo.video(videoID); got.Status != domain.StatusDone || len(env.repo.events) != 0 {
		t.Errorf("status %s, %d events; want DONE without event", got.Status, len(env.repo.events))
	}
}

func TestDecodeVideoEvent(t *testing.T) {
	good := app.VideoEvent{
		EventID: "00000000-0000-4000-8000-000000000001", Type: app.TopicVideoFailed, VideoID: videoID,
		OwnerID: ownerID, OwnerEmail: "ana@example.com", OriginalName: "férias.mp4", ErrorMessage: "boom",
		UploadedAt: fixedNow.UTC(), OccurredAt: fixedNow.UTC(),
	}
	msg, err := app.NewVideoEventMessage(good)
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.DecodeVideoEvent(msg.Body)
	if err != nil || !got.UploadedAt.Equal(good.UploadedAt) || got.OriginalName != good.OriginalName || got.EventID != good.EventID {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	for name, body := range map[string]string{
		"not json":      "{",
		"no event id":   `{"type":"video.failed","video_id":"` + videoID + `","owner_email":"a@b.c"}`,
		"no email":      `{"event_id":"00000000-0000-4000-8000-000000000001","type":"video.failed","video_id":"` + videoID + `"}`,
		"bad event id":  `{"event_id":"x","type":"video.failed","video_id":"` + videoID + `","owner_email":"a@b.c"}`,
		"empty payload": `{}`,
	} {
		if _, err := app.DecodeVideoEvent([]byte(body)); !errors.Is(err, app.ErrMalformedMessage) {
			t.Errorf("%s: err = %v, want ErrMalformedMessage", name, err)
		}
	}
}
