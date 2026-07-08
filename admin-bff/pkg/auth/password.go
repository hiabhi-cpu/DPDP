// Package auth handles admin user lookup, password verification, and the
// hospital-token exchange used by the BFF.
package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost matches the hospital API-key hashing cost used elsewhere.
const bcryptCost = 12

// HashPassword returns a bcrypt hash of the plaintext password.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword reports whether plain matches the stored bcrypt hash. It is
// constant-time within bcrypt and never panics on malformed hashes.
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
