package integration

// Shared helpers for the integration tests.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var (
	httpClient = &http.Client{Timeout: 2 * time.Minute}
	framePNG   = regexp.MustCompile(`^frame_\d{4}\.png$`)
	zipName    = regexp.MustCompile(`^frames_\d{8}_\d{6}\.zip$`)
)

// uploadResult is the JSON body returned by POST /upload.
type uploadResult struct {
	Success    bool     `json:"success"`
	Message    string   `json:"message"`
	ZipPath    string   `json:"zip_path,omitempty"`
	FrameCount int      `json:"frame_count,omitempty"`
	Images     []string `json:"images,omitempty"`
}

// notImplemented skips a test until the implementation supports it. To
// enable a test, delete its notImplemented line.
func notImplemented(t *testing.T) {
	t.Helper()
	t.Skip("not implemented yet: delete the notImplemented line to enable this test")
}

// appURL returns the absolute URL of path on the app under test. It fails
// the test when no app is running (no BASE_URL and no Go code to build).
func appURL(t *testing.T, path string) string {
	t.Helper()
	if baseURL == "" {
		t.Fatal("no app to test: add the implementation at the module root or set BASE_URL")
	}
	return baseURL + path
}

// requireReferenceApp skips tests that need the app launched by TestMain:
// they inspect or manipulate its working directory (uploads/, outputs/,
// temp/) or start extra instances of its binary. They cannot run against an
// external BASE_URL.
func requireReferenceApp(t *testing.T) {
	t.Helper()
	if referenceDir == "" {
		t.Skip("needs the reference app launched by the test harness; skipped when BASE_URL is set or no app was found")
	}
}

// usingReferenceApp reports whether the app was launched by TestMain, so
// tests can add filesystem checks on top of their black-box assertions.
func usingReferenceApp() bool {
	return referenceDir != ""
}

// resetWorkspace gives each test a clean uploads/outputs/temp layout when
// running the reference app. Against an external BASE_URL it is a no-op, so
// black-box tests must not assume the server starts empty.
func resetWorkspace(t *testing.T) {
	t.Helper()
	if !usingReferenceApp() {
		return
	}
	for _, dir := range []string{"uploads", "outputs", "temp"} {
		if err := os.RemoveAll(filepath.Join(referenceDir, dir)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(referenceDir, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, dir := range []string{"uploads", "outputs", "temp"} {
			os.RemoveAll(filepath.Join(referenceDir, dir))
			_ = os.MkdirAll(filepath.Join(referenceDir, dir), 0755)
		}
	})
}

// replaceDirWithFile turns one of the app directories into a regular file so
// that creating files inside it fails for real (works even when running as root).
func replaceDirWithFile(t *testing.T, dir string) {
	t.Helper()
	requireReferenceApp(t)
	path := filepath.Join(referenceDir, dir)
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
}

// makeVideo renders a real test video with ffmpeg and returns its bytes.
func makeVideo(t *testing.T, ext, codec string, seconds int) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "source."+ext)
	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=size=64x48:rate=25:duration=%d", seconds),
		"-c:v", codec,
		"-y", out,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg could not generate %s video: %v\n%s", ext, err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// upcomingTimestamps returns the timestamps the reference app will use (same
// format as handleVideoUpload) for requests handled within the next few seconds, so
// tests can prepare real filesystem state at the exact paths the app will use.
func upcomingTimestamps(seconds int) []string {
	now := time.Now()
	ts := make([]string, 0, seconds+1)
	for i := 0; i <= seconds; i++ {
		ts = append(ts, now.Add(time.Duration(i)*time.Second).Format("20060102_150405"))
	}
	return ts
}

func uploadRequest(t *testing.T, field, filename string, content []byte) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	if field != "" {
		part, err := w.CreateFormFile(field, filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, appURL(t, "/upload"), body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func upload(t *testing.T, field, filename string, content []byte) (int, uploadResult) {
	t.Helper()
	resp, err := httpClient.Do(uploadRequest(t, field, filename, content))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result uploadResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	return resp.StatusCode, result
}

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

type statusFile struct {
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"created_at"`
	DownloadURL string `json:"download_url"`
}

type statusResponse struct {
	Files []statusFile `json:"files"`
	Total int          `json:"total"`
}

func getStatus(t *testing.T) statusResponse {
	t.Helper()
	resp, body := get(t, "/api/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/status: expected 200, got %d: %s", resp.StatusCode, body)
	}
	var status statusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("invalid status JSON: %v (%s)", err, body)
	}
	return status
}

// dirEntries lists a directory of the reference app's working directory.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	requireReferenceApp(t)
	entries, err := os.ReadDir(filepath.Join(referenceDir, dir))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// assertValidZip opens the ZIP produced by the app and checks every entry is
// a decodable PNG frame with the dimensions of the source video.
func assertValidZip(t *testing.T, data []byte, wantImages []string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("output is not a valid zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		img, err := png.Decode(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s is not a valid PNG: %v", f.Name, err)
		}
		if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 48 {
			t.Errorf("%s: expected 64x48 frame, got %dx%d", f.Name, b.Dx(), b.Dy())
		}
	}
	sort.Strings(names)
	want := append([]string(nil), wantImages...)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("zip entries = %v, want %v", names, want)
	}
}

// download fetches a ZIP through GET /download/:filename and fails the test
// if it is not served.
func download(t *testing.T, name string) []byte {
	t.Helper()
	resp, body := get(t, "/download/"+name)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /download/%s: expected 200, got %d: %s", name, resp.StatusCode, body)
	}
	return body
}
