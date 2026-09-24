package integration

// Integration tests for GET /

import (
	"net/http"
	"strings"
	"testing"
)

func TestIndexServesUploadPage(t *testing.T) {
	resp, body := get(t, "/")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
	html := string(body)
	for _, want := range []string{
		"<!DOCTYPE html>",
		"<title>FIAP X - Processador de Vídeos</title>",
		`id="uploadForm"`,
		`id="videoFile"`,
		"formData.append('video', file)",
		`accept="video/*"`,
		"fetch('/upload'",
		"fetch('/api/status')",
		"/download/",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index page missing %q", want)
		}
	}
}
