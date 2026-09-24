package ffmpeg_test

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-processor/internal/adapters/ffmpeg"
	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// generate runs ffmpeg with args to create name in a temp dir and returns
// its path.
func generate(t *testing.T, name string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatalf("these tests need ffmpeg in PATH: %v", err)
	}
	out := filepath.Join(t.TempDir(), name)
	args = append(append([]string{"-loglevel", "error", "-nostdin"}, args...), "-y", out)
	if output, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("generating %s: %v\n%s", name, err, output)
	}
	return out
}

func testVideo(t *testing.T, ext, codec string, seconds int) string {
	t.Helper()
	return generate(t, "in."+ext,
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=64x48:rate=25:duration=%d", seconds),
		"-c:v", codec, "-pix_fmt", "yuv420p")
}

func TestExtractFramesOneFramePerSecond(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ext, codec string
		seconds    int
	}{
		{"mp4", "mpeg4", 1},
		{"mp4", "mpeg4", 3},
		{"mp4", "libx264", 5},
		{"mkv", "mpeg4", 2},
		{"webm", "libvpx", 2},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s_%ds", tt.ext, tt.codec, tt.seconds), func(t *testing.T) {
			t.Parallel()
			in := testVideo(t, tt.ext, tt.codec, tt.seconds)
			out := t.TempDir()
			frames, err := ffmpeg.New().ExtractFrames(context.Background(), in, out)
			if err != nil {
				t.Fatal(err)
			}
			if len(frames) != tt.seconds {
				t.Fatalf("got %d frames, want %d", len(frames), tt.seconds)
			}
			for i, p := range frames {
				if want := filepath.Join(out, domain.FrameName(i+1)); p != want {
					t.Errorf("frame %d = %s, want %s", i, p, want)
				}
				assertPNG(t, p, 64, 48)
			}
			entries, err := os.ReadDir(out)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != tt.seconds {
				t.Errorf("output dir has %d entries, want %d", len(entries), tt.seconds)
			}
		})
	}
}

func assertPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("%s is not a PNG: %v", path, err)
	}
	if cfg.Width != w || cfg.Height != h {
		t.Errorf("%s is %dx%d, want %dx%d", path, cfg.Width, cfg.Height, w, h)
	}
}

func TestExtractFramesUnprocessableInput(t *testing.T) {
	t.Parallel()
	corrupt := filepath.Join(t.TempDir(), "corrupt.mp4")
	if err := os.WriteFile(corrupt, []byte(strings.Repeat("this is not a video file\n", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		input   string
		wantMsg string // exact message, or prefix when wantPfx
		wantPfx bool
	}{
		{
			name:    "corrupt",
			input:   corrupt,
			wantMsg: "ffmpeg: ",
			wantPfx: true,
		},
		{
			name:    "audio only",
			input:   generate(t, "audio.mp4", "-f", "lavfi", "-i", "anullsrc", "-t", "1", "-c:a", "aac"),
			wantMsg: "no video stream found in input",
		},
		{
			name: "zero frames",
			input: generate(t, "empty.avi", "-f", "lavfi", "-i", "testsrc=size=64x48",
				"-frames:v", "0", "-c:v", "rawvideo", "-pix_fmt", "bgr24"),
			wantMsg: "no frames could be extracted from the video",
		},
		{
			name:    "missing file",
			input:   filepath.Join(t.TempDir(), "nope.mp4"),
			wantMsg: "ffmpeg: ",
			wantPfx: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			frames, err := ffmpeg.New().ExtractFrames(context.Background(), tt.input, t.TempDir())
			if err == nil {
				t.Fatalf("want error, got %d frames", len(frames))
			}
			if !errors.Is(err, app.ErrUnprocessableVideo) {
				t.Errorf("err = %v, want it to wrap ErrUnprocessableVideo", err)
			}
			var ferr *ffmpeg.Error
			if !errors.As(err, &ferr) {
				t.Fatalf("err = %T, want *ffmpeg.Error", err)
			}
			msg := err.Error()
			if tt.wantPfx {
				if !strings.HasPrefix(msg, tt.wantMsg) || len(msg) <= len(tt.wantMsg) {
					t.Errorf("message %q, want prefix %q and a reason", msg, tt.wantMsg)
				}
				if strings.TrimSpace(ferr.Stderr) == "" {
					t.Error("stderr was not captured")
				}
			} else if msg != tt.wantMsg {
				t.Errorf("message %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}

func TestExtractFramesTimeout(t *testing.T) {
	t.Parallel()
	in := generate(t, "slow.mp4",
		"-f", "lavfi", "-i", "color=c=blue:size=1280x720:rate=25:duration=60",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
	start := time.Now()
	_, err := ffmpeg.New(ffmpeg.WithTimeout(50*time.Millisecond)).ExtractFrames(context.Background(), in, t.TempDir())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if errors.Is(err, app.ErrUnprocessableVideo) {
		t.Error("a timeout must not be reported as an unprocessable video")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s to stop ffmpeg", elapsed)
	}
}

func TestExtractFramesCanceledContext(t *testing.T) {
	t.Parallel()
	in := testVideo(t, "mp4", "mpeg4", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ffmpeg.New().ExtractFrames(ctx, in, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
}

func TestExtractFramesSetupErrors(t *testing.T) {
	t.Parallel()
	in := filepath.Join(t.TempDir(), "in.mp4")
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		ex   *ffmpeg.Extractor
		out  string
	}{
		{"missing binary", ffmpeg.New(ffmpeg.WithBinary(filepath.Join(t.TempDir(), "no-ffmpeg"))), t.TempDir()},
		{"missing output dir", ffmpeg.New(), filepath.Join(t.TempDir(), "missing")},
		{"output is a file", ffmpeg.New(), notDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.ex.ExtractFrames(context.Background(), in, tt.out)
			if err == nil {
				t.Fatal("want error")
			}
			if errors.Is(err, app.ErrUnprocessableVideo) {
				t.Errorf("err = %v: a setup problem is not the video's fault", err)
			}
		})
	}
}
