package integration

// Integration tests for GET /download/:filename

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadReturnsGeneratedZip(t *testing.T) {
	notImplemented(t)
	resetWorkspace(t)
	_, result := upload(t, "video", "clip.mp4", makeVideo(t, "mp4", "mpeg4", 2))
	if !result.Success {
		t.Fatalf("upload failed: %+v", result)
	}

	resp, body := get(t, "/download/"+result.ZipPath)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	headers := map[string]string{
		"Content-Type":              "application/zip",
		"Content-Disposition":       "attachment; filename=" + result.ZipPath,
		"Content-Description":       "File Transfer",
		"Content-Transfer-Encoding": "binary",
	}
	for h, v := range headers {
		if got := resp.Header.Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
	assertValidZip(t, body, result.Images)

	if usingReferenceApp() {
		onDisk, err := os.ReadFile(filepath.Join(referenceDir, "outputs", result.ZipPath))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, onDisk) {
			t.Errorf("downloaded zip differs from file on disk")
		}
	}
}

func TestDownloadUnknownFileReturns404(t *testing.T) {
	notImplemented(t)
	resetWorkspace(t)

	for _, name := range []string{"missing.zip", "frames_20000101_000000.zip", "outputs.zip"} {
		t.Run(name, func(t *testing.T) {
			resp, body := get(t, "/download/"+name)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", resp.StatusCode, body)
			}
			var payload map[string]string
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if payload["error"] != "Arquivo não encontrado" {
				t.Errorf("error = %q", payload["error"])
			}
		})
	}
}
