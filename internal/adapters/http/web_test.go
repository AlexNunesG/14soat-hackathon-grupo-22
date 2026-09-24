package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	httpapi "video-processor/internal/adapters/http"
)

func newUI() http.Handler {
	return httpapi.NewRouter(httpapi.Options{
		Auth: &fakeAuth{}, Tokens: fakeTokens{}, Videos: &fakeVideoService{}, Uploads: &fakeUploads{}, WebUI: true,
	})
}

func TestWebUIPage(t *testing.T) {
	rec := get(t, newUI(), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "no-cache",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s %q, want %q", header, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") ||
		!strings.Contains(csp, "script-src 'self'") {
		t.Errorf("Content-Security-Policy %q", csp)
	}

	// JS-free smoke check: the page has every element the script drives,
	// and loads its assets from /ui/ (same origin, no CDN).
	page := rec.Body.String()
	for _, id := range []string{
		"login-form", "register-form", "upload-form", "upload-files", "videos-table", "videos-body",
		"prev-page", "next-page", "page-size", "logout-button", "notice",
	} {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("page has no element #%s", id)
		}
	}
	for _, ref := range []string{`src="/ui/app.js"`, `href="/ui/style.css"`, `name="videos"`} {
		if !strings.Contains(page, ref) {
			t.Errorf("page does not contain %s", ref)
		}
	}
	for _, attr := range []string{`src="http`, `href="http`, `src="//`, `href="//`} {
		if strings.Contains(page, attr) {
			t.Errorf("page loads an external resource (%s...)", attr)
		}
	}
}

func TestWebUIAssets(t *testing.T) {
	h := newUI()
	for path, wantType := range map[string]string{
		"/ui/app.js":    "text/javascript; charset=utf-8",
		"/ui/style.css": "text/css; charset=utf-8",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != wantType {
			t.Errorf("GET %s: Content-Type %q, want %q", path, ct, wantType)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Body.Len() == 0 {
			t.Errorf("GET %s: headers %v, %d bytes", path, rec.Header(), rec.Body.Len())
		}
	}
	// The script drives the API over the same origin.
	js := get(t, h, "/ui/app.js").Body.String()
	for _, want := range []string{`"/api/v1"`, `"Authorization"`, "sessionStorage", `"videos"`, "Content-Disposition"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js does not contain %s", want)
		}
	}
}

func TestWebUIDoesNotShadowOtherRoutes(t *testing.T) {
	h := newUI()
	for _, path := range []string{
		"/ui/", "/ui/missing.js", "/ui/index.html", "/ui/../go.mod", "/nope", "/api/v1/unknown", "/api/v1/",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, rec.Code)
			continue
		}
		if body := decode[httpapi.ErrorBody](t, rec); body.Error.Code != httpapi.CodeNotFound {
			t.Errorf("GET %s: body %+v", path, body)
		}
	}
	if rec := get(t, h, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz: status %d", rec.Code)
	}
	if rec := get(t, h, "/readyz"); rec.Code != http.StatusOK {
		t.Errorf("/readyz: status %d", rec.Code)
	}
	// The API still answers with JSON (here: 401 without a token).
	rec := get(t, h, "/api/v1/videos")
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("/api/v1/videos: status %d, Content-Type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestWebUIIsOptional(t *testing.T) {
	for _, path := range []string{"/", "/ui/app.js"} {
		if rec := get(t, newAPI(nil, nil), path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s without WebUI: status %d, want 404", path, rec.Code)
		}
	}
}
