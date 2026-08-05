package integrationredis

import (
	"os"
	"testing"

	"github.com/omnara-ai/omnara/internal/redistore"
)

const redisURLEnv = "OMNARA_TEST_REDIS_URL"

func URL(t testing.TB) string {
	t.Helper()

	rawURL := os.Getenv(redisURLEnv)
	if rawURL == "" {
		t.Skip(redisURLEnv + " is not set")
	}
	assertTestRedisURL(t, rawURL)
	return rawURL
}

func OpenClient(t testing.TB) *redistore.Client {
	t.Helper()

	rawURL := URL(t)
	client, err := redistore.Connect(rawURL)
	if err != nil {
		t.Fatalf("connect integration redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
