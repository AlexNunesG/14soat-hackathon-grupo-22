package integration

// Shared helpers for the integration tests. Only helpers used by some test
// live here; new ones arrive together with the tests that need them.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder for image.DecodeConfig
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// httpClient is shared by every request to the app. The timeout keeps a
// stuck server from hanging the suite.
var httpClient = &http.Client{Timeout: 2 * time.Minute}

// notImplemented skips a test until the implementation supports it. To
// enable a test, delete its notImplemented line in the pull request that
// implements the behavior (see tests/integration/README.md).
func notImplemented(t *testing.T) {
	t.Helper()
	t.Skip("not implemented yet: delete the notImplemented line to enable this test (see tests/integration/README.md)")
}

// appURL returns the absolute URL of path on the API under test. It fails
// the test when there is no app: BASE_URL unset and no compose stack
// started, or the API never became healthy.
func appURL(t *testing.T, path string) string {
	t.Helper()
	if baseURL == "" {
		t.Fatal(errNoApp)
	}
	return baseURL + path
}

// get sends GET path to the API and returns the response with its body
// already read and closed.
func get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := httpClient.Get(appURL(t, path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

// assertJSON fails the test unless resp is JSON and body decodes into v.
func assertJSON(t *testing.T, resp *http.Response, body []byte, v any) {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("unexpected content type %q", ct)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("invalid JSON body: %v (%s)", err, body)
	}
}

// ffmpeg runs ffmpeg with args, writing to a file named name in a temp dir,
// and returns the bytes of that file.
func ffmpeg(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	args = append(append([]string{"-loglevel", "error", "-nostdin"}, args...), "-y", out)
	if output, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg could not generate %s: %v\n%s", name, err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// makeVideo renders a small (64x48, 25 fps) test video of the given length
// in the container ext with the video codec, and returns its bytes.
func makeVideo(t *testing.T, ext, codec string, seconds int) []byte {
	t.Helper()
	return ffmpeg(t, "source."+ext,
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=64x48:rate=25:duration=%d", seconds),
		"-c:v", codec, "-pix_fmt", "yuv420p",
	)
}

// makeMP4 is makeVideo for the most common case, an MPEG-4 mp4.
func makeMP4(t *testing.T, seconds int) []byte {
	t.Helper()
	return makeVideo(t, "mp4", "mpeg4", seconds)
}

// makeAudioOnly returns a valid 1 s mp4 file with an AAC audio stream and
// no video stream.
func makeAudioOnly(t *testing.T) []byte {
	t.Helper()
	return ffmpeg(t, "audio.mp4", "-f", "lavfi", "-i", "anullsrc", "-t", "1", "-c:a", "aac")
}

// makeZeroFrameVideo returns a valid AVI with a raw video stream holding
// zero frames: ffmpeg decodes it without error but extracts no image.
func makeZeroFrameVideo(t *testing.T) []byte {
	t.Helper()
	return ffmpeg(t, "empty.avi",
		"-f", "lavfi", "-i", "testsrc=size=64x48",
		"-frames:v", "0", "-c:v", "rawvideo", "-pix_fmt", "bgr24",
	)
}

// corruptVideo is content no decoder accepts, to be uploaded with a
// supported extension.
func corruptVideo() []byte {
	return bytes.Repeat([]byte("this is not a video file\n"), 64)
}

// frameNames returns frame_0001.png ... frame_000N.png.
func frameNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("frame_%04d.png", i+1)
	}
	return names
}

// assertFrames checks that data is a valid zip holding exactly
// frame_0001.png ... frame_<n>.png at its root, each a valid PNG.
func assertFrames(t *testing.T, data []byte, n int) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("download is not a valid zip: %v", err)
	}
	got := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		got = append(got, f.Name)
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("zip entry %s: %v", f.Name, err)
		}
		_, format, err := image.DecodeConfig(rc)
		rc.Close()
		if err != nil || format != "png" {
			t.Errorf("zip entry %s is not a valid PNG (format %q): %v", f.Name, format, err)
		}
	}
	if want := frameNames(n); strings.Join(sortedCopy(got), ",") != strings.Join(want, ",") {
		t.Errorf("zip entries = %v, want exactly %v", got, want)
	}
}

// downloadZip fetches the frames zip of a DONE video, requiring 200, and
// returns the response and the zip bytes.
func downloadZip(t *testing.T, token, id string) (*http.Response, []byte) {
	t.Helper()
	resp, body := authGet(t, token, downloadPath(id))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download %s: expected 200, got %d: %s", id, resp.StatusCode, body)
	}
	return resp, body
}
