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

	// Parse hash|mobile or hash|mobile|ref
	parts := strings.SplitN(val, "|", 3)
	if len(parts) < 2 {
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

func claimSetKey(hospitalID string) string  { return fmt.Sprintf("claimset:%s", hospitalID) }
func resolveAttemptsKey(h string) string     { return fmt.Sprintf("resolve_attempts:%s", h) }

func (s *redisOTPStore) SaveClaimOTP(ctx context.Context, refID, hash, mobile, ref, hospitalID string, ttl time.Duration) error {
	// otp:{refID} = hash|mobile|ref  (same key the verify path reads)
	if err := s.client.Set(ctx, fmt.Sprintf("otp:%s", refID), fmt.Sprintf("%s|%s|%s", hash, mobile, ref), ttl).Err(); err != nil {
		return fmt.Errorf("repository.SaveClaimOTP: set otp: %w", err)
	}
	setKey := claimSetKey(hospitalID)
	if err := s.client.SAdd(ctx, setKey, refID).Err(); err != nil {
		return fmt.Errorf("repository.SaveClaimOTP: sadd: %w", err)
	}
	// Refresh the set TTL so it never outlives the OTPs it points at by much.
	if err := s.client.Expire(ctx, setKey, ttl).Err(); err != nil {
		return fmt.Errorf("repository.SaveClaimOTP: expire set: %w", err)
	}
	return nil
}

func (s *redisOTPStore) GetClaimOTP(ctx context.Context, refID string) (string, string, string, error) {
	val, err := s.client.Get(ctx, fmt.Sprintf("otp:%s", refID)).Result()
	if err == redis.Nil {
		return "", "", "", nil // expired between SMEMBERS and here
	}
	if err != nil {
		return "", "", "", fmt.Errorf("repository.GetClaimOTP: get: %w", err)
	}
	parts := strings.SplitN(val, "|", 3)
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("repository.GetClaimOTP: bad value")
	}
	ref := ""
	if len(parts) == 3 {
		ref = parts[2]
	}
	return parts[0], parts[1], ref, nil
}

func (s *redisOTPStore) ClaimMembers(ctx context.Context, hospitalID string) ([]string, error) {
	members, err := s.client.SMembers(ctx, claimSetKey(hospitalID)).Result()
	if err != nil {
		return nil, fmt.Errorf("repository.ClaimMembers: %w", err)
	}
	return members, nil
}

func (s *redisOTPStore) RemoveClaim(ctx context.Context, hospitalID, refID string) error {
	return s.client.SRem(ctx, claimSetKey(hospitalID), refID).Err()
}

func (s *redisOTPStore) IncrResolveAttempts(ctx context.Context, hospitalID string, ttl time.Duration) (int64, error) {
	key := resolveAttemptsKey(hospitalID)
	n, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("repository.IncrResolveAttempts: %w", err)
	}
	if n == 1 {
		if err := s.client.Expire(ctx, key, ttl).Err(); err != nil {
			return 0, fmt.Errorf("repository.IncrResolveAttempts: expire: %w", err)
		}
	}
	return n, nil
}
