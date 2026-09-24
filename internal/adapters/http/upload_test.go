package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// fakeUploads is an UploadService that records the files it got (their
// content read at call time) and returns one PENDING video per file.
type fakeUploads struct {
	err      error
	calls    int
	gotOwner string
	names    []string
	contents []string
	sizes    []int64
}

func (f *fakeUploads) Upload(_ context.Context, ownerID string, files []app.UploadFile) ([]*domain.Video, error) {
	f.calls++
	f.gotOwner = ownerID
	for _, file := range files {
		data, err := io.ReadAll(file.Content)
		if err != nil {
			return nil, err
		}
		f.names, f.contents, f.sizes = append(f.names, file.Name), append(f.contents, string(data)), append(f.sizes, file.Size)
	}
	if f.err != nil {
		return nil, f.err
	}
	created := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	videos := make([]*domain.Video, len(files))
	for i, file := range files {
		videos[i] = &domain.Video{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), OwnerID: ownerID, OriginalName: file.Name,
			Status: domain.StatusPending, CreatedAt: created, UpdatedAt: created,
		}
	}
	return videos, nil
}

type part struct {
	field, name, content string
}

func multipartBody(t *testing.T, parts ...part) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for _, p := range parts {
		var (
			pw  io.Writer
			err error
		)
		if p.name == "" {
			pw, err = w.CreateFormField(p.field)
		} else {
			pw, err = w.CreateFormFile(p.field, p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(pw, p.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body, w.FormDataContentType()
}

func uploadAPI(uploads *fakeUploads, maxBytes int64, tempDir string) http.Handler {
	return httpapi.NewRouter(httpapi.Options{
		Tokens: fakeTokens{}, Videos: &fakeVideoService{}, Uploads: uploads,
		MaxUploadBytes: maxBytes, UploadTempDir: tempDir,
	})
}

func postUpload(h http.Handler, body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos", body)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertNoTempFiles checks that the handler removed its spool files.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestUploadAccepted(t *testing.T) {
	uploads, dir := &fakeUploads{}, t.TempDir()
	body, ct := multipartBody(t,
		part{"videos", "holiday.mp4", "first video"},
		part{"description", "", "ignored form value"},
		part{"file", "other.mp4", "a file in another field is ignored"},
		part{"videos", "Trip.MKV", "second"},
		part{"videos", "vídeo com espaços.webm", ""},
	)
	rec := postUpload(uploadAPI(uploads, 0, dir), body, ct)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[struct {
		Videos []map[string]any `json:"videos"`
	}](t, rec)
	wantNames := []string{"holiday.mp4", "Trip.MKV", "vídeo com espaços.webm"}
	if len(got.Videos) != 3 {
		t.Fatalf("videos %v", got.Videos)
	}
	for i, v := range got.Videos {
		if v["original_name"] != wantNames[i] || v["status"] != "PENDING" || v["frame_count"] != nil ||
			v["error_message"] != nil || v["created_at"] != "2026-09-24T12:00:00Z" {
			t.Errorf("videos[%d] = %v", i, v)
		}
		if _, ok := v["download_url"]; ok {
			t.Errorf("videos[%d] has a download_url while PENDING", i)
		}
	}
	if uploads.gotOwner != aliceID || strings.Join(uploads.names, "|") != strings.Join(wantNames, "|") {
		t.Errorf("service got owner %q names %v", uploads.gotOwner, uploads.names)
	}
	if uploads.contents[0] != "first video" || uploads.sizes[0] != int64(len("first video")) ||
		uploads.contents[2] != "" || uploads.sizes[2] != 0 {
		t.Errorf("contents %q sizes %v", uploads.contents, uploads.sizes)
	}
	assertNoTempFiles(t, dir)
}

func TestUploadMissingFile(t *testing.T) {
	cases := map[string]struct{ contentType, body string }{
		"multipart without parts": {"multipart/form-data; boundary=XyZ", "--XyZ--\r\n"},
		"file in another field": {"multipart/form-data; boundary=XyZ",
			"--XyZ\r\nContent-Disposition: form-data; name=\"file\"; filename=\"clip.mp4\"\r\n\r\ndata\r\n--XyZ--\r\n"},
		"videos field without a file": {"multipart/form-data; boundary=XyZ",
			"--XyZ\r\nContent-Disposition: form-data; name=\"videos\"\r\n\r\n\r\n--XyZ--\r\n"},
		"non-multipart body":    {"application/json", `{"videos": ["clip.mp4"]}`},
		"no content type":       {"", "clip.mp4"},
		"multipart no boundary": {"multipart/form-data", "--XyZ--\r\n"},
		"not really multipart":  {"multipart/form-data; boundary=XyZ", "garbage"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			uploads, dir := &fakeUploads{}, t.TempDir()
			rec := postUpload(uploadAPI(uploads, 0, dir), strings.NewReader(tc.body), tc.contentType)
			assertError(t, rec, http.StatusBadRequest, httpapi.CodeMissingFile)
			if uploads.calls != 0 {
				t.Error("the service was called")
			}
			assertNoTempFiles(t, dir)
		})
	}
}

func TestUploadUnsupportedFormatStopsEarly(t *testing.T) {
	uploads, dir := &fakeUploads{}, t.TempDir()
	body, ct := multipartBody(t,
		part{"videos", "good.mp4", "ok"},
		part{"videos", "notes.txt", "bad"},
		part{"videos", "later.mp4", "never read"},
	)
	rec := postUpload(uploadAPI(uploads, 0, dir), body, ct)
	e := assertError(t, rec, http.StatusBadRequest, httpapi.CodeUnsupportedFormat)
	for _, f := range domain.SupportedFormats() {
		if !strings.Contains(e.Error.Message, f) {
			t.Errorf("message %q does not list %s", e.Error.Message, f)
		}
	}
	if uploads.calls != 0 {
		t.Error("the service was called")
	}
	assertNoTempFiles(t, dir)
}

func TestUploadTooLarge(t *testing.T) {
	const limit = 1024
	big := strings.Repeat("x", 2*limit)

	t.Run("declared length", func(t *testing.T) {
		uploads, dir := &fakeUploads{}, t.TempDir()
		body, ct := multipartBody(t, part{"videos", "big.mp4", big})
		rec := postUpload(uploadAPI(uploads, limit, dir), body, ct) // httptest sets ContentLength
		e := assertError(t, rec, http.StatusRequestEntityTooLarge, httpapi.CodePayloadTooLarge)
		if !strings.Contains(e.Error.Message, "1024") {
			t.Errorf("message %q does not name the limit", e.Error.Message)
		}
		if uploads.calls != 0 {
			t.Error("the service was called")
		}
	})
	t.Run("streamed body", func(t *testing.T) {
		uploads, dir := &fakeUploads{}, t.TempDir()
		body, ct := multipartBody(t, part{"videos", "small.mp4", "ok"}, part{"videos", "big.mp4", big})
		// A reader without a known length: the limit is enforced while
		// reading.
		rec := postUpload(uploadAPI(uploads, limit, dir), io.MultiReader(body), ct)
		assertError(t, rec, http.StatusRequestEntityTooLarge, httpapi.CodePayloadTooLarge)
		if uploads.calls != 0 {
			t.Error("the service was called")
		}
		assertNoTempFiles(t, dir)
	})
	t.Run("under the limit", func(t *testing.T) {
		body, ct := multipartBody(t, part{"videos", "small.mp4", "ok"})
		if rec := postUpload(uploadAPI(&fakeUploads{}, limit, t.TempDir()), body, ct); rec.Code != http.StatusAccepted {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
}

func TestUploadTruncatedBody(t *testing.T) {
	uploads, dir := &fakeUploads{}, t.TempDir()
	body := "--XyZ\r\nContent-Disposition: form-data; name=\"videos\"; filename=\"a.mp4\"\r\n\r\npartial data"
	rec := postUpload(uploadAPI(uploads, 0, dir), strings.NewReader(body), "multipart/form-data; boundary=XyZ")
	assertError(t, rec, http.StatusBadRequest, httpapi.CodeInvalidRequest)
	if uploads.calls != 0 {
		t.Error("the service was called")
	}
	assertNoTempFiles(t, dir)
}

func TestUploadServiceErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   httpapi.ErrorCode
	}{
		{app.ErrMissingFile, http.StatusBadRequest, httpapi.CodeMissingFile},
		{domain.ValidateFormat("x.txt"), http.StatusBadRequest, httpapi.CodeUnsupportedFormat},
		{errors.New("storage down"), http.StatusInternalServerError, httpapi.CodeInternal},
	}
	for _, tt := range tests {
		dir := t.TempDir()
		body, ct := multipartBody(t, part{"videos", "a.mp4", "data"})
		rec := postUpload(uploadAPI(&fakeUploads{err: tt.err}, 0, dir), body, ct)
		e := assertError(t, rec, tt.status, tt.code)
		if tt.status == http.StatusInternalServerError && strings.Contains(e.Error.Message, "storage") {
			t.Errorf("internal details leaked: %q", e.Error.Message)
		}
		assertNoTempFiles(t, dir)
	}
}

func TestUploadUnwritableTempDirIsInternal(t *testing.T) {
	body, ct := multipartBody(t, part{"videos", "a.mp4", "data"})
	rec := postUpload(uploadAPI(&fakeUploads{}, 0, "/nonexistent/dir"), body, ct)
	assertError(t, rec, http.StatusInternalServerError, httpapi.CodeInternal)
}
