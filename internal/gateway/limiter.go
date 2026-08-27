package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter is intentionally storage-neutral. Production deployments use a
// shared Redis adapter; the bounded memory implementation is for local
// development and deterministic tests.
type Limiter interface {
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

type UnlimitedLimiter struct{}

func (UnlimitedLimiter) Allow(context.Context, string) (bool, time.Duration, error) {
	return true, 0, nil
}

type limitWindow struct {
	startedAt time.Time
	count     int
}

type MemoryLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	maxEntries int
	now        func() time.Time
	entries    map[string]limitWindow
}

func NewMemoryLimiter(limit int, window time.Duration, maxEntries int) (*MemoryLimiter, error) {
	if limit < 1 || window <= 0 || maxEntries < 1 {
		return nil, ErrInvalidConfiguration
	}
	return &MemoryLimiter{
		limit:      limit,
		window:     window,
		maxEntries: maxEntries,
		now:        time.Now,
		entries:    make(map[string]limitWindow),
	}, nil
}

func (limiter *MemoryLimiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	now := limiter.now()
	entry, exists := limiter.entries[key]
	if !exists || now.Sub(entry.startedAt) >= limiter.window {
		if !exists && len(limiter.entries) >= limiter.maxEntries {
			limiter.evictOldest()
		}
		limiter.entries[key] = limitWindow{startedAt: now, count: 1}
		return true, 0, nil
	}
	if entry.count >= limiter.limit {
		retryAfter := limiter.window - now.Sub(entry.startedAt)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return false, retryAfter, nil
	}
	entry.count++
	limiter.entries[key] = entry
	return true, 0, nil
}

type redisEvaluator interface {
	Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd
}

// RedisLimiter is the horizontally-scalable production adapter. Its single Lua
// evaluation makes increment, first-use expiry and remaining-time calculation
// atomic on one Redis key.
type RedisLimiter struct {
	client redisEvaluator
	prefix string
	limit  int64
	window time.Duration
}

const fixedWindowScript = `
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
return {current, ttl}
`

func NewRedisLimiter(client redisEvaluator, prefix string, limit int64, window time.Duration) (*RedisLimiter, error) {
	if client == nil || !validIdentifier(prefix, 64) || limit < 1 || window < time.Second || window > 24*time.Hour {
		return nil, ErrInvalidConfiguration
	}
	return &RedisLimiter{client: client, prefix: prefix, limit: limit, window: window}, nil
}

func (limiter *RedisLimiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	result, err := limiter.client.Eval(
		ctx,
		fixedWindowScript,
		[]string{limiter.prefix + ":" + key},
		limiter.window.Milliseconds(),
	).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("evaluate Redis rate limit: %w", err)
	}
	if len(result) != 2 {
		return false, 0, fmt.Errorf("decode Redis rate result")
	}
	count, ok := redisInteger(result[0])
	if !ok {
		return false, 0, fmt.Errorf("decode Redis rate count")
	}
	ttlMilliseconds, ok := redisInteger(result[1])
	if !ok || ttlMilliseconds < 0 {
		return false, 0, fmt.Errorf("decode Redis rate TTL")
	}
	if count <= limiter.limit {
		return true, 0, nil
	}
	retryAfter := time.Duration(ttlMilliseconds) * time.Millisecond
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return false, retryAfter, nil
}

func redisInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

func (limiter *MemoryLimiter) evictOldest() {
	var oldestKey string
	var oldestTime time.Time
	for key, entry := range limiter.entries {
		if oldestKey == "" || entry.startedAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.startedAt
		}
	}
	delete(limiter.entries, oldestKey)
}
