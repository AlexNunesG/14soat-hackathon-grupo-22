package integration

// Integration tests for sign-up, login and bearer authentication
// (docs/openapi.yaml, tag "auth", and the bearerAuth security scheme).
//
// Expired tokens also yield 401 unauthorized per the contract, but a
// black-box test cannot produce one: it would need the signing secret, or
// waiting out the real token lifetime. That case is covered by the unit
// tests of the token verification instead.

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRegisterCreatesUser(t *testing.T) {
	t.Parallel()
	mixedCase := "Ada.User-" + strings.ToUpper(randomHex(t, 8)) + "@Example.COM"

	resp, body := register(t, "Ada Lovelace", mixedCase, "correct-horse-battery")

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}
	var fields map[string]json.RawMessage
	assertJSON(t, resp, body, &fields)
	for key := range fields {
		switch key {
		case "id", "name", "email", "created_at":
		default:
			t.Errorf("unexpected field %q in the user (the password must never be returned): %s", key, body)
		}
	}
	var u userResponse
	assertJSON(t, resp, body, &u)
	if !uuidPattern.MatchString(u.ID) {
		t.Errorf("id %q is not a UUID", u.ID)
	}
	if u.Name != "Ada Lovelace" {
		t.Errorf("name = %q, want %q", u.Name, "Ada Lovelace")
	}
	if want := strings.ToLower(mixedCase); u.Email != want {
		t.Errorf("email = %q, want it lowercased: %q", u.Email, want)
	}
	if _, err := time.Parse(time.RFC3339, u.CreatedAt); err != nil {
		t.Errorf("created_at %q is not RFC 3339: %v", u.CreatedAt, err)
	}
	if bytes.Contains(body, []byte("correct-horse-battery")) {
		t.Errorf("the response contains the password: %s", body)
	}
}

func TestRegisterDuplicateEmailIsRejected(t *testing.T) {
	t.Parallel()
	email := uniqueEmail(t)
	if resp, body := register(t, "First", email, "password-1"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first registration: expected 201, got %d: %s", resp.StatusCode, body)
	}

	for name, dup := range map[string]string{
		"same e-mail":           email,
		"different letter case": strings.ToUpper(email),
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := register(t, "Second", dup, "password-2")
			assertError(t, resp, body, http.StatusConflict, "email_taken")
		})
	}
}

func TestRegisterInvalidInputIsRejected(t *testing.T) {
	t.Parallel()
	valid := func() map[string]any {
		return map[string]any{"name": "Ada", "email": uniqueEmail(t), "password": "long-enough"}
	}
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing name", func(m map[string]any) { delete(m, "name") }},
		{"empty name", func(m map[string]any) { m["name"] = "" }},
		{"name longer than 100 characters", func(m map[string]any) { m["name"] = strings.Repeat("a", 101) }},
		{"missing email", func(m map[string]any) { delete(m, "email") }},
		{"invalid email", func(m map[string]any) { m["email"] = "not-an-email" }},
		{"missing password", func(m map[string]any) { delete(m, "password") }},
		{"password shorter than 8 characters", func(m map[string]any) { m["password"] = "1234567" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := valid()
			tc.edit(req)
			resp, body := postJSON(t, "/api/v1/auth/register", req)
			assertError(t, resp, body, http.StatusBadRequest, "invalid_request")
		})
	}
	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		resp, body := doRequest(t, http.MethodPost, "/api/v1/auth/register", "", "application/json",
			strings.NewReader(`{"name": "Ada", "email": `))
		assertError(t, resp, body, http.StatusBadRequest, "invalid_request")
	})
}

