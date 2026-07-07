package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hiabhi-cpu/notification-service/pkg/otp/model"
	"github.com/redis/go-redis/v9"
)

// OTPStore handles Redis operations for OTPs and authenticated sessions.
type OTPStore interface {
	SaveOTPHash(ctx context.Context, refID string, hash string, mobile string, ttl time.Duration) error
	GetOTPHash(ctx context.Context, refID string) (hash string, mobile string, err error)
	DeleteOTP(ctx context.Context, refID string) error

	SaveSession(ctx context.Context, sessionID string, state model.SessionState, ttl time.Duration) error
	// GetSession returns the verified-OTP session, or nil if missing/expired.
	GetSession(ctx context.Context, sessionID string) (*model.SessionState, error)

	// IncrVerifyAttempts counts verification attempts for one reference ID so a
	// 6-digit OTP cannot be brute-forced. The counter shares the OTP's lifetime.
	IncrVerifyAttempts(ctx context.Context, refID string, ttl time.Duration) (int64, error)
	// AcquireSendCooldown returns false while the per-mobile resend cooldown is
	// still running (SET NX), true when this send may proceed.
	AcquireSendCooldown(ctx context.Context, mobile string, ttl time.Duration) (bool, error)
	// IncrHourlySends counts sends per mobile in a rolling hour — the guard
	// against SMS-pumping cost attacks.
	IncrHourlySends(ctx context.Context, mobile string) (int64, error)
}

type redisOTPStore struct {
	client *redis.Client
}

// NewRedisStore creates a new Redis-backed OTP store.
func NewRedisStore(client *redis.Client) OTPStore {
	return &redisOTPStore{client: client}
}

func (s *redisOTPStore) SaveOTPHash(ctx context.Context, refID string, hash string, mobile string, ttl time.Duration) error {
	key := fmt.Sprintf("otp:%s", refID)
	// We store both hash and mobile so we can verify the mobile number matches at verification time.
	val := fmt.Sprintf("%s|%s", hash, mobile)
	
	if err := s.client.Set(ctx, key, val, ttl).Err(); err != nil {
		return fmt.Errorf("repository.SaveOTPHash: redis set failed: %w", err)
	}
	return nil
}

func (s *redisOTPStore) GetOTPHash(ctx context.Context, refID string) (string, string, error) {
	key := fmt.Sprintf("otp:%s", refID)
	val, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return "", "", fmt.Errorf("otp not found or expired")
		}
		return "", "", fmt.Errorf("repository.GetOTPHash: redis get failed: %w", err)
	}

	// Parse hash|mobile
	parts := strings.SplitN(val, "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("repository.GetOTPHash: invalid data format in redis")
	}

	return parts[0], parts[1], nil
}

func (s *redisOTPStore) DeleteOTP(ctx context.Context, refID string) error {
	key := fmt.Sprintf("otp:%s", refID)
	return s.client.Del(ctx, key).Err()
}

func (s *redisOTPStore) SaveSession(ctx context.Context, sessionID string, state model.SessionState, ttl time.Duration) error {
	key := fmt.Sprintf("session:%s", sessionID)
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("repository.SaveSession: json marshal failed: %w", err)
	}

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("repository.SaveSession: redis set failed: %w", err)
	}
	return nil
}

func (s *redisOTPStore) GetSession(ctx context.Context, sessionID string) (*model.SessionState, error) {
	key := fmt.Sprintf("session:%s", sessionID)
	val, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("repository.GetSession: redis get failed: %w", err)
	}

	var state model.SessionState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, fmt.Errorf("repository.GetSession: json unmarshal failed: %w", err)
	}
	return &state, nil
}

func (s *redisOTPStore) IncrVerifyAttempts(ctx context.Context, refID string, ttl time.Duration) (int64, error) {
	key := fmt.Sprintf("otp_attempts:%s", refID)
	n, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("repository.IncrVerifyAttempts: redis incr failed: %w", err)
	}
	if n == 1 {
		// First attempt sets the window; the counter dies with the OTP.
		if err := s.client.Expire(ctx, key, ttl).Err(); err != nil {
			return n, fmt.Errorf("repository.IncrVerifyAttempts: redis expire failed: %w", err)
		}
	}
	return n, nil
}

func (s *redisOTPStore) AcquireSendCooldown(ctx context.Context, mobile string, ttl time.Duration) (bool, error) {
	key := fmt.Sprintf("otp_cooldown:%s", mobile)
	ok, err := s.client.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("repository.AcquireSendCooldown: redis setnx failed: %w", err)
	}
	return ok, nil
}

func (s *redisOTPStore) IncrHourlySends(ctx context.Context, mobile string) (int64, error) {
	key := fmt.Sprintf("otp_sends:%s", mobile)
	n, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("repository.IncrHourlySends: redis incr failed: %w", err)
	}
	if n == 1 {
		if err := s.client.Expire(ctx, key, time.Hour).Err(); err != nil {
			return n, fmt.Errorf("repository.IncrHourlySends: redis expire failed: %w", err)
		}
	}
	return n, nil
}
