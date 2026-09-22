// Package auth provides password hashing and JWT sign/verify helpers.
//
// The JWT helpers in particular are written to be reusable beyond
// auth-service: video-service's auth middleware (added in a later
// delivery) verifies tokens signed here using the same shared JWT_SECRET,
// since both services sit inside the same trust boundary (see
// .ai-agents/PLAN.md).
package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword hashes a plaintext password using bcrypt with the default
// cost factor.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword reports whether the plaintext password matches the given
// bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
