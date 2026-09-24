package postgres

import (
	"context"
	"testing"

	"video-processor/internal/app"
	"video-processor/internal/platform/logging"
)

// TestOutboxKeepsCorrelationID checks the round trip of correlation ids
// through the outbox: taken from the context (upload) or the message
// (explicit), stored, and handed to the publisher; rows without one (e.g.
// queued before migration 00004) are published without one.
func TestOutboxKeepsCorrelationID(t *testing.T) {
	pool := freshDatabase(t)
	videos, msgs := uploadedVideos(t, pool, 2)
	msgs[1].CorrelationID = "explicit-1" // wins over the context's

	ctx := logging.WithRequestID(context.Background(), "demo-123")
	if err := NewVideos(pool).CreateWithMessages(ctx, videos, msgs); err != nil {
		t.Fatal(err)
	}
	// A legacy row, as queued before the column existed.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO outbox (message_id, topic, payload) VALUES ('legacy', 'video.uploaded', '{}')`); err != nil {
		t.Fatal(err)
	}
	// A message queued outside any request.
	_, more := uploadedVideos(t, pool, 1)
	if err := NewVideos(pool).CreateWithMessages(context.Background(), nil, more); err != nil {
		t.Fatal(err)
	}

	var got []app.Message
	n, err := NewOutbox(pool).Relay(context.Background(), 10, func(_ context.Context, batch []app.Message) []error {
		got = append(got, batch...)
		return make([]error, len(batch))
	})
	if err != nil || n != 4 {
		t.Fatalf("Relay = %d, %v", n, err)
	}
	want := []string{"demo-123", "explicit-1", "", ""}
	for i, m := range got {
		if m.CorrelationID != want[i] {
			t.Errorf("message %d (%s): correlation id %q, want %q", i, m.ID, m.CorrelationID, want[i])
		}
	}
}

// TestFinalStatusEventsKeepCorrelationID checks that the worker's events get
// the correlation id of the job's context.
func TestFinalStatusEventsKeepCorrelationID(t *testing.T) {
	pool := freshDatabase(t)
	videos, msgs := uploadedVideos(t, pool, 1)
	repo := NewVideos(pool)
	if err := repo.CreateWithMessages(context.Background(), videos, msgs); err != nil {
		t.Fatal(err)
	}
	jobCtx := logging.WithRequestID(context.Background(), "job-7")
	event := app.Message{ID: "e-1", Topic: app.TopicVideoFailed, Body: []byte(`{}`)}
	if ok, err := repo.MarkFailed(jobCtx, videos[0].ID, "boom", now(), event); err != nil || !ok {
		t.Fatalf("MarkFailed = %v, %v", ok, err)
	}
	var corr *string
	if err := pool.QueryRow(context.Background(),
		`SELECT correlation_id FROM outbox WHERE message_id = 'e-1'`).Scan(&corr); err != nil {
		t.Fatal(err)
	}
	if corr == nil || *corr != "job-7" {
		t.Errorf("event correlation id = %v, want job-7", corr)
	}
}
