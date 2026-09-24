package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "video-processor/internal/adapters/http"
	"video-processor/internal/app"
	"video-processor/internal/domain"
)

const (
	aliceID    = "11111111-1111-4111-8111-111111111111"
	aliceToken = "alice-token"
	videoID    = "22222222-2222-4222-8222-222222222222"
)

// fakeAuth is an AuthService returning canned results and recording input.
type fakeAuth struct {
	user     *domain.User
	token    app.AccessToken
	err      error
	gotInput app.RegisterInput
	gotEmail string
	gotPass  string
}

func (f *fakeAuth) Register(_ context.Context, in app.RegisterInput) (*domain.User, error) {
	f.gotInput = in
	return f.user, f.err
}

func (f *fakeAuth) Login(_ context.Context, email, password string) (app.AccessToken, error) {
	f.gotEmail, f.gotPass = email, password
	return f.token, f.err
}

// fakeTokens accepts only aliceToken.
type fakeTokens struct{}

func (fakeTokens) Verify(token string) (string, error) {
	if token == aliceToken {
		return aliceID, nil
	}
	return "", app.ErrInvalidToken
}

// fakeVideoService returns canned results and records its input.
type fakeVideoService struct {
	page     app.VideoPage
	video    *domain.Video
	err      error
	gotOwner string
	gotPage  app.Page
	gotID    string
	calls    int
}

func (f *fakeVideoService) List(_ context.Context, ownerID string, page app.Page) (app.VideoPage, error) {
	f.calls++
	f.gotOwner, f.gotPage = ownerID, page
	if f.err != nil {
		return app.VideoPage{}, f.err
	}
	if err := page.Validate(); err != nil {
		return app.VideoPage{}, err
	}
	p := f.page
	p.Page = page
	return p, nil
}

func (f *fakeVideoService) Get(_ context.Context, ownerID, id string) (*domain.Video, error) {
	f.calls++
	f.gotOwner, f.gotID = ownerID, id
	return f.video, f.err
}

func newAPI(auth *fakeAuth, videos *fakeVideoService) http.Handler {
	if auth == nil {
		auth = &fakeAuth{}
	}
	if videos == nil {
		videos = &fakeVideoService{}
	}
	return httpapi.NewRouter(httpapi.Options{Auth: auth, Tokens: fakeTokens{}, Videos: videos})
}

func serve(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertError checks the status and the error envelope.
func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code httpapi.ErrorCode) httpapi.ErrorBody {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	body := decode[httpapi.ErrorBody](t, rec)
	if body.Error.Code != code || body.Error.Message == "" {
		t.Errorf("error %+v, want code %s and a message", body.Error, code)
	}
	return body
}

