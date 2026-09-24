package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DefaultAppURL is the link to the web UI in the e-mails when none is
// configured (the local compose stack).
const DefaultAppURL = "http://localhost:8080"

// Notifier is the notification use case (RF5): it e-mails the owner of a
// video that ended FAILED, once per event.
//
// Events are delivered at least once (outbox republish, broker redelivery),
// so the notifier remembers the events it notified (NotificationLog) and
// skips a redelivered one. Only TopicVideoFailed sends an e-mail; other
// events are acknowledged and ignored.
type Notifier struct {
	mailer  Mailer
	sent    NotificationLog
	appURL  string
	log     *slog.Logger
	metrics OutcomeRecorder
}

// NotifierOption customizes Notifier.
type NotifierOption func(*Notifier)

// WithNotifierLogger sets the logger.
func WithNotifierLogger(log *slog.Logger) NotifierOption {
	return func(n *Notifier) { n.log = log }
}

// WithAppURL sets the link to the web UI written in the e-mails.
func WithAppURL(url string) NotifierOption {
	return func(n *Notifier) { n.appURL = url }
}

// WithNotifierMetrics sets the metrics port: the outcome of every event
// handled without error (sent, duplicate, ignored) and its duration.
func WithNotifierMetrics(m OutcomeRecorder) NotifierOption {
	return func(n *Notifier) { n.metrics = m }
}

// NewNotifier returns the notification use case.
func NewNotifier(mailer Mailer, sent NotificationLog, opts ...NotifierOption) *Notifier {
	n := &Notifier{mailer: mailer, sent: sent, appURL: DefaultAppURL, log: slog.New(slog.DiscardHandler), metrics: noMetrics{}}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// Handle notifies the event in body. It returns nil when the event is done
// with (e-mail sent, already sent, or nothing to send), an error wrapping
// ErrMalformedMessage for a body that is not an event, an error wrapping
// ErrPermanent when the mail server refused the e-mail for good, and any
// other error for a failure worth retrying. When it returns nil, it
// reports the event's outcome to the metrics port.
func (n *Notifier) Handle(ctx context.Context, body []byte) error {
	begin := time.Now()
	outcome, err := n.handle(ctx, body)
	if err == nil {
		n.metrics.RecordOutcome(outcome, time.Since(begin))
	}
	return err
}

// handle is Handle; it returns the event's outcome when err is nil.
func (n *Notifier) handle(ctx context.Context, body []byte) (Outcome, error) {
	e, err := DecodeVideoEvent(body)
	if err != nil {
		return "", err
	}
	log := n.log.With(slog.String("event_id", e.EventID), slog.String("video_id", e.VideoID))
	if e.Type != TopicVideoFailed {
		log.DebugContext(ctx, "event ignored: nothing to notify", slog.String("type", e.Type))
		return OutcomeIgnored, nil
	}
	mail := FailureMail(e, n.appURL)
	record := SentNotification{EventID: e.EventID, VideoID: e.VideoID, Kind: e.Type, Recipient: e.OwnerEmail}
	sent, err := n.sent.SendOnce(ctx, record, func(ctx context.Context) error {
		return n.mailer.Send(ctx, mail)
	})
	switch {
	case errors.Is(err, ErrNotRecorded):
		// Retrying would send the e-mail again; a later duplicate of the
		// event is the lesser risk.
		log.WarnContext(ctx, "failure e-mail sent, but not recorded as sent", slog.Any("error", err))
		return OutcomeSent, nil
	case err != nil:
		return "", fmt.Errorf("send the failure e-mail of video %s: %w", e.VideoID, err)
	case !sent:
		log.InfoContext(ctx, "failure e-mail already sent; duplicate event ignored")
		return OutcomeDuplicate, nil
	default:
		log.InfoContext(ctx, "failure e-mail sent")
		return OutcomeSent, nil
	}
}

// GiveUp is called when the e-mail of the event in body could not be sent
// after every attempt, before the event is dead-lettered (to video.notify.dlq,
// where it can be replayed). It only logs.
func (n *Notifier) GiveUp(ctx context.Context, body []byte, cause error) error {
	e, _ := DecodeVideoEvent(body)
	n.log.ErrorContext(ctx, "giving up on a failure e-mail; event dead-lettered",
		slog.String("event_id", e.EventID), slog.String("video_id", e.VideoID), slog.Any("error", cause))
	return nil
}

// FailureMail is the e-mail telling the owner of the video in e (a
// TopicVideoFailed event) that its processing failed: the subject has the
// original file name and the body the error message, the upload time and
// a link to the web UI at appURL.
func FailureMail(e VideoEvent, appURL string) Mail {
	var b strings.Builder
	if name := strings.TrimSpace(e.OwnerName); name != "" {
		fmt.Fprintf(&b, "Hello %s,\n\n", name)
	} else {
		b.WriteString("Hello,\n\n")
	}
	fmt.Fprintf(&b, "We could not process your video \"%s\", uploaded on %s.\n\n",
		e.OriginalName, e.UploadedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	fmt.Fprintf(&b, "Reason: %s\n\n", e.ErrorMessage)
	fmt.Fprintf(&b, "No frames were extracted. You can check your videos and upload it again at %s\n\n", appURL)
	fmt.Fprintf(&b, "Video id: %s\nFailed at: %s\n\n", e.VideoID, e.OccurredAt.UTC().Format(time.RFC3339))
	b.WriteString("-- \nFIAP X Video Processor\n")
	return Mail{
		ID:      e.EventID,
		To:      e.OwnerEmail,
		Subject: "Video processing failed: " + e.OriginalName,
		Body:    b.String(),
	}
}
