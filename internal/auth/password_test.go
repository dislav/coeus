package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("mypassword")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "mypassword" {
		t.Fatal("hash should not equal plaintext")
	}
	if !VerifyPassword(hash, "mypassword") {
		t.Error("VerifyPassword should return true for correct password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Error("VerifyPassword should return false for wrong password")
	}
}

func TestHashPasswordUniqueness(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	if h1 == h2 {
		t.Error("hashes should differ (bcrypt salt)")
	}
}

const wantAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*"

func TestGeneratePassword_LengthAndCharset(t *testing.T) {
	pw, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	if len(pw) != 20 {
		t.Errorf("len = %d, want 20", len(pw))
	}
	for i, r := range pw {
		if !strings.ContainsRune(wantAlphabet, r) {
			t.Errorf("char at %d (%q) not in alphabet", i, r)
		}
	}
}

func TestGeneratePassword_NoPanicAndUniform(t *testing.T) {
	seen := make(map[rune]bool)
	for i := 0; i < 5000; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		for _, r := range pw {
			seen[r] = true
		}
	}
	// Uniformity sanity: over a large sample, every alphabet symbol appears.
	for _, r := range wantAlphabet {
		if !seen[r] {
			t.Errorf("alphabet symbol %q never appeared in 5000 samples", r)
		}
	}
}
