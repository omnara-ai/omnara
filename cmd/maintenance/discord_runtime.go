package main

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/observability/metrics"
)

const discordRuntimeMetricsInterval = 30 * time.Second

func runDiscordRuntimeMetricsLoop(
	ctx context.Context, log *slog.Logger, store *integrationstore.Store,
	recorder *metrics.DiscordRuntimeDemandRecorder,
) {
	for ctx.Err() == nil {
		count, err := sampleDiscordRuntimeDemand(ctx, log, store.CountUnclaimedDiscordIntegrations)
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

func sampleDiscordRuntimeDemand(
	ctx context.Context,
	log *slog.Logger,
	sample func(context.Context) (int64, error),
) (count int64, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("discord runtime demand sample panicked: %v", recovered)
			log.Error("Discord runtime demand sample panicked", "error", recovered, "stack", string(debug.Stack()))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return sample(ctx)
}
