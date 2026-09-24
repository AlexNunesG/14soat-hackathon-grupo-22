package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors returned by the Video methods.
var (
	// ErrInvalidTransition is wrapped when a status change is not allowed
	// from the video's current status.
	ErrInvalidTransition = errors.New("invalid video status transition")
	// ErrInvalidVideo is wrapped when a video's fields break an invariant.
	ErrInvalidVideo = errors.New("invalid video")
)

// Video is an uploaded video and the state of its processing.
//
// Invariants: FrameCount is > 0 and ZipKey is set only when Status is DONE;
// ErrorMessage is non-empty only when Status is FAILED. Zero values mean
// "none" (null in the API).
type Video struct {
	ID           string
	OwnerID      string
	OriginalName string // file name as uploaded, extension and case kept
	StorageKey   string // object key of the uploaded video
	ZipKey       string // object key of the frames archive, set when DONE
	Status       VideoStatus
	FrameCount   int    // number of frames in the archive, set when DONE
	ErrorMessage string // why processing failed, set when FAILED
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewVideo returns a PENDING video. It fails when a field is empty or the
// original name has an unsupported format (the error then also wraps
// ErrUnsupportedFormat).
func NewVideo(id, ownerID, originalName, storageKey string, now time.Time) (*Video, error) {
	required := []struct{ field, value string }{
		{"id", id}, {"owner id", ownerID}, {"original name", originalName}, {"storage key", storageKey},
	}
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			return nil, fmt.Errorf("%w: %s is empty", ErrInvalidVideo, r.field)
		}
	}
	if err := ValidateFormat(originalName); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidVideo, err)
	}
	now = now.UTC()
	return &Video{
		ID:           id,
		OwnerID:      ownerID,
		OriginalName: originalName,
		StorageKey:   storageKey,
		Status:       StatusPending,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// ZipName is the download file name of the video's frames archive.
func (v *Video) ZipName() string { return ZipName(v.OriginalName) }

// Start moves the video to PROCESSING.
func (v *Video) Start(now time.Time) error {
	return v.transition(StatusProcessing, now)
}

// Complete moves the video to DONE with its archive key and frame count.
func (v *Video) Complete(zipKey string, frameCount int, now time.Time) error {
	if strings.TrimSpace(zipKey) == "" {
		return fmt.Errorf("%w: zip key is empty", ErrInvalidVideo)
	}
	if frameCount < 1 {
		return fmt.Errorf("%w: frame count %d, want at least 1", ErrInvalidVideo, frameCount)
	}
	if err := v.transition(StatusDone, now); err != nil {
		return err
	}
	v.ZipKey, v.FrameCount = zipKey, frameCount
	return nil
}

// Fail moves the video to FAILED with a non-empty reason.
func (v *Video) Fail(reason string, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: failure reason is empty", ErrInvalidVideo)
	}
	if err := v.transition(StatusFailed, now); err != nil {
		return err
	}
	v.ErrorMessage = reason
	return nil
}

func (v *Video) transition(next VideoStatus, now time.Time) error {
	if !v.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, v.Status, next)
	}
	v.Status = next
	v.UpdatedAt = now.UTC()
	return nil
}
