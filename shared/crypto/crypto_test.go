package crypto_test

import (
	"strings"
	"testing"

	"github.com/hiabhi-cpu/DPDP/shared/crypto"
)

// ── Patient Key Tests ─────────────────────────────────────────────────────────

func TestComputePatientKey_Deterministic(t *testing.T) {
	mobile := "9820123456"
	salt := "test-system-salt"
	hospitalKey := "hospital-A-secret"

	key1 := crypto.ComputePatientKey(mobile, salt, hospitalKey)
	key2 := crypto.ComputePatientKey(mobile, salt, hospitalKey)

	if key1 != key2 {
		t.Fatalf("expected deterministic output, got %q and %q", key1, key2)
	}
}

func TestComputePatientKey_HasVersionPrefix(t *testing.T) {
	key := crypto.ComputePatientKey("9820123456", "salt", "hosp-key")
	if !strings.HasPrefix(key, "v1|") {
		t.Fatalf("expected key to start with 'v1|', got %q", key)
	}
}

// CRITICAL: The same patient at two different hospitals MUST produce different hashes.
// This is the cryptographic enforcement of the data silo guarantee.
func TestComputePatientKey_DifferentHospitalsDifferentHashes(t *testing.T) {
	mobile := "9820123456"
	salt := "shared-system-salt"

	keyA := crypto.ComputePatientKey(mobile, salt, "hospital-A-key")
	keyB := crypto.ComputePatientKey(mobile, salt, "hospital-B-key")

	if keyA == keyB {
		t.Fatal("CRITICAL FAILURE: same patient at different hospitals produced identical hashes — data silo is broken")
	}
}

// Different mobiles at the same hospital must produce different hashes.
func TestComputePatientKey_DifferentMobilesDifferentHashes(t *testing.T) {
	salt := "salt"
	hospKey := "hospital-key"

	key1 := crypto.ComputePatientKey("9820111111", salt, hospKey)
	key2 := crypto.ComputePatientKey("9820222222", salt, hospKey)

	if key1 == key2 {
		t.Fatal("different mobiles produced the same patient key")
	}
}

// ── API Key Tests ─────────────────────────────────────────────────────────────

func TestGenerateAPIKey_UniqueEachTime(t *testing.T) {
	raw1, hash1, err := crypto.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	raw2, hash2, err := crypto.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}

	if raw1 == raw2 {
		t.Fatal("generated duplicate API keys")
	}
	if hash1 == hash2 {
		t.Fatal("generated duplicate API key hashes")
	}
}

func TestVerifyAPIKey_CorrectKey(t *testing.T) {
	raw, hash, err := crypto.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.VerifyAPIKey(raw, hash) {
		t.Fatal("VerifyAPIKey returned false for a valid key")
	}
}

func TestVerifyAPIKey_WrongKey(t *testing.T) {
	_, hash, err := crypto.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if crypto.VerifyAPIKey("wrong-key", hash) {
		t.Fatal("VerifyAPIKey returned true for an invalid key")
	}
}

// ── OTP Tests ─────────────────────────────────────────────────────────────────

func TestGenerateOTP_IsExactlySixDigits(t *testing.T) {
	for i := 0; i < 100; i++ {
		otp, err := crypto.GenerateOTP()
		if err != nil {
			t.Fatalf("GenerateOTP error: %v", err)
		}
		if len(otp) != 6 {
			t.Fatalf("expected 6-digit OTP, got %q (len=%d)", otp, len(otp))
		}
		for _, c := range otp {
			if c < '0' || c > '9' {
				t.Fatalf("OTP contains non-digit character: %q", otp)
			}
		}
	}
}

func TestGenerateOTP_NotAlwaysSame(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		otp, _ := crypto.GenerateOTP()
		seen[otp] = true
	}
	// With 6 digits (1M possibilities) and 20 iterations, getting all identical is ~impossible
	if len(seen) == 1 {
		t.Fatal("OTP generator appears to be returning the same value every time")
	}
}
