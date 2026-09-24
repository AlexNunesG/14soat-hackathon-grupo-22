// Package auth holds the credential adapters of the api: bcrypt password
// hashing (app.PasswordHasher) and HS256 JWT access tokens
// (app.TokenIssuer, app.TokenVerifier).
package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// DefaultBcryptCost is the bcrypt work factor used by the api: about
// 0.2-0.3 s per hash on current hardware, above bcrypt.DefaultCost (10).
const DefaultBcryptCost = 12

// Bcrypt hashes passwords with bcrypt.
type Bcrypt struct {
	cost int
}

// NewBcrypt returns a hasher with the given cost, which must be within
// [bcrypt.MinCost, bcrypt.MaxCost].
func NewBcrypt(cost int) (*Bcrypt, error) {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return nil, fmt.Errorf("bcrypt: cost %d outside [%d, %d]", cost, bcrypt.MinCost, bcrypt.MaxCost)
	}
	return &Bcrypt{cost: cost}, nil
}

// Hash returns the bcrypt hash of password (salted, with the cost encoded).
func (b *Bcrypt) Hash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), b.cost)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}
	return string(h), nil
}

// Compare returns nil when password matches hash.
func (b *Bcrypt) Compare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
