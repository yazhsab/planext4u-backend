//go:build integration

package gateway

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisLimiterAgainstLocalDependency(t *testing.T) {
	redisURL := os.Getenv("REDIS_TEST_URL")
	if redisURL == "" {
		t.Skip("REDIS_TEST_URL is not configured")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse REDIS_TEST_URL: %v", err)
	}
	client := redis.NewClient(options)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping Redis: %v", err)
	}
	prefix := fmt.Sprintf("planext4u:integration:gateway:%d", time.Now().UnixNano())
	key := "synthetic-rate-key"
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupContext, prefix+":"+key).Err()
	})
	limiter, err := NewRedisLimiter(client, prefix, 2, time.Minute)
	if err != nil {
		t.Fatalf("NewRedisLimiter() error = %v", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		allowed, retryAfter, err := limiter.Allow(ctx, key)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if attempt <= 2 && !allowed {
			t.Fatalf("attempt %d was unexpectedly denied", attempt)
		}
		if attempt == 3 && (allowed || retryAfter <= 0 || retryAfter > time.Minute) {
			t.Fatalf("attempt 3 = allowed %v, retry %v", allowed, retryAfter)
		}
	}
}
