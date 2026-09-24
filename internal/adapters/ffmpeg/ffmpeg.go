// Package ffmpeg implements app.FrameExtractor with the ffmpeg command line
// tool: it extracts 1 frame per second of video as PNG images.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// Defaults for Extractor.
const (
	DefaultBinary  = "ffmpeg"
	DefaultTimeout = 10 * time.Minute

	// maxStderr bounds how much of ffmpeg's stderr is kept for the error.
	maxStderr = 16 << 10
)

// Error messages for inputs that are not processable videos. They end up in
// the video's error_message and in the failure e-mail.
const (
	msgNoVideoStream = "no video stream found in input"
	msgNoFrames      = "no frames could be extracted from the video"
)

// Error is a failed ffmpeg run. Its message is ffmpeg's last stderr line
// (or a plainer description for well-known cases); Stderr holds the tail of
// the full output for logs.
type Error struct {
	Msg    string
	Stderr string
	Err    error // the exec error, or app.ErrUnprocessableVideo
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Err }

// Extractor runs ffmpeg to extract frames. The zero value is not usable;
// build one with New.
type Extractor struct {
	binary  string
	timeout time.Duration
}

// Option configures an Extractor.
type Option func(*Extractor)

// WithBinary sets the ffmpeg executable (name in PATH or path).
func WithBinary(path string) Option { return func(e *Extractor) { e.binary = path } }

// WithTimeout bounds each extraction; non-positive values keep the default.
func WithTimeout(d time.Duration) Option {
	return func(e *Extractor) {
		if d > 0 {
			e.timeout = d
		}
	}
}

// New returns an Extractor using DefaultBinary and DefaultTimeout unless
// overridden.
func New(opts ...Option) *Extractor {
	e := &Extractor{binary: DefaultBinary, timeout: DefaultTimeout}
	for _, o := range opts {
		o(e)
	}
	return e
}

var _ app.FrameExtractor = (*Extractor)(nil)

// ExtractFrames runs
//
//	ffmpeg -i <input> -map 0:v:0 -vf fps=1 <outDir>/frame_%04d.png
//
// and returns the paths of the frames it wrote, in order. Errors caused by
// the input wrap app.ErrUnprocessableVideo; a timeout wraps
// context.DeadlineExceeded.
func (e *Extractor) ExtractFrames(ctx context.Context, inputPath, outDir string) ([]string, error) {
	if info, err := os.Stat(outDir); err != nil {
		return nil, fmt.Errorf("ffmpeg: output directory: %w", err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("ffmpeg: output %s is not a directory", outDir)
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// #nosec G204 -- fixed arguments; the paths are passed as separate
	// arguments, never through a shell.
	cmd := exec.CommandContext(ctx, e.binary,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", inputPath,
		"-map", "0:v:0", // first video stream only; fails clearly without one
		"-vf", "fps=1",
		filepath.Join(outDir, domain.FramePattern),
	)
	stderr := &tailBuffer{max: maxStderr}
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("ffmpeg: stopped after %s: %w", e.timeout, ctxErr)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// ffmpeg could not be started at all: not the input's fault.
			return nil, fmt.Errorf("ffmpeg: %w", err)
		}
		return nil, newRunError(stderr.String(), err)
	}

	frames := framesIn(outDir)
	if len(frames) == 0 {
		return nil, &Error{Msg: msgNoFrames, Stderr: stderr.String(), Err: app.ErrUnprocessableVideo}
	}
	return frames, nil
}

// newRunError describes a non-zero ffmpeg exit.
func newRunError(stderr string, runErr error) *Error {
	err := fmt.Errorf("%w: %w", app.ErrUnprocessableVideo, runErr)
	if strings.Contains(stderr, "matches no streams") {
		return &Error{Msg: msgNoVideoStream, Stderr: stderr, Err: err}
	}
	msg := lastLine(stderr)
	if msg == "" {
		msg = runErr.Error()
	}
	return &Error{Msg: "ffmpeg: " + msg, Stderr: stderr, Err: err}
}

// framesIn returns frame_0001.png, frame_0002.png, ... in dir, stopping at
// the first missing one.
func framesIn(dir string) []string {
	var frames []string
	for n := 1; ; n++ {
		p := filepath.Join(dir, domain.FrameName(n))
		if _, err := os.Stat(p); err != nil {
			return frames
		}
		frames = append(frames, p)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	buf bytes.Buffer
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return n, nil
}

func (t *tailBuffer) String() string { return t.buf.String() }
