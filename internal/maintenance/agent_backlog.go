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
	agentBacklogInterval      = 15 * time.Second
	agentBacklogSampleTimeout = 5 * time.Second
)

func recordAgentBacklog(
	log *slog.Logger,
	sample func(context.Context) (executionstore.ReadyAgentBacklog, error),
	recorder *metrics.AgentBacklogRecorder,
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		backlog, err := sampleAgentBacklog(ctx, log, sample)
		recorder.Record(metrics.AgentBacklogSample{Ready: backlog.Ready, OldestReadyAge: backlog.OldestReadyAge}, err)
		if err != nil && ctx.Err() == nil {
			log.Warn("sample ready agent backlog", "error", err)
			return maintenanceResult{err: err}
		}
		return maintenanceResult{}
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