func TestRegister(t *testing.T) {
	created := time.Date(2026, 9, 24, 12, 0, 0, 123456000, time.UTC)
	auth := &fakeAuth{user: &domain.User{
		ID: aliceID, Name: "Ada", Email: "ada@example.com", PasswordHash: "secret-hash", CreatedAt: created,
	}}
	rec := serve(newAPI(auth, nil), http.MethodPost, "/api/v1/auth/register", "",
		`{"name":"Ada","email":"Ada@Example.com","password":"long-enough","extra":1}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := decode[map[string]string](t, rec)
	want := map[string]string{
		"id": aliceID, "name": "Ada", "email": "ada@example.com", "created_at": "2026-09-24T12:00:00.123456Z",
	}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Errorf("body %v, want exactly %v", body, want)
	}
	if auth.gotInput != (app.RegisterInput{Name: "Ada", Email: "Ada@Example.com", Password: "long-enough"}) {
		t.Errorf("input %+v", auth.gotInput)
	}
}

func TestRegisterErrors(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		err    error
		status int
		code   httpapi.ErrorCode
	}{
		{"malformed JSON", `{"name": "Ada", "email": `, nil, http.StatusBadRequest, httpapi.CodeInvalidRequest},
		{"not an object", `["Ada"]`, nil, http.StatusBadRequest, httpapi.CodeInvalidRequest},
		{"wrong field type", `{"name": 5}`, nil, http.StatusBadRequest, httpapi.CodeInvalidRequest},
		{"trailing data", `{"name":"Ada"} {}`, nil, http.StatusBadRequest, httpapi.CodeInvalidRequest},
		{"empty body", ``, nil, http.StatusBadRequest, httpapi.CodeInvalidRequest},
		{
			"validation", `{}`, &app.ValidationError{Field: "password", Message: "must have at least 8 characters"},
			http.StatusBadRequest, httpapi.CodeInvalidRequest,
		},
		{"email taken", `{}`, fmt.Errorf("insert: %w", app.ErrEmailTaken), http.StatusConflict, httpapi.CodeEmailTaken},
		{"unexpected", `{}`, errors.New("db password=hunter2 refused"), http.StatusInternalServerError, httpapi.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(newAPI(&fakeAuth{err: tt.err}, nil), http.MethodPost, "/api/v1/auth/register", "", tt.body)
			body := assertError(t, rec, tt.status, tt.code)
			if strings.Contains(body.Error.Message, "hunter2") {
				t.Error("internal error details leaked")
			}
		})
	}
}

func TestRegisterValidationMessageNamesField(t *testing.T) {
	auth := &fakeAuth{err: &app.ValidationError{Field: "password", Message: "must have at least 8 characters"}}
	rec := serve(newAPI(auth, nil), http.MethodPost, "/api/v1/auth/register", "", `{}`)
	if body := assertError(t, rec, http.StatusBadRequest, httpapi.CodeInvalidRequest); body.Error.Message !=
		"password: must have at least 8 characters" {
		t.Errorf("message %q", body.Error.Message)
	}
}

func TestRegisterRejectsHugeBody(t *testing.T) {
	huge := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`
	rec := serve(newAPI(nil, nil), http.MethodPost, "/api/v1/auth/register", "", huge)
	assertError(t, rec, http.StatusBadRequest, httpapi.CodeInvalidRequest)
}

func TestLogin(t *testing.T) {
	auth := &fakeAuth{token: app.AccessToken{Value: "jwt-value", ExpiresIn: time.Hour}}
	rec := serve(newAPI(auth, nil), http.MethodPost, "/api/v1/auth/login", "",
		`{"email":"ada@example.com","password":"long-enough"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != `{"access_token":"jwt-value","token_type":"Bearer","expires_in":3600}` {
		t.Errorf("body %s", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if auth.gotEmail != "ada@example.com" || auth.gotPass != "long-enough" {
		t.Errorf("credentials %q %q", auth.gotEmail, auth.gotPass)
	}
}

func TestLoginErrors(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		err    error
		status int
		code   httpapi.ErrorCode
	}{
		{"malformed JSON", `{`, nil, http.StatusBadRequest, httpapi.CodeInvalidRequest},
		{"validation", `{}`, &app.ValidationError{Field: "email", Message: "is required"}, http.StatusBadRequest, httpapi.CodeInvalidRequest},
		{"invalid credentials", `{}`, app.ErrInvalidCredentials, http.StatusUnauthorized, httpapi.CodeInvalidCredentials},
		{"unexpected", `{}`, errors.New("boom"), http.StatusInternalServerError, httpapi.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(newAPI(&fakeAuth{err: tt.err}, nil), http.MethodPost, "/api/v1/auth/login", "", tt.body)
			assertError(t, rec, tt.status, tt.code)
		})
	}
}

func TestVideoRoutesRequireBearerToken(t *testing.T) {
	headers := map[string]string{
		"none":                  "",
		"Basic":                 "Basic dXNlcjpwYXNzd29yZA==",
		"token without scheme":  aliceToken,
		"other scheme":          "Token " + aliceToken,
		"Bearer alone":          "Bearer",
		"Bearer and space":      "Bearer ",
		"two tokens":            "Bearer " + aliceToken + " " + aliceToken,
		"rejected by verifier":  "Bearer not-a-jwt",
		"Bearer without space":  "Bearer" + aliceToken,
		"lowercase bad token":   "bearer x",
		"tab-separated scheme":  "Bearer\t" + aliceToken,
		"double space prefixed": "Bearer  bad",
	}
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/videos"},
		{http.MethodPost, "/api/v1/videos"},
		{http.MethodGet, "/api/v1/videos/" + videoID},
		{http.MethodGet, "/api/v1/videos/" + videoID + "/download"},
	}
	for _, route := range routes {
		for name, header := range headers {
			t.Run(route.method+" "+route.path+"/"+name, func(t *testing.T) {
				videos := &fakeVideoService{}
				req := httptest.NewRequest(route.method, route.path, nil)
				if header != "" {
					req.Header.Set("Authorization", header)
				}
				rec := httptest.NewRecorder()
				newAPI(nil, videos).ServeHTTP(rec, req)
				assertError(t, rec, http.StatusUnauthorized, httpapi.CodeUnauthorized)
				if rec.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Errorf("WWW-Authenticate %q", rec.Header().Get("WWW-Authenticate"))
				}
				if videos.calls != 0 {
					t.Error("the handler ran for an unauthenticated request")
				}
			})
		}
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos", nil)
	req.Header.Set("Authorization", "bearer "+aliceToken)
	rec := httptest.NewRecorder()
	newAPI(nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestListVideos(t *testing.T) {
	created := time.Date(2026, 9, 24, 12, 5, 0, 0, time.UTC)
	videos := &fakeVideoService{page: app.VideoPage{
		Items: []domain.Video{
			{
				ID: videoID, OwnerID: aliceID, OriginalName: "holiday.mp4", Status: domain.StatusDone,
				FrameCount: 12, ZipKey: "zips/x.zip", CreatedAt: created, UpdatedAt: created.Add(9 * time.Second),
			},
			{
				ID: "33333333-3333-4333-8333-333333333333", OwnerID: aliceID, OriginalName: "broken.avi",
				Status: domain.StatusFailed, ErrorMessage: "no video stream", CreatedAt: created, UpdatedAt: created,
			},
			{
				ID: "44444444-4444-4444-8444-444444444444", OwnerID: aliceID, OriginalName: "new.mkv",
				Status: domain.StatusPending, CreatedAt: created, UpdatedAt: created,
			},
		},
		Total: 3,
	}}
	rec := serve(newAPI(nil, videos), http.MethodGet, "/api/v1/videos", aliceToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	want := `{"items":[` +
		`{"id":"22222222-2222-4222-8222-222222222222","original_name":"holiday.mp4","status":"DONE","frame_count":12,` +
		`"error_message":null,"created_at":"2026-09-24T12:05:00Z","updated_at":"2026-09-24T12:05:09Z",` +
		`"download_url":"/api/v1/videos/22222222-2222-4222-8222-222222222222/download"},` +
		`{"id":"33333333-3333-4333-8333-333333333333","original_name":"broken.avi","status":"FAILED","frame_count":null,` +
		`"error_message":"no video stream","created_at":"2026-09-24T12:05:00Z","updated_at":"2026-09-24T12:05:00Z"},` +
		`{"id":"44444444-4444-4444-8444-444444444444","original_name":"new.mkv","status":"PENDING","frame_count":null,` +
		`"error_message":null,"created_at":"2026-09-24T12:05:00Z","updated_at":"2026-09-24T12:05:00Z"}` +
		`],"page":1,"page_size":20,"total":3}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
	if videos.gotOwner != aliceID || videos.gotPage != (app.Page{Number: 1, Size: 20}) {
		t.Errorf("service called with owner %q page %+v", videos.gotOwner, videos.gotPage)
	}
}

func TestListVideosEmptyItemsIsArray(t *testing.T) {
	rec := serve(newAPI(nil, &fakeVideoService{}), http.MethodGet, "/api/v1/videos?page=3&page_size=2", aliceToken, "")
	if got := rec.Body.String(); rec.Code != http.StatusOK || got != `{"items":[],"page":3,"page_size":2,"total":0}` {
		t.Errorf("status %d body %s", rec.Code, got)
	}
}

func TestListVideosInvalidPagination(t *testing.T) {
	for _, query := range []string{
		"page=0", "page=-1", "page=abc", "page=1.5", "page=", "page=99999999999999999999",
		"page_size=0", "page_size=101", "page_size=-1", "page_size=abc",
	} {
		t.Run(query, func(t *testing.T) {
			rec := serve(newAPI(nil, &fakeVideoService{}), http.MethodGet, "/api/v1/videos?"+query, aliceToken, "")
			assertError(t, rec, http.StatusBadRequest, httpapi.CodeInvalidRequest)
		})
	}
}

func TestListVideosInternalError(t *testing.T) {
	rec := serve(newAPI(nil, &fakeVideoService{err: errors.New("db down")}), http.MethodGet, "/api/v1/videos", aliceToken, "")
	assertError(t, rec, http.StatusInternalServerError, httpapi.CodeInternal)
}

func TestGetVideo(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	videos := &fakeVideoService{video: &domain.Video{
		ID: videoID, OwnerID: aliceID, OriginalName: "a.mp4", Status: domain.StatusProcessing, CreatedAt: now, UpdatedAt: now,
	}}
	rec := serve(newAPI(nil, videos), http.MethodGet, "/api/v1/videos/"+videoID, aliceToken, "")
	want := `{"id":"22222222-2222-4222-8222-222222222222","original_name":"a.mp4","status":"PROCESSING",` +
		`"frame_count":null,"error_message":null,"created_at":"2026-09-24T12:00:00Z","updated_at":"2026-09-24T12:00:00Z"}`
	if got := rec.Body.String(); rec.Code != http.StatusOK || got != want {
		t.Errorf("status %d body\n got %s\nwant %s", rec.Code, got, want)
	}
	if videos.gotOwner != aliceID || videos.gotID != videoID {
		t.Errorf("service called with owner %q id %q", videos.gotOwner, videos.gotID)
	}
}

func TestGetVideoErrors(t *testing.T) {
	notFound := serve(newAPI(nil, &fakeVideoService{err: fmt.Errorf("x: %w", app.ErrNotFound)}),
		http.MethodGet, "/api/v1/videos/not-a-uuid", aliceToken, "")
	assertError(t, notFound, http.StatusNotFound, httpapi.CodeNotFound)

	internal := serve(newAPI(nil, &fakeVideoService{err: errors.New("db down")}),
		http.MethodGet, "/api/v1/videos/"+videoID, aliceToken, "")
	assertError(t, internal, http.StatusInternalServerError, httpapi.CodeInternal)
}

func TestUploadAndDownloadNotImplementedYet(t *testing.T) {
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/videos"},
		{http.MethodGet, "/api/v1/videos/" + videoID + "/download"},
	} {
		rec := serve(newAPI(nil, nil), r.method, r.path, aliceToken, "")
		assertError(t, rec, http.StatusNotImplemented, httpapi.CodeInternal)
	}
}
