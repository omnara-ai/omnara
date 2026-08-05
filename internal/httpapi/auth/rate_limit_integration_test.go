//go:build integration

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
)

const redisNowMillisScript = `
local redis_time = redis.call("TIME")
return (redis_time[1] * 1000) + math.floor(redis_time[2] / 1000)
`

func TestRedisRateLimiterUsesRollingBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	redisClient := integrationredis.OpenClient(t)
	limiter := NewRedisRateLimiter(redisClient)
	key := "auth:test:rolling:" + identitystore.HashBearerToken(t.Name()+time.Now().UTC().Format(time.RFC3339Nano))
	const limit = 3
	window := 3 * time.Second

	for i := 0; i < limit; i++ {
		if err := limiter.Allow(ctx, key, limit, window); err != nil {
			t.Fatalf("burst request %d error = %v", i, err)
		}
	}
	if err := limiter.Allow(ctx, key, limit, window); !errors.Is(err, errRateLimited) {
		t.Fatalf("post-burst request error = %v, want rate limited", err)
	}

	boundaryKey := key + ":boundary"
	nowMillis, err := redisClient.EvalInt(ctx, redisNowMillisScript, nil)
	if err != nil {
		t.Fatalf("load redis time: %v", err)
	}
	interval := window / limit
	burst := window - interval
	if err := redisClient.Set(ctx, boundaryKey, nowMillis+int(burst/time.Millisecond), 10*time.Second); err != nil {
		t.Fatalf("seed rolling boundary state: %v", err)
	}
	if err := limiter.Allow(ctx, boundaryKey, limit, window); err != nil {
		t.Fatalf("first boundary request error = %v", err)
	}
	if err := limiter.Allow(ctx, boundaryKey, limit, window); !errors.Is(err, errRateLimited) {
		t.Fatalf("second boundary request error = %v, want rate limited", err)
	}
}
