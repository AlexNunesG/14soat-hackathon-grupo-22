package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRecovererAnswers500WithEnvelope(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	r := gin.New()
	r.Use(requestLogger(log), recoverer(log))
	r.GET("/boom", func(*gin.Context) { panic("kaboom") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != CodeInternal || body.Error.Message != "internal server error" {
		t.Errorf("body %+v", body)
	}
	if strings.Contains(rec.Body.String(), "kaboom") {
		t.Error("panic value leaked into the response")
	}
	if !strings.Contains(logs.String(), "kaboom") || !strings.Contains(logs.String(), `"status":500`) {
		t.Errorf("panic or request not logged: %s", logs.String())
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	WriteError(c, http.StatusConflict, CodeVideoNotReady, "video is not ready for download (status: PROCESSING)")
	if rec.Code != http.StatusConflict || !c.IsAborted() {
		t.Fatalf("status %d, aborted %v", rec.Code, c.IsAborted())
	}
	const want = `{"error":{"code":"video_not_ready","message":"video is not ready for download (status: PROCESSING)"}}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body %s, want %s", got, want)
	}
}
