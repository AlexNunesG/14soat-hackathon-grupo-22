package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"video-processor/db/migrations"
	"video-processor/internal/app"
	"video-processor/internal/domain"
)

func TestVideosStateTransitions(t *testing.T) {
	pool := migratedPool(t)
	users, videos := NewUsers(pool), NewVideos(pool)
	ctx := context.Background()
	owner := newUser(t, users)
	v := newVideo(t, owner.ID, "a.mp4", now())
	if err := videos.Create(ctx, v); err != nil {
		t.Fatal(err)
	}
	at := now().Add(time.Second)

	step := func(name string, want bool, f func() (bool, error)) {
		t.Helper()
		got, err := f()
		if err != nil || got != want {
			t.Fatalf("%s: got %v, %v; want %v", name, got, err, want)
		}
	}
	step("done while pending", false, func() (bool, error) { return videos.MarkDone(ctx, v.ID, "z.zip", 2, at) })
	step("processing", true, func() (bool, error) { return videos.MarkProcessing(ctx, v.ID, at) })
	step("processing again (redelivery)", true, func() (bool, error) { return videos.MarkProcessing(ctx, v.ID, at) })
	step("done", true, func() (bool, error) { return videos.MarkDone(ctx, v.ID, "z.zip", 2, at) })
	step("done twice", false, func() (bool, error) { return videos.MarkDone(ctx, v.ID, "other.zip", 9, at) })
	step("processing when done", false, func() (bool, error) { return videos.MarkProcessing(ctx, v.ID, at) })
	step("failed when done", false, func() (bool, error) { return videos.MarkFailed(ctx, v.ID, "boom", at) })

	got, err := videos.GetByID(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusDone || got.ZipKey != "z.zip" || got.FrameCount != 2 || !got.UpdatedAt.Equal(at) || got.OwnerID != owner.ID {
		t.Errorf("video %+v", got)
	}

	// PENDING -> FAILED (e.g. given up before any worker started it).
	f := newVideo(t, owner.ID, "b.mp4", now())
	if err := videos.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	step("failed while pending", true, func() (bool, error) { return videos.MarkFailed(ctx, f.ID, "no video stream", at) })
	step("processing when failed", false, func() (bool, error) { return videos.MarkProcessing(ctx, f.ID, at) })
	if got, _ := videos.GetByID(ctx, f.ID); got.Status != domain.StatusFailed || got.ErrorMessage != "no video stream" {
		t.Errorf("video %+v", got)
	}

	// Unknown and malformed ids.
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if _, err := videos.GetByID(ctx, id); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("GetByID(%q): err = %v, want ErrNotFound", id, err)
		}
		step("processing unknown "+id, false, func() (bool, error) { return videos.MarkProcessing(ctx, id, at) })
	}
}

// freshDatabase creates an empty, migrated database next to
// POSTGRES_TEST_URL's and drops it when the test ends, so outbox tests do
// not race a running relay on the shared database.
func freshDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := migratedPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := "outbox_test_" + uuid.NewString()[:8]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(os.Getenv("POSTGRES_TEST_URL"))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := NewPool(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := Migrate(ctx, pool, migrations.FS, nil); err != nil {
		t.Fatal(err)
	}
	return pool
}

type outboxRow struct {
	messageID, topic, payload string
	attempts                  int
	lastError                 *string
	availableIn               time.Duration
}

