package integration

// Integration tests for GET /api/status
//
// Against an external BASE_URL the server may already hold files, so the
// black-box test only checks that a new ZIP shows up. Tests that need a known
// starting state or crafted files in outputs/ require the reference app.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusListsProcessedZip(t *testing.T) {
	notImplemented(t)
	resetWorkspace(t)
	_, result := upload(t, "video", "clip.mp4", makeVideo(t, "mp4", "mpeg4", 2))
	if !result.Success {
		t.Fatalf("upload failed: %+v", result)
	}

	resp, _ := get(t, "/api/status")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("unexpected content type %q", ct)
	}
	status := getStatus(t)

	if status.Total != len(status.Files) {
		t.Errorf("total = %d but %d files listed", status.Total, len(status.Files))
	}
	var entry *statusFile
	for i := range status.Files {
		if status.Files[i].Filename == result.ZipPath {
			entry = &status.Files[i]
		}
	}
	if entry == nil {
		t.Fatalf("status does not list %s: %+v", result.ZipPath, status)
	}
	if entry.DownloadURL != "/download/"+result.ZipPath {
		t.Errorf("download_url = %q", entry.DownloadURL)
	}
	if size := int64(len(download(t, result.ZipPath))); entry.Size != size {
		t.Errorf("size = %d, downloaded %d bytes", entry.Size, size)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", entry.CreatedAt); err != nil {
		t.Errorf("created_at %q has unexpected format: %v", entry.CreatedAt, err)
	}
}

func TestStatusWithNoProcessedFiles(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)
	resetWorkspace(t)

	status := getStatus(t)

	if status.Total != 0 || len(status.Files) != 0 {
		t.Errorf("expected empty status, got %+v", status)
	}
}

func TestStatusListsOnlyReadableZipFiles(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)
	resetWorkspace(t)
	outputs := filepath.Join(referenceDir, "outputs")

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
}
