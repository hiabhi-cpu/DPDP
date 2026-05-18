package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateOTP creates a cryptographically random 6-digit OTP string.
// Uses crypto/rand (not math/rand) for security.
func GenerateOTP() (string, error) {
	// Generate 3 random bytes → convert to uint32 → take mod 1_000_000 for 6 digits
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("otp generation failed: %w", err)
	}

	// Combine 3 bytes into an integer, then take modulo for 6 digits
	n := (uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])) % 1_000_000
	return fmt.Sprintf("%06d", n), nil
}

// GenerateRequestID creates a random 16-byte hex string for use as a trace/request ID.
func GenerateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck — rand.Read never returns an error on non-empty slice
	return hex.EncodeToString(b)
}
