// Package session stores authenticated admin sessions server-side. The browser
// holds only an opaque session id (in an HttpOnly cookie); all identity lives here.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound means no live session exists for the given id.
var ErrNotFound = errors.New("session not found")

// Session is the identity attached to a logged-in admin.
type Session struct {
	UserID     string `json:"user_id"`
	Email      string `json:"email"`
	Role       string `json:"role"`
	HospitalID string `json:"hospital_id"`
}

// Store persists sessions keyed by an opaque id.
type Store interface {
	Create(ctx context.Context, s Session) (id string, err error)
	Get(ctx context.Context, id string) (Session, error)
	Delete(ctx context.Context, id string) error
}

// newID returns a 256-bit cryptographically-random opaque session id.
func newID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session: rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ── Redis implementation ─────────────────────────────────────────────────────

const redisPrefix = "admin_sess:"

type redisStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisStore returns a Redis-backed Store with the given session TTL.
func NewRedisStore(rdb *redis.Client, ttl time.Duration) Store {
	return &redisStore{rdb: rdb, ttl: ttl}
}

func (r *redisStore) Create(ctx context.Context, s Session) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("session: marshal: %w", err)
	}
	if err := r.rdb.Set(ctx, redisPrefix+id, payload, r.ttl).Err(); err != nil {
		return "", fmt.Errorf("session: redis set: %w", err)
	}
	return id, nil
}

func (r *redisStore) Get(ctx context.Context, id string) (Session, error) {
	payload, err := r.rdb.Get(ctx, redisPrefix+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("session: redis get: %w", err)
	}
	var s Session
	if err := json.Unmarshal(payload, &s); err != nil {
		return Session{}, fmt.Errorf("session: unmarshal: %w", err)
	}
	return s, nil
}

func (r *redisStore) Delete(ctx context.Context, id string) error {
	if err := r.rdb.Del(ctx, redisPrefix+id).Err(); err != nil {
		return fmt.Errorf("session: redis del: %w", err)
	}
	return nil
}

// ── In-memory implementation (tests / single-instance dev) ───────────────────

type memStore struct {
	mu sync.RWMutex
	m  map[string]Session
}

// NewMemStore returns an in-memory Store. Not for multi-instance production use.
func NewMemStore() Store {
	return &memStore{m: make(map[string]Session)}
}

func (s *memStore) Create(_ context.Context, sess Session) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.m[id] = sess
	s.mu.Unlock()
	return id, nil
}

func (s *memStore) Get(_ context.Context, id string) (Session, error) {
	s.mu.RLock()
	sess, ok := s.m[id]
	s.mu.RUnlock()
	if !ok {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

func (s *memStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
	return nil
}
