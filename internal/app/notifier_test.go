package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"video-processor/internal/app"
)

// fakeMailer records the e-mails sent, or fails with err.
type fakeMailer struct {
	mu   sync.Mutex
	sent []app.Mail
	err  error
}

func (m *fakeMailer) Send(_ context.Context, mail app.Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, mail)
	return nil
}

// fakeNotificationLog is an in-memory app.NotificationLog with the same
// semantics as the SQL one (one event at a time).
type fakeNotificationLog struct {
	mu        sync.Mutex
	recorded  map[string]app.SentNotification
	recordErr error // returned after a successful send, wrapped in ErrNotRecorded
}

func newFakeNotificationLog() *fakeNotificationLog {
	return &fakeNotificationLog{recorded: map[string]app.SentNotification{}}
}

func (l *fakeNotificationLog) SendOnce(ctx context.Context, n app.SentNotification, send func(context.Context) error) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.recorded[n.EventID]; ok {
		return false, nil
	}
	if err := send(ctx); err != nil {
		return false, err
	}
	if l.recordErr != nil {
		return true, fmt.Errorf("%w: %w", app.ErrNotRecorded, l.recordErr)
	}
	l.recorded[n.EventID] = n
	return true, nil
}

const eventID = "00000000-0000-4000-8000-000000000001"

func failedEvent() app.VideoEvent {
	return app.VideoEvent{
		EventID: eventID, Type: app.TopicVideoFailed, VideoID: videoID, OwnerID: ownerID,
		OwnerEmail: "ana@example.com", OwnerName: "Ana", OriginalName: "férias corrompidas.mp4",
		UploadedAt: fixedNow, ErrorMessage: "invalid data found when processing input", OccurredAt: fixedNow,
	}
}

func eventBody(t *testing.T, e app.VideoEvent) []byte {
	t.Helper()
	m, err := app.NewVideoEventMessage(e)
	if err != nil {
		t.Fatal(err)
	}
	return m.Body
}

func TestNotifierSendsFailureMailOnce(t *testing.T) {
	mailer, sent := &fakeMailer{}, newFakeNotificationLog()
	n := app.NewNotifier(mailer, sent, app.WithAppURL("https://videos.example.com"))
	body := eventBody(t, failedEvent())
	for range 3 { // redeliveries of the same event
		if err := n.Handle(context.Background(), body); err != nil {
			t.Fatal(err)
		}
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("%d e-mails, want 1", len(mailer.sent))
	}
	m := mailer.sent[0]
	if m.To != "ana@example.com" || m.ID != eventID || m.Subject != "Video processing failed: férias corrompidas.mp4" {
		t.Errorf("mail %+v", m)
	}
	for _, want := range []string{
		"Hello Ana,", `"férias corrompidas.mp4"`, "invalid data found when processing input",
		"2026-09-24 15:00:00 UTC", "https://videos.example.com", videoID,
	} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body does not contain %q:\n%s", want, m.Body)
		}
	}
	if rec := sent.recorded[eventID]; rec.VideoID != videoID || rec.Recipient != "ana@example.com" || rec.Kind != app.TopicVideoFailed {
		t.Errorf("recorded %+v", rec)
	}
}

func TestNotifierIgnoresProcessedEvents(t *testing.T) {
	mailer := &fakeMailer{}
	e := failedEvent()
	e.Type, e.ErrorMessage, e.FrameCount = app.TopicVideoProcessed, "", 3
	if err := app.NewNotifier(mailer, newFakeNotificationLog()).Handle(context.Background(), eventBody(t, e)); err != nil {
		t.Fatal(err)
	}
	if len(mailer.sent) != 0 {
		t.Errorf("sent %v for a DONE video", mailer.sent)
	}
}

func TestNotifierErrors(t *testing.T) {
	permanent := fmt.Errorf("mailer: RCPT TO refused: %w: 550 no such user", app.ErrPermanent)
	for name, tt := range map[string]struct {
		body    []byte
		sendErr error
		want    error
	}{
		"malformed":         {body: []byte("{"), want: app.ErrMalformedMessage},
		"transient failure": {sendErr: errBoom, want: errBoom},
		"permanent failure": {sendErr: permanent, want: app.ErrPermanent},
	} {
		t.Run(name, func(t *testing.T) {
			body := tt.body
			if body == nil {
				body = eventBody(t, failedEvent())
			}
			sent := newFakeNotificationLog()
			err := app.NewNotifier(&fakeMailer{err: tt.sendErr}, sent).Handle(context.Background(), body)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if len(sent.recorded) != 0 {
				t.Errorf("a failed e-mail was recorded as sent")
			}
		})
	}
}

func TestNotifierRetriesAfterTransientFailure(t *testing.T) {
	mailer, sent := &fakeMailer{err: errBoom}, newFakeNotificationLog()
	n := app.NewNotifier(mailer, sent)
	body := eventBody(t, failedEvent())
	if err := n.Handle(context.Background(), body); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	mailer.err = nil // the mail server is back
	if err := n.Handle(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if len(mailer.sent) != 1 {
		t.Errorf("%d e-mails, want 1", len(mailer.sent))
	}
}

func TestNotifierSentButNotRecordedIsNotRetried(t *testing.T) {
	mailer, sent := &fakeMailer{}, newFakeNotificationLog()
	sent.recordErr = errors.New("connection reset")
	var logs bytes.Buffer
	n := app.NewNotifier(mailer, sent, app.WithNotifierLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err := n.Handle(context.Background(), eventBody(t, failedEvent())); err != nil {
		t.Fatalf("err = %v, want nil (retrying would send the e-mail again)", err)
	}
	if len(mailer.sent) != 1 || !strings.Contains(logs.String(), "not recorded") {
		t.Errorf("%d e-mails; logs: %s", len(mailer.sent), logs.String())
	}
}

func TestNotifierGiveUpOnlyLogs(t *testing.T) {
	var logs bytes.Buffer
	n := app.NewNotifier(&fakeMailer{}, newFakeNotificationLog(), app.WithNotifierLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err := n.GiveUp(context.Background(), eventBody(t, failedEvent()), errBoom); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), eventID) || !strings.Contains(logs.String(), "boom") {
		t.Errorf("logs: %s", logs.String())
	}
}

func TestFailureMailWithoutOwnerName(t *testing.T) {
	e := failedEvent()
	e.OwnerName = " "
	if m := app.FailureMail(e, app.DefaultAppURL); !strings.HasPrefix(m.Body, "Hello,\n") || !strings.Contains(m.Body, app.DefaultAppURL) {
		t.Errorf("body:\n%s", m.Body)
	}
}
