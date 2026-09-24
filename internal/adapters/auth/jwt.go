package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"video-processor/internal/app"
)

// Issuer is the "iss" claim of the api's access tokens.
const Issuer = "video-processor-api"

// MinSecretLength is the minimum length in bytes of the HS256 key (the
// size of the SHA-256 output, as RFC 7518 §3.2 requires).
const MinSecretLength = 32

// leeway tolerates clock skew between api replicas when checking exp/iat.
const leeway = 5 * time.Second

// JWT issues and verifies HS256 access tokens. The claims are iss, sub
// (the user id), iat and exp; verification accepts only HS256 tokens from
// Issuer with a UUID subject that have not expired.
type JWT struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
	parser *jwt.Parser
}

// NewJWT returns the token adapter. secret must have at least
// MinSecretLength bytes and ttl must be at least one second.
func NewJWT(secret []byte, ttl time.Duration) (*JWT, error) {
	return newJWT(secret, ttl, time.Now)
}

func newJWT(secret []byte, ttl time.Duration, now func() time.Time) (*JWT, error) {
	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("jwt: secret has %d bytes, want at least %d", len(secret), MinSecretLength)
	}
	if ttl < time.Second {
		return nil, fmt.Errorf("jwt: ttl %s, want at least 1s", ttl)
	}
	return &JWT{
		secret: append([]byte(nil), secret...),
		ttl:    ttl,
		now:    now,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(Issuer),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(leeway),
			jwt.WithTimeFunc(now),
		),
	}, nil
}

// Issue returns a token for userID that expires after the configured TTL.
func (j *JWT) Issue(userID string) (app.AccessToken, error) {
	now := j.now()
	claims := jwt.RegisteredClaims{
		Issuer:    Issuer,
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(j.ttl)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(j.secret)
	if err != nil {
		return app.AccessToken{}, fmt.Errorf("jwt: sign: %w", err)
	}
	return app.AccessToken{Value: signed, ExpiresIn: j.ttl}, nil
}

// Verify returns the user id (sub) of a valid token. Any other token
// yields an error wrapping app.ErrInvalidToken.
func (j *JWT) Verify(token string) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := j.parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		// WithValidMethods already restricts alg to HS256; checking the
		// method type too keeps the key from ever being used with another
		// algorithm family.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", app.ErrInvalidToken, err)
	}
	if len(claims.Subject) != 36 || uuid.Validate(claims.Subject) != nil {
		return "", fmt.Errorf("%w: subject is not a user id", app.ErrInvalidToken)
	}
	return claims.Subject, nil
}
