package main

// Integration tests for the video processor.
//
// This file holds the shared setup (TestMain) and helpers; each endpoint has
// its own *_test.go file with its integration tests.
//
// No mocks are used: TestMain boots the real application (main()) inside an
// isolated working directory, every test talks to it over real HTTP on
// :8080, videos are generated and processed by the real ffmpeg binary, and
// assertions are made against the real filesystem and real ZIP/PNG output.
//
// Requirements: ffmpeg in PATH and port 8080 free.
// Run with: go test -v -count=1 -coverprofile=coverage.out ./...

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const baseURL = "http://127.0.0.1:8080"

var (
	workDir    string
	httpClient = &http.Client{Timeout: 2 * time.Minute}
	framePNG   = regexp.MustCompile(`^frame_\d{4}\.png$`)
	zipName    = regexp.MustCompile(`^frames_\d{8}_\d{6}\.zip$`)
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintln(os.Stderr, "integration tests require ffmpeg in PATH:", err)
		return 1
	}

	ln, err := net.Listen("tcp", ":8080")
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration tests require port 8080 to be free:", err)
		return 1
	}
	ln.Close()

	// The app uses paths relative to the working directory; run it in a
	// throwaway directory so the repository is never touched.
	workDir, err = os.MkdirTemp("", "video-processor-it-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(workDir)

	if err := os.Chdir(workDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter = io.Discard

	go main()

	if err := waitForServer(15 * time.Second); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	return m.Run()
}

func waitForServer(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/status")
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("server did not start within %s", timeout)
}

// resetWorkspace gives each test a clean uploads/outputs/temp layout.
func resetWorkspace(t *testing.T) {
	t.Helper()
	for _, dir := range []string{"uploads", "outputs", "temp"} {
		if err := os.RemoveAll(filepath.Join(workDir, dir)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(workDir, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, dir := range []string{"uploads", "outputs", "temp"} {
			os.RemoveAll(filepath.Join(workDir, dir))
			os.MkdirAll(filepath.Join(workDir, dir), 0755)
		}
	})
}

// replaceDirWithFile turns one of the app directories into a regular file so
// that creating files inside it fails for real (works even when running as root).
func replaceDirWithFile(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(workDir, dir)
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

// upcomingTimestamps returns the timestamps the server will use (same format
// as handleVideoUpload) for requests handled within the next few seconds, so
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
	req, err := http.NewRequest(http.MethodPost, baseURL+"/upload", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func upload(t *testing.T, field, filename string, content []byte) (int, ProcessingResult) {
	t.Helper()
	resp, err := httpClient.Do(uploadRequest(t, field, filename, content))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result ProcessingResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	return resp.StatusCode, result
}

func get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := httpClient.Get(baseURL + path)
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

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(workDir, dir))
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
		if f.Method != zip.Deflate {
			t.Errorf("%s: expected Deflate compression, got method %d", f.Name, f.Method)
		}
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