func outboxRows(t *testing.T, db DB) []outboxRow {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		SELECT message_id, topic, payload::text, attempts, last_error,
		       EXTRACT(EPOCH FROM available_at - clock_timestamp())::float8
		FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (outboxRow, error) {
		var (
			o       outboxRow
			seconds float64
		)
		err := r.Scan(&o.messageID, &o.topic, &o.payload, &o.attempts, &o.lastError, &seconds)
		o.availableIn = time.Duration(seconds * float64(time.Second))
		return o, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func uploadedVideos(t *testing.T, pool *pgxpool.Pool, n int) ([]*domain.Video, []app.Message) {
	t.Helper()
	owner := newUser(t, NewUsers(pool))
	var (
		videos []*domain.Video
		msgs   []app.Message
	)
	for i := range n {
		v := newVideo(t, owner.ID, fmt.Sprintf("v%d.mp4", i), now())
		m, err := app.NewVideoUploadedMessage(v)
		if err != nil {
			t.Fatal(err)
		}
		videos, msgs = append(videos, v), append(msgs, m)
	}
	return videos, msgs
}

func TestCreateWithMessages(t *testing.T) {
	pool := freshDatabase(t)
	repo := NewVideos(pool)
	ctx := context.Background()
	videos, msgs := uploadedVideos(t, pool, 2)
	if err := repo.CreateWithMessages(ctx, videos, msgs); err != nil {
		t.Fatal(err)
	}
	for _, v := range videos {
		if _, err := repo.GetByID(ctx, v.ID); err != nil {
			t.Errorf("video %s not stored: %v", v.ID, err)
		}
	}
	rows := outboxRows(t, pool)
	if len(rows) != 2 || rows[0].messageID != videos[0].ID || rows[0].topic != app.TopicVideoUploaded ||
		rows[0].payload != `{"video_id": "`+videos[0].ID+`"}` || rows[0].attempts != 0 {
		t.Errorf("outbox %+v", rows)
	}

	// All or nothing: a failing video (duplicate id) rolls back the others
	// and their messages.
	more, moreMsgs := uploadedVideos(t, pool, 1)
	err := repo.CreateWithMessages(ctx, append(more, videos[0]), append(moreMsgs, msgs[0]))
	if err == nil {
		t.Fatal("duplicate id: want error")
	}
	if _, err := repo.GetByID(ctx, more[0].ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("video of a failed transaction exists: %v", err)
	}
	if rows := outboxRows(t, pool); len(rows) != 2 {
		t.Errorf("outbox has %d rows after a failed transaction, want 2", len(rows))
	}
}

func TestOutboxRelayPublishesAndBacksOff(t *testing.T) {
	pool := freshDatabase(t)
	ctx := context.Background()
	videos, msgs := uploadedVideos(t, pool, 3)
	if err := NewVideos(pool).CreateWithMessages(ctx, videos, msgs); err != nil {
		t.Fatal(err)
	}
	outbox := NewOutbox(pool)
	if n, err := outbox.Pending(ctx); err != nil || n != 3 {
		t.Fatalf("Pending before the relay = %d, %v; want 3", n, err)
	}

	// The broker accepts the first and third messages only.
	var got []app.Message
	n, err := outbox.Relay(ctx, 10, func(_ context.Context, batch []app.Message) []error {
		got = append(got, batch...)
		return []error{nil, errors.New("broker down"), nil}
	})
	if err != nil || n != 3 {
		t.Fatalf("Relay = %d, %v", n, err)
	}
	if len(got) != 3 || got[0].ID != msgs[0].ID || got[2].ID != msgs[2].ID || got[1].Topic != app.TopicVideoUploaded {
		t.Fatalf("published %+v", got)
	}
	// The payload is stored as jsonb: equivalent JSON, not the same bytes.
	if body, err := app.DecodeVideoUploaded(got[1].Body); err != nil || body.VideoID != videos[1].ID {
		t.Errorf("body %s: %+v, %v", got[1].Body, body, err)
	}
	rows := outboxRows(t, pool)
	if len(rows) != 1 || rows[0].messageID != msgs[1].ID || rows[0].attempts != 1 ||
		rows[0].lastError == nil || *rows[0].lastError != "broker down" ||
		rows[0].availableIn < 500*time.Millisecond || rows[0].availableIn > baseRelayBackoff {
		t.Fatalf("after a failed publish: %+v", rows)
	}
	// A delayed message is still pending.
	if n, err := outbox.Pending(ctx); err != nil || n != 1 {
		t.Fatalf("Pending after a failed publish = %d, %v; want 1", n, err)
	}

	// Not due yet: nothing is claimed.
	n, err = outbox.Relay(ctx, 10, func(context.Context, []app.Message) []error {
		t.Error("published a message before its backoff")
		return nil
	})
	if err != nil || n != 0 {
		t.Fatalf("Relay before backoff = %d, %v", n, err)
	}

	// Once due it is published and removed.
	if _, err := pool.Exec(ctx, `UPDATE outbox SET available_at = clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	n, err = outbox.Relay(ctx, 10, func(_ context.Context, batch []app.Message) []error {
		return make([]error, len(batch))
	})
	if err != nil || n != 1 {
		t.Fatalf("Relay after backoff = %d, %v", n, err)
	}
	if rows := outboxRows(t, pool); len(rows) != 0 {
		t.Errorf("outbox not empty: %+v", rows)
	}
	if n, err := outbox.Pending(ctx); err != nil || n != 0 {
		t.Errorf("Pending once published = %d, %v; want 0", n, err)
	}
}

func TestOutboxBackoffIsCapped(t *testing.T) {
	pool := freshDatabase(t)
	ctx := context.Background()
	videos, msgs := uploadedVideos(t, pool, 1)
	if err := NewVideos(pool).CreateWithMessages(ctx, videos, msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox SET attempts = 40`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOutbox(pool).Relay(ctx, 10, func(_ context.Context, b []app.Message) []error {
		return []error{errors.New("down")}
	}); err != nil {
		t.Fatal(err)
	}
	rows := outboxRows(t, pool)
	if len(rows) != 1 || rows[0].availableIn > MaxRelayBackoff || rows[0].availableIn < MaxRelayBackoff-2*time.Second {
		t.Errorf("backoff %v, want about %v", rows[0].availableIn, MaxRelayBackoff)
	}
}

func TestOutboxConcurrentRelaysNeverShareRows(t *testing.T) {
	pool := freshDatabase(t)
	ctx := context.Background()
	videos, msgs := uploadedVideos(t, pool, 40)
	if err := NewVideos(pool).CreateWithMessages(ctx, videos, msgs); err != nil {
		t.Fatal(err)
	}
	outbox := NewOutbox(pool)
	var (
		mu        sync.Mutex
		published []string
		wg        sync.WaitGroup
	)
	for range 4 { // four api replicas
		wg.Go(func() {
			for {
				n, err := outbox.Relay(ctx, 5, func(_ context.Context, batch []app.Message) []error {
					time.Sleep(20 * time.Millisecond) // hold the claim while "publishing"
					mu.Lock()
					for _, m := range batch {
						published = append(published, m.ID)
					}
					mu.Unlock()
					return make([]error, len(batch))
				})
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					return
				}
			}
		})
	}
	wg.Wait()
	slices.Sort(published)
	if len(published) != 40 || len(slices.Compact(slices.Clone(published))) != 40 {
		t.Errorf("published %d messages, %d distinct; want each of the 40 once", len(published), len(slices.Compact(published)))
	}
}

func TestOutboxRelayRollsBackOnMismatchedResults(t *testing.T) {
	pool := freshDatabase(t)
	ctx := context.Background()
	videos, msgs := uploadedVideos(t, pool, 2)
	if err := NewVideos(pool).CreateWithMessages(ctx, videos, msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOutbox(pool).Relay(ctx, 10, func(context.Context, []app.Message) []error { return nil }); err == nil {
		t.Fatal("want error")
	}
	if rows := outboxRows(t, pool); len(rows) != 2 || rows[0].attempts != 0 {
		t.Errorf("outbox changed: %+v", rows)
	}
}
