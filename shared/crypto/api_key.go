package crypto

import (
	"crypto/rand"
	"encoding/base64"

	"golang.org/x/crypto/bcrypt"
)

const (
	// APIKeyCost is the bcrypt cost for hospital API keys.
	// Cost 12 is intentionally high — API key verification happens once per JWT issuance,
	// not on every request, so the latency cost is acceptable.
	APIKeyCost = 12

	// APIKeyLength is the number of random bytes used to generate a new API key.
	// 32 bytes = 256 bits of entropy — sufficient for a long-lived API key.
	APIKeyLength = 32
)

// GenerateAPIKey creates a new cryptographically random API key.
// Returns the raw key (to be shown to the hospital admin ONCE) and its bcrypt hash
// (to be stored in the database). The raw key is never stored anywhere.
func GenerateAPIKey() (raw string, hash string, err error) {
	b := make([]byte, APIKeyLength)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}

	// base64 URL encoding for a human-copyable key (no +/= chars)
	raw = base64.URLEncoding.EncodeToString(b)

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(raw), APIKeyCost)
	if err != nil {
		return "", "", err
	}

	return raw, string(hashBytes), nil
}

// HashAPIKey produces a bcrypt hash of an existing raw API key string.
// Used when onboarding a hospital with a predetermined key (e.g., the dev seed).
func HashAPIKey(raw string) (string, error) {
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(raw), APIKeyCost)
	if err != nil {
		return "", err
	}
	return string(hashBytes), nil
}

// VerifyAPIKey checks a raw API key against a stored bcrypt hash.
// Returns true if the key matches, false otherwise.
// Timing-safe — bcrypt.CompareHashAndPassword is constant-time.
func VerifyAPIKey(raw, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(raw))
	return err == nil
}
