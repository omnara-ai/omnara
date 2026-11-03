package tools

import (
	"context"
	"time"
)

const (
	integrationMessageSendAttempts          = 3
	integrationMessageSendMaxRateLimitSleep = 15 * time.Second
)

func sleepForIntegrationRateLimit(
	ctx context.Context,
	retryAfter time.Duration,
	rateLimitSlept *time.Duration,
	attempt int,
) (bool, error) {
	if retryAfter < 0 || *rateLimitSlept+retryAfter > integrationMessageSendMaxRateLimitSleep ||
		attempt >= integrationMessageSendAttempts {
		return false, nil
	}
	timer := time.NewTimer(retryAfter)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false, ctx.Err()
	case <-timer.C:
	}
	*rateLimitSlept += retryAfter
	return true, nil
}
