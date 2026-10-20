package cleanupcontext

import (
	"context"
	"time"
)

type lifetimeKey struct{}

func WithLifetime(ctx, lifetime context.Context) context.Context {
	return context.WithValue(ctx, lifetimeKey{}, lifetime)
}

func Detach(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	lifetime, ok := ctx.Value(lifetimeKey{}).(context.Context)
	if !ok {
		return cleanupCtx, cancel
	}
	if lifetime.Err() != nil {
		cancel()
	}
	stopWatchingLifetime := context.AfterFunc(lifetime, cancel)
	return cleanupCtx, func() {
		stopWatchingLifetime()
		cancel()
	}
}
