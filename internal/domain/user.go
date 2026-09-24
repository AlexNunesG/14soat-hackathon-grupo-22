package domain

import (
	"strings"
	"time"
)

// User is an account that owns videos. The password is only ever kept as a
// hash.
type User struct {
	ID           string
	Name         string
	Email        string // lowercased, unique
	PasswordHash string
	CreatedAt    time.Time
}

// NormalizeEmail returns the canonical form of an e-mail address: trimmed
// and lowercased. E-mails are unique case-insensitively.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
