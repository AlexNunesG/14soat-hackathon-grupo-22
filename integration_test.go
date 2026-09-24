package main

// Integration tests for the video processor.
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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// GET /
// ---------------------------------------------------------------------------

func TestIndexServesUploadPage(t *testing.T) {
	resp, body := get(t, "/")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
	html := string(body)
	for _, want := range []string{
		"<!DOCTYPE html>",
		"<title>FIAP X - Processador de Vídeos</title>",
		`id="uploadForm"`,
		`id="videoFile"`,
		"formData.append('video', file)",
		`accept="video/*"`,
		"fetch('/upload'",
		"fetch('/api/status')",
		"/download/",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index page missing %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// CORS middleware
// ---------------------------------------------------------------------------

func TestCORSHeadersOnEveryResponse(t *testing.T) {
	for _, path := range []string{"/", "/api/status", "/download/missing.zip"} {
		t.Run(path, func(t *testing.T) {
			resp, _ := get(t, path)
			assertCORS(t, resp)
		})
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	for _, path := range []string{"/upload", "/api/status", "/download/x.zip", "/any/unknown/route"} {
		t.Run(path, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodOptions, baseURL+path, nil)
			req.Header.Set("Origin", "http://example.com")
			req.Header.Set("Access-Control-Request-Method", "POST")
			resp, err := httpClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("expected 204, got %d", resp.StatusCode)
			}
			if len(body) != 0 {
				t.Errorf("expected empty body, got %q", body)
			}
			assertCORS(t, resp)
		})
	}
}

func assertCORS(t *testing.T, resp *http.Response) {
	t.Helper()
	want := map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "POST, GET, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type",
	}
	for h, v := range want {
		if got := resp.Header.Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
}

// ---------------------------------------------------------------------------
// POST /upload — happy paths
// ---------------------------------------------------------------------------

func TestUploadExtractsFramesForEverySupportedFormat(t *testing.T) {
	cases := []struct {
		filename string
		ext      string
		codec    string
	}{
		{"clip.mp4", "mp4", "mpeg4"},
		{"clip.avi", "avi", "mpeg4"},
		{"clip.mov", "mov", "mpeg4"},
		{"clip.mkv", "mkv", "mpeg4"},
		{"clip.wmv", "wmv", "wmv2"},
		{"clip.flv", "flv", "flv"},
		{"clip.webm", "webm", "libvpx"},
		{"UPPER CASE.MP4", "mp4", "mpeg4"},
		{"vídeo com acentuação.Mkv", "mkv", "mpeg4"},
	}

	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			resetWorkspace(t)
			video := makeVideo(t, tc.ext, tc.codec, 3)

			status, result := upload(t, "video", tc.filename, video)

			if status != http.StatusOK {
				t.Fatalf("expected 200, got %d: %+v", status, result)
			}
			if !result.Success {
				t.Fatalf("expected success, got %+v", result)
			}
			if result.FrameCount != 3 {
				t.Errorf("expected 3 frames for a 3s video at fps=1, got %d", result.FrameCount)
			}
			if want := fmt.Sprintf("Processamento concluído! %d frames extraídos.", result.FrameCount); result.Message != want {
				t.Errorf("message = %q, want %q", result.Message, want)
			}
			if len(result.Images) != result.FrameCount {
				t.Errorf("images (%d) and frame_count (%d) disagree", len(result.Images), result.FrameCount)
			}
			for _, img := range result.Images {
				if !framePNG.MatchString(img) {
					t.Errorf("unexpected image name %q", img)
				}
			}
			if !zipName.MatchString(result.ZipPath) {
				t.Fatalf("unexpected zip name %q", result.ZipPath)
			}

			zipData, err := os.ReadFile(filepath.Join(workDir, "outputs", result.ZipPath))
			if err != nil {
				t.Fatalf("zip not written to outputs: %v", err)
			}
			assertValidZip(t, zipData, result.Images)

			if left := dirEntries(t, "uploads"); len(left) != 0 {
				t.Errorf("uploaded video should be removed after success, found %v", left)
			}
			if left := dirEntries(t, "temp"); len(left) != 0 {
				t.Errorf("temp frames should be cleaned up, found %v", left)
			}
		})
	}
}

