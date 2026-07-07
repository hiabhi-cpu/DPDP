package crypto

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputePatientKey(t *testing.T) {
	mobile := "9820123456"
	systemSalt := "test-system-salt"

	t.Run("same inputs produce same key", func(t *testing.T) {
		key1 := ComputePatientKey(mobile, systemSalt, "hospital-key-A")
		key2 := ComputePatientKey(mobile, systemSalt, "hospital-key-A")
		assert.Equal(t, key1, key2)
	})

	t.Run("different hospital keys produce different patient keys", func(t *testing.T) {
		keyA := ComputePatientKey(mobile, systemSalt, "hospital-key-A")
		keyB := ComputePatientKey(mobile, systemSalt, "hospital-key-B")
		assert.NotEqual(t, keyA, keyB, "same patient at different hospitals must produce different keys")
	})

	t.Run("output is versioned: v1|<64-char hex>", func(t *testing.T) {
		key := ComputePatientKey(mobile, systemSalt, "hospital-key-A")
		prefix := PatientKeyVersion + "|"
		assert.True(t, strings.HasPrefix(key, prefix), "key must carry the version prefix")
		assert.Len(t, key, len(prefix)+64, "hash portion must be 64-char SHA256 hex")
	})

	t.Run("raw mobile not present in output", func(t *testing.T) {
		key := ComputePatientKey(mobile, systemSalt, "hospital-key-A")
		assert.NotContains(t, key, mobile)
	})
}

func TestGenerateOTP(t *testing.T) {
	t.Run("generates 6-digit string", func(t *testing.T) {
		otp, err := GenerateOTP()
		require.NoError(t, err)
		assert.Len(t, otp, 6)
	})

	t.Run("generated OTPs are different", func(t *testing.T) {
		otps := make(map[string]bool)
		for i := 0; i < 10; i++ {
			otp, err := GenerateOTP()
			require.NoError(t, err)
			otps[otp] = true
		}
		// Extremely unlikely all 10 are the same
		assert.Greater(t, len(otps), 1)
	})
}

func TestHashAndVerifyOTP(t *testing.T) {
	otp := "482910"

	t.Run("correct OTP verifies", func(t *testing.T) {
		hash, err := HashOTP(otp)
		require.NoError(t, err)
		assert.True(t, VerifyOTP(otp, hash))
	})

	t.Run("wrong OTP does not verify", func(t *testing.T) {
		hash, err := HashOTP(otp)
		require.NoError(t, err)
		assert.False(t, VerifyOTP("000000", hash))
	})
}

func TestHashAndVerifyAPIKey(t *testing.T) {
	rawKey := "hospital-api-key-secret"

	t.Run("correct key verifies", func(t *testing.T) {
		hash := HashAPIKey(rawKey)
		assert.True(t, VerifyAPIKey(rawKey, hash))
	})

	t.Run("wrong key does not verify", func(t *testing.T) {
		hash := HashAPIKey(rawKey)
		assert.False(t, VerifyAPIKey("wrong-key", hash))
	})
}

func TestComputeArtifactHash(t *testing.T) {
	t.Run("same fields produce same hash", func(t *testing.T) {
		h1 := ComputeArtifactHash("uuid-1", "hospital-1", "patient-key-1", "treatment")
		h2 := ComputeArtifactHash("uuid-1", "hospital-1", "patient-key-1", "treatment")
		assert.Equal(t, h1, h2)
	})

	t.Run("changed field produces different hash", func(t *testing.T) {
		h1 := ComputeArtifactHash("uuid-1", "hospital-1", "patient-key-1", "treatment")
		h2 := ComputeArtifactHash("uuid-1", "hospital-1", "patient-key-1", "research")
		assert.NotEqual(t, h1, h2)
	})

	t.Run("output is 64-char hex string", func(t *testing.T) {
		h := ComputeArtifactHash("a", "b", "c")
		assert.Len(t, h, 64)
	})
}
