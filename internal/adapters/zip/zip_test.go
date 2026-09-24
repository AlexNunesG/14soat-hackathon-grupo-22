package zip_test

import (
	stdzip "archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"video-processor/internal/adapters/zip"
	"video-processor/internal/domain"
)

func writeFiles(t *testing.T, dir string, contents map[string]string) {
	t.Helper()
	for name, body := range contents {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := map[string]string{
		domain.FrameName(1): "first frame",
		domain.FrameName(2): "second frame",
		domain.FrameName(3): string(bytes.Repeat([]byte{0, 1, 2, 255}, 4096)),
	}
	writeFiles(t, dir, want)
	paths := []string{
		filepath.Join(dir, domain.FrameName(1)),
		filepath.Join(dir, domain.FrameName(2)),
		filepath.Join(dir, domain.FrameName(3)),
	}

	var buf bytes.Buffer
	if err := zip.New().Archive(context.Background(), &buf, paths); err != nil {
		t.Fatal(err)
	}

	zr, err := stdzip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(paths) {
		t.Fatalf("zip has %d entries, want %d", len(zr.File), len(paths))
	}
	for i, f := range zr.File {
		if wantName := domain.FrameName(i + 1); f.Name != wantName {
			t.Errorf("entry %d = %q, want %q (at the root, in order)", i, f.Name, wantName)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want[f.Name] {
			t.Errorf("%s: content differs", f.Name)
		}
	}
}

func TestArchiveEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := zip.New().Archive(context.Background(), &buf, nil); err != nil {
		t.Fatal(err)
	}
	zr, err := stdzip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 0 {
		t.Errorf("got %d entries", len(zr.File))
	}
}

func TestArchiveErrors(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeFiles(t, a, map[string]string{"frame_0001.png": "a"})
	writeFiles(t, b, map[string]string{"frame_0001.png": "b"})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name  string
		ctx   context.Context
		paths []string
		want  error
	}{
		{"missing file", context.Background(), []string{filepath.Join(a, "nope.png")}, os.ErrNotExist},
		{"directory", context.Background(), []string{a}, nil},
		{"duplicate base name", context.Background(), []string{filepath.Join(a, "frame_0001.png"), filepath.Join(b, "frame_0001.png")}, nil},
		{"canceled", canceled, []string{filepath.Join(a, "frame_0001.png")}, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := zip.New().Archive(tt.ctx, io.Discard, tt.paths)
			if err == nil {
				t.Fatal("want error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestArchiveWriteError(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"frame_0001.png": "x"})
	if err := zip.New().Archive(context.Background(), failingWriter{}, []string{filepath.Join(dir, "frame_0001.png")}); err == nil {
		t.Fatal("want error")
	}
}
