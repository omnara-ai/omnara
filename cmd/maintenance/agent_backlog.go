package main

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
	agentBacklogInterval      = 15 * time.Second
	agentBacklogSampleTimeout = 5 * time.Second
)

func runAgentBacklogMetricsLoop(
	ctx context.Context, log *slog.Logger,
	sample func(context.Context) (executionstore.ReadyAgentBacklog, error),
	recorder *metrics.AgentBacklogRecorder,
) {
	for ctx.Err() == nil {
		backlog, err := sampleAgentBacklog(ctx, log, sample)
		recorder.Record(metrics.AgentBacklogSample{Ready: backlog.Ready, OldestReadyAge: backlog.OldestReadyAge}, err)
		if err != nil && ctx.Err() == nil {
			log.Warn("sample ready agent backlog", "error", err)
		}
		timer := time.NewTimer(jitteredMaintenanceDelay(agentBacklogInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func sampleAgentBacklog(
	ctx context.Context,
	log *slog.Logger,
	sample func(context.Context) (executionstore.ReadyAgentBacklog, error),
) (backlog executionstore.ReadyAgentBacklog, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("agent backlog sample panicked: %v", recovered)
			log.Error("agent backlog sample panicked", "error", recovered, "stack", string(debug.Stack()))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, agentBacklogSampleTimeout)
	defer cancel()
	return sample(ctx)
}
