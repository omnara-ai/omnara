package tools

import (
	"context"
	"time"
)

const (
	appMessageSendAttempts          = 3
	appMessageSendMaxRateLimitSleep = 15 * time.Second
)

func sleepForAppRateLimit(
	ctx context.Context,
	retryAfter time.Duration,
	rateLimitSlept *time.Duration,
	attempt int,
) (bool, error) {
	if retryAfter < 0 || *rateLimitSlept+retryAfter > appMessageSendMaxRateLimitSleep ||
		attempt >= appMessageSendAttempts {
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
