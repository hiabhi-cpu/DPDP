package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const hashVersion = "v1"

// ComputePatientKey returns a versioned HMAC-SHA256 hash of the patient's mobile number.
//
// Formula: HMAC_SHA256(mobile + SYSTEM_SALT + hospitalKey)
// Output format: "v1|<64-char hex string>"
//
// Why versioned:
//   The "v1|" prefix supports future key rotation (see product plan §21 Q10).
//   When a hospital key is rotated, the app can detect which version of hash is stored
//   and re-hash using the correct key during the next patient interaction (lazy migration).
//
// Critical rules:
//   - The raw mobile number MUST be discarded immediately after calling this function.
//   - NEVER log, store, or transmit the raw mobile number.
//   - The same patient at a different hospital produces a COMPLETELY different hash
//     because each hospital has a unique hospitalKey — this is the data silo guarantee.
func ComputePatientKey(mobile, systemSalt, hospitalKey string) string {
	// Key = SYSTEM_SALT + hospital_key concatenated
	// This ensures both the global salt AND hospital-specific key protect the hash
	key := []byte(systemSalt + hospitalKey)

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(mobile))
	hash := hex.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%s|%s", hashVersion, hash)
}

// ExtractHashVersion returns the version prefix from a stored patient key.
// Returns "" if the key has no version prefix (legacy / malformed).
func ExtractHashVersion(patientKey string) string {
	if len(patientKey) > 3 && patientKey[2] == '|' {
		return patientKey[:2]
	}
	return ""
}
