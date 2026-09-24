package main

// Integration tests for GET /api/status

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
