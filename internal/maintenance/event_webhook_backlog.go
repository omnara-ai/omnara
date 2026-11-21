package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	eventWebhookBacklogInterval      = 15 * time.Second
	eventWebhookBacklogSampleTimeout = 5 * time.Second
)

func recordEventWebhookBacklog(
	log *slog.Logger,
	sample func(context.Context) (executionstore.EventWebhookBacklog, error),
	recorder *metrics.EventWebhookBacklogRecorder,
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		backlog, err := sampleEventWebhookBacklog(ctx, log, sample)
		recorder.Record(metrics.EventWebhookBacklogSample{
			Due: backlog.Due, Delayed: backlog.Delayed, InFlight: backlog.InFlight, Expired: backlog.Expired,
			OldestDueAge: backlog.OldestDueAge, OldestDueLag: backlog.OldestDueLag,
		}, err)
		if err != nil && ctx.Err() == nil {
			log.Warn("sample event webhook backlog", "error", err)
			return maintenanceResult{err: err}
		}
		return maintenanceResult{}
	}
}

func sampleEventWebhookBacklog(
	ctx context.Context,
	log *slog.Logger,
	sample func(context.Context) (executionstore.EventWebhookBacklog, error),
) (backlog executionstore.EventWebhookBacklog, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("event webhook backlog sample panicked: %v", recovered)
			log.Error("event webhook backlog sample panicked", "error", recovered, "stack", string(debug.Stack()))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, eventWebhookBacklogSampleTimeout)
	defer cancel()
	return sample(ctx)
}
