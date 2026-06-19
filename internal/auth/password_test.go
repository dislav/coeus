package auth

import "testing"

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
