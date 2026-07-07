// Package crypto provides cryptographic utilities for the DPDP Consent Manager.
// All patient identity operations flow through this package — raw mobile numbers
// must never leave this package.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// PatientKeyVersion is the key-version tag prepended to every patient key.
// It records which generation of hospital key produced the hash, so that key
// rotation can later identify which key to use for verification (see the key
// rotation guide, §4A "Versioned Hashing"). Bump this when the key-derivation
// scheme or a rotated key changes.
const PatientKeyVersion = "v1"

// patientKeySeparator delimits the version tag from the hash: "v1|<hash>".
const patientKeySeparator = "|"

// ComputePatientKey generates a hospital-scoped, double-keyed HMAC-SHA256 hash
// of the patient's mobile number, prefixed with the key version.
//
// The result has the form "v1|<64-char hex>" — the version prefix lets rotation
// logic tell which hospital-key generation produced the hash without needing the
// raw mobile. This is why consent_vault.patient_key is VARCHAR(72), not (64).
//
// The same mobile number at different hospitals produces completely different
// hashes because each hospital has a unique hospitalKey — this is the
// cryptographic enforcement of the data silo architecture.
//
// The raw mobile number is NOT stored anywhere and should be discarded by the
// caller immediately after this function returns.
func ComputePatientKey(mobile, systemSalt, hospitalKey string) string {
	// Inner HMAC: HMAC_SHA256(mobile, systemSalt)
	inner := hmacSHA256(mobile, systemSalt)
	// Outer HMAC: HMAC_SHA256(inner, hospitalKey) — hospital-scoped
	hash := hmacSHA256(inner, hospitalKey)
	return PatientKeyVersion + patientKeySeparator + hash
}

// GenerateOTP generates a cryptographically random 6-digit OTP code.
// Uses crypto/rand — NOT math/rand — to ensure unpredictability.
func GenerateOTP() (string, error) {
	// Generate a random number in [0, 1_000_000)
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("crypto.GenerateOTP: failed to generate random number: %w", err)
	}
	// Zero-pad to exactly 6 digits
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// HashOTP creates a bcrypt hash of the OTP for safe storage in Redis.
// The raw OTP is never stored — only this hash.
func HashOTP(otp string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(otp), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("crypto.HashOTP: bcrypt failed: %w", err)
	}
	return string(hash), nil
}

// VerifyOTP performs a timing-safe comparison of a raw OTP against its bcrypt hash.
// Returns true only if they match.
func VerifyOTP(rawOTP, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(rawOTP))
	return err == nil
}

// HashAPIKey creates a fast, deterministic SHA-256 hash of a raw hospital API key.
// Used for O(1) DB lookups since API keys have high entropy.
func HashAPIKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}

// VerifyAPIKey compares a raw key against a stored hash in constant time, so
// the comparison itself cannot leak hash prefixes via timing. Kept for
// backwards compatibility; the main login path re-hashes and looks up the DB.
func VerifyAPIKey(rawKey, hash string) bool {
	computed := HashAPIKey(rawKey)
	return subtle.ConstantTimeCompare([]byte(computed), []byte(hash)) == 1
}

// ComputeArtifactHash generates a SHA-256 tamper-evident hash of a consent
// artifact. All key fields are concatenated with a pipe separator and hashed.
// This hash is stored alongside the artifact — any modification to the fields
// will produce a different hash, detecting tampering.
func ComputeArtifactHash(fields ...string) string {
	combined := strings.Join(fields, "|")
	sum := sha256.Sum256([]byte(combined))
	return hex.EncodeToString(sum[:])
}

// hmacSHA256 is a private helper that computes HMAC-SHA256(message, key)
// and returns the result as a lowercase hex string.
func hmacSHA256(message, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
