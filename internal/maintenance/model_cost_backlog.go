package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	modelCostBacklogInterval      = 15 * time.Second
	modelCostBacklogSampleTimeout = 5 * time.Second
	modelCostBacklogCeiling       = 100_000
)

func recordModelCostBacklog(
	log *slog.Logger,
	count func(context.Context, int32) (int64, error),
	recorder *metrics.ModelCostBacklogRecorder,
) func(context.Context) maintenanceResult {
	return func(ctx context.Context) maintenanceResult {
		pending, err := sampleModelCostBacklog(ctx, log, count)
		recorder.Record(pending, err)
		if err != nil && ctx.Err() == nil {
			log.Warn("sample model cost merge backlog", "error", err)
			return maintenanceResult{err: err}
		}
		return maintenanceResult{}
	}
}

func sampleModelCostBacklog(
	ctx context.Context,
	log *slog.Logger,
	count func(context.Context, int32) (int64, error),
) (pending int64, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("model cost merge backlog sample panicked: %v", recovered)
			log.Error("model cost merge backlog sample panicked", "error", recovered, "stack", string(debug.Stack()))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, modelCostBacklogSampleTimeout)
	defer cancel()
	return count(ctx, modelCostBacklogCeiling)
}
