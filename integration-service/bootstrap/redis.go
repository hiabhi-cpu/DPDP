package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedis creates a Redis client and verifies connectivity with a bounded ping.
func NewRedis(ctx context.Context, redisURL string) *redis.Client {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: failed to parse REDIS_URL: %v", err))
	}
	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		panic(fmt.Sprintf("bootstrap: Redis ping failed: %v", err))
	}
	return client
}
