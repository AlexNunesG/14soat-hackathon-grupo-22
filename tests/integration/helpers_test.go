package integration

// Shared helpers for the integration tests. Only helpers used by some test
// live here; new ones arrive together with the tests that need them.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// httpClient is shared by every request to the app. The timeout keeps a
// stuck server from hanging the suite.
var httpClient = &http.Client{Timeout: 2 * time.Minute}

// notImplemented skips a test until the implementation supports it. To
// enable a test, delete its notImplemented line in the pull request that
// implements the behavior (see tests/integration/README.md).
func notImplemented(t *testing.T) {
	t.Helper()
	t.Skip("not implemented yet: delete the notImplemented line to enable this test (see tests/integration/README.md)")
}

// appURL returns the absolute URL of path on the API under test. It fails
// the test when there is no app: BASE_URL unset and no compose stack
// started, or the API never became healthy.
func appURL(t *testing.T, path string) string {
	t.Helper()
	if baseURL == "" {
		t.Fatal(errNoApp)
	}
	return baseURL + path
}

// get sends GET path to the API and returns the response with its body
// already read and closed.
func get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := httpClient.Get(appURL(t, path))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

// assertJSON fails the test unless resp is JSON and body decodes into v.
func assertJSON(t *testing.T, resp *http.Response, body []byte, v any) {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("unexpected content type %q", ct)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("invalid JSON body: %v (%s)", err, body)
	}
}
