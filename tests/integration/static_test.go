package integration

// Integration tests for the static routes GET /uploads/* and GET /outputs/*

import (
	"bytes"
	"net/http"
	"testing"
)

func TestStaticRoutesReturn404ForMissingFiles(t *testing.T) {
	notImplemented(t)
	resetWorkspace(t)
	for _, path := range []string{"/outputs/nope.zip", "/uploads/nope.mp4"} {
		resp, _ := get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: expected 404, got %d", path, resp.StatusCode)
		}
	}
}

func TestStaticOutputsServesGeneratedZip(t *testing.T) {
	notImplemented(t)
	resetWorkspace(t)
	_, result := upload(t, "video", "clip.mp4", makeVideo(t, "mp4", "mpeg4", 2))
	if !result.Success {
		t.Fatalf("upload failed: %+v", result)
	}

	resp, body := get(t, "/outputs/"+result.ZipPath)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /outputs/%s: expected 200, got %d", result.ZipPath, resp.StatusCode)
	}
	if !bytes.Equal(body, download(t, result.ZipPath)) {
		t.Errorf("/outputs and /download serve different content")
	}
	assertValidZip(t, body, result.Images)
}

// The name of a kept upload is never returned by the API, so finding it
// requires the reference app's uploads/ directory.
func TestStaticUploadsServesKeptUpload(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)
	resetWorkspace(t)
	content := []byte("this is definitely not a video stream")

	// A failed processing keeps the original upload on disk.
	if _, result := upload(t, "video", "broken.mp4", content); result.Success {
		t.Fatalf("expected processing to fail, got %+v", result)
	}
	stored := dirEntries(t, "uploads")
	if len(stored) != 1 {
		t.Fatalf("expected one kept upload, found %v", stored)
	}

	resp, body := get(t, "/uploads/"+stored[0])

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /uploads/%s: expected 200, got %d", stored[0], resp.StatusCode)
	}
	if !bytes.Equal(body, content) {
		t.Errorf("served upload differs from what was sent")
	}
}
