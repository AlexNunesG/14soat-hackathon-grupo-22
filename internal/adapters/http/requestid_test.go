package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/domain"
	"video-processor/internal/platform/logging"
)

// loggedAPI is newAPI with a debug-level JSON logger writing to the
// returned buffer.
func loggedAPI(videos *fakeVideoService) (http.Handler, *bytes.Buffer) {
	var buf bytes.Buffer
	h := httpapi.NewRouter(httpapi.Options{
		Logger: logging.New(&buf, "api", slog.LevelDebug),
		Auth:   &fakeAuth{}, Tokens: fakeTokens{}, Videos: videos, Uploads: &fakeUploads{},
		WebUI: true,
	})
	return h, &buf
}

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, rec)
	}
	return out
}

func accessLog(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var found map[string]any
	for _, rec := range logRecords(t, buf) {
		if rec["msg"] == "http request" {
			if found != nil {
				t.Fatalf("more than one access log line: %s", buf.String())
			}
			found = rec
		}
	}
	if found == nil {
		t.Fatalf("no access log line: %s", buf.String())
	}
	return found
}

func TestRequestIDIsAcceptedAndEchoed(t *testing.T) {
	h, buf := loggedAPI(&fakeVideoService{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(httpapi.HeaderRequestID, "demo-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get(httpapi.HeaderRequestID); got != "demo-123" {
		t.Errorf("X-Request-ID = %q, want demo-123", got)
	}
	if got := accessLog(t, buf)["request_id"]; got != "demo-123" {
		t.Errorf("access log request_id = %v", got)
	}
}

func TestRequestIDIsGeneratedWhenMissingOrInvalid(t *testing.T) {
	for name, header := range map[string]string{
		"missing":   "",
		"too long":  strings.Repeat("a", 129),
		"bad chars": "abc def<script>",
		"newline":   "abc\r\nX-Evil: 1",
	} {
		t.Run(name, func(t *testing.T) {
			h, buf := loggedAPI(&fakeVideoService{})
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			if header != "" {
				req.Header[httpapi.HeaderRequestID] = []string{header}
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get(httpapi.HeaderRequestID)
			if uuid.Validate(got) != nil {
				t.Fatalf("X-Request-ID = %q, want a generated UUID", got)
			}
			if logged := accessLog(t, buf)["request_id"]; logged != got {
				t.Errorf("access log request_id = %v, response header %q", logged, got)
			}
		})
	}
	// Every request gets its own id.
	h, _ := loggedAPI(&fakeVideoService{})
	a, b := serve(h, http.MethodGet, "/healthz", "", ""), serve(h, http.MethodGet, "/healthz", "", "")
	if a.Header().Get(httpapi.HeaderRequestID) == b.Header().Get(httpapi.HeaderRequestID) {
		t.Error("two requests got the same generated id")
	}
}

func TestAccessLogFields(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	videos := &fakeVideoService{video: &domain.Video{
		ID: videoID, OwnerID: aliceID, OriginalName: "a.mp4", Status: domain.StatusProcessing, CreatedAt: now, UpdatedAt: now,
	}}
	h, buf := loggedAPI(videos)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+videoID, nil)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	req.Header.Set(httpapi.HeaderRequestID, "req-42")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	line := accessLog(t, buf)
	for k, want := range map[string]any{
		"level": "INFO", "service": "api", "method": "GET", "route": "/api/v1/videos/:id",
		"status": float64(200), "bytes": float64(rec.Body.Len()),
		"request_id": "req-42", "user_id": aliceID, "video_id": videoID,
	} {
		if line[k] != want {
			t.Errorf("%s = %v, want %v", k, line[k], want)
		}
	}
	if d, ok := line["duration_ms"].(float64); !ok || d < 0 {
		t.Errorf("duration_ms = %v", line["duration_ms"])
	}
	if _, ok := line["path"]; ok {
		t.Error("access log has the raw path")
	}
	if strings.Contains(buf.String(), aliceToken) {
		t.Error("the bearer token was logged")
	}
}

func TestAccessLogLevels(t *testing.T) {
	for path, want := range map[string]string{
		"/healthz":       "DEBUG",
		"/readyz":        "DEBUG",
		"/":              "DEBUG",
		"/ui/app.js":     "DEBUG",
		"/api/v1/videos": "INFO", // 401
		"/no/such/route": "INFO",
	} {
		h, buf := loggedAPI(&fakeVideoService{})
		serve(h, http.MethodGet, path, "", "")
		line := accessLog(t, buf)
		if line["level"] != want {
			t.Errorf("%s: level %v, want %s", path, line["level"], want)
		}
		if path == "/no/such/route" && line["route"] != "unmatched" {
			t.Errorf("unmatched route logged as %v", line["route"])
		}
	}
}

func TestErrorLogCarriesCorrelationFields(t *testing.T) {
	h, buf := loggedAPI(&fakeVideoService{err: errors.New("database is down")})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+videoID, nil)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	req.Header.Set(httpapi.HeaderRequestID, "req-err")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	var failed map[string]any
	for _, r := range logRecords(t, buf) {
		if r["msg"] == "request failed" {
			failed = r
		}
	}
	if failed == nil {
		t.Fatalf("error not logged: %s", buf.String())
	}
	for k, want := range map[string]any{
		"level": "ERROR", "error": "database is down", "request_id": "req-err",
		"user_id": aliceID, "video_id": videoID, "route": "/api/v1/videos/:id",
	} {
		if failed[k] != want {
			t.Errorf("%s = %v, want %v", k, failed[k], want)
		}
	}
	if accessLog(t, buf)["level"] != "ERROR" {
		t.Error("a 500 is not logged at error level")
	}
}
