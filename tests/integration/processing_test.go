package integration

// Integration tests for asynchronous processing (docs/openapi.yaml,
// "Processing semantics"): PENDING → PROCESSING → DONE | FAILED, frames
// extracted at 1 fps as PNG, and the zip of frames. Videos are real files
// generated with ffmpeg.

import (
	"fmt"
	"testing"
)

func TestProcessingReachesDone(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	uploaded := uploadOne(t, token, "holiday.mp4", makeMP4(t, 2))

	done := waitForStatus(t, token, uploaded.ID, statusDone)

	if done.ID != uploaded.ID || done.OriginalName != uploaded.OriginalName || done.CreatedAt != uploaded.CreatedAt {
		t.Errorf("the processed video changed identity: uploaded %+v, got %+v", uploaded, done)
	}
	// decodeVideo already checked frame_count >= 1 and error_message null.
	_, zipData := downloadZip(t, token, done.ID)
	assertFrames(t, zipData, *done.FrameCount)
}

func TestProcessingFrameCountFollowsDuration(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	// At 1 fps a video of N whole seconds yields N frames.
	for _, seconds := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("%ds", seconds), func(t *testing.T) {
			t.Parallel()
			_, token := registerAndLogin(t)
			uploaded := uploadOne(t, token, "duration.mp4", makeMP4(t, seconds))

			done := waitForStatus(t, token, uploaded.ID, statusDone)

			if *done.FrameCount != seconds {
				t.Errorf("frame_count = %d, want %d for a %ds video at 1 fps", *done.FrameCount, seconds, seconds)
			}
			_, zipData := downloadZip(t, token, done.ID)
			// frame_count equals the number of entries in the zip.
			assertFrames(t, zipData, *done.FrameCount)
		})
	}
}

func TestProcessingEverySupportedFormat(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	const seconds = 3
	cases := []struct {
		name, ext, codec string
	}{
		{"clip.mp4", "mp4", "libx264"},
		{"clip.avi", "avi", "mpeg4"},
		{"clip.mov", "mov", "mpeg4"},
		{"clip.mkv", "mkv", "libx264"},
		{"clip.wmv", "wmv", "wmv2"},
		{"clip.flv", "flv", "flv"},
		{"clip.webm", "webm", "libvpx"},
		{"UPPER CASE.MP4", "mp4", "mpeg4"},
		{"vídeo com acentuação.Mkv", "mkv", "mpeg4"},
	}
	_, token := registerAndLogin(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uploaded := uploadOne(t, token, tc.name, makeVideo(t, tc.ext, tc.codec, seconds))

			done := waitForStatus(t, token, uploaded.ID, statusDone)

			if *done.FrameCount != seconds {
				t.Errorf("frame_count = %d, want %d", *done.FrameCount, seconds)
			}
			_, zipData := downloadZip(t, token, done.ID)
			assertFrames(t, zipData, seconds)
		})
	}
}

func TestProcessingUndecodableVideoFails(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	cases := []struct {
		name string
		data func(*testing.T) []byte
	}{
		{"corrupt.mp4", func(*testing.T) []byte { return corruptVideo() }},
		{"audio.mp4", makeAudioOnly},
		{"empty.avi", makeZeroFrameVideo},
	}
	_, token := registerAndLogin(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Content is validated by the worker, not by the upload.
			uploaded := uploadOne(t, token, tc.name, tc.data(t))

			failed := waitForStatus(t, token, uploaded.ID, statusFailed)

			if failed.ErrorMessage == nil || *failed.ErrorMessage == "" {
				t.Errorf("error_message must be non-empty, got %v", failed.ErrorMessage)
			}
			if failed.FrameCount != nil {
				t.Errorf("frame_count = %d, want null", *failed.FrameCount)
			}
		})
	}
}
