package logent

import (
	"context"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/log"
)

func MaintenanceLoop(
	ctx context.Context,
	interval time.Duration,
	now time.Time,
) (context.Context, *log.Event) {
	event := log.NewEvent(ctx, "maintenance.loop", log.Fields{
		"maintenance.loop.interval": interval,
		"maintenance.loop.poll_at":  now,
	})
	return log.WithEvent(ctx, event), event
}

func MaintenanceLoopResult(
	ctx context.Context,
	reapedRuntimeLocks int64,
	reapRuntimeLocksErr error,
	rebuiltAgentWakeups int64,
	rebuildErr error,
) {
	fields := log.Fields{
		"maintenance.reap_runtime_locks.count":    reapedRuntimeLocks,
		"maintenance.rebuild_agent_wakeups.count": rebuiltAgentWakeups,
		"maintenance.loop.worked": reapedRuntimeLocks > 0 ||
			rebuiltAgentWakeups > 0,
	}
	if reapRuntimeLocksErr != nil {
		if !IsCanceledDuringShutdown(ctx, reapRuntimeLocksErr) {
			fields["maintenance.reap_runtime_locks.error"] = reapRuntimeLocksErr.Error()
			log.Error(ctx, reapRuntimeLocksErr)
		}
	}
	if rebuildErr != nil {
		if !IsCanceledDuringShutdown(ctx, rebuildErr) {
			fields["maintenance.rebuild_agent_wakeups.error"] = rebuildErr.Error()
			log.Error(ctx, rebuildErr)
		}
	}
	log.Attach(ctx, fields)
}

func IsCanceledDuringShutdown(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}
