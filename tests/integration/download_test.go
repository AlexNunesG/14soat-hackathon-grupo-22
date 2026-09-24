package integration

// Integration tests for GET /api/v1/videos/{id}/download (docs/openapi.yaml,
// operationId downloadVideoFrames).

import (
	"mime"
	"net/http"
	"testing"
)

func TestDownloadDoneVideoReturnsZip(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	cases := []struct {
		name, wantFile string
	}{
		{"holiday.mp4", "holiday_frames.zip"},
		{"Trip.MKV", "Trip_frames.zip"},
		// Only the last extension is removed.
		{"my.summer.trip.webm", "my.summer.trip_frames.zip"},
	}
	data := makeMP4(t, 2)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uploaded := uploadOne(t, token, tc.name, data)
			done := waitForStatus(t, token, uploaded.ID, statusDone)

			resp, zipData := downloadZip(t, token, done.ID)

			if mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mediaType != "application/zip" {
				t.Errorf("Content-Type = %q, want application/zip", resp.Header.Get("Content-Type"))
			}
			disposition, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
			if err != nil || disposition != "attachment" || params["filename"] != tc.wantFile {
				t.Errorf("Content-Disposition = %q, want attachment with filename %q",
					resp.Header.Get("Content-Disposition"), tc.wantFile)
			}
			assertFrames(t, zipData, *done.FrameCount)
		})
	}
}

// TestDownloadBeforeDoneIsNotReady requests the download right after the
// upload returns.
//
// Timing assumption: the video is still PENDING or PROCESSING when the
// download is requested, because processing it takes far longer than the
// single HTTP round trip between the 202 and the download request. The
// video is 60 s of 1280x720 at 25 fps: a small file (a static color
// compresses to tens of KB) whose decoding alone takes ffmpeg about 0.7 s,
// on top of queueing, fetching the upload, zipping and storing the result,
// while the round trip takes milliseconds.
//
// The test then waits for DONE and checks the same download succeeds, so
// the 409 was about readiness.
func TestDownloadBeforeDoneIsNotReady(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	data := ffmpeg(t, "long.mp4",
		"-f", "lavfi", "-i", "color=c=blue:size=1280x720:rate=25:duration=60",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
	)

	uploaded := uploadOne(t, token, "long.mp4", data)
	resp, body := authGet(t, token, downloadPath(uploaded.ID))

	assertError(t, resp, body, http.StatusConflict, "video_not_ready")

	done := waitForStatus(t, token, uploaded.ID, statusDone)
	_, zipData := downloadZip(t, token, done.ID)
	assertFrames(t, zipData, *done.FrameCount)
}

func TestDownloadFailedVideoIsNotReady(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, token := registerAndLogin(t)
	uploaded := uploadOne(t, token, "broken.avi", corruptVideo())
	waitForStatus(t, token, uploaded.ID, statusFailed)

	resp, body := authGet(t, token, downloadPath(uploaded.ID))

	assertError(t, resp, body, http.StatusConflict, "video_not_ready")
}

func TestDownloadNotFound(t *testing.T) {
	notImplemented(t)
	t.Parallel()
	_, ownerToken := registerAndLogin(t)
	_, otherToken := registerAndLogin(t)
	owned := uploadOne(t, ownerToken, "private.mp4", makeMP4(t, 1))
	// Another user's video is 404 even when it could be downloaded.
	waitForStatus(t, ownerToken, owned.ID, statusDone)

	for name, id := range map[string]string{
		"unknown id":           randomUUID(t),
		"malformed id":         "not-a-uuid",
		"another user's video": owned.ID,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp, body := authGet(t, otherToken, downloadPath(id))
			assertError(t, resp, body, http.StatusNotFound, "not_found")
		})
	}
}
