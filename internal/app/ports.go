// Package app holds the use cases of the video processor and the ports
// (interfaces) they need from the outside world. Adapters under
// internal/adapters implement the ports; cmd/* wires them together.
package app

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrObjectNotFound is returned by ObjectStorage.Get when the key does
	// not exist.
	ErrObjectNotFound = errors.New("object not found")

	// ErrUnprocessableVideo is wrapped by FrameExtractor errors caused by the
	// input itself (undecodable, no video stream, no frames): retrying does
	// not help, the video should end FAILED.
	ErrUnprocessableVideo = errors.New("unprocessable video")
)

// FrameExtractor turns a video file into PNG frames at 1 frame per second.
type FrameExtractor interface {
	// ExtractFrames writes the frames of the video at inputPath into the
	// existing directory outDir, named domain.FrameName(1..n), and returns
	// their paths in order. It fails when the input cannot be decoded, has
	// no video stream, or yields no frame (errors wrapping
	// ErrUnprocessableVideo), and when ctx is done.
	ExtractFrames(ctx context.Context, inputPath, outDir string) ([]string, error)
}

// Archiver packs files into a zip archive.
type Archiver interface {
	// Archive writes a zip holding the given files at its root, under their
	// base names and in the given order, to w.
	Archive(ctx context.Context, w io.Writer, paths []string) error
}

// ObjectStorage stores videos and frame archives as objects in one bucket.
type ObjectStorage interface {
	// Put streams r to key. size is the length of r, or -1 when unknown.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get opens the object at key; the caller closes it. A missing key
	// yields an error wrapping ErrObjectNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// EnsureBucket creates the bucket when it does not exist yet.
	EnsureBucket(ctx context.Context) error
	// Ping checks that the storage is reachable and the bucket exists.
	Ping(ctx context.Context) error
}
