package integration

// Helpers that speak the v1 API of docs/openapi.yaml: schema types, users and
// tokens, authenticated requests, uploads, status polling and the error
// envelope.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// Video statuses (schema VideoStatus).
const (
	statusPending    = "PENDING"
	statusProcessing = "PROCESSING"
	statusDone       = "DONE"
	statusFailed     = "FAILED"
)

// supportedFormats are the upload extensions accepted by the contract.
var supportedFormats = []string{"mp4", "avi", "mov", "mkv", "wmv", "flv", "webm"}

// uuidPattern matches the canonical textual form of a UUID.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// userResponse mirrors the User schema.
type userResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

// tokenResponse mirrors the Token schema.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// video mirrors the Video schema. Nullable and optional fields are pointers.
type video struct {
	ID           string  `json:"id"`
	OriginalName string  `json:"original_name"`
	Status       string  `json:"status"`
	FrameCount   *int    `json:"frame_count"`
	ErrorMessage *string `json:"error_message"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	DownloadURL  *string `json:"download_url"`
}

// videoPage mirrors the VideoPage schema.
type videoPage struct {
	Items    []video `json:"items"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Total    int     `json:"total"`
}

// apiError mirrors the Error schema.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// testUser is a registered user and the password it registered with.
type testUser struct {
	userResponse
	Password string
}

// namedFile is one file of an upload request.
type namedFile struct {
	name string
	data []byte
}

// randomHex returns n random bytes, hex encoded.
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// uniqueEmail returns an e-mail address no other test uses.
func uniqueEmail(t *testing.T) string {
	t.Helper()
	return "user-" + randomHex(t, 8) + "@example.com"
}

// doRequest sends a request to the API and returns the response with its
// body already read and closed. An empty token sends no Authorization
// header; otherwise the header is "Bearer <token>".
func doRequest(t *testing.T, method, path, token, contentType string, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return doRequestWithHeader(t, method, path, header, body)
}

// doRequestWithHeader is doRequest with full control over the headers.
func doRequestWithHeader(t *testing.T, method, path string, header http.Header, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, appURL(t, path), body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading body: %v", method, path, err)
	}
	return resp, data
}

// authGet sends GET path with the bearer token.
func authGet(t *testing.T, token, path string) (*http.Response, []byte) {
	t.Helper()
	return doRequest(t, http.MethodGet, path, token, "", nil)
}

// postJSON sends POST path with v encoded as JSON, without authentication.
func postJSON(t *testing.T, path string, v any) (*http.Response, []byte) {
	t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return doRequest(t, http.MethodPost, path, "", "application/json", bytes.NewReader(payload))
}

// register creates a user and returns the response.
func register(t *testing.T, name, email, password string) (*http.Response, []byte) {
	t.Helper()
	return postJSON(t, "/api/v1/auth/register", map[string]string{
		"name": name, "email": email, "password": password,
	})
}

// login exchanges credentials for a token and returns the response.
func login(t *testing.T, email, password string) (*http.Response, []byte) {
	t.Helper()
	return postJSON(t, "/api/v1/auth/login", map[string]string{
		"email": email, "password": password,
	})
}

// registerAndLogin creates a fresh user with a random e-mail and returns it
// with a valid access token.
//
// The nolint is needed while the tests are skipped: unparam sees the code
// after notImplemented(t) as unreachable, so only callers that drop the user
// count.
//
//nolint:unparam // see above
func registerAndLogin(t *testing.T) (testUser, string) {
	t.Helper()
	u := testUser{Password: "pw-" + randomHex(t, 8)}
	resp, body := register(t, "Test User", uniqueEmail(t), u.Password)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", resp.StatusCode, body)
	}
	assertJSON(t, resp, body, &u.userResponse)

	resp, body = login(t, u.Email, u.Password)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: expected 200, got %d: %s", resp.StatusCode, body)
	}
	var tok tokenResponse
	assertJSON(t, resp, body, &tok)
	if tok.AccessToken == "" {
		t.Fatalf("login returned an empty access_token: %s", body)
	}
	return u, tok.AccessToken
}

