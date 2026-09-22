package auth

import "testing"

func TestHashPassword_CheckPassword(t *testing.T) {
	password := "s3cr3t-password"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if hash == "" || hash == password {
		t.Fatalf("HashPassword() returned an invalid hash: %q", hash)
	}

	if !CheckPassword(hash, password) {
		t.Error("CheckPassword() = false, want true for the correct password")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Error("CheckPassword() = true, want false for an incorrect password")
	}
}

func TestHashPassword_DifferentHashesPerCall(t *testing.T) {
	// bcrypt salts each hash, so two hashes of the same password must
	// differ even though both verify correctly.
	password := "s3cr3t-password"

	h1, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	h2, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if h1 == h2 {
		t.Error("HashPassword() produced identical hashes for two calls; expected salted output")
	}
	if !CheckPassword(h1, password) || !CheckPassword(h2, password) {
		t.Error("CheckPassword() failed to verify one of the salted hashes")
	}
}