func TestUploadFrameCountFollowsVideoDuration(t *testing.T) {
	for _, seconds := range []int{1, 5} {
		t.Run(fmt.Sprintf("%ds", seconds), func(t *testing.T) {
			resetWorkspace(t)
			status, result := upload(t, "video", "duration.mp4", makeVideo(t, "mp4", "mpeg4", seconds))

			if status != http.StatusOK || !result.Success {
				t.Fatalf("expected success, got %d %+v", status, result)
			}
			if result.FrameCount != seconds {
				t.Errorf("expected %d frames, got %d", seconds, result.FrameCount)
			}
			want := make([]string, seconds)
			for i := range want {
				want[i] = fmt.Sprintf("frame_%04d.png", i+1)
			}
			if strings.Join(result.Images, ",") != strings.Join(want, ",") {
				t.Errorf("images = %v, want %v", result.Images, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// POST /upload — error paths
// ---------------------------------------------------------------------------

func TestUploadWithoutVideoFieldIsRejected(t *testing.T) {
	resetWorkspace(t)

	t.Run("empty multipart form", func(t *testing.T) {
		status, result := upload(t, "", "", nil)
		assertUploadError(t, status, result, http.StatusBadRequest, "Erro ao receber arquivo: ")
	})

	t.Run("file sent under another field name", func(t *testing.T) {
		status, result := upload(t, "file", "clip.mp4", []byte("data"))
		assertUploadError(t, status, result, http.StatusBadRequest, "Erro ao receber arquivo: ")
	})

	t.Run("non multipart body", func(t *testing.T) {
		resp, err := httpClient.Post(baseURL+"/upload", "application/json", strings.NewReader(`{"video_path":"x.mp4"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result ProcessingResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		assertUploadError(t, resp.StatusCode, result, http.StatusBadRequest, "Erro ao receber arquivo: ")
	})

	if left := dirEntries(t, "uploads"); len(left) != 0 {
		t.Errorf("nothing should be stored, found %v", left)
	}
}

func TestUploadRejectsUnsupportedExtensions(t *testing.T) {
	resetWorkspace(t)
	video := makeVideo(t, "mp4", "mpeg4", 1)

	for _, name := range []string{"clip.txt", "clip.gif", "clip.mp3", "clip", "clip.mp4.exe", "mp4", ".mp4x"} {
		t.Run(name, func(t *testing.T) {
			status, result := upload(t, "video", name, video)
			assertUploadError(t, status, result, http.StatusBadRequest,
				"Formato de arquivo não suportado. Use: mp4, avi, mov, mkv")
		})
	}

	if left := dirEntries(t, "uploads"); len(left) != 0 {
		t.Errorf("rejected files must not be stored, found %v", left)
	}
}

func TestUploadOfCorruptVideoReportsFFmpegErrorAndKeepsFile(t *testing.T) {
	resetWorkspace(t)
	content := []byte("this is definitely not a video stream")

	status, result := upload(t, "video", "broken.mp4", content)

	assertUploadError(t, status, result, http.StatusOK, "Erro no ffmpeg: ")
	if !strings.Contains(result.Message, "Output:") {
		t.Errorf("expected ffmpeg output in message, got %q", result.Message)
	}

	// On failure the original upload is kept (and is served statically).
	stored := dirEntries(t, "uploads")
	if len(stored) != 1 || !strings.HasSuffix(stored[0], "_broken.mp4") {
		t.Fatalf("expected the failed upload to be kept, found %v", stored)
	}
	resp, body := get(t, "/uploads/"+stored[0])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /uploads/%s: expected 200, got %d", stored[0], resp.StatusCode)
	}
	if !bytes.Equal(body, content) {
		t.Errorf("stored upload differs from what was sent")
	}

	if left := dirEntries(t, "temp"); len(left) != 0 {
		t.Errorf("temp dir should be cleaned up, found %v", left)
	}
	if out := dirEntries(t, "outputs"); len(out) != 0 {
		t.Errorf("no zip should be produced, found %v", out)
	}
}

func TestUploadOfAudioOnlyFileFails(t *testing.T) {
	resetWorkspace(t)
	src := filepath.Join(t.TempDir(), "audio.mp4")
	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc", "-t", "1", "-c:a", "aac", "-y", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	audio, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	status, result := upload(t, "video", "audio.mp4", audio)

	assertUploadError(t, status, result, http.StatusOK, "Erro no ffmpeg: ")
}

func TestUploadFailsWhenUploadsDirIsNotWritable(t *testing.T) {
	resetWorkspace(t)
	replaceDirWithFile(t, "uploads")

	status, result := upload(t, "video", "clip.mp4", makeVideo(t, "mp4", "mpeg4", 1))

	assertUploadError(t, status, result, http.StatusInternalServerError, "Erro ao salvar arquivo: ")
}

func TestUploadFailsWhenDiskIsFullWhileSaving(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available on this platform")
	}
	resetWorkspace(t)
	video := makeVideo(t, "mp4", "mpeg4", 1)

	// Point the exact destination path at /dev/full: the kernel accepts the
	// open but every write fails with ENOSPC.
	for _, ts := range upcomingTimestamps(5) {
		if err := os.Symlink("/dev/full", filepath.Join(workDir, "uploads", ts+"_full.mp4")); err != nil {
			t.Fatal(err)
		}
	}

	status, result := upload(t, "video", "full.mp4", video)

	assertUploadError(t, status, result, http.StatusInternalServerError, "Erro ao salvar arquivo: ")
	if !strings.Contains(result.Message, "no space left on device") {
		t.Errorf("expected ENOSPC in message, got %q", result.Message)
	}
}

func TestUploadOfVideoWithoutFramesReportsNoFrames(t *testing.T) {
	resetWorkspace(t)

	// An AVI with a valid raw video stream but zero frames: ffmpeg exits 0
	// and writes no images.
	src := filepath.Join(t.TempDir(), "empty.avi")
	cmd := exec.Command("ffmpeg", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=64x48",
		"-frames:v", "0", "-c:v", "rawvideo", "-pix_fmt", "bgr24",
		"-y", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	video, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	status, result := upload(t, "video", "empty.avi", video)

	assertUploadError(t, status, result, http.StatusOK, "Nenhum frame foi extraído do vídeo")
	if out := dirEntries(t, "outputs"); len(out) != 0 {
		t.Errorf("no zip should be produced, found %v", out)
	}
	if left := dirEntries(t, "temp"); len(left) != 0 {
		t.Errorf("temp dir should be cleaned up, found %v", left)
	}
}

// Files that match *.png inside the processing directory but cannot be
// archived make ZIP creation fail.
func TestUploadFailsWhenAFrameCannotBeArchived(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"unreadable frame (dangling symlink)": func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(workDir, "nowhere"), path); err != nil {
				t.Fatal(err)
			}
		},
		"frame is a directory": func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
		},
	}

	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			resetWorkspace(t)
			video := makeVideo(t, "mp4", "mpeg4", 1)

			for _, ts := range upcomingTimestamps(5) {
				dir := filepath.Join(workDir, "temp", ts)
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				prepare(t, filepath.Join(dir, "a_bad_frame.png"))
			}

			status, result := upload(t, "video", "clip.mp4", video)

			assertUploadError(t, status, result, http.StatusOK, "Erro ao criar arquivo ZIP: ")
			if !strings.Contains(result.Message, "a_bad_frame.png") {
				t.Errorf("expected the offending frame in message, got %q", result.Message)
			}
			if kept := dirEntries(t, "uploads"); len(kept) != 1 {
				t.Errorf("upload should be kept when processing fails, found %v", kept)
			}
		})
	}
}

func TestUploadFailsWhenZipCannotBeCreated(t *testing.T) {
	resetWorkspace(t)
	replaceDirWithFile(t, "outputs")

	status, result := upload(t, "video", "clip.mp4", makeVideo(t, "mp4", "mpeg4", 2))

	assertUploadError(t, status, result, http.StatusOK, "Erro ao criar arquivo ZIP: ")
	if left := dirEntries(t, "temp"); len(left) != 0 {
		t.Errorf("temp frames should be cleaned up, found %v", left)
	}
	if kept := dirEntries(t, "uploads"); len(kept) != 1 {
		t.Errorf("upload should be kept when processing fails, found %v", kept)
	}
}

func assertUploadError(t *testing.T, gotStatus int, result ProcessingResult, wantStatus int, msgPrefix string) {
	t.Helper()
	if gotStatus != wantStatus {
		t.Errorf("status = %d, want %d (%+v)", gotStatus, wantStatus, result)
	}
	if result.Success {
		t.Errorf("expected success=false")
	}
	if !strings.HasPrefix(result.Message, msgPrefix) {
		t.Errorf("message = %q, want prefix %q", result.Message, msgPrefix)
	}
	if result.ZipPath != "" || result.FrameCount != 0 || len(result.Images) != 0 {
		t.Errorf("error response must not carry results: %+v", result)
	}
}

// ---------------------------------------------------------------------------
// GET /download/:filename and static /outputs
// ---------------------------------------------------------------------------

func TestDownloadReturnsGeneratedZip(t *testing.T) {
	resetWorkspace(t)
	_, result := upload(t, "video", "clip.mp4", makeVideo(t, "mp4", "mpeg4", 2))
	if !result.Success {
		t.Fatalf("upload failed: %+v", result)
	}

	resp, body := get(t, "/download/"+result.ZipPath)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	headers := map[string]string{
		"Content-Type":              "application/zip",
		"Content-Disposition":       "attachment; filename=" + result.ZipPath,
		"Content-Description":       "File Transfer",
		"Content-Transfer-Encoding": "binary",
	}
	for h, v := range headers {
		if got := resp.Header.Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
	onDisk, err := os.ReadFile(filepath.Join(workDir, "outputs", result.ZipPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, onDisk) {
		t.Errorf("downloaded zip differs from file on disk")
	}
	assertValidZip(t, body, result.Images)

	// The same archive is also exposed through the static /outputs route.
	staticResp, staticBody := get(t, "/outputs/"+result.ZipPath)
	if staticResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /outputs/%s: expected 200, got %d", result.ZipPath, staticResp.StatusCode)
	}
	if !bytes.Equal(staticBody, onDisk) {
		t.Errorf("static zip differs from file on disk")
	}
}

func TestDownloadUnknownFileReturns404(t *testing.T) {
	resetWorkspace(t)

	for _, name := range []string{"missing.zip", "frames_20000101_000000.zip", "outputs.zip"} {
		t.Run(name, func(t *testing.T) {
			resp, body := get(t, "/download/"+name)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", resp.StatusCode, body)
			}
			var payload map[string]string
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if payload["error"] != "Arquivo não encontrado" {
				t.Errorf("error = %q", payload["error"])
			}
		})
	}
}

func TestStaticRoutesReturn404ForMissingFiles(t *testing.T) {
	resetWorkspace(t)
	for _, path := range []string{"/outputs/nope.zip", "/uploads/nope.mp4"} {
		resp, _ := get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: expected 404, got %d", path, resp.StatusCode)
		}
	}
}

// ---------------------------------------------------------------------------
// GET /api/status
// ---------------------------------------------------------------------------

func TestStatusWithNoProcessedFiles(t *testing.T) {
	resetWorkspace(t)

	resp, body := get(t, "/api/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("unexpected content type %q", ct)
	}
	var status statusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if status.Total != 0 || len(status.Files) != 0 {
		t.Errorf("expected empty status, got %+v", status)
	}
}

func TestStatusListsOnlyReadableZipFiles(t *testing.T) {
	resetWorkspace(t)
	outputs := filepath.Join(workDir, "outputs")

	// Only *.zip files are listed ...
	if err := os.WriteFile(filepath.Join(outputs, "notes.txt"), []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}
	// ... and entries that cannot be stat'ed (dangling symlink) are skipped.
	if err := os.Symlink(filepath.Join(outputs, "does-not-exist"), filepath.Join(outputs, "dangling.zip")); err != nil {
		t.Fatal(err)
	}
	manual := []byte("PK\x05\x06" + strings.Repeat("\x00", 18)) // empty zip
	if err := os.WriteFile(filepath.Join(outputs, "manual.zip"), manual, 0644); err != nil {
		t.Fatal(err)
	}

	status := getStatus(t)

	if status.Total != 1 || len(status.Files) != 1 {
		t.Fatalf("expected exactly manual.zip, got %+v", status)
	}
	f := status.Files[0]
	if f.Filename != "manual.zip" || f.Size != int64(len(manual)) || f.DownloadURL != "/download/manual.zip" {
		t.Errorf("unexpected entry %+v", f)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", f.CreatedAt); err != nil {
		t.Errorf("created_at %q has unexpected format: %v", f.CreatedAt, err)
	}
}

// ---------------------------------------------------------------------------
// End-to-end flow, exactly as the web page drives it
// ---------------------------------------------------------------------------

func TestEndToEndUploadStatusDownload(t *testing.T) {
	resetWorkspace(t)

	var zips []string
	for i, seconds := range []int{2, 4} {
		if i > 0 {
			// ZIP names have one-second resolution; avoid overwriting the first one.
			time.Sleep(1100 * time.Millisecond)
		}
		status, result := upload(t, "video", fmt.Sprintf("movie%d.mp4", i), makeVideo(t, "mp4", "mpeg4", seconds))
		if status != http.StatusOK || !result.Success {
			t.Fatalf("upload %d failed: %d %+v", i, status, result)
		}
		zips = append(zips, result.ZipPath)
	}

	status := getStatus(t)
	if status.Total != 2 || len(status.Files) != 2 {
		t.Fatalf("expected 2 processed files, got %+v", status)
	}

	byName := map[string]statusFile{}
	for _, f := range status.Files {
		byName[f.Filename] = f
	}
	for _, name := range zips {
		f, ok := byName[name]
		if !ok {
			t.Fatalf("status does not list %s: %+v", name, status)
		}
		info, err := os.Stat(filepath.Join(workDir, "outputs", name))
		if err != nil {
			t.Fatal(err)
		}
		if f.Size != info.Size() {
			t.Errorf("%s: size %d, want %d", name, f.Size, info.Size())
		}

		resp, body := get(t, f.DownloadURL)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", f.DownloadURL, resp.StatusCode)
		}
		if int64(len(body)) != f.Size {
			t.Errorf("%s: downloaded %d bytes, status says %d", name, len(body), f.Size)
		}
		if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); err != nil {
			t.Errorf("%s: not a valid zip: %v", name, err)
		}
	}
}