// multipartBody builds a multipart/form-data body with each file as a part
// of the field "videos", and returns it with its content type.
func multipartBody(t *testing.T, files ...namedFile) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for _, f := range files {
		part, err := w.CreateFormFile("videos", f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body, w.FormDataContentType()
}

// uploadVideos sends POST /api/v1/videos with the files in the multipart
// field "videos" and returns the raw response.
func uploadVideos(t *testing.T, token string, files ...namedFile) (*http.Response, []byte) {
	t.Helper()
	body, contentType := multipartBody(t, files...)
	return doRequest(t, http.MethodPost, "/api/v1/videos", token, contentType, body)
}

// mustUpload uploads the files, requires 202 and returns the created videos,
// checked to be one per file, in upload order, all PENDING.
func mustUpload(t *testing.T, token string, files ...namedFile) []video {
	t.Helper()
	resp, body := uploadVideos(t, token, files...)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload: expected 202, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Videos []json.RawMessage `json:"videos"`
	}
	assertJSON(t, resp, body, &out)
	if len(out.Videos) != len(files) {
		t.Fatalf("upload of %d files returned %d videos: %s", len(files), len(out.Videos), body)
	}
	videos := make([]video, 0, len(files))
	for i, f := range files {
		v := decodeVideo(t, out.Videos[i])
		if v.OriginalName != f.name {
			t.Errorf("videos[%d].original_name = %q, want %q (upload order)", i, v.OriginalName, f.name)
		}
		if v.Status != statusPending {
			t.Errorf("videos[%d].status = %q, want %q", i, v.Status, statusPending)
		}
		videos = append(videos, v)
	}
	return videos
}

// uploadOne uploads a single file and returns the created video.
func uploadOne(t *testing.T, token, name string, data []byte) video {
	t.Helper()
	return mustUpload(t, token, namedFile{name, data})[0]
}

// decodeVideo decodes a Video object and checks what the schema promises for
// every video: all required fields present, a UUID id, a known status,
// RFC 3339 timestamps, frame_count/error_message consistent with the status
// and download_url absent unless DONE.
func decodeVideo(t *testing.T, raw json.RawMessage) video {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("video is not a JSON object: %v (%s)", err, raw)
	}
	for _, key := range []string{"id", "original_name", "status", "frame_count", "error_message", "created_at", "updated_at"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("video is missing required field %q: %s", key, raw)
		}
	}
	var v video
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid video: %v (%s)", err, raw)
	}
	if !uuidPattern.MatchString(v.ID) {
		t.Errorf("id %q is not a UUID", v.ID)
	}
	for name, ts := range map[string]string{"created_at": v.CreatedAt, "updated_at": v.UpdatedAt} {
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Errorf("%s %q is not RFC 3339: %v", name, ts, err)
		}
	}
	switch v.Status {
	case statusDone:
		if v.FrameCount == nil || *v.FrameCount < 1 {
			t.Errorf("DONE video must have frame_count >= 1: %s", raw)
		}
		if v.ErrorMessage != nil {
			t.Errorf("DONE video must have error_message null: %s", raw)
		}
	case statusFailed:
		if v.FrameCount != nil {
			t.Errorf("FAILED video must have frame_count null: %s", raw)
		}
		if v.ErrorMessage == nil || *v.ErrorMessage == "" {
			t.Errorf("FAILED video must have a non-empty error_message: %s", raw)
		}
	case statusPending, statusProcessing:
		if v.FrameCount != nil || v.ErrorMessage != nil {
			t.Errorf("%s video must have frame_count and error_message null: %s", v.Status, raw)
		}
	default:
		t.Errorf("status %q is not one of PENDING, PROCESSING, DONE, FAILED", v.Status)
	}
	if v.DownloadURL != nil {
		if v.Status != statusDone {
			t.Errorf("download_url must be present only when DONE, got it with status %s: %s", v.Status, raw)
		} else if want := "/api/v1/videos/" + v.ID + "/download"; *v.DownloadURL != want {
			t.Errorf("download_url = %q, want %q", *v.DownloadURL, want)
		}
	}
	return v
}

// getVideo fetches GET /api/v1/videos/{id}, requiring 200.
func getVideo(t *testing.T, token, id string) video {
	t.Helper()
	resp, body := authGet(t, token, "/api/v1/videos/"+id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET video %s: expected 200, got %d: %s", id, resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("unexpected content type %q", ct)
	}
	return decodeVideo(t, body)
}

