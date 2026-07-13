package bootstrap

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewRedis creates a Redis client and verifies connectivity.
func NewRedis(ctx context.Context, redisURL string) *redis.Client {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: failed to parse REDIS_URL: %v", err))
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		panic(fmt.Sprintf("bootstrap: Redis ping failed: %v", err))
	}
	return client
}
