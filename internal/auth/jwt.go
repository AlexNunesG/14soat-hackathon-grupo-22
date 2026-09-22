package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned for any structurally or cryptographically
// invalid token (bad signature, malformed, wrong algorithm, ...).
var ErrInvalidToken = errors.New("auth: invalid token")

// ErrExpiredToken is returned specifically when the token's exp claim has
// passed, so callers can distinguish "expired" from "otherwise invalid" if
// they want to.
var ErrExpiredToken = errors.New("auth: token expired")

// Claims is the JWT payload shared across services. The user id is carried
// in the standard `sub` claim.
type Claims struct {
	jwt.RegisteredClaims
}

// GenerateToken issues an HS256-signed JWT for the given user id, valid for
// ttl starting now. It returns the signed token and its absolute expiry
// time.
func GenerateToken(secret, userID string, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	now := time.Now()
	expiresAt = now.Add(ttl)

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token, err = t.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// VerifyToken parses and validates an HS256 JWT signed with secret,
// returning its claims. It rejects tokens signed with any other algorithm,
// tokens with an invalid signature, and expired tokens.
//
// Exported so other services (e.g. video-service's auth middleware) can
// reuse it directly against the same shared JWT_SECRET, without an
// auth-service-specific dependency.
func VerifyToken(secret, tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return []byte(secret), nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}

	if !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
