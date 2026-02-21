package l2redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const defaultRedisPort = "6379"

// NewClient initializes an L2 Redis client and verifies connectivity.
func NewClient(ctx context.Context, host string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     host + ":" + defaultRedisPort,
		DB:       0,
		Password: "",
	})

	if _, err := client.Ping(ctx).Result(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return client, nil
}
