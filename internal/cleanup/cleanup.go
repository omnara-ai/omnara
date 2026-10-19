package cleanup

import (
	"context"
	"time"
)

type lifetimeContextKey struct{}

func WithLifetime(ctx, lifetime context.Context) context.Context {
	return context.WithValue(ctx, lifetimeContextKey{}, lifetime)
}

func Detach(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	lifetime, ok := ctx.Value(lifetimeContextKey{}).(context.Context)
	if !ok {
		return detached, cancel
	}
	stopWatchingLifetime := context.AfterFunc(lifetime, cancel)
	return detached, func() {
		stopWatchingLifetime()
		cancel()
	}
}
