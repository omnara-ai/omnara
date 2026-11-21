package maintenance

import (
	"context"
	"log/slog"

	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	modelCostMergeJob   = "merge_daily_model_costs"
	modelCostMergeBatch = 1000
)

func mergeDailyModelCosts(
	log *slog.Logger,
	merge func(context.Context, int32) (int64, error),
	recorder *metrics.JobRecorder,
) func(context.Context) maintenanceResult {
	recorder.InitializeItems(modelCostMergeJob, "merged")
	return func(ctx context.Context) maintenanceResult {
		merged, err := merge(ctx, modelCostMergeBatch)
		if err != nil {
			merged = 0
		}
		recorder.RecordItems(modelCostMergeJob, "merged", merged)
		outcome := completedMaintenanceOutcome(ctx, err)
		if outcome.err != nil {
			log.Error("merge daily model costs", "error", outcome.err)
		}
		return maintenanceResult{worked: merged > 0, more: merged == modelCostMergeBatch, err: outcome.err}
	}
}
