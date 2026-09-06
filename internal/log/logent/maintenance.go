package logent

import (
	"context"
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
	worked bool,
	err error,
) {
	fields := log.Fields{
		"maintenance.reap_runtime_locks.count": reapedRuntimeLocks,
		"maintenance.loop.worked":              worked,
	}
	if reapRuntimeLocksErr != nil {
		fields["maintenance.reap_runtime_locks.error"] = reapRuntimeLocksErr.Error()
	}
	if err != nil {
		log.Error(ctx, err)
	} else if !worked {
		log.Level(ctx, log.DebugLevel)
	}
	log.Attach(ctx, fields)
}
