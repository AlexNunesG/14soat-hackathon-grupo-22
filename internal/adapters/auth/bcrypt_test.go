package auth

import (
	"strings"
	"testing"
)

func TestBcrypt(t *testing.T) {
	if _, err := NewBcrypt(3); err == nil {
		t.Error("cost 3: want error")
	}
	if _, err := NewBcrypt(32); err == nil {
		t.Error("cost 32: want error")
	}
	b, err := NewBcrypt(4) // bcrypt.MinCost keeps the test fast
	if err != nil {
		t.Fatal(err)
	}
	hash, err := b.Hash("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "correct-horse") || !strings.HasPrefix(hash, "$2a$04$") {
		t.Errorf("hash %q", hash)
	}
	if again, _ := b.Hash("correct-horse"); again == hash {
		t.Error("two hashes of the same password are equal: not salted")
	}
	if err := b.Compare(hash, "correct-horse"); err != nil {
		t.Errorf("right password: %v", err)
	}
	if err := b.Compare(hash, "wrong-horse"); err == nil {
		t.Error("wrong password: want error")
	}
	if err := b.Compare("not-a-hash", "correct-horse"); err == nil {
		t.Error("malformed hash: want error")
	}
	if _, err := b.Hash(strings.Repeat("p", 73)); err == nil {
		t.Error("password over 72 bytes: want error")
	}
}