// listVideos fetches GET /api/v1/videos with the raw query, requiring 200.
func listVideos(t *testing.T, token, query string) videoPage {
	t.Helper()
	path := "/api/v1/videos"
	if query != "" {
		path += "?" + query
	}
	resp, body := authGet(t, token, path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", path, resp.StatusCode, body)
	}
	var raw struct {
		Items    json.RawMessage `json:"items"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
		Total    int             `json:"total"`
	}
	assertJSON(t, resp, body, &raw)
	var items []json.RawMessage
	if err := json.Unmarshal(raw.Items, &items); err != nil || items == nil {
		t.Fatalf("GET %s: items must be an array: %s", path, body)
	}
	page := videoPage{Items: make([]video, 0, len(items)), Page: raw.Page, PageSize: raw.PageSize, Total: raw.Total}
	for _, item := range items {
		page.Items = append(page.Items, decodeVideo(t, item))
	}
	return page
}

// processingTimeout bounds waitForStatus: PROCESSING_TIMEOUT (a Go
// duration) or 3 minutes.
func processingTimeout(t *testing.T) time.Duration {
	t.Helper()
	v := os.Getenv("PROCESSING_TIMEOUT")
	if v == "" {
		return 3 * time.Minute
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("invalid PROCESSING_TIMEOUT %q: %v", v, err)
	}
	return d
}

// waitForStatus polls GET /api/v1/videos/{id} every 500ms until the video
// reaches a final status (DONE or FAILED). It fails the test at once when
// that final status is not want, and when the timeout expires.
func waitForStatus(t *testing.T, token, id, want string) video {
	t.Helper()
	deadline := time.Now().Add(processingTimeout(t))
	for {
		v := getVideo(t, token, id)
		if v.Status == statusDone || v.Status == statusFailed {
			if v.Status != want {
				msg := ""
				if v.ErrorMessage != nil {
					msg = *v.ErrorMessage
				}
				t.Fatalf("video %s (%s) ended %s, want %s (error_message %q)", id, v.OriginalName, v.Status, want, msg)
			}
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("video %s (%s) still %s after %s, want %s", id, v.OriginalName, v.Status, processingTimeout(t), want)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// assertError checks that the response has the given status and the error
// envelope {"error": {"code": code, "message": "<non-empty>"}} and nothing
// else at the top level.
func assertError(t *testing.T, resp *http.Response, body []byte, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Errorf("expected status %d, got %d: %s", status, resp.StatusCode, body)
	}
	var top map[string]json.RawMessage
	assertJSON(t, resp, body, &top)
	if len(top) != 1 || top["error"] == nil {
		t.Errorf(`error body must have only the "error" key: %s`, body)
	}
	var e apiError
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("invalid error body: %v (%s)", err, body)
	}
	if e.Error.Code != code {
		t.Errorf("error.code = %q, want %q (%s)", e.Error.Code, code, body)
	}
	if e.Error.Message == "" {
		t.Errorf("error.message is empty: %s", body)
	}
}

// errorMessage returns error.message of an error body.
func errorMessage(t *testing.T, body []byte) string {
	t.Helper()
	var e apiError
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("invalid error body: %v (%s)", err, body)
	}
	return e.Error.Message
}

// assertNoVideos fails unless the user has no videos at all.
func assertNoVideos(t *testing.T, token string) {
	t.Helper()
	page := listVideos(t, token, "")
	if page.Total != 0 || len(page.Items) != 0 {
		t.Errorf("expected no videos, got total %d and %d items", page.Total, len(page.Items))
	}
}

// videoIDs returns the ids of the videos, in order.
func videoIDs(videos []video) []string {
	ids := make([]string, len(videos))
	for i, v := range videos {
		ids[i] = v.ID
	}
	return ids
}

// sortedCopy returns a sorted copy of s.
func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

// downloadPath is the download route of a video.
func downloadPath(id string) string {
	return fmt.Sprintf("/api/v1/videos/%s/download", id)
}

// describe renders a video as JSON for failure messages.
func describe(v video) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(data)
}

// randomUUID returns a random version 4 UUID, which no video has.
func randomUUID(t *testing.T) string {
	t.Helper()
	h := []byte(randomHex(t, 16))
	h[12] = '4'
	h[16] = "89ab"[h[16]%4]
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
