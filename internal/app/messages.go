package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/domain"
)

// TopicVideoUploaded is the routing key of the message that asks the
// workers to process a newly uploaded video.
const TopicVideoUploaded = "video.uploaded"

// ErrMalformedMessage is wrapped when a message body cannot be decoded.
// Retrying cannot fix it: consumers dead-letter such messages at once.
var ErrMalformedMessage = errors.New("malformed message")

// Message is a message to publish: ID is its message id (the same on every
// redelivery or republish, so consumers can recognize duplicates), Topic its
// routing key and Body its JSON payload.
type Message struct {
	ID    string
	Topic string
	Body  []byte
}

// VideoUploaded is the payload of TopicVideoUploaded. It carries only the
// video id: the worker reads everything else from the database, which is
// the source of truth.
type VideoUploaded struct {
	VideoID string `json:"video_id"`
}

// NewVideoUploadedMessage returns the message announcing v. Its id is the
// video id.
func NewVideoUploadedMessage(v *domain.Video) (Message, error) {
	body, err := json.Marshal(VideoUploaded{VideoID: v.ID})
	if err != nil {
		return Message{}, fmt.Errorf("encode %s message: %w", TopicVideoUploaded, err)
	}
	return Message{ID: v.ID, Topic: TopicVideoUploaded, Body: body}, nil
}

// DecodeVideoUploaded decodes a TopicVideoUploaded payload. A body that is
// not such a payload, or has no video id, yields an error wrapping
// ErrMalformedMessage.
func DecodeVideoUploaded(body []byte) (VideoUploaded, error) {
	var m VideoUploaded
	if err := json.Unmarshal(body, &m); err != nil {
		return VideoUploaded{}, fmt.Errorf("%w: %s: %w", ErrMalformedMessage, TopicVideoUploaded, err)
	}
	if strings.TrimSpace(m.VideoID) == "" {
		return VideoUploaded{}, fmt.Errorf("%w: %s: video_id is empty", ErrMalformedMessage, TopicVideoUploaded)
	}
	return m, nil
}

// Routing keys of the events the worker records when a video reaches a
// final status (RF5). They are queued in the outbox in the same
// transaction as the status change (ADR 0004), so an event exists exactly
// when its status change was committed.
const (
	// TopicVideoFailed announces that a video ended FAILED. The notifier
	// e-mails its owner.
	TopicVideoFailed = "video.failed"
	// TopicVideoProcessed announces that a video ended DONE. No service
	// consumes it yet; it is published so future consumers (a "your frames
	// are ready" e-mail, metrics) need no change in the worker.
	TopicVideoProcessed = "video.processed"
)

// ErrPermanent is wrapped by handler errors that retrying cannot fix (for
// example an e-mail address the mail server rejects): consumers
// dead-letter such messages at once, like malformed ones.
var ErrPermanent = errors.New("permanent failure")

// VideoEvent is the payload of TopicVideoFailed and TopicVideoProcessed. It
// is self-contained: a consumer needs no database read to act on it. The
// owner's e-mail and name are the ones registered when the event occurred.
type VideoEvent struct {
	// EventID identifies the event (a UUID); it is also the message id.
	// Consumers deduplicate redeliveries by it.
	EventID string `json:"event_id"`
	// Type is the event's routing key: TopicVideoFailed or
	// TopicVideoProcessed.
	Type         string `json:"type"`
	VideoID      string `json:"video_id"`
	OwnerID      string `json:"owner_id"`
	OwnerEmail   string `json:"owner_email"`
	OwnerName    string `json:"owner_name"`
	OriginalName string `json:"original_name"`
	// UploadedAt is when the video was uploaded (its created_at).
	UploadedAt time.Time `json:"uploaded_at"`
	// ErrorMessage is the video's error_message (failed only).
	ErrorMessage string `json:"error_message,omitempty"`
	// FrameCount is the number of frames extracted (processed only).
	FrameCount int `json:"frame_count,omitempty"`
	// OccurredAt is when the video reached its final status.
	OccurredAt time.Time `json:"occurred_at"`
}

// NewVideoEventMessage returns the message carrying e. Its id is the event
// id and its topic the event type.
func NewVideoEventMessage(e VideoEvent) (Message, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return Message{}, fmt.Errorf("encode %s event: %w", e.Type, err)
	}
	return Message{ID: e.EventID, Topic: e.Type, Body: body}, nil
}

// DecodeVideoEvent decodes a TopicVideoFailed or TopicVideoProcessed
// payload. A body that is not such a payload, or lacks the event id, type,
// video id or owner e-mail, yields an error wrapping ErrMalformedMessage.
func DecodeVideoEvent(body []byte) (VideoEvent, error) {
	var e VideoEvent
	if err := json.Unmarshal(body, &e); err != nil {
		return VideoEvent{}, fmt.Errorf("%w: video event: %w", ErrMalformedMessage, err)
	}
	var missing []string
	for field, v := range map[string]string{
		"event_id": e.EventID, "type": e.Type, "video_id": e.VideoID, "owner_email": e.OwnerEmail,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return VideoEvent{}, fmt.Errorf("%w: video event: empty %s", ErrMalformedMessage, strings.Join(missing, ", "))
	}
	if uuid.Validate(e.EventID) != nil || uuid.Validate(e.VideoID) != nil {
		return VideoEvent{}, fmt.Errorf("%w: video event: event_id and video_id must be UUIDs", ErrMalformedMessage)
	}
	return e, nil
}
