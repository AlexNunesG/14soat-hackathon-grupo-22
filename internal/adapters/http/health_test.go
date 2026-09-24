package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "video-processor/internal/adapters/http"
)

func ok() httpapi.Pinger { return httpapi.PingFunc(func(context.Context) error { return nil }) }

func failing() httpapi.Pinger {
	return httpapi.PingFunc(func(context.Context) error { return errors.New("connection refused") })
}

// hanging blocks until its context is done, like a dependency that does not
// answer.
func hanging() httpapi.Pinger {
	return httpapi.PingFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return v
}

type readiness struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func TestHealthz(t *testing.T) {
	// Liveness never looks at dependencies, even failing ones.
	h := httpapi.NewRouter(httpapi.Options{Checks: []httpapi.Check{{Name: "database", Pinger: failing()}}})
	rec := get(t, h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if body := decode[map[string]string](t, rec); len(body) != 1 || body["status"] != "ok" {
		t.Errorf(`body %v, want {"status":"ok"}`, body)
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		checks     []httpapi.Check
		wantCode   int
		wantStatus string
		wantChecks map[string]string
	}{
		{
			name: "all up",
			checks: []httpapi.Check{
				{Name: "database", Pinger: ok()}, {Name: "broker", Pinger: ok()}, {Name: "storage", Pinger: ok()},
			},
			wantCode: http.StatusOK, wantStatus: "ok",
			wantChecks: map[string]string{"database": "ok", "broker": "ok", "storage": "ok"},
		},
		{
			name: "broker down",
			checks: []httpapi.Check{
				{Name: "database", Pinger: ok()}, {Name: "broker", Pinger: failing()}, {Name: "storage", Pinger: ok()},
			},
			wantCode: http.StatusServiceUnavailable, wantStatus: "error",
			wantChecks: map[string]string{"database": "ok", "broker": "error", "storage": "ok"},
		},
		{
			name: "database hangs, storage down",
			checks: []httpapi.Check{
				{Name: "database", Pinger: hanging()}, {Name: "broker", Pinger: ok()}, {Name: "storage", Pinger: failing()},
			},
			wantCode: http.StatusServiceUnavailable, wantStatus: "error",
			wantChecks: map[string]string{"database": "error", "broker": "ok", "storage": "error"},
		},
		{
			name:       "no dependencies",
			wantCode:   http.StatusOK,
			wantStatus: "ok",
			wantChecks: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := httpapi.NewRouter(httpapi.Options{Checks: tt.checks, CheckTimeout: 50 * time.Millisecond})
			start := time.Now()
			rec := get(t, h, "/readyz")
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("took %s: checks must be bounded by the timeout", elapsed)
			}
			if rec.Code != tt.wantCode {
				t.Errorf("status %d, want %d", rec.Code, tt.wantCode)
			}
			body := decode[readiness](t, rec)
			if body.Status != tt.wantStatus {
				t.Errorf("status %q, want %q", body.Status, tt.wantStatus)
			}
			if len(body.Checks) != len(tt.wantChecks) {
				t.Errorf("checks %v, want %v", body.Checks, tt.wantChecks)
			}
			for k, v := range tt.wantChecks {
				if body.Checks[k] != v {
					t.Errorf("checks.%s = %q, want %q", k, body.Checks[k], v)
				}
			}
			if strings.Contains(rec.Body.String(), "connection refused") {
				t.Error("error details leaked into the response")
			}
		})
	}
}

func TestReadyzRunsChecksConcurrently(t *testing.T) {
	slow := httpapi.PingFunc(func(context.Context) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	})
	h := httpapi.NewRouter(httpapi.Options{
		Checks:       []httpapi.Check{{Name: "a", Pinger: slow}, {Name: "b", Pinger: slow}, {Name: "c", Pinger: slow}},
		CheckTimeout: time.Second,
	})
	start := time.Now()
	if rec := get(t, h, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if elapsed := time.Since(start); elapsed >= 550*time.Millisecond {
		t.Errorf("took %s, want the checks to run in parallel", elapsed)
	}
}

func TestUnknownRouteUsesErrorEnvelope(t *testing.T) {
	h := httpapi.NewRouter(httpapi.Options{})
	for _, path := range []string{"/", "/nope", "/api/v1/unknown"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, rec.Code)
		}
		body := decode[httpapi.ErrorBody](t, rec)
		if body.Error.Code != httpapi.CodeNotFound || body.Error.Message == "" {
			t.Errorf("GET %s: body %+v", path, body)
		}
	}
}
