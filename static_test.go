package main

// Integration tests for the static routes GET /uploads/* and GET /outputs/*

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticRoutesReturn404ForMissingFiles(t *testing.T) {
	resetWorkspace(t)
	for _, path := range []string{"/outputs/nope.zip", "/uploads/nope.mp4"} {
		resp, _ := get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: expected 404, got %d", path, resp.StatusCode)
		}
	}
}

func TestStaticOutputsServesGeneratedZip(t *testing.T) {
	resetWorkspace(t)
	_, result := upload(t, "video", "clip.mp4", makeVideo(t, "mp4", "mpeg4", 2))
	if !result.Success {
		t.Fatalf("upload failed: %+v", result)
	}

	resp, body := get(t, "/outputs/"+result.ZipPath)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /outputs/%s: expected 200, got %d", result.ZipPath, resp.StatusCode)
	}
	onDisk, err := os.ReadFile(filepath.Join(workDir, "outputs", result.ZipPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, onDisk) {
		t.Errorf("static zip differs from file on disk")
	}
	assertValidZip(t, body, result.Images)
}

func TestStaticUploadsServesKeptUpload(t *testing.T) {
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
