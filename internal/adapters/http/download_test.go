package httpapi_test

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"testing"

	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// trackingBody is an object body that records whether it was closed.
type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

func TestDownload(t *testing.T) {
	tests := []struct {
		original, wantFile, wantHeader string
	}{
		{"holiday.mp4", "holiday_frames.zip", `attachment; filename="holiday_frames.zip"`},
		{"my.summer.trip.webm", "my.summer.trip_frames.zip", `attachment; filename="my.summer.trip_frames.zip"`},
		{"UPPER CASE.MP4", "UPPER CASE_frames.zip", `attachment; filename="UPPER CASE_frames.zip"`},
		{`quote"back\slash.mp4`, `quote"back\slash_frames.zip`,
			`attachment; filename="quote_back_slash_frames.zip"; filename*=UTF-8''quote%22back%5Cslash_frames.zip`},
		{"vídeo com acentuação.Mkv", "vídeo com acentuação_frames.zip",
			`attachment; filename="v_deo com acentua__o_frames.zip"; filename*=UTF-8''v%C3%ADdeo%20com%20acentua%C3%A7%C3%A3o_frames.zip`},
	}
	for _, tt := range tests {
		t.Run(tt.original, func(t *testing.T) {
			body := &trackingBody{Reader: strings.NewReader("zip bytes")}
			videos := &fakeVideoService{
				video:  &domain.Video{ID: videoID, OriginalName: tt.original, Status: domain.StatusDone},
				object: &app.Object{ReadCloser: body, Size: 9},
			}
			rec := serve(newAPI(nil, videos), http.MethodGet, "/api/v1/videos/"+videoID+"/download", aliceToken, "")

			if rec.Code != http.StatusOK || rec.Body.String() != "zip bytes" {
				t.Fatalf("status %d body %q", rec.Code, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
				t.Errorf("Content-Type %q", ct)
			}
			if cl := rec.Header().Get("Content-Length"); cl != "9" {
				t.Errorf("Content-Length %q", cl)
			}
			cd := rec.Header().Get("Content-Disposition")
			if cd != tt.wantHeader {
				t.Errorf("Content-Disposition\n got %s\nwant %s", cd, tt.wantHeader)
			}
			// What the integration tests (and browsers) do with it.
			disposition, params, err := mime.ParseMediaType(cd)
			if err != nil || disposition != "attachment" || params["filename"] != tt.wantFile {
				t.Errorf("parsed %q %v %v, want filename %q", disposition, params, err, tt.wantFile)
			}
			if !body.closed {
				t.Error("the object was not closed")
			}
			if videos.gotOwner != aliceID || videos.gotID != videoID {
				t.Errorf("service got owner %q id %q", videos.gotOwner, videos.gotID)
			}
		})
	}
}

func TestDownloadErrors(t *testing.T) {
	tests := []struct {
		err     error
		status  int
		code    httpapi.ErrorCode
		message string
	}{
		{fmt.Errorf("x: %w", app.ErrNotFound), http.StatusNotFound, httpapi.CodeNotFound, "video not found"},
		{&app.NotReadyError{Status: domain.StatusProcessing}, http.StatusConflict, httpapi.CodeVideoNotReady,
			"video is not ready for download (status: PROCESSING)"},
		{fmt.Errorf("wrapped: %w", &app.NotReadyError{Status: domain.StatusFailed}), http.StatusConflict,
			httpapi.CodeVideoNotReady, "video is not ready for download (status: FAILED)"},
		{errors.New("storage down"), http.StatusInternalServerError, httpapi.CodeInternal, "internal server error"},
	}
	for _, tt := range tests {
		rec := serve(newAPI(nil, &fakeVideoService{err: tt.err}), http.MethodGet,
			"/api/v1/videos/"+videoID+"/download", aliceToken, "")
		if e := assertError(t, rec, tt.status, tt.code); e.Error.Message != tt.message {
			t.Errorf("message %q, want %q", e.Error.Message, tt.message)
		}
	}
}
