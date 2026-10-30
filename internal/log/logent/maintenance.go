package logent

import (
	"context"
	"time"

	log "github.com/omnara-ai/omnara/observability/wideevent"
)

func MaintenanceJob(ctx context.Context, name string, interval time.Duration) (context.Context, *log.Event) {
	return log.Start(ctx, "maintenance.job", log.Fields{
		"maintenance.job.name":     name,
		"maintenance.job.interval": interval,
	})
}

func MaintenanceJobCount(ctx context.Context, count int64) {
	log.Attach(ctx, log.Fields{"maintenance.job.count": count})
}

func MaintenanceJobResult(ctx context.Context, worked, more bool, err error) {
	log.Attach(ctx, log.Fields{
		"maintenance.job.worked": worked,
		"maintenance.job.more":   more,
	})
	if err != nil {
		log.Error(ctx, err)
	} else if !worked {
		log.Level(ctx, log.DebugLevel)
	}
}
