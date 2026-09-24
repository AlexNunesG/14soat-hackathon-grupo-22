package integration

// Integration tests for the CORS middleware applied to every route.

import (
	"io"
	"net/http"
	"testing"
)

func TestCORSHeadersOnEveryResponse(t *testing.T) {
	for _, path := range []string{"/", "/api/status", "/download/missing.zip"} {
		t.Run(path, func(t *testing.T) {
			resp, _ := get(t, path)
			assertCORS(t, resp)
		})
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	for _, path := range []string{"/upload", "/api/status", "/download/x.zip", "/any/unknown/route"} {
		t.Run(path, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodOptions, baseURL+path, nil)
			req.Header.Set("Origin", "http://example.com")
			req.Header.Set("Access-Control-Request-Method", "POST")
			resp, err := httpClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("expected 204, got %d", resp.StatusCode)
			}
			if len(body) != 0 {
				t.Errorf("expected empty body, got %q", body)
			}
			assertCORS(t, resp)
		})
	}
}

func assertCORS(t *testing.T, resp *http.Response) {
	t.Helper()
	want := map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "POST, GET, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type",
	}
	for h, v := range want {
		if got := resp.Header.Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
}
