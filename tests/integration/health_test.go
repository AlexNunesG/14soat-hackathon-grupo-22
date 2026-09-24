package integration

// Integration tests for the probes GET /healthz and GET /readyz
// (docs/openapi.yaml, tag "health"). They need no authentication.

import (
	"net/http"
	"testing"
)

func TestHealthzReturnsOK(t *testing.T) {
	resp, body := get(t, "/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz: expected 200, got %d: %s", resp.StatusCode, body)
	}
	var liveness struct {
		Status string `json:"status"`
	}
	assertJSON(t, resp, body, &liveness)
	if liveness.Status != "ok" {
		t.Errorf(`status = %q, want "ok"`, liveness.Status)
	}
}

func TestReadyzReportsDependencies(t *testing.T) {
	resp, body := get(t, "/readyz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz: expected 200, got %d: %s", resp.StatusCode, body)
	}
	var readiness struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	assertJSON(t, resp, body, &readiness)
	if readiness.Status != "ok" {
		t.Errorf(`status = %q, want "ok"`, readiness.Status)
	}
	for _, dep := range []string{"database", "broker", "storage"} {
		if got, ok := readiness.Checks[dep]; !ok {
			t.Errorf("checks.%s is missing: %s", dep, body)
		} else if got != "ok" {
			t.Errorf(`checks.%s = %q, want "ok"`, dep, got)
		}
	}
}
