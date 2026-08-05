package integrationredis

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func validateTestRedisURL(redisURL string) error {
	parsed, err := url.Parse(redisURL)
	if err != nil {
		return fmt.Errorf("parse local test redis URL: %w", err)
	}
	host := parsed.Hostname()
	localHost := host == "127.0.0.1" || host == "localhost"
	if parsed.Scheme != "redis" || !localHost {
		return fmt.Errorf("refusing non-local test redis URL: %s", redactRedisURL(redisURL))
	}
	return nil
}

func assertTestRedisURL(t testing.TB, redisURL string) {
	t.Helper()
	if err := validateTestRedisURL(redisURL); err != nil {
		t.Fatal(err)
	}
}

func redactRedisURL(redisURL string) string {
	if at := strings.LastIndex(redisURL, "@"); at >= 0 {
		return "redis://<redacted>" + redisURL[at:]
	}
	return redisURL
}
