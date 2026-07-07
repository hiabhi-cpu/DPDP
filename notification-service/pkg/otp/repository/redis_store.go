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
