package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

// PendingTTL is how long a pre-staged registration survives if unused.
const PendingTTL = 72 * time.Hour

// RedisStore persists PendingRegistration records under
// pending:{hospital_id}:{hms_patient_id} with a TTL.
type RedisStore struct {
	client *redis.Client
}

func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func key(hospitalID, hmsPatientID string) string {
	return fmt.Sprintf("pending:%s:%s", hospitalID, hmsPatientID)
}

// Upsert stores (or overwrites) a record with the standard TTL. Idempotent.
func (s *RedisStore) Upsert(ctx context.Context, reg model.PendingRegistration) error {
	b, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("repository.Upsert: marshal: %w", err)
	}
	if err := s.client.Set(ctx, key(reg.HospitalID, reg.HMSPatientID), b, PendingTTL).Err(); err != nil {
		return fmt.Errorf("repository.Upsert: redis set: %w", err)
	}
	return nil
}

// Get returns the record, or (nil, nil) if it is absent or expired.
func (s *RedisStore) Get(ctx context.Context, hospitalID, hmsPatientID string) (*model.PendingRegistration, error) {
	val, err := s.client.Get(ctx, key(hospitalID, hmsPatientID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("repository.Get: redis get: %w", err)
	}
	var reg model.PendingRegistration
	if err := json.Unmarshal([]byte(val), &reg); err != nil {
		return nil, fmt.Errorf("repository.Get: unmarshal: %w", err)
	}
	return &reg, nil
}

// List returns all pending records for one hospital.
// ponytail: SCAN over a per-hospital prefix. If a single hospital ever holds
// thousands of concurrent pending records, add a per-hospital index set (SADD)
// — unneeded at pilot scale (a few live registrations at a time).
func (s *RedisStore) List(ctx context.Context, hospitalID string) ([]model.PendingRegistration, error) {
	pattern := fmt.Sprintf("pending:%s:*", hospitalID)
	var out []model.PendingRegistration
	iter := s.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		val, err := s.client.Get(ctx, iter.Val()).Result()
		if err == redis.Nil {
			continue // expired between SCAN and GET
		}
		if err != nil {
			return nil, fmt.Errorf("repository.List: redis get: %w", err)
		}
		var reg model.PendingRegistration
		if err := json.Unmarshal([]byte(val), &reg); err != nil {
			return nil, fmt.Errorf("repository.List: unmarshal: %w", err)
		}
		out = append(out, reg)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("repository.List: scan: %w", err)
	}
	return out, nil
}
