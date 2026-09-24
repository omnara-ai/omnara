package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

const discordRuntimeMetricsInterval = 30 * time.Second

func runDiscordRuntimeMetricsLoop(
	ctx context.Context, log *slog.Logger, store *integrationstore.Store,
	recorder *metrics.DiscordRuntimeDemandRecorder,
) {
	for ctx.Err() == nil {
		sampleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		count, err := store.CountUnclaimedDiscordIntegrations(sampleCtx)
		cancel()
		recorder.RecordUnclaimed(count, err)
		if err != nil && ctx.Err() == nil {
			log.Warn("sample unclaimed Discord integrations", "error", err)
		}
		timer := time.NewTimer(jitteredMaintenanceDelay(discordRuntimeMetricsInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
