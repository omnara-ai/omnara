package cleanupcontext

import (
	"context"
	"time"
)

type drainKey struct{}

func WithDrain(ctx, drain context.Context) context.Context {
	return context.WithValue(ctx, drainKey{}, drain)
}

func Detach(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	drain, ok := ctx.Value(drainKey{}).(context.Context)
	deadline := time.Now().Add(timeout)
	if ok {
		if cutoff, bounded := drain.Deadline(); bounded && cutoff.Before(deadline) {
			deadline = cutoff
		}
	}
	cleanupCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	if !ok {
		return cleanupCtx, cancel
	}
	if drain.Err() != nil {
		cancel()
	}
	stopWatchingDrain := context.AfterFunc(drain, cancel)
	return cleanupCtx, func() {
		stopWatchingDrain()
		cancel()
	}
}
