package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"golang.org/x/crypto/bcrypt"
)

const (
	generatedPasswordLength = 20
	// [A-Za-z0-9!@#$%^&*] — 70 symbols, ~122 bits of entropy at length 20.
	passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*"
)

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// GeneratePassword returns a bias-free 20-character password drawn uniformly
// from passwordAlphabet using crypto/rand (via math/big modulo).
func GeneratePassword() (string, error) {
	n := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, generatedPasswordLength)
	for i := range out {
		idx, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = passwordAlphabet[idx.Int64()]
	}
	return string(out), nil
}
