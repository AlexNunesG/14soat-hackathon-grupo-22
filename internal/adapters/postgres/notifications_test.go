package postgres

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

func eventMessage(t *testing.T, topic, videoID string) app.Message {
	t.Helper()
	m, err := app.NewVideoEventMessage(app.VideoEvent{
		EventID: uuid.NewString(), Type: topic, VideoID: videoID, OwnerEmail: "a@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFinalStatusChangesQueueTheirEvents(t *testing.T) {
	pool := freshDatabase(t)
	users, videos := NewUsers(pool), NewVideos(pool)
	ctx := context.Background()
	owner := newUser(t, users)
	at := now()

	create := func(name string, status domain.VideoStatus) *domain.Video {
		v := newVideo(t, owner.ID, name, now())
		if err := videos.Create(ctx, v); err != nil {
			t.Fatal(err)
		}
		if status == domain.StatusProcessing {
			if ok, err := videos.MarkProcessing(ctx, v.ID, at); !ok || err != nil {
				t.Fatal(ok, err)
			}
		}
		return v
	}
	done := create("done.mp4", domain.StatusProcessing)
	failed := create("failed.mp4", domain.StatusPending)

	processed := eventMessage(t, app.TopicVideoProcessed, done.ID)
	if ok, err := videos.MarkDone(ctx, done.ID, "z.zip", 2, at, processed); !ok || err != nil {
		t.Fatalf("mark done: %v, %v", ok, err)
	}
	failure := eventMessage(t, app.TopicVideoFailed, failed.ID)
	if ok, err := videos.MarkFailed(ctx, failed.ID, "no video stream", at, failure); !ok || err != nil {
		t.Fatalf("mark failed: %v, %v", ok, err)
	}
	// Losing changes (the video is already final) queue nothing.
	if ok, err := videos.MarkDone(ctx, done.ID, "other.zip", 9, at, eventMessage(t, app.TopicVideoProcessed, done.ID)); ok || err != nil {
		t.Fatalf("second done: %v, %v", ok, err)
	}
	if ok, err := videos.MarkFailed(ctx, done.ID, "late", at, eventMessage(t, app.TopicVideoFailed, done.ID)); ok || err != nil {
		t.Fatalf("failed after done: %v, %v", ok, err)
	}
	if ok, err := videos.MarkFailed(ctx, "not-a-uuid", "x", at, eventMessage(t, app.TopicVideoFailed, done.ID)); ok || err != nil {
		t.Fatalf("malformed id: %v, %v", ok, err)
	}

	rows := outboxRows(t, pool)
	if len(rows) != 2 || rows[0].messageID != processed.ID || rows[0].topic != app.TopicVideoProcessed ||
		rows[1].messageID != failure.ID || rows[1].topic != app.TopicVideoFailed {
		t.Errorf("outbox %+v, want exactly the two winning events", rows)
	}

	// A failing event insert rolls the status change back.
	v := create("rollback.mp4", domain.StatusPending)
	bad := app.Message{ID: "", Topic: app.TopicVideoFailed, Body: []byte("{}")} // violates message_id <> ''
	if _, err := videos.MarkFailed(ctx, v.ID, "boom", at, bad); err == nil {
		t.Fatal("invalid event: want error")
	}
	if got, _ := videos.GetByID(ctx, v.ID); got.Status != domain.StatusPending {
		t.Errorf("status %s after a failed event insert, want PENDING", got.Status)
	}
}

func TestNotificationsSendOnce(t *testing.T) {
	pool := migratedPool(t)
	log := NewNotifications(pool)
	ctx := context.Background()
	rec := app.SentNotification{EventID: uuid.NewString(), VideoID: uuid.NewString(), Kind: app.TopicVideoFailed, Recipient: "a@example.com"}

	// A failed send records nothing: the retry sends again.
	errSMTP := errors.New("421 try later")
	if sent, err := log.SendOnce(ctx, rec, func(context.Context) error { return errSMTP }); sent || !errors.Is(err, errSMTP) {
		t.Fatalf("failed send: %v, %v", sent, err)
	}
	calls := 0
	send := func(context.Context) error { calls++; return nil }
	if sent, err := log.SendOnce(ctx, rec, send); !sent || err != nil {
		t.Fatalf("send: %v, %v", sent, err)
	}
	if sent, err := log.SendOnce(ctx, rec, send); sent || err != nil || calls != 1 {
		t.Fatalf("duplicate: %v, %v, %d calls", sent, err, calls)
	}
	var recipient string
	if err := pool.QueryRow(ctx, `SELECT recipient FROM notifications_sent WHERE event_id = $1`, rec.EventID).Scan(&recipient); err != nil || recipient != rec.Recipient {
		t.Errorf("row: %q, %v", recipient, err)
	}

	bad := rec
	bad.EventID = "x"
	if _, err := log.SendOnce(ctx, bad, send); !errors.Is(err, app.ErrPermanent) {
		t.Errorf("malformed event id: err = %v", err)
	}
}

func TestNotificationsConcurrentDuplicatesSendOnce(t *testing.T) {
	pool := migratedPool(t)
	log := NewNotifications(pool)
	rec := app.SentNotification{EventID: uuid.NewString(), VideoID: uuid.NewString(), Kind: app.TopicVideoFailed, Recipient: "a@example.com"}
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			_, err := log.SendOnce(context.Background(), rec, func(context.Context) error {
				calls.Add(1)
				time.Sleep(50 * time.Millisecond) // a slow mail server
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("sent %d times, want 1", n)
	}
}
