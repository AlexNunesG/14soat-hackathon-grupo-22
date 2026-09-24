// Package zip implements app.Archiver with archive/zip.
package zip

import (
	stdzip "archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"video-processor/internal/app"
)

// Archiver writes zip archives with every file at the root. Entries are
// stored without compression: PNG frames are already compressed, so
// deflating them costs CPU for almost no gain.
type Archiver struct{}

// New returns an Archiver.
func New() *Archiver { return &Archiver{} }

var _ app.Archiver = (*Archiver)(nil)

// Archive writes a zip with the files at paths to w, each at the root under
// its base name, in order. Two paths with the same base name are an error.
// It stops with ctx.Err() when ctx is done between files.
func (a *Archiver) Archive(ctx context.Context, w io.Writer, paths []string) (err error) {
	zw := stdzip.NewWriter(w)
	defer func() {
		if cerr := zw.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("zip: finishing archive: %w", cerr)
		}
	}()

	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := filepath.Base(p)
		if seen[name] {
			return fmt.Errorf("zip: duplicate entry %q", name)
		}
		seen[name] = true
		if err := addFile(zw, p, name); err != nil {
			return err
		}
	}
	return nil
}

func addFile(zw *stdzip.Writer, path, name string) error {
	f, err := os.Open(path) // #nosec G304 -- paths come from the caller (frames it produced)
	if err != nil {
		return fmt.Errorf("zip: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("zip: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("zip: %s is not a regular file", path)
	}
	hdr, err := stdzip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("zip: %s: %w", path, err)
	}
	hdr.Name = name
	hdr.Method = stdzip.Store
	entry, err := zw.CreateHeader(hdr)
	if err != nil {
		return fmt.Errorf("zip: %s: %w", name, err)
	}
	if _, err := io.Copy(entry, f); err != nil {
		return fmt.Errorf("zip: %s: %w", name, err)
	}
	return nil
}