func TestLoginReturnsBearerToken(t *testing.T) {
	t.Parallel()
	email := uniqueEmail(t)
	if resp, body := register(t, "Ada", email, "correct-horse-battery"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d: %s", resp.StatusCode, body)
	}

	for name, loginEmail := range map[string]string{
		"same e-mail":           email,
		"different letter case": strings.ToUpper(email),
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := login(t, loginEmail, "correct-horse-battery")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
			}
			var tok tokenResponse
			assertJSON(t, resp, body, &tok)
			if tok.AccessToken == "" {
				t.Error("access_token is empty")
			}
			if tok.TokenType != "Bearer" {
				t.Errorf(`token_type = %q, want "Bearer"`, tok.TokenType)
			}
			if tok.ExpiresIn < 1 {
				t.Errorf("expires_in = %d, want > 0", tok.ExpiresIn)
			}
			// The token grants access to the protected routes.
			if resp, body := authGet(t, tok.AccessToken, "/api/v1/videos"); resp.StatusCode != http.StatusOK {
				t.Errorf("GET /api/v1/videos with the new token: expected 200, got %d: %s", resp.StatusCode, body)
			}
		})
	}
}

func TestLoginWithInvalidCredentialsIsRejected(t *testing.T) {
	t.Parallel()
	user, _ := registerAndLogin(t)

	wrongPassResp, wrongPassBody := login(t, user.Email, user.Password+"-wrong")
	assertError(t, wrongPassResp, wrongPassBody, http.StatusUnauthorized, "invalid_credentials")

	unknownResp, unknownBody := login(t, uniqueEmail(t), user.Password)
	assertError(t, unknownResp, unknownBody, http.StatusUnauthorized, "invalid_credentials")

	if !bytes.Equal(bytes.TrimSpace(wrongPassBody), bytes.TrimSpace(unknownBody)) {
		t.Errorf("wrong password and unknown e-mail must get the same response:\n  wrong password: %s\n  unknown e-mail: %s",
			wrongPassBody, unknownBody)
	}
}

// forgedToken returns a well-formed HS256 JWT for subject, valid for an
// hour, signed with a random key the server cannot know.
func forgedToken(t *testing.T, subject string) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	now := time.Now().Unix()
	claims, err := json.Marshal(map[string]any{"sub": subject, "iat": now, "exp": now + 3600})
	if err != nil {
		t.Fatal(err)
	}
	signingInput := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signingInput))
	return signingInput + "." + enc.EncodeToString(mac.Sum(nil))
}

func TestProtectedRoutesRequireValidToken(t *testing.T) {
	t.Parallel()
	user, token := registerAndLogin(t)
	someID := randomUUID(t)

	authorizations := []struct {
		name   string
		header string // Authorization header; "" sends none
	}{
		{"no Authorization header", ""},
		{"Basic scheme", "Basic dXNlcjpwYXNzd29yZA=="},
		{"valid token without scheme", token},
		{"valid token with another scheme", "Token " + token},
		{"Bearer without token", "Bearer"},
		{"Bearer with empty token", "Bearer "},
		{"malformed token", "Bearer not-a-jwt"},
		{"malformed three-part token", "Bearer a.b.c"},
		{"token with bad signature", "Bearer " + forgedToken(t, user.ID)},
	}
	video := makeMP4(t, 1)
	routes := []struct {
		method, path string
		upload       bool
	}{
		{http.MethodGet, "/api/v1/videos", false},
		{http.MethodGet, "/api/v1/videos/" + someID, false},
		{http.MethodGet, downloadPath(someID), false},
		{http.MethodPost, "/api/v1/videos", true},
	}
	// The group returns once all its parallel subtests have finished.
	t.Run("routes", func(t *testing.T) {
		for _, route := range routes {
			for _, auth := range authorizations {
				t.Run(route.method+" "+route.path+"/"+auth.name, func(t *testing.T) {
					t.Parallel()
					header := http.Header{}
					if auth.header != "" {
						header.Set("Authorization", auth.header)
					}
					body := &bytes.Buffer{}
					if route.upload {
						var contentType string
						body, contentType = multipartBody(t, namedFile{"clip.mp4", video})
						header.Set("Content-Type", contentType)
					}
					resp, respBody := doRequestWithHeader(t, route.method, route.path, header, body)
					assertError(t, resp, respBody, http.StatusUnauthorized, "unauthorized")
				})
			}
		}
	})

	// None of the rejected uploads created a video.
	assertNoVideos(t, token)
}
