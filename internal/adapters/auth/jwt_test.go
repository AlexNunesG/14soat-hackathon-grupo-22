package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"video-processor/internal/app"
)

var (
	testSecret = []byte("unit-test-secret-of-at-least-32-bytes!")
	testUserID = "0b8f6a2e-4c1d-4f7a-9e3b-2d5c8a1f6e90"
	t0         = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
)

// clock is a settable time source.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newTestJWT(t *testing.T, c *clock) *JWT {
	t.Helper()
	j, err := newJWT(testSecret, time.Hour, c.Now)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// sign builds a token with arbitrary claims, method and key.
func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// validClaims are the claims Issue would produce at t0.
func validClaims() jwt.MapClaims {
	return jwt.MapClaims{"iss": Issuer, "sub": testUserID, "iat": t0.Unix(), "exp": t0.Add(time.Hour).Unix()}
}

func TestNewJWTValidatesSettings(t *testing.T) {
	if _, err := NewJWT([]byte("short"), time.Hour); err == nil {
		t.Error("short secret: want error")
	}
	if _, err := NewJWT(testSecret, 500*time.Millisecond); err == nil {
		t.Error("sub-second ttl: want error")
	}
	if _, err := NewJWT(testSecret, time.Hour); err != nil {
		t.Error(err)
	}
}

func TestIssueAndVerify(t *testing.T) {
	c := &clock{now: t0}
	j := newTestJWT(t, c)
	tok, err := j.Issue(testUserID)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ExpiresIn != time.Hour || strings.Count(tok.Value, ".") != 2 {
		t.Errorf("token %+v", tok)
	}

	// The claims are exactly iss, sub, iat and exp, signed with HS256.
	parsed, _, err := jwt.NewParser().ParseUnverified(tok.Value, jwt.MapClaims{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Method.Alg() != "HS256" {
		t.Errorf("alg %s, want HS256", parsed.Method.Alg())
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if len(claims) != 4 || claims["iss"] != Issuer || claims["sub"] != testUserID ||
		claims["iat"] != float64(t0.Unix()) || claims["exp"] != float64(t0.Add(time.Hour).Unix()) {
		t.Errorf("claims %v", claims)
	}

	c.now = t0.Add(59 * time.Minute)
	got, err := j.Verify(tok.Value)
	if err != nil {
		t.Fatal(err)
	}
	if got != testUserID {
		t.Errorf("subject %q, want %q", got, testUserID)
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	c := &clock{now: t0}
	j := newTestJWT(t, c)
	tok, err := j.Issue(testUserID)
	if err != nil {
		t.Fatal(err)
	}
	c.now = t0.Add(time.Hour + leeway + time.Second)
	_, err = j.Verify(tok.Value)
	if !errors.Is(err, app.ErrInvalidToken) || !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("err = %v, want ErrInvalidToken wrapping ErrTokenExpired", err)
	}
}

func TestVerifyRejectsOtherAlgorithms(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"HS384 with the same secret": sign(t, jwt.SigningMethodHS384, testSecret, validClaims()),
		"HS512 with the same secret": sign(t, jwt.SigningMethodHS512, testSecret, validClaims()),
		"alg none":                   sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims()),
		"RS256":                      sign(t, jwt.SigningMethodRS256, rsaKey, validClaims()),
	}
	j := newTestJWT(t, &clock{now: t0})
	for name, tok := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := j.Verify(tok); !errors.Is(err, app.ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyRejectsInvalidTokens(t *testing.T) {
	good := sign(t, jwt.SigningMethodHS256, testSecret, validClaims())
	parts := strings.Split(good, ".")
	with := func(edit func(jwt.MapClaims)) string {
		c := validClaims()
		edit(c)
		return sign(t, jwt.SigningMethodHS256, testSecret, c)
	}
	tests := map[string]string{
		"empty":             "",
		"not a JWT":         "not-a-jwt",
		"three junk parts":  "a.b.c",
		"other secret":      sign(t, jwt.SigningMethodHS256, []byte("another-secret-of-at-least-32-bytes!!"), validClaims()),
		"tampered payload":  parts[0] + "." + strings.TrimRight(parts[1], "=") + "x." + parts[2],
		"no signature":      parts[0] + "." + parts[1] + ".",
		"wrong issuer":      with(func(c jwt.MapClaims) { c["iss"] = "someone-else" }),
		"no issuer":         with(func(c jwt.MapClaims) { delete(c, "iss") }),
		"no expiry":         with(func(c jwt.MapClaims) { delete(c, "exp") }),
		"issued in future":  with(func(c jwt.MapClaims) { c["iat"] = t0.Add(time.Minute).Unix() }),
		"no subject":        with(func(c jwt.MapClaims) { delete(c, "sub") }),
		"non-UUID subject":  with(func(c jwt.MapClaims) { c["sub"] = "admin" }),
		"not yet valid nbf": with(func(c jwt.MapClaims) { c["nbf"] = t0.Add(time.Minute).Unix() }),
	}
	j := newTestJWT(t, &clock{now: t0})
	if _, err := j.Verify(good); err != nil {
		t.Fatalf("control token rejected: %v", err)
	}
	for name, tok := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := j.Verify(tok); !errors.Is(err, app.ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}
