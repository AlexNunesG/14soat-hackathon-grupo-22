package integration

// End-to-end flow across endpoints, exactly as the web page drives it:
// POST /upload -> GET /api/status -> GET /download/:filename

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestEndToEndUploadStatusDownload(t *testing.T) {
	resetWorkspace(t)

	var zips []string
	for i, seconds := range []int{2, 4} {
		if i > 0 {
			// ZIP names have one-second resolution; avoid overwriting the first one.
			time.Sleep(1100 * time.Millisecond)
		}
		status, result := upload(t, "video", fmt.Sprintf("movie%d.mp4", i), makeVideo(t, "mp4", "mpeg4", seconds))
		if status != http.StatusOK || !result.Success {
			t.Fatalf("upload %d failed: %d %+v", i, status, result)
		}
		zips = append(zips, result.ZipPath)
	}

	status := getStatus(t)
	if status.Total != len(status.Files) {
		t.Errorf("total = %d but %d files listed", status.Total, len(status.Files))
	}
	if usingReferenceApp() && status.Total != 2 {
		t.Fatalf("expected 2 processed files, got %+v", status)
	}

	byName := map[string]statusFile{}
	for _, f := range status.Files {
		byName[f.Filename] = f
	}
	for _, name := range zips {
		f, ok := byName[name]
		if !ok {
			t.Fatalf("status does not list %s: %+v", name, status)
		}
		resp, body := get(t, f.DownloadURL)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", f.DownloadURL, resp.StatusCode)
		}
		if int64(len(body)) != f.Size {
			t.Errorf("%s: downloaded %d bytes, status says %d", name, len(body), f.Size)
		}
		if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); err != nil {
			t.Errorf("%s: not a valid zip: %v", name, err)
		}
	}
}
