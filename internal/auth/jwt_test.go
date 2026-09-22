package auth

import (
	"errors"
	"testing"
	"time"
)

func TestGenerateAndVerifyToken_RoundTrip(t *testing.T) {
	secret := "test-secret"
	userID := "11111111-1111-1111-1111-111111111111"

	token, expiresAt, err := GenerateToken(secret, userID, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	if token == "" {
		t.Fatal("GenerateToken() returned empty token")
	}

	claims, err := VerifyToken(secret, token)
	if err != nil {
		t.Fatalf("VerifyToken() error = %v", err)
	}
	if claims.Subject != userID {
		t.Errorf("claims.Subject = %q, want %q", claims.Subject, userID)
	}
	// JWT NumericDate claims are second-precision, so compare at that
	// granularity rather than requiring exact nanosecond equality.
	if claims.ExpiresAt.Time.Unix() != expiresAt.Unix() {
		t.Errorf("claims.ExpiresAt = %v, want %v", claims.ExpiresAt.Time, expiresAt)
	}
}

func TestVerifyToken_ExpiredRejected(t *testing.T) {
	secret := "test-secret"

	// Issue a token whose TTL already elapsed, to exercise the exp check.
	token, _, err := GenerateToken(secret, "user-1", -time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	_, err = VerifyToken(secret, token)
	if err == nil {
		t.Fatal("VerifyToken() expected an error for an expired token, got nil")
	}
	if !errors.Is(err, ErrExpiredToken) {
		t.Errorf("VerifyToken() error = %v, want ErrExpiredToken", err)
	}
}

func TestVerifyToken_WrongSecretRejected(t *testing.T) {
	token, _, err := GenerateToken("secret-a", "user-1", time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	_, err = VerifyToken("secret-b", token)
	if err == nil {
		t.Fatal("VerifyToken() expected an error for a token signed with a different secret, got nil")
	}
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("VerifyToken() error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyToken_MalformedRejected(t *testing.T) {
	_, err := VerifyToken("test-secret", "not-a-jwt")
	if err == nil {
		t.Fatal("VerifyToken() expected an error for a malformed token, got nil")
	}
}
