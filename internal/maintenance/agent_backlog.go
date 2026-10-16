package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	agentBacklogInterval      = 15 * time.Second
	agentBacklogSampleTimeout = 5 * time.Second
)

// RunAgentBacklogMetrics samples the global ready agent backlog until ctx ends.
func RunAgentBacklogMetrics(
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
		spread := agentBacklogInterval / 10
		timer := time.NewTimer(agentBacklogInterval - spread + time.Duration(rand.Int64N(int64(2*spread)+1)))
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
