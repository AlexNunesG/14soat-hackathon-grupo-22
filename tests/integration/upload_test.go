package integration

// Integration tests for POST /upload
//
// The first group is black-box and runs against any implementation. The
// second group (requireReferenceApp) reaches failure paths by manipulating the
// reference app's working directory and is skipped when BASE_URL is set.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Black-box
// ---------------------------------------------------------------------------

func TestUploadExtractsFramesForEverySupportedFormat(t *testing.T) {
	notImplemented(t)
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

			assertValidZip(t, download(t, result.ZipPath), result.Images)

			if usingReferenceApp() {
				if left := dirEntries(t, "uploads"); len(left) != 0 {
					t.Errorf("uploaded video should be removed after success, found %v", left)
				}
				if left := dirEntries(t, "temp"); len(left) != 0 {
					t.Errorf("temp frames should be cleaned up, found %v", left)
				}
			}
		})
	}
}

func TestUploadFrameCountFollowsVideoDuration(t *testing.T) {
	notImplemented(t)
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

func TestUploadWithoutVideoFieldIsRejected(t *testing.T) {
	notImplemented(t)
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
		resp, err := httpClient.Post(appURL(t, "/upload"), "application/json", strings.NewReader(`{"video_path":"x.mp4"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result uploadResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		assertUploadError(t, resp.StatusCode, result, http.StatusBadRequest, "Erro ao receber arquivo: ")
	})

	if usingReferenceApp() {
		if left := dirEntries(t, "uploads"); len(left) != 0 {
			t.Errorf("nothing should be stored, found %v", left)
		}
	}
}

func TestUploadRejectsUnsupportedExtensions(t *testing.T) {
	notImplemented(t)
	resetWorkspace(t)
	video := makeVideo(t, "mp4", "mpeg4", 1)

	for _, name := range []string{"clip.txt", "clip.gif", "clip.mp3", "clip", "clip.mp4.exe", "mp4", ".mp4x"} {
		t.Run(name, func(t *testing.T) {
			status, result := upload(t, "video", name, video)
			assertUploadError(t, status, result, http.StatusBadRequest,
				"Formato de arquivo não suportado. Use: mp4, avi, mov, mkv")
		})
	}

	if usingReferenceApp() {
		if left := dirEntries(t, "uploads"); len(left) != 0 {
			t.Errorf("rejected files must not be stored, found %v", left)
		}
	}
}

func TestUploadOfCorruptVideoReportsFFmpegErrorAndKeepsFile(t *testing.T) {
	notImplemented(t)
	resetWorkspace(t)
	content := []byte("this is definitely not a video stream")

	status, result := upload(t, "video", "broken.mp4", content)

	assertUploadError(t, status, result, http.StatusOK, "Erro no ffmpeg: ")
	if !strings.Contains(result.Message, "Output:") {
		t.Errorf("expected ffmpeg output in message, got %q", result.Message)
	}

	if !usingReferenceApp() {
		return
	}

	// On failure the original upload is kept.
	stored := dirEntries(t, "uploads")
	if len(stored) != 1 || !strings.HasSuffix(stored[0], "_broken.mp4") {
		t.Fatalf("expected the failed upload to be kept, found %v", stored)
	}
	kept, err := os.ReadFile(filepath.Join(referenceDir, "uploads", stored[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept, content) {
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
	notImplemented(t)
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

func TestUploadOfVideoWithoutFramesReportsNoFrames(t *testing.T) {
	notImplemented(t)
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
	if !usingReferenceApp() {
		return
	}
	if out := dirEntries(t, "outputs"); len(out) != 0 {
		t.Errorf("no zip should be produced, found %v", out)
	}
	if left := dirEntries(t, "temp"); len(left) != 0 {
		t.Errorf("temp dir should be cleaned up, found %v", left)
	}
}

// ---------------------------------------------------------------------------
// Reference implementation only: failures induced through its filesystem
// ---------------------------------------------------------------------------

func TestUploadFailsWhenUploadsDirIsNotWritable(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)
	resetWorkspace(t)
	replaceDirWithFile(t, "uploads")

	status, result := upload(t, "video", "clip.mp4", makeVideo(t, "mp4", "mpeg4", 1))

	assertUploadError(t, status, result, http.StatusInternalServerError, "Erro ao salvar arquivo: ")
}

func TestUploadFailsWhenDiskIsFullWhileSaving(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available on this platform")
	}
	resetWorkspace(t)
	video := makeVideo(t, "mp4", "mpeg4", 1)

	// Point the exact destination path at /dev/full: the kernel accepts the
	// open but every write fails with ENOSPC.
	for _, ts := range upcomingTimestamps(5) {
		if err := os.Symlink("/dev/full", filepath.Join(referenceDir, "uploads", ts+"_full.mp4")); err != nil {
			t.Fatal(err)
		}
	}

	status, result := upload(t, "video", "full.mp4", video)

	assertUploadError(t, status, result, http.StatusInternalServerError, "Erro ao salvar arquivo: ")
	if !strings.Contains(result.Message, "no space left on device") {
		t.Errorf("expected ENOSPC in message, got %q", result.Message)
	}
}

// Files that match *.png inside the processing directory but cannot be
// archived make ZIP creation fail.
func TestUploadFailsWhenAFrameCannotBeArchived(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)
	cases := map[string]func(t *testing.T, path string){
		"unreadable frame (dangling symlink)": func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(referenceDir, "nowhere"), path); err != nil {
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
				dir := filepath.Join(referenceDir, "temp", ts)
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
	notImplemented(t)
	requireReferenceApp(t)
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

func assertUploadError(t *testing.T, gotStatus int, result uploadResult, wantStatus int, msgPrefix string) {
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
