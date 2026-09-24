package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
