package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// OTP TTL per product plan: 5 minutes
	OTPExpiry = 5 * time.Minute
	// Maximum OTP verification attempts before lockout
	MaxAttempts = 3
)

// OTPRecord is the data stored in Redis for each active OTP session.
type OTPRecord struct {
	OTPHash    string    `json:"otp_hash"`    // bcrypt(raw_otp)
	Attempts   int       `json:"attempts"`
	Purpose    string    `json:"purpose"`     // CONSENT | WITHDRAWAL | PATIENT_PORTAL_LOGIN
	ExpiresAt  time.Time `json:"expires_at"`
}

// RedisOTPStore manages OTP state in Redis.
// Redis key format: "otp:{hospital_id}:{mobile_hash}"
// The mobile_hash ensures raw mobile numbers never appear in Redis keys.
type RedisOTPStore struct {
	client *redis.Client
}

// NewRedisOTPStore creates a RedisOTPStore.
func NewRedisOTPStore(client *redis.Client) *RedisOTPStore {
	return &RedisOTPStore{client: client}
}

func redisKey(hospitalID, mobileHash string) string {
	return fmt.Sprintf("otp:%s:%s", hospitalID, mobileHash)
}

// Store saves an OTP record with a 5-minute TTL.
func (s *RedisOTPStore) Store(ctx context.Context, hospitalID, mobileHash, otpHash, purpose string) error {
	key := redisKey(hospitalID, mobileHash)

	// Store as a Redis hash for atomic field updates
	pipe := s.client.Pipeline()
	pipe.HSet(ctx, key,
		"otp_hash", otpHash,
		"attempts", 0,
		"purpose", purpose,
		"expires_at", time.Now().Add(OTPExpiry).Unix(),
	)
	pipe.Expire(ctx, key, OTPExpiry)

	_, err := pipe.Exec(ctx)
	return err
}

// GetRecord retrieves the OTP record for a mobile+hospital pair.
// Returns nil (not an error) if the key doesn't exist or has expired.
func (s *RedisOTPStore) GetRecord(ctx context.Context, hospitalID, mobileHash string) (*OTPRecord, error) {
	key := redisKey(hospitalID, mobileHash)

	vals, err := s.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, nil // expired or not found
	}

	record := &OTPRecord{
		OTPHash: vals["otp_hash"],
		Purpose: vals["purpose"],
	}
	fmt.Sscanf(vals["attempts"], "%d", &record.Attempts)

	return record, nil
}

// IncrementAttempts atomically increments the attempt counter.
// Returns the new attempt count.
func (s *RedisOTPStore) IncrementAttempts(ctx context.Context, hospitalID, mobileHash string) (int, error) {
	key := redisKey(hospitalID, mobileHash)
	count, err := s.client.HIncrBy(ctx, key, "attempts", 1).Result()
	return int(count), err
}

// MarkUsed deletes the OTP key after successful verification (prevents replay).
func (s *RedisOTPStore) MarkUsed(ctx context.Context, hospitalID, mobileHash string) error {
	key := redisKey(hospitalID, mobileHash)
	return s.client.Del(ctx, key).Err()
}
